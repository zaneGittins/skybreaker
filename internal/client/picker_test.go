package client

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func ts(s string) time.Time { t, _ := time.Parse(time.RFC3339, s); return t }

func TestEntries(t *testing.T) {
	in := Inbox{
		Projects: []Project{{Name: "bifract", Harnesses: []string{"claude", "codex"}}, {Name: "orbit", Harnesses: []string{"codex"}}},
		Sessions: []Session{
			{ID: "idle-old", Project: "bifract", Harness: "claude", Status: "running", Mode: "chat", State: "idle", Updated: ts("2026-10-19T08:00:00Z")},
			{ID: "term-new", Project: "orbit", Harness: "shell", Status: "running", Created: ts("2026-10-19T11:00:00Z")},
			{ID: "waiting", Project: "bifract", Harness: "codex", Status: "running", Mode: "chat", State: "waiting", Updated: ts("2026-10-19T07:00:00Z")},
			{ID: "working", Project: "bifract", Harness: "claude", Status: "running", Mode: "chat", State: "working", Updated: ts("2026-10-19T09:00:00Z")},
			{ID: "ended", Project: "orbit", Harness: "claude", Status: "exited"},
		},
	}
	var got []string
	for _, e := range Entries(in) {
		if e.Session != nil {
			got = append(got, e.Session.ID)
		} else {
			got = append(got, "new "+e.Project+"/"+e.Harness)
		}
	}
	// What needs you, then what's working, then the newest; ended sessions
	// aren't listed; then a new session for every project and harness.
	want := "waiting working term-new idle-old new bifract/claude new bifract/codex new orbit/codex"
	if strings.Join(got, " ") != want {
		t.Fatalf("entries:\n got %s\nwant %s", strings.Join(got, " "), want)
	}
}

func TestRenderRevealsAndFits(t *testing.T) {
	es := []Entry{
		{Session: &Session{ID: "s1", Project: "bifract", Harness: "claude", Title: "evil\u202egnp.exe \x1b]52;c;aGk=\x07 and a very long title that goes on and on", Mode: "chat", State: "waiting"}},
		{Project: "bifract", Harness: "codex"},
	}
	lines := Render(es, 0, 60)
	if len(lines) != 2 {
		t.Fatalf("lines %q", lines)
	}
	for _, l := range lines {
		plain := stripCSI(l)
		if strings.ContainsAny(plain, "\x1b\x07\u202e") {
			t.Fatalf("hidden characters reached the terminal: %q", l)
		}
		if n := utf8.RuneCountInString(plain); n > 60 {
			t.Fatalf("line of %d runes: %q", n, plain)
		}
		if !utf8.ValidString(l) {
			t.Fatalf("cut mid-rune: %q", l)
		}
	}
	if !strings.Contains(lines[0], "<U+202E>") || !strings.Contains(lines[0], "waiting") {
		t.Fatalf("first line %q", stripCSI(lines[0]))
	}
	if !strings.Contains(stripCSI(lines[1]), "new") {
		t.Fatalf("new line %q", stripCSI(lines[1]))
	}
}

// stripCSI removes the picker's own colour codes.
func stripCSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func TestKeys(t *testing.T) {
	for in, want := range map[string]Key{
		"\x1b[A": KeyUp, "\x1bOA": KeyUp, "k": KeyUp,
		"\x1b[B": KeyDown, "\x1bOB": KeyDown, "j": KeyDown,
		"\r": KeyEnter, "\n": KeyEnter,
		"q": KeyQuit, "\x03": KeyQuit, "\x1b": KeyQuit,
		"r": KeyRefresh, "x": KeyNone,
	} {
		if got := ParseKey([]byte(in)); got != want {
			t.Errorf("ParseKey(%q) = %v, want %v", in, got, want)
		}
	}
}

// Your window title says which session you're in, and the session's names
// can't end the title early or add sequences of their own.
func TestWindowTitle(t *testing.T) {
	got := windowTitle(label(Entry{Session: &Session{Project: "bifract", Harness: "claude"}}))
	if got != "\x1b[22;0t\x1b]0;skybreaker · bifract/claude\x07" {
		t.Errorf("title = %q", got)
	}
	got = windowTitle(label(Entry{Project: "a\x07\x1b]0;evil\x1b\\", Harness: "x\x9c"}))
	if strings.Count(got, "\x07") != 1 || strings.Count(got, "\x1b") != 2 || strings.Contains(got, "\x9c") {
		t.Errorf("hostile title = %q", got)
	}
}
