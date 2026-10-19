package sandbox_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// run runs runtime.sh with args, against a fake real runc that records its
// arguments, and returns them.
func run(t *testing.T, ca string, args ...string) []string {
	t.Helper()
	for _, tool := range []string{"sh", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	bin := t.TempDir()
	out := filepath.Join(bin, "args")
	os.WriteFile(filepath.Join(bin, "runc"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >"+out+"\n"), 0o755)
	cmd := exec.Command("sh", append([]string{"runtime.sh"}, args...)...)
	cmd.Env = append(os.Environ(), "SKYBREAKER_RUNC="+filepath.Join(bin, "runc"), "SKYBREAKER_CA_BUNDLE="+ca)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("runtime.sh: %v\n%s", err, b)
	}
	b, _ := os.ReadFile(out)
	return strings.Fields(string(b))
}

type spec struct {
	Root    struct{ Path string } `json:"root"`
	Process struct {
		Env []string `json:"env"`
	} `json:"process"`
	Mounts []struct {
		Destination, Type, Source string
		Options                   []string
	} `json:"mounts"`
}

// bundle makes an OCI bundle whose image keeps its CA bundle at the Debian
// path.
func bundle(t *testing.T, env ...string) string {
	t.Helper()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "rootfs", "etc", "ssl", "certs"), 0o755)
	os.WriteFile(filepath.Join(dir, "rootfs", "etc", "ssl", "certs", "ca-certificates.crt"), []byte("image roots"), 0o644)
	cfg := map[string]any{"root": map[string]any{"path": "rootfs"}, "process": map[string]any{"env": env}, "mounts": []any{map[string]any{"destination": "/proc", "type": "proc"}}}
	b, _ := json.Marshal(cfg)
	os.WriteFile(filepath.Join(dir, "config.json"), b, 0o644)
	return dir
}

func read(t *testing.T, dir string) spec {
	t.Helper()
	var s spec
	b, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func caFile(t *testing.T) string {
	ca := filepath.Join(t.TempDir(), "ca.crt")
	os.WriteFile(ca, []byte("public roots + broker"), 0o644)
	return ca
}

func TestRuntimeAddsTheCABundle(t *testing.T) {
	ca := caFile(t)
	dir := bundle(t, "PATH=/usr/bin", "NODE_EXTRA_CA_CERTS=/mine.pem")
	args := run(t, ca, "--root", "/run/runc", "create", "--bundle", dir, "--pid-file", "/x.pid", "c1")
	if !slices.Equal(args, []string{"--root", "/run/runc", "create", "--bundle", dir, "--pid-file", "/x.pid", "c1"}) {
		t.Fatalf("runc got %q", args)
	}
	s := read(t, dir)
	dests := map[string]bool{}
	for _, m := range s.Mounts {
		if m.Source == ca {
			dests[m.Destination] = true
			if m.Type != "bind" || !slices.Contains(m.Options, "ro") {
				t.Errorf("mount %+v", m)
			}
		}
	}
	// The image's own bundle path, and ours; not the Red Hat path this
	// image doesn't have.
	if !dests["/etc/ssl/certs/ca-certificates.crt"] || !dests["/etc/skybreaker/ca-bundle.crt"] || dests["/etc/pki/tls/certs/ca-bundle.crt"] {
		t.Fatalf("mounted at %v", dests)
	}
	if len(s.Mounts) != 3 || s.Mounts[0].Destination != "/proc" {
		t.Fatalf("existing mounts disturbed: %+v", s.Mounts)
	}
	env := strings.Join(s.Process.Env, "\n")
	for _, want := range []string{"SSL_CERT_FILE=/etc/skybreaker/ca-bundle.crt", "REQUESTS_CA_BUNDLE=/etc/skybreaker/ca-bundle.crt", "GIT_SSL_CAINFO=/etc/skybreaker/ca-bundle.crt", "PATH=/usr/bin"} {
		if !strings.Contains(env, want) {
			t.Errorf("env lacks %s:\n%s", want, env)
		}
	}
	// A variable the image sets itself is left alone.
	if !strings.Contains(env, "NODE_EXTRA_CA_CERTS=/mine.pem") || strings.Contains(env, "NODE_EXTRA_CA_CERTS=/etc/skybreaker") {
		t.Errorf("image's own setting overridden:\n%s", env)
	}
}

func TestRuntimeOnlyTouchesCreate(t *testing.T) {
	ca := caFile(t)
	dir := bundle(t)
	before, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	for _, args := range [][]string{{"start", "c1"}, {"delete", "--force", "c1"}, {"state", "c1"}} {
		if got := run(t, ca, args...); !slices.Equal(got, args) {
			t.Fatalf("runc got %q, want %q", got, args)
		}
	}
	if after, _ := os.ReadFile(filepath.Join(dir, "config.json")); string(after) != string(before) {
		t.Fatal("config changed outside create")
	}
	// No CA bundle in the sandbox: the container starts as it is.
	run(t, filepath.Join(t.TempDir(), "missing.crt"), "create", "--bundle", dir, "c2")
	if after, _ := os.ReadFile(filepath.Join(dir, "config.json")); string(after) != string(before) {
		t.Fatal("config changed without a CA bundle")
	}
}

// BuildKit (`docker compose build`, `docker buildx`) starts its steps with
// `runc run`, finding runc on PATH rather than through dockerd's runtime.
func TestRuntimeCoversBuildKitRun(t *testing.T) {
	ca := caFile(t)
	dir := bundle(t)
	args := run(t, ca, "--log-format", "json", "run", "--bundle", dir, "step1")
	if !slices.Equal(args, []string{"--log-format", "json", "run", "--bundle", dir, "step1"}) {
		t.Fatalf("runc got %q", args)
	}
	if env := strings.Join(read(t, dir).Process.Env, "\n"); !strings.Contains(env, "SSL_CERT_FILE=/etc/skybreaker/ca-bundle.crt") {
		t.Fatalf("run step lacks the CA:\n%s", env)
	}
}

// Applied twice (dockerd's runtime, then runc on PATH), the bundle is added
// once.
func TestRuntimeIsIdempotent(t *testing.T) {
	ca := caFile(t)
	dir := bundle(t)
	run(t, ca, "create", "--bundle", dir, "c1")
	once, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	run(t, ca, "create", "--bundle", dir, "c1")
	if twice, _ := os.ReadFile(filepath.Join(dir, "config.json")); string(twice) != string(once) {
		t.Fatalf("second pass changed the config:\n%s\n%s", once, twice)
	}
}
