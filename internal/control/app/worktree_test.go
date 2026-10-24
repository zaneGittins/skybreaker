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

// uncloned makes the workspace a directory the repo was never cloned into.
func (g *gitSandbox) uncloned() {
	g.t.Helper()
	if err := os.RemoveAll(g.ws); err != nil {
		g.t.Fatal(err)
	}
	os.MkdirAll(g.ws, 0o755)
}

// path maps a sandbox path to this machine's stand-in, and the project's
// repo on GitHub to the bare one.
func (g *gitSandbox) path(p string) string {
	if p == "https://github.com/zaneGittins/orbit.git" {
		return g.origin
	}
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
		args[0] == "sh" && len(args) > 2 && (args[2] == snapshotScript || args[2] == presentScript || args[2] == cloneScript || args[2] == mergedScript):
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

// failedTurn is the error a chat's last failed turn ended with, if any.
func failedTurn(f *fixture, id string) string {
	ev, _ := f.app.ChatEvents(id, 0)
	for _, e := range slices.Backward(ev) {
		if e.Kind == chat.KindTurn && e.Turn != nil && e.Turn.State == chat.TurnFailed {
			return e.Turn.Error
		}
	}
	return ""
}

func TestCloneKeepsWhatsInTheWorkspace(t *testing.T) {
	// Files already in the workspace, made before the clone or by an
	// agent, don't keep the project from having a clone to branch from.
	f, _ := chatFixture(t)
	g := newGitSandbox(t, f)
	g.uncloned()
	os.MkdirAll(filepath.Join(g.ws, "old"), 0o755)
	os.WriteFile(filepath.Join(g.ws, "old", "notes"), []byte("mine\n"), 0o644)

	a := startedChat(t, f, NewOptions{OwnBranch: true})
	if got := g.read(a.ID, "README.md"); got != "orbit\n" {
		t.Errorf("worktree README = %q", got)
	}
	if b, err := os.ReadFile(filepath.Join(g.ws, "old", "notes")); err != nil || string(b) != "mine\n" {
		t.Errorf("workspace file = %q, %v", b, err)
	}
	if b, _ := os.ReadFile(filepath.Join(g.ws, "README.md")); string(b) != "orbit\n" {
		t.Errorf("workspace README = %q", b)
	}
	if got := g.run(g.ws, "status", "--porcelain"); got != "?? old/" {
		t.Errorf("workspace status = %q", got)
	}
}

func TestCloneNeverOverwritesTheWorkspace(t *testing.T) {
	// A file the clone would replace stops it, untouched. The chat says
	// why at once instead of waiting out cloneWait, and the next message
	// tries again.
	f, sp := chatFixture(t)
	g := newGitSandbox(t, f)
	g.uncloned()
	readme := filepath.Join(g.ws, "README.md")
	os.WriteFile(readme, []byte("mine\n"), 0o644)
	ctx := context.Background()
	s, err := f.app.NewChat(ctx, "orbit", "claude", NewOptions{OwnBranch: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.app.ChatSend(ctx, s.ID, "hello"); err != nil {
		t.Fatal(err)
	}
	var msg string
	waitUntil(t, "the turn to fail", func() bool { msg = failedTurn(f, s.ID); return msg != "" })
	if !strings.Contains(msg, "README.md") || !strings.Contains(msg, "zaneGittins/orbit") {
		t.Errorf("error = %q", msg)
	}
	if b, _ := os.ReadFile(readme); string(b) != "mine\n" {
		t.Errorf("README = %q", b)
	}
	if entries, _ := os.ReadDir(g.ws); len(entries) != 1 {
		t.Errorf("workspace left with %v", entries)
	}
	if sp.last() != nil {
		t.Error("the harness started without a worktree")
	}

	os.Remove(readme)
	if err := f.app.ChatSend(ctx, s.ID, "again"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the turn", func() bool {
		x, _ := f.app.store.Session(s.ID)
		return x.Resume == "conv-1" && x.State == store.ChatIdle
	})
	if got := g.read(s.ID, "README.md"); got != "orbit\n" {
		t.Errorf("worktree README = %q", got)
	}
}

func TestOwnBranchWithoutARepoFailsAtOnce(t *testing.T) {
	f := newFixtureWith(t, `{
	  "data_dir": %q,
	  "users": ["zane@example.com"],
	  "sandbox": {"image": "skybreaker-sandbox:test"},
	  "projects": {"orbit": {"harnesses": ["claude"]}}
	}`)
	f.app.spawn = (&fakeSpawn{}).spawn
	f.app.readyPoll = time.Millisecond
	t.Cleanup(f.app.CloseChats)
	f.docker.set(func() {
		f.docker.execOut = func(args []string) (string, error) {
			if len(args) > 2 && args[2] == presentScript {
				return "no", nil
			}
			return "", nil
		}
	})
	ctx := context.Background()
	s, err := f.app.NewChat(ctx, "orbit", "claude", NewOptions{OwnBranch: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.app.ChatSend(ctx, s.ID, "hello"); err != nil {
		t.Fatal(err)
	}
	var msg string
	waitUntil(t, "the turn to fail", func() bool { msg = failedTurn(f, s.ID); return msg != "" })
	if !strings.Contains(msg, "no repository") {
		t.Errorf("error = %q", msg)
	}
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

func TestEndedChatCarriesOn(t *testing.T) {
	f, sp := chatFixture(t)
	g := newGitSandbox(t, f)
	ctx := context.Background()
	s := startedChat(t, f, NewOptions{OwnBranch: true})
	os.WriteFile(filepath.Join(g.dir(s.ID), "main.go"), []byte("package main // done\n"), 0o644)
	g.run(g.dir(s.ID), "commit", "--quiet", "-am", "done")
	if err := f.app.KillSession(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	f.app.bg.Wait()
	if _, err := os.Stat(g.dir(s.ID)); !os.IsNotExist(err) {
		t.Fatal("worktree not tidied")
	}

	if err := f.app.ChatSend(ctx, s.ID, "more"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the resumed turn", func() bool { return turnsDone(f, s.ID) == 2 })
	x, _ := f.app.store.Session(s.ID)
	if x.Status != store.StatusRunning || x.Ended != nil || x.WorktreeGone {
		t.Errorf("session = %+v", x)
	}
	if !slices.Contains(sp.last(), "conv-1") {
		t.Errorf("harness didn't resume the conversation: %q", sp.last())
	}
	if g.read(s.ID, "main.go") != "package main // done\n" {
		t.Errorf("worktree has %q", g.read(s.ID, "main.go"))
	}
}

// turnsDone counts a chat's turns that have ended.
func turnsDone(f *fixture, id string) (n int) {
	ev, _ := f.app.ChatEvents(id, 0)
	for _, e := range ev {
		if e.Kind == chat.KindTurn && e.Turn != nil && e.Turn.State != chat.TurnWorking {
			n++
		}
	}
	return n
}

func TestReopenChat(t *testing.T) {
	f, _ := chatFixture(t)
	ctx := context.Background()
	s, _ := f.app.NewChat(ctx, "orbit", "claude", NewOptions{})
	if err := f.app.KillSession(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	ended, _ := f.app.store.Session(s.ID)
	x, err := f.app.ReopenChat(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Back as it was, in its place among the others.
	if x.Status != store.StatusRunning || x.Ended != nil || !x.Updated.Equal(ended.Updated) || !x.Seen.Equal(ended.Seen) {
		t.Errorf("reopened = %+v", x)
	}
	if y, _ := f.app.store.Session(s.ID); y.Status != store.StatusRunning {
		t.Errorf("stored = %+v", y)
	}
	if y, err := f.app.ReopenChat(ctx, s.ID); err != nil || y.Status != store.StatusRunning {
		t.Errorf("reopening a live chat: %+v, %v", y, err)
	}
	if _, err := f.app.ReopenChat(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown session: %v", err)
	}

	f.app.KillSession(ctx, s.ID)
	f.app.bg.Wait() // the sandbox's warm start reads the config
	f.app.cfg.Projects["orbit"].Harnesses = []string{"codex"}
	if _, err := f.app.ReopenChat(ctx, s.ID); err == nil {
		t.Error("reopened a chat whose harness the project no longer enables")
	}
}

func TestReopenRefusedWhileItsWorktreeIsInUse(t *testing.T) {
	// A cleared chat's successor carries on in its worktree.
	f, _ := chatFixture(t)
	newGitSandbox(t, f)
	ctx := context.Background()
	a := startedChat(t, f, NewOptions{OwnBranch: true})
	b, err := f.app.NewChat(ctx, "orbit", "claude", NewOptions{Continues: a.ID})
	if err != nil {
		t.Fatal(err)
	}
	f.app.KillSession(ctx, a.ID)
	if _, err := f.app.ReopenChat(ctx, a.ID); !errors.Is(err, ErrBusy) {
		t.Errorf("reopen while %s works there: %v", b.ID, err)
	}
	if err := f.app.ChatSend(ctx, a.ID, "more"); !errors.Is(err, ErrBusy) {
		t.Errorf("send while %s works there: %v", b.ID, err)
	}
	f.app.KillSession(ctx, b.ID)
	if _, err := f.app.ReopenChat(ctx, a.ID); err != nil {
		t.Error(err)
	}
}

func TestUndoBeforeTidyKeepsTheWork(t *testing.T) {
	f, _ := chatFixture(t)
	g := newGitSandbox(t, f)
	ctx := context.Background()
	s := startedChat(t, f, NewOptions{OwnBranch: true})
	os.WriteFile(filepath.Join(g.dir(s.ID), "wip.txt"), []byte("wip\n"), 0o644)
	f.app.KillSession(ctx, s.ID)
	if _, err := f.app.ReopenChat(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	f.app.bg.Wait()
	if g.read(s.ID, "wip.txt") != "wip\n" {
		t.Errorf("worktree has wip.txt %q", g.read(s.ID, "wip.txt"))
	}
	if x, _ := f.app.store.Session(s.ID); x.WorktreeGone || x.Status != store.StatusRunning {
		t.Errorf("session = %+v", x)
	}
}

// landOnMain lands commits on GitHub's main from a fresh clone: f runs
// there with the worktree's HEAD fetched as FETCH_HEAD.
func (g *gitSandbox) landOnMain(id string, f func(dir string)) {
	g.t.Helper()
	tmp := g.t.TempDir()
	g.run("", "clone", "--quiet", g.origin, tmp)
	if id != "" {
		g.run(tmp, "fetch", "--quiet", g.dir(id), "HEAD")
	}
	f(tmp)
	g.run(tmp, "push", "--quiet", "origin", "main")
}

// squashMerge lands a worktree's commits on main as one commit, as
// GitHub's squash and merge does.
func (g *gitSandbox) squashMerge(id string) {
	g.landOnMain(id, func(dir string) {
		g.run(dir, "merge", "--quiet", "--squash", "FETCH_HEAD")
		g.run(dir, "commit", "--quiet", "-m", "squashed")
	})
}

func TestMergedChatsEnd(t *testing.T) {
	f, _ := chatFixture(t)
	g := newGitSandbox(t, f)
	ctx := context.Background()
	commit := func(s store.Session, name string) {
		t.Helper()
		os.WriteFile(filepath.Join(g.dir(s.ID), name), []byte(s.ID+"\n"), 0o644)
		g.run(g.dir(s.ID), "add", "-A")
		g.run(g.dir(s.ID), "commit", "--quiet", "-m", name)
	}
	push := func(s store.Session) { t.Helper(); g.run(g.dir(s.ID), "push", "--quiet", "origin", "HEAD") }
	// Quiet for a while, and its last turn seen.
	settle := func(s store.Session) {
		f.app.store.UpdateSession(s.ID, func(x *store.Session) {
			x.Updated = time.Now().UTC().Add(-time.Hour)
			x.Seen = time.Now().UTC()
		})
	}
	type want struct {
		name  string
		s     store.Session
		ended bool
	}
	var cases []want
	chatFor := func(name string, ended bool) store.Session {
		s := startedChat(t, f, NewOptions{OwnBranch: true})
		cases = append(cases, want{name, s, ended})
		return s
	}

	squashed := chatFor("squash merged", true)
	commit(squashed, "a.txt")
	commit(squashed, "b.txt")
	push(squashed)
	g.squashMerge(squashed.ID)

	merged := chatFor("merged", true)
	commit(merged, "c.txt")
	push(merged)
	g.run(g.dir(merged.ID), "push", "--quiet", "origin", "HEAD:main")

	// Main moves on, then GitHub's rebase and merge copies each commit.
	rebased := chatFor("rebase merged", true)
	commit(rebased, "d1.txt")
	commit(rebased, "d2.txt")
	push(rebased)
	g.landOnMain("", func(dir string) { g.run(dir, "commit", "--quiet", "--allow-empty", "-m", "elsewhere") })
	g.landOnMain(rebased.ID, func(dir string) { g.run(dir, "cherry-pick", "--quiet", "HEAD..FETCH_HEAD") })

	open := chatFor("unmerged", false)
	commit(open, "e.txt")
	push(open)

	chatFor("no commits", false)

	dirty := chatFor("uncommitted work", false)
	commit(dirty, "f.txt")
	push(dirty)
	g.squashMerge(dirty.ID)
	os.WriteFile(filepath.Join(g.dir(dirty.ID), "more.txt"), []byte("wip\n"), 0o644)

	unpushed := chatFor("a commit not pushed", false)
	commit(unpushed, "g.txt")
	push(unpushed)
	g.squashMerge(unpushed.ID)
	commit(unpushed, "g2.txt")

	local := chatFor("never pushed", false)
	commit(local, "h.txt")
	g.squashMerge(local.ID)

	// After its merge the agent starts more work from main.
	followup := chatFor("on a new branch from main", false)
	commit(followup, "i.txt")
	push(followup)
	g.squashMerge(followup.ID)
	g.run(g.ws, "fetch", "--quiet", "origin")
	g.run(g.dir(followup.ID), "switch", "--quiet", "-c", "next", "origin/main")

	// Done and undone: nothing to land, whatever main has.
	reverted := chatFor("no change in all", false)
	commit(reverted, "j.txt")
	g.run(g.dir(reverted.ID), "revert", "--quiet", "--no-edit", "HEAD")
	push(reverted)
	g.landOnMain("", func(dir string) { g.run(dir, "commit", "--quiet", "--allow-empty", "-m", "nothing") })

	unseen := chatFor("unseen", false)
	commit(unseen, "k.txt")
	push(unseen)
	g.squashMerge(unseen.ID)

	recent := chatFor("recently active", false)
	commit(recent, "l.txt")
	push(recent)
	g.squashMerge(recent.ID)

	// It moves on while git looks.
	busy := chatFor("busy since", false)
	commit(busy, "m.txt")
	push(busy)
	g.squashMerge(busy.ID)

	for _, c := range cases {
		if c.s.ID != recent.ID {
			settle(c.s)
		}
	}
	f.app.store.UpdateSession(unseen.ID, func(x *store.Session) { x.Seen = x.Updated.Add(-time.Minute) })
	f.docker.set(func() {
		f.docker.execOut = func(args []string) (string, error) {
			if len(args) > 4 && args[2] == mergedScript && args[4] == busy.Worktree {
				f.app.store.UpdateSession(busy.ID, func(x *store.Session) { x.State, x.Updated = store.ChatWorking, time.Now().UTC() })
			}
			return g.exec(args)
		}
	})

	f.app.EndMerged(ctx)
	f.app.bg.Wait()
	for _, c := range cases {
		x, _ := f.app.store.Session(c.s.ID)
		if got := x.Status != store.StatusRunning; got != c.ended {
			t.Errorf("%s chat: status %q", c.name, x.Status)
		}
		ev, _ := f.app.ChatEvents(c.s.ID, 0)
		noted := len(ev) > 0 && ev[len(ev)-1].Kind == chat.KindNotice && strings.Contains(ev[len(ev)-1].Text, "merged")
		if noted != c.ended {
			t.Errorf("%s chat: noted %v", c.name, noted)
		}
	}
}

func TestMergedSkipsWhatItCantCheck(t *testing.T) {
	f, _ := chatFixture(t)
	g := newGitSandbox(t, f)
	ctx := context.Background()
	s := startedChat(t, f, NewOptions{OwnBranch: true})
	os.WriteFile(filepath.Join(g.dir(s.ID), "a.txt"), []byte("a\n"), 0o644)
	g.run(g.dir(s.ID), "add", "-A")
	g.run(g.dir(s.ID), "commit", "--quiet", "-m", "a")
	g.run(g.dir(s.ID), "push", "--quiet", "origin", "HEAD")
	if f.app.merged(ctx, s, "main") {
		t.Error("unmerged branch taken for merged")
	}
	// A project branch GitHub doesn't have.
	if f.app.merged(ctx, s, "nope") {
		t.Error("merged into a branch that isn't there")
	}
	g.squashMerge(s.ID)
	g.run(g.ws, "fetch", "--quiet", "origin")
	if !f.app.merged(ctx, s, "main") {
		t.Fatal("squash merge missed")
	}
	// A stopped sandbox isn't started to look.
	f.app.store.UpdateSession(s.ID, func(x *store.Session) { x.Updated, x.Seen = time.Now().UTC().Add(-time.Hour), time.Now().UTC() })
	if err := f.app.StopSandbox(ctx, "orbit"); err != nil {
		t.Fatal(err)
	}
	fetches := func() int { return len(f.docker.find(func(c []string) bool { return slices.Contains(c, "fetch") })) }
	before := fetches()
	f.app.EndMerged(ctx)
	if x, _ := f.app.store.Session(s.ID); x.Status != store.StatusRunning {
		t.Errorf("ended with its sandbox stopped: %q", x.Status)
	}
	if fetches() != before {
		t.Error("fetched for a stopped sandbox")
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
