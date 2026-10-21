// Package setup installs, upgrades and rolls back Skybreaker on one Linux
// host: the binary and its units from a GitHub release, a config and
// secrets from a short wizard, gVisor registered with Docker, and the
// sandbox image. Every command goes through a Runner and every path through
// a Layout, so tests see exactly what would happen to a host without one.
package setup

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Layout is where an install puts things.
type Layout struct {
	Bin    string // skybreaker and skybreaker-sandbox-update
	Etc    string // skybreaker.yaml, secrets.yaml, env, github-app.pem
	Data   string // data_dir: workspaces, homes, the broker's CA and audit log
	Units  string // systemd units
	Image  string // the sandbox image's build context, for the weekly rebuild
	Docker string // dockerd's daemon.json

	OSRelease string // os-release, read for the distribution Docker is installed for
}

// DefaultLayout matches the paths in deploy/*.service.
var DefaultLayout = Layout{
	Bin:    "/usr/local/bin",
	Etc:    "/etc/skybreaker",
	Data:   "/var/lib/skybreaker",
	Units:  "/etc/systemd/system",
	Image:  "/opt/skybreaker/image",
	Docker: "/etc/docker/daemon.json",

	OSRelease: "/etc/os-release",
}

func (l Layout) Config() string  { return filepath.Join(l.Etc, "skybreaker.yaml") }
func (l Layout) Secrets() string { return filepath.Join(l.Etc, "secrets.yaml") }
func (l Layout) AppKey() string  { return filepath.Join(l.Etc, "github-app.pem") }
func (l Layout) Binary() string  { return filepath.Join(l.Bin, "skybreaker") }

// Installed reports whether a config is already in place.
func (l Layout) Installed() bool {
	_, err := os.Stat(l.Config())
	return err == nil
}

// Runner runs a command on the host and returns its combined output.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return out, nil
}

// Host is the machine being set up.
type Host struct {
	Layout
	Cmd      Runner
	Releases Releases
	Out      *Progress
	Arch     string              // GOARCH of the release assets
	Version  string              // the release to install; "dev" means the latest
	Sleep    func(time.Duration) // waits while a service settles
	Settle   time.Duration       // how long a restarted service must stay up
}

func (h *Host) run(ctx context.Context, name string, args ...string) (string, error) {
	out, err := h.Cmd.Run(ctx, name, args...)
	s := strings.TrimSpace(string(out))
	if err != nil && s != "" {
		err = fmt.Errorf("%w\n%s", err, lastLines(out, 8))
	}
	return s, err
}

// lastLines keeps a failing command's error short enough to read.
func lastLines(b []byte, n int) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// writeFile replaces path atomically and reports whether its content
// changed.
func writeFile(path string, data []byte, mode os.FileMode) (bool, error) {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		if fi, err := os.Stat(path); err == nil && fi.Mode().Perm() == mode {
			return false, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	return true, os.Rename(tmp.Name(), path)
}
