package chat

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zaneGittins/skybreaker/internal/control/store"
)

// lastTool is each tool card as the events last left it, and the index of
// that event.
func lastTool(events []Event) (map[string]Tool, map[string]int) {
	tools, at := map[string]Tool{}, map[string]int{}
	for i, e := range events {
		if e.Kind == KindTool {
			tools[e.ID], at[e.ID] = *e.Tool, i
		}
	}
	return tools, at
}

// toolAt is a card as it stood just before event i.
func toolAt(events []Event, id string, i int) (Tool, bool) {
	tools, _ := lastTool(events[:i])
	t, ok := tools[id]
	return t, ok
}

func turnIndex(events []Event, state string, nth int) int {
	for i, e := range events {
		if e.Kind == KindTurn && e.Turn.State == state {
			if nth == 0 {
				return i
			}
			nth--
		}
	}
	return -1
}

// claude-subagents.jsonl: Claude Code 2.1.304 runs a subagent in the
// foreground, then two in the background that outlive the turn. Each
// reports back by itself, and Claude takes a turn of its own for each.
func TestClaudeReplaySubagents(t *testing.T) {
	events, _, _ := replaySent(t, newClaude(), "claude-subagents.jsonl", 1)
	const fg, napA, napB = "toolu_01EurKQ6CJyvsXVNRReZj8JW", "toolu_01PucABuu1FEngkfykjo7mZW", "toolu_01WZ4br8kHJpYDyRFhSEHqd6"
	tools, _ := lastTool(events)
	for id, out := range map[string]string{fg: "hi", napA: "a", napB: "b"} {
		tl := tools[id]
		if tl.Kind != ToolAgent || tl.Status != StatusDone || tl.Output != out {
			t.Errorf("agent %s = %+v, want done with %q", id, tl, out)
		}
	}
	// The turn that launched them ends with both still running.
	end := turnIndex(events, TurnDone, 0)
	for _, id := range []string{napA, napB} {
		if tl, _ := toolAt(events, id, end); tl.Status != StatusRunning {
			t.Errorf("at the first turn's end %s = %s, want running", id, tl.Status)
		}
	}
	// Nap A reports back, and Claude takes a turn nobody sent.
	again := turnIndex(events, TurnWorking, 0)
	if again < end {
		t.Fatalf("no turn after the subagent reported back: %v", kinds(events))
	}
	if tl, _ := toolAt(events, napA, again); tl.Status != StatusDone {
		t.Errorf("nap A before Claude's own turn = %+v", tl)
	}
	// That turn ends with Nap B still going; it reports back later.
	second := turnIndex(events, TurnDone, 1)
	if tl, _ := toolAt(events, napB, second); tl.Status != StatusRunning {
		t.Errorf("nap B at the second turn's end = %s, want running", tl.Status)
	}
	var states []string
	for _, e := range find(events, KindTurn) {
		states = append(states, e.Turn.State)
	}
	if want := []string{TurnDone, TurnWorking, TurnDone, TurnWorking, TurnDone}; !slices.Equal(states, want) {
		t.Errorf("turns = %v, want %v", states, want)
	}
	// Their steps nest in them and finish, though they ran past the turn.
	for id, tl := range tools {
		if tl.Kind == ToolCommand && (tl.Status != StatusDone || (tl.Parent != napA && tl.Parent != napB)) {
			t.Errorf("step %s = %+v", id, tl)
		}
	}
	for _, e := range find(events, KindText) {
		if e.Text == "a" || e.Text == "b" || e.Text == "hi" {
			t.Errorf("a subagent's reply surfaced as the agent's: %q", e.Text)
		}
	}
}

func TestClaudeSubagentEnds(t *testing.T) {
	launch := func(d *claude, id, task string) {
		d.Send("go")
		d.Read([]byte(`{"type":"assistant","message":{"id":"m` + id + `","content":[{"type":"tool_use","id":"` + id + `","name":"Agent","input":{"description":"Look around","run_in_background":true}}]},"parent_tool_use_id":null}`))
		d.Read([]byte(`{"type":"system","subtype":"task_started","task_id":"` + task + `","tool_use_id":"` + id + `","is_backgrounded":true,"task_type":"local_agent"}`))
		d.Read([]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"` + id + `","content":"Async agent launched successfully."}]},"tool_use_result":{"isAsync":true,"status":"async_launched"}}`))
		d.Read([]byte(`{"type":"result","subtype":"success","is_error":false}`))
	}
	d := newClaude()
	launch(d, "t1", "k1")
	out := d.Read([]byte(`{"type":"system","subtype":"task_notification","task_id":"k1","tool_use_id":"t1","status":"failed","summary":"ran out of road"}`))
	if tl := find(out.Events, KindTool); len(tl) != 1 || tl[0].Tool.Status != StatusFailed || tl[0].Tool.Output != "ran out of road" {
		t.Errorf("failed = %+v", out.Events)
	}
	launch(d, "t2", "k2")
	if out := d.Read([]byte(`{"type":"system","subtype":"task_notification","task_id":"k2","status":"stopped"}`)); len(out.Events) != 1 || out.Events[0].ID != "t2" || out.Events[0].Tool.Status != StatusDeclined {
		t.Errorf("stopped, by task ID = %+v", out.Events)
	}
	// A notification that never comes: the background list says it's gone.
	launch(d, "t3", "k3")
	if out := d.Read([]byte(`{"type":"system","subtype":"background_tasks_changed","tasks":[{"task_id":"k3"}]}`)); len(out.Events) != 0 {
		t.Errorf("still listed, but ended: %+v", out.Events)
	}
	if out := d.Read([]byte(`{"type":"system","subtype":"background_tasks_changed","tasks":[]}`)); len(out.Events) != 1 || out.Events[0].Tool.Status != StatusDone {
		t.Errorf("gone from the list = %+v", out.Events)
	}
	if len(d.agents) != 0 || len(d.tasks) != 0 {
		t.Errorf("left over: agents %v tasks %v", d.agents, d.tasks)
	}
}

// codex-subagent.jsonl: codex app-server 0.163 spawns a subagent and ends
// its turn; the subagent runs on in a thread of its own.
func TestCodexReplaySubagent(t *testing.T) {
	d := newCodex()
	d.Open(Options{Cwd: "/workspace"})
	events, _, _ := replaySent(t, d, "codex-subagent.jsonl", 1)
	const card = "call_84e063f344b4486b8cb6519a65049885"
	tools, _ := lastTool(events)
	if tl := tools[card]; tl.Kind != ToolAgent || tl.Status != StatusDone || tl.Title != "sleep command" || tl.Output != "done" {
		t.Errorf("agent = %+v", tl)
	}
	end := turnIndex(events, TurnDone, 0)
	if tl, _ := toolAt(events, card, end); tl.Status != StatusRunning {
		t.Errorf("at the turn's end the agent = %s, want running", tl.Status)
	}
	if n := len(find(events, KindTurn)); n != 1 {
		t.Errorf("the subagent's turn was taken for the conversation's: %d turn events", n)
	}
	if tl := tools["exec-a821f17c-5236-4d86-8506-a36f17c5e2fc"]; tl.Parent != card || tl.Status != StatusDone || tl.Kind != ToolCommand {
		t.Errorf("the subagent's command = %+v", tl)
	}
	if len(tools) != 2 {
		t.Errorf("cards = %v", tools)
	}
	for _, e := range find(events, KindText) {
		if e.Text == "done" {
			t.Error("the subagent's reply surfaced as the agent's")
		}
	}
}

// codex-subagents-wait.jsonl: codex spawns two subagents and waits for
// them, in the same turn. The recording stops with the second still going.
func TestCodexReplaySubagentsWait(t *testing.T) {
	d := newCodex()
	d.Open(Options{Cwd: "/workspace"})
	events, _, _ := replaySent(t, d, "codex-subagents-wait.jsonl", 1)
	tools, _ := lastTool(events)
	var agents []Tool
	for _, tl := range tools {
		if tl.Kind == ToolAgent {
			agents = append(agents, tl)
		}
		if tl.Name == "collabAgentToolCall" {
			t.Errorf("a wait drew a card: %+v", tl)
		}
	}
	slices.SortFunc(agents, func(a, b Tool) int { return strings.Compare(a.Title, b.Title) })
	if len(agents) != 2 || agents[0].Title != "one" || agents[0].Status != StatusDone || agents[0].Output != "one" ||
		agents[1].Title != "two" || agents[1].Status != StatusRunning {
		t.Errorf("agents = %+v", agents)
	}
	if n := len(find(events, KindTurn)); n != 0 {
		t.Errorf("turn events = %d; the parent is still waiting", n)
	}
	// A message now steers the parent's turn, not a subagent's.
	lines, _ := d.Send("and then?")
	var m struct {
		Method string `json:"method"`
		Params struct {
			Turn string `json:"expectedTurnId"`
		} `json:"params"`
	}
	json.Unmarshal(lines[0], &m)
	if m.Method != "turn/steer" || m.Params.Turn != "01a15a0b-d2c4-7a12-80f0-0940573a51f2" {
		t.Errorf("steer = %s", lines[0])
	}
}

// bgAgent is a script whose turn launches a background subagent and ends.
func bgAgent(f *fakeClaude, m map[string]any) {
	if m["type"] != "user" {
		return
	}
	f.say(`{"type":"system","subtype":"init","session_id":"conv-1","model":"claude-test"}`)
	f.say(`{"type":"assistant","message":{"id":"m1","content":[{"type":"tool_use","id":"ag1","name":"Agent","input":{"description":"Dig in","run_in_background":true}}]},"parent_tool_use_id":null}`)
	f.say(`{"type":"system","subtype":"task_started","task_id":"k1","tool_use_id":"ag1","is_backgrounded":true,"task_type":"local_agent"}`)
	f.say(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"ag1","content":"Async agent launched successfully."}]},"tool_use_result":{"isAsync":true,"status":"async_launched"}}`)
	f.say(`{"type":"result","subtype":"success","is_error":false,"duration_ms":5,"session_id":"conv-1"}`)
}

func TestManagerWorkingWhileSubagentsRun(t *testing.T) {
	h := newHarness(t)
	h.script = bgAgent
	s := h.session("claude")
	h.m.Send(context.Background(), s.ID, "go")
	// The log has the turn's end a moment before the state follows it.
	waitFor(t, "the turn's end", func() bool { return len(find(h.events(s.ID), KindTurn)) == 2 })
	waitFor(t, "working on the subagent", func() bool { return h.state(s.ID).Note == "1 agent working" })
	if got := h.state(s.ID); got.State != store.ChatWorking {
		t.Errorf("with the turn over and its subagent running: %q", got.State)
	}
	h.m.Reap(time.Now().Add(time.Hour))
	if !h.m.Running(s.ID) {
		t.Fatal("reaped a harness whose subagent runs")
	}
	h.mu.Lock()
	f := h.procs[0]
	h.mu.Unlock()
	f.say(`{"type":"system","subtype":"task_notification","task_id":"k1","tool_use_id":"ag1","status":"completed","summary":"found it"}`)
	waitFor(t, "idle", func() bool { return h.state(s.ID).State == store.ChatIdle })
	h.mu.Lock()
	change := slices.Clone(h.change)
	h.mu.Unlock()
	// Finished once: when the subagent did, not when the turn did.
	if want := []string{"idle>working", "working>working", "working>idle"}; !slices.Equal(change, want) {
		t.Errorf("changes = %v, want %v", change, want)
	}
}

func TestManagerSubagentsEndWithTheHarness(t *testing.T) {
	h := newHarness(t)
	h.script = bgAgent
	s := h.session("claude")
	h.m.Send(context.Background(), s.ID, "go")
	waitFor(t, "the turn's end", func() bool { return len(find(h.events(s.ID), KindTurn)) == 2 })
	h.mu.Lock()
	f := h.procs[0]
	h.mu.Unlock()
	f.stdoutW.Close()
	// The work died, though no turn was running: that's a failure, not
	// "finished", and the harness's last words say why.
	waitFor(t, "the failure", func() bool { return h.state(s.ID).State == store.ChatFailed })
	ev := h.events(s.ID)
	if tools, _ := lastTool(ev); tools["ag1"].Status != StatusFailed {
		t.Errorf("agent = %+v", tools["ag1"])
	}
	if turns := find(ev, KindTurn); turns[len(turns)-1].Turn.State != TurnFailed || !strings.Contains(turns[len(turns)-1].Turn.Error, "boom") {
		t.Errorf("turns = %+v", turns)
	}
	h.mu.Lock()
	change := slices.Clone(h.change)
	h.mu.Unlock()
	if slices.Contains(change, "working>idle") {
		t.Errorf("told finished when it died: %v", change)
	}
}

// Stopping the chat on purpose while only subagents run stops them; it
// isn't a failure.
func TestManagerStopWithSubagents(t *testing.T) {
	h := newHarness(t)
	h.script = bgAgent
	s := h.session("claude")
	h.m.Send(context.Background(), s.ID, "go")
	waitFor(t, "the turn's end", func() bool { return len(find(h.events(s.ID), KindTurn)) == 2 })
	h.m.Stop(s.ID)
	if got := h.state(s.ID); got.State != store.ChatIdle {
		t.Errorf("state = %q", got.State)
	}
	if tools, _ := lastTool(h.events(s.ID)); tools["ag1"].Status != StatusDeclined {
		t.Errorf("agent = %+v", tools["ag1"])
	}
}

// A subagent can ask between turns. Answering it goes back to working,
// and a failed turn with subagents still at work keeps the chat working,
// so nothing stops their sandbox.
func TestManagerSubagentAsksAndFailedTurns(t *testing.T) {
	h := newHarness(t)
	h.script = bgAgent
	s := h.session("claude")
	h.m.Send(context.Background(), s.ID, "go")
	waitFor(t, "the turn's end", func() bool { return len(find(h.events(s.ID), KindTurn)) == 2 })
	h.mu.Lock()
	f := h.procs[0]
	h.mu.Unlock()
	f.say(`{"type":"assistant","message":{"id":"sm1","content":[{"type":"tool_use","id":"st1","name":"Bash","input":{"command":"make"}}]},"parent_tool_use_id":"ag1"}`)
	f.say(`{"type":"control_request","request_id":"r9","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"make"},"tool_use_id":"st1"}}`)
	waitFor(t, "waiting", func() bool { return h.state(s.ID).State == store.ChatWaiting })
	if err := h.m.Answer(context.Background(), s.ID, "r9", Answer{Allow: true}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "working again", func() bool { s := h.state(s.ID); return s.State == store.ChatWorking && s.Note == "1 agent working" })
	// Claude takes a turn of its own, which fails; the subagent runs on.
	f.say(`{"type":"system","subtype":"init","session_id":"conv-1"}`)
	f.say(`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"API Error: 529 Overloaded"}`)
	waitFor(t, "the failure told", func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return slices.Contains(h.change, "working>failed")
	})
	waitFor(t, "working", func() bool { return h.state(s.ID).State == store.ChatWorking })
}

func TestManagerRecoverEndsSubagents(t *testing.T) {
	h := newHarness(t)
	s := h.session("claude")
	h.m.c.Log.Append(s.ID, Event{Time: time.Now(), Kind: KindTool, ID: "ag1", Tool: &Tool{Name: "Agent", Kind: ToolAgent, Status: StatusRunning}})
	h.st.UpdateSession(s.ID, func(x *store.Session) { x.State, x.Note = store.ChatWorking, "1 agent working" })
	h.m.Recover(h.st.ListSessions(""))
	if got := h.state(s.ID); got.State != store.ChatIdle {
		t.Errorf("state = %q", got.State)
	}
	if tools, _ := lastTool(h.events(s.ID)); tools["ag1"].Status != StatusDeclined {
		t.Errorf("agent = %+v", tools["ag1"])
	}
}

// A background subagent's ask outlives the turn's end; when the subagent
// ends, its ask and its open steps end with it.
func TestClaudeSubagentAsks(t *testing.T) {
	d := newClaude()
	d.Send("go")
	d.Read([]byte(`{"type":"assistant","message":{"id":"m1","content":[{"type":"tool_use","id":"ag","name":"Agent","input":{"description":"Dig","run_in_background":true}}]},"parent_tool_use_id":null}`))
	d.Read([]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"ag","content":"Async agent launched successfully."}]},"tool_use_result":{"isAsync":true,"status":"async_launched"}}`))
	d.Read([]byte(`{"type":"assistant","message":{"id":"m2","content":[{"type":"tool_use","id":"st","name":"Bash","input":{"command":"make"}}]},"parent_tool_use_id":"ag"}`))
	d.Read([]byte(`{"type":"control_request","request_id":"r1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"make"},"tool_use_id":"st"}}`))
	out := d.Read([]byte(`{"type":"result","subtype":"success","is_error":false}`))
	if len(find(out.Events, KindAnswered)) != 0 || len(d.asks) != 1 {
		t.Fatalf("the turn's end withdrew the subagent's ask: %+v", out.Events)
	}
	out = d.Read([]byte(`{"type":"system","subtype":"task_notification","tool_use_id":"ag","status":"stopped"}`))
	if a := find(out.Events, KindAnswered); len(a) != 1 || a[0].ID != "r1" {
		t.Errorf("answered = %+v", a)
	}
	tools, _ := lastTool(out.Events)
	if tools["st"].Status != StatusDeclined || tools["ag"].Status != StatusDeclined {
		t.Errorf("cards = %+v", tools)
	}
	// The list ended it first; the notification that comes late still says how.
	d.Send("again")
	d.Read([]byte(`{"type":"assistant","message":{"id":"m3","content":[{"type":"tool_use","id":"ag2","name":"Agent","input":{"description":"Dig"}}]},"parent_tool_use_id":null}`))
	d.Read([]byte(`{"type":"system","subtype":"task_started","task_id":"k2","tool_use_id":"ag2","task_type":"local_agent"}`))
	d.Read([]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"ag2","content":"launched"}]},"tool_use_result":{"isAsync":true}}`))
	d.Read([]byte(`{"type":"system","subtype":"background_tasks_changed","tasks":[]}`))
	out = d.Read([]byte(`{"type":"system","subtype":"task_notification","task_id":"k2","tool_use_id":"ag2","status":"failed","summary":"no luck"}`))
	if tl := find(out.Events, KindTool); len(tl) != 1 || tl[0].Tool.Status != StatusFailed || tl[0].Tool.Output != "no luck" {
		t.Errorf("late notification = %+v", out.Events)
	}
}

// A subagent's approval is its thread's: the conversation's turn ending
// leaves it open, and the subagent's ending withdraws it. Activity codex
// reports that isn't a start or an end changes nothing.
func TestCodexSubagentAsks(t *testing.T) {
	d := newCodex()
	d.Open(Options{})
	d.thread, d.turn = "root", "t1"
	d.Read([]byte(`{"method":"item/started","params":{"threadId":"root","item":{"type":"subAgentActivity","id":"card","kind":"started","agentThreadId":"kid","agentPath":"/root/dig"}}}`))
	d.Read([]byte(`{"method":"turn/started","params":{"threadId":"kid","turn":{"id":"kt"}}}`))
	d.Read([]byte(`{"method":"item/started","params":{"threadId":"kid","item":{"type":"userMessage","id":"um"}}}`))
	d.Read([]byte(`{"method":"item/started","params":{"threadId":"kid","item":{"type":"commandExecution","id":"cmd","command":"make","status":"inProgress"}}}`))
	d.Read([]byte(`{"id":7,"method":"item/commandExecution/requestApproval","params":{"threadId":"kid","itemId":"cmd","command":"make"}}`))
	if _, ok := d.items["um"]; ok {
		t.Error("a subagent's message left a card behind")
	}
	out := d.Read([]byte(`{"method":"turn/completed","params":{"threadId":"root","turn":{"status":"completed"}}}`))
	if len(find(out.Events, KindAnswered)) != 0 {
		t.Errorf("the conversation's turn withdrew the subagent's ask: %+v", out.Events)
	}
	if out := d.Read([]byte(`{"method":"item/started","params":{"threadId":"root","item":{"type":"subAgentActivity","id":"x","kind":"interacted","agentThreadId":"kid"}}}`)); len(out.Events) != 0 {
		t.Errorf("other activity = %+v", out.Events)
	}
	out = d.Read([]byte(`{"method":"turn/completed","params":{"threadId":"kid","turn":{"status":"interrupted"}}}`))
	if a := find(out.Events, KindAnswered); len(a) != 1 || a[0].ID != "c7" {
		t.Errorf("answered = %+v", out.Events)
	}
	tools, _ := lastTool(out.Events)
	if tools["cmd"].Status != StatusDeclined || tools["cmd"].Parent != "card" || tools["card"].Status != StatusDeclined {
		t.Errorf("cards = %+v", tools)
	}
}
