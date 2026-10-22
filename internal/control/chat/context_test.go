package chat

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/zaneGittins/skybreaker/internal/control/store"
)

// contexts lists what the events said about the context window, in order.
func contexts(events []Event) []Context {
	var out []Context
	for _, e := range events {
		if e.Kind == KindInfo && e.Info.Context != nil {
			out = append(out, *e.Info.Context)
		}
	}
	return out
}

func TestClaudeReplayCompact(t *testing.T) {
	// A real session: one short turn, then /compact.
	events, _, _ := replaySent(t, newClaude(), "claude-compact.jsonl", 2)
	cs := contexts(events)
	if len(cs) == 0 {
		t.Fatalf("no context usage in %+v", events)
	}
	// The first message's usage, all of its input and output: Claude's own
	// pre_tokens for the compaction agrees.
	if cs[0].Used != 24181 {
		t.Errorf("first usage = %+v", cs[0])
	}
	if !slices.ContainsFunc(cs, func(c Context) bool { return c.Used == 24181 && c.Window == 1000000 }) {
		t.Errorf("the turn's end didn't give the window: %+v", cs)
	}
	last := cs[len(cs)-1]
	if last.Used != 8033 || last.Window != 1000000 {
		t.Errorf("after compacting = %+v, want 8033 of 1000000", last)
	}
	notes := find(events, KindNotice)
	if len(notes) != 1 || !strings.Contains(notes[0].Text, "compacted") {
		t.Errorf("notices = %+v", notes)
	}
	turns := find(events, KindTurn)
	if len(turns) != 2 || turns[1].Turn.State != TurnDone {
		t.Errorf("turns = %+v", turns)
	}
	// The summary Claude carries on from isn't drawn as anything.
	for _, e := range events {
		if strings.Contains(e.Text, "being continued") {
			t.Errorf("summary drawn: %+v", e)
		}
	}
}

func TestClaudeCompactSendsTheCommand(t *testing.T) {
	d := newClaude()
	lines, err := d.Compact()
	if err != nil || len(lines) != 1 {
		t.Fatalf("compact = %q, %v", lines, err)
	}
	m := decodeLine(t, lines[0])
	if m["type"] != "user" || m["message"].(map[string]any)["content"] != "/compact" {
		t.Errorf("compact wrote %s", lines[0])
	}
}

func TestClaudeArgvFork(t *testing.T) {
	d := newClaude()
	got := d.Argv(Options{Resume: "abc", Fork: true, NewID: "ignored"})
	if !containsSeq(got, []string{"--resume", "abc", "--fork-session"}) || slices.Contains(got, "--session-id") {
		t.Errorf("fork argv = %q", got)
	}
	if got := d.Argv(Options{Resume: "abc"}); slices.Contains(got, "--fork-session") {
		t.Errorf("a plain resume forked: %q", got)
	}
	// Nothing to fork from: a fresh conversation.
	if got := d.Argv(Options{Fork: true, NewID: "n"}); slices.Contains(got, "--fork-session") || slices.Contains(got, "--resume") {
		t.Errorf("fork of nothing = %q", got)
	}
}

func TestClaudeReplayFork(t *testing.T) {
	// A real fork: resumed with --fork-session, it reports its own ID.
	events, _, resume := replay(t, newClaude(), "claude-fork.jsonl")
	if resume != "7a1b42f6-582a-497a-be70-cd595a321f17" {
		t.Errorf("resume = %q", resume)
	}
	cs := contexts(events)
	if len(cs) == 0 || cs[len(cs)-1].Window != 1000000 || cs[len(cs)-1].Used == 0 {
		t.Errorf("contexts = %+v", cs)
	}
}

func TestClaudeSubagentUsageIgnored(t *testing.T) {
	d := newClaude()
	out := d.Read([]byte(`{"type":"assistant","message":{"id":"m1","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":5,"cache_read_input_tokens":100,"output_tokens":1}},"parent_tool_use_id":"tool-1"}`))
	if cs := contexts(out.Events); len(cs) != 0 {
		t.Errorf("a subagent's usage counted: %+v", cs)
	}
	out = d.Read([]byte(`{"type":"assistant","message":{"id":"m2","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":5,"cache_read_input_tokens":100,"output_tokens":1}},"parent_tool_use_id":null}`))
	if cs := contexts(out.Events); len(cs) != 1 || cs[0].Used != 106 {
		t.Errorf("contexts = %+v", cs)
	}
	// The same message again (one line per block) says nothing new.
	out = d.Read([]byte(`{"type":"assistant","message":{"id":"m2","content":[{"type":"text","text":"more"}],"usage":{"input_tokens":5,"cache_read_input_tokens":100,"output_tokens":1}},"parent_tool_use_id":null}`))
	if cs := contexts(out.Events); len(cs) != 0 {
		t.Errorf("repeated usage = %+v", cs)
	}
}

func TestCodexTokenUsage(t *testing.T) {
	d, _ := handshake(t, Options{Cwd: "/workspace"})
	out := d.Read([]byte(`{"method":"thread/tokenUsage/updated","params":{"threadId":"thr-1","turnId":"turn-1","tokenUsage":{"total":{"totalTokens":90000,"inputTokens":80000,"cachedInputTokens":70000,"outputTokens":10000,"reasoningOutputTokens":0},"last":{"totalTokens":31000,"inputTokens":30000,"cachedInputTokens":20000,"outputTokens":1000,"reasoningOutputTokens":200},"modelContextWindow":258400}}}`))
	cs := contexts(out.Events)
	if len(cs) != 1 || cs[0].Used != 31000 || cs[0].Window != 258400 {
		t.Errorf("contexts = %+v", cs)
	}
	// Another thread's usage isn't this chat's.
	out = d.Read([]byte(`{"method":"thread/tokenUsage/updated","params":{"threadId":"thr-2","turnId":"t","tokenUsage":{"total":{"totalTokens":1},"last":{"totalTokens":1},"modelContextWindow":1}}}`))
	if len(out.Events) != 0 {
		t.Errorf("foreign usage = %+v", out.Events)
	}
}

func TestCodexCompact(t *testing.T) {
	d, _ := handshake(t, Options{Cwd: "/workspace"})
	lines, err := d.Compact()
	if err != nil || len(lines) != 1 {
		t.Fatalf("compact = %q, %v", lines, err)
	}
	m := decodeLine(t, lines[0])
	if m["method"] != "thread/compact/start" || m["params"].(map[string]any)["threadId"] != "thr-1" {
		t.Errorf("compact wrote %s", lines[0])
	}
	// A refusal ends the turn the manager started for it.
	out := d.Read(line(map[string]any{"id": m["id"], "error": map[string]any{"code": -32600, "message": "nothing to compact"}}))
	if turns := find(out.Events, KindTurn); len(turns) != 1 || turns[0].Turn.State != TurnFailed || turns[0].Turn.Error != "nothing to compact" {
		t.Errorf("refused compact = %+v", out.Events)
	}
}

func TestCodexCompactWaitsForThread(t *testing.T) {
	d := newCodex()
	d.Open(Options{Cwd: "/workspace", Resume: "thr-0"})
	if lines, err := d.Compact(); err != nil || len(lines) != 0 {
		t.Fatalf("compact before the thread = %q, %v", lines, err)
	}
	out := d.Read([]byte(`{"id":1,"result":{}}`))
	var resumeID any
	for _, w := range out.Write {
		if m := decodeLine(t, w); m["method"] == "thread/resume" {
			resumeID = m["id"]
		}
	}
	out = d.Read(line(map[string]any{"id": resumeID, "result": map[string]any{"thread": map[string]any{"id": "thr-0"}, "model": "m"}}))
	if len(out.Write) != 1 || decodeLine(t, out.Write[0])["method"] != "thread/compact/start" {
		t.Fatalf("after the thread wrote %q", out.Write)
	}
}

func TestCodexFork(t *testing.T) {
	d, _ := handshake(t, Options{Cwd: "/workspace", Resume: "thr-0", Fork: true})
	if d.thread != "thr-1" {
		t.Errorf("thread = %q", d.thread)
	}
	// A fork that fails starts afresh, and says so.
	d = newCodex()
	d.Open(Options{Cwd: "/workspace", Resume: "gone", Fork: true})
	out := d.Read([]byte(`{"id":1,"result":{}}`))
	var forkID any
	for _, w := range out.Write {
		if m := decodeLine(t, w); m["method"] == "thread/fork" {
			forkID = m["id"]
			if m["params"].(map[string]any)["threadId"] != "gone" {
				t.Errorf("fork params = %v", m["params"])
			}
		}
	}
	out = d.Read(line(map[string]any{"id": forkID, "error": map[string]any{"code": -32600, "message": "no rollout found"}}))
	if len(out.Write) != 1 || decodeLine(t, out.Write[0])["method"] != "thread/start" {
		t.Fatalf("after a failed fork wrote %q", out.Write)
	}
	if n := find(out.Events, KindNotice); len(n) != 1 || !strings.Contains(n[0].Text, "fork") {
		t.Errorf("notice = %+v", out.Events)
	}
}

func TestManagerCompact(t *testing.T) {
	h := newHarness(t)
	h.script = func(f *fakeClaude, m map[string]any) {
		if m["type"] != "user" {
			return
		}
		if m["message"].(map[string]any)["content"] == "/compact" {
			f.say(`{"type":"system","subtype":"compact_boundary","session_id":"conv-1","compact_metadata":{"trigger":"manual","pre_tokens":500,"post_tokens":40}}`)
			f.say(`{"type":"result","subtype":"success","is_error":false,"duration_ms":5,"result":"","session_id":"conv-1"}`)
			return
		}
		answer(f, m)
	}
	s := h.session("claude")
	ctx := context.Background()
	if err := h.m.Compact(ctx, s.ID); err == nil {
		t.Error("compacted a conversation that hasn't started")
	}
	h.m.Send(ctx, s.ID, "hello")
	waitFor(t, "the first turn", func() bool { return len(find(h.events(s.ID), KindTurn)) == 2 })
	if err := h.m.Compact(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the compaction", func() bool { return len(find(h.events(s.ID), KindTurn)) == 4 })
	ev := h.events(s.ID)
	if n := len(find(ev, KindUser)); n != 1 {
		t.Errorf("compacting drew %d user messages", n)
	}
	if cs := contexts(ev); len(cs) == 0 || cs[len(cs)-1].Used != 40 {
		t.Errorf("contexts = %+v", cs)
	}
	if h.state(s.ID).State != store.ChatIdle {
		t.Errorf("state = %q", h.state(s.ID).State)
	}
}

func TestManagerCompactRefusedWhileWorking(t *testing.T) {
	h := newHarness(t)
	h.script = func(f *fakeClaude, m map[string]any) {} // never answers
	s := h.session("claude")
	h.st.UpdateSession(s.ID, func(x *store.Session) { x.Resume = "conv-1" })
	h.m.Send(context.Background(), s.ID, "long job")
	if err := h.m.Compact(context.Background(), s.ID); err == nil {
		t.Error("compacted mid-turn")
	}
}

func TestManagerStartsAFork(t *testing.T) {
	h := newHarness(t)
	s := h.session("claude")
	h.st.UpdateSession(s.ID, func(x *store.Session) { x.Resume = "parent-conv"; x.Fork = true })
	h.m.Send(context.Background(), s.ID, "hello")
	waitFor(t, "the turn", func() bool { return len(find(h.events(s.ID), KindTurn)) == 2 })
	h.mu.Lock()
	argv := h.procs[0].argv
	h.mu.Unlock()
	if !containsSeq(argv, []string{"--resume", "parent-conv", "--fork-session"}) {
		t.Errorf("argv = %q", argv)
	}
	// Once the fork has its own conversation, later starts resume that.
	got := h.state(s.ID)
	if got.Fork || got.Resume != "conv-1" {
		t.Errorf("after the first start: fork %v, resume %q", got.Fork, got.Resume)
	}
}

func TestLogCopy(t *testing.T) {
	l, _ := OpenLog(t.TempDir())
	att, _ := l.Keep("from", []byte("a log"), ".bin")
	l.Append("from", Event{Kind: KindUser, Text: "hi", Attach: []Attachment{{Name: "x.log", Kind: AttachFile, Kept: att}}})
	name, err := l.Keep("from", []byte("png bytes"), ".png")
	if err != nil {
		t.Fatal(err)
	}
	l.Append("from", Event{Kind: KindShow, ID: name, Show: &Show{Path: "/workspace/a.png", Kind: ShowImage}})
	l.Append("from", Event{Kind: KindText, Text: "done"})
	if err := l.Copy("from", "to"); err != nil {
		t.Fatal(err)
	}
	got, _ := l.Since("to", 0)
	if len(got) != 3 || got[0].Seq != 1 || got[2].Text != "done" || got[1].ID != name {
		t.Fatalf("copied = %+v", got)
	}
	if b, err := l.Kept("to", name); err != nil || string(b) != "png bytes" {
		t.Errorf("the shown file didn't come along: %q, %v", b, err)
	}
	if b, err := l.Kept("to", att); err != nil || string(b) != "a log" {
		t.Errorf("the attachment didn't come along: %q, %v", b, err)
	}
	// The copy is its own: appending to one doesn't touch the other.
	l.Append("to", Event{Kind: KindText, Text: "fork only"})
	if from, _ := l.Since("from", 0); len(from) != 3 {
		t.Errorf("source = %+v", from)
	}
	if err := l.Copy("from", "../x"); err == nil {
		t.Error("copied to a bad id")
	}
}
