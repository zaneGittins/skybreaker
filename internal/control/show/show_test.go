package show

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/zaneGittins/skybreaker/internal/assistant"
	"github.com/zaneGittins/skybreaker/internal/control/chat"
	"github.com/zaneGittins/skybreaker/internal/control/mcp"
)

// fakeBackend is a sandbox with files and one running chat, s1 in p.
type fakeBackend struct {
	files       map[string]string // resolved path → content
	reads       []int64           // the max of each read
	shown       []shown
	shared      []libShare // to an assistant's library
	items       []chat.Show
	collections []libCollection
}

type libShare struct {
	project, session string
	sh               assistant.Share
}

type libCollection struct {
	project, title string
	p              assistant.CollectionPatch
	cover          *assistant.Blob
}

type shown struct {
	session string
	sh      chat.Show
	data    []byte
	ext     string
}

func (f *fakeBackend) ChatIn(project, session string) bool {
	return (project == "p" || project == "pip") && session == "s1"
}

func (f *fakeBackend) IsAssistant(project string) bool { return project == "pip" }

func (f *fakeBackend) AssistantShare(project, session string, sh assistant.Share) (assistant.Item, error) {
	f.shared = append(f.shared, libShare{project, session, sh})
	return assistant.Item{ID: fmt.Sprintf("i%d", len(f.shared)), Kind: sh.Item.Kind, Title: sh.Item.Title, Size: int64(len(sh.Data))}, nil
}

func (f *fakeBackend) AssistantCollection(project, title string, p assistant.CollectionPatch, cover *assistant.Blob) (assistant.Collection, error) {
	f.collections = append(f.collections, libCollection{project, title, p, cover})
	c := assistant.Collection{Title: title, Layout: "list", KeepDays: 30}
	if p.Layout != nil {
		c.Layout = *p.Layout
	}
	if p.Keep != nil {
		c.Keep = *p.Keep
	}
	return c, nil
}

func (f *fakeBackend) ChatShowItem(session string, sh chat.Show) error {
	f.items = append(f.items, sh)
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

// An assistant's routine has no chat: what it shares goes in its library
// and on Today. A repository's runs share nothing.
func TestShareFromARoutine(t *testing.T) {
	b := &fakeBackend{files: map[string]string{
		"/workspace/downloads/podcast.mp3": "ID3\x04\x00podcast", "/workspace/brief.md": "# Brief\n",
		"/workspace/plan.html": "<!doctype html><html><body>plan</body></html>", "/workspace/report.pdf": "%PDF-1.7\n",
	}}
	run := mcp.Caller{Project: "pip", Session: "-"}
	tools := Tools(b)(run)
	if len(tools) != 2 || tools[0].Name != "show_file" || tools[1].Name != "set_collection" || !strings.Contains(tools[0].Description, "Today") {
		t.Fatalf("tools = %+v", tools)
	}
	if out, err := call(t, b, run, "show_file", `{"path":"downloads/podcast.mp3","title":"Sunday's podcast","collection":"Weekly","summary":"Bread"}`); err != nil || !strings.Contains(out, "Today") {
		t.Fatalf("%q %v", out, err)
	}
	s := b.shared[0]
	if s.project != "pip" || s.session != "" || s.sh.Item.Kind != assistant.KindAudio || s.sh.Ext != ".mp3" || s.sh.Item.Title != "Sunday's podcast" ||
		s.sh.Item.Collection != "Weekly" || s.sh.Item.Summary != "Bread" || s.sh.Item.Name != "/workspace/downloads/podcast.mp3" {
		t.Fatalf("shared %+v", s)
	}
	// Text is read in the reader; HTML from a routine is always a page; a PDF is a PDF.
	for p, want := range map[string][2]string{"brief.md": {assistant.KindText, ".md"}, "plan.html": {assistant.KindView, ".html"}, "report.pdf": {assistant.KindPDF, ".pdf"}} {
		if _, err := call(t, b, run, "show_file", `{"path":"`+p+`"}`); err != nil {
			t.Fatal(err)
		}
		if got := b.shared[len(b.shared)-1].sh; got.Item.Kind != want[0] || got.Ext != want[1] {
			t.Errorf("%s: %s %s", p, got.Item.Kind, got.Ext)
		}
	}
	if len(b.shown) != 0 || len(b.items) != 0 {
		t.Error("shown in a chat")
	}
	if tools := Tools(b)(mcp.Caller{Project: "p", Session: "-"}); len(tools) != 0 {
		t.Errorf("a repository's run got %d tools", len(tools))
	}
}

var inAssistantChat = mcp.Caller{Project: "pip", Session: "s1"}

// In an assistant's chat, what's made to be played or read later goes in
// its library, with a card in the chat; screenshots and snippets don't.
func TestShareInAnAssistantChat(t *testing.T) {
	b := &fakeBackend{files: map[string]string{
		"/workspace/ep.mp3": "ID3\x04\x00ep", "/workspace/shot.png": png1x1, "/workspace/notes.md": "# Notes\n",
		"/workspace/cover.png": png1x1, "/workspace/ep.txt": "Hello there.", "/workspace/page.html": "<html><body>hi</body></html>",
	}}
	tools := Tools(b)(inAssistantChat)
	if len(tools) != 3 || tools[2].Name != "set_collection" || !strings.Contains(string(tools[0].Input), `"collection"`) {
		t.Fatalf("tools = %+v", tools)
	}
	if strings.Contains(string(Tools(b)(inChat)[0].Input), `"collection"`) {
		t.Error("a repository's chat is offered the library")
	}
	out, err := call(t, b, inAssistantChat, "show_file", `{"path":"ep.mp3","title":"Episode 1","cover":"cover.png","transcript":"ep.txt","chapters":[{"at":"1:10","title":"Two"},{"at":0,"title":"One"}]}`)
	if err != nil || !strings.Contains(out, "library (i1)") {
		t.Fatalf("%q %v", out, err)
	}
	s := b.shared[0].sh
	if b.shared[0].session != "s1" || s.Cover == nil || s.Cover.Ext != ".png" || string(s.Transcript) != "Hello there." || len(s.Item.Chapters) != 2 || s.Item.Chapters[0].At != 70 {
		t.Fatalf("share %+v", s)
	}
	if len(b.items) != 1 || b.items[0].Item != "i1" || b.items[0].Kind != assistant.KindAudio || b.items[0].Path != "ep.mp3" || len(b.shown) != 0 {
		t.Fatalf("card %+v, shown %+v", b.items, b.shown)
	}
	// A screenshot and a snippet stay in the chat, unless they're in a collection.
	call(t, b, inAssistantChat, "show_file", `{"path":"shot.png"}`)
	call(t, b, inAssistantChat, "show_file", `{"path":"notes.md"}`)
	if len(b.shown) != 2 || len(b.shared) != 1 {
		t.Errorf("shown %d, shared %d", len(b.shown), len(b.shared))
	}
	call(t, b, inAssistantChat, "show_file", `{"path":"notes.md","collection":"Notes"}`)
	if len(b.shared) != 2 || b.shared[1].sh.Item.Kind != assistant.KindText {
		t.Errorf("collected notes: %+v", b.shared)
	}
	// A page runs only when asked for; otherwise it's source.
	call(t, b, inAssistantChat, "show_file", `{"path":"page.html","view":true}`)
	if b.shared[2].sh.Item.Kind != assistant.KindView {
		t.Errorf("view: %+v", b.shared[2].sh.Item)
	}
	for _, bad := range []string{
		`{"path":"ep.mp3","start_line":2}`,
		`{"path":"notes.md","collection":"Notes","chapters":[{"at":1,"title":"x"}]}`,
		`{"path":"ep.mp3","cover":"ep.txt"}`,
		`{"path":"ep.mp3","chapters":[{"at":"1:99","title":"x"}]}`,
	} {
		if _, err := call(t, b, inAssistantChat, "show_file", bad); err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
}

func TestChapterAt(t *testing.T) {
	for in, want := range map[string]float64{`70`: 70, `"1:10"`: 70, `"1:02:03"`: 3723, `"0:05.5"`: 5.5, `12.5`: 12.5} {
		if got, err := chapterAt(json.RawMessage(in)); err != nil || got != want {
			t.Errorf("%s = %v, %v", in, got, err)
		}
	}
	for _, in := range []string{`"x"`, `"1:60"`, `"1:2:3:4"`, `true`, `"-1"`} {
		if _, err := chapterAt(json.RawMessage(in)); err == nil {
			t.Errorf("%s: no error", in)
		}
	}
}

func TestSetCollectionTool(t *testing.T) {
	b := &fakeBackend{files: map[string]string{"/workspace/cover.png": png1x1, "/workspace/x.txt": "x"}}
	out, err := call(t, b, inAssistantChat, "set_collection", `{"name":"Morning briefing","layout":"podcast","keep":14,"speed":1.25}`)
	if err != nil || !strings.Contains(out, "podcast layout") || !strings.Contains(out, "newest 14") {
		t.Fatalf("%q %v", out, err)
	}
	c := b.collections[0]
	if c.project != "pip" || c.title != "Morning briefing" || *c.p.Speed != 1.25 || c.p.About != nil || c.cover != nil {
		t.Errorf("collection %+v", c)
	}
	call(t, b, inAssistantChat, "set_collection", `{"name":"Morning briefing","cover":"cover.png"}`)
	if b.collections[1].cover == nil || b.collections[1].cover.Ext != ".png" {
		t.Errorf("cover %+v", b.collections[1])
	}
	if _, err := call(t, b, inAssistantChat, "set_collection", `{"name":"x","cover":"x.txt"}`); err == nil {
		t.Error("took text for a cover")
	}
}
