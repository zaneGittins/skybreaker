package controld

import (
	"context"
	"errors"
	"fmt"
	"github.com/zaneGittins/skybreaker/internal/control/config"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeDocker struct {
	mu    sync.Mutex
	calls []string
	up    bool
}

func (f *fakeDocker) RunInput(ctx context.Context, _ io.Reader, _ int64, name string, args ...string) ([]byte, error) {
	return f.Run(ctx, name, args...)
}

func (f *fakeDocker) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	line := strings.Join(args, " ")
	f.calls = append(f.calls, line)
	switch {
	case strings.HasPrefix(line, "info "):
		return []byte(`{"runc":{"path":"runc"},"runsc":{"path":"/usr/bin/runsc"}}`), nil
	case strings.HasPrefix(line, "network inspect"):
		return nil, errors.New("Error: No such network")
	case strings.HasPrefix(line, "container inspect"):
		if !f.up {
			return nil, errors.New("Error: No such container")
		}
		return []byte("abc true"), nil
	case strings.HasPrefix(line, "run "):
		f.up = true
	}
	return nil, nil
}

func (f *fakeDocker) ran(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func TestRunDevMode(t *testing.T) {
	dir := t.TempDir()
	cfg := fmt.Sprintf(`{
	  "data_dir": %q, "users": ["zane@example.com"],
	  "tailscale": {"disabled": true, "dev_listen": "127.0.0.1:0"},
	  "broker": {"listen": "127.0.0.1:0"},
	  "sandbox": {"image": "skybreaker-sandbox:test", "hook_listen": "127.0.0.1:0"},
	  "projects": {"orbit": {"repo": "zaneGittins/orbit", "allow": [{"host": "registry.npmjs.org", "mode": "tunnel"}]}},
	  "jobs": [{"name": "nightly", "project": "orbit", "schedule": "0 2 * * *", "prompt": "hi"}]
	}`, filepath.Join(dir, "data"))
	cfgPath := filepath.Join(dir, "skybreaker.json")
	os.WriteFile(cfgPath, []byte(cfg), 0o600)
	// Workspaces hold files the sandbox's root made, setuid ones included,
	// so a data dir the operator made world-readable is closed.
	os.Mkdir(filepath.Join(dir, "data"), 0o755)

	docker := &fakeDocker{}
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan [3]net.Addr, 1)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{ConfigPath: cfgPath, Version: "test", Runner: docker, Ready: func(ui, hooks, proxy net.Addr) { ready <- [3]net.Addr{ui, hooks, proxy} }})
	}()
	var addrs [3]net.Addr
	select {
	case addrs = <-ready:
	case err := <-done:
		t.Fatalf("Run: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("not ready")
	}
	ui, hooksURL, proxyURL := "http://"+addrs[0].String(), "http://"+addrs[1].String(), "http://"+addrs[2].String()

	call := func(method, url, body string) (int, string) {
		req, _ := http.NewRequest(method, url, strings.NewReader(body))
		if method != "GET" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := call("GET", ui+"/", ""); code != 200 || !strings.Contains(body, "<title>Skybreaker</title>") {
		t.Fatalf("index: %d", code)
	}
	if code, body := call("GET", ui+"/api/projects", ""); code != 200 || !strings.Contains(body, `"orbit"`) {
		t.Fatalf("projects: %d %s", code, body)
	}
	rebound, _ := http.NewRequest("GET", ui+"/api/projects", nil)
	rebound.Host = "evil.example" // a name rebound to 127.0.0.1
	if resp, err := http.DefaultClient.Do(rebound); err != nil || resp.StatusCode != http.StatusMisdirectedRequest {
		t.Fatalf("dev UI under a rebound name: %v %v", resp.Status, err)
	}
	if code, body := call("POST", ui+"/api/projects/orbit/sandbox", `{"action":"start"}`); code != 200 {
		t.Fatalf("start sandbox: %d %s", code, body)
	}
	if !docker.ran("run -d") {
		t.Fatal("docker run not issued")
	}
	if _, err := os.Stat(filepath.Join(dir, "data", "projects", "orbit", "etc", "ca.crt")); err != nil {
		t.Errorf("CA not installed for the sandbox: %v", err)
	}
	// The broker serves sandbox networks only; loopback is not one.
	if code, _ := call("GET", proxyURL+"/ca.crt", ""); code != 403 {
		t.Errorf("broker answered a caller outside the sandboxes: %d", code)
	}
	if code, body := call("GET", ui+"/api/activity", ""); code != 200 || !strings.Contains(body, `"project":"orbit"`) {
		t.Errorf("activity should list the sandbox's token: %d %s", code, body)
	}
	if code, _ := call("POST", hooksURL+"/hooks/claude/orbit/wrong", `{"hook_event_name":"Stop"}`); code != 403 {
		t.Errorf("hook with a bad token: %d", code)
	}
	if fi, err := os.Stat(filepath.Join(dir, "data")); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("data dir: %v %v, want mode 0700", fi.Mode().Perm(), err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "data", "hook.key")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("hook key: %v %v", fi, err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestNftables(t *testing.T) {
	dir := t.TempDir()
	cfg := fmt.Sprintf(`{
	  "data_dir": %q, "users": ["zane@example.com"],
	  "sandbox": {"image": "img"},
	  "projects": {"orbit": {}, "bifract": {}}
	}`, dir)
	p := filepath.Join(dir, "skybreaker.json")
	os.WriteFile(p, []byte(cfg), 0o600)
	out, err := Nftables(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "tcp dport { 7701, 8080 } accept") || !strings.Contains(out, "10.89.0.0/16") {
		t.Fatalf("ruleset:\n%s", out)
	}
	// The ruleset covers the whole sandbox range, so a project added later
	// (in the app, by a service that isn't root) needs no new rules.
	os.WriteFile(p, []byte(strings.Replace(cfg, `"bifract": {}`, `"bifract": {}, "comet": {}`, 1)), 0o600)
	if more, _ := Nftables(p); more != out {
		t.Fatalf("adding a project changed the ruleset:\n%s\n%s", out, more)
	}
}

func TestRefusesWithoutGVisor(t *testing.T) {
	dir := t.TempDir()
	cfg := fmt.Sprintf(`{
	  "data_dir": %q, "users": ["zane@example.com"],
	  "tailscale": {"disabled": true, "dev_listen": "127.0.0.1:0"},
	  "sandbox": {"image": "img", "hook_listen": "127.0.0.1:0"},
	  "projects": {"orbit": {}}
	}`, filepath.Join(dir, "data"))
	p := filepath.Join(dir, "skybreaker.json")
	os.WriteFile(p, []byte(cfg), 0o600)
	err := Run(context.Background(), Options{ConfigPath: p, Runner: runcOnly{}})
	if err == nil || !strings.Contains(err.Error(), "gVisor") {
		t.Fatalf("started without gVisor: %v", err)
	}
}

type runcOnly struct{}

func (r runcOnly) RunInput(ctx context.Context, _ io.Reader, _ int64, name string, args ...string) ([]byte, error) {
	return r.Run(ctx, name, args...)
}

func (runcOnly) Run(context.Context, string, ...string) ([]byte, error) {
	return []byte(`{"runc":{"path":"runc"}}`), nil
}

// Creating a project in the app writes projects.json and restarts the
// server (once no agent is working) so the new project runs.
func TestCreateProjectRestarts(t *testing.T) {
	dir := t.TempDir()
	cfg := fmt.Sprintf(`{
	  "data_dir": %q, "users": ["zane@example.com"],
	  "tailscale": {"disabled": true, "dev_listen": "127.0.0.1:0"},
	  "broker": {"listen": "127.0.0.1:0"},
	  "sandbox": {"image": "skybreaker-sandbox:test", "hook_listen": "127.0.0.1:0"},
	  "projects": {"orbit": {"repo": "zaneGittins/orbit", "harnesses": ["claude"]}}
	}`, filepath.Join(dir, "data"))
	cfgPath := filepath.Join(dir, "skybreaker.json")
	os.WriteFile(cfgPath, []byte(cfg), 0o600)
	ready := make(chan net.Addr, 1)
	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), Options{ConfigPath: cfgPath, Version: "test", Runner: &fakeDocker{}, Ready: func(ui, _, _ net.Addr) { ready <- ui }})
	}()
	var ui net.Addr
	select {
	case ui = <-ready:
	case err := <-done:
		t.Fatalf("Run: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("not ready")
	}
	req, _ := http.NewRequest("POST", "http://"+ui.String()+"/api/projects", strings.NewReader(`{"name":"comet","repo":"zaneGittins/comet","harnesses":["claude"]}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"restart":"now"`) {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrRestart) {
			t.Fatalf("Run returned %v, want a restart", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no restart")
	}
	c, err := config.Load(cfgPath)
	if err != nil || c.Projects["comet"] == nil || !c.Added("comet") {
		t.Fatalf("after restart: %v %+v", err, c)
	}
}
