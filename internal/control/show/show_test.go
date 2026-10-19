package show

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/zaneGittins/skybreaker/internal/control/chat"
	"github.com/zaneGittins/skybreaker/internal/control/mcp"
)

// fakeBackend is a sandbox with files and one running chat, s1 in p.
type fakeBackend struct {
	files map[string]string // resolved path → content
	reads []int64           // the max of each read
	shown []shown
}

type shown struct {
	session string
	sh      chat.Show
	data    []byte
	ext     string
}

func (f *fakeBackend) ChatIn(project, session string) bool { return project == "p" && session == "s1" }

func (f *fakeBackend) ReadSandboxFile(_ context.Context, project, path string, max int64) (string, int64, []byte, error) {
	f.reads = append(f.reads, max)
	if !strings.HasPrefix(path, "/") {
		path = "/workspace/" + path
	}
	c, ok := f.files[path]
	if !ok {
		return "", 0, nil, errors.New("no such file: " + path)
	}
	data := []byte(c)
	if int64(len(data)) > max {
		data = data[:max]
	}
	return path, int64(len(c)), data, nil
}

func (f *fakeBackend) ChatShow(session string, sh chat.Show, data []byte, ext string) error {
	f.shown = append(f.shown, shown{session, sh, append([]byte(nil), data...), ext})
	return nil
}

var png1x1 = "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89"

func call(t *testing.T, b *fakeBackend, c mcp.Caller, name, args string) (string, error) {
	t.Helper()
	for _, tool := range Tools(b)(c) {
		if tool.Name == name {
			if !json.Valid(tool.Input) {
				t.Fatalf("%s schema isn't JSON: %s", name, tool.Input)
			}
			return tool.Call(context.Background(), c, json.RawMessage(args))
		}
	}
	t.Fatalf("no tool %s for %+v", name, c)
	return "", nil
}

var inChat = mcp.Caller{Project: "p", Session: "s1"}

func TestToolsOnlyInARunningChat(t *testing.T) {
	b := &fakeBackend{}
	for _, c := range []mcp.Caller{
		{Project: "p", Session: ""}, {Project: "p", Session: "-"}, {Project: "", Session: "s1", User: "zane"},
		{Project: "q", Session: "s1"}, {Project: "p", Session: "s2"},
	} {
		if tools := Tools(b)(c); len(tools) != 0 {
			t.Errorf("%+v got %d tools", c, len(tools))
		}
	}
	tools := Tools(b)(inChat)
	if len(tools) != 2 || tools[0].Name != "show_file" || tools[1].Name != "show_image" || !tools[0].ReadOnly {
		t.Errorf("tools = %+v", tools)
	}
}

func TestShowImage(t *testing.T) {
	b := &fakeBackend{files: map[string]string{"/workspace/shot.png": png1x1, "/tmp/out/x.webp": "RIFF\x10\x00\x00\x00WEBPVP8 ", "/workspace/a.svg": `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`, "/workspace/fake.png": "<html>hi</html>"}}
	out, err := call(t, b, inChat, "show_image", `{"path":"shot.png","caption":"The login page"}`)
	if err != nil || !strings.Contains(out, "Shown to the user") {
		t.Fatalf("show_image = %q, %v", out, err)
	}
	s := b.shown[0]
	if s.session != "s1" || s.ext != ".png" || s.sh.Kind != chat.ShowImage || s.sh.Path != "shot.png" || s.sh.Title != "The login page" || string(s.data) != png1x1 {
		t.Errorf("shown = %+v", s)
	}
	if b.reads[0] != maxImage+1 {
		t.Errorf("read max = %d", b.reads[0])
	}
	if _, err := call(t, b, inChat, "show_image", `{"path":"/tmp/out/x.webp"}`); err != nil || b.shown[1].ext != ".webp" || b.shown[1].sh.Path != "/tmp/out/x.webp" {
		t.Errorf("webp: %v %+v", err, b.shown)
	}
	// The type comes from the bytes, never the name; SVG and HTML aren't images here.
	for _, p := range []string{"a.svg", "fake.png"} {
		if _, err := call(t, b, inChat, "show_image", `{"path":"`+p+`"}`); err == nil {
			t.Errorf("%s was shown as an image", p)
		}
	}
	if _, err := call(t, b, inChat, "show_image", `{"path":"nope.png"}`); err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Errorf("missing file: %v", err)
	}
	b.files["/workspace/huge.png"] = png1x1 + strings.Repeat("\x00", maxImage)
	if _, err := call(t, b, inChat, "show_image", `{"path":"huge.png"}`); err == nil || !strings.Contains(err.Error(), "MB") {
		t.Errorf("huge image: %v", err)
	}
}

func TestShowFile(t *testing.T) {
	src := "package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n"
	b := &fakeBackend{files: map[string]string{
		"/workspace/main.go": src, "/workspace/README.md": "# Title\n\nText.\n",
		"/workspace/bin": "\x7fELF\x00\x01", "/workspace/shot.png": png1x1, "/workspace/latin1.txt": "caf\xe9\n",
	}}
	if _, err := call(t, b, inChat, "show_file", `{"path":"main.go","title":"Entry point"}`); err != nil {
		t.Fatal(err)
	}
	s := b.shown[0]
	if s.ext != ".txt" || s.sh.Kind != chat.ShowText || string(s.data) != src || s.sh.Start != 1 || s.sh.End != 5 || s.sh.Title != "Entry point" || s.sh.Size != int64(len(src)) {
		t.Errorf("shown = %+v %q", s.sh, s.data)
	}
	if _, err := call(t, b, inChat, "show_file", `{"path":"main.go","start_line":3,"end_line":4}`); err != nil {
		t.Fatal(err)
	}
	if s := b.shown[1]; string(s.data) != "func main() {\n\tprintln(\"hi\")\n" || s.sh.Start != 3 || s.sh.End != 4 {
		t.Errorf("range = %+v %q", s.sh, s.data)
	}
	// An end past the file stops at its last line; a start past it is an error.
	if _, err := call(t, b, inChat, "show_file", `{"path":"main.go","start_line":4,"end_line":99}`); err != nil || b.shown[2].sh.End != 5 {
		t.Errorf("end past the file: %v %+v", err, b.shown[2].sh)
	}
	for _, args := range []string{`{"path":"main.go","start_line":9}`, `{"path":"main.go","start_line":3,"end_line":2}`, `{"path":"main.go","start_line":-1}`} {
		if _, err := call(t, b, inChat, "show_file", args); err == nil {
			t.Errorf("%s was shown", args)
		}
	}
	if _, err := call(t, b, inChat, "show_file", `{"path":"README.md"}`); err != nil || b.shown[3].sh.Kind != chat.ShowMarkdown {
		t.Errorf("markdown: %v %+v", err, b.shown[3].sh)
	}
	// A range of a markdown file is source, not a rendered document.
	if _, err := call(t, b, inChat, "show_file", `{"path":"README.md","start_line":1,"end_line":1}`); err != nil || b.shown[4].sh.Kind != chat.ShowText {
		t.Errorf("markdown range: %v %+v", err, b.shown[4].sh)
	}
	n := len(b.shown)
	for p, want := range map[string]string{"bin": "binary", "shot.png": "show_image", "latin1.txt": "UTF-8"} {
		if _, err := call(t, b, inChat, "show_file", `{"path":"`+p+`"}`); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", p, err, want)
		}
	}
	if len(b.shown) != n {
		t.Error("a refused file was shown")
	}
	for _, args := range []string{`{}`, `{"path":""}`, `{"path":"main.go","extra":1}`, `{"path":"a\u0000b"}`, `{"path":"` + strings.Repeat("a", 5000) + `"}`} {
		if _, err := call(t, b, inChat, "show_file", args); err == nil {
			t.Errorf("%.60s was accepted", args)
		}
	}
}

func TestShowFileTruncates(t *testing.T) {
	line := strings.Repeat("x", 99) + "\n"
	big := strings.Repeat(line, maxText/len(line)+50)
	b := &fakeBackend{files: map[string]string{"/workspace/big.log": big}}
	if _, err := call(t, b, inChat, "show_file", `{"path":"big.log"}`); err != nil {
		t.Fatal(err)
	}
	s := b.shown[0]
	if !s.sh.Truncated || len(s.data) > maxText || !bytes.HasSuffix(s.data, []byte("\n")) || s.sh.End != len(s.data)/len(line) {
		t.Errorf("truncated = %v, %d bytes, end %d", s.sh.Truncated, len(s.data), s.sh.End)
	}
	// Past what's read from the sandbox, a range can't be reached.
	b.files["/workspace/huge.log"] = strings.Repeat(line, maxTextRead/len(line)+10)
	if _, err := call(t, b, inChat, "show_file", `{"path":"huge.log","start_line":`+itoa(maxTextRead/len(line)+5)+`}`); err == nil || !strings.Contains(err.Error(), "MB") {
		t.Errorf("unreachable range: %v", err)
	}
}

func TestCaptionsAreBounded(t *testing.T) {
	b := &fakeBackend{files: map[string]string{"/workspace/a.txt": "a\n"}}
	if _, err := call(t, b, inChat, "show_file", `{"path":"a.txt","title":"`+strings.Repeat("t", 1000)+`"}`); err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(b.shown[0].sh.Title)); n > maxTitle+1 {
		t.Errorf("title is %d runes", n)
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }
