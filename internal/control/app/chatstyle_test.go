package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/zaneGittins/skybreaker/internal/control/config"
	"github.com/zaneGittins/skybreaker/internal/control/mcp"
	"github.com/zaneGittins/skybreaker/internal/control/store"
)

func setStyle(t *testing.T, f *fixture, project string, s config.ChatStyle) {
	t.Helper()
	if _, err := config.EditChatStyles(f.app.cfg.ChatStyles(), func(m config.ChatStyles) error { m[project] = s; return nil }); err != nil {
		t.Fatal(err)
	}
}

var vulnStyle = config.ChatStyle{Format: "kebab", MaxLength: 20, Colors: map[string]string{"mars": "a vulnerability", "sun": "a bug"}}

// The agent hears the project's style with the first message only, and
// only where one is set.
func TestChatStyleGoesWithFirstMessage(t *testing.T) {
	f, sp := chatFixture(t)
	ctx := context.Background()
	setStyle(t, f, "orbit", vulnStyle)
	s, _ := f.app.NewChat(ctx, "orbit", "claude", NewOptions{})
	f.app.ChatSend(ctx, s.ID, "the login leaks tokens")
	waitUntil(t, "the first turn", func() bool { x, _ := f.app.store.Session(s.ID); return x.Resume != "" && x.State == store.ChatIdle })
	f.app.ChatSend(ctx, s.ID, "more")
	waitUntil(t, "the second turn", func() bool { sp.mu.Lock(); defer sp.mu.Unlock(); return len(sp.sent) == 2 })
	if !strings.Contains(sp.sent[0], "name_chat") || !strings.Contains(sp.sent[0], "mars (red): a vulnerability") || !strings.Contains(sp.sent[0], "the login leaks tokens") {
		t.Errorf("first message: %s", sp.sent[0])
	}
	if strings.Contains(sp.sent[1], "name_chat") {
		t.Errorf("second message carries the style: %s", sp.sent[1])
	}

	plain, _ := f.app.NewChat(ctx, "bifract", "claude", NewOptions{})
	f.app.ChatSend(ctx, plain.ID, "hello")
	waitUntil(t, "the plain turn", func() bool { sp.mu.Lock(); defer sp.mu.Unlock(); return len(sp.sent) == 3 })
	if strings.Contains(sp.sent[2], "name_chat") {
		t.Errorf("a project without a style was told one: %s", sp.sent[2])
	}
}

func nameTool(t *testing.T, f *fixture, c mcp.Caller) *mcp.Tool {
	t.Helper()
	for _, tool := range f.app.ChatStyleTools()(c) {
		if tool.Name == "name_chat" {
			return &tool
		}
	}
	return nil
}

func callName(t *testing.T, tool *mcp.Tool, c mcp.Caller, args map[string]string) (string, error) {
	t.Helper()
	b, _ := json.Marshal(args)
	return tool.Call(context.Background(), c, b)
}

func TestNameChat(t *testing.T) {
	f := newFixture(t)
	f.app.store.CreateSession(store.Session{ID: "c1", Project: "orbit", Mode: store.ModeChat, Status: store.StatusRunning, Title: "the login leaks tokens"})
	c := mcp.Caller{Project: "orbit", Session: "c1"}
	if nameTool(t, f, c) != nil {
		t.Fatal("name_chat offered without a style")
	}
	setStyle(t, f, "orbit", vulnStyle)
	tool := nameTool(t, f, c)
	if tool == nil {
		t.Fatal("name_chat not offered")
	}
	if !strings.Contains(tool.Description, "kebab-case") {
		t.Errorf("description lacks the style: %s", tool.Description)
	}
	for _, other := range []mcp.Caller{{Project: "bifract", Session: "c1"}, {Project: "orbit", Session: "-"}, {Project: "orbit"}} {
		if nameTool(t, f, other) != nil {
			t.Errorf("offered to %+v", other)
		}
	}

	// The server holds the name to the style, whatever the agent sends.
	if _, err := callName(t, tool, c, map[string]string{"title": "Login Token Leak In The Redirect", "color": "mars"}); err != nil {
		t.Fatal(err)
	}
	if s, _ := f.app.store.Session("c1"); s.Title != "login-token-leak-in" || s.Color != "mars" {
		t.Errorf("named %q %q", s.Title, s.Color)
	}
	for name, args := range map[string]map[string]string{
		"colour not in the style": {"title": "x", "color": "venus"},
		"empty name":              {"title": "!!!"},
	} {
		if _, err := callName(t, tool, c, args); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	// A name or colour you gave stays yours.
	str := func(s string) *string { return &s }
	f.app.EditSession("c1", SessionEdit{Title: str("Mine")})
	out, err := callName(t, tool, c, map[string]string{"title": "other name", "color": "sun"})
	if s, _ := f.app.store.Session("c1"); err != nil || s.Title != "Mine" || s.Color != "sun" {
		t.Errorf("after your rename: %q %q %v", s.Title, s.Color, err)
	}
	if !strings.Contains(out, "kept") {
		t.Errorf("the agent isn't told the name was kept: %s", out)
	}
	f.app.EditSession("c1", SessionEdit{Color: str("")})
	callName(t, tool, c, map[string]string{"title": "x", "color": "mars"})
	if s, _ := f.app.store.Session("c1"); s.Color != "" {
		t.Errorf("an agent coloured over your choice: %q", s.Color)
	}
}

func TestSetChatStyle(t *testing.T) {
	f := newFixture(t)
	if _, err := f.app.SetChatStyle("orbit", vulnStyle); err != nil {
		t.Fatal(err)
	}
	if s, err := f.app.ChatStyle("orbit"); err != nil || s.Format != "kebab" {
		t.Fatalf("%+v %v", s, err)
	}
	if _, err := f.app.SetChatStyle("nope", vulnStyle); err == nil {
		t.Error("styled an unknown project")
	}
	if _, err := f.app.SetChatStyle("orbit", config.ChatStyle{Format: "camel"}); err == nil {
		t.Error("saved a bad style")
	}
}
