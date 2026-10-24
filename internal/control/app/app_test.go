package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
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
	imageID  string   // what `docker image inspect` reports; containers run "sha256:old"
	inputs   [][]byte // what each command run with input was given
}

func newFakeDocker() *fakeDocker {
	return &fakeDocker{running: map[string]bool{}, networks: map[string]string{}, tmux: map[string]string{}, sessions: map[string]map[string]bool{}}
}

func (f *fakeDocker) RunInput(ctx context.Context, stdin io.Reader, _ int64, name string, args ...string) ([]byte, error) {
	b, _ := io.ReadAll(stdin)
	f.mu.Lock()
	f.inputs = append(f.inputs, b)
	f.mu.Unlock()
	// docker exec -i reads like docker exec.
	if len(args) > 1 && args[0] == "exec" && args[1] == "-i" {
		args = append([]string{"exec"}, args[2:]...)
	}
	return f.Run(ctx, name, args...)
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
	return newFixtureWith(t, `{
	  "data_dir": %q,
	  "users": ["zane@example.com"],
	  "sandbox": {"image": "skybreaker-sandbox:test"},
	  "projects": {
	    "orbit": {"repo": "zaneGittins/orbit", "harnesses": ["claude", "codex"], "env": {"ANTHROPIC_API_KEY": "skybreaker-injected"}},
	    "bifract": {"repo": "zaneGittins/bifract"}
	  }
	}`)
}

// newFixtureWith runs a config with %q for the data directory.
func newFixtureWith(t *testing.T, tmpl string) *fixture {
	t.Helper()
	dir := t.TempDir()
	cfgJSON := fmt.Sprintf(tmpl, dir)
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

// A project that runs pi gets its model servers: models.json in the
// read-only /etc/skybreaker, for skybreaker-pi to merge, and a llama.cpp
// router's address and key in the environment.
func TestStartSandboxSetsUpPi(t *testing.T) {
	f := newFixtureWith(t, `{
	  "data_dir": %q, "users": ["zane@example.com"], "sandbox": {"image": "img"},
	  "projects": {"orbit": {"repo": "a/orbit", "harnesses": ["pi"], "capabilities": ["local"]}},
	  "capabilities": {"local": [
	    {"route": "/ollama", "upstream": "http://192.168.77.50:11434", "allow_private": ["192.168.77.50"], "rules": [{"match": "* /**", "action": "auto"}]},
	    {"route": "/llama", "upstream": "http://127.0.0.1:8081", "allow_private": ["127.0.0.1"], "rules": [{"match": "* /**", "action": "auto"}]}
	  ]},
	  "pi": {"providers": {
	    "ollama": {"route": "/ollama", "api": "openai-completions", "models": ["qwen3-coder:30b"]},
	    "llama": {"route": "/llama", "api": "llama.cpp"}
	  }}
	}`)
	if err := f.app.StartSandbox(context.Background(), "orbit"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(f.dir, "projects", "orbit", "etc", "pi-models.json"))
	if err != nil || !strings.Contains(string(b), `"baseUrl": "http://broker:8080/ollama/v1"`) {
		t.Fatalf("pi-models.json: %s %v", b, err)
	}
	runs := f.docker.find(func(c []string) bool { return len(c) > 1 && c[1] == "run" })
	if len(runs) != 1 || !slices.Contains(runs[0], "LLAMA_BASE_URL=http://broker:8080/llama") || !slices.Contains(runs[0], "LLAMA_API_KEY=secret-tok1") {
		t.Fatalf("docker run = %v", runs)
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
	s, err := f.app.NewSession(ctx, "orbit", "claude", "fix login", NewOptions{})
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
	if _, err := f.app.NewSession(ctx, "orbit", "pi", "", NewOptions{}); err == nil {
		t.Error("harness not enabled for the project was started")
	}
	if _, err := f.app.NewSession(ctx, "bifract", "codex", "", NewOptions{}); err == nil {
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
	s, _ := f.app.NewSession(ctx, "orbit", "claude", "", NewOptions{})
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
		s, err := f.app.NewSession(ctx, "orbit", "claude", "", NewOptions{})
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
	withBoard, err := f.app.NewSession(ctx, "orbit", "claude", "", NewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	without, err := f.app.NewSession(ctx, "bifract", "claude", "", NewOptions{})
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
	s, err := f.app.NewSession(ctx, "orbit", "claude", "", NewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, c := range f.docker.find(func(c []string) bool { return hasSeq(c, "tmux") }) {
		switch {
		case hasSeq(c, "new-session", "-d", "-s", RunsSession):
			order = append(order, "runs")
		case hasSeq(c, "remain-on-exit", "failed"):
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
	s, _ := f.app.NewSession(ctx, "orbit", "claude", "", NewOptions{})
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
				if len(clones) != 1 || !slices.Contains(clones[0], cloneScript) || !slices.Contains(clones[0], workspaceDir) {
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
	s, _ := f.app.NewSession(ctx, "orbit", "claude", "", NewOptions{})
	f.app.Snapshot(ctx, s.ID, 10)
	// tmux 3.4 needs "=name:" for a pane target; "=name" alone fails.
	if len(f.docker.find(func(c []string) bool { return hasSeq(c, "capture-pane") && slices.Contains(c, "="+s.ID+":") })) != 1 {
		t.Fatalf("capture-pane target wrong: %v", f.docker.find(func(c []string) bool { return hasSeq(c, "capture-pane") }))
	}
}

func TestHistory(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s, _ := f.app.NewSession(ctx, "orbit", "claude", "", NewOptions{})
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
	s, _ := f.app.NewSession(ctx, "orbit", "claude", "", NewOptions{})
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
	s2, _ := f.app.NewSession(ctx, "orbit", "claude", "", NewOptions{})
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
	s, _ := f.app.NewSession(ctx, "orbit", "claude", "", NewOptions{})
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

func TestAgentsWorking(t *testing.T) {
	f := newFixture(t)
	if f.app.AgentsWorking() {
		t.Fatal("working with no chats")
	}
	f.app.store.CreateSession(store.Session{ID: "c1", Project: "orbit", Mode: store.ModeChat, Status: store.StatusRunning, State: store.ChatIdle})
	f.app.store.CreateSession(store.Session{ID: "t1", Project: "orbit", Status: store.StatusRunning}) // a terminal survives a restart
	if f.app.AgentsWorking() {
		t.Fatal("an idle chat and a terminal count as working")
	}
	f.app.store.CreateSession(store.Session{ID: "c2", Project: "orbit", Mode: store.ModeChat, Status: store.StatusRunning, State: store.ChatWaiting})
	if !f.app.AgentsWorking() {
		t.Fatal("a chat waiting on you doesn't count")
	}
}

func TestEditSession(t *testing.T) {
	f := newFixture(t)
	f.app.store.CreateSession(store.Session{ID: "c1", Project: "orbit", Mode: store.ModeChat, Status: store.StatusRunning, Title: "fix the login"})
	str := func(s string) *string { return &s }
	s, err := f.app.EditSession("c1", SessionEdit{Title: str("  Login flow  "), Color: str("stormlight"), Goal: str("Ship the login fix with tests")})
	if err != nil || s.Title != "Login flow" || s.Color != "stormlight" || s.Goal != "Ship the login fix with tests" {
		t.Fatalf("edit: %+v %v", s, err)
	}
	// Fields left out stay; empty colour and goal clear them.
	s, err = f.app.EditSession("c1", SessionEdit{Color: str(""), Goal: str("")})
	if err != nil || s.Title != "Login flow" || s.Color != "" || s.Goal != "" {
		t.Fatalf("clear: %+v %v", s, err)
	}
	for name, e := range map[string]SessionEdit{
		"empty title":    {Title: str("   ")},
		"long title":     {Title: str(strings.Repeat("x", 201))},
		"unknown colour": {Color: str("chartreuse")},
		"markup colour":  {Color: str("blue;background:url(x)")},
		"long goal":      {Goal: str(strings.Repeat("x", 501))},
	} {
		if _, err := f.app.EditSession("c1", e); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := f.app.EditSession("nope", SessionEdit{Title: str("x")}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown session: %v", err)
	}
}

func TestSSHKeysReachEverySandbox(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if err := f.app.StartSandbox(ctx, "orbit"); err != nil {
		t.Fatal(err)
	}
	keys := "ssh-ed25519 AAAA skybreaker-1\n"
	if err := f.app.SetAuthorizedKeys([]byte(keys)); err != nil {
		t.Fatal(err)
	}
	read := func(p string) string {
		b, _ := os.ReadFile(filepath.Join(f.dir, "projects", p, "etc", "authorized_keys"))
		return string(b)
	}
	if read("orbit") != keys {
		t.Fatalf("running sandbox has %q", read("orbit"))
	}
	// A sandbox started later gets them too.
	if err := f.app.StartSandbox(ctx, "bifract"); err != nil {
		t.Fatal(err)
	}
	if read("bifract") != keys {
		t.Fatalf("new sandbox has %q", read("bifract"))
	}
	f.app.SetAuthorizedKeys(nil)
	if read("orbit") != "" || read("bifract") != "" {
		t.Fatal("a removed key stayed")
	}
}

func TestSSHConnDialsTheSandbox(t *testing.T) {
	f := newFixture(t)
	var dialed string
	f.app.dial = func(_ context.Context, addr string) (net.Conn, error) {
		dialed = addr
		a, _ := net.Pipe()
		return a, nil
	}
	c, err := f.app.SSHConn(context.Background(), "orbit")
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if want := netip.AddrPortFrom(sandbox.Addr(f.app.cfg.Subnet("orbit")), 22).String(); dialed != want {
		t.Fatalf("dialed %s, want %s", dialed, want)
	}
	if len(f.docker.find(func(c []string) bool { return len(c) > 1 && c[1] == "run" })) != 1 {
		t.Fatal("the sandbox wasn't started")
	}
	if _, err := f.app.SSHConn(context.Background(), "ghost"); err == nil {
		t.Fatal("SSH to a project that doesn't exist")
	}
}

// Every agent's instructions reach each sandbox's etc/: as written for
// Claude Code and pi, and as Codex's managed config. A sandbox started
// later gets them too, and they're rewritten in place, since each file is
// mounted on its own.
func TestInstructions(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if err := f.app.StartSandbox(ctx, "orbit"); err != nil {
		t.Fatal(err)
	}
	etc := filepath.Join(f.dir, "projects", "orbit", "etc")
	before, _ := os.Stat(filepath.Join(etc, sandbox.InstructionsFile))
	if err := f.app.SetInstructions("Say \"hi\" \\ then\n\ttab\x01 and é"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(etc, sandbox.InstructionsFile))
	if string(b) != "Say \"hi\" \\ then\n\ttab\x01 and é" {
		t.Errorf("instructions.md %q", b)
	}
	after, _ := os.Stat(filepath.Join(etc, sandbox.InstructionsFile))
	if before == nil || !os.SameFile(before, after) {
		t.Error("instructions.md was replaced, not rewritten")
	}
	toml, _ := os.ReadFile(filepath.Join(etc, sandbox.CodexConfigFile))
	if !strings.Contains(string(toml), `developer_instructions = "Say \"hi\" \\ then\n\ttab\u0001 and é"`+"\n") {
		t.Errorf("codex config %s", toml)
	}

	if err := f.app.StartSandbox(ctx, "bifract"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(f.dir, "projects", "bifract", "etc", sandbox.InstructionsFile)); !strings.HasPrefix(string(b), "Say") {
		t.Errorf("a later sandbox got %q", b)
	}

	// None leaves both empty.
	f.app.SetInstructions("")
	for _, name := range []string{sandbox.InstructionsFile, sandbox.CodexConfigFile} {
		if b, _ := os.ReadFile(filepath.Join(etc, name)); len(b) != 0 {
			t.Errorf("%s %q", name, b)
		}
	}
}

func TestSetupRunsAfterClone(t *testing.T) {
	// A new container runs the repo's setup script once the workspace is
	// cloned, and a failure says where its output went.
	f := newFixture(t)
	f.app.readyPoll = 5 * time.Millisecond
	var order []string
	f.docker.set(func() {
		f.docker.execOut = func(args []string) (string, error) {
			switch {
			case slices.Contains(args, "https://github.com/zaneGittins/orbit.git"):
				order = append(order, "clone")
			case slices.Contains(args, setupScript):
				order = append(order, "setup")
				return "", errors.New("exit status 1")
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
			if d, _ := ev.Data.(string); strings.HasPrefix(d, "setup failed") {
				if !strings.Contains(d, setupLog) {
					t.Errorf("event %q doesn't say where the output is", ev.Data)
				}
				f.docker.mu.Lock()
				defer f.docker.mu.Unlock()
				if !slices.Equal(order, []string{"clone", "setup"}) {
					t.Fatalf("order %v, want clone then setup", order)
				}
				return
			}
		case <-deadline:
			t.Fatal("setup never ran")
		}
	}
}

func TestAgentsWaitForSetup(t *testing.T) {
	// Chats and runs start once the setup script is done, so the tools it
	// installs are there.
	f := newFixture(t)
	f.app.readyPoll = 5 * time.Millisecond
	release := make(chan struct{})
	f.docker.set(func() {
		f.docker.execOut = func(args []string) (string, error) {
			if slices.Contains(args, setupScript) {
				<-release
				return "ran\n", nil
			}
			return "", nil
		}
	})
	if err := f.app.StartSandbox(context.Background(), "orbit"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := f.app.WaitWorkspace(ctx, "orbit"); err == nil {
		t.Fatal("didn't wait for setup")
	}
	close(release)
	if err := f.app.WaitWorkspace(context.Background(), "orbit"); err != nil {
		t.Fatal(err)
	}
}
