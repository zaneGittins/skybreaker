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
	files  map[string]string // resolved path → content
	reads  []int64           // the max of each read
	shown  []shown
	shared []shown // to an assistant's Today, its project in session
}

type shown struct {
	session string
	sh      chat.Show
	data    []byte
	ext     string
}

func (f *fakeBackend) ChatIn(project, session string) bool { return project == "p" && session == "s1" }

func (f *fakeBackend) IsAssistant(project string) bool { return project == "pip" }

func (f *fakeBackend) AssistantShare(project string, sh chat.Show, data []byte, ext string) error {
	f.shared = append(f.shared, shown{project, sh, append([]byte(nil), data...), ext})
	return nil
}

func (f *fakeBackend) ReadSandboxFile(_ context.Context, project, _, path string, max int64) (string, int64, []byte, error) {
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
	// Anything that isn't text is shared as what it is: an image to see, a
	// download otherwise.
	for p, want := range map[string]struct{ kind, ext string }{"bin": {chat.ShowDownload, ".bin"}, "shot.png": {chat.ShowImage, ".png"}, "latin1.txt": {chat.ShowDownload, ".bin"}} {
		n := len(b.shown)
		if _, err := call(t, b, inChat, "show_file", `{"path":"`+p+`"}`); err != nil || len(b.shown) != n+1 {
			t.Fatalf("%s: %v", p, err)
		}
		if s := b.shown[n]; s.sh.Kind != want.kind || s.ext != want.ext || string(s.data) != b.files["/workspace/"+p] || s.sh.Path != p {
			t.Errorf("%s: %+v %s", p, s.sh, s.ext)
		}
	}
	if _, err := call(t, b, inChat, "show_file", `{"path":"bin","start_line":1}`); err == nil {
		t.Error("lines of a binary file")
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

// A podcast the agent made, or downloaded, is shared to be played: read
// whole, past what's read of text, up to the limit.
func TestShareAudioAndBigFiles(t *testing.T) {
	was := maxShare
	maxShare = 64 << 10
	t.Cleanup(func() { maxShare = was })
	mp3 := "ID3\x04\x00\x00\x00\x00\x00\x00" + strings.Repeat("\xff\xfb", int(maxShare/4))
	b := &fakeBackend{files: map[string]string{
		"/workspace/downloads/podcast.mp3": mp3,
		"/workspace/huge.zip":              "PK\x03\x04" + strings.Repeat("z", int(maxShare)),
	}}
	if out, err := call(t, b, inChat, "show_file", `{"path":"downloads/podcast.mp3","title":"This week's podcast"}`); err != nil || !strings.Contains(out, "podcast.mp3") {
		t.Fatalf("%q %v", out, err)
	}
	if s := b.shown[0]; s.sh.Kind != chat.ShowAudio || s.ext != ".mp3" || string(s.data) != mp3 || s.sh.Title != "This week's podcast" {
		t.Errorf("audio: %+v %s %d bytes", s.sh, s.ext, len(s.data))
	}
	if _, err := call(t, b, inChat, "show_file", `{"path":"huge.zip"}`); err == nil || !strings.Contains(err.Error(), "MB") {
		t.Errorf("past the limit: %v", err)
	}
}

// An assistant's routine has no chat: what it shares goes on its Today.
// A repository's runs share nothing.
func TestShareFromARoutine(t *testing.T) {
	b := &fakeBackend{files: map[string]string{"/workspace/downloads/podcast.mp3": "ID3\x04\x00podcast", "/workspace/brief.md": "# Brief\n"}}
	run := mcp.Caller{Project: "pip", Session: "-"}
	tools := Tools(b)(run)
	if len(tools) != 1 || tools[0].Name != "show_file" || !strings.Contains(tools[0].Description, "Today") {
		t.Fatalf("tools = %+v", tools)
	}
	if out, err := call(t, b, run, "show_file", `{"path":"downloads/podcast.mp3","title":"Sunday's podcast"}`); err != nil || !strings.Contains(out, "Today") {
		t.Fatalf("%q %v", out, err)
	}
	if len(b.shared) != 1 || b.shared[0].session != "pip" || b.shared[0].sh.Kind != chat.ShowAudio || b.shared[0].ext != ".mp3" || b.shared[0].sh.Title != "Sunday's podcast" {
		t.Fatalf("shared %+v", b.shared)
	}
	// Text goes as a file to open, not as lines.
	if _, err := call(t, b, run, "show_file", `{"path":"brief.md"}`); err != nil || b.shared[1].sh.Kind != chat.ShowDownload {
		t.Errorf("text from a routine: %v %+v", err, b.shared)
	}
	if len(b.shown) != 0 {
		t.Error("shown in a chat")
	}
	if tools := Tools(b)(mcp.Caller{Project: "p", Session: "-"}); len(tools) != 0 {
		t.Errorf("a repository's run got %d tools", len(tools))
	}
}
