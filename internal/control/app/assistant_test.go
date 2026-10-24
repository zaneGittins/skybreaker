package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zaneGittins/skybreaker/internal/control/sandbox"
)

func newAssistantFixture(t *testing.T) *fixture {
	return newFixtureWith(t, `{
	  "data_dir": %q,
	  "users": ["zane@example.com"],
	  "sandbox": {"image": "skybreaker-sandbox:test"},
	  "projects": {"pip": {"kind": "assistant", "harnesses": ["claude", "codex"]}}
	}`)
}

// An assistant has no repo: it starts without a clone, and the features
// that work on one say so instead of running git.
func TestAssistantHasNoGit(t *testing.T) {
	f := newAssistantFixture(t)
	ctx := context.Background()
	if err := f.app.StartSandbox(ctx, "pip"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.NewChat(ctx, "pip", "claude", NewOptions{OwnBranch: true}); !errors.Is(err, ErrNoRepo) {
		t.Errorf("own branch: %v", err)
	}
	if _, err := f.app.NewChat(ctx, "pip", "claude", NewOptions{}); err != nil {
		t.Errorf("plain chat: %v", err)
	}
	if _, err := f.app.Review(ctx, "pip"); !errors.Is(err, ErrNoRepo) {
		t.Errorf("review: %v", err)
	}
	if _, err := f.app.Diff(ctx, "pip"); !errors.Is(err, ErrNoRepo) {
		t.Errorf("diff: %v", err)
	}
	if git := f.docker.find(func(c []string) bool { return strings.Contains(strings.Join(c, " "), "git") }); len(git) > 0 {
		t.Errorf("ran git for an assistant: %v", git)
	}
}

// An assistant's own instructions (its personality and memories) follow
// every agent's, in the files all three harnesses read, and are rewritten
// when they change.
func TestAssistantInstructions(t *testing.T) {
	f := newAssistantFixture(t)
	ctx := context.Background()
	extra := "# You are Pip"
	f.app.projectInstructions = func(project string) string {
		if project == "pip" {
			return extra
		}
		return ""
	}
	f.app.SetInstructions("Be concise.")
	if err := f.app.StartSandbox(ctx, "pip"); err != nil {
		t.Fatal(err)
	}
	etc := filepath.Join(f.app.ProjectDir("pip"), "etc")
	read := func(name string) string { b, _ := os.ReadFile(filepath.Join(etc, name)); return string(b) }
	if md := read(sandbox.InstructionsFile); !strings.Contains(md, "Be concise.") || !strings.Contains(md, "# You are Pip") || strings.Index(md, "Be concise.") > strings.Index(md, "# You are Pip") {
		t.Fatalf("instructions.md:\n%s", md)
	}
	if toml := read(sandbox.CodexConfigFile); !strings.Contains(toml, "# You are Pip") {
		t.Fatalf("codex config:\n%s", toml)
	}
	extra = "# You are Juno"
	if err := f.app.RefreshInstructions("pip"); err != nil {
		t.Fatal(err)
	}
	if md := read(sandbox.InstructionsFile); !strings.Contains(md, "# You are Juno") || strings.Contains(md, "Pip") {
		t.Fatalf("after refresh:\n%s", md)
	}
	f.app.SetInstructions("Be brief.")
	if md := read(sandbox.InstructionsFile); !strings.Contains(md, "Be brief.") || !strings.Contains(md, "# You are Juno") {
		t.Fatalf("after a global change:\n%s", md)
	}
}

func TestBrowserIsForAssistants(t *testing.T) {
	f := newFixture(t)
	if _, err := f.app.Browser(context.Background(), "orbit"); err == nil {
		t.Fatal("a coding project got a browser")
	}
}

// What the browser hands the agent goes into its workspace through the
// sandbox, never by the host's own hand.
func TestBrowserFilesGoToTheWorkspace(t *testing.T) {
	f := newAssistantFixture(t)
	ctx := context.Background()
	f.app.StartSandbox(ctx, "pip")
	path, err := f.app.SaveToWorkspace(ctx, "pip", "screenshots/1.png", []byte("\x89PNG"))
	if err != nil || path != "/workspace/screenshots/1.png" {
		t.Fatalf("%q %v", path, err)
	}
	last := f.docker.calls[len(f.docker.calls)-1]
	if !slices.Contains(last, "skybreaker-pip") || last[len(last)-1] != "/workspace/screenshots/1.png" {
		t.Fatalf("docker %v", last)
	}
	if got := f.docker.inputs[len(f.docker.inputs)-1]; string(got) != "\x89PNG" {
		t.Fatalf("wrote %q", got)
	}
	for in, want := range map[string]string{
		"podcast.mp3":            "podcast.mp3",
		"../../etc/passwd":       "passwd",
		"we\x00ird\nname?.pdf":   "we_ird_name_.pdf",
		"":                       "download",
		"..":                     "download",
		".hidden":                "hidden",
		strings.Repeat("a", 300): strings.Repeat("a", 100),
	} {
		if got := downloadName(in); got != want {
			t.Errorf("downloadName(%q) = %q, want %q", in, got, want)
		}
	}
}

// A browser keeps its broker token while it works, and mints another only
// when a challenge comes straight back: the old one was revoked.
func TestBrowserCredentials(t *testing.T) {
	var lb liveBrowser
	n := 0
	mint := func() (Minted, error) {
		n++
		return Minted{ID: fmt.Sprint("t", n), Token: fmt.Sprint("secret", n)}, nil
	}
	now := time.Now()
	var revoked []string
	revoke := func(id string) { revoked = append(revoked, id) }
	if tok, _ := lb.credentials(now, mint, revoke); tok != "secret1" {
		t.Fatalf("first %q", tok)
	}
	if tok, _ := lb.credentials(now.Add(time.Minute), mint, revoke); tok != "secret1" || n != 1 {
		t.Fatalf("later challenge minted again: %q after %d", tok, n)
	}
	if tok, _ := lb.credentials(now.Add(time.Minute+time.Second), mint, revoke); tok != "secret2" {
		t.Fatalf("a refused token was kept: %q", tok)
	}
	if strings.Join(revoked, ",") != "t1" || lb.tokenID != "t2" {
		t.Fatalf("revoked %v, holding %s", revoked, lb.tokenID)
	}
}
