// Package secrets holds every credential the broker injects. Each secret is
// bound to the upstream hosts it may be sent to: asking for it on behalf of
// any other host fails closed, because an unbound secret turns the proxy into
// an oracle (point it at an echo server and read the secret back).
package secrets

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zaneGittins/skybreaker/internal/proxy/githubapp"
	"github.com/zaneGittins/skybreaker/internal/proxy/k8s"
)

var (
	ErrUnbound = errors.New("secret is not bound to this host")
	ErrUnknown = errors.New("unknown secret")
)

// Minted values stay redactable this long after they were handed out, which
// outlives every token lifetime the minters use.
const mintedMemory = 2 * time.Hour

type Spec struct {
	Name         string            `json:"name" yaml:"name"`
	Kind         string            `json:"kind" yaml:"kind"`
	Value        string            `json:"value,omitempty" yaml:"value,omitempty"`
	File         string            `json:"file,omitempty" yaml:"file,omitempty"`
	Env          string            `json:"env,omitempty" yaml:"env,omitempty"`
	Hosts        []string          `json:"hosts" yaml:"hosts"`
	GitHubApp    *GitHubAppSpec    `json:"github_app,omitempty" yaml:"github_app,omitempty"`
	TokenRequest *TokenRequestSpec `json:"k8s_tokenrequest,omitempty" yaml:"k8s_tokenrequest,omitempty"`
}

type GitHubAppSpec struct {
	AppID          string            `json:"app_id" yaml:"app_id"`
	KeyFile        string            `json:"key_file" yaml:"key_file"`
	InstallationID int64             `json:"installation_id,omitempty" yaml:"installation_id,omitempty"`
	APIBase        string            `json:"api_base,omitempty" yaml:"api_base,omitempty"`
	Permissions    map[string]string `json:"permissions,omitempty" yaml:"permissions,omitempty"`
}

type TokenRequestSpec struct {
	APIServer      string `json:"api_server" yaml:"api_server"`
	CAFile         string `json:"ca_file,omitempty" yaml:"ca_file,omitempty"`
	BearerFile     string `json:"bearer_file" yaml:"bearer_file"`
	Namespace      string `json:"namespace" yaml:"namespace"`
	ServiceAccount string `json:"service_account" yaml:"service_account"`
	TTL            string `json:"ttl,omitempty" yaml:"ttl,omitempty"`
}

// Scope carries what a minter needs to narrow its credential.
type Scope struct{ Repo string }

type secret struct {
	hosts  map[string]bool
	hostLs []string
	value  string                                                 // static, file, env
	mint   func(ctx context.Context, scope Scope) (string, error) // github_app, k8s_tokenrequest
	app    *githubapp.App                                         // github_app
}

type Store struct {
	secrets map[string]*secret
	static  []string // values to redact, including standing minting credentials

	mu     sync.Mutex
	minted map[string]time.Time
	now    func() time.Time
}

// NewStore validates specs and loads their values. Errors name the secret,
// never its value.
func NewStore(specs []Spec, httpClient *http.Client) (*Store, error) {
	s := &Store{secrets: map[string]*secret{}, minted: map[string]time.Time{}, now: time.Now}
	for _, sp := range specs {
		if sp.Name == "" {
			return nil, errors.New("secrets: a secret has no name")
		}
		if _, dup := s.secrets[sp.Name]; dup {
			return nil, fmt.Errorf("secrets: %s is defined twice", sp.Name)
		}
		sec, err := s.build(sp, httpClient)
		if err != nil {
			return nil, fmt.Errorf("secrets: %s: %w", sp.Name, err)
		}
		s.secrets[sp.Name] = sec
	}
	return s, nil
}

func (s *Store) build(sp Spec, httpClient *http.Client) (*secret, error) {
	if len(sp.Hosts) == 0 {
		return nil, errors.New("hosts is required: list the upstream hosts this secret may be sent to")
	}
	sec := &secret{hosts: map[string]bool{}}
	for _, h := range sp.Hosts {
		n, err := specHost(h)
		if err != nil {
			return nil, err
		}
		sec.hosts[n] = true
		sec.hostLs = append(sec.hostLs, n)
	}
	if (sp.GitHubApp != nil && sp.Kind != "github_app") || (sp.TokenRequest != nil && sp.Kind != "k8s_tokenrequest") {
		return nil, fmt.Errorf("kind %q does not take that block", sp.Kind)
	}
	switch sp.Kind {
	case "static":
		sec.value = sp.Value
	case "file":
		v, err := readTrimmed(sp.File)
		if err != nil {
			return nil, err
		}
		sec.value = v
	case "env":
		sec.value = strings.TrimSpace(os.Getenv(sp.Env))
		if sp.Env == "" || sec.value == "" {
			return nil, fmt.Errorf("environment variable %q is empty", sp.Env)
		}
	case "github_app":
		app, mint, err := githubAppMinter(sp.GitHubApp, httpClient)
		if err != nil {
			return nil, err
		}
		sec.mint, sec.app = mint, app
	case "k8s_tokenrequest":
		mint, bearer, err := tokenRequestMinter(sp.TokenRequest, httpClient)
		if err != nil {
			return nil, err
		}
		s.static = append(s.static, bearer)
		sec.mint = mint
	default:
		return nil, fmt.Errorf("unknown kind %q (static, file, env, github_app, k8s_tokenrequest)", sp.Kind)
	}
	if sec.mint == nil {
		if sec.value == "" {
			return nil, errors.New("value is empty")
		}
		s.static = append(s.static, sec.value)
	}
	return sec, nil
}

func githubAppMinter(g *GitHubAppSpec, httpClient *http.Client) (*githubapp.App, func(context.Context, Scope) (string, error), error) {
	if g == nil || g.AppID == "" || g.KeyFile == "" {
		return nil, nil, errors.New("github_app needs app_id and key_file")
	}
	pemBytes, err := os.ReadFile(g.KeyFile)
	if err != nil {
		return nil, nil, err
	}
	key, err := githubapp.ParseKey(pemBytes)
	if err != nil {
		return nil, nil, err
	}
	app := &githubapp.App{AppID: g.AppID, Key: key, InstallationID: g.InstallationID, APIBase: g.APIBase, Permissions: g.Permissions, HTTP: httpClient}
	return app, func(ctx context.Context, scope Scope) (string, error) {
		if scope.Repo == "" {
			return "", errors.New("github_app needs a repo scope")
		}
		return app.Token(ctx, scope.Repo)
	}, nil
}

// GitHubApp is the App of the first github_app secret, by name, or nil.
func (s *Store) GitHubApp() *githubapp.App {
	for _, name := range slices.Sorted(maps.Keys(s.secrets)) {
		if a := s.secrets[name].app; a != nil {
			return a
		}
	}
	return nil
}

func tokenRequestMinter(k *TokenRequestSpec, httpClient *http.Client) (func(context.Context, Scope) (string, error), string, error) {
	if k == nil || k.APIServer == "" || k.BearerFile == "" || k.Namespace == "" || k.ServiceAccount == "" {
		return nil, "", errors.New("k8s_tokenrequest needs api_server, bearer_file, namespace and service_account")
	}
	var ttl time.Duration
	if k.TTL != "" {
		d, err := time.ParseDuration(k.TTL)
		if err != nil {
			return nil, "", fmt.Errorf("ttl: %w", err)
		}
		ttl = d
	}
	bearer, err := readTrimmed(k.BearerFile)
	if err != nil {
		return nil, "", err
	}
	client := httpClient
	if k.CAFile != "" {
		caPEM, err := os.ReadFile(k.CAFile)
		if err != nil {
			return nil, "", err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, "", fmt.Errorf("%s holds no certificates", k.CAFile)
		}
		// The cluster's own CA only: a DOKS endpoint is not in the system pool.
		client = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
			Proxy:           nil,
		}}
	}
	src := &k8s.TokenSource{APIServer: k.APIServer, Bearer: bearer, Namespace: k.Namespace, ServiceAccount: k.ServiceAccount, TTL: ttl, HTTP: client}
	return func(ctx context.Context, _ Scope) (string, error) { return src.Token(ctx) }, bearer, nil
}

func readTrimmed(path string) (string, error) {
	if path == "" {
		return "", errors.New("file is required")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(string(b))
	if v == "" {
		return "", fmt.Errorf("%s is empty", path)
	}
	return v, nil
}

// specHost validates a configured host: an exact name or IP, no wildcard,
// port or path.
func specHost(h string) (string, error) {
	n := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
	if ip := net.ParseIP(n); ip != nil {
		return ip.String(), nil
	}
	if n == "" || strings.ContainsAny(n, "*:/[] \t\\@?#") {
		return "", fmt.Errorf("host %q must be an exact host name or IP (no wildcard, port or path)", h)
	}
	return n, nil
}

// requestHost normalises the host a request is going to: lowercase, no port,
// brackets or trailing dot.
func requestHost(h string) string {
	h = strings.TrimSpace(h)
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	h = strings.TrimSuffix(strings.ToLower(strings.Trim(h, "[]")), ".")
	if ip := net.ParseIP(h); ip != nil {
		return ip.String()
	}
	return h
}

// Resolve returns the value of secret name for a request to host.
func (s *Store) Resolve(ctx context.Context, name, host string, scope Scope) (string, error) {
	sec, ok := s.secrets[name]
	if !ok {
		return "", fmt.Errorf("secrets: %q: %w", name, ErrUnknown)
	}
	if h := requestHost(host); h == "" || !sec.hosts[h] {
		return "", fmt.Errorf("secrets: %s for %q: %w", name, host, ErrUnbound)
	}
	if sec.mint == nil {
		return sec.value, nil
	}
	v, err := sec.mint(ctx, scope)
	if err != nil {
		return "", fmt.Errorf("secrets: %s: %w", name, err)
	}
	s.remember(v)
	return v, nil
}

func (s *Store) remember(v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.minted[v] = now
	for k, t := range s.minted {
		if now.Sub(t) > mintedMemory {
			delete(s.minted, k)
		}
	}
}

func (s *Store) Has(name string) bool { _, ok := s.secrets[name]; return ok }

func (s *Store) HostsFor(name string) []string {
	sec, ok := s.secrets[name]
	if !ok {
		return nil
	}
	return append([]string(nil), sec.hostLs...)
}

// Redact replaces every known secret value in text by exact match, longest
// first so a value containing another is not left half-visible. Exact values,
// never patterns: token formats change (GitHub's ghs_ format did in 2026).
func (s *Store) Redact(text string) string {
	if text == "" {
		return text
	}
	vals := append([]string(nil), s.static...)
	s.mu.Lock()
	now := s.now()
	for v, t := range s.minted {
		if now.Sub(t) <= mintedMemory {
			vals = append(vals, v)
		}
	}
	s.mu.Unlock()
	sort.Slice(vals, func(i, j int) bool { return len(vals[i]) > len(vals[j]) })
	for _, v := range vals {
		if v != "" {
			text = strings.ReplaceAll(text, v, "[redacted]")
		}
	}
	return text
}
