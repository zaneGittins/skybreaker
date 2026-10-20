package approval

import (
	"context"
	"errors"
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

func newStore() (*Store, *clock) {
	c := &clock{t: time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)}
	return New(Options{Now: c.now}), c
}

// waiters reports how many requests are waiting on an approval.
func (s *Store) waiters(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.byID[id]; e != nil {
		return e.waiting
	}
	return 0
}

func req(path string) Request {
	return Request{Project: "orbit", Method: "PUT", Host: "api.github.com", Path: path, Body: []byte(`{"merge_method":"squash"}`),
		View: View{Title: "Merge PR #4", Fields: []Field{{"Repo", "zaneGittins/orbit"}}}, Sender: "agent-vm"}
}

func TestHashBindsEveryField(t *testing.T) {
	base := Hash("orbit", "PUT", "api.github.com", "/repos/a/b/pulls/4/merge", []byte("body"))
	if base != Hash("orbit", "PUT", "api.github.com", "/repos/a/b/pulls/4/merge", []byte("body")) {
		t.Fatal("hash is not deterministic")
	}
	if len(base) != 64 {
		t.Errorf("hash %q is not hex sha256", base)
	}
	others := map[string]string{
		"project": Hash("bifract", "PUT", "api.github.com", "/repos/a/b/pulls/4/merge", []byte("body")),
		"method":  Hash("orbit", "POST", "api.github.com", "/repos/a/b/pulls/4/merge", []byte("body")),
		"host":    Hash("orbit", "PUT", "evil.example", "/repos/a/b/pulls/4/merge", []byte("body")),
		"path":    Hash("orbit", "PUT", "api.github.com", "/repos/a/b/pulls/5/merge", []byte("body")),
		"body":    Hash("orbit", "PUT", "api.github.com", "/repos/a/b/pulls/4/merge", []byte("body2")),
		"nobody":  Hash("orbit", "PUT", "api.github.com", "/repos/a/b/pulls/4/merge", nil),
	}
	for name, h := range others {
		if h == base {
			t.Errorf("changing %s did not change the hash", name)
		}
	}
	// Field boundaries are length-prefixed, so shifting bytes between fields
	// can't collide.
	if Hash("a", "GET", "h", "bc", nil) == Hash("ab", "GET", "h", "c", nil) {
		t.Error("project/path boundary is ambiguous")
	}
	if Hash("p", "GE", "Th", "/", nil) == Hash("p", "GET", "h", "/", nil) {
		t.Error("method/host boundary is ambiguous")
	}
}

func TestTimeoutReturnsPendingThenApprovedRetryPassesOnce(t *testing.T) {
	s, _ := newStore()
	a, out := s.Request(context.Background(), req("/m"), 10*time.Millisecond)
	if out != Pending || a.ID == "" || a.State != StatePending {
		t.Fatalf("first request = %v, %+v", out, a)
	}
	if a.View.Title != "Merge PR #4" || a.Sender != "agent-vm" || a.Hash == "" {
		t.Errorf("approval lost request details: %+v", a)
	}
	if _, err := s.Decide(a.ID, true, "zane@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, out := s.Request(context.Background(), req("/m"), 0); out != Allowed {
		t.Fatalf("retry after approval = %v, want Allowed", out)
	}
	// Single use: the same request again needs a fresh approval.
	b, out := s.Request(context.Background(), req("/m"), 0)
	if out != Pending || b.ID == a.ID {
		t.Fatalf("replay = %v (id %s, first %s); want a new pending approval", out, b.ID, a.ID)
	}
	got, _ := s.Get(a.ID)
	if got.State != StateUsed || got.DecidedBy != "zane@example.com" {
		t.Errorf("used approval = %+v", got)
	}
}

func TestApprovalDoesNotCoverADifferentRequest(t *testing.T) {
	s, _ := newStore()
	a, _ := s.Request(context.Background(), req("/m"), 0)
	s.Decide(a.ID, true, "me")
	variants := []Request{req("/other")}
	r := req("/m")
	r.Body = []byte(`{"merge_method":"rebase"}`)
	variants = append(variants, r)
	r = req("/m")
	r.Project = "bifract"
	variants = append(variants, r)
	r = req("/m")
	r.Host = "evil.example"
	variants = append(variants, r)
	for _, v := range variants {
		if _, out := s.Request(context.Background(), v, 0); out == Allowed {
			t.Errorf("approval for /m let %+v through", v)
		}
	}
	if _, out := s.Request(context.Background(), req("/m"), 0); out != Allowed {
		t.Error("the approved request itself should still pass")
	}
}

func TestApprovedDuringHold(t *testing.T) {
	s, _ := newStore()
	events, cancel := s.Subscribe()
	defer cancel()
	go func() {
		a := <-events
		if _, err := s.Decide(a.ID, true, "me"); err != nil {
			t.Error(err)
		}
	}()
	a, out := s.Request(context.Background(), req("/m"), 5*time.Second)
	if out != Allowed || a.State != StateUsed {
		t.Fatalf("got %v %+v, want Allowed", out, a)
	}
}

func TestDeniedDuringHoldAndOnRetry(t *testing.T) {
	s, c := newStore()
	events, cancel := s.Subscribe()
	defer cancel()
	go func() {
		a := <-events
		s.Decide(a.ID, false, "me")
	}()
	if _, out := s.Request(context.Background(), req("/m"), 5*time.Second); out != Denied {
		t.Fatalf("got %v, want Denied", out)
	}
	if _, out := s.Request(context.Background(), req("/m"), 0); out != Denied {
		t.Fatalf("retry after deny = %v, want Denied", out)
	}
	c.add(6 * time.Minute)
	if _, out := s.Request(context.Background(), req("/m"), 0); out != Pending {
		t.Fatalf("after the denial expires a retry should ask again, got %v", out)
	}
}

func TestConcurrentIdenticalRequestsShareOnePending(t *testing.T) {
	s, _ := newStore()
	events, cancel := s.Subscribe()
	defer cancel()

	const n = 5
	type result struct {
		a   Approval
		out Outcome
	}
	results := make(chan result, n)
	for i := 0; i < n; i++ {
		go func() {
			a, out := s.Request(context.Background(), req("/m"), 5*time.Second)
			results <- result{a, out}
		}()
	}
	first := <-events
	// Let every request join the pending one before deciding.
	deadline := time.Now().Add(2 * time.Second)
	for s.waiters(first.ID) < n && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := len(s.List()); got != 1 {
		t.Fatalf("%d approvals created for identical requests, want 1", got)
	}
	s.Decide(first.ID, true, "me")
	allowed := 0
	for i := 0; i < n; i++ {
		r := <-results
		if r.a.ID != first.ID {
			t.Errorf("request got approval %s, want %s", r.a.ID, first.ID)
		}
		if r.out == Allowed {
			allowed++
		}
	}
	if allowed != 1 {
		t.Errorf("%d requests passed on one approval, want exactly 1", allowed)
	}
}

func TestPendingExpires(t *testing.T) {
	s, c := newStore()
	a, _ := s.Request(context.Background(), req("/m"), 0)
	c.add(11 * time.Minute)
	if _, err := s.Decide(a.ID, true, "me"); !errors.Is(err, ErrNotPending) {
		t.Fatalf("approving an expired request: err = %v, want ErrNotPending", err)
	}
	got, _ := s.Get(a.ID)
	if got.State != StateExpired {
		t.Errorf("state = %s, want expired", got.State)
	}
	b, out := s.Request(context.Background(), req("/m"), 0)
	if out != Pending || b.ID == a.ID {
		t.Errorf("after expiry a new approval should be created, got %v %s", out, b.ID)
	}
}

func TestApprovedExpiresBeforeRetry(t *testing.T) {
	s, c := newStore()
	a, _ := s.Request(context.Background(), req("/m"), 0)
	s.Decide(a.ID, true, "me")
	c.add(6 * time.Minute)
	if _, out := s.Request(context.Background(), req("/m"), 0); out == Allowed {
		t.Fatal("approval used after its TTL")
	}
}

func TestDecideErrors(t *testing.T) {
	s, _ := newStore()
	if _, err := s.Decide("nope", true, "me"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown id: %v", err)
	}
	a, _ := s.Request(context.Background(), req("/m"), 0)
	s.Decide(a.ID, false, "me")
	if _, err := s.Decide(a.ID, true, "me"); !errors.Is(err, ErrNotPending) {
		t.Errorf("flipping a denial to approved: %v", err)
	}
}

func TestContextCancelEndsHold(t *testing.T) {
	s, _ := newStore()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	start := time.Now()
	a, out := s.Request(ctx, req("/m"), time.Minute)
	if out != Pending || a.ID == "" {
		t.Errorf("got %v %+v", out, a)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("cancel did not end the hold")
	}
}

func TestListNewestFirstAndPrunes(t *testing.T) {
	s, c := newStore()
	a, _ := s.Request(context.Background(), req("/a"), 0)
	c.add(time.Second)
	b, _ := s.Request(context.Background(), req("/b"), 0)
	l := s.List()
	if len(l) != 2 || l[0].ID != b.ID || l[1].ID != a.ID {
		t.Fatalf("List = %+v", l)
	}
	c.add(2 * time.Hour)
	if n := len(s.List()); n != 0 {
		t.Errorf("List kept %d long-expired approvals", n)
	}
	if _, ok := s.Get(a.ID); ok {
		t.Error("pruned approval still found")
	}
}

func TestSubscribeSeesCreateAndDecide(t *testing.T) {
	s, _ := newStore()
	ch, cancel := s.Subscribe()
	a, _ := s.Request(context.Background(), req("/m"), 0)
	s.Decide(a.ID, true, "me")
	e1, e2 := <-ch, <-ch
	if e1.ID != a.ID || e1.State != StatePending || e2.State != StateApproved {
		t.Errorf("events = %+v, %+v", e1, e2)
	}
	cancel()
	// A slow or gone subscriber must never block requests.
	for i := 0; i < 200; i++ {
		s.Request(context.Background(), req("/x"+string(rune('a'+i%26))+string(rune('a'+i/26))), 0)
	}
	_, cancel2 := s.Subscribe() // never read
	defer cancel2()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 200; i++ {
			s.Request(context.Background(), req("/y"+string(rune('a'+i%26))+string(rune('a'+i/26))), 0)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a full subscriber blocked requests")
	}
}

func TestIDsAreUnguessableAndDistinct(t *testing.T) {
	s := New(Options{MaxPending: 100})
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		a, _ := s.Request(context.Background(), req("/"+string(rune('a'+i%26))+string(rune('a'+i/26))), 0)
		if len(a.ID) != 16 || seen[a.ID] {
			t.Fatalf("bad or repeated id %q", a.ID)
		}
		seen[a.ID] = true
	}
}

func TestPendingCapPerProject(t *testing.T) {
	// A hostile agent varying the body would otherwise create unbounded
	// cards, held goroutines and phone pushes.
	s := New(Options{MaxPending: 2})
	ctx := context.Background()
	for i := range 2 {
		if _, out := s.Request(ctx, Request{Project: "orbit", Method: "POST", Host: "h", Path: "/", Body: []byte{byte(i)}}, 0); out != Pending {
			t.Fatalf("request %d: %v", i, out)
		}
	}
	if _, out := s.Request(ctx, Request{Project: "orbit", Method: "POST", Host: "h", Path: "/", Body: []byte{9}}, 0); out != Throttled {
		t.Fatalf("over the cap: %v, want Throttled", out)
	}
	// Joining an existing pending approval is not a new card.
	if _, out := s.Request(ctx, Request{Project: "orbit", Method: "POST", Host: "h", Path: "/", Body: []byte{0}}, 0); out != Pending {
		t.Fatalf("identical retry: %v, want Pending", out)
	}
	// Other projects have their own budget.
	if _, out := s.Request(ctx, Request{Project: "bifract", Method: "POST", Host: "h", Path: "/"}, 0); out != Pending {
		t.Fatalf("another project: %v", out)
	}
	if n := len(s.List()); n != 3 {
		t.Fatalf("%d approvals stored, want 3", n)
	}
}

func TestDisconnectedWaiterLeavesApproval(t *testing.T) {
	// If the client went away, its approval must stand for the retry.
	s := New(Options{})
	ctx, cancel := context.WithCancel(context.Background())
	sub, unsub := s.Subscribe()
	defer unsub()
	done := make(chan Outcome, 1)
	r := Request{Project: "orbit", Method: "PUT", Host: "h", Path: "/merge"}
	go func() { _, out := s.Request(ctx, r, 5*time.Second); done <- out }()
	a := <-sub
	cancel()
	s.Decide(a.ID, true, "zane")
	<-done
	if _, out := s.Request(context.Background(), r, 0); out != Allowed {
		t.Fatalf("retry after the first client left: %v, want Allowed", out)
	}
}

func TestViewRevealsInvisibleCharacters(t *testing.T) {
	s, _ := newStore()
	r := req("/x")
	r.View = View{
		Title:  "PUT api.github.com/repos/a/b/contents/\u202egpj.exe",
		Fields: []Field{{"Ref", "refs/heads/agent/x\u200b"}, {"Name\x1b[2J", "a\nb"}},
		Body:   "{\"a\":\t1,\n \"b\": \"ok\u2066\ufeff\U000E0041\"}\r\n\xff",
	}
	a, _ := s.Request(context.Background(), r, 0)
	v := a.View
	if want := "PUT api.github.com/repos/a/b/contents/<U+202E>gpj.exe"; v.Title != want {
		t.Errorf("Title = %q, want %q", v.Title, want)
	}
	if want := "refs/heads/agent/x<U+200B>"; v.Fields[0].Value != want {
		t.Errorf("Field = %q, want %q", v.Fields[0].Value, want)
	}
	if v.Fields[1].Label != "Name<U+001B>[2J" || v.Fields[1].Value != "a<U+000A>b" {
		t.Errorf("Field = %+v: controls and line breaks in fields must be shown", v.Fields[1])
	}
	if want := "{\"a\":\t1,\n \"b\": \"ok<U+2066><U+FEFF><U+E0041>\"}\r\n<0xFF>"; v.Body != want {
		t.Errorf("Body = %q, want %q", v.Body, want)
	}
	if r.View.Fields[0].Value != "refs/heads/agent/x\u200b" {
		t.Error("the caller's View was modified")
	}
	if got, _ := s.Get(a.ID); got.View.Title != v.Title {
		t.Error("the stored card differs from the returned one")
	}
}

// Revoking a project's token ends what was asked under it: a card left
// pending or approved can't be redeemed by the next token.
func TestExpireProject(t *testing.T) {
	s, _ := newStore()
	pending, _ := s.Request(context.Background(), req("/pending"), 0)
	approved, _ := s.Request(context.Background(), req("/approved"), 0)
	s.Decide(approved.ID, true, "zane@example.com")
	other := req("/other")
	other.Project = "bifract"
	theirs, _ := s.Request(context.Background(), other, 0)
	if n := s.Expire("orbit"); n != 2 {
		t.Fatalf("expired %d", n)
	}
	if _, err := s.Decide(pending.ID, true, "z"); !errors.Is(err, ErrNotPending) {
		t.Fatalf("decided an expired card: %v", err)
	}
	if _, out := s.Request(context.Background(), req("/approved"), 0); out == Allowed {
		t.Fatal("an approval outlived its project's revocation")
	}
	if a, _ := s.Get(theirs.ID); a.State != StatePending {
		t.Fatalf("another project's card: %s", a.State)
	}
	if n := s.Expire(""); n != 2 {
		t.Fatalf("expire all: %d", n)
	}
}
