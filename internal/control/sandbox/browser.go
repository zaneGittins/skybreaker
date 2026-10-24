package sandbox

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"

	"github.com/zaneGittins/skybreaker/internal/names"
)

// BrowserContainer is the Docker name of an assistant's browser.
func BrowserContainer(project string) string { return ContainerName(project) + "-browser" }

// BrowserAddr is the browser's address on the assistant's network, after
// the sandbox's.
func BrowserAddr(subnet netip.Prefix) netip.Addr { return Addr(subnet).Next() }

// BrowserSpec is an assistant's browser: the sandbox image, asleep, with
// Chromium started in it over a pipe. Its profile, holding the logins, is
// mounted here only, never into the sandbox.
type BrowserSpec struct {
	Project, Image string
	Subnet         netip.Prefix
	Profile        string // host directory
	Memory         string
}

// RunArgs builds the browser's `docker run` arguments.
func (b BrowserSpec) RunArgs() ([]string, error) {
	var errs []error
	if !names.Project(b.Project) {
		errs = append(errs, fmt.Errorf("invalid project name %q", b.Project))
	}
	if !imageRe.MatchString(b.Image) {
		errs = append(errs, fmt.Errorf("invalid image %q", b.Image))
	}
	if err := checkSubnet(b.Subnet); err != nil {
		errs = append(errs, err)
	}
	if !filepath.IsAbs(b.Profile) || filepath.Clean(b.Profile) != b.Profile || strings.ContainsAny(b.Profile, ",=\"\n") {
		errs = append(errs, fmt.Errorf("browser profile %q must be a clean absolute path without , = or \"", b.Profile))
	}
	if b.Memory != "" && !memoryRe.MatchString(b.Memory) {
		errs = append(errs, fmt.Errorf("invalid memory limit %q", b.Memory))
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("browser spec: %w", errors.Join(errs...))
	}
	args := []string{
		"run", "-d",
		"--name", BrowserContainer(b.Project),
		"--hostname", "browser",
		"--runtime", Runtime,
		"--network", NetworkName(b.Project),
		"--ip", BrowserAddr(b.Subnet).String(),
		"--dns", "127.0.0.1",
		"--read-only", "--tmpfs", "/tmp:size=256m", "--tmpfs", "/downloads:size=512m",
		// Chromium runs without its own sandbox here, so it gets nothing
		// to escalate with.
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "512", "--cpus", "2",
		"-e", "HOME=/tmp",
		"--mount", "type=bind,source=" + b.Profile + ",target=/profile",
		"--label", "skybreaker.project=" + b.Project,
		"--label", "skybreaker.browser=1",
		"--entrypoint", "/bin/sleep",
	}
	if b.Memory != "" {
		args = append(args, "--memory", b.Memory)
	}
	return append(args, b.Image, "infinity"), nil
}

// StartBrowser makes sure an assistant's browser container runs, replacing
// a stopped one. The sandbox's network must exist.
func (m *Manager) StartBrowser(ctx context.Context, b BrowserSpec) error {
	args, err := b.RunArgs()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(b.Profile, 0o700); err != nil {
		return err
	}
	name := BrowserContainer(b.Project)
	out, err := m.run(ctx, "container", "inspect", "-f", "{{.Id}} {{.State.Running}}", name)
	switch {
	case err == nil && strings.HasSuffix(strings.TrimSpace(string(out)), " true"):
		return nil
	case err == nil:
		if _, err := m.run(ctx, "rm", "-f", name); err != nil {
			return fmt.Errorf("remove stopped browser %s: %w", b.Project, err)
		}
	case !notFound(err):
		return fmt.Errorf("inspect browser %s: %w", b.Project, err)
	}
	if _, err := m.run(ctx, args...); err != nil {
		return fmt.Errorf("start browser %s: %w", b.Project, err)
	}
	return nil
}

// StopBrowser removes an assistant's browser container; its profile stays.
func (m *Manager) StopBrowser(ctx context.Context, project string) error {
	if !names.Project(project) {
		return fmt.Errorf("invalid project name %q", project)
	}
	if _, err := m.run(ctx, "rm", "-f", BrowserContainer(project)); err != nil && !notFound(err) {
		return err
	}
	return nil
}

// BrowserChromium is the command, run in the browser with docker exec -i,
// that starts Playwright's Chromium with its DevTools pipe on stdin and
// stdout. Its own output goes nowhere, so it can't corrupt the pipe.
func (m *Manager) BrowserChromium(project string, args ...string) []string {
	script := `bin=$(ls -d /opt/ms-playwright/chromium-*/chrome-linux*/chrome 2>/dev/null | head -n 1); exec "$bin" "$@" 3<&0 4>&1 >/dev/null 2>&1`
	return append([]string{m.docker(), "exec", "-i", BrowserContainer(project), "sh", "-c", script, "chromium"}, args...)
}

// BrowserExec runs a command in the browser and returns its stdout.
func (m *Manager) BrowserExec(ctx context.Context, project string, maxOutput int64, args ...string) ([]byte, error) {
	if !names.Project(project) {
		return nil, fmt.Errorf("invalid project name %q", project)
	}
	return m.R.RunInput(ctx, strings.NewReader(""), maxOutput, m.docker(), append([]string{"exec", BrowserContainer(project)}, args...)...)
}
