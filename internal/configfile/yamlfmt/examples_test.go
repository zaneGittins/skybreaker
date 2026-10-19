//go:build !nodeps

package yamlfmt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zaneGittins/skybreaker/internal/configfile"
	"github.com/zaneGittins/skybreaker/internal/control/config"
	"github.com/zaneGittins/skybreaker/internal/proxy/ca"
	"github.com/zaneGittins/skybreaker/internal/proxy/secrets"
)

// The shipped examples must load, and every secret they inject must be bound
// to the host it goes to.
func TestExamples(t *testing.T) {
	// The example names a cluster CA that exists only on a real host; point
	// it at a real certificate so the file check runs.
	src, err := os.ReadFile("../../../deploy/skybreaker.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	const caPath = "/etc/skybreaker/k8s-prod-ca.crt"
	if !strings.Contains(string(src), caPath) {
		t.Fatalf("example no longer names %s; update this test", caPath)
	}
	authority, err := ca.New()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile := filepath.Join(dir, "cluster-ca.crt")
	os.WriteFile(certFile, authority.CertPEM(), 0o644)
	cfgYAML := filepath.Join(dir, "skybreaker.yaml")
	os.WriteFile(cfgYAML, []byte(strings.ReplaceAll(string(src), caPath, certFile)), 0o600)
	cfg, err := config.Load(cfgYAML)
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Secrets []secrets.Spec `json:"secrets"`
	}
	if err := configfile.Load("../../../deploy/secrets.example.yaml", &f); err != nil {
		t.Fatal(err)
	}
	hosts := map[string][]string{}
	for _, s := range f.Secrets {
		hosts[s.Name] = s.Hosts
	}
	if err := cfg.Policy.CheckSecrets(func(n string) ([]string, bool) { h, ok := hosts[n]; return h, ok }); err != nil {
		t.Fatal(err)
	}
}
