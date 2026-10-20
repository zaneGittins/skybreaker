package githubapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// CreatedPath is where GitHub sends you back after creating an App from a
// manifest, on Skybreaker's UI.
const CreatedPath = "/api/github-app/created"

var (
	orgRe  = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)
	codeRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)
	slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,99}$`)
)

// Manifest is a GitHub App manifest: GitHub's "create an App from a
// manifest" form takes it to make an App with the permissions agents need
// in one click. The App is private and has no webhook.
func Manifest(name, uiURL string) ([]byte, error) {
	u, err := url.Parse(uiURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, errors.New("githubapp: Skybreaker's ui_url must be its https address")
	}
	base := "https://" + u.Host
	return json.Marshal(map[string]any{
		"name":                name,
		"url":                 base,
		"redirect_url":        base + CreatedPath,
		"hook_attributes":     map[string]any{"url": base + "/api/github-app/hook", "active": false},
		"public":              false,
		"default_permissions": Needed,
		"default_events":      []string{},
	})
}

// NewAppURL is where the manifest form posts: your own account, or an
// organization's.
func NewAppURL(org, state string) (string, error) {
	q := "?state=" + url.QueryEscape(state)
	if org == "" {
		return "https://github.com/settings/apps/new" + q, nil
	}
	if !orgRe.MatchString(org) {
		return "", fmt.Errorf("githubapp: %q is not a GitHub organization name", org)
	}
	return "https://github.com/organizations/" + org + "/settings/apps/new" + q, nil
}

// Created is an App GitHub made from a manifest.
type Created struct {
	ID      string
	Slug    string
	Name    string
	HTMLURL string
	Owner   string
	PEM     string
}

// InstallURL is where to install the App on repositories.
func (c Created) InstallURL() string {
	return "https://github.com/apps/" + c.Slug + "/installations/new"
}

// Convert trades the one-time code GitHub sends back for the new App's ID
// and private key. The code is the only credential: the call carries none.
func Convert(ctx context.Context, client *http.Client, apiBase, code string) (Created, error) {
	if !codeRe.MatchString(code) {
		return Created{}, errors.New("githubapp: GitHub sent back no usable code")
	}
	if apiBase == "" {
		apiBase = defaultAPIBase
	}
	path := "/app-manifests/" + code + "/conversions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(apiBase, "/")+path, bytes.NewReader(nil))
	if err != nil {
		return Created{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return Created{}, fmt.Errorf("githubapp: creating the App: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Created{}, fmt.Errorf("githubapp: creating the App: %w", err)
	}
	if resp.StatusCode != http.StatusCreated {
		var e struct {
			Message string `json:"message"`
		}
		json.Unmarshal(b, &e)
		return Created{}, &StatusError{Method: http.MethodPost, Path: "/app-manifests/…/conversions", Status: resp.StatusCode, Message: e.Message}
	}
	var out struct {
		ID      int64  `json:"id"`
		Slug    string `json:"slug"`
		Name    string `json:"name"`
		HTMLURL string `json:"html_url"`
		Owner   struct {
			Login string `json:"login"`
		} `json:"owner"`
		PEM string `json:"pem"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return Created{}, fmt.Errorf("githubapp: creating the App: %w", err)
	}
	if out.ID <= 0 || !slugRe.MatchString(out.Slug) {
		return Created{}, errors.New("githubapp: GitHub's answer has no App ID or slug")
	}
	if _, err := ParseKey([]byte(out.PEM)); err != nil {
		return Created{}, fmt.Errorf("githubapp: the new App's key: %w", err)
	}
	return Created{ID: strconv.FormatInt(out.ID, 10), Slug: out.Slug, Name: out.Name, HTMLURL: out.HTMLURL, Owner: out.Owner.Login, PEM: out.PEM}, nil
}
