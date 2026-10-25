package show

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/zaneGittins/skybreaker/internal/assistant"
	"github.com/zaneGittins/skybreaker/internal/control/chat"
	"github.com/zaneGittins/skybreaker/internal/control/mcp"
)

// An assistant's shares go in its library, where the user plays or reads
// them and Skybreaker keeps their place. These are the extra arguments
// show_file takes there, and set_collection.

const libraryProps = `"summary":{"type":"string","description":"One line on what's in it, e.g. Rain from 4, two meetings"},` +
	`"collection":{"type":"string","description":"The series it belongs to, e.g. Morning briefing. Shown together in the library; see set_collection"},` +
	`"cover":{"type":"string","description":"An image file for its cover art"},` +
	`"chapters":{"type":"array","description":"For audio: where each part starts","items":{"type":"object","properties":{` +
	`"at":{"type":"string","description":"Where it starts: seconds, m:ss or h:mm:ss, e.g. 1:10"},"title":{"type":"string"}},"required":["at","title"],"additionalProperties":false}},` +
	`"transcript":{"type":"string","description":"For audio: a text file of what's said"},` +
	`"view":{"type":"boolean","description":"An HTML file is an interactive page: see the tool description"}`

const libraryHelp = " Share a recording whole, never split into parts: the player keeps the user's place. In the library: audio gets a player that remembers where the user stopped (give chapters and a transcript if you " +
	"have them); a PDF, Markdown or text file opens in a reader that remembers their page; an HTML file with view set runs as an " +
	"interactive page, sandboxed with no network, so inline everything. Such a page is sent postMessage({sky:'init', theme, accent, " +
	"progress}) and may post {sky:'progress', value: 0 to 1}, {sky:'done'} and {sky:'height', value: pixels} to the parent. " +
	"Group a series with collection, and use set_collection to choose its layout and how long it's kept."

type libraryArgs struct {
	Summary    string            `json:"summary"`
	Collection string            `json:"collection"`
	Cover      string            `json:"cover"`
	Chapters   []json.RawMessage `json:"chapters"`
	Transcript string            `json:"transcript"`
	View       bool              `json:"view"`
}

// chapterAt reads a chapter's start: seconds, or m:ss or h:mm:ss.
func chapterAt(raw json.RawMessage) (float64, error) {
	var n float64
	if err := json.Unmarshal(raw, &n); err == nil {
		return n, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return 0, fmt.Errorf("chapter start %s isn't a time", raw)
	}
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) > 3 {
		return 0, fmt.Errorf("chapter start %q isn't a time", s)
	}
	total := 0.0
	for i, p := range parts {
		v, err := strconv.ParseFloat(p, 64)
		if err != nil || v < 0 || (i > 0 && v >= 60) {
			return 0, fmt.Errorf("chapter start %q isn't a time: use seconds or m:ss", s)
		}
		total = total*60 + v
	}
	return total, nil
}

func (in libraryArgs) chapters() ([]assistant.Chapter, error) {
	var out []assistant.Chapter
	for _, raw := range in.Chapters {
		var c struct {
			At    json.RawMessage `json:"at"`
			Title string          `json:"title"`
		}
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, errors.New("each chapter is {at, title}")
		}
		at, err := chapterAt(c.At)
		if err != nil {
			return nil, err
		}
		out = append(out, assistant.Chapter{At: at, Title: c.Title})
	}
	return out, nil
}

// libraryKind is what a file is in the library, from its bytes; view is
// whether HTML is a page to run rather than text.
func libraryKind(data []byte, resolved string, view bool) (kind, ext string) {
	ctype := http.DetectContentType(data)
	if e, ok := chat.ImageType(ctype); ok && int64(len(data)) <= maxImage {
		return assistant.KindImage, e
	}
	if e, ok := chat.AudioType(data, resolved); ok {
		return assistant.KindAudio, e
	}
	if ctype == "application/pdf" {
		return assistant.KindPDF, ".pdf"
	}
	if text := strings.HasPrefix(ctype, "text/") && utf8.Valid(data) && bytes.IndexByte(data, 0) < 0; text {
		switch {
		case strings.HasPrefix(ctype, "text/html") && view:
			return assistant.KindView, ".html"
		case markdownName(resolved):
			return assistant.KindText, ".md"
		case !strings.HasPrefix(ctype, "text/html"):
			return assistant.KindText, ".txt"
		}
	}
	return assistant.KindFile, ".bin"
}

func markdownName(p string) bool {
	ext := strings.ToLower(path.Ext(p))
	return ext == ".md" || ext == ".markdown"
}

// readWhole reads a file to share whole, up to the limit.
func readWhole(ctx context.Context, b Backend, c mcp.Caller, p string, max int64, what string) (string, []byte, error) {
	if err := checkPath(p); err != nil {
		return "", nil, fmt.Errorf("%s: %w", cmp.Or(what, "file"), err)
	}
	resolved, size, data, err := b.ReadSandboxFile(ctx, c.Project, c.Session, p, max+1)
	if err != nil {
		return "", nil, err
	}
	if size > max || int64(len(data)) > max {
		return "", nil, fmt.Errorf("%s %s is %.1f MB; up to %d MB", what, chat.Rel(resolved), float64(size)/(1<<20), max>>20)
	}
	if int64(len(data)) != size {
		return "", nil, fmt.Errorf("%s changed as it was read", chat.Rel(resolved))
	}
	return resolved, data, nil
}

// librarySave reads a file whole, with its cover and transcript, and files
// it in the assistant's library: from a chat, or from a routine (session
// "") onto Today as well.
func librarySave(ctx context.Context, b Backend, c mcp.Caller, p, title string, in libraryArgs, session string) (assistant.Item, string, error) {
	resolved, data, err := readWhole(ctx, b, c, p, maxShare, "")
	if err != nil {
		return assistant.Item{}, "", err
	}
	kind, ext := libraryKind(data, resolved, in.View || session == "")
	chapters, err := in.chapters()
	if err != nil {
		return assistant.Item{}, "", err
	}
	if (len(chapters) > 0 || in.Transcript != "") && kind != assistant.KindAudio {
		return assistant.Item{}, "", fmt.Errorf("%s isn't audio: chapters and transcript are for audio", chat.Rel(resolved))
	}
	sh := assistant.Share{Item: assistant.Item{Kind: kind, Name: resolved, Title: title, Summary: in.Summary, Collection: in.Collection, Chapters: chapters}, Data: data, Ext: ext}
	if in.Cover != "" {
		cr, cover, err := readWhole(ctx, b, c, in.Cover, maxImage, "cover")
		if err != nil {
			return assistant.Item{}, "", err
		}
		e, ok := chat.ImageType(http.DetectContentType(cover))
		if !ok {
			return assistant.Item{}, "", fmt.Errorf("cover %s isn't a PNG, JPEG, GIF or WebP image", chat.Rel(cr))
		}
		sh.Cover = &assistant.Blob{Data: cover, Ext: e}
	}
	if in.Transcript != "" {
		if _, sh.Transcript, err = readWhole(ctx, b, c, in.Transcript, assistant.MaxTranscript, "transcript"); err != nil {
			return assistant.Item{}, "", err
		}
	}
	it, err := b.AssistantShare(c.Project, session, sh)
	return it, chat.Rel(resolved), err
}

// inLibrary is whether a file shown in an assistant's chat goes in its
// library: what's made to be played or read later, or anything in a
// collection. Screenshots and snippets stay in the chat.
func inLibrary(data []byte, resolved string, in libraryArgs) bool {
	if in.Collection != "" || in.View {
		return true
	}
	ctype := http.DetectContentType(data)
	_, audio := chat.AudioType(data, resolved)
	return audio || ctype == "application/pdf"
}

func chatLibraryShare(ctx context.Context, b Backend, c mcp.Caller, p, title string, in libraryArgs) (string, error) {
	it, name, err := librarySave(ctx, b, c, p, title, in, c.Session)
	if err != nil {
		return "", err
	}
	if err := b.ChatShowItem(c.Session, chat.Show{Path: name, Kind: it.Kind, Title: it.Title, Size: it.Size, Item: it.ID}); err != nil {
		return "", err
	}
	return fmt.Sprintf("Shared with the user and kept in your library (%s): %s", it.ID, name), nil
}

func setCollection(b Backend) mcp.Tool {
	return mcp.Tool{
		Name: "set_collection",
		Description: "Set how a collection in your library (a series you share with show_file's collection) looks and is kept. " +
			"Layouts: list; podcast (episodes, newest first, with a play-new button); issues (cards, for reports and newsletters); " +
			"gallery (a grid, for images). Only what you give changes. Keep it to what the user asked for.",
		Input: json.RawMessage(`{"type":"object","properties":{` +
			`"name":{"type":"string","description":"The collection, as you name it in show_file, e.g. Morning briefing"},` +
			`"layout":{"type":"string","enum":["list","podcast","issues","gallery"]},` +
			`"about":{"type":"string","description":"One line under its name, e.g. Every weekday at 7"},` +
			`"keep":{"type":"integer","minimum":0,"description":"How many of the newest to keep; 0 for no limit"},` +
			`"keep_days":{"type":"integer","minimum":0,"description":"How many days to keep each (30 unless set); 0 for no limit"},` +
			`"speed":{"type":"number","minimum":0.5,"maximum":3,"description":"Audio's starting playback speed"},` +
			`"notify":{"type":"boolean","description":"Notify the user when something new arrives"},` +
			`"cover":{"type":"string","description":"An image file for the collection's cover"}},` +
			`"required":["name"],"additionalProperties":false}`),
		Call: func(ctx context.Context, c mcp.Caller, raw json.RawMessage) (string, error) {
			var in struct {
				Name     string   `json:"name"`
				Layout   *string  `json:"layout"`
				About    *string  `json:"about"`
				Keep     *int     `json:"keep"`
				KeepDays *int     `json:"keep_days"`
				Speed    *float64 `json:"speed"`
				Notify   *bool    `json:"notify"`
				Cover    string   `json:"cover"`
			}
			if err := mcp.Args(raw, &in); err != nil {
				return "", err
			}
			var cover *assistant.Blob
			if in.Cover != "" {
				cr, data, err := readWhole(ctx, b, c, in.Cover, maxImage, "cover")
				if err != nil {
					return "", err
				}
				e, ok := chat.ImageType(http.DetectContentType(data))
				if !ok {
					return "", fmt.Errorf("cover %s isn't a PNG, JPEG, GIF or WebP image", chat.Rel(cr))
				}
				cover = &assistant.Blob{Data: data, Ext: e}
			}
			col, err := b.AssistantCollection(c.Project, in.Name, assistant.CollectionPatch{Layout: in.Layout, About: in.About, Keep: in.Keep, KeepDays: in.KeepDays, Speed: in.Speed, Notify: in.Notify}, cover)
			if err != nil {
				return "", err
			}
			keep := "no limit"
			switch {
			case col.Keep > 0 && col.KeepDays > 0:
				keep = fmt.Sprintf("the newest %d, for %d days", col.Keep, col.KeepDays)
			case col.Keep > 0:
				keep = fmt.Sprintf("the newest %d", col.Keep)
			case col.KeepDays > 0:
				keep = fmt.Sprintf("%d days", col.KeepDays)
			}
			return fmt.Sprintf("Collection %q: %s layout, keeps %s.", col.Title, col.Layout, keep), nil
		},
	}
}
