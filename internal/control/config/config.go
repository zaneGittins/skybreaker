// Package config is Skybreaker's one config file: who may use it, the
// sandbox image, projects (with what each may reach through the broker),
// the capabilities projects share, harness overrides and scheduled jobs.
// Every project also gets a board; see package board.
package config

import (
	"errors"
	"fmt"
	"maps"
	"net"
	"net/netip"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/zaneGittins/skybreaker/internal/configfile"
	"github.com/zaneGittins/skybreaker/internal/control/cron"
	"github.com/zaneGittins/skybreaker/internal/control/harness"
	"github.com/zaneGittins/skybreaker/internal/control/sandbox"
	"github.com/zaneGittins/skybreaker/internal/names"
	"github.com/zaneGittins/skybreaker/internal/notify"
	policy "github.com/zaneGittins/skybreaker/internal/proxy/config"
)

type Config struct {
	DataDir string `json:"data_dir" yaml:"data_dir"`
	// Users are the tailnet logins allowed to use Skybreaker, approvals
	// included.
	Users []string `json:"users" yaml:"users"`
	// Admins may change settings in the app (secrets, the GitHub App,
	// bundles, project access and new projects). Default: every user.
	Admins []string `json:"admins" yaml:"admins"`
	// AppSettings false makes this file the only way to change them; the
	// app's files load as they stand. Default true.
	AppSettings  *bool                       `json:"app_settings" yaml:"app_settings"`
	UIURL        string                      `json:"ui_url" yaml:"ui_url"` // Skybreaker's address, for notification links
	Tailscale    Tailscale                   `json:"tailscale" yaml:"tailscale"`
	SecretsFile  string                      `json:"secrets_file" yaml:"secrets_file"`
	Broker       Broker                      `json:"broker" yaml:"broker"`
	Sandbox      Sandbox                     `json:"sandbox" yaml:"sandbox"`
	Ntfy         notify.Config               `json:"ntfy" yaml:"ntfy"`
	Overrides    map[string]harness.Manifest `json:"harnesses,omitempty" yaml:"harnesses,omitempty"`
	Capabilities map[string][]*policy.Entry  `json:"capabilities" yaml:"capabilities"`
	Projects     map[string]*Project         `json:"projects" yaml:"projects"`
	Jobs         []*Job                      `json:"jobs" yaml:"jobs"`
	// Pi sets pi up in sandboxes, with model servers behind broker routes.
	Pi Pi `json:"pi" yaml:"pi"`

	// Harnesses is built from the builtins plus Overrides.
	Harnesses *harness.Registry `json:"-" yaml:"-"`
	// Policy is the broker's compiled view of projects and capabilities.
	Policy *policy.Config `json:"-" yaml:"-"`
	// AppSecretsFile holds the secrets added in the app.
	AppSecretsFile string `json:"-" yaml:"-"`

	base       netip.Prefix
	subnets    map[string]netip.Prefix
	added      map[string]bool  // projects created in the app (projects.json)
	fileAccess map[string]Grant // each project's access as the config file gives it
	appBundles map[string]bool  // bundles made in the app (access.json)
}

type Tailscale struct {
	Hostname string `json:"hostname" yaml:"hostname"`   // default "skybreaker"
	StateDir string `json:"state_dir" yaml:"state_dir"` // default <data_dir>/tailscale
	// Disabled serves the UI on DevListen without Tailscale, as the first
	// configured user. Only loopback addresses are accepted.
	Disabled  bool   `json:"disabled" yaml:"disabled"`
	DevListen string `json:"dev_listen" yaml:"dev_listen"`
}

// Broker tunes the egress proxy. Sandboxes reach it on their own bridge
// gateway, at "broker:<port>".
type Broker struct {
	Port uint16 `json:"port" yaml:"port"` // default 8080
	// Listen is where the proxy binds, default ":<port>". Callers are
	// checked against the sandbox networks.
	Listen      string              `json:"listen" yaml:"listen"`
	Hold        configfile.Duration `json:"hold" yaml:"hold"`                 // how long an ask request waits, default 30s
	PendingTTL  configfile.Duration `json:"pending_ttl" yaml:"pending_ttl"`   // default 10m
	ApprovedTTL configfile.Duration `json:"approved_ttl" yaml:"approved_ttl"` // default 5m
	Netguard    Netguard            `json:"netguard" yaml:"netguard"`
}

// Netguard adjusts which addresses upstreams may resolve to. Loopback,
// link-local, private, CGNAT (tailnet) and reserved ranges are always
// blocked; add your LAN to block, and allow a private upstream per route
// with allow_private.
type Netguard struct {
	Block []string `json:"block" yaml:"block"`
	Allow []string `json:"allow" yaml:"allow"`
}

type Sandbox struct {
	Image      string `json:"image" yaml:"image"`
	SubnetBase string `json:"subnet_base" yaml:"subnet_base"` // a /16, default 10.89.0.0/16
	HookPort   uint16 `json:"hook_port" yaml:"hook_port"`     // default 7701
	// HookListen is where the hook listener binds, default ":<hook_port>"
	// (every sandbox calls its own bridge gateway). Callers are checked
	// against their project's subnet.
	HookListen string `json:"hook_listen" yaml:"hook_listen"`
	Memory     string `json:"memory" yaml:"memory"`
	CPUs       string `json:"cpus" yaml:"cpus"`
}

type Project struct {
	Repo      string            `json:"repo" yaml:"repo"` // owner/name on GitHub
	Branch    string            `json:"branch" yaml:"branch"`
	Harnesses []string          `json:"harnesses" yaml:"harnesses"`
	Key       string            `json:"key" yaml:"key"` // the board's ref prefix, such as BIF; derived from the name when unset
	Subnet    string            `json:"subnet" yaml:"subnet"`
	Env       map[string]string `json:"env" yaml:"env"`
	// Allow and Capabilities are what the project's sandbox may reach;
	// nothing else gets out.
	Allow        []*policy.Entry `json:"allow" yaml:"allow"`
	Capabilities []string        `json:"capabilities" yaml:"capabilities"`
}

const (
	JobPrompt = "prompt"
	JobBoard  = "board" // work the next Todo item on the project's board
)

type Job struct {
	Name         string              `json:"name" yaml:"name"`
	Project      string              `json:"project" yaml:"project"`
	Harness      string              `json:"harness" yaml:"harness"`
	Schedule     string              `json:"schedule" yaml:"schedule"`
	Kind         string              `json:"kind" yaml:"kind"` // prompt (default) or board
	Prompt       string              `json:"prompt" yaml:"prompt"`
	BudgetUSD    float64             `json:"budget_usd" yaml:"budget_usd"`
	AllowedTools []string            `json:"allowed_tools" yaml:"allowed_tools"`
	Model        string              `json:"model" yaml:"model"`
	Timeout      configfile.Duration `json:"timeout" yaml:"timeout"` // default 1h

	Cron *cron.Schedule `json:"-" yaml:"-"`
}

var (
	jobNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)
	keyRe     = regexp.MustCompile(`^[A-Z][A-Z0-9]{0,7}$`)
	branchRe  = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._/-]{0,199}$`)
)

// Load reads the config file and what the app adds to it: projects,
// access and secrets (see Paths).
func Load(path string) (*Config, error) { return loadWith(path, nil) }

// loadWith loads the config file with the app's files, or with staged ones
// that Edit is checking.
func loadWith(path string, staged *Paths) (*Config, error) {
	var c Config
	if err := configfile.Load(path, &c); err != nil {
		return nil, err
	}
	paths := c.AppPaths()
	switch {
	case staged != nil:
		paths = *staged
	case !filepath.IsAbs(c.DataDir):
		paths = Paths{} // validate refuses it; read nothing relative
	}
	c.fileAccess, c.appBundles = map[string]Grant{}, map[string]bool{}
	for name, p := range c.Projects {
		if p != nil {
			c.fileAccess[name] = Grant{Capabilities: slices.Clone(p.Capabilities), Allow: slices.Clone(p.Allow)}
		}
	}
	f, err := readAdded(paths.Projects)
	if err != nil {
		return nil, err
	}
	if err := c.merge(f, paths.Projects); err != nil {
		return nil, err
	}
	var a Access
	if err := readJSON(paths.Access, &a); err != nil {
		return nil, err
	}
	if err := c.mergeAccess(a, paths.Access); err != nil {
		return nil, err
	}
	c.AppSecretsFile = paths.Secrets
	if err := c.init(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

// SubnetBase is the range every sandbox network is in.
func (c *Config) SubnetBase() netip.Prefix { return c.base }

func (c *Config) init() error {
	c.defaults()
	reg, err := harness.NewRegistry(c.Overrides)
	if err != nil {
		return err
	}
	c.Harnesses = reg
	return c.validate()
}

func (c *Config) defaults() {
	if c.Tailscale.Hostname == "" {
		c.Tailscale.Hostname = "skybreaker"
	}
	if c.Tailscale.StateDir == "" && c.DataDir != "" {
		c.Tailscale.StateDir = filepath.Join(c.DataDir, "tailscale")
	}
	if c.Broker.Port == 0 {
		c.Broker.Port = 8080
	}
	if c.Broker.Listen == "" {
		c.Broker.Listen = ":" + strconv.Itoa(int(c.Broker.Port))
	}
	c.Broker.Hold = configfile.Duration(c.Broker.Hold.Or(30 * time.Second))
	if c.Sandbox.SubnetBase == "" {
		c.Sandbox.SubnetBase = "10.89.0.0/16"
	}
	if c.Sandbox.HookPort == 0 {
		c.Sandbox.HookPort = 7701
	}
	if c.Sandbox.HookListen == "" {
		c.Sandbox.HookListen = ":" + strconv.Itoa(int(c.Sandbox.HookPort))
	}
	for _, p := range c.Projects {
		if p == nil {
			continue
		}
		if p.Branch == "" {
			p.Branch = "main"
		}
		if len(p.Harnesses) == 0 {
			p.Harnesses = []string{"claude"}
		}
		if !slices.Contains(p.Harnesses, harness.Shell) {
			p.Harnesses = append(p.Harnesses, harness.Shell)
		}
	}
	for _, j := range c.Jobs {
		if j == nil {
			continue
		}
		if j.Harness == "" {
			j.Harness = "claude"
		}
		if j.Kind == "" {
			j.Kind = JobPrompt
		}
		if j.Timeout == 0 {
			j.Timeout = configfile.Duration(time.Hour)
		}
	}
}

func (c *Config) validate() error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	if !filepath.IsAbs(c.DataDir) {
		bad("data_dir must be an absolute path")
	}
	if len(c.Users) == 0 {
		bad("users: list at least one tailnet login allowed to use the UI")
	}
	for _, a := range c.Admins {
		if !slices.ContainsFunc(c.Users, func(u string) bool { return strings.EqualFold(u, a) }) {
			bad("admins: %s is not in users", a)
		}
	}
	if c.Tailscale.Disabled {
		host, _, err := net.SplitHostPort(c.Tailscale.DevListen)
		ip, perr := netip.ParseAddr(host)
		if err != nil || perr != nil || !ip.IsLoopback() {
			bad("tailscale.dev_listen must be a loopback address when tailscale is disabled")
		}
	}
	if c.Broker.Port == c.Sandbox.HookPort {
		bad("broker.port and sandbox.hook_port must differ")
	}
	for _, s := range append(slices.Clone(c.Broker.Netguard.Block), c.Broker.Netguard.Allow...) {
		if _, err := netip.ParsePrefix(s); err != nil {
			if _, err := netip.ParseAddr(s); err != nil {
				bad("broker.netguard: %q is not an address or CIDR", s)
			}
		}
	}
	if c.Sandbox.Image == "" {
		bad("sandbox.image is required")
	}
	var err error
	c.base, err = netip.ParsePrefix(c.Sandbox.SubnetBase)
	if err != nil || !c.base.Addr().Is4() || c.base.Bits() != 16 || c.base.Masked() != c.base {
		bad("sandbox.subnet_base must be an IPv4 /16 such as 10.89.0.0/16")
		return errors.Join(errs...)
	}

	if len(c.Projects) == 0 {
		bad("projects: define at least one project")
	}
	c.subnets = map[string]netip.Prefix{}
	owner := map[netip.Prefix]string{}
	keys := map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(c.Projects)) {
		p := c.Projects[name]
		if p == nil {
			bad("project %s is empty", name)
			continue
		}
		if !names.Project(name) {
			bad("project name %q: use lowercase letters, digits and dashes", name)
			continue
		}
		if p.Repo != "" && !names.Repo(p.Repo) {
			bad("project %s: repo must be owner/name", name)
		}
		if p.Key != "" {
			if !keyRe.MatchString(p.Key) {
				bad("project %s: key %q: use an uppercase letter then up to seven uppercase letters or digits", name, p.Key)
			} else if other, taken := keys[p.Key]; taken {
				bad("projects %s and %s both use board key %s", other, name, p.Key)
			}
			keys[p.Key] = name
		}
		if !validBranch(p.Branch) {
			bad("project %s: branch %q is not a plain git branch name", name, p.Branch)
		}
		if err := sandbox.ValidateEnv(p.Env); err != nil {
			bad("project %s: %w", name, err)
		}
		for _, h := range p.Harnesses {
			if _, ok := c.Harnesses.Get(h); !ok {
				bad("project %s: unknown harness %q (known: %v)", name, h, c.Harnesses.Names())
			}
		}
		sn := sandbox.SubnetFor(name, c.base)
		if p.Subnet != "" {
			sn, err = netip.ParsePrefix(p.Subnet)
			if err != nil || sn.Bits() != 24 || sn.Masked() != sn || !c.base.Contains(sn.Addr()) {
				bad("project %s: subnet must be a /24 inside %s", name, c.base)
				continue
			}
		}
		if other, taken := owner[sn]; taken {
			bad("projects %s and %s both use subnet %s; set an explicit subnet on one", other, name, sn)
			continue
		}
		owner[sn] = name
		c.subnets[name] = sn
	}

	projects := map[string]*policy.Project{}
	for name, p := range c.Projects {
		if p != nil {
			projects[name] = &policy.Project{Repo: p.Repo, Allow: p.Allow, Capabilities: p.Capabilities}
		}
	}
	if c.Policy, err = policy.New(projects, c.Capabilities); err != nil {
		errs = append(errs, err)
	}
	if err := c.validatePi(); err != nil {
		errs = append(errs, err)
	}

	seen := map[string]bool{}
	for i, j := range c.Jobs {
		if j == nil {
			bad("jobs[%d] is empty", i)
			continue
		}
		if !jobNameRe.MatchString(j.Name) {
			bad("jobs[%d]: name %q: use lowercase letters, digits and dashes", i, j.Name)
		}
		if seen[j.Name] {
			bad("job %s is defined twice", j.Name)
		}
		seen[j.Name] = true
		p, ok := c.Projects[j.Project]
		if !ok || p == nil {
			bad("job %s: unknown project %q", j.Name, j.Project)
			continue
		}
		if !slices.Contains(p.Harnesses, j.Harness) {
			bad("job %s: harness %q is not enabled for project %s", j.Name, j.Harness, j.Project)
		} else if m, ok := c.Harnesses.Get(j.Harness); ok && len(m.Headless) == 0 {
			bad("job %s: harness %s has no headless command", j.Name, j.Harness)
		}
		if j.Cron, err = cron.Parse(j.Schedule); err != nil {
			bad("job %s: %v", j.Name, err)
		}
		switch j.Kind {
		case JobPrompt:
			if j.Prompt == "" {
				bad("job %s: prompt is required", j.Name)
			}
		case JobBoard:
		default:
			bad("job %s: kind must be prompt or board", j.Name)
		}
		if j.BudgetUSD < 0 {
			bad("job %s: budget_usd is negative", j.Name)
		}
	}
	return errors.Join(errs...)
}

// Subnet is a project's sandbox network.
func (c *Config) Subnet(project string) netip.Prefix { return c.subnets[project] }

// Gateway is the host's address on a project's network, where the hook
// listener answers.
func (c *Config) Gateway(project string) netip.Addr { return sandbox.Gateway(c.subnets[project]) }

// HookURL is the URL a sandbox's Claude hooks post to.
func (c *Config) HookURL(project, token string) string {
	return "http://" + net.JoinHostPort(c.Gateway(project).String(), strconv.Itoa(int(c.Sandbox.HookPort))) + "/hooks/claude/" + project + "/" + token
}

// MCPURL is where a sandbox reaches Skybreaker's MCP server: the hook
// listener on its own gateway, with the project's hook token, for one chat
// session ("-" for none). The host is "broker", the sandbox's name for its
// gateway: it is in NO_PROXY, where the gateway's address is not, so
// harnesses don't send the call through the broker.
func (c *Config) MCPURL(project, token, session string) string {
	return "http://broker:" + strconv.Itoa(int(c.Sandbox.HookPort)) + "/mcp/" + project + "/" + token + "/" + session
}

// ProjectOf names the project whose sandbox network holds ip.
func (c *Config) ProjectOf(ip netip.Addr) (string, bool) {
	ip = ip.Unmap()
	for name, sn := range c.subnets {
		if sn.Contains(ip) {
			return name, true
		}
	}
	return "", false
}

// Job returns a job by name.
func (c *Config) Job(name string) (*Job, bool) {
	i := slices.IndexFunc(c.Jobs, func(j *Job) bool { return j.Name == name })
	if i < 0 {
		return nil, false
	}
	return c.Jobs[i], true
}

// validBranch accepts a plain git branch name. It reaches git as
// origin/<branch>, so nothing git reads as an option, a revision expression
// (.., ~, ^, @{) or a glob gets through.
func validBranch(b string) bool {
	return branchRe.MatchString(b) && !strings.Contains(b, "..") && !strings.Contains(b, "//") &&
		!strings.Contains(b, "/.") && !strings.HasSuffix(b, "/") && !strings.HasSuffix(b, ".") && !strings.HasSuffix(b, ".lock")
}
