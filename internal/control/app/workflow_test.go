package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zaneGittins/skybreaker/internal/control/chat"
	"github.com/zaneGittins/skybreaker/internal/control/mcp"
	"github.com/zaneGittins/skybreaker/internal/control/store"
)

func TestReviewCopiesWorkAndLinksChats(t *testing.T) {
	f, _ := chatFixture(t)
	g := newGitSandbox(t, f)
	s := workflowSource(t, f)
	os.WriteFile(filepath.Join(g.dir(s.ID), "review-me.txt"), []byte("uncommitted"), 0600)
	review, err := f.app.StartReview(context.Background(), s.ID, "claude", testReviewBrief)
	if err != nil {
		t.Fatal(err)
	}
	if review.ReviewOf != s.ID || review.Worktree == s.Worktree || review.Resume != "" {
		t.Fatalf("review = %+v", review)
	}
	b, err := os.ReadFile(filepath.Join(g.dir(review.ID), "review-me.txt"))
	if err != nil || string(b) != "uncommitted" {
		t.Fatalf("snapshot: %q %v", b, err)
	}
	source, _ := f.app.store.Session(s.ID)
	if source.ReviewID != review.ID || source.Workflow != "reviewing" {
		t.Fatalf("source = %+v", source)
	}
	if !strings.Contains(review.Handoff, "review-me") && !strings.Contains(review.Handoff, "Review") {
		t.Fatal("missing review context")
	}
}

func TestReviewRefusesBusySourceAndInvalidAgent(t *testing.T) {
	f, _ := chatFixture(t)
	newGitSandbox(t, f)
	s := workflowSource(t, f)
	f.app.store.UpdateSession(s.ID, func(x *store.Session) { x.State = store.ChatWorking })
	if _, err := f.app.StartReview(context.Background(), s.ID, "claude", testReviewBrief); err == nil {
		t.Fatal("reviewed busy source")
	}
	f.app.store.UpdateSession(s.ID, func(x *store.Session) { x.State = store.ChatIdle })
	if _, err := f.app.StartReview(context.Background(), s.ID, "missing", testReviewBrief); err == nil {
		t.Fatal("accepted missing agent")
	}
}

func TestWorkflowValidation(t *testing.T) {
	f, _ := chatFixture(t)
	s, err := f.app.NewChat(context.Background(), "orbit", "claude", NewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	stage := "ready_for_review"
	got, err := f.app.EditSession(s.ID, SessionEdit{Workflow: &stage})
	if err != nil || got.Workflow != stage {
		t.Fatalf("stage: %+v %v", got, err)
	}
	stage = "merged"
	if _, err := f.app.EditSession(s.ID, SessionEdit{Workflow: &stage}); err == nil {
		t.Fatal("accepted invented workflow")
	}
}

func TestReviewResultRejectsChangedSnapshot(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprint(changed), func(t *testing.T) {
			f, _ := chatFixture(t)
			g := newGitSandbox(t, f)
			s := workflowSource(t, f)
			review, err := f.app.StartReview(context.Background(), s.ID, "claude", testReviewBrief)
			if err != nil {
				t.Fatal(err)
			}
			if changed {
				if err := os.WriteFile(filepath.Join(g.dir(s.ID), "later.txt"), []byte("later"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			caller := mcp.Caller{Project: s.Project, Session: review.ID}
			tools := f.app.WorkflowTools()(caller)
			if len(tools) < 1 {
				t.Fatal("missing workflow tool")
			}
			if _, err := tools[0].Call(context.Background(), caller, json.RawMessage(`{"stage":"ready_to_merge"}`)); err != nil {
				t.Fatal(err)
			}
			source, _ := f.app.store.Session(s.ID)
			if changed {
				if !source.ReviewStale || source.Workflow == "ready_to_merge" {
					t.Fatalf("accepted outdated review: %+v", source)
				}
			} else if source.Workflow != "ready_to_merge" {
				t.Fatalf("lost result: %+v", source)
			}
		})
	}
}

func TestReviewCanUseDifferentHarness(t *testing.T) {
	f, _ := chatFixture(t)
	newGitSandbox(t, f)
	s := workflowSource(t, f)
	f.app.spawn = func(context.Context, []string) (*chat.Proc, error) {
		return nil, errors.New("review startup unavailable")
	}
	review, err := f.app.StartReview(context.Background(), s.ID, "codex", testReviewBrief)
	if err != nil {
		t.Fatal(err)
	}
	if review.Harness != "codex" || review.ReviewOf != s.ID || review.Resume != "" {
		t.Fatalf("cross-harness review: %+v", review)
	}
}

func workflowSource(t *testing.T, f *fixture) store.Session {
	t.Helper()
	ctx := context.Background()
	s, err := f.app.NewChat(ctx, "orbit", "claude", NewOptions{OwnBranch: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.app.StartSandbox(ctx, s.Project); err != nil {
		t.Fatal(err)
	}
	if err = f.app.ensureWorktree(ctx, &s, nil); err != nil {
		t.Fatal(err)
	}
	return s
}

var testReviewBrief = store.ReviewBrief{Requirements: "Reconnect sessions without losing pending messages.", Acceptance: "Reconnect delivers each pending message once.", References: "docs/sessions.md"}

func TestReviewUsesBriefWithoutConversation(t *testing.T) {
	f, _ := chatFixture(t)
	newGitSandbox(t, f)
	s := workflowSource(t, f)
	f.app.store.UpdateSession(s.ID, func(x *store.Session) { x.Goal = "elephant-private-goal"; x.Resume = "elephant-conversation" })
	dir := t.TempDir()
	log, err := chat.OpenLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []chat.Event{{Kind: chat.KindUser, Text: "elephant-private-request"}, {Kind: chat.KindText, Text: "elephant-private-reasoning"}} {
		if _, err = log.Append(s.ID, event); err != nil {
			t.Fatal(err)
		}
	}
	f.app.CloseChats()
	f.app.chat, err = f.app.newChatManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	review, err := f.app.StartReview(context.Background(), s.ID, "claude", testReviewBrief)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(review.Handoff, "elephant-private") || review.Goal != "" || review.Resume != "" || review.Fork {
		t.Fatalf("inherited author context: %+v", review)
	}
	for _, text := range []string{testReviewBrief.Requirements, testReviewBrief.Acceptance, testReviewBrief.References, "missing", "conversation"} {
		if !strings.Contains(review.Handoff, text) {
			t.Fatalf("missing %q", text)
		}
	}
	events, err := f.app.ChatEvents(review.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if strings.Contains(event.Text, "elephant-private") {
			t.Fatal("conversation copied")
		}
	}
	source, _ := f.app.store.Session(s.ID)
	if review.ReviewBrief != testReviewBrief || source.ReviewBrief != testReviewBrief {
		t.Fatal("brief not preserved")
	}
}

func TestReviewRejectsMissingBriefBeforeCreatingSession(t *testing.T) {
	f, _ := chatFixture(t)
	newGitSandbox(t, f)
	s := workflowSource(t, f)
	before := len(f.app.store.ListSessions(s.Project))
	if _, err := f.app.StartReview(context.Background(), s.ID, "claude", store.ReviewBrief{}); err == nil {
		t.Fatal("accepted missing brief")
	}
	if len(f.app.store.ListSessions(s.Project)) != before {
		t.Fatal("created review without brief")
	}
}

func TestPrepareReviewKeepsReviewedBriefAndMarksChangesStale(t *testing.T) {
	f, _ := chatFixture(t)
	newGitSandbox(t, f)
	s := workflowSource(t, f)
	caller := mcp.Caller{Project: s.Project, Session: s.ID}
	var prepare mcp.Tool
	for _, tool := range f.app.WorkflowTools()(caller) {
		if tool.Name == "prepare_review" {
			prepare = tool
		}
	}
	if prepare.Call == nil {
		t.Fatal("missing brief tool")
	}
	encoded, _ := json.Marshal(testReviewBrief)
	if _, err := prepare.Call(context.Background(), caller, encoded); err != nil {
		t.Fatal(err)
	}
	source, _ := f.app.store.Session(s.ID)
	if source.ReviewID != "" || source.ReviewBrief != testReviewBrief {
		t.Fatal("preparing a brief started a review or lost the brief")
	}
	review, err := f.app.StartReview(context.Background(), s.ID, "claude", testReviewBrief)
	if err != nil {
		t.Fatal(err)
	}
	revised := testReviewBrief
	revised.Acceptance = "Reconnect delivers each pending message once, including after a restart."
	encoded, _ = json.Marshal(revised)
	if _, err = prepare.Call(context.Background(), caller, encoded); err != nil {
		t.Fatal(err)
	}
	source, _ = f.app.store.Session(s.ID)
	savedReview, _ := f.app.store.Session(review.ID)
	if !source.ReviewStale || savedReview.ReviewBrief != testReviewBrief {
		t.Fatal("changed the reviewed brief or kept its result current")
	}
	if _, err = prepare.Call(context.Background(), mcp.Caller{Project: s.Project, Session: review.ID}, encoded); err == nil {
		t.Fatal("reviewer rewrote brief")
	}
}

func TestReviewCannotApproveMissingAcceptanceCriteria(t *testing.T) {
	f, _ := chatFixture(t)
	newGitSandbox(t, f)
	s := workflowSource(t, f)
	brief := testReviewBrief
	brief.Acceptance = ""
	review, err := f.app.StartReview(context.Background(), s.ID, "claude", brief)
	if err != nil {
		t.Fatal(err)
	}
	caller := mcp.Caller{Project: s.Project, Session: review.ID}
	tool := f.app.WorkflowTools()(caller)[0]
	if _, err = tool.Call(context.Background(), caller, json.RawMessage(`{"stage":"ready_to_merge"}`)); err == nil {
		t.Fatal("approved missing criteria")
	}
	if _, err = tool.Call(context.Background(), caller, json.RawMessage(`{"stage":"changes_requested"}`)); err != nil {
		t.Fatal(err)
	}
	source, _ := f.app.store.Session(s.ID)
	if source.Workflow != "changes_requested" {
		t.Fatalf("missing criteria not reported: %s", source.Workflow)
	}
}

func TestReviewBriefLimits(t *testing.T) {
	for _, brief := range []store.ReviewBrief{{Requirements: " "}, {Requirements: strings.Repeat("a", 8001)}, {Requirements: "x", Acceptance: strings.Repeat("a", 4001)}, {Requirements: "x", References: strings.Repeat("a", 4001)}} {
		if checkReviewBrief(brief) == nil {
			t.Fatal("accepted invalid brief")
		}
	}
	if err := checkReviewBrief(store.ReviewBrief{Requirements: strings.Repeat("界", 8000)}); err != nil {
		t.Fatal(err)
	}
}
