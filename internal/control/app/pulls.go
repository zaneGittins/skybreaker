package app

import (
	"context"
	"fmt"
	"log"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/zaneGittins/skybreaker/internal/control/config"
	"github.com/zaneGittins/skybreaker/internal/control/store"
	"github.com/zaneGittins/skybreaker/internal/notify"
)

// maxPullRounds is how many follow-ups a chat gets without a word from you,
// so an agent that can't make CI pass doesn't loop on it.
const maxPullRounds = 3

// maxCommentText bounds each comment passed on.
const maxCommentText = 2000

var commitRe = regexp.MustCompile(`^[0-9a-f]{1,64}$`)

type ghPull struct {
	Number  int       `json:"number"`
	URL     string    `json:"html_url"`
	Created time.Time `json:"created_at"`
	Head    struct {
		SHA string `json:"sha"`
	} `json:"head"`
}

type ghComment struct {
	User struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"user"`
	Association string    `json:"author_association"`
	Body        string    `json:"body"`
	Path        string    `json:"path"`
	Line        int       `json:"line"`
	State       string    `json:"state"`
	Created     time.Time `json:"created_at"`
	Submitted   time.Time `json:"submitted_at"`
}

// FollowPulls passes on what's new on each chat's open pull request: checks
// that finished failing, and comments from people with write access. A
// chat gets them when idle, up to maxPullRounds times before you reply.
func (a *App) FollowPulls(ctx context.Context) {
	if a.github == nil {
		return
	}
	for _, project := range slices.Sorted(maps.Keys(a.cfg.Projects)) {
		p, err := a.repoProject(project)
		if err != nil || p.Repo == "" {
			continue
		}
		var due []store.Session
		for _, s := range a.store.ListSessions(project) {
			if s.Mode == store.ModeChat && s.Status == store.StatusRunning && s.Worktree != "" && !s.WorktreeGone && s.Terminal == "" {
				due = append(due, s)
			}
		}
		if len(due) == 0 {
			continue
		}
		if st, err := a.sandbox.Status(ctx, project); err != nil || !st.Running {
			continue
		}
		for _, s := range due {
			if err := a.followPull(ctx, p, s); err != nil {
				a.logPullErr(project, err)
				break
			}
		}
	}
}

// logPullErr logs a failed GitHub read once, not on every round.
func (a *App) logPullErr(project string, err error) {
	a.pullErrMu.Lock()
	defer a.pullErrMu.Unlock()
	if a.pullErr == nil {
		a.pullErr = map[string]string{}
	}
	if a.pullErr[project] != err.Error() {
		a.pullErr[project] = err.Error()
		log.Printf("skybreaker: follow pull requests in %s: %v", project, err)
	}
}

func (a *App) followPull(ctx context.Context, p *config.Project, s store.Session) error {
	branch, err := a.git(ctx, s.Project, s.Worktree, "symbolic-ref", "-q", "--short", "HEAD")
	if err != nil || !config.ValidBranch(branch) {
		return nil
	}
	owner, _, _ := strings.Cut(p.Repo, "/")
	var pulls []ghPull
	q := url.Values{"head": {owner + ":" + branch}, "state": {"open"}}
	if err := a.github.GitHubGet(ctx, p.Repo, "/pulls?"+q.Encode(), &pulls); err != nil {
		return err
	}
	if len(pulls) == 0 || pulls[0].Number <= 0 || !commitRe.MatchString(pulls[0].Head.SHA) {
		if s.Pull.Number != 0 {
			a.store.UpdateSession(s.ID, func(x *store.Session) { x.Pull = store.Pull{} })
		}
		return nil
	}
	pr := pulls[0]
	seen := s.Pull
	if seen.Number != pr.Number {
		seen = store.Pull{Number: pr.Number, URL: pr.URL, Since: pr.Created}
	}

	checked, failed := seen.Checked, []string(nil)
	if pr.Head.SHA != seen.Checked {
		f, done, err := a.failedChecks(ctx, p.Repo, pr.Head.SHA)
		if err != nil {
			return err
		}
		if done {
			checked, failed = pr.Head.SHA, f
		}
	}
	comments, since, err := a.newComments(ctx, p.Repo, pr.Number, seen.Since)
	if err != nil {
		return err
	}

	defer a.lockSession(s.ID)()
	x, ok := a.store.Session(s.ID)
	if !ok || x.Status != store.StatusRunning {
		return nil
	}
	if x.Pull.Number == pr.Number {
		seen.Rounds, seen.Paused = x.Pull.Rounds, x.Pull.Paused
	}
	save := func(pl store.Pull) { a.store.UpdateSession(s.ID, func(x *store.Session) { x.Pull = pl }) }
	if len(failed) == 0 && len(comments) == 0 {
		seen.Checked, seen.Since = checked, since
		save(seen)
		return nil
	}
	if x.State != store.ChatIdle {
		save(seen)
		return nil
	}
	if seen.Rounds >= maxPullRounds {
		if !seen.Paused {
			seen.Paused = true
			a.chat.Note(s.ID, fmt.Sprintf("Stopped passing on pull request #%d after %d rounds. Reply to carry on.", pr.Number, maxPullRounds))
			a.notifyPaused(x, pr.Number)
		}
		save(seen)
		return nil
	}
	if err := a.chat.Send(ctx, s.ID, pullMessage(pr, checked, failed, comments)); err != nil {
		save(seen)
		return nil
	}
	seen.Checked, seen.Since, seen.Rounds = checked, since, seen.Rounds+1
	save(seen)
	return nil
}

// failedChecks are the names of commit sha's failed checks, once every check
// on it has finished.
func (a *App) failedChecks(ctx context.Context, repo, sha string) (failed []string, done bool, err error) {
	var runs struct {
		CheckRuns []struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			URL        string `json:"html_url"`
		} `json:"check_runs"`
	}
	if err := a.github.GitHubGet(ctx, repo, "/commits/"+sha+"/check-runs?per_page=100", &runs); err != nil {
		return nil, false, err
	}
	var statuses struct {
		Statuses []struct {
			Context string `json:"context"`
			State   string `json:"state"`
			URL     string `json:"target_url"`
		} `json:"statuses"`
	}
	if err := a.github.GitHubGet(ctx, repo, "/commits/"+sha+"/status", &statuses); err != nil {
		return nil, false, err
	}
	// None yet is CI that hasn't started.
	if len(runs.CheckRuns) == 0 && len(statuses.Statuses) == 0 {
		return nil, false, nil
	}
	named := func(name, link string) string {
		if strings.HasPrefix(link, "https://") {
			return name + " (" + link + ")"
		}
		return name
	}
	for _, r := range runs.CheckRuns {
		if r.Status != "completed" {
			return nil, false, nil
		}
		switch r.Conclusion {
		case "failure", "timed_out", "startup_failure":
			failed = append(failed, named(r.Name, r.URL))
		}
	}
	for _, st := range statuses.Statuses {
		switch st.State {
		case "pending":
			return nil, false, nil
		case "failure", "error":
			failed = append(failed, named(st.Context, st.URL))
		}
	}
	return failed, true, nil
}

// newComments are the comments on pull request n since since from people
// with write access to the repo, and the time of the newest comment seen.
func (a *App) newComments(ctx context.Context, repo string, n int, since time.Time) ([]string, time.Time, error) {
	type comment struct {
		at   time.Time
		text string
	}
	var out []comment
	newest := since
	for _, path := range []string{"/issues/%d/comments", "/pulls/%d/comments", "/pulls/%d/reviews"} {
		var cs []ghComment
		if err := a.github.GitHubGet(ctx, repo, fmt.Sprintf(path, n)+"?per_page=100", &cs); err != nil {
			return nil, since, err
		}
		for _, c := range cs {
			at := c.Created
			if !c.Submitted.IsZero() {
				at = c.Submitted
			}
			if !at.After(since) || c.State == "PENDING" {
				continue
			}
			if at.After(newest) {
				newest = at
			}
			if !trustedAuthor(c) {
				continue
			}
			body := strings.TrimSpace(c.Body)
			if r := []rune(body); len(r) > maxCommentText {
				body = string(r[:maxCommentText]) + "..."
			}
			who := c.User.Login
			switch {
			case c.Path != "" && c.Line > 0:
				who += fmt.Sprintf(" on %s:%d", c.Path, c.Line)
			case c.Path != "":
				who += " on " + c.Path
			case c.State == "CHANGES_REQUESTED":
				who += " requested changes"
			}
			if body == "" {
				if c.State != "CHANGES_REQUESTED" {
					continue
				}
				body = "(no comment)"
			}
			out = append(out, comment{at, "- " + who + ": " + body})
		}
	}
	slices.SortStableFunc(out, func(x, y comment) int { return x.at.Compare(y.at) })
	texts := make([]string, len(out))
	for i, c := range out {
		texts[i] = c.text
	}
	return texts, newest, nil
}

// trustedAuthor is a person with write access to the repo: comments become
// prompts, so nobody else's reach the agent, nor any bot's (its own among
// them).
func trustedAuthor(c ghComment) bool {
	switch c.Association {
	case "OWNER", "MEMBER", "COLLABORATOR":
		return c.User.Type != "Bot"
	}
	return false
}

func pullMessage(pr ghPull, sha string, failed, comments []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "From GitHub, on your pull request #%d", pr.Number)
	if strings.HasPrefix(pr.URL, "https://") {
		fmt.Fprintf(&b, " (%s)", pr.URL)
	}
	b.WriteString(":\n")
	if len(failed) > 0 {
		fmt.Fprintf(&b, "\nChecks failed on %s:\n- %s\nRead the failing logs (gh run view --log-failed), fix the cause and push.\n", sha[:min(len(sha), 7)], strings.Join(failed, "\n- "))
	}
	if len(comments) > 0 {
		fmt.Fprintf(&b, "\nNew review comments from people with write access to the repo:\n%s\nAddress them and push, or say here why not.\n", strings.Join(comments, "\n"))
	}
	return strings.TrimRight(b.String(), "\n")
}

func (a *App) notifyPaused(s store.Session, n int) {
	m := notify.Message{
		ID:    fmt.Sprintf("pull-paused-%s-%d", s.ID, n),
		Title: "A pull request needs you",
		Body:  fmt.Sprintf("PR #%d in %s still needs work after %d automatic rounds.", n, s.Project, maxPullRounds),
		Click: a.uiURL + "/s/" + s.ID, Priority: 4,
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := a.notify.Notify(ctx, m); err != nil {
			log.Printf("skybreaker: notify: %v", err)
		}
	}()
}
