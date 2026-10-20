// Package tmux builds tmux command lines and parses their output. It never
// runs tmux itself: callers execute the argv inside a sandbox (docker exec),
// so nothing here goes through a shell.
//
// Targets use tmux's "=" prefix for exact matches, so session "s1" never
// resolves to "s10" by prefix.
package tmux

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// A leading dash is refused too, so a name can never read as a flag.
var validName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]{0,63}$`)

// ValidName reports whether name is safe as a session or window name.
func ValidName(name string) bool { return validName.MatchString(name) }

// NewSession starts a detached session running cmd (the default shell when
// cmd is empty) in dir.
func NewSession(name, dir string, env map[string]string, cmd []string) []string {
	args := []string{"tmux", "new-session", "-d", "-s", name}
	return withCommand(args, dir, env, cmd)
}

// NewWindow adds a detached window to session.
func NewWindow(session, window, dir string, env map[string]string, cmd []string) []string {
	args := []string{"tmux", "new-window", "-d", "-t", "=" + session + ":", "-n", window}
	return withCommand(args, dir, env, cmd)
}

func withCommand(args []string, dir string, env map[string]string, cmd []string) []string {
	if dir != "" {
		args = append(args, "-c", dir)
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		args = append(args, "-e", k+"="+env[k])
	}
	if len(cmd) > 0 {
		args = append(args, "--")
		args = append(args, cmd...)
	}
	return args
}

// EnsureServerOptions are applied once per server. window-size latest stops a
// phone and a desktop attached to one session fighting over its size, and
// remain-on-exit failed keeps a pane's last screen when its command fails, so
// the user can read why a harness quit. A harness that exits cleanly (you
// typed /exit) takes its session with it, and its viewers go back to where
// they came from, as they would from a harness run in their own terminal.
func EnsureServerOptions() [][]string {
	return [][]string{
		{"tmux", "set-option", "-g", "window-size", "latest"},
		{"tmux", "set-option", "-g", "history-limit", strconv.Itoa(HistoryLimit)},
		// The browser scrolls the history natively (History) and selects
		// text itself; tmux leaves the mouse alone.
		{"tmux", "set-option", "-g", "mouse", "off"},
		{"tmux", "set-option", "-g", "remain-on-exit", "failed"},
		// No status bar: the UI shows the session, and the agent's prompt
		// gets the bottom row.
		{"tmux", "set-option", "-g", "status", "off"},
	}
}

// Attach is a viewer. -u: the viewer is UTF-8 whatever the locale says, or
// tmux would show non-ASCII characters as "_".
func Attach(session string) []string {
	return []string{"tmux", "-u", "attach-session", "-t", "=" + session}
}
func Has(session string) []string  { return []string{"tmux", "has-session", "-t", "=" + session} }
func Kill(session string) []string { return []string{"tmux", "kill-session", "-t", "=" + session} }

func KillWindow(session, window string) []string {
	return []string{"tmux", "kill-window", "-t", "=" + session + ":=" + window}
}

// SendKeys sends keys (tmux key names such as C-c) to a target.
func SendKeys(target string, keys ...string) []string {
	return append([]string{"tmux", "send-keys", "-t", target}, keys...)
}

// HistoryLimit is the history-limit set on every server.
const HistoryLimit = 50000

// History prints a pane's history size, then up to n of the rows that have
// scrolled off its screen, one per row as the pane shows them, with colours.
func History(target string, n int) []string {
	return []string{"tmux", "display-message", "-p", "-t", target, "#{history_size}", ";",
		"capture-pane", "-p", "-e", "-t", target, "-S", "-" + strconv.Itoa(n), "-E", "-1"}
}

// Capture prints the last lines of a pane, joining wrapped lines.
func Capture(target string, lines int) []string {
	return []string{"tmux", "capture-pane", "-p", "-J", "-t", target, "-S", "-" + strconv.Itoa(lines)}
}

// Pane is one row of ListPanes. Exit is nil while the pane runs, and when
// its command was killed by a signal.
type Pane struct {
	Session string
	Dead    bool
	Exit    *int
}

// tmux writes control characters in -F output as '_', tabs included, so
// fields are split on ':', which tmux never allows in a session name. The
// name goes last regardless.
const panesFormat = "#{pane_dead}:#{pane_dead_status}:#{session_name}"

// ListPanes lists every pane of every session.
func ListPanes() []string { return []string{"tmux", "list-panes", "-a", "-F", panesFormat} }

// ParsePanes reads ListPanes output. Empty output means no sessions; "no
// server running" is an error the caller sees from tmux itself.
func ParsePanes(out string) ([]Pane, error) {
	var panes []Pane
	for line := range strings.Lines(out) {
		line = strings.TrimRight(line, "\r\n")
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.SplitN(line, ":", 3)
		if len(f) != 3 || (f[0] != "0" && f[0] != "1") || f[2] == "" {
			return nil, fmt.Errorf("tmux: bad list-panes line %q", line)
		}
		p := Pane{Session: f[2], Dead: f[0] == "1"}
		if f[1] != "" {
			v, err := strconv.Atoi(f[1])
			if err != nil || v < 0 {
				return nil, fmt.Errorf("tmux: list-panes line %q: bad exit status", line)
			}
			p.Exit = &v
		}
		panes = append(panes, p)
	}
	return panes, nil
}
