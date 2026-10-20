package tmux

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
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
		{"tmux", "set-option", "-g", "remain-on-exit", "failed"},
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

// A harness that exits cleanly ends its session, so its viewer goes back
// where it came from instead of sitting on a dead pane; one that fails
// keeps its screen, and its viewer, to read why. Runs the options in a
// real tmux.
func TestExitsLetViewersGo(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("no tmux")
	}
	dir, err := os.MkdirTemp("", "tm") // short: socket paths are limited
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "s")
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("tmux", append([]string{"-S", sock, "-f", "/dev/null"}, args[1:]...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("%q: %v %s", args, err, out)
		}
		return string(out)
	}
	defer exec.Command("tmux", "-S", sock, "kill-server").Run()
	run("tmux", "new-session", "-d", "-s", "keep", "sleep 60")
	for _, o := range EnsureServerOptions() {
		run(o...)
	}
	run("tmux", "new-session", "-d", "-s", "clean", "sleep 2")
	run("tmux", "new-session", "-d", "-s", "failed", "sleep 2; echo why; exit 3")
	for _, s := range []string{"clean", "failed", "keep"} {
		// A control-mode viewer; its stdin stays open until the test ends.
		c := exec.Command("tmux", "-S", sock, "-C", "attach", "-t", "="+s)
		in, _ := c.StdinPipe()
		defer in.Close()
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		go c.Wait()
	}
	want := "[failed] [keep]"
	var got string
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		out, _ := exec.Command("tmux", "-S", sock, "list-clients", "-F", "[#{session_name}]").Output()
		f := strings.Fields(string(out))
		slices.Sort(f)
		if got = strings.Join(f, " "); got == want && !strings.Contains(run("tmux", "ls"), "clean") {
			if p := run("tmux", "list-panes", "-t", "=failed", "-F", "#{pane_dead}:#{pane_dead_status}"); p != "1:3\n" {
				t.Errorf("failed pane = %q", p)
			}
			if !strings.Contains(run("tmux", "capture-pane", "-p", "-S", "-", "-t", "=failed:"), "why") {
				t.Error("the failed harness's screen wasn't kept")
			}
			return
		}
	}
	t.Errorf("viewers = %s, want %s (and the clean session gone)", got, want)
}
