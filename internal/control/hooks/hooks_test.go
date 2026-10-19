package hooks

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

type recorder struct {
	mu     sync.Mutex
	events []Event
}

func (r *recorder) add(e Event) { r.mu.Lock(); r.events = append(r.events, e); r.mu.Unlock() }
func (r *recorder) all() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.events...)
}

func newHandler(t *testing.T) (*Handler, *httptest.Server, *recorder) {
	t.Helper()
	rec := &recorder{}
	h := New(func(project, token string) bool { return project == "orbit" && token == "hooktok" }, rec.add)
	h.Timeout = 2 * time.Second
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return h, ts, rec
}

// send is safe to call from goroutines: it reports failures in its result.
func send(ts *httptest.Server, path, session, body string) (int, string) {
	req, _ := http.NewRequest("POST", ts.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if session != "" {
		req.Header.Set("X-Skybreaker-Session", session)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func post(t *testing.T, ts *httptest.Server, path, session, body string) (int, string) {
	t.Helper()
	code, b := send(ts, path, session, body)
	if code == 0 {
		t.Fatal(b)
	}
	return code, b
}

const permBody = `{"session_id":"c-1","hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"rm -rf build"}}`

func TestAuth(t *testing.T) {
	_, ts, _ := newHandler(t)
	for _, path := range []string{"/hooks/claude/orbit/wrong", "/hooks/claude/other/hooktok", "/hooks/claude/orbit", "/hooks/codex/orbit/hooktok"} {
		if code, _ := post(t, ts, path, "s1", permBody); code != http.StatusForbidden && code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 403/404", path, code)
		}
	}
}

// waitPending polls until one approval is pending and returns it.
func waitPending(t *testing.T, h *Handler) Pending {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if p := h.Pending(""); len(p) == 1 {
			return p[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no pending approval appeared")
	return Pending{}
}

func TestPermissionAllow(t *testing.T) {
	h, ts, rec := newHandler(t)
	done := make(chan string)
	go func() {
		_, body := send(ts, "/hooks/claude/orbit/hooktok", "s1", permBody)
		done <- body
	}()
	p := waitPending(t, h)
	if p.Project != "orbit" || p.Session != "s1" || p.Tool != "Bash" || !strings.Contains(string(p.Input), "rm -rf build") {
		t.Fatalf("pending = %+v", p)
	}
	if err := h.Decide(p.ID, Decision{Allow: true, By: "zane"}); err != nil {
		t.Fatal(err)
	}
	var out struct {
		HookSpecificOutput struct {
			HookEventName string `json:"hookEventName"`
			Decision      struct {
				Behavior string `json:"behavior"`
				Message  string `json:"message"`
			} `json:"decision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(<-done), &out); err != nil {
		t.Fatal(err)
	}
	if out.HookSpecificOutput.HookEventName != "PermissionRequest" || out.HookSpecificOutput.Decision.Behavior != "allow" {
		t.Fatalf("response = %+v", out)
	}
	if len(h.Pending("")) != 0 {
		t.Error("approval still pending after decision")
	}
	if err := h.Decide(p.ID, Decision{Allow: true}); !errors.Is(err, ErrNotFound) {
		t.Errorf("second decide: %v, want ErrNotFound", err)
	}
	ev := rec.all()
	if len(ev) < 2 || ev[0].Kind != KindPermission || ev[len(ev)-1].Kind != KindDecided {
		t.Errorf("events = %+v", ev)
	}
}

func TestPermissionDeny(t *testing.T) {
	h, ts, _ := newHandler(t)
	done := make(chan string)
	go func() { _, b := send(ts, "/hooks/claude/orbit/hooktok", "s1", permBody); done <- b }()
	p := waitPending(t, h)
	h.Decide(p.ID, Decision{Allow: false, Message: "not now"})
	body := <-done
	if !strings.Contains(body, `"behavior":"deny"`) || !strings.Contains(body, "not now") {
		t.Fatalf("body %s", body)
	}
}

func TestPermissionTimeoutFallsBackToTUI(t *testing.T) {
	h, ts, _ := newHandler(t)
	h.Timeout = 50 * time.Millisecond
	code, body := post(t, ts, "/hooks/claude/orbit/hooktok", "s1", permBody)
	if code != 200 || strings.TrimSpace(body) != "{}" {
		t.Fatalf("timeout response %d %q, want 200 {}", code, body)
	}
	if len(h.Pending("")) != 0 {
		t.Error("expired approval left pending")
	}
}

func TestNotificationAndStop(t *testing.T) {
	_, ts, rec := newHandler(t)
	post(t, ts, "/hooks/claude/orbit/hooktok", "s1", `{"hook_event_name":"Notification","message":"Claude needs your permission"}`)
	post(t, ts, "/hooks/claude/orbit/hooktok", "s1", `{"hook_event_name":"Stop"}`)
	ev := rec.all()
	if len(ev) != 2 || ev[0].Kind != KindNotification || ev[0].Message != "Claude needs your permission" || ev[1].Kind != KindStop || ev[1].Session != "s1" {
		t.Fatalf("events = %+v", ev)
	}
}

func TestBadInput(t *testing.T) {
	_, ts, _ := newHandler(t)
	if code, _ := post(t, ts, "/hooks/claude/orbit/hooktok", "s1", "{nope"); code != http.StatusBadRequest {
		t.Errorf("bad json: %d", code)
	}
	if code, _ := post(t, ts, "/hooks/claude/orbit/hooktok", "bad session!", permBody); code != http.StatusBadRequest {
		t.Errorf("bad session header: %d", code)
	}
	big := `{"hook_event_name":"Stop","x":"` + strings.Repeat("a", 2<<20) + `"}`
	if code, _ := post(t, ts, "/hooks/claude/orbit/hooktok", "s1", big); code != http.StatusRequestEntityTooLarge && code != http.StatusBadRequest {
		t.Errorf("oversized body: %d", code)
	}
}

func TestPendingFilter(t *testing.T) {
	h, ts, _ := newHandler(t)
	go send(ts, "/hooks/claude/orbit/hooktok", "s1", permBody)
	p := waitPending(t, h)
	if len(h.Pending("other")) != 0 || len(h.Pending("orbit")) != 1 {
		t.Error("Pending filter wrong")
	}
	h.Decide(p.ID, Decision{Allow: false})
}

func TestClaudeSettings(t *testing.T) {
	b, err := ClaudeSettings("http://10.89.4.1:7701/hooks/claude/orbit/hooktok")
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	for _, ev := range []string{"PermissionRequest", "Notification", "Stop"} {
		g := s.Hooks[ev]
		if len(g) != 1 || len(g[0].Hooks) != 1 {
			t.Fatalf("%s: %+v", ev, g)
		}
		c := g[0].Hooks[0]
		if c.Type != "command" || !strings.Contains(c.Command, "'http://10.89.4.1:7701/hooks/claude/orbit/hooktok'") ||
			!strings.Contains(c.Command, "${SKYBREAKER_SESSION:-}") || !strings.Contains(c.Command, "--noproxy") {
			t.Errorf("%s command: %s", ev, c.Command)
		}
	}
	if s.Hooks["PermissionRequest"][0].Hooks[0].Timeout != 600 {
		t.Error("permission hook timeout should be 600s")
	}
	if _, err := ClaudeSettings("http://x/'; rm -rf /"); err == nil {
		t.Error("URL with a quote accepted")
	}
}

func TestSourceCheck(t *testing.T) {
	h, ts, _ := newHandler(t)
	// httptest clients come from 127.0.0.1; pretend orbit's subnet is elsewhere.
	h.AllowSource = func(project string, ip netip.Addr) bool { return project == "orbit" && ip.String() == "10.89.4.2" }
	if code, _ := post(t, ts, "/hooks/claude/orbit/hooktok", "s1", `{"hook_event_name":"Stop"}`); code != http.StatusForbidden {
		t.Fatalf("call from outside the project's subnet: %d", code)
	}
	h.AllowSource = func(project string, ip netip.Addr) bool { return ip.IsLoopback() }
	if code, _ := post(t, ts, "/hooks/claude/orbit/hooktok", "s1", `{"hook_event_name":"Stop"}`); code != 200 {
		t.Fatalf("call from an allowed source: %d", code)
	}
}

func TestPendingCapPerProject(t *testing.T) {
	// A hostile agent can't pile up held requests: past the cap they fall
	// straight back to the TUI.
	h, ts, _ := newHandler(t)
	h.MaxPending = 2
	h.Timeout = 5 * time.Second
	for i := 0; i < 2; i++ {
		go send(ts, "/hooks/claude/orbit/hooktok", "s1", permBody)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(h.Pending("orbit")) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	start := time.Now()
	code, body := post(t, ts, "/hooks/claude/orbit/hooktok", "s1", permBody)
	if code != 200 || strings.TrimSpace(body) != "{}" || time.Since(start) > time.Second {
		t.Fatalf("over the cap: %d %q after %v", code, body, time.Since(start))
	}
	for _, p := range h.Pending("") {
		h.Decide(p.ID, Decision{})
	}
}

func TestDecisionAtDeadlineIsKept(t *testing.T) {
	// Decide wins the race with the timer: its decision must reach Claude.
	h := New(func(string, string) bool { return true }, nil)
	p, _ := h.registerIfRoom(Pending{Project: "orbit", Session: "s1"})
	if err := h.Decide(p.ID, Decision{Allow: true}); err != nil {
		t.Fatal(err)
	}
	d, ok := h.expire(p)
	if !ok || !d.Allow {
		t.Fatalf("expire after Decide = %+v %v", d, ok)
	}
	q, _ := h.registerIfRoom(Pending{Project: "orbit"})
	if _, ok := h.expire(q); ok {
		t.Fatal("expire with no decision reported one")
	}
	if err := h.Decide(q.ID, Decision{Allow: true}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Decide after expiry: %v", err)
	}
}

func TestPendingCapHoldsUnderConcurrency(t *testing.T) {
	// The cap is checked and the request registered under one lock, so a
	// burst of simultaneous requests can't all slip past the check.
	h := New(func(string, string) bool { return true }, nil)
	h.MaxPending = 2
	var wg sync.WaitGroup
	start := make(chan struct{})
	var mu sync.Mutex
	admitted := 0
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, ok := h.registerIfRoom(Pending{Project: "orbit"}); ok {
				mu.Lock()
				admitted++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()
	if admitted != 2 || len(h.Pending("orbit")) != 2 {
		t.Fatalf("admitted %d, pending %d; want 2", admitted, len(h.Pending("orbit")))
	}
}

// Chat sessions answer permission prompts in their own conversation, so
// their hooks return at once with no decision and raise nothing here.
func TestChatSessionsPassThrough(t *testing.T) {
	h, ts, rec := newHandler(t)
	h.Direct = func(project, session string) bool { return project == "orbit" && session == "schat" }
	start := time.Now()
	code, body := post(t, ts, "/hooks/claude/orbit/hooktok", "schat", permBody)
	if code != http.StatusOK || strings.Contains(body, "decision") || time.Since(start) > time.Second {
		t.Errorf("chat permission hook = %d %s after %v", code, body, time.Since(start))
	}
	post(t, ts, "/hooks/claude/orbit/hooktok", "schat", `{"hook_event_name":"Stop"}`)
	if len(h.Pending("")) != 0 || len(rec.all()) != 0 {
		t.Errorf("chat hook raised %+v / %+v", h.Pending(""), rec.all())
	}
	// A terminal session in the same project still gets a card.
	go send(ts, "/hooks/claude/orbit/hooktok", "sterm", permBody)
	p := waitPending(t, h)
	h.Decide(p.ID, Decision{Allow: true})
}
