// Package broker assembles the credential broker inside the skybreaker
// process: secrets, CA, session tokens, audit log, approvals and the proxy
// sandboxes use as their only way out. The control plane mints and revokes
// tokens through it directly, and the UI answers approvals through it.
package broker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"github.com/zaneGittins/skybreaker/internal/notify"
	"github.com/zaneGittins/skybreaker/internal/proxy/approval"
	"github.com/zaneGittins/skybreaker/internal/proxy/audit"
	"github.com/zaneGittins/skybreaker/internal/proxy/ca"
	"github.com/zaneGittins/skybreaker/internal/proxy/config"
	"github.com/zaneGittins/skybreaker/internal/proxy/netguard"
	"github.com/zaneGittins/skybreaker/internal/proxy/secrets"
	"github.com/zaneGittins/skybreaker/internal/proxy/server"
	"github.com/zaneGittins/skybreaker/internal/proxy/session"
)

var ErrNotFound = errors.New("not found")

type Options struct {
	DataDir     string // CA, session tokens and audit log
	SecretsFile string // "" for none
	// AppSecretsFile holds the secrets added in Skybreaker's settings; ""
	// for none.
	AppSecretsFile string
	Policy         *config.Config
	// Sandbox names the project whose sandbox network a connection comes
	// from; see server.Deps.
	Sandbox func(remoteAddr string) (project string, ok bool)
	// Block and Allow adjust which addresses upstreams may resolve to.
	Block, Allow []string
	// Tailnet, when set, reaches tailnet upstreams through Skybreaker's own
	// node.
	Tailnet     netguard.Tailnet
	Hold        time.Duration // how long an ask request waits, default 30s
	PendingTTL  time.Duration
	ApprovedTTL time.Duration
	UIURL       string // where approvals are answered
}

type Broker struct {
	approvals *approval.Store
	sessions  *session.Registry
	audit     *audit.Logger
	auditPath string
	ca        *ca.Authority
	srv       *server.Server
	secrets   *secrets.Store
}

// Open starts the broker. It fails if any injected secret is missing or
// isn't bound to the host it would be sent to.
func Open(o Options) (*Broker, error) {
	if o.Policy == nil || o.Sandbox == nil {
		return nil, errors.New("broker: missing policy or sandbox check")
	}
	store, err := loadSecrets(o.Policy, o.SecretsFile, o.AppSecretsFile)
	if err != nil {
		return nil, err
	}
	authority, err := ca.LoadOrCreate(filepath.Join(o.DataDir, "ca"))
	if err != nil {
		return nil, err
	}
	sessions, err := session.NewRegistry(filepath.Join(o.DataDir, "sessions.json"), nil)
	if err != nil {
		return nil, err
	}
	auditPath := filepath.Join(o.DataDir, "audit.log")
	auditLog, err := audit.Open(auditPath)
	if err != nil {
		return nil, err
	}
	guard, err := netguard.New(o.Block, o.Allow)
	if err != nil {
		auditLog.Close()
		return nil, err
	}
	if o.Tailnet != nil {
		guard = guard.WithTailnet(o.Tailnet)
	}
	b := &Broker{
		approvals: approval.New(approval.Options{PendingTTL: o.PendingTTL, ApprovedTTL: o.ApprovedTTL}),
		sessions:  sessions, audit: auditLog, auditPath: auditPath, ca: authority, secrets: store,
	}
	b.srv, err = server.New(server.Deps{
		Config: o.Policy, Sandbox: o.Sandbox, Sessions: sessions, Secrets: store, CA: authority,
		Approvals: b.approvals, Audit: auditLog, Guard: guard, Hold: o.Hold, UIURL: o.UIURL,
	})
	if err != nil {
		auditLog.Close()
		return nil, err
	}
	return b, nil
}

// Handler is the proxy sandboxes use.
func (b *Broker) Handler() http.Handler { return b.srv }

func (b *Broker) Close() {
	b.srv.Close()
	b.audit.Close()
}

// --- for the control plane --------------------------------------------------

// Minted is a new session token: the sandbox's proxy credential.
type Minted struct {
	ID      string
	Token   string
	Expires time.Time
}

func (b *Broker) Mint(_ context.Context, project string, ttl time.Duration) (Minted, error) {
	if _, ok := b.srv.Project(project); !ok {
		return Minted{}, fmt.Errorf("broker: unknown project %s", project)
	}
	tok, info, err := b.sessions.Mint(project, ttl)
	if err != nil {
		return Minted{}, err
	}
	b.audit.Log(audit.Entry{Project: project, Decision: "mint", Reason: "session " + info.ID})
	return Minted{ID: info.ID, Token: tok, Expires: info.Expires}, nil
}

func (b *Broker) Revoke(_ context.Context, id string) error {
	project := b.projectOf(id)
	if _, err := b.sessions.Revoke(id); err != nil {
		return err
	}
	b.revokedToken(project)
	b.audit.Log(audit.Entry{Decision: "revoke", Reason: "session " + id})
	return nil
}

// projectOf names the project a session token belongs to.
func (b *Broker) projectOf(id string) string {
	for _, s := range b.sessions.List() {
		if s.ID == id {
			return s.Project
		}
	}
	return ""
}

// revoked takes effect at once: open connections close, held requests
// end, and what the project asked for can't be approved or redeemed by its
// next token. project "" is every project.
func (b *Broker) revoked(project string) {
	b.approvals.Expire(project)
	b.srv.Sweep()
}

// revokedToken is revoked for one token, whose project may be unknown: then
// connections are still swept, and no approvals are touched.
func (b *Broker) revokedToken(project string) {
	if project == "" {
		b.srv.Sweep()
		return
	}
	b.revoked(project)
}

// RevokeProject revokes every token of a project, including ones whose IDs
// the control plane lost.
func (b *Broker) RevokeProject(_ context.Context, project string) error {
	n, err := b.sessions.RevokeProject(project)
	b.revoked(project)
	if n > 0 {
		b.audit.Log(audit.Entry{Project: project, Decision: "revoke", Reason: fmt.Sprintf("%d sessions", n)})
	}
	return err
}

// CA is the public half of the broker's CA, which sandboxes trust.
func (b *Broker) CA(context.Context) ([]byte, error) { return b.ca.CertPEM(), nil }

// ErrNoGitHubApp means no github_app secret is configured.
var ErrNoGitHubApp = errors.New("no GitHub App is configured")

// GitHubAccess reports whether the GitHub App is installed on repo, and
// where to install it.
func (b *Broker) GitHubAccess(ctx context.Context, repo string) (installed bool, installURL string, err error) {
	app := b.secrets.GitHubApp()
	if app == nil {
		return false, "", ErrNoGitHubApp
	}
	if installed, err = app.Installed(ctx, repo); err != nil {
		return false, "", err
	}
	installURL, _ = app.InstallURL(ctx)
	return installed, installURL, nil
}

// GitHubInstallURL is where to install the GitHub App on more repositories.
func (b *Broker) GitHubInstallURL(ctx context.Context) (string, error) {
	app := b.secrets.GitHubApp()
	if app == nil {
		return "", ErrNoGitHubApp
	}
	return app.InstallURL(ctx)
}

// SecretsUsed is when each secret was last sent, since start.
func (b *Broker) SecretsUsed() map[string]time.Time { return b.secrets.LastUsed() }

// LogConfig records a settings change made in the app. what never holds a
// secret's value.
func (b *Broker) LogConfig(by, what string) {
	b.audit.Log(audit.Entry{Decision: "config", By: by, Reason: what})
}

// --- for the UI -------------------------------------------------------------

// Approvals lists pending and recently decided capability requests.
func (b *Broker) Approvals() []approval.Approval { return b.approvals.List() }

// Subscribe reports every change to an approval.
func (b *Broker) Subscribe() (<-chan approval.Approval, func()) { return b.approvals.Subscribe() }

// Decide answers a pending approval as by.
func (b *Broker) Decide(id string, approve bool, by string) (approval.Approval, error) {
	a, err := b.approvals.Decide(id, approve, by)
	if err != nil {
		return a, err
	}
	decision := "deny"
	if approve {
		decision = "approve"
	}
	b.audit.Log(audit.Entry{Sender: by, By: by, Project: a.Project, Host: a.Host, Method: a.Method, Path: a.Path, Decision: decision, ApprovalID: a.ID})
	return a, nil
}

// Tokens lists the live session tokens, one per running sandbox.
func (b *Broker) Tokens() []session.Info { return b.sessions.List() }

// RevokeToken cuts one sandbox off until it is restarted.
func (b *Broker) RevokeToken(id, by string) error {
	project := b.projectOf(id)
	ok, err := b.sessions.Revoke(id)
	if !ok {
		return ErrNotFound
	}
	b.revokedToken(project)
	if err != nil {
		b.audit.Log(audit.Entry{Sender: by, By: by, Decision: "revoke", Reason: "session " + id + " revoked in memory but not saved: " + err.Error()})
		return fmt.Errorf("revoked now, but not saved: a restart would restore it (%w)", err)
	}
	b.audit.Log(audit.Entry{Sender: by, By: by, Decision: "revoke", Reason: "session " + id})
	return nil
}

// RevokeAll is the kill switch: every sandbox loses all brokered access at
// once, open connections included.
func (b *Broker) RevokeAll(by string) (int, error) {
	n, err := b.sessions.RevokeAll()
	b.revoked("")
	if err != nil {
		b.audit.Log(audit.Entry{Sender: by, By: by, Decision: "revoke", Reason: fmt.Sprintf("kill switch: %d sessions revoked in memory but not saved: %v", n, err)})
		return n, fmt.Errorf("revoked %d now, but not saved: a restart would restore them (%w)", n, err)
	}
	b.audit.Log(audit.Entry{Sender: by, By: by, Decision: "revoke", Reason: fmt.Sprintf("kill switch: %d sessions", n)})
	return n, nil
}

// Audit returns the last n audit entries, oldest first.
func (b *Broker) Audit(n int) ([]audit.Entry, error) { return audit.Tail(b.auditPath, n) }

// AuditBack walks the audit log newest first until each returns false.
func (b *Broker) AuditBack(each func(audit.Entry) bool) error { return audit.Back(b.auditPath, each) }

// Notify buzzes the phone for new pending approvals until ctx ends.
func (b *Broker) Notify(ctx context.Context, n notify.Notifier, uiURL string) {
	notifyApprovals(ctx, b.approvals, n, uiURL)
}

// notifyApprovals buzzes the phone for new pending approvals. The message
// names no host, path or project: it only says to look. IDs are per project
// and minute, so the deduper turns a flood into one push.
func notifyApprovals(ctx context.Context, s *approval.Store, n notify.Notifier, uiURL string) {
	ch, cancel := s.Subscribe()
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case a := <-ch:
			if a.State != approval.StatePending {
				continue
			}
			nctx, done := context.WithTimeout(ctx, 15*time.Second)
			if err := n.Notify(nctx, notify.Message{ID: "cap-" + a.Project + "-" + strconv.FormatInt(a.Created.Unix()/60, 10), Title: "Approval waiting", Body: "An agent's request needs your decision.", Click: uiURL, Priority: 4, Tags: []string{"lock"}}); err != nil {
				log.Printf("skybreaker: notify: %v", err)
			}
			done()
		}
	}
}
