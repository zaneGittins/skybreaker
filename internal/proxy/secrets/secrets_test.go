package secrets

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mustStore(t *testing.T, specs ...Spec) *Store {
	t.Helper()
	s, err := NewStore(specs, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestStaticFileEnv(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "do")
	os.WriteFile(f, []byte("  dop_v1_abc \n"), 0o600)
	t.Setenv("SKYBREAKER_TEST_ANTHROPIC", "sk-ant-xyz")
	s := mustStore(t,
		Spec{Name: "static", Kind: "static", Value: "v1", Hosts: []string{"example.com"}},
		Spec{Name: "do_token", Kind: "file", File: f, Hosts: []string{"api.digitalocean.com"}},
		Spec{Name: "anthropic", Kind: "env", Env: "SKYBREAKER_TEST_ANTHROPIC", Hosts: []string{"api.anthropic.com"}},
	)
	ctx := context.Background()
	for name, want := range map[string]string{"static": "v1", "do_token": "dop_v1_abc", "anthropic": "sk-ant-xyz"} {
		host := s.HostsFor(name)[0]
		got, err := s.Resolve(ctx, name, host, Scope{})
		if err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", name, got, err, want)
		}
	}
	if !s.Has("static") || s.Has("nope") {
		t.Error("Has")
	}
}

func TestResolveFailsClosedOffHost(t *testing.T) {
	s := mustStore(t, Spec{Name: "gh", Kind: "static", Value: "secret", Hosts: []string{"api.github.com"}})
	ctx := context.Background()
	for _, host := range []string{"echo.example.com", "api.github.com.evil.com", "github.com", "", "api.github.co", "evil.com/api.github.com"} {
		v, err := s.Resolve(ctx, "gh", host, Scope{})
		if !errors.Is(err, ErrUnbound) || v != "" {
			t.Errorf("host %q: %q %v, want ErrUnbound", host, v, err)
		}
	}
	if _, err := s.Resolve(ctx, "missing", "api.github.com", Scope{}); !errors.Is(err, ErrUnknown) {
		t.Errorf("unknown secret: %v", err)
	}
}

func TestResolveNormalisesHost(t *testing.T) {
	s := mustStore(t,
		Spec{Name: "gh", Kind: "static", Value: "secret", Hosts: []string{"API.GitHub.com."}},
		Spec{Name: "ip6", Kind: "static", Value: "s6", Hosts: []string{"2001:db8::1"}},
	)
	for _, host := range []string{"api.github.com", "API.GITHUB.COM", "api.github.com.", "api.github.com:443", "Api.Github.Com.:8443"} {
		if v, err := s.Resolve(context.Background(), "gh", host, Scope{}); err != nil || v != "secret" {
			t.Errorf("host %q: %q %v", host, v, err)
		}
	}
	for _, host := range []string{"2001:db8::1", "[2001:db8::1]:443", "[2001:db8::1]"} {
		if v, err := s.Resolve(context.Background(), "ip6", host, Scope{}); err != nil || v != "s6" {
			t.Errorf("host %q: %q %v", host, v, err)
		}
	}
}

func TestNewStoreRejectsBadSpecs(t *testing.T) {
	cases := map[string][]Spec{
		"no hosts":       {{Name: "a", Kind: "static", Value: "v"}},
		"empty host":     {{Name: "a", Kind: "static", Value: "v", Hosts: []string{""}}},
		"wildcard":       {{Name: "a", Kind: "static", Value: "v", Hosts: []string{"*.github.com"}}},
		"bare wildcard":  {{Name: "a", Kind: "static", Value: "v", Hosts: []string{"*"}}},
		"path in host":   {{Name: "a", Kind: "static", Value: "v", Hosts: []string{"github.com/x"}}},
		"port in host":   {{Name: "a", Kind: "static", Value: "v", Hosts: []string{"github.com:443"}}},
		"space in host":  {{Name: "a", Kind: "static", Value: "v", Hosts: []string{"git hub.com"}}},
		"no name":        {{Kind: "static", Value: "v", Hosts: []string{"x.com"}}},
		"duplicate":      {{Name: "a", Kind: "static", Value: "v", Hosts: []string{"x.com"}}, {Name: "a", Kind: "static", Value: "w", Hosts: []string{"x.com"}}},
		"bad kind":       {{Name: "a", Kind: "magic", Value: "v", Hosts: []string{"x.com"}}},
		"empty static":   {{Name: "a", Kind: "static", Hosts: []string{"x.com"}}},
		"missing file":   {{Name: "a", Kind: "file", File: "/nonexistent/skybreaker", Hosts: []string{"x.com"}}},
		"unset env":      {{Name: "a", Kind: "env", Env: "SKYBREAKER_TEST_DEFINITELY_UNSET", Hosts: []string{"x.com"}}},
		"app no key":     {{Name: "a", Kind: "github_app", Hosts: []string{"x.com"}, GitHubApp: &GitHubAppSpec{AppID: "1"}}},
		"app no spec":    {{Name: "a", Kind: "github_app", Hosts: []string{"x.com"}}},
		"k8s no spec":    {{Name: "a", Kind: "k8s_tokenrequest", Hosts: []string{"x.com"}}},
		"k8s bad ttl":    {{Name: "a", Kind: "k8s_tokenrequest", Hosts: []string{"x.com"}, TokenRequest: &TokenRequestSpec{APIServer: "https://k", BearerFile: "/dev/null", Namespace: "n", ServiceAccount: "s", TTL: "soon"}}},
		"k8s no server":  {{Name: "a", Kind: "k8s_tokenrequest", Hosts: []string{"x.com"}, TokenRequest: &TokenRequestSpec{Namespace: "n", ServiceAccount: "s"}}},
		"static and app": {{Name: "a", Kind: "static", Value: "v", Hosts: []string{"x.com"}, GitHubApp: &GitHubAppSpec{AppID: "1"}}},
	}
	for name, specs := range cases {
		if _, err := NewStore(specs, nil); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestErrorsNeverContainValues(t *testing.T) {
	_, err := NewStore([]Spec{
		{Name: "a", Kind: "static", Value: "sk-super-secret", Hosts: []string{"x.com"}},
		{Name: "a", Kind: "static", Value: "sk-super-secret", Hosts: []string{"x.com"}},
	}, nil)
	if err == nil || strings.Contains(err.Error(), "sk-super-secret") {
		t.Errorf("err %v", err)
	}
}

func TestRedact(t *testing.T) {
	s := mustStore(t,
		Spec{Name: "short", Kind: "static", Value: "abc123", Hosts: []string{"x.com"}},
		Spec{Name: "long", Kind: "static", Value: "abc123def456", Hosts: []string{"y.com"}},
	)
	got := s.Redact("token=abc123def456 and abc123 and abc12")
	want := "token=[redacted] and [redacted] and abc12"
	if got != want {
		t.Errorf("Redact = %q, want %q", got, want)
	}
	if s.Redact("") != "" {
		t.Error("empty")
	}
}

func writeKey(t *testing.T) string {
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	p := filepath.Join(t.TempDir(), "app.pem")
	os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}), 0o600)
	return p
}

func TestGitHubAppKind(t *testing.T) {
	var mints int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/app/installations/9/access_tokens" {
			http.NotFound(w, r)
			return
		}
		b, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(b), `"repositories":["orbit"]`) {
			t.Errorf("body %s", b)
		}
		mints++
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"token":"ghs_minted%d","expires_at":%q}`, mints, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	}))
	defer srv.Close()
	s, err := NewStore([]Spec{{
		Name: "github_app", Kind: "github_app", Hosts: []string{"github.com", "api.github.com"},
		GitHubApp: &GitHubAppSpec{AppID: "1", KeyFile: writeKey(t), InstallationID: 9, APIBase: srv.URL},
	}}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := s.Resolve(ctx, "github_app", "github.com", Scope{}); err == nil {
		t.Error("github_app resolved without a repo scope")
	}
	v, err := s.Resolve(ctx, "github_app", "github.com", Scope{Repo: "zaneGittins/orbit"})
	if err != nil || v != "ghs_minted1" {
		t.Fatalf("%q %v", v, err)
	}
	if _, err := s.Resolve(ctx, "github_app", "evil.com", Scope{Repo: "zaneGittins/orbit"}); !errors.Is(err, ErrUnbound) {
		t.Errorf("minted secret resolved off-host: %v", err)
	}
	if got := s.Redact("Authorization: Bearer ghs_minted1"); got != "Authorization: Bearer [redacted]" {
		t.Errorf("minted value not redacted: %q", got)
	}
}

func TestK8sTokenRequestKind(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v1/namespaces/apps/serviceaccounts/skybreaker-agent/token" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer minting-cred" {
			t.Errorf("Authorization %q", r.Header.Get("Authorization"))
		}
		var body struct {
			Spec struct {
				ExpirationSeconds int64 `json:"expirationSeconds"`
			} `json:"spec"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.Spec.ExpirationSeconds != 900 {
			t.Errorf("expirationSeconds %d", body.Spec.ExpirationSeconds)
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"status":{"token":"sa-token","expirationTimestamp":%q}}`, time.Now().Add(15*time.Minute).UTC().Format(time.RFC3339))
	}))
	defer srv.Close()
	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.crt")
	os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o644)
	bearer := filepath.Join(dir, "bearer")
	os.WriteFile(bearer, []byte("minting-cred\n"), 0o600)

	s, err := NewStore([]Spec{{
		Name: "k8s_prod", Kind: "k8s_tokenrequest", Hosts: []string{"k8s.example.com"},
		TokenRequest: &TokenRequestSpec{APIServer: srv.URL, CAFile: caFile, BearerFile: bearer, Namespace: "apps", ServiceAccount: "skybreaker-agent", TTL: "15m"},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.Resolve(context.Background(), "k8s_prod", "k8s.example.com", Scope{})
	if err != nil || v != "sa-token" {
		t.Fatalf("%q %v", v, err)
	}
	if got := s.Redact("sa-token"); got != "[redacted]" {
		t.Errorf("minted token not redacted: %q", got)
	}
	// The minting credential itself is never injectable, but is still redacted.
	if got := s.Redact("minting-cred"); got != "[redacted]" {
		t.Errorf("bearer not redacted: %q", got)
	}
}

func TestSpecJSONTags(t *testing.T) {
	var s Spec
	err := json.Unmarshal([]byte(`{"name":"k","kind":"k8s_tokenrequest","hosts":["h"],
		"github_app":{"app_id":"1","key_file":"/k","installation_id":5,"api_base":"https://x","permissions":{"contents":"write"}},
		"k8s_tokenrequest":{"api_server":"https://k","ca_file":"/ca","bearer_file":"/b","namespace":"n","service_account":"sa","ttl":"15m"}}`), &s)
	if err != nil {
		t.Fatal(err)
	}
	g, k := s.GitHubApp, s.TokenRequest
	if g.AppID != "1" || g.KeyFile != "/k" || g.InstallationID != 5 || g.APIBase != "https://x" || g.Permissions["contents"] != "write" {
		t.Errorf("github_app %+v", g)
	}
	if k.APIServer != "https://k" || k.CAFile != "/ca" || k.BearerFile != "/b" || k.Namespace != "n" || k.ServiceAccount != "sa" || k.TTL != "15m" {
		t.Errorf("k8s_tokenrequest %+v", k)
	}
}
