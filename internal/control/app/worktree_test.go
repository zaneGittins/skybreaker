package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zaneGittins/skybreaker/internal/control/chat"
	"github.com/zaneGittins/skybreaker/internal/control/sandbox"
	"github.com/zaneGittins/skybreaker/internal/control/store"
)

// gitSandbox runs a sandbox's git, test, ls and snapshot commands on this
// machine for real, with temp directories standing in for /workspace and
// /skybreaker/worktrees, and a bare repository for GitHub.
type gitSandbox struct {
	t                     *testing.T
	origin, ws, worktrees string
}

func newGitSandbox(t *testing.T, f *fixture) *gitSandbox {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	root := t.TempDir()
	g := &gitSandbox{t: t, origin: filepath.Join(root, "origin.git"), ws: filepath.Join(root, "workspace"), worktrees: filepath.Join(root, "worktrees")}
	os.MkdirAll(g.worktrees, 0o755)
	seed := filepath.Join(root, "seed")
	g.run("", "init", "--quiet", "--bare", "-b", "main", g.origin)
	g.run("", "init", "--quiet", "-b", "main", seed)
	os.WriteFile(filepath.Join(seed, "README.md"), []byte("orbit\n"), 0o644)
	os.WriteFile(filepath.Join(seed, "main.go"), []byte("package main\n"), 0o644)
	g.run(seed, "add", "-A")
	g.run(seed, "commit", "--quiet", "-m", "first")
	g.run(seed, "push", "--quiet", g.origin, "main")
	g.run("", "clone", "--quiet", g.origin, g.ws)
	f.docker.set(func() { f.docker.execOut = g.exec })
	return g
}

// path maps a sandbox path to this machine's stand-in.
func (g *gitSandbox) path(p string) string {
	for from, to := range map[string]string{workspaceDir: g.ws, sandbox.WorktreesDir: g.worktrees} {
		if p == from || strings.HasPrefix(p, from+"/") {
			return to + strings.TrimPrefix(p, from)
		}
	}
	return p
}

func (g *gitSandbox) exec(args []string) (string, error) {
	switch {
	case args[0] == "git", args[0] == "ls",
		args[0] == "test" && len(args) == 3 && args[2] != readyMarker,
		args[0] == "sh" && len(args) > 2 && (args[2] == snapshotScript || args[2] == presentScript):
	default:
		return "", nil
	}
	mapped := make([]string, len(args))
	for i, a := range args {
		mapped[i] = g.path(a)
	}
	cmd := exec.Command(mapped[0], mapped[1:]...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return string(out), errors.New(err.Error() + ": " + string(ee.Stderr))
		}
		return string(out), err
	}
	if len(out) == 0 {
		// The fake reads an empty answer as "not mine"; git's quiet
		// successes have nothing to say either way.
		return "", nil
	}
	return string(out), nil
}

// run is git on this machine, for setting up and checking.
func (g *gitSandbox) run(dir string, args ...string) string {
	g.t.Helper()
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		g.t.Fatalf("git %q: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (g *gitSandbox) dir(id string) string { return filepath.Join(g.worktrees, id) }

func (g *gitSandbox) read(id, name string) string {
	b, err := os.ReadFile(filepath.Join(g.dir(id), name))
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func (g *gitSandbox) hasBranch(name string) bool {
	return exec.Command("git", "-C", g.ws, "rev-parse", "--verify", "--quiet", "refs/heads/"+name).Run() == nil
}

// startedChat is a chat on its own branch that has had its first turn.
func startedChat(t *testing.T, f *fixture, opts NewOptions) store.Session {
	t.Helper()
	ctx := context.Background()
	s, err := f.app.NewChat(ctx, "orbit", "claude", opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.app.ChatSend(ctx, s.ID, "hello"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the turn", func() bool {
		x, _ := f.app.store.Session(s.ID)
		return x.Resume == "conv-1" && x.State == store.ChatIdle
	})
	s, _ = f.app.store.Session(s.ID)
	return s
}

func TestChatOnItsOwnBranch(t *testing.T) {
	f, sp := chatFixture(t)
	g := newGitSandbox(t, f)
	a := startedChat(t, f, NewOptions{OwnBranch: true})
	dir := sandbox.WorktreesDir + "/" + a.ID
	if a.Worktree != dir || a.Branch != "skybreaker/"+a.ID || a.BranchFrom == "" {
		t.Fatalf("session = %+v", a)
	}
	if argv := sp.last(); !hasSeq(argv, "docker", "exec", "-i", "-w", dir) {
		t.Errorf("harness didn't run in its worktree: %q", argv)
	}
	if got := g.run(g.dir(a.ID), "rev-parse", "--abbrev-ref", "HEAD"); got != a.Branch {
		t.Errorf("worktree on %q, want %q", got, a.Branch)
	}
	if got := g.run(g.ws, "rev-parse", "origin/main"); got != a.BranchFrom {
		t.Errorf("branch from %s, want origin/main %s", a.BranchFrom, got)
	}
	if o := f.app.chatOptions(a); o.Cwd != dir {
		t.Errorf("chat cwd = %q", o.Cwd)
	}

	// A second chat works apart from the first, and neither touches the
	// workspace.
	b := startedChat(t, f, NewOptions{OwnBranch: true})
	os.WriteFile(filepath.Join(g.dir(a.ID), "main.go"), []byte("package main // a\n"), 0o644)
	os.WriteFile(filepath.Join(g.dir(b.ID), "main.go"), []byte("package main // b\n"), 0o644)
	if g.read(a.ID, "main.go") != "package main // a\n" || g.read(b.ID, "main.go") != "package main // b\n" {
		t.Error("chats share files")
	}
	if ws, _ := os.ReadFile(filepath.Join(g.ws, "main.go")); string(ws) != "package main\n" {
		t.Errorf("workspace changed: %q", ws)
	}

	// Its changes are its worktree's, against the project's branch.
	r, err := f.app.ReviewSession(context.Background(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Branch != a.Branch || r.Base != "origin/main" || len(r.Files) != 1 || r.Files[0].Path != "main.go" || !strings.Contains(r.Diff, "// a") {
		t.Errorf("review = %+v", r)
	}
	if r, _ := f.app.Review(context.Background(), "orbit"); !r.Clean {
		t.Errorf("workspace review = %+v", r)
	}

	// A chat without one stays in the workspace.
	c := startedChat(t, f, NewOptions{})
	if c.Worktree != "" || !hasSeq(sp.last(), "-w", "/workspace") {
		t.Errorf("plain chat = %+v, argv %q", c, sp.last())
	}
}

func TestChatTerminalOpensInItsWorktree(t *testing.T) {
	f, _ := chatFixture(t)
	newGitSandbox(t, f)
	a := startedChat(t, f, NewOptions{OwnBranch: true})
	term, err := f.app.ChatTerminal(context.Background(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if term.Worktree != a.Worktree || term.Branch != a.Branch {
		t.Errorf("terminal = %+v", term)
	}
	got := f.docker.find(func(c []string) bool { return hasSeq(c, "tmux", "new-session") && slices.Contains(c, term.ID) })
	if len(got) != 1 || !hasSeq(got[0], "-c", a.Worktree) || !hasSeq(got[0], "claude", "--resume", "conv-1") {
		t.Errorf("tmux = %q", got)
	}
}

func TestTerminalOnItsOwnBranch(t *testing.T) {
	f, _ := chatFixture(t)
	g := newGitSandbox(t, f)
	s, err := f.app.NewSession(context.Background(), "orbit", "codex", "", NewOptions{OwnBranch: true})
	if err != nil {
		t.Fatal(err)
	}
	if s.Worktree == "" || g.run(g.dir(s.ID), "rev-parse", "--abbrev-ref", "HEAD") != "skybreaker/"+s.ID {
		t.Fatalf("terminal = %+v", s)
	}
	got := f.docker.find(func(c []string) bool { return hasSeq(c, "tmux", "new-session") && slices.Contains(c, s.ID) })
	if len(got) != 1 || !hasSeq(got[0], "-c", s.Worktree) {
		t.Errorf("tmux = %q", got)
	}
}

func TestForkCopiesTheWorkOntoANewBranch(t *testing.T) {
	f, _ := chatFixture(t)
	g := newGitSandbox(t, f)
	a := startedChat(t, f, NewOptions{OwnBranch: true})
	// A commit, an edit, a deletion and a new file.
	os.WriteFile(filepath.Join(g.dir(a.ID), "main.go"), []byte("package main // committed\n"), 0o644)
	g.run(g.dir(a.ID), "commit", "--quiet", "-am", "work")
	head := g.run(g.dir(a.ID), "rev-parse", "HEAD")
	os.WriteFile(filepath.Join(g.dir(a.ID), "main.go"), []byte("package main // uncommitted\n"), 0o644)
	os.Remove(filepath.Join(g.dir(a.ID), "README.md"))
	os.WriteFile(filepath.Join(g.dir(a.ID), "new.go"), []byte("package main // new\n"), 0o644)
	statusBefore := g.run(g.dir(a.ID), "status", "--porcelain")

	fk, err := f.app.ChatFork(context.Background(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fk.Worktree == "" || fk.Worktree == a.Worktree || fk.Branch != "skybreaker/"+fk.ID || fk.BranchFrom != head {
		t.Fatalf("fork = %+v", fk)
	}
	if g.run(g.dir(fk.ID), "rev-parse", "HEAD") != head {
		t.Error("fork's branch doesn't start at the original's commit")
	}
	if g.read(fk.ID, "main.go") != "package main // uncommitted\n" || g.read(fk.ID, "new.go") != "package main // new\n" || g.read(fk.ID, "README.md") != "<missing>" {
		t.Errorf("fork's files: main=%q new=%q readme=%q", g.read(fk.ID, "main.go"), g.read(fk.ID, "new.go"), g.read(fk.ID, "README.md"))
	}
	if got := g.run(g.dir(fk.ID), "status", "--porcelain"); got != statusBefore {
		t.Errorf("fork status %q, want the original's %q", got, statusBefore)
	}
	// The original is as it was, index and all.
	if got := g.run(g.dir(a.ID), "status", "--porcelain"); got != statusBefore {
		t.Errorf("original status now %q, was %q", got, statusBefore)
	}
	// From here they go their own ways.
	os.WriteFile(filepath.Join(g.dir(fk.ID), "new.go"), []byte("package main // fork\n"), 0o644)
	if g.read(a.ID, "new.go") != "package main // new\n" {
		t.Error("fork's edit reached the original")
	}

	// A plain chat's fork stays in the workspace.
	c := startedChat(t, f, NewOptions{})
	if cf, err := f.app.ChatFork(context.Background(), c.ID); err != nil || cf.Worktree != "" {
		t.Errorf("plain fork = %+v, %v", cf, err)
	}
}

func TestEndedChatsWorktreesAreTidied(t *testing.T) {
	f, _ := chatFixture(t)
	g := newGitSandbox(t, f)
	ctx := context.Background()
	end := func(s store.Session) {
		t.Helper()
		if err := f.app.KillSession(ctx, s.ID); err != nil {
			t.Fatal(err)
		}
		f.app.bg.Wait()
	}

	// Nothing done: the worktree and its branch go.
	idle := startedChat(t, f, NewOptions{OwnBranch: true})
	end(idle)
	if _, err := os.Stat(g.dir(idle.ID)); !os.IsNotExist(err) {
		t.Errorf("idle chat's worktree still there: %v", err)
	}
	if g.hasBranch(idle.Branch) {
		t.Error("empty branch kept")
	}
	if x, _ := f.app.store.Session(idle.ID); !x.WorktreeGone {
		t.Errorf("session = %+v", x)
	}

	// Committed work: the worktree goes, the branch stays, renamed or not.
	done := startedChat(t, f, NewOptions{OwnBranch: true})
	os.WriteFile(filepath.Join(g.dir(done.ID), "main.go"), []byte("package main // done\n"), 0o644)
	g.run(g.dir(done.ID), "checkout", "--quiet", "-b", "feature/done")
	g.run(g.dir(done.ID), "commit", "--quiet", "-am", "done")
	end(done)
	if _, err := os.Stat(g.dir(done.ID)); !os.IsNotExist(err) {
		t.Error("committed chat's worktree still there")
	}
	if !g.hasBranch("feature/done") || g.hasBranch(done.Branch) {
		t.Error("wrong branches kept")
	}
	if x, _ := f.app.store.Session(done.ID); x.Branch != "feature/done" {
		t.Errorf("recorded branch %q", x.Branch)
	}
	// Forking it later picks the work up again.
	fk, err := f.app.ChatFork(ctx, done.ID)
	if err != nil {
		t.Fatal(err)
	}
	if g.read(fk.ID, "main.go") != "package main // done\n" {
		t.Errorf("fork of ended chat has %q", g.read(fk.ID, "main.go"))
	}

	// Uncommitted work stays on disk, for the newest few.
	var dirty []store.Session
	for i := range keepDirty + 1 {
		s := startedChat(t, f, NewOptions{OwnBranch: true})
		os.WriteFile(filepath.Join(g.dir(s.ID), "wip.txt"), []byte(s.ID), 0o644)
		dirty = append(dirty, s)
		end(s)
		time.Sleep(2 * time.Millisecond) // distinct end times
		_ = i
	}
	oldest := dirty[0]
	if _, err := os.Stat(g.dir(oldest.ID)); !os.IsNotExist(err) {
		t.Error("the oldest dirty worktree beyond the cap is still there")
	}
	for _, s := range dirty[1:] {
		if g.read(s.ID, "wip.txt") != s.ID {
			t.Errorf("dirty worktree %s lost", s.ID)
		}
	}
	// What it held is kept, and a fork brings it back.
	if exec.Command("git", "-C", g.ws, "rev-parse", "--verify", "--quiet", wipPrefix+oldest.ID).Run() != nil {
		t.Fatal("no wip ref for the archived worktree")
	}
	back, err := f.app.ChatFork(ctx, oldest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if g.read(back.ID, "wip.txt") != oldest.ID {
		t.Errorf("fork of archived chat has wip.txt %q", g.read(back.ID, "wip.txt"))
	}
}

func TestClearedChatKeepsItsWorktree(t *testing.T) {
	f, _ := chatFixture(t)
	g := newGitSandbox(t, f)
	ctx := context.Background()
	a := startedChat(t, f, NewOptions{OwnBranch: true})
	os.WriteFile(filepath.Join(g.dir(a.ID), "main.go"), []byte("package main // a\n"), 0o644)
	n, err := f.app.NewChat(ctx, "orbit", "claude", NewOptions{Continues: a.ID})
	if err != nil {
		t.Fatal(err)
	}
	if n.Worktree != a.Worktree || n.Branch != a.Branch {
		t.Errorf("cleared chat = %+v", n)
	}
	f.app.KillSession(ctx, a.ID)
	f.app.bg.Wait()
	if g.read(a.ID, "main.go") != "package main // a\n" {
		t.Error("the worktree went while the cleared chat still uses it")
	}
	if f.app.chatOptions(n).Cwd != a.Worktree {
		t.Error("cleared chat runs elsewhere")
	}
	if _, err := f.app.NewChat(ctx, "orbit", "claude", NewOptions{Continues: "snope"}); err == nil {
		t.Error("continued an unknown session")
	}
}

func TestOrphanWorktreesAreKeptThenRemoved(t *testing.T) {
	f, _ := chatFixture(t)
	g := newGitSandbox(t, f)
	if err := f.app.StartSandbox(context.Background(), "orbit"); err != nil {
		t.Fatal(err)
	}
	// A worktree no session knows, as when the store has dropped the
	// oldest ended sessions.
	g.run(g.ws, "worktree", "add", "--quiet", "-b", "skybreaker/sorphan", g.dir("sorphan"), "HEAD")
	os.WriteFile(filepath.Join(g.dir("sorphan"), "wip.txt"), []byte("orphan"), 0o644)
	os.MkdirAll(filepath.Join(g.worktrees, "not-a-worktree"), 0o755)
	f.app.TidyWorktrees(context.Background())
	if _, err := os.Stat(g.dir("sorphan")); !os.IsNotExist(err) {
		t.Error("orphan worktree still there")
	}
	if exec.Command("git", "-C", g.ws, "rev-parse", "--verify", "--quiet", wipPrefix+"sorphan").Run() != nil {
		t.Error("orphan's work not kept")
	}
	if _, err := os.Stat(filepath.Join(g.worktrees, "not-a-worktree")); err != nil {
		t.Error("removed a directory that isn't a worktree")
	}
}

func TestOldSandboxRestartsToHoldWorktrees(t *testing.T) {
	f, _ := chatFixture(t)
	g := newGitSandbox(t, f)
	ctx := context.Background()
	if err := f.app.StartSandbox(ctx, "orbit"); err != nil {
		t.Fatal(err)
	}
	// Started by an earlier Skybreaker: no worktrees mount.
	os.RemoveAll(g.worktrees)
	starts := func() int {
		return len(f.docker.find(func(c []string) bool { return len(c) > 1 && c[1] == "run" }))
	}
	// In use: refused, not restarted under the user.
	if _, err := f.app.NewChat(ctx, "orbit", "claude", NewOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.NewChat(ctx, "orbit", "claude", NewOptions{OwnBranch: true}); !errors.Is(err, ErrNoWorktrees) {
		t.Errorf("busy sandbox: err = %v", err)
	}
	if starts() != 1 {
		t.Error("restarted a sandbox in use")
	}

	// Idle: restarted onto one that can.
	f2, _ := chatFixture(t)
	g2 := newGitSandbox(t, f2)
	if err := f2.app.StartSandbox(ctx, "orbit"); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(g2.worktrees)
	if _, err := f2.app.NewChat(ctx, "orbit", "claude", NewOptions{OwnBranch: true}); err != nil {
		t.Fatalf("idle sandbox: %v", err)
	}
	if n := len(f2.docker.find(func(c []string) bool { return len(c) > 1 && c[1] == "run" })); n != 2 {
		t.Errorf("sandbox started %d times, want a restart", n)
	}
}

func TestRefreshRestartsOldSandboxWhenIdle(t *testing.T) {
	f, _ := chatFixture(t)
	g := newGitSandbox(t, f)
	ctx := context.Background()
	if err := f.app.StartSandbox(ctx, "orbit"); err != nil {
		t.Fatal(err)
	}
	f.app.RefreshSandboxes(ctx)
	runs := func() int { return len(f.docker.find(func(c []string) bool { return len(c) > 1 && c[1] == "run" })) }
	if runs() != 1 {
		t.Fatal("restarted a sandbox that holds worktrees")
	}
	os.RemoveAll(g.worktrees)
	f.app.RefreshSandboxes(ctx)
	if runs() != 2 {
		t.Error("didn't restart an idle sandbox without the worktrees mount")
	}
}

func TestWorktreeMadeAgainIsTidiedAgain(t *testing.T) {
	f, _ := chatFixture(t)
	g := newGitSandbox(t, f)
	ctx := context.Background()
	a := startedChat(t, f, NewOptions{OwnBranch: true})
	os.WriteFile(filepath.Join(g.dir(a.ID), "main.go"), []byte("package main // a\n"), 0o644)
	g.run(g.dir(a.ID), "commit", "--quiet", "-am", "a")
	f.app.KillSession(ctx, a.ID)
	f.app.bg.Wait()
	if _, err := os.Stat(g.dir(a.ID)); !os.IsNotExist(err) {
		t.Fatal("not tidied")
	}
	// Carried on after it was tidied: made again on its branch, and
	// tidied again once that ends.
	n := startedChat(t, f, NewOptions{Continues: a.ID})
	if g.read(a.ID, "main.go") != "package main // a\n" {
		t.Fatalf("made again with %q", g.read(a.ID, "main.go"))
	}
	f.app.KillSession(ctx, n.ID)
	f.app.bg.Wait()
	if _, err := os.Stat(g.dir(a.ID)); !os.IsNotExist(err) {
		t.Error("not tidied the second time")
	}
}

func TestRelStripsWorktrees(t *testing.T) {
	for in, want := range map[string]string{
		"/workspace/a/b.go":                "a/b.go",
		"/skybreaker/worktrees/sabc/a.go":  "a.go",
		"/skybreaker/worktrees/sabc":       "/skybreaker/worktrees/sabc",
		"/skybreaker/worktrees/sabc/":      "/skybreaker/worktrees/sabc/",
		"/etc/passwd":                      "/etc/passwd",
		"/skybreaker/worktrees-other/x.go": "/skybreaker/worktrees-other/x.go",
	} {
		if got := chat.Rel(in); got != want {
			t.Errorf("Rel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGitFailedKeepsALineOfWhatGitSaid(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		// git's error, past the progress it printed first.
		{"docker exec: exit status 128: Preparing worktree (checking out 'x')\nfatal: 'x' is already used by worktree at '/workspace'",
			"git worktree add: fatal: 'x' is already used by worktree at '/workspace'"},
		{"docker exec: exit status 1: error: pathspec did not match\nmore", "git worktree add: error: pathspec did not match"},
		// Without one, the first line.
		{"docker exec: exit status 1: something\nYOUR SESSION EXPIRED, click here", "git worktree add: docker exec: exit status 1: something"},
	} {
		if got := gitFailed("git worktree add", errors.New(c.in)).Error(); got != c.want {
			t.Errorf("gitFailed(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	long := gitFailed("git restore", errors.New("fatal: "+strings.Repeat("x", 500))).Error()
	if n := len([]rune(long)); n > 320 || !strings.HasSuffix(long, "…") {
		t.Errorf("long error kept %d runes", n)
	}
}

// failing wraps a gitSandbox so commands matching fail error out, while
// fail returns true.
func failing(f *fixture, g *gitSandbox, fail func(args []string) bool) {
	f.docker.set(func() {
		f.docker.execOut = func(args []string) (string, error) {
			if fail(args) {
				return "", errors.New("docker exec: exit status 1: it broke")
			}
			return g.exec(args)
		}
	})
}

func TestTidyLeavesWhatItCantCheck(t *testing.T) {
	f, _ := chatFixture(t)
	g := newGitSandbox(t, f)
	a := startedChat(t, f, NewOptions{OwnBranch: true})
	// The sandbox stops answering, as when it's stopped mid-tidy.
	failing(f, g, func(args []string) bool { return args[0] == "sh" && args[2] == presentScript })
	f.app.KillSession(context.Background(), a.ID)
	f.app.bg.Wait()
	if x, _ := f.app.store.Session(a.ID); x.WorktreeGone {
		t.Error("marked gone without being checked")
	}
	// Once it answers, the next tidy does it.
	f.docker.set(func() { f.docker.execOut = g.exec })
	f.app.TidyWorktrees(context.Background())
	if x, _ := f.app.store.Session(a.ID); !x.WorktreeGone {
		t.Error("not tidied once the sandbox answered")
	}
}

func TestIgnoredFilesKeepAWorktree(t *testing.T) {
	f, _ := chatFixture(t)
	g := newGitSandbox(t, f)
	a := startedChat(t, f, NewOptions{OwnBranch: true})
	os.WriteFile(filepath.Join(g.dir(a.ID), ".gitignore"), []byte("*.env\n"), 0o644)
	g.run(g.dir(a.ID), "add", ".gitignore")
	g.run(g.dir(a.ID), "commit", "--quiet", "-m", "ignore env")
	os.WriteFile(filepath.Join(g.dir(a.ID), "x.env"), []byte("TOKEN=1"), 0o644)
	f.app.KillSession(context.Background(), a.ID)
	f.app.bg.Wait()
	if g.read(a.ID, "x.env") != "TOKEN=1" {
		t.Error("a worktree with ignored files was removed")
	}
}

func TestDetachedHeadsCommitsAreKept(t *testing.T) {
	f, _ := chatFixture(t)
	g := newGitSandbox(t, f)
	a := startedChat(t, f, NewOptions{OwnBranch: true})
	g.run(g.dir(a.ID), "checkout", "--quiet", "--detach")
	os.WriteFile(filepath.Join(g.dir(a.ID), "main.go"), []byte("package main // detached\n"), 0o644)
	g.run(g.dir(a.ID), "commit", "--quiet", "-am", "detached")
	head := g.run(g.dir(a.ID), "rev-parse", "HEAD")
	f.app.KillSession(context.Background(), a.ID)
	f.app.bg.Wait()
	if _, err := os.Stat(g.dir(a.ID)); !os.IsNotExist(err) {
		t.Fatal("not tidied")
	}
	if got := g.run(g.ws, "rev-parse", a.Branch); got != head {
		t.Errorf("own branch at %s, want the detached commit %s", got, head)
	}
	if x, _ := f.app.store.Session(a.ID); x.Branch != a.Branch {
		t.Errorf("recorded branch %q", x.Branch)
	}
	// Carried on, it's back on a branch, not a bare commit.
	n := startedChat(t, f, NewOptions{Continues: a.ID})
	if got := g.run(g.dir(a.ID), "rev-parse", "--abbrev-ref", "HEAD"); got != a.Branch || n.Branch != a.Branch {
		t.Errorf("carried on at %q (session %q)", got, n.Branch)
	}
}

func TestFailedRestoreIsTriedAgain(t *testing.T) {
	f, _ := chatFixture(t)
	g := newGitSandbox(t, f)
	ctx := context.Background()
	// A worktree tidied with uncommitted work, past the cap.
	a := startedChat(t, f, NewOptions{OwnBranch: true})
	os.WriteFile(filepath.Join(g.dir(a.ID), "wip.txt"), []byte("kept"), 0o644)
	f.app.KillSession(ctx, a.ID)
	f.app.bg.Wait()
	f.app.tidyWorktrees(ctx, "orbit", false)
	if _, err := f.app.removeWorktree(ctx, "orbit", a.Worktree, true, true, a.BranchFrom); err != nil {
		t.Fatal(err)
	}
	f.app.store.UpdateSession(a.ID, func(x *store.Session) { x.WorktreeGone = true })
	// Carrying it on fails to restore the work the first time.
	broke := true
	failing(f, g, func(args []string) bool { return broke && slices.Contains(args, "restore") })
	n, err := f.app.NewChat(ctx, "orbit", "claude", NewOptions{Continues: a.ID})
	if err != nil {
		t.Fatal(err)
	}
	x, _ := f.app.store.Session(n.ID)
	if err := f.app.ensureWorktree(ctx, &x, nil); err == nil {
		t.Fatal("restore didn't fail")
	}
	broke = false
	f.docker.set(func() { f.docker.execOut = g.exec })
	if err := f.app.ensureWorktree(ctx, &x, nil); err != nil {
		t.Fatal(err)
	}
	if g.read(a.ID, "wip.txt") != "kept" {
		t.Error("kept work not restored on the second try")
	}
}

func TestRestartedSandboxGetsTmuxForANewTerminal(t *testing.T) {
	f, _ := chatFixture(t)
	g := newGitSandbox(t, f)
	ctx := context.Background()
	if err := f.app.StartSandbox(ctx, "orbit"); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(g.worktrees)
	// The restart makes a new container, which has the mount.
	f.docker.set(func() {
		f.docker.execOut = func(args []string) (string, error) {
			if slices.ContainsFunc(f.docker.calls, func(c []string) bool { return len(c) > 1 && c[1] == "stop" }) {
				os.MkdirAll(g.worktrees, 0o755)
			}
			return g.exec(args)
		}
	})
	// The sandbox's background clone records calls meanwhile: read them
	// under the fake's lock.
	var before int
	f.docker.set(func() { before = len(f.docker.calls) })
	if _, err := f.app.NewSession(ctx, "orbit", "codex", "", NewOptions{OwnBranch: true}); err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	f.docker.set(func() { calls = slices.Clone(f.docker.calls[before:]) })
	lastRun, sleeper := -1, -1
	for i, c := range calls {
		if len(c) > 1 && c[1] == "run" {
			lastRun = i
		}
		if hasSeq(c, "tmux", "new-session", "-d", "-s", RunsSession) {
			sleeper = i
		}
	}
	if lastRun < 0 || sleeper < lastRun {
		t.Errorf("tmux not set up after the restart (run at %d, sleeper at %d)", lastRun, sleeper)
	}
}
