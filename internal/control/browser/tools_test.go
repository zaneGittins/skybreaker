package browser

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

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

// toolRef is the number browser_read gave the element named name.
func toolRef(t *testing.T, h *fakeHost, name string) string {
	t.Helper()
	out, err := use(t, h, "browser_read", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "[") && strings.Contains(line, `"`+name+`"`) {
			return strings.Trim(strings.Fields(line)[0], "[]")
		}
	}
	t.Fatalf("no %q in %s", name, out)
	return ""
}

func TestDownloadToolReportsProgress(t *testing.T) {
	old := downloadWait
	downloadWait = time.Second
	t.Cleanup(func() { downloadWait = old })
	release := make(chan struct{})
	ts := episodes(t, release)
	h := &fakeHost{b: start(t, Options{}), saved: map[string][]byte{}}
	use(t, h, "browser_open", `{"url": "`+ts.URL+`/"}`)
	args := `{"ref": ` + toolRef(t, h, "Download episode") + `}`
	out, err := use(t, h, "browser_download", args)
	if err != nil || !strings.Contains(out, "Still downloading episode.wav: 0.1 of 1.0 MB") || !strings.Contains(out, "browser_status") {
		t.Fatalf("download: %q %v", out, err)
	}
	if _, err := use(t, h, "browser_download", args); err == nil || !strings.Contains(err.Error(), "already going") {
		t.Fatalf("a second download: %v", err)
	}
	if out, _ := use(t, h, "browser_status", `{}`); !strings.Contains(out, "Downloading episode.wav: 0.1 of 1.0 MB") {
		t.Fatalf("status mid-download: %q", out)
	}
	close(release)
	deadline := time.Now().Add(20 * time.Second)
	for !strings.Contains(out, "/workspace/downloads/episode.wav") {
		if time.Now().After(deadline) {
			t.Fatalf("status never got the file: %q", out)
		}
		time.Sleep(200 * time.Millisecond)
		out, _ = use(t, h, "browser_status", `{}`)
	}
	if len(h.copied) != 1 || h.copied[0].Name != "episode.wav" {
		t.Fatalf("copied %+v", h.copied)
	}
	if out, _ := use(t, h, "browser_status", `{}`); strings.Contains(out, "episode") {
		t.Fatalf("a delivered download is told again: %q", out)
	}
}
