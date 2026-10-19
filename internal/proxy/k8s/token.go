package k8s

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const minTTL = 10 * time.Minute // the apiserver's minimum expirationSeconds

// TokenSource mints ServiceAccount tokens with the TokenRequest API.
type TokenSource struct {
	APIServer                 string
	Bearer                    string // may only create tokens for this ServiceAccount
	Namespace, ServiceAccount string
	TTL                       time.Duration // default 15m, at least 10m
	HTTP                      *http.Client
	Now                       func() time.Time

	mu      sync.Mutex
	token   string
	refresh time.Time
}

// Token returns a cached token until 80% of its lifetime has passed. The
// mutex is held across the fetch so concurrent callers share one request.
func (s *TokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.token != "" && now.Before(s.refresh) {
		return s.token, nil
	}
	tok, exp, err := s.fetch(ctx)
	if err != nil {
		return "", err
	}
	s.token = tok
	s.refresh = now.Add(exp.Sub(now) * 8 / 10)
	return tok, nil
}

func (s *TokenSource) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *TokenSource) fetch(ctx context.Context) (string, time.Time, error) {
	if !validName(s.Namespace) || !validName(s.ServiceAccount) {
		return "", time.Time{}, fmt.Errorf("k8s token: bad namespace %q or service account %q", s.Namespace, s.ServiceAccount)
	}
	ttl := s.TTL
	if ttl == 0 {
		ttl = 15 * time.Minute
	}
	ttl = max(ttl, minTTL)
	body, _ := json.Marshal(map[string]any{
		"apiVersion": "authentication.k8s.io/v1",
		"kind":       "TokenRequest",
		"spec":       map[string]any{"expirationSeconds": int64(ttl / time.Second)},
	})
	u := strings.TrimSuffix(s.APIServer, "/") + "/api/v1/namespaces/" + s.Namespace + "/serviceaccounts/" + s.ServiceAccount + "/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("k8s token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.Bearer)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	client := s.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("k8s token: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return "", time.Time{}, fmt.Errorf("k8s token: %s for %s/%s", resp.Status, s.Namespace, s.ServiceAccount)
	}
	var out struct {
		Status struct {
			Token               string    `json:"token"`
			ExpirationTimestamp time.Time `json:"expirationTimestamp"`
		} `json:"status"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", time.Time{}, fmt.Errorf("k8s token: decoding response: %w", err)
	}
	if out.Status.Token == "" || out.Status.ExpirationTimestamp.IsZero() {
		return "", time.Time{}, errors.New("k8s token: response has no token or expiry")
	}
	return out.Status.Token, out.Status.ExpirationTimestamp, nil
}
