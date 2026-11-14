package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zaneGittins/skybreaker/internal/control/chat"
	"github.com/zaneGittins/skybreaker/internal/control/mcp"
	"github.com/zaneGittins/skybreaker/internal/control/store"
)

var workflowStages = []string{"", "implementing", "ready_for_review", "reviewing", "changes_requested", "ready_to_merge", "finished", "abandoned"}

// StartReview copies the code into an independent chat with a link back to its author.
func (a *App) StartReview(ctx context.Context, id, harness string, brief store.ReviewBrief) (store.Session, error) {
	brief = cleanReviewBrief(brief)
	if err := checkReviewBrief(brief); err != nil {
		return store.Session{}, err
	}
	defer a.lockSession(id)()
	s, err := a.chatSession(id)
	if err != nil {
		return store.Session{}, err
	}
	if _, err = a.repoProject(s.Project); err != nil {
		return store.Session{}, err
	}
	if s.State == store.ChatWorking || s.State == store.ChatWaiting || s.Terminal != "" {
		return store.Session{}, ErrBusy
	}
	if s.ReviewOf != "" {
		return store.Session{}, errors.New("start another review from the implementation chat")
	}
	if err = a.enabled(s.Project, harness); err != nil {
		return store.Session{}, err
	}
	m, ok := a.cfg.Harnesses.Get(harness)
	if !ok || m.Chat == "" {
		return store.Session{}, errors.New("choose a chat agent")
	}
	if s.ReviewID != "" {
		if previous, ok := a.store.Session(s.ReviewID); ok && (previous.State == store.ChatWorking || previous.State == store.ChatWaiting) {
			return store.Session{}, errors.New("a review is already running")
		}
	}
	from := s
	if from.Worktree == "" {
		from.Worktree = workspaceDir
	}
	now := time.Now().UTC()
	r := store.Session{ID: newSessionID(), Project: s.Project, Harness: harness, Mode: store.ModeChat, Status: store.StatusRunning, State: store.ChatIdle, PermMode: chat.ModeAsk, Created: now, Updated: now, Seen: now, Title: "Review: " + s.Title, ReviewBrief: brief, ReviewOf: id}
	if err = a.forkWorktree(ctx, from, &r); err != nil {
		return store.Session{}, err
	}
	r.ReviewHead = r.BranchFrom
	_, snapshot, err := a.snapshot(ctx, r.Project, r.Worktree)
	if err != nil {
		a.dropWorktree(r)
		return store.Session{}, err
	}
	r.ReviewTree, err = a.git(ctx, r.Project, r.Worktree, "rev-parse", snapshot+"^{tree}")
	if err != nil {
		a.dropWorktree(r)
		return store.Session{}, err
	}
	base := s.BranchFrom
	if !validRef(base) {
		base, err = a.branchStart(ctx, s.Project)
		if err != nil {
			a.dropWorktree(r)
			return store.Session{}, err
		}
	}
	r.Handoff = fmt.Sprintf(`You are a fresh, independent code reviewer. The standalone review brief below and its referenced source material define the intended behavior. Inspect the copied code and tests, including uncommitted and untracked files, relative to base commit %s.
Do not retrieve the author's conversation, prior reviewer conversations, or their reasoning. Do not infer undocumented requirements from the implementation. Treat missing requirements, ambiguous acceptance criteria, and unavailable references as review findings. Identify assumptions explicitly and distinguish verified behavior from untested claims.
Check correctness, clarity, edge cases, and test coverage against the brief. Report actionable findings with file and line references and the criterion affected, or explicitly say no findings. List validation performed and remaining uncertainties.
Work only in this copied worktree. Do not edit, commit, push, or open a PR. Call set_workflow with changes_requested when there are blockers or missing information; use ready_to_merge only when the brief is sufficient and no blockers remain. The human resolves trade-offs and decides whether to merge.

%s`, base, reviewBriefText(brief))

	if err = a.store.CreateSession(r); err != nil {
		a.dropWorktree(r)
		return store.Session{}, err
	}
	if err = a.store.UpdateSession(id, func(x *store.Session) {
		x.ReviewBrief = brief
		x.ReviewID = r.ID
		x.ReviewHead = r.ReviewHead
		x.ReviewTree = r.ReviewTree
		x.ReviewStale = false
		x.Workflow = "reviewing"
	}); err != nil {
		return r, err
	}
	a.chat.Note(r.ID, reviewBriefText(brief))
	a.chat.Note(id, "Review started in chat "+r.ID+" with "+harness+".")
	a.Publish(Event{Type: "session", Project: s.Project, Session: id, Data: "review started"})
	a.Publish(Event{Type: "session", Project: s.Project, Session: r.ID, Data: "started"})
	if err = a.ChatSend(ctx, r.ID, "Review this code snapshot against the standalone brief and report your findings."); err != nil {
		a.chat.Note(r.ID, "Review could not start: "+err.Error()+". Send a message to retry.")
	}
	return r, nil
}

// WorkflowTools lets agents report work stages without guessing from their prose.
func (a *App) WorkflowTools() func(mcp.Caller) []mcp.Tool {
	return func(c mcp.Caller) []mcp.Tool {
		if !a.ChatIn(c.Project, c.Session) {
			return nil
		}
		if _, err := a.repoProject(c.Project); err != nil {
			return nil
		}
		return []mcp.Tool{{Name: "set_workflow", Description: "Report this chat's work stage. Call ready_for_review when implementation is complete. For a review chat, report changes_requested or ready_to_merge after writing your findings. Never mark work merged based on a review.", Input: json.RawMessage(`{"type":"object","properties":{"stage":{"type":"string","enum":["implementing","ready_for_review","changes_requested","ready_to_merge","finished","abandoned"]}},"required":["stage"],"additionalProperties":false}`), Call: func(ctx context.Context, c mcp.Caller, args json.RawMessage) (string, error) {
			var in struct {
				Stage string `json:"stage"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", err
			}
			current, _ := a.store.Session(c.Session)
			if current.ReviewOf != "" && in.Stage == "ready_to_merge" && strings.TrimSpace(current.ReviewBrief.Acceptance) == "" {
				return "", errors.New("report missing acceptance criteria as a finding and request changes")
			}
			s, err := a.EditSession(c.Session, SessionEdit{Workflow: &in.Stage})
			if err != nil {
				return "", err
			}
			if s.ReviewOf != "" && (in.Stage == "changes_requested" || in.Stage == "ready_to_merge") {
				defer a.lockSession(s.ReviewOf)()
				source, ok := a.store.Session(s.ReviewOf)
				if ok && source.ReviewID == s.ID && !source.ReviewStale {
					head, snapshot, snapshotErr := a.snapshot(ctx, source.Project, workdir(source))
					tree, treeErr := a.git(ctx, source.Project, workdir(source), "rev-parse", snapshot+"^{tree}")
					if snapshotErr != nil || treeErr != nil || head != source.ReviewHead || tree != source.ReviewTree {
						a.store.UpdateSession(source.ID, func(x *store.Session) { x.ReviewStale = true })
						a.Publish(Event{Type: "session", Project: source.Project, Session: source.ID, Data: "review outdated"})
					} else {
						_, err = a.EditSession(source.ID, SessionEdit{Workflow: &in.Stage})
					}
				}
			}
			return "Work stage updated.", err
		}}, {
			Name: "prepare_review", Description: "Write a standalone review brief for the human to inspect before starting a fresh reviewer. State intended behavior and acceptance criteria, plus relevant document paths or URLs. Record unresolved questions explicitly. Do not include conversation transcripts, author reasoning, or claims that the implementation is correct. This saves the brief; it does not start a review.",
			Input: json.RawMessage(`{"type":"object","properties":{"requirements":{"type":"string"},"acceptance":{"type":"string"},"references":{"type":"string"}},"required":["requirements"],"additionalProperties":false}`),
			Call: func(ctx context.Context, c mcp.Caller, args json.RawMessage) (string, error) {
				var brief store.ReviewBrief
				if err := json.Unmarshal(args, &brief); err != nil {
					return "", err
				}
				brief = cleanReviewBrief(brief)
				if err := checkReviewBrief(brief); err != nil {
					return "", err
				}
				defer a.lockSession(c.Session)()
				s, err := a.chatSession(c.Session)
				if err != nil {
					return "", err
				}
				if s.ReviewOf != "" {
					return "", errors.New("reviewers report gaps; the implementation chat prepares the brief")
				}
				if err = a.store.UpdateSession(s.ID, func(x *store.Session) {
					if x.ReviewID != "" && x.ReviewBrief != brief {
						x.ReviewStale = true
					}
					x.ReviewBrief = brief
				}); err != nil {
					return "", err
				}
				a.Publish(Event{Type: "session", Project: s.Project, Session: s.ID, Data: "review brief"})
				return "Review brief saved. The user can inspect it in Review with… before selecting a reviewer.", nil
			},
		}}
	}
}

func cleanReviewBrief(b store.ReviewBrief) store.ReviewBrief {
	b.Requirements = strings.TrimSpace(b.Requirements)
	b.Acceptance = strings.TrimSpace(b.Acceptance)
	b.References = strings.TrimSpace(b.References)
	return b
}

func checkReviewBrief(b store.ReviewBrief) error {
	if strings.TrimSpace(b.Requirements) == "" {
		return errors.New("provide standalone requirements for the review")
	}
	for _, field := range []struct {
		name, text string
		limit      int
	}{{"requirements", b.Requirements, 8000}, {"acceptance criteria", b.Acceptance, 4000}, {"references", b.References, 4000}} {
		if !utf8.ValidString(field.text) || utf8.RuneCountInString(field.text) > field.limit {
			return fmt.Errorf("%s must be valid text of at most %d characters", field.name, field.limit)
		}
	}
	return nil
}

func reviewBriefText(b store.ReviewBrief) string {
	acceptance, references := b.Acceptance, b.References
	if acceptance == "" {
		acceptance = "Not supplied. Report missing acceptance criteria as a finding."
	}
	if references == "" {
		references = "None supplied."
	}
	return "## Review brief\n\n### Requirements\n" + b.Requirements + "\n\n### Acceptance criteria\n" + acceptance + "\n\n### Referenced documents\n" + references
}
