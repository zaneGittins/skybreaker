package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zaneGittins/skybreaker/internal/proxy/policy"
)

func TestParse(t *testing.T) {
	cases := []struct {
		method, path, query string
		want                Attributes
	}{
		{"GET", "/api/v1/namespaces/apps/pods", "", Attributes{Verb: "list", APIVersion: "v1", Namespace: "apps", Resource: "pods"}},
		{"GET", "/api/v1/namespaces/apps/pods/web-1", "", Attributes{Verb: "get", APIVersion: "v1", Namespace: "apps", Resource: "pods", Name: "web-1"}},
		{"GET", "/api/v1/namespaces/apps/pods", "watch=true", Attributes{Verb: "watch", APIVersion: "v1", Namespace: "apps", Resource: "pods"}},
		{"GET", "/api/v1/namespaces/apps/pods", "watch=1", Attributes{Verb: "watch", APIVersion: "v1", Namespace: "apps", Resource: "pods"}},
		{"GET", "/api/v1/namespaces/apps/pods", "watch=false", Attributes{Verb: "list", APIVersion: "v1", Namespace: "apps", Resource: "pods"}},
		{"GET", "/api/v1/watch/namespaces/apps/pods", "", Attributes{Verb: "watch", APIVersion: "v1", Namespace: "apps", Resource: "pods"}},
		{"POST", "/apis/apps/v1/namespaces/apps/deployments", "", Attributes{Verb: "create", APIGroup: "apps", APIVersion: "v1", Namespace: "apps", Resource: "deployments"}},
		{"PUT", "/apis/apps/v1/namespaces/apps/deployments/web", "", Attributes{Verb: "update", APIGroup: "apps", APIVersion: "v1", Namespace: "apps", Resource: "deployments", Name: "web"}},
		{"PATCH", "/apis/apps/v1/namespaces/apps/deployments/web/scale", "", Attributes{Verb: "patch", APIGroup: "apps", APIVersion: "v1", Namespace: "apps", Resource: "deployments", Name: "web", Subresource: "scale"}},
		{"DELETE", "/api/v1/namespaces/apps/pods/web-1", "", Attributes{Verb: "delete", APIVersion: "v1", Namespace: "apps", Resource: "pods", Name: "web-1"}},
		{"DELETE", "/api/v1/namespaces/apps/pods", "", Attributes{Verb: "deletecollection", APIVersion: "v1", Namespace: "apps", Resource: "pods"}},
		{"POST", "/api/v1/namespaces/apps/pods/web-1/exec", "command=sh", Attributes{Verb: "create", APIVersion: "v1", Namespace: "apps", Resource: "pods", Name: "web-1", Subresource: "exec"}},
		{"GET", "/api/v1/namespaces/apps/pods/web-1/exec", "", Attributes{Verb: "get", APIVersion: "v1", Namespace: "apps", Resource: "pods", Name: "web-1", Subresource: "exec"}},
		{"GET", "/api/v1/namespaces/apps/pods/web-1/proxy/some/path", "", Attributes{Verb: "get", APIVersion: "v1", Namespace: "apps", Resource: "pods", Name: "web-1", Subresource: "proxy"}},
		// The namespace object itself.
		{"GET", "/api/v1/namespaces/kube-system", "", Attributes{Verb: "get", APIVersion: "v1", Namespace: "kube-system", Resource: "namespaces", Name: "kube-system"}},
		{"GET", "/api/v1/namespaces", "", Attributes{Verb: "list", APIVersion: "v1", Resource: "namespaces"}},
		{"PUT", "/api/v1/namespaces/apps/finalize", "", Attributes{Verb: "update", APIVersion: "v1", Namespace: "apps", Resource: "namespaces", Name: "apps", Subresource: "finalize"}},
		{"GET", "/api/v1/namespaces/apps/status", "", Attributes{Verb: "get", APIVersion: "v1", Namespace: "apps", Resource: "namespaces", Name: "apps", Subresource: "status"}},
		// Cluster-scoped.
		{"GET", "/api/v1/nodes/n1", "", Attributes{Verb: "get", APIVersion: "v1", Resource: "nodes", Name: "n1"}},
		{"GET", "/apis/rbac.authorization.k8s.io/v1/clusterroles", "", Attributes{Verb: "list", APIGroup: "rbac.authorization.k8s.io", APIVersion: "v1", Resource: "clusterroles"}},
		{"HEAD", "/api/v1/nodes", "", Attributes{Verb: "list", APIVersion: "v1", Resource: "nodes"}},
		// Discovery and other non-resource paths.
		{"GET", "/api", "", Attributes{Verb: "get", NonResource: true}},
		{"GET", "/api/v1", "", Attributes{Verb: "get", NonResource: true}},
		{"GET", "/apis", "", Attributes{Verb: "get", NonResource: true}},
		{"GET", "/apis/apps", "", Attributes{Verb: "get", NonResource: true}},
		{"GET", "/apis/apps/v1", "", Attributes{Verb: "get", NonResource: true}},
		{"GET", "/version", "", Attributes{Verb: "get", NonResource: true}},
		{"GET", "/openapi/v3/apis/apps/v1", "", Attributes{Verb: "get", NonResource: true}},
		{"POST", "/version", "", Attributes{Verb: "post", NonResource: true}},
		{"GET", "/api/v1/nodes/", "", Attributes{Verb: "list", APIVersion: "v1", Resource: "nodes"}}, // one trailing slash is trimmed
	}
	for _, c := range cases {
		q, _ := url.ParseQuery(c.query)
		got, err := Parse(c.method, c.path, q)
		if err != nil {
			t.Errorf("%s %s: %v", c.method, c.path, err)
			continue
		}
		c.want.Path = c.path
		if got != c.want {
			t.Errorf("%s %s?%s:\n got %+v\nwant %+v", c.method, c.path, c.query, got, c.want)
		}
	}
}

func TestParseRejects(t *testing.T) {
	cases := []struct{ method, path string }{
		{"GET", "/api/v1/namespaces/kube-system/../default/pods"},
		{"GET", "/api/v1/namespaces/apps/./pods"},
		{"GET", "/api/v1//namespaces/apps/pods"},
		{"GET", "/api/v1/namespaces/apps/pods//"},
		{"GET", "api/v1/pods"},
		{"GET", ""},
		{"GET", "/api/v1/namespaces/apps/pods/a/exec/extra"},
		{"GET", "/api/v1/namespaces//pods"},
		{"OPTIONS", "/api/v1/pods"},
		{"CONNECT", "/api/v1/pods"},
		{"GET", "/api/v1/namespaces/apps\\x/pods"},
		{"GET", "/api/v1/namespaces/apps\x00/pods"},
		{"GET", "/api/v1/watch"},
	}
	for _, c := range cases {
		if got, err := Parse(c.method, c.path, nil); err == nil {
			t.Errorf("%s %q accepted: %+v", c.method, c.path, got)
		}
	}
}

var prod = Policy{
	Auto: []string{"get", "list", "watch"},
	Ask:  []string{"create", "apply", "patch", "delete"},
	Deny: []string{"secrets", "pods/exec", "pods/portforward", "ns:kube-system"},
}

func TestPolicyValidate(t *testing.T) {
	if err := prod.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := []Policy{
		{Auto: []string{"gte"}},
		{Ask: []string{"secrets"}},
		{Deny: []string{"ns:"}},
		{Deny: []string{"pods/exec/x"}},
		{Deny: []string{""}},
		{Auto: []string{"get"}, Deny: []string{"Secrets"}},
		{Deny: []string{"/exec"}},
	}
	for _, p := range bad {
		if err := p.Validate(); err == nil {
			t.Errorf("Validate(%+v) = nil, want error", p)
		}
	}
	if err := (Policy{Auto: []string{"*"}}).Validate(); err != nil {
		t.Errorf("* rejected: %v", err)
	}
}

func TestPolicyDecide(t *testing.T) {
	cases := []struct {
		method, path, query string
		want                policy.Action
	}{
		{"GET", "/api/v1/namespaces/apps/pods", "", policy.Auto},
		{"GET", "/api/v1/namespaces/apps/pods", "watch=true", policy.Auto},
		{"GET", "/apis", "", policy.Auto}, // discovery follows "get"
		{"GET", "/version", "", policy.Auto},
		{"POST", "/apis/apps/v1/namespaces/apps/deployments", "", policy.Ask},
		{"PATCH", "/apis/apps/v1/namespaces/apps/deployments/web", "", policy.Ask},
		{"DELETE", "/api/v1/namespaces/apps/pods/web-1", "", policy.Ask},
		{"PUT", "/apis/apps/v1/namespaces/apps/deployments/web", "", policy.Deny}, // update not listed
		{"DELETE", "/api/v1/namespaces/apps/pods", "", policy.Deny},               // deletecollection not listed
		{"GET", "/api/v1/namespaces/apps/secrets", "", policy.Deny},
		{"GET", "/api/v1/namespaces/apps/secrets/db", "", policy.Deny},
		{"GET", "/api/v1/namespaces/apps/secrets", "watch=1", policy.Deny},
		{"GET", "/api/v1/secrets", "", policy.Deny}, // cluster-wide list
		{"POST", "/api/v1/namespaces/apps/pods/web-1/exec", "", policy.Deny},
		{"GET", "/api/v1/namespaces/apps/pods/web-1/exec", "", policy.Deny},
		{"POST", "/api/v1/namespaces/apps/pods/web-1/portforward", "", policy.Deny},
		{"GET", "/api/v1/namespaces/kube-system/pods", "", policy.Deny},
		{"GET", "/api/v1/namespaces/kube-system", "", policy.Deny},
		{"GET", "/api/v1/namespaces/apps/pods/web-1/log", "", policy.Auto},
		{"POST", "/version", "", policy.Deny},
	}
	for _, c := range cases {
		q, _ := url.ParseQuery(c.query)
		a, err := Parse(c.method, c.path, q)
		if err != nil {
			t.Fatalf("%s %s: %v", c.method, c.path, err)
		}
		got, reason := prod.Decide(a)
		if got != c.want {
			t.Errorf("%s %s?%s: %q (%s), want %q", c.method, c.path, c.query, got, reason, c.want)
		}
		if reason == "" {
			t.Errorf("%s %s: empty reason", c.method, c.path)
		}
	}
}

func TestPolicyDenyVerb(t *testing.T) {
	p := Policy{Auto: []string{"*"}, Deny: []string{"delete"}}
	a, _ := Parse("DELETE", "/api/v1/namespaces/apps/pods/x", nil)
	if got, _ := p.Decide(a); got != policy.Deny {
		t.Errorf("deny verb not applied: %s", got)
	}
	a, _ = Parse("PUT", "/api/v1/namespaces/apps/pods/x", nil)
	if got, _ := p.Decide(a); got != policy.Auto {
		t.Errorf("* verb not applied: %s", got)
	}
}

type tokenServer struct {
	calls atomic.Int32
	mu    sync.Mutex
	last  map[string]any
	ttl   time.Duration
	now   func() time.Time
}

func (s *tokenServer) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := s.calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/namespaces/apps/serviceaccounts/skybreaker-agent/token" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer minting-cred" {
			t.Errorf("Authorization = %q", got)
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		s.last = body
		s.mu.Unlock()
		exp := s.now().Add(s.ttl).UTC().Format(time.RFC3339)
		fmt.Fprintf(w, `{"status":{"token":"tok-%d","expirationTimestamp":%q}}`, n, exp)
	}
}

func TestTokenSource(t *testing.T) {
	var mu sync.Mutex
	clock := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	advance := func(d time.Duration) { mu.Lock(); clock = clock.Add(d); mu.Unlock() }

	ts := &tokenServer{ttl: 15 * time.Minute, now: now}
	srv := httptest.NewServer(ts.handler(t))
	defer srv.Close()

	src := &TokenSource{APIServer: srv.URL, Bearer: "minting-cred", Namespace: "apps", ServiceAccount: "skybreaker-agent", TTL: 15 * time.Minute, HTTP: srv.Client(), Now: now}
	ctx := context.Background()
	tok, err := src.Token(ctx)
	if err != nil || tok != "tok-1" {
		t.Fatalf("Token = %q, %v", tok, err)
	}
	ts.mu.Lock()
	spec, _ := ts.last["spec"].(map[string]any)
	if ts.last["kind"] != "TokenRequest" || ts.last["apiVersion"] != "authentication.k8s.io/v1" || spec["expirationSeconds"] != float64(900) {
		t.Errorf("request body = %v", ts.last)
	}
	ts.mu.Unlock()

	advance(11 * time.Minute) // under 80% of 15m
	if tok, _ := src.Token(ctx); tok != "tok-1" {
		t.Errorf("cached token not reused: %q", tok)
	}
	advance(2 * time.Minute) // past 12m
	if tok, _ := src.Token(ctx); tok != "tok-2" {
		t.Errorf("token not refreshed: %q", tok)
	}

	// Concurrent callers share one fetch.
	advance(time.Hour)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); src.Token(ctx) }()
	}
	wg.Wait()
	if n := ts.calls.Load(); n != 3 {
		t.Errorf("%d token requests, want 3", n)
	}
}

func TestTokenSourceClampsTTL(t *testing.T) {
	now := time.Now
	ts := &tokenServer{ttl: 10 * time.Minute, now: now}
	srv := httptest.NewServer(ts.handler(t))
	defer srv.Close()
	src := &TokenSource{APIServer: srv.URL, Bearer: "minting-cred", Namespace: "apps", ServiceAccount: "skybreaker-agent", TTL: time.Minute, HTTP: srv.Client()}
	if _, err := src.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	spec := ts.last["spec"].(map[string]any)
	if spec["expirationSeconds"] != float64(600) {
		t.Errorf("expirationSeconds = %v, want 600", spec["expirationSeconds"])
	}
}

func TestTokenSourceErrors(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"status": func(w http.ResponseWriter, r *http.Request) { http.Error(w, "forbidden", http.StatusForbidden) },
		"empty":  func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"status":{}}`)) },
		"junk":   func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`not json`)) },
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(h)
			defer srv.Close()
			src := &TokenSource{APIServer: srv.URL, Bearer: "x", Namespace: "apps", ServiceAccount: "sa", HTTP: srv.Client()}
			tok, err := src.Token(context.Background())
			if err == nil {
				t.Fatalf("got token %q, want error", tok)
			}
			if strings.Contains(err.Error(), "x") && strings.Contains(err.Error(), "Bearer") {
				t.Errorf("error leaks the credential: %v", err)
			}
		})
	}
	bad := &TokenSource{APIServer: "https://k8s", Namespace: "apps/../kube-system", ServiceAccount: "sa", Bearer: "x"}
	if _, err := bad.Token(context.Background()); err == nil {
		t.Error("accepted a namespace with a slash")
	}
}

func TestWatchValues(t *testing.T) {
	// The apiserver treats any watch value other than false/0 as a watch.
	for v, want := range map[string]string{"true": "watch", "1": "watch", "yes": "watch", "TRUE": "watch", "false": "list", "0": "list", "": "list"} {
		q := url.Values{}
		if v != "" {
			q.Set("watch", v)
		}
		a, err := Parse("GET", "/api/v1/namespaces/apps/pods", q)
		if err != nil || a.Verb != want {
			t.Errorf("watch=%q: verb %q, want %q (%v)", v, a.Verb, want, err)
		}
	}
}

func TestNamespaceDenyCoversClusterWideReads(t *testing.T) {
	// GET /api/v1/pods lists pods in every namespace, kube-system included.
	// With an ns: deny, a namespaced resource must name its namespace.
	p := Policy{Auto: []string{"get", "list", "watch"}, Deny: []string{"ns:kube-system"}}
	// A custom resource named like a built-in cluster-scoped one is still
	// namespaced: the group tells them apart.
	for _, path := range []string{"/api/v1/pods", "/api/v1/watch/pods", "/api/v1/configmaps", "/apis/apps/v1/deployments", "/apis/example.com/v1/widgets", "/apis/evil.example/v1/nodes", "/apis/evil.example/v1/namespaces", "/apis/rbac.example/v1/clusterroles"} {
		a, err := Parse("GET", path, url.Values{})
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if act, _ := p.Decide(a); act != policy.Deny {
			t.Errorf("%s: %s, want deny", path, act)
		}
	}
	// Cluster-scoped resources and discovery still work, and so does a
	// namespace that isn't denied.
	for _, path := range []string{"/api/v1/namespaces", "/api/v1/nodes", "/apis/apiextensions.k8s.io/v1/customresourcedefinitions", "/api", "/apis", "/version", "/api/v1/namespaces/apps/pods"} {
		a, err := Parse("GET", path, url.Values{})
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if act, why := p.Decide(a); act != policy.Auto {
			t.Errorf("%s: %s (%s), want auto", path, act, why)
		}
	}
	// Without an ns: deny, cluster-wide lists are left to the verb rules.
	open := Policy{Auto: []string{"list"}}
	a, _ := Parse("GET", "/api/v1/pods", url.Values{})
	if act, _ := open.Decide(a); act != policy.Auto {
		t.Errorf("no ns deny: %s, want auto", act)
	}
}

// Proxy subresources reach a kubelet, a pod or a service with the injected
// credential, past every rule that looks at the outer resource. They are
// denied unless a route lists the resource under proxy.
func TestProxySubresources(t *testing.T) {
	paths := []string{
		"/api/v1/nodes/worker/proxy/pods",
		"/api/v1/nodes/worker/proxy",
		"/api/v1/namespaces/apps/pods/web/proxy/metrics",
		"/api/v1/namespaces/apps/services/web:80/proxy/admin",
		"/api/v1/namespaces/apps/services/https:web:443/proxy/",
	}
	open := Policy{Auto: []string{"*"}}
	for _, p := range paths {
		a, err := Parse("GET", p, url.Values{})
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if got, reason := open.Decide(a); got != policy.Deny || !strings.Contains(reason, "proxy") {
			t.Errorf("%s: %q (%s), want deny", p, got, reason)
		}
	}
	svc := Policy{Auto: []string{"get"}, Deny: []string{"ns:kube-system"}, Proxy: []string{"services"}}
	if err := svc.Validate(); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]policy.Action{
		"/api/v1/namespaces/apps/services/web:80/proxy/healthz":        policy.Auto,
		"/api/v1/namespaces/apps/pods/web/proxy/metrics":               policy.Deny,
		"/api/v1/nodes/worker/proxy/pods":                              policy.Deny,
		"/api/v1/namespaces/kube-system/services/dns:53/proxy/metrics": policy.Deny,
	} {
		a, _ := Parse("GET", p, url.Values{})
		if got, reason := svc.Decide(a); got != want {
			t.Errorf("%s: %q (%s), want %q", p, got, reason, want)
		}
	}
	if err := (Policy{Proxy: []string{"secrets"}}).Validate(); err == nil {
		t.Error("proxy: [secrets] accepted")
	}
}
