package chat

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLogAppendSinceAndReopen(t *testing.T) {
	dir := t.TempDir()
	l, err := OpenLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i, text := range []string{"one", "two", "three"} {
		e, err := l.Append("s1", Event{Kind: KindText, Text: text})
		if err != nil || e.Seq != int64(i+1) {
			t.Fatalf("append %q = %+v, %v", text, e, err)
		}
	}
	got, _ := l.Since("s1", 1)
	if len(got) != 2 || got[0].Text != "two" || got[1].Seq != 3 {
		t.Errorf("since 1 = %+v", got)
	}
	if got, _ := l.Since("s1", 3); len(got) != 0 {
		t.Errorf("since the end = %+v", got)
	}
	if got, _ := l.Since("other", 0); len(got) != 0 {
		t.Errorf("an unknown session has events: %+v", got)
	}

	// A crash can leave half a line; reopening skips it and carries on.
	f, _ := os.OpenFile(filepath.Join(dir, "s1.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"seq":4,"kind":"te`)
	f.Close()
	l2, _ := OpenLog(dir)
	got, _ = l2.Since("s1", 0)
	if len(got) != 3 || got[2].Text != "three" {
		t.Fatalf("reopened = %+v", got)
	}
	if e, _ := l2.Append("s1", Event{Kind: KindText, Text: "four"}); e.Seq != 4 {
		t.Errorf("seq after reopen = %d", e.Seq)
	}
	if info, _ := os.Stat(filepath.Join(dir, "s1.jsonl")); info.Mode().Perm() != 0o600 {
		t.Errorf("log mode %v", info.Mode())
	}

	if err := l2.Delete("s1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := l2.Since("s1", 0); len(got) != 0 {
		t.Errorf("deleted log still has %+v", got)
	}
}

func TestLogRejectsBadIDs(t *testing.T) {
	l, _ := OpenLog(t.TempDir())
	for _, id := range []string{"", "../x", "a/b", ".hidden.", "x\x00"} {
		if _, err := l.Append(id, Event{Kind: KindText}); err == nil {
			t.Errorf("append to %q succeeded", id)
		}
	}
}

func TestLogInMemory(t *testing.T) {
	l, _ := OpenLog("")
	l.Append("s", Event{Kind: KindUser, Text: "hi"})
	if got, _ := l.Since("s", 0); len(got) != 1 {
		t.Errorf("in-memory log = %+v", got)
	}
}
