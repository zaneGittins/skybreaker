package scheduler

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/zaneGittins/skybreaker/internal/control/chat"
	"github.com/zaneGittins/skybreaker/internal/control/config"
	"github.com/zaneGittins/skybreaker/internal/control/store"
)

const claudeLog = `{"type":"system","subtype":"init","session_id":"c-123"}
{"type":"assistant","message":{"content":[{"type":"text","text":"working"}]}}
{"type":"result","subtype":"success","is_error":false,"total_cost_usd":0.37,"session_id":"c-123","result":"Opened PR #12"}
`

// fakeBackend plays the sandbox: git commands succeed, and starting the run
// window "runs" the harness by writing its log and exit code to the host
// run directory, as the real wrapper script would.
type fakeBackend struct {
	dir      string
	mu       sync.Mutex
	calls    [][]string
	exitCode string // "" never finishes
	log      string
	started  int
	tmuxUp   int
	failWT   bool
}

func (b *fakeBackend) EnsureTmux(context.Context, string) error {
	b.mu.Lock()
	b.tmuxUp++
	b.mu.Unlock()
	return nil
}

func (b *fakeBackend) StartSandbox(context.Context, string) error {
	b.mu.Lock()
	b.started++
	b.mu.Unlock()
	return nil
}
func (b *fakeBackend) ProjectDir(p string) string { return filepath.Join(b.dir, "projects", p) }

func (b *fakeBackend) Exec(_ context.Context, project string, args ...string) ([]byte, error) {
	b.mu.Lock()
	b.calls = append(b.calls, args)
	b.mu.Unlock()
	if slices.Contains(args, "worktree") && b.failWT {
		return nil, errors.New("fatal: invalid reference: origin/main")
	}
	if slices.Contains(args, "rev-parse") {
		return []byte("0123456789abcdef0123456789abcdef01234567\n"), nil
	}
	if len(args) > 1 && args[0] == "tmux" && args[1] == "new-window" {
		id := args[slices.Index(args, "-n")+1]
		host := filepath.Join(b.ProjectDir(project), "runs", id)
		if b.exitCode != "" {
			go func() {
				time.Sleep(20 * time.Millisecond)
				os.WriteFile(filepath.Join(host, "log.jsonl"), []byte(b.log), 0o644)
				os.WriteFile(filepath.Join(host, "exit"), []byte(b.exitCode+"\n"), 0o644)
			}()
		}
	}
	return nil, nil
}

func (b *fakeBackend) find(word string) [][]string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out [][]string
	for _, c := range b.calls {
		if slices.Contains(c, word) {
			out = append(out, c)
		}
	}
	return out
}

func setup(t *testing.T, jobs string) (*Scheduler, *fakeBackend, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	cfgJSON := fmt.Sprintf(`{
	  "data_dir": %q, "users": ["zane@example.com"],
	  "sandbox": {"image": "img"},
	  "projects": {"orbit": {"repo": "zaneGittins/orbit", "harnesses": ["claude", "pi"]}},
	  "jobs": %s
	}`, dir, jobs)
	p := filepath.Join(dir, "skybreaker.json")
	os.WriteFile(p, []byte(cfgJSON), 0o600)
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := store.Open("")
	b := &fakeBackend{dir: dir, exitCode: "0", log: claudeLog}
	s := New(Deps{Config: cfg, Store: st, Backend: b, Poll: 5 * time.Millisecond, Grace: 20 * time.Millisecond})
	return s, b, st
}

const nightly = `[{"name": "nightly", "project": "orbit", "schedule": "0 2 * * *", "kind": "board", "budget_usd": 5, "allowed_tools": ["Bash", "Edit"]}]`

func TestTriggerRun(t *testing.T) {
	s, b, st := setup(t, nightly)
	run, err := s.Trigger(context.Background(), "nightly")
	if err != nil {
		t.Fatal(err)
	}
	s.Wait()
	got, _ := st.Run(run.ID)
	if got.Status != store.RunOK || got.ExitCode == nil || *got.ExitCode != 0 {
		t.Fatalf("run = %+v", got)
	}
	// Windows outlive their command (remain-on-exit), so a finished run's
	// window is closed; its output is in the log.
	if b.tmuxUp != 1 || len(b.find("kill-window")) != 1 {
		t.Errorf("tmux ensured %d times, kill-window %v", b.tmuxUp, b.find("kill-window"))
	}
	if got.CostUSD != 0.37 || got.SessionID != "c-123" || got.GitSHA != "0123456789abcdef0123456789abcdef01234567" {
		t.Errorf("result not parsed: %+v", got)
	}
	if !strings.Contains(got.Prompt, "orbit") || !strings.Contains(got.Prompt, "review") {
		t.Errorf("orbit prompt = %q", got.Prompt)
	}
	// A fresh worktree from origin/main, with the safe git flags.
	wt := b.find("worktree")
	if len(wt) != 1 || !slices.Contains(wt[0], "/skybreaker/runs/"+run.ID+"/work") || !slices.Contains(wt[0], "origin/main") || !slices.Contains(wt[0], "core.hooksPath=/dev/null") {
		t.Fatalf("worktree = %v", wt)
	}
	win := b.find("new-window")
	if len(win) != 1 {
		t.Fatalf("new-window calls %d", len(win))
	}
	w := win[0]
	for _, want := range []string{"-p", "--bare", "--max-budget-usd", "5.00", "Bash,Edit", "--mcp-config", "/etc/skybreaker/mcp.json"} {
		if !slices.Contains(w, want) {
			t.Errorf("headless argv lacks %q: %v", want, w)
		}
	}
	if !slices.Contains(w, "=skybreaker-runs:") || !slices.Contains(w, "/skybreaker/runs/"+run.ID+"/work") {
		t.Errorf("window target/dir wrong: %v", w)
	}
	// The prompt is passed as one argv element, never through a shell string.
	script := w[slices.Index(w, "-c")+1]
	if strings.Contains(script, "orbit") {
		t.Errorf("prompt interpolated into the shell script: %s", script)
	}
	if b.started == 0 {
		t.Error("sandbox not started")
	}
}

func TestRunFailure(t *testing.T) {
	s, b, st := setup(t, nightly)
	b.exitCode = "3"
	b.log = "not json\n"
	run, _ := s.Trigger(context.Background(), "nightly")
	s.Wait()
	got, _ := st.Run(run.ID)
	if got.Status != store.RunFailed || *got.ExitCode != 3 {
		t.Fatalf("run = %+v", got)
	}
}

func TestRunTimeout(t *testing.T) {
	s, b, st := setup(t, `[{"name": "slow", "project": "orbit", "schedule": "@hourly", "prompt": "x", "timeout": "50ms"}]`)
	b.exitCode = ""
	run, _ := s.Trigger(context.Background(), "slow")
	s.Wait()
	got, _ := st.Run(run.ID)
	if got.Status != store.RunTimeout {
		t.Fatalf("status %q", got.Status)
	}
	// SIGINT first (C-c), then the window is killed.
	if len(b.find("C-c")) != 1 || len(b.find("kill-window")) != 1 {
		t.Fatalf("interrupt/kill: %v / %v", b.find("C-c"), b.find("kill-window"))
	}
}

func TestWorktreeFallsBackToHEAD(t *testing.T) {
	s, b, st := setup(t, nightly)
	b.failWT = true
	run, _ := s.Trigger(context.Background(), "nightly")
	s.Wait()
	got, _ := st.Run(run.ID)
	if got.Status != store.RunFailed || !strings.Contains(got.Error, "worktree") {
		t.Fatalf("run = %+v", got)
	}
	if len(b.find("worktree")) != 2 {
		t.Fatalf("expected a retry from HEAD: %v", b.find("worktree"))
	}
}

func TestOneRunPerJob(t *testing.T) {
	s, b, _ := setup(t, nightly)
	b.exitCode = ""
	s.grace = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := s.Trigger(ctx, "nightly"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Trigger(ctx, "nightly"); !errors.Is(err, ErrBusy) {
		t.Fatalf("second trigger: %v, want ErrBusy", err)
	}
	// A restart waits for runs as it waits for chats.
	if !s.Busy() {
		t.Fatal("not busy with a run going")
	}
	cancel()
	s.Wait()
	if s.Busy() {
		t.Fatal("busy with nothing running")
	}
}

func TestUnknownJob(t *testing.T) {
	s, _, _ := setup(t, nightly)
	if _, err := s.Trigger(context.Background(), "nope"); err == nil {
		t.Fatal("unknown job triggered")
	}
}

func TestTickFiresDueJobs(t *testing.T) {
	s, _, st := setup(t, nightly)
	loc := time.UTC
	s.Tick(context.Background(), time.Date(2026, 10, 17, 1, 59, 0, 0, loc)) // primes
	s.Tick(context.Background(), time.Date(2026, 10, 17, 2, 0, 30, 0, loc)) // 02:00 passed
	s.Wait()
	if n := len(st.ListRuns("orbit", 0)); n != 1 {
		t.Fatalf("runs after 02:00 tick: %d, want 1", n)
	}
	s.Tick(context.Background(), time.Date(2026, 10, 17, 2, 1, 0, 0, loc))
	s.Wait()
	if n := len(st.ListRuns("orbit", 0)); n != 1 {
		t.Fatalf("job fired twice: %d", n)
	}
}

func TestParseClaudeResult(t *testing.T) {
	r := ParseResult("claude-stream-json", []byte(claudeLog))
	if r.CostUSD != 0.37 || r.SessionID != "c-123" || r.IsError || r.Summary != "Opened PR #12" {
		t.Fatalf("%+v", r)
	}
	// A run's tokens: Claude's result counts every model the run used;
	// codex exec reports each turn's.
	r = ParseResult("claude-stream-json", []byte(`{"type":"result","modelUsage":{"a":{"inputTokens":1,"cacheCreationInputTokens":2,"cacheReadInputTokens":3,"outputTokens":4},"b":{"inputTokens":10,"outputTokens":1}}}`))
	if r.Tokens != (chat.Usage{Input: 13, Cached: 3, Output: 5}) {
		t.Errorf("claude tokens %+v", r.Tokens)
	}
	r = ParseResult("codex-json", []byte(`{"type":"thread.started","thread_id":"t1"}
{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":60,"output_tokens":7}}
{"type":"turn.completed","usage":{"input_tokens":-5,"cached_input_tokens":0,"output_tokens":1}}`))
	if r.Tokens != (chat.Usage{Input: 40, Cached: 60, Output: 7}) {
		t.Errorf("codex tokens %+v", r.Tokens)
	}
	if r := ParseResult("text", []byte("hello\n")); r.CostUSD != 0 || r.Summary != "hello" {
		t.Fatalf("text: %+v", r)
	}
	if r := ParseResult("claude-stream-json", []byte("garbage")); r.SessionID != "" {
		t.Fatalf("garbage: %+v", r)
	}
}

func TestBoardPromptTreatsItemsAsData(t *testing.T) {
	p := BoardPrompt("orbit")
	for _, want := range []string{"next_item", "claim", "feature/", "fix/", "review", "untrusted"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
}

func TestRunFilesAreNotFollowed(t *testing.T) {
	// The runs directory is writable from inside the sandbox. A symlink
	// there must not make the control plane read host files or /dev/zero.
	dir := t.TempDir()
	secret := filepath.Join(dir, "hook.key")
	os.WriteFile(secret, []byte("host secret"), 0o600)
	link := filepath.Join(dir, "log.jsonl")
	os.Symlink(secret, link)
	if _, err := ReadRunFile(link, 1<<20); err == nil {
		t.Fatal("followed a symlink out of the run directory")
	}
	os.Symlink("/dev/zero", filepath.Join(dir, "exit"))
	if _, err := ReadRunFile(filepath.Join(dir, "exit"), 64); err == nil {
		t.Fatal("read a device through a symlink")
	}
	// The tail: a long run's result event is its last line.
	os.WriteFile(filepath.Join(dir, "big"), []byte("0123456789"), 0o644)
	if b, err := ReadRunFile(filepath.Join(dir, "big"), 4); err != nil || string(b) != "6789" {
		t.Fatalf("limit: %q %v", b, err)
	}
}

func TestRunDirSwappedForSymlinkIsNotFollowed(t *testing.T) {
	// The sandbox can replace runs/<id> itself with a link to a host
	// directory; O_NOFOLLOW only guards the last component.
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	os.Mkdir(outside, 0o700)
	os.WriteFile(filepath.Join(outside, "log.jsonl"), []byte("host data"), 0o600)
	runs := filepath.Join(base, "runs")
	os.Mkdir(runs, 0o755)
	os.Symlink(outside, filepath.Join(runs, "r1"))
	if b, err := ReadRunFile(filepath.Join(runs, "r1", "log.jsonl"), 1<<20); err == nil {
		t.Fatalf("read %q through a swapped run directory", b)
	}
	os.Symlink("../outside", filepath.Join(runs, "r2"))
	if _, err := ReadRunFile(filepath.Join(runs, "r2", "log.jsonl"), 1<<20); err == nil {
		t.Fatal("followed a relative link out of runs/")
	}
	os.MkdirAll(filepath.Join(runs, "r3"), 0o755)
	os.WriteFile(filepath.Join(runs, "r3", "log.jsonl"), []byte("ok"), 0o644)
	if b, err := ReadRunFile(filepath.Join(runs, "r3", "log.jsonl"), 1<<20); err != nil || string(b) != "ok" {
		t.Fatalf("a real run file: %q %v", b, err)
	}
}

func TestOldRunsArePruned(t *testing.T) {
	s, b, st := setup(t, nightly)
	runs := filepath.Join(b.ProjectDir("orbit"), "runs")
	t0 := time.Now().Add(-time.Hour)
	for i := range 12 {
		id := fmt.Sprintf("old%02d", i)
		st.CreateRun(store.Run{ID: id, Job: "nightly", Project: "orbit", Status: store.RunOK, Started: t0.Add(time.Duration(i) * time.Minute)})
		os.MkdirAll(filepath.Join(runs, id, "work"), 0o755)
	}
	os.MkdirAll(filepath.Join(runs, "aged-out", "work"), 0o755) // no longer in the store
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "keep"), []byte("host file"), 0o600)
	os.Symlink(outside, filepath.Join(runs, "planted")) // made from inside the sandbox

	if _, err := s.Trigger(context.Background(), "nightly"); err != nil {
		t.Fatal(err)
	}
	s.Wait()

	var removed []string
	for _, c := range b.find("remove") {
		removed = append(removed, c[len(c)-1])
	}
	// 13 runs with the new one; the 10 newest keep their worktrees.
	if want := []string{"/skybreaker/runs/old00/work", "/skybreaker/runs/old01/work", "/skybreaker/runs/old02/work"}; !slices.Equal(removed, want) {
		t.Errorf("worktrees removed %v, want %v", removed, want)
	}
	if len(b.find("prune")) == 0 {
		t.Error("git worktree prune was not run after removing a run directory")
	}
	for _, gone := range []string{"aged-out", "planted"} {
		if _, err := os.Lstat(filepath.Join(runs, gone)); err == nil {
			t.Errorf("runs/%s was kept", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(outside, "keep")); err != nil {
		t.Errorf("pruning followed a planted link: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runs, "old00", "work")); err != nil {
		t.Errorf("an old run's directory went too: %v (its log stays while the run is listed)", err)
	}
}

func TestReconcileMarksInterruptedRuns(t *testing.T) {
	s, _, st := setup(t, nightly)
	st.CreateRun(store.Run{ID: "old", Job: "nightly", Project: "orbit", Status: store.RunRunning, Started: time.Now().Add(-time.Hour)})
	s.Reconcile()
	r, _ := st.Run("old")
	if r.Status != store.RunFailed || !strings.Contains(r.Error, "restart") {
		t.Fatalf("run = %+v", r)
	}
}

func TestShutdownStopsUITriggeredRuns(t *testing.T) {
	s, b, st := setup(t, `[{"name": "slow", "project": "orbit", "schedule": "@hourly", "prompt": "x", "timeout": "1h"}]`)
	b.exitCode = ""
	ctx, cancel := context.WithCancel(context.Background())
	s.SetContext(ctx)
	run, err := s.Trigger(context.WithoutCancel(context.Background()), "slow") // as the API does
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	done := make(chan struct{})
	go func() { s.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown waited for a UI-triggered run's full timeout")
	}
	if r, _ := st.Run(run.ID); r.Status == store.RunRunning {
		t.Fatalf("run still running after shutdown: %+v", r)
	}
}

func TestWrapperSurvivesInterrupt(t *testing.T) {
	// SIGINT reaches tmux's whole process group. The wrapper and tee must
	// outlive it so the harness's last output and exit code are kept.
	dir := t.TempDir()
	cmd := exec.Command("sh", "-c", wrapperScript(dir), "skybreaker-run", "sh", "-c", `trap 'echo bye; exit 130' INT; echo started; while :; do sleep 0.05; done`)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
	cmd.Wait()
	exit, _ := os.ReadFile(filepath.Join(dir, "exit"))
	log, _ := os.ReadFile(filepath.Join(dir, "log.jsonl"))
	if strings.TrimSpace(string(exit)) != "130" || !strings.Contains(string(log), "bye") {
		t.Fatalf("exit %q log %q", exit, log)
	}
	// The normal path still records the exit code.
	dir2 := t.TempDir()
	exec.Command("sh", "-c", wrapperScript(dir2), "skybreaker-run", "sh", "-c", "echo hi; exit 4").Run()
	exit, _ = os.ReadFile(filepath.Join(dir2, "exit"))
	log, _ = os.ReadFile(filepath.Join(dir2, "log.jsonl"))
	if strings.TrimSpace(string(exit)) != "4" || strings.TrimSpace(string(log)) != "hi" {
		t.Fatalf("normal exit %q log %q", exit, log)
	}
}
