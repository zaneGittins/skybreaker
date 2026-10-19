package config

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func mainConfig(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "skybreaker.json")
	os.WriteFile(p, []byte(good), 0o600)
	return p
}

func TestAddProject(t *testing.T) {
	path := mainConfig(t)
	add := Added{Repo: "zaneGittins/comet", Harnesses: []string{"claude"}, Capabilities: []string{"github"}}
	if err := AddProject(path, "comet", add, nil); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := c.Projects["comet"]
	if !ok || p.Repo != "zaneGittins/comet" || !slices.Equal(p.Capabilities, []string{"github"}) || !c.Added("comet") || c.Added("orbit") {
		t.Fatalf("comet %+v added=%v", p, c.Added("comet"))
	}
	if !c.Subnet("comet").IsValid() || c.Policy == nil {
		t.Fatal("added project not compiled like the rest")
	}
	fi, _ := os.Stat(ProjectsPath(path))
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("projects.json mode %v", fi.Mode().Perm())
	}
}

func TestAddProjectRefusals(t *testing.T) {
	path := mainConfig(t)
	ok := Added{Repo: "zaneGittins/comet", Harnesses: []string{"claude"}, Capabilities: []string{"github"}}
	AddProject(path, "comet", ok, nil)
	before, _ := os.ReadFile(ProjectsPath(path))
	checkFails := func(*Config) error { return errors.New("secret github_app isn't bound to api.github.com") }
	for name, c := range map[string]struct {
		name  string
		added Added
		check func(*Config) error
		want  string
	}{
		"a name in the config file": {"orbit", ok, nil, "already"},
		"a name added already":      {"comet", ok, nil, "already"},
		"a bad name":                {"Comet!", ok, nil, "name"},
		"no repo":                   {"nova", Added{Harnesses: []string{"claude"}}, nil, "repo"},
		"a bad repo":                {"nova", Added{Repo: "not a repo", Harnesses: []string{"claude"}}, nil, "repo"},
		"no agents":                 {"nova", Added{Repo: "a/b"}, nil, "agent"},
		"an unknown agent":          {"nova", Added{Repo: "a/b", Harnesses: []string{"hal"}}, nil, "hal"},
		"an unknown capability":     {"nova", Added{Repo: "a/b", Harnesses: []string{"claude"}, Capabilities: []string{"everything"}}, nil, "everything"},
		"a board key in use":        {"nova", Added{Repo: "a/b", Harnesses: []string{"claude"}, Key: "ORB"}, nil, "ORB"},
		"the broker's check":        {"nova", Added{Repo: "a/b", Harnesses: []string{"claude"}}, checkFails, "github_app"},
	} {
		err := AddProject(path, c.name, c.added, c.check)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
	if after, _ := os.ReadFile(ProjectsPath(path)); string(after) != string(before) {
		t.Fatal("a refused project changed projects.json")
	}
}

// projects.json holds only what the app may set. Hosts, rules and env stay
// in the config file you edit, so nothing written through the app can
// widen what a sandbox reaches beyond the bundles you defined.
func TestProjectsFileIsStrict(t *testing.T) {
	for name, body := range map[string]string{
		"allow":     `{"projects": {"nova": {"repo": "a/b", "harnesses": ["claude"], "allow": [{"host": "evil.example", "mode": "tunnel"}]}}}`,
		"env":       `{"projects": {"nova": {"repo": "a/b", "harnesses": ["claude"], "env": {"HTTPS_PROXY": "x"}}}}`,
		"subnet":    `{"projects": {"nova": {"repo": "a/b", "harnesses": ["claude"], "subnet": "10.89.1.0/24"}}}`,
		"duplicate": `{"projects": {"nova": {"repo": "a/b", "harnesses": ["claude"]}, "nova": {"repo": "c/d", "harnesses": ["claude"]}}}`,
		"orbit":     `{"projects": {"orbit": {"repo": "a/b", "harnesses": ["claude"]}}}`,
	} {
		path := mainConfig(t)
		os.WriteFile(ProjectsPath(path), []byte(body), 0o600)
		if _, err := Load(path); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
}
