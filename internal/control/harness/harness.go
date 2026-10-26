// Package harness describes the agent CLIs skybreaker runs (Claude Code,
// Codex and pi, plus a plain shell) as manifests, so a flag that changes is
// configuration rather than code.
package harness

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Manifest is everything skybreaker needs to know to install, run and log in a
// harness. Install, Login and Egress describe the harness for the operator
// (the image's Dockerfile, the broker's allow list); skybreaker reads none of them.
type Manifest struct {
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description" yaml:"description"`
	// Install is a shell snippet that adds the harness to the sandbox image.
	Install string   `json:"install" yaml:"install"`
	TUI     []string `json:"tui" yaml:"tui"`
	// Headless is a template; see HeadlessArgs for its placeholders.
	Headless []string `json:"headless" yaml:"headless"`
	// Approval names the adapter that relays harness permission prompts.
	Approval string `json:"approval" yaml:"approval"`
	// Login lists paths relative to $HOME that hold the harness login.
	Login []string `json:"login" yaml:"login"`
	// Egress lists the hosts the harness needs to reach. It's a note for
	// the broker config, which alone decides egress: never enforce it here.
	Egress []string          `json:"egress" yaml:"egress"`
	Env    map[string]string `json:"env,omitempty" yaml:"env,omitempty"`
	// Result is the format of headless output: claude-stream-json,
	// codex-json or text.
	Result string `json:"result" yaml:"result"`
	// Chat names the driver (package chat) that runs this harness as a
	// conversation in its structured mode; empty means terminal only.
	Chat string `json:"chat,omitempty" yaml:"chat,omitempty"`
	// Resume is the TUI command that continues a chat's conversation,
	// with {resume}, {mcp_config} and {mcp_url}; a chat opened in a
	// terminal runs it.
	Resume []string `json:"resume,omitempty" yaml:"resume,omitempty"`
}

var (
	approvals    = []string{"claude-hooks", "codex-app-server", "none"}
	results      = []string{"claude-stream-json", "codex-json", "text"}
	chats        = []string{"", "claude", "codex", "pi"}
	validName    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	placeholder  = regexp.MustCompile(`\{[a-z_]+\}`)
	placeholders = []string{"{prompt}", "{budget}", "{allowed_tools}", "{mcp_config}", "{mcp_url}", "{model}", "{permission_mode}"}
	// mcpFlags are the placeholders that hand a harness Skybreaker's MCP
	// server: a config file naming it, or its URL.
	mcpFlags = []string{"{mcp_config}", "{mcp_url}"}
)

// Shell is the builtin harness every project gets.
const Shell = "shell"

// codexMCP is the -c override that names Skybreaker's MCP server to Codex.
// In a terminal, Codex asks you before running its tools.
const codexMCP = `mcp_servers.skybreaker.url="{mcp_url}"`

// codexRunMCP is codexMCP for a scheduled run, where nobody can answer:
// codex exec never asks, so it would refuse every tool that changes
// anything (claim_item, say). The board's tools are scoped to the project,
// so a run may use them as a Claude run may with allowed_tools.
const codexRunMCP = `mcp_servers.skybreaker={url="{mcp_url}", default_tools_approval_mode="approve"}`

// Builtins returns fresh copies of the built-in manifests. Check the flags
// against the installed CLI versions (skybreaker-sandbox-update does, for the
// ones the chat drivers use) and override them in config where they differ.
func Builtins() map[string]Manifest {
	return map[string]Manifest{
		"claude": {
			Name:        "claude",
			Description: "Claude Code",
			Install:     "npm install -g @anthropic-ai/claude-code",
			TUI:         []string{"claude", "--mcp-config", "{mcp_config}"},
			// Inline, not full screen: the conversation goes into the
			// terminal's history, which the UI scrolls natively.
			Env: map[string]string{"CLAUDE_CODE_DISABLE_ALTERNATE_SCREEN": "1"},
			// The prompt sits right after -p: --allowedTools takes several
			// values and would swallow a trailing prompt. Not --bare: it
			// reads no subscription sign-in, no hooks and no CLAUDE.md, so
			// a scheduled run would fail, unapproved and unbriefed.
			Headless: []string{"claude", "-p", "{prompt}", "--output-format", "stream-json", "--verbose",
				"--permission-prompts", "none", "--permission-mode", "{permission_mode}", "--max-budget-usd", "{budget}", "--allowedTools", "{allowed_tools}",
				"--mcp-config", "{mcp_config}", "--model", "{model}"},
			Approval: "claude-hooks",
			Login:    []string{".claude", ".claude.json"},
			Egress:   []string{"api.anthropic.com", "statsig.anthropic.com", "sentry.io"},
			Result:   "claude-stream-json",
			Chat:     "claude",
			Resume:   []string{"claude", "--resume", "{resume}", "--mcp-config", "{mcp_config}"},
		},
		"codex": {
			Name:        "codex",
			Description: "OpenAI Codex CLI",
			Install:     "npm install -g @openai/codex",
			// Skybreaker's MCP server goes in as a config override, as the
			// chat driver's does: Codex's config is the agent's to edit.
			TUI: []string{"codex", "-c", codexMCP, "--no-alt-screen"},
			Headless: []string{"codex", "exec", "--json", "--sandbox", "workspace-write", "-c", codexRunMCP,
				"--model", "{model}", "{prompt}"},
			Approval: "codex-app-server",
			Login:    []string{".codex"},
			Egress:   []string{"api.openai.com", "chatgpt.com"},
			Result:   "codex-json",
			Chat:     "codex",
			Resume:   []string{"codex", "resume", "-c", codexMCP, "{resume}", "--no-alt-screen"},
		},
		"pi": {
			Name:        "pi",
			Description: "pi coding agent, with your own keys or local models",
			Install:     "npm install -g @earendil-works/pi-coding-agent",
			// skybreaker-pi merges Skybreaker's MCP server and the model
			// servers in config (pi.providers) into pi's own config first.
			TUI:      []string{"skybreaker-pi", "--skybreaker-mcp", "{mcp_config}"},
			Headless: []string{"skybreaker-pi", "--skybreaker-mcp", "{mcp_config}", "--print", "{prompt}", "--model", "{model}"},
			// pi's own update check and telemetry: nothing a sandbox may
			// reach, so off rather than refused at the broker each start.
			Env:      map[string]string{"PI_SKIP_VERSION_CHECK": "1", "PI_TELEMETRY": "0"},
			Approval: "none", // pi has no permission system
			Login:    []string{".pi"},
			Egress:   []string{"its provider's API, or a route to your model server"},
			Result:   "text",
			Chat:     "pi",
			Resume:   []string{"skybreaker-pi", "--skybreaker-mcp", "{mcp_config}", "--session-id", "{resume}"},
		},
		// A plain terminal in the sandbox. Every project has it.
		Shell: {
			Name:        Shell,
			Description: "A login shell in the sandbox",
			TUI:         []string{"bash", "-l"},
			Approval:    "none",
			Result:      "text",
		},
	}
}

// Validate checks a manifest is complete and uses known adapters, formats
// and placeholders.
func (m Manifest) Validate() error {
	if !validName.MatchString(m.Name) {
		return fmt.Errorf("harness %q: name must match %s", m.Name, validName)
	}
	if len(m.TUI) == 0 || strings.TrimSpace(m.TUI[0]) == "" {
		return fmt.Errorf("harness %s: tui command is required", m.Name)
	}
	for _, arg := range m.TUI {
		for _, p := range placeholder.FindAllString(arg, -1) {
			if !slices.Contains(mcpFlags, p) {
				return fmt.Errorf("harness %s: the tui command takes only {mcp_config} and {mcp_url}, not %s", m.Name, p)
			}
		}
	}
	for _, arg := range m.Resume {
		for _, p := range placeholder.FindAllString(arg, -1) {
			if p != "{resume}" && !slices.Contains(mcpFlags, p) {
				return fmt.Errorf("harness %s: the resume command takes only {resume}, {mcp_config} and {mcp_url}, not %s", m.Name, p)
			}
		}
	}
	if !slices.Contains(chats, m.Chat) {
		return fmt.Errorf("harness %s: chat %q is not one of %s", m.Name, m.Chat, strings.Join(chats[1:], ", "))
	}
	if !slices.Contains(approvals, m.Approval) {
		return fmt.Errorf("harness %s: approval %q is not one of %s", m.Name, m.Approval, strings.Join(approvals, ", "))
	}
	if !slices.Contains(results, m.Result) {
		return fmt.Errorf("harness %s: result %q is not one of %s", m.Name, m.Result, strings.Join(results, ", "))
	}
	if len(m.Headless) > 0 {
		hasPrompt := false
		for _, arg := range m.Headless {
			for _, p := range placeholder.FindAllString(arg, -1) {
				if !slices.Contains(placeholders, p) {
					return fmt.Errorf("harness %s: unknown placeholder %s in headless command", m.Name, p)
				}
				hasPrompt = hasPrompt || p == "{prompt}"
			}
		}
		if !hasPrompt {
			return fmt.Errorf("harness %s: headless command has no {prompt}", m.Name)
		}
	}
	return nil
}

// MCP is how a harness reaches Skybreaker's MCP server: a config file
// naming it (Claude's --mcp-config) and its URL.
type MCP struct {
	Config string
	URL    string
}

// RunOptions fill a headless command template.
type RunOptions struct {
	Prompt       string
	BudgetUSD    float64
	AllowedTools []string
	MCP          MCP
	Model        string
	// PermissionMode is the harness's own name for the mode a run starts in.
	PermissionMode string
}

// Takes reports whether the headless command has placeholder p, so a job
// setting that fills it has an effect.
func (m Manifest) Takes(p string) bool {
	for _, arg := range m.Headless {
		if slices.Contains(placeholder.FindAllString(arg, -1), p) {
			return true
		}
	}
	return false
}

// TakesMCP reports whether the headless command is given Skybreaker's MCP
// server, which board jobs need.
func (m Manifest) TakesMCP() bool { return m.Takes("{mcp_config}") || m.Takes("{mcp_url}") }

func (m Manifest) endsOptionsBeforePrompt() bool {
	i := slices.Index(m.Headless, "{prompt}")
	return i > 0 && m.Headless[i-1] == "--"
}

// HeadlessArgs fills the headless template (see fill). Placeholders are
// {prompt}, {budget}, {allowed_tools}, {mcp_config}, {mcp_url}, {model}
// and {permission_mode}.
func (m Manifest) HeadlessArgs(o RunOptions) ([]string, error) {
	if len(m.Headless) == 0 {
		return nil, fmt.Errorf("harness %s: no headless command", m.Name)
	}
	if strings.TrimSpace(o.Prompt) == "" {
		return nil, errors.New("harness: empty prompt")
	}
	if strings.HasPrefix(o.Prompt, "-") && !m.endsOptionsBeforePrompt() {
		// A positional prompt, or one after a boolean flag like claude's -p,
		// would be parsed as options.
		return nil, fmt.Errorf("harness %s: a prompt must not start with '-' unless the headless command has \"--\" before {prompt}", m.Name)
	}
	values := map[string]string{
		"{prompt}":          o.Prompt,
		"{allowed_tools}":   strings.Join(o.AllowedTools, ","),
		"{mcp_config}":      o.MCP.Config,
		"{mcp_url}":         plainURL(o.MCP.URL),
		"{model}":           o.Model,
		"{permission_mode}": o.PermissionMode,
	}
	if o.BudgetUSD > 0 {
		values["{budget}"] = strconv.FormatFloat(o.BudgetUSD, 'f', 2, 64)
	}
	return fill(m.Headless, values), nil
}

// TUIArgs is the interactive command, with Skybreaker's MCP server filled
// in or, when there is none, dropped with its flag.
func (m Manifest) TUIArgs(mcp MCP) []string {
	return fill(m.TUI, map[string]string{"{mcp_config}": mcp.Config, "{mcp_url}": plainURL(mcp.URL)})
}

// ResumeArgs is the TUI command that continues conversation id, or the
// plain TUI when there is no conversation or no resume command. The id is
// the harness's, read from the sandbox: one that would pass for a flag
// isn't resumed.
func (m Manifest) ResumeArgs(id string, mcp MCP) []string {
	if id == "" || strings.HasPrefix(id, "-") || len(m.Resume) == 0 {
		return m.TUIArgs(mcp)
	}
	return fill(m.Resume, map[string]string{"{resume}": id, "{mcp_config}": mcp.Config, "{mcp_url}": plainURL(mcp.URL)})
}

var plainURLRe = regexp.MustCompile(`^https?://[A-Za-z0-9._~:/%-]+$`)

// plainURL is url if nothing in it could end the quoted string a template
// puts it in (Codex's TOML -c), else empty, which drops it and its flag.
func plainURL(url string) string {
	if !plainURLRe.MatchString(url) {
		return ""
	}
	return url
}

// fill substitutes placeholders in a command template. An element whose
// placeholder is empty is dropped, along with the flag just before it when
// that flag takes a separate value ("-m {model}" goes, "--x=1 {model}" keeps
// "--x=1"). Values are substituted once, so a value that mentions a
// placeholder stays as written.
func fill(template []string, values map[string]string) []string {
	out := make([]string, 0, len(template))
	for _, arg := range template {
		empty := false
		filled := placeholder.ReplaceAllStringFunc(arg, func(p string) string {
			v := values[p]
			empty = empty || v == ""
			return v
		})
		if empty {
			// Only a bare value consumes the flag before it; an element like
			// "--budget={budget}" is a whole flag on its own.
			if n := len(out); n > 0 && isBareFlag(out[n-1]) && !strings.HasPrefix(arg, "-") {
				out = out[:n-1]
			}
			continue
		}
		out = append(out, filled)
	}
	return out
}

// isBareFlag reports whether arg is a flag whose value is the next element.
func isBareFlag(arg string) bool {
	return strings.HasPrefix(arg, "-") && !strings.Contains(arg, "=")
}

// Registry is the set of harnesses available on this host.
type Registry struct {
	manifests map[string]Manifest
}

// NewRegistry starts from the builtins; an override with the same name
// replaces the builtin entirely. Every manifest is validated.
func NewRegistry(overrides map[string]Manifest) (*Registry, error) {
	r := &Registry{manifests: Builtins()}
	for name, m := range overrides {
		if m.Name == "" {
			m.Name = name
		}
		if m.Name != name {
			return nil, fmt.Errorf("harness %s: name %q doesn't match its key", name, m.Name)
		}
		if err := m.Validate(); err != nil {
			return nil, err
		}
		r.manifests[name] = m
	}
	return r, nil
}

func (r *Registry) Get(name string) (Manifest, bool) {
	m, ok := r.manifests[name]
	return m, ok
}

// Names lists the harnesses, sorted.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.manifests))
	for n := range r.manifests {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}
