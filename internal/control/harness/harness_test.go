package harness

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestBuiltins(t *testing.T) {
	b := Builtins()
	// Claude Code, Codex and pi are supported; the shell is every project's.
	if got := len(b); got != 4 {
		t.Errorf("%d builtins", got)
	}
	for _, name := range []string{"claude", "codex", "pi"} {
		m, ok := b[name]
		if !ok {
			t.Errorf("missing builtin %q", name)
			continue
		}
		if m.Name != name {
			t.Errorf("%s: Name = %q", name, m.Name)
		}
		if err := m.Validate(); err != nil {
			t.Errorf("%s: Validate: %v", name, err)
		}
	}
	if b["claude"].Approval != "claude-hooks" || b["claude"].Result != "claude-stream-json" {
		t.Errorf("claude manifest: %+v", b["claude"])
	}
	sh := b["shell"]
	if err := sh.Validate(); err != nil || !slices.Equal(sh.TUI, []string{"bash", "-l"}) || sh.Approval != "none" {
		t.Errorf("shell manifest: %+v %v", sh, err)
	}
	if _, err := sh.HeadlessArgs(RunOptions{Prompt: "x"}); err == nil {
		t.Error("shell has a headless command")
	}
	// Builtins returns a fresh copy each call: mutating it must not leak.
	b["claude"].TUI[0] = "mutated"
	if Builtins()["claude"].TUI[0] != "claude" {
		t.Error("Builtins shares slices between calls")
	}
}

func TestValidate(t *testing.T) {
	ok := Manifest{Name: "x", TUI: []string{"x"}, Approval: "none", Result: "text"}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid manifest: %v", err)
	}
	tests := map[string]func(*Manifest){
		"empty name":       func(m *Manifest) { m.Name = "" },
		"bad name":         func(m *Manifest) { m.Name = "a b" },
		"empty tui":        func(m *Manifest) { m.TUI = nil },
		"blank tui binary": func(m *Manifest) { m.TUI = []string{""} },
		"unknown approval": func(m *Manifest) { m.Approval = "yolo" },
		"empty approval":   func(m *Manifest) { m.Approval = "" },
		"unknown result":   func(m *Manifest) { m.Result = "xml" },
		"empty result":     func(m *Manifest) { m.Result = "" },
		"unknown placeholder": func(m *Manifest) {
			m.Headless = []string{"x", "{nope}"}
		},
		"headless without prompt": func(m *Manifest) { m.Headless = []string{"x", "-p"} },
		"prompt in tui":           func(m *Manifest) { m.TUI = []string{"x", "{prompt}"} },
		"acp approval (retired)":  func(m *Manifest) { m.Approval = "acp" },
	}
	for name, mutate := range tests {
		m := ok
		mutate(&m)
		if err := m.Validate(); err == nil {
			t.Errorf("%s: Validate succeeded", name)
		}
	}
}

// The agents write their conversation into the terminal's history, which
// the UI scrolls natively, rather than redrawing it inside a full screen.
func TestTUIsRunInline(t *testing.T) {
	b := Builtins()
	if b["claude"].Env["CLAUDE_CODE_DISABLE_ALTERNATE_SCREEN"] != "1" {
		t.Errorf("claude env = %v", b["claude"].Env)
	}
	if !slices.Contains(b["codex"].TUIArgs(MCP{}), "--no-alt-screen") {
		t.Errorf("codex tui = %q", b["codex"].TUIArgs(MCP{}))
	}
}

func TestTUIArgs(t *testing.T) {
	claude := Builtins()["claude"]
	if got := claude.TUIArgs(MCP{Config: "/etc/skybreaker/orbit-mcp.json"}); !slices.Equal(got, []string{"claude", "--mcp-config", "/etc/skybreaker/orbit-mcp.json"}) {
		t.Errorf("claude with mcp = %q", got)
	}
	if got := claude.TUIArgs(MCP{}); !slices.Equal(got, []string{"claude"}) {
		t.Errorf("claude without mcp = %q", got)
	}
	if got := Builtins()["shell"].TUIArgs(MCP{Config: "/x", URL: "http://x"}); !slices.Equal(got, []string{"bash", "-l"}) {
		t.Errorf("shell = %q", got)
	}
}

func TestHeadlessArgsClaude(t *testing.T) {
	m := Builtins()["claude"]
	got, err := m.HeadlessArgs(RunOptions{
		Prompt:         "fix the bug",
		BudgetUSD:      2.5,
		AllowedTools:   []string{"Read", "Edit", "Bash(git:*)"},
		MCP:            MCP{Config: "/etc/skybreaker/orbit-mcp.json"},
		Model:          "claude-opus-5-5",
		PermissionMode: "bypassPermissions",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"claude", "-p", "fix the bug", "--output-format", "stream-json", "--verbose",
		"--permission-prompts", "none", "--permission-mode", "bypassPermissions", "--max-budget-usd", "2.50", "--allowedTools", "Read,Edit,Bash(git:*)",
		"--mcp-config", "/etc/skybreaker/orbit-mcp.json", "--model", "claude-opus-5-5"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestHeadlessArgsDropsEmptyOptions(t *testing.T) {
	m := Builtins()["claude"]
	got, err := m.HeadlessArgs(RunOptions{Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"claude", "-p", "go", "--output-format", "stream-json", "--verbose",
		"--permission-prompts", "none"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestHeadlessArgsSubstitution(t *testing.T) {
	m := Manifest{Name: "t", TUI: []string{"t"}, Approval: "none", Result: "text",
		Headless: []string{"t", "--budget={budget}", "-m", "{model}", "--x", "--", "{prompt}"}}

	tests := []struct {
		name string
		o    RunOptions
		want []string
	}{
		{"inline placeholder", RunOptions{Prompt: "p", BudgetUSD: 1}, []string{"t", "--budget=1.00", "--x", "--", "p"}},
		{"inline empty drops only itself", RunOptions{Prompt: "p", Model: "m1"}, []string{"t", "-m", "m1", "--x", "--", "p"}},
		// A prompt mentioning a placeholder literally must not be substituted again.
		{"no re-substitution", RunOptions{Prompt: "use {budget} and {model}", BudgetUSD: 3, Model: "m"},
			[]string{"t", "--budget=3.00", "-m", "m", "--x", "--", "use {budget} and {model}"}},
		// A prompt starting with "-" is still exactly one argv element.
		{"dash prompt", RunOptions{Prompt: "--dangerously-skip-permissions; rm -rf /"},
			[]string{"t", "--x", "--", "--dangerously-skip-permissions; rm -rf /"}},
		{"multiline prompt", RunOptions{Prompt: "a\nb c"}, []string{"t", "--x", "--", "a\nb c"}},
	}
	for _, tt := range tests {
		got, err := m.HeadlessArgs(tt.o)
		if err != nil {
			t.Errorf("%s: %v", tt.name, err)
			continue
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s:\n got  %q\n want %q", tt.name, got, tt.want)
		}
	}
}

func TestHeadlessArgsDropsOnlyFlagWithoutValue(t *testing.T) {
	// "--x=1" is a complete flag and must survive when the next element drops.
	m := Manifest{Name: "t", TUI: []string{"t"}, Approval: "none", Result: "text",
		Headless: []string{"t", "--x=1", "{model}", "positional", "{mcp_config}", "--verbose", "--budget={budget}", "{prompt}"}}
	got, err := m.HeadlessArgs(RunOptions{Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	// "--budget={budget}" carries its own value, so the boolean "--verbose" stays.
	if want := []string{"t", "--x=1", "positional", "--verbose", "p"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestHeadlessArgsErrors(t *testing.T) {
	if _, err := Builtins()["claude"].HeadlessArgs(RunOptions{}); err == nil {
		t.Error("empty prompt accepted")
	}
	if _, err := Builtins()["claude"].HeadlessArgs(RunOptions{Prompt: "   "}); err == nil {
		t.Error("blank prompt accepted")
	}
	// Every builtin's prompt is a positional argument or follows a boolean
	// flag, so a leading dash would be read as an option.
	for name, m := range Builtins() {
		for _, p := range []string{"--dangerously-skip-permissions", "-c"} {
			if _, err := m.HeadlessArgs(RunOptions{Prompt: p}); err == nil {
				t.Errorf("%s: prompt %q accepted", name, p)
			}
		}
	}
	if _, err := Builtins()["claude"].HeadlessArgs(RunOptions{Prompt: "fix -x handling"}); err != nil {
		t.Errorf("a dash inside the prompt: %v", err)
	}
	m := Manifest{Name: "t", TUI: []string{"t"}, Approval: "none", Result: "text"}
	if _, err := m.HeadlessArgs(RunOptions{Prompt: "p"}); err == nil {
		t.Error("manifest without headless accepted")
	}
}

func TestHeadlessArgsDoesNotAliasTemplate(t *testing.T) {
	m := Builtins()["pi"]
	a, _ := m.HeadlessArgs(RunOptions{Prompt: "one"})
	a[0] = "mutated"
	b, _ := m.HeadlessArgs(RunOptions{Prompt: "two"})
	if b[0] != "skybreaker-pi" || m.Headless[0] != "skybreaker-pi" {
		t.Errorf("template mutated: %q %q", b, m.Headless)
	}
}

func TestRegistry(t *testing.T) {
	r, err := NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Names(); !slices.Equal(got, []string{"claude", "codex", "pi", "shell"}) {
		t.Errorf("Names = %v", got)
	}
	if _, ok := r.Get("nope"); ok {
		t.Error("Get(nope) found something")
	}

	r, err = NewRegistry(map[string]Manifest{
		"claude": {TUI: []string{"claude", "--rc"}, Approval: "claude-hooks", Result: "claude-stream-json",
			Headless: []string{"claude", "-p", "{prompt}"}},
		"aider": {TUI: []string{"aider"}, Approval: "none", Result: "text"},
	})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := r.Get("claude")
	if c.Name != "claude" || !slices.Equal(c.TUI, []string{"claude", "--rc"}) || len(c.Egress) != 0 {
		t.Errorf("override should replace the builtin wholesale: %+v", c)
	}
	if a, ok := r.Get("aider"); !ok || a.Name != "aider" {
		t.Errorf("new harness: %+v %v", a, ok)
	}
	if !slices.Contains(r.Names(), "aider") {
		t.Error("aider missing from Names")
	}

	if _, err := NewRegistry(map[string]Manifest{"bad": {TUI: nil, Approval: "none", Result: "text"}}); err == nil {
		t.Error("invalid override accepted")
	}
	if _, err := NewRegistry(map[string]Manifest{"x": {Name: "y", TUI: []string{"x"}, Approval: "none", Result: "text"}}); err == nil ||
		!strings.Contains(err.Error(), "x") {
		t.Errorf("mismatched name: %v", err)
	}
}

func TestCodexHeadlessFlags(t *testing.T) {
	// `codex exec` never asks for approval and rejects --ask-for-approval
	// (checked against codex-cli 0.163); the sandbox mode is all it needs.
	m := Builtins()["codex"]
	args, err := m.HeadlessArgs(RunOptions{Prompt: "do it"})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(args, "--ask-for-approval") || !slices.Contains(args, "--sandbox") || args[len(args)-1] != "do it" {
		t.Fatalf("codex argv %v", args)
	}
}

func TestChatAndResume(t *testing.T) {
	b := Builtins()
	if b["claude"].Chat != "claude" || b["codex"].Chat != "codex" || b["shell"].Chat != "" || b["pi"].Chat != "pi" {
		t.Errorf("chat drivers: claude %q codex %q shell %q pi %q", b["claude"].Chat, b["codex"].Chat, b["shell"].Chat, b["pi"].Chat)
	}
	got := b["claude"].ResumeArgs("abc", MCP{Config: "/etc/skybreaker/orbit-mcp.json"})
	if !slices.Equal(got, []string{"claude", "--resume", "abc", "--mcp-config", "/etc/skybreaker/orbit-mcp.json"}) {
		t.Errorf("claude resume = %q", got)
	}
	if got := b["codex"].ResumeArgs("t1", MCP{}); !slices.Equal(got, []string{"codex", "resume", "t1", "--no-alt-screen"}) {
		t.Errorf("codex resume = %q", got)
	}
	if got := b["pi"].ResumeArgs("p1", MCP{Config: "/etc/skybreaker/mcp.json"}); !slices.Equal(got, []string{"skybreaker-pi", "--skybreaker-mcp", "/etc/skybreaker/mcp.json", "--session-id", "p1"}) {
		t.Errorf("pi resume = %q", got)
	}
	// Nothing to resume, or no resume command: the plain TUI.
	if got := b["claude"].ResumeArgs("", MCP{}); !slices.Equal(got, []string{"claude"}) {
		t.Errorf("claude without a conversation = %q", got)
	}
	if got := b["shell"].ResumeArgs("x", MCP{}); !slices.Equal(got, []string{"bash", "-l"}) {
		t.Errorf("shell resume = %q", got)
	}
	m := b["claude"]
	m.Chat = "gemini"
	if err := m.Validate(); err == nil || !strings.Contains(err.Error(), "chat") {
		t.Errorf("unknown chat driver: %v", err)
	}
	m = b["claude"]
	m.Resume = []string{"claude", "--resume", "{prompt}"}
	if err := m.Validate(); err == nil {
		t.Error("resume template took {prompt}")
	}
}

func TestCodexGetsSkybreakersMCPServer(t *testing.T) {
	codex := Builtins()["codex"]
	mcp := MCP{Config: "/etc/skybreaker/mcp.json", URL: "http://broker:8402/mcp/orbit/tok/-"}
	flag := []string{"-c", `mcp_servers.skybreaker.url="http://broker:8402/mcp/orbit/tok/-"`}
	if got := codex.TUIArgs(mcp); !slices.Equal(got, append(append([]string{"codex"}, flag...), "--no-alt-screen")) {
		t.Errorf("tui = %q", got)
	}
	if got := codex.ResumeArgs("t1", mcp); !slices.Equal(got, append(append([]string{"codex", "resume"}, flag...), "t1", "--no-alt-screen")) {
		t.Errorf("resume = %q", got)
	}
	got, err := codex.HeadlessArgs(RunOptions{Prompt: "go", MCP: mcp})
	if err != nil {
		t.Fatal(err)
	}
	// A run can't answer Codex's questions, so the board's tools are
	// approved for it.
	run := []string{"-c", `mcp_servers.skybreaker={url="http://broker:8402/mcp/orbit/tok/-", default_tools_approval_mode="approve"}`}
	if !hasSeq(got, run...) || got[len(got)-1] != "go" {
		t.Errorf("headless = %q", got)
	}
	if got, _ := codex.HeadlessArgs(RunOptions{Prompt: "go"}); slices.Contains(got, "-c") {
		t.Errorf("headless without a server = %q", got)
	}
	// Without a server, the flag goes with it.
	if got := codex.TUIArgs(MCP{}); slices.Contains(got, "-c") {
		t.Errorf("tui without a server = %q", got)
	}
	// A URL that could end the TOML string it goes in is left out.
	for _, bad := range []string{`http://x/"; approval_policy="never`, "http://x/\\", "http://x/\nnl", "file:///etc"} {
		if got := codex.TUIArgs(MCP{URL: bad}); slices.Contains(got, "-c") {
			t.Errorf("tui with url %q = %q", bad, got)
		}
		if got, _ := codex.HeadlessArgs(RunOptions{Prompt: "go", MCP: MCP{URL: bad}}); slices.Contains(got, "-c") {
			t.Errorf("headless with url %q = %q", bad, got)
		}
	}
	// A conversation ID that would pass for a flag isn't resumed.
	if got := codex.ResumeArgs("--dangerously-bypass-approvals-and-sandbox", mcp); slices.Contains(got, "resume") || slices.Contains(got, "--dangerously-bypass-approvals-and-sandbox") {
		t.Errorf("resume with a flag for an id = %q", got)
	}
	if !codex.TakesMCP() || codex.Takes("{budget}") || codex.Takes("{allowed_tools}") {
		t.Error("codex's headless placeholders")
	}
	c := Builtins()["claude"]
	if !c.TakesMCP() || !c.Takes("{budget}") || !c.Takes("{allowed_tools}") {
		t.Error("claude's headless placeholders")
	}
	if err := (Manifest{Name: "x", TUI: []string{"x", "{mcp_url}"}, Resume: []string{"x", "{resume}", "{mcp_url}"}, Approval: "none", Result: "text"}).Validate(); err != nil {
		t.Errorf("mcp_url in tui and resume: %v", err)
	}
}

func hasSeq(c []string, seq ...string) bool {
	for i := 0; i+len(seq) <= len(c); i++ {
		if slices.Equal(c[i:i+len(seq)], seq) {
			return true
		}
	}
	return false
}
