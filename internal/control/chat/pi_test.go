package chat

import (
	"bufio"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// The pi-*.jsonl recordings are pi 1.3.0's stdout in RPC mode, with
// claude-haiku-5-5, answering get_state, get_available_models and
// get_commands before each conversation. System prompts are cut short.

// replayPi feeds a recording to d line by line, running after, when not
// nil, once d has read each line, with what that line was.
func replayPi(t *testing.T, d Driver, file string, after func(l map[string]any)) []Event {
	t.Helper()
	f, err := os.Open("testdata/" + file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 4<<20)
	var events []Event
	for sc.Scan() {
		events = append(events, d.Read(sc.Bytes()).Events...)
		if after != nil {
			after(decodeLine(t, sc.Bytes()))
		}
	}
	return events
}

// must returns what a driver call wrote, failing the test on its error.
func must(t *testing.T) func([][]byte, error) [][]byte {
	return func(l [][]byte, err error) [][]byte {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
}

func commands(t *testing.T, lines [][]byte) []string {
	t.Helper()
	var out []string
	for _, l := range lines {
		out = append(out, decodeLine(t, l)["type"].(string))
	}
	return out
}

func TestPiArgv(t *testing.T) {
	d := newPi()
	for _, c := range []struct {
		o    Options
		want []string
	}{
		{Options{NewID: "n-1", MCP: "http://broker:7701/mcp/p/t/s1"}, []string{"skybreaker-pi", "--mode", "rpc", "--session-id", "n-1"}},
		{Options{Resume: "r-1", Model: "anthropic/claude-haiku-5-5"}, []string{"skybreaker-pi", "--mode", "rpc", "--model", "anthropic/claude-haiku-5-5", "--session-id", "r-1"}},
		{Options{Resume: "r-1", Fork: true}, []string{"skybreaker-pi", "--mode", "rpc", "--fork", "r-1"}},
	} {
		if got := d.Argv(c.o); !slices.Equal(got, c.want) {
			t.Errorf("argv(%+v) = %q, want %q", c.o, got, c.want)
		}
	}
	if d.Env()["PI_SKIP_VERSION_CHECK"] != "1" || d.Env()["PI_TELEMETRY"] != "0" {
		t.Errorf("env = %v", d.Env())
	}
}

// pi has no permission modes and signs in only in its terminal.
func TestPiModesAndLogin(t *testing.T) {
	d := newPi()
	if !slices.Equal(d.Modes(), []string{ModeAuto}) {
		t.Errorf("modes = %q", d.Modes())
	}
	if _, err := d.SetMode(ModeAsk); err == nil {
		t.Error("ask mode taken")
	}
	if _, err := d.Login(); err != ErrTerminalLogin {
		t.Errorf("login = %v", err)
	}
	if got := commands(t, newPi().Open(Options{})); !slices.Equal(got, []string{"get_state", "get_available_models", "get_commands"}) {
		t.Errorf("open = %q", got)
	}
}

// pi-plain.jsonl: one short reply.
func TestPiReplayPlain(t *testing.T) {
	events, _, resume := replaySent(t, newPi(), "pi-plain.jsonl", 1)
	if resume != "0e1a5b0c-5f0e-4a7e-9d39-6f3c2a1b7d10" {
		t.Errorf("resume = %q", resume)
	}
	var info Info
	for _, e := range find(events, KindInfo) {
		if e.Info.Model != "" {
			info.Model, info.Mode = e.Info.Model, e.Info.Mode
		}
		if e.Info.Models != nil {
			info.Models = e.Info.Models
		}
		if e.Info.Commands != nil {
			info.Commands = e.Info.Commands
		}
		if c := e.Info.Context; c != nil {
			if c.Used == 0 {
				// pi's state says nothing of what the context holds, as on a resume.
				t.Errorf("context emptied: %+v", c)
			}
			info.Context = c
		}
	}
	if info.Model != "anthropic/claude-haiku-5-5" || info.Mode != ModeAuto {
		t.Errorf("info = %+v", info)
	}
	if len(info.Models) != 3 || !slices.ContainsFunc(info.Models, func(m Model) bool {
		return m.ID == "anthropic/claude-haiku-5-5" && m.Name == "Claude Haiku 5.5"
	}) {
		t.Errorf("models = %+v", info.Models)
	}
	if !slices.Equal(info.Commands, []string{"llama", "mcp"}) {
		t.Errorf("commands = %q", info.Commands)
	}
	if info.Context == nil || info.Context.Used != 4097 || info.Context.Window != 1000000 {
		t.Errorf("context = %+v", info.Context)
	}
	if got := kinds(slices.DeleteFunc(slices.Clone(events), func(e Event) bool { return e.Kind == KindInfo })); !slices.Equal(got, []string{KindDelta, KindText, KindTurn}) {
		t.Fatalf("kinds = %q (%+v)", got, events)
	}
	text := find(events, KindText)
	if len(text) != 1 || text[0].Text != "hello from pi" || text[0].ID != find(events, KindDelta)[0].ID {
		t.Errorf("text = %+v", text)
	}
	if turns := find(events, KindTurn); turns[0].Turn.State != TurnDone {
		t.Errorf("turn = %+v", turns[0].Turn)
	}
	if u, _ := readAll(t, newPi(), "pi-plain.jsonl"); u != (Usage{Input: 4 + 2027, Cached: 2059, Output: 7}) {
		t.Errorf("usage = %+v", u)
	}
}

// pi-tools.jsonl: bash and read at once, then edit and write at once.
func TestPiReplayTools(t *testing.T) {
	events, _, _ := replaySent(t, newPi(), "pi-tools.jsonl", 1)
	tools, at := lastTool(events)
	const bash, read, edit, write = "toolu_01JURyAK1CqQsiv8Yqdst5qi", "toolu_01Y9XJ4NqzU8fU6skT7sH6jb", "toolu_01NjVNnahkVSY4M8uTfmMpj7", "toolu_014wiZETrFctj2uF1UeYsePa"
	if b := tools[bash]; b.Kind != ToolCommand || b.Title != "ls" || b.Status != StatusDone || b.Output != "notes.txt\n" || b.Exit == nil || *b.Exit != 0 {
		t.Errorf("bash = %+v", b)
	}
	if r := tools[read]; r.Kind != ToolRead || r.Title != "notes.txt" || r.Status != StatusDone || r.Output != "apple\nbanana\n" {
		t.Errorf("read = %+v", r)
	}
	e := tools[edit]
	if e.Kind != ToolEdit || e.Title != "notes.txt" || e.Status != StatusDone || e.Added != 1 || e.Removed != 1 || !strings.Contains(e.Diff, "-apple\n+pear\n") {
		t.Errorf("edit = %+v", e)
	}
	if w := tools[write]; w.Kind != ToolEdit || w.Title != "done.txt" || w.Status != StatusDone || w.Added != 1 || !strings.Contains(w.Diff, "+ok\n") {
		t.Errorf("write = %+v", w)
	}
	// A card is sent again only when it changes.
	last := map[string]Tool{}
	for i, e := range events {
		if e.Kind != KindTool {
			continue
		}
		if b, ok := last[e.ID]; ok && b.Status == e.Tool.Status && b.Output == e.Tool.Output {
			t.Errorf("event %d: %s again unchanged", i, e.ID)
		}
		last[e.ID] = *e.Tool
	}
	// Each card shows as running before it ends.
	for _, id := range []string{bash, read, edit, write} {
		if c, ok := toolAt(events, id, at[id]); !ok || c.Status != StatusRunning {
			t.Errorf("%s before its end = %+v", id, c)
		}
	}
	if turns := find(events, KindTurn); len(turns) != 1 || turns[0].Turn.State != TurnDone || turnIndex(events, TurnDone, 0) < at[write] {
		t.Errorf("turns = %+v", turns)
	}
	if text := find(events, KindText); len(text) != 1 || !strings.Contains(strings.ToLower(text[0].Text), "done") {
		t.Errorf("text = %+v", text)
	}
}

// pi-interrupt.jsonl: stopped during `sleep 30`, then asked again.
func TestPiReplayInterrupt(t *testing.T) {
	d := newPi()
	if stop := d.Interrupt(); stop != nil {
		t.Errorf("interrupt when idle = %q", stop)
	}
	d.Send("go")
	var stop [][]byte
	sent := false
	events := replayPi(t, d, "pi-interrupt.jsonl", func(l map[string]any) {
		switch {
		case l["type"] == "tool_execution_update" && stop == nil:
			stop = d.Interrupt()
		case l["type"] == "response" && l["command"] == "abort" && !sent:
			sent = true
			d.Send("go")
		}
	})
	if got := commands(t, stop); !slices.Equal(got, []string{"clear_queue", "abort"}) {
		t.Errorf("interrupt = %q", got)
	}
	turns := find(events, KindTurn)
	if len(turns) != 2 || turns[0].Turn.State != TurnInterrupted || turns[1].Turn.State != TurnDone {
		t.Fatalf("turns = %+v", turns)
	}
	tools, _ := lastTool(events)
	if b := tools["toolu_01YLyAsZUtaFqJ7J1HutQ6uL"]; b.Status != StatusDeclined || b.Title != "sleep 30 && echo finished" {
		t.Errorf("stopped command = %+v", b)
	}
	if text := find(events, KindText); len(text) != 1 || !strings.Contains(strings.ToLower(text[0].Text), "still here") {
		t.Errorf("text = %+v", text)
	}
	if th := find(events, KindThinking); len(th) != 1 || th[0].Text == "" {
		t.Errorf("thinking = %+v", th)
	}
}

// pi drops an abort that comes before its run has started, so the driver
// sends it again once the run starts.
func TestPiInterruptBeforeTheRun(t *testing.T) {
	d := newPi()
	d.Send("go")
	d.Interrupt()
	out := d.Read([]byte(`{"type":"agent_start"}`))
	if got := commands(t, out.Write); !slices.Equal(got, []string{"abort"}) || len(out.Events) != 0 {
		t.Errorf("start = %+v", out)
	}
	out = d.Read([]byte(`{"type":"agent_settled","aborted":true}`))
	if out.Events[0].Turn.State != TurnInterrupted {
		t.Errorf("settle = %+v", out.Events)
	}
	if out := d.Read([]byte(`{"type":"agent_start"}`)); len(out.Write) != 0 {
		t.Errorf("the next run aborted too: %q", out.Write)
	}
}

// pi-subagent.jsonl: the example subagent extension runs the scout agent
// as a separate pi inside one tool call, which reports progress as it goes.
func TestPiReplaySubagent(t *testing.T) {
	events, _, _ := replaySent(t, newPi(), "pi-subagent.jsonl", 1)
	const sub = "toolu_01E2h5PUZZiKMKz21iCBRETs"
	tools, at := lastTool(events)
	s := tools[sub]
	if s.Kind != ToolAgent || s.Name != "subagent" || s.Title != "scout" || s.Status != StatusDone || !strings.Contains(s.Output, "pear") {
		t.Errorf("subagent = %+v", s)
	}
	// Its card runs, with the chat working, until the subagent returns.
	var updates int
	for i, e := range events[:at[sub]] {
		if e.Kind == KindTool && e.ID == sub {
			updates++
			if e.Tool.Status != StatusRunning {
				t.Errorf("event %d: card %s before the end", i, e.Tool.Status)
			}
		}
		if e.Kind == KindTurn {
			t.Errorf("event %d: turn %s while the subagent runs", i, e.Turn.State)
		}
	}
	if updates < 2 {
		t.Errorf("%d running updates", updates)
	}
	if end := turnIndex(events, TurnDone, 0); end < at[sub] {
		t.Errorf("turn ended at %d, subagent at %d", end, at[sub])
	}
}

// pi-resume.jsonl: pi-plain's conversation, continued in a new process.
func TestPiReplayResume(t *testing.T) {
	d := newPi()
	d.Open(Options{Resume: "0e1a5b0c-5f0e-4a7e-9d39-6f3c2a1b7d10"})
	events, _, resume := replaySent(t, d, "pi-resume.jsonl", 1)
	if resume != "0e1a5b0c-5f0e-4a7e-9d39-6f3c2a1b7d10" {
		t.Errorf("resume = %q", resume)
	}
	if n := find(events, KindNotice); len(n) != 0 {
		t.Errorf("notices = %+v", n)
	}
	if text := find(events, KindText); len(text) != 1 || !strings.Contains(text[0].Text, "hello from pi") {
		t.Errorf("text = %+v", text)
	}
}

// A conversation pi can't find to resume starts afresh, and says so.
func TestPiResumeLost(t *testing.T) {
	state := []byte(`{"type":"response","command":"get_state","success":true,"data":{"sessionId":"r-1","messageCount":0}}`)
	d := newPi()
	d.Open(Options{Resume: "r-1"})
	if n := find(d.Read(state).Events, KindNotice); len(n) != 1 || !strings.Contains(n[0].Text, "couldn't be resumed") {
		t.Errorf("notices = %+v", n)
	}
	d = newPi()
	d.Open(Options{NewID: "r-1"})
	if n := find(d.Read(state).Events, KindNotice); len(n) != 0 {
		t.Errorf("a new conversation: %+v", n)
	}
}

// pi-compact.jsonl: a turn, a model switch, a switch to a model that
// isn't there, another turn, then a compaction pi refuses.
func TestPiReplayModelAndCompact(t *testing.T) {
	d := newPi()
	d.Send("go")
	turns := 0
	var switched, compact [][]byte
	events := replayPi(t, d, "pi-compact.jsonl", func(l map[string]any) {
		if l["type"] != "agent_settled" {
			return
		}
		// What the recorder sent after each turn, sent here as it ends.
		switch turns++; turns {
		case 1:
			switched = append(must(t)(d.SetModel("anthropic/claude-haiku-4-5")), must(t)(d.SetModel("anthropic/no-such-model"))...)
			d.Send("go")
		case 2:
			compact = must(t)(d.Compact())
		}
	})
	if m := decodeLine(t, switched[0]); m["type"] != "set_model" || m["provider"] != "anthropic" || m["modelId"] != "claude-haiku-4-5" {
		t.Errorf("set_model = %v", m)
	}
	if got := commands(t, compact); !slices.Equal(got, []string{"compact"}) {
		t.Errorf("compact = %q", got)
	}
	if _, err := d.SetModel("no-provider"); err == nil {
		t.Error("a model without its provider was taken")
	}
	var models []string
	for _, e := range find(events, KindInfo) {
		if e.Info.Model != "" {
			models = append(models, e.Info.Model)
		}
	}
	if !slices.Equal(models, []string{"anthropic/claude-haiku-5-5", "anthropic/claude-haiku-4-5"}) {
		t.Errorf("models = %q", models)
	}
	if n := find(events, KindNotice); len(n) != 1 || !strings.Contains(n[0].Text, "no-such-model") {
		t.Errorf("notices = %+v", n)
	}
	got := find(events, KindTurn)
	if len(got) != 3 || got[0].Turn.State != TurnDone || got[2].Turn.State != TurnFailed || !strings.Contains(got[2].Turn.Error, "Nothing to compact") {
		t.Errorf("turns = %+v", got)
	}
}

func TestPiCompacted(t *testing.T) {
	d := newPi()
	d.Read([]byte(`{"type":"response","command":"get_state","success":true,"data":{"model":{"id":"m","provider":"p","contextWindow":200000}}}`))
	d.Compact()
	events := d.Read([]byte(`{"type":"compaction_end","reason":"manual","result":{"summary":"s","estimatedTokensAfter":3200},"aborted":false,"willRetry":false}`)).Events
	if got := kinds(events); !slices.Equal(got, []string{KindNotice, KindInfo}) || *events[1].Info.Context != (Context{Used: 3200, Window: 200000}) {
		t.Errorf("events = %+v", events)
	}
	events = d.Read([]byte(`{"type":"response","command":"compact","success":true,"data":{}}`)).Events
	if len(events) != 1 || events[0].Turn.State != TurnDone {
		t.Errorf("end = %+v", events)
	}
	// A compaction stopped part way is interrupted, not failed.
	d.Compact()
	d.Interrupt()
	events = d.Read([]byte(`{"type":"response","command":"compact","success":false,"error":"Compaction aborted"}`)).Events
	if len(events) != 1 || events[0].Turn.State != TurnInterrupted {
		t.Errorf("stopped = %+v", events)
	}
}

// pi-badkey.jsonl: the provider refuses the API key.
func TestPiReplayBadKey(t *testing.T) {
	events, _, _ := replaySent(t, newPi(), "pi-badkey.jsonl", 1)
	turns := find(events, KindTurn)
	if len(turns) != 1 || turns[0].Turn.State != TurnFailed || !strings.Contains(turns[0].Turn.Error, "invalid x-api-key") {
		t.Errorf("turns = %+v", turns)
	}
	if l := find(events, KindLogin); len(l) != 1 || !l[0].Login.Needed || !l[0].Login.Terminal {
		t.Errorf("login = %+v", l)
	}
}

// With no model configured, pi reports a placeholder, which isn't shown.
func TestPiNoModel(t *testing.T) {
	out := newPi().Read([]byte(`{"type":"response","command":"get_state","success":true,"data":{"model":{"id":"unknown","name":"unknown","provider":"unknown","contextWindow":0},"sessionId":"s"}}`))
	if info := find(out.Events, KindInfo); len(info) != 1 || info[0].Info.Model != "" {
		t.Errorf("info = %+v", info)
	}
}

// Every message says to steer: pi reads it only during a run, which may
// have begun without the driver having read so.
func TestPiSend(t *testing.T) {
	d := newPi()
	for _, text := range []string{"fix it", "and test it"} {
		m := decodeLine(t, must(t)(d.Send(text))[0])
		if m["type"] != "prompt" || m["message"] != text || m["streamingBehavior"] != "steer" {
			t.Errorf("send = %v", m)
		}
	}
	if _, err := d.Send("  "); err == nil {
		t.Error("empty message sent")
	}
}

// A prompt pi handles itself, like an extension's command, or refuses ends
// the turn only when no run carries it on.
func TestPiPromptResponses(t *testing.T) {
	handled := []byte(`{"type":"response","command":"prompt","success":true,"data":{"disposition":"handled"}}`)
	refused := []byte(`{"type":"response","command":"prompt","success":false,"error":"No model selected"}`)
	queued := []byte(`{"type":"response","command":"prompt","success":true,"data":{"disposition":"queued"}}`)

	d := newPi()
	d.Send("/mcp")
	if out := d.Read(handled); len(out.Events) != 1 || out.Events[0].Turn.State != TurnDone {
		t.Errorf("handled = %+v", out.Events)
	}
	d.Send("go")
	if out := d.Read(refused); len(out.Events) != 1 || out.Events[0].Turn.State != TurnFailed || out.Events[0].Turn.Error != "No model selected" {
		t.Errorf("refused = %+v", out.Events)
	}

	// During a run: the run goes on, and a refusal is only a notice.
	d.Send("go")
	d.Read([]byte(`{"type":"agent_start"}`))
	d.Send("/mcp")
	for _, l := range [][]byte{handled, queued} {
		if out := d.Read(l); len(out.Events) != 0 {
			t.Errorf("%s = %+v", l, out.Events)
		}
	}
	d.Send("and this")
	if out := d.Read(refused); kinds(out.Events)[0] != KindNotice {
		t.Errorf("refused mid-run = %+v", out.Events)
	}
	if out := d.Read([]byte(`{"type":"agent_settled","aborted":false}`)); out.Events[len(out.Events)-1].Turn.State != TurnDone {
		t.Errorf("settle = %+v", out.Events)
	}
}

// A run nobody sent, as an extension's follow-up, still reads as working.
func TestPiUnaskedTurn(t *testing.T) {
	d := newPi()
	out := d.Read([]byte(`{"type":"agent_start"}`))
	if len(out.Events) != 1 || out.Events[0].Turn.State != TurnWorking {
		t.Errorf("events = %+v", out.Events)
	}
	if out := d.Read([]byte(`{"type":"agent_start"}`)); len(out.Events) != 0 {
		t.Errorf("a second start = %+v", out.Events)
	}
}

// Two messages in the same millisecond still get their own IDs.
func TestPiMessageIDs(t *testing.T) {
	d := newPi()
	d.Send("go")
	var ids []string
	for range 2 {
		d.Read([]byte(`{"type":"message_start","message":{"role":"assistant","content":[],"timestamp":1}}`))
		out := d.Read([]byte(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"timestamp":1}}`))
		// Text a provider didn't stream shows when its message ends.
		ids = append(ids, find(out.Events, KindText)[0].ID)
	}
	if ids[0] == ids[1] {
		t.Errorf("ids = %q", ids)
	}
}

// A reply that failed with no reason still fails the turn, and a card
// still running when the run ends fails with it.
func TestPiFailedRun(t *testing.T) {
	d := newPi()
	d.Send("go")
	d.Read([]byte(`{"type":"agent_start"}`))
	d.Read([]byte(`{"type":"tool_execution_start","toolCallId":"t1","toolName":"bash","args":{"command":"make"}}`))
	d.Read([]byte(`{"type":"message_end","message":{"role":"assistant","content":[],"stopReason":"error"}}`))
	out := d.Read([]byte(`{"type":"agent_settled","aborted":false}`))
	tools, _ := lastTool(out.Events)
	turn := find(out.Events, KindTurn)[0].Turn
	if tools["t1"].Status != StatusFailed || turn.State != TurnFailed || turn.Error != "The model's reply failed." || len(find(out.Events, KindLogin)) != 0 {
		t.Errorf("events = %+v", out.Events)
	}
}

func TestPiToolCards(t *testing.T) {
	d := newPi()
	d.Read([]byte(`{"type":"tool_execution_start","toolCallId":"t","toolName":"bash","args":{"command":"false"}}`))
	end := d.Read([]byte(`{"type":"tool_execution_end","toolCallId":"t","toolName":"bash","result":{"content":[{"type":"text","text":"boom"}],"structuredContent":{"exit_code":2}},"isError":false}`))
	if c := end.Events[0].Tool; c.Status != StatusFailed || *c.Exit != 2 || c.Output != "boom" {
		t.Errorf("non-zero exit = %+v", c)
	}
	for _, c := range []struct{ name, args, kind, title string }{
		{"grep", `{"pattern":"TODO","path":"/workspace/src"}`, ToolSearch, "TODO in src"},
		{"find", `{"pattern":"*.go"}`, ToolSearch, "*.go"},
		{"ls", `{"path":"/workspace/web"}`, ToolSearch, "web"},
		{"mcp__skybreaker__list_items", `{"status":"todo"}`, ToolMCP, "todo"},
		{"task", `{"prompt":"look around\nand report"}`, ToolAgent, "look around …"},
		{"lint", `{"b":"second","a":"first"}`, ToolOther, "first"},
	} {
		tool := piTool(c.name, []byte(c.args))
		if tool.Kind != c.kind || tool.Title != c.title || tool.Status != StatusRunning {
			t.Errorf("%s = %+v", c.name, tool)
		}
	}
	if tool := piTool("mcp__skybreaker__list_items", nil); tool.Name != "skybreaker · list_items" {
		t.Errorf("mcp name = %q", tool.Name)
	}
}

// Extensions' dialogs become asks; their notifications, notices.
func TestPiExtensionUI(t *testing.T) {
	d := newPi()
	d.Send("go")
	d.Read([]byte(`{"type":"agent_start"}`))
	out := d.Read([]byte(`{"type":"extension_ui_request","id":"u1","method":"select","title":"Allow dangerous command?","options":["Allow","Block"]}`))
	if len(out.Events) != 1 || out.Events[0].Kind != KindAsk {
		t.Fatalf("select = %+v", out.Events)
	}
	a := out.Events[0].Ask
	if a.Kind != AskQuestion || len(a.Questions) != 1 || a.Questions[0].Question != "Allow dangerous command?" || len(a.Questions[0].Options) != 2 {
		t.Errorf("ask = %+v", a)
	}
	m := decodeLine(t, must(t)(d.Answer(out.Events[0].ID, Answer{Allow: true, Answers: map[string][]string{a.Questions[0].ID: {"Block"}}}))[0])
	if m["type"] != "extension_ui_response" || m["id"] != "u1" || m["value"] != "Block" {
		t.Errorf("answer = %v", m)
	}
	if _, err := d.Answer(out.Events[0].ID, Answer{}); err != errNoAsk {
		t.Errorf("answered twice: %v", err)
	}

	out = d.Read([]byte(`{"type":"extension_ui_request","id":"u2","method":"confirm","title":"Clear session?","message":"All messages will be lost."}`))
	a = out.Events[0].Ask
	if a.Kind != AskPermission || a.Title != "Clear session?" || a.Detail != "All messages will be lost." {
		t.Errorf("confirm = %+v", a)
	}
	m = decodeLine(t, must(t)(d.Answer(out.Events[0].ID, Answer{Allow: false}))[0])
	if m["id"] != "u2" || m["confirmed"] != false {
		t.Errorf("confirm answer = %v", m)
	}

	out = d.Read([]byte(`{"type":"extension_ui_request","id":"u3","method":"input","title":"Branch name?","placeholder":"main"}`))
	a = out.Events[0].Ask
	m = decodeLine(t, must(t)(d.Answer(out.Events[0].ID, Answer{Allow: false}))[0])
	if a.Kind != AskQuestion || a.Questions[0].Header != "main" || m["id"] != "u3" || m["cancelled"] != true {
		t.Errorf("input = %+v, cancel = %v", a, m)
	}

	for _, l := range []string{
		`{"type":"extension_ui_request","id":"u5","method":"setStatus","statusKey":"x","statusText":"busy"}`,
		`{"type":"extension_ui_request","id":"u6","method":"notify"}`,
		`{"type":"extension_ui_request","method":"confirm","title":"no id"}`,
	} {
		if out := d.Read([]byte(l)); len(out.Events)+len(out.Write) != 0 {
			t.Errorf("%s = %+v", l, out)
		}
	}
	if out := d.Read([]byte(`{"type":"extension_ui_request","id":"u4","method":"notify","message":"Command blocked","notifyType":"warning"}`)); len(out.Events) != 1 || out.Events[0].Kind != KindNotice || out.Events[0].Text != "Command blocked" {
		t.Errorf("notify = %+v", out.Events)
	}

	// A dialog still open when its run settles is withdrawn.
	id := d.Read([]byte(`{"type":"extension_ui_request","id":"u7","method":"editor","title":"Edit the plan"}`)).Events[0].ID
	out = d.Read([]byte(`{"type":"agent_settled","aborted":false}`))
	if got := kinds(out.Events); !slices.Equal(got, []string{KindAnswered, KindTurn}) || out.Events[0].ID != id {
		t.Errorf("settle = %+v", out.Events)
	}
	if m := decodeLine(t, out.Write[0]); m["id"] != "u7" || m["cancelled"] != true {
		t.Errorf("withdraw = %v", m)
	}
}

// A dialog with a timeout is withdrawn once pi has answered it by itself;
// one from outside a run outlives the next run.
func TestPiDialogTimeouts(t *testing.T) {
	d := newPi()
	timed := d.Read([]byte(`{"type":"extension_ui_request","id":"u1","method":"confirm","title":"Go?","timeout":1}`)).Events[0].ID
	lasting := d.Read([]byte(`{"type":"extension_ui_request","id":"u2","method":"confirm","title":"Keep?"}`)).Events[0].ID
	time.Sleep(5 * time.Millisecond)
	d.Send("go")
	out := d.Read([]byte(`{"type":"agent_start"}`))
	if len(out.Events) != 1 || out.Events[0].Kind != KindAnswered || out.Events[0].ID != timed {
		t.Errorf("start = %+v", out.Events)
	}
	if out := d.Read([]byte(`{"type":"agent_settled","aborted":false}`)); len(find(out.Events, KindAnswered)) != 0 {
		t.Errorf("settle = %+v", out.Events)
	}
	if _, err := d.Answer(lasting, Answer{Allow: true}); err != nil {
		t.Errorf("answer = %v", err)
	}
}

// Lines from pi or its extensions are fit to a title before they're shown.
func TestPiClipsWhatItShows(t *testing.T) {
	long := strings.Repeat("x", 1000)
	opts := `"` + strings.Repeat(`o","`, 80) + `o"`
	out := newPi().Read([]byte(`{"type":"extension_ui_request","id":"u1","method":"select","title":"` + long + `","options":[` + opts + `]}`))
	q := out.Events[0].Ask.Questions[0]
	if len(q.Question) > 320 || len(q.Options) != 50 {
		t.Errorf("question %d long, %d options", len(q.Question), len(q.Options))
	}
}

func FuzzPiRead(f *testing.F) {
	f.Add([]byte(`{"type":"tool_execution_end","toolCallId":"t","result":{"content":[{}]},"isError":true}`))
	f.Add([]byte(`{"type":"extension_ui_request","id":"u","method":"select","options":[]}`))
	f.Add([]byte(`{"type":"message_update","assistantMessageEvent":{"type":"toolcall_end","toolCall":{"id":"t"}}}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		d := newPi()
		d.Send("go")
		d.Read([]byte(`{"type":"message_start","message":{"role":"assistant"}}`))
		d.Read(b)
	})
}
