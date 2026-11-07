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
	review, err := f.app.StartReview(context.Background(), s.ID, "claude")
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
	if _, err := f.app.StartReview(context.Background(), s.ID, "claude"); err == nil {
		t.Fatal("reviewed busy source")
	}
	f.app.store.UpdateSession(s.ID, func(x *store.Session) { x.State = store.ChatIdle })
	if _, err := f.app.StartReview(context.Background(), s.ID, "missing"); err == nil {
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
			review, err := f.app.StartReview(context.Background(), s.ID, "claude")
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
			if len(tools) != 1 {
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
	review, err := f.app.StartReview(context.Background(), s.ID, "codex")
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
