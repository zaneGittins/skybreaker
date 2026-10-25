// Package show is the skybreaker MCP server's show_file and show_image
// tools: an agent in a chat puts a file from its sandbox in front of the
// user. Skybreaker reads the file inside the sandbox, checks what it is from
// its bytes, keeps a copy and adds a card to the chat. The model gets a
// line of text back, never the file.
package show

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/zaneGittins/skybreaker/internal/control/chat"
	"github.com/zaneGittins/skybreaker/internal/control/mcp"
)

// Backend is what the tools need from the app.
type Backend interface {
	// ChatIn reports whether session is a running chat of project's.
	ChatIn(project, session string) bool
	// ReadSandboxFile reads at most max bytes of a regular file inside the
	// project's sandbox, resolving p there against the session's working
	// directory. It returns the resolved path and the file's full size.
	ReadSandboxFile(ctx context.Context, project, session, p string, max int64) (resolved string, size int64, data []byte, err error)
	// ChatShow keeps a copy of data and adds a show card to the chat.
	ChatShow(session string, sh chat.Show, data []byte, ext string) error
	// IsAssistant reports whether project is an assistant.
	IsAssistant(project string) bool
	// AssistantShare keeps a copy of data and posts it to the assistant's
	// Today, for a routine, which has no chat to show it in.
	AssistantShare(project string, sh chat.Show, data []byte, ext string) error
}

const (
	maxImage    = 10 << 20  // the largest image shown
	maxTextRead = 4 << 20   // how much of a text file is read
	maxText     = 256 << 10 // how much of it is shown
	maxTitle    = 200
	maxPath     = 4096
)

// maxShare is the largest file shared whole: audio, or a download.
var maxShare int64 = 100 << 20

// Tools is the tool set for a caller: show_file and show_image inside a
// running chat; show_file, to its Today, in an assistant's routine; nothing
// elsewhere (a repository's runs, terminals, the UI mount).
func Tools(b Backend) func(mcp.Caller) []mcp.Tool {
	return func(c mcp.Caller) []mcp.Tool {
		if c.Project != "" && c.Session == "-" && b.IsAssistant(c.Project) {
			return []mcp.Tool{routineShare(b)}
		}
		if c.Project == "" || c.Session == "" || c.Session == "-" || !b.ChatIn(c.Project, c.Session) {
			return nil
		}
		return []mcp.Tool{
			{
				Name: "show_file",
				Description: "Show or share a file from your workspace with the user in the Skybreaker chat. Text shows as code with " +
					"line numbers (a whole Markdown file rendered); audio (MP3, WAV, Ogg, M4A) gets a player; an image is shown; " +
					"anything else, a PDF or a zip, is offered as a download. Up to 100 MB. Use it for a file the user should look at, " +
					"hear or have, such as a podcast you made or downloaded. Relative paths are under /workspace; files under /tmp " +
					"work too. You don't get the content back.",
				Input: json.RawMessage(`{"type":"object","properties":{` +
					`"path":{"type":"string","description":"The file, relative to /workspace or absolute"},` +
					`"start_line":{"type":"integer","minimum":1,"description":"For text: first line to show (default 1)"},` +
					`"end_line":{"type":"integer","minimum":1,"description":"Last line to show (default the end)"},` +
					`"title":{"type":"string","description":"A short caption for the user"}},` +
					`"required":["path"],"additionalProperties":false}`),
				ReadOnly: true,
				Call: func(ctx context.Context, c mcp.Caller, args json.RawMessage) (string, error) {
					return showFile(ctx, b, c, args)
				},
			},
			{
				Name: "show_image",
				Description: "Show an image file (PNG, JPEG, GIF or WebP) from your workspace to the user in the Skybreaker chat, " +
					"such as a screenshot or a chart you made. Relative paths are under /workspace; files under /tmp work too. " +
					"You don't get the image back. When you finish a change the user can see, show them it working: screenshot it " +
					"(playwright screenshot --viewport-size=390,844 http://localhost:<port> /tmp/shot.png; add --full-page for a long page) " +
					"and show that before you say you're done.",
				Input: json.RawMessage(`{"type":"object","properties":{` +
					`"path":{"type":"string","description":"The image, relative to /workspace or absolute"},` +
					`"caption":{"type":"string","description":"A short caption for the user"}},` +
					`"required":["path"],"additionalProperties":false}`),
				ReadOnly: true,
				Call: func(ctx context.Context, c mcp.Caller, args json.RawMessage) (string, error) {
					return showImage(ctx, b, c, args)
				},
			},
		}
	}
}

func checkPath(p string) error {
	if p == "" {
		return errors.New("path is required")
	}
	if len(p) > maxPath || strings.ContainsRune(p, 0) {
		return errors.New("that isn't a usable path")
	}
	return nil
}

// title bounds the agent's caption; the UI shows it as text.
func title(s string) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > maxTitle {
		s = string([]rune(s)[:maxTitle]) + "…"
	}
	return s
}

func showImage(ctx context.Context, b Backend, c mcp.Caller, raw json.RawMessage) (string, error) {
	var in struct {
		Path    string `json:"path"`
		Caption string `json:"caption"`
	}
	if err := mcp.Args(raw, &in); err != nil {
		return "", err
	}
	if err := checkPath(in.Path); err != nil {
		return "", err
	}
	resolved, size, data, err := b.ReadSandboxFile(ctx, c.Project, c.Session, in.Path, maxImage+1)
	if err != nil {
		return "", err
	}
	if size > maxImage || len(data) > maxImage {
		return "", fmt.Errorf("%s is %.1f MB; images up to %d MB can be shown", chat.Rel(resolved), float64(size)/(1<<20), maxImage>>20)
	}
	ext, ok := chat.ImageType(http.DetectContentType(data))
	if !ok {
		return "", fmt.Errorf("%s isn't a PNG, JPEG, GIF or WebP image; use show_file for text, including SVG source", chat.Rel(resolved))
	}
	sh := chat.Show{Path: chat.Rel(resolved), Kind: chat.ShowImage, Title: title(in.Caption), Size: size}
	if err := b.ChatShow(c.Session, sh, data, ext); err != nil {
		return "", err
	}
	return "Shown to the user: " + sh.Path, nil
}

// routineShare is show_file for an assistant's routine: whatever the file,
// it goes on the assistant's Today whole, to play, see or open.
func routineShare(b Backend) mcp.Tool {
	return mcp.Tool{
		Name: "show_file",
		Description: "Share a file from your workspace with the user: it goes on your Today page, where they'll see it. Audio " +
			"(MP3, WAV, Ogg, M4A) gets a player; an image is shown; anything else, a report or a PDF, is offered to open. Up to " +
			"100 MB. Use it for what this routine made for them, such as a podcast. Relative paths are under /workspace.",
		Input: json.RawMessage(`{"type":"object","properties":{` +
			`"path":{"type":"string","description":"The file, relative to /workspace or absolute"},` +
			`"title":{"type":"string","description":"One line for the user, e.g. This week's podcast"}},` +
			`"required":["path"],"additionalProperties":false}`),
		ReadOnly: true,
		Call: func(ctx context.Context, c mcp.Caller, raw json.RawMessage) (string, error) {
			var in struct {
				Path  string `json:"path"`
				Title string `json:"title"`
			}
			if err := mcp.Args(raw, &in); err != nil {
				return "", err
			}
			if err := checkPath(in.Path); err != nil {
				return "", err
			}
			resolved, size, data, err := b.ReadSandboxFile(ctx, c.Project, c.Session, in.Path, maxTextRead)
			if err != nil {
				return "", err
			}
			if _, err := share(ctx, b, c, in.Title, resolved, size, data, false, func(sh chat.Show, data []byte, ext string) error {
				return b.AssistantShare(c.Project, sh, data, ext)
			}); err != nil {
				return "", err
			}
			return "Shared with the user on your Today page: " + chat.Rel(resolved), nil
		},
	}
}

// share puts a file that isn't text in front of the user whole: an image to
// see, audio to play, anything else to download. put is where it goes: the
// chat, or the assistant's Today.
func share(ctx context.Context, b Backend, c mcp.Caller, caption, resolved string, size int64, data []byte, ranged bool, put func(chat.Show, []byte, string) error) (string, error) {
	name := chat.Rel(resolved)
	if ranged {
		return "", fmt.Errorf("%s isn't text: start_line and end_line are for text", name)
	}
	if size > maxShare {
		return "", fmt.Errorf("%s is %.1f MB; files up to %d MB can be shared", name, float64(size)/(1<<20), maxShare>>20)
	}
	if size > int64(len(data)) {
		var err error
		if resolved, size, data, err = b.ReadSandboxFile(ctx, c.Project, c.Session, resolved, maxShare+1); err != nil {
			return "", err
		}
		if size > maxShare || int64(len(data)) != size {
			return "", fmt.Errorf("%s changed as it was read", name)
		}
	}
	sh := chat.Show{Path: name, Kind: chat.ShowDownload, Title: title(caption), Size: size}
	ext := ".bin"
	if e, ok := chat.ImageType(http.DetectContentType(data)); ok && size <= maxImage {
		sh.Kind, ext = chat.ShowImage, e
	} else if e, ok := chat.AudioType(data, resolved); ok {
		sh.Kind, ext = chat.ShowAudio, e
	}
	if err := put(sh, data, ext); err != nil {
		return "", err
	}
	return "Shared with the user: " + name, nil
}

// toChat puts a shared file in the caller's chat.
func toChat(b Backend, c mcp.Caller) func(chat.Show, []byte, string) error {
	return func(sh chat.Show, data []byte, ext string) error { return b.ChatShow(c.Session, sh, data, ext) }
}

func showFile(ctx context.Context, b Backend, c mcp.Caller, raw json.RawMessage) (string, error) {
	var in struct {
		Path  string `json:"path"`
		Start int    `json:"start_line"`
		End   int    `json:"end_line"`
		Title string `json:"title"`
	}
	if err := mcp.Args(raw, &in); err != nil {
		return "", err
	}
	if err := checkPath(in.Path); err != nil {
		return "", err
	}
	if in.Start < 0 || in.End < 0 || (in.End > 0 && in.End < max(in.Start, 1)) {
		return "", errors.New("start_line and end_line must be line numbers from 1, with end_line not before start_line")
	}
	ranged := in.Start > 0 || in.End > 0
	resolved, size, data, err := b.ReadSandboxFile(ctx, c.Project, c.Session, in.Path, maxTextRead)
	if err != nil {
		return "", err
	}
	name := chat.Rel(resolved)
	// Text is what reads as text: a zip or a PDF whose start happens to be
	// valid UTF-8 is still shared whole.
	ctype := http.DetectContentType(data)
	_, image := chat.ImageType(ctype)
	_, audio := chat.AudioType(data, resolved)
	if image || audio || !strings.HasPrefix(ctype, "text/") || bytes.IndexByte(data, 0) >= 0 {
		return share(ctx, b, c, in.Title, resolved, size, data, ranged, toChat(b, c))
	}
	cut := size > int64(len(data))
	if cut {
		// Drop the line the read ended in the middle of.
		if i := bytes.LastIndexByte(data, '\n'); i >= 0 {
			data = data[:i+1]
		}
	}
	if !utf8.Valid(data) {
		return share(ctx, b, c, in.Title, resolved, size, data, ranged, toChat(b, c))
	}
	lines := strings.SplitAfter(string(data), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	start, end := max(in.Start, 1), in.End
	if end == 0 || end > len(lines) {
		end = len(lines)
	}
	if start > len(lines) {
		if cut {
			return "", fmt.Errorf("%s is over %d MB; only lines in its first %d MB can be shown", name, maxTextRead>>20, maxTextRead>>20)
		}
		return "", fmt.Errorf("%s has %d lines; start_line %d is past the end", name, len(lines), start)
	}
	var out strings.Builder
	truncated := cut && (in.End == 0 || in.End > len(lines))
	shownEnd := start - 1
	for _, l := range lines[start-1 : end] {
		if out.Len()+len(l) > maxText {
			truncated = true
			if out.Len() == 0 {
				// One line longer than the limit: show its start.
				l = l[:maxText]
				for !utf8.ValidString(l) {
					l = l[:len(l)-1]
				}
				out.WriteString(l)
				shownEnd = start
			}
			break
		}
		out.WriteString(l)
		shownEnd++
	}
	kind := chat.ShowText
	if ext := strings.ToLower(path.Ext(resolved)); !ranged && (ext == ".md" || ext == ".markdown") {
		kind = chat.ShowMarkdown
	}
	sh := chat.Show{Path: name, Kind: kind, Title: title(in.Title), Size: size, Start: start, End: shownEnd, Truncated: truncated}
	if err := b.ChatShow(c.Session, sh, []byte(out.String()), ".txt"); err != nil {
		return "", err
	}
	msg := fmt.Sprintf("Shown to the user: %s, lines %d–%d", name, start, shownEnd)
	if truncated {
		msg += fmt.Sprintf(" (cut at %d KB; show a range for the rest)", maxText>>10)
	}
	return msg, nil
}
