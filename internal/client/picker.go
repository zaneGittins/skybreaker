package client

import (
	"cmp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Entry is one line of the picker: a running session, or a new one.
type Entry struct {
	Session          *Session
	Project, Harness string // for a new session
}

func rank(s Session) int {
	switch s.State {
	case "waiting", "login":
		return 0
	case "working":
		return 1
	}
	return 2
}

func recent(s Session) time.Time {
	if !s.Updated.IsZero() {
		return s.Updated
	}
	return s.Created
}

// Entries lists running sessions, what needs you first, then the newest;
// then a new session for each project and harness.
func Entries(in Inbox) []Entry {
	var live []Session
	for _, s := range in.Sessions {
		if s.Status == "running" && s.Parent == "" {
			live = append(live, s)
		}
	}
	slices.SortStableFunc(live, func(a, b Session) int {
		return cmp.Or(cmp.Compare(rank(a), rank(b)), recent(b).Compare(recent(a)), cmp.Compare(a.ID, b.ID))
	})
	var out []Entry
	for i := range live {
		out = append(out, Entry{Session: &live[i]})
	}
	for _, p := range in.Projects {
		for _, h := range p.Harnesses {
			out = append(out, Entry{Project: p.Name, Harness: h})
		}
	}
	return out
}

const (
	reset  = "\x1b[0m"
	bold   = "\x1b[1m"
	dim    = "\x1b[2m"
	yellow = "\x1b[33m"
	cyan   = "\x1b[36m"
	red    = "\x1b[31m"
)

// fit cuts s to n runes, with an ellipsis, and pads it to n.
func fit(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) > n {
		r := []rune(s)
		s = string(r[:n-1]) + "…"
	}
	return s + strings.Repeat(" ", n-utf8.RuneCountInString(s))
}

// Render draws entries as lines of at most width columns, sel highlighted.
// Everything a sandbox or person wrote passes through Visible.
func Render(es []Entry, sel, width int) []string {
	const hw, sw = 8, 9
	pw := 4
	for _, e := range es {
		p := e.Project
		if e.Session != nil {
			p = e.Session.Project
		}
		pw = max(pw, utf8.RuneCountInString(Visible(p)))
	}
	pw = min(pw, 16)
	tw := max(width-(2+pw+2+hw+2+2+sw), 1)
	lines := make([]string, len(es))
	for i, e := range es {
		mark := "  "
		if i == sel {
			mark = yellow + "▸ " + reset
		}
		var line string
		if s := e.Session; s != nil {
			title := Visible(s.Title)
			if title == "" {
				title = "(untitled)"
			}
			state, colour := s.State, dim
			switch {
			case !s.Chat():
				state = "terminal"
			case state == "waiting" || state == "login":
				colour = yellow
			case state == "working":
				colour = cyan
			case state == "failed":
				colour = red
			}
			line = fit(Visible(s.Project), pw) + "  " + fit(Visible(s.Harness), hw) + "  " + fit(title, tw) + "  " + colour + fit(state, sw) + reset
		} else {
			line = dim + fit(Visible(e.Project), pw) + "  " + fit(Visible(e.Harness), hw) + "  " + fit("+ new session", tw) + "  " + fit("", sw) + reset
		}
		if i == sel {
			line = bold + line
		}
		lines[i] = mark + line + reset
	}
	return lines
}

type Key int

const (
	KeyNone Key = iota
	KeyUp
	KeyDown
	KeyEnter
	KeyQuit
	KeyRefresh
)

// ParseKey reads one keypress, as a raw terminal delivers it.
func ParseKey(b []byte) Key {
	switch string(b) {
	case "\x1b[A", "\x1bOA", "k":
		return KeyUp
	case "\x1b[B", "\x1bOB", "j":
		return KeyDown
	case "\r", "\n":
		return KeyEnter
	case "q", "\x03", "\x1b":
		return KeyQuit
	case "r":
		return KeyRefresh
	}
	return KeyNone
}
