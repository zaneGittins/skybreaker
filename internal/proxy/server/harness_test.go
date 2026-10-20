package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zaneGittins/skybreaker/internal/proxy/approval"
	"github.com/zaneGittins/skybreaker/internal/proxy/audit"
	"github.com/zaneGittins/skybreaker/internal/proxy/ca"
	"github.com/zaneGittins/skybreaker/internal/proxy/config"
	"github.com/zaneGittins/skybreaker/internal/proxy/netguard"
	"github.com/zaneGittins/skybreaker/internal/proxy/secrets"
	"github.com/zaneGittins/skybreaker/internal/proxy/session"
)

// The test world: every upstream host name resolves to one local TLS server
// whose certificates come from a separate "public" CA, so the proxy's real
// upstream verification runs. "internal.test" resolves to a blocked address.
const testConfig = `{
  "projects": {
    "orbit": {
      "repo": "zaneGittins/orbit",
      "allow": [
        {"host": "tunnel.test", "mode": "tunnel"},
        {"host": "plain.test", "mode": "tunnel", "ports": [80, 443]},
        {"host": "internal.test", "mode": "mitm", "methods": ["GET"]},
        {"host": "open.test", "mode": "mitm", "methods": ["GET", "POST"]}
      ],
      "capabilities": ["github", "llm", "k8s"]
    },
    "bifract": {"repo": "zaneGittins/bifract", "capabilities": ["github"]}
  },
  "capabilities": {
    "github": [
      {"host": "github.test", "mode": "mitm",
       "inject": {"basic_auth": {"user": "x-access-token", "secret": "gh"}},
       "rules": [
         {"match": "GET /{repo}.git/info/refs", "action": "auto"},
         {"match": "POST /{repo}.git/git-upload-pack", "action": "auto"},
         {"match": "POST /{repo}.git/git-receive-pack", "action": "auto", "refs": ["refs/heads/agent/*"]}
       ]},
      {"host": "api.github.test", "mode": "mitm", "inject": {"bearer": "gh"},
       "rules": [
         {"match": "GET /repos/{repo}/**", "action": "auto"},
         {"match": "PUT /repos/{repo}/pulls/*/merge", "action": "ask"},
         {"match": "POST /graphql", "action": "auto", "graphql": {"mutations": ["createPullRequest", "addComment"], "other": "ask"}},
         {"match": "DELETE /**", "action": "deny"}
       ]}
    ],
    "llm": [
      {"host": "llm.test", "mode": "mitm", "inject": {"header": {"name": "x-api-key", "secret": "llm"}},
       "rules": [{"match": "POST /v1/messages", "action": "auto"}]},
      {"host": "unbound.test", "mode": "mitm", "inject": {"bearer": "llm"},
       "rules": [{"match": "GET /**", "action": "auto"}]}
    ],
    "k8s": [
      {"route": "/local", "upstream": "http://local.test", "allow_private": ["127.0.0.1"],
       "rules": [{"match": "POST /v1/**", "action": "auto"}, {"match": "GET /models", "action": "auto"}]},
      {"route": "/k8s/prod", "upstream": "https://kube.test", "ca_file": "CLUSTER_CA", "inject": {"bearer": "kube"},
       "k8s": {"auto": ["get", "list", "watch"], "ask": ["create", "patch", "delete"], "deny": ["secrets", "pods/exec", "ns:kube-system"]}}
    ]
  }
}`

// fakeSecrets binds each secret to hosts, like the real store.
type fakeSecrets struct{}

var secretHosts = map[string][]string{
	"gh":   {"github.test", "api.github.test"},
	"llm":  {"llm.test"}, // deliberately not unbound.test
	"kube": {"kube.test"},
}
var secretValues = map[string]string{"gh": "ghs_SECRET", "llm": "sk-ant-SECRET", "kube": "kube-SECRET"}

func (fakeSecrets) Resolve(_ context.Context, name, host string, scope secrets.Scope) (string, error) {
	hs, ok := secretHosts[name]
	if !ok {
		return "", secrets.ErrUnknown
	}
	for _, h := range hs {
		if h == host {
			return secretValues[name], nil
		}
	}
	return "", secrets.ErrUnbound
}

func (fakeSecrets) Redact(s string) string {
	for _, v := range secretValues {
		s = strings.ReplaceAll(s, v, "[redacted]")
	}
	return s
}

// upstreamHit is what the fake upstream saw.
type upstreamHit struct {
	Host   string
	Method string
	Path   string // escaped path + query
	Header http.Header
	Body   []byte
}

type world struct {
	t         *testing.T
	srv       *Server
	proxy     *httptest.Server
	upstream  *httptest.Server // TLS, for port 443
	plain     *httptest.Server // plain HTTP, for port 80
	brokerCA  *ca.Authority
	publicCA  *ca.Authority
	clusterCA *ca.Authority // signs kube.test only, trusted via the route's ca_file
	sessions  *session.Registry
	approvals *approval.Store
	auditBuf  *syncBuffer
	token     string // orbit's session token

	mu   sync.Mutex
	hits []upstreamHit
	from string // the sandbox network clients connect from

	// handlers serve a host in place of the echoing upstream.
	handlers map[string]http.Handler
}

func (w *world) setFrom(project string) {
	w.mu.Lock()
	w.from = project
	w.mu.Unlock()
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

type mapResolver map[string]string

func (m mapResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if ip, ok := m[host]; ok {
		return []netip.Addr{netip.MustParseAddr(ip)}, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

// newWorld's clients all connect from loopback; setFrom picks which
// project's sandbox network that counts as ("" for none).
func newWorld(t *testing.T) *world { return newWorldWith(t, testConfig, nil) }

// newWorldWith uses another policy, and handlers in place of the upstream
// for some hosts.
func newWorldWith(t *testing.T, policy string, handlers map[string]http.Handler) *world {
	t.Helper()
	w := &world{t: t, auditBuf: &syncBuffer{}, from: "orbit", handlers: handlers}

	var err error
	if w.publicCA, err = ca.New(); err != nil {
		t.Fatal(err)
	}
	if w.brokerCA, err = ca.New(); err != nil {
		t.Fatal(err)
	}
	if w.clusterCA, err = ca.New(); err != nil {
		t.Fatal(err)
	}

	w.upstream = httptest.NewUnstartedServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if h, ok := w.handlers[strings.Split(r.Host, ":")[0]]; ok {
			h.ServeHTTP(rw, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		path := r.URL.EscapedPath()
		if r.URL.RawQuery != "" {
			path += "?" + r.URL.RawQuery
		}
		w.mu.Lock()
		w.hits = append(w.hits, upstreamHit{Host: r.Host, Method: r.Method, Path: path, Header: r.Header.Clone(), Body: body})
		w.mu.Unlock()
		rw.Header().Set("X-Upstream", r.Host)
		if r.URL.Query().Get("watch") == "stream" {
			// A watch: events until the client goes away.
			for range 200 {
				io.WriteString(rw, "event\n")
				rw.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
					return
				case <-time.After(50 * time.Millisecond):
				}
			}
			return
		}
		io.WriteString(rw, "hello from "+r.Host)
	}))
	w.upstream.TLS = &tls.Config{GetCertificate: func(h *tls.ClientHelloInfo) (*tls.Certificate, error) {
		if h.ServerName == "kube.test" {
			return w.clusterCA.Leaf(h.ServerName)
		}
		return w.publicCA.Leaf(h.ServerName)
	}}
	w.upstream.StartTLS()
	t.Cleanup(w.upstream.Close)
	up := netip.MustParseAddrPort(w.upstream.Listener.Addr().String())
	w.plain = httptest.NewServer(w.upstream.Config.Handler)
	t.Cleanup(w.plain.Close)
	plainPort := netip.MustParseAddrPort(w.plain.Listener.Addr().String())

	tmp := t.TempDir()
	caPath := filepath.Join(tmp, "cluster-ca.crt")
	os.WriteFile(caPath, w.clusterCA.CertPEM(), 0o644)
	cfgPath := filepath.Join(tmp, "proxy.json")
	os.WriteFile(cfgPath, []byte(strings.Replace(policy, "CLUSTER_CA", caPath, 1)), 0o600)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	res := mapResolver{"internal.test": "10.0.0.5"}
	for _, h := range []string{"tunnel.test", "plain.test", "open.test", "github.test", "api.github.test", "llm.test", "unbound.test", "kube.test", "local.test"} {
		res[h] = "127.0.0.1"
	}
	guard, err := netguard.New(nil, []string{"127.0.0.1/32"})
	if err != nil {
		t.Fatal(err)
	}
	guard = guard.WithResolver(res)

	w.sessions, _ = session.NewRegistry("", nil)
	w.token, _, _ = w.sessions.Mint("orbit", time.Hour)
	w.approvals = approval.New(approval.Options{})

	w.srv, err = New(Deps{
		Config: cfg,
		Sandbox: func(string) (string, bool) {
			w.mu.Lock()
			defer w.mu.Unlock()
			return w.from, w.from != ""
		},
		Sessions:      w.sessions,
		Secrets:       fakeSecrets{},
		CA:            w.brokerCA,
		Approvals:     w.approvals,
		Audit:         audit.New(w.auditBuf),
		Guard:         guard,
		UpstreamRoots: w.publicCA.Pool(),
		Hold:          50 * time.Millisecond,
		// Every upstream host is served by the local test servers.
		portMap:   map[int]int{443: int(up.Port()), 80: int(plainPort.Port())},
		reapEvery: 20 * time.Millisecond,
		githubAPI: map[string]bool{"api.github.test": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.srv.Close)
	w.proxy = httptest.NewServer(w.srv)
	t.Cleanup(w.proxy.Close)
	return w
}

// client returns an HTTP client that uses the proxy as project:token and
// trusts both CAs (the broker's for mitm, the public one for tunnels).
func (w *world) client(project, token string) *http.Client {
	pu, _ := url.Parse(w.proxy.URL)
	if project != "" {
		pu.User = url.UserPassword(project, token)
	}
	pool := w.brokerCA.Pool()
	pool.AppendCertsFromPEM(w.publicCA.CertPEM())
	tr := &http.Transport{Proxy: http.ProxyURL(pu), TLSClientConfig: &tls.Config{RootCAs: pool}}
	return &http.Client{Transport: tr, Timeout: 5 * time.Second}
}

func (w *world) orbit() *http.Client { return w.client("orbit", w.token) }

func (w *world) hitsFor(host string) []upstreamHit {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []upstreamHit
	for _, h := range w.hits {
		if strings.HasPrefix(h.Host, host) {
			out = append(out, h)
		}
	}
	return out
}

// do sends a request and returns status, body and headers.
func do(t *testing.T, c *http.Client, method, u string, body string, hdr map[string]string) (int, string, http.Header) {
	t.Helper()
	req, err := http.NewRequest(method, u, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, err.Error(), nil
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}
