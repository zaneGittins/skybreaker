package app

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/zaneGittins/skybreaker/internal/control/chat"
	"github.com/zaneGittins/skybreaker/internal/control/store"
)

// This file is what package show's tools need: they read a file inside a
// sandbox and put it in a chat. Workspaces are never read on the host, so
// the path is resolved, checked and read in the sandbox by readScript.

// readScript resolves $1 against $3 inside the sandbox and, if it is a
// regular file under $3 or $4, prints "ok", its path and its size, each
// ending in NUL, then at most $2 bytes of it. Anything else prints "err"
// and why. The roots are arguments: the app passes the session's working
// directory and /tmp. Paths go in as arguments, never into the script.
const readScript = `cd "$3" || exit 1
p=$(realpath -e -- "$1" 2>/dev/null) || { printf 'err\0no such file: %s' "$1"; exit 0; }
case "$p" in "$3"|"$3"/*|"$4"|"$4"/*) ;; *) printf 'err\0%s is outside %s and %s' "$p" "$3" "$4"; exit 0;; esac
[ -f "$p" ] || { printf 'err\0%s is not a regular file' "$p"; exit 0; }
printf 'ok\0%s\0%s\0' "$p" "$(wc -c < "$p")"
exec head -c "$2" -- "$p"`

// parseRead reads readScript's output.
func parseRead(out []byte) (string, int64, []byte, error) {
	f := bytes.SplitN(out, []byte{0}, 4)
	switch {
	case len(f) == 2 && string(f[0]) == "err":
		return "", 0, nil, errors.New(string(f[1]))
	case len(f) != 4 || string(f[0]) != "ok" || !strings.HasPrefix(string(f[1]), "/"):
		return "", 0, nil, errors.New("couldn't read the file in the sandbox")
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(f[2])), 10, 64)
	if err != nil || size < 0 {
		return "", 0, nil, errors.New("couldn't read the file in the sandbox")
	}
	return string(f[1]), size, f[3], nil
}

// ReadSandboxFile reads at most max bytes of a regular file under the
// session's working directory (its worktree, or /workspace) or /tmp in the
// project's sandbox; p is resolved there, against the working directory.
func (a *App) ReadSandboxFile(ctx context.Context, project, session, p string, max int64) (string, int64, []byte, error) {
	if _, err := a.project(project); err != nil {
		return "", 0, nil, err
	}
	dir := workspaceDir
	if s, ok := a.store.Session(session); ok && s.Project == project {
		dir = workdir(s)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := a.sandbox.Exec(ctx, project, "sh", "-c", readScript, "sh", p, strconv.FormatInt(max, 10), dir, "/tmp")
	if err != nil {
		return "", 0, nil, errors.New("couldn't read the file in the sandbox")
	}
	return parseRead(out)
}

// ChatIn reports whether session is a running chat of project's.
func (a *App) ChatIn(project, session string) bool {
	s, ok := a.store.Session(session)
	return ok && s.Project == project && s.Mode == store.ModeChat && s.Status == store.StatusRunning
}

// ChatShow puts a file in front of the user in a chat.
func (a *App) ChatShow(session string, sh chat.Show, data []byte, ext string) error {
	return a.chat.Show(session, sh, data, ext)
}

// ChatShowItem puts an assistant's library item in front of the user in a chat.
func (a *App) ChatShowItem(session string, sh chat.Show) error {
	return a.chat.ShowItem(session, sh)
}

// ChatPreview puts a preview link in front of the user in a chat.
func (a *App) ChatPreview(session string, p chat.Preview) error {
	return a.chat.Preview(session, p)
}

// ChatShown returns the kept copy of a file shown in a chat.
func (a *App) ChatShown(id, name string) ([]byte, error) {
	if _, err := a.chatSession(id); err != nil {
		return nil, err
	}
	return a.chat.Shown(id, name)
}
