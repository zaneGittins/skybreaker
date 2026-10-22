package chat

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
)

// codex drives `codex app-server`, the JSON-RPC interface OpenAI's own
// Codex apps use. Messages are one JSON object per line, without the
// "jsonrpc" member.
type codex struct {
	opts    Options
	mode    string
	next    int
	sent    map[int]string // our request ID → its method
	thread  string
	turn    string
	queue   [][]any // inputs sent before the thread exists
	held    [][]any // inputs sent while a turn starts, steered into it once codex names it
	asks    map[string]codexAsk
	items   map[string]*Tool  // item ID → its card
	agents  map[string]*Tool  // a subagent's card ID → its card, running until its thread's turn ends
	threads map[string]string // a subagent's thread ID → its card ID
	resumed bool
	compact bool    // a compaction asked for before the thread exists
	signing bool    // a sign-in is under way
	model   string  // the model the user chose; empty leaves it to codex
	models  []Model // model/list pages so far
	pages   int
	counted int64            // the thread's total tokens as last counted
	limits  map[string]Limit // the account's windows, primary and secondary
}

type codexAsk struct {
	rpcID     json.RawMessage
	method    string
	questions []Question
	request   json.RawMessage // a permissions request's profile, granted as asked
	thread    string          // the thread asking: the conversation's or a subagent's
}

func newCodex() *codex {
	return &codex{sent: map[int]string{}, asks: map[string]codexAsk{}, items: map[string]*Tool{},
		agents: map[string]*Tool{}, threads: map[string]string{}}
}

var codexPolicies = map[string]string{ModeAsk: "untrusted", ModeEdits: "on-request", ModeAuto: "never"}

func (c *codex) Modes() []string { return []string{ModeAsk, ModeEdits, ModeAuto} }

func (c *codex) Argv(o Options) []string {
	argv := []string{"codex", "app-server"}
	if o.MCP != "" {
		argv = append(argv, "-c", "mcp_servers.skybreaker.url="+strconv.Quote(o.MCP))
	}
	return argv
}

func (c *codex) Env() map[string]string { return nil }

func (c *codex) request(method string, params any) []byte {
	c.next++
	c.sent[c.next] = method
	return line(map[string]any{"id": c.next, "method": method, "params": params})
}

func (c *codex) Open(o Options) [][]byte {
	c.opts, c.mode, c.model = o, o.Mode, o.Model
	if codexPolicies[c.mode] == "" {
		c.mode = ModeAsk
	}
	return [][]byte{c.request("initialize", map[string]any{"clientInfo": map[string]any{"name": "skybreaker", "title": "Skybreaker", "version": "1"}})}
}

// threadParams are the settings every thread gets: the workspace, the
// approval policy, and no sandbox of codex's own, since it runs inside
// Skybreaker's.
func (c *codex) threadParams() map[string]any {
	cwd := c.opts.Cwd
	if cwd == "" {
		cwd = "/workspace"
	}
	p := map[string]any{"cwd": cwd, "approvalPolicy": codexPolicies[c.mode], "sandbox": "danger-full-access"}
	if c.model != "" {
		p["model"] = c.model
	}
	return p
}

func (c *codex) startThread() []byte {
	if c.opts.Resume != "" && !c.resumed {
		c.resumed = true
		p := c.threadParams()
		p["threadId"] = c.opts.Resume
		if c.opts.Fork {
			return c.request("thread/fork", p)
		}
		return c.request("thread/resume", p)
	}
	return c.request("thread/start", c.threadParams())
}

// input is a message as codex takes it, its images as data URLs.
func input(text string, images ...Image) []any {
	in := []any{map[string]any{"type": "text", "text": text}}
	for _, im := range images {
		in = append(in, map[string]any{"type": "image", "url": "data:" + im.Mime + ";base64," + base64.StdEncoding.EncodeToString(im.Data)})
	}
	return in
}

func (c *codex) Send(text string, images ...Image) ([][]byte, error) {
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("empty message")
	}
	in := input(text, images...)
	switch {
	case c.thread == "":
		c.queue = append(c.queue, in)
		return nil, nil
	case c.turn == "pending":
		// codex steers only the turn it names; until it has, wait.
		c.held = append(c.held, in)
		return nil, nil
	case c.turn != "":
		return [][]byte{c.request("turn/steer", map[string]any{"threadId": c.thread, "expectedTurnId": c.turn, "input": in})}, nil
	}
	return [][]byte{c.startTurn(in)}, nil
}

func (c *codex) startTurn(in []any) []byte {
	// A placeholder, so a second message before turn/started steers rather
	// than starting a turn alongside.
	c.turn = "pending"
	p := map[string]any{"threadId": c.thread, "input": in, "approvalPolicy": codexPolicies[c.mode]}
	if c.model != "" {
		p["model"] = c.model
	}
	return c.request("turn/start", p)
}

// Compact asks codex to summarise the thread. Its progress comes as an
// ordinary turn.
func (c *codex) Compact() ([][]byte, error) {
	if c.thread == "" {
		c.compact = true
		return nil, nil
	}
	return [][]byte{c.request("thread/compact/start", map[string]any{"threadId": c.thread})}, nil
}

func (c *codex) Interrupt() [][]byte {
	if c.thread == "" || c.turn == "" || c.turn == "pending" {
		return nil
	}
	return [][]byte{c.request("turn/interrupt", map[string]any{"threadId": c.thread, "turnId": c.turn})}
}

// SetMode takes effect from the next turn, which carries its policy, and
// at once for what the running turn asks (see serverRequest).
func (c *codex) SetMode(mode string) ([][]byte, error) {
	if err := validMode(c, mode); err != nil {
		return nil, err
	}
	c.mode = mode
	return nil, nil
}

// SetModel takes effect from the next turn, which carries it.
func (c *codex) SetModel(model string) ([][]byte, error) {
	if err := validModel(model); err != nil {
		return nil, err
	}
	c.model = model
	return nil, nil
}

func (c *codex) Login() ([][]byte, error) {
	c.signing = true
	return [][]byte{c.request("account/login/start", map[string]any{"type": "chatgptDeviceCode"})}, nil
}

func (c *codex) Answer(id string, a Answer) ([][]byte, error) {
	ask, ok := c.asks[id]
	if !ok {
		return nil, errNoAsk
	}
	delete(c.asks, id)
	var result any
	switch ask.method {
	case "item/tool/requestUserInput":
		answers := map[string]any{}
		for _, q := range ask.questions {
			if v := a.Answers[q.ID]; len(v) > 0 && a.Allow {
				answers[q.ID] = map[string]any{"answers": v}
			}
		}
		result = map[string]any{"answers": answers}
	case "item/permissions/requestApproval":
		granted := json.RawMessage(`{}`)
		if a.Allow && len(ask.request) > 0 {
			granted = ask.request
		}
		scope := "turn"
		if a.Always {
			scope = "session"
		}
		result = map[string]any{"permissions": granted, "scope": scope}
	default:
		decision := "decline"
		switch {
		case a.Allow && a.Always:
			decision = "acceptForSession"
		case a.Allow:
			decision = "accept"
		}
		result = map[string]any{"decision": decision}
	}
	return [][]byte{line(map[string]any{"id": ask.rpcID, "result": result})}, nil
}

type codexLine struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (c *codex) Read(b []byte) Out {
	out := c.read(b)
	// Once codex has named the turn, what waited for it steers it.
	if c.turn != "" && c.turn != "pending" {
		for _, in := range c.held {
			out.Write = append(out.Write, c.request("turn/steer", map[string]any{"threadId": c.thread, "expectedTurnId": c.turn, "input": in}))
		}
		c.held = nil
	}
	return out
}

func (c *codex) read(b []byte) Out {
	var l codexLine
	if json.Unmarshal(b, &l) != nil {
		return Out{}
	}
	now := time.Now().UTC()
	switch {
	case l.Method != "" && len(l.ID) > 0:
		return c.serverRequest(l, now)
	case l.Method != "":
		switch l.Method {
		case "thread/tokenUsage/updated":
			return c.tokenUsage(l, now)
		case "account/rateLimits/updated":
			var p struct {
				RateLimits codexLimits `json:"rateLimits"`
			}
			if json.Unmarshal(l.Params, &p) != nil {
				return Out{}
			}
			return Out{Limits: c.mergeLimits(p.RateLimits)}
		}
		return Out{Events: c.notification(l, now)}
	case len(l.ID) > 0:
		return c.response(l, now)
	}
	return Out{}
}

func (c *codex) response(l codexLine, now time.Time) Out {
	id, err := strconv.Atoi(string(l.ID))
	if err != nil {
		return Out{}
	}
	method, ok := c.sent[id]
	if !ok {
		return Out{}
	}
	delete(c.sent, id)
	var out Out
	if l.Error != nil {
		switch method {
		case "thread/resume":
			out.Events = append(out.Events, Event{Time: now, Kind: KindNotice, Text: "The earlier conversation couldn't be resumed, so this is a new one."})
			out.Write = append(out.Write, c.startThread())
		case "thread/fork":
			out.Events = append(out.Events, Event{Time: now, Kind: KindNotice, Text: "The earlier conversation couldn't be forked, so this is a new one."})
			out.Write = append(out.Write, c.startThread())
		case "thread/start", "thread/compact/start":
			out.Events = append(out.Events, Event{Time: now, Kind: KindTurn, Turn: &Turn{State: TurnFailed, Error: l.Error.Message}})
		case "turn/start", "turn/steer":
			c.turn = ""
			out.Events = append(out.Events, Event{Time: now, Kind: KindTurn, Turn: &Turn{State: TurnFailed, Error: l.Error.Message}})
			if len(c.held) > 0 {
				// What waited for the failed turn starts the next one.
				first := c.held[0]
				c.held = c.held[1:]
				out.Write = append(out.Write, c.startTurn(first))
			}
		case "account/login/start":
			c.signing = false
			out.Events = append(out.Events, Event{Time: now, Kind: KindLogin, Login: &Login{Needed: true, Error: l.Error.Message}})
		}
		return out
	}
	switch method {
	case "initialize":
		out.Write = append(out.Write, line(map[string]any{"method": "initialized"}), c.startThread(), c.request("account/read", map[string]any{}),
			c.request("model/list", map[string]any{}), c.request("account/rateLimits/read", nil))
	case "account/rateLimits/read":
		var r struct {
			RateLimits codexLimits             `json:"rateLimits"`
			ByID       map[string]*codexLimits `json:"rateLimitsByLimitId"`
		}
		if json.Unmarshal(l.Result, &r) != nil {
			return out
		}
		if l := r.ByID["codex"]; l != nil {
			r.RateLimits = *l
		}
		out.Limits = c.mergeLimits(r.RateLimits)
	case "model/list":
		var r struct {
			Data []struct {
				Model       string `json:"model"`
				DisplayName string `json:"displayName"`
				Description string `json:"description"`
				Hidden      bool   `json:"hidden"`
				IsDefault   bool   `json:"isDefault"`
			} `json:"data"`
			NextCursor *string `json:"nextCursor"`
		}
		if json.Unmarshal(l.Result, &r) != nil {
			return out
		}
		for _, m := range r.Data {
			if !m.Hidden && validModel(m.Model) == nil {
				c.models = append(c.models, Model{ID: m.Model, Name: m.DisplayName, Description: m.Description, Default: m.IsDefault})
			}
		}
		// The catalog is short; the page cap only stops a cursor that never ends.
		if c.pages++; r.NextCursor != nil && *r.NextCursor != "" && c.pages < 10 {
			out.Write = append(out.Write, c.request("model/list", map[string]any{"cursor": *r.NextCursor}))
			return out
		}
		if len(c.models) > 0 {
			out.Events = append(out.Events, Event{Time: now, Kind: KindInfo, Info: &Info{Models: c.models}})
		}
		c.models, c.pages = nil, 0
	case "thread/start", "thread/resume", "thread/fork":
		var r struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
			Model string `json:"model"`
		}
		if json.Unmarshal(l.Result, &r) != nil || r.Thread.ID == "" {
			return out
		}
		c.thread, out.Resume = r.Thread.ID, r.Thread.ID
		out.Events = append(out.Events, Event{Time: now, Kind: KindInfo, Info: &Info{Model: r.Model, Mode: c.mode}})
		if c.compact {
			c.compact = false
			out.Write = append(out.Write, c.request("thread/compact/start", map[string]any{"threadId": c.thread}))
		}
		queued := c.queue
		c.queue = nil
		if len(queued) > 0 {
			out.Write = append(out.Write, c.startTurn(queued[0]))
			c.held = append(c.held, queued[1:]...)
		}
	case "turn/start":
		var r struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		if json.Unmarshal(l.Result, &r) == nil && r.Turn.ID != "" && c.turn == "pending" {
			c.turn = r.Turn.ID
		}
	case "account/read":
		var r map[string]json.RawMessage
		if json.Unmarshal(l.Result, &r) == nil {
			// During a sign-in, "no account" is old news: the code card stays.
			if acct, ok := r["account"]; ok && string(acct) == "null" && !c.signing {
				out.Events = append(out.Events, Event{Time: now, Kind: KindLogin, Login: &Login{Needed: true}})
			}
		}
	case "account/login/start":
		var r struct {
			URL  string `json:"verificationUrl"`
			Code string `json:"userCode"`
			Auth string `json:"authUrl"`
		}
		json.Unmarshal(l.Result, &r)
		if r.URL == "" {
			r.URL = r.Auth
		}
		out.Events = append(out.Events, Event{Time: now, Kind: KindLogin, Login: &Login{Needed: true, URL: r.URL, Code: r.Code}})
	}
	return out
}

// tokenUsage reads what a request used. The last request's tokens are
// also what the context holds now. A report whose thread total hasn't
// moved, as on a resume, adds nothing.
func (c *codex) tokenUsage(l codexLine, now time.Time) Out {
	type breakdown struct {
		Total  int64 `json:"totalTokens"`
		Input  int64 `json:"inputTokens"`
		Cached int64 `json:"cachedInputTokens"`
		Output int64 `json:"outputTokens"`
	}
	var p struct {
		ThreadID   string `json:"threadId"`
		TokenUsage struct {
			Total  breakdown `json:"total"`
			Last   breakdown `json:"last"`
			Window *int64    `json:"modelContextWindow"`
		} `json:"tokenUsage"`
	}
	if json.Unmarshal(l.Params, &p) != nil || p.ThreadID != c.thread || c.thread == "" {
		return Out{}
	}
	ctx := &Context{Used: p.TokenUsage.Last.Total}
	if p.TokenUsage.Window != nil {
		ctx.Window = *p.TokenUsage.Window
	}
	out := Out{Events: []Event{{Time: now, Kind: KindInfo, Info: &Info{Context: ctx}}}}
	last := p.TokenUsage.Last
	if total := p.TokenUsage.Total.Total; total != c.counted && last.Input >= last.Cached && last.Cached >= 0 && last.Output >= 0 {
		c.counted = total
		if u := (Usage{Input: last.Input - last.Cached, Cached: last.Cached, Output: last.Output}); u != (Usage{}) {
			out.Usage = &u
		}
	}
	return out
}

// codexLimits is a rate limit snapshot: the account's primary window (five
// hours) and secondary (a week). An update may leave one out.
type codexLimits struct {
	Primary   *codexWindow `json:"primary"`
	Secondary *codexWindow `json:"secondary"`
}

type codexWindow struct {
	UsedPercent float64 `json:"usedPercent"`
	Minutes     *int64  `json:"windowDurationMins"`
	ResetsAt    *int64  `json:"resetsAt"`
}

// mergeLimits takes in a snapshot's windows and returns them all, or nil
// when it had none.
func (c *codex) mergeLimits(s codexLimits) []Limit {
	if c.limits == nil {
		c.limits = map[string]Limit{}
	}
	took := false
	for name, w := range map[string]*codexWindow{"primary": s.Primary, "secondary": s.Secondary} {
		if w == nil || w.UsedPercent < 0 || w.UsedPercent > 100 {
			continue
		}
		l := Limit{Name: name, Used: w.UsedPercent}
		if w.Minutes != nil && *w.Minutes > 0 {
			l.Minutes = *w.Minutes
		}
		if w.ResetsAt != nil {
			l.Resets = unixTime(*w.ResetsAt)
		}
		c.limits[name], took = l, true
	}
	if !took {
		return nil
	}
	out := slices.Collect(maps.Values(c.limits))
	sortLimits(out)
	return out
}

// codexItem is the union of the thread items we draw.
type codexItem struct {
	Type    string   `json:"type"`
	ID      string   `json:"id"`
	Text    string   `json:"text"`
	Summary []string `json:"summary"`
	Command string   `json:"command"`
	Output  *string  `json:"aggregatedOutput"`
	Exit    *int     `json:"exitCode"`
	Status  string   `json:"status"`
	Changes []struct {
		Path string `json:"path"`
		Kind struct {
			Type string `json:"type"`
		} `json:"kind"`
		Diff string `json:"diff"`
	} `json:"changes"`
	Server    string          `json:"server"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
	Query     string          `json:"query"`
	Prompt    string          `json:"prompt"`
	Error     json.RawMessage `json:"error"`
	Result    json.RawMessage `json:"result"`
	// A subagent: kind is started or completed.
	Kind        string `json:"kind"`
	AgentThread string `json:"agentThreadId"`
	AgentPath   string `json:"agentPath"`
}

func (c *codex) notification(l codexLine, now time.Time) []Event {
	var th struct {
		ThreadID string `json:"threadId"`
	}
	json.Unmarshal(l.Params, &th)
	if th.ThreadID != "" && c.thread != "" && th.ThreadID != c.thread {
		// Subagents run in threads of their own on the same connection.
		return c.subagent(c.threads[th.ThreadID], l, now)
	}
	switch l.Method {
	case "turn/started":
		var p struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		if json.Unmarshal(l.Params, &p) == nil && p.Turn.ID != "" {
			unasked := c.turn == ""
			c.turn = p.Turn.ID
			if unasked {
				// A turn nobody sent, as when codex picks up after its subagents.
				return []Event{{Time: now, Kind: KindTurn, Turn: &Turn{State: TurnWorking}}}
			}
		}
	case "turn/completed":
		return c.turnCompleted(l, now)
	case "item/agentMessage/delta":
		var p struct {
			Delta  string `json:"delta"`
			ItemID string `json:"itemId"`
		}
		if json.Unmarshal(l.Params, &p) == nil && p.Delta != "" && p.ItemID != "" {
			return []Event{{Time: now, Kind: KindDelta, ID: p.ItemID, Text: p.Delta}}
		}
	case "item/started", "item/completed":
		var p struct {
			Item codexItem `json:"item"`
		}
		if json.Unmarshal(l.Params, &p) != nil || p.Item.ID == "" {
			return nil
		}
		return c.item(p.Item, "", l.Method == "item/completed", now)
	case "account/login/completed":
		var p struct {
			Success bool    `json:"success"`
			Error   *string `json:"error"`
		}
		json.Unmarshal(l.Params, &p)
		lg := &Login{Done: p.Success}
		c.signing = false
		if !p.Success {
			lg.Needed, lg.Error = true, "Sign-in failed"
			if p.Error != nil && *p.Error != "" {
				lg.Error = *p.Error
			}
		}
		return []Event{{Time: now, Kind: KindLogin, Login: lg}}
	}
	return nil
}

// item draws one of a thread's items; parent is the subagent card a
// subagent's items nest in.
func (c *codex) item(it codexItem, parent string, done bool, now time.Time) []Event {
	switch it.Type {
	case "agentMessage", "plan":
		if done && strings.TrimSpace(it.Text) != "" {
			return []Event{{Time: now, Kind: KindText, ID: it.ID, Text: clip(it.Text, maxText)}}
		}
		return nil
	case "reasoning":
		if text := strings.TrimSpace(strings.Join(it.Summary, "\n\n")); done && text != "" {
			return []Event{{Time: now, Kind: KindThinking, ID: it.ID, Text: clip(text, maxText)}}
		}
		return nil
	case "contextCompaction":
		if done {
			return []Event{{Time: now, Kind: KindNotice, Text: "The conversation was compacted to fit the context window."}}
		}
		return nil
	case "userMessage", "hookPrompt", "":
		return nil
	case "subAgentActivity":
		return c.spawned(it, now)
	case "collabAgentToolCall":
		if it.Tool == "wait" {
			return nil // the subagents' own cards show what it waits on
		}
	}
	t := c.items[it.ID]
	if t == nil {
		t = &Tool{Name: it.Type, Kind: ToolOther, Title: it.Type, Parent: parent}
		c.items[it.ID] = t
	}
	t.Status = codexStatus(it.Status, done)
	switch it.Type {
	case "commandExecution":
		t.Kind, t.Title = ToolCommand, firstLine(it.Command)
		if it.Output != nil {
			t.Output = clipTail(*it.Output, maxOutput)
		}
		t.Exit = it.Exit
		if t.Status == StatusDone && it.Exit != nil && *it.Exit != 0 {
			t.Status = StatusFailed
		}
	case "fileChange":
		t.Kind = ToolEdit
		if len(it.Changes) > 0 {
			var b strings.Builder
			for _, ch := range it.Changes {
				p := Rel(ch.Path)
				switch {
				case ch.Kind.Type == "add" && !strings.HasPrefix(ch.Diff, "@@"):
					b.WriteString(wholeFile(p, ch.Diff, '+'))
				case ch.Kind.Type == "delete" && !strings.HasPrefix(ch.Diff, "@@"):
					b.WriteString(wholeFile(p, ch.Diff, '-'))
				default:
					b.WriteString(fileHeader(p) + strings.TrimSuffix(ch.Diff, "\n") + "\n")
				}
			}
			t.Diff = clip(b.String(), maxDiff)
			t.Added, t.Removed = countDiff(t.Diff)
			t.Title = Rel(it.Changes[0].Path)
			if n := len(it.Changes) - 1; n > 0 {
				t.Title += " and " + strconv.Itoa(n) + " more"
			}
		}
	case "mcpToolCall":
		t.Kind, t.Name = ToolMCP, it.Server+" · "+it.Tool
		t.Title, t.Input = it.Tool, clipJSON(it.Arguments)
		if done {
			t.Output = clip(string(it.Result), maxOutput)
			if len(it.Error) > 0 && string(it.Error) != "null" {
				t.Status, t.Output = StatusFailed, clip(string(it.Error), maxOutput)
			}
		}
	case "webSearch":
		t.Kind, t.Title = ToolWeb, it.Query
	case "collabAgentToolCall":
		t.Kind, t.Title = ToolAgent, firstLine(it.Prompt)
	}
	cp := *t
	if done {
		delete(c.items, it.ID)
	}
	return []Event{{Time: now, Kind: KindTool, ID: it.ID, Tool: &cp}}
}

// spawned follows a subagent's start, and its end when codex reports
// that before the subagent's own turn ends. Its card runs until then,
// whatever its item says.
func (c *codex) spawned(it codexItem, now time.Time) []Event {
	switch {
	case it.AgentThread == "":
		return nil
	case it.Kind == "completed":
		return c.agentDone(c.threads[it.AgentThread], StatusDone, now)
	case it.Kind != "started":
		return nil // something else it did: its own thread says how it's going
	}
	if id, ok := c.threads[it.AgentThread]; ok && id != it.ID {
		return nil
	}
	t := c.agents[it.ID]
	if t == nil {
		name := strings.ReplaceAll(path.Base(it.AgentPath), "_", " ")
		if name == "." || name == "/" {
			name = ""
		}
		t = &Tool{Name: "agent", Kind: ToolAgent, Title: name}
		c.agents[it.ID] = t
		c.threads[it.AgentThread] = it.ID
	}
	t.Status = StatusRunning
	cp := *t
	return []Event{{Time: now, Kind: KindTool, ID: it.ID, Tool: &cp}}
}

// agentDone ends a subagent's card, and any of its steps still running.
func (c *codex) agentDone(id, status string, now time.Time) []Event {
	t := c.agents[id]
	if t == nil || t.Status != StatusRunning {
		return nil
	}
	var out []Event
	for ask, a := range c.asks {
		if a.thread != "" && c.threads[a.thread] == id {
			delete(c.asks, ask)
			out = append(out, Event{Time: now, Kind: KindAnswered, ID: ask, Text: "withdrawn"})
		}
	}
	for sid, st := range c.items {
		if st.Parent != id {
			continue
		}
		if st.Status == StatusRunning {
			st.Status = stepEnd(status)
			cp := *st
			out = append(out, Event{Time: now, Kind: KindTool, ID: sid, Tool: &cp})
		}
		delete(c.items, sid)
	}
	t.Status = status
	cp := *t
	return append(out, Event{Time: now, Kind: KindTool, ID: id, Tool: &cp})
}

// subagent reads what happens in a subagent's thread: its steps nest in
// its card, its last message is the card's output, and its turn is the
// card's status. A thread that isn't a known subagent's is ignored.
func (c *codex) subagent(id string, l codexLine, now time.Time) []Event {
	t := c.agents[id]
	if t == nil {
		return nil
	}
	switch l.Method {
	case "turn/started":
		if t.Status != StatusRunning {
			t.Status = StatusRunning // given more to do
			cp := *t
			return []Event{{Time: now, Kind: KindTool, ID: id, Tool: &cp}}
		}
	case "turn/completed":
		var p struct {
			Turn struct {
				Status string `json:"status"`
			} `json:"turn"`
		}
		json.Unmarshal(l.Params, &p)
		status := StatusDone
		switch p.Turn.Status {
		case "failed":
			status = StatusFailed
		case "interrupted":
			status = StatusDeclined
		}
		return c.agentDone(id, status, now)
	case "item/started", "item/completed":
		var p struct {
			Item codexItem `json:"item"`
		}
		if json.Unmarshal(l.Params, &p) != nil || p.Item.ID == "" {
			return nil
		}
		it := p.Item
		switch it.Type {
		case "agentMessage":
			if l.Method == "item/completed" && strings.TrimSpace(it.Text) != "" {
				t.Output = clip(it.Text, maxOutput)
			}
			return nil
		case "plan", "reasoning", "contextCompaction":
			return nil
		}
		if it.Type == "subAgentActivity" {
			// A subagent of a subagent nests in its parent's card.
			out := c.spawned(it, now)
			if a := c.agents[it.ID]; a != nil && a.Parent == "" {
				a.Parent = id
				for _, e := range out {
					if e.ID == it.ID {
						e.Tool.Parent = id
					}
				}
			}
			return out
		}
		return c.item(it, id, l.Method == "item/completed", now)
	}
	return nil
}

func codexStatus(s string, done bool) string {
	switch s {
	case "failed":
		return StatusFailed
	case "declined":
		return StatusDeclined
	case "completed":
		return StatusDone
	}
	if done {
		return StatusDone
	}
	return StatusRunning
}

func (c *codex) turnCompleted(l codexLine, now time.Time) []Event {
	var p struct {
		Turn struct {
			Status     string `json:"status"`
			DurationMS int64  `json:"durationMs"`
			Error      *struct {
				Message string          `json:"message"`
				Info    json.RawMessage `json:"codexErrorInfo"`
			} `json:"error"`
		} `json:"turn"`
	}
	if json.Unmarshal(l.Params, &p) != nil {
		return nil
	}
	c.turn = ""
	turn := &Turn{State: TurnDone, DurationMS: p.Turn.DurationMS}
	var out []Event
	switch p.Turn.Status {
	case "interrupted":
		turn.State = TurnInterrupted
	case "failed":
		turn.State = TurnFailed
		if p.Turn.Error != nil {
			turn.Error = clip(p.Turn.Error.Message, 4096)
			if strings.Contains(string(p.Turn.Error.Info), "401") || strings.Contains(p.Turn.Error.Message, "401 Unauthorized") {
				out = append(out, Event{Time: now, Kind: KindLogin, Login: &Login{Needed: true}})
			}
		}
		if turn.Error == "" {
			turn.Error = "The turn failed."
		}
	}
	for id, a := range c.asks {
		if a.thread != "" && a.thread != c.thread {
			continue // a subagent's, which its own turn settles
		}
		delete(c.asks, id)
		out = append(out, Event{Time: now, Kind: KindAnswered, ID: id, Text: "withdrawn"})
	}
	for id, t := range c.items {
		if c.agents[t.Parent] != nil {
			continue // a subagent's step: its own turn settles it
		}
		if t.Status == StatusRunning {
			t.Status = StatusFailed
			if turn.State == TurnInterrupted {
				t.Status = StatusDeclined
			}
			cp := *t
			out = append(out, Event{Time: now, Kind: KindTool, ID: id, Tool: &cp})
		}
		delete(c.items, id)
	}
	return append(out, Event{Time: now, Kind: KindTurn, Turn: turn})
}

// serverRequest handles codex asking us something: approvals and questions
// become asks; anything else is answered at once, so codex never waits on
// a reply that won't come.
func (c *codex) serverRequest(l codexLine, now time.Time) Out {
	askID := "c" + strings.Trim(string(l.ID), `"`)
	var p struct {
		ItemID      string          `json:"itemId"`
		Command     string          `json:"command"`
		Reason      string          `json:"reason"`
		Questions   json.RawMessage `json:"questions"`
		Permissions json.RawMessage `json:"permissions"`
		ThreadID    string          `json:"threadId"`
	}
	json.Unmarshal(l.Params, &p)
	a := &Ask{Kind: AskPermission, ToolID: p.ItemID, Reason: p.Reason, Always: true}
	ask := codexAsk{rpcID: l.ID, method: l.Method, thread: p.ThreadID}
	switch l.Method {
	case "item/commandExecution/requestApproval":
		a.Tool, a.Title, a.Detail = "command", "Run "+firstLine(p.Command), clip(p.Command, maxOutput)
	case "item/fileChange/requestApproval":
		a.Tool, a.Title = "edit", "Edit files"
		if t := c.items[p.ItemID]; t != nil {
			a.Title, a.Detail = "Edit "+t.Title, t.Diff
		}
	case "item/permissions/requestApproval":
		a.Tool, a.Title, a.Detail = "permissions", "Grant more access", clip(prettyJSON(p.Permissions), maxOutput)
		ask.request = p.Permissions
	case "item/tool/requestUserInput":
		var qs []struct {
			ID       string   `json:"id"`
			Header   string   `json:"header"`
			Question string   `json:"question"`
			Options  []Option `json:"options"`
		}
		json.Unmarshal(p.Questions, &qs)
		ask.questions = []Question{}
		for _, q := range qs {
			if q.ID != "" {
				ask.questions = append(ask.questions, Question{ID: q.ID, Header: q.Header, Question: q.Question, Options: q.Options})
			}
		}
		a.Kind, a.Title, a.Questions, a.Always = AskQuestion, "A question for you", ask.questions, false
	case "mcpServer/elicitation/request":
		return Out{Write: [][]byte{line(map[string]any{"id": l.ID, "result": map[string]any{"action": "decline"}})}}
	default:
		return Out{Write: [][]byte{line(map[string]any{"id": l.ID, "error": map[string]any{"code": -32601, "message": "skybreaker does not handle " + l.Method}})}}
	}
	c.asks[askID] = ask
	// codex keeps a turn's approval policy for the whole turn, so a mode
	// switched mid-turn would go on asking: what the mode allows is
	// answered here, at once.
	if a.Kind == AskPermission && (c.mode == ModeAuto || c.mode == ModeEdits && a.Tool == "edit") {
		lines, _ := c.Answer(askID, Answer{Allow: true})
		return Out{Write: lines}
	}
	return Out{Events: []Event{{Time: now, Kind: KindAsk, ID: askID, Ask: a}}}
}
