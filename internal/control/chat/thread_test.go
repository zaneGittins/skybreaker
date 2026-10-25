package chat

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/zaneGittins/skybreaker/internal/control/store"
)

// A session's hand-off goes to the agent ahead of the first message only,
// and the conversation shows what you wrote, not the hand-off.
func TestManagerDeliversHandoffOnce(t *testing.T) {
	h := newHarness(t)
	s := h.session("claude")
	h.st.UpdateSession(s.ID, func(x *store.Session) { x.Handoff = "Earlier: you booked a table." })
	ctx := context.Background()
	h.m.Send(ctx, s.ID, "and the time?")
	waitFor(t, "the first turn", func() bool { return len(find(h.events(s.ID), KindTurn)) == 2 })
	h.m.Send(ctx, s.ID, "thanks")
	waitFor(t, "the second turn", func() bool { return len(find(h.events(s.ID), KindTurn)) == 4 })

	var sent []string
	h.mu.Lock()
	for _, m := range h.procs[0].received() {
		if m["type"] == "user" {
			c := m["message"].(map[string]any)["content"]
			if blocks, ok := c.([]any); ok {
				c = blocks[0].(map[string]any)["text"]
			}
			sent = append(sent, c.(string))
		}
	}
	h.mu.Unlock()
	if len(sent) != 2 || !strings.HasPrefix(sent[0], "Earlier: you booked a table.") || !strings.HasSuffix(sent[0], "and the time?") || sent[1] != "thanks" {
		t.Fatalf("sent %q", sent)
	}
	if users := find(h.events(s.ID), KindUser); users[0].Text != "and the time?" {
		t.Errorf("shown %q", users[0].Text)
	}
	if h.state(s.ID).Handoff != "" {
		t.Error("the hand-off stayed after it was delivered")
	}
}

// A card is redrawn in place: each update carries the whole card under the
// same ID, and a later one wins.
func TestManagerCard(t *testing.T) {
	h := newHarness(t)
	s := h.session("claude")
	h.m.Card(s.ID, Card{Kind: CardTask, Ref: "t1", Title: "Find flights", Status: CardWorking})
	h.m.Card(s.ID, Card{Kind: CardTask, Ref: "t1", Title: "Find flights", Status: CardDone, Summary: "Three options under $400"})
	cards := find(h.events(s.ID), KindCard)
	if len(cards) != 2 || cards[0].ID != cards[1].ID || cards[0].ID != "task:t1" || cards[1].Card.Status != CardDone || cards[1].Card.Summary == "" {
		t.Fatalf("cards = %+v", cards)
	}
	// A card doesn't move the session's state: it's news, not a turn.
	if st := h.state(s.ID).State; st != store.ChatIdle {
		t.Errorf("state = %q", st)
	}
	if err := h.m.Card("nope", Card{Kind: CardTask, Ref: "x"}); err == nil {
		t.Error("carded an unknown session")
	}
}

// A hand-off waits for an agent that takes it: a start that fails keeps it
// for the next message.
func TestManagerHandoffSurvivesAFailedStart(t *testing.T) {
	h := newHarness(t)
	s := h.session("claude")
	h.st.UpdateSession(s.ID, func(x *store.Session) { x.Handoff = "Earlier: you booked a table." })
	h.mu.Lock()
	h.fail = errors.New("sandbox down")
	h.mu.Unlock()
	ctx := context.Background()
	h.m.Send(ctx, s.ID, "first")
	waitFor(t, "the failed turn", func() bool { return h.state(s.ID).State == store.ChatFailed })
	if h.state(s.ID).Handoff == "" {
		t.Fatal("a failed start lost the hand-off")
	}
	h.mu.Lock()
	h.fail = nil
	h.mu.Unlock()
	h.m.Send(ctx, s.ID, "again")
	waitFor(t, "the turn", func() bool { return h.state(s.ID).Handoff == "" && h.state(s.ID).State == store.ChatIdle })
	h.mu.Lock()
	got := h.procs[0].received()
	h.mu.Unlock()
	var first string
	for _, m := range got {
		if m["type"] == "user" {
			first, _ = m["message"].(map[string]any)["content"].(string)
			break
		}
	}
	if !strings.HasPrefix(first, "Earlier: you booked a table.") || !strings.HasSuffix(first, "again") {
		t.Errorf("delivered %q", first)
	}
}

// OnChange may call back into the manager, as an assistant's task does to
// redraw its card, while the idle reaper runs.
func TestOnChangeMayCallTheManager(t *testing.T) {
	st, _ := store.Open("")
	lg, _ := OpenLog(t.TempDir())
	var m *Manager
	m = NewManager(Config{Log: lg, Sessions: st, OnChange: func(before, after store.Session) {
		if after.ID != "task" {
			return
		}
		go m.Reap(time.Now())
		time.Sleep(50 * time.Millisecond)
		m.Card("thread", Card{Kind: CardTask, Ref: "task", Status: CardDone})
	}})
	t.Cleanup(m.Close)
	for _, id := range []string{"thread", "task"} {
		st.CreateSession(store.Session{ID: id, Project: "p", Harness: "claude", Mode: store.ModeChat, Status: store.StatusRunning, Created: time.Now(), State: store.ChatWorking})
	}
	m.run("thread")
	done := make(chan struct{})
	go func() {
		s, _ := st.Session("task")
		m.Recover([]store.Session{s})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("deadlock: a callback under a chat's lock against the reaper")
	}
}

func TestShorten(t *testing.T) {
	for in, want := range map[string]string{
		"short":                       "short",
		strings.Repeat("é", 10):       "éééé…",
		"one two three four five six": "one two…",
	} {
		if got := Shorten(in, 9); got != want || !utf8.ValidString(got) {
			t.Errorf("Shorten(%q) = %q, want %q", in, got, want)
		}
	}
}

// Handing the browser back tells the agent so, and the conversation shows
// it as a hand-back on the site, not as something you wrote.
func TestManagerHandback(t *testing.T) {
	h := newHarness(t)
	s := h.session("claude")
	if err := h.m.Handback(context.Background(), s.ID, "I'm done in the browser.", "accounts.google.com"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the turn", func() bool { return len(find(h.events(s.ID), KindTurn)) == 2 })
	users := find(h.events(s.ID), KindUser)
	if len(users) != 1 || users[0].Handback == nil || users[0].Handback.Site != "accounts.google.com" || users[0].Text != "I'm done in the browser." {
		t.Fatalf("shown %+v", users)
	}
	if h.state(s.ID).Title != "" {
		t.Errorf("a hand-back named the chat %q", h.state(s.ID).Title)
	}
}
