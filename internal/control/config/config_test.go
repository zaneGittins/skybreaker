package config

import (
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/zaneGittins/skybreaker/internal/control/sandbox"
)

const good = `{
  "data_dir": "/var/lib/skybreaker",
  "users": ["zane@example.com"],
  "sandbox": {"image": "ghcr.io/zanegittins/skybreaker-sandbox:0.0.1"},
  "projects": {
    "orbit": {"repo": "zaneGittins/orbit", "harnesses": ["claude", "codex"], "key": "ORB",
      "allow": [{"host": "registry.npmjs.org", "mode": "tunnel"}],
      "capabilities": ["github", "tracker"]},
    "bifract": {"repo": "zaneGittins/bifract"}
  },
  "capabilities": {
    "github": [{"host": "api.github.com", "mode": "mitm", "rules": [{"match": "GET /repos/{repo}/**", "action": "auto"}]}],
    "tracker": [{"route": "/tracker", "upstream": "https://tracker.example.ts.net", "rules": [{"match": "POST /mcp", "action": "auto"}]}]
  },
  "jobs": [
    {"name": "nightly-orbit", "project": "orbit", "schedule": "0 2 * * *", "kind": "board", "budget_usd": 5}
  ]
}`

func load(t *testing.T, body string) (*Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "skybreaker.json")
	os.WriteFile(p, []byte(body), 0o600)
	return Load(p)
}

func TestLoadDefaults(t *testing.T) {
	c, err := load(t, good)
	if err != nil {
		t.Fatal(err)
	}
	if c.Sandbox.HookPort != 7701 || c.Sandbox.HookListen != ":7701" || c.Sandbox.SubnetBase != "10.89.0.0/16" {
		t.Errorf("sandbox defaults: %+v", c.Sandbox)
	}
	if c.Tailscale.Hostname != "skybreaker" {
		t.Errorf("tailscale hostname default %q", c.Tailscale.Hostname)
	}
	if c.Broker.Port != 8080 || c.Broker.Listen != ":8080" || c.Broker.Hold.D().Seconds() != 30 {
		t.Errorf("broker defaults: %+v", c.Broker)
	}
	p := c.Projects["bifract"]
	if !slices.Equal(p.Harnesses, []string{"claude", "shell"}) || p.Branch != "main" {
		t.Errorf("project defaults: %+v", p)
	}
	// Every project gets a shell, after its own harnesses.
	if e := c.Projects["orbit"].Harnesses; !slices.Equal(e, []string{"claude", "codex", "shell"}) {
		t.Errorf("orbit harnesses %v", e)
	}
	j := c.Jobs[0]
	if j.Harness != "claude" || j.Timeout.D() == 0 {
		t.Errorf("job defaults: %+v", j)
	}
	if c.Harnesses == nil {
		t.Error("harness registry missing")
	}
}

func TestSubnets(t *testing.T) {
	c, err := load(t, good)
	if err != nil {
		t.Fatal(err)
	}
	a, b := c.Subnet("orbit"), c.Subnet("bifract")
	base := netip.MustParsePrefix("10.89.0.0/16")
	if !base.Contains(a.Addr()) || !base.Contains(b.Addr()) || a == b || a.Bits() != 24 {
		t.Fatalf("subnets %v %v", a, b)
	}
	if got := c.HookURL("orbit", "tok"); got != "http://"+c.Gateway("orbit").String()+":7701/hooks/claude/orbit/tok" {
		t.Errorf("HookURL = %s", got)
	}
}

func TestExplicitSubnet(t *testing.T) {
	c, err := load(t, strings.Replace(good, `"repo": "zaneGittins/bifract"`, `"repo": "zaneGittins/bifract", "subnet": "10.89.200.0/24"`, 1))
	if err != nil {
		t.Fatal(err)
	}
	if c.Subnet("bifract").String() != "10.89.200.0/24" {
		t.Errorf("explicit subnet ignored: %v", c.Subnet("bifract"))
	}
}

func TestInvalid(t *testing.T) {
	cases := map[string]func(string) string{
		"no users":         func(s string) string { return strings.Replace(s, `"users": ["zane@example.com"],`, ``, 1) },
		"bad project name": func(s string) string { return strings.Replace(s, `"bifract": {`, `"Bif ract": {`, 1) },
		"unknown capability": func(s string) string {
			return strings.Replace(s, `["github", "tracker"]`, `["github", "gitlab"]`, 1)
		},
		"bad allow entry": func(s string) string { return strings.Replace(s, `"mode": "tunnel"`, `"mode": "socks"`, 1) },
		"bad capability rule": func(s string) string {
			return strings.Replace(s, `"action": "auto"`, `"action": "perhaps"`, 1)
		},
		"bad board key": func(s string) string { return strings.Replace(s, `"key": "ORB"`, `"key": "ela"`, 1) },
		"shared board key": func(s string) string {
			return strings.Replace(s, `"repo": "zaneGittins/bifract"`, `"repo": "zaneGittins/bifract", "key": "ORB"`, 1)
		},
		"unknown job kind": func(s string) string { return strings.Replace(s, `"kind": "board"`, `"kind": "orbit"`, 1) },
		"bad netguard": func(s string) string {
			return strings.Replace(s, `"sandbox"`, `"broker": {"netguard": {"block": ["lan"]}}, "sandbox"`, 1)
		},
		"runtime is not a choice": func(s string) string {
			return strings.Replace(s, `"image": "ghcr`, `"runtime": "runc", "image": "ghcr`, 1)
		},
		"unknown harness":  func(s string) string { return strings.Replace(s, `["claude", "codex"]`, `["claude", "gemini"]`, 1) },
		"job bad schedule": func(s string) string { return strings.Replace(s, `0 2 * * *`, `0 25 * * *`, 1) },
		"job unknown proj": func(s string) string {
			return strings.Replace(s, `"project": "orbit", "schedule"`, `"project": "nope", "schedule"`, 1)
		},
		"job harness not allowed": func(s string) string {
			return strings.Replace(s, `"kind": "board"`, `"kind": "board", "harness": "pi"`, 1)
		},
		"job harness without headless": func(s string) string {
			return strings.Replace(s, `"kind": "board"`, `"kind": "board", "harness": "shell"`, 1)
		},
		"prompt job no prompt": func(s string) string {
			return strings.Replace(s, `"kind": "board"`, `"kind": "prompt"`, 1)
		},
		"duplicate job": func(s string) string {
			return strings.Replace(s, `"budget_usd": 5}`, `"budget_usd": 5}, {"name": "nightly-orbit", "project": "orbit", "schedule": "@daily", "prompt": "x"}`, 1)
		},
		"subnet outside base": func(s string) string {
			return strings.Replace(s, `"repo": "zaneGittins/bifract"`, `"repo": "zaneGittins/bifract", "subnet": "192.168.77.0/24"`, 1)
		},
		"subnet collision": func(s string) string {
			taken := sandbox.SubnetFor("orbit", netip.MustParsePrefix("10.89.0.0/16"))
			return strings.Replace(s, `"repo": "zaneGittins/bifract"`, `"repo": "zaneGittins/bifract", "subnet": "`+taken.String()+`"`, 1)
		},
		"broker port is the hook port": func(s string) string {
			return strings.Replace(s, `"sandbox"`, `"broker": {"port": 7701}, "sandbox"`, 1)
		},
		"no image": func(s string) string {
			return strings.Replace(s, `"image": "ghcr.io/zanegittins/skybreaker-sandbox:0.0.1"`, `"image": ""`, 1)
		},
		"relative data dir": func(s string) string { return strings.Replace(s, `"/var/lib/skybreaker"`, `"data"`, 1) },
		"bad repo":          func(s string) string { return strings.Replace(s, `zaneGittins/bifract`, `../../etc`, 1) },
		"unknown field":     func(s string) string { return strings.Replace(s, `"users"`, `"usres"`, 1) },
	}
	bifract := func(extra string) func(string) string {
		return func(s string) string {
			return strings.Replace(s, `"repo": "zaneGittins/bifract"`, `"repo": "zaneGittins/bifract", `+extra, 1)
		}
	}
	for _, b := range []string{"-x", "--upload-pack=sh", "a..b", "a b", "a~1", "a^", "a:b", "a/", "/a", "a//b", "a.lock", "a@{1}", `a\\b`, "a?", "a*", "a[x]", ".hidden", "a/.b", "a."} {
		cases["branch "+b] = bifract(`"branch": "` + b + `"`)
	}
	for k, v := range map[string]string{
		"bad env name":     `"env": {"1X": "v"}`,
		"reserved env":     `"env": {"HTTPS_PROXY": "http://evil:1"}`,
		"reserved env lc":  `"env": {"https_proxy": "http://evil:1"}`,
		"env with newline": `"env": {"X": "a\nb"}`,
	} {
		cases[k] = bifract(v)
	}
	for name, mut := range cases {
		if _, err := load(t, mut(good)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestGoodBranchAndEnv(t *testing.T) {
	body := strings.Replace(good, `"repo": "zaneGittins/bifract"`, `"repo": "zaneGittins/bifract", "branch": "release/v1.2_x-y", "env": {"GOFLAGS": "-mod=mod", "ANTHROPIC_API_KEY": "skybreaker-injected"}`, 1)
	if _, err := load(t, body); err != nil {
		t.Fatal(err)
	}
}

func TestHarnessOverride(t *testing.T) {
	body := strings.Replace(good, `"projects"`, `"harnesses": {"claude": {"tui": ["claude", "--rc"], "headless": ["claude", "-p", "{prompt}"], "approval": "claude-hooks", "result": "claude-stream-json"}}, "projects"`, 1)
	c, err := load(t, body)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := c.Harnesses.Get("claude")
	if len(m.TUI) != 2 || m.TUI[1] != "--rc" {
		t.Fatalf("override not applied: %+v", m.TUI)
	}
}

func TestDevMode(t *testing.T) {
	body := strings.Replace(good, `"projects"`, `"tailscale": {"disabled": true, "dev_listen": "127.0.0.1:7700"}, "projects"`, 1)
	if _, err := load(t, body); err != nil {
		t.Fatal(err)
	}
	bad := strings.Replace(good, `"projects"`, `"tailscale": {"disabled": true, "dev_listen": "0.0.0.0:7700"}, "projects"`, 1)
	if _, err := load(t, bad); err == nil {
		t.Fatal("dev mode on a non-loopback address accepted")
	}
}

func TestPolicy(t *testing.T) {
	c, err := load(t, good)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := c.Policy.Project("orbit")
	if !ok || p.Host("registry.npmjs.org") == nil || p.Host("api.github.com") == nil || p.Route("/tracker/mcp") == nil {
		t.Fatalf("orbit's policy: %+v", p)
	}
	// A project with no allow list and no capabilities reaches nothing.
	b, ok := c.Policy.Project("bifract")
	if !ok || b.Host("registry.npmjs.org") != nil {
		t.Fatalf("bifract's policy: %+v", b)
	}
}
