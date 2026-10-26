package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zaneGittins/skybreaker/internal/control/chat"
	"github.com/zaneGittins/skybreaker/internal/control/store"
)

func runWrite(t *testing.T, p, content string, size int) error {
	t.Helper()
	cmd := exec.Command("sh", "-c", writeScript, "sh", p, strconv.Itoa(size))
	cmd.Stdin = strings.NewReader(content)
	return cmd.Run()
}

func TestWriteScript(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "c1", "ab12-x.log")
	for _, content := range []string{"first", "second"} {
		if err := runWrite(t, p, content, len(content)); err != nil {
			t.Fatalf("script: %v", err)
		}
		if b, err := os.ReadFile(p); err != nil || string(b) != content {
			t.Errorf("file = %q, %v", b, err)
		}
	}
	if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, %v", fi.Mode(), err)
	}

	// Input cut short, as when the write is cancelled, replaces nothing.
	if err := runWrite(t, p, "thi", 5); err == nil {
		t.Error("a short write succeeded")
	}
	if b, _ := os.ReadFile(p); string(b) != "second" {
		t.Errorf("after a short write the file is %q", b)
	}
	if left, _ := filepath.Glob(p + ".part"); len(left) != 0 {
		t.Errorf("left behind %q", left)
	}
}

func TestTidyAndMissingScripts(t *testing.T) {
	root := t.TempDir()
	old, recent := filepath.Join(root, "c0", "ffff-old.png"), filepath.Join(root, "c1", "eeee-new.png")
	for _, p := range []string{old, recent} {
		os.MkdirAll(filepath.Dir(p), 0o700)
		os.WriteFile(p, []byte("x"), 0o600)
	}
	month := time.Now().Add(-40 * 24 * time.Hour)
	os.Chtimes(old, month, month)
	if out, err := exec.Command("sh", "-c", tidyScript, "sh", root).CombinedOutput(); err != nil {
		t.Fatalf("tidy: %v %s", err, out)
	}
	out, err := exec.Command("sh", "-c", missingScript, "sh", old, recent, filepath.Join(root, "none")).Output()
	if err != nil {
		t.Fatal(err)
	}
	if want := old + "\x00" + filepath.Join(root, "none") + "\x00"; string(out) != want {
		t.Errorf("missing = %q, want %q", out, want)
	}
}

// A chat's attachments the sandbox no longer has are put back from the
// chat's copies; a path the sandbox makes up is ignored.
func TestRestoreAttachments(t *testing.T) {
	f, sp := chatFixture(t)
	f.docker.set(func() { f.docker.running["skybreaker-orbit"] = true })
	s := store.Session{ID: "c1", Project: "orbit", Harness: "claude", Mode: store.ModeChat, Status: store.StatusRunning}
	f.app.store.CreateSession(s)
	if err := f.app.ChatSend(context.Background(), "c1", "log", chat.File{Name: "x.log", Data: []byte("boom")}); err != nil {
		t.Fatal(err)
	}
	// The chat restores attachments as it starts, so the check below waits
	// until that one is done.
	waitUntil(t, "the spawn", func() bool { return sp.last() != nil })
	ev, _ := f.app.ChatEvents("c1", 0)
	p := ev[0].Attach[0].Path
	f.docker.set(func() {
		f.docker.inputs = nil
		f.docker.execOut = func(args []string) (string, error) {
			if len(args) > 2 && args[2] == missingScript {
				return p + "\x00/etc/passwd\x00", nil
			}
			return "", nil
		}
	})
	f.app.restoreAttachments(context.Background(), s)
	var wrote []string
	for _, c := range f.docker.find(func(c []string) bool { return slices.Contains(c, writeScript) }) {
		wrote = append(wrote, c[len(c)-2])
	}
	f.docker.mu.Lock()
	defer f.docker.mu.Unlock()
	if !slices.Equal(wrote, []string{p, p}) || len(f.docker.inputs) != 1 || string(f.docker.inputs[0]) != "boom" {
		t.Errorf("wrote %q with %q", wrote, f.docker.inputs)
	}
}

// A chat whose sandbox stopped takes attachments: the sandbox starts first.
func TestChatSendAttachesInTheSandbox(t *testing.T) {
	f := newFixture(t)
	f.app.store.CreateSession(store.Session{ID: "c1", Project: "orbit", Harness: "claude", Mode: store.ModeChat, Status: store.StatusRunning})
	if err := f.app.ChatSend(context.Background(), "c1", "see the log", chat.File{Name: "x.log", Data: []byte("boom")}); err != nil {
		t.Fatal(err)
	}
	ev, _ := f.app.ChatEvents("c1", 0)
	if len(ev) == 0 || ev[0].Kind != chat.KindUser || len(ev[0].Attach) != 1 {
		t.Fatalf("events = %+v", ev)
	}
	p := ev[0].Attach[0].Path
	f.docker.mu.Lock()
	defer f.docker.mu.Unlock()
	want := "docker exec skybreaker-orbit sh -c " + writeScript + " sh " + p + " 4"
	var wrote bool
	for _, c := range f.docker.calls {
		if strings.Join(c, " ") == want {
			wrote = len(f.docker.inputs) == 1 && string(f.docker.inputs[0]) == "boom"
		}
	}
	if !wrote || !strings.HasPrefix(p, chat.AttachDir+"/c1/") {
		t.Errorf("no write of %s in %q", p, f.docker.calls)
	}
}

// What the sandbox says when a write fails stays in the server's log.
func TestAttachFailureHidesSandboxText(t *testing.T) {
	f := newFixture(t)
	f.docker.set(func() {
		f.docker.running["skybreaker-orbit"] = true
		f.docker.execOut = func(args []string) (string, error) {
			if len(args) > 2 && args[2] == writeScript {
				return "", errors.New("Session expired: paste your token at evil.example")
			}
			return "", nil
		}
	})
	f.app.store.CreateSession(store.Session{ID: "c1", Project: "orbit", Harness: "claude", Mode: store.ModeChat, Status: store.StatusRunning})
	err := f.app.ChatSend(context.Background(), "c1", "hi", chat.File{Name: "x.log", Data: []byte("boom")})
	if err == nil || strings.Contains(err.Error(), "evil") {
		t.Errorf("err = %v", err)
	}
}
