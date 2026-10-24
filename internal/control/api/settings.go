package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zaneGittins/skybreaker/internal/control/config"
	policy "github.com/zaneGittins/skybreaker/internal/proxy/config"
	"github.com/zaneGittins/skybreaker/internal/proxy/content"
	"github.com/zaneGittins/skybreaker/internal/proxy/githubapp"
	"github.com/zaneGittins/skybreaker/internal/proxy/secrets"
	"github.com/zaneGittins/skybreaker/internal/tsauth"
	"github.com/zaneGittins/skybreaker/internal/webutil"
)

// Settings saves what admins change in the app: secrets, the GitHub App,
// bundles and project access (see config.Edit). The config file stays the
// base; the app's files only add to it.
type Settings interface {
	// Apply checks change against the whole configuration and, unless dry,
	// saves it, records "by: what" in the audit log and restarts
	// Skybreaker to apply it once no agent is working.
	Apply(ctx context.Context, by, what string, change func(*config.AppFiles) error, dry bool) (Applied, error)
	// Saved is the configuration as saved, with the app's files: ahead of
	// the running one while a restart is pending.
	Saved() (*config.Config, config.AppFiles, error)
	// Pending reports whether a saved change waits for a restart.
	Pending() bool
	// FileSecrets lists the secrets file's secrets, never their values.
	FileSecrets() ([]FileSecret, error)
	// SecretsUsed is when each secret was last sent, since start.
	SecretsUsed() map[string]time.Time
	// GitHubInstallURL is where to install the config file's App.
	GitHubInstallURL(ctx context.Context) (string, error)
	// GitHubLacking is what the running App lacks of the permissions
	// agents use.
	GitHubLacking(ctx context.Context) (githubapp.Lacking, error)
	// ConvertGitHubApp trades the code GitHub sends back for the App it
	// created from a manifest.
	ConvertGitHubApp(ctx context.Context, code string) (githubapp.Created, error)
	// Instructions are what every agent is told, as saved.
	Instructions() (string, error)
	// SetInstructions saves them, records by in the audit log and gives
	// them to every sandbox at once; no restart is needed.
	SetInstructions(by, text string) error
	// Prompts are the canned prompts anyone can send with /prompt, as saved.
	Prompts() ([]config.Prompt, error)
	// SetPrompts replaces them and records by in the audit log; chats
	// pick them up at once.
	SetPrompts(by string, prompts []config.Prompt) error
	// ChatModes are the permission modes new chats start in, as saved.
	ChatModes() (config.ChatModes, error)
	// ChangeChatModes applies change to them, saves them and records
	// "by: what" in the audit log; new chats start in them at once.
	ChangeChatModes(by, what string, change func(*config.ChatModes) error) (config.ChatModes, error)
}

// Applied is what a change did.
type Applied struct {
	Before, After *config.Config
	// Restart is when Skybreaker restarts to apply it: "now", "when_idle"
	// once no agent is working, or "" for a dry run.
	Restart string
}

// FileSecret is a secret from the secrets file, without its value.
type FileSecret struct {
	Name    string
	Kind    string
	Hosts   []string
	FromApp bool   // a github_app left to the app to create
	AppID   string // a github_app's
}

const ghSecret = "github_app"

var (
	secretNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,47}$`)
	secretHostRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)
	errNoSecret  = errors.New("no such secret added in the app")
	errNoBundle  = errors.New("no such bundle made in the app")
)

func (s *server) settingsRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/settings", s.settings)
	m.HandleFunc("POST /api/secrets", s.addSecret)
	m.HandleFunc("PUT /api/secrets/{name}", s.replaceSecret)
	m.HandleFunc("DELETE /api/secrets/{name}", s.deleteSecret)
	m.HandleFunc("PUT /api/bundles/{name}", s.saveBundle)
	m.HandleFunc("DELETE /api/bundles/{name}", s.deleteBundle)
	m.HandleFunc("PUT /api/projects/{p}/access", s.saveAccess)
	m.HandleFunc("POST /api/github-app/manifest", s.githubManifest)
	m.HandleFunc("GET "+githubapp.CreatedPath, s.githubCreated)
	m.HandleFunc("GET /api/instructions", s.instructions)
	m.HandleFunc("PUT /api/instructions", s.saveInstructions)
	m.HandleFunc("GET /api/prompts", s.prompts)
	m.HandleFunc("PUT /api/prompts", s.savePrompts)
	m.HandleFunc("GET /api/chat-modes", s.chatModes)
	m.HandleFunc("PUT /api/chat-modes/default", s.saveChatMode)
	m.HandleFunc("PUT /api/chat-modes/projects/{p}", s.saveChatMode)
	m.HandleFunc("PUT /api/content-rules", s.saveContentRules)
}

// admin admits a listed admin where the config file lets the app change
// settings, and names them.
func (s *server) admin(w http.ResponseWriter, r *http.Request) (string, bool) {
	p, _ := tsauth.FromContext(r.Context())
	switch {
	case !s.Config.Admin(p.Login):
		webutil.Error(w, http.StatusForbidden, "only admins can change settings")
	case !s.Config.SettingsInApp():
		webutil.Error(w, http.StatusForbidden, config.ErrSettingsOff.Error())
	default:
		return p.Login, true
	}
	return "", false
}

// editor is admin, where settings can be saved at all.
func (s *server) editor(w http.ResponseWriter, r *http.Request) (string, bool) {
	by, ok := s.admin(w, r)
	if ok && s.Settings == nil {
		webutil.Error(w, http.StatusNotFound, "settings can't be changed here")
		return "", false
	}
	return by, ok
}

// decodeStrict reads a settings request strictly: an unknown field is refused,
// never ignored.
func decodeStrict(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		webutil.Error(w, http.StatusBadRequest, "bad request: "+err.Error())
		return false
	}
	return true
}

// --- the page ---------------------------------------------------------------

type githubView struct {
	State      string `json:"state"` // none, placeholder, file or app
	CanCreate  bool   `json:"can_create"`
	AppID      string `json:"app_id,omitempty"`
	Slug       string `json:"slug,omitempty"`
	Owner      string `json:"owner,omitempty"`
	HTMLURL    string `json:"html_url,omitempty"`
	InstallURL string `json:"install_url,omitempty"`
	Error      string `json:"error,omitempty"`
	// Lacking is what the App can't yet do that agents need, such as read
	// CI; nil when it lacks nothing, or couldn't be asked.
	Lacking *githubapp.Lacking `json:"lacking,omitempty"`
}

type secretView struct {
	Name        string     `json:"name"`
	Kind        string     `json:"kind"`
	Hosts       []string   `json:"hosts"`
	Source      string     `json:"source"` // file or app
	Fingerprint string     `json:"fingerprint,omitempty"`
	AddedBy     string     `json:"added_by,omitempty"`
	Added       *time.Time `json:"added,omitempty"`
	LastUsed    *time.Time `json:"last_used,omitempty"`
	UsedBy      []string   `json:"used_by"`
}

type bundleView struct {
	Name    string          `json:"name"`
	Source  string          `json:"source"`
	Entries []*policy.Entry `json:"entries"`
	UsedBy  []string        `json:"used_by"`
}

// contentView is the content rules from each source; the broker applies
// both, the stricter limit and every pattern.
type contentView struct {
	File *content.Rules `json:"file"`
	App  *content.Rules `json:"app"`
}

type accessView struct {
	Name    string         `json:"name"`
	Added   bool           `json:"added"` // made in the app
	File    config.Grant   `json:"file"`
	App     config.Grant   `json:"app"`
	Reaches []policy.Reach `json:"reaches"`
}

func (s *server) settings(w http.ResponseWriter, r *http.Request) {
	if s.Settings == nil {
		webutil.Error(w, http.StatusNotFound, "settings can't be changed here")
		return
	}
	c, files, err := s.Settings.Saved()
	if err != nil {
		webutil.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	fileSecrets, err := s.Settings.FileSecrets()
	if err != nil {
		webutil.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	p, _ := tsauth.FromContext(r.Context())
	admin := s.Config.Admin(p.Login)
	out := struct {
		Admin    bool         `json:"admin"`
		Editable bool         `json:"editable"`
		Pending  bool         `json:"pending"`
		GitHub   githubView   `json:"github"`
		Secrets  []secretView `json:"secrets"`
		Bundles  []bundleView `json:"bundles"`
		Projects []accessView `json:"projects"`
		Content  contentView  `json:"content_rules"`
	}{Admin: admin, Editable: admin && s.Config.SettingsInApp() && c.SettingsInApp(), Pending: s.Settings.Pending(),
		Secrets: []secretView{}, Bundles: []bundleView{}, Projects: []accessView{},
		Content: contentView{File: c.ContentRules, App: files.Access.ContentRules}}
	// What the App lacks is asked alongside, so a slow GitHub costs the
	// page one wait, not two.
	lacking := make(chan *githubapp.Lacking, 1)
	go func() {
		gctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		if l, err := s.Settings.GitHubLacking(gctx); err == nil && !l.None() {
			lacking <- &l
			return
		}
		lacking <- nil
	}()
	out.GitHub = s.creatable(s.githubView(r.Context(), fileSecrets, files))
	if l := <-lacking; out.GitHub.State == "app" || out.GitHub.State == "file" {
		out.GitHub.Lacking = l
	}

	used, users := s.Settings.SecretsUsed(), secretUsers(c)
	when := func(name string) *time.Time {
		if t, ok := used[name]; ok {
			return &t
		}
		return nil
	}
	slices.SortFunc(fileSecrets, func(a, b FileSecret) int { return strings.Compare(a.Name, b.Name) })
	for _, f := range fileSecrets {
		out.Secrets = append(out.Secrets, secretView{Name: f.Name, Kind: f.Kind, Hosts: nonNil(f.Hosts), Source: "file", LastUsed: when(f.Name), UsedBy: nonNil(users[f.Name])})
	}
	app := slices.Clone(files.Secrets.Secrets)
	slices.SortFunc(app, func(a, b secrets.Stored) int { return strings.Compare(a.Name, b.Name) })
	for _, st := range app {
		if st.Kind != "static" {
			continue // the GitHub App has its own section
		}
		added := st.Added
		out.Secrets = append(out.Secrets, secretView{Name: st.Name, Kind: st.Kind, Hosts: nonNil(st.Hosts), Source: "app",
			Fingerprint: files.Secrets.Fingerprint(st.Value), AddedBy: st.AddedBy, Added: &added, LastUsed: when(st.Name), UsedBy: nonNil(users[st.Name])})
	}

	usedBy := map[string][]string{}
	for _, name := range slices.Sorted(maps.Keys(c.Projects)) {
		for _, cp := range c.Projects[name].Capabilities {
			usedBy[cp] = append(usedBy[cp], name)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(c.Capabilities)) {
		src := "file"
		if c.AppBundle(name) {
			src = "app"
		}
		out.Bundles = append(out.Bundles, bundleView{Name: name, Source: src, Entries: nonNil(c.Capabilities[name]), UsedBy: nonNil(usedBy[name])})
	}
	for _, name := range slices.Sorted(maps.Keys(c.Projects)) {
		v := accessView{Name: name, Added: c.Added(name), File: c.FromFile(name), App: files.Grant(name), Reaches: []policy.Reach{}}
		if pp, ok := c.Policy.Project(name); ok {
			v.Reaches = nonNil(pp.Reaches())
		}
		out.Projects = append(out.Projects, v)
	}
	webutil.WriteJSON(w, http.StatusOK, out)
}

// instructions are every agent's, for anyone to read.
func (s *server) instructions(w http.ResponseWriter, r *http.Request) {
	if s.Settings == nil {
		webutil.Error(w, http.StatusNotFound, "settings can't be changed here")
		return
	}
	text, err := s.Settings.Instructions()
	if err != nil {
		webutil.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	p, _ := tsauth.FromContext(r.Context())
	webutil.WriteJSON(w, http.StatusOK, map[string]any{"text": text, "editable": s.Config.Admin(p.Login) && s.Config.SettingsInApp(), "max": config.MaxInstructions})
}

func (s *server) saveInstructions(w http.ResponseWriter, r *http.Request) {
	by, ok := s.editor(w, r)
	if !ok {
		return
	}
	var in struct {
		Text string `json:"text"`
	}
	if !decodeStrict(w, r, &in) {
		return
	}
	text := config.CleanInstructions(in.Text)
	if err := config.CheckInstructions(text); err != nil {
		webutil.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.Settings.SetInstructions(by, text); err != nil {
		webutil.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	webutil.WriteJSON(w, http.StatusOK, map[string]any{"text": text})
}

// prompts are everyone's to read and send.
func (s *server) prompts(w http.ResponseWriter, r *http.Request) {
	if s.Settings == nil {
		webutil.Error(w, http.StatusNotFound, "settings can't be changed here")
		return
	}
	ps, err := s.Settings.Prompts()
	if err != nil {
		webutil.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	p, _ := tsauth.FromContext(r.Context())
	webutil.WriteJSON(w, http.StatusOK, map[string]any{"prompts": ps, "editable": s.Config.Admin(p.Login) && s.Config.SettingsInApp(),
		"max": config.MaxPrompts, "max_text": config.MaxPromptsText})
}

func (s *server) savePrompts(w http.ResponseWriter, r *http.Request) {
	by, ok := s.editor(w, r)
	if !ok {
		return
	}
	var in struct {
		Prompts []config.Prompt `json:"prompts"`
	}
	if !decodeStrict(w, r, &in) {
		return
	}
	ps := config.CleanPrompts(in.Prompts)
	if err := config.CheckPrompts(ps); err != nil {
		webutil.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.Settings.SetPrompts(by, ps); err != nil {
		webutil.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	webutil.WriteJSON(w, http.StatusOK, map[string]any{"prompts": ps})
}

// chatModes are everyone's to read.
func (s *server) chatModes(w http.ResponseWriter, r *http.Request) {
	if s.Settings == nil {
		webutil.Error(w, http.StatusNotFound, "settings can't be changed here")
		return
	}
	m, err := s.Settings.ChatModes()
	if err != nil {
		webutil.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeChatModes(w, r, m)
}

func (s *server) writeChatModes(w http.ResponseWriter, r *http.Request, m config.ChatModes) {
	if m.Projects == nil {
		m.Projects = map[string]string{}
	}
	p, _ := tsauth.FromContext(r.Context())
	webutil.WriteJSON(w, http.StatusOK, map[string]any{"default": m.Default, "projects": m.Projects,
		"editable": s.Config.Admin(p.Login) && s.Config.SettingsInApp()})
}

// saveChatMode sets the default mode, or one project's ("" for the
// default), and drops the modes of projects that are gone.
func (s *server) saveChatMode(w http.ResponseWriter, r *http.Request) {
	by, ok := s.editor(w, r)
	if !ok {
		return
	}
	var in struct {
		Mode string `json:"mode"`
	}
	if !decodeStrict(w, r, &in) {
		return
	}
	if in.Mode != "" && !config.ChatMode(in.Mode) {
		webutil.Error(w, http.StatusBadRequest, "mode "+strconv.Quote(in.Mode)+" is not a chat mode")
		return
	}
	saved, _, err := s.Settings.Saved()
	if err != nil {
		webutil.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	project := r.PathValue("p")
	if _, ok := saved.Projects[project]; project != "" && !ok {
		webutil.Error(w, http.StatusBadRequest, "there's no project called "+project)
		return
	}
	what, to := "set new chats", "start in "+in.Mode
	if project != "" {
		what += " in " + project
	}
	if in.Mode == "" {
		to = "start in the default"
	}
	m, err := s.Settings.ChangeChatModes(by, what+" to "+to, func(m *config.ChatModes) error {
		if project == "" {
			m.Default = in.Mode
		} else if in.Mode == "" {
			delete(m.Projects, project)
		} else {
			if m.Projects == nil {
				m.Projects = map[string]string{}
			}
			m.Projects[project] = in.Mode
		}
		maps.DeleteFunc(m.Projects, func(p, _ string) bool { return saved.Projects[p] == nil })
		return nil
	})
	if err != nil {
		webutil.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeChatModes(w, r, m)
}

// secretUsers names, for each secret, the bundles and projects that send it.
func secretUsers(c *config.Config) map[string][]string {
	out := map[string][]string{}
	add := func(e *policy.Entry, who string) {
		if e == nil {
			return
		}
		if n := e.Inject.SecretName(); n != "" && !slices.Contains(out[n], who) {
			out[n] = append(out[n], who)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(c.Capabilities)) {
		for _, e := range c.Capabilities[name] {
			add(e, "bundle "+name)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(c.Projects)) {
		for _, e := range c.Projects[name].Allow {
			add(e, "project "+name)
		}
	}
	return out
}

func (s *server) githubView(ctx context.Context, file []FileSecret, files config.AppFiles) githubView {
	for _, st := range files.Secrets.Secrets {
		if st.Name == ghSecret && st.Kind == "github_app" && st.GitHubApp != nil {
			v := githubView{State: "app", AppID: st.GitHubApp.AppID}
			if a := st.App; a != nil {
				v.Slug, v.Owner, v.HTMLURL = a.Slug, a.Owner, a.HTMLURL
				v.InstallURL = githubapp.Created{Slug: a.Slug}.InstallURL()
			}
			return v
		}
	}
	for _, f := range file {
		if f.Kind != "github_app" {
			continue
		}
		if f.FromApp && f.Name == ghSecret {
			return githubView{State: "placeholder", CanCreate: true}
		}
		v := githubView{State: "file", AppID: f.AppID}
		gctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		var err error
		if v.InstallURL, err = s.Settings.GitHubInstallURL(gctx); err != nil {
			v.Error = err.Error()
		}
		return v
	}
	taken := slices.ContainsFunc(file, func(f FileSecret) bool { return f.Name == ghSecret })
	return githubView{State: "none", CanCreate: !taken}
}

// creatable is whether the GitHub App can be created from here: GitHub
// sends you back to ui_url, which must be Skybreaker's https address.
func (s *server) creatable(v githubView) githubView {
	if v.CanCreate {
		if _, err := githubapp.Manifest("x", s.Config.UIURL); err != nil {
			v.CanCreate, v.Error = false, "set ui_url in the config file to Skybreaker's https address to create the App from here"
		} else if msg := config.Misnamed(s.Config.UIURL, s.Self); msg != "" {
			v.CanCreate, v.Error = false, msg
		}
	}
	return v
}

// --- writes -------------------------------------------------------------------

type reviewView struct {
	Project string         `json:"project"`
	Added   []policy.Reach `json:"added"`
	Removed []policy.Reach `json:"removed"`
}

// review is what a change does to every project's reach.
func review(before, after *config.Config) []reviewView {
	out := []reviewView{}
	names := append(before.Policy.ProjectNames(), after.Policy.ProjectNames()...)
	slices.Sort(names)
	for _, name := range slices.Compact(names) {
		var a, b []policy.Reach
		if pp, ok := before.Policy.Project(name); ok {
			a = pp.Reaches()
		}
		if pp, ok := after.Policy.Project(name); ok {
			b = pp.Reaches()
		}
		if added, removed := policy.DiffReaches(a, b); len(added)+len(removed) > 0 {
			out = append(out, reviewView{Project: name, Added: nonNil(added), Removed: nonNil(removed)})
		}
	}
	return out
}

// apply saves a change and answers with what it did.
func (s *server) apply(w http.ResponseWriter, r *http.Request, by, what string, change func(*config.AppFiles) error, dry bool) {
	a, err := s.Settings.Apply(r.Context(), by, what, change, dry)
	switch {
	case errors.Is(err, errNoSecret), errors.Is(err, errNoBundle):
		webutil.Error(w, http.StatusNotFound, err.Error())
		return
	case err != nil:
		webutil.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	webutil.WriteJSON(w, http.StatusOK, map[string]any{"restart": a.Restart, "review": review(a.Before, a.After)})
}

// secretInput checks a value and its hosts as typed. A value goes into
// request headers, so nothing that could end one is let in.
func secretInput(value string, hosts []string) (string, []string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 8<<10 {
		return "", nil, errors.New("enter the secret's value")
	}
	if strings.ContainsFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", nil, errors.New("the value has a line break or control character in it")
	}
	if len(hosts) == 0 || len(hosts) > 20 {
		return "", nil, errors.New("list the hosts this secret may be sent to (up to 20)")
	}
	var out []string
	for _, h := range hosts {
		n := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
		if !secretHostRe.MatchString(n) {
			return "", nil, fmt.Errorf("%q: use an exact host name such as api.example.com (no wildcard, scheme, port or path)", h)
		}
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return value, out, nil
}

func (s *server) addSecret(w http.ResponseWriter, r *http.Request) {
	by, ok := s.editor(w, r)
	if !ok {
		return
	}
	var in struct {
		Name  string   `json:"name"`
		Value string   `json:"value"`
		Hosts []string `json:"hosts"`
	}
	if !decodeStrict(w, r, &in) {
		return
	}
	if !secretNameRe.MatchString(in.Name) {
		webutil.Error(w, http.StatusBadRequest, "name: use lowercase letters, digits, dashes and underscores, starting with a letter")
		return
	}
	value, hosts, err := secretInput(in.Value, in.Hosts)
	if err != nil {
		webutil.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	file, err := s.Settings.FileSecrets()
	if err != nil {
		webutil.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	if slices.ContainsFunc(file, func(f FileSecret) bool { return f.Name == in.Name }) {
		webutil.Error(w, http.StatusBadRequest, "a secret named "+in.Name+" is in the secrets file already")
		return
	}
	s.apply(w, r, by, fmt.Sprintf("added secret %s for %s", in.Name, strings.Join(hosts, ", ")), func(f *config.AppFiles) error {
		if slices.ContainsFunc(f.Secrets.Secrets, func(st secrets.Stored) bool { return st.Name == in.Name }) {
			return fmt.Errorf("a secret named %s already exists", in.Name)
		}
		f.Secrets.Secrets = append(f.Secrets.Secrets, secrets.Stored{
			Spec:    secrets.Spec{Name: in.Name, Kind: "static", Value: value, Hosts: hosts},
			AddedBy: by, Added: time.Now().UTC(),
		})
		return nil
	}, false)
}

// replaceSecret takes a new value, and with it new hosts if wanted. Hosts
// never change on their own: sending a secret somewhere new needs the
// secret itself, which a hijacked session doesn't have.
func (s *server) replaceSecret(w http.ResponseWriter, r *http.Request) {
	by, ok := s.editor(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	var in struct {
		Value string   `json:"value"`
		Hosts []string `json:"hosts"`
	}
	if !decodeStrict(w, r, &in) {
		return
	}
	value, hosts, err := secretInput(in.Value, in.Hosts)
	if err != nil {
		webutil.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	s.apply(w, r, by, fmt.Sprintf("replaced secret %s, for %s", name, strings.Join(hosts, ", ")), func(f *config.AppFiles) error {
		i := slices.IndexFunc(f.Secrets.Secrets, func(st secrets.Stored) bool { return st.Name == name && st.Kind == "static" })
		if i < 0 {
			return errNoSecret
		}
		st := &f.Secrets.Secrets[i]
		st.Value, st.Hosts, st.AddedBy, st.Added = value, hosts, by, time.Now().UTC()
		return nil
	}, false)
}

func (s *server) deleteSecret(w http.ResponseWriter, r *http.Request) {
	by, ok := s.editor(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	s.apply(w, r, by, "deleted secret "+name, func(f *config.AppFiles) error {
		i := slices.IndexFunc(f.Secrets.Secrets, func(st secrets.Stored) bool { return st.Name == name && st.Kind == "static" })
		if i < 0 {
			return errNoSecret
		}
		f.Secrets.Secrets = slices.Delete(f.Secrets.Secrets, i, i+1)
		return nil
	}, false)
}

func (s *server) saveBundle(w http.ResponseWriter, r *http.Request) {
	by, ok := s.editor(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	var in struct {
		Entries []*policy.Entry `json:"entries"`
		DryRun  bool            `json:"dry_run"`
	}
	if !decodeStrict(w, r, &in) {
		return
	}
	if !config.BundleName(name) {
		webutil.Error(w, http.StatusBadRequest, "bundle name: use lowercase letters, digits, dashes and underscores")
		return
	}
	if len(in.Entries) == 0 {
		webutil.Error(w, http.StatusBadRequest, "a bundle needs at least one host")
		return
	}
	s.apply(w, r, by, "saved bundle "+name+": "+hostList(in.Entries), func(f *config.AppFiles) error {
		if f.Access.Capabilities == nil {
			f.Access.Capabilities = map[string][]*policy.Entry{}
		}
		f.Access.Capabilities[name] = in.Entries
		return nil
	}, in.DryRun)
}

// saveContentRules replaces the app's content rules; empty clears them.
// They are checked as the broker compiles them before anything is saved.
func (s *server) saveContentRules(w http.ResponseWriter, r *http.Request) {
	by, ok := s.editor(w, r)
	if !ok {
		return
	}
	var in content.Rules
	if !decodeStrict(w, r, &in) {
		return
	}
	rules := &in
	if set, err := content.Compile(rules); err != nil {
		webutil.Error(w, http.StatusBadRequest, err.Error())
		return
	} else if set == nil {
		rules = nil
	}
	what := "cleared the commit and pull request rules"
	if rules != nil {
		b, _ := json.Marshal(rules)
		what = "set the commit and pull request rules: " + string(b)
	}
	s.apply(w, r, by, what, func(f *config.AppFiles) error {
		f.Access.ContentRules = rules
		return nil
	}, false)
}

func (s *server) deleteBundle(w http.ResponseWriter, r *http.Request) {
	by, ok := s.editor(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	s.apply(w, r, by, "deleted bundle "+name, func(f *config.AppFiles) error {
		if _, ok := f.Access.Capabilities[name]; !ok {
			return errNoBundle
		}
		delete(f.Access.Capabilities, name)
		return nil
	}, false)
}

func (s *server) saveAccess(w http.ResponseWriter, r *http.Request) {
	by, ok := s.editor(w, r)
	if !ok {
		return
	}
	name := r.PathValue("p")
	var in struct {
		Capabilities []string        `json:"capabilities"`
		Allow        []*policy.Entry `json:"allow"`
		DryRun       bool            `json:"dry_run"`
	}
	if !decodeStrict(w, r, &in) {
		return
	}
	c, _, err := s.Settings.Saved()
	if err != nil {
		webutil.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, ok := c.Projects[name]; !ok {
		webutil.Error(w, http.StatusNotFound, "no such project")
		return
	}
	what := fmt.Sprintf("set %s's access in the app: bundles [%s], hosts [%s]", name, strings.Join(in.Capabilities, ", "), hostList(in.Allow))
	s.apply(w, r, by, what, func(f *config.AppFiles) error {
		f.SetGrant(name, config.Grant{Capabilities: in.Capabilities, Allow: in.Allow})
		return nil
	}, in.DryRun)
}

func hostList(es []*policy.Entry) string {
	var hs []string
	for _, e := range es {
		if e != nil {
			hs = append(hs, e.Host)
		}
	}
	return strings.Join(hs, ", ")
}

// --- the GitHub App, from a manifest ------------------------------------------

// ghStates are the manifest flow's state values: each good once, for the
// user it was issued to, for an hour. GitHub sends it back with the code;
// without a match nothing is saved.
type ghStates struct {
	mu  sync.Mutex
	m   map[string]ghState
	now func() time.Time
}

type ghState struct {
	by      string
	expires time.Time
}

func (g *ghStates) clock() time.Time {
	if g.now != nil {
		return g.now()
	}
	return time.Now()
}

func (g *ghStates) issue(by string) string {
	b := make([]byte, 24)
	rand.Read(b)
	st := hex.EncodeToString(b)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.m == nil {
		g.m = map[string]ghState{}
	}
	now := g.clock()
	for k, v := range g.m {
		if now.After(v.expires) {
			delete(g.m, k)
		}
	}
	g.m[st] = ghState{by: by, expires: now.Add(time.Hour)}
	return st
}

func (g *ghStates) take(st, by string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	v, ok := g.m[st]
	delete(g.m, st)
	return ok && v.by == by && !g.clock().After(v.expires)
}

// githubManifest starts creating the GitHub App: the page posts the
// manifest to GitHub, which creates the App when you confirm and sends you
// back to githubCreated.
func (s *server) githubManifest(w http.ResponseWriter, r *http.Request) {
	by, ok := s.editor(w, r)
	if !ok {
		return
	}
	var in struct {
		Org string `json:"org"`
	}
	if !decodeStrict(w, r, &in) {
		return
	}
	_, files, err := s.Settings.Saved()
	if err != nil {
		webutil.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	fileSecrets, err := s.Settings.FileSecrets()
	if err != nil {
		webutil.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	if msg := config.Misnamed(s.Config.UIURL, s.Self); msg != "" {
		webutil.Error(w, http.StatusConflict, msg)
		return
	}
	if !s.githubView(r.Context(), fileSecrets, files).CanCreate {
		webutil.Error(w, http.StatusConflict, "a GitHub App is set up already")
		return
	}
	suffix := make([]byte, 3)
	rand.Read(suffix)
	manifest, err := githubapp.Manifest("Skybreaker "+hex.EncodeToString(suffix), s.Config.UIURL)
	if err != nil {
		webutil.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	action, err := githubapp.NewAppURL(strings.TrimSpace(in.Org), s.gh.issue(by))
	if err != nil {
		webutil.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	webutil.WriteJSON(w, http.StatusOK, map[string]string{"action": action, "manifest": string(manifest)})
}

// githubCreated is where GitHub sends you back with the new App's code. It
// is a navigation from github.com, so it answers with a redirect to the
// settings page either way.
func (s *server) githubCreated(w http.ResponseWriter, r *http.Request) {
	back := func(q string) { http.Redirect(w, r, "/settings?"+q, http.StatusSeeOther) }
	fail := func(msg string) { back("github_error=" + url.QueryEscape(msg)) }
	p, _ := tsauth.FromContext(r.Context())
	q := r.URL.Query()
	switch {
	case s.Settings == nil || !s.Config.Admin(p.Login) || !s.Config.SettingsInApp():
		fail("only admins can create the GitHub App here")
		return
	case !s.gh.take(q.Get("state"), p.Login):
		fail("that link has expired or was started by someone else; start again here")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	app, err := s.Settings.ConvertGitHubApp(ctx, q.Get("code"))
	if err != nil {
		fail("GitHub didn't hand over the App: " + err.Error())
		return
	}
	_, err = s.Settings.Apply(ctx, p.Login, fmt.Sprintf("created GitHub App %s (ID %s) for %s", app.Slug, app.ID, app.Owner), func(f *config.AppFiles) error {
		if slices.ContainsFunc(f.Secrets.Secrets, func(st secrets.Stored) bool { return st.Name == ghSecret }) {
			return errors.New("a GitHub App is set up already")
		}
		f.Secrets.Secrets = append(f.Secrets.Secrets, secrets.Stored{
			Spec: secrets.Spec{Name: ghSecret, Kind: "github_app", Hosts: []string{"github.com", "api.github.com"},
				GitHubApp: &secrets.GitHubAppSpec{AppID: app.ID, Key: app.PEM}},
			AddedBy: p.Login, Added: time.Now().UTC(),
			App: &secrets.AppInfo{Slug: app.Slug, Owner: app.Owner, HTMLURL: app.HTMLURL},
		})
		return nil
	}, false)
	if err != nil {
		fail(fmt.Sprintf("GitHub created %s, but Skybreaker couldn't save it: %v. Delete it on GitHub and start again.", app.Slug, err))
		return
	}
	back("github=created")
}
