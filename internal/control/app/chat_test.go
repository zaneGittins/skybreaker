package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zaneGittins/skybreaker/internal/control/chat"
	"github.com/zaneGittins/skybreaker/internal/control/mcp"
	"github.com/zaneGittins/skybreaker/internal/control/sandbox"
	"github.com/zaneGittins/skybreaker/internal/control/store"
)

// fakeSpawn plays a Claude Code that answers every message with "ok".
type fakeSpawn struct {
	mu    sync.Mutex
	argvs [][]string
}

func (f *fakeSpawn) spawn(_ context.Context, argv []string) (*chat.Proc, error) {
	f.mu.Lock()
	f.argvs = append(f.argvs, argv)
	f.mu.Unlock()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go func() {
		defer outW.Close()
		sc := bufio.NewScanner(inR)
		for sc.Scan() {
			var m map[string]any
			json.Unmarshal(sc.Bytes(), &m)
			if m["type"] == "user" {
				io.WriteString(outW, `{"type":"system","subtype":"init","session_id":"conv-1","model":"m"}`+"\n")
				io.WriteString(outW, `{"type":"result","subtype":"success","duration_ms":1}`+"\n")
			}
		}
	}()
	return &chat.Proc{Stdin: inW, Stdout: outR, Wait: func() error { return nil }, Kill: func() { inR.Close(); outW.Close() }}, nil
}

func (f *fakeSpawn) last() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.argvs) == 0 {
		return nil
	}
	return f.argvs[len(f.argvs)-1]
}

func chatFixture(t *testing.T) (*fixture, *fakeSpawn) {
	t.Helper()
	f := newFixture(t)
	sp := &fakeSpawn{}
	f.app.spawn = sp.spawn
	f.app.readyPoll = time.Millisecond
	t.Cleanup(f.app.CloseChats)
	return f, sp
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestNewChat(t *testing.T) {
	f, _ := chatFixture(t)
	ctx := context.Background()
	s, err := f.app.NewChat(ctx, "orbit", "claude", NewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Mode != store.ModeChat || s.State != store.ChatIdle || s.PermMode != chat.ModeAsk || s.Status != store.StatusRunning {
		t.Errorf("chat = %+v", s)
	}
	if got := f.docker.find(func(c []string) bool { return hasSeq(c, "tmux", "new-session") && slices.Contains(c, s.ID) }); len(got) != 0 {
		t.Errorf("a chat made a tmux session: %q", got)
	}
	if _, err := f.app.NewChat(ctx, "orbit", "shell", NewOptions{}); err == nil {
		t.Error("shell became a chat")
	}
	if _, err := f.app.NewChat(ctx, "bifract", "codex", NewOptions{}); err == nil {
		t.Error("chat with a harness the project doesn't enable")
	}
	if _, err := f.app.NewChat(ctx, "nope", "claude", NewOptions{}); err == nil {
		t.Error("chat in an unknown project")
	}
	if modes := f.app.ChatModes("codex"); !slices.Equal(modes, []string{chat.ModeAsk, chat.ModeEdits, chat.ModeAuto}) {
		t.Errorf("codex modes = %q", modes)
	}
	if f.app.ChatModes("shell") != nil {
		t.Error("shell has chat modes")
	}
}

func TestChatRunsHarnessInItsSandbox(t *testing.T) {
	f, sp := chatFixture(t)
	ctx := context.Background()
	s, _ := f.app.NewChat(ctx, "orbit", "claude", NewOptions{})
	if err := f.app.ChatSend(ctx, s.ID, "hello"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the turn", func() bool {
		x, _ := f.app.store.Session(s.ID)
		return x.Resume == "conv-1" && x.State == store.ChatIdle
	})
	argv := sp.last()
	for _, want := range [][]string{
		{"docker", "exec", "-i", "-w", "/workspace"}, {"-e", "SKYBREAKER_SESSION=" + s.ID}, {"skybreaker-orbit", "sh", "-c", pidWrapper, s.ID, "claude", "-p"},
		{"--mcp-config", chat.MCPConfig(f.app.MCPURL("orbit", s.ID))}, {"--permission-mode", "default"},
	} {
		if !hasSeq(argv, want...) {
			t.Errorf("argv %q lacks %q", argv, want)
		}
	}
	// The sandbox was started, and ready, before the harness.
	if runs := f.docker.find(func(c []string) bool { return len(c) > 1 && c[1] == "run" }); len(runs) == 0 {
		t.Error("sandbox not started")
	}
	if ready := f.docker.find(func(c []string) bool { return hasSeq(c, "test", "-e", readyMarker) }); len(ready) == 0 {
		t.Error("didn't wait for the sandbox to be ready")
	}
	// Terminal sessions' reconciliation leaves chats alone.
	list, err := f.app.Sessions(ctx, "orbit")
	if err != nil {
		t.Fatal(err)
	}
	if i := slices.IndexFunc(list, func(x store.Session) bool { return x.ID == s.ID }); i < 0 || list[i].Status != store.StatusRunning {
		t.Errorf("sessions = %+v", list)
	}
	ev, _ := f.app.ChatEvents(s.ID, 0)
	if len(ev) == 0 || ev[0].Kind != chat.KindUser {
		t.Errorf("events = %+v", ev)
	}
	if !f.app.IsChat("orbit", s.ID) || f.app.IsChat("bifract", s.ID) || f.app.IsChat("orbit", "nope") {
		t.Error("IsChat is wrong")
	}
}

func TestChatCodexGetsSkybreakerMCP(t *testing.T) {
	f, sp := chatFixture(t)
	s, _ := f.app.NewChat(context.Background(), "orbit", "codex", NewOptions{})
	f.app.ChatSend(context.Background(), s.ID, "hi")
	waitUntil(t, "the spawn", func() bool { return sp.last() != nil })
	if argv := sp.last(); !hasSeq(argv, "codex", "app-server", "-c", "mcp_servers.skybreaker.url="+strconv.Quote(f.app.MCPURL("orbit", s.ID))) {
		t.Errorf("argv = %q", argv)
	}
}

func TestCodexTerminalsGetSkybreakersMCPServer(t *testing.T) {
	f, _ := chatFixture(t)
	ctx := context.Background()
	flag := `mcp_servers.skybreaker.url="` + f.app.MCPURL("orbit", "-") + `"`
	term, err := f.app.NewSession(ctx, "orbit", "codex", "", NewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	starts := f.docker.find(func(c []string) bool { return hasSeq(c, "tmux", "new-session") && slices.Contains(c, term.ID) })
	if len(starts) != 1 || !hasSeq(starts[0], "codex", "-c", flag, "--no-alt-screen") {
		t.Errorf("codex terminal = %q", starts)
	}
	// A Codex chat continued in its terminal.
	s, _ := f.app.NewChat(ctx, "orbit", "codex", NewOptions{})
	f.app.store.UpdateSession(s.ID, func(x *store.Session) { x.Resume = "t1" })
	ct, err := f.app.ChatTerminal(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	starts = f.docker.find(func(c []string) bool { return hasSeq(c, "tmux", "new-session") && slices.Contains(c, ct.ID) })
	if len(starts) != 1 || !hasSeq(starts[0], "codex", "resume", "-c", flag, "t1", "--no-alt-screen") {
		t.Errorf("codex chat's terminal = %q", starts)
	}
}

func TestChatTerminalHandoff(t *testing.T) {
	f, sp := chatFixture(t)
	ctx := context.Background()
	s, _ := f.app.NewChat(ctx, "orbit", "claude", NewOptions{})
	f.app.ChatSend(ctx, s.ID, "hello")
	waitUntil(t, "the turn", func() bool { x, _ := f.app.store.Session(s.ID); return x.Resume != "" && x.State == store.ChatIdle })

	term, err := f.app.ChatTerminal(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if term.Parent != s.ID || term.Mode != "" || f.app.chat.Running(s.ID) {
		t.Errorf("terminal = %+v, chat running %v", term, f.app.chat.Running(s.ID))
	}
	starts := f.docker.find(func(c []string) bool { return hasSeq(c, "tmux", "new-session") && slices.Contains(c, term.ID) })
	if len(starts) != 1 || !hasSeq(starts[0], "claude", "--resume", "conv-1", "--mcp-config", MCPPath) {
		t.Fatalf("terminal start = %q", starts)
	}
	if x, _ := f.app.store.Session(s.ID); x.Terminal != term.ID {
		t.Errorf("chat terminal = %q", x.Terminal)
	}
	// Asking again returns the same terminal.
	again, _ := f.app.ChatTerminal(ctx, s.ID)
	if again.ID != term.ID {
		t.Errorf("second terminal %s, want %s", again.ID, term.ID)
	}
	// Back to the chat: the terminal closes before the harness resumes.
	n := len(sp.argvs)
	f.app.ChatSend(ctx, s.ID, "back")
	waitUntil(t, "the resume", func() bool { sp.mu.Lock(); defer sp.mu.Unlock(); return len(sp.argvs) > n })
	if kills := f.docker.find(func(c []string) bool { return hasSeq(c, "tmux", "kill-session") && slices.Contains(c, "="+term.ID) }); len(kills) == 0 {
		t.Error("terminal not closed")
	}
	if x, _ := f.app.store.Session(term.ID); x.Status != store.StatusKilled {
		t.Errorf("terminal status %q", x.Status)
	}
	if x, _ := f.app.store.Session(s.ID); x.Terminal != "" {
		t.Error("chat still points at its terminal")
	}
	if argv := sp.last(); !hasSeq(argv, "--resume", "conv-1") {
		t.Errorf("chat didn't resume: %q", argv)
	}
}

func TestChatTerminalRefusedWhileWorking(t *testing.T) {
	f, _ := chatFixture(t)
	s, _ := f.app.NewChat(context.Background(), "orbit", "claude", NewOptions{})
	f.app.store.UpdateSession(s.ID, func(x *store.Session) { x.State = store.ChatWorking })
	if _, err := f.app.ChatTerminal(context.Background(), s.ID); err != ErrBusy {
		t.Errorf("err = %v", err)
	}
}

func TestKillChat(t *testing.T) {
	f, _ := chatFixture(t)
	ctx := context.Background()
	s, _ := f.app.NewChat(ctx, "orbit", "claude", NewOptions{})
	f.app.ChatSend(ctx, s.ID, "hello")
	waitUntil(t, "running", func() bool { return f.app.chat.Running(s.ID) })
	if err := f.app.KillSession(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	if f.app.chat.Running(s.ID) {
		t.Error("harness still running")
	}
	x, _ := f.app.store.Session(s.ID)
	if x.Status != store.StatusKilled {
		t.Errorf("status = %q", x.Status)
	}
	if err := f.app.ChatSend(ctx, s.ID, "more"); err == nil {
		t.Error("sent to an ended chat")
	}
	// Its conversation stays readable.
	if ev, err := f.app.ChatEvents(s.ID, 0); err != nil || len(ev) == 0 {
		t.Errorf("events after end = %d, %v", len(ev), err)
	}
}

func TestStopSandboxKeepsChats(t *testing.T) {
	f, _ := chatFixture(t)
	ctx := context.Background()
	s, _ := f.app.NewChat(ctx, "orbit", "claude", NewOptions{})
	f.app.ChatSend(ctx, s.ID, "hello")
	waitUntil(t, "running", func() bool { return f.app.chat.Running(s.ID) })
	if err := f.app.StopSandbox(ctx, "orbit"); err != nil {
		t.Fatal(err)
	}
	if f.app.chat.Running(s.ID) {
		t.Error("harness outlived its sandbox")
	}
	if x, _ := f.app.store.Session(s.ID); x.Status != store.StatusRunning {
		t.Errorf("chat ended with its sandbox: %q", x.Status)
	}
}

func TestChatNotifications(t *testing.T) {
	f, _ := chatFixture(t)
	s := store.Session{ID: "sx", Project: "orbit", Harness: "claude", Mode: store.ModeChat}
	f.app.chatChanged(store.Session{State: store.ChatWorking}, withState(s, store.ChatWaiting))
	f.app.chatChanged(store.Session{State: store.ChatWorking}, withState(s, store.ChatIdle))
	f.app.chatChanged(store.Session{State: store.ChatIdle}, withState(s, store.ChatWorking))
	waitUntil(t, "two notifications", func() bool { f.notes.mu.Lock(); defer f.notes.mu.Unlock(); return len(f.notes.msgs) == 2 })
	f.notes.mu.Lock()
	msgs := slices.Clone(f.notes.msgs)
	f.notes.mu.Unlock()
	titles := []string{msgs[0].Title, msgs[1].Title}
	slices.Sort(titles)
	if !slices.Equal(titles, []string{"Agent finished", "Approval waiting"}) {
		t.Errorf("titles = %q", titles)
	}
	for _, m := range msgs {
		if m.Click != "https://skybreaker.example.ts.net/s/sx" || !strings.Contains(m.Body, "claude session in orbit") {
			t.Errorf("message = %+v", m)
		}
	}
}

func withState(s store.Session, st string) store.Session { s.State = st; return s }

func TestRecoverChats(t *testing.T) {
	f, _ := chatFixture(t)
	s, _ := f.app.NewChat(context.Background(), "orbit", "claude", NewOptions{})
	f.app.store.UpdateSession(s.ID, func(x *store.Session) { x.State = store.ChatWorking })
	f.app.RecoverChats()
	if x, _ := f.app.store.Session(s.ID); x.State != store.ChatIdle {
		t.Errorf("state = %q", x.State)
	}
}

func TestReview(t *testing.T) {
	f, _ := chatFixture(t)
	ctx := context.Background()
	f.app.StartSandbox(ctx, "orbit")
	f.docker.set(func() {
		f.docker.execOut = func(args []string) (string, error) {
			if !slices.Contains(args, "git") {
				return "", nil
			}
			j := strings.Join(args, " ")
			for _, safe := range []string{"core.hooksPath=/dev/null", "core.fsmonitor=false", "diff.external="} {
				if !strings.Contains(j, safe) {
					t.Errorf("git without %s: %s", safe, j)
				}
			}
			switch {
			case strings.Contains(j, "rev-parse --abbrev-ref HEAD"):
				return "feature/x\n", nil
			case strings.Contains(j, "--symbolic-full-name"):
				return "", errExit
			case strings.Contains(j, "merge-base origin/HEAD HEAD"):
				return "abc123\n", nil
			case strings.Contains(j, "rev-parse --abbrev-ref origin/HEAD"):
				return "origin/trunk\n", nil
			case strings.Contains(j, "merge-base"):
				return "", errExit
			case strings.Contains(j, "log --format"):
				return "def456\x1fAdd x\x1f2026-10-18T10:00:00Z\n", nil
			case strings.Contains(j, "--numstat"):
				return "3\t1\tmain.go\n", nil
			case strings.Contains(j, "ls-files"):
				return "notes.txt\x00", nil
			case strings.Contains(j, "--no-index"):
				return "diff --git a/notes.txt b/notes.txt\n+++ b/notes.txt\n+one\n+two\n", errExit
			case strings.Contains(j, " diff "):
				return "diff --git a/main.go b/main.go\n-old\n+new\n", nil
			}
			return "", nil
		}
	})
	r, err := f.app.Review(ctx, "orbit")
	if err != nil {
		t.Fatal(err)
	}
	if r.Branch != "feature/x" || r.Base != "origin/trunk" || r.Clean {
		t.Errorf("review = %+v", r)
	}
	if len(r.Commits) != 1 || r.Commits[0].Subject != "Add x" {
		t.Errorf("commits = %+v", r.Commits)
	}
	if len(r.Files) != 2 || r.Files[0].Path != "main.go" || r.Files[0].Added != 3 || !r.Files[1].New || r.Files[1].Added != 2 {
		t.Errorf("files = %+v", r.Files)
	}
	if !strings.Contains(r.Diff, "+new") || !strings.Contains(r.Diff, "+one") {
		t.Errorf("diff = %q", r.Diff)
	}
}

var errExit = errors.New("exit status 1")

func TestRefreshSandboxesWaitsForIdle(t *testing.T) {
	f, _ := chatFixture(t)
	ctx := context.Background()
	f.app.StartSandbox(ctx, "orbit")
	restarts := func() int {
		return len(f.docker.find(func(c []string) bool { return len(c) > 1 && c[1] == "stop" }))
	}
	// Same image: nothing to do.
	f.app.RefreshSandboxes(ctx)
	if restarts() != 0 {
		t.Fatal("restarted a current sandbox")
	}
	f.docker.set(func() { f.docker.imageID = "sha256:new" })
	// A chat that's working holds the restart off; so does one used just now.
	s, _ := f.app.NewChat(ctx, "orbit", "claude", NewOptions{})
	f.app.store.UpdateSession(s.ID, func(x *store.Session) { x.State = store.ChatWorking; x.Updated = time.Now() })
	f.app.RefreshSandboxes(ctx)
	if restarts() != 0 {
		t.Fatal("restarted under a working chat")
	}
	f.app.store.UpdateSession(s.ID, func(x *store.Session) { x.State = store.ChatIdle; x.Updated = time.Now() })
	f.app.RefreshSandboxes(ctx)
	if restarts() != 0 {
		t.Fatal("restarted under a chat used a moment ago")
	}
	// A live terminal holds it off too.
	f.app.store.UpdateSession(s.ID, func(x *store.Session) { x.Updated = time.Now().Add(-time.Hour) })
	f.docker.set(func() { f.docker.tmux["skybreaker-orbit"] = "0::sterm\n" })
	f.app.store.CreateSession(store.Session{ID: "sterm", Project: "orbit", Harness: "shell", Status: store.StatusRunning, Created: time.Now()})
	f.app.RefreshSandboxes(ctx)
	if restarts() != 0 {
		t.Fatal("restarted under a live terminal")
	}
	f.app.KillSession(ctx, "sterm")
	f.docker.set(func() { f.docker.tmux["skybreaker-orbit"] = "" })
	f.app.RefreshSandboxes(ctx)
	if restarts() != 1 {
		t.Fatalf("idle stale sandbox restarted %d times", restarts())
	}
	// The chat survives the restart.
	if x, _ := f.app.store.Session(s.ID); x.Status != store.StatusRunning {
		t.Errorf("chat %q after the restart", x.Status)
	}
}

func TestMCPCaller(t *testing.T) {
	f, _ := chatFixture(t)
	s, _ := f.app.NewChat(context.Background(), "orbit", "claude", NewOptions{})
	orbit := f.app.cfg.Subnet("orbit").Addr().Next().Next().String() + ":40000"
	bifract := f.app.cfg.Subnet("bifract").Addr().Next().Next().String() + ":40000"
	tok := f.app.HookToken("orbit")
	tryAs := func(remote, path, session string) (mcp.Caller, error) {
		r := httptest.NewRequest("POST", path, nil)
		r.RemoteAddr = remote
		if session != "" {
			r.Header.Set("X-Skybreaker-Session", session)
		}
		return f.app.MCPCaller(r)
	}
	try := func(remote, path string) (mcp.Caller, error) { return tryAs(remote, path, "") }
	if c, err := try(orbit, "/mcp/orbit/"+tok+"/"+s.ID); err != nil || c.Project != "orbit" || c.Session != s.ID {
		t.Fatalf("own chat: %+v %v", c, err)
	}
	if c, err := try(orbit, "/mcp/orbit/"+tok+"/-"); err != nil || c.Session != "-" {
		t.Fatalf("no chat: %+v %v", c, err)
	}
	// pi's MCP config is shared by its sessions, so it names the chat in a
	// header; one in the URL wins.
	if c, err := tryAs(orbit, "/mcp/orbit/"+tok+"/-", s.ID); err != nil || c.Session != s.ID {
		t.Fatalf("chat by header: %+v %v", c, err)
	}
	if c, err := tryAs(orbit, "/mcp/orbit/"+tok+"/-", "-"); err != nil || c.Session != "-" {
		t.Fatalf("no chat by header: %+v %v", c, err)
	}
	if c, err := tryAs(orbit, "/mcp/orbit/"+tok+"/"+s.ID, "sother"); err != nil || c.Session != s.ID {
		t.Fatalf("URL and header: %+v %v", c, err)
	}
	// A pi that outlives its chat still reaches the board.
	if c, err := tryAs(orbit, "/mcp/orbit/"+tok+"/-", "sghost"); err != nil || c.Session != "-" {
		t.Errorf("unknown session by header: %+v %v", c, err)
	}
	for name, c := range map[string][2]string{
		"another project's network": {bifract, "/mcp/orbit/" + tok + "/-"},
		"another project's token":   {bifract, "/mcp/bifract/" + tok + "/-"},
		"from outside":              {"192.168.77.79:5000", "/mcp/orbit/" + tok + "/-"},
		"bad token":                 {orbit, "/mcp/orbit/nope/-"},
		"unknown session":           {orbit, "/mcp/orbit/" + tok + "/sghost"},
		"short path":                {orbit, "/mcp/orbit/" + tok},
	} {
		if got, err := try(c[0], c[1]); err == nil {
			t.Errorf("%s: placed as %+v", name, got)
		}
	}
	// Another project's session can't be named from orbit's sandbox.
	other, _ := f.app.NewChat(context.Background(), "bifract", "claude", NewOptions{})
	if _, err := try(orbit, "/mcp/orbit/"+tok+"/"+other.ID); err == nil {
		t.Error("bifract's chat named from orbit")
	}
	if c, err := tryAs(orbit, "/mcp/orbit/"+tok+"/-", other.ID); err != nil || c.Session != "-" {
		t.Errorf("bifract's chat named from orbit by header: %+v %v", c, err)
	}
}

func TestMCPURLBypassesTheProxy(t *testing.T) {
	// Harnesses send everything not in NO_PROXY through the broker, which
	// refuses Skybreaker's own address; the MCP URL's host must be exempt.
	f, _ := chatFixture(t)
	u, err := url.Parse(f.app.MCPURL("orbit", "s1"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(strings.Split(sandbox.NoProxy, ","), u.Hostname()) {
		t.Fatalf("MCP host %s is not in NO_PROXY %s", u.Hostname(), sandbox.NoProxy)
	}
}

// containerStops counts `docker stop` runs inside orbit's sandbox: the
// agents' own containers, not the sandbox.
func containerStops(f *fixture) [][]string {
	return f.docker.find(func(c []string) bool {
		return len(c) > 4 && c[1] == "exec" && c[3] == "docker" && c[4] == "stop"
	})
}

func innerContainers(f *fixture, ids string) {
	f.docker.set(func() {
		f.docker.execOut = func(args []string) (string, error) {
			if len(args) >= 2 && args[0] == "docker" && args[1] == "ps" {
				return ids, nil
			}
			return "", nil
		}
	})
}

func TestEndingTheLastSessionStopsContainers(t *testing.T) {
	f, _ := chatFixture(t)
	ctx := context.Background()
	f.app.StartSandbox(ctx, "orbit")
	innerContainers(f, "c1\nc2\n")
	a, _ := f.app.NewChat(ctx, "orbit", "claude", NewOptions{})
	b, _ := f.app.NewChat(ctx, "orbit", "claude", NewOptions{})
	// Another session is still live: its stack may be in use.
	f.app.KillSession(ctx, a.ID)
	time.Sleep(50 * time.Millisecond)
	if n := len(containerStops(f)); n != 0 {
		t.Fatalf("stopped containers with a session still live (%d)", n)
	}
	f.app.KillSession(ctx, b.ID)
	waitUntil(t, "the containers stopped", func() bool { return len(containerStops(f)) == 1 })
	if got := containerStops(f)[0]; !slices.Contains(got, "c1") || !slices.Contains(got, "c2") {
		t.Fatalf("stop = %q", got)
	}
}

func TestSweepStopsContainersOnceIdle(t *testing.T) {
	f, _ := chatFixture(t)
	ctx := context.Background()
	f.app.StartSandbox(ctx, "orbit")
	innerContainers(f, "c1\n")
	s, _ := f.app.NewChat(ctx, "orbit", "claude", NewOptions{})
	f.app.store.UpdateSession(s.ID, func(x *store.Session) { x.State = store.ChatIdle; x.Updated = time.Now() })
	f.app.SweepContainers(ctx)
	if len(containerStops(f)) != 0 {
		t.Fatal("stopped containers under a chat used a moment ago")
	}
	// Quiet for longer than idleFor: stop them, once.
	f.app.store.UpdateSession(s.ID, func(x *store.Session) { x.Updated = time.Now().Add(-time.Hour) })
	f.app.SweepContainers(ctx)
	f.app.SweepContainers(ctx)
	if n := len(containerStops(f)); n != 1 {
		t.Fatalf("idle sweep stopped containers %d times, want 1", n)
	}
	// In use again, then quiet again: stopped again.
	f.app.store.UpdateSession(s.ID, func(x *store.Session) { x.Updated = time.Now() })
	f.app.SweepContainers(ctx)
	f.app.store.UpdateSession(s.ID, func(x *store.Session) { x.Updated = time.Now().Add(-time.Hour) })
	f.app.SweepContainers(ctx)
	if n := len(containerStops(f)); n != 2 {
		t.Fatalf("stops after a second quiet spell = %d, want 2", n)
	}
	// Nothing running: nothing to stop.
	innerContainers(f, "")
	f.app.store.UpdateSession(s.ID, func(x *store.Session) { x.Updated = time.Now() })
	f.app.SweepContainers(ctx)
	f.app.store.UpdateSession(s.ID, func(x *store.Session) { x.Updated = time.Now().Add(-time.Hour) })
	f.app.SweepContainers(ctx)
	if n := len(containerStops(f)); n != 2 {
		t.Fatalf("ran docker stop with no containers (%d)", n)
	}
}

func TestChatFork(t *testing.T) {
	f, sp := chatFixture(t)
	ctx := context.Background()
	s, _ := f.app.NewChat(ctx, "orbit", "claude", NewOptions{})
	if _, err := f.app.ChatFork(ctx, s.ID); err == nil {
		t.Error("forked a chat with no conversation yet")
	}
	f.app.ChatSend(ctx, s.ID, "hello")
	waitUntil(t, "the turn", func() bool {
		x, _ := f.app.store.Session(s.ID)
		return x.Resume == "conv-1" && x.State == store.ChatIdle
	})
	f.app.store.UpdateSession(s.ID, func(x *store.Session) {
		x.Title, x.Color, x.Goal, x.PermMode, x.ChosenModel = "Login", "blue", "Ship it", chat.ModeEdits, "sonnet"
	})

	fork, err := f.app.ChatFork(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fork.ID == s.ID || fork.Project != "orbit" || fork.Harness != "claude" || fork.Mode != store.ModeChat || fork.Status != store.StatusRunning ||
		fork.Resume != "conv-1" || !fork.Fork || fork.Title != "Login (fork)" || fork.Color != "blue" || fork.Goal != "Ship it" ||
		fork.PermMode != chat.ModeEdits || fork.ChosenModel != "sonnet" || fork.State != store.ChatIdle {
		t.Errorf("fork = %+v", fork)
	}
	// It starts with the conversation so far, and says where it came from.
	ev, _ := f.app.ChatEvents(fork.ID, 0)
	if len(ev) < 2 || ev[0].Kind != chat.KindUser || ev[0].Text != "hello" || ev[len(ev)-1].Kind != chat.KindNotice || !strings.Contains(ev[len(ev)-1].Text, "Login") {
		t.Errorf("fork events = %+v", ev)
	}
	if pev, _ := f.app.ChatEvents(s.ID, 0); slices.ContainsFunc(pev, func(e chat.Event) bool { return e.Kind == chat.KindNotice }) {
		t.Errorf("the original got the fork's notice: %+v", pev)
	}
	// Its terminal would resume the original, so it waits for the first message.
	if _, err := f.app.ChatTerminal(ctx, fork.ID); err == nil {
		t.Error("opened a terminal on a fork that hasn't started")
	}
	f.app.ChatSend(ctx, fork.ID, "next")
	waitUntil(t, "the fork's turn", func() bool { x, _ := f.app.store.Session(fork.ID); return !x.Fork && x.State == store.ChatIdle })
	if argv := sp.last(); !hasSeq(argv, "--resume", "conv-1", "--fork-session") {
		t.Errorf("fork argv = %q", argv)
	}

	// An ended chat can be forked, to carry on from it; a busy one can't.
	f.app.KillSession(ctx, s.ID)
	if _, err := f.app.ChatFork(ctx, s.ID); err != nil {
		t.Errorf("fork of an ended chat: %v", err)
	}
	f.app.store.UpdateSession(fork.ID, func(x *store.Session) { x.State = store.ChatWorking })
	if _, err := f.app.ChatFork(ctx, fork.ID); err != ErrBusy {
		t.Errorf("fork of a busy chat: %v", err)
	}
}

func TestChatCompact(t *testing.T) {
	f, sp := chatFixture(t)
	ctx := context.Background()
	s, _ := f.app.NewChat(ctx, "orbit", "claude", NewOptions{})
	if err := f.app.ChatCompact(ctx, s.ID); err == nil {
		t.Error("compacted an empty chat")
	}
	f.app.ChatSend(ctx, s.ID, "hello")
	waitUntil(t, "the turn", func() bool {
		x, _ := f.app.store.Session(s.ID)
		return x.Resume == "conv-1" && x.State == store.ChatIdle
	})
	n := len(sp.argvs)
	if err := f.app.ChatCompact(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the compaction", func() bool {
		ev, _ := f.app.ChatEvents(s.ID, 0)
		turns := 0
		for _, e := range ev {
			if e.Kind == chat.KindTurn {
				turns++
			}
		}
		return turns == 4
	})
	if len(sp.argvs) != n {
		t.Error("compacting started another process")
	}
	if err := f.app.ChatCompact(ctx, "nope"); err == nil {
		t.Error("compacted an unknown session")
	}
}
