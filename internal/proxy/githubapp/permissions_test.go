package githubapp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"
)

func TestLacking(t *testing.T) {
	old := map[string]string{"contents": "write", "pull_requests": "write", "issues": "write", "metadata": "read"}
	full := map[string]string{"contents": "admin", "pull_requests": "write", "issues": "write", "metadata": "read", "actions": "write", "checks": "read", "statuses": "read"}
	app, installs := old, []map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			t.Error("no JWT")
		}
		switch r.URL.Path {
		case "/app":
			json.NewEncoder(w).Encode(map[string]any{"slug": "sky-app", "permissions": app, "owner": map[string]string{"login": "acme", "type": "Organization"}})
		case "/app/installations":
			json.NewEncoder(w).Encode(installs)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	now := time.Unix(1_800_000_000, 0)
	a := &App{AppID: "1", Key: testKey, APIBase: srv.URL, HTTP: srv.Client(), Now: func() time.Time { return now }}
	ctx := context.Background()

	// An App made before it read CI: the App needs changing, and so its
	// installations, which aren't listed for it twice.
	installs = []map[string]any{{"account": map[string]string{"login": "zane"}, "html_url": "https://github.com/settings/installations/9", "permissions": old}}
	l, err := a.Lacking(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"actions:read", "checks:read", "statuses:read"}; !slices.Equal(l.App, want) || len(l.Installations) != 0 {
		t.Errorf("lacking = %+v", l)
	}
	if l.PermissionsURL != "https://github.com/organizations/acme/settings/apps/sky-app/permissions" {
		t.Errorf("permissions url = %q", l.PermissionsURL)
	}
	// The App changed, one installation not yet accepted; a higher level
	// counts.
	app = full
	installs = append(installs, map[string]any{"account": map[string]string{"login": "acme"}, "html_url": "javascript:alert(1)", "permissions": full})
	l, _ = a.Lacking(ctx)
	if len(l.App) != 0 || len(l.Installations) != 1 || l.Installations[0].Account != "zane" || l.Installations[0].URL != "https://github.com/settings/installations/9" ||
		!slices.Equal(l.Installations[0].Lacks, []string{"actions:read", "checks:read", "statuses:read"}) {
		t.Errorf("lacking = %+v", l)
	}
	// Accepted everywhere.
	installs[0]["permissions"] = full
	if l, _ := a.Lacking(ctx); !l.None() {
		t.Errorf("lacking = %+v", l)
	}
	// A suspended installation mints nothing, so lacks nothing that matters.
	installs[0]["permissions"] = old
	installs[0]["suspended_at"] = "2026-10-01T00:00:00Z"
	if l, _ := a.Lacking(ctx); !l.None() {
		t.Errorf("suspended installation listed: %+v", l)
	}
	delete(installs[0], "suspended_at")
	installs[0]["permissions"] = full
	// A link GitHub didn't make isn't passed on.
	installs[1]["permissions"] = old
	if l, _ := a.Lacking(ctx); len(l.Installations) != 1 || l.Installations[0].URL != "" {
		t.Errorf("lacking = %+v", l)
	}
}
