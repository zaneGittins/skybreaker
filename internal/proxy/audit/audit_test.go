package audit

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLogWritesOneJSONLinePerEntry(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf)
	if err := l.Log(Entry{Sender: "agent-vm", Project: "orbit", Host: "github.com", Method: "GET", Path: "/x", Decision: "auto", Status: 200}); err != nil {
		t.Fatal(err)
	}
	if err := l.Log(Entry{Sender: "agent-vm", Decision: "deny", Reason: "no rule matched"}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines: %q", len(lines), buf.String())
	}
	var e Entry
	if err := json.Unmarshal([]byte(lines[0]), &e); err != nil {
		t.Fatal(err)
	}
	if e.Project != "orbit" || e.Status != 200 || e.Time.IsZero() {
		t.Errorf("round trip lost fields: %+v", e)
	}
	if e.Time.Location() != time.UTC {
		t.Errorf("time not UTC: %v", e.Time.Location())
	}
	if strings.Contains(lines[1], "project") || strings.Contains(lines[1], "status") {
		t.Errorf("empty fields should be omitted: %s", lines[1])
	}
}

func TestLogKeepsGivenTime(t *testing.T) {
	var buf bytes.Buffer
	when := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	New(&buf).Log(Entry{Time: when, Decision: "auto"})
	var e Entry
	json.Unmarshal(buf.Bytes(), &e)
	if !e.Time.Equal(when) {
		t.Errorf("time = %v, want %v", e.Time, when)
	}
}

func TestEntryWithNewlinesStaysOneLine(t *testing.T) {
	var buf bytes.Buffer
	New(&buf).Log(Entry{Decision: "deny", Path: "/a\n{\"decision\":\"auto\"}", Reason: "x\r\ny"})
	if n := strings.Count(buf.String(), "\n"); n != 1 {
		t.Fatalf("a hostile path split the record into %d lines: %q", n, buf.String())
	}
}

func TestConcurrentLogsDoNotInterleave(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.Log(Entry{Decision: "auto", Path: strings.Repeat("p", 2000)})
		}()
	}
	wg.Wait()
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 50 {
		t.Fatalf("got %d lines", len(lines))
	}
	for _, line := range lines {
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("interleaved line: %v", err)
		}
	}
}

func TestOpenAppendsWithPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "audit.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	l.Log(Entry{Decision: "auto", Path: "/1"})
	l.Close()

	l, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	l.Log(Entry{Decision: "auto", Path: "/2"})
	l.Close()

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v, want 0600", fi.Mode().Perm())
	}
	di, _ := os.Stat(filepath.Dir(path))
	if di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v, want 0700", di.Mode().Perm())
	}
	got, err := Tail(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Path != "/1" || got[1].Path != "/2" {
		t.Errorf("reopen should append, got %+v", got)
	}
}

func TestTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/a", "/b", "/c", "/d"} {
		l.Log(Entry{Decision: "auto", Path: p})
	}
	l.Close()
	// A torn or corrupt line must not hide the rest.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("not json\n")
	f.Close()

	got, err := Tail(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Path != "/c" || got[1].Path != "/d" {
		t.Errorf("Tail(2) = %+v", got)
	}
	all, _ := Tail(path, 100)
	if len(all) != 4 {
		t.Errorf("Tail(100) returned %d entries, want 4", len(all))
	}
}

func TestTailMissingFile(t *testing.T) {
	got, err := Tail(filepath.Join(t.TempDir(), "nope.jsonl"), 5)
	if err != nil || len(got) != 0 {
		t.Errorf("Tail on missing file = %v, %v; want empty, nil", got, err)
	}
}

func TestTailAcrossChunkBoundaries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	l, _ := Open(path)
	for i := 0; i < 300; i++ {
		l.Log(Entry{Decision: "auto", Path: "/" + strings.Repeat("x", 700) + string(rune('a'+i%26)), Status: i})
	}
	l.Close()
	got, err := Tail(path, 250)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 250 {
		t.Fatalf("got %d entries", len(got))
	}
	for i, e := range got {
		if e.Status != 50+i {
			t.Fatalf("entry %d has status %d, want %d", i, e.Status, 50+i)
		}
	}
	all, _ := Tail(path, 1000)
	if len(all) != 300 || all[0].Status != 0 {
		t.Fatalf("Tail(1000) = %d entries, first status %d", len(all), all[0].Status)
	}
}

// Back walks the log newest first, across chunks, until told to stop.
func TestBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	l, _ := Open(path)
	for i := 0; i < 300; i++ {
		l.Log(Entry{Decision: "allow", Path: "/" + strings.Repeat("x", 700), Status: i})
	}
	l.Close()
	var seen []int
	if err := Back(path, func(e Entry) bool { seen = append(seen, e.Status); return e.Status > 120 }); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 180 || seen[0] != 299 || seen[len(seen)-1] != 120 {
		t.Fatalf("saw %d, first %d, last %d", len(seen), seen[0], seen[len(seen)-1])
	}
	n := 0
	Back(path, func(Entry) bool { n++; return true })
	if n != 300 {
		t.Fatalf("whole log: %d", n)
	}
	if err := Back(filepath.Join(t.TempDir(), "none"), func(Entry) bool { t.Fatal("called"); return false }); err != nil {
		t.Fatal(err)
	}
}
