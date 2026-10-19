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
		err := Check(policy(t), sf)
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
}
