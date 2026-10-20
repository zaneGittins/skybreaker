package api

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zaneGittins/skybreaker/internal/control/config"
	"github.com/zaneGittins/skybreaker/internal/proxy/broker"
	"github.com/zaneGittins/skybreaker/internal/proxy/githubapp"
)

// fakeSettings saves for real, through config.Edit and the broker's check,
// and records what it would audit.
type fakeSettings struct {
	path    string
	file    []FileSecret
	log     []string
	pending bool
	key     string

	instructions string
}

func (s *fakeSettings) Apply(_ context.Context, by, what string, change func(*config.AppFiles) error, dry bool) (Applied, error) {
	before, after, err := config.Edit(s.path, change, func(c *config.Config) error { return broker.Check(c.Policy, c.SecretsFile, c.AppSecretsFile) }, dry)
	if err != nil {
		return Applied{}, err
	}
	out := Applied{Before: before, After: after}
	if !dry {
		s.log = append(s.log, by+": "+what)
		s.pending, out.Restart = true, "now"
	}
	return out, nil
}

func (s *fakeSettings) Saved() (*config.Config, config.AppFiles, error) {
	c, err := config.Load(s.path)
	if err != nil {
		return nil, config.AppFiles{}, err
	}
	f, err := config.ReadAppFiles(c.AppPaths())
	return c, f, err
}

func (s *fakeSettings) Pending() bool                      { return s.pending }
func (s *fakeSettings) FileSecrets() ([]FileSecret, error) { return s.file, nil }
func (s *fakeSettings) SecretsUsed() map[string]time.Time {
	return map[string]time.Time{"openai": time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)}
}
func (s *fakeSettings) GitHubInstallURL(context.Context) (string, error) {
	return "https://github.com/apps/file-app/installations/new", nil
}
func (s *fakeSettings) ConvertGitHubApp(_ context.Context, code string) (githubapp.Created, error) {
	if code != "good-code" {
		return githubapp.Created{}, &githubapp.StatusError{Status: 404, Message: "Not Found"}
	}
	return githubapp.Created{ID: "42", Slug: "skybreaker-abc", Owner: "zaneGittins", HTMLURL: "https://github.com/apps/skybreaker-abc", PEM: s.key}, nil
}

// withSettings adds extra fields to the fixture's config and saves through
// a fakeSettings.
func withSettings(t *testing.T, s *fakeSettings, extra string) func(*Deps) {
	return func(d *Deps) {
		path := filepath.Join(d.Config.DataDir, "skybreaker.json")
		b, _ := os.ReadFile(path)
		body := strings.Replace(string(b), "{", "{"+extra, 1)
		body = strings.Replace(body, `"users": ["zane@example.com"]`, `"users": ["zane@example.com", "kim@example.com"], "ui_url": "https://skybreaker.example.ts.net"`, 1)
		os.WriteFile(path, []byte(body), 0o600)
		c, err := config.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		k, _ := rsa.GenerateKey(rand.Reader, 2048)
		s.path, s.key = path, string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}))
		d.Config, d.Settings = c, s
	}
}

type settingsView struct {
	Admin    bool `json:"admin"`
	Editable bool `json:"editable"`
	Pending  bool `json:"pending"`
	GitHub   struct {
		State      string `json:"state"`
		CanCreate  bool   `json:"can_create"`
		AppID      string `json:"app_id"`
		InstallURL string `json:"install_url"`
	} `json:"github"`
	Secrets []struct {
		Name        string    `json:"name"`
		Source      string    `json:"source"`
		Hosts       []string  `json:"hosts"`
		Fingerprint string    `json:"fingerprint"`
		AddedBy     string    `json:"added_by"`
		LastUsed    time.Time `json:"last_used"`
	} `json:"secrets"`
	Bundles []struct {
		Name   string `json:"name"`
		Source string `json:"source"`
	} `json:"bundles"`
	Projects []struct {
		Name string       `json:"name"`
		App  config.Grant `json:"app"`
	} `json:"projects"`
}

func (f *fixture) settings(t *testing.T) settingsView {
	t.Helper()
	code, out := f.do(t, "GET", "/api/settings", "", nil)
	var v settingsView
	if err := json.Unmarshal([]byte(out), &v); err != nil || code != 200 {
		t.Fatalf("settings: %d %s", code, out)
	}
	return v
}

func TestSecretsAPI(t *testing.T) {
	s := &fakeSettings{file: []FileSecret{{Name: "do", Kind: "static", Hosts: []string{"api.digitalocean.com"}}}}
	f := newFixture(t, you, withSettings(t, s, ""))
	if code, out := f.do(t, "POST", "/api/secrets", `{"name":"openai","value":"sk-very-secret","hosts":["api.openai.com"]}`, nil); code != 200 {
		t.Fatalf("add: %d %s", code, out)
	}
	v := f.settings(t)
	if !v.Admin || !v.Editable || !v.Pending || len(v.Secrets) != 2 {
		t.Fatalf("%+v", v)
	}
	sec := v.Secrets[1]
	if sec.Name != "openai" || sec.Source != "app" || sec.Hosts[0] != "api.openai.com" || len(sec.Fingerprint) != 12 || sec.AddedBy != "zane@example.com" || sec.LastUsed.IsZero() {
		t.Fatalf("listed %+v", sec)
	}
	if v.Secrets[0].Name != "do" || v.Secrets[0].Source != "file" {
		t.Fatalf("file secret %+v", v.Secrets[0])
	}
	_, all := f.do(t, "GET", "/api/settings", "", nil)
	if strings.Contains(all, "sk-very-secret") || strings.Contains(strings.Join(s.log, "\n"), "sk-very-secret") {
		t.Fatal("a value came back out")
	}
	for name, body := range map[string]string{
		"a name in use":          `{"name":"openai","value":"x","hosts":["api.openai.com"]}`,
		"a name the file has":    `{"name":"do","value":"x","hosts":["api.digitalocean.com"]}`,
		"no hosts":               `{"name":"a","value":"x","hosts":[]}`,
		"a wildcard host":        `{"name":"a","value":"x","hosts":["*.example.com"]}`,
		"a newline in the value": `{"name":"a","value":"x\r\nX-Evil: 1","hosts":["h.example"]}`,
		"no value":               `{"name":"a","value":"","hosts":["h.example"]}`,
		"a bad name":             `{"name":"A B","value":"x","hosts":["h.example"]}`,
		"a kind":                 `{"name":"a","kind":"file","file":"/etc/shadow","hosts":["h.example"]}`,
	} {
		if code, _ := f.do(t, "POST", "/api/secrets", body, nil); code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, code)
		}
	}
	// New hosts only with the value: a session that never saw it can't
	// send it somewhere new.
	if code, _ := f.do(t, "PUT", "/api/secrets/openai", `{"hosts":["evil.example"]}`, nil); code != http.StatusBadRequest {
		t.Fatalf("hosts changed without the value: %d", code)
	}
	if code, out := f.do(t, "PUT", "/api/secrets/openai", `{"value":"sk-2","hosts":["api.openai.com","eu.api.openai.com"]}`, nil); code != 200 {
		t.Fatalf("replace: %d %s", code, out)
	}
	if v := f.settings(t); len(v.Secrets[1].Hosts) != 2 || v.Secrets[1].Fingerprint == sec.Fingerprint {
		t.Fatalf("replaced %+v", v.Secrets[1])
	}
	if code, _ := f.do(t, "PUT", "/api/secrets/do", `{"value":"x","hosts":["api.digitalocean.com"]}`, nil); code != http.StatusNotFound {
		t.Fatalf("replaced the file's secret: %d", code)
	}
	if code, _ := f.do(t, "DELETE", "/api/secrets/openai", "", nil); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if v := f.settings(t); len(v.Secrets) != 1 {
		t.Fatalf("after delete %+v", v.Secrets)
	}
	if len(s.log) != 3 || !strings.HasPrefix(s.log[0], "zane@example.com: ") {
		t.Fatalf("audit %q", s.log)
	}
}

// A secret in use can't be deleted out from under its bundle.
func TestSecretInUse(t *testing.T) {
	s := &fakeSettings{}
	f := newFixture(t, you, withSettings(t, s, ""))
	f.do(t, "POST", "/api/secrets", `{"name":"openai","value":"sk","hosts":["api.openai.com"]}`, nil)
	bundle := `{"entries":[{"host":"api.openai.com","mode":"mitm","methods":["POST"],"inject":{"bearer":"openai"}}]}`
	if code, out := f.do(t, "PUT", "/api/bundles/openai", bundle, nil); code != 200 {
		t.Fatalf("bundle: %d %s", code, out)
	}
	if code, out := f.do(t, "PUT", "/api/projects/orbit/access", `{"capabilities":["openai"]}`, nil); code != 200 {
		t.Fatalf("grant: %d %s", code, out)
	}
	if code, out := f.do(t, "DELETE", "/api/secrets/openai", "", nil); code != http.StatusBadRequest || !strings.Contains(out, "openai") {
		t.Fatalf("deleted a secret in use: %d %s", code, out)
	}
	// Injecting a secret toward a host it isn't bound to fails the check.
	if code, _ := f.do(t, "PUT", "/api/bundles/leak", `{"entries":[{"host":"evil.example","mode":"mitm","methods":["GET"],"inject":{"bearer":"openai"}}]}`, nil); code != http.StatusBadRequest {
		t.Fatalf("secret sent off its hosts: %d", code)
	}
}

func TestAccessAPI(t *testing.T) {
	s := &fakeSettings{}
	f := newFixture(t, you, withSettings(t, s, ""))
	bundle := `{"entries":[{"host":"api.openai.com","mode":"mitm","methods":["GET","POST"]}]}`
	if code, out := f.do(t, "PUT", "/api/bundles/openai", bundle, nil); code != 200 {
		t.Fatalf("bundle: %d %s", code, out)
	}
	before, _ := os.ReadFile(filepath.Join(d0(t, s.path), "access.json"))
	code, out := f.do(t, "PUT", "/api/projects/orbit/access", `{"capabilities":["openai"],"allow":[{"host":"pypi.org","mode":"tunnel"}],"dry_run":true}`, nil)
	var r struct {
		Restart string `json:"restart"`
		Review  []struct {
			Project string `json:"project"`
			Added   []struct {
				Target string `json:"target"`
				Bundle string `json:"bundle"`
			} `json:"added"`
			Removed []any `json:"removed"`
		} `json:"review"`
	}
	json.Unmarshal([]byte(out), &r)
	if code != 200 || r.Restart != "" || len(r.Review) != 1 || r.Review[0].Project != "orbit" || len(r.Review[0].Added) != 2 || r.Review[0].Added[0].Target != "api.openai.com" || r.Review[0].Added[0].Bundle != "openai" {
		t.Fatalf("review: %d %s", code, out)
	}
	if after, _ := os.ReadFile(filepath.Join(d0(t, s.path), "access.json")); string(after) != string(before) {
		t.Fatal("a dry run saved")
	}
	if code, out := f.do(t, "PUT", "/api/projects/orbit/access", `{"capabilities":["openai"],"allow":[{"host":"pypi.org","mode":"tunnel"}]}`, nil); code != 200 || !strings.Contains(out, `"restart":"now"`) {
		t.Fatalf("save: %d %s", code, out)
	}
	v := f.settings(t)
	if len(v.Projects) != 1 || len(v.Projects[0].App.Capabilities) != 1 || len(v.Projects[0].App.Allow) != 1 {
		t.Fatalf("projects %+v", v.Projects)
	}
	if len(v.Bundles) != 1 || v.Bundles[0].Source != "app" {
		t.Fatalf("bundles %+v", v.Bundles)
	}
	for name, c := range map[string][2]string{
		"a route":           {"/api/bundles/x", `{"entries":[{"route":"/x","upstream":"https://x.example","rules":[{"match":"GET /**","action":"auto"}]}]}`},
		"an unknown field":  {"/api/projects/orbit/access", `{"capabilities":[],"env":{"A":"b"}}`},
		"an unknown bundle": {"/api/projects/orbit/access", `{"capabilities":["everything"]}`},
		"a bad bundle name": {"/api/bundles/Bad%20Name", bundle},
	} {
		if code, _ := f.do(t, "PUT", c[0], c[1], nil); code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, code)
		}
	}
	if code, _ := f.do(t, "PUT", "/api/projects/ghost/access", `{}`, nil); code != http.StatusNotFound {
		t.Fatalf("unknown project: %d", code)
	}
	// A bundle in use can't be deleted; once unused it can.
	if code, _ := f.do(t, "DELETE", "/api/bundles/openai", "", nil); code != http.StatusBadRequest {
		t.Fatalf("deleted a bundle in use: %d", code)
	}
	f.do(t, "PUT", "/api/projects/orbit/access", `{}`, nil)
	if code, out := f.do(t, "DELETE", "/api/bundles/openai", "", nil); code != 200 {
		t.Fatalf("delete: %d %s", code, out)
	}
}

func TestSettingsNeedAnAdmin(t *testing.T) {
	s := &fakeSettings{}
	kim := you
	kim.Login = "kim@example.com"
	f := newFixture(t, kim, withSettings(t, s, `"admins": ["zane@example.com"],`))
	if v := f.settings(t); v.Admin || v.Editable {
		t.Fatalf("%+v", v)
	}
	for _, c := range [][3]string{
		{"POST", "/api/secrets", `{"name":"a","value":"x","hosts":["h.example"]}`},
		{"PUT", "/api/secrets/a", `{"value":"x","hosts":["h.example"]}`},
		{"DELETE", "/api/secrets/a", ""},
		{"PUT", "/api/bundles/a", `{"entries":[]}`},
		{"DELETE", "/api/bundles/a", ""},
		{"PUT", "/api/projects/orbit/access", `{}`},
		{"POST", "/api/github-app/manifest", `{}`},
		{"POST", "/api/projects", `{"name":"comet","repo":"a/b","harnesses":["claude"]}`},
	} {
		if code, _ := f.do(t, c[0], c[1], c[2], nil); code != http.StatusForbidden {
			t.Errorf("%s %s: %d", c[0], c[1], code)
		}
	}
	if len(s.log) != 0 {
		t.Fatal("a non-admin saved")
	}
}

func TestSettingsOffInFile(t *testing.T) {
	s := &fakeSettings{}
	f := newFixture(t, you, withSettings(t, s, `"app_settings": false,`))
	if v := f.settings(t); !v.Admin || v.Editable {
		t.Fatalf("%+v", v)
	}
	if code, _ := f.do(t, "POST", "/api/secrets", `{"name":"a","value":"x","hosts":["h.example"]}`, nil); code != http.StatusForbidden {
		t.Fatalf("saved with app_settings off: %d", code)
	}
}

func TestGitHubAppCreation(t *testing.T) {
	s := &fakeSettings{file: []FileSecret{{Name: "github_app", Kind: "github_app", Hosts: []string{"github.com", "api.github.com"}, FromApp: true}}}
	f := newFixture(t, you, withSettings(t, s, ""))
	if v := f.settings(t); v.GitHub.State != "placeholder" || !v.GitHub.CanCreate {
		t.Fatalf("%+v", v.GitHub)
	}
	code, out := f.do(t, "POST", "/api/github-app/manifest", `{"org":""}`, nil)
	var m struct {
		Action   string `json:"action"`
		Manifest string `json:"manifest"`
	}
	json.Unmarshal([]byte(out), &m)
	u, err := url.Parse(m.Action)
	if code != 200 || err != nil || u.Host != "github.com" || u.Query().Get("state") == "" || !strings.Contains(m.Manifest, `"redirect_url":"https://skybreaker.example.ts.net/api/github-app/created"`) {
		t.Fatalf("manifest: %d %s", code, out)
	}
	state := u.Query().Get("state")
	noRedirect := func(path string) (int, string) {
		c := f.ts.Client()
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		resp, err := c.Get(f.ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode, resp.Header.Get("Location")
	}
	if code, loc := noRedirect("/api/github-app/created?code=good-code&state=forged"); code != http.StatusSeeOther || !strings.Contains(loc, "github_error=") {
		t.Fatalf("forged state: %d %s", code, loc)
	}
	if len(s.log) != 0 {
		t.Fatal("saved on a forged state")
	}
	if code, loc := noRedirect("/api/github-app/created?code=good-code&state=" + url.QueryEscape(state)); code != http.StatusSeeOther || loc != "/settings?github=created" {
		t.Fatalf("created: %d %s", code, loc)
	}
	// One use per state.
	if _, loc := noRedirect("/api/github-app/created?code=good-code&state=" + url.QueryEscape(state)); !strings.Contains(loc, "github_error=") {
		t.Fatalf("state reused: %s", loc)
	}
	saved, _ := config.Load(s.path)
	files, _ := config.ReadAppFiles(saved.AppPaths())
	if len(files.Secrets.Secrets) != 1 || files.Secrets.Secrets[0].Name != "github_app" || files.Secrets.Secrets[0].GitHubApp.AppID != "42" || files.Secrets.Secrets[0].App.Slug != "skybreaker-abc" {
		t.Fatalf("saved %+v", files.Secrets)
	}
	if v := f.settings(t); v.GitHub.State != "app" || v.GitHub.AppID != "42" || v.GitHub.InstallURL != "https://github.com/apps/skybreaker-abc/installations/new" || v.GitHub.CanCreate {
		t.Fatalf("%+v", v.GitHub)
	}
	_, all := f.do(t, "GET", "/api/settings", "", nil)
	if strings.Contains(all, "PRIVATE KEY") {
		t.Fatal("the App's key came back out")
	}
	if code, _ := f.do(t, "POST", "/api/github-app/manifest", `{}`, nil); code != http.StatusConflict {
		t.Fatalf("a second App: %d", code)
	}
}

// A state is good once, for the user it was issued to, for an hour.
func TestGitHubStates(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	g := ghStates{now: func() time.Time { return now }}
	st := g.issue("zane@example.com")
	if g.take(st, "kim@example.com") || g.take(st, "zane@example.com") {
		t.Fatal("another user's state, or one tried by another user")
	}
	st = g.issue("zane@example.com")
	if !g.take(st, "zane@example.com") || g.take(st, "zane@example.com") {
		t.Fatal("used twice")
	}
	st = g.issue("zane@example.com")
	now = now.Add(61 * time.Minute)
	if g.take(st, "zane@example.com") {
		t.Fatal("an hour-old state")
	}
	if g.take("", "zane@example.com") {
		t.Fatal("no state")
	}
}

func TestFileGitHubApp(t *testing.T) {
	s := &fakeSettings{file: []FileSecret{{Name: "github_app", Kind: "github_app", Hosts: []string{"github.com"}, AppID: "5373876"}}}
	f := newFixture(t, you, withSettings(t, s, ""))
	if v := f.settings(t); v.GitHub.State != "file" || v.GitHub.CanCreate || v.GitHub.AppID != "5373876" || v.GitHub.InstallURL != "https://github.com/apps/file-app/installations/new" {
		t.Fatalf("%+v", v.GitHub)
	}
	if code, _ := f.do(t, "POST", "/api/github-app/manifest", `{}`, nil); code != http.StatusConflict {
		t.Fatalf("replaced the file's App: %d", code)
	}
}

// The app's pages may post a form to themselves and to GitHub (the App's
// manifest), nowhere else.
func TestFormAction(t *testing.T) {
	f := newFixture(t, you)
	resp, err := f.ts.Client().Get(f.ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "form-action 'self' https://github.com;") {
		t.Fatalf("csp %q", csp)
	}
}

func TestGitHubAppNeedsUIURL(t *testing.T) {
	s := &fakeSettings{}
	f := newFixture(t, you, withSettings(t, s, ""), func(d *Deps) { d.Config.UIURL = "" })
	if v := f.settings(t); v.GitHub.CanCreate {
		t.Fatalf("%+v", v.GitHub)
	}
}

// d0 is where the app's files for the config at path go.
func d0(t *testing.T, path string) string {
	t.Helper()
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(c.AppPaths().Access)
}

func (s *fakeSettings) Instructions() (string, error) { return s.instructions, nil }
func (s *fakeSettings) SetInstructions(by, text string) error {
	if err := config.CheckInstructions(text); err != nil {
		return err
	}
	s.instructions = text
	s.log = append(s.log, by+": changed the agents' instructions")
	return nil
}

// Every agent's instructions: anyone may read them, admins change them.
func TestInstructionsAPI(t *testing.T) {
	s := &fakeSettings{instructions: "Be brief."}
	f := newFixture(t, you, withSettings(t, s, ""))
	get := func(f *fixture) (string, bool) {
		t.Helper()
		code, body := f.do(t, "GET", "/api/instructions", "", nil)
		var out struct {
			Text     string `json:"text"`
			Editable bool   `json:"editable"`
		}
		if err := json.Unmarshal([]byte(body), &out); err != nil || code != 200 {
			t.Fatalf("%d %s", code, body)
		}
		return out.Text, out.Editable
	}
	if text, editable := get(f); text != "Be brief." || !editable {
		t.Fatalf("%q %v", text, editable)
	}
	if code, out := f.do(t, "PUT", "/api/instructions", `{"text":"Use British spelling.\r\nRun the tests."}`, nil); code != 200 {
		t.Fatalf("save: %d %s", code, out)
	}
	if s.instructions != "Use British spelling.\nRun the tests." || len(s.log) != 1 || s.log[0] != "zane@example.com: changed the agents' instructions" {
		t.Fatalf("saved %q, log %q", s.instructions, s.log)
	}
	for name, body := range map[string]string{
		"nul":     `{"text":"a\u0000b"}`,
		"unknown": `{"text":"a","extra":1}`,
		"long":    `{"text":"` + strings.Repeat("x", config.MaxInstructions+1) + `"}`,
	} {
		if code, _ := f.do(t, "PUT", "/api/instructions", body, nil); code != 400 {
			t.Errorf("%s: %d", name, code)
		}
	}

	kim := you
	kim.Login = "kim@example.com"
	f = newFixture(t, kim, withSettings(t, s, `"admins": ["zane@example.com"],`))
	if text, editable := get(f); text == "" || editable {
		t.Errorf("kim: %q %v", text, editable)
	}
	if code, _ := f.do(t, "PUT", "/api/instructions", `{"text":"x"}`, nil); code != 403 {
		t.Errorf("kim saved: %d", code)
	}
}
