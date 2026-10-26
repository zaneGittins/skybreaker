// Package client is the terminal side of Skybreaker: `skybreaker` on your
// own machine lists your sessions and attaches to one, over the tailnet,
// as you. It is standard library only, like the server it ships in.
package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client talks to one Skybreaker. The server knows you by your machine's
// Tailscale login, so there is nothing to sign in with.
type Client struct {
	base *url.URL
	HTTP *http.Client
	TLS  *tls.Config // for the terminal's WebSocket; nil is the system's roots
}

func New(base string) (*Client, error) {
	s, err := NormalizeURL(base)
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(s)
	return &Client{base: u, HTTP: &http.Client{Timeout: 2 * time.Minute}}, nil
}

// Host is the server's host name, for display.
func (c *Client) Host() string { return c.base.Host }

func (c *Client) origin() string { return c.base.Scheme + "://" + c.base.Host }

// endpoint joins path segments onto the server's URL, escaping each, so a
// session ID or project name can't reach another endpoint.
func (c *Client) endpoint(parts ...string) string {
	esc := make([]string, len(parts))
	for i, p := range parts {
		esc[i] = url.PathEscape(p)
	}
	return c.origin() + "/" + strings.Join(esc, "/")
}

func (c *Client) wsEndpoint(parts ...string) string {
	scheme := "wss"
	if c.base.Scheme == "http" {
		scheme = "ws"
	}
	return scheme + strings.TrimPrefix(c.endpoint(parts...), c.base.Scheme)
}

type Meta struct {
	Version   string   `json:"version"`
	User      string   `json:"user"`
	Harnesses []string `json:"harnesses"`
}

type Project struct {
	Name      string   `json:"name"`
	Harnesses []string `json:"harnesses"`
	Running   bool     `json:"running"`
}

type Session struct {
	ID      string    `json:"id"`
	Project string    `json:"project"`
	Harness string    `json:"harness"`
	Title   string    `json:"title"`
	Status  string    `json:"status"`
	Mode    string    `json:"mode"` // "chat", or empty for a terminal
	State   string    `json:"state"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
	Parent  string    `json:"parent"`
}

// Chat reports whether the session runs in its harness's structured mode,
// so attaching means switching it to the terminal first.
func (s Session) Chat() bool { return s.Mode == "chat" }

type Inbox struct {
	Projects []Project `json:"projects"`
	Sessions []Session `json:"sessions"`
}

func (c *Client) Meta(ctx context.Context) (Meta, error) {
	var m Meta
	return m, c.do(ctx, http.MethodGet, c.endpoint("api", "meta"), nil, &m)
}

func (c *Client) Inbox(ctx context.Context) (Inbox, error) {
	var in Inbox
	return in, c.do(ctx, http.MethodGet, c.endpoint("api", "inbox"), nil, &in)
}

// NewSession starts a harness in a terminal.
func (c *Client) NewSession(ctx context.Context, project, harness string) (Session, error) {
	var s Session
	body := map[string]string{"harness": harness, "mode": "terminal"}
	return s, c.do(ctx, http.MethodPost, c.endpoint("api", "projects", project, "sessions"), body, &s)
}

// ChatTerminal continues a chat in its harness's own terminal UI, on the
// same conversation, and returns the terminal session to attach to.
func (c *Client) ChatTerminal(ctx context.Context, id string) (Session, error) {
	var s Session
	return s, c.do(ctx, http.MethodPost, c.endpoint("api", "sessions", id, "terminal"), struct{}{}, &s)
}

// NewProject is a project to create: a repo's, or none.
type NewProject struct {
	Name         string   `json:"name"`
	Repo         string   `json:"repo,omitempty"`
	Harnesses    []string `json:"harnesses"`
	Capabilities []string `json:"capabilities"`
}

// AddedProject is a created project. Skybreaker restarts to run it, "now"
// or "when_idle".
type AddedProject struct {
	Name    string `json:"name"`
	Restart string `json:"restart"`
	GitHub  *struct {
		Installed  bool   `json:"installed"`
		InstallURL string `json:"install_url"`
		Error      string `json:"error"`
	} `json:"github"`
}

func (c *Client) CreateProject(ctx context.Context, p NewProject) (AddedProject, error) {
	var out AddedProject
	return out, c.do(ctx, http.MethodPost, c.endpoint("api", "projects"), p, &out)
}

// Capabilities are the bundles of access a new project can be given.
func (c *Client) Capabilities(ctx context.Context) ([]string, error) {
	var caps []struct {
		Name string `json:"name"`
	}
	if err := c.do(ctx, http.MethodGet, c.endpoint("api", "capabilities"), nil, &caps); err != nil {
		return nil, err
	}
	out := make([]string, len(caps))
	for i, cp := range caps {
		out[i] = cp.Name
	}
	return out, nil
}

// FillWorkspace sends a gzipped tar to unpack into the project's empty
// workspace. It takes as long as the folder does, so no timeout.
func (c *Client) FillWorkspace(ctx context.Context, project string, archive io.Reader) error {
	h := *c.HTTP
	h.Timeout = 0
	return c.send(ctx, &h, http.MethodPut, c.endpoint("api", "projects", project, "workspace"), "application/gzip", archive, nil)
}

func (c *Client) do(ctx context.Context, method, u string, in, out any) error {
	if in == nil {
		return c.send(ctx, c.HTTP, method, u, "", nil, out)
	}
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return c.send(ctx, c.HTTP, method, u, "application/json", bytes.NewReader(b), out)
}

func (c *Client) send(ctx context.Context, h *http.Client, method, u, contentType string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Origin", c.origin())
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := h.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusForbidden {
		return errors.New("Skybreaker refused this machine: its Tailscale login isn't one of the server's users (and tagged machines never are)")
	}
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(b, &e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}
		return fmt.Errorf("%s %s: %s", method, u, resp.Status)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(b, out)
}
