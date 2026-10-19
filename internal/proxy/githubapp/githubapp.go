// Package githubapp mints GitHub App installation tokens, each narrowed to a
// single repository and cached until shortly before it expires. The App's
// private key stays on the broker; agents only ever see requests carrying a
// token for their own repo, and never the token itself.
package githubapp

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/zaneGittins/skybreaker/internal/names"
)

const (
	defaultAPIBase = "https://api.github.com"
	// Refresh this long before expiry so a token never lapses mid-push.
	refreshBefore = 5 * time.Minute
)

// ParseKey reads an App private key in PKCS#1 (GitHub's download format) or
// PKCS#8 PEM.
func ParseKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	b, _ := pem.Decode(pemBytes)
	if b == nil {
		return nil, errors.New("githubapp: key is not PEM")
	}
	switch b.Type {
	case "RSA PRIVATE KEY":
		return x509.ParsePKCS1PrivateKey(b.Bytes)
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
		if err != nil {
			return nil, fmt.Errorf("githubapp: %w", err)
		}
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("githubapp: key is not RSA")
		}
		return rk, nil
	}
	return nil, fmt.Errorf("githubapp: unexpected PEM block %q", b.Type)
}

type App struct {
	AppID          string
	Key            *rsa.PrivateKey
	InstallationID int64 // 0: look it up per repo
	APIBase        string
	Permissions    map[string]string
	HTTP           *http.Client
	Now            func() time.Time

	mu    sync.Mutex
	repos map[string]*repoEntry
}

// repoEntry serialises minting per repo, so concurrent pushes share a token.
type repoEntry struct {
	mu           sync.Mutex
	installation int64
	token        string
	expires      time.Time
}

func (a *App) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// JWT signs the App's short-lived authentication token. iat is backdated a
// minute to absorb clock drift, as GitHub recommends.
func (a *App) JWT() (string, error) {
	if a.AppID == "" || a.Key == nil {
		return "", errors.New("githubapp: AppID and Key are required")
	}
	now := a.now()
	header := b64json(map[string]string{"alg": "RS256", "typ": "JWT"})
	claims := b64json(map[string]any{"iat": now.Unix() - 60, "exp": now.Add(9 * time.Minute).Unix(), "iss": a.AppID})
	signing := header + "." + claims
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.Key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("githubapp: sign: %w", err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func b64json(v any) string {
	b, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(b)
}

// Token returns an installation token restricted to repo ("owner/name").
func (a *App) Token(ctx context.Context, repo string) (string, error) {
	if !names.Repo(repo) {
		return "", fmt.Errorf("githubapp: invalid repo %q", repo)
	}
	key := strings.ToLower(repo)
	a.mu.Lock()
	if a.repos == nil {
		a.repos = map[string]*repoEntry{}
	}
	e, ok := a.repos[key]
	if !ok {
		e = &repoEntry{}
		a.repos[key] = e
	}
	a.mu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.token != "" && e.expires.Sub(a.now()) > refreshBefore {
		return e.token, nil
	}
	if a.InstallationID != 0 {
		return e.mint(ctx, a, a.InstallationID, repo)
	}
	if e.installation != 0 {
		tok, err := e.mint(ctx, a, e.installation, repo)
		if err == nil {
			return tok, nil
		}
		// Reinstalling the App changes its installation ID, so a cached
		// one can go stale. Look it up again, once.
		e.installation = 0
	}
	id, err := a.lookupInstallation(ctx, repo)
	if err != nil {
		return "", err
	}
	e.installation = id
	return e.mint(ctx, a, id, repo)
}

// mint refreshes e's token. The caller holds e.mu.
func (e *repoEntry) mint(ctx context.Context, a *App, installation int64, repo string) (string, error) {
	tok, exp, err := a.mint(ctx, installation, repo)
	if err != nil {
		return "", err
	}
	e.token, e.expires = tok, exp
	return tok, nil
}

func (a *App) lookupInstallation(ctx context.Context, repo string) (int64, error) {
	var out struct {
		ID int64 `json:"id"`
	}
	if err := a.call(ctx, http.MethodGet, "/repos/"+repo+"/installation", nil, http.StatusOK, &out); err != nil {
		return 0, err
	}
	if out.ID == 0 {
		return 0, fmt.Errorf("githubapp: no installation for %s", repo)
	}
	return out.ID, nil
}

func (a *App) mint(ctx context.Context, installation int64, repo string) (string, time.Time, error) {
	body := map[string]any{"repositories": []string{repo[strings.IndexByte(repo, '/')+1:]}}
	if len(a.Permissions) > 0 {
		body["permissions"] = a.Permissions
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	path := fmt.Sprintf("/app/installations/%d/access_tokens", installation)
	if err := a.call(ctx, http.MethodPost, path, body, http.StatusCreated, &out); err != nil {
		return "", time.Time{}, err
	}
	if out.Token == "" || !out.ExpiresAt.After(a.now()) {
		return "", time.Time{}, fmt.Errorf("githubapp: unusable token response for %s", repo)
	}
	return out.Token, out.ExpiresAt, nil
}

func (a *App) call(ctx context.Context, method, path string, in any, want int, out any) error {
	jwt, err := a.JWT()
	if err != nil {
		return err
	}
	var rd io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	base := a.APIBase
	if base == "" {
		base = defaultAPIBase
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(base, "/")+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+jwt)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := a.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("githubapp: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("githubapp: %s %s: %w", method, path, err)
	}
	if resp.StatusCode != want {
		var e struct {
			Message string `json:"message"`
		}
		json.Unmarshal(b, &e)
		return fmt.Errorf("githubapp: %s %s: status %d: %s", method, path, resp.StatusCode, e.Message)
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("githubapp: %s %s: %w", method, path, err)
	}
	return nil
}
