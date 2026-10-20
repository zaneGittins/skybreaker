package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/zaneGittins/skybreaker/internal/names"
)

// Runner runs a command and returns its stdout. Errors carry the trimmed
// stderr so docker's message reaches the caller.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ExecRunner runs commands with os/exec. What a sandbox command prints is
// the sandbox's to choose (it can replace git, sh or tmux), so stdout is
// read only up to MaxOutput, and a command that prints more is killed;
// stderr keeps its first maxStderr bytes.
type ExecRunner struct {
	MaxOutput int64 // 0 is DefaultMaxOutput
}

const (
	DefaultMaxOutput = 32 << 20
	maxStderr        = 64 << 10
)

// ErrOutputLimit reports a command stopped for printing too much.
var ErrOutputLimit = errors.New("output over the limit")

// capped keeps the first max bytes written to it. Over the limit it calls
// over (once) and, if fail is set, fails the write so the copy stops.
type capped struct {
	buf  bytes.Buffer
	max  int64
	fail bool
	hit  bool
	over func()
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - int64(c.buf.Len()); int64(len(p)) > room {
		c.buf.Write(p[:max(room, 0)])
		if !c.hit {
			c.hit = true
			c.over()
		}
		if c.fail {
			return 0, ErrOutputLimit
		}
		return len(p), nil
	}
	return c.buf.Write(p)
}

func (r ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	limit := r.MaxOutput
	if limit <= 0 {
		limit = DefaultMaxOutput
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stdout := &capped{max: limit, fail: true, over: cancel}
	stderr := &capped{max: maxStderr, over: func() {}}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	if stdout.hit {
		return stdout.buf.Bytes(), fmt.Errorf("%s %s: %w (%d bytes)", name, firstArg(args), ErrOutputLimit, limit)
	}
	if err != nil {
		if msg := strings.TrimSpace(stderr.buf.String()); msg != "" {
			return stdout.buf.Bytes(), fmt.Errorf("%s %s: %w: %s", name, firstArg(args), err, msg)
		}
		return stdout.buf.Bytes(), fmt.Errorf("%s %s: %w", name, firstArg(args), err)
	}
	return stdout.buf.Bytes(), nil
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

// CheckRuntime makes sure Docker has gVisor registered as runsc. Skybreaker
// runs the broker, its secrets and the agents on one host, and gVisor is
// what stands between them: without it, Skybreaker refuses to start.
func (m *Manager) CheckRuntime(ctx context.Context) error {
	out, err := m.run(ctx, "info", "--format", "{{json .Runtimes}}")
	if err != nil {
		return fmt.Errorf("checking for gVisor: %w", err)
	}
	var runtimes map[string]struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &runtimes); err != nil {
		return fmt.Errorf("checking for gVisor: docker info: %w", err)
	}
	rt, ok := runtimes[Runtime]
	if !ok {
		return errors.New("gVisor is not installed: Docker has no runsc runtime (see deploy/docker-daemon.json)")
	}
	if filepath.Base(rt.Path) != "runsc" {
		return fmt.Errorf("gVisor is not installed: Docker's runsc runtime runs %s", rt.Path)
	}
	return nil
}

// State is what Docker knows about a project's sandbox.
type State struct {
	Exists, Running bool
	ID              string
}

// Manager drives the docker CLI. The docker socket is root-equivalent, so
// only the control plane holds it; nothing an agent controls ever does.
type Manager struct {
	R      Runner
	Docker string // docker binary, default "docker"
}

func (m *Manager) docker() string {
	if m.Docker == "" {
		return "docker"
	}
	return m.Docker
}

func (m *Manager) run(ctx context.Context, args ...string) ([]byte, error) {
	return m.R.Run(ctx, m.docker(), args...)
}

// notFound recognises docker's "doesn't exist" errors across object kinds and
// versions.
func notFound(err error) bool {
	s := err.Error()
	return strings.Contains(s, "No such") || strings.Contains(s, "not found")
}

// EnsureNetwork creates the project's bridge network on subnet, or checks an
// existing one is on it: nftables rules are written for that subnet, and a
// network elsewhere would fall outside them.
func (m *Manager) EnsureNetwork(ctx context.Context, project string, subnet netip.Prefix) error {
	if !names.Project(project) {
		return fmt.Errorf("invalid project name %q", project)
	}
	if err := checkSubnet(subnet); err != nil {
		return err
	}
	subnet = subnet.Masked()
	name := NetworkName(project)
	out, err := m.run(ctx, "network", "inspect", "-f", "{{range .IPAM.Config}}{{.Subnet}} {{end}}", name)
	if err == nil {
		for _, f := range strings.Fields(string(out)) {
			if p, err := netip.ParsePrefix(f); err == nil && p.Masked() == subnet {
				return nil
			}
		}
		return fmt.Errorf("network %s exists on %q, not %v; remove it or fix the config", name, strings.TrimSpace(string(out)), subnet)
	}
	if !notFound(err) {
		return fmt.Errorf("inspect network %s: %w", name, err)
	}
	_, err = m.run(ctx, "network", "create",
		"--driver", "bridge",
		"--subnet", subnet.String(),
		"--opt", "com.docker.network.bridge.name="+BridgeName(project),
		"--label", "skybreaker.project="+project,
		name)
	if err != nil {
		return fmt.Errorf("create network %s: %w", name, err)
	}
	return nil
}

// Status reports whether the project's sandbox exists and runs.
func (m *Manager) Status(ctx context.Context, project string) (State, error) {
	if !names.Project(project) {
		return State{}, fmt.Errorf("invalid project name %q", project)
	}
	// `container inspect`, because plain `inspect` also matches the
	// project's network, which has the same name.
	out, err := m.run(ctx, "container", "inspect", "-f", "{{.Id}} {{.State.Running}}", ContainerName(project))
	if err != nil {
		if notFound(err) {
			return State{}, nil
		}
		return State{}, fmt.Errorf("inspect sandbox %s: %w", project, err)
	}
	f := strings.Fields(string(out))
	if len(f) != 2 || (f[1] != "true" && f[1] != "false") {
		return State{}, fmt.Errorf("inspect sandbox %s: unexpected output %q", project, out)
	}
	return State{Exists: true, Running: f[1] == "true", ID: f[0]}, nil
}

// Start makes sure the sandbox runs. A running sandbox is left alone. A
// stopped one is replaced, because its environment still holds the session
// token it was created with, which may have been revoked.
func (m *Manager) Start(ctx context.Context, s Spec) error {
	args, err := s.RunArgs()
	if err != nil {
		return err
	}
	if err := m.EnsureNetwork(ctx, s.Project, s.Subnet); err != nil {
		return err
	}
	if err := ensureDataDir(s.DataDir); err != nil {
		return err
	}
	st, err := m.Status(ctx, s.Project)
	if err != nil {
		return err
	}
	if st.Running {
		return nil
	}
	if st.Exists {
		if _, err := m.run(ctx, "rm", "-f", ContainerName(s.Project)); err != nil {
			return fmt.Errorf("remove stopped sandbox %s: %w", s.Project, err)
		}
	}
	if _, err := m.run(ctx, args...); err != nil {
		return fmt.Errorf("start sandbox %s: %w", s.Project, err)
	}
	return nil
}

// ensureDataDir creates the host directories the mounts need, plus the
// files mounted on their own (managed settings, instructions) so each
// read-only file mount has a source. Existing files are kept: the control
// plane writes the real ones.
func ensureDataDir(dir string) error {
	for _, d := range []string{"workspace", "home", "etc", "runs", "docker"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o700); err != nil {
			return fmt.Errorf("sandbox data dir: %w", err)
		}
	}
	for name, body := range map[string]string{settingsFile: "{}\n", InstructionsFile: "", CodexConfigFile: ""} {
		f, err := os.OpenFile(filepath.Join(dir, "etc", name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("sandbox %s: %w", name, err)
		}
		_, err = f.WriteString(body)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// Stop stops and removes the sandbox. Its data directory stays.
func (m *Manager) Stop(ctx context.Context, project string) error {
	st, err := m.Status(ctx, project)
	if err != nil || !st.Exists {
		return err
	}
	name := ContainerName(project)
	if st.Running {
		if _, err := m.run(ctx, "stop", "-t", "10", name); err != nil {
			return fmt.Errorf("stop sandbox %s: %w", project, err)
		}
	}
	if _, err := m.run(ctx, "rm", "-f", name); err != nil {
		return fmt.Errorf("remove sandbox %s: %w", project, err)
	}
	return nil
}

// Exec runs a command inside the sandbox and returns its stdout.
func (m *Manager) Exec(ctx context.Context, project string, args ...string) ([]byte, error) {
	if !names.Project(project) {
		return nil, fmt.Errorf("invalid project name %q", project)
	}
	return m.run(ctx, append([]string{"exec", ContainerName(project)}, args...)...)
}

// ExecArgv is the full command line for an interactive exec under a PTY,
// such as a terminal viewer's `tmux attach`.
func (m *Manager) ExecArgv(project string, args ...string) []string {
	return append([]string{m.docker(), "exec", "-it", ContainerName(project)}, args...)
}

// ExecPipeArgv is the command line for a long-lived exec over pipes, with
// no TTY: a chat harness speaking JSON lines on stdin and stdout. Env is
// passed sorted, so the command is stable.
func (m *Manager) ExecPipeArgv(project, workdir string, env map[string]string, args ...string) []string {
	out := []string{m.docker(), "exec", "-i"}
	if workdir != "" {
		out = append(out, "-w", workdir)
	}
	for _, k := range slices.Sorted(maps.Keys(env)) {
		out = append(out, "-e", k+"="+env[k])
	}
	return append(append(out, ContainerName(project)), args...)
}

// ImageStale reports whether the project's container runs an older build
// of image than the one now tagged: the weekly rebuild promoted a new one.
func (m *Manager) ImageStale(ctx context.Context, project, image string) (bool, error) {
	if !names.Project(project) {
		return false, fmt.Errorf("invalid project name %q", project)
	}
	have, err := m.run(ctx, "container", "inspect", "-f", "{{.Image}}", ContainerName(project))
	if err != nil {
		if notFound(err) {
			return false, nil
		}
		return false, err
	}
	want, err := m.run(ctx, "image", "inspect", "-f", "{{.Id}}", image)
	if err != nil {
		return false, err
	}
	h, w := strings.TrimSpace(string(have)), strings.TrimSpace(string(want))
	return h != "" && w != "" && h != w, nil
}
