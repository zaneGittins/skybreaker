package store

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func open(t *testing.T) (*Store, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "state", "skybreaker.json")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	return s, p
}

func TestSessionsPersist(t *testing.T) {
	s, p := open(t)
	t0 := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	for i, id := range []string{"s1", "s2", "s3"} {
		proj := "orbit"
		if id == "s2" {
			proj = "bifract"
		}
		if err := s.CreateSession(Session{ID: id, Project: proj, Harness: "claude", Created: t0.Add(time.Duration(i) * time.Minute), Status: StatusRunning}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CreateSession(Session{ID: "s1", Project: "orbit"}); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate create: %v", err)
	}
	if err := s.UpdateSession("s1", func(x *Session) { x.Status = StatusExited }); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateSession("nope", func(*Session) {}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update missing: %v", err)
	}

	s2, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	got := s2.ListSessions("orbit")
	if len(got) != 2 || got[0].ID != "s3" || got[1].ID != "s1" {
		t.Fatalf("ListSessions(orbit) = %+v, want s3, s1 (newest first)", got)
	}
	if got[1].Status != StatusExited {
		t.Errorf("status not persisted: %q", got[1].Status)
	}
	if all := s2.ListSessions(""); len(all) != 3 {
		t.Errorf("ListSessions(\"\") = %d, want 3", len(all))
	}
	if x, ok := s2.Session("s2"); !ok || x.Project != "bifract" {
		t.Errorf("Session(s2) = %+v %v", x, ok)
	}
}

func TestFilePermissions(t *testing.T) {
	s, p := open(t)
	if err := s.CreateSession(Session{ID: "a", Project: "p"}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", fi.Mode().Perm())
	}
}

func TestRuns(t *testing.T) {
	s, p := open(t)
	t0 := time.Date(2026, 10, 17, 2, 0, 0, 0, time.UTC)
	for i := range 3 {
		r := Run{ID: string(rune('a' + i)), Job: "nightly", Project: "orbit", Started: t0.Add(time.Duration(i) * time.Hour), Status: RunRunning}
		if err := s.CreateRun(r); err != nil {
			t.Fatal(err)
		}
	}
	code := 0
	if err := s.UpdateRun("b", func(r *Run) { r.Status = RunOK; r.ExitCode = &code; r.CostUSD = 0.42 }); err != nil {
		t.Fatal(err)
	}
	s2, _ := Open(p)
	runs := s2.ListRuns("orbit", 2)
	if len(runs) != 2 || runs[0].ID != "c" || runs[1].ID != "b" {
		t.Fatalf("ListRuns = %+v", runs)
	}
	if runs[1].ExitCode == nil || *runs[1].ExitCode != 0 || runs[1].CostUSD != 0.42 {
		t.Errorf("run b not updated: %+v", runs[1])
	}
	if r, ok := s2.Run("a"); !ok || r.Job != "nightly" {
		t.Errorf("Run(a) = %+v", r)
	}
}

func TestRunsPruned(t *testing.T) {
	s, _ := open(t)
	s.maxRuns = 5
	t0 := time.Unix(0, 0)
	for i := range 8 {
		s.CreateRun(Run{ID: string(rune('a' + i)), Project: "p", Started: t0.Add(time.Duration(i) * time.Minute)})
	}
	runs := s.ListRuns("", 0)
	if len(runs) != 5 || runs[4].ID != "d" {
		t.Fatalf("kept %d runs, oldest %q; want 5, d", len(runs), runs[len(runs)-1].ID)
	}
}

func TestEndedSessionsPruned(t *testing.T) {
	s, _ := open(t)
	s.maxEnded = 3
	t0 := time.Unix(0, 0)
	for i := range 6 {
		status := StatusExited
		if i%3 == 0 {
			status = StatusRunning // a and d: never pruned, however old
		}
		s.CreateSession(Session{ID: string(rune('a' + i)), Project: "p", Status: status, Created: t0.Add(time.Duration(i) * time.Minute)})
	}
	var ids []string
	for _, x := range s.ListSessions("") {
		ids = append(ids, x.ID)
	}
	if want := []string{"f", "e", "d", "c", "a"}; !slices.Equal(ids, want) {
		t.Fatalf("sessions %v, want %v", ids, want)
	}
}

func TestTokens(t *testing.T) {
	s, p := open(t)
	if _, ok := s.Token("orbit"); ok {
		t.Fatal("token before set")
	}
	if err := s.SetToken("orbit", TokenRef{ID: "tok1", Minted: time.Unix(5, 0)}); err != nil {
		t.Fatal(err)
	}
	s2, _ := Open(p)
	if r, ok := s2.Token("orbit"); !ok || r.ID != "tok1" {
		t.Fatalf("Token = %+v %v", r, ok)
	}
	s2.DeleteToken("orbit")
	if _, ok := s2.Token("orbit"); ok {
		t.Fatal("token after delete")
	}
}

func TestMemoryStore(t *testing.T) {
	s, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(Session{ID: "a", Project: "p"}); err != nil {
		t.Fatal(err)
	}
	if len(s.ListSessions("p")) != 1 {
		t.Fatal("memory store lost session")
	}
}

func TestCorruptFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.json")
	os.WriteFile(p, []byte("{not json"), 0o600)
	if _, err := Open(p); err == nil {
		t.Fatal("want error for corrupt state")
	}
}

func TestPreviewPorts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, _ := Open(path)
	a, err := s.PreviewPort("orbit", 8401, 8403)
	b, _ := s.PreviewPort("comet", 8401, 8403)
	again, _ := s.PreviewPort("orbit", 8401, 8403)
	if err != nil || a != 8401 || b != 8402 || again != 8401 {
		t.Fatalf("ports %d %d %d %v", a, b, again, err)
	}
	// Kept across restarts, and never handed to another project.
	s2, _ := Open(path)
	if p, _ := s2.PreviewPort("comet", 8401, 8403); p != 8402 {
		t.Fatalf("after reopen comet has %d", p)
	}
	if p, _ := s2.PreviewPort("atlas", 8401, 8403); p != 8403 {
		t.Fatalf("atlas got %d", p)
	}
	if _, err := s2.PreviewPort("nova", 8401, 8403); err == nil {
		t.Fatal("ran past the range")
	}
}
