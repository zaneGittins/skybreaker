package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func newFillFixture(t *testing.T) *fixture {
	return newFixtureWith(t, `{
	  "data_dir": %q,
	  "users": ["zane@example.com"],
	  "sandbox": {"image": "skybreaker-sandbox:test"},
	  "projects": {
	    "orbit": {"repo": "zaneGittins/orbit"},
	    "notes": {},
	    "helper": {"kind": "assistant"}
	  }
	}`)
}

// waitEvent waits for a sandbox event about project.
func waitEvent(t *testing.T, ch <-chan Event, project, data string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Type == "sandbox" && ev.Project == project && ev.Data == data {
				return
			}
		case <-deadline:
			t.Fatalf("no %q event for %s", data, project)
		}
	}
}

func TestFillWorkspaceUnpacksIntoAnEmptyWorkspace(t *testing.T) {
	f := newFillFixture(t)
	ch, cancel := f.app.Subscribe()
	defer cancel()
	if err := f.app.FillWorkspace(context.Background(), "notes", strings.NewReader("archive")); err != nil {
		t.Fatal(err)
	}
	waitEvent(t, ch, "notes", "workspace ready")
	if err := f.app.WaitWorkspace(context.Background(), "notes"); err != nil {
		t.Fatal(err)
	}
	tars := f.docker.find(func(c []string) bool { return slices.Contains(c, "tar") })
	if len(tars) != 1 || !hasSeq(tars[0], "-C", workspaceDir) || !slices.Contains(tars[0], "--no-same-owner") {
		t.Fatalf("tar = %v", tars)
	}
	f.docker.mu.Lock()
	gave := slices.ContainsFunc(f.docker.inputs, func(b []byte) bool { return string(b) == "archive" })
	f.docker.mu.Unlock()
	if !gave {
		t.Fatal("the archive never reached tar")
	}
	if len(f.docker.find(func(c []string) bool { return slices.Contains(c, setupScript) })) != 1 {
		t.Error("the folder's setup script didn't run")
	}
	if len(f.docker.find(func(c []string) bool { return slices.Contains(c, cloneScript) })) != 0 {
		t.Error("a project without a repo was cloned")
	}
}

func TestFillWorkspaceRefusesAFullWorkspace(t *testing.T) {
	f := newFillFixture(t)
	f.docker.set(func() {
		f.docker.execOut = func(args []string) (string, error) {
			if hasSeq(args, "ls", "-A", workspaceDir) {
				return "README.md\n", nil
			}
			return "", nil
		}
	})
	err := f.app.FillWorkspace(context.Background(), "notes", strings.NewReader("archive"))
	if !errors.Is(err, ErrWorkspaceFull) {
		t.Fatalf("err = %v", err)
	}
	if len(f.docker.find(func(c []string) bool { return slices.Contains(c, "tar") })) != 0 {
		t.Fatal("unpacked over existing files")
	}
}

func TestFillWorkspaceClearsAFailedUnpack(t *testing.T) {
	f := newFillFixture(t)
	f.docker.set(func() {
		f.docker.execOut = func(args []string) (string, error) {
			if slices.Contains(args, "tar") {
				return "", errors.New("unexpected end of file")
			}
			return "", nil
		}
	})
	if err := f.app.FillWorkspace(context.Background(), "notes", strings.NewReader("archive")); err == nil {
		t.Fatal("a failed unpack succeeded")
	}
	if len(f.docker.find(func(c []string) bool { return hasSeq(c, "find", workspaceDir, "-mindepth", "1", "-delete") })) != 1 {
		t.Fatal("half an unpack was left behind, so a retry would be refused")
	}
	// Forgotten, like a failed clone, so trying again works.
	if err := f.app.WaitWorkspace(context.Background(), "notes"); err != nil {
		t.Fatal(err)
	}
}

func TestFillWorkspaceTakesTheCloneItsPlace(t *testing.T) {
	f := newFillFixture(t)
	ch, cancel := f.app.Subscribe()
	defer cancel()
	if err := f.app.FillWorkspace(context.Background(), "orbit", strings.NewReader("archive")); err != nil {
		t.Fatal(err)
	}
	waitEvent(t, ch, "orbit", "workspace ready")
	// The copy's remote is the broker's way to GitHub, whatever it was.
	origin := f.docker.find(func(c []string) bool { return slices.Contains(c, originScript) })
	if len(origin) != 1 || !slices.Contains(origin[0], "https://github.com/zaneGittins/orbit.git") {
		t.Fatalf("origin = %v", origin)
	}
	// One clone check, after the unpack: the sandbox start joined this run.
	calls := f.docker.find(func(c []string) bool { return slices.Contains(c, "tar") || slices.Contains(c, cloneScript) })
	if len(calls) != 2 || !slices.Contains(calls[0], "tar") || !slices.Contains(calls[1], cloneScript) {
		t.Fatalf("tar and clone calls = %v", calls)
	}
}

func TestFillWorkspaceRefusesAnAssistant(t *testing.T) {
	f := newFillFixture(t)
	if err := f.app.FillWorkspace(context.Background(), "helper", strings.NewReader("archive")); !errors.Is(err, ErrNoRepo) {
		t.Fatalf("err = %v", err)
	}
}
