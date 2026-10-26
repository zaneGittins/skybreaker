package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSkybreaker answers what `new-project` asks, adding a created project
// to the inbox only after a poll or two, as a restart would.
type fakeSkybreaker struct {
	mu       sync.Mutex
	projects []string
	created  []NewProject
	polls    int
	uploads  map[string][]byte
	full     bool // the workspace has files
}

func (f *fakeSkybreaker) serve(t *testing.T) *Client {
	t.Helper()
	f.uploads = map[string][]byte{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.URL.Path == "/api/meta":
			w.Write([]byte(`{"version":"v0.1.0","user":"you","harnesses":["claude","codex","pi","shell"]}`))
		case r.URL.Path == "/api/capabilities":
			w.Write([]byte(`[{"name":"github"},{"name":"tools"},{"name":"claude-login"},{"name":"codex-login"},{"name":"slack"}]`))
		case r.URL.Path == "/api/inbox":
			f.polls++
			var ps []Project
			for _, p := range f.projects {
				ps = append(ps, Project{Name: p, Harnesses: []string{"claude"}})
			}
			for _, p := range f.created {
				if f.polls > 2 {
					ps = append(ps, Project{Name: p.Name, Harnesses: p.Harnesses})
				}
			}
			json.NewEncoder(w).Encode(Inbox{Projects: ps})
		case r.Method == "POST" && r.URL.Path == "/api/projects":
			var p NewProject
			json.NewDecoder(r.Body).Decode(&p)
			f.created = append(f.created, p)
			w.Write([]byte(`{"name":"` + p.Name + `","restart":"now"}`))
		case r.Method == "PUT" && strings.HasSuffix(r.URL.Path, "/workspace"):
			if r.Header.Get("Content-Type") != "application/gzip" {
				http.Error(w, "type", http.StatusUnsupportedMediaType)
				return
			}
			b, _ := io.ReadAll(r.Body)
			if f.full {
				w.WriteHeader(http.StatusConflict)
				w.Write([]byte(`{"error":"the workspace isn't empty, so nothing was copied into it"}`))
				return
			}
			f.uploads[strings.Split(r.URL.Path, "/")[3]] = b
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	old := pollEvery
	pollEvery = time.Millisecond
	t.Cleanup(func() { pollEvery = old })
	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// noGit stands in for git in a folder whose origin isn't asked about.
func noGit(context.Context, string, ...string) ([]byte, error) {
	return nil, errors.New("not a git repository")
}

func myApp(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "My App")
	os.MkdirAll(filepath.Join(dir, ".claude"), 0o755)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0o644)
	os.WriteFile(filepath.Join(dir, ".claude", "settings.json"), []byte("{}"), 0o644)
	return dir
}

func TestNewProjectFromAFolder(t *testing.T) {
	f := &fakeSkybreaker{}
	c := f.serve(t)
	dir := myApp(t)
	var out bytes.Buffer
	e, err := newProject(context.Background(), c, noGit, []string{dir, "--yes"}, strings.NewReader(""), &out, false)
	if err != nil {
		t.Fatal(err)
	}
	if e.Project != "my-app" || e.Harness != "claude" {
		t.Errorf("entry %+v", e)
	}
	want := NewProject{Name: "my-app", Harnesses: []string{"claude", "codex"}, Capabilities: []string{"github", "tools", "claude-login", "codex-login"}}
	if len(f.created) != 1 || f.created[0].Name != want.Name || f.created[0].Repo != "" ||
		!slices.Equal(f.created[0].Harnesses, want.Harnesses) || !slices.Equal(f.created[0].Capabilities, want.Capabilities) {
		t.Fatalf("created %+v", f.created)
	}
	hdrs, bodies := untar(t, f.uploads["my-app"])
	if hdrs[".claude/settings.json"] == nil || bodies["main.go"] != "package main" {
		t.Fatalf("uploaded %v", hdrs)
	}
	for _, s := range []string{"no GitHub remote", "Restarting", "Copied"} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("output lacks %q:\n%s", s, out.String())
		}
	}
}

func TestNewProjectUsesTheGitHubOrigin(t *testing.T) {
	f := &fakeSkybreaker{}
	c := f.serve(t)
	dir := myApp(t)
	os.Mkdir(filepath.Join(dir, ".git"), 0o755)
	git := func(context.Context, string, ...string) ([]byte, error) {
		return []byte("git@github.com:zaneGittins/my-app.git\n"), nil
	}
	var out bytes.Buffer
	if _, err := newProject(context.Background(), c, git, []string{dir, "app2", "--harness", "pi", "-y"}, strings.NewReader(""), &out, false); err != nil {
		t.Fatal(err)
	}
	if len(f.created) != 1 || f.created[0].Name != "app2" || f.created[0].Repo != "zaneGittins/my-app" ||
		!slices.Equal(f.created[0].Harnesses, []string{"pi"}) || !slices.Equal(f.created[0].Capabilities, []string{"github", "tools"}) {
		t.Fatalf("created %+v", f.created)
	}
	if !strings.Contains(out.String(), "zaneGittins/my-app") {
		t.Errorf("output:\n%s", out.String())
	}
}

func TestNewProjectAsksFirst(t *testing.T) {
	f := &fakeSkybreaker{}
	c := f.serve(t)
	dir := myApp(t)
	os.WriteFile(filepath.Join(dir, ".env"), []byte("TOKEN=x"), 0o600)
	var out bytes.Buffer
	_, err := newProject(context.Background(), c, noGit, []string{dir}, strings.NewReader("n\n"), &out, true)
	if err == nil || len(f.created) != 0 || len(f.uploads) != 0 {
		t.Fatalf("went ahead without a yes: %v %+v", err, f.created)
	}
	if !strings.Contains(out.String(), ".env") || !strings.Contains(out.String(), "credentials") {
		t.Errorf("the credential wasn't pointed out:\n%s", out.String())
	}
	out.Reset()
	if _, err := newProject(context.Background(), c, noGit, []string{dir}, strings.NewReader("y\n"), &out, true); err != nil || len(f.uploads) != 1 {
		t.Fatalf("yes: %v", err)
	}
	// Without a terminal to ask on, it takes --yes.
	if _, err := newProject(context.Background(), c, noGit, []string{dir, "other"}, strings.NewReader("y\n"), &out, false); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("no terminal: %v", err)
	}
}

// Run again once Skybreaker has restarted, it copies into the project made
// the first time.
func TestNewProjectIntoAnExistingProject(t *testing.T) {
	f := &fakeSkybreaker{projects: []string{"my-app"}}
	c := f.serve(t)
	var out bytes.Buffer
	if _, err := newProject(context.Background(), c, noGit, []string{myApp(t), "-y"}, strings.NewReader(""), &out, false); err != nil {
		t.Fatal(err)
	}
	if len(f.created) != 0 || f.uploads["my-app"] == nil {
		t.Fatalf("created %+v, uploads %d", f.created, len(f.uploads))
	}
	f.full = true
	if _, err := newProject(context.Background(), c, noGit, []string{myApp(t), "-y"}, strings.NewReader(""), &out, false); err == nil || !strings.Contains(err.Error(), "isn't empty") {
		t.Fatalf("full workspace: %v", err)
	}
}

func TestNewProjectRefusals(t *testing.T) {
	f := &fakeSkybreaker{}
	c := f.serve(t)
	file := filepath.Join(t.TempDir(), "notes.txt")
	os.WriteFile(file, nil, 0o644)
	odd := filepath.Join(t.TempDir(), "___")
	os.Mkdir(odd, 0o755)
	for _, args := range [][]string{
		nil,
		{"--yes"},
		{filepath.Join(t.TempDir(), "missing")},
		{file},
		{odd},
		{myApp(t), "Bad Name"},
		{myApp(t), "a", "b"},
		{myApp(t), "--harness"},
		{myApp(t), "--frob"},
	} {
		if _, err := newProject(context.Background(), c, noGit, args, strings.NewReader("y\n"), &bytes.Buffer{}, true); err == nil {
			t.Errorf("%q: no error", args)
		}
	}
	if len(f.created) != 0 {
		t.Fatalf("created %+v", f.created)
	}
}
