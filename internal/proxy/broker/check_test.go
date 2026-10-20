package broker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheck(t *testing.T) {
	sf := filepath.Join(t.TempDir(), "secrets.json")
	for _, c := range []struct {
		name, secrets, want string
	}{
		{"bound", goodSecrets, ""},
		{"unbound", `{"secrets": [{"name": "do", "kind": "static", "value": "x", "hosts": ["example.com"]}]}`, "not bound"},
		{"missing", `{"secrets": []}`, "do"},
		{"unreadable file", `{"secrets": [{"name": "do", "kind": "file", "file": "/nonexistent", "hosts": ["api.digitalocean.com"]}]}`, "nonexistent"},
	} {
		os.WriteFile(sf, []byte(c.secrets), 0o600)
		err := Check(policy(t), sf, "")
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
}

// The app's secrets count as the file's do, and can't stand in for one.
func TestCheckAppSecrets(t *testing.T) {
	dir := t.TempDir()
	sf, af := filepath.Join(dir, "secrets.json"), filepath.Join(dir, "app-secrets.json")
	os.WriteFile(sf, []byte(`{"secrets": []}`), 0o600)
	os.WriteFile(af, []byte(`{"key":"k","secrets":[{"name":"do","kind":"static","value":"x","hosts":["api.digitalocean.com"],"added_by":"z","added":"2026-10-19T00:00:00Z"}]}`), 0o600)
	if err := Check(policy(t), sf, af); err != nil {
		t.Fatal(err)
	}
	if err := Check(policy(t), sf, filepath.Join(dir, "none.json")); err == nil {
		t.Fatal("missing secret passed")
	}
	os.WriteFile(sf, []byte(goodSecrets), 0o600)
	if err := Check(policy(t), sf, af); err == nil || !strings.Contains(err.Error(), "in the app") {
		t.Fatalf("a name in both: %v", err)
	}
}
