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
	if caps := strings.Join(cfg.Projects["my-repo"].Capabilities, ","); caps != "github,claude-login" {
		t.Errorf("capabilities = %s", caps)
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
