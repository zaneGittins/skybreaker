// Package sandbox runs one long-lived gVisor (runsc) container per project.
//
// The container is the agent's whole world: its repo, toolchains, tmux and an
// inner dockerd. It reaches the network only through the broker's proxy (the
// proxy URL carries the project's session token), has no DNS resolver, and
// sees the broker CA and harness settings through read-only mounts so an agent
// can't quietly disable its own hooks.
package sandbox

import (
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/zaneGittins/skybreaker/internal/names"
)

const (
	// Runtime is gVisor's OCI runtime name as registered in Docker's
	// daemon.json. Sandboxes run under nothing else.
	Runtime = "runsc"
	// NoProxy is what tools in the sandbox reach directly: itself, and the
	// host as "broker" (its gateway), for routes and Skybreaker's own MCP.
	NoProxy = "localhost,127.0.0.1,broker"
	// Placeholder is the value tools that insist on a credential get. The
	// broker swaps it for the real one on hosts it is bound to.
	Placeholder = "skybreaker-injected"

	caBundle = "/etc/ssl/certs/ca-certificates.crt"
	caCert   = "/etc/skybreaker/ca.crt"
)

// ContainerName is the Docker name of a project's sandbox.
func ContainerName(project string) string { return "skybreaker-" + project }

// NetworkName is the Docker network a project's sandbox sits on alone.
func NetworkName(project string) string { return "skybreaker-" + project }

// Mount is a host directory or file bound into the sandbox.
type Mount struct {
	Source, Target string
	ReadOnly       bool
}

// Spec describes a project's sandbox container.
type Spec struct {
	Project, Image string
	Subnet         netip.Prefix // the project's own bridge network
	ProxyPort      uint16       // the broker's port on the bridge gateway
	Token          string       // broker session token for this sandbox
	DataDir        string       // host dir holding workspace/, home/, etc/, runs/, docker/
	Env            map[string]string
	Memory, CPUs   string
	Hostname       string
}

var (
	hostnameRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
	envKeyRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	// The token is placed in a URL's userinfo, so anything outside the
	// unreserved set could rewrite the proxy URL (an "@" moves the host).
	tokenRe  = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)
	imageRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@-]*$`)
	memoryRe = regexp.MustCompile(`^[0-9]+[bkmgBKMG]?$`)
	cpusRe   = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)
)

// reserved env vars are set by the sandbox itself; letting config override
// them could point the sandbox around the broker.
var reserved = map[string]bool{
	"HTTPS_PROXY": true, "HTTP_PROXY": true, "NO_PROXY": true, "ALL_PROXY": true,
	"SSL_CERT_FILE": true, "REQUESTS_CA_BUNDLE": true, "NODE_EXTRA_CA_CERTS": true, "GIT_SSL_CAINFO": true,
	"SKYBREAKER_PROJECT": true, "SKYBREAKER_TOKEN": true,
}

// ValidateEnv checks a project's extra sandbox environment. The variables
// the sandbox sets itself, such as its proxy settings, can't be overridden.
func ValidateEnv(env map[string]string) error {
	var errs []error
	for _, k := range slices.Sorted(maps.Keys(env)) {
		switch {
		case !envKeyRe.MatchString(k):
			errs = append(errs, fmt.Errorf("invalid env name %q", k))
		case reserved[strings.ToUpper(k)]:
			errs = append(errs, fmt.Errorf("env %s is set by the sandbox and can't be overridden", k))
		case strings.ContainsAny(env[k], "\x00\n\r"):
			errs = append(errs, fmt.Errorf("env %s contains a control character", k))
		}
	}
	return errors.Join(errs...)
}

// Validate checks every field that reaches the docker command line.
func (s Spec) Validate() error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	if !names.Project(s.Project) {
		bad("invalid project name %q", s.Project)
	}
	if !imageRe.MatchString(s.Image) {
		bad("invalid image %q", s.Image)
	}
	if err := checkSubnet(s.Subnet); err != nil {
		errs = append(errs, err)
	}
	if s.ProxyPort == 0 {
		bad("no broker port")
	}
	if !tokenRe.MatchString(s.Token) {
		bad("invalid or missing session token")
	}
	switch {
	case !filepath.IsAbs(s.DataDir) || filepath.Clean(s.DataDir) != s.DataDir:
		bad("data dir %q must be a clean absolute path", s.DataDir)
	case strings.ContainsAny(s.DataDir, ",=\"\n"):
		// These would split or rewrite a --mount option.
		bad("data dir %q contains , = \" or a newline", s.DataDir)
	}
	if s.Hostname != "" && !hostnameRe.MatchString(s.Hostname) {
		bad("invalid hostname %q", s.Hostname)
	}
	if err := ValidateEnv(s.Env); err != nil {
		errs = append(errs, err)
	}
	if s.Memory != "" && !memoryRe.MatchString(s.Memory) {
		bad("invalid memory limit %q", s.Memory)
	}
	if s.CPUs != "" && !cpusRe.MatchString(s.CPUs) {
		bad("invalid cpu limit %q", s.CPUs)
	}
	if len(errs) > 0 {
		return fmt.Errorf("sandbox spec: %w", errors.Join(errs...))
	}
	return nil
}

func checkSubnet(p netip.Prefix) error {
	if !p.IsValid() || !p.Addr().Is4() || p.Bits() < 16 || p.Bits() > 28 {
		return fmt.Errorf("subnet %v must be an IPv4 prefix between /16 and /28", p)
	}
	return nil
}

// Mounts lists the bind mounts, in docker argument order.
func (s Spec) Mounts() []Mount {
	d := s.DataDir
	return []Mount{
		{Source: filepath.Join(d, "workspace"), Target: "/workspace"},
		{Source: filepath.Join(d, "home"), Target: "/root"},
		{Source: filepath.Join(d, "etc"), Target: "/etc/skybreaker", ReadOnly: true},
		// Managed settings outrank user and project settings, so the
		// control plane's hooks can't be overridden from inside.
		{Source: filepath.Join(d, "etc", settingsFile), Target: "/etc/claude-code/managed-settings.json", ReadOnly: true},
		// Every agent's instructions, set in the app: Claude Code reads its
		// managed CLAUDE.md, Codex its managed config's developer
		// instructions (and pi gets /etc/skybreaker/instructions.md).
		{Source: filepath.Join(d, "etc", InstructionsFile), Target: "/etc/claude-code/CLAUDE.md", ReadOnly: true},
		{Source: filepath.Join(d, "etc", CodexConfigFile), Target: "/etc/codex/managed_config.toml", ReadOnly: true},
		{Source: filepath.Join(d, "runs"), Target: "/skybreaker/runs"},
		// The inner dockerd's images and build cache, on disk rather than a
		// tmpfs, which would take them out of the sandbox's memory. overlay2
		// works on the bind mount (only gVisor's own root overlay would push
		// it down to vfs). Like workspace/ and home/, the host never reads it.
		{Source: filepath.Join(d, "docker"), Target: "/var/lib/docker"},
	}
}

const settingsFile = "claude-managed-settings.json"

// The files in a sandbox's etc/ that hold every agent's instructions: as
// written, and as Codex's managed config. They are mounted on their own,
// so they're rewritten in place: a rename would leave the mount on the old
// file.
const (
	InstructionsFile = "instructions.md"
	CodexConfigFile  = "codex-managed-config.toml"
)

// env returns the sandbox environment, sorted by name.
func (s Spec) env() []string {
	proxy := fmt.Sprintf("http://%s:%s@broker:%d", s.Project, s.Token, s.ProxyPort)
	m := map[string]string{
		"HTTPS_PROXY": proxy, "https_proxy": proxy,
		"HTTP_PROXY": proxy, "http_proxy": proxy,
		"NO_PROXY": NoProxy, "no_proxy": NoProxy,
		"SSL_CERT_FILE":       caBundle,
		"REQUESTS_CA_BUNDLE":  caBundle,
		"GIT_SSL_CAINFO":      caBundle,
		"NODE_EXTRA_CA_CERTS": caCert,
		"GH_TOKEN":            Placeholder,
		"SKYBREAKER_PROJECT":  s.Project,
		"SKYBREAKER_TOKEN":    s.Token,
	}
	for k, v := range s.Env {
		m[k] = v
	}
	return sortedPairs(m)
}

func sortedPairs(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	slices.Sort(out)
	return out
}

// RunArgs builds the `docker run` arguments, without the docker binary.
func (s Spec) RunArgs() ([]string, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	hostname := s.Hostname
	if hostname == "" {
		hostname = s.Project
	}
	args := []string{
		"run", "-d",
		"--name", ContainerName(s.Project),
		"--hostname", hostname,
		"--runtime", Runtime,
		"--network", NetworkName(s.Project),
		// Nothing listens here: the proxy resolves names, and DNS is a
		// proven exfiltration channel.
		"--dns", "127.0.0.1",
		// The broker listens on the host; this is the host's address on
		// the project's own network.
		"--add-host", "broker:" + Gateway(s.Subnet).String(),
		"--ip", Addr(s.Subnet).String(),
		// Inner dockerd needs capabilities. Under runsc they exist only in
		// gVisor's user-space kernel.
		"--cap-add", "ALL",
		"--label", "skybreaker.project=" + s.Project,
	}
	if s.Memory != "" {
		args = append(args, "--memory", s.Memory)
	}
	if s.CPUs != "" {
		args = append(args, "--cpus", s.CPUs)
	}
	for _, m := range s.Mounts() {
		// --mount, unlike -v, fails on a missing source instead of creating
		// a directory in its place.
		opt := "type=bind,source=" + m.Source + ",target=" + m.Target
		if m.ReadOnly {
			opt += ",readonly"
		}
		args = append(args, "--mount", opt)
	}
	for _, kv := range s.env() {
		args = append(args, "-e", kv)
	}
	return append(args, s.Image), nil
}
