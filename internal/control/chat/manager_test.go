package chat

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zaneGittins/skybreaker/internal/control/store"
)

// fakeClaude is a harness process speaking enough of Claude Code's
// stream-json to exercise the manager. Its script decides what each user
// message gets back.
type fakeClaude struct {
	t       *testing.T
	stdinR  *io.PipeReader
	stdoutW *io.PipeWriter
	argv    []string
	env     map[string]string

	mu     sync.Mutex
	got    []map[string]any
	killed bool
	done   chan struct{}
}

func (f *fakeClaude) say(s string) { f.stdoutW.Write([]byte(s + "\n")) }

func (f *fakeClaude) run(script func(f *fakeClaude, m map[string]any)) {
	defer close(f.done)
	sc := bufio.NewScanner(f.stdinR)
	for sc.Scan() {
		var m map[string]any
		json.Unmarshal(sc.Bytes(), &m)
		f.mu.Lock()
		f.got = append(f.got, m)
		f.mu.Unlock()
		if m["type"] == "control_request" && m["request"].(map[string]any)["subtype"] == "initialize" {
			f.say(`{"type":"control_response","response":{"subtype":"success","request_id":"` + m["request_id"].(string) + `","response":{"models":[` +
				`{"value":"default","displayName":"Default"},{"value":"sonnet","displayName":"Sonnet"},{"value":"fable","displayName":"Fable"}]}}}`)
			continue
		}
		script(f, m)
	}
	f.stdoutW.Close()
}

func (f *fakeClaude) received() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.got)
}

// answer is the default script: every message gets "echo: <text>".
func answer(f *fakeClaude, m map[string]any) {
	if m["type"] != "user" {
		return
	}
	text := m["message"].(map[string]any)["content"].(string)
	f.say(`{"type":"system","subtype":"init","session_id":"conv-1","model":"claude-test","permissionMode":"default"}`)
	b, _ := json.Marshal("echo: " + text)
	f.say(`{"type":"assistant","message":{"id":"m-` + text + `","content":[{"type":"text","text":` + string(b) + `}]},"parent_tool_use_id":null}`)
	f.say(`{"type":"result","subtype":"success","is_error":false,"duration_ms":5,"session_id":"conv-1"}`)
}

type harness struct {
	t      *testing.T
	m      *Manager
	st     *store.Store
	mu     sync.Mutex
	procs  []*fakeClaude
	script func(f *fakeClaude, m map[string]any)
	starts int
	fail   error
	change []string
}

func newHarness(t *testing.T) *harness {
	h := &harness{t: t, script: answer}
	h.st, _ = store.Open("")
	log, _ := OpenLog(t.TempDir())
	h.m = NewManager(Config{
		Log: log, Sessions: h.st, Start: h.start,
		Options: func(s store.Session) Options {
			return Options{Cwd: "/workspace", MCP: "http://10.89.1.1:7701/mcp/orbit/tok/s1"}
		},
		OnChange: func(before, after store.Session) {
			h.mu.Lock()
			h.change = append(h.change, before.State+">"+after.State)
			h.mu.Unlock()
		},
	})
	t.Cleanup(h.m.Close)
	return h
}

func (h *harness) start(ctx context.Context, s store.Session, argv []string, env map[string]string) (*Proc, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.starts++
	if h.fail != nil {
		return nil, h.fail
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	f := &fakeClaude{t: h.t, stdinR: inR, stdoutW: outW, argv: argv, env: env, done: make(chan struct{})}
	h.procs = append(h.procs, f)
	go f.run(h.script)
	return &Proc{
		Stdin: inW, Stdout: outR,
		Wait:   func() error { return nil },
		Kill:   func() { f.mu.Lock(); f.killed = true; f.mu.Unlock(); inR.Close(); outW.Close() },
		Stderr: func() string { return "boom: the harness crashed" },
	}, nil
}

func (h *harness) session(harness string) store.Session {
	s := store.Session{ID: "s" + strings.Repeat("a", 4) + harness, Project: "p", Harness: harness, Mode: store.ModeChat, Status: store.StatusRunning, Created: time.Now(), State: store.ChatIdle}
	if err := h.st.CreateSession(s); err != nil {
		h.t.Fatal(err)
	}
	return s
}

// waitFor polls until cond holds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (h *harness) state(id string) store.Session {
	s, _ := h.st.Session(id)
	return s
}

func (h *harness) events(id string) []Event {
	ev, err := h.m.Events(id, 0)
	if err != nil {
		h.t.Fatal(err)
	}
	return ev
}

func TestManagerSendRunsATurn(t *testing.T) {
	h := newHarness(t)
	s := h.session("claude")
	sub, cancel := h.m.Subscribe(s.ID)
	defer cancel()
	if err := h.m.Send(context.Background(), s.ID, "hello there"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the turn to end", func() bool { return h.state(s.ID).State == store.ChatIdle && len(find(h.events(s.ID), KindTurn)) == 2 })
	got := h.state(s.ID)
	if got.Resume != "conv-1" || got.Model != "claude-test" || got.Title != "hello there" || got.Updated.IsZero() {
		t.Errorf("session = %+v", got)
	}
	ev := h.events(s.ID)
	// Two infos: the model list from initialize, then the init.
	if k := kinds(ev); !slices.Equal(k, []string{KindUser, KindTurn, KindInfo, KindText, KindTurn}) || len(find(ev, KindInfo)) != 2 {
		t.Errorf("kinds = %q", k)
	}
	if ev[0].Text != "hello there" || ev[4].Text != "echo: hello there" || ev[1].Turn.State != TurnWorking || ev[5].Turn.State != TurnDone {
		t.Errorf("events = %+v", ev)
	}
	for i, e := range ev {
		if e.Seq != int64(i+1) {
			t.Errorf("event %d has seq %d", i, e.Seq)
		}
	}
	// Subscribers saw the same, live.
	var live []Event
	for len(live) < len(ev) {
		select {
		case e := <-sub:
			live = append(live, e)
		case <-time.After(2 * time.Second):
			t.Fatalf("live got %d events", len(live))
		}
	}
	if live[3].Text != ev[3].Text {
		t.Errorf("live = %+v", live)
	}
	h.mu.Lock()
	f := h.procs[0]
	h.mu.Unlock()
	if !containsSeq(f.argv, []string{"--mcp-config", MCPConfig("http://10.89.1.1:7701/mcp/orbit/tok/s1")}) || !slices.Contains(f.argv, "--session-id") {
		t.Errorf("argv = %q", f.argv)
	}
	if f.env["SKYBREAKER_SESSION"] != s.ID || f.env["IS_SANDBOX"] != "1" {
		t.Errorf("env = %v", f.env)
	}
	h.mu.Lock()
	changes := slices.Clone(h.change)
	h.mu.Unlock()
	if !slices.Contains(changes, "idle>working") || !slices.Contains(changes, "working>idle") {
		t.Errorf("state changes = %q", changes)
	}

	// A second message reuses the process.
	h.m.Send(context.Background(), s.ID, "again")
	waitFor(t, "the second turn", func() bool { return len(find(h.events(s.ID), KindTurn)) == 4 })
	if h.starts != 1 {
		t.Errorf("started %d processes", h.starts)
	}
	if h.state(s.ID).Title != "hello there" {
		t.Error("the title changed after the first message")
	}
}

func TestManagerAskAndAnswer(t *testing.T) {
	h := newHarness(t)
	h.script = func(f *fakeClaude, m map[string]any) {
		switch m["type"] {
		case "user":
			f.say(`{"type":"control_request","request_id":"r1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"make"},"tool_use_id":"t1"}}`)
		case "control_response":
			f.say(`{"type":"result","subtype":"success","is_error":false,"duration_ms":5}`)
		}
	}
	s := h.session("claude")
	h.m.Send(context.Background(), s.ID, "build it")
	waitFor(t, "the ask", func() bool { return h.state(s.ID).State == store.ChatWaiting })
	if n := h.state(s.ID).Note; n != "Run make" {
		t.Errorf("note = %q", n)
	}
	if err := h.m.Answer(context.Background(), s.ID, "nope", Answer{Allow: true}); err == nil {
		t.Error("answered an unknown ask")
	}
	if err := h.m.Answer(context.Background(), s.ID, "r1", Answer{Allow: true}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the turn to end", func() bool { return h.state(s.ID).State == store.ChatIdle })
	ev := h.events(s.ID)
	answered := find(ev, KindAnswered)
	if len(answered) != 1 || answered[0].ID != "r1" || answered[0].Text != "allowed" {
		t.Errorf("answered = %+v", answered)
	}
	h.mu.Lock()
	got := h.procs[0].received()
	h.mu.Unlock()
	last := got[len(got)-1]
	if last["type"] != "control_response" {
		t.Errorf("harness got %v", last)
	}
}

func TestManagerProcessDiesMidTurn(t *testing.T) {
	h := newHarness(t)
	h.script = func(f *fakeClaude, m map[string]any) {
		if m["type"] == "user" {
			f.say(`{"type":"system","subtype":"init","session_id":"conv-9","model":"x"}`)
			f.say(`{"type":"control_request","request_id":"r1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"make"}}}`)
			f.stdoutW.Close()
		}
	}
	s := h.session("claude")
	h.m.Send(context.Background(), s.ID, "go")
	waitFor(t, "the failure", func() bool { return h.state(s.ID).State == store.ChatFailed })
	ev := h.events(s.ID)
	turns := find(ev, KindTurn)
	if last := turns[len(turns)-1].Turn; last.State != TurnFailed || !strings.Contains(last.Error, "boom") {
		t.Errorf("turn = %+v", last)
	}
	if a := find(ev, KindAnswered); len(a) != 1 || a[0].ID != "r1" {
		t.Errorf("the dead process's ask is still open: %+v", a)
	}
	if h.m.Running(s.ID) {
		t.Error("still running")
	}
	// The next message starts a new process that resumes the conversation.
	h.script = answer
	h.m.Send(context.Background(), s.ID, "retry")
	waitFor(t, "the retry", func() bool { return h.state(s.ID).State == store.ChatIdle })
	h.mu.Lock()
	argv := h.procs[1].argv
	h.mu.Unlock()
	if !containsSeq(argv, []string{"--resume", "conv-9"}) {
		t.Errorf("restart argv = %q", argv)
	}
}

func TestManagerStartFailure(t *testing.T) {
	h := newHarness(t)
	h.fail = errors.New("sandbox would not start")
	s := h.session("claude")
	// Sending returns at once; the failure shows in the conversation.
	if err := h.m.Send(context.Background(), s.ID, "hi"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the failure", func() bool { return h.state(s.ID).State == store.ChatFailed })
	turns := find(h.events(s.ID), KindTurn)
	if last := turns[len(turns)-1].Turn; !strings.Contains(last.Error, "would not start") {
		t.Errorf("turn = %+v", last)
	}
}

func TestManagerModesAndLogin(t *testing.T) {
	h := newHarness(t)
	s := h.session("claude")
	if err := h.m.SetMode(context.Background(), s.ID, ModeEdits); err != nil {
		t.Fatal(err)
	}
	if err := h.m.SetMode(context.Background(), s.ID, "everything"); err == nil {
		t.Error("bad mode accepted")
	}
	if pm := h.state(s.ID).PermMode; pm != ModeEdits {
		t.Errorf("perm mode = %q", pm)
	}
	h.m.Send(context.Background(), s.ID, "x")
	waitFor(t, "the turn", func() bool { return h.state(s.ID).State == store.ChatIdle && h.state(s.ID).Resume != "" })
	h.mu.Lock()
	argv := h.procs[0].argv
	h.mu.Unlock()
	if !containsSeq(argv, []string{"--permission-mode", "acceptEdits"}) {
		t.Errorf("argv = %q", argv)
	}
	// Running, a mode change goes to the harness at once.
	h.m.SetMode(context.Background(), s.ID, ModeAuto)
	waitFor(t, "the mode request", func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		for _, m := range h.procs[0].received() {
			if r, ok := m["request"].(map[string]any); ok && r["mode"] == "bypassPermissions" {
				return true
			}
		}
		return false
	})
	if err := h.m.Login(context.Background(), s.ID); !errors.Is(err, ErrTerminalLogin) {
		t.Errorf("claude login = %v", err)
	}
}

func TestManagerModel(t *testing.T) {
	h := newHarness(t)
	s := h.session("claude")
	if err := h.m.SetModel(context.Background(), s.ID, "a b"); err == nil {
		t.Error("bad model accepted")
	}
	// Before the harness runs its list isn't known; the choice goes on its
	// command line.
	if err := h.m.SetModel(context.Background(), s.ID, "sonnet"); err != nil {
		t.Fatal(err)
	}
	if c := h.state(s.ID).ChosenModel; c != "sonnet" {
		t.Errorf("chosen model = %q", c)
	}
	h.m.Send(context.Background(), s.ID, "x")
	waitFor(t, "the turn", func() bool { return h.state(s.ID).State == store.ChatIdle && h.state(s.ID).Resume != "" })
	h.mu.Lock()
	argv := h.procs[0].argv
	h.mu.Unlock()
	if !slices.Contains(argv, "--model=sonnet") {
		t.Errorf("argv = %q", argv)
	}
	if ms := models(h.events(s.ID)); len(ms) != 3 {
		t.Errorf("listed models = %+v", ms)
	}
	// Running, only a model the harness listed is taken, and at once.
	if err := h.m.SetModel(context.Background(), s.ID, "gpt-unlisted"); err == nil {
		t.Error("a model the harness didn't list was taken")
	}
	if err := h.m.SetModel(context.Background(), s.ID, "fable"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the model request", func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		for _, m := range h.procs[0].received() {
			if r, ok := m["request"].(map[string]any); ok && r["subtype"] == "set_model" && r["model"] == "fable" {
				return true
			}
		}
		return false
	})
	if c := h.state(s.ID).ChosenModel; c != "fable" {
		t.Errorf("chosen model = %q", c)
	}
}

func TestManagerInterruptAndStop(t *testing.T) {
	h := newHarness(t)
	h.script = func(f *fakeClaude, m map[string]any) {
		if r, ok := m["request"].(map[string]any); ok && r["subtype"] == "interrupt" {
			f.say(`{"type":"result","subtype":"error_during_execution","is_error":true,"terminal_reason":"aborted_streaming"}`)
		}
	}
	s := h.session("claude")
	h.m.Send(context.Background(), s.ID, "long job")
	waitFor(t, "working", func() bool { return h.state(s.ID).State == store.ChatWorking })
	if err := h.m.Interrupt(s.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the interrupt", func() bool { return h.state(s.ID).State == store.ChatIdle })
	turns := find(h.events(s.ID), KindTurn)
	if turns[len(turns)-1].Turn.State != TurnInterrupted {
		t.Errorf("turns = %+v", turns)
	}
	h.m.Stop(s.ID)
	if h.m.Running(s.ID) {
		t.Error("running after Stop")
	}
	// A deliberate stop isn't a failure.
	if st := h.state(s.ID).State; st != store.ChatIdle {
		t.Errorf("state after stop = %q", st)
	}
	if err := h.m.Interrupt(s.ID); err != nil {
		t.Errorf("interrupting a stopped session = %v", err)
	}
}

func TestManagerReapsIdleProcesses(t *testing.T) {
	h := newHarness(t)
	s := h.session("claude")
	h.m.Send(context.Background(), s.ID, "x")
	waitFor(t, "idle", func() bool { return h.state(s.ID).State == store.ChatIdle && h.m.Running(s.ID) })
	h.m.Reap(time.Now())
	if !h.m.Running(s.ID) {
		t.Fatal("reaped a process that was just used")
	}
	h.m.Reap(time.Now().Add(time.Hour))
	if h.m.Running(s.ID) {
		t.Error("idle process not reaped")
	}
}

func TestManagerRecoverAfterRestart(t *testing.T) {
	h := newHarness(t)
	s := h.session("claude")
	h.st.UpdateSession(s.ID, func(x *store.Session) { x.State = store.ChatWorking })
	h.m.Recover(h.st.ListSessions(""))
	got := h.state(s.ID)
	if got.State != store.ChatIdle {
		t.Errorf("state = %q", got.State)
	}
	ev := h.events(s.ID)
	if len(find(ev, KindNotice)) != 1 || find(ev, KindTurn)[0].Turn.State != TurnInterrupted {
		t.Errorf("events = %+v", ev)
	}
}

func TestManagerRefusesNonChat(t *testing.T) {
	h := newHarness(t)
	s := store.Session{ID: "sterm", Project: "p", Harness: "shell", Status: store.StatusRunning}
	h.st.CreateSession(s)
	if err := h.m.Send(context.Background(), s.ID, "hi"); err == nil {
		t.Error("sent to a terminal session")
	}
	if err := h.m.Send(context.Background(), "missing", "hi"); err == nil {
		t.Error("sent to a missing session")
	}
	ended := h.session("claude")
	h.st.UpdateSession(ended.ID, func(x *store.Session) { x.Status = store.StatusKilled })
	if err := h.m.Send(context.Background(), ended.ID, "hi"); err == nil {
		t.Error("sent to an ended session")
	}
}

// A subscriber that can't keep up is cut off rather than silently missing
// events; it reconnects and replays from its last seq.
func TestManagerSlowSubscriberIsClosed(t *testing.T) {
	h := newHarness(t)
	s := h.session("claude")
	ch, cancel := h.m.Subscribe(s.ID)
	defer cancel()
	for i := range subBuffer + 10 {
		h.m.Note(s.ID, "note "+string(rune('a'+i%26)))
	}
	n := 0
	for range ch {
		n++
	}
	if n > subBuffer {
		t.Errorf("got %d events past the buffer", n)
	}
	if ev := h.events(s.ID); len(ev) != subBuffer+10 {
		t.Errorf("log has %d events", len(ev))
	}
}

// echoDriver opens with several lines and turns each line back into a
// notice.
type echoDriver struct{ claude }

func (echoDriver) Open(Options) [][]byte {
	return [][]byte{[]byte(`{"n":1}`), []byte(`{"n":2}`), []byte(`{"n":3}`)}
}
func (echoDriver) Read(b []byte) Out {
	return Out{Events: []Event{{Kind: KindNotice, Text: string(b)}}, Write: [][]byte{[]byte(`{"ack":true}`)}}
}

// A harness that answers each line before reading the next, over pipes
// with no buffer, must not deadlock the manager.
func TestManagerHarnessAnswersBeforeReading(t *testing.T) {
	h := newHarness(t)
	h.m.c.Driver = func(store.Session) Driver { return &echoDriver{*newClaude()} }
	h.script = nil
	s := h.session("claude")
	h.m.c.Start = func(ctx context.Context, _ store.Session, argv []string, env map[string]string) (*Proc, error) {
		inR, inW := io.Pipe()
		outR, outW := io.Pipe()
		go func() {
			sc := bufio.NewScanner(inR)
			for sc.Scan() {
				if !strings.Contains(sc.Text(), "ack") {
					outW.Write(append(sc.Bytes(), '\n'))
				}
			}
			outW.Close()
		}()
		return &Proc{Stdin: inW, Stdout: outR, Kill: func() { inR.Close(); outW.Close() }}, nil
	}
	h.m.Send(context.Background(), s.ID, "go")
	waitFor(t, "every line echoed", func() bool { return len(find(h.events(s.ID), KindNotice)) >= 4 })
}

// What a harness reports using reaches OnUsage with the session's ID.
func TestManagerUsage(t *testing.T) {
	h := newHarness(t)
	type report struct {
		id     string
		u      *Usage
		limits []Limit
	}
	got := make(chan report, 4)
	h.m.c.OnUsage = func(id string, u *Usage, limits []Limit) { got <- report{id, u, limits} }
	h.script = func(f *fakeClaude, m map[string]any) {
		if m["type"] != "user" {
			return
		}
		f.say(`{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{"five_hour":{"utilization":0.25,"resetsAt":1792473000}}}}`)
		f.say(`{"type":"result","subtype":"success","duration_ms":5,"modelUsage":{"m":{"inputTokens":3,"outputTokens":4,"cacheReadInputTokens":5}}}`)
	}
	s := h.session("claude")
	if err := h.m.Send(context.Background(), s.ID, "hi"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"limits", "usage"} {
		select {
		case r := <-got:
			if r.id != s.ID {
				t.Errorf("id %q", r.id)
			}
			if want == "limits" && (len(r.limits) != 1 || r.limits[0].Used != 25) {
				t.Errorf("limits %+v", r.limits)
			}
			if want == "usage" && (r.u == nil || *r.u != (Usage{Input: 3, Cached: 5, Output: 4})) {
				t.Errorf("usage %+v", r.u)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("no %s", want)
		}
	}
}
