package app

import (
	"context"
	"encoding/json"
	"github.com/zaneGittins/skybreaker/internal/assistant"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/zaneGittins/skybreaker/internal/control/chat"
	"github.com/zaneGittins/skybreaker/internal/control/mcp"
	"github.com/zaneGittins/skybreaker/internal/control/show"
	"github.com/zaneGittins/skybreaker/internal/control/store"
)

// runRead runs the sandbox read script on this machine, with two temp
// directories standing in for /workspace and /tmp.
func runRead(t *testing.T, ws, tmp, p string, max int64) (string, int64, []byte, error) {
	t.Helper()
	out, err := exec.Command("sh", "-c", readScript, "sh", p, strconv.FormatInt(max, 10), ws, tmp).Output()
	if err != nil {
		t.Fatalf("script: %v", err)
	}
	return parseRead(out)
}

func TestReadScript(t *testing.T) {
	if _, err := exec.LookPath("realpath"); err != nil {
		t.Skip("no realpath")
	}
	root := t.TempDir()
	ws, tmp, outside := filepath.Join(root, "workspace"), filepath.Join(root, "tmp"), filepath.Join(root, "secret")
	for _, d := range []string{ws, tmp, outside, filepath.Join(ws, "dir")} {
		os.MkdirAll(d, 0o755)
	}
	os.WriteFile(filepath.Join(ws, "a.txt"), []byte("hello\x00world"), 0o644)
	os.WriteFile(filepath.Join(ws, "-n"), []byte("dash"), 0o644)
	os.WriteFile(filepath.Join(tmp, "shot.png"), []byte("png"), 0o644)
	os.WriteFile(filepath.Join(outside, "key"), []byte("SECRET"), 0o600)
	os.Symlink(filepath.Join(outside, "key"), filepath.Join(ws, "link"))
	os.Symlink(filepath.Join(ws, "a.txt"), filepath.Join(ws, "inside-link"))
	os.WriteFile(filepath.Join(root, "workspace-evil"), []byte("SECRET"), 0o600)
	exec.Command("mkfifo", filepath.Join(ws, "fifo")).Run()

	p, size, data, err := runRead(t, ws, tmp, "a.txt", 100)
	if err != nil || p != filepath.Join(ws, "a.txt") || size != 11 || string(data) != "hello\x00world" {
		t.Errorf("a.txt = %q %d %q %v", p, size, data, err)
	}
	if _, size, data, err := runRead(t, ws, tmp, "a.txt", 5); err != nil || size != 11 || string(data) != "hello" {
		t.Errorf("capped = %d %q %v", size, data, err)
	}
	if _, _, data, err := runRead(t, ws, tmp, filepath.Join(tmp, "shot.png"), 100); err != nil || string(data) != "png" {
		t.Errorf("tmp file = %q %v", data, err)
	}
	if _, _, data, err := runRead(t, ws, tmp, "-n", 100); err != nil || string(data) != "dash" {
		t.Errorf("dash file = %q %v", data, err)
	}
	if p, _, _, err := runRead(t, ws, tmp, "inside-link", 100); err != nil || p != filepath.Join(ws, "a.txt") {
		t.Errorf("symlink inside = %q %v", p, err)
	}
	for _, c := range []struct{ path, want string }{
		{"link", "outside"},
		{"../secret/key", "outside"},
		{filepath.Join(outside, "key"), "outside"},
		{"../workspace-evil", "outside"},
		{"/etc/passwd", "outside"},
		{"dir", "not a regular file"},
		{"fifo", "not a regular file"},
		{"nope.txt", "no such file"},
		{".", "not a regular file"},
	} {
		_, _, data, err := runRead(t, ws, tmp, c.path, 100)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v (data %q), want %q", c.path, err, data, c.want)
		}
		if strings.Contains(string(data), "SECRET") {
			t.Errorf("%s leaked the file outside", c.path)
		}
	}
}

func TestParseReadRefusesJunk(t *testing.T) {
	for _, out := range []string{"", "ok", "ok\x00/workspace/a", "ok\x00/workspace/a\x00x\x00", "what\x00ever", "ok\x00relative\x003\x00abc"} {
		if _, _, _, err := parseRead([]byte(out)); err == nil {
			t.Errorf("%q parsed", out)
		}
	}
}

func TestChatIn(t *testing.T) {
	f := newFixture(t)
	f.app.store.CreateSession(store.Session{ID: "c1", Project: "orbit", Harness: "claude", Mode: store.ModeChat, Status: store.StatusRunning})
	f.app.store.CreateSession(store.Session{ID: "t1", Project: "orbit", Harness: "claude", Status: store.StatusRunning})
	f.app.store.CreateSession(store.Session{ID: "c2", Project: "orbit", Harness: "claude", Mode: store.ModeChat, Status: store.StatusExited})
	if !f.app.ChatIn("orbit", "c1") {
		t.Error("a running chat isn't in its project")
	}
	for _, c := range [][2]string{{"bifract", "c1"}, {"orbit", "t1"}, {"orbit", "c2"}, {"orbit", "nope"}} {
		if f.app.ChatIn(c[0], c[1]) {
			t.Errorf("ChatIn(%q, %q)", c[0], c[1])
		}
	}
}

func TestReadSandboxFileRunsInTheSandbox(t *testing.T) {
	f := newFixture(t)
	var got []string
	f.docker.set(func() {
		f.docker.running["skybreaker-orbit"] = true
		f.docker.execOut = func(args []string) (string, error) {
			got = args
			return "ok\x00/workspace/a.txt\x005\x00hello", nil
		}
	})
	p, size, data, err := f.app.ReadSandboxFile(context.Background(), "orbit", "", "a.txt", 64)
	if err != nil || p != "/workspace/a.txt" || size != 5 || string(data) != "hello" {
		t.Fatalf("read = %q %d %q %v", p, size, data, err)
	}
	want := []string{"sh", "-c", readScript, "sh", "a.txt", "64", "/workspace", "/tmp"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("exec = %q", got)
	}
	if _, _, _, err := f.app.ReadSandboxFile(context.Background(), "nope", "", "a.txt", 64); err == nil {
		t.Error("read from an unknown project")
	}
}

// A big file, a podcast, is read whole: past the default limit on what a
// sandbox command may print, up to what was asked for.
func TestReadSandboxFileReadsBigFiles(t *testing.T) {
	f := newFixture(t)
	f.docker.set(func() {
		f.docker.running["skybreaker-orbit"] = true
		f.docker.execOut = func([]string) (string, error) { return "ok\x00/workspace/ep.mp3\x003\x00ID3", nil }
	})
	const want = 100 << 20
	if _, _, _, err := f.app.ReadSandboxFile(context.Background(), "orbit", "", "ep.mp3", want); err != nil {
		t.Fatal(err)
	}
	f.docker.mu.Lock()
	defer f.docker.mu.Unlock()
	if n := len(f.docker.limits); n == 0 || f.docker.limits[n-1] < want {
		t.Errorf("read with output limits %v, want at least %d", f.docker.limits, want)
	}
}

// An agent in a chat calls show_image over the skybreaker MCP server, as
// it's mounted on the hook listener, and the chat gets the card and a
// copy it can serve.
func TestShowEndToEnd(t *testing.T) {
	f := newFixture(t)
	png := "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"
	f.docker.set(func() {
		f.docker.running["skybreaker-orbit"] = true
		f.docker.execOut = func(args []string) (string, error) {
			if len(args) > 6 && args[0] == "sh" && args[2] == readScript && args[4] == "shot.png" {
				return "ok\x00/workspace/shot.png\x00" + strconv.Itoa(len(png)) + "\x00" + png, nil
			}
			return "", nil
		}
	})
	f.app.store.CreateSession(store.Session{ID: "c1", Project: "orbit", Harness: "claude", Mode: store.ModeChat, Status: store.StatusRunning})
	srv := &mcp.Server{Name: "skybreaker", Tools: []func(mcp.Caller) []mcp.Tool{show.Tools(noAssistants{f.app})}}
	h := srv.Handler(f.app.MCPCaller)
	from := func(project string) string { return f.app.cfg.Subnet(project).Addr().Next().Next().String() + ":40000" }
	rpc := func(path, remote, sid, body string) (*httptest.ResponseRecorder, map[string]any) {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.RemoteAddr, r.Header["Content-Type"] = remote, []string{"application/json"}
		if sid != "" {
			r.Header.Set("Mcp-Session-Id", sid)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var m map[string]any
		json.Unmarshal(w.Body.Bytes(), &m)
		return w, m
	}
	const initReq = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","clientInfo":{"name":"claude-code","version":"2"}}}`
	open := func(session string) string {
		w, _ := rpc("/mcp/orbit/"+f.app.HookToken("orbit")+"/"+session, from("orbit"), "", initReq)
		if w.Code != 200 || w.Header().Get("Mcp-Session-Id") == "" {
			t.Fatalf("initialize: %d %s", w.Code, w.Body)
		}
		return w.Header().Get("Mcp-Session-Id")
	}
	path := "/mcp/orbit/" + f.app.HookToken("orbit") + "/c1"
	sid := open("c1")
	_, list := rpc(path, from("orbit"), sid, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if b, _ := json.Marshal(list); !strings.Contains(string(b), `"show_image"`) || !strings.Contains(string(b), `"show_file"`) {
		t.Fatalf("tools/list = %s", b)
	}
	_, res := rpc(path, from("orbit"), sid, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"show_image","arguments":{"path":"shot.png","caption":"Login page"}}}`)
	if b, _ := json.Marshal(res); !strings.Contains(string(b), "Shown to the user") || strings.Contains(string(b), `"isError":true`) {
		t.Fatalf("tools/call = %s", b)
	}
	events, err := f.app.ChatEvents("c1", 0)
	if err != nil {
		t.Fatal(err)
	}
	var shownEv *chat.Event
	for i := range events {
		if events[i].Kind == chat.KindShow {
			shownEv = &events[i]
		}
	}
	if shownEv == nil || shownEv.Show.Path != "shot.png" || shownEv.Show.Title != "Login page" || shownEv.Show.Kind != chat.ShowImage {
		t.Fatalf("events = %+v", events)
	}
	if b, err := f.app.ChatShown("c1", shownEv.ID); err != nil || string(b) != png {
		t.Errorf("kept copy = %q, %v", b, err)
	}

	// Outside a chat (a scheduled run's "-"), there's nothing to show in.
	sid = open("-")
	_, list = rpc("/mcp/orbit/"+f.app.HookToken("orbit")+"/-", from("orbit"), sid, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if b, _ := json.Marshal(list); strings.Contains(string(b), "show_") {
		t.Errorf("tools outside a chat: %s", b)
	}
	// Another project's sandbox can't use orbit's URL, token or chat (404: a 403 would read as "needs sign-in").
	if w, _ := rpc(path, from("bifract"), "", initReq); w.Code != http.StatusNotFound {
		t.Errorf("from bifract's network: %d", w.Code)
	}
	if w, _ := rpc("/mcp/bifract/"+f.app.HookToken("bifract")+"/c1", from("bifract"), "", initReq); w.Code != http.StatusNotFound {
		t.Errorf("bifract naming orbit's chat: %d", w.Code)
	}
}

// noAssistants is the app as show's backend with no assistants: the
// server's own host adds them.
type noAssistants struct{ *App }

func (noAssistants) IsAssistant(string) bool { return false }

func (noAssistants) AssistantShare(string, string, assistant.Share) (assistant.Item, error) {
	return assistant.Item{}, nil
}

func (noAssistants) AssistantCollection(string, string, assistant.CollectionPatch, *assistant.Blob) (assistant.Collection, error) {
	return assistant.Collection{}, nil
}
