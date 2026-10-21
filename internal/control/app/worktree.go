package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/zaneGittins/skybreaker/internal/control/config"
	"github.com/zaneGittins/skybreaker/internal/control/sandbox"
	"github.com/zaneGittins/skybreaker/internal/control/store"
	"github.com/zaneGittins/skybreaker/internal/control/tmux"
)

// A session can work in a git worktree of its own, on a branch of its own,
// so agents side by side in one project never touch each other's files.
// The worktree lives in the sandbox under sandbox.WorktreesDir, named after
// the session that made it; the branch is skybreaker/<that session>, until
// the agent renames it to ship. Git runs only in the sandbox.
//
// A worktree is made on the session's first start and stays while any
// session working in it is running. Once they've all ended it's tidied: a
// clean one goes at once; one with uncommitted work or ignored files stays
// for the keepDirty most recently ended, and beyond that its uncommitted
// work is kept in a commit under refs/skybreaker/wip/ first (ignored files
// go). The branch always stays, unless nothing was ever committed on it.

// workspaceDir is the project's own clone, where sessions work by default.
const workspaceDir = "/workspace"

const (
	branchPrefix = "skybreaker/"
	wipPrefix    = "refs/skybreaker/wip/"
	// keepDirty is how many ended sessions' worktrees with uncommitted work
	// or ignored files stay on disk, newest first.
	keepDirty = 10
	// cloneWait bounds how long a first start waits for the workspace's
	// clone, which a new sandbox starts in the background.
	cloneWait = 5 * time.Minute
)

// ErrNoWorktrees is a sandbox started before sessions could have their own
// worktree: its container has no mount for them.
var ErrNoWorktrees = errors.New("this project's sandbox started before chats could have a branch of their own: stop and start the sandbox, then try again")

// workdir is where a session's harness runs.
func workdir(s store.Session) string {
	if s.Worktree != "" {
		return s.Worktree
	}
	return workspaceDir
}

func worktreeOf(id string) string { return sandbox.WorktreesDir + "/" + id }

// ownBranch is the branch a worktree gets when it's made.
func ownBranch(dir string) string { return branchPrefix + path.Base(dir) }

var shaRe = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// validRef accepts a branch name or a commit ID read back from the
// sandbox, before it goes to git again: nothing git reads as an option or a
// revision expression.
func validRef(r string) bool { return shaRe.MatchString(r) || config.ValidBranch(r) }

// gitFailed words a failed git command for the user. What git printed is
// the sandbox's to choose, so only one line is kept, cut short: git's last
// error line, past any progress it printed first, else the first line.
func gitFailed(op string, err error) error {
	lines := strings.Split(err.Error(), "\n")
	msg := strings.TrimSpace(lines[0])
	for _, l := range slices.Backward(lines) {
		if i := max(strings.Index(l, "fatal: "), strings.Index(l, "error: ")); i >= 0 {
			msg = strings.TrimSpace(l[i:])
			break
		}
	}
	if r := []rune(msg); len(r) > 300 {
		msg = string(r[:300]) + "…"
	}
	return fmt.Errorf("%s: %s", op, msg)
}

// git runs git in a directory of a project's sandbox and returns its output,
// trimmed.
func (a *App) git(ctx context.Context, project, dir string, args ...string) (string, error) {
	out, err := a.sandbox.Exec(ctx, project, GitArgs(dir, args...)...)
	return strings.TrimSpace(string(out)), err
}

// commit resolves a ref to a commit ID, or "" if it names none.
func (a *App) commit(ctx context.Context, project, dir, ref string) string {
	if !validRef(ref) && !strings.HasPrefix(ref, wipPrefix) {
		return ""
	}
	out, err := a.git(ctx, project, dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil || !shaRe.MatchString(out) {
		return ""
	}
	return out
}

const presentScript = `if [ -e "$1" ]; then echo yes; else echo no; fi`

// present reports whether a path exists in the sandbox. A command that
// couldn't run (a stopped sandbox, a deadline) is an error, never "no".
func (a *App) present(ctx context.Context, project, p string) (bool, error) {
	out, err := a.sandbox.Exec(ctx, project, "sh", "-c", presentScript, "sh", p)
	switch strings.TrimSpace(string(out)) {
	case "yes":
		return true, nil
	case "no":
		return false, nil
	}
	if err == nil {
		err = errors.New("unexpected answer from the sandbox")
	}
	return false, err
}

// holdsWorktrees reports whether a running sandbox has the mount for
// worktrees, which one started by an earlier Skybreaker lacks.
func (a *App) holdsWorktrees(ctx context.Context, project string) bool {
	_, err := a.sandbox.Exec(ctx, project, "test", "-d", sandbox.WorktreesDir)
	return err == nil
}

// waitClone waits for the workspace's clone, which a sandbox's first start
// makes in the background, or clones it now if none is under way: the
// last one failed, or the workspace was emptied since. A failed clone
// says why at once.
func (a *App) waitClone(ctx context.Context, project string) error {
	if ok, err := a.present(ctx, project, workspaceDir+"/.git"); err != nil || ok {
		return err
	}
	p, err := a.project(project)
	if err != nil {
		return err
	}
	if p.Repo == "" {
		return errors.New("this project has no repository, so there's no branch to work on")
	}
	ctx, cancel := context.WithTimeout(ctx, cloneWait)
	defer cancel()
	c := a.clone(project, p.Repo)
	select {
	case <-c.done:
		return c.err
	case <-ctx.Done():
		return errors.New("the workspace hasn't been cloned yet, so there's no branch to work on")
	}
}

// fetch brings the project's branch up to date from GitHub, as best it can.
func (a *App) fetch(ctx context.Context, project string) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	a.git(ctx, project, workspaceDir, "fetch", "--quiet", "origin")
}

// branchStart is the commit a new branch starts at: the tip of the project's
// branch on GitHub, as last fetched, or the workspace's HEAD without one.
func (a *App) branchStart(ctx context.Context, project string) (string, error) {
	p, err := a.project(project)
	if err != nil {
		return "", err
	}
	if sha := a.commit(ctx, project, workspaceDir, "origin/"+p.Branch); sha != "" {
		return sha, nil
	}
	if sha := a.commit(ctx, project, workspaceDir, "HEAD"); sha != "" {
		return sha, nil
	}
	return "", errors.New("the workspace has no commits to start a branch from")
}

// ensureWorktree makes the worktree session s works in, if it has one that
// isn't there: new, or tidied away. Its branch comes back, with any work
// kept for it; s gets the branch it's on. then, if set, runs once the
// worktree is there, before tidying can look at it: a session saved only
// once its terminal runs is never taken for an orphan.
func (a *App) ensureWorktree(ctx context.Context, s *store.Session, then func() error) error {
	if then == nil {
		then = func() error { return nil }
	}
	if s.Worktree == "" {
		return then()
	}
	dir := s.Worktree
	ok, err := a.present(ctx, s.Project, dir+"/.git")
	if err != nil {
		return err
	}
	if ok {
		return then()
	}
	// The slow parts come before the lock, so tidying never waits on them.
	if !a.holdsWorktrees(ctx, s.Project) {
		return ErrNoWorktrees
	}
	if err := a.waitClone(ctx, s.Project); err != nil {
		return err
	}
	a.fetch(ctx, s.Project)
	defer a.lockProject(s.Project + "/worktrees")()
	if ok, err := a.present(ctx, s.Project, dir+"/.git"); err != nil || ok {
		if err != nil {
			return err
		}
		return then()
	}
	// A worktree whose directory went is still registered, and holds its
	// branch, until pruned.
	a.git(ctx, s.Project, workspaceDir, "worktree", "prune")
	own := ownBranch(dir)
	branch, from := s.Branch, s.BranchFrom
	switch {
	case branch != "" && !shaRe.MatchString(branch) && a.commit(ctx, s.Project, workspaceDir, branch) != "":
		if _, err := a.git(ctx, s.Project, workspaceDir, "worktree", "add", dir, branch); err != nil {
			return gitFailed("git worktree add", err)
		}
	case a.commit(ctx, s.Project, workspaceDir, own) != "":
		branch = own
		if _, err := a.git(ctx, s.Project, workspaceDir, "worktree", "add", dir, own); err != nil {
			return gitFailed("git worktree add", err)
		}
	default:
		// A new branch: where the old one ended if it was deleted, else
		// where it began, else the project's branch now.
		start := ""
		for _, ref := range []string{branch, from} {
			if start = a.commit(ctx, s.Project, workspaceDir, ref); start != "" {
				break
			}
		}
		if start == "" {
			if start, err = a.branchStart(ctx, s.Project); err != nil {
				return err
			}
		}
		if from == "" {
			from = start
		}
		branch = own
		if _, err := a.git(ctx, s.Project, workspaceDir, "worktree", "add", "-b", own, dir, start); err != nil {
			return gitFailed("git worktree add", err)
		}
	}
	wip := wipPrefix + path.Base(dir)
	if state := a.commit(ctx, s.Project, workspaceDir, wip); state != "" {
		if err := a.restoreState(ctx, s.Project, dir, state); err != nil {
			// Undone, so the next start tries again; the branch and the
			// kept work stay.
			a.git(ctx, s.Project, workspaceDir, "worktree", "remove", "--force", dir)
			return err
		}
		a.git(ctx, s.Project, workspaceDir, "update-ref", "-d", wip)
	}
	s.Branch, s.BranchFrom, s.WorktreeGone = branch, from, false
	a.store.UpdateSession(s.ID, func(x *store.Session) { x.Branch, x.BranchFrom, x.WorktreeGone = branch, from, false })
	return then()
}

// forkWorktree gives fork f a worktree of its own, on a new branch that
// starts where s's work is now.
func (a *App) forkWorktree(ctx context.Context, s store.Session, f *store.Session) error {
	if err := a.StartSandbox(ctx, s.Project); err != nil {
		return err
	}
	if err := a.waitReady(ctx, s.Project); err != nil {
		return err
	}
	if !a.holdsWorktrees(ctx, s.Project) {
		return ErrNoWorktrees
	}
	defer a.lockProject(s.Project + "/worktrees")()
	from, state, err := a.forkPoint(ctx, s)
	if err != nil {
		return err
	}
	dir := worktreeOf(f.ID)
	if _, err := a.git(ctx, s.Project, workspaceDir, "worktree", "add", "-b", ownBranch(dir), dir, from); err != nil {
		return gitFailed("git worktree add", err)
	}
	f.Worktree, f.Branch, f.BranchFrom = dir, ownBranch(dir), from
	if state != from {
		if err := a.restoreState(ctx, s.Project, dir, state); err != nil {
			a.removeNew(ctx, *f)
			return err
		}
	}
	return nil
}

// dropWorktree removes the worktree just made for a session that then
// couldn't be saved.
func (a *App) dropWorktree(s store.Session) {
	if s.Worktree == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	defer a.lockProject(s.Project + "/worktrees")()
	a.removeNew(ctx, s)
}

func (a *App) removeNew(ctx context.Context, s store.Session) {
	a.git(ctx, s.Project, workspaceDir, "worktree", "remove", "--force", s.Worktree)
	a.git(ctx, s.Project, workspaceDir, "branch", "-D", ownBranch(s.Worktree))
}

// restoreState makes a worktree's files a snapshot commit's, leaving its
// index at HEAD: what was uncommitted stays uncommitted, and what was new
// stays untracked.
func (a *App) restoreState(ctx context.Context, project, dir, state string) error {
	if _, err := a.git(ctx, project, dir, "restore", "--source="+state, "--worktree", "--", "."); err != nil {
		return gitFailed("git restore", err)
	}
	return nil
}

// snapshotScript prints a worktree's HEAD and a commit of everything in it,
// uncommitted and untracked work included (but not what .gitignore keeps
// out), or HEAD twice when there's nothing uncommitted. It works on a copy
// of the index, so the worktree and its index are left as they were. The
// directory is an argument, never part of the script.
const snapshotScript = `set -e
wt=$1
g() { git -C "$wt" -c core.fsmonitor=false -c core.hooksPath=/dev/null --no-pager "$@"; }
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
head=$(g rev-parse --verify HEAD)
export GIT_INDEX_FILE="$tmp/index"
# The worktree's own index as a start, for its record of what's unchanged
# on disk; git hashes only the rest.
cp "$(g rev-parse --path-format=absolute --git-path index)" "$GIT_INDEX_FILE" 2>/dev/null || g read-tree "$head" >/dev/null
g add -A >/dev/null
tree=$(g write-tree)
if [ "$tree" = "$(g rev-parse "$head^{tree}")" ]; then echo "$head $head"; exit 0; fi
export GIT_AUTHOR_NAME=Skybreaker GIT_AUTHOR_EMAIL=skybreaker@localhost GIT_COMMITTER_NAME=Skybreaker GIT_COMMITTER_EMAIL=skybreaker@localhost
echo "$head $(g commit-tree -p "$head" -m "Uncommitted work, kept by Skybreaker" "$tree")"`

// snapshot returns a worktree's HEAD and a commit of all its files.
func (a *App) snapshot(ctx context.Context, project, dir string) (head, state string, err error) {
	out, err := a.sandbox.Exec(ctx, project, "sh", "-c", snapshotScript, "sh", dir)
	if err != nil {
		return "", "", gitFailed("snapshot the worktree", err)
	}
	f := strings.Fields(string(out))
	if len(f) != 2 || !shaRe.MatchString(f[0]) || !shaRe.MatchString(f[1]) {
		return "", "", errors.New("snapshot the worktree: unexpected output from git")
	}
	return f[0], f[1], nil
}

// forkPoint is where a fork of session s starts: the commit its branch is at
// and a snapshot of its files, from its worktree if it's still there, else
// from what was kept when it was tidied.
func (a *App) forkPoint(ctx context.Context, s store.Session) (from, state string, err error) {
	if ok, err := a.present(ctx, s.Project, s.Worktree+"/.git"); err != nil {
		return "", "", err
	} else if ok {
		return a.snapshot(ctx, s.Project, s.Worktree)
	}
	wip := wipPrefix + path.Base(s.Worktree)
	if state := a.commit(ctx, s.Project, workspaceDir, wip); state != "" {
		if parent := a.commit(ctx, s.Project, workspaceDir, wip+"^"); parent != "" {
			return parent, state, nil
		}
	}
	for _, ref := range []string{s.Branch, s.BranchFrom} {
		if sha := a.commit(ctx, s.Project, workspaceDir, ref); sha != "" {
			return sha, sha, nil
		}
	}
	return "", "", errors.New("the original chat's branch is gone, so there's nothing to fork from")
}

// tidyTimeout bounds tidying one project's worktrees, once it has their
// lock. Its repo config can make git slow (a filter, say), and that mustn't
// hold up the others.
const tidyTimeout = 2 * time.Minute

// TidyWorktrees tidies the worktrees of every running sandbox.
func (a *App) TidyWorktrees(ctx context.Context) {
	for _, project := range slices.Sorted(maps.Keys(a.cfg.Projects)) {
		sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		st, err := a.sandbox.Status(sctx, project)
		cancel()
		if err == nil && st.Running {
			a.tidyWorktrees(ctx, project, true)
		}
	}
}

// tidyWorktrees tidies a project's worktrees whose sessions have all
// ended, and with orphans, the ones no session knows any more. Anything it
// can't check, it leaves for next time.
func (a *App) tidyWorktrees(ctx context.Context, project string, orphans bool) {
	defer a.lockProject(project + "/worktrees")()
	ctx, cancel := context.WithTimeout(ctx, tidyTimeout)
	defer cancel()
	type dirUse struct {
		live     bool
		ended    time.Time
		from     string
		sessions []store.Session
	}
	uses := map[string]*dirUse{}
	for _, s := range a.store.ListSessions(project) {
		if s.Worktree == "" {
			continue
		}
		u := uses[s.Worktree]
		if u == nil {
			u = &dirUse{}
			uses[s.Worktree] = u
		}
		u.sessions = append(u.sessions, s)
		if u.from == "" {
			u.from = s.BranchFrom
		}
		if s.Status == store.StatusRunning {
			u.live = true
		} else if s.Ended != nil && s.Ended.After(u.ended) {
			u.ended = *s.Ended
		}
	}
	var ended []string
	for dir, u := range uses {
		// Gone for every session in it, unless one has since made it again.
		if !u.live && slices.ContainsFunc(u.sessions, func(s store.Session) bool { return !s.WorktreeGone }) {
			ended = append(ended, dir)
		}
	}
	// Newest first, so the ones kept are the latest.
	slices.SortFunc(ended, func(x, y string) int { return uses[y].ended.Compare(uses[x].ended) })
	kept := 0
	for _, dir := range ended {
		gone := func(branch string) {
			for _, s := range uses[dir].sessions {
				a.store.UpdateSession(s.ID, func(x *store.Session) {
					x.WorktreeGone = true
					if branch != "" {
						x.Branch = branch
					}
				})
			}
		}
		ok, err := a.present(ctx, project, dir+"/.git")
		if err != nil {
			continue
		}
		if !ok {
			gone("")
			continue
		}
		changed, ignored, err := a.worktreeState(ctx, project, dir)
		if err != nil {
			continue
		}
		// Ignored files (a .env, dependencies) can't be kept in a commit,
		// so a worktree with them stays like a dirty one.
		if changed || ignored {
			if kept++; kept <= keepDirty {
				continue
			}
		}
		branch, err := a.removeWorktree(ctx, project, dir, changed, changed || ignored, uses[dir].from)
		if err != nil {
			log.Printf("skybreaker: tidy worktree %s in %s: %v", dir, project, err)
			continue
		}
		gone(branch)
	}
	if !orphans {
		return
	}
	out, err := a.sandbox.Exec(ctx, project, "ls", "-1", sandbox.WorktreesDir)
	if err != nil {
		return
	}
	for _, name := range strings.Fields(string(out)) {
		dir := worktreeOf(name)
		if !tmux.ValidName(name) || uses[dir] != nil {
			continue
		}
		if ok, err := a.present(ctx, project, dir+"/.git"); err != nil || !ok {
			continue
		}
		if _, err := a.removeWorktree(ctx, project, dir, true, true, ""); err != nil {
			log.Printf("skybreaker: tidy worktree %s in %s: %v", dir, project, err)
		}
	}
}

// worktreeState reports whether a worktree has uncommitted work (changed
// or untracked files) and whether it has ignored files.
func (a *App) worktreeState(ctx context.Context, project, dir string) (changed, ignored bool, err error) {
	out, err := a.git(ctx, project, dir, "status", "--porcelain", "--ignored")
	if err != nil {
		return false, false, err
	}
	for line := range strings.SplitSeq(out, "\n") {
		switch {
		case strings.HasPrefix(line, "!! "):
			ignored = true
		case line != "":
			changed = true
		}
	}
	return changed, ignored, nil
}

// removeWorktree removes a worktree, first keeping any uncommitted work, if
// keep, in a commit under refs/skybreaker/wip/, and returns the branch it
// was on. A detached HEAD is kept on the worktree's own branch first. Force
// removes it with what git can't keep (ignored files). The own branch goes
// too if nothing was ever committed on it, from where it started.
func (a *App) removeWorktree(ctx context.Context, project, dir string, keep, force bool, from string) (string, error) {
	own := ownBranch(dir)
	branch, err := a.git(ctx, project, dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", gitFailed("read the worktree's branch", err)
	}
	if branch == "HEAD" {
		// Commits on a detached HEAD would be kept by nothing once the
		// worktree goes.
		head := a.commit(ctx, project, dir, "HEAD")
		if head == "" {
			return "", errors.New("read the worktree's HEAD")
		}
		if _, err := a.git(ctx, project, workspaceDir, "branch", "-f", own, head); err != nil {
			return "", gitFailed("keep the worktree's commits", err)
		}
		branch = own
	}
	if !validRef(branch) {
		branch = ""
	}
	if keep {
		head, state, err := a.snapshot(ctx, project, dir)
		if err != nil {
			return "", err
		}
		if state != head {
			wip := wipPrefix + path.Base(dir)
			if a.commit(ctx, project, workspaceDir, wip) != "" {
				return "", errors.New("work kept for it earlier was never restored")
			}
			if _, err := a.git(ctx, project, workspaceDir, "update-ref", wip, state); err != nil {
				return "", gitFailed("keep uncommitted work", err)
			}
		}
	}
	args := []string{"worktree", "remove", dir}
	if force {
		args = []string{"worktree", "remove", "--force", dir}
	}
	if _, err := a.git(ctx, project, workspaceDir, args...); err != nil {
		return "", gitFailed("git worktree remove", err)
	}
	if from != "" && a.commit(ctx, project, workspaceDir, own) == from {
		// Never checked out elsewhere: git refuses to delete one that is.
		a.git(ctx, project, workspaceDir, "branch", "-D", own)
		if branch == own {
			branch = from
		}
	}
	return branch, nil
}
