// Package config is the broker's policy: which hosts each project may reach,
// in which mode, which credential is added on the way out, and which methods
// and paths are automatic, asked or denied. The control plane's config file
// carries it (projects' allow lists and the shared capabilities); New
// compiles it.
package config

import (
	"crypto/x509"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/zaneGittins/skybreaker/internal/configfile"
	"github.com/zaneGittins/skybreaker/internal/names"
	"github.com/zaneGittins/skybreaker/internal/proxy/k8s"
	"github.com/zaneGittins/skybreaker/internal/proxy/policy"
	"github.com/zaneGittins/skybreaker/internal/proxy/secrets"
)

const (
	ModeTunnel = "tunnel"
	ModeMITM   = "mitm"
	ModeRoute  = "route"
)

// Config is the compiled policy.
type Config struct {
	Projects     map[string]*Project `json:"projects" yaml:"projects"`
	Capabilities map[string][]*Entry `json:"capabilities" yaml:"capabilities"`

	resolved map[string]*ProjectPolicy
}

type Project struct {
	Repo         string   `json:"repo" yaml:"repo"`
	Allow        []*Entry `json:"allow" yaml:"allow"`
	Capabilities []string `json:"capabilities" yaml:"capabilities"`
}

// Entry is one host or route as written in config.
type Entry struct {
	Host    string        `json:"host,omitempty" yaml:"host,omitempty"`
	Mode    string        `json:"mode,omitempty" yaml:"mode,omitempty"`
	Ports   []int         `json:"ports,omitempty" yaml:"ports,omitempty"`
	Methods []string      `json:"methods,omitempty" yaml:"methods,omitempty"` // shorthand for "M1,M2 /**: auto"
	Rules   []policy.Rule `json:"rules,omitempty" yaml:"rules,omitempty"`
	Inject  *Inject       `json:"inject,omitempty" yaml:"inject,omitempty"`

	Route        string      `json:"route,omitempty" yaml:"route,omitempty"`
	Upstream     string      `json:"upstream,omitempty" yaml:"upstream,omitempty"`
	K8s          *k8s.Policy `json:"k8s,omitempty" yaml:"k8s,omitempty"`
	AllowPrivate []string    `json:"allow_private,omitempty" yaml:"allow_private,omitempty"` // addresses the upstream may resolve to despite netguard
	CAFile       string      `json:"ca_file,omitempty" yaml:"ca_file,omitempty"`             // CA that signs the upstream (a cluster's own CA); default system roots
}

// Inject names how a secret is added to the request. Exactly one is set.
type Inject struct {
	Bearer    string     `json:"bearer,omitempty" yaml:"bearer,omitempty"`
	BasicAuth *BasicAuth `json:"basic_auth,omitempty" yaml:"basic_auth,omitempty"`
	Header    *Header    `json:"header,omitempty" yaml:"header,omitempty"`
}

type BasicAuth struct {
	User   string `json:"user" yaml:"user"`
	Secret string `json:"secret" yaml:"secret"`
}

type Header struct {
	Name   string `json:"name" yaml:"name"`
	Secret string `json:"secret" yaml:"secret"`
	Prefix string `json:"prefix,omitempty" yaml:"prefix,omitempty"`
}

func (i *Inject) SecretName() string {
	switch {
	case i == nil:
		return ""
	case i.Bearer != "":
		return i.Bearer
	case i.BasicAuth != nil:
		return i.BasicAuth.Secret
	case i.Header != nil:
		return i.Header.Secret
	}
	return ""
}

// ProjectPolicy is one project's resolved view: hosts and routes from its
// allow list and capabilities, with {repo} expanded.
type ProjectPolicy struct {
	Name   string
	Repo   string
	hosts  map[string]*HostRule
	routes []*RouteRule // longest prefix first
}

type HostRule struct {
	Host       string
	Mode       string
	Ports      []int
	Rules      *policy.Set // nil for tunnel
	Inject     *Inject
	Capability string // "" for the project's own allow list
}

func (h *HostRule) AllowsPort(p int) bool { return slices.Contains(h.Ports, p) }

type RouteRule struct {
	Prefix       string
	Upstream     *url.URL
	Inject       *Inject
	Rules        *policy.Set
	K8s          *k8s.Policy
	AllowPrivate []netip.Prefix
	RootCAs      *x509.CertPool // nil: system roots
	Capability   string
}

// Strip returns the upstream path for a request path under the route.
func (r *RouteRule) Strip(path string) string {
	rest := strings.TrimPrefix(path, r.Prefix)
	if rest == "" {
		return "/"
	}
	return rest
}

// Host returns the rule for a host, ignoring case and a trailing dot.
func (p *ProjectPolicy) Host(host string) *HostRule {
	return p.hosts[normHost(host)]
}

// Route returns the route whose prefix owns path, matching whole segments.
func (p *ProjectPolicy) Route(path string) *RouteRule {
	for _, r := range p.routes {
		if path == r.Prefix || strings.HasPrefix(path, r.Prefix+"/") {
			return r
		}
	}
	return nil
}

func (c *Config) Project(name string) (*ProjectPolicy, bool) {
	p, ok := c.resolved[name]
	return p, ok
}

func (c *Config) ProjectNames() []string { return slices.Sorted(maps.Keys(c.resolved)) }

// New compiles projects and the capabilities they name.
func New(projects map[string]*Project, capabilities map[string][]*Entry) (*Config, error) {
	c := &Config{Projects: projects, Capabilities: capabilities}
	if err := c.resolve(); err != nil {
		return nil, err
	}
	return c, nil
}

// Load reads a policy file: {"projects": ..., "capabilities": ...}.
func Load(path string) (*Config, error) {
	var c Config
	if err := configfile.Load(path, &c); err != nil {
		return nil, err
	}
	if err := c.resolve(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

// LoadSecrets reads the secrets file: {"secrets": [ ... ]}.
func LoadSecrets(path string) ([]secrets.Spec, error) {
	if _, err := configfile.ReadSecret(path); err != nil {
		return nil, err
	}
	var f struct {
		Secrets []secrets.Spec `json:"secrets" yaml:"secrets"`
	}
	if err := configfile.Load(path, &f); err != nil {
		return nil, err
	}
	return f.Secrets, nil
}

var (
	hostRe   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)
	routeRe  = regexp.MustCompile(`^(/[a-z0-9][a-z0-9_-]*)+$`)
	headerRe = regexp.MustCompile(`^[A-Za-z0-9-]+$`)
	reserved = []string{"/ca.crt", "/healthz"}
)

func normHost(h string) string { return strings.TrimSuffix(strings.ToLower(h), ".") }

func (c *Config) resolve() error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	// Check every capability on its own, against a stand-in project, so a
	// mistake in one no project uses yet still fails here.
	stub := &Project{Repo: "example/example"}
	for _, name := range slices.Sorted(maps.Keys(c.Capabilities)) {
		for i, e := range c.Capabilities[name] {
			if e == nil {
				bad("capability %s[%d] is empty", name, i)
				continue
			}
			var err error
			if e.Route != "" {
				_, err = buildRoute(name, e, map[string]string{"repo": stub.Repo}, stub)
			} else {
				_, err = buildHost(name, e, map[string]string{"repo": stub.Repo})
			}
			if err != nil {
				bad("capability %s[%d]: %w", name, i, err)
			}
		}
	}

	c.resolved = map[string]*ProjectPolicy{}
	for _, name := range slices.Sorted(maps.Keys(c.Projects)) {
		p := c.Projects[name]
		if p == nil || !names.Project(name) {
			bad("project %q: invalid name or empty definition", name)
			continue
		}
		if p.Repo != "" && !names.Repo(p.Repo) {
			bad("project %s: repo must be owner/name", name)
			continue
		}
		pp, err := c.resolveProject(name, p)
		if err != nil {
			bad("project %s: %w", name, err)
			continue
		}
		c.resolved[name] = pp
	}
	return errors.Join(errs...)
}

func (c *Config) resolveProject(name string, p *Project) (*ProjectPolicy, error) {
	pp := &ProjectPolicy{Name: name, Repo: p.Repo, hosts: map[string]*HostRule{}}
	vars := map[string]string{}
	if p.Repo != "" {
		vars["repo"] = p.Repo
	}
	add := func(capName string, e *Entry) error {
		if e.Host != "" && e.Route != "" {
			return fmt.Errorf("an entry has both host and route")
		}
		if e.Route != "" {
			r, err := buildRoute(capName, e, vars, p)
			if err != nil {
				return fmt.Errorf("route %s: %w", e.Route, err)
			}
			for _, o := range pp.routes {
				if o.Prefix == r.Prefix {
					return fmt.Errorf("route %s is defined twice", r.Prefix)
				}
			}
			pp.routes = append(pp.routes, r)
			return nil
		}
		h, err := buildHost(capName, e, vars)
		if err != nil {
			return fmt.Errorf("host %q: %w", e.Host, err)
		}
		if _, dup := pp.hosts[h.Host]; dup {
			return fmt.Errorf("host %s is listed twice", h.Host)
		}
		pp.hosts[h.Host] = h
		return nil
	}
	for _, e := range p.Allow {
		if e == nil {
			return nil, errors.New("empty allow entry")
		}
		if err := add("", e); err != nil {
			return nil, err
		}
	}
	for _, capName := range p.Capabilities {
		entries, ok := c.Capabilities[capName]
		if !ok {
			return nil, fmt.Errorf("unknown capability %q", capName)
		}
		for _, e := range entries {
			if e == nil {
				continue
			}
			if err := add(capName, e); err != nil {
				return nil, fmt.Errorf("capability %s: %w", capName, err)
			}
		}
	}
	slices.SortFunc(pp.routes, func(a, b *RouteRule) int { return len(b.Prefix) - len(a.Prefix) })
	return pp, nil
}

func buildHost(capName string, e *Entry, vars map[string]string) (*HostRule, error) {
	host := normHost(e.Host)
	if !hostRe.MatchString(host) {
		return nil, errors.New("must be a bare host name: no scheme, port or wildcard")
	}
	h := &HostRule{Host: host, Mode: e.Mode, Ports: e.Ports, Inject: e.Inject, Capability: capName}
	if len(h.Ports) == 0 {
		h.Ports = []int{443}
	}
	for _, port := range h.Ports {
		if port < 1 || port > 65535 {
			return nil, fmt.Errorf("bad port %d", port)
		}
	}
	if e.K8s != nil || e.Upstream != "" || len(e.AllowPrivate) > 0 || e.CAFile != "" {
		return nil, errors.New("k8s, upstream, allow_private and ca_file belong to routes")
	}
	switch e.Mode {
	case ModeTunnel:
		if len(e.Methods) > 0 || len(e.Rules) > 0 || e.Inject != nil {
			return nil, errors.New("a tunnel is opaque: methods, rules and inject need mode mitm")
		}
		return h, nil
	case ModeMITM:
	default:
		return nil, fmt.Errorf("mode must be tunnel or mitm, not %q", e.Mode)
	}
	if err := checkInject(e.Inject); err != nil {
		return nil, err
	}
	rules, err := entryRules(e)
	if err != nil {
		return nil, err
	}
	if h.Rules, err = policy.Compile(rules, vars); err != nil {
		return nil, err
	}
	return h, nil
}

// entryRules turns the methods shorthand into a rule, refusing both at once.
func entryRules(e *Entry) ([]policy.Rule, error) {
	switch {
	case len(e.Methods) > 0 && len(e.Rules) > 0:
		return nil, errors.New("use methods or rules, not both")
	case len(e.Methods) > 0:
		return []policy.Rule{{Match: strings.Join(e.Methods, ",") + " /**", Action: policy.Auto}}, nil
	case len(e.Rules) == 0:
		return nil, errors.New("mitm needs rules or methods; unmatched requests would all be denied")
	}
	return e.Rules, nil
}

func checkInject(i *Inject) error {
	if i == nil {
		return nil
	}
	n := 0
	if i.Bearer != "" {
		n++
	}
	if i.BasicAuth != nil {
		n++
		if i.BasicAuth.Secret == "" {
			return errors.New("inject.basic_auth.secret is required")
		}
	}
	if i.Header != nil {
		n++
		if !headerRe.MatchString(i.Header.Name) || i.Header.Secret == "" {
			return errors.New("inject.header needs a valid name and a secret")
		}
	}
	if n != 1 {
		return errors.New("inject must set exactly one of bearer, basic_auth or header")
	}
	return nil
}

func buildRoute(capName string, e *Entry, vars map[string]string, p *Project) (*RouteRule, error) {
	if !routeRe.MatchString(e.Route) {
		return nil, errors.New("must look like /name or /name/sub, lowercase, no trailing slash")
	}
	for _, res := range reserved {
		if e.Route == res || strings.HasPrefix(e.Route, res+"/") {
			return nil, fmt.Errorf("%s is reserved", res)
		}
	}
	if e.Host != "" || e.Mode != "" && e.Mode != ModeRoute || len(e.Methods) > 0 || len(e.Ports) > 0 {
		return nil, errors.New("host, mode, methods and ports don't apply to routes")
	}
	u, err := url.Parse(e.Upstream)
	if err != nil || u.Scheme != "https" || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.User != nil {
		return nil, errors.New("upstream must be an https origin such as https://host[:port]")
	}
	u.Path = ""
	if err := checkInject(e.Inject); err != nil {
		return nil, err
	}
	r := &RouteRule{Prefix: e.Route, Upstream: u, Inject: e.Inject, K8s: e.K8s, Capability: capName}
	kinds := 0
	if len(e.Rules) > 0 {
		kinds++
		if r.Rules, err = policy.Compile(e.Rules, vars); err != nil {
			return nil, err
		}
	}
	if e.K8s != nil {
		kinds++
		if err := e.K8s.Validate(); err != nil {
			return nil, err
		}
	}
	if kinds != 1 {
		return nil, errors.New("set exactly one of rules or k8s")
	}
	if e.CAFile != "" {
		pem, err := os.ReadFile(e.CAFile)
		if err != nil {
			return nil, fmt.Errorf("ca_file: %w", err)
		}
		r.RootCAs = x509.NewCertPool()
		if !r.RootCAs.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("ca_file %s holds no PEM certificate", e.CAFile)
		}
	}
	for _, s := range e.AllowPrivate {
		pfx, err := parsePrefix(s)
		if err != nil {
			return nil, fmt.Errorf("allow_private: %w", err)
		}
		r.AllowPrivate = append(r.AllowPrivate, pfx)
	}
	return r, nil
}

// parsePrefix accepts a CIDR or a bare address.
func parsePrefix(s string) (netip.Prefix, error) {
	if p, err := netip.ParsePrefix(s); err == nil {
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("%q is not an address or CIDR", s)
	}
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// CheckSecrets verifies that every injected secret exists and is bound to
// the host it would be sent to, so a policy mistake fails at start-up
// instead of at the first request.
func (c *Config) CheckSecrets(hostsOf func(name string) ([]string, bool)) error {
	var errs []error
	check := func(where, secret, host string) {
		hosts, ok := hostsOf(secret)
		if !ok {
			errs = append(errs, fmt.Errorf("%s: secret %q is not in the secrets file", where, secret))
			return
		}
		if !slices.Contains(hosts, normHost(host)) {
			errs = append(errs, fmt.Errorf("%s: secret %q is not bound to %s (its hosts: %v)", where, secret, host, hosts))
		}
	}
	for _, name := range c.ProjectNames() {
		p := c.resolved[name]
		for _, h := range slices.Sorted(maps.Keys(p.hosts)) {
			if s := p.hosts[h].Inject.SecretName(); s != "" {
				check("project "+name+" host "+h, s, h)
			}
		}
		for _, r := range p.routes {
			if s := r.Inject.SecretName(); s != "" {
				check("project "+name+" route "+r.Prefix, s, r.Upstream.Hostname())
			}
		}
	}
	return errors.Join(errs...)
}
