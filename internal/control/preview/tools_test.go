package preview

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/zaneGittins/skybreaker/internal/control/chat"
	"github.com/zaneGittins/skybreaker/internal/control/mcp"
)

type fakeBackend struct {
	listening []Listening
	chats     map[string]string // session → project
	shown     []chat.Preview
}

func (f *fakeBackend) Listening(context.Context, string) ([]Listening, error) { return f.listening, nil }
func (f *fakeBackend) ChatIn(project, session string) bool                { return f.chats[session] == project }
func (f *fakeBackend) ChatPreview(session string, p chat.Preview) error {
	f.shown = append(f.shown, p)
	return nil
}

func call(t *testing.T, tools func(mcp.Caller) []mcp.Tool, c mcp.Caller, args string) (string, error) {
	t.Helper()
	for _, tl := range tools(c) {
		if tl.Name == "preview" {
			return tl.Call(context.Background(), c, json.RawMessage(args))
		}
	}
	t.Fatalf("no preview tool for %+v", c)
	return "", nil
}

func TestPreviewTool(t *testing.T) {
	reg := NewRegistry()
	reg.SetOrigin("orbit", "https://skybreaker.example.ts.net:8401")
	b := &fakeBackend{listening: []Listening{{Port: 3000}, {Port: 5173, Public: true}}, chats: map[string]string{"s1": "orbit"}}
	tools := Tools(reg, b)

	// Not for you on the UI mount, nor for a project without a preview.
	if n := len(tools(mcp.Caller{User: "you@github"})); n != 0 {
		t.Fatalf("UI mount has %d tools", n)
	}
	if n := len(tools(mcp.Caller{Project: "comet", Session: "-"})); n != 0 {
		t.Fatalf("project without a preview has %d tools", n)
	}

	inChat := mcp.Caller{Project: "orbit", Session: "s1"}
	for args, want := range map[string]string{
		`{"port": 8080}`:   "nothing is listening on port 8080",
		`{"port": 3000}`:   "0.0.0.0",
		`{"port": 0}`:      "between 1 and 65535",
		`{"port": 70000}`:  "between 1 and 65535",
		`{"port": "5173"}`: "port",
		`{}`:               "port",
	} {
		if _, err := call(t, tools, inChat, args); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", args, err, want)
		}
	}
	if _, ok := reg.Target("orbit"); ok {
		t.Fatal("a refused call changed the preview")
	}

	out, err := call(t, tools, inChat, `{"port": 5173, "title": "The new settings page"}`)
	if err != nil || !strings.Contains(out, "https://skybreaker.example.ts.net:8401") {
		t.Fatalf("in a chat: %q %v", out, err)
	}
	if p, _ := reg.Target("orbit"); p != 5173 {
		t.Fatalf("target %d", p)
	}
	// The link is Skybreaker's, never the agent's.
	if len(b.shown) != 1 || b.shown[0].URL != "https://skybreaker.example.ts.net:8401" || b.shown[0].Port != 5173 || b.shown[0].Title != "The new settings page" {
		t.Fatalf("card %+v", b.shown)
	}

	// From a terminal or a scheduled run: no chat to put a card in.
	out, err = call(t, tools, mcp.Caller{Project: "orbit", Session: "-"}, `{"port": 5173}`)
	if err != nil || !strings.Contains(out, "https://skybreaker.example.ts.net:8401") || len(b.shown) != 1 {
		t.Fatalf("terminal: %q %v, %d cards", out, err, len(b.shown))
	}

	// A long caption is cut.
	call(t, tools, inChat, `{"port": 5173, "title": "`+strings.Repeat("x", 500)+`"}`)
	if n := len(b.shown[len(b.shown)-1].Title); n > 200 {
		t.Fatalf("caption of %d bytes", n)
	}
}
