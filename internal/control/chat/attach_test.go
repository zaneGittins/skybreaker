package chat

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/zaneGittins/skybreaker/internal/control/store"
)

var redPNG = Image{Mime: "image/png", Data: []byte("\x89PNG\r\n\x1a\nnot really")}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// Claude takes images as content blocks beside the text.
func TestClaudeSendsImages(t *testing.T) {
	d := newClaude()
	lines, err := d.Send("what is this?", redPNG)
	if err != nil {
		t.Fatal(err)
	}
	content, ok := decodeLine(t, lines[0])["message"].(map[string]any)["content"].([]any)
	if !ok || len(content) != 2 {
		t.Fatalf("send = %s", lines[0])
	}
	if c := content[0].(map[string]any); c["type"] != "text" || c["text"] != "what is this?" {
		t.Errorf("text block = %v", c)
	}
	img := content[1].(map[string]any)
	src, _ := img["source"].(map[string]any)
	if img["type"] != "image" || src["type"] != "base64" || src["media_type"] != "image/png" || src["data"] != b64(redPNG.Data) {
		t.Errorf("image block = %v", img)
	}
}

// Codex takes images as data URLs, whether the message starts a turn,
// steers one, or waits for the thread or the turn.
func TestCodexSendsImages(t *testing.T) {
	url := "data:image/png;base64," + b64(redPNG.Data)
	hasImage := func(l []byte) bool { return strings.Contains(string(l), `"type":"image","url":"`+url+`"`) }

	d, _ := handshake(t, Options{Cwd: "/workspace", Mode: ModeAsk})
	lines, _ := d.Send("first", redPNG)
	if !hasImage(lines[0]) {
		t.Errorf("turn/start = %s", lines[0])
	}
	if lines, _ := d.Send("held", redPNG); len(lines) != 0 {
		t.Fatalf("sent before the turn started: %q", lines)
	}
	out := d.Read([]byte(`{"method":"turn/started","params":{"threadId":"thr-1","turn":{"id":"turn-1","items":[],"status":"inProgress"}}}`))
	if len(out.Write) != 1 || !hasImage(out.Write[0]) {
		t.Errorf("held steer = %q", out.Write)
	}
	lines, _ = d.Send("steer", redPNG)
	if !hasImage(lines[0]) {
		t.Errorf("turn/steer = %s", lines[0])
	}

	q := newCodex()
	q.Open(Options{Cwd: "/workspace"})
	q.Send("early", redPNG)
	out = q.Read([]byte(`{"id":1,"result":{}}`))
	var startID any
	for _, w := range out.Write {
		if m := decodeLine(t, w); m["method"] == "thread/start" {
			startID = m["id"]
		}
	}
	out = q.Read(line(map[string]any{"id": startID, "result": map[string]any{"thread": map[string]any{"id": "thr-9"}, "model": "m"}}))
	if len(out.Write) != 1 || !hasImage(out.Write[0]) {
		t.Errorf("queued = %q", out.Write)
	}
}

func TestPiSendsImages(t *testing.T) {
	m := decodeLine(t, must(t)(newPi().Send("look", redPNG))[0])
	imgs, _ := m["images"].([]any)
	if m["message"] != "look" || len(imgs) != 1 {
		t.Fatalf("send = %v", m)
	}
	if img := imgs[0].(map[string]any); img["type"] != "image" || img["mimeType"] != "image/png" || img["data"] != b64(redPNG.Data) {
		t.Errorf("image = %v", img)
	}
	if m := decodeLine(t, must(t)(newPi().Send("no image"))[0]); m["images"] != nil {
		t.Errorf("images without any = %v", m)
	}
}

// attachHarness is a harness whose sandbox writes are recorded.
func attachHarness(t *testing.T) (*harness, map[string][]byte, *sync.Mutex) {
	h := newHarness(t)
	var mu sync.Mutex
	wrote := map[string][]byte{}
	h.m.c.Attach = func(ctx context.Context, s store.Session, files []SandboxFile) error {
		mu.Lock()
		defer mu.Unlock()
		for _, f := range files {
			wrote[f.Path] = f.Data
		}
		return nil
	}
	return h, wrote, &mu
}

func TestManagerSendsAttachments(t *testing.T) {
	h, wrote, mu := attachHarness(t)
	s := h.session("claude")
	png := []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("x", 100))
	logText := []byte("error: boom\n")
	err := h.m.Send(context.Background(), s.ID, "see these", File{Name: "shot.png", Data: png}, File{Name: "../build log.txt", Data: logText})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the turn to end", func() bool { return len(find(h.events(s.ID), KindTurn)) == 2 })

	user := find(h.events(s.ID), KindUser)[0]
	if user.Text != "see these" || len(user.Attach) != 2 {
		t.Fatalf("user = %+v", user)
	}
	img, file := user.Attach[0], user.Attach[1]
	dir := AttachDir + "/" + s.ID + "/"
	if img.Kind != AttachImage || img.Name != "shot.png" || img.Size != int64(len(png)) || !strings.HasPrefix(img.Path, dir) || !strings.HasSuffix(img.Path, "-shot.png") {
		t.Errorf("image = %+v", img)
	}
	if file.Kind != AttachFile || file.Name != "build log.txt" || !strings.HasPrefix(file.Path, dir) || !strings.HasSuffix(file.Path, "-build_log.txt") {
		t.Errorf("file = %+v", file)
	}
	mu.Lock()
	if !bytes.Equal(wrote[img.Path], png) || !bytes.Equal(wrote[file.Path], logText) {
		t.Errorf("sandbox got %q", wrote)
	}
	mu.Unlock()
	for _, a := range user.Attach {
		if b, err := h.m.Shown(s.ID, a.Kept); err != nil || len(b) != int(a.Size) {
			t.Errorf("kept %s = %d bytes, %v", a.Kept, len(b), err)
		}
	}

	// The model sees the image, and where both files are.
	h.mu.Lock()
	f := h.procs[0]
	h.mu.Unlock()
	var content []any
	for _, m := range f.received() {
		if m["type"] == "user" {
			content, _ = m["message"].(map[string]any)["content"].([]any)
		}
	}
	if len(content) != 2 {
		t.Fatalf("content = %v", content)
	}
	text := content[0].(map[string]any)["text"].(string)
	if !strings.HasPrefix(text, "see these") || !strings.Contains(text, img.Path) || !strings.Contains(text, file.Path) {
		t.Errorf("text = %q", text)
	}
	if src := content[1].(map[string]any)["source"].(map[string]any); src["data"] != b64(png) {
		t.Errorf("image = %v", src)
	}
}

// A message can be only attachments; the chat is named for the first.
func TestManagerSendsOnlyAttachments(t *testing.T) {
	h, _, _ := attachHarness(t)
	s := h.session("claude")
	if err := h.m.Send(context.Background(), s.ID, "  ", File{Name: "server.log", Data: []byte("line\n")}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the turn to end", func() bool { return len(find(h.events(s.ID), KindTurn)) == 2 })
	if got := h.state(s.ID).Title; got != "server.log" {
		t.Errorf("title = %q", got)
	}
	if u := find(h.events(s.ID), KindUser)[0]; u.Text != "" || len(u.Attach) != 1 {
		t.Errorf("user = %+v", u)
	}
}

// An image too large for the models goes as a file: the agent can still
// open it in the sandbox.
func TestManagerSendsLargeImageAsFile(t *testing.T) {
	h, _, _ := attachHarness(t)
	s := h.session("claude")
	big := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, maxImageInput)...)
	if err := h.m.Send(context.Background(), s.ID, "big", File{Name: "big.png", Data: big}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the turn to end", func() bool { return len(find(h.events(s.ID), KindTurn)) == 2 })
	if u := find(h.events(s.ID), KindUser)[0]; u.Attach[0].Kind != AttachImage {
		t.Errorf("shown as %q", u.Attach[0].Kind)
	}
	h.mu.Lock()
	f := h.procs[0]
	h.mu.Unlock()
	for _, m := range f.received() {
		if m["type"] == "user" {
			if _, ok := m["message"].(map[string]any)["content"].(string); !ok {
				t.Errorf("large image sent as image input")
			}
		}
	}
}

func TestManagerRefusesAttachments(t *testing.T) {
	h, wrote, mu := attachHarness(t)
	s := h.session("claude")
	many := make([]File, maxAttachments+1)
	for i := range many {
		many[i] = File{Name: "f.txt", Data: []byte{byte(i)}}
	}
	cases := map[string][]File{
		"too many":  many,
		"too large": {{Name: "huge.bin", Data: make([]byte, maxAttachment+1)}},
		"empty":     {{Name: "empty.txt"}},
	}
	for name, files := range cases {
		if err := h.m.Send(context.Background(), s.ID, "hi", files...); err == nil {
			t.Errorf("%s: sent", name)
		}
	}
	// The write fails: no file is kept.
	h.m.c.Attach = func(context.Context, store.Session, []SandboxFile) error { return errors.New("sandbox down") }
	first := []byte("first")
	if err := h.m.Send(context.Background(), s.ID, "hi", File{Name: "a.txt", Data: first}, File{Name: "b.txt", Data: []byte("b")}); err == nil || !strings.Contains(err.Error(), "sandbox down") {
		t.Errorf("failed write = %v", err)
	}
	if _, err := h.m.Shown(s.ID, keptName(first, ".bin")); err == nil {
		t.Error("a file of a refused message was kept")
	}
	if ev := h.events(s.ID); len(ev) != 0 {
		t.Errorf("refused messages recorded: %+v", ev)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(wrote) != 0 {
		t.Errorf("refused messages wrote %d files", len(wrote))
	}
}

func TestAttachName(t *testing.T) {
	for in, want := range map[string]string{
		"shot.png":                        "shot.png",
		"../../etc/passwd":                "passwd",
		`C:\Users\me\log.txt`:             "log.txt",
		"my report (1).pdf":               "my_report_1_.pdf",
		".bashrc":                         "bashrc",
		"-rf":                             "rf",
		"":                                "file",
		"日本.txt":                          "_.txt",
		strings.Repeat("a", 300) + ".txt": strings.Repeat("a", 96) + ".txt",
	} {
		if got := attachName(in); got != want {
			t.Errorf("attachName(%q) = %q, want %q", in, got, want)
		}
	}
}

// A full attachment shelf refuses a message before anything is written.
func TestManagerAttachmentQuota(t *testing.T) {
	h, wrote, mu := attachHarness(t)
	s := h.session("claude")
	saved := attachedShelf
	attachedShelf.maxBytes = 10
	t.Cleanup(func() { attachedShelf = saved })
	if err := h.m.Send(context.Background(), s.ID, "hi", File{Name: "a.txt", Data: []byte("0123456789x")}); err == nil || !strings.Contains(err.Error(), "limit of attachments") {
		t.Errorf("over the quota = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(wrote) != 0 {
		t.Errorf("wrote %d files", len(wrote))
	}
}

// Shown files and attachments have a quota each, so neither crowds out
// the other; Kept finds both, and a fork copies both.
func TestLogShelves(t *testing.T) {
	l, _ := OpenLog(t.TempDir())
	big := bytes.Repeat([]byte("x"), int(shownShelf.maxBytes))
	if _, err := l.Keep("s1", big, ".txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Keep("s1", []byte("more"), ".txt"); err == nil {
		t.Fatal("the shown shelf has no quota")
	}
	if err := l.AttachRoom("s1", [][]byte{[]byte("log")}); err != nil {
		t.Errorf("attachments crowded out: %v", err)
	}
	att, err := l.KeepAttached("s1", []byte("log"), ".bin")
	if err != nil {
		t.Fatal(err)
	}
	if b, err := l.Kept("s1", att); err != nil || string(b) != "log" {
		t.Errorf("kept attachment = %q, %v", b, err)
	}
	// Room counts content already kept once.
	saved := attachedShelf
	attachedShelf.maxBytes = 5
	t.Cleanup(func() { attachedShelf = saved })
	if err := l.AttachRoom("s1", [][]byte{[]byte("log"), []byte("log"), []byte("ab")}); err != nil {
		t.Errorf("room = %v", err)
	}
	if err := l.AttachRoom("s1", [][]byte{[]byte("abcdef")}); err == nil {
		t.Error("room past the quota")
	}
	l.Delete("s1")
	if _, err := l.Kept("s1", att); err == nil {
		t.Error("delete left the attachment")
	}
}

// Images go as image input until a message's share is used; the rest go
// as files.
func TestManagerCapsImageInput(t *testing.T) {
	h, _, _ := attachHarness(t)
	s := h.session("claude")
	var files []File
	for i := range 4 {
		files = append(files, File{Name: fmt.Sprintf("%d.png", i), Data: append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{byte(i)}, 3<<20)...)})
	}
	if err := h.m.Send(context.Background(), s.ID, "four", files...); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the turn to end", func() bool { return len(find(h.events(s.ID), KindTurn)) == 2 })
	h.mu.Lock()
	f := h.procs[0]
	h.mu.Unlock()
	for _, m := range f.received() {
		if m["type"] == "user" {
			content := m["message"].(map[string]any)["content"].([]any)
			if images := len(content) - 1; images != 3 {
				t.Errorf("%d images as input, want 3", images)
			}
		}
	}
}

func TestDisplayName(t *testing.T) {
	long := strings.Repeat("é", 150) + ".png"
	for in, want := range map[string]string{
		"shot.png":               "shot.png",
		"a\nb\x00c\u202eгnp.exe": "abcгnp.exe",
		"dir/  ":                 "file",
		long:                     strings.Repeat("é", 98) + "…",
	} {
		if got := displayName(in); got != want {
			t.Errorf("displayName(%q) = %q, want %q", in, got, want)
		}
	}
}

// A harness that stops reading can't make the host hold its messages:
// past maxQueued bytes, its input closes.
func TestWriteBoundsQueuedBytes(t *testing.T) {
	m := NewManager(Config{})
	defer m.Close()
	r := &run{id: "s1", in: make(chan []byte, 10), queued: new(atomic.Int64)}
	big := make([]byte, maxQueued/2)
	m.write(r, [][]byte{big})
	if r.in == nil {
		t.Fatal("closed under the limit")
	}
	m.write(r, [][]byte{big})
	if r.in != nil {
		t.Errorf("open with %d bytes queued", r.queued.Load())
	}
}
