package chat

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// handshake walks a codex driver through initialize and thread/start, as
// codex app-server 0.163 answers them.
func handshake(t *testing.T, o Options) (*codex, []Event) {
	t.Helper()
	d := newCodex()
	open := d.Open(o)
	if len(open) != 1 || decodeLine(t, open[0])["method"] != "initialize" {
		t.Fatalf("open = %q", open)
	}
	id := decodeLine(t, open[0])["id"]
	out := d.Read(line(map[string]any{"id": id, "result": map[string]any{"userAgent": "skybreaker/0.163.0", "codexHome": "/root/.codex"}}))
	methods := []string{}
	var startID any
	for _, w := range out.Write {
		m := decodeLine(t, w)
		methods = append(methods, m["method"].(string))
		if m["method"] == "thread/start" || m["method"] == "thread/resume" || m["method"] == "thread/fork" {
			startID = m["id"]
			p := m["params"].(map[string]any)
			if p["cwd"] != "/workspace" || p["sandbox"] != "danger-full-access" {
				t.Errorf("thread params = %v", p)
			}
		}
	}
	want := []string{"initialized", "thread/start", "account/read", "model/list"}
	switch {
	case o.Resume != "" && o.Fork:
		want[1] = "thread/fork"
	case o.Resume != "":
		want[1] = "thread/resume"
	}
	if !slices.Equal(methods, want) {
		t.Fatalf("after initialize wrote %q, want %q", methods, want)
	}
	out = d.Read(line(map[string]any{"id": startID, "result": map[string]any{
		"thread": map[string]any{"id": "thr-1"}, "model": "gpt-6.1-sol", "approvalPolicy": "untrusted", "cwd": "/workspace", "modelProvider": "openai", "sandbox": map[string]any{"type": "dangerFullAccess"}, "approvalsReviewer": "user",
	}}))
	if out.Resume != "thr-1" {
		t.Errorf("resume = %q", out.Resume)
	}
	return d, out.Events
}

func TestCodexArgv(t *testing.T) {
	d := newCodex()
	argv := d.Argv(Options{MCP: "http://10.89.1.1:7701/mcp/orbit/tok/s1"})
	if argv[0] != "codex" || argv[1] != "app-server" {
		t.Errorf("argv = %q", argv)
	}
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, `mcp_servers.skybreaker.url="http://10.89.1.1:7701/mcp/orbit/tok/s1"`) {
		t.Errorf("argv lacks Skybreaker's MCP server: %q", argv)
	}
	if a := d.Argv(Options{}); len(a) != 2 {
		t.Errorf("argv without MCP = %q", a)
	}
}

func TestCodexHandshakeAndTurn(t *testing.T) {
	d, events := handshake(t, Options{Cwd: "/workspace", Mode: ModeAsk})
	info := find(events, KindInfo)
	if len(info) != 1 || info[0].Info.Model != "gpt-6.1-sol" || info[0].Info.Mode != ModeAsk {
		t.Fatalf("info = %+v", events)
	}
	lines, err := d.Send("fix the bug")
	if err != nil {
		t.Fatal(err)
	}
	m := decodeLine(t, lines[0])
	p := m["params"].(map[string]any)
	if m["method"] != "turn/start" || p["threadId"] != "thr-1" || p["approvalPolicy"] != "untrusted" {
		t.Fatalf("send = %s", lines[0])
	}
	in := p["input"].([]any)[0].(map[string]any)
	if in["type"] != "text" || in["text"] != "fix the bug" {
		t.Errorf("input = %v", in)
	}
	d.Read([]byte(`{"method":"turn/started","params":{"threadId":"thr-1","turn":{"id":"turn-1","items":[],"status":"inProgress"}}}`))

	// While a turn runs, a new message steers it.
	lines, _ = d.Send("also add a test")
	m = decodeLine(t, lines[0])
	if m["method"] != "turn/steer" || m["params"].(map[string]any)["expectedTurnId"] != "turn-1" {
		t.Errorf("steer = %s", lines[0])
	}
	m = decodeLine(t, d.Interrupt()[0])
	if m["method"] != "turn/interrupt" || m["params"].(map[string]any)["turnId"] != "turn-1" {
		t.Errorf("interrupt = %v", m)
	}

	var all []Event
	for _, l := range []string{
		`{"method":"item/started","params":{"item":{"type":"agentMessage","id":"msg-1","text":""},"threadId":"thr-1","turnId":"turn-1","startedAtMs":1}}`,
		`{"method":"item/agentMessage/delta","params":{"delta":"Looking","itemId":"msg-1","threadId":"thr-1","turnId":"turn-1"}}`,
		`{"method":"item/agentMessage/delta","params":{"delta":" now.","itemId":"msg-1","threadId":"thr-1","turnId":"turn-1"}}`,
		`{"method":"item/completed","params":{"item":{"type":"agentMessage","id":"msg-1","text":"Looking now."},"threadId":"thr-1","turnId":"turn-1","completedAtMs":2}}`,
		`{"method":"item/started","params":{"item":{"type":"commandExecution","id":"cmd-1","command":"go test ./...","commandActions":[],"cwd":"/workspace","status":"inProgress"},"threadId":"thr-1","turnId":"turn-1","startedAtMs":3}}`,
		`{"method":"item/completed","params":{"item":{"type":"commandExecution","id":"cmd-1","command":"go test ./...","commandActions":[],"cwd":"/workspace","status":"completed","exitCode":1,"aggregatedOutput":"FAIL x\n"},"threadId":"thr-1","turnId":"turn-1","completedAtMs":4}}`,
		`{"method":"item/completed","params":{"item":{"type":"reasoning","id":"r-1","summary":["Tests fail on x."],"content":[]},"threadId":"thr-1","turnId":"turn-1","completedAtMs":5}}`,
		`{"method":"turn/completed","params":{"threadId":"thr-1","turn":{"id":"turn-1","items":[],"status":"completed","durationMs":4200}}}`,
	} {
		all = append(all, d.Read([]byte(l)).Events...)
	}
	if got := kinds(all); !slices.Equal(got, []string{KindDelta, KindText, KindTool, KindThinking, KindTurn}) {
		t.Fatalf("kinds = %q (%+v)", got, all)
	}
	if all[2].ID != "msg-1" || all[2].Text != "Looking now." || all[0].ID != "msg-1" {
		t.Errorf("text = %+v", all[2])
	}
	tools := find(all, KindTool)
	end := tools[len(tools)-1].Tool
	if end.Kind != ToolCommand || end.Title != "go test ./..." || end.Status != StatusFailed || *end.Exit != 1 || end.Output != "FAIL x\n" {
		t.Errorf("command = %+v", end)
	}
	if th := find(all, KindThinking); th[0].Text != "Tests fail on x." {
		t.Errorf("thinking = %+v", th)
	}
	turn := find(all, KindTurn)[0].Turn
	if turn.State != TurnDone || turn.DurationMS != 4200 {
		t.Errorf("turn = %+v", turn)
	}
	// The turn is over: the next message starts a new one.
	lines, _ = d.Send("next")
	if decodeLine(t, lines[0])["method"] != "turn/start" {
		t.Errorf("after the turn, send = %s", lines[0])
	}
}

func TestCodexQueuesUntilThread(t *testing.T) {
	d := newCodex()
	d.Open(Options{Cwd: "/workspace"})
	lines, err := d.Send("early")
	if err != nil || len(lines) != 0 {
		t.Fatalf("send before the thread = %q, %v", lines, err)
	}
	out := d.Read([]byte(`{"id":1,"result":{}}`))
	var startID any
	for _, w := range out.Write {
		if m := decodeLine(t, w); m["method"] == "thread/start" {
			startID = m["id"]
		}
	}
	out = d.Read(line(map[string]any{"id": startID, "result": map[string]any{"thread": map[string]any{"id": "thr-9"}, "model": "m"}}))
	if len(out.Write) != 1 {
		t.Fatalf("queued message not sent: %q", out.Write)
	}
	m := decodeLine(t, out.Write[0])
	if m["method"] != "turn/start" || !strings.Contains(string(out.Write[0]), "early") {
		t.Errorf("flushed = %s", out.Write[0])
	}
}

func TestCodexResumeFallsBackToNewThread(t *testing.T) {
	d := newCodex()
	d.Open(Options{Cwd: "/workspace", Resume: "gone"})
	out := d.Read([]byte(`{"id":1,"result":{}}`))
	var resumeID any
	for _, w := range out.Write {
		if m := decodeLine(t, w); m["method"] == "thread/resume" {
			resumeID = m["id"]
		}
	}
	out = d.Read(line(map[string]any{"id": resumeID, "error": map[string]any{"code": -32600, "message": "no rollout found"}}))
	if len(out.Write) != 1 || decodeLine(t, out.Write[0])["method"] != "thread/start" {
		t.Fatalf("after a failed resume wrote %q", out.Write)
	}
	if n := find(out.Events, KindNotice); len(n) != 1 {
		t.Errorf("no notice of the fresh start: %+v", out.Events)
	}
}

func TestCodexCommandApproval(t *testing.T) {
	d, _ := handshake(t, Options{Cwd: "/workspace"})
	out := d.Read([]byte(`{"id":7,"method":"item/commandExecution/requestApproval","params":{"itemId":"cmd-2","threadId":"thr-1","turnId":"turn-1","startedAtMs":1,"command":"rm -rf build","reason":"clean the build"}}`))
	asks := find(out.Events, KindAsk)
	if len(asks) != 1 {
		t.Fatalf("events = %+v", out.Events)
	}
	a := asks[0]
	if a.Ask.Kind != AskPermission || a.Ask.Title != "Run rm -rf build" || a.Ask.Reason != "clean the build" || a.Ask.ToolID != "cmd-2" || !a.Ask.Always {
		t.Errorf("ask = %+v", a.Ask)
	}
	lines, err := d.Answer(a.ID, Answer{Allow: true, Always: true})
	if err != nil {
		t.Fatal(err)
	}
	var resp struct {
		ID     int `json:"id"`
		Result struct {
			Decision string `json:"decision"`
		} `json:"result"`
	}
	json.Unmarshal(lines[0], &resp)
	if resp.ID != 7 || resp.Result.Decision != "acceptForSession" {
		t.Errorf("answer = %s", lines[0])
	}
	if _, err := d.Answer(a.ID, Answer{}); err == nil {
		t.Error("answered twice")
	}
	d.Read([]byte(`{"id":"s8","method":"item/commandExecution/requestApproval","params":{"itemId":"cmd-3","threadId":"thr-1","turnId":"turn-1","startedAtMs":1,"command":"curl x"}}`))
	lines, _ = d.Answer("cs8", Answer{})
	if !strings.Contains(string(lines[0]), `"id":"s8"`) || !strings.Contains(string(lines[0]), `"decline"`) {
		t.Errorf("decline = %s", lines[0])
	}
}

func TestCodexFileChange(t *testing.T) {
	d, _ := handshake(t, Options{Cwd: "/workspace"})
	out := d.Read([]byte(`{"method":"item/started","params":{"item":{"type":"fileChange","id":"fc-1","status":"inProgress","changes":[{"path":"/workspace/a.go","kind":{"type":"update","move_path":null},"diff":"@@ -1 +1 @@\n-x\n+y\n"},{"path":"/workspace/new.txt","kind":{"type":"add"},"diff":"hello\nworld\n"}]},"threadId":"thr-1","turnId":"turn-1","startedAtMs":1}}`))
	tl := find(out.Events, KindTool)[0].Tool
	if tl.Kind != ToolEdit || tl.Title != "a.go and 1 more" || tl.Added != 3 || tl.Removed != 1 {
		t.Errorf("file change = %+v", tl)
	}
	if !strings.Contains(tl.Diff, "--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-x\n+y") || !strings.Contains(tl.Diff, "+++ b/new.txt\n@@ -0,0 +1,2 @@\n+hello\n+world") {
		t.Errorf("diff = %q", tl.Diff)
	}
	out = d.Read([]byte(`{"id":9,"method":"item/fileChange/requestApproval","params":{"itemId":"fc-1","threadId":"thr-1","turnId":"turn-1","startedAtMs":1}}`))
	a := find(out.Events, KindAsk)[0].Ask
	if a.Title != "Edit a.go and 1 more" || a.Detail != tl.Diff {
		t.Errorf("ask = %+v", a)
	}
}

func TestCodexUserInput(t *testing.T) {
	d, _ := handshake(t, Options{Cwd: "/workspace"})
	out := d.Read([]byte(`{"id":11,"method":"item/tool/requestUserInput","params":{"isBlocking":true,"itemId":"i","threadId":"thr-1","turnId":"turn-1","questions":[{"id":"db","header":"DB","question":"Which database?","options":[{"label":"SQLite","description":"file"}]}]}}`))
	a := find(out.Events, KindAsk)[0]
	if a.Ask.Kind != AskQuestion || a.Ask.Questions[0].ID != "db" {
		t.Fatalf("ask = %+v", a.Ask)
	}
	lines, _ := d.Answer(a.ID, Answer{Allow: true, Answers: map[string][]string{"db": {"SQLite"}}})
	if !strings.Contains(string(lines[0]), `"answers":{"db":{"answers":["SQLite"]}}`) {
		t.Errorf("answer = %s", lines[0])
	}
}

// Requests skybreaker doesn't serve get an error back, so codex never hangs.
func TestCodexRefusesUnknownRequests(t *testing.T) {
	d, _ := handshake(t, Options{Cwd: "/workspace"})
	out := d.Read([]byte(`{"id":12,"method":"account/chatgptAuthTokens/refresh","params":{}}`))
	if len(out.Write) != 1 || !strings.Contains(string(out.Write[0]), `"error"`) || len(out.Events) != 0 {
		t.Errorf("out = %+v", out)
	}
	out = d.Read([]byte(`{"id":13,"method":"mcpServer/elicitation/request","params":{}}`))
	if len(out.Write) != 1 || !strings.Contains(string(out.Write[0]), `"decline"`) {
		t.Errorf("elicitation = %s", out.Write)
	}
}

// codex-unauthorized.jsonl: codex app-server 0.163 with no ChatGPT login.
func TestCodexReplayUnauthorized(t *testing.T) {
	d := newCodex()
	d.Open(Options{Cwd: "/workspace"})
	events, _, resume := replay(t, d, "codex-unauthorized.jsonl")
	if resume != "01a15221-7d7b-7d21-beea-e02f01455ed8" {
		t.Errorf("resume = %q", resume)
	}
	if l := find(events, KindLogin); len(l) != 1 || !l[0].Login.Needed || l[0].Login.Terminal {
		t.Errorf("login = %+v", l)
	}
	turns := find(events, KindTurn)
	if len(turns) != 1 || turns[0].Turn.State != TurnFailed || !strings.Contains(turns[0].Turn.Error, "401") {
		t.Errorf("turns = %+v", turns)
	}
	if n := find(events, KindText); len(n) != 0 {
		t.Errorf("retries shown as text: %+v", n)
	}
}

func TestCodexLogin(t *testing.T) {
	d, _ := handshake(t, Options{Cwd: "/workspace"})
	out := d.Read([]byte(`{"id":3,"result":{"account":null,"requiresOpenaiAuth":true}}`))
	if l := find(out.Events, KindLogin); len(l) != 1 || !l[0].Login.Needed {
		t.Errorf("no account = %+v", out.Events)
	}
	lines, err := d.Login()
	if err != nil {
		t.Fatal(err)
	}
	m := decodeLine(t, lines[0])
	if m["method"] != "account/login/start" || m["params"].(map[string]any)["type"] != "chatgptDeviceCode" {
		t.Fatalf("login = %s", lines[0])
	}
	out = d.Read(line(map[string]any{"id": m["id"], "result": map[string]any{"type": "chatgptDeviceCode", "loginId": "l1", "userCode": "ABCD-1234", "verificationUrl": "https://auth.openai.com/codex/device"}}))
	l := find(out.Events, KindLogin)[0].Login
	if l.Code != "ABCD-1234" || l.URL != "https://auth.openai.com/codex/device" {
		t.Errorf("device code = %+v", l)
	}
	out = d.Read([]byte(`{"method":"account/login/completed","params":{"loginId":"l1","success":true}}`))
	if l := find(out.Events, KindLogin)[0].Login; !l.Done || l.Error != "" {
		t.Errorf("completed = %+v", l)
	}
}

func TestCodexModes(t *testing.T) {
	d, _ := handshake(t, Options{Cwd: "/workspace", Mode: ModeAsk})
	if lines, err := d.SetMode(ModeAuto); err != nil || len(lines) != 0 {
		t.Fatalf("set mode = %q, %v", lines, err)
	}
	lines, _ := d.Send("go")
	if p := decodeLine(t, lines[0])["params"].(map[string]any); p["approvalPolicy"] != "never" {
		t.Errorf("auto mode sent %v", p["approvalPolicy"])
	}
	if _, err := d.SetMode(ModePlan); err == nil {
		t.Error("codex took plan mode")
	}
}

func TestCodexTurnEndWithdrawsAsks(t *testing.T) {
	d, _ := handshake(t, Options{Cwd: "/workspace"})
	d.Read([]byte(`{"id":7,"method":"item/commandExecution/requestApproval","params":{"itemId":"cmd-2","threadId":"thr-1","turnId":"turn-1","startedAtMs":1,"command":"ls"}}`))
	d.Read([]byte(`{"method":"item/started","params":{"item":{"type":"commandExecution","id":"cmd-2","command":"ls","commandActions":[],"status":"inProgress"},"threadId":"thr-1","turnId":"turn-1","startedAtMs":1}}`))
	out := d.Read([]byte(`{"method":"turn/completed","params":{"threadId":"thr-1","turn":{"id":"turn-1","items":[],"status":"interrupted"}}}`))
	if a := find(out.Events, KindAnswered); len(a) != 1 || a[0].ID != "c7" {
		t.Errorf("answered = %+v", out.Events)
	}
	if tl := find(out.Events, KindTool); len(tl) != 1 || tl[0].Tool.Status != StatusDeclined {
		t.Errorf("tools = %+v", out.Events)
	}
	if tr := find(out.Events, KindTurn)[0].Turn; tr.State != TurnInterrupted {
		t.Errorf("turn = %+v", tr)
	}
}

func TestCodexIgnoresJunk(t *testing.T) {
	d, _ := handshake(t, Options{Cwd: "/workspace"})
	for _, l := range []string{``, `nope`, `{"method":"thread/tokenUsage/updated","params":{}}`, `{"method":"item/completed","params":{"item":"x"}}`,
		`{"id":999,"result":{}}`} {
		if out := d.Read([]byte(l)); len(out.Events) != 0 {
			t.Errorf("%s → %+v", l, out.Events)
		}
	}
}

// A kind of item this driver has never seen still shows, as a plain card.
func TestCodexUnknownItemIsGeneric(t *testing.T) {
	d, _ := handshake(t, Options{Cwd: "/workspace"})
	out := d.Read([]byte(`{"method":"item/completed","params":{"item":{"type":"futureThing","id":"f"}}}`))
	if tl := find(out.Events, KindTool); len(tl) != 1 || tl[0].Tool.Kind != ToolOther || tl[0].Tool.Title != "futureThing" || tl[0].Tool.Status != StatusDone {
		t.Errorf("events = %+v", out.Events)
	}
}

func FuzzCodexRead(f *testing.F) {
	f.Add([]byte(`{"method":"item/started","params":{"item":{"type":"fileChange","id":"x","changes":[{"path":"a","kind":{"type":"add"},"diff":"x"}]}}}`))
	f.Add([]byte(`{"id":1,"method":"item/tool/requestUserInput","params":{"questions":[{}]}}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		d := newCodex()
		d.Open(Options{})
		d.Read(b)
	})
}

// Signing in straight after start: codex answers account/read (no account)
// after the device code, which mustn't hide the code again.
func TestCodexLoginBeforeAccountRead(t *testing.T) {
	d := newCodex()
	d.Open(Options{Cwd: "/workspace"})
	lines, _ := d.Login()
	loginID := decodeLine(t, lines[0])["id"]
	out := d.Read([]byte(`{"id":1,"result":{}}`))
	var acctID any
	for _, w := range out.Write {
		if m := decodeLine(t, w); m["method"] == "account/read" {
			acctID = m["id"]
		}
	}
	d.Read(line(map[string]any{"id": loginID, "result": map[string]any{"type": "chatgptDeviceCode", "loginId": "l", "userCode": "ABCD-1234", "verificationUrl": "https://auth.openai.com/codex/device"}}))
	out = d.Read(line(map[string]any{"id": acctID, "result": map[string]any{"account": nil}}))
	if l := find(out.Events, KindLogin); len(l) != 0 {
		t.Errorf("account/read after the code: %+v", l)
	}
}

// A message sent before codex says which turn it started waits for that
// turn's ID, then steers it; codex refuses a steer for any other turn.
func TestCodexSteersOnlyAKnownTurn(t *testing.T) {
	d, _ := handshake(t, Options{Cwd: "/workspace"})
	lines, _ := d.Send("first")
	startID := decodeLine(t, lines[0])["id"]
	if lines, _ := d.Send("second"); len(lines) != 0 {
		t.Fatalf("steered a turn with no ID yet: %s", lines)
	}
	out := d.Read(line(map[string]any{"id": startID, "result": map[string]any{"turn": map[string]any{"id": "turn-7"}}}))
	if len(out.Write) != 1 {
		t.Fatalf("held message not sent: %q", out.Write)
	}
	m := decodeLine(t, out.Write[0])
	if m["method"] != "turn/steer" || m["params"].(map[string]any)["expectedTurnId"] != "turn-7" || !strings.Contains(string(out.Write[0]), "second") {
		t.Fatalf("flushed = %s", out.Write[0])
	}

	// If the turn never starts, what was held starts the next one.
	lines, _ = d.Send("third")
	if len(lines) != 1 || decodeLine(t, lines[0])["method"] != "turn/steer" {
		t.Fatalf("steer the running turn = %q", lines)
	}
	d2, _ := handshake(t, Options{Cwd: "/workspace"})
	lines, _ = d2.Send("a")
	startID = decodeLine(t, lines[0])["id"]
	d2.Send("b")
	out = d2.Read(line(map[string]any{"id": startID, "error": map[string]any{"code": -1, "message": "busy"}}))
	var started bool
	for _, w := range out.Write {
		if m := decodeLine(t, w); m["method"] == "turn/start" && strings.Contains(string(w), `"b"`) {
			started = true
		}
	}
	if !started {
		t.Fatalf("held message lost when the turn failed: %q", out.Write)
	}
}

// A mode switched mid-turn holds at once: codex keeps the turn's old
// policy, so the driver answers what the new mode allows itself.
func TestCodexModeAppliesMidTurn(t *testing.T) {
	cmd := []byte(`{"id":8,"method":"item/commandExecution/requestApproval","params":{"itemId":"cmd-3","threadId":"thr-1","turnId":"turn-1","command":"go test ./..."}}`)
	edit := []byte(`{"id":9,"method":"item/fileChange/requestApproval","params":{"itemId":"fc-1","threadId":"thr-1","turnId":"turn-1"}}`)
	decision := func(out Out) string {
		for _, w := range out.Write {
			var r struct {
				Result struct {
					Decision string `json:"decision"`
				} `json:"result"`
			}
			if json.Unmarshal(w, &r) == nil && r.Result.Decision != "" {
				return r.Result.Decision
			}
		}
		return ""
	}
	d, _ := handshake(t, Options{Cwd: "/workspace", Mode: ModeAsk})
	d.SetMode(ModeAuto)
	out := d.Read(cmd)
	if len(find(out.Events, KindAsk)) != 0 || decision(out) != "accept" {
		t.Fatalf("auto still asks: %+v %q", out.Events, out.Write)
	}
	d.SetMode(ModeEdits)
	if out := d.Read(edit); len(find(out.Events, KindAsk)) != 0 || decision(out) != "accept" {
		t.Fatalf("edits asks for an edit: %+v", out.Events)
	}
	if out := d.Read(cmd); len(find(out.Events, KindAsk)) != 1 {
		t.Fatalf("edits didn't ask for a command: %+v", out.Events)
	}
	d.SetMode(ModeAsk)
	if out := d.Read(edit); len(find(out.Events, KindAsk)) != 1 {
		t.Fatalf("ask mode didn't ask: %+v", out.Events)
	}
}
