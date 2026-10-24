package browser

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/zaneGittins/skybreaker/internal/control/mcp"
)

type fakeHost struct {
	b        *Browser
	saved    map[string][]byte
	copied   []Downloaded
	takeover []string
}

func (h *fakeHost) Browser(context.Context, string) (*Browser, error) { return h.b, nil }
func (h *fakeHost) SaveToWorkspace(_ context.Context, _, rel string, data []byte) (string, error) {
	h.saved[rel] = data
	return "/workspace/" + rel, nil
}
func (h *fakeHost) CopyDownload(_ context.Context, _ string, d Downloaded) (string, error) {
	h.copied = append(h.copied, d)
	return "/workspace/downloads/" + d.Name, nil
}
func (h *fakeHost) RequestTakeover(project, session, reason string) error {
	h.takeover = append(h.takeover, project+":"+session+":"+reason)
	return nil
}

func isPip(p string) bool { return p == "pip" }

func use(t *testing.T, h *fakeHost, name, args string) (string, error) {
	t.Helper()
	c := mcp.Caller{Project: "pip", Session: "s1"}
	for _, tool := range Tools(h, isPip)(c) {
		if tool.Name == name {
			return tool.Call(context.Background(), c, json.RawMessage(args))
		}
	}
	t.Fatalf("no tool %s", name)
	return "", nil
}

func TestBrowserToolsAreForAssistants(t *testing.T) {
	h := &fakeHost{}
	for _, c := range []mcp.Caller{{Project: "orbit"}, {User: "zane@example.com"}} {
		if n := len(Tools(h, isPip)(c)); n != 0 {
			t.Errorf("%+v got %d tools", c, n)
		}
	}
	if n := len(Tools(h, isPip)(mcp.Caller{Project: "pip"})); n != 10 {
		t.Errorf("an assistant got %d tools", n)
	}
}

func TestBrowserTools(t *testing.T) {
	ts := site(t)
	h := &fakeHost{b: start(t, Options{}), saved: map[string][]byte{}}
	if _, err := use(t, h, "browser_open", `{"url": "`+ts.URL+`/"}`); err != nil {
		t.Fatal(err)
	}
	out, err := use(t, h, "browser_read", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<untrusted", "Welcome home", `link "Next page"`, "secret", "</untrusted>"} {
		if !strings.Contains(out, want) {
			t.Errorf("read lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "hunter2") {
		t.Error("read shows a password")
	}
	if _, err := use(t, h, "browser_screenshot", `{}`); err != nil || len(h.saved) != 1 {
		t.Fatalf("screenshot: %v %v", err, h.saved)
	}
	var dl int
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "Get the podcast") {
			json.Unmarshal([]byte(strings.Trim(strings.Fields(line)[0], "[]")), &dl)
		}
	}
	if out, err := use(t, h, "browser_download", `{"ref": `+itoa(dl)+`}`); err != nil || !strings.Contains(out, "/workspace/downloads/podcast.mp3") {
		t.Fatalf("download: %q %v", out, err)
	}
	if _, err := use(t, h, "browser_takeover", `{"reason": "Google wants a code"}`); err != nil || len(h.takeover) != 1 || h.takeover[0] != "pip:s1:Google wants a code" {
		t.Fatalf("takeover %v %v", h.takeover, err)
	}
	if _, err := use(t, h, "browser_open", `{"url": "file:///etc/passwd"}`); err == nil {
		t.Error("opened a file")
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }
