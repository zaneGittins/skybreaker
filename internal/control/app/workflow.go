package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/zaneGittins/skybreaker/internal/control/chat"
	"github.com/zaneGittins/skybreaker/internal/control/mcp"
	"github.com/zaneGittins/skybreaker/internal/control/store"
)

var workflowStages = []string{"", "implementing", "ready_for_review", "reviewing", "changes_requested", "ready_to_merge", "finished", "abandoned"}

// StartReview copies the code into an independent chat with a link back to its author.
func (a *App) StartReview(ctx context.Context, id, harness string) (store.Session, error) {
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
	r := store.Session{ID: newSessionID(), Project: s.Project, Harness: harness, Mode: store.ModeChat, Status: store.StatusRunning, State: store.ChatIdle, PermMode: chat.ModeAsk, Created: now, Updated: now, Seen: now, Title: "Review: " + s.Title, Goal: s.Goal, ReviewOf: id}
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
	r.Handoff = fmt.Sprintf("Review the implementation from chat %s independently. Work only in this copied worktree. Review the changes relative to %s, including uncommitted and untracked files. Do not edit, commit, push, or open a PR. Report actionable findings with file and line references, or explicitly say no findings. The source may have changed since this snapshot.\nTask: %s\nGoal: %s\nCall set_workflow with changes_requested if fixes are needed, or ready_to_merge if your review found no blockers. This is an agent review, not human merge approval.", id, base, s.Title, s.Goal)
	events, _ := a.chat.Events(id, 0)
	var requests []string
	for _, event := range events {
		if event.Kind == chat.KindUser {
			requests = append(requests, event.Text)
		}
	}
	var requirements strings.Builder
	if len(requests) > 0 {
		first := []rune(requests[0])
		requirements.WriteString("\n\nInitial request:\n" + string(first[:min(len(first), 4000)]))
		var recent []string
		size := 0
		for i := len(requests) - 1; i > 0 && size < 8000; i-- {
			text := []rune(requests[i])
			part := string(text[:min(len(text), 4000, 8000-size)])
			recent = append(recent, part)
			size += len([]rune(part))
		}
		slices.Reverse(recent)
		for _, text := range recent {
			requirements.WriteString("\n\nLater request:\n" + text)
		}
	}

	r.Handoff += "\n\nTask context (quoted user requests):" + requirements.String()
	if err = a.store.CreateSession(r); err != nil {
		a.dropWorktree(r)
		return store.Session{}, err
	}
	if err = a.store.UpdateSession(id, func(x *store.Session) {
		x.ReviewID = r.ID
		x.ReviewHead = r.ReviewHead
		x.ReviewTree = r.ReviewTree
		x.ReviewStale = false
		x.Workflow = "reviewing"
	}); err != nil {
		return r, err
	}
	a.chat.Note(id, "Review started in chat "+r.ID+" with "+harness+".")
	a.Publish(Event{Type: "session", Project: s.Project, Session: id, Data: "review started"})
	a.Publish(Event{Type: "session", Project: s.Project, Session: r.ID, Data: "started"})
	if err = a.ChatSend(ctx, r.ID, "Review the code snapshot and report your findings."); err != nil {
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
		}}}
	}
}
