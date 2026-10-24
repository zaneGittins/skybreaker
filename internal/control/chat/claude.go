package chat

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// claude drives Claude Code's print mode with stream-json in and out, the
// protocol the Agent SDK speaks. Permission prompts come to us as
// can_use_tool control requests.
type claude struct {
	asks        map[string]claudeAsk // control request ID → what it asked
	tools       map[string]*Tool     // tool_use ID → its card
	agents      map[string]*Tool     // a background subagent's tool_use ID → its card, running past the turn
	ended       map[string]*Tool     // background subagents ended by the task list, for a notification that comes late
	tasks       map[string]string    // Claude's task ID for a background subagent → its tool_use ID
	inTurn      bool                 // a turn runs: between a message going in and its result
	ran         bool                 // a turn has ended in this process
	blocks      map[string]int       // message ID → text blocks finished in it
	message     string               // the message streaming now
	draft       string               // the text block streaming now, if any
	interrupted bool
	seq         int
	sent        map[string]string // our control request ID → what it set (set_model's model), or its subtype
	mode        string            // the permission mode Claude is in, as far as we know
	model       string            // the model Claude says it runs
	counted     string            // the last message whose usage was reported
	used        int64             // tokens in the context as of the last message
	window      int64             // the model's context window, once a result says
	spent       map[string]Usage  // per model, the tokens results have counted
}

type claudeAsk struct {
	tool        string
	input       json.RawMessage
	suggestions json.RawMessage
	questions   []Question
	toolID      string // the tool call it asks about
}

func newClaude() *claude {
	return &claude{asks: map[string]claudeAsk{}, tools: map[string]*Tool{}, agents: map[string]*Tool{}, ended: map[string]*Tool{}, tasks: map[string]string{},
		blocks: map[string]int{}, sent: map[string]string{}}
}

var claudeModes = map[string]string{ModeAsk: "default", ModeEdits: "acceptEdits", ModeAuto: "bypassPermissions", ModePlan: "plan"}

// modeNames are the modes as the UI names them.
var modeNames = map[string]string{ModeAsk: "Ask", ModeEdits: "Edits", ModeAuto: "Auto", ModePlan: "Plan"}

func (c *claude) Modes() []string { return []string{ModeAsk, ModeEdits, ModePlan, ModeAuto} }

func (c *claude) Argv(o Options) []string {
	mode := claudeModes[o.Mode]
	if mode == "" {
		mode = "default"
	}
	c.mode = o.Mode
	if c.mode == "" {
		c.mode = ModeAsk
	}
	// --allow-dangerously-skip-permissions doesn't skip anything: it lets
	// the user switch to Auto later, which Claude otherwise refuses for a
	// session it didn't launch in that mode. gVisor and the broker bound
	// what Auto can do.
	argv := []string{"claude", "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--include-partial-messages", "--permission-prompt-tool", "stdio", "--permission-mode", mode,
		"--allow-dangerously-skip-permissions"}
	if o.MCP != "" {
		argv = append(argv, "--mcp-config", MCPConfig(o.MCP))
	}
	if o.Model != "" {
		argv = append(argv, "--model="+o.Model)
	}
	if o.Effort != "" {
		argv = append(argv, "--effort="+o.Effort)
	}
	switch {
	case o.Resume != "":
		argv = append(argv, "--resume", o.Resume)
		if o.Fork {
			argv = append(argv, "--fork-session")
		}
	case o.NewID != "":
		argv = append(argv, "--session-id", o.NewID)
	}
	return argv
}

// Env: Claude Code refuses bypassPermissions as root outside a sandbox
// unless told it is in one, which it is.
func (c *claude) Env() map[string]string { return map[string]string{"IS_SANDBOX": "1"} }

func line(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func (c *claude) control(req map[string]any) []byte {
	c.seq++
	id := "skybreaker-" + strconv.Itoa(c.seq)
	switch req["subtype"] {
	case "initialize":
		c.sent[id] = "initialize"
	case "set_model":
		c.sent[id] = "set_model " + req["model"].(string)
	case "set_permission_mode":
		c.sent[id] = "set_mode " + req["skybreaker_mode"].(string)
		delete(req, "skybreaker_mode")
	case "get_settings":
		c.sent[id] = "get_settings"
	}
	return line(map[string]any{"type": "control_request", "request_id": id, "request": req})
}

func (c *claude) Open(Options) [][]byte {
	return [][]byte{c.control(map[string]any{"subtype": "initialize", "hooks": nil})}
}

func (c *claude) Send(text string, images ...Image) ([][]byte, error) {
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("empty message")
	}
	c.interrupted = false
	c.inTurn = true
	var content any = text
	if len(images) > 0 {
		blocks := []any{map[string]any{"type": "text", "text": text}}
		for _, im := range images {
			blocks = append(blocks, map[string]any{"type": "image", "source": map[string]any{
				"type": "base64", "media_type": im.Mime, "data": base64.StdEncoding.EncodeToString(im.Data)}})
		}
		content = blocks
	}
	return [][]byte{line(map[string]any{
		"type": "user", "message": map[string]any{"role": "user", "content": content}, "parent_tool_use_id": nil,
	})}, nil
}

// Compact runs Claude Code's own /compact, which stream-json takes as a
// message. A compact_boundary reports what's left.
func (c *claude) Compact() ([][]byte, error) { return c.Send("/compact") }

func (c *claude) Interrupt() [][]byte {
	c.interrupted = true
	return [][]byte{c.control(map[string]any{"subtype": "interrupt"})}
}

func (c *claude) SetMode(mode string) ([][]byte, error) {
	if err := validMode(c, mode); err != nil {
		return nil, err
	}
	return [][]byte{c.control(map[string]any{"subtype": "set_permission_mode", "mode": claudeModes[mode], "skybreaker_mode": mode})}, nil
}

func (c *claude) SetModel(model string) ([][]byte, error) {
	if err := validModel(model); err != nil {
		return nil, err
	}
	return [][]byte{c.control(map[string]any{"subtype": "set_model", "model": model})}, nil
}

// SetEffort applies the level as a flag would. Claude takes any level
// without complaint, so what it then runs at is asked for.
func (c *claude) SetEffort(effort string) ([][]byte, error) {
	if err := validEffort(effort); err != nil {
		return nil, err
	}
	return [][]byte{
		c.control(map[string]any{"subtype": "apply_flag_settings", "settings": map[string]any{"effortLevel": effort}}),
		c.control(map[string]any{"subtype": "get_settings"}),
	}, nil
}

func (c *claude) Login() ([][]byte, error) { return nil, ErrTerminalLogin }

func (c *claude) Answer(id string, a Answer) ([][]byte, error) {
	ask, ok := c.asks[id]
	if !ok {
		return nil, errNoAsk
	}
	delete(c.asks, id)
	var r map[string]any
	switch {
	case !a.Allow:
		msg := a.Message
		if msg == "" {
			msg = "The user declined this."
		}
		r = map[string]any{"behavior": "deny", "message": msg}
	case ask.questions != nil:
		// AskUserQuestion reads its answers from its own input, keyed by
		// question text.
		var in map[string]any
		json.Unmarshal(ask.input, &in)
		if in == nil {
			in = map[string]any{}
		}
		answers := map[string]string{}
		for _, q := range ask.questions {
			if v := a.Answers[q.ID]; len(v) > 0 {
				answers[q.Question] = strings.Join(v, ", ")
			}
		}
		in["answers"] = answers
		r = map[string]any{"behavior": "allow", "updatedInput": in}
	default:
		r = map[string]any{"behavior": "allow", "updatedInput": ask.input}
		if a.Always && len(ask.suggestions) > 0 && string(ask.suggestions) != "null" {
			r["updatedPermissions"] = ask.suggestions
		}
	}
	return [][]byte{line(map[string]any{"type": "control_response", "response": map[string]any{
		"subtype": "success", "request_id": id, "response": r,
	}})}, nil
}

// claudeLine is the union of the stream-json messages we read.
type claudeLine struct {
	Type          string          `json:"type"`
	Subtype       string          `json:"subtype"`
	SessionID     string          `json:"session_id"`
	Message       json.RawMessage `json:"message"`
	Event         json.RawMessage `json:"event"`
	Parent        *string         `json:"parent_tool_use_id"`
	ToolUseResult json.RawMessage `json:"tool_use_result"`
	RequestID     string          `json:"request_id"`
	Request       json.RawMessage `json:"request"`
	Response      json.RawMessage `json:"response"`

	Model          string   `json:"model"`
	PermissionMode string   `json:"permissionMode"`
	Version        string   `json:"claude_code_version"`
	SlashCommands  []string `json:"slash_commands"`

	IsError        bool     `json:"is_error"`
	Result         *string  `json:"result"`
	DurationMS     int64    `json:"duration_ms"`
	Errors         []string `json:"errors"`
	TerminalReason string   `json:"terminal_reason"`
	ModelUsage     map[string]struct {
		ContextWindow int64 `json:"contextWindow"`
		Input         int64 `json:"inputTokens"`
		Output        int64 `json:"outputTokens"`
		CacheRead     int64 `json:"cacheReadInputTokens"`
		CacheCreation int64 `json:"cacheCreationInputTokens"`
	} `json:"modelUsage"`
	RateLimit *struct {
		Windows map[string]struct {
			Utilization *float64 `json:"utilization"`
			ResetsAt    int64    `json:"resetsAt"`
		} `json:"unifiedWindows"`
	} `json:"rate_limit_info"`

	// Background tasks: task_started, task_notification and
	// background_tasks_changed.
	TaskID    string `json:"task_id"`
	ToolUseID string `json:"tool_use_id"`
	TaskType  string `json:"task_type"`
	Status    string `json:"status"`
	Summary   string `json:"summary"`
	Tasks     []struct {
		TaskID string `json:"task_id"`
	} `json:"tasks"`

	CompactMetadata *struct {
		Trigger    string `json:"trigger"`
		PostTokens int64  `json:"post_tokens"`
	} `json:"compact_metadata"`
}

type claudeBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

type claudeMessage struct {
	ID      string          `json:"id"`
	Content json.RawMessage `json:"content"`
	Usage   *struct {
		Input         int64 `json:"input_tokens"`
		CacheCreation int64 `json:"cache_creation_input_tokens"`
		CacheRead     int64 `json:"cache_read_input_tokens"`
		Output        int64 `json:"output_tokens"`
	} `json:"usage"`
}

func (c *claude) Read(b []byte) Out {
	var l claudeLine
	if json.Unmarshal(b, &l) != nil {
		return Out{}
	}
	now := time.Now().UTC()
	ev := func(e Event) Event { e.Time = now; return e }
	var out Out
	switch l.Type {
	case "system":
		if l.Subtype == "init" && c.ran && !c.inTurn {
			// Claude took a turn nobody sent it, as when a background
			// subagent reports back.
			c.inTurn = true
			out.Events = append(out.Events, ev(Event{Kind: KindTurn, Turn: &Turn{State: TurnWorking}}))
		}
		if l.Subtype == "init" {
			out.Resume = l.SessionID
			mode := ModeAsk
			for k, v := range claudeModes {
				if v == l.PermissionMode {
					mode = k
				}
			}
			c.mode = mode
			if l.Model != "" {
				c.model = l.Model
			}
			out.Events = append(out.Events, ev(Event{Kind: KindInfo, Info: &Info{Model: l.Model, Mode: mode, Version: l.Version, Commands: l.SlashCommands}}))
		}
		if l.Subtype == "compact_boundary" && l.CompactMetadata != nil {
			c.used = l.CompactMetadata.PostTokens
			text := "The conversation was compacted."
			if l.CompactMetadata.Trigger == "auto" {
				text = "The conversation was compacted to fit the context window."
			}
			out.Events = append(out.Events, ev(Event{Kind: KindNotice, Text: text}),
				ev(Event{Kind: KindInfo, Info: &Info{Context: &Context{Used: c.used, Window: c.window}}}))
		}
		switch l.Subtype {
		case "task_started", "task_notification", "background_tasks_changed":
			out.Events = append(out.Events, c.task(l, now)...)
		}
	case "stream_event":
		out.Events = append(out.Events, c.stream(l, now)...)
	case "assistant":
		out.Events = append(out.Events, c.assistant(l, now)...)
	case "user":
		out.Events = append(out.Events, c.user(l, now)...)
	case "result":
		out.Events = append(out.Events, c.result(l, now)...)
		out.Usage = c.usage(l)
	case "rate_limit_event":
		out.Limits = claudeLimits(l)
	case "control_request":
		e, w := c.request(l, now)
		out.Events = append(out.Events, e...)
		out.Write = append(out.Write, w...)
	case "control_response":
		events, w := c.response(l, now)
		out.Events = append(out.Events, events...)
		out.Write = append(out.Write, w...)
	}
	return out
}

// response answers one of our control requests: initialize lists the
// models, set_model takes or refuses one, get_settings says the effort.
func (c *claude) response(l claudeLine, now time.Time) ([]Event, [][]byte) {
	var r struct {
		Subtype   string `json:"subtype"`
		RequestID string `json:"request_id"`
		Error     string `json:"error"`
		Response  struct {
			Models []struct {
				Value       string   `json:"value"`
				Resolved    string   `json:"resolvedModel"`
				DisplayName string   `json:"displayName"`
				Description string   `json:"description"`
				Efforts     []string `json:"supportedEffortLevels"`
			} `json:"models"`
			Applied struct {
				Effort string `json:"effort"`
			} `json:"applied"`
		} `json:"response"`
	}
	if json.Unmarshal(l.Response, &r) != nil {
		return nil, nil
	}
	sent, ok := c.sent[r.RequestID]
	if !ok {
		return nil, nil
	}
	delete(c.sent, r.RequestID)
	if mode, ok := strings.CutPrefix(sent, "set_mode "); ok {
		// A refused switch must not leave the chip on a mode Claude isn't in.
		if r.Subtype != "success" {
			return []Event{
				{Time: now, Kind: KindNotice, Text: clip("Couldn't switch to "+modeNames[mode]+": "+r.Error, 4096)},
				{Time: now, Kind: KindInfo, Info: &Info{Mode: c.mode}},
			}, nil
		}
		c.mode = mode
		return []Event{{Time: now, Kind: KindInfo, Info: &Info{Mode: mode}}}, nil
	}
	model, isSet := strings.CutPrefix(sent, "set_model ")
	switch {
	case isSet && r.Subtype == "success":
		c.model = model
		return []Event{{Time: now, Kind: KindInfo, Info: &Info{Model: model}}}, [][]byte{c.control(map[string]any{"subtype": "get_settings"})}
	case isSet:
		return []Event{{Time: now, Kind: KindNotice, Text: clip("Couldn't switch to "+model+": "+r.Error, 4096)}}, nil
	case sent == "get_settings" && r.Subtype == "success" && validEffort(r.Response.Applied.Effort) == nil:
		return []Event{{Time: now, Kind: KindInfo, Info: &Info{Effort: r.Response.Applied.Effort}}}, nil
	case sent == "initialize" && r.Subtype == "success":
		ask := [][]byte{c.control(map[string]any{"subtype": "get_settings"})}
		ms := []Model{}
		for _, m := range r.Response.Models {
			if validModel(m.Value) != nil {
				continue
			}
			md := Model{ID: m.Value, Name: m.DisplayName, Description: m.Description, Resolved: m.Resolved, Default: m.Value == "default", EffortsKnown: true}
			for _, e := range m.Efforts {
				if validEffort(e) == nil {
					md.Efforts = append(md.Efforts, e)
				}
			}
			ms = append(ms, md)
		}
		if len(ms) > 0 {
			return []Event{{Time: now, Kind: KindInfo, Info: &Info{Models: ms}}}, ask
		}
		return nil, ask
	}
	return nil, nil
}

// stream turns partial messages into text deltas. A text block's ID is its
// message's ID and its place among the message's text blocks, which the
// finished block gets too.
func (c *claude) stream(l claudeLine, now time.Time) []Event {
	if l.Parent != nil && *l.Parent != "" {
		return nil
	}
	var se struct {
		Type    string `json:"type"`
		Message struct {
			ID string `json:"id"`
		} `json:"message"`
		ContentBlock struct {
			Type string `json:"type"`
		} `json:"content_block"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	}
	if json.Unmarshal(l.Event, &se) != nil {
		return nil
	}
	switch se.Type {
	case "message_start":
		c.message, c.draft = se.Message.ID, ""
	case "content_block_start":
		if se.ContentBlock.Type == "text" {
			c.draft = fmt.Sprintf("%s:%d", c.message, c.blocks[c.message])
		}
	case "content_block_delta":
		if se.Delta.Type == "text_delta" && se.Delta.Text != "" && c.draft != "" {
			return []Event{{Time: now, Kind: KindDelta, ID: c.draft, Text: se.Delta.Text}}
		}
	}
	return nil
}

func (c *claude) textID(msg string) string {
	id := fmt.Sprintf("%s:%d", msg, c.blocks[msg])
	c.blocks[msg]++
	if c.draft == id {
		c.draft = ""
	}
	return id
}

func (c *claude) assistant(l claudeLine, now time.Time) []Event {
	var m claudeMessage
	var blocks []claudeBlock
	if json.Unmarshal(l.Message, &m) != nil || json.Unmarshal(m.Content, &blocks) != nil {
		return nil
	}
	parent := ""
	if l.Parent != nil {
		parent = *l.Parent
	}
	var out []Event
	// Each top-level message's usage is the whole context it was sent, so
	// the last one says how full the window is. Subagents have their own.
	if u := m.Usage; u != nil && parent == "" && m.ID != c.counted {
		c.counted = m.ID
		c.used = u.Input + u.CacheCreation + u.CacheRead + u.Output
		out = append(out, Event{Time: now, Kind: KindInfo, Info: &Info{Context: &Context{Used: c.used, Window: c.window}}})
	}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if parent != "" || strings.TrimSpace(b.Text) == "" {
				continue
			}
			out = append(out, Event{Time: now, Kind: KindText, ID: c.textID(m.ID), Text: clip(b.Text, maxText)})
		case "thinking":
			if parent != "" || strings.TrimSpace(b.Thinking) == "" {
				continue
			}
			out = append(out, Event{Time: now, Kind: KindThinking, ID: m.ID + ":t" + strconv.Itoa(len(out)), Text: clip(b.Thinking, maxText)})
		case "tool_use":
			t := claudeTool(b.Name, b.Input)
			t.Parent = parent
			c.tools[b.ID] = t
			cp := *t
			out = append(out, Event{Time: now, Kind: KindTool, ID: b.ID, Tool: &cp})
		}
	}
	return out
}

// user carries tool results back to the model; each finishes a card.
func (c *claude) user(l claudeLine, now time.Time) []Event {
	var m claudeMessage
	var blocks []claudeBlock
	if json.Unmarshal(l.Message, &m) != nil || json.Unmarshal(m.Content, &blocks) != nil {
		return nil
	}
	var out []Event
	for _, b := range blocks {
		if b.Type != "tool_result" {
			continue
		}
		t, ok := c.tools[b.ToolUseID]
		if !ok {
			continue
		}
		text := blockText(b.Content)
		if t.Kind == ToolAgent && !b.IsError && launched(l.ToolUseResult) {
			// A background subagent: its result says only that it
			// started. It runs until a task_notification ends it.
			c.agents[b.ToolUseID] = t
			delete(c.tools, b.ToolUseID)
			continue
		}
		switch {
		case b.IsError && strings.Contains(text, "doesn't want to proceed"):
			t.Status = StatusDeclined
		case b.IsError:
			t.Status = StatusFailed
		default:
			t.Status = StatusDone
		}
		c.finishTool(t, text, l.ToolUseResult)
		cp := *t
		out = append(out, Event{Time: now, Kind: KindTool, ID: b.ToolUseID, Tool: &cp})
		delete(c.tools, b.ToolUseID)
	}
	return out
}

// launched reports whether an Agent tool's result is a background
// launch rather than the subagent's answer.
func launched(result json.RawMessage) bool {
	var r struct {
		IsAsync bool   `json:"isAsync"`
		Status  string `json:"status"`
	}
	json.Unmarshal(result, &r)
	return r.IsAsync || r.Status == "async_launched"
}

// task follows background subagents: a notification ends one, with its
// summary, and the list of background tasks ends any whose notification
// hasn't come. One that comes late still corrects the card.
func (c *claude) task(l claudeLine, now time.Time) []Event {
	switch l.Subtype {
	case "task_started":
		if l.TaskID != "" && l.ToolUseID != "" && l.TaskType == "local_agent" {
			c.tasks[l.TaskID] = l.ToolUseID
		}
	case "task_notification":
		status := StatusDone
		switch l.Status {
		case "completed":
		case "failed":
			status = StatusFailed
		default: // stopped, killed
			status = StatusDeclined
		}
		id := l.ToolUseID
		if id == "" {
			id = c.tasks[l.TaskID]
		}
		if t := c.ended[id]; t != nil {
			delete(c.ended, id)
			t.Status = status
			if l.Summary != "" {
				t.Output = clip(l.Summary, maxOutput)
			}
			cp := *t
			return []Event{{Time: now, Kind: KindTool, ID: id, Tool: &cp}}
		}
		return c.finish(id, status, l.Summary, now)
	case "background_tasks_changed":
		live := map[string]bool{}
		for _, t := range l.Tasks {
			live[t.TaskID] = true
		}
		var out []Event
		for task, id := range c.tasks {
			if !live[task] {
				delete(c.tasks, task)
				if c.agents[id] != nil {
					c.ended[id] = c.agents[id]
				}
				out = append(out, c.finish(id, StatusDone, "", now)...)
			}
		}
		return out
	}
	return nil
}

// finish ends a background subagent's card. Steps of it still open and
// asks it was waiting on end with it.
func (c *claude) finish(id, status, summary string, now time.Time) []Event {
	t := c.agents[id]
	if t == nil {
		return nil
	}
	var out []Event
	for ask, a := range c.asks {
		if c.agentOf(a.toolID) == id {
			delete(c.asks, ask)
			out = append(out, Event{Time: now, Kind: KindAnswered, ID: ask, Text: "withdrawn"})
		}
	}
	for sid, st := range c.tools {
		if st.Parent != id {
			continue
		}
		st.Status = stepEnd(status)
		cp := *st
		out = append(out, Event{Time: now, Kind: KindTool, ID: sid, Tool: &cp})
		delete(c.tools, sid)
	}
	delete(c.agents, id)
	for k, v := range c.tasks {
		if v == id {
			delete(c.tasks, k)
		}
	}
	t.Status = status
	if summary != "" {
		t.Output = clip(summary, maxOutput)
	}
	cp := *t
	return append(out, Event{Time: now, Kind: KindTool, ID: id, Tool: &cp})
}

// agentOf is the background subagent a tool call belongs to, if any.
func (c *claude) agentOf(toolID string) string {
	if t := c.tools[toolID]; t != nil && c.agents[t.Parent] != nil {
		return t.Parent
	}
	return ""
}

// blockText reads a tool result's content: a string, or text blocks.
func blockText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []claudeBlock
	json.Unmarshal(raw, &blocks)
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func (c *claude) finishTool(t *Tool, text string, result json.RawMessage) {
	var r struct {
		Stdout          string `json:"stdout"`
		Stderr          string `json:"stderr"`
		FilePath        string `json:"filePath"`
		Type            string `json:"type"`
		Content         string `json:"content"`
		StructuredPatch []struct {
			OldStart int      `json:"oldStart"`
			OldLines int      `json:"oldLines"`
			NewStart int      `json:"newStart"`
			NewLines int      `json:"newLines"`
			Lines    []string `json:"lines"`
		} `json:"structuredPatch"`
	}
	json.Unmarshal(result, &r)
	switch t.Kind {
	case ToolCommand:
		out := r.Stdout
		if r.Stderr != "" {
			out = strings.TrimRight(out+"\n"+r.Stderr, "\n")
		}
		if out == "" {
			out = text
		}
		t.Output = clipTail(out, maxOutput)
	case ToolEdit:
		path := Rel(r.FilePath)
		if path == "" {
			path = t.Title
		}
		if len(r.StructuredPatch) > 0 {
			var b strings.Builder
			b.WriteString(fileHeader(path))
			for _, h := range r.StructuredPatch {
				fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", h.OldStart, h.OldLines, h.NewStart, h.NewLines)
				for _, l := range h.Lines {
					b.WriteString(l + "\n")
				}
			}
			t.Diff = clip(b.String(), maxDiff)
		} else if r.Type == "create" {
			t.Diff = clip(wholeFile(path, r.Content, '+'), maxDiff)
		}
		t.Added, t.Removed = countDiff(t.Diff)
		if t.Status != StatusDone {
			t.Output = clip(text, maxOutput)
		}
	case ToolRead:
		if t.Status != StatusDone {
			t.Output = clip(text, maxOutput)
		}
	case ToolTodo:
	case ToolAgent:
		// The subagent's reply, without the frame Claude wraps it in for
		// the model.
		var a struct {
			Content json.RawMessage `json:"content"`
		}
		json.Unmarshal(result, &a)
		if reply := blockText(a.Content); strings.TrimSpace(reply) != "" {
			text = reply
		}
		t.Output = clip(text, maxOutput)
	default:
		t.Output = clip(text, maxOutput)
	}
}

func (c *claude) result(l claudeLine, now time.Time) []Event {
	turn := &Turn{State: TurnDone, DurationMS: l.DurationMS}
	var out []Event
	msg := ""
	if l.Result != nil {
		msg = *l.Result
	}
	if msg == "" && len(l.Errors) > 0 {
		msg = strings.Join(l.Errors, "; ")
	}
	switch {
	case c.interrupted || strings.HasPrefix(l.TerminalReason, "aborted"):
		turn.State = TurnInterrupted
	case l.IsError || strings.HasPrefix(l.Subtype, "error"):
		turn.State, turn.Error = TurnFailed, clip(msg, 4096)
		if turn.Error == "" {
			turn.Error = strings.ReplaceAll(l.Subtype, "_", " ")
		}
		if strings.Contains(msg, "/login") || strings.Contains(strings.ToLower(msg), "not logged in") || strings.Contains(msg, "Invalid API key") {
			out = append(out, Event{Time: now, Kind: KindLogin, Login: &Login{Needed: true, Terminal: true}})
		}
	}
	c.interrupted = false
	// Asks the harness gave up on are void; a background subagent's go on.
	for id, a := range c.asks {
		if c.agentOf(a.toolID) != "" {
			continue
		}
		delete(c.asks, id)
		out = append(out, Event{Time: now, Kind: KindAnswered, ID: id, Text: "withdrawn"})
	}
	c.inTurn, c.ran = false, true
	for id, t := range c.tools {
		if c.agents[t.Parent] != nil {
			continue // a step of a background subagent, which outlives the turn
		}
		if t.Status == StatusRunning {
			t.Status = StatusFailed
			if turn.State == TurnInterrupted {
				t.Status = StatusDeclined
			}
			cp := *t
			out = append(out, Event{Time: now, Kind: KindTool, ID: id, Tool: &cp})
		}
		delete(c.tools, id)
	}
	clear(c.blocks)
	c.draft = ""
	if w := c.contextWindow(l); w > 0 && w != c.window {
		c.window = w
		out = append(out, Event{Time: now, Kind: KindInfo, Info: &Info{Context: &Context{Used: c.used, Window: w}}})
	}
	return append(out, Event{Time: now, Kind: KindTurn, Turn: turn})
}

// usage is what a result adds to the tokens counted so far. Its per-model
// usage covers subagents and compaction and grows over the process's
// life; a count that went down is a new process starting again.
func (c *claude) usage(l claudeLine) *Usage {
	if c.spent == nil {
		c.spent = map[string]Usage{}
	}
	var sum Usage
	for m, u := range l.ModelUsage {
		now := Usage{Input: u.Input + u.CacheCreation, Cached: u.CacheRead, Output: u.Output}
		before := c.spent[m]
		if now.Input < before.Input || now.Cached < before.Cached || now.Output < before.Output {
			before = Usage{}
		}
		c.spent[m] = now
		sum.Input += now.Input - before.Input
		sum.Cached += now.Cached - before.Cached
		sum.Output += now.Output - before.Output
	}
	if sum == (Usage{}) {
		return nil
	}
	return &sum
}

// claudeLimits reads the account's usage windows from a rate limit event:
// utilization is a fraction, resetsAt a Unix time. Junk is dropped.
func claudeLimits(l claudeLine) []Limit {
	if l.RateLimit == nil {
		return nil
	}
	var out []Limit
	for name, w := range l.RateLimit.Windows {
		if w.Utilization == nil || *w.Utilization < 0 || *w.Utilization > 1 || !limitNameRe.MatchString(name) {
			continue
		}
		out = append(out, Limit{Name: name, Minutes: claudeWindows[name], Used: *w.Utilization * 100, Resets: unixTime(w.ResetsAt)})
	}
	sortLimits(out)
	return out
}

var claudeWindows = map[string]int64{"five_hour": 5 * 60, "seven_day": 7 * 24 * 60}

// contextWindow reads the window of the model Claude runs from a result's
// per-model usage, which subagents' models share. An alias the user chose
// won't match the full name, so failing that, the largest window there.
func (c *claude) contextWindow(l claudeLine) int64 {
	if u, ok := l.ModelUsage[c.model]; ok {
		return u.ContextWindow
	}
	var w int64
	for _, u := range l.ModelUsage {
		w = max(w, u.ContextWindow)
	}
	return w
}

// request handles the CLI asking its host something. Only permission
// prompts become asks; anything else is refused, so the CLI never waits on
// an answer that won't come.
func (c *claude) request(l claudeLine, now time.Time) ([]Event, [][]byte) {
	var r struct {
		Subtype     string          `json:"subtype"`
		ToolName    string          `json:"tool_name"`
		Input       json.RawMessage `json:"input"`
		Suggestions json.RawMessage `json:"permission_suggestions"`
		ToolUseID   string          `json:"tool_use_id"`
		Reason      string          `json:"decision_reason"`
		Description string          `json:"description"`
	}
	if json.Unmarshal(l.Request, &r) != nil || l.RequestID == "" {
		return nil, nil
	}
	if r.Subtype != "can_use_tool" {
		return nil, [][]byte{line(map[string]any{"type": "control_response", "response": map[string]any{
			"subtype": "error", "request_id": l.RequestID, "error": "skybreaker does not handle " + r.Subtype,
		}})}
	}
	ask := claudeAsk{tool: r.ToolName, input: r.Input, suggestions: r.Suggestions, toolID: r.ToolUseID}
	a := &Ask{Kind: AskPermission, Tool: r.ToolName, ToolID: r.ToolUseID, Reason: r.Reason}
	if r.ToolName == "AskUserQuestion" {
		var in struct {
			Questions []struct {
				Question    string   `json:"question"`
				Header      string   `json:"header"`
				MultiSelect bool     `json:"multiSelect"`
				Options     []Option `json:"options"`
			} `json:"questions"`
		}
		json.Unmarshal(r.Input, &in)
		ask.questions = []Question{}
		for i, q := range in.Questions {
			ask.questions = append(ask.questions, Question{ID: "q" + strconv.Itoa(i), Header: q.Header, Question: q.Question, Options: q.Options, Multi: q.MultiSelect})
		}
		a.Kind, a.Title, a.Questions = AskQuestion, "A question for you", ask.questions
	} else {
		t := claudeTool(r.ToolName, r.Input)
		a.Title = toolVerb(t) + " " + t.Title
		// The model's own words for what the call does, when they add to
		// the title.
		if a.Reason == "" && r.Description != "" && r.Description != t.Title {
			a.Reason = r.Description
		}
		a.Always = len(r.Suggestions) > 0 && string(r.Suggestions) != "null" && string(r.Suggestions) != "[]"
		switch {
		case t.Diff != "":
			a.Detail = t.Diff
		case t.Kind == ToolCommand:
			var in struct {
				Command string `json:"command"`
			}
			json.Unmarshal(r.Input, &in)
			a.Detail = clip(in.Command, maxOutput)
		default:
			a.Detail = clip(prettyJSON(r.Input), maxOutput)
		}
	}
	c.asks[l.RequestID] = ask
	return []Event{{Time: now, Kind: KindAsk, ID: l.RequestID, Ask: a}}, nil
}

func prettyJSON(b json.RawMessage) string {
	var v any
	if json.Unmarshal(b, &v) != nil {
		return string(b)
	}
	out, _ := json.MarshalIndent(v, "", "  ")
	return string(out)
}

// toolVerb starts an ask's title: "Run", "Edit", ...
func toolVerb(t *Tool) string {
	switch t.Kind {
	case ToolCommand:
		return "Run"
	case ToolEdit:
		return "Edit"
	case ToolRead:
		return "Read"
	case ToolSearch:
		return "Search"
	case ToolWeb:
		return "Fetch"
	}
	return "Use " + t.Name + " on"
}

// claudeTool makes a card from a Claude Code tool call. Tools it doesn't
// know get a generic card titled with their first string argument.
func claudeTool(name string, raw json.RawMessage) *Tool {
	var in map[string]any
	json.Unmarshal(raw, &in)
	str := func(k string) string { s, _ := in[k].(string); return s }
	t := &Tool{Name: name, Status: StatusRunning, Input: clipJSON(raw)}
	switch name {
	case "Bash", "BashOutput", "PowerShell":
		t.Kind, t.Title = ToolCommand, firstLine(str("command"))
		if t.Title == "" {
			t.Title = str("description")
		}
	case "Edit", "MultiEdit":
		t.Kind, t.Title = ToolEdit, Rel(str("file_path"))
		if old, nw := str("old_string"), str("new_string"); old != "" || nw != "" {
			t.Diff = clip(replaceDiff(t.Title, old, nw), maxDiff)
		}
		if edits, ok := in["edits"].([]any); ok {
			var b strings.Builder
			for _, e := range edits {
				m, _ := e.(map[string]any)
				old, _ := m["old_string"].(string)
				nw, _ := m["new_string"].(string)
				b.WriteString(replaceDiff(t.Title, old, nw))
			}
			t.Diff = clip(b.String(), maxDiff)
		}
		t.Added, t.Removed = countDiff(t.Diff)
	case "Write":
		t.Kind, t.Title = ToolEdit, Rel(str("file_path"))
		t.Diff = clip(wholeFile(t.Title, str("content"), '+'), maxDiff)
		t.Added, t.Removed = countDiff(t.Diff)
	case "NotebookEdit":
		t.Kind, t.Title = ToolEdit, Rel(str("notebook_path"))
	case "Read":
		t.Kind, t.Title = ToolRead, Rel(str("file_path"))
	case "Grep", "Glob":
		t.Kind, t.Title = ToolSearch, str("pattern")
		if p := str("path"); p != "" {
			t.Title += " in " + Rel(p)
		}
	case "WebFetch":
		t.Kind, t.Title = ToolWeb, str("url")
	case "WebSearch":
		t.Kind, t.Title = ToolWeb, str("query")
	case "Task", "Agent":
		t.Kind, t.Title = ToolAgent, str("description")
	case "TodoWrite":
		t.Kind, t.Title = ToolTodo, "Plan"
		var todos struct {
			Todos []struct {
				Content string `json:"content"`
				Status  string `json:"status"`
			} `json:"todos"`
		}
		json.Unmarshal(raw, &todos)
		var lines []string
		for _, td := range todos.Todos {
			mark := "[ ]"
			switch td.Status {
			case "completed":
				mark = "[x]"
			case "in_progress":
				mark = "[~]"
			}
			lines = append(lines, mark+" "+td.Content)
		}
		t.Output = strings.Join(lines, "\n")
	default:
		otherTool(t, name, in)
	}
	if len(t.Title) > 300 {
		t.Title = clip(t.Title, 300)
	}
	return t
}
