package chat

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Permission modes, from asking about everything to asking about nothing.
// Each driver maps them onto its harness's own and lists those it has.
const (
	ModeAsk   = "ask"   // ask before edits and commands
	ModeEdits = "edits" // edit freely, ask before other commands
	ModeAuto  = "auto"  // never ask: the sandbox and broker are the boundary
	ModePlan  = "plan"  // plan only, change nothing
)

// Options start or resume a harness conversation.
type Options struct {
	Cwd    string
	Mode   string
	Model  string // a model the user chose from the harness's list
	Resume string // the harness's conversation to continue
	NewID  string // an ID for a new conversation, for harnesses that take one
	Fork   bool   // with Resume: continue a copy of that conversation, under a new ID
	MCP    string // Skybreaker's MCP URL for this conversation (the board and its other tools)
}

// MCPConfig is a Claude --mcp-config naming Skybreaker's MCP server at url.
func MCPConfig(url string) string {
	b, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"skybreaker": map[string]string{"type": "http", "url": url}}})
	return string(b)
}

// Out is what a driver makes of one line from its harness.
type Out struct {
	Events []Event
	Write  [][]byte // lines for the harness's stdin
	// Resume is the harness's conversation ID once it reports one.
	Resume string
	// Usage is the tokens used since the last report; Limits, when not
	// nil, the account's usage windows as they stand now.
	Usage  *Usage
	Limits []Limit
}

// A Driver translates between Skybreaker and one harness's structured protocol.
// It does no I/O and isn't safe for concurrent use; the Manager serialises
// every call for a session.
type Driver interface {
	// Argv is the command to run in the sandbox, Env its extra environment.
	Argv(o Options) []string
	Env() map[string]string
	// Open is what to write once the process starts.
	Open(o Options) [][]byte
	Send(text string) ([][]byte, error)
	Answer(askID string, a Answer) ([][]byte, error)
	Interrupt() [][]byte
	SetMode(mode string) ([][]byte, error)
	// SetModel switches to one of the models the harness listed.
	SetModel(model string) ([][]byte, error)
	// Compact summarises the conversation so far to free its context.
	Compact() ([][]byte, error)
	// Login starts a sign-in, or reports that this harness signs in only
	// in its terminal UI.
	Login() ([][]byte, error)
	Read(line []byte) Out
	// Modes lists the permission modes this harness has, safest first.
	Modes() []string
}

// ErrTerminalLogin: the harness can only sign in in its terminal UI.
var ErrTerminalLogin = errors.New("this harness signs in from its terminal")

// errNoAsk: the ask was answered already, or never existed.
var errNoAsk = errors.New("nothing is waiting for that answer")

// New returns the driver called name, or nil.
func New(name string) Driver {
	switch name {
	case "claude":
		return newClaude()
	case "codex":
		return newCodex()
	}
	return nil
}

func validMode(d Driver, mode string) error {
	for _, m := range d.Modes() {
		if m == mode {
			return nil
		}
	}
	return fmt.Errorf("mode %q is not one of %s", mode, strings.Join(d.Modes(), ", "))
}

// validModel accepts what a harness could call a model: no spaces, controls
// or flag-like leading dash. The harness itself decides whether it exists.
func validModel(m string) error {
	if m == "" || len(m) > 128 || m[0] == '-' {
		return fmt.Errorf("model %q is not a model name", m)
	}
	for _, r := range m {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._:/@[]-", r)
		if !ok {
			return fmt.Errorf("model %q is not a model name", m)
		}
	}
	return nil
}

// stepEnd is what becomes of a subagent's step still open when the
// subagent ends with status: failed if it failed, else stopped.
func stepEnd(status string) string {
	if status == StatusFailed {
		return StatusFailed
	}
	return StatusDeclined
}

// Rel shows a path in the workspace, or in a session's worktree, relative
// to it.
func Rel(p string) string {
	if r, ok := strings.CutPrefix(p, "/workspace/"); ok && r != "" {
		return r
	}
	if r, ok := strings.CutPrefix(p, worktrees); ok {
		if _, r, ok := strings.Cut(r, "/"); ok && r != "" {
			return r
		}
	}
	return p
}

// worktrees is sandbox.WorktreesDir, where sessions' own worktrees are.
const worktrees = "/skybreaker/worktrees/"

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " …"
	}
	return s
}

// countDiff counts a unified diff's added and removed lines.
func countDiff(diff string) (added, removed int) {
	for line := range strings.SplitSeq(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			removed++
		}
	}
	return
}

// fileHeader starts one file's part of a unified diff.
func fileHeader(path string) string {
	return "--- a/" + path + "\n+++ b/" + path + "\n"
}

// wholeFile is a diff that adds (or with sign '-', removes) every line.
func wholeFile(path, content string, sign byte) string {
	content = strings.TrimSuffix(content, "\n")
	lines := strings.Split(content, "\n")
	if content == "" {
		lines = nil
	}
	var b strings.Builder
	b.WriteString(fileHeader(path))
	if sign == '+' {
		fmt.Fprintf(&b, "@@ -0,0 +1,%d @@\n", len(lines))
	} else {
		fmt.Fprintf(&b, "@@ -1,%d +0,0 @@\n", len(lines))
	}
	for _, l := range lines {
		b.WriteByte(sign)
		b.WriteString(l)
		b.WriteByte('\n')
	}
	return b.String()
}

// replaceDiff shows an edit not yet applied: old lines out, new lines in.
func replaceDiff(path, old, new string) string {
	var b strings.Builder
	b.WriteString(fileHeader(path))
	o := strings.Split(strings.TrimSuffix(old, "\n"), "\n")
	n := strings.Split(strings.TrimSuffix(new, "\n"), "\n")
	fmt.Fprintf(&b, "@@ -1,%d +1,%d @@\n", len(o), len(n))
	for _, l := range o {
		b.WriteString("-" + l + "\n")
	}
	for _, l := range n {
		b.WriteString("+" + l + "\n")
	}
	return b.String()
}
