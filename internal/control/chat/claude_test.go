package chat

import (
	"bufio"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

// replay feeds a recorded session to a driver and collects what it says.
func replay(t *testing.T, d Driver, file string) (events []Event, writes [][]byte, resume string) {
	t.Helper()
	return replaySent(t, d, file, 0)
}

// replaySent replays a session whose first sent turns came from messages
// the user sent: one goes in before the recording, and another after each
// of those turns' results. Later turns are the harness's own.
func replaySent(t *testing.T, d Driver, file string, sent int) (events []Event, writes [][]byte, resume string) {
	t.Helper()
	send := func() {
		if sent > 0 {
			sent--
			if _, err := d.Send("go"); err != nil {
				t.Fatal(err)
			}
		}
	}
	send()
	f, err := os.Open("testdata/" + file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 4<<20)
	for sc.Scan() {
		out := d.Read(sc.Bytes())
		if slices.ContainsFunc(out.Events, func(e Event) bool { return e.Kind == KindTurn && e.Turn.State != TurnWorking }) {
			send()
		}
		events = append(events, out.Events...)
		writes = append(writes, out.Write...)
		if out.Resume != "" {
			resume = out.Resume
		}
	}
	return
}

func kinds(events []Event) []string {
	var out []string
	for _, e := range events {
		if len(out) == 0 || out[len(out)-1] != e.Kind {
			out = append(out, e.Kind)
		}
	}
	return out
}

func find(events []Event, kind string) []Event {
	var out []Event
	for _, e := range events {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func decodeLine(t *testing.T, b []byte) map[string]any {
	t.Helper()
	if strings.Contains(string(b), "\n") {
		t.Fatalf("a line for the harness contains a newline: %s", b)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("line %s: %v", b, err)
	}
	return m
}

func TestClaudeArgv(t *testing.T) {
	d := newClaude()
	got := d.Argv(Options{Mode: ModeAsk, NewID: "11111111-2222-3333-4444-555555555555", MCP: "http://10.89.1.1:7701/mcp/orbit/tok/s1"})
	for _, want := range [][]string{
		{"--input-format", "stream-json"}, {"--output-format", "stream-json"}, {"--permission-mode", "default"},
		{"--session-id", "11111111-2222-3333-4444-555555555555"}, {"--mcp-config", `{"mcpServers":{"skybreaker":{"type":"http","url":"http://10.89.1.1:7701/mcp/orbit/tok/s1"}}}`},
		{"--include-partial-messages"}, {"-p"},
	} {
		if !containsSeq(got, want) {
			t.Errorf("argv %q lacks %q", got, want)
		}
	}
	if slices.Contains(got, "--resume") {
		t.Error("a new conversation resumed")
	}
	got = d.Argv(Options{Mode: ModeAuto, Resume: "abc", NewID: "ignored"})
	if !containsSeq(got, []string{"--resume", "abc"}) || slices.Contains(got, "--session-id") || !containsSeq(got, []string{"--permission-mode", "bypassPermissions"}) {
		t.Errorf("resume argv = %q", got)
	}
	if slices.Contains(got, "--mcp-config") {
		t.Errorf("empty mcp config kept its flag: %q", got)
	}
	// Without this at launch, Claude refuses to switch to Auto later
	// ("bypass_not_launched"); it only allows the switch, it doesn't make it.
	for _, m := range []string{ModeAsk, ModeEdits, ModePlan, ModeAuto} {
		if !slices.Contains(d.Argv(Options{Mode: m}), "--allow-dangerously-skip-permissions") {
			t.Errorf("%s: argv can't switch to auto later", m)
		}
	}
	if d.Env()["IS_SANDBOX"] != "1" {
		t.Error("bypassPermissions needs IS_SANDBOX=1 when claude runs as root")
	}
}

func containsSeq(s, sub []string) bool {
	for i := range s {
		if i+len(sub) <= len(s) && slices.Equal(s[i:i+len(sub)], sub) {
			return true
		}
	}
	return false
}

func TestClaudeOpenInitializes(t *testing.T) {
	lines := newClaude().Open(Options{})
	if len(lines) != 1 {
		t.Fatalf("open wrote %d lines", len(lines))
	}
	m := decodeLine(t, lines[0])
	if m["type"] != "control_request" || m["request"].(map[string]any)["subtype"] != "initialize" {
		t.Errorf("open = %s", lines[0])
	}
}

// claude-bash.jsonl is a recorded turn (Claude Code 2.1.303): one Bash call,
// auto-approved, then an answer.
func TestClaudeReplayBash(t *testing.T) {
	events, writes, resume := replay(t, newClaude(), "claude-bash.jsonl")
	if len(writes) != 0 {
		t.Errorf("replay wrote %q", writes)
	}
	if resume != "15e3d7db-82bd-455a-98c3-90e02034e81b" {
		t.Errorf("resume = %q", resume)
	}
	info := slices.DeleteFunc(find(events, KindInfo), func(e Event) bool { return e.Info.Context != nil })
	if len(info) != 1 || info[0].Info.Model != "claude-opus-5-5" || info[0].Info.Mode != ModeAsk || !slices.Contains(info[0].Info.Commands, "compact") {
		t.Fatalf("info = %+v", info)
	}
	// Two messages, each reporting how full the context is; the result
	// adds the model's window. (The result's own usage sums the turn's
	// requests, so it isn't the context.)
	cs := contexts(events)
	if len(cs) != 3 || cs[0].Used != 22390 || cs[2] != (Context{Used: cs[1].Used, Window: 1000000}) || cs[1].Used <= cs[0].Used {
		t.Errorf("contexts = %+v", cs)
	}
	tools := find(events, KindTool)
	if len(tools) != 2 {
		t.Fatalf("tool events = %+v", tools)
	}
	start, end := tools[0].Tool, tools[1].Tool
	if tools[0].ID != "toolu_01Qrit89oobaaz9xu7Mgsiho" || tools[1].ID != tools[0].ID {
		t.Errorf("tool IDs %q, %q", tools[0].ID, tools[1].ID)
	}
	if start.Kind != ToolCommand || start.Title != "ls /workspace | head -3" || start.Status != StatusRunning {
		t.Errorf("start = %+v", start)
	}
	if end.Status != StatusDone || end.Output != "CLAUDE.md\nDockerfile\nREADME.md" || end.Title != start.Title {
		t.Errorf("end = %+v", end)
	}
	texts := find(events, KindText)
	if len(texts) != 1 || texts[0].Text != "The first entry is `CLAUDE.md`." {
		t.Fatalf("texts = %+v", texts)
	}
	// The streamed text and the finished text are the same item.
	deltas := find(events, KindDelta)
	if len(deltas) == 0 {
		t.Fatal("no deltas")
	}
	var streamed strings.Builder
	for _, d := range deltas {
		if d.ID != texts[0].ID {
			t.Errorf("delta for %q, text is %q", d.ID, texts[0].ID)
		}
		streamed.WriteString(d.Text)
	}
	if streamed.String() != texts[0].Text {
		t.Errorf("deltas spell %q", streamed.String())
	}
	turns := find(events, KindTurn)
	if len(turns) != 1 || turns[0].Turn.State != TurnDone || turns[0].Turn.DurationMS == 0 {
		t.Errorf("turns = %+v", turns)
	}
	if got := kinds(events); got[len(got)-1] != KindTurn {
		t.Errorf("last event %q, want the turn's end", got[len(got)-1])
	}
}

// claude-interrupted.jsonl: a Write waited for permission and the user
// interrupted.
func TestClaudeReplayInterrupted(t *testing.T) {
	d := newClaude()
	d.Interrupt()
	events, _, _ := replay(t, d, "claude-interrupted.jsonl")
	tools := find(events, KindTool)
	if len(tools) < 2 {
		t.Fatalf("tools = %+v", tools)
	}
	w := tools[0].Tool
	if w.Kind != ToolEdit || w.Title != ".skybreaker-probe.txt" || !strings.Contains(w.Diff, "+hi") || w.Added != 1 {
		t.Errorf("write card = %+v", w)
	}
	if last := tools[len(tools)-1].Tool; last.Status != StatusDeclined {
		t.Errorf("declined write ended %q", last.Status)
	}
	for _, e := range find(events, KindText) {
		if strings.Contains(e.Text, "Request interrupted") {
			t.Errorf("harness bookkeeping shown as text: %q", e.Text)
		}
	}
	turns := find(events, KindTurn)
	if len(turns) != 1 || turns[0].Turn.State != TurnInterrupted {
		t.Errorf("turns = %+v", turns)
	}
}

func TestClaudePermission(t *testing.T) {
	d := newClaude()
	req := `{"type":"control_request","request_id":"r1","request":{"subtype":"can_use_tool","tool_name":"Edit","input":{"file_path":"/workspace/main.go","old_string":"a := 1","new_string":"a := 2"},"permission_suggestions":[{"type":"addRules","rules":[{"toolName":"Edit"}],"behavior":"allow","destination":"session"}],"tool_use_id":"toolu_9","decision_reason":"edits need approval"}}`
	out := d.Read([]byte(req))
	if len(out.Events) != 1 || out.Events[0].Kind != KindAsk {
		t.Fatalf("events = %+v", out.Events)
	}
	ask := out.Events[0]
	if ask.ID != "r1" || ask.Ask.Kind != AskPermission || ask.Ask.Tool != "Edit" || ask.Ask.ToolID != "toolu_9" || ask.Ask.Title != "Edit main.go" || !ask.Ask.Always {
		t.Errorf("ask = %+v", ask.Ask)
	}
	if !strings.Contains(ask.Ask.Detail, "-a := 1") || !strings.Contains(ask.Ask.Detail, "+a := 2") {
		t.Errorf("detail = %q", ask.Ask.Detail)
	}

	lines, err := d.Answer("r1", Answer{Allow: true, Always: true})
	if err != nil {
		t.Fatal(err)
	}
	m := decodeLine(t, lines[0])
	resp := m["response"].(map[string]any)
	if m["type"] != "control_response" || resp["request_id"] != "r1" || resp["subtype"] != "success" {
		t.Fatalf("answer = %s", lines[0])
	}
	r := resp["response"].(map[string]any)
	if r["behavior"] != "allow" || r["updatedInput"].(map[string]any)["new_string"] != "a := 2" || r["updatedPermissions"] == nil {
		t.Errorf("allow = %s", lines[0])
	}
	if _, err := d.Answer("r1", Answer{Allow: true}); err == nil {
		t.Error("answered the same ask twice")
	}

	d.Read([]byte(strings.Replace(req, `"r1"`, `"r2"`, 1)))
	lines, _ = d.Answer("r2", Answer{Message: "use a constant"})
	r = decodeLine(t, lines[0])["response"].(map[string]any)["response"].(map[string]any)
	if r["behavior"] != "deny" || r["message"] != "use a constant" {
		t.Errorf("deny = %s", lines[0])
	}
}

func TestClaudeQuestion(t *testing.T) {
	d := newClaude()
	req := `{"type":"control_request","request_id":"q1","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","tool_use_id":"toolu_q","input":{"questions":[{"question":"Which database?","header":"DB","multiSelect":false,"options":[{"label":"SQLite","description":"one file"},{"label":"Postgres","description":"a server"}]}]}}}`
	out := d.Read([]byte(req))
	if len(out.Events) != 1 {
		t.Fatalf("events = %+v", out.Events)
	}
	a := out.Events[0].Ask
	if a.Kind != AskQuestion || len(a.Questions) != 1 || a.Questions[0].Question != "Which database?" || len(a.Questions[0].Options) != 2 || a.Questions[0].ID == "" {
		t.Fatalf("ask = %+v", a)
	}
	lines, err := d.Answer("q1", Answer{Allow: true, Answers: map[string][]string{a.Questions[0].ID: {"SQLite"}}})
	if err != nil {
		t.Fatal(err)
	}
	r := decodeLine(t, lines[0])["response"].(map[string]any)["response"].(map[string]any)
	in := r["updatedInput"].(map[string]any)
	if r["behavior"] != "allow" || in["answers"].(map[string]any)["Which database?"] != "SQLite" || in["questions"] == nil {
		t.Errorf("answer = %s", lines[0])
	}
}

func TestClaudeLoginNeeded(t *testing.T) {
	d := newClaude()
	out := d.Read([]byte(`{"type":"result","subtype":"success","is_error":true,"result":"Not logged in · Please run /login","duration_ms":12,"session_id":"x"}`))
	logins := find(out.Events, KindLogin)
	if len(logins) != 1 || !logins[0].Login.Needed || !logins[0].Login.Terminal {
		t.Errorf("events = %+v", out.Events)
	}
	if turns := find(out.Events, KindTurn); len(turns) != 1 || turns[0].Turn.State != TurnFailed {
		t.Errorf("turns = %+v", turns)
	}
	if _, err := d.Login(); err != ErrTerminalLogin {
		t.Errorf("Login() = %v", err)
	}
}

func TestClaudeEditResult(t *testing.T) {
	d := newClaude()
	d.Read([]byte(`{"type":"assistant","message":{"id":"m1","content":[{"type":"tool_use","id":"t1","name":"Edit","input":{"file_path":"/workspace/a.go","old_string":"x","new_string":"y"}}]},"parent_tool_use_id":null}`))
	out := d.Read([]byte(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"The file has been updated."}]},"tool_use_result":{"filePath":"/workspace/a.go","structuredPatch":[{"oldStart":3,"oldLines":2,"newStart":3,"newLines":2,"lines":[" keep","-x","+y"]}]}}`))
	tools := find(out.Events, KindTool)
	if len(tools) != 1 {
		t.Fatalf("events = %+v", out.Events)
	}
	tl := tools[0].Tool
	if tl.Status != StatusDone || tl.Added != 1 || tl.Removed != 1 || !strings.Contains(tl.Diff, "@@ -3,2 +3,2 @@\n keep\n-x\n+y") || !strings.HasPrefix(tl.Diff, "--- a/a.go") {
		t.Errorf("edit = %+v", tl)
	}
}

func TestClaudeSubagentToolsNest(t *testing.T) {
	d := newClaude()
	out := d.Read([]byte(`{"type":"assistant","message":{"id":"m2","content":[{"type":"tool_use","id":"t2","name":"Grep","input":{"pattern":"TODO","path":"/workspace/internal"}}]},"parent_tool_use_id":"task1"}`))
	tl := find(out.Events, KindTool)[0].Tool
	if tl.Parent != "task1" || tl.Kind != ToolSearch || tl.Title != "TODO in internal" {
		t.Errorf("nested = %+v", tl)
	}
	// A subagent's own text is its business, not the conversation's.
	out = d.Read([]byte(`{"type":"assistant","message":{"id":"m3","content":[{"type":"text","text":"sub says"}]},"parent_tool_use_id":"task1"}`))
	if len(find(out.Events, KindText)) != 0 {
		t.Errorf("subagent text surfaced: %+v", out.Events)
	}
}

func TestClaudeTodo(t *testing.T) {
	d := newClaude()
	out := d.Read([]byte(`{"type":"assistant","message":{"id":"m4","content":[{"type":"tool_use","id":"t4","name":"TodoWrite","input":{"todos":[{"content":"Write tests","status":"completed"},{"content":"Implement","status":"in_progress"},{"content":"Ship","status":"pending"}]}}]}}`))
	tl := find(out.Events, KindTool)[0].Tool
	if tl.Kind != ToolTodo || tl.Output != "[x] Write tests\n[~] Implement\n[ ] Ship" {
		t.Errorf("todo = %+v", tl)
	}
}

func TestClaudeIntents(t *testing.T) {
	d := newClaude()
	lines, err := d.Send("hello\nworld")
	if err != nil {
		t.Fatal(err)
	}
	m := decodeLine(t, lines[0])
	if m["type"] != "user" || m["message"].(map[string]any)["content"] != "hello\nworld" {
		t.Errorf("send = %s", lines[0])
	}
	m = decodeLine(t, d.Interrupt()[0])
	if m["request"].(map[string]any)["subtype"] != "interrupt" {
		t.Errorf("interrupt = %v", m)
	}
	lines, err = d.SetMode(ModeEdits)
	if err != nil {
		t.Fatal(err)
	}
	if r := decodeLine(t, lines[0])["request"].(map[string]any); r["subtype"] != "set_permission_mode" || r["mode"] != "acceptEdits" {
		t.Errorf("set mode = %v", r)
	}
	if _, err := d.SetMode("yolo"); err == nil {
		t.Error("unknown mode accepted")
	}
	if _, err := d.Send("  "); err == nil {
		t.Error("empty message sent")
	}
}

// Whatever the harness prints, the driver neither panics nor invents events.
func TestClaudeIgnoresJunk(t *testing.T) {
	d := newClaude()
	for _, l := range []string{``, `not json`, `[]`, `{"type":"mystery"}`, `{"type":"assistant","message":"x"}`, `{"type":"user","message":{"content":"plain text"}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{"}}}`,
		`{"type":"system","subtype":"permission_denied","message":"x"}`, `{"type":"control_request","request_id":"z","request":{"subtype":"mystery"}}`} {
		out := d.Read([]byte(l))
		if len(out.Events) != 0 {
			t.Errorf("%s → %+v", l, out.Events)
		}
	}
}

func FuzzClaudeRead(f *testing.F) {
	f.Add([]byte(`{"type":"assistant","message":{"id":"m","content":[{"type":"tool_use","id":"t","name":"Bash","input":{"command":"ls"}}]}}`))
	f.Add([]byte(`{"type":"control_request","request_id":"r","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":{"questions":[{}]}}}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		newClaude().Read(b)
	})
}

// claude-permission.jsonl (Claude Code 2.1.303): a Write and a Bash call,
// each asked over stdio with --permission-prompt-tool stdio and allowed.
func TestClaudeReplayPermission(t *testing.T) {
	events, writes, _ := replay(t, newClaude(), "claude-permission.jsonl")
	if len(writes) != 0 {
		t.Errorf("wrote %q", writes)
	}
	asks := find(events, KindAsk)
	if len(asks) != 2 {
		t.Fatalf("asks = %+v", asks)
	}
	w, b := asks[0].Ask, asks[1].Ask
	if w.Title != "Edit .skybreaker-probe.txt" || !strings.Contains(w.Detail, "+hi") || !w.Always || w.ToolID != "toolu_01LFccu9FB91QDkstsYqMgwU" {
		t.Errorf("write ask = %+v", w)
	}
	if b.Title != "Run rm /workspace/.skybreaker-probe.txt" || b.Reason != "Delete the probe file" || b.Detail != "rm /workspace/.skybreaker-probe.txt" {
		t.Errorf("bash ask = %+v", b)
	}
	if turns := find(events, KindTurn); len(turns) != 1 || turns[0].Turn.State != TurnDone {
		t.Errorf("turns = %+v", turns)
	}
	if tools := find(events, KindTool); tools[len(tools)-1].Tool.Status != StatusDone {
		t.Errorf("tools = %+v", tools)
	}
}

func TestClaudeModeSwitchAnswered(t *testing.T) {
	d := newClaude()
	d.Argv(Options{Mode: ModeEdits})
	id := func(b []byte) string { return decodeLine(t, b)["request_id"].(string) }
	reply := func(rid, body string) Out {
		return d.Read([]byte(`{"type":"control_response","response":{"request_id":"` + rid + `",` + body + `}}`))
	}

	lines, _ := d.SetMode(ModeAuto)
	out := reply(id(lines[0]), `"subtype":"error","error":"Cannot set permission mode to bypassPermissions because the session was not launched with --dangerously-skip-permissions"`)
	var notice, mode string
	for _, e := range out.Events {
		if e.Kind == KindNotice {
			notice = e.Text
		}
		if e.Kind == KindInfo && e.Info != nil {
			mode = e.Info.Mode
		}
	}
	// A refused switch says so and puts the chip back where Claude is.
	if !strings.Contains(notice, "Auto") || mode != ModeEdits {
		t.Fatalf("refused switch: notice %q, mode %q", notice, mode)
	}

	lines, _ = d.SetMode(ModePlan)
	out = reply(id(lines[0]), `"subtype":"success","response":{"mode":"plan"}`)
	if len(out.Events) != 1 || out.Events[0].Info == nil || out.Events[0].Info.Mode != ModePlan {
		t.Fatalf("accepted switch: %+v", out.Events)
	}
	// A later refusal goes back to plan, the mode Claude is in now.
	lines, _ = d.SetMode(ModeAuto)
	out = reply(id(lines[0]), `"subtype":"error","error":"no"`)
	for _, e := range out.Events {
		if e.Kind == KindInfo && e.Info.Mode != ModePlan {
			t.Fatalf("reverted to %q, want plan", e.Info.Mode)
		}
	}
}
