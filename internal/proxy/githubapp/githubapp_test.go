package githubapp

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var testKey = func() *rsa.PrivateKey {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return k
}()

func TestParseKey(t *testing.T) {
	pkcs1 := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(testKey)})
	der, _ := x509.MarshalPKCS8PrivateKey(testKey)
	pkcs8 := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	for name, b := range map[string][]byte{"pkcs1": pkcs1, "pkcs8": pkcs8} {
		k, err := ParseKey(b)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !k.Equal(testKey) {
			t.Errorf("%s: wrong key", name)
		}
	}
	for name, b := range map[string][]byte{
		"empty":   nil,
		"garbage": []byte("not a key"),
		"cert":    pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte{1, 2, 3}}),
	} {
		if _, err := ParseKey(b); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

// verifyJWT checks the RS256 signature with the public key and returns the claims.
func verifyJWT(t *testing.T, tok string) map[string]any {
	t.Helper()
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt has %d parts", len(parts))
	}
	var hdr map[string]string
	hb, _ := base64.RawURLEncoding.DecodeString(parts[0])
	if err := json.Unmarshal(hb, &hdr); err != nil {
		t.Fatal(err)
	}
	if hdr["alg"] != "RS256" || hdr["typ"] != "JWT" {
		t.Errorf("header %v", hdr)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&testKey.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatalf("bad signature: %v", err)
	}
	var claims map[string]any
	cb, _ := base64.RawURLEncoding.DecodeString(parts[1])
	if err := json.Unmarshal(cb, &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

func TestJWT(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	a := &App{AppID: "12345", Key: testKey, Now: func() time.Time { return now }}
	tok, err := a.JWT()
	if err != nil {
		t.Fatal(err)
	}
	c := verifyJWT(t, tok)
	if c["iss"] != "12345" {
		t.Errorf("iss %v", c["iss"])
	}
	if int64(c["iat"].(float64)) != now.Unix()-60 {
		t.Errorf("iat %v", c["iat"])
	}
	if int64(c["exp"].(float64)) != now.Add(9*time.Minute).Unix() {
		t.Errorf("exp %v", c["exp"])
	}
	if _, err := (&App{Key: testKey}).JWT(); err == nil {
		t.Error("JWT without AppID succeeded")
	}
	if _, err := (&App{AppID: "1"}).JWT(); err == nil {
		t.Error("JWT without key succeeded")
	}
}

type fakeGitHub struct {
	t        *testing.T
	mints    atomic.Int32
	lookups  atomic.Int32
	expires  time.Time
	lastBody map[string]any
	mu       sync.Mutex
	status   int
	install  atomic.Int64 // the current installation ID; 0 means 4242
}

func (f *fakeGitHub) installation() int64 {
	if id := f.install.Load(); id != 0 {
		return id
	}
	return 4242
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	t := f.t
	if r.Header.Get("Accept") != "application/vnd.github+json" {
		t.Errorf("Accept %q", r.Header.Get("Accept"))
	}
	if r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
		t.Errorf("API version %q", r.Header.Get("X-GitHub-Api-Version"))
	}
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		t.Errorf("Authorization %q", auth)
	} else if c := verifyJWT(t, strings.TrimPrefix(auth, "Bearer ")); c["iss"] != "777" {
		t.Errorf("iss %v", c["iss"])
	}
	switch {
	case r.Method == "GET" && r.URL.Path == "/repos/zaneGittins/nope/installation":
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"Not Found"}`)
	case r.Method == "GET" && r.URL.Path == "/app":
		fmt.Fprint(w, `{"slug":"skybreaker-test","html_url":"https://github.com/apps/skybreaker-test"}`)
	case r.Method == "GET" && r.URL.Path == fmt.Sprintf("/app/installations/%d", f.installation()):
		fmt.Fprintf(w, `{"id": %d, "account": {"login": "zaneGittins"}}`, f.installation())
	case r.Method == "GET" && r.URL.Path == "/repos/zaneGittins/orbit/installation":
		f.lookups.Add(1)
		fmt.Fprintf(w, `{"id": %d}`, f.installation())
	case r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/app/installations/") &&
		r.URL.Path != fmt.Sprintf("/app/installations/%d/access_tokens", f.installation()):
		w.WriteHeader(http.StatusNotFound) // an old installation, since uninstalled
		fmt.Fprint(w, `{"message":"Not Found"}`)
	case r.Method == "POST" && r.URL.Path == fmt.Sprintf("/app/installations/%d/access_tokens", f.installation()):
		if f.status != 0 {
			w.WriteHeader(f.status)
			fmt.Fprint(w, `{"message":"Bad credentials"}`)
			return
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type %q", r.Header.Get("Content-Type"))
		}
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(b, &body); err != nil {
			t.Errorf("body %s: %v", b, err)
		}
		f.mu.Lock()
		f.lastBody = body
		f.mu.Unlock()
		n := f.mints.Add(1)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"token": fmt.Sprintf("ghs_token%d", n), "expires_at": f.expires.UTC().Format(time.RFC3339)})
	default:
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	}
}

func newApp(t *testing.T, f *fakeGitHub, now *time.Time) *App {
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return &App{AppID: "777", Key: testKey, APIBase: srv.URL, HTTP: srv.Client(), Now: func() time.Time { return *now }}
}

func TestTokenMintsScopedToOneRepoAndCaches(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	f := &fakeGitHub{t: t, expires: now.Add(time.Hour)}
	a := newApp(t, f, &now)
	a.InstallationID = 4242
	a.Permissions = map[string]string{"contents": "write"}

	tok, err := a.Token(context.Background(), "zaneGittins/orbit")
	if err != nil {
		t.Fatal(err)
	}
	if tok != "ghs_token1" {
		t.Errorf("token %q", tok)
	}
	repos, _ := f.lastBody["repositories"].([]any)
	if len(repos) != 1 || repos[0] != "orbit" {
		t.Errorf("repositories %v, want [orbit]", f.lastBody["repositories"])
	}
	if perms, _ := f.lastBody["permissions"].(map[string]any); perms["contents"] != "write" || len(perms) != 1 {
		t.Errorf("permissions %v", f.lastBody["permissions"])
	}

	now = now.Add(50 * time.Minute) // 10m left: still cached
	if tok, _ := a.Token(context.Background(), "zaneGittins/orbit"); tok != "ghs_token1" {
		t.Errorf("not cached: %q", tok)
	}
	now = now.Add(6 * time.Minute) // 4m left: inside the 5m margin
	if tok, _ := a.Token(context.Background(), "zaneGittins/orbit"); tok != "ghs_token2" {
		t.Errorf("not refreshed near expiry: %q", tok)
	}
	if f.lookups.Load() != 0 {
		t.Error("looked up the installation although its ID was configured")
	}
}

// A configured installation belongs to one account: a repo of any other
// owner is refused, not minted for the installation's same-named repo.
func TestFixedInstallationKeepsToItsAccount(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	f := &fakeGitHub{t: t, expires: now.Add(time.Hour)}
	a := newApp(t, f, &now)
	a.InstallationID = 4242
	if _, err := a.Token(context.Background(), "someoneelse/orbit"); err == nil {
		t.Fatal("minted for another owner's repo")
	}
	if f.mints.Load() != 0 {
		t.Fatal("a token was minted")
	}
	if _, err := a.Token(context.Background(), "ZaneGittins/orbit"); err != nil {
		t.Fatalf("the installation's own repo: %v", err)
	}
}

func TestTokenLooksUpInstallation(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	f := &fakeGitHub{t: t, expires: now.Add(time.Hour)}
	a := newApp(t, f, &now)
	if _, err := a.Token(context.Background(), "zaneGittins/orbit"); err != nil {
		t.Fatal(err)
	}
	if f.lookups.Load() != 1 {
		t.Errorf("lookups %d", f.lookups.Load())
	}
	if _, ok := f.lastBody["permissions"]; ok {
		t.Error("sent permissions although none were configured")
	}
}

func TestTokenFollowsReinstall(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	f := &fakeGitHub{t: t, expires: now.Add(time.Hour)}
	a := newApp(t, f, &now)
	if _, err := a.Token(context.Background(), "zaneGittins/orbit"); err != nil {
		t.Fatal(err)
	}
	f.install.Store(5151) // the App was uninstalled and installed again
	now = now.Add(2 * time.Hour)
	f.expires = now.Add(time.Hour)
	if _, err := a.Token(context.Background(), "zaneGittins/orbit"); err != nil {
		t.Fatalf("after a reinstall: %v", err)
	}
	if n := f.lookups.Load(); n != 2 {
		t.Errorf("lookups = %d, want 2", n)
	}

	f.status = http.StatusInternalServerError // failing for another reason: one lookup per call, no loop
	now = now.Add(2 * time.Hour)
	if _, err := a.Token(context.Background(), "zaneGittins/orbit"); err == nil {
		t.Fatal("mint error was swallowed")
	}
	if n := f.lookups.Load(); n != 3 {
		t.Errorf("lookups = %d, want 3", n)
	}
}

func TestTokenSeparateCachePerRepo(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	f := &fakeGitHub{t: t, expires: now.Add(time.Hour)}
	a := newApp(t, f, &now)
	a.InstallationID = 4242
	t1, _ := a.Token(context.Background(), "zaneGittins/orbit")
	t2, _ := a.Token(context.Background(), "zaneGittins/other")
	if t1 == t2 {
		t.Error("two repos shared one token")
	}
}

func TestTokenConcurrentSingleFlight(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	f := &fakeGitHub{t: t, expires: now.Add(time.Hour)}
	a := newApp(t, f, &now)
	a.InstallationID = 4242
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := a.Token(context.Background(), "zaneGittins/orbit"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := f.mints.Load(); n != 1 {
		t.Errorf("minted %d tokens for one repo, want 1", n)
	}
}

func TestTokenErrors(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	f := &fakeGitHub{t: t, expires: now.Add(time.Hour), status: http.StatusUnauthorized}
	a := newApp(t, f, &now)
	a.InstallationID = 4242
	_, err := a.Token(context.Background(), "zaneGittins/orbit")
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("err %v, want status 401", err)
	}
	for _, repo := range []string{"", "orbit", "a/b/c", "/orbit", "owner/", "own er/x", "../x"} {
		if _, err := a.Token(context.Background(), repo); err == nil {
			t.Errorf("Token(%q) succeeded", repo)
		}
	}
}

func TestTokenRejectsEmptyOrExpiredResponse(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"token":"","expires_at":"2020-01-01T00:00:00Z"}`)
	}))
	defer srv.Close()
	a := &App{AppID: "1", Key: testKey, InstallationID: 1, APIBase: srv.URL, HTTP: srv.Client(), Now: func() time.Time { return now }}
	if _, err := a.Token(context.Background(), "o/r"); err == nil {
		t.Error("accepted an empty token")
	}
}

func TestInstalledAndInstallURL(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	a := newApp(t, &fakeGitHub{t: t, expires: now.Add(time.Hour)}, &now)
	ctx := context.Background()
	if ok, err := a.Installed(ctx, "zaneGittins/orbit"); !ok || err != nil {
		t.Fatalf("installed repo: %v %v", ok, err)
	}
	if ok, err := a.Installed(ctx, "zaneGittins/nope"); ok || err != nil {
		t.Fatalf("repo without the App: %v %v", ok, err)
	}
	if u, err := a.InstallURL(ctx); err != nil || u != "https://github.com/apps/skybreaker-test/installations/new" {
		t.Fatalf("install url %q %v", u, err)
	}
}
