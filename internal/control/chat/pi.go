package chat

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

// pi drives pi's RPC mode: commands in, responses and session events out,
// one JSON object per line. pi has no permission prompts; the sandbox and
// broker are its boundary. Its subagents come from extensions, as tools
// whose cards run until the subagent returns.
type pi struct {
	inTurn     bool // the chat is working: from a message sent, or a run started, to its end
	running    bool // pi's run goes on: from agent_start to agent_settled
	compacting bool // a compaction we asked for, which its response ends
	abort      bool // we stopped this turn
	started    time.Time
	message    string       // the assistant message streaming now
	messages   int          // assistant messages seen, so no two share an ID
	emitted    map[int]bool // the streaming message's blocks already shown
	tools      map[string]*Tool
	asks       map[string]piAsk
	failed     string // the run's last assistant error, if its last message failed
	window     int64
	resume     bool                // continuing a conversation, which pi starts afresh if it can't find it
	current    string              // the model pi runs, provider/id
	models     []Model             // the models pi listed
	levels     map[string][]string // a model's thinking levels, for those pi has run
}

type piAsk struct {
	id      string // pi's request ID
	method  string
	run     bool      // asked during a run, which withdraws it when it settles
	expires time.Time // when pi answers it by itself, if it does
}

// piAnswer is the ID of the one question an extension's dialog asks.
const piAnswer = "answer"

func newPi() *pi {
	return &pi{emitted: map[int]bool{}, tools: map[string]*Tool{}, asks: map[string]piAsk{}}
}

func (p *pi) Modes() []string { return []string{ModeAuto} }

// Argv runs pi through skybreaker-pi, which puts Skybreaker's MCP server
// and model servers into pi's config first. o.MCP goes unused: that config
// is shared, so the server learns the chat from $SKYBREAKER_SESSION.
func (p *pi) Argv(o Options) []string {
	argv := []string{"skybreaker-pi", "--mode", "rpc"}
	if o.Model != "" {
		argv = append(argv, "--model", o.Model)
	}
	if o.Effort != "" {
		argv = append(argv, "--thinking", o.Effort)
	}
	switch {
	case o.Resume != "" && o.Fork:
		argv = append(argv, "--fork", o.Resume)
	case o.Resume != "":
		argv = append(argv, "--session-id", o.Resume)
	case o.NewID != "":
		argv = append(argv, "--session-id", o.NewID)
	}
	return argv
}

// Env turns off pi's update check and telemetry, which no sandbox may reach.
func (p *pi) Env() map[string]string {
	return map[string]string{"PI_SKIP_VERSION_CHECK": "1", "PI_TELEMETRY": "0"}
}

func (p *pi) Open(o Options) [][]byte {
	p.resume = o.Resume != "" && !o.Fork
	return [][]byte{line(map[string]string{"type": "get_state"}), line(map[string]string{"type": "get_available_models"}),
		line(map[string]string{"type": "get_available_thinking_levels"}), line(map[string]string{"type": "get_commands"})}
}

// Send always says to steer: pi reads it only during a run, which may have
// started without our having read so yet.
func (p *pi) Send(text string, images ...Image) ([][]byte, error) {
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("empty message")
	}
	if !p.inTurn {
		p.begin()
	}
	m := map[string]any{"type": "prompt", "message": text, "streamingBehavior": "steer"}
	if len(images) > 0 {
		var imgs []any
		for _, im := range images {
			imgs = append(imgs, map[string]string{"type": "image", "mimeType": im.Mime, "data": base64.StdEncoding.EncodeToString(im.Data)})
		}
		m["images"] = imgs
	}
	return [][]byte{line(m)}, nil
}

func (p *pi) begin() {
	p.inTurn, p.started, p.failed, p.abort = true, time.Now(), "", false
}

// Interrupt drops queued messages too: otherwise pi goes on to them.
func (p *pi) Interrupt() [][]byte {
	if !p.inTurn {
		return nil
	}
	p.abort = true
	return [][]byte{line(map[string]string{"type": "clear_queue"}), line(map[string]string{"type": "abort"})}
}

func (p *pi) SetMode(mode string) ([][]byte, error) { return nil, validMode(p, mode) }

// SetModel takes a model as pi lists it here, provider/id.
func (p *pi) SetModel(model string) ([][]byte, error) {
	if err := validModel(model); err != nil {
		return nil, err
	}
	provider, id, ok := strings.Cut(model, "/")
	if !ok || provider == "" || id == "" {
		return nil, fmt.Errorf("model %q is not provider/model", model)
	}
	return [][]byte{line(map[string]string{"type": "set_model", "provider": provider, "modelId": id})}, nil
}

// SetEffort sets pi's thinking level, which pi fits to the model and
// reports.
func (p *pi) SetEffort(effort string) ([][]byte, error) {
	if err := validEffort(effort); err != nil {
		return nil, err
	}
	return [][]byte{line(map[string]string{"type": "set_thinking_level", "level": effort})}, nil
}

// modelsInfo lists pi's models, each with its levels if pi has said them.
// pi says them only for the model it runs.
func (p *pi) modelsInfo() *Info {
	ms := slices.Clone(p.models)
	for i := range ms {
		ms[i].Efforts, ms[i].EffortsKnown = p.levels[ms[i].ID]
	}
	return &Info{Models: ms}
}

// Compact runs as a turn of its own, which pi's response ends.
func (p *pi) Compact() ([][]byte, error) {
	p.begin()
	p.compacting = true
	return [][]byte{line(map[string]string{"type": "compact"})}, nil
}

func (p *pi) Login() ([][]byte, error) { return nil, ErrTerminalLogin }

func (p *pi) Answer(id string, a Answer) ([][]byte, error) {
	ask, ok := p.asks[id]
	if !ok {
		return nil, errNoAsk
	}
	delete(p.asks, id)
	r := map[string]any{"type": "extension_ui_response", "id": ask.id}
	switch {
	case ask.method == "confirm":
		r["confirmed"] = a.Allow
	case a.Allow && len(a.Answers[piAnswer]) > 0:
		r["value"] = a.Answers[piAnswer][0]
	default:
		r["cancelled"] = true
	}
	return [][]byte{line(r)}, nil
}

type piLine struct {
	Type    string          `json:"type"`
	Command string          `json:"command"`
	Success bool            `json:"success"`
	Error   string          `json:"error"`
	Data    json.RawMessage `json:"data"`
	Message json.RawMessage `json:"message"`
	Update  *struct {
		Type     string      `json:"type"`
		Index    int         `json:"contentIndex"`
		Delta    string      `json:"delta"`
		Content  string      `json:"content"`
		ToolCall *piToolCall `json:"toolCall"`
	} `json:"assistantMessageEvent"`
	Level      string          `json:"level"` // thinking_level_changed
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Args       json.RawMessage `json:"args"`
	Result     json.RawMessage `json:"result"` // a tool's piResult, or a compaction's
	Partial    *piResult       `json:"partialResult"`
	IsError    bool            `json:"isError"`
	Aborted    bool            `json:"aborted"`
	// extension_ui_request; its message is Message, a string
	ID      string   `json:"id"`
	Method  string   `json:"method"`
	Title   string   `json:"title"`
	Options []string `json:"options"`
	Hint    string   `json:"placeholder"`
	Timeout int64    `json:"timeout"` // milliseconds
}

type piToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"arguments"`
}

type piResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Structured json.RawMessage `json:"structuredContent"`
}

func (r *piResult) text() string {
	var b strings.Builder
	for _, c := range r.Content {
		if c.Type == "text" {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

type piMessage struct {
	Role      string `json:"role"`
	Timestamp int64  `json:"timestamp"`
	Content   []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Thinking string `json:"thinking"`
	} `json:"content"`
	Usage *struct {
		Input      int64 `json:"input"`
		Output     int64 `json:"output"`
		CacheRead  int64 `json:"cacheRead"`
		CacheWrite int64 `json:"cacheWrite"`
		Total      int64 `json:"totalTokens"`
	} `json:"usage"`
	StopReason string `json:"stopReason"`
	Error      string `json:"errorMessage"`
}

type piModel struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Window   int64  `json:"contextWindow"`
}

func (m piModel) ref() string { return m.Provider + "/" + m.ID }

// piShort is a line of pi's, or an extension's, fit for a title or label.
func piShort(s string) string { return clip(firstLine(s), 300) }

// Read also withdraws dialogs pi has since answered by itself.
func (p *pi) Read(b []byte) Out {
	now := time.Now().UTC()
	var expired []Event
	for _, id := range slices.Sorted(maps.Keys(p.asks)) {
		if e := p.asks[id].expires; !e.IsZero() && now.After(e) {
			delete(p.asks, id)
			expired = append(expired, Event{Time: now, Kind: KindAnswered, ID: id, Text: "withdrawn"})
		}
	}
	out := p.read(b, now)
	out.Events = append(expired, out.Events...)
	return out
}

func (p *pi) read(b []byte, now time.Time) Out {
	var l piLine
	if json.Unmarshal(b, &l) != nil {
		return Out{}
	}
	ev := func(e Event) Event { e.Time = now; return e }
	var out Out
	switch l.Type {
	case "response":
		return p.response(l, now)
	case "agent_start":
		p.running = true
		switch {
		case !p.inTurn:
			// A run nobody sent, as an extension's follow-up.
			p.begin()
			out.Events = append(out.Events, ev(Event{Kind: KindTurn, Turn: &Turn{State: TurnWorking}}))
		case p.abort:
			// pi drops an abort that comes before its run starts.
			out.Write = append(out.Write, line(map[string]string{"type": "abort"}))
		}
	case "agent_settled":
		return p.settled(l, now)
	case "thinking_level_changed":
		if validEffort(l.Level) == nil {
			out.Events = append(out.Events, ev(Event{Kind: KindInfo, Info: &Info{Effort: l.Level}}))
		}
	case "message_start", "message_end":
		var m piMessage
		if json.Unmarshal(l.Message, &m) != nil || m.Role != "assistant" {
			return out
		}
		if l.Type == "message_start" {
			p.messages++
			p.message = fmt.Sprintf("pi%d.%d", m.Timestamp, p.messages)
			clear(p.emitted)
			return out
		}
		return p.messageEnd(m, now)
	case "message_update":
		out.Events = p.update(l, now)
	case "tool_execution_start", "tool_execution_update", "tool_execution_end":
		out.Events = p.tool(l, now)
	case "compaction_end":
		var r *struct {
			After int64 `json:"estimatedTokensAfter"`
		}
		if json.Unmarshal(l.Result, &r) == nil && r != nil {
			out.Events = append(out.Events, ev(Event{Kind: KindNotice, Text: "The conversation was compacted."}),
				ev(Event{Kind: KindInfo, Info: &Info{Context: &Context{Used: r.After, Window: p.window}}}))
		}
	case "extension_ui_request":
		return p.dialog(l, now)
	}
	return out
}

// response reads pi's answer to a command. A prompt pi refused or handled
// itself ends the turn only if no run or compaction carries it on.
func (p *pi) response(l piLine, now time.Time) Out {
	var out Out
	ev := func(e Event) { e.Time = now; out.Events = append(out.Events, e) }
	alone := p.inTurn && !p.running && !p.compacting
	if !l.Success {
		switch {
		case l.Command == "compact" || l.Command == "prompt" && alone:
			turn := &Turn{State: TurnFailed, Error: clip(l.Error, 4096)}
			if p.abort {
				turn = &Turn{State: TurnInterrupted}
			}
			p.end()
			ev(Event{Kind: KindTurn, Turn: turn})
		case l.Command == "prompt":
			ev(Event{Kind: KindNotice, Text: "pi didn't take that message: " + piShort(l.Error)})
		case l.Command == "set_model":
			ev(Event{Kind: KindNotice, Text: "The model didn't change: " + piShort(l.Error)})
		}
		return out
	}
	switch l.Command {
	case "get_state":
		var s struct {
			Model     *piModel `json:"model"`
			SessionID string   `json:"sessionId"`
			Messages  int      `json:"messageCount"`
			Thinking  string   `json:"thinkingLevel"`
		}
		if json.Unmarshal(l.Data, &s) != nil {
			return out
		}
		out.Resume = s.SessionID
		if p.resume && s.Messages == 0 {
			ev(Event{Kind: KindNotice, Text: "The earlier conversation couldn't be resumed, so this is a new one."})
		}
		p.resume = false
		info := &Info{Mode: ModeAuto}
		// With none configured, pi reports a placeholder with no window.
		if m := s.Model; m != nil && m.Window > 0 && validModel(m.ref()) == nil {
			p.window, info.Model, p.current = m.Window, m.ref(), m.ref()
		}
		if validEffort(s.Thinking) == nil {
			info.Effort = s.Thinking
		}
		ev(Event{Kind: KindInfo, Info: info})
	case "set_model":
		var m piModel
		if json.Unmarshal(l.Data, &m) == nil && m.ID != "" && validModel(m.ref()) == nil {
			p.window, p.current = m.Window, m.ref()
			ev(Event{Kind: KindInfo, Info: &Info{Model: m.ref()}})
			out.Write = append(out.Write, line(map[string]string{"type": "get_available_thinking_levels"}))
		}
	case "get_available_thinking_levels":
		var r struct {
			Levels []string `json:"levels"`
		}
		if json.Unmarshal(l.Data, &r) != nil || p.current == "" {
			return out
		}
		var levels []string
		for _, lv := range r.Levels {
			if validEffort(lv) == nil {
				levels = append(levels, lv)
			}
		}
		if p.levels == nil {
			p.levels = map[string][]string{}
		}
		p.levels[p.current] = levels
		if len(p.models) > 0 {
			ev(Event{Kind: KindInfo, Info: p.modelsInfo()})
		}
	case "get_available_models":
		var r struct {
			Models []piModel `json:"models"`
		}
		json.Unmarshal(l.Data, &r)
		var models []Model
		for _, m := range r.Models {
			if validModel(m.ref()) == nil {
				models = append(models, Model{ID: m.ref(), Name: piShort(m.Name)})
			}
		}
		if len(models) > 0 {
			p.models = models
			ev(Event{Kind: KindInfo, Info: p.modelsInfo()})
		}
	case "get_commands":
		var r struct {
			Commands []struct {
				Name string `json:"name"`
			} `json:"commands"`
		}
		json.Unmarshal(l.Data, &r)
		cmds := []string{}
		for _, c := range r.Commands {
			if c.Name != "" && len(cmds) < 500 {
				cmds = append(cmds, piShort(c.Name))
			}
		}
		ev(Event{Kind: KindInfo, Info: &Info{Commands: cmds}})
	case "prompt":
		var r struct {
			Disposition string `json:"disposition"`
		}
		// Handled, as by an extension's command: no run follows.
		if json.Unmarshal(l.Data, &r) == nil && r.Disposition == "handled" && alone {
			p.end()
			ev(Event{Kind: KindTurn, Turn: &Turn{State: TurnDone}})
		}
	case "compact":
		turn := &Turn{State: TurnDone, DurationMS: time.Since(p.started).Milliseconds()}
		p.end()
		ev(Event{Kind: KindTurn, Turn: turn})
	}
	return out
}

// end closes the turn.
func (p *pi) end() {
	p.inTurn, p.compacting, p.abort, p.failed, p.message = false, false, false, "", ""
}

// update streams the assistant's text, and ends its text and thinking
// blocks. A tool call's card starts once its arguments are complete.
func (p *pi) update(l piLine, now time.Time) []Event {
	u := l.Update
	if u == nil || p.message == "" {
		return nil
	}
	id := fmt.Sprintf("%s:%d", p.message, u.Index)
	switch u.Type {
	case "text_delta":
		if u.Delta != "" {
			return []Event{{Time: now, Kind: KindDelta, ID: id, Text: u.Delta}}
		}
	case "text_end", "thinking_end":
		p.emitted[u.Index] = true
		kind := KindText
		if u.Type == "thinking_end" {
			kind = KindThinking
		}
		if text := strings.TrimSpace(u.Content); text != "" {
			return []Event{{Time: now, Kind: kind, ID: id, Text: clip(text, maxText)}}
		}
	case "toolcall_end":
		if tc := u.ToolCall; tc != nil && tc.ID != "" && p.tools[tc.ID] == nil {
			t := piTool(tc.Name, tc.Args)
			p.tools[tc.ID] = t
			cp := *t
			return []Event{{Time: now, Kind: KindTool, ID: tc.ID, Tool: &cp}}
		}
	}
	return nil
}

// messageEnd counts what the message used, and shows any of its text a
// provider didn't stream.
func (p *pi) messageEnd(m piMessage, now time.Time) Out {
	var out Out
	for i, c := range m.Content {
		if p.emitted[i] {
			continue
		}
		id := fmt.Sprintf("%s:%d", p.message, i)
		switch {
		case c.Type == "text" && strings.TrimSpace(c.Text) != "":
			out.Events = append(out.Events, Event{Time: now, Kind: KindText, ID: id, Text: clip(strings.TrimSpace(c.Text), maxText)})
		case c.Type == "thinking" && strings.TrimSpace(c.Thinking) != "":
			out.Events = append(out.Events, Event{Time: now, Kind: KindThinking, ID: id, Text: clip(strings.TrimSpace(c.Thinking), maxText)})
		}
	}
	p.message = ""
	p.failed = ""
	if m.StopReason == "error" || m.StopReason == "aborted" {
		p.failed = m.Error
		if p.failed == "" {
			p.failed = "The model's reply failed."
		}
	}
	if u := m.Usage; u != nil && u.Total > 0 {
		out.Usage = &Usage{Input: u.Input + u.CacheWrite, Cached: u.CacheRead, Output: u.Output}
		out.Events = append(out.Events, Event{Time: now, Kind: KindInfo, Info: &Info{Context: &Context{Used: u.Total, Window: p.window}}})
	}
	return out
}

func (p *pi) tool(l piLine, now time.Time) []Event {
	if l.ToolCallID == "" {
		return nil
	}
	t, known := p.tools[l.ToolCallID]
	if !known {
		t = piTool(l.ToolName, l.Args)
		p.tools[l.ToolCallID] = t
	}
	before := t.Output
	switch l.Type {
	case "tool_execution_update":
		// pi's own tools send what they have so far, not what's new.
		if l.Partial != nil {
			if text := l.Partial.text(); text != "" {
				t.Output = clipTail(text, maxOutput)
			}
		}
	case "tool_execution_end":
		delete(p.tools, l.ToolCallID)
		t.Status = StatusDone
		var r piResult
		if json.Unmarshal(l.Result, &r) == nil {
			t.Output = clipTail(r.text(), maxOutput)
			var s struct {
				Exit *int `json:"exit_code"`
			}
			if json.Unmarshal(r.Structured, &s) == nil && s.Exit != nil {
				t.Exit = s.Exit
			}
		}
		switch {
		case l.IsError && p.abort:
			t.Status = StatusDeclined
		case l.IsError || t.Exit != nil && *t.Exit != 0:
			t.Status = StatusFailed
		}
	}
	if known && t.Status == StatusRunning && t.Output == before {
		return nil
	}
	cp := *t
	return []Event{{Time: now, Kind: KindTool, ID: l.ToolCallID, Tool: &cp}}
}

// settled ends the run: pi does nothing more until it's sent something.
// The run's dialogs still open are withdrawn, and cards still running end.
func (p *pi) settled(l piLine, now time.Time) Out {
	var out Out
	turn := &Turn{State: TurnDone}
	if p.inTurn {
		turn.DurationMS = time.Since(p.started).Milliseconds()
	}
	switch {
	case l.Aborted || p.abort:
		turn.State = TurnInterrupted
	case p.failed != "":
		turn.State, turn.Error = TurnFailed, clip(p.failed, 4096)
		low := strings.ToLower(p.failed)
		if strings.Contains(low, "401") || strings.Contains(low, "authentication") || strings.Contains(low, "api key") || strings.Contains(low, "no credentials") {
			out.Events = append(out.Events, Event{Time: now, Kind: KindLogin, Login: &Login{Needed: true, Terminal: true}})
		}
	}
	for _, id := range slices.Sorted(maps.Keys(p.asks)) {
		if !p.asks[id].run {
			continue // an extension's own, outside any run
		}
		out.Write = append(out.Write, line(map[string]any{"type": "extension_ui_response", "id": p.asks[id].id, "cancelled": true}))
		out.Events = append(out.Events, Event{Time: now, Kind: KindAnswered, ID: id, Text: "withdrawn"})
		delete(p.asks, id)
	}
	for _, id := range slices.Sorted(maps.Keys(p.tools)) {
		t := p.tools[id]
		t.Status = StatusFailed
		if turn.State == TurnInterrupted {
			t.Status = StatusDeclined
		}
		cp := *t
		out.Events = append(out.Events, Event{Time: now, Kind: KindTool, ID: id, Tool: &cp})
		delete(p.tools, id)
	}
	p.running = false
	p.end()
	out.Events = append(out.Events, Event{Time: now, Kind: KindTurn, Turn: turn})
	return out
}

// dialog shows an extension's dialog as an ask, and a notification as a
// notice. The rest of pi's extension UI is for its terminal.
func (p *pi) dialog(l piLine, now time.Time) Out {
	var text string
	json.Unmarshal(l.Message, &text)
	if l.ID == "" || len(l.ID) > 128 {
		return Out{}
	}
	a := &Ask{Kind: AskQuestion, Title: "A question for you"}
	q := Question{ID: piAnswer, Question: piShort(l.Title), Header: piShort(l.Hint)}
	switch l.Method {
	case "notify":
		if text == "" {
			return Out{}
		}
		return Out{Events: []Event{{Time: now, Kind: KindNotice, Text: clip(text, 4096)}}}
	case "confirm":
		a = &Ask{Kind: AskPermission, Title: piShort(l.Title), Detail: clip(text, 4096)}
	case "select":
		for _, o := range l.Options[:min(len(l.Options), 50)] {
			q.Options = append(q.Options, Option{Label: piShort(o)})
		}
		a.Questions = []Question{q}
	case "input", "editor":
		a.Questions = []Question{q}
	default:
		return Out{}
	}
	id := "x" + l.ID
	ask := piAsk{id: l.ID, method: l.Method, run: p.running}
	if l.Timeout > 0 {
		ask.expires = now.Add(time.Duration(l.Timeout) * time.Millisecond)
	}
	p.asks[id] = ask
	return Out{Events: []Event{{Time: now, Kind: KindAsk, ID: id, Ask: a}}}
}

// piTool is a card for one of pi's built-in tools, or any other by its
// name and first argument. A tool named for subagents shows as one.
func piTool(name string, raw json.RawMessage) *Tool {
	var in map[string]any
	json.Unmarshal(raw, &in)
	str := func(k string) string { s, _ := in[k].(string); return s }
	t := &Tool{Name: name, Status: StatusRunning, Input: clipJSON(raw)}
	switch name {
	case "bash":
		t.Kind, t.Title = ToolCommand, firstLine(str("command"))
	case "read":
		t.Kind, t.Title = ToolRead, Rel(str("path"))
	case "edit":
		t.Kind, t.Title = ToolEdit, Rel(str("path"))
		edits, _ := in["edits"].([]any)
		var b strings.Builder
		for _, e := range edits {
			m, _ := e.(map[string]any)
			old, _ := m["oldText"].(string)
			nw, _ := m["newText"].(string)
			b.WriteString(replaceDiff(t.Title, old, nw))
		}
		t.Diff = clip(b.String(), maxDiff)
		t.Added, t.Removed = countDiff(t.Diff)
	case "write":
		t.Kind, t.Title = ToolEdit, Rel(str("path"))
		t.Diff = clip(wholeFile(t.Title, str("content"), '+'), maxDiff)
		t.Added, t.Removed = countDiff(t.Diff)
	case "grep", "find":
		t.Kind, t.Title = ToolSearch, str("pattern")
		if p := str("path"); p != "" {
			t.Title += " in " + Rel(p)
		}
	case "ls":
		t.Kind, t.Title = ToolSearch, Rel(str("path"))
	default:
		otherTool(t, name, in)
		if name == "subagent" || name == "agent" || name == "task" {
			t.Kind = ToolAgent
		}
	}
	if len(t.Title) > 300 {
		t.Title = clip(t.Title, 300)
	}
	return t
}
