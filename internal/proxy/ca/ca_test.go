package ca

import (
	"crypto/x509"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
)

func TestNewIsACertificateAuthority(t *testing.T) {
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	c := a.cert
	if !c.IsCA || !c.BasicConstraintsValid || c.MaxPathLen != 0 || !c.MaxPathLenZero {
		t.Errorf("not a constrained CA: IsCA=%v MaxPathLen=%d", c.IsCA, c.MaxPathLen)
	}
	if c.KeyUsage&x509.KeyUsageCertSign == 0 || c.KeyUsage&x509.KeyUsageCRLSign == 0 {
		t.Errorf("key usage %v lacks CertSign|CRLSign", c.KeyUsage)
	}
	if c.Subject.CommonName != "Skybreaker broker CA" {
		t.Errorf("CN %q", c.Subject.CommonName)
	}
	if life := c.NotAfter.Sub(c.NotBefore); life < 9*365*24*time.Hour {
		t.Errorf("CA lifetime %v, want ~10y", life)
	}
	if len(a.CertPEM()) == 0 {
		t.Error("empty CertPEM")
	}
}

func TestLeafVerifiesForItsHostOnly(t *testing.T) {
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := a.Leaf("api.github.com")
	if err != nil {
		t.Fatal(err)
	}
	if leaf.Leaf == nil {
		t.Fatal("tls.Certificate.Leaf not populated")
	}
	if _, err := leaf.Leaf.Verify(x509.VerifyOptions{DNSName: "api.github.com", Roots: a.Pool()}); err != nil {
		t.Errorf("leaf does not verify for its host: %v", err)
	}
	if _, err := leaf.Leaf.Verify(x509.VerifyOptions{DNSName: "evil.example.com", Roots: a.Pool()}); err == nil {
		t.Error("leaf verified for a different host")
	}
	other, _ := New()
	if _, err := leaf.Leaf.Verify(x509.VerifyOptions{DNSName: "api.github.com", Roots: other.Pool()}); err == nil {
		t.Error("leaf verified against another CA")
	}
	if leaf.Leaf.IsCA {
		t.Error("leaf must not be a CA")
	}
	life := leaf.Leaf.NotAfter.Sub(leaf.Leaf.NotBefore)
	if life < 7*24*time.Hour || life > 7*24*time.Hour+2*time.Hour {
		t.Errorf("leaf lifetime %v, want 7d plus 1h backdate", life)
	}
}

func TestLeafForIPLiteralUsesIPSAN(t *testing.T) {
	a, _ := New()
	for _, host := range []string{"203.0.113.7", "2001:db8::1", "[2001:db8::1]"} {
		leaf, err := a.Leaf(host)
		if err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		if len(leaf.Leaf.IPAddresses) != 1 || len(leaf.Leaf.DNSNames) != 0 {
			t.Errorf("%s: IPs %v DNS %v, want one IP SAN only", host, leaf.Leaf.IPAddresses, leaf.Leaf.DNSNames)
		}
		ip := net.ParseIP(trimBrackets(host))
		if !leaf.Leaf.IPAddresses[0].Equal(ip) {
			t.Errorf("%s: SAN %v", host, leaf.Leaf.IPAddresses[0])
		}
	}
}

func TestLeafRejectsBadHosts(t *testing.T) {
	a, _ := New()
	for _, host := range []string{"", "a b", "exa\x00mple.com", "host:443", "*.example.com"} {
		if _, err := a.Leaf(host); err == nil {
			t.Errorf("Leaf(%q) succeeded", host)
		}
	}
}

func TestLeafIsCachedAndNormalised(t *testing.T) {
	a, _ := New()
	l1, _ := a.Leaf("Example.COM.")
	l2, _ := a.Leaf("example.com")
	if l1 != l2 {
		t.Error("same host (case, trailing dot) produced different certificates")
	}
	if l1.Leaf.DNSNames[0] != "example.com" {
		t.Errorf("SAN %q", l1.Leaf.DNSNames[0])
	}
}

func TestLeafReissuedNearExpiry(t *testing.T) {
	a, _ := New()
	now := time.Now()
	a.now = func() time.Time { return now }
	l1, _ := a.Leaf("example.com")
	a.now = func() time.Time { return now.Add(6*24*time.Hour + time.Hour) } // < 24h left
	l2, _ := a.Leaf("example.com")
	if l1 == l2 {
		t.Error("leaf with < 24h left was not reissued")
	}
}

func TestLeafCacheIsBounded(t *testing.T) {
	a, _ := New()
	for i := 0; i < maxLeaves+10; i++ {
		if _, err := a.Leaf(net.IPv4(10, 0, byte(i>>8), byte(i)).String()); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(a.leaves); n > maxLeaves {
		t.Errorf("cache holds %d leaves, max %d", n, maxLeaves)
	}
}

func TestLeafConcurrent(t *testing.T) {
	a, _ := New()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := a.Leaf("concurrent.example.com"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}

func TestLoadOrCreatePersists(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ca")
	a1, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v, want 0700", st.Mode().Perm())
	}
	kst, _ := os.Stat(filepath.Join(dir, "ca.key"))
	if kst.Mode().Perm() != 0o600 {
		t.Errorf("key mode %v, want 0600", kst.Mode().Perm())
	}
	a2, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if string(a1.CertPEM()) != string(a2.CertPEM()) {
		t.Error("second load produced a different CA")
	}
	leaf, _ := a2.Leaf("example.com")
	if _, err := leaf.Leaf.Verify(x509.VerifyOptions{DNSName: "example.com", Roots: a1.Pool()}); err != nil {
		t.Errorf("reloaded CA signs leaves the original trusts: %v", err)
	}
}

func TestLoadOrCreateRefusesReadableKey(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadOrCreate(dir); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []os.FileMode{0o644, 0o640, 0o604} {
		os.Chmod(filepath.Join(dir, "ca.key"), mode)
		if _, err := LoadOrCreate(dir); err == nil {
			t.Errorf("loaded a CA key with mode %v", mode)
		}
	}
}

func TestLoadOrCreateRefusesMismatchedPair(t *testing.T) {
	d1, d2 := t.TempDir(), t.TempDir()
	LoadOrCreate(d1)
	LoadOrCreate(d2)
	crt, _ := os.ReadFile(filepath.Join(d2, "ca.crt"))
	os.WriteFile(filepath.Join(d1, "ca.crt"), crt, 0o644)
	if _, err := LoadOrCreate(d1); err == nil {
		t.Error("loaded a certificate that does not match the key")
	}
}

func TestLoadOrCreateRefusesHalfPresent(t *testing.T) {
	dir := t.TempDir()
	LoadOrCreate(dir)
	os.Remove(filepath.Join(dir, "ca.crt"))
	if _, err := LoadOrCreate(dir); err == nil {
		t.Error("silently replaced a CA whose certificate is missing")
	}
}

func TestCrashDuringCreateLeavesNoHalfCA(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "ca")
	if err := os.Mkdir(dir, 0o700); err != nil { // systemd's StateDirectory may make it first
		t.Fatal(err)
	}
	orig := publish
	publish = func(string, string) error { return errors.New("power cut") }
	_, err := LoadOrCreate(dir)
	publish = orig
	if err == nil {
		t.Fatal("create succeeded although the CA was never published")
	}
	if names := dirNames(t, dir); len(names) != 0 {
		t.Fatalf("an unpublished CA left %v in place", names)
	}

	a, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("restart after the crash: %v", err)
	}
	if names := dirNames(t, dir); !slices.Equal(names, []string{"ca.crt", "ca.key"}) {
		t.Errorf("ca dir holds %v", names)
	}
	if names := dirNames(t, parent); !slices.Equal(names, []string{"ca"}) {
		t.Errorf("the crashed attempt's key was left behind: %v", names)
	}
	if b, _ := LoadOrCreate(dir); b == nil || string(b.CertPEM()) != string(a.CertPEM()) {
		t.Error("the CA did not persist")
	}
}

func TestCreateRefusesNonEmptyDir(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600)
	if _, err := LoadOrCreate(dir); err == nil {
		t.Error("created a CA over a directory that already holds files")
	}
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}
