package broker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zaneGittins/skybreaker/internal/proxy/approval"
	"github.com/zaneGittins/skybreaker/internal/proxy/config"
)

func policy(t *testing.T) *config.Config {
	t.Helper()
	p, err := config.New(map[string]*config.Project{
		"orbit": {Repo: "zaneGittins/orbit", Allow: []*config.Entry{
			{Host: "api.digitalocean.com", Mode: config.ModeMITM, Inject: &config.Inject{Bearer: "do"}, Methods: []string{"GET"}},
		}},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func open(t *testing.T, secrets string) (*Broker, string, error) {
	t.Helper()
	dir := t.TempDir()
	sf := filepath.Join(dir, "secrets.json")
	os.WriteFile(sf, []byte(secrets), 0o600)
	b, err := Open(Options{
		DataDir: filepath.Join(dir, "broker"), SecretsFile: sf, Policy: policy(t),
		Sandbox: func(string) (string, bool) { return "", false },
	})
	if err == nil {
		t.Cleanup(b.Close)
	}
	return b, dir, err
}

const goodSecrets = `{"secrets": [{"name": "do", "kind": "static", "value": "dop_v1_x", "hosts": ["api.digitalocean.com"]}]}`

func TestOpen(t *testing.T) {
	b, dir, err := open(t, goodSecrets)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "broker", "ca", "ca.key")); err != nil {
		t.Errorf("CA key not persisted: %v", err)
	}
	pem, err := b.CA(context.Background())
	if err != nil || !strings.Contains(string(pem), "BEGIN CERTIFICATE") {
		t.Fatalf("CA: %v", err)
	}
	// Nothing but a sandbox network may use the proxy.
	rec := httptest.NewRecorder()
	b.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/ca.crt", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("outside caller: %d", rec.Code)
	}
}

func TestOpenRefusesUnboundSecret(t *testing.T) {
	_, _, err := open(t, `{"secrets": [{"name": "do", "kind": "static", "value": "x", "hosts": ["example.com"]}]}`)
	if err == nil || !strings.Contains(err.Error(), "not bound") {
		t.Fatalf("err = %v", err)
	}
}

func TestTokensAndKillSwitch(t *testing.T) {
	b, dir, err := open(t, goodSecrets)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	m, err := b.Mint(ctx, "orbit", time.Hour)
	if err != nil || m.Token == "" || m.ID == "" {
		t.Fatalf("mint: %+v %v", m, err)
	}
	if _, err := b.Mint(ctx, "ghost", time.Hour); err == nil {
		t.Fatal("minted a token for an unknown project")
	}
	if len(b.Tokens()) != 1 {
		t.Fatalf("tokens = %v", b.Tokens())
	}
	if err := b.RevokeToken(m.ID, "zane@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := b.RevokeToken(m.ID, "zane@example.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second revoke: %v", err)
	}
	b.Mint(ctx, "orbit", time.Hour)
	b.Mint(ctx, "orbit", time.Hour)
	if n, err := b.RevokeAll("zane@example.com"); err != nil || n != 2 {
		t.Fatalf("kill switch: %d %v", n, err)
	}
	entries, err := b.Audit(50)
	if err != nil {
		t.Fatal(err)
	}
	log := ""
	for _, e := range entries {
		log += e.Decision + ":" + e.By + ":" + e.Reason + "\n"
	}
	for _, want := range []string{"mint::session " + m.ID, "revoke:zane@example.com:session " + m.ID, "revoke:zane@example.com:kill switch: 2 sessions"} {
		if !strings.Contains(log, want) {
			t.Errorf("audit lacks %q:\n%s", want, log)
		}
	}
	// Tokens survive a restart: the registry is on disk.
	if fi, err := os.Stat(filepath.Join(dir, "broker", "sessions.json")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("sessions file: %v %v", fi, err)
	}
}

func TestDecide(t *testing.T) {
	b, _, err := open(t, goodSecrets)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := b.approvals.Request(context.Background(), approval.Request{Project: "orbit", Method: "PUT", Host: "api.github.com", Path: "/x"}, 0)
	got, err := b.Decide(a.ID, true, "zane@example.com")
	if err != nil || got.State != approval.StateApproved || got.DecidedBy != "zane@example.com" {
		t.Fatalf("decide: %+v %v", got, err)
	}
	if _, err := b.Decide(a.ID, false, "zane@example.com"); !errors.Is(err, approval.ErrNotPending) {
		t.Fatalf("decided twice: %v", err)
	}
	entries, _ := b.Audit(10)
	if len(entries) == 0 || entries[len(entries)-1].Decision != "approve" || entries[len(entries)-1].ApprovalID != a.ID {
		t.Fatalf("decision not audited: %+v", entries)
	}
}
