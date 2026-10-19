package tmux

import (
	"reflect"
	"testing"
)

func eq(t *testing.T, name string, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s:\n got  %q\n want %q", name, got, want)
	}
}

func TestValidName(t *testing.T) {
	good := []string{"a", "s1", "skybreaker-runs", "A_b-9", "0123456789012345678901234567890123456789012345678901234567890123"}
	for _, n := range good {
		if !ValidName(n) {
			t.Errorf("ValidName(%q) = false", n)
		}
	}
	bad := []string{"", "a;b", "a b", "a=b", "..", "a.b", "a:b", "=a", "-", "-a", "a\n", "a\x00", "sé", "ａ", "a/b", "$(x)", "`x`",
		"01234567890123456789012345678901234567890123456789012345678901234"}
	for _, n := range bad {
		if ValidName(n) {
			t.Errorf("ValidName(%q) = true", n)
		}
	}
}

func TestNewSession(t *testing.T) {
	eq(t, "with cmd", NewSession("s1", "/workspace", map[string]string{"SKYBREAKER_SESSION": "s1", "A": "1"}, []string{"claude", "--rc"}),
		[]string{"tmux", "new-session", "-d", "-s", "s1", "-c", "/workspace", "-e", "A=1", "-e", "SKYBREAKER_SESSION=s1", "--", "claude", "--rc"})
	eq(t, "no cmd", NewSession("s1", "/w", nil, nil),
		[]string{"tmux", "new-session", "-d", "-s", "s1", "-c", "/w"})
	eq(t, "no dir", NewSession("s1", "", nil, []string{"bash"}),
		[]string{"tmux", "new-session", "-d", "-s", "s1", "--", "bash"})
}

func TestNewWindow(t *testing.T) {
	eq(t, "window", NewWindow("skybreaker-runs", "r42", "/skybreaker/runs/r42", map[string]string{"Z": "z", "B": "b"}, []string{"bash", "-c", "x"}),
		[]string{"tmux", "new-window", "-d", "-t", "=skybreaker-runs:", "-n", "r42", "-c", "/skybreaker/runs/r42", "-e", "B=b", "-e", "Z=z", "--", "bash", "-c", "x"})
}

func TestSimpleCommands(t *testing.T) {
	// -u: the viewer is a UTF-8 terminal, whatever the exec's locale says;
	// otherwise tmux shows every non-ASCII character as "_".
	eq(t, "attach", Attach("s1"), []string{"tmux", "-u", "attach-session", "-t", "=s1"})
	eq(t, "has", Has("s1"), []string{"tmux", "has-session", "-t", "=s1"})
	eq(t, "kill", Kill("s1"), []string{"tmux", "kill-session", "-t", "=s1"})
	eq(t, "kill window", KillWindow("skybreaker-runs", "r1"), []string{"tmux", "kill-window", "-t", "=skybreaker-runs:=r1"})
	eq(t, "send keys", SendKeys("=s1", "C-c"), []string{"tmux", "send-keys", "-t", "=s1", "C-c"})
	// History: the history size, then up to n rows that have scrolled off
	// the screen, one per row as the pane shows them, with colours.
	eq(t, "history", History("=s1:", 3000), []string{"tmux", "display-message", "-p", "-t", "=s1:", "#{history_size}", ";",
		"capture-pane", "-p", "-e", "-t", "=s1:", "-S", "-3000", "-E", "-1"})
	eq(t, "capture", Capture("=s1", 40), []string{"tmux", "capture-pane", "-p", "-J", "-t", "=s1", "-S", "-40"})
	eq(t, "panes", ListPanes(), []string{"tmux", "list-panes", "-a", "-F", "#{pane_dead}:#{pane_dead_status}:#{session_name}"})
}

func TestEnsureServerOptions(t *testing.T) {
	got := EnsureServerOptions()
	want := [][]string{
		{"tmux", "set-option", "-g", "window-size", "latest"},
		{"tmux", "set-option", "-g", "history-limit", "50000"},
		{"tmux", "set-option", "-g", "mouse", "off"},
		{"tmux", "set-option", "-g", "remain-on-exit", "on"},
		{"tmux", "set-option", "-g", "status", "off"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q", got)
	}
}

func TestEnvSortedAndNotAliased(t *testing.T) {
	env := map[string]string{"C": "3", "A": "1", "B": "2"}
	for range 20 { // map iteration order is random; output must not be
		eq(t, "sorted", NewSession("s", "", env, nil),
			[]string{"tmux", "new-session", "-d", "-s", "s", "-e", "A=1", "-e", "B=2", "-e", "C=3"})
	}
	cmd := []string{"x"}
	out := NewSession("s", "", nil, cmd)
	out[len(out)-1] = "mutated"
	if cmd[0] != "x" {
		t.Error("NewSession aliases cmd")
	}
}

func TestParsePanes(t *testing.T) {
	out := "0::s1\n1:3:s1\n1::s2\r\n0::skybreaker-runs\n0::a:b\n"
	got, err := ParsePanes(out)
	if err != nil {
		t.Fatal(err)
	}
	three := 3
	want := []Pane{
		{Session: "s1"},
		{Session: "s1", Dead: true, Exit: &three},
		{Session: "s2", Dead: true}, // killed by a signal: no exit status
		{Session: "skybreaker-runs"},
		{Session: "a:b"}, // the name is the rest of the line
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	for _, empty := range []string{"", "\n", "  \n"} {
		if got, err := ParsePanes(empty); err != nil || len(got) != 0 {
			t.Errorf("ParsePanes(%q) = %v, %v", empty, got, err)
		}
	}
	for _, bad := range []string{"0:s1", "x::s1", "1:x:s1", "1:-1:s1", "0::", "s1\t0\t", "0_0_s1"} {
		if _, err := ParsePanes(bad); err == nil {
			t.Errorf("ParsePanes(%q) succeeded", bad)
		}
	}
}
