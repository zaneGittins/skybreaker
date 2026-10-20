package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zaneGittins/skybreaker/internal/control/app"
	"github.com/zaneGittins/skybreaker/internal/control/chat"
	"github.com/zaneGittins/skybreaker/internal/control/store"
	"github.com/zaneGittins/skybreaker/internal/notify/webpush"
)

type fakeChat struct {
	mu       sync.Mutex
	calls    []string
	answers  []chat.Answer
	after    []int64
	past     []chat.Event
	live     chan chat.Event
	loginErr error
}

func newFakeChat() *fakeChat {
	return &fakeChat{live: make(chan chat.Event, 8)}
}

func (c *fakeChat) call(s string) {
	c.mu.Lock()
	c.calls = append(c.calls, s)
	c.mu.Unlock()
}

func (c *fakeChat) got() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.calls)
}

func known(id string) error {
	if id != "c1" {
		return app.ErrNotFound
	}
	return nil
}

func (b *fakeBackend) NewChat(_ context.Context, p, h string, o app.NewOptions) (store.Session, error) {
	b.chat.call("new " + p + " " + h + optsCall(o))
	return store.Session{ID: "c1", Project: p, Harness: h, Mode: store.ModeChat, Status: store.StatusRunning}, nil
}
func (b *fakeBackend) ChatSend(_ context.Context, id, text string) error {
	b.chat.call("send " + id + " " + text)
	return known(id)
}
func (b *fakeBackend) ChatAnswer(_ context.Context, id, ask string, a chat.Answer) error {
	b.chat.call("answer " + id + " " + ask)
	b.chat.mu.Lock()
	b.chat.answers = append(b.chat.answers, a)
	b.chat.mu.Unlock()
	return known(id)
}
func (b *fakeBackend) ChatInterrupt(_ context.Context, id string) error {
	b.chat.call("interrupt " + id)
	return known(id)
}
func (b *fakeBackend) ChatMode(_ context.Context, id, mode string) error {
	b.chat.call("mode " + id + " " + mode)
	return known(id)
}
func (b *fakeBackend) ChatShown(id, name string) ([]byte, error) {
	if id == "c1" && name == "0123456789abcdef0123456789abcdef.png" {
		return []byte("\x89PNG"), nil
	}
	if id == "c1" && name == "0123456789abcdef0123456789abcdef.txt" {
		return []byte("<script>alert(1)</script>"), nil
	}
	return nil, os.ErrNotExist
}
func (b *fakeBackend) ChatModel(_ context.Context, id, model string) error {
	b.chat.call("model " + id + " " + model)
	return known(id)
}
func (b *fakeBackend) ChatLogin(_ context.Context, id string) error {
	b.chat.call("login " + id)
	return b.chat.loginErr
}
func (b *fakeBackend) ChatTerminal(_ context.Context, id string) (store.Session, error) {
	if id == "busy" {
		return store.Session{}, app.ErrBusy
	}
	return store.Session{ID: "t1", Parent: id, Status: store.StatusRunning}, known(id)
}
func (b *fakeBackend) ChatCompact(_ context.Context, id string) error {
	b.chat.call("compact " + id)
	return known(id)
}
func (b *fakeBackend) ChatFork(_ context.Context, id string) (store.Session, error) {
	switch id {
	case "busy":
		return store.Session{}, app.ErrBusy
	case "new":
		return store.Session{}, errors.New("there's nothing to fork yet")
	}
	return store.Session{ID: "f1", Resume: "conv-1", Fork: true, Status: store.StatusRunning}, known(id)
}
func (b *fakeBackend) ChatEvents(id string, after int64) ([]chat.Event, error) {
	if err := known(id); err != nil {
		return nil, err
	}
	b.chat.mu.Lock()
	defer b.chat.mu.Unlock()
	b.chat.after = append(b.chat.after, after)
	var out []chat.Event
	for _, e := range b.chat.past {
		if e.Seq > after {
			out = append(out, e)
		}
	}
	return out, nil
}
func (b *fakeBackend) ChatSubscribe(id string) (<-chan chat.Event, func()) {
	return b.chat.live, func() {}
}
func (b *fakeBackend) ChatModes(h string) []string {
	if h == "claude" {
		return []string{"ask", "edits", "plan", "auto"}
	}
	return nil
}
func (b *fakeBackend) MarkSeen(id string) error { b.chat.call("seen " + id); return known(id) }
func (b *fakeBackend) Review(_ context.Context, p string) (app.Review, error) {
	return app.Review{Branch: "feature/x", Diff: "+new", Files: []app.FileChange{{Path: "a.go", Added: 1}}}, nil
}
func (b *fakeBackend) ReviewSession(_ context.Context, id string) (app.Review, error) {
	b.chat.call("review " + id)
	if err := known(id); err != nil {
		return app.Review{}, err
	}
	return app.Review{Branch: "skybreaker/" + id}, nil
}

// optsCall is how a fake call records where a session works.
func optsCall(o app.NewOptions) string {
	out := ""
	if o.OwnBranch {
		out += " own-branch"
	}
	if o.Continues != "" {
		out += " continues " + o.Continues
	}
	return out
}

func TestNewSessionModes(t *testing.T) {
	f := newFixture(t, you)
	code, body := f.do(t, "POST", "/api/projects/orbit/sessions", `{"harness":"claude"}`, nil)
	if code != 200 || !strings.Contains(body, `"mode":"chat"`) {
		t.Fatalf("claude: %d %s", code, body)
	}
	code, body = f.do(t, "POST", "/api/projects/orbit/sessions", `{"harness":"claude","mode":"terminal"}`, nil)
	if code != 200 || strings.Contains(body, `"mode":"chat"`) {
		t.Fatalf("claude terminal: %d %s", code, body)
	}
	code, body = f.do(t, "POST", "/api/projects/orbit/sessions", `{"harness":"shell"}`, nil)
	if code != 200 || strings.Contains(body, `"mode":"chat"`) {
		t.Fatalf("shell: %d %s", code, body)
	}
	if code, _ := f.do(t, "POST", "/api/projects/orbit/sessions", `{"harness":"shell","mode":"chat"}`, nil); code != 400 {
		t.Errorf("shell chat: %d", code)
	}
	if code, _ := f.do(t, "POST", "/api/projects/orbit/sessions", `{"harness":"claude","mode":"weird"}`, nil); code != 400 {
		t.Errorf("weird mode: %d", code)
	}
	if got := f.backend.chat.got(); !slices.Equal(got, []string{"new orbit claude"}) {
		t.Errorf("chat calls = %q", got)
	}
}

func TestNewSessionOnItsOwnBranch(t *testing.T) {
	f := newFixture(t, you)
	for _, body := range []string{
		`{"harness":"claude","own_branch":true}`,
		`{"harness":"claude","continues":"c1"}`,
		`{"harness":"codex","mode":"terminal","own_branch":true}`,
	} {
		if code, out := f.do(t, "POST", "/api/projects/orbit/sessions", body, nil); code != 200 {
			t.Fatalf("%s: %d %s", body, code, out)
		}
	}
	want := []string{"new orbit claude own-branch", "new orbit claude continues c1", "terminal orbit codex own-branch"}
	if got := f.backend.chat.got(); !slices.Equal(got, want) {
		t.Errorf("calls = %q, want %q", got, want)
	}
	code, body := f.do(t, "GET", "/api/sessions/c1/review", "", nil)
	if code != 200 || !strings.Contains(body, `"branch":"skybreaker/c1"`) {
		t.Errorf("session review: %d %s", code, body)
	}
	if code, _ := f.do(t, "GET", "/api/sessions/nope/review", "", nil); code != 404 {
		t.Errorf("unknown session's review: %d", code)
	}
}

func TestChatActions(t *testing.T) {
	f := newFixture(t, you)
	for _, c := range []struct {
		path, body string
		want       int
	}{
		{"/api/sessions/c1/messages", `{"text":"hello"}`, 200},
		{"/api/sessions/c1/messages", `{"text":""}`, 400},
		{"/api/sessions/nope/messages", `{"text":"x"}`, 404},
		{"/api/sessions/c1/answer", `{"ask":"r1","allow":true,"always":true,"answers":{"q0":["SQLite"]}}`, 200},
		{"/api/sessions/c1/answer", `{"allow":true}`, 400},
		{"/api/sessions/c1/interrupt", `{}`, 200},
		{"/api/sessions/c1/mode", `{"mode":"edits"}`, 200},
		{"/api/sessions/c1/model", `{"model":"sonnet"}`, 200},
		{"/api/sessions/c1/model", `{"model":""}`, 400},
		{"/api/sessions/c1/seen", `{}`, 200},
		{"/api/sessions/c1/login", `{}`, 200},
		{"/api/sessions/c1/compact", `{}`, 200},
		{"/api/sessions/nope/compact", `{}`, 404},
	} {
		if code, body := f.do(t, "POST", c.path, c.body, nil); code != c.want {
			t.Errorf("%s %s: %d %s, want %d", c.path, c.body, code, body, c.want)
		}
	}
	got := f.backend.chat.got()
	for _, want := range []string{"send c1 hello", "answer c1 r1", "interrupt c1", "mode c1 edits", "model c1 sonnet", "seen c1", "login c1", "compact c1"} {
		if !slices.Contains(got, want) {
			t.Errorf("calls %q lack %q", got, want)
		}
	}
	if a := f.backend.chat.answers[0]; !a.Allow || !a.Always || a.Answers["q0"][0] != "SQLite" {
		t.Errorf("answer = %+v", a)
	}
	// A harness that signs in only in its terminal says so.
	f.backend.chat.loginErr = chat.ErrTerminalLogin
	if code, body := f.do(t, "POST", "/api/sessions/c1/login", `{}`, nil); code != 409 || !strings.Contains(body, `"terminal":true`) {
		t.Errorf("terminal login: %d %s", code, body)
	}
	if code, body := f.do(t, "POST", "/api/sessions/c1/terminal", `{}`, nil); code != 200 || !strings.Contains(body, `"id":"t1"`) {
		t.Errorf("terminal: %d %s", code, body)
	}
	if code, _ := f.do(t, "POST", "/api/sessions/busy/terminal", `{}`, nil); code != 409 {
		t.Errorf("busy terminal: %d", code)
	}
	if code, body := f.do(t, "POST", "/api/sessions/c1/fork", `{}`, nil); code != 200 || !strings.Contains(body, `"id":"f1"`) || !strings.Contains(body, `"fork":true`) {
		t.Errorf("fork: %d %s", code, body)
	}
	for path, want := range map[string]int{"/api/sessions/busy/fork": 409, "/api/sessions/nope/fork": 404, "/api/sessions/new/fork": 400} {
		if code, body := f.do(t, "POST", path, `{}`, nil); code != want {
			t.Errorf("%s: %d %s, want %d", path, code, body, want)
		}
	}
}

func readSSE(t *testing.T, sc *bufio.Scanner, n int) (ids []string, events []chat.Event) {
	t.Helper()
	id := ""
	for len(events) < n && sc.Scan() {
		line := sc.Text()
		if v, ok := strings.CutPrefix(line, "id: "); ok {
			id = v
		}
		if v, ok := strings.CutPrefix(line, "data: "); ok {
			var e chat.Event
			if err := json.Unmarshal([]byte(v), &e); err != nil {
				t.Fatalf("data %q: %v", v, err)
			}
			events = append(events, e)
			ids = append(ids, id)
			id = ""
		}
	}
	if len(events) < n {
		t.Fatalf("stream ended after %d events: %v", len(events), sc.Err())
	}
	return
}

func TestChatEventsReplayThenLive(t *testing.T) {
	f := newFixture(t, you)
	f.backend.chat.past = []chat.Event{{Seq: 1, Kind: chat.KindUser, Text: "hi"}, {Seq: 2, Kind: chat.KindTurn, Turn: &chat.Turn{State: chat.TurnWorking}}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, "GET", f.ts.URL+"/api/sessions/c1/events", nil)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("%d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	sc := bufio.NewScanner(resp.Body)
	ids, ev := readSSE(t, sc, 2)
	if !slices.Equal(ids, []string{"1", "2"}) || ev[0].Text != "hi" {
		t.Fatalf("replay ids %q events %+v", ids, ev)
	}
	// Live: a repeat of a replayed event is skipped; deltas carry no id.
	f.backend.chat.live <- chat.Event{Seq: 2, Kind: chat.KindTurn}
	f.backend.chat.live <- chat.Event{Kind: chat.KindDelta, ID: "m", Text: "He"}
	f.backend.chat.live <- chat.Event{Seq: 3, Kind: chat.KindText, ID: "m", Text: "Hello"}
	ids, ev = readSSE(t, sc, 2)
	if ev[0].Kind != chat.KindDelta || ids[0] != "" || ev[1].Seq != 3 || ids[1] != "3" {
		t.Errorf("live ids %q events %+v", ids, ev)
	}
}

func TestChatEventsResumeFromLastEventID(t *testing.T) {
	f := newFixture(t, you)
	f.backend.chat.past = []chat.Event{{Seq: 1, Kind: chat.KindUser}, {Seq: 2, Kind: chat.KindUser, Text: "second"}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, "GET", f.ts.URL+"/api/sessions/c1/events", nil)
	r.Header.Set("Last-Event-ID", "1")
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, ev := readSSE(t, bufio.NewScanner(resp.Body), 1)
	if ev[0].Text != "second" {
		t.Errorf("resumed with %+v", ev)
	}
	if code, _ := f.do(t, "GET", "/api/sessions/c1/events?after=x", "", nil); code != 400 {
		t.Errorf("bad after: %d", code)
	}
	if code, _ := f.do(t, "GET", "/api/sessions/nope/events", "", nil); code != 404 {
		t.Errorf("unknown session: %d", code)
	}
}

func TestInbox(t *testing.T) {
	f := newFixture(t, you)
	now := time.Now()
	f.backend.mu.Lock()
	f.backend.sessions = []store.Session{
		{ID: "c1", Project: "orbit", Harness: "claude", Mode: store.ModeChat, Status: store.StatusRunning, State: store.ChatWaiting, Created: now},
		{ID: "t1", Project: "orbit", Harness: "claude", Status: store.StatusRunning, Parent: "c1", Created: now},
		{ID: "s2", Project: "orbit", Harness: "shell", Status: store.StatusRunning, Created: now},
	}
	f.backend.mu.Unlock()
	code, body := f.do(t, "GET", "/api/inbox", "", nil)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var in struct {
		Projects []struct {
			Name string `json:"name"`
		} `json:"projects"`
		Sessions []struct {
			ID      string `json:"id"`
			Project string `json:"project"`
			Tint    int    `json:"tint"`
		} `json:"sessions"`
		Pending []json.RawMessage `json:"pending"`
	}
	json.Unmarshal([]byte(body), &in)
	ids := []string{}
	for _, s := range in.Sessions {
		ids = append(ids, s.ID)
	}
	if len(in.Projects) != 1 || !slices.Equal(ids, []string{"c1", "s2"}) || len(in.Pending) != 1 {
		t.Errorf("inbox = %s", body)
	}
}

func TestMetaListsChatModes(t *testing.T) {
	f := newFixture(t, you)
	_, body := f.do(t, "GET", "/api/meta", "", nil)
	if !strings.Contains(body, `"chat":{"claude":["ask","edits","plan","auto"]}`) {
		t.Errorf("meta = %s", body)
	}
}

func TestReviewRoute(t *testing.T) {
	f := newFixture(t, you)
	code, body := f.do(t, "GET", "/api/projects/orbit/review", "", nil)
	if code != 200 || !strings.Contains(body, `"branch":"feature/x"`) {
		t.Errorf("review: %d %s", code, body)
	}
	if code, _ := f.do(t, "GET", "/api/projects/ghost/review", "", nil); code != 404 {
		t.Errorf("unknown project: %d", code)
	}
}

type fakePush struct {
	subs []webpush.Subscription
	gone []string
}

func (p *fakePush) PublicKey() string { return "BPUB" }
func (p *fakePush) Subscribe(s webpush.Subscription) error {
	if !strings.HasPrefix(s.Endpoint, "https://web.push.apple.com/") {
		return errors.New("webpush: not a known push service")
	}
	p.subs = append(p.subs, s)
	return nil
}
func (p *fakePush) Unsubscribe(e string) error { p.gone = append(p.gone, e); return nil }

func TestPushRoutes(t *testing.T) {
	if code, _ := newFixture(t, you).do(t, "GET", "/api/push/key", "", nil); code != 404 {
		t.Errorf("push without a service: %d", code)
	}
	p := &fakePush{}
	f := newFixture(t, you, func(d *Deps) { d.Push = p })
	if code, body := f.do(t, "GET", "/api/push/key", "", nil); code != 200 || !strings.Contains(body, `"key":"BPUB"`) {
		t.Errorf("key: %d %s", code, body)
	}
	if code, _ := f.do(t, "POST", "/api/push/subscribe", `{"endpoint":"https://web.push.apple.com/x","keys":{"p256dh":"P","auth":"A"}}`, nil); code != 200 {
		t.Errorf("subscribe: %d", code)
	}
	if len(p.subs) != 1 || p.subs[0].P256dh != "P" || p.subs[0].Auth != "A" {
		t.Errorf("subs = %+v", p.subs)
	}
	if code, _ := f.do(t, "POST", "/api/push/subscribe", `{"endpoint":"https://evil.example/x","keys":{"p256dh":"P","auth":"A"}}`, nil); code != 400 {
		t.Errorf("bad endpoint: %d", code)
	}
	if code, _ := f.do(t, "DELETE", "/api/push/subscribe", `{"endpoint":"https://web.push.apple.com/x"}`, nil); code != 200 || len(p.gone) != 1 {
		t.Errorf("unsubscribe: %d %v", code, p.gone)
	}
}

func TestChatShownIsInert(t *testing.T) {
	f := newFixture(t, you)
	for name, want := range map[string]string{
		"0123456789abcdef0123456789abcdef.png": "image/png",
		"0123456789abcdef0123456789abcdef.txt": "text/plain; charset=utf-8",
	} {
		resp, err := http.Get(f.ts.URL + "/api/sessions/c1/shown/" + name)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		h := resp.Header
		if resp.StatusCode != 200 || h.Get("Content-Type") != want {
			t.Errorf("%s: %d %q", name, resp.StatusCode, h.Get("Content-Type"))
		}
		if csp := h.Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") || !strings.Contains(csp, "default-src 'none'") {
			t.Errorf("%s CSP = %q", name, csp)
		}
		if h.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s sniffable", name)
		}
	}
	for _, p := range []string{
		"/api/sessions/c1/shown/0123456789abcdef0123456789abcdef.html",
		"/api/sessions/c1/shown/ffffffffffffffffffffffffffffffff.png",
		"/api/sessions/c2/shown/0123456789abcdef0123456789abcdef.png",
		"/api/sessions/c1/shown/..%2Fc1.jsonl",
	} {
		if code, _ := f.do(t, "GET", p, "", nil); code != 404 {
			t.Errorf("%s: %d", p, code)
		}
	}
}

func TestEditSessionAPI(t *testing.T) {
	f := newFixture(t, you)
	code, body := f.do(t, "PATCH", "/api/sessions/s1", `{"title":"Login flow","color":"blue"}`, nil)
	if code != 200 || !strings.Contains(body, `"title":"Login flow"`) {
		t.Fatalf("edit: %d %s", code, body)
	}
	if code, _ := f.do(t, "PATCH", "/api/sessions/s1", `{"color":"chartreuse"}`, nil); code != http.StatusBadRequest {
		t.Fatalf("unknown colour: %d", code)
	}
	if code, _ := f.do(t, "PATCH", "/api/sessions/s1", `{"title":"x","status":"killed"}`, nil); code != http.StatusBadRequest {
		t.Fatalf("other fields: %d", code)
	}
}
