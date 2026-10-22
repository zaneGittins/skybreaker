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
}

const (
	maxImage    = 10 << 20  // the largest image shown
	maxTextRead = 4 << 20   // how much of a text file is read
	maxText     = 256 << 10 // how much of it is shown
	maxTitle    = 200
	maxPath     = 4096
)

// images are the types shown as images, by their sniffed type. SVG isn't
// one: it can carry script, so it's shown as source.
// Tools is the tool set for a caller: show_file and show_image inside a
// running chat, nothing elsewhere (scheduled runs, terminals, the UI mount).
func Tools(b Backend) func(mcp.Caller) []mcp.Tool {
	return func(c mcp.Caller) []mcp.Tool {
		if c.Project == "" || c.Session == "" || c.Session == "-" || !b.ChatIn(c.Project, c.Session) {
			return nil
		}
		return []mcp.Tool{
			{
				Name: "show_file",
				Description: "Show a text file from your workspace to the user in the Skybreaker chat, as code with line numbers " +
					"(a whole Markdown file is shown rendered). Use it when the user should look at a file or part of one. " +
					"Relative paths are under /workspace; files under /tmp work too. You don't get the content back.",
				Input: json.RawMessage(`{"type":"object","properties":{` +
					`"path":{"type":"string","description":"The file, relative to /workspace or absolute"},` +
					`"start_line":{"type":"integer","minimum":1,"description":"First line to show (default 1)"},` +
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
					"You don't get the image back.",
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
	ext, ok := chat.ImageExt[http.DetectContentType(data)]
	if !ok {
		return "", fmt.Errorf("%s isn't a PNG, JPEG, GIF or WebP image; use show_file for text, including SVG source", chat.Rel(resolved))
	}
	sh := chat.Show{Path: chat.Rel(resolved), Kind: chat.ShowImage, Title: title(in.Caption), Size: size}
	if err := b.ChatShow(c.Session, sh, data, ext); err != nil {
		return "", err
	}
	return "Shown to the user: " + sh.Path, nil
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
	if _, ok := chat.ImageExt[http.DetectContentType(data)]; ok {
		return "", fmt.Errorf("%s is an image; use show_image", name)
	}
	cut := size > int64(len(data))
	if cut {
		// Drop the line the read ended in the middle of.
		if i := bytes.LastIndexByte(data, '\n'); i >= 0 {
			data = data[:i+1]
		}
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "", fmt.Errorf("%s is a binary file; show_file shows text", name)
	}
	if !utf8.Valid(data) {
		return "", fmt.Errorf("%s isn't UTF-8 text", name)
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
