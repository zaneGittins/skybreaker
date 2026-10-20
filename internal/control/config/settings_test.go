package config

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	policy "github.com/zaneGittins/skybreaker/internal/proxy/config"
	rules "github.com/zaneGittins/skybreaker/internal/proxy/policy"
	"github.com/zaneGittins/skybreaker/internal/proxy/secrets"
)

var openai = []*policy.Entry{{Host: "api.openai.com", Mode: policy.ModeMITM, Methods: []string{"GET", "POST"}}}

func TestEditAccess(t *testing.T) {
	path := mainConfig(t)
	before, after, err := Edit(path, func(f *AppFiles) error {
		f.Access.Capabilities = map[string][]*policy.Entry{"openai": openai}
		f.SetGrant("orbit", Grant{Capabilities: []string{"openai"}, Allow: []*policy.Entry{{Host: "pypi.org", Mode: policy.ModeTunnel}}})
		return nil
	}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if pp, _ := before.Policy.Project("orbit"); pp.Host("api.openai.com") != nil {
		t.Fatal("before already reaches it")
	}
	for _, c := range []*Config{after, mustLoad(t, path)} {
		pp, _ := c.Policy.Project("orbit")
		if pp.Host("api.openai.com") == nil || pp.Host("pypi.org") == nil || pp.Host("registry.npmjs.org") == nil {
			t.Fatal("orbit doesn't reach the app's hosts and its own")
		}
		// The file's part and the app's part stay apart, for the settings page.
		if f := c.FromFile("orbit"); slices.Contains(f.Capabilities, "openai") || len(f.Allow) != 1 {
			t.Fatalf("file part %+v", f)
		}
		if !c.AppBundle("openai") || c.AppBundle("github") {
			t.Fatal("AppBundle")
		}
	}
	fi, _ := os.Stat(PathsFor(path).Access)
	if fi == nil || fi.Mode().Perm() != 0o600 {
		t.Fatal("access.json isn't 0600")
	}
	f, _ := ReadAppFiles(path)
	if g := f.Grant("orbit"); !slices.Equal(g.Capabilities, []string{"openai"}) {
		t.Fatalf("grant %+v", g)
	}
}

// A project made in the app keeps its bundles in projects.json until its
// access is edited, then in access.json like any other.
func TestEditAccessOfAddedProject(t *testing.T) {
	path := mainConfig(t)
	if err := AddProject(path, "comet", Added{Repo: "a/comet", Harnesses: []string{"claude"}, Capabilities: []string{"github"}}, nil); err != nil {
		t.Fatal(err)
	}
	f, _ := ReadAppFiles(path)
	if g := f.Grant("comet"); !slices.Equal(g.Capabilities, []string{"github"}) {
		t.Fatalf("grant %+v", g)
	}
	_, after, err := Edit(path, func(f *AppFiles) error {
		f.SetGrant("comet", Grant{Capabilities: []string{"tracker"}})
		return nil
	}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if p := after.Projects["comet"]; !slices.Equal(p.Capabilities, []string{"tracker"}) {
		t.Fatalf("comet %+v", p.Capabilities)
	}
	if f, _ := ReadAppFiles(path); len(f.Projects["comet"].Capabilities) != 0 {
		t.Fatal("projects.json still grants")
	}
}

func TestEditRefusals(t *testing.T) {
	path := mainConfig(t)
	snapshot := func() string {
		var s string
		for _, p := range []string{PathsFor(path).Projects, PathsFor(path).Access, PathsFor(path).Secrets} {
			b, _ := os.ReadFile(p)
			s += string(b) + "|"
		}
		return s
	}
	was := snapshot()
	route := []*policy.Entry{{Route: "/x", Upstream: "https://x.example", Rules: []rules.Rule{{Match: "GET /**", Action: rules.Auto}}}}
	for name, c := range map[string]struct {
		change func(*AppFiles) error
		check  func(*Config) error
		want   string
	}{
		"a bundle the file has": {func(f *AppFiles) error {
			f.Access.Capabilities = map[string][]*policy.Entry{"github": openai}
			return nil
		}, nil, "github"},
		"a route": {func(f *AppFiles) error { f.Access.Capabilities = map[string][]*policy.Entry{"x": route}; return nil }, nil, "hosts only"},
		"a CA file": {func(f *AppFiles) error {
			f.SetGrant("orbit", Grant{Allow: []*policy.Entry{{Host: "x.example", Mode: policy.ModeMITM, Methods: []string{"GET"}, CAFile: "/etc/shadow"}}})
			return nil
		}, nil, "hosts only"},
		"a private address": {func(f *AppFiles) error {
			f.SetGrant("orbit", Grant{Allow: []*policy.Entry{{Host: "x.example", Mode: policy.ModeTunnel, AllowPrivate: []string{"192.168.0.0/16"}}}})
			return nil
		}, nil, "hosts only"},
		"a wildcard": {func(f *AppFiles) error {
			f.SetGrant("orbit", Grant{Allow: []*policy.Entry{{Host: "*", Mode: policy.ModeTunnel}}})
			return nil
		}, nil, "wildcard"},
		"a host listed twice": {func(f *AppFiles) error {
			f.SetGrant("orbit", Grant{Allow: []*policy.Entry{{Host: "registry.npmjs.org", Mode: policy.ModeTunnel}}})
			return nil
		}, nil, "twice"},
		"an unknown bundle": {func(f *AppFiles) error { f.SetGrant("orbit", Grant{Capabilities: []string{"nope"}}); return nil }, nil, "nope"},
		"the broker's check": {func(f *AppFiles) error {
			f.SetGrant("orbit", Grant{Allow: []*policy.Entry{{Host: "pypi.org", Mode: policy.ModeTunnel}}})
			return nil
		},
			func(*Config) error { return errors.New("secret x isn't bound") }, "bound"},
		"the change itself": {func(f *AppFiles) error { return errors.New("no thanks") }, nil, "no thanks"},
	} {
		_, _, err := Edit(path, c.change, c.check, false)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
	if snapshot() != was {
		t.Fatal("a refused edit changed a file")
	}
	// A dry run checks and reports, and saves nothing.
	_, after, err := Edit(path, func(f *AppFiles) error {
		f.SetGrant("orbit", Grant{Allow: []*policy.Entry{{Host: "pypi.org", Mode: policy.ModeTunnel}}})
		return nil
	}, nil, true)
	if err != nil || after == nil {
		t.Fatal(err)
	}
	if pp, _ := after.Policy.Project("orbit"); pp.Host("pypi.org") == nil || snapshot() != was {
		t.Fatal("dry run")
	}
}

// The check sees the app's secrets as they would be saved.
func TestEditSecretsReachCheck(t *testing.T) {
	path := mainConfig(t)
	var saw string
	_, _, err := Edit(path, func(f *AppFiles) error {
		f.Secrets.Secrets = append(f.Secrets.Secrets, secrets.Stored{Spec: secrets.Spec{Name: "openai", Kind: "static", Value: "sk", Hosts: []string{"api.openai.com"}}})
		return nil
	}, func(c *Config) error {
		f, err := secrets.ReadAppFile(c.AppSecretsFile)
		if err == nil && len(f.Secrets) == 1 {
			saw = f.Secrets[0].Name
		}
		return err
	}, false)
	if err != nil || saw != "openai" {
		t.Fatalf("%v saw %q", err, saw)
	}
	c := mustLoad(t, path)
	if c.AppSecretsFile != PathsFor(path).Secrets {
		t.Fatalf("AppSecretsFile %q", c.AppSecretsFile)
	}
	fi, _ := os.Stat(c.AppSecretsFile)
	if fi.Mode().Perm() != 0o600 {
		t.Fatal("app secrets aren't 0600")
	}
}

// Grants for a project the config file no longer has are ignored: they
// only ever add, so dropping them can't widen anything.
func TestGrantForRemovedProject(t *testing.T) {
	path := mainConfig(t)
	os.WriteFile(PathsFor(path).Access, []byte(`{"projects": {"gone": {"capabilities": ["github"]}}}`), 0o600)
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
}

func TestAccessFileIsStrict(t *testing.T) {
	for name, body := range map[string]string{
		"unknown field": `{"projects": {"orbit": {"env": {"A": "b"}}}}`,
		"a route":       `{"capabilities": {"x": [{"route": "/x", "upstream": "https://x.example", "rules": [{"match": "GET /**", "action": "auto"}]}]}}`,
	} {
		path := mainConfig(t)
		os.WriteFile(PathsFor(path).Access, []byte(body), 0o600)
		if _, err := Load(path); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
}

func TestAdmins(t *testing.T) {
	c, err := load(t, strings.Replace(good, `"users": ["zane@example.com"],`, `"users": ["zane@example.com", "kim@example.com"],`, 1))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Admin("zane@example.com") || !c.Admin("kim@example.com") || c.Admin("eve@example.com") {
		t.Fatal("admins default to users")
	}
	c, err = load(t, strings.Replace(good, `"users": ["zane@example.com"],`, `"users": ["zane@example.com", "kim@example.com"], "admins": ["zane@example.com"],`, 1))
	if err != nil || !c.Admin("zane@example.com") || c.Admin("kim@example.com") {
		t.Fatalf("listed admins: %v", err)
	}
	if _, err := load(t, strings.Replace(good, `"users": ["zane@example.com"],`, `"users": ["zane@example.com"], "admins": ["eve@example.com"],`, 1)); err == nil {
		t.Fatal("an admin who isn't a user")
	}
}

func TestSettingsOff(t *testing.T) {
	path := mainConfig(t)
	os.WriteFile(path, []byte(strings.Replace(good, `"users"`, `"app_settings": false, "users"`, 1)), 0o600)
	if c := mustLoad(t, path); c.SettingsInApp() {
		t.Fatal("app_settings: false")
	}
	if _, _, err := Edit(path, func(*AppFiles) error { return nil }, nil, false); !errors.Is(err, ErrSettingsOff) {
		t.Fatalf("edit with settings off: %v", err)
	}
	if err := AddProject(path, "comet", Added{Repo: "a/comet", Harnesses: []string{"claude"}}, nil); !errors.Is(err, ErrSettingsOff) {
		t.Fatalf("add with settings off: %v", err)
	}
}

func mustLoad(t *testing.T, path string) *Config {
	t.Helper()
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
