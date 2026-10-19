package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zaneGittins/skybreaker/internal/control/config"
	"github.com/zaneGittins/skybreaker/internal/control/hooks"
	"github.com/zaneGittins/skybreaker/internal/control/sandbox"
	"github.com/zaneGittins/skybreaker/internal/control/store"
	"github.com/zaneGittins/skybreaker/internal/notify"
)

// fakeDocker answers docker CLI calls the way the real one would for a
// single host, and records every command.
type fakeDocker struct {
	mu       sync.Mutex
	calls    [][]string
	running  map[string]bool            // container name → running
	networks map[string]string          // name → subnet
	tmux     map[string]string          // container → list-panes output
	sessions map[string]map[string]bool // container → tmux sessions created since it started
	execOut  func(args []string) (string, error)
	blockRun func(container string) bool // docker run for these waits on release
	release  chan struct{}
	imageID  string // what `docker image inspect` reports; containers run "sha256:old"
}

func newFakeDocker() *fakeDocker {
	return &fakeDocker{running: map[string]bool{}, networks: map[string]string{}, tmux: map[string]string{}, sessions: map[string]map[string]bool{}}
}

func (f *fakeDocker) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if ctx.Err() != nil {
		// Like exec.CommandContext: a cancelled context kills the command.
		return nil, ctx.Err()
	}
	f.mu.Lock()
	if len(args) > 2 && args[0] == "run" && f.blockRun != nil {
		for i, a := range args {
			if a == "--name" && f.blockRun(args[i+1]) {
				rel := f.release
				f.mu.Unlock()
				<-rel
				f.mu.Lock()
			}
		}
	}
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string{name}, args...))
	switch {
	case len(args) >= 2 && args[0] == "network" && args[1] == "inspect":
		n := args[len(args)-1]
		sn, ok := f.networks[n]
		if !ok {
			return nil, errors.New("Error: No such network: " + n)
		}
		return []byte(sn + " "), nil
	case len(args) >= 2 && args[0] == "network" && args[1] == "create":
		for i, a := range args {
			if a == "--subnet" {
				f.networks[args[len(args)-1]] = args[i+1]
			}
		}
	case len(args) >= 2 && args[0] == "container" && args[1] == "inspect":
		n := args[len(args)-1]
		r, ok := f.running[n]
		if !ok {
			return nil, errors.New("Error: No such container: " + n)
		}
		if slices.Contains(args, "{{.Image}}") {
			return []byte("sha256:old\n"), nil
		}
		return []byte(fmt.Sprintf("abc123 %v", r)), nil
	case len(args) >= 2 && args[0] == "image" && args[1] == "inspect":
		if f.imageID == "" {
			return []byte("sha256:old\n"), nil
		}
		return []byte(f.imageID + "\n"), nil
	case args[0] == "run":
		for i, a := range args {
			if a == "--name" {
				f.running[args[i+1]] = true
				f.sessions[args[i+1]] = map[string]bool{}
			}
		}
	case args[0] == "stop":
		f.running[args[len(args)-1]] = false
	case args[0] == "rm":
		delete(f.running, args[len(args)-1])
	case args[0] == "exec":
		container := args[1]
		rest := args[2:]
		if !f.running[container] {
			return nil, errors.New("Error: container " + container + " is not running")
		}
		if f.execOut != nil {
			out, err := f.execOut(rest)
			if out != "" || err != nil {
				return []byte(out), err
			}
		}
		if len(rest) > 1 && rest[0] == "tmux" && rest[1] == "list-panes" {
			return []byte(f.tmux[container]), nil
		}
		if len(rest) > 4 && rest[0] == "tmux" && rest[1] == "new-session" {
			f.sessions[container][rest[4]] = true
		}
		if len(rest) > 3 && rest[0] == "tmux" && rest[1] == "has-session" && !f.sessions[container][strings.TrimPrefix(rest[3], "=")] {
			return nil, errors.New("exit status 1: can't find session")
		}
	}
	return nil, nil
}

// set changes the fake's scripted behaviour under its lock: background
// work (the workspace clone) runs Exec concurrently with the test.
func (f *fakeDocker) set(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

func (f *fakeDocker) find(pred func([]string) bool) [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out [][]string
	for _, c := range f.calls {
		if pred(c) {
			out = append(out, c)
		}
	}
	return out
}

func hasSeq(c []string, seq ...string) bool {
	for i := 0; i+len(seq) <= len(c); i++ {
		if slices.Equal(c[i:i+len(seq)], seq) {
			return true
		}
	}
	return false
}

type fakeBroker struct {
	mu              sync.Mutex
	minted          []string
	revoked         []string
	revokedProjects []string
	n               int
}

func (b *fakeBroker) Mint(_ context.Context, project string, ttl time.Duration) (Minted, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.n++
	id := fmt.Sprintf("tok%d", b.n)
	b.minted = append(b.minted, project)
	return Minted{ID: id, Token: "secret-" + id, Expires: time.Now().Add(ttl)}, nil
}

func (b *fakeBroker) Revoke(_ context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.revoked = append(b.revoked, id)
	return nil
}

func (b *fakeBroker) RevokeProject(_ context.Context, project string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.revokedProjects = append(b.revokedProjects, project)
	return nil
}

func (b *fakeBroker) CA(context.Context) ([]byte, error) {
	return []byte("-----BEGIN CERTIFICATE-----\nCA\n-----END CERTIFICATE-----\n"), nil
}

type recNotify struct {
	mu   sync.Mutex
	msgs []notify.Message
}

func (r *recNotify) Notify(_ context.Context, m notify.Message) error {
	r.mu.Lock()
	r.msgs = append(r.msgs, m)
	r.mu.Unlock()
	return nil
}

type fixture struct {
	app    *App
	docker *fakeDocker
	broker *fakeBroker
	notes  *recNotify
	dir    string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	cfgJSON := fmt.Sprintf(`{
	  "data_dir": %q,
	  "users": ["zane@example.com"],
	  "sandbox": {"image": "skybreaker-sandbox:test"},
	  "projects": {
	    "orbit": {"repo": "zaneGittins/orbit", "harnesses": ["claude", "codex"], "env": {"ANTHROPIC_API_KEY": "skybreaker-injected"}},
	    "bifract": {"repo": "zaneGittins/bifract"}
	  }
	}`, dir)
	p := filepath.Join(dir, "skybreaker.json")
	os.WriteFile(p, []byte(cfgJSON), 0o600)
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := store.Open("")
	f := &fixture{docker: newFakeDocker(), broker: &fakeBroker{}, notes: &recNotify{}, dir: dir}
	f.app, err = New(Deps{
		Config: cfg, Store: st, Sandbox: &sandbox.Manager{R: f.docker}, Broker: f.broker,
		HookKey: []byte("0123456789abcdef0123456789abcdef"), Notify: f.notes, UIURL: "https://skybreaker.example.ts.net",
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestStartSandbox(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if err := f.app.StartSandbox(ctx, "orbit"); err != nil {
		t.Fatal(err)
	}
	pdir := filepath.Join(f.dir, "projects", "orbit")
	ca, err := os.ReadFile(filepath.Join(pdir, "etc", "ca.crt"))
	if err != nil || !strings.Contains(string(ca), "BEGIN CERTIFICATE") {
		t.Fatalf("CA not written: %v", err)
	}
	settings, _ := os.ReadFile(filepath.Join(pdir, "etc", "claude-managed-settings.json"))
	hookURL := f.app.cfg.HookURL("orbit", f.app.HookToken("orbit"))
	if !strings.Contains(string(settings), hookURL) {
		t.Fatalf("managed settings lack the hook URL %s:\n%s", hookURL, settings)
	}
	var mcp struct {
		MCPServers map[string]struct {
			Type    string            `json:"type"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	b, _ := os.ReadFile(filepath.Join(pdir, "etc", "mcp.json"))
	if err := json.Unmarshal(b, &mcp); err != nil || mcp.MCPServers["skybreaker"].URL != "http://broker:7701/mcp/orbit/"+f.app.HookToken("orbit")+"/-" {
		t.Fatalf("MCP config: %s %v", b, err)
	}
	runs := f.docker.find(func(c []string) bool { return len(c) > 1 && c[1] == "run" })
	if len(runs) != 1 || !hasSeq(runs[0], "--runtime", "runsc") || !slices.Contains(runs[0], "ANTHROPIC_API_KEY=skybreaker-injected") ||
		!slices.Contains(runs[0], "HTTPS_PROXY=http://orbit:secret-tok1@broker:8080") {
		t.Fatalf("docker run = %v", runs)
	}
	if ref, ok := f.app.store.Token("orbit"); !ok || ref.ID != "tok1" {
		t.Fatalf("token not recorded: %+v", ref)
	}
	if len(f.docker.find(func(c []string) bool { return hasSeq(c, "tmux", "set-option", "-g", "window-size", "latest") })) == 0 {
		t.Error("tmux window-size latest not set")
	}
	// Starting again while running does not mint a second token.
	if err := f.app.StartSandbox(ctx, "orbit"); err != nil {
		t.Fatal(err)
	}
	if len(f.broker.minted) != 1 {
		t.Errorf("minted %d tokens for a running sandbox", len(f.broker.minted))
	}
}

func TestStopRevokesEveryTokenOfTheProject(t *testing.T) {
	// A token whose ref was lost (a crash between mint and save, a reset
	// state file) must still die with the sandbox.
	f := newFixture(t)
	ctx := context.Background()
	f.app.StartSandbox(ctx, "orbit")
	if !slices.Equal(f.broker.revokedProjects, []string{"orbit"}) {
		t.Fatalf("start did not clear earlier tokens first: %v", f.broker.revokedProjects)
	}
	f.broker.revokedProjects = nil
	f.app.store.DeleteToken("orbit")
	if err := f.app.StopSandbox(ctx, "orbit"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.broker.revokedProjects, []string{"orbit"}) {
		t.Fatalf("revoked projects %v, want [orbit]", f.broker.revokedProjects)
	}
}

func TestMintedTokenRevokedWhenItsRefIsNotSaved(t *testing.T) {
	f := newFixture(t)
	dir := filepath.Join(t.TempDir(), "state")
	st, err := store.Open(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	st.CreateSession(store.Session{ID: "s0", Project: "bifract"}) // creates dir
	os.Chmod(dir, 0o500)
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	f.app.store = st
	if err := f.app.StartSandbox(context.Background(), "orbit"); err == nil {
		t.Fatal("start succeeded although the token ref could not be saved")
	}
	if !slices.Contains(f.broker.revoked, "tok1") {
		t.Fatalf("the unrecorded token was left live: revoked %v", f.broker.revoked)
	}
}

func TestRestartRevokesOldToken(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.app.StartSandbox(ctx, "orbit")
	if err := f.app.StopSandbox(ctx, "orbit"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.broker.revokedProjects, "orbit") {
		t.Fatalf("stop did not revoke the token: %v", f.broker.revokedProjects)
	}
	if _, ok := f.app.store.Token("orbit"); ok {
		t.Error("token ref kept after stop")
	}
	if err := f.app.StartSandbox(ctx, "orbit"); err != nil {
		t.Fatal(err)
	}
	if ref, _ := f.app.store.Token("orbit"); ref.ID != "tok2" {
		t.Errorf("restart token = %s, want tok2", ref.ID)
	}
}

func TestUnknownProject(t *testing.T) {
	f := newFixture(t)
	if err := f.app.StartSandbox(context.Background(), "ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestHookToken(t *testing.T) {
	f := newFixture(t)
	a, b := f.app.HookToken("orbit"), f.app.HookToken("bifract")
	if a == b || len(a) < 32 {
		t.Fatalf("tokens %q %q", a, b)
	}
	if !f.app.CheckHookToken("orbit", a) || f.app.CheckHookToken("orbit", b) || f.app.CheckHookToken("ghost", a) || f.app.CheckHookToken("orbit", "") {
		t.Fatal("CheckHookToken wrong")
	}
}

func TestSessions(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s, err := f.app.NewSession(ctx, "orbit", "claude", "fix login")
	if err != nil {
		t.Fatal(err)
	}
	if s.Project != "orbit" || s.Harness != "claude" || s.Status != store.StatusRunning {
		t.Fatalf("session = %+v", s)
	}
	news := f.docker.find(func(c []string) bool { return hasSeq(c, "tmux", "new-session") && hasSeq(c, "-s", s.ID) })
	if len(news) != 1 || !hasSeq(news[0], "-s", s.ID) || !slices.Contains(news[0], "SKYBREAKER_SESSION="+s.ID) || !hasSeq(news[0], "--", "claude") {
		t.Fatalf("tmux new-session = %v", news)
	}
	if _, err := f.app.NewSession(ctx, "orbit", "pi", ""); err == nil {
		t.Error("harness not enabled for the project was started")
	}
	if _, err := f.app.NewSession(ctx, "bifract", "codex", ""); err == nil {
		t.Error("codex started on bifract, which only enables claude")
	}

	// tmux reports only this session: it stays running. Others are reconciled.
	f.docker.set(func() { f.docker.tmux["skybreaker-orbit"] = "0::" + s.ID + "\n" })
	list, err := f.app.Sessions(ctx, "orbit")
	if err != nil || len(list) != 1 || list[0].Status != store.StatusRunning {
		t.Fatalf("Sessions = %+v %v", list, err)
	}
	f.docker.set(func() { f.docker.tmux["skybreaker-orbit"] = "" })
	list, _ = f.app.Sessions(ctx, "orbit")
	if list[0].Status != store.StatusExited || list[0].Ended == nil || list[0].Exit != nil || list[0].Screen {
		t.Fatalf("vanished session not marked exited: %+v", list[0])
	}

	argv, err := f.app.AttachArgv(s.ID)
	if err != nil || !hasSeq(argv, "exec", "-it") || !hasSeq(argv, "tmux", "-u", "attach-session", "-t", "="+s.ID) {
		t.Fatalf("AttachArgv = %v %v", argv, err)
	}
	if _, err := f.app.AttachArgv("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("attach unknown: %v", err)
	}
}

// A harness that exits keeps its last screen (remain-on-exit), so the user
// can read why, e.g. "Unable to connect to Anthropic services".
func TestExitedSessionKeepsItsScreen(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s, _ := f.app.NewSession(ctx, "orbit", "claude", "")
	// A second, live window keeps the session running.
	f.docker.set(func() { f.docker.tmux["skybreaker-orbit"] = "1:3:" + s.ID + "\n0::" + s.ID + "\n" })
	if list, _ := f.app.Sessions(ctx, "orbit"); list[0].Status != store.StatusRunning {
		t.Fatalf("session with a live pane: %+v", list[0])
	}
	f.docker.set(func() { f.docker.tmux["skybreaker-orbit"] = "1:3:" + s.ID + "\n1:0:" + s.ID + "\n" })
	list, err := f.app.Sessions(ctx, "orbit")
	if err != nil {
		t.Fatal(err)
	}
	if got := list[0]; got.Status != store.StatusExited || got.Exit == nil || *got.Exit != 3 || !got.Screen || got.Ended == nil {
		t.Fatalf("exited session = %+v", got)
	}
	kills := func() int {
		return len(f.docker.find(func(c []string) bool { return hasSeq(c, "tmux", "kill-session", "-t", "="+s.ID) }))
	}
	if kills() != 0 {
		t.Fatal("the screen of a just-exited session was closed")
	}
	// Closing it removes the screen but keeps how it ended.
	if err := f.app.KillSession(ctx, s.ID); err != nil || kills() != 1 {
		t.Fatalf("close: %v, %d kills", err, kills())
	}
	if got, _ := f.app.store.Session(s.ID); got.Status != store.StatusExited || *got.Exit != 3 {
		t.Fatalf("after close: %+v", got)
	}
	f.docker.set(func() { f.docker.tmux["skybreaker-orbit"] = "" })
	if list, _ := f.app.Sessions(ctx, "orbit"); list[0].Screen || *list[0].Exit != 3 {
		t.Fatalf("after the screen went: %+v", list[0])
	}
}

func TestOldScreensAreClosed(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	var ids []string
	for range keepScreens + 2 {
		s, err := f.app.NewSession(ctx, "orbit", "claude", "")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, s.ID)
		f.app.store.UpdateSession(s.ID, func(x *store.Session) { x.Created = time.Unix(int64(len(ids)), 0) })
	}
	var panes string
	for _, id := range ids {
		panes += "1:0:" + id + "\n"
	}
	// A dead session the store doesn't know goes too; live ones are never touched.
	panes += "1:0:stray\n0::mine\n0::skybreaker-runs\n"
	f.docker.set(func() { f.docker.tmux["skybreaker-orbit"] = panes })
	if _, err := f.app.Sessions(ctx, "orbit"); err != nil {
		t.Fatal(err)
	}
	var killed []string
	for _, c := range f.docker.find(func(c []string) bool { return hasSeq(c, "tmux", "kill-session") }) {
		killed = append(killed, strings.TrimPrefix(c[len(c)-1], "="))
	}
	slices.Sort(killed)
	want := []string{ids[0], ids[1], "stray"}
	slices.Sort(want)
	if !slices.Equal(killed, want) {
		t.Fatalf("killed %v, want the two oldest screens and the stray: %v", killed, want)
	}
}

// tmux exits with its last session, losing its options; a new session must
// not start a bare server where an exiting harness vanishes.
func TestNewSessionGivesClaudeTheOrbitMCP(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	withBoard, err := f.app.NewSession(ctx, "orbit", "claude", "")
	if err != nil {
		t.Fatal(err)
	}
	without, err := f.app.NewSession(ctx, "bifract", "claude", "")
	if err != nil {
		t.Fatal(err)
	}
	started := func(id string) []string {
		c := f.docker.find(func(c []string) bool { return hasSeq(c, "new-session", "-d", "-s", id) })
		if len(c) != 1 {
			t.Fatalf("new-session for %s: %v", id, c)
		}
		return c[0]
	}
	// Every project has a board, so every terminal gets the MCP config.
	for _, id := range []string{withBoard.ID, without.ID} {
		if !hasSeq(started(id), "claude", "--mcp-config", MCPPath) {
			t.Errorf("session %s: %q", id, started(id))
		}
	}
}

func TestNewSessionRestoresTmuxServer(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if err := f.app.StartSandbox(ctx, "orbit"); err != nil {
		t.Fatal(err)
	}
	f.docker.set(func() {
		f.docker.calls = nil
		f.docker.execOut = func(args []string) (string, error) {
			if hasSeq(args, "has-session", "-t", "="+RunsSession) {
				return "", errors.New("exit status 1: no server running on /tmp/tmux-0/default")
			}
			return "", nil
		}
	})
	s, err := f.app.NewSession(ctx, "orbit", "claude", "")
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, c := range f.docker.find(func(c []string) bool { return hasSeq(c, "tmux") }) {
		switch {
		case hasSeq(c, "new-session", "-d", "-s", RunsSession):
			order = append(order, "runs")
		case hasSeq(c, "remain-on-exit", "on"):
			order = append(order, "remain")
		case hasSeq(c, "new-session", "-d", "-s", s.ID):
			order = append(order, "session")
		}
	}
	if !slices.Equal(order, []string{"runs", "remain", "session"}) {
		t.Fatalf("order %v", order)
	}
}

func TestKillSession(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s, _ := f.app.NewSession(ctx, "orbit", "claude", "")
	if err := f.app.KillSession(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	if len(f.docker.find(func(c []string) bool { return hasSeq(c, "tmux", "kill-session", "-t", "="+s.ID) })) != 1 {
		t.Fatal("tmux kill-session not run")
	}
	got, _ := f.app.store.Session(s.ID)
	if got.Status != store.StatusKilled {
		t.Fatalf("status %q", got.Status)
	}
}

func TestDiffIsSafe(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.app.StartSandbox(ctx, "orbit")
	f.docker.set(func() {
		f.docker.execOut = func(args []string) (string, error) {
			if len(args) > 0 && args[0] == "git" {
				return "diff --git a/x b/x\n", nil
			}
			return "", nil
		}
	})
	out, err := f.app.Diff(ctx, "orbit")
	if err != nil || !strings.Contains(out, "diff --git") {
		t.Fatalf("Diff = %q %v", out, err)
	}
	gits := f.docker.find(func(c []string) bool { return slices.Contains(c, "git") })
	for _, g := range gits {
		// Repo config can name programs (fsmonitor, hooks, diff drivers);
		// none may run.
		for _, want := range []string{"core.fsmonitor=false", "core.hooksPath=/dev/null", "--no-ext-diff", "--no-textconv"} {
			if !slices.Contains(g, want) {
				t.Errorf("git call %v lacks %s", g, want)
			}
		}
	}
	if len(gits) == 0 {
		t.Fatal("no git call")
	}
}

func TestHookEventsNotifyAndPublish(t *testing.T) {
	f := newFixture(t)
	ch, cancel := f.app.Subscribe()
	defer cancel()
	f.app.HookEvent(hooks.Event{Kind: hooks.KindPermission, Project: "orbit", Session: "s1", Tool: "Bash", ApprovalID: "a1", Input: json.RawMessage(`{"command":"cat ~/.ssh/id_rsa"}`)})
	select {
	case ev := <-ch:
		if ev.Type != "hook" || ev.Project != "orbit" {
			t.Fatalf("event %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no event published")
	}
	// Notifications go out in the background.
	for i := 0; i < 100; i++ {
		f.notes.mu.Lock()
		n := len(f.notes.msgs)
		f.notes.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.notes.mu.Lock()
	defer f.notes.mu.Unlock()
	if len(f.notes.msgs) != 1 {
		t.Fatalf("notifications: %+v", f.notes.msgs)
	}
	m := f.notes.msgs[0]
	// Content-free: no command, tool input or session text in the push.
	for _, s := range []string{m.Title, m.Body, m.Click} {
		if strings.Contains(s, "ssh") || strings.Contains(s, "Bash") {
			t.Errorf("notification leaks content: %+v", m)
		}
	}
	if m.ID == "" || !strings.HasPrefix(m.Click, "https://skybreaker.example.ts.net") {
		t.Errorf("notification = %+v", m)
	}
}

func TestStartClonesRepo(t *testing.T) {
	f := newFixture(t)
	ch, cancel := f.app.Subscribe()
	defer cancel()
	if err := f.app.StartSandbox(context.Background(), "orbit"); err != nil {
		t.Fatal(err)
	}
	// The clone runs in the background and reports when done.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Type == "sandbox" && ev.Data == "workspace ready" {
				clones := f.docker.find(func(c []string) bool { return slices.Contains(c, "https://github.com/zaneGittins/orbit.git") })
				if len(clones) != 1 || !slices.ContainsFunc(clones[0], func(a string) bool { return strings.Contains(a, "test -d /workspace/.git") }) {
					t.Fatalf("clone call = %v", clones)
				}
				return
			}
		case <-deadline:
			t.Fatal("no workspace-ready event")
		}
	}
}

func TestSnapshotTarget(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s, _ := f.app.NewSession(ctx, "orbit", "claude", "")
	f.app.Snapshot(ctx, s.ID, 10)
	// tmux 3.4 needs "=name:" for a pane target; "=name" alone fails.
	if len(f.docker.find(func(c []string) bool { return hasSeq(c, "capture-pane") && slices.Contains(c, "="+s.ID+":") })) != 1 {
		t.Fatalf("capture-pane target wrong: %v", f.docker.find(func(c []string) bool { return hasSeq(c, "capture-pane") }))
	}
}

func TestHistory(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s, _ := f.app.NewSession(ctx, "orbit", "claude", "")
	reply := ""
	f.docker.set(func() {
		f.docker.execOut = func(args []string) (string, error) {
			if hasSeq(args, "#{history_size}") {
				if !slices.Contains(args, "="+s.ID+":") {
					t.Errorf("history target: %q", args)
				}
				return reply, nil
			}
			return "", nil
		}
	})
	cases := []struct {
		name, reply string
		since       int
		want        History
	}{
		{"first load", "4\na\nb\nc\nd\n", -1, History{Size: 4, Replace: true, Text: "a\nb\nc\nd"}},
		{"two new rows", "4\na\nb\nc\nd\n", 2, History{Size: 4, Text: "c\nd"}},
		{"nothing new", "4\na\nb\nc\nd\n", 4, History{Size: 4}},
		{"shrank (a resize pulled rows back)", "3\na\nb\nc\n", 4, History{Size: 3, Replace: true, Text: "a\nb\nc"}},
		{"more new than fetched", "9000\nx\ny\n", 1000, History{Size: 9000, Replace: true, Text: "x\ny"}},
		{"at the history limit, rows roll", "50000\nx\ny\n", 49999, History{Size: 50000, Replace: true, Text: "x\ny"}},
		{"empty", "0\n", -1, History{Replace: true}},
	}
	for _, c := range cases {
		reply = c.reply
		got, err := f.app.History(ctx, s.ID, c.since)
		if err != nil || got != c.want {
			t.Errorf("%s: got %+v, %v; want %+v", c.name, got, err, c.want)
		}
	}
	if _, err := f.app.History(ctx, "nope", 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown session: %v", err)
	}
}

func TestNoTmuxServer(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s, _ := f.app.NewSession(ctx, "orbit", "claude", "")
	gone := errors.New("error connecting to /tmp/tmux-0/default (No such file or directory)")
	f.docker.set(func() {
		f.docker.execOut = func(args []string) (string, error) {
			if len(args) > 1 && args[0] == "tmux" && (args[1] == "list-sessions" || args[1] == "kill-session") {
				return "", gone
			}
			return "", nil
		}
	})
	list, err := f.app.Sessions(ctx, "orbit")
	if err != nil || list[0].Status != store.StatusExited {
		t.Fatalf("Sessions with no tmux server = %+v, %v", list, err)
	}
	s2, _ := f.app.NewSession(ctx, "orbit", "claude", "")
	if err := f.app.KillSession(ctx, s2.ID); err != nil {
		t.Fatalf("KillSession with no tmux server: %v", err)
	}
	_ = s
}

func TestNotificationsAreBucketed(t *testing.T) {
	// A flood of permission requests must collapse to one push per project
	// per minute once deduplicated, so a hostile agent can't spam the phone.
	f := newFixture(t)
	now := time.Now()
	ids := map[string]bool{}
	for i := range 20 {
		ids[f.app.notificationFor(hooks.Event{Kind: hooks.KindPermission, Project: "orbit", ApprovalID: fmt.Sprint(i), Time: now}).ID] = true
		ids[f.app.notificationFor(hooks.Event{Kind: hooks.KindStop, Project: "orbit", Session: fmt.Sprint(i), Time: now}).ID] = true
	}
	if len(ids) != 2 {
		t.Fatalf("%d distinct notification IDs for one project in one minute, want 2", len(ids))
	}
}

func TestRenewTokens(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	now := t0
	f.app.now = func() time.Time { return now }
	if err := f.app.StartSandbox(ctx, "orbit"); err != nil {
		t.Fatal(err)
	}
	ch, cancel := f.app.Subscribe()
	defer cancel()

	// Plenty of time left: nothing happens.
	now = t0.Add(20 * 24 * time.Hour)
	f.app.RenewTokens(ctx)
	if len(f.broker.minted) != 1 {
		t.Fatalf("renewed with 10 days left: minted %d", len(f.broker.minted))
	}

	// Inside the margin with a live session: warn, but don't kill it yet.
	s, _ := f.app.NewSession(ctx, "orbit", "claude", "")
	f.docker.set(func() { f.docker.tmux["skybreaker-orbit"] = "0::" + s.ID + "\n" })
	now = t0.Add(SandboxTokenTTL - 2*24*time.Hour)
	f.app.RenewTokens(ctx)
	if len(f.broker.minted) != 1 {
		t.Fatalf("restarted a sandbox with a live session and 2 days left")
	}
	warned := false
	for len(ch) > 0 {
		if ev := <-ch; ev.Type == "sandbox" && strings.Contains(fmt.Sprint(ev.Data), "token expires") {
			warned = true
		}
	}
	if !warned {
		t.Error("no warning event before the token expires")
	}

	// Last day: restart even with live sessions, minting a fresh token.
	now = t0.Add(SandboxTokenTTL - 12*time.Hour)
	f.app.RenewTokens(ctx)
	if len(f.broker.minted) != 2 || !slices.Contains(f.broker.revokedProjects, "orbit") {
		t.Fatalf("minted %v revoked %v; want a fresh token and the old one revoked", f.broker.minted, f.broker.revokedProjects)
	}
	if ref, _ := f.app.store.Token("orbit"); ref.ID != "tok2" || ref.Expires.IsZero() {
		t.Fatalf("token ref after renewal = %+v", ref)
	}
}

func TestStartsDontBlockOtherProjects(t *testing.T) {
	// A slow image pull for one project must not hold up another.
	f := newFixture(t)
	release := make(chan struct{})
	f.docker.set(func() {
		f.docker.execOut = nil
		f.docker.blockRun = func(name string) bool { return name == "skybreaker-orbit" }
		f.docker.release = release
	})
	orbitDone := make(chan error, 1)
	go func() { orbitDone <- f.app.StartSandbox(context.Background(), "orbit") }()
	time.Sleep(50 * time.Millisecond)
	bifract := make(chan error, 1)
	go func() { bifract <- f.app.StartSandbox(context.Background(), "bifract") }()
	select {
	case err := <-bifract:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("starting bifract waited for orbit's docker run")
	}
	close(release)
	if err := <-orbitDone; err != nil {
		t.Fatal(err)
	}
}

func TestStartSurvivesCancelledRequest(t *testing.T) {
	// The browser closing its request must not kill docker run halfway.
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.app.StartSandbox(ctx, "orbit"); err != nil {
		t.Fatalf("start with a cancelled request context: %v", err)
	}
	if !f.docker.running["skybreaker-orbit"] {
		t.Fatal("sandbox not started")
	}
}

func TestCloneWaitsForCA(t *testing.T) {
	// git checks TLS against the system bundle, which only holds the broker
	// CA once the entrypoint has run; the clone must wait for its marker.
	f := newFixture(t)
	f.app.readyPoll = 5 * time.Millisecond
	var order []string
	notYet := 3
	f.docker.set(func() {
		f.docker.execOut = func(args []string) (string, error) {
			switch {
			case slices.Contains(args, readyMarker):
				if notYet > 0 {
					notYet--
					return "", errors.New("exit status 1")
				}
				order = append(order, "ready")
			case slices.Contains(args, "https://github.com/zaneGittins/orbit.git"):
				order = append(order, "clone")
			}
			return "", nil
		}
	})
	ch, cancel := f.app.Subscribe()
	defer cancel()
	if err := f.app.StartSandbox(context.Background(), "orbit"); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Data == "workspace ready" {
				f.docker.mu.Lock()
				defer f.docker.mu.Unlock()
				if !slices.Equal(order, []string{"ready", "clone"}) {
					t.Fatalf("order %v, want ready then clone", order)
				}
				return
			}
		case <-deadline:
			t.Fatal("clone never ran")
		}
	}
}

// A diff the runner cut off at its output limit is shown truncated, not
// as a failure.
func TestDiffOverTheOutputLimit(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.app.StartSandbox(ctx, "orbit")
	big := "diff --git a/x b/x\n" + strings.Repeat("+line\n", 1<<20)
	f.docker.set(func() {
		f.docker.execOut = func(args []string) (string, error) {
			if len(args) > 0 && args[0] == "git" {
				return big, fmt.Errorf("docker exec: %w", sandbox.ErrOutputLimit)
			}
			return "", nil
		}
	})
	out, err := f.app.Diff(ctx, "orbit")
	if err != nil || !strings.HasSuffix(out, "… diff truncated") || len(out) > 3<<20 {
		t.Fatalf("Diff: %d bytes, %v", len(out), err)
	}
}
