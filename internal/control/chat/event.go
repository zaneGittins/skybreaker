// Package chat runs harnesses in their structured modes (Claude Code's
// stream-json, Codex's app-server) and turns what they say into one small
// event model the UI draws as a conversation. Each harness has a Driver, a
// pure translator; the Manager runs the processes, keeps each session's
// event log and fans events out.
//
// The UI depends only on the events here. A harness that changes its
// protocol is absorbed in its driver: unknown messages are ignored, and an
// unknown tool still shows as a generic card.
package chat

import (
	"encoding/json"
	"time"
)

// Event kinds.
const (
	KindUser     = "user"     // a message the user sent: ID, Text
	KindText     = "text"     // a finished block of agent text: ID, Text
	KindDelta    = "delta"    // streamed agent text to append to ID; never stored
	KindThinking = "thinking" // the agent's reasoning summary: ID, Text
	KindTool     = "tool"     // a tool call, created or updated: ID, Tool
	KindAsk      = "ask"      // the agent waits for an answer: ID, Ask
	KindAnswered = "answered" // an ask was answered or withdrawn: ID, Text
	KindTurn     = "turn"     // a turn began or ended: Turn
	KindInfo     = "info"     // what the harness reports about itself: Info
	KindLogin    = "login"    // the harness needs signing in: Login
	KindNotice   = "notice"   // a note from Skybreaker, like a restart: Text
	KindShow     = "show"     // a file the agent showed the user: ID (its kept copy), Show
)

// Event is one entry in a session's log. Seq orders stored events; deltas
// have none.
type Event struct {
	Seq   int64     `json:"seq,omitempty"`
	Time  time.Time `json:"time"`
	Kind  string    `json:"kind"`
	ID    string    `json:"id,omitempty"`
	Text  string    `json:"text,omitempty"`
	Tool  *Tool     `json:"tool,omitempty"`
	Ask   *Ask      `json:"ask,omitempty"`
	Turn  *Turn     `json:"turn,omitempty"`
	Info  *Info     `json:"info,omitempty"`
	Login *Login    `json:"login,omitempty"`
	Show  *Show     `json:"show,omitempty"`
}

// Tool kinds, for the card's icon and its one-line summary.
const (
	ToolCommand = "command"
	ToolEdit    = "edit"
	ToolRead    = "read"
	ToolSearch  = "search"
	ToolWeb     = "web"
	ToolAgent   = "agent"
	ToolTodo    = "todo"
	ToolMCP     = "mcp"
	ToolOther   = "other"
)

// Tool statuses.
const (
	StatusRunning  = "running"
	StatusDone     = "done"
	StatusFailed   = "failed"
	StatusDeclined = "declined"
)

// Tool is one tool call, as much as the harness has told us so far. Every
// update carries the whole card.
type Tool struct {
	Name    string          `json:"name"`
	Kind    string          `json:"kind"`
	Title   string          `json:"title"`
	Status  string          `json:"status"`
	Input   json.RawMessage `json:"input,omitempty"`
	Output  string          `json:"output,omitempty"`
	Diff    string          `json:"diff,omitempty"`
	Added   int             `json:"added,omitempty"`
	Removed int             `json:"removed,omitempty"`
	Exit    *int            `json:"exit,omitempty"`
	Parent  string          `json:"parent,omitempty"` // the subagent call this one belongs to
}

// Ask kinds.
const (
	AskPermission = "permission"
	AskQuestion   = "question"
)

// Ask is what the agent is waiting on.
type Ask struct {
	Kind      string     `json:"kind"`
	Tool      string     `json:"tool,omitempty"`
	ToolID    string     `json:"tool_id,omitempty"`
	Title     string     `json:"title"`
	Detail    string     `json:"detail,omitempty"`
	Reason    string     `json:"reason,omitempty"`
	Always    bool       `json:"always,omitempty"` // the harness can remember an approval
	Questions []Question `json:"questions,omitempty"`
}

type Question struct {
	ID       string   `json:"id"`
	Header   string   `json:"header,omitempty"`
	Question string   `json:"question"`
	Options  []Option `json:"options,omitempty"`
	Multi    bool     `json:"multi,omitempty"`
}

type Option struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// Answer is the user's reply to an Ask.
type Answer struct {
	Allow   bool                `json:"allow"`
	Always  bool                `json:"always,omitempty"`
	Message string              `json:"message,omitempty"`
	Answers map[string][]string `json:"answers,omitempty"` // question ID → chosen labels or typed text
}

// Turn states.
const (
	TurnWorking     = "working"
	TurnDone        = "done"
	TurnFailed      = "failed"
	TurnInterrupted = "interrupted"
)

type Turn struct {
	State      string `json:"state"`
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

// Info is what the harness says about itself. Skybreaker never names a model;
// it shows the one the harness reports and offers the ones it lists.
type Info struct {
	Model    string   `json:"model,omitempty"`
	Mode     string   `json:"mode,omitempty"`
	Version  string   `json:"version,omitempty"`
	Commands []string `json:"commands,omitempty"`
	Models   []Model  `json:"models,omitempty"`
}

// Model is one the harness offers, as it describes it.
type Model struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	// Resolved is the model an alias stands for, when the harness says.
	Resolved string `json:"resolved,omitempty"`
	Default  bool   `json:"default,omitempty"`
}

// How a shown file is drawn.
const (
	ShowImage    = "image"
	ShowText     = "text"
	ShowMarkdown = "markdown"
)

// Show is a file from the sandbox the agent put in front of the user, read
// and checked by Skybreaker. The bytes are a kept copy, fetched by the event's ID.
type Show struct {
	Path  string `json:"path"` // where it was in the sandbox
	Kind  string `json:"kind"`
	Title string `json:"title,omitempty"` // the agent's caption
	Size  int64  `json:"size"`            // of the file in the sandbox
	// Start and End are the first and last line shown, for text.
	Start     int  `json:"start,omitempty"`
	End       int  `json:"end,omitempty"`
	Truncated bool `json:"truncated,omitempty"`
}

// Login is a harness asking to be signed in. With a URL and code it is a
// device sign-in in progress; Done ends it.
type Login struct {
	Needed bool   `json:"needed,omitempty"`
	URL    string `json:"url,omitempty"`
	Code   string `json:"code,omitempty"`
	Done   bool   `json:"done,omitempty"`
	Error  string `json:"error,omitempty"`
	// Terminal: this harness signs in only in its own terminal UI.
	Terminal bool `json:"terminal,omitempty"`
}

// Limits on what one event carries, so a chatty tool can't bloat the log.
const (
	maxOutput = 16 << 10
	maxDiff   = 128 << 10
	maxInput  = 32 << 10
	maxText   = 256 << 10
)

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Cut on a rune boundary.
	for n > 0 && n < len(s) && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n] + "\n… (cut)"
}

// clipTail keeps the end of s, where a command's result usually is.
func clipTail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := len(s) - n
	for i < len(s) && s[i]&0xC0 == 0x80 {
		i++
	}
	return "(cut) …\n" + s[i:]
}

func clipJSON(b json.RawMessage) json.RawMessage {
	if len(b) <= maxInput {
		return b
	}
	out, _ := json.Marshal(map[string]string{"truncated": clip(string(b), maxInput)})
	return out
}
