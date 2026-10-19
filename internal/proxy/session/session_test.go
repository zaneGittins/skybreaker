package session

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newClock() *clock { return &clock{t: time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)} }

func TestMintAndLookup(t *testing.T) {
	c := newClock()
	r, err := NewRegistry("", c.now)
	if err != nil {
		t.Fatal(err)
	}
	tok, info, err := r.Mint("orbit", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil || len(raw) != 32 {
		t.Fatalf("token %q is not 32 bytes of base64url: %v", tok, err)
	}
	if info.Project != "orbit" || info.ID == "" || !info.Expires.Equal(c.now().Add(time.Hour)) {
		t.Errorf("info = %+v", info)
	}
	got, ok := r.Lookup(tok)
	if !ok || got.ID != info.ID || got.Project != "orbit" {
		t.Errorf("Lookup = %+v, %v", got, ok)
	}
	tok2, _, _ := r.Mint("orbit", time.Hour)
	if tok2 == tok {
		t.Error("two mints returned the same token")
	}
}

func TestLookupRejects(t *testing.T) {
	c := newClock()
	r, _ := NewRegistry("", c.now)
	tok, _, _ := r.Mint("orbit", time.Minute)
	for _, bad := range []string{"", "x", tok + "x", strings.ToUpper(tok), " " + tok} {
		if _, ok := r.Lookup(bad); ok {
			t.Errorf("Lookup(%q) accepted", bad)
		}
	}
	c.add(time.Minute)
	if _, ok := r.Lookup(tok); ok {
		t.Error("expired token accepted")
	}
	if n := len(r.List()); n != 0 {
		t.Errorf("List shows %d expired sessions", n)
	}
}

func TestDefaultTTL(t *testing.T) {
	c := newClock()
	r, _ := NewRegistry("", c.now)
	_, info, _ := r.Mint("orbit", 0)
	if want := c.now().Add(30 * 24 * time.Hour); !info.Expires.Equal(want) {
		t.Errorf("Expires = %v, want %v", info.Expires, want)
	}
}

func TestMintValidatesProject(t *testing.T) {
	r, _ := NewRegistry("", nil)
	for _, p := range []string{"", "Orbit", "-x", "a b", "a/b", "../x", strings.Repeat("a", 41), "é"} {
		if _, _, err := r.Mint(p, time.Hour); err == nil {
			t.Errorf("Mint(%q) accepted", p)
		}
	}
	for _, p := range []string{"a", "orbit", "my-proj-2", strings.Repeat("a", 40)} {
		if _, _, err := r.Mint(p, time.Hour); err != nil {
			t.Errorf("Mint(%q): %v", p, err)
		}
	}
}

func TestRevoke(t *testing.T) {
	r, _ := NewRegistry("", nil)
	a, ia, _ := r.Mint("a", time.Hour)
	b1, _, _ := r.Mint("b", time.Hour)
	b2, _, _ := r.Mint("b", time.Hour)
	c, _, _ := r.Mint("c", time.Hour)

	if ok, err := r.Revoke(ia.ID); !ok || err != nil {
		t.Error("Revoke returned false for a live session")
	}
	if again, _ := r.Revoke(ia.ID); again {
		t.Fatal("revoked twice")
	}
	if missing, _ := r.Revoke("nope"); missing {
		t.Error("Revoke returned true for an unknown session")
	}
	if _, ok := r.Lookup(a); ok {
		t.Error("revoked token still works")
	}
	if n, _ := r.RevokeProject("b"); n != 2 {
		t.Errorf("RevokeProject = %d, want 2", n)
	}
	for _, tok := range []string{b1, b2} {
		if _, ok := r.Lookup(tok); ok {
			t.Error("project-revoked token still works")
		}
	}
	if _, ok := r.Lookup(c); !ok {
		t.Error("other project's token was revoked")
	}
	r.Mint("d", time.Hour)
	if n, _ := r.RevokeAll(); n != 2 {
		t.Errorf("RevokeAll = %d, want 2", n)
	}
	if len(r.List()) != 0 {
		t.Error("sessions survive RevokeAll")
	}
}

func TestListSortedByCreated(t *testing.T) {
	c := newClock()
	r, _ := NewRegistry("", c.now)
	for _, p := range []string{"c", "a", "b"} {
		r.Mint(p, time.Hour)
		c.add(time.Second)
	}
	l := r.List()
	if len(l) != 3 || l[0].Project != "c" || l[1].Project != "a" || l[2].Project != "b" {
		t.Errorf("List = %+v", l)
	}
}

func TestPersistence(t *testing.T) {
	c := newClock()
	path := filepath.Join(t.TempDir(), "state", "sessions.json")
	r, err := NewRegistry(path, c.now)
	if err != nil {
		t.Fatal(err)
	}
	tok, info, _ := r.Mint("orbit", time.Hour)
	gone, _, _ := r.Mint("other", time.Hour)
	r.RevokeProject("other")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), tok) || strings.Contains(string(data), gone) {
		t.Fatal("session file contains a raw token")
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("session file mode %v, want 0600", fi.Mode().Perm())
	}

	r2, err := NewRegistry(path, c.now)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := r2.Lookup(tok)
	if !ok || got.ID != info.ID {
		t.Errorf("token not restored: %+v %v", got, ok)
	}
	if _, ok := r2.Lookup(gone); ok {
		t.Error("revoked token came back after reload")
	}
	c.add(2 * time.Hour)
	r3, _ := NewRegistry(path, c.now)
	if _, ok := r3.Lookup(tok); ok {
		t.Error("expired token accepted after reload")
	}
}

func TestCorruptFileIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	os.WriteFile(path, []byte("{nope"), 0o600)
	if _, err := NewRegistry(path, nil); err == nil {
		t.Error("corrupt session file accepted")
	}
}

func TestConcurrentUse(t *testing.T) {
	r, _ := NewRegistry(filepath.Join(t.TempDir(), "s.json"), nil)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok, info, err := r.Mint("p", time.Hour)
			if err != nil {
				t.Error(err)
				return
			}
			r.Lookup(tok)
			r.List()
			r.Revoke(info.ID)
		}()
	}
	wg.Wait()
}

func TestRevokeReportsSaveFailure(t *testing.T) {
	// A revocation that wasn't saved comes back after a restart, so the
	// caller must hear about it rather than report success.
	dir := filepath.Join(t.TempDir(), "state")
	r, err := NewRegistry(filepath.Join(dir, "sessions.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	tok, info, _ := r.Mint("orbit", time.Hour)
	r.Mint("bifract", time.Hour)
	os.Chmod(dir, 0o500)
	defer os.Chmod(dir, 0o700)
	if ok, err := r.Revoke(info.ID); !ok || err == nil {
		t.Fatalf("Revoke = %v, %v; want revoked with a save error", ok, err)
	}
	if _, live := r.Lookup(tok); live {
		t.Fatal("token still live in memory after a failed save")
	}
	if n, err := r.RevokeAll(); n != 1 || err == nil {
		t.Fatalf("RevokeAll = %d, %v; want 1 with a save error", n, err)
	}
	os.Chmod(dir, 0o700)
	if n, err := r.RevokeProject("ghost"); n != 0 || err != nil {
		t.Fatalf("RevokeProject(ghost) = %d, %v", n, err)
	}
}
