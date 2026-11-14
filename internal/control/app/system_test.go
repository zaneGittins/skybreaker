package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zaneGittins/skybreaker/internal/control/sandbox"
	"github.com/zaneGittins/skybreaker/internal/control/store"
	host "github.com/zaneGittins/skybreaker/internal/control/system"
)

func TestStorageParsesOnlyKnownCounters(t *testing.T) {
	r, err := parseStorage([]byte("workspace 1024\nworktrees 2048\nhome 4096\ndocker 8192\ngo 1024\n"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != 15360 || r.Home != 3072 || r.GoCache != 1024 {
		t.Fatalf("storage: %+v", r)
	}
	for _, bad := range []string{"workspace -1", "workspace 1\n", "workspace 1\nworktrees 2\nhome 3\ndocker 4\ngo 5", "workspace 1\nworktrees 2\nhome 3\ndocker 4\ngo 0\nextra 9"} {
		if _, err := parseStorage([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestCleanupRefusesActiveOrStoppedProjects(t *testing.T) {
	for _, state := range []string{store.ChatWorking, store.ChatWaiting, "terminal", "recent", "stopped"} {
		t.Run(state, func(t *testing.T) {
			f := newFixture(t)
			f.docker.running[sandbox.ContainerName("orbit")] = state != "stopped"
			if state != "stopped" {
				s := store.Session{ID: "c1", Project: "orbit", Mode: store.ModeChat, Status: store.StatusRunning, State: state, Updated: time.Now().Add(-time.Hour)}
				if state == "terminal" {
					s.Mode = ""
				}
				if state == "recent" {
					s.State = store.ChatIdle
					s.Updated = time.Now()
				}
				f.app.store.CreateSession(s)
			}
			_, err := f.app.CleanStorage(context.Background(), "orbit", host.Cleanup{Go: true, Docker: true, OlderThanDays: 7})
			if err == nil {
				t.Fatal("cleanup accepted")
			}
			for _, call := range f.docker.calls {
				if strings.Contains(strings.Join(call, " "), "prune") {
					t.Fatal("pruned active project")
				}
			}
		})
	}
}

func TestMaintenanceExcludesNewWork(t *testing.T) {
	f := newFixture(t)
	gate := f.app.maintenanceGate("orbit")
	gate.Lock()
	defer gate.Unlock()
	if _, err := f.app.NewSession(context.Background(), "orbit", "claude", "", NewOptions{}); !errors.Is(err, ErrMaintenance) {
		t.Fatalf("start: %v", err)
	}
	f.app.store.CreateSession(store.Session{ID: "c1", Project: "orbit", Harness: "claude", Mode: store.ModeChat, Status: store.StatusRunning, State: store.ChatIdle})
	if err := f.app.ChatSend(context.Background(), "c1", "hello"); !errors.Is(err, ErrMaintenance) {
		t.Fatalf("send: %v", err)
	}
	if _, err := f.app.SSHConn(context.Background(), "orbit"); !errors.Is(err, ErrMaintenance) {
		t.Fatalf("SSH: %v", err)
	}
	if _, err := f.app.Exec(context.Background(), "orbit", "go", "build"); !errors.Is(err, ErrMaintenance) {
		t.Fatalf("exec: %v", err)
	}
}

func TestCleanupRunsOnlyInProjectAndMeasuresRecoveredBytes(t *testing.T) {
	f := newFixture(t)
	f.docker.running[sandbox.ContainerName("orbit")] = true
	scans := 0
	f.docker.execOut = func(args []string) (string, error) {
		script := strings.Join(args, " ")
		if strings.Contains(script, "du -s -B1") {
			scans++
			if scans == 1 {
				return "workspace 0\nworktrees 0\nhome 4096\ndocker 8192\ngo 4096\n", nil
			}
			return "workspace 0\nworktrees 0\nhome 0\ndocker 4096\ngo 0\n", nil
		}
		return "", nil
	}
	r, err := f.app.CleanStorage(context.Background(), "orbit", host.Cleanup{Go: true, Docker: true, OlderThanDays: 9})
	if err != nil {
		t.Fatal(err)
	}
	if r.Recovered != 8192 {
		t.Fatalf("recovered: %+v", r)
	}
	found := false
	for _, call := range f.docker.calls {
		cmd := strings.Join(call, " ")
		if strings.Contains(cmd, "go clean -cache") {
			found = true
			if !strings.Contains(cmd, "exec skybreaker-orbit") || !strings.Contains(cmd, "216h") {
				t.Fatalf("wrong cleanup scope or age: %s", cmd)
			}
			if strings.Contains(cmd, "--volumes") || strings.Contains(cmd, "modcache") || strings.Contains(cmd, "system prune") {
				t.Fatal("unsafe cleanup")
			}
		}
	}
	if !found {
		t.Fatal("no cleanup")
	}
}

func TestCleanupReportsPartialFailure(t *testing.T) {
	f := newFixture(t)
	f.docker.running[sandbox.ContainerName("orbit")] = true
	f.docker.execOut = func(args []string) (string, error) {
		cmd := strings.Join(args, " ")
		if strings.Contains(cmd, "du -s -B1") {
			return "workspace 0\nworktrees 0\nhome 4096\ndocker 0\ngo 4096\n", nil
		}
		if strings.Contains(cmd, "go clean -cache") {
			return "", errors.New("cleanup failed")
		}
		return "", nil
	}
	r, err := f.app.CleanStorage(context.Background(), "orbit", host.Cleanup{Go: true, OlderThanDays: 7})
	if err != nil || r.Error == "" || r.After == nil || r.Recovered != 0 {
		t.Fatalf("partial result: %+v, %v", r, err)
	}
}

func TestCleanupLeavesSetupAndScheduledRunsAlone(t *testing.T) {
	for _, kind := range []string{"setup", "run"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			f.docker.running[sandbox.ContainerName("orbit")] = true
			if kind == "setup" {
				f.app.clones = map[string]*cloneRun{"orbit": {done: make(chan struct{})}}
			} else {
				f.app.store.CreateRun(store.Run{ID: "r1", Project: "orbit", Status: store.RunRunning})
			}
			if _, err := f.app.CleanStorage(context.Background(), "orbit", host.Cleanup{Docker: true, OlderThanDays: 7}); !errors.Is(err, ErrCleanupBusy) {
				t.Fatalf("%s: %v", kind, err)
			}
		})
	}
}

func TestCleanupContinuesAfterOneCacheFails(t *testing.T) {
	f := newFixture(t)
	f.docker.running[sandbox.ContainerName("orbit")] = true
	var script string
	f.docker.execOut = func(args []string) (string, error) {
		cmd := strings.Join(args, " ")
		if strings.Contains(cmd, "du -s -B1") {
			return "workspace 0\nworktrees 0\nhome 0\ndocker 0\ngo 0\n", nil
		}
		if strings.Contains(cmd, "go clean -cache") {
			script = args[len(args)-1]
		}
		return "", nil
	}
	if _, err := f.app.CleanStorage(context.Background(), "orbit", host.Cleanup{Go: true, Docker: true, OlderThanDays: 3}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	logFile := filepath.Join(dir, "commands")
	for name, body := range map[string]string{
		"go":     "#!/bin/sh\nif [ \"$1\" = env ]; then echo /root/.cache/go-build; else echo go-clean >>\"$TEST_CLEAN_LOG\"; exit 1; fi\n",
		"docker": "#!/bin/sh\necho \"docker $*\" >>\"$TEST_CLEAN_LOG\"\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("sh", "-c", strings.Replace(script, "cd /\n", "", 1))
	cmd.Env = []string{"PATH=" + dir, "TEST_CLEAN_LOG=" + logFile}
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("failed Go cleanup returned success: %s", out)
	}
	out, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); !strings.Contains(got, "go-clean\n") || !strings.Contains(got, "docker builder prune --force --filter until=72h") || !strings.Contains(got, "docker image prune --force --filter until=72h") {
		t.Fatalf("cleanup stopped before other categories: %s", got)
	}
}

func TestAcceptedCleanupSurvivesDisconnectAndKeepsWorkOut(t *testing.T) {
	f := newFixture(t)
	f.docker.running[sandbox.ContainerName("orbit")] = true
	f.app.store.CreateSession(store.Session{ID: "c1", Project: "orbit", Harness: "claude", Mode: store.ModeChat, Status: store.StatusRunning, State: store.ChatIdle, Updated: time.Now().Add(-time.Hour)})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cleaned := false
	f.docker.execOut = func(args []string) (string, error) {
		cmd := strings.Join(args, " ")
		if strings.Contains(cmd, "du -s -B1") {
			cancel()
			return "workspace 0\nworktrees 0\nhome 0\ndocker 0\ngo 0\n", nil
		}
		if strings.Contains(cmd, "go clean -cache") {
			cleaned = true
			if err := f.app.ChatSend(context.Background(), "c1", "hello"); !errors.Is(err, ErrMaintenance) {
				t.Errorf("work started during cleanup: %v", err)
			}
		}
		return "", nil
	}
	result, err := f.app.CleanStorage(ctx, "orbit", host.Cleanup{Go: true, OlderThanDays: 7})
	if err != nil || !cleaned || result.Error != "" || result.After == nil {
		t.Fatalf("disconnected cleanup: %+v, %v; ran=%t", result, err, cleaned)
	}
	if !f.app.maintenanceGate("orbit").TryLock() {
		t.Fatal("maintenance gate was not released")
	}
	f.app.maintenanceGate("orbit").Unlock()
}
