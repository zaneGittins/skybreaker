package setup

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	_ "github.com/zaneGittins/skybreaker/internal/configfile/yamlfmt"
	"github.com/zaneGittins/skybreaker/internal/control/config"
	"github.com/zaneGittins/skybreaker/internal/control/controld"
)

func testLayout(t *testing.T) Layout {
	dir := t.TempDir()
	return Layout{
		Bin: filepath.Join(dir, "bin"), Etc: filepath.Join(dir, "etc"), Data: filepath.Join(dir, "data"),
		Units: filepath.Join(dir, "units"), Image: filepath.Join(dir, "image"), Docker: filepath.Join(dir, "docker", "daemon.json"),
	}
}

func writeAppKey(t *testing.T) string {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "app.pem")
	os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}), 0o600)
	return p
}

func testAnswers(t *testing.T) Answers {
	return Answers{
		Users: []string{"you@github"}, Tailnet: "tail1234.ts.net", AuthKey: "tskey-auth-abc123",
		AppID: "123456", AppKey: writeAppKey(t),
		Repo: "you/My.Repo", Branch: "main", Harnesses: []string{"claude", "codex"},
		GitName: "You", GitEmail: "you@example.com",
		Block: []string{"192.168.77.0/24"}, Image: DefaultImage,
	}
}

// writeConfig lays the rendered files down as an install would.
func writeConfig(t *testing.T, a Answers, l Layout) {
	t.Helper()
	files, err := a.Files(l)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if _, err := writeFile(f.Path, f.Data, f.Mode); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRenderedConfigPassesServerCheck(t *testing.T) {
	l := testLayout(t)
	a := testAnswers(t)
	writeConfig(t, a, l)
	if err := controld.Check(l.Config()); err != nil {
		t.Fatalf("the server rejects the installer's config: %v", err)
	}
	cfg, err := config.Load(l.Config())
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Projects["my-repo"]
	if p == nil || p.Repo != "you/My.Repo" || cfg.UIURL != "https://skybreaker.tail1234.ts.net" || cfg.DataDir != l.Data {
		t.Fatalf("config: %+v, project %+v", cfg, p)
	}
	for _, c := range []string{"github", "claude-login", "codex-login"} {
		if !strings.Contains(strings.Join(p.Capabilities, ","), c) {
			t.Errorf("project lacks %s: %v", c, p.Capabilities)
		}
	}
	for _, f := range []string{l.Config(), l.Secrets(), l.AppKey(), filepath.Join(l.Etc, "env")} {
		if fi, err := os.Stat(f); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v %v", f, fi.Mode(), err)
		}
	}
	env, _ := os.ReadFile(filepath.Join(l.Etc, "env"))
	if string(env) != "TS_AUTHKEY=tskey-auth-abc123\n" {
		t.Errorf("env = %q", env)
	}
}

// Without an App, the install leaves it to Skybreaker's Settings: the
// secret's hosts are bound now, and the server starts without one.
func TestGitHubAppLater(t *testing.T) {
	l := testLayout(t)
	a := testAnswers(t)
	a.AppID, a.AppKey = "", ""
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, a, l)
	if err := controld.Check(l.Config()); err != nil {
		t.Fatalf("the server rejects an install without an App: %v", err)
	}
	sec, _ := os.ReadFile(l.Secrets())
	if !strings.Contains(string(sec), "from_app: true") || strings.Contains(string(sec), "key_file") {
		t.Fatalf("secrets:\n%s", sec)
	}
	if _, err := os.Stat(l.AppKey()); err == nil {
		t.Fatal("wrote a key file without a key")
	}
	half := testAnswers(t)
	half.AppKey = ""
	if half.ValidateGitHub() == nil {
		t.Fatal("an App ID without its key")
	}
}

func TestRenderedConfigOnlyNamesChosenLogins(t *testing.T) {
	l := testLayout(t)
	a := testAnswers(t)
	a.Harnesses = []string{"claude"}
	writeConfig(t, a, l)
	cfg, err := config.Load(l.Config())
	if err != nil {
		t.Fatal(err)
	}
	if caps := strings.Join(cfg.Projects["my-repo"].Capabilities, ","); caps != "github,tools,claude-login" {
		t.Errorf("capabilities = %s", caps)
	}
}

// Agents install their own tools (mise, uv, Playwright), so a fresh
// install reaches where those come from, and only for reading.
func TestRenderedConfigReachesToolDownloads(t *testing.T) {
	l := testLayout(t)
	writeConfig(t, testAnswers(t), l)
	cfg, err := config.Load(l.Config())
	if err != nil {
		t.Fatal(err)
	}
	pp, ok := cfg.Policy.Project("my-repo")
	if !ok {
		t.Fatal("no policy for my-repo")
	}
	for _, h := range []string{"releases.hashicorp.com", "galaxy.ansible.com", "pypi.org", "files.pythonhosted.org", "cdn.playwright.dev", "playwright.download.prss.microsoft.com"} {
		if pp.Host(h) == nil {
			t.Errorf("can't reach %s", h)
		}
	}
	for _, c := range []struct {
		host, method, path, action, follows string
	}{
		{"github.com", "GET", "/digitalocean/doctl/releases/download/v1.150.0/doctl-1.150.0-linux-amd64.tar.gz", "auto", "release-assets.githubusercontent.com"},
		{"github.com", "GET", "/mikefarah/yq/releases/latest/download/yq_linux_amd64", "auto", "github.com"},
		{"api.github.com", "GET", "/repos/hashicorp/terraform/releases/latest", "auto", ""},
		{"api.github.com", "GET", "/repos/digitalocean/doctl/attestations/sha256:abc", "auto", ""},
		{"github.com", "POST", "/digitalocean/doctl/releases/download/v1/x", "deny", ""},
		{"github.com", "POST", "/someone/else.git/git-receive-pack", "deny", ""},
		{"api.github.com", "GET", "/repos/someone/else/contents/secrets.txt", "deny", ""},
	} {
		d := pp.Host(c.host).Rules.Decide(c.method, c.path)
		if string(d.Action) != c.action {
			t.Errorf("%s %s%s = %s (%s), want %s", c.method, c.host, c.path, d.Action, d.Reason, c.action)
		}
		if c.follows != "" && (d.Rule == nil || !d.Rule.Follows(c.follows)) {
			t.Errorf("%s %s%s doesn't follow redirects to %s", c.method, c.host, c.path, c.follows)
		}
	}
}

// skybreaker-sandbox-update finds the image with a sed expression; the
// line must stay in the shape it reads.
func TestRenderedImageLineReadableBySandboxUpdate(t *testing.T) {
	l := testLayout(t)
	writeConfig(t, testAnswers(t), l)
	b, _ := os.ReadFile(l.Config())
	re := regexp.MustCompile(`(?m)^\s*image:\s*"?([^"]*)"?\s*$`)
	if m := re.FindSubmatch(b); m == nil || string(m[1]) != DefaultImage {
		t.Fatalf("image line not found in:\n%s", b)
	}
}

// Values typed into the wizard are quoted, never spliced into YAML.
func TestRenderedConfigQuotesValues(t *testing.T) {
	l := testLayout(t)
	a := testAnswers(t)
	a.GitName = `Mallory" , evil: [x] # `
	writeConfig(t, a, l)
	cfg, err := config.Load(l.Config())
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Projects["my-repo"].Env["GIT_AUTHOR_NAME"]; got != a.GitName {
		t.Fatalf("GIT_AUTHOR_NAME = %q", got)
	}
}

func TestValidate(t *testing.T) {
	for _, c := range []struct {
		field string
		edit  func(*Answers)
	}{
		{"login", func(a *Answers) { a.Users = nil }},
		{"login", func(a *Answers) { a.Users = []string{"you"} }},
		{"login", func(a *Answers) { a.Users = []string{"you @github"} }},
		{"tailnet", func(a *Answers) { a.Tailnet = "" }},
		{"tailnet", func(a *Answers) { a.Tailnet = "https://tail1234.ts.net" }},
		{"auth key", func(a *Answers) { a.AuthKey = "hunter2" }},
		{"auth key", func(a *Answers) { a.AuthKey = "tskey-auth-x\nSKYBREAKER_DO_TOKEN=y" }},
		{"App ID", func(a *Answers) { a.AppID = "abc" }},
		{"private key", func(a *Answers) { a.AppKey = "/nonexistent.pem" }},
		{"private key", func(a *Answers) { a.AppKey = "/etc/hostname" }},
		{"repo", func(a *Answers) { a.Repo = "orbit" }},
		{"repo", func(a *Answers) { a.Repo = "you/*" }},
		{"repo", func(a *Answers) { a.Repo = "you/..." }},
		{"branch", func(a *Answers) { a.Branch = "-x" }},
		{"harness", func(a *Answers) { a.Harnesses = nil }},
		{"harness", func(a *Answers) { a.Harnesses = []string{"claude", "rm -rf"} }},
		{"email", func(a *Answers) { a.GitEmail = "nope" }},
		{"name", func(a *Answers) { a.GitName = "a\x1b[31m" }},
		{"network", func(a *Answers) { a.Block = []string{"192.168.0.0/33"} }},
		{"machine name", func(a *Answers) { a.Hostname = "Sky Breaker" }},
		{"machine name", func(a *Answers) { a.Hostname = "-skybreaker" }},
		{"machine name", func(a *Answers) { a.Hostname = "sky.breaker" }},
		{"machine name", func(a *Answers) { a.Hostname = strings.Repeat("a", 64) }},
	} {
		a := testAnswers(t)
		c.edit(&a)
		if err := a.Validate(); err == nil || !strings.Contains(err.Error(), c.field) {
			t.Errorf("%s: err = %v", c.field, err)
		}
	}
	a := testAnswers(t)
	a.GitName, a.GitEmail, a.Block, a.AuthKey = "", "", nil, ""
	if err := a.Validate(); err != nil {
		t.Errorf("optional fields: %v", err)
	}
}

func TestFilesWithoutAuthKey(t *testing.T) {
	l := testLayout(t)
	a := testAnswers(t)
	a.AuthKey = ""
	files, err := a.Files(l)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Path == filepath.Join(l.Etc, "env") {
			if len(f.Data) != 0 || f.Mode != 0o600 {
				t.Errorf("env = %q, mode %v", f.Data, f.Mode)
			}
			return
		}
	}
	t.Error("no env file: a key from an earlier install would stay")
}

func TestProjectName(t *testing.T) {
	for repo, want := range map[string]string{
		"you/orbit":                      "orbit",
		"you/My.Repo":                    "my-repo",
		"you/__x__":                      "x",
		"you/a_b-c":                      "a-b-c",
		"you/.github":                    "github",
		"you/" + strings.Repeat("a", 60): strings.Repeat("a", 40),
	} {
		if got := ProjectName(repo); got != want {
			t.Errorf("ProjectName(%q) = %q, want %q", repo, got, want)
		}
	}
}

func TestRenderedConfigFitsTheHost(t *testing.T) {
	// A 2-CPU, 4 GB VM: Docker refuses a container more CPUs than the host
	// has, so the config's limits must fit it. A second Skybreaker on the
	// tailnet takes a name of its own, and its address follows.
	l := testLayout(t)
	a := testAnswers(t)
	a.CPUs, a.MemoryGB, a.Hostname = 2, 3, "skybreaker-test"
	writeConfig(t, a, l)
	if err := controld.Check(l.Config()); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(l.Config())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sandbox.CPUs != "2" || cfg.Sandbox.Memory != "3g" || cfg.Tailscale.Hostname != "skybreaker-test" || cfg.UIURL != "https://skybreaker-test.tail1234.ts.net" {
		t.Errorf("cpus %q, memory %q, hostname %q, ui_url %q", cfg.Sandbox.CPUs, cfg.Sandbox.Memory, cfg.Tailscale.Hostname, cfg.UIURL)
	}

	// Answers from before these existed keep today's defaults.
	l = testLayout(t)
	writeConfig(t, testAnswers(t), l)
	if cfg, err = config.Load(l.Config()); err != nil {
		t.Fatal(err)
	}
	if cfg.Sandbox.CPUs != "4" || cfg.Sandbox.Memory != "8g" || cfg.Tailscale.Hostname != "skybreaker" {
		t.Errorf("defaults: cpus %q, memory %q, hostname %q", cfg.Sandbox.CPUs, cfg.Sandbox.Memory, cfg.Tailscale.Hostname)
	}
}

func TestFitHost(t *testing.T) {
	for _, c := range []struct {
		cpus, memKiB, wantCPUs, wantGB int
	}{
		{2, 4012544, 2, 3},   // a 4 GB droplet: some of it is the kernel's
		{8, 32 << 20, 4, 8},  // a big host keeps the defaults
		{1, 1 << 20, 1, 1},   // 1 GB
		{1, 512 << 10, 1, 1}, // never below 1 GB
		{4, 0, 4, 8},         // memory unknown: the default
	} {
		cpus, gb := fitHost(c.cpus, c.memKiB)
		if cpus != c.wantCPUs || gb != c.wantGB {
			t.Errorf("fitHost(%d, %d) = %d, %d; want %d, %d", c.cpus, c.memKiB, cpus, gb, c.wantCPUs, c.wantGB)
		}
	}
	if got := memKiB("MemFree: 10 kB\nMemTotal:        4012544 kB\n"); got != 4012544 {
		t.Errorf("memKiB = %d", got)
	}
	if got := memKiB("garbage"); got != 0 {
		t.Errorf("memKiB(garbage) = %d", got)
	}
}
