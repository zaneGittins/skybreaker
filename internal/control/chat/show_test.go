package chat

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zaneGittins/skybreaker/internal/control/store"
)

func TestLogKeepsShownFiles(t *testing.T) {
	for _, dir := range []string{"", t.TempDir()} {
		l, _ := OpenLog(dir)
		name, err := l.Keep("s1", []byte("hello"), ".txt")
		if err != nil {
			t.Fatal(err)
		}
		if !shownName.MatchString(name) || !strings.HasSuffix(name, ".txt") {
			t.Errorf("name = %q", name)
		}
		got, err := l.Kept("s1", name)
		if err != nil || string(got) != "hello" {
			t.Errorf("kept = %q, %v", got, err)
		}
		// Another session's file, a bad name, or a traversal: not found.
		for _, c := range [][2]string{{"s2", name}, {"s1", "../s1.jsonl"}, {"s1", "x.txt"}, {"../s1", name}, {"s1", name + "/"}} {
			if _, err := l.Kept(c[0], c[1]); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("Kept(%q, %q) = %v", c[0], c[1], err)
			}
		}
		if _, err := l.Keep("s1", []byte("x"), ".exe"); err == nil {
			t.Error("an unknown extension was kept")
		}
		l.Delete("s1")
		if _, err := l.Kept("s1", name); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("after delete: %v", err)
		}
		if dir != "" {
			if _, err := os.Stat(filepath.Join(dir, "s1.shown")); !os.IsNotExist(err) {
				t.Errorf("shown dir left behind: %v", err)
			}
		}
	}
}

func TestLogShownQuota(t *testing.T) {
	l, _ := OpenLog(t.TempDir())
	big := bytes.Repeat([]byte("x"), maxShownBytes/2+1)
	if _, err := l.Keep("s1", big, ".txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Keep("s1", append(big, 'y'), ".txt"); err == nil {
		t.Error("a session went past its shown-file quota")
	}
	// The same content again costs nothing: it's stored once.
	if _, err := l.Keep("s1", big, ".txt"); err != nil {
		t.Errorf("the same file again: %v", err)
	}
	if _, err := l.Keep("s2", big, ".txt"); err != nil {
		t.Errorf("the quota is per session: %v", err)
	}
	// After a restart the quota still counts what's on disk.
	l2, _ := OpenLog(l.dir)
	if _, err := l2.Keep("s1", append(big, 'z'), ".txt"); err == nil {
		t.Error("a reopened log forgot the quota")
	}
}

func TestManagerShow(t *testing.T) {
	h := newHarness(t)
	s := h.session("claude")
	if err := h.m.Show(s.ID, Show{Path: "shot.png", Kind: ShowImage, Size: 4}, []byte("\x89PNG"), ".png"); err != nil {
		t.Fatal(err)
	}
	ev := find(h.events(s.ID), KindShow)
	if len(ev) != 1 || ev[0].Show == nil || ev[0].Show.Path != "shot.png" || !shownName.MatchString(ev[0].ID) {
		t.Fatalf("show events = %+v", ev)
	}
	if got, err := h.m.Shown(s.ID, ev[0].ID); err != nil || string(got) != "\x89PNG" {
		t.Errorf("shown = %q, %v", got, err)
	}
	if err := h.m.Show("nope", Show{Kind: ShowText}, []byte("x"), ".txt"); err == nil {
		t.Error("showed in a session that doesn't exist")
	}
	h.st.UpdateSession(s.ID, func(x *store.Session) { x.Status = store.StatusExited })
	if err := h.m.Show(s.ID, Show{Kind: ShowText}, []byte("x"), ".txt"); err == nil {
		t.Error("showed in an ended session")
	}
	term := store.Session{ID: "sterm", Project: "p", Harness: "claude", Status: store.StatusRunning}
	h.st.CreateSession(term)
	if err := h.m.Show(term.ID, Show{Kind: ShowText}, []byte("x"), ".txt"); !errors.Is(err, errNotChat) {
		t.Errorf("terminal session show = %v", err)
	}
	h.m.Forget(s.ID)
	if _, err := h.m.Shown(s.ID, ev[0].ID); err == nil {
		t.Error("a forgotten session's file is still served")
	}
}
