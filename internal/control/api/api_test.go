package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/zaneGittins/skybreaker/internal/assistant"
	"github.com/zaneGittins/skybreaker/internal/board"
	"github.com/zaneGittins/skybreaker/internal/control/app"
	"github.com/zaneGittins/skybreaker/internal/control/config"
	"github.com/zaneGittins/skybreaker/internal/control/hooks"
	"github.com/zaneGittins/skybreaker/internal/control/mcp"
	"github.com/zaneGittins/skybreaker/internal/control/sandbox"
	"github.com/zaneGittins/skybreaker/internal/control/store"
	"github.com/zaneGittins/skybreaker/internal/control/terminal"
	"github.com/zaneGittins/skybreaker/internal/tsauth"
)

type fakeBackend struct {
	mu         sync.Mutex
	running    map[string]bool
	sessions   []store.Session
	killed     []string
	events     chan app.Event
	chat       *fakeChat
	authorized string // what SetAuthorizedKeys last got
	onSend     func() // called as a message is sent
}

func (b *fakeBackend) SandboxState(_ context.Context, p string) (sandbox.State, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if p == "ghost" {
		return sandbox.State{}, app.ErrNotFound
	}
	return sandbox.State{Exists: b.running[p], Running: b.running[p]}, nil
}
func (b *fakeBackend) StartSandbox(_ context.Context, p string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.running[p] = true
	return nil
}
func (b *fakeBackend) StopSandbox(_ context.Context, p string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.running[p] = false
	return nil
}
func (b *fakeBackend) NewSession(_ context.Context, p, h, title string, o app.NewOptions) (store.Session, error) {
	if o != (app.NewOptions{}) {
		b.chat.call("terminal " + p + " " + h + optsCall(o))
	}
	if h == "pi" {
		return store.Session{}, errors.New(`harness "pi" is not enabled for orbit`)
	}
	s := store.Session{ID: "s1", Project: p, Harness: h, Title: title, Status: store.StatusRunning, Created: time.Now()}
	b.mu.Lock()
	b.sessions = append(b.sessions, s)
	b.mu.Unlock()
	return s, nil
}
func (b *fakeBackend) KillSession(_ context.Context, id string) error {
	if id != "s1" {
		return app.ErrNotFound
	}
	b.killed = append(b.killed, id)
	return nil
}
func (b *fakeBackend) Sessions(_ context.Context, p string) ([]store.Session, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sessions, nil
}
func (b *fakeBackend) Snapshot(_ context.Context, id string, lines int) (string, error) {
	if id != "s1" {
		return "", app.ErrNotFound
	}
	return fmt.Sprintf("last %d lines", lines), nil
}
func (b *fakeBackend) History(_ context.Context, id string, since int) (app.History, error) {
	if id != "s1" {
		return app.History{}, app.ErrNotFound
	}
	return app.History{Size: 10, Text: fmt.Sprintf("since %d", since)}, nil
}
func (b *fakeBackend) AttachArgv(id string) ([]string, error) {
	if id != "s1" {
		return nil, app.ErrNotFound
	}
	return []string{"docker", "exec", "-it", "skybreaker-orbit", "tmux", "attach-session", "-t", "=s1"}, nil
}
func (b *fakeBackend) Diff(context.Context, string) (string, error) { return "diff --git a/x b/x", nil }
func (b *fakeBackend) Subscribe() (<-chan app.Event, func())        { return b.events, func() {} }

type fakeApprovals struct {
	decided map[string]hooks.Decision
}

func (f *fakeApprovals) Pending(project string) []hooks.Pending {
	return []hooks.Pending{{ID: "a1", Project: "orbit", Session: "s1", Tool: "Bash", Input: json.RawMessage(`{"command":"make test"}`)}}
}
func (f *fakeApprovals) Decide(id string, d hooks.Decision) error {
	if id != "a1" {
		return hooks.ErrNotFound
	}
	f.decided[id] = d
	return nil
}

type fakeRuns struct{ triggered []string }

func (f *fakeRuns) Trigger(_ context.Context, job string) (store.Run, error) {
	if job != "nightly" {
		return store.Run{}, errors.New("job not found")
	}
	f.triggered = append(f.triggered, job)
	return store.Run{ID: "r1", Job: job, Project: "orbit", Status: store.RunRunning}, nil
}

type fixture struct {
	ts        *httptest.Server
	backend   *fakeBackend
	approvals *fakeApprovals
	network   *fakeNetwork
	boards    *board.Store
	runs      *fakeRuns
	store     *store.Store
	attached  chan []string
	cfg       *config.Config
}

func newFixture(t *testing.T, peer tsauth.Peer, opts ...func(*Deps)) *fixture {
	t.Helper()
	dir := t.TempDir()
	cfgJSON := fmt.Sprintf(`{
	  "data_dir": %q, "users": ["zane@example.com"],
	  "sandbox": {"image": "img"},
	  "projects": {"orbit": {"repo": "zaneGittins/orbit", "harnesses": ["claude", "codex"]}},
	  "jobs": [{"name": "nightly", "project": "orbit", "schedule": "0 2 * * *", "kind": "board"}]
	}`, dir)
	p := filepath.Join(dir, "skybreaker.json")
	os.WriteFile(p, []byte(cfgJSON), 0o600)
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := store.Open("")
	logPath := filepath.Join(dir, "r1.log")
	os.WriteFile(logPath, []byte("line one\nline two\nERROR error sending request for url (http://broker:8402/mcp/orbit/tok-orbit-0123456789/-)\n"), 0o644)
	st.CreateRun(store.Run{ID: "r1", Job: "nightly", Project: "orbit", Status: store.RunOK, LogPath: logPath, Started: time.Now()})
	f := &fixture{cfg: cfg,
		backend:   &fakeBackend{running: map[string]bool{}, events: make(chan app.Event, 4), chat: newFakeChat()},
		approvals: &fakeApprovals{decided: map[string]hooks.Decision{}},
		network:   newFakeNetwork(),
		boards:    newBoards(t),
		runs:      &fakeRuns{},
		store:     st,
		attached:  make(chan []string, 1),
	}
	d := Deps{
		Config: cfg, Backend: f.backend, Approvals: f.approvals, Network: f.network, Board: f.boards, MCP: &mcp.Server{Name: "skybreaker", Tools: []func(mcp.Caller) []mcp.Tool{board.Tools(f.boards)}}, Runs: f.runs, Store: st,
		Identifier: tsauth.Static(peer), Static: fstest.MapFS{"index.html": {Data: []byte("<!doctype html>skybreaker")}},
		Version: "test",
		Upgrade: func(w http.ResponseWriter, r *http.Request) (terminal.Conn, error) {
			return nil, errors.New("no websocket in tests")
		},
		Bridge: func(ctx context.Context, c terminal.Conn, argv []string) error {
			f.attached <- argv
			return nil
		},
	}
	for _, o := range opts {
		o(&d)
	}
	h, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	f.ts = httptest.NewServer(h)
	t.Cleanup(f.ts.Close)
	return f
}

var you = tsauth.Peer{Node: "phone", Login: "zane@example.com"}

func (f *fixture) do(t *testing.T, method, path, body string, hdr map[string]string) (int, string) {
	t.Helper()
	r, _ := http.NewRequest(method, f.ts.URL+path, strings.NewReader(body))
	if method != "GET" {
		r.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestOnlyUsers(t *testing.T) {
	for _, p := range []tsauth.Peer{{Node: "x", Login: "eve@example.com"}, {Node: "agent", Tags: []string{"tag:agent-host"}, Login: "zane@example.com"}} {
		f := newFixture(t, p)
		if code, _ := f.do(t, "GET", "/api/projects", "", nil); code != 403 {
			t.Errorf("%s: %d", p.Node, code)
		}
	}
}

func TestProjects(t *testing.T) {
	f := newFixture(t, you)
	code, body := f.do(t, "GET", "/api/projects", "", nil)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var ps []struct {
		Name      string   `json:"name"`
		Harnesses []string `json:"harnesses"`
		Running   bool     `json:"running"`
		Pending   int      `json:"pending"`
		Tint      int      `json:"tint"`
	}
	json.Unmarshal([]byte(body), &ps)
	if len(ps) != 1 || ps[0].Name != "orbit" || !slices.Equal(ps[0].Harnesses, []string{"claude", "codex", "shell"}) || ps[0].Pending != 2 {
		t.Fatalf("projects = %s", body)
	}
	// The live count comes from the reconciled list, not the store, which
	// still says running for a session whose harness has exited.
	f.store.CreateSession(store.Session{ID: "s9", Project: "orbit", Status: store.StatusRunning})
	f.backend.mu.Lock()
	f.backend.sessions = []store.Session{{ID: "s9", Project: "orbit", Status: store.StatusExited}}
	f.backend.mu.Unlock()
	if _, body := f.do(t, "GET", "/api/projects", "", nil); !strings.Contains(body, `"live":0`) {
		t.Fatalf("exited session counted live: %s", body)
	}
	code, body = f.do(t, "GET", "/api/projects/orbit", "", nil)
	if code != 200 || !strings.Contains(body, `"jobs"`) || !strings.Contains(body, `"nightly"`) || !strings.Contains(body, `"r1"`) {
		t.Fatalf("project detail: %d %s", code, body)
	}
	// What's wrong with a job reaches the Runs tab.
	j, _ := f.cfg.Job("nightly")
	j.Ignored, j.Problem = []string{"budget_usd"}, "no MCP server"
	if _, body := f.do(t, "GET", "/api/projects/orbit", "", nil); !strings.Contains(body, `"ignored":["budget_usd"]`) || !strings.Contains(body, `"problem":"no MCP server"`) {
		t.Errorf("job view: %s", body)
	}
	if code, _ := f.do(t, "GET", "/api/projects/ghost", "", nil); code != 404 {
		t.Errorf("unknown project: %d", code)
	}
}

// The app shows assistants apart from repos, so every project says its kind.
func TestProjectKind(t *testing.T) {
	as, _ := assistant.Open("")
	as.SetLook("pip", assistant.Look{Name: "Juno", Shape: "crescent", Color: "ink", Temper: "calm"})
	as.Propose("pip", assistant.Proposal{Kind: assistant.ProposeMemory, Title: "Likes jazz"})
	f := newFixture(t, you, func(d *Deps) {
		d.Config.Projects["pip"] = &config.Project{Kind: config.KindAssistant, Harnesses: []string{"claude"}}
		d.Assistants = as
	})
	_, body := f.do(t, "GET", "/api/projects", "", nil)
	// An assistant brings its look, for the sidebar to draw.
	if !strings.Contains(body, `"name":"pip","kind":"assistant"`) || !strings.Contains(body, `"look":{"name":"Juno","shape":"crescent"`) || !strings.Contains(body, `"asks":1`) || !strings.Contains(body, `"name":"orbit","kind":""`) {
		t.Fatalf("projects = %s", body)
	}
}

// A session's page finds its session and project in one quick read: no
// sandbox is asked.
func TestSessionLookup(t *testing.T) {
	f := newFixture(t, you)
	f.store.CreateSession(store.Session{ID: "s7", Project: "orbit", Harness: "claude", Title: "fix login", Status: store.StatusRunning})
	code, body := f.do(t, "GET", "/api/sessions/s7", "", nil)
	if code != 200 || !strings.Contains(body, `"fix login"`) || !strings.Contains(body, `"project":{"name":"orbit","tint":`) {
		t.Fatalf("lookup: %d %s", code, body)
	}
	if code, _ := f.do(t, "GET", "/api/sessions/nope", "", nil); code != 404 {
		t.Errorf("unknown session: %d", code)
	}
}

func TestSandboxAndSessions(t *testing.T) {
	f := newFixture(t, you)
	if code, b := f.do(t, "POST", "/api/projects/orbit/sandbox", `{"action":"start"}`, nil); code != 200 {
		t.Fatalf("start: %d %s", code, b)
	}
	if !f.backend.running["orbit"] {
		t.Fatal("sandbox not started")
	}
	if code, _ := f.do(t, "POST", "/api/projects/orbit/sandbox", `{"action":"explode"}`, nil); code != 400 {
		t.Errorf("bad action: %d", code)
	}
	code, body := f.do(t, "POST", "/api/projects/orbit/sessions", `{"harness":"claude","title":"fix","mode":"terminal"}`, nil)
	if code != 200 || !strings.Contains(body, `"s1"`) {
		t.Fatalf("new session: %d %s", code, body)
	}
	if code, _ := f.do(t, "POST", "/api/projects/orbit/sessions", `{"harness":"pi"}`, nil); code != 400 {
		t.Errorf("disabled harness: %d", code)
	}
	if code, b := f.do(t, "GET", "/api/sessions/s1/history?since=7", "", nil); code != 200 || !strings.Contains(b, `"since 7"`) || !strings.Contains(b, `"size":10`) {
		t.Errorf("history: %d %s", code, b)
	}
	if code, b := f.do(t, "GET", "/api/sessions/s1/history", "", nil); code != 200 || !strings.Contains(b, `"since -1"`) {
		t.Errorf("history without since: %d %s", code, b)
	}
	if code, _ := f.do(t, "GET", "/api/sessions/nope/history", "", nil); code != 404 {
		t.Errorf("unknown history: %d", code)
	}
	if code, b := f.do(t, "GET", "/api/sessions/s1/snapshot?lines=10", "", nil); code != 200 || !strings.Contains(b, "last 10 lines") {
		t.Errorf("snapshot: %d %s", code, b)
	}
	if code, _ := f.do(t, "DELETE", "/api/sessions/s1", "", nil); code != 204 {
		t.Errorf("kill: %d", code)
	}
	if code, _ := f.do(t, "DELETE", "/api/sessions/zz", "", nil); code != 404 {
		t.Errorf("kill unknown: %d", code)
	}
	if code, b := f.do(t, "GET", "/api/projects/orbit/diff", "", nil); code != 200 || !strings.Contains(b, "diff --git") {
		t.Errorf("diff: %d %s", code, b)
	}
}

func TestWritesNeedJSON(t *testing.T) {
	f := newFixture(t, you)
	code, _ := f.do(t, "POST", "/api/projects/orbit/sandbox", `action=start`, map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	if code != http.StatusUnsupportedMediaType || f.backend.running["orbit"] {
		t.Fatalf("form post: %d running=%v", code, f.backend.running["orbit"])
	}
}

func TestApprovals(t *testing.T) {
	f := newFixture(t, you)
	code, body := f.do(t, "GET", "/api/approvals", "", nil)
	if code != 200 || !strings.Contains(body, "make test") {
		t.Fatalf("list: %d %s", code, body)
	}
	if code, _ := f.do(t, "POST", "/api/approvals/a1", `{"allow":true}`, nil); code != 200 {
		t.Fatalf("decide: %d", code)
	}
	if d := f.approvals.decided["a1"]; !d.Allow || d.By != "zane@example.com" {
		t.Fatalf("decision = %+v", d)
	}
	if code, _ := f.do(t, "POST", "/api/approvals/zz", `{"allow":true}`, nil); code != 404 {
		t.Errorf("unknown approval: %d", code)
	}
}

func TestRuns(t *testing.T) {
	f := newFixture(t, you)
	if code, b := f.do(t, "POST", "/api/jobs/nightly/run", `{}`, nil); code != 200 || !strings.Contains(b, `"r1"`) {
		t.Fatalf("trigger: %d %s", code, b)
	}
	if code, _ := f.do(t, "POST", "/api/jobs/nope/run", `{}`, nil); code != 404 {
		t.Errorf("unknown job: %d", code)
	}
	if code, b := f.do(t, "GET", "/api/runs?project=orbit", "", nil); code != 200 || !strings.Contains(b, `"nightly"`) {
		t.Errorf("runs: %d %s", code, b)
	}
	if code, b := f.do(t, "GET", "/api/runs/r1/log", "", nil); code != 200 || !strings.Contains(b, "line two") {
		t.Errorf("log: %d %s", code, b)
	} else if strings.Contains(b, "tok-orbit-0123456789") || !strings.Contains(b, "/mcp/orbit/[hook token]/-") {
		t.Errorf("hook token not redacted: %s", b)
	}
}

func TestAttachChecksOrigin(t *testing.T) {
	f := newFixture(t, you)
	host := strings.TrimPrefix(f.ts.URL, "http://")
	code, _ := f.do(t, "GET", "/api/sessions/s1/attach", "", map[string]string{"Origin": "https://evil.example.ts.net", "Upgrade": "websocket", "Connection": "Upgrade"})
	if code != 403 {
		t.Fatalf("cross-origin attach: %d", code)
	}
	code, _ = f.do(t, "GET", "/api/sessions/s1/attach", "", map[string]string{"Upgrade": "websocket", "Connection": "Upgrade"})
	if code != 403 {
		t.Fatalf("attach without Origin: %d", code)
	}
	// Same origin passes the check and reaches the upgrader (which fails in tests).
	code, _ = f.do(t, "GET", "/api/sessions/s1/attach", "", map[string]string{"Origin": "http://" + host})
	if code == 403 {
		t.Fatal("same-origin attach refused")
	}
	if code, _ := f.do(t, "GET", "/api/sessions/nope/attach", "", map[string]string{"Origin": "http://" + host}); code != 404 {
		t.Errorf("attach unknown session: %d", code)
	}
}

func TestMeta(t *testing.T) {
	f := newFixture(t, you)
	code, body := f.do(t, "GET", "/api/meta", "", nil)
	if code != 200 || !strings.Contains(body, `"user":"zane@example.com"`) || strings.Contains(body, "broker_ui") {
		t.Fatalf("meta: %d %s", code, body)
	}
}

func TestRunLogRefusesSymlink(t *testing.T) {
	f := newFixture(t, you)
	dir := t.TempDir()
	secret := filepath.Join(dir, "state.json")
	os.WriteFile(secret, []byte("host state"), 0o600)
	link := filepath.Join(dir, "log.jsonl")
	os.Symlink(secret, link)
	f.store.CreateRun(store.Run{ID: "r2", Job: "nightly", Project: "orbit", LogPath: link, Started: time.Now()})
	if code, b := f.do(t, "GET", "/api/runs/r2/log", "", nil); code == 200 || strings.Contains(b, "host state") {
		t.Fatalf("symlinked log served: %d %s", code, b)
	}
}

func TestEventsStream(t *testing.T) {
	f := newFixture(t, you)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, "GET", f.ts.URL+"/api/events", nil)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); resp.StatusCode != 200 || ct != "text/event-stream" {
		t.Fatalf("%d %q", resp.StatusCode, ct)
	}
	f.backend.events <- app.Event{Type: "session", Project: "orbit", Session: "s1"}
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var ev app.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil || ev.Type != "session" || ev.Session != "s1" {
			t.Fatalf("event %q: %v", line, err)
		}
		return
	}
	t.Fatalf("stream ended without the event: %v", sc.Err())
}

func TestEventsOnlyForUsers(t *testing.T) {
	f := newFixture(t, tsauth.Peer{Node: "agent", Tags: []string{"tag:agent-host"}})
	if code, _ := f.do(t, "GET", "/api/events", "", nil); code != http.StatusForbidden {
		t.Fatalf("a tagged node subscribed to events: %d", code)
	}
}
