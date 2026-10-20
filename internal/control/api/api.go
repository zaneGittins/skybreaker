// Package api is Skybreaker's HTTP surface: a JSON API and the embedded UI,
// served only to listed tailnet users. The broker's approvals, tokens, kill
// switch and audit log are answered here too. Terminal WebSockets also
// check Origin, because a WebSocket upgrade is a GET that cross-origin
// protection lets through.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"maps"
	"net"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/zaneGittins/skybreaker/internal/board"
	"github.com/zaneGittins/skybreaker/internal/control/app"
	"github.com/zaneGittins/skybreaker/internal/control/chat"
	"github.com/zaneGittins/skybreaker/internal/control/config"
	"github.com/zaneGittins/skybreaker/internal/control/hooks"
	"github.com/zaneGittins/skybreaker/internal/control/mcp"
	"github.com/zaneGittins/skybreaker/internal/control/preview"
	"github.com/zaneGittins/skybreaker/internal/control/sandbox"
	"github.com/zaneGittins/skybreaker/internal/control/scheduler"
	"github.com/zaneGittins/skybreaker/internal/control/sshkeys"
	"github.com/zaneGittins/skybreaker/internal/control/store"
	"github.com/zaneGittins/skybreaker/internal/control/terminal"
	"github.com/zaneGittins/skybreaker/internal/notify/webpush"
	"github.com/zaneGittins/skybreaker/internal/proxy/approval"
	"github.com/zaneGittins/skybreaker/internal/proxy/audit"
	"github.com/zaneGittins/skybreaker/internal/proxy/broker"
	"github.com/zaneGittins/skybreaker/internal/proxy/session"
	"github.com/zaneGittins/skybreaker/internal/tsauth"
	"github.com/zaneGittins/skybreaker/internal/webutil"
)

type Backend interface {
	SandboxState(ctx context.Context, project string) (sandbox.State, error)
	StartSandbox(ctx context.Context, project string) error
	StopSandbox(ctx context.Context, project string) error
	NewSession(ctx context.Context, project, harness, title string) (store.Session, error)
	KillSession(ctx context.Context, id string) error
	Sessions(ctx context.Context, project string) ([]store.Session, error)
	Snapshot(ctx context.Context, id string, lines int) (string, error)
	History(ctx context.Context, id string, since int) (app.History, error)
	AttachArgv(id string) ([]string, error)
	Diff(ctx context.Context, project string) (string, error)
	Subscribe() (<-chan app.Event, func())

	NewChat(ctx context.Context, project, harness string) (store.Session, error)
	ChatSend(ctx context.Context, id, text string) error
	ChatAnswer(ctx context.Context, id, ask string, a chat.Answer) error
	ChatInterrupt(ctx context.Context, id string) error
	ChatMode(ctx context.Context, id, mode string) error
	ChatModel(ctx context.Context, id, model string) error
	ChatShown(id, name string) ([]byte, error)
	ChatLogin(ctx context.Context, id string) error
	ChatTerminal(ctx context.Context, id string) (store.Session, error)
	ChatCompact(ctx context.Context, id string) error
	ChatFork(ctx context.Context, id string) (store.Session, error)
	ChatEvents(id string, after int64) ([]chat.Event, error)
	ChatSubscribe(id string) (<-chan chat.Event, func())
	ChatModes(harness string) []string
	MarkSeen(id string) error
	Review(ctx context.Context, project string) (app.Review, error)
	Listening(ctx context.Context, project string) ([]preview.Listening, error)
	EditSession(id string, e app.SessionEdit) (store.Session, error)
	// SetAuthorizedKeys gives every sandbox's sshd these keys.
	SetAuthorizedKeys(keys []byte) error
	// SSHConn connects to the project's sandbox sshd, starting the sandbox.
	SSHConn(ctx context.Context, project string) (net.Conn, error)
}

// Approvals are harness approvals: may an agent in a terminal run this tool
// in its own sandbox?
type Approvals interface {
	Pending(project string) []hooks.Pending
	Decide(id string, d hooks.Decision) error
}

// Network is the broker as the UI sees it: requests waiting for approval,
// the sandboxes' tokens, the kill switch and the audit log.
type Network interface {
	Approvals() []approval.Approval
	Decide(id string, approve bool, by string) (approval.Approval, error)
	Tokens() []session.Info
	RevokeToken(id, by string) error
	RevokeAll(by string) (int, error)
	Audit(n int) ([]audit.Entry, error)
}

// Push is the web push service.
type Push interface {
	PublicKey() string
	Subscribe(s webpush.Subscription) error
	Unsubscribe(endpoint string) error
}

type Runs interface {
	Trigger(ctx context.Context, job string) (store.Run, error)
}

type Deps struct {
	Config     *config.Config
	Backend    Backend
	Approvals  Approvals
	Network    Network
	Board      *board.Store
	MCP        *mcp.Server // the board's tools and others, for your own agents; nil for none
	Runs       Runs
	Store      *store.Store
	Push       Push              // nil when web push is off
	Previews   *preview.Registry // nil when previews are off
	Projects   ProjectAdder      // nil when projects can't be created here
	SSHKeys    *sshkeys.Store    // nil when SSH into sandboxes is off
	Settings   Settings          // nil when settings can't be changed here
	Identifier tsauth.Identifier
	Static     fs.FS
	Version    string
	// Upgrade accepts a WebSocket; Bridge relays it to argv under a PTY.
	Upgrade func(w http.ResponseWriter, r *http.Request) (terminal.Conn, error)
	Bridge  func(ctx context.Context, c terminal.Conn, argv []string) error
}

type server struct {
	Deps
	tints map[string]int
	ssh   relays
	gh    ghStates
}

func New(d Deps) (http.Handler, error) {
	if d.Config == nil || d.Backend == nil || d.Approvals == nil || d.Network == nil || d.Board == nil || d.Runs == nil || d.Store == nil || d.Identifier == nil || d.Static == nil {
		return nil, errors.New("api: missing dependency")
	}
	s := &server{Deps: d, tints: map[string]int{}}
	for i, name := range slices.Sorted(maps.Keys(d.Config.Projects)) {
		s.tints[name] = i % 8
	}
	m := http.NewServeMux()
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	m.HandleFunc("GET /api/meta", s.meta)
	m.HandleFunc("GET /api/events", s.events)
	m.HandleFunc("GET /api/projects", s.projects)
	m.HandleFunc("POST /api/projects", s.createProject)
	m.HandleFunc("GET /api/capabilities", s.capabilities)
	m.HandleFunc("GET /api/ssh-keys", s.sshKeys)
	m.HandleFunc("POST /api/ssh-keys", s.addSSHKey)
	m.HandleFunc("DELETE /api/ssh-keys/{id}", s.deleteSSHKey)
	m.HandleFunc("GET /api/projects/{p}/ssh", s.sshRelay)
	m.HandleFunc("GET /api/projects/{p}", s.project)
	m.HandleFunc("POST /api/projects/{p}/sandbox", s.sandbox)
	m.HandleFunc("POST /api/projects/{p}/sessions", s.newSession)
	m.HandleFunc("GET /api/projects/{p}/diff", s.diff)
	m.HandleFunc("GET /api/projects/{p}/review", s.review)
	m.HandleFunc("GET /api/projects/{p}/preview", s.preview)
	m.HandleFunc("POST /api/projects/{p}/preview", s.setPreview)
	m.HandleFunc("GET /api/inbox", s.inbox)
	m.HandleFunc("GET /api/sessions/{id}/events", s.chatEvents)
	m.HandleFunc("POST /api/sessions/{id}/messages", s.chatSend)
	m.HandleFunc("POST /api/sessions/{id}/answer", s.chatAnswer)
	m.HandleFunc("POST /api/sessions/{id}/interrupt", s.chatInterrupt)
	m.HandleFunc("POST /api/sessions/{id}/mode", s.chatMode)
	m.HandleFunc("POST /api/sessions/{id}/model", s.chatModel)
	m.HandleFunc("GET /api/sessions/{id}/shown/{name}", s.chatShown)
	m.HandleFunc("POST /api/sessions/{id}/login", s.chatLogin)
	m.HandleFunc("POST /api/sessions/{id}/terminal", s.chatTerminal)
	m.HandleFunc("POST /api/sessions/{id}/compact", s.chatCompact)
	m.HandleFunc("POST /api/sessions/{id}/fork", s.chatFork)
	m.HandleFunc("POST /api/sessions/{id}/seen", s.seen)
	m.HandleFunc("GET /api/push/key", s.pushKey)
	m.HandleFunc("POST /api/push/subscribe", s.pushSubscribe)
	m.HandleFunc("DELETE /api/push/subscribe", s.pushUnsubscribe)
	m.HandleFunc("DELETE /api/sessions/{id}", s.killSession)
	m.HandleFunc("GET /api/sessions/{id}", s.session)
	m.HandleFunc("PATCH /api/sessions/{id}", s.editSession)
	m.HandleFunc("GET /api/sessions/{id}/snapshot", s.snapshot)
	m.HandleFunc("GET /api/sessions/{id}/history", s.history)
	m.HandleFunc("GET /api/sessions/{id}/attach", s.attach)
	m.HandleFunc("GET /api/approvals", s.approvals)
	m.HandleFunc("POST /api/approvals/{id}", s.decide)
	s.boardRoutes(m)
	s.settingsRoutes(m)
	m.HandleFunc("GET /api/requests", s.requests)
	m.HandleFunc("POST /api/requests/{id}/{verb}", s.decideRequest)
	m.HandleFunc("GET /api/activity", s.activity)
	m.HandleFunc("POST /api/tokens/{id}/revoke", s.revokeToken)
	m.HandleFunc("POST /api/kill", s.kill)
	m.HandleFunc("POST /api/jobs/{name}/run", s.runJob)
	m.HandleFunc("GET /api/runs", s.runs)
	m.HandleFunc("GET /api/runs/{id}/log", s.runLog)
	m.Handle("GET /", webutil.Static(d.Static))
	return tsauth.RequireUsers(d.Identifier, d.Config.Users)(webutil.HardenWith(webutil.TerminalCSP, m)), nil
}

func fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, app.ErrNotFound), errors.Is(err, hooks.ErrNotFound), errors.Is(err, approval.ErrNotFound), errors.Is(err, broker.ErrNotFound):
		webutil.Error(w, http.StatusNotFound, err.Error())
	case errors.Is(err, approval.ErrNotPending):
		webutil.Error(w, http.StatusConflict, "this request was already decided or has expired")
	case errors.Is(err, app.ErrBusy):
		webutil.Error(w, http.StatusConflict, err.Error())
	default:
		webutil.Error(w, http.StatusInternalServerError, err.Error())
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(v); err != nil {
		webutil.Error(w, http.StatusBadRequest, "bad JSON body")
		return false
	}
	return true
}

func (s *server) meta(w http.ResponseWriter, r *http.Request) {
	p, _ := tsauth.FromContext(r.Context())
	modes := map[string][]string{}
	for _, h := range s.Config.Harnesses.Names() {
		if m := s.Backend.ChatModes(h); m != nil {
			modes[h] = m
		}
	}
	webutil.WriteJSON(w, http.StatusOK, map[string]any{
		"version": s.Version, "user": p.Name(), "harnesses": s.Config.Harnesses.Names(),
		"chat": modes, "admin": s.Config.Admin(p.Login),
	})
}

func (s *server) events(w http.ResponseWriter, r *http.Request) {
	ch, cancel := s.Backend.Subscribe()
	defer cancel()
	webutil.ServeSSE(w, r, ch, 25*time.Second)
}

type projectView struct {
	Name      string   `json:"name"`
	Repo      string   `json:"repo"`
	Harnesses []string `json:"harnesses"`
	Key       string   `json:"key,omitempty"` // the board's ref prefix
	Subnet    string   `json:"subnet"`
	Tint      int      `json:"tint"`
	Running   bool     `json:"running"`
	Live      int      `json:"live"` // running sessions
	Pending   int      `json:"pending"`
	Open      int      `json:"open"` // board items not done
	Error     string   `json:"error,omitempty"`
}

// view summarises a project, with its sessions from s.sessions. The two
// sandbox questions (its state, its tmux sessions) are asked at once.
func (s *server) view(ctx context.Context, name string) (projectView, []store.Session) {
	p := s.Config.Projects[name]
	v := projectView{Name: name, Repo: p.Repo, Harnesses: p.Harnesses,
		Subnet: s.Config.Subnet(name).String(), Tint: s.tints[name], Key: s.boardKey(name), Open: s.openItems(name), Pending: len(s.Approvals.Pending(name)) + len(s.pendingRequests(name))}
	var sessions []store.Session
	var wg sync.WaitGroup
	wg.Go(func() { sessions = s.sessions(ctx, name) })
	st, err := s.Backend.SandboxState(ctx, name)
	wg.Wait()
	if err != nil {
		v.Error = err.Error()
	}
	v.Running = st.Running
	for _, x := range sessions {
		if x.Status == store.StatusRunning {
			v.Live++
		}
	}
	return v, sessions
}

// sessions lists a project's sessions reconciled with tmux, or as stored
// when the sandbox can't be asked.
func (s *server) sessions(ctx context.Context, name string) []store.Session {
	list, err := s.Backend.Sessions(ctx, name)
	if err != nil {
		return s.Store.ListSessions(name)
	}
	return list
}

func (s *server) projects(w http.ResponseWriter, r *http.Request) {
	// Each project asks its sandbox, which takes a docker call or two; ask
	// them all at once.
	names := slices.Sorted(maps.Keys(s.Config.Projects))
	out := make([]projectView, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Go(func() { out[i], _ = s.view(r.Context(), name) })
	}
	wg.Wait()
	webutil.WriteJSON(w, http.StatusOK, out)
}

func (s *server) known(w http.ResponseWriter, name string) bool {
	if _, ok := s.Config.Projects[name]; !ok {
		webutil.Error(w, http.StatusNotFound, "no such project")
		return false
	}
	return true
}

func (s *server) project(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("p")
	if !s.known(w, name) {
		return
	}
	view, sessions := s.view(r.Context(), name)
	var jobs []*config.Job
	for _, j := range s.Config.Jobs {
		if j.Project == name {
			jobs = append(jobs, j)
		}
	}
	webutil.WriteJSON(w, http.StatusOK, map[string]any{
		"project":  view,
		"sessions": nonNil(sessions),
		"pending":  nonNil(s.Approvals.Pending(name)),
		"requests": nonNil(s.pendingRequests(name)),
		"board":    s.boardCounts(name),
		"runs":     nonNil(s.Store.ListRuns(name, 20)),
		"jobs":     nonNil(jobs),
	})
}

func nonNil[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}

func (s *server) sandbox(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("p")
	if !s.known(w, name) {
		return
	}
	var in struct {
		Action string `json:"action"`
	}
	if !decode(w, r, &in) {
		return
	}
	var err error
	switch in.Action {
	case "start":
		err = s.Backend.StartSandbox(r.Context(), name)
	case "stop":
		err = s.Backend.StopSandbox(r.Context(), name)
	default:
		webutil.Error(w, http.StatusBadRequest, "action must be start or stop")
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	v, _ := s.view(r.Context(), name)
	webutil.WriteJSON(w, http.StatusOK, v)
}

func (s *server) newSession(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("p")
	if !s.known(w, name) {
		return
	}
	var in struct {
		Harness string `json:"harness"`
		Title   string `json:"title"`
		// Mode is "chat" or "terminal"; by default a harness that can
		// chat does.
		Mode string `json:"mode"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.Title) > 200 {
		in.Title = in.Title[:200]
	}
	if !slices.Contains(s.Config.Projects[name].Harnesses, in.Harness) {
		webutil.Error(w, http.StatusBadRequest, "harness "+strconv.Quote(in.Harness)+" is not enabled for "+name)
		return
	}
	canChat := s.Backend.ChatModes(in.Harness) != nil
	switch in.Mode {
	case "":
		if canChat {
			in.Mode = "chat"
		}
	case "chat":
		if !canChat {
			webutil.Error(w, http.StatusBadRequest, in.Harness+" runs only in a terminal")
			return
		}
	case "terminal":
	default:
		webutil.Error(w, http.StatusBadRequest, "mode must be chat or terminal")
		return
	}
	var sess store.Session
	var err error
	if in.Mode == "chat" {
		sess, err = s.Backend.NewChat(r.Context(), name, in.Harness)
	} else {
		sess, err = s.Backend.NewSession(r.Context(), name, in.Harness, in.Title)
	}
	if err != nil {
		webutil.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	webutil.WriteJSON(w, http.StatusOK, sess)
}

func (s *server) diff(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("p")
	if !s.known(w, name) {
		return
	}
	d, err := s.Backend.Diff(r.Context(), name)
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.WriteString(w, d)
}

func (s *server) killSession(w http.ResponseWriter, r *http.Request) {
	if err := s.Backend.KillSession(r.Context(), r.PathValue("id")); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) snapshot(w http.ResponseWriter, r *http.Request) {
	lines, _ := strconv.Atoi(r.URL.Query().Get("lines"))
	text, err := s.Backend.Snapshot(r.Context(), r.PathValue("id"), lines)
	if err != nil {
		fail(w, err)
		return
	}
	webutil.WriteJSON(w, http.StatusOK, map[string]string{"text": text})
}

// session is one session and its project's name and tint, from the store:
// a terminal page needs nothing slower before it attaches.
func (s *server) session(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.Store.Session(r.PathValue("id"))
	if !ok {
		webutil.Error(w, http.StatusNotFound, "no such session")
		return
	}
	webutil.WriteJSON(w, http.StatusOK, map[string]any{
		"session": sess,
		"project": map[string]any{"name": sess.Project, "tint": s.tints[sess.Project]},
	})
}

// editSession renames a session, colours it or sets its goal.
func (s *server) editSession(w http.ResponseWriter, r *http.Request) {
	var e app.SessionEdit
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&e); err != nil {
		webutil.Error(w, http.StatusBadRequest, "bad edit: "+err.Error())
		return
	}
	sess, err := s.Backend.EditSession(r.PathValue("id"), e)
	if errors.Is(err, app.ErrNotFound) {
		webutil.Error(w, http.StatusNotFound, "no such session")
		return
	}
	if err != nil {
		webutil.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	webutil.WriteJSON(w, http.StatusOK, sess)
}

func (s *server) history(w http.ResponseWriter, r *http.Request) {
	since := -1
	if v := r.URL.Query().Get("since"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			webutil.Error(w, http.StatusBadRequest, "since must be a row count")
			return
		}
		since = n
	}
	h, err := s.Backend.History(r.Context(), r.PathValue("id"), since)
	if err != nil {
		fail(w, err)
		return
	}
	webutil.WriteJSONGzip(w, r, http.StatusOK, h)
}

func (s *server) attach(w http.ResponseWriter, r *http.Request) {
	if !tsauth.SameOrigin(r) {
		webutil.Error(w, http.StatusForbidden, "cross-origin terminal refused")
		return
	}
	argv, err := s.Backend.AttachArgv(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	conn, err := s.Upgrade(w, r)
	if err != nil {
		// The upgrader has already answered the request.
		return
	}
	if err := s.Bridge(r.Context(), conn, argv); err != nil {
		log.Printf("skybreaker: terminal %s: %v", r.PathValue("id"), err)
	}
}

func (s *server) approvals(w http.ResponseWriter, r *http.Request) {
	webutil.WriteJSON(w, http.StatusOK, nonNil(s.Approvals.Pending(r.URL.Query().Get("project"))))
}

func (s *server) decide(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Allow   bool   `json:"allow"`
		Message string `json:"message"`
	}
	if !decode(w, r, &in) {
		return
	}
	p, _ := tsauth.FromContext(r.Context())
	if err := s.Approvals.Decide(r.PathValue("id"), hooks.Decision{Allow: in.Allow, Message: in.Message, By: p.Name()}); err != nil {
		fail(w, err)
		return
	}
	webutil.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *server) runJob(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := s.Config.Job(name); !ok {
		webutil.Error(w, http.StatusNotFound, "no such job")
		return
	}
	// The run outlives this request.
	run, err := s.Runs.Trigger(context.WithoutCancel(r.Context()), name)
	if err != nil {
		webutil.Error(w, http.StatusConflict, err.Error())
		return
	}
	webutil.WriteJSON(w, http.StatusOK, run)
}

func (s *server) runs(w http.ResponseWriter, r *http.Request) {
	webutil.WriteJSON(w, http.StatusOK, nonNil(s.Store.ListRuns(r.URL.Query().Get("project"), 100)))
}

// runLog returns the tail of a run's output.
func (s *server) runLog(w http.ResponseWriter, r *http.Request) {
	run, ok := s.Store.Run(r.PathValue("id"))
	if !ok || run.LogPath == "" {
		webutil.Error(w, http.StatusNotFound, "no such run")
		return
	}
	// The run directory is writable from the sandbox: never follow links.
	b, err := scheduler.ReadRunFile(run.LogPath, 256<<10)
	if err != nil {
		webutil.Error(w, http.StatusNotFound, "no log yet")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(b)
}

func (s *server) boardCounts(project string) map[string]int {
	p, _ := s.Board.GetProject(project)
	return p.Counts
}

func (s *server) boardKey(project string) string {
	p, err := s.Board.GetProject(project)
	if err != nil {
		return ""
	}
	return p.Key
}

func (s *server) openItems(project string) int {
	n := 0
	for st, c := range s.boardCounts(project) {
		if st != "done" {
			n += c
		}
	}
	return n
}
