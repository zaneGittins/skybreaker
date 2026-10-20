package usage

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zaneGittins/skybreaker/internal/control/chat"
)

func open(t *testing.T, path string, now *time.Time) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return *now }
	return s
}

// Tokens add up per harness and UTC hour, and the report reaches back the
// days asked for; limits are the last a harness reported.
func TestStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	now := time.Date(2026, 10, 19, 14, 20, 0, 0, time.UTC)
	s := open(t, path, &now)
	s.Add("claude", chat.Usage{Input: 10, Cached: 100, Output: 5})
	s.Add("claude", chat.Usage{Input: 1, Cached: 2, Output: 3})
	s.Add("codex", chat.Usage{Input: 7})
	now = now.Add(time.Hour)
	s.Add("claude", chat.Usage{Output: 1})
	s.SetLimits("claude", []chat.Limit{{Name: "five_hour", Minutes: 300, Used: 12}})
	s.SetLimits("claude", []chat.Limit{{Name: "five_hour", Minutes: 300, Used: 20}})

	r := s.Report(15)
	if len(r.Hours) != 3 {
		t.Fatalf("hours %+v", r.Hours)
	}
	h := r.Hours[0]
	if h.Harness != "claude" || !h.T.Equal(time.Date(2026, 10, 19, 14, 0, 0, 0, time.UTC)) || h.Input != 11 || h.Cached != 102 || h.Output != 8 {
		t.Errorf("first hour %+v", h)
	}
	if l := r.Limits["claude"]; len(l.Windows) != 1 || l.Windows[0].Used != 20 || !l.At.Equal(now) {
		t.Errorf("limits %+v", r.Limits)
	}
	if _, ok := r.Limits["codex"]; ok {
		t.Error("codex reported no limits")
	}

	// Saved and read back; what's older than the report's reach is left
	// out, and what's older than a month is dropped.
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
	now = now.Add(20 * 24 * time.Hour)
	s = open(t, path, &now)
	if r := s.Report(15); len(r.Hours) != 0 || len(r.Limits) != 1 {
		t.Errorf("after 20 days %+v", r)
	}
	if r := s.Report(30); len(r.Hours) != 3 {
		t.Errorf("30 days back %+v", r.Hours)
	}
	now = now.Add(15 * 24 * time.Hour)
	s.Add("codex", chat.Usage{Output: 1})
	if r := s.Report(60); len(r.Hours) != 1 {
		t.Errorf("a month on %+v", r.Hours)
	}
}

// Only harness names Skybreaker knows the shape of are kept, and nothing
// negative.
func TestStoreRefusesJunk(t *testing.T) {
	now := time.Now()
	s := open(t, filepath.Join(t.TempDir(), "usage.json"), &now)
	s.Add("Bad Name", chat.Usage{Input: 1})
	s.Add("claude", chat.Usage{Input: -5})
	s.Add("claude", chat.Usage{})
	s.SetLimits("../x", []chat.Limit{{Name: "a", Used: 1}})
	if r := s.Report(15); len(r.Hours) != 0 || len(r.Limits) != 0 {
		t.Errorf("%+v", r)
	}
}

func TestOpenMissing(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "none.json")); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(bad, []byte("{"), 0o600)
	if _, err := Open(bad); err == nil {
		t.Error("a corrupt file opened")
	}
}
