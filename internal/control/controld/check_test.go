package controld

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheck(t *testing.T) {
	dir := t.TempDir()
	secrets := filepath.Join(dir, "secrets.json")
	os.WriteFile(secrets, []byte(`{"secrets": [{"name": "do", "kind": "static", "value": "x", "hosts": ["example.com"]}]}`), 0o600)
	cfg := fmt.Sprintf(`{
	  "data_dir": %q, "users": ["zane@example.com"], "secrets_file": %q,
	  "sandbox": {"image": "img"},
	  "projects": {"orbit": {"allow": [{"host": "api.digitalocean.com", "mode": "mitm", "inject": {"bearer": "do"}, "methods": ["GET"]}]}}
	}`, dir, secrets)
	p := filepath.Join(dir, "skybreaker.json")
	os.WriteFile(p, []byte(cfg), 0o600)
	// A secret sent to a host it isn't bound to fails here, before serve.
	if err := Check(p); err == nil || !strings.Contains(err.Error(), "not bound") {
		t.Fatalf("err = %v", err)
	}
	os.WriteFile(secrets, []byte(`{"secrets": [{"name": "do", "kind": "static", "value": "x", "hosts": ["api.digitalocean.com"]}]}`), 0o600)
	if err := Check(p); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(p, []byte(`{"users": []}`), 0o600)
	if err := Check(p); err == nil {
		t.Fatal("an invalid config passed")
	}
}
