package secrets

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func keyPEM(t *testing.T) string {
	t.Helper()
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}))
}

// A github_app secret the config file leaves for the app to create binds
// its hosts now and resolves to nothing until then.
func TestFromAppPlaceholder(t *testing.T) {
	s := mustStore(t, Spec{Name: "github_app", Kind: "github_app", Hosts: []string{"github.com"}, GitHubApp: &GitHubAppSpec{FromApp: true}})
	if !s.Has("github_app") || s.HostsFor("github_app")[0] != "github.com" {
		t.Fatal("placeholder isn't a secret")
	}
	if _, err := s.Resolve(context.Background(), "github_app", "github.com", Scope{Repo: "a/b"}); !errors.Is(err, ErrNotCreated) {
		t.Fatalf("placeholder resolved: %v", err)
	}
	if s.GitHubApp() != nil {
		t.Fatal("placeholder is an App")
	}
	for name, g := range map[string]*GitHubAppSpec{
		"from_app with an app_id": {FromApp: true, AppID: "1"},
		"from_app with a key":     {FromApp: true, KeyFile: "/k"},
	} {
		if _, err := NewStore([]Spec{{Name: "g", Kind: "github_app", Hosts: []string{"github.com"}, GitHubApp: g}}, nil); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestGitHubAppInlineKey(t *testing.T) {
	s := mustStore(t, Spec{Name: "github_app", Kind: "github_app", Hosts: []string{"github.com"}, GitHubApp: &GitHubAppSpec{AppID: "7", Key: keyPEM(t)}})
	if a := s.GitHubApp(); a == nil || a.AppID != "7" {
		t.Fatalf("%+v", a)
	}
	if _, err := NewStore([]Spec{{Name: "g", Kind: "github_app", Hosts: []string{"github.com"}, GitHubApp: &GitHubAppSpec{AppID: "7", Key: keyPEM(t), KeyFile: "/k"}}}, nil); err == nil {
		t.Error("both key and key_file accepted")
	}
}

func TestMerge(t *testing.T) {
	file := []Spec{
		{Name: "do", Kind: "static", Value: "x", Hosts: []string{"api.digitalocean.com"}},
		{Name: "github_app", Kind: "github_app", Hosts: []string{"github.com", "api.github.com"}, GitHubApp: &GitHubAppSpec{FromApp: true}},
	}
	key := keyPEM(t)
	app := []Stored{
		{Spec: Spec{Name: "openai", Kind: "static", Value: "sk-1", Hosts: []string{"api.openai.com"}}},
		// The app fills in the App; where it may go stays the file's.
		{Spec: Spec{Name: "github_app", Kind: "github_app", Hosts: []string{"evil.example"}, GitHubApp: &GitHubAppSpec{AppID: "9", Key: key}}},
	}
	specs, err := Merge(file, app)
	if err != nil {
		t.Fatal(err)
	}
	s := mustStore(t, specs...)
	if got := s.HostsFor("github_app"); strings.Join(got, ",") != "github.com,api.github.com" {
		t.Fatalf("App hosts %v", got)
	}
	if a := s.GitHubApp(); a == nil || a.AppID != "9" {
		t.Fatalf("App %+v", a)
	}
	if v, err := s.Resolve(context.Background(), "openai", "api.openai.com", Scope{}); err != nil || v != "sk-1" {
		t.Fatalf("app secret %q %v", v, err)
	}

	for name, app := range map[string][]Stored{
		"a name the file has":  {{Spec: Spec{Name: "do", Kind: "static", Value: "y", Hosts: []string{"api.digitalocean.com"}}}},
		"twice":                {{Spec: Spec{Name: "a", Kind: "static", Value: "y", Hosts: []string{"h"}}}, {Spec: Spec{Name: "a", Kind: "static", Value: "z", Hosts: []string{"h"}}}},
		"a file on this host":  {{Spec: Spec{Name: "a", Kind: "file", File: "/etc/shadow", Hosts: []string{"h"}}}},
		"an environment var":   {{Spec: Spec{Name: "a", Kind: "env", Env: "TS_AUTHKEY", Hosts: []string{"h"}}}},
		"a key file":           {{Spec: Spec{Name: "b", Kind: "github_app", Hosts: []string{"github.com"}, GitHubApp: &GitHubAppSpec{AppID: "1", KeyFile: "/etc/skybreaker/github-app.pem"}}}},
		"another API base":     {{Spec: Spec{Name: "b", Kind: "github_app", Hosts: []string{"github.com"}, GitHubApp: &GitHubAppSpec{AppID: "1", Key: key, APIBase: "https://evil.example"}}}},
		"a k8s minter":         {{Spec: Spec{Name: "a", Kind: "k8s_tokenrequest", Hosts: []string{"h"}}}},
		"a placeholder itself": {{Spec: Spec{Name: "a", Kind: "github_app", Hosts: []string{"github.com"}, GitHubApp: &GitHubAppSpec{FromApp: true}}}},
	} {
		if _, err := Merge(file, app); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// The file's own App, not a placeholder, is never replaced.
	own := []Spec{{Name: "github_app", Kind: "github_app", Hosts: []string{"github.com"}, GitHubApp: &GitHubAppSpec{AppID: "1", KeyFile: "/k"}}}
	if _, err := Merge(own, app[1:]); err == nil {
		t.Error("the app replaced the file's App")
	}
}

func TestAppFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	f, err := ReadAppFile(path)
	if err != nil || len(f.Secrets) != 0 {
		t.Fatalf("missing file: %+v %v", f, err)
	}
	os.WriteFile(path, []byte(`{"key":"00","secrets":[{"name":"a","kind":"static","value":"v","hosts":["h"],"added_by":"zane@example.com","added":"2026-10-19T00:00:00Z","surprise":1}]}`), 0o600)
	if _, err := ReadAppFile(path); err == nil {
		t.Fatal("unknown field accepted")
	}
	os.WriteFile(path, []byte(`{"key":"00","secrets":[{"name":"a","kind":"static","value":"v","hosts":["h"],"added_by":"zane@example.com","added":"2026-10-19T00:00:00Z"}]}`), 0o600)
	os.Chmod(path, 0o644)
	if _, err := ReadAppFile(path); err == nil {
		t.Fatal("a world-readable secrets file accepted")
	}
	os.Chmod(path, 0o600)
	if f, err = ReadAppFile(path); err != nil || f.Secrets[0].AddedBy != "zane@example.com" {
		t.Fatalf("%+v %v", f, err)
	}
	// Fingerprints tell values apart without giving them away, and differ
	// between installs.
	a, b := AppFile{Key: "k1"}, AppFile{Key: "k2"}
	if a.Fingerprint("v") == a.Fingerprint("w") || a.Fingerprint("v") == b.Fingerprint("v") || strings.Contains(a.Fingerprint("secret-value"), "secret") {
		t.Fatal("fingerprints")
	}
}

func TestLastUsed(t *testing.T) {
	s := mustStore(t, Spec{Name: "a", Kind: "static", Value: "v", Hosts: []string{"h"}})
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	if len(s.LastUsed()) != 0 {
		t.Fatal("used before use")
	}
	s.Resolve(context.Background(), "a", "other", Scope{})
	if len(s.LastUsed()) != 0 {
		t.Fatal("a refused resolve counts as a use")
	}
	s.Resolve(context.Background(), "a", "h", Scope{})
	if !s.LastUsed()["a"].Equal(now) {
		t.Fatalf("%v", s.LastUsed())
	}
}
