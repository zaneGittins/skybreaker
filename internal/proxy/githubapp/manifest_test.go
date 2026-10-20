package githubapp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestManifest(t *testing.T) {
	b, err := Manifest("Skybreaker 1a2b3c", "https://skybreaker.example.ts.net")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Name        string            `json:"name"`
		URL         string            `json:"url"`
		RedirectURL string            `json:"redirect_url"`
		Public      bool              `json:"public"`
		Perms       map[string]string `json:"default_permissions"`
		Hook        struct {
			Active bool `json:"active"`
		} `json:"hook_attributes"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m.RedirectURL != "https://skybreaker.example.ts.net/api/github-app/created" || m.Public || m.Hook.Active {
		t.Fatalf("%s", b)
	}
	if m.Perms["contents"] != "write" || m.Perms["pull_requests"] != "write" || m.Perms["issues"] != "write" || m.Perms["metadata"] != "read" ||
		m.Perms["actions"] != "read" || m.Perms["checks"] != "read" || m.Perms["statuses"] != "read" || len(m.Perms) != 7 {
		t.Fatalf("permissions %v", m.Perms)
	}
	if _, err := Manifest("x", "http://skybreaker.example"); err == nil {
		t.Fatal("a plain-http UI accepted")
	}
}

func TestNewAppURL(t *testing.T) {
	if u, _ := NewAppURL("", "s1"); u != "https://github.com/settings/apps/new?state=s1" {
		t.Fatal(u)
	}
	if u, _ := NewAppURL("acme-co", "s1"); u != "https://github.com/organizations/acme-co/settings/apps/new?state=s1" {
		t.Fatal(u)
	}
	for _, org := range []string{"../evil", "a/b", "-x", strings.Repeat("a", 40), "a b", "a?x=1"} {
		if _, err := NewAppURL(org, "s"); err == nil {
			t.Errorf("org %q accepted", org)
		}
	}
}

func TestConvert(t *testing.T) {
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	key := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/app-manifests/abc123/conversions" || r.Header.Get("Authorization") != "" {
			t.Errorf("%s %s auth %q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusCreated)
		out, _ := json.Marshal(map[string]any{"id": 42, "slug": "skybreaker-1a2b3c", "name": "Skybreaker 1a2b3c", "html_url": "https://github.com/apps/skybreaker-1a2b3c",
			"owner": map[string]any{"login": "zaneGittins"}, "pem": key, "client_secret": "cs", "webhook_secret": "ws"})
		w.Write(out)
	}))
	defer srv.Close()
	c, err := Convert(context.Background(), srv.Client(), srv.URL, "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if c.ID != "42" || c.Slug != "skybreaker-1a2b3c" || c.Owner != "zaneGittins" || c.PEM != key || c.HTMLURL != "https://github.com/apps/skybreaker-1a2b3c" {
		t.Fatalf("%+v", c)
	}
	if c.InstallURL() != "https://github.com/apps/skybreaker-1a2b3c/installations/new" {
		t.Fatal(c.InstallURL())
	}
	for _, code := range []string{"", "../x", "a/b", "a?b", strings.Repeat("a", 101)} {
		if _, err := Convert(context.Background(), srv.Client(), srv.URL, code); err == nil {
			t.Errorf("code %q accepted", code)
		}
	}
}

func TestConvertRefusesBadAnswers(t *testing.T) {
	for name, body := range map[string]string{
		"no key":   `{"id": 1, "slug": "s", "pem": ""}`,
		"bad key":  `{"id": 1, "slug": "s", "pem": "-----BEGIN RSA PRIVATE KEY-----\nAAAA\n-----END RSA PRIVATE KEY-----\n"}`,
		"no id":    `{"slug": "s", "pem": "x"}`,
		"bad slug": `{"id": 1, "slug": "../../evil", "pem": "x"}`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, body)
		}))
		if _, err := Convert(context.Background(), srv.Client(), srv.URL, "code"); err == nil {
			t.Errorf("%s: accepted", name)
		}
		srv.Close()
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	}))
	defer srv.Close()
	if _, err := Convert(context.Background(), srv.Client(), srv.URL, "used"); err == nil {
		t.Error("a spent code accepted")
	}
}
