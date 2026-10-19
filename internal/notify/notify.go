// Package notify sends push notifications. Payloads are content-free by
// design: a title such as "Approval waiting" and a link, never the command,
// path or prompt, because ntfy.sh and the phone's push provider see them.
package notify

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zaneGittins/skybreaker/internal/configfile"
)

type Message struct {
	ID       string // de-duplication key; empty is never de-duplicated
	Title    string
	Body     string
	Click    string // URL opened when the notification is tapped
	Priority int    // ntfy 1–5; 0 leaves the default
	Tags     []string
}

type Notifier interface {
	Notify(ctx context.Context, m Message) error
}

// Config is the ntfy section of both daemons' config files.
type Config struct {
	URL       string `json:"url" yaml:"url"`
	TokenFile string `json:"token_file" yaml:"token_file"`
}

// FromConfig is the configured ntfy topic, deduplicated over ten minutes so
// the same approval arriving twice buzzes once. No URL means Nop.
func FromConfig(c Config) (Notifier, error) {
	if c.URL == "" {
		return Nop{}, nil
	}
	n := &Ntfy{URL: c.URL}
	if c.TokenFile != "" {
		tok, err := configfile.ReadSecret(c.TokenFile)
		if err != nil {
			return nil, fmt.Errorf("ntfy token: %w", err)
		}
		n.Token = tok
	}
	return Dedup(n, 10*time.Minute), nil
}

// Ntfy posts to an ntfy topic URL such as https://ntfy.sh/<topic>.
type Ntfy struct {
	URL   string
	Token string // optional access token
	HTTP  *http.Client
}

func (n *Ntfy) Notify(ctx context.Context, m Message) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.URL, strings.NewReader(m.Body))
	if err != nil {
		return err
	}
	set := func(k, v string) {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
	set("Title", m.Title)
	set("Click", m.Click)
	set("Tags", strings.Join(m.Tags, ","))
	if m.Priority > 0 {
		set("Priority", strconv.Itoa(m.Priority))
	}
	if n.Token != "" {
		set("Authorization", "Bearer "+n.Token)
	}
	c := n.HTTP
	if c == nil {
		c = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("ntfy: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("ntfy: status %d", resp.StatusCode)
	}
	return nil
}

// Multi sends each message to every notifier and joins their errors.
type Multi []Notifier

func (m Multi) Notify(ctx context.Context, msg Message) error {
	var errs []error
	for _, n := range m {
		if err := n.Notify(ctx, msg); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Nop drops every message.
type Nop struct{}

func (Nop) Notify(context.Context, Message) error { return nil }

// Deduper drops a message whose ID was already sent within the window, so
// the same approval arriving over two paths buzzes the phone once.
type Deduper struct {
	next   Notifier
	window time.Duration
	now    func() time.Time

	mu   sync.Mutex
	sent map[string]time.Time
}

func Dedup(next Notifier, window time.Duration) *Deduper {
	return &Deduper{next: next, window: window, now: time.Now, sent: map[string]time.Time{}}
}

func (d *Deduper) Notify(ctx context.Context, m Message) error {
	if m.ID != "" {
		d.mu.Lock()
		now := d.now()
		for id, at := range d.sent {
			if now.Sub(at) >= d.window {
				delete(d.sent, id)
			}
		}
		_, dup := d.sent[m.ID]
		if !dup {
			d.sent[m.ID] = now
		}
		d.mu.Unlock()
		if dup {
			return nil
		}
	}
	return d.next.Notify(ctx, m)
}
