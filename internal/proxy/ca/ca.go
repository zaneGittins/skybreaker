// Package ca is the broker's certificate authority for mitm hosts. It mints
// short-lived leaf certificates on demand. The private key stays in
// Skybreaker's data directory, which no sandbox mounts; sandboxes trust only
// the public certificate.
package ca

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	leafLifetime = 7 * 24 * time.Hour
	leafBackdate = time.Hour
	// Leaves are reissued once less than this is left, so a cached
	// certificate never expires mid-connection.
	leafRenewBefore = 24 * time.Hour
	maxLeaves       = 1024
)

type Authority struct {
	cert    *x509.Certificate
	key     crypto.Signer
	certPEM []byte
	now     func() time.Time

	mu     sync.Mutex
	leaves map[string]*tls.Certificate
}

// New creates an in-memory authority.
func New() (*Authority, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "Skybreaker broker CA", Organization: []string{"Skybreaker"}},
		NotBefore:             now.Add(-leafBackdate),
		NotAfter:              now.AddDate(10, 0, 0),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return nil, err
	}
	return fromDER(der, key)
}

func fromDER(der []byte, key crypto.Signer) (*Authority, error) {
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &Authority{
		cert:    cert,
		key:     key,
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		now:     time.Now,
		leaves:  map[string]*tls.Certificate{},
	}, nil
}

// LoadOrCreate reads dir/ca.key and dir/ca.crt, or creates both when neither
// exists. A key readable by group or others is refused: anyone who can read
// it can impersonate every mitm host.
func LoadOrCreate(dir string) (*Authority, error) {
	keyPath, crtPath := filepath.Join(dir, "ca.key"), filepath.Join(dir, "ca.crt")
	_, kerr := os.Stat(keyPath)
	_, cerr := os.Stat(crtPath)
	switch {
	case errors.Is(kerr, fs.ErrNotExist) && errors.Is(cerr, fs.ErrNotExist):
		return create(dir, keyPath, crtPath)
	case kerr != nil:
		return nil, fmt.Errorf("ca: %w", kerr)
	case cerr != nil:
		return nil, fmt.Errorf("ca: %w", cerr)
	}
	return load(keyPath, crtPath)
}

// publish moves a fully written CA into place. It is rename(2) itself,
// because os.Rename refuses any existing directory, even an empty one. A
// variable so tests can simulate a crash just before it.
var publish = syscall.Rename

// create writes the key and certificate into a fresh sibling directory and
// renames it over dir, so a crash leaves either a whole CA or none, never a
// key without its certificate. rename(2) replaces only an empty directory,
// so a CA another process just published is never overwritten.
func create(dir, keyPath, crtPath string) (*Authority, error) {
	parent, base := filepath.Split(filepath.Clean(dir))
	if parent == "" {
		parent = "."
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, fmt.Errorf("ca: %w", err)
	}
	// An attempt that crashed left its never-published key here.
	stale, _ := filepath.Glob(filepath.Join(parent, "."+base+".new-*"))
	for _, p := range stale {
		os.RemoveAll(p)
	}
	a, err := New()
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(a.key)
	if err != nil {
		return nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	tmp, err := os.MkdirTemp(parent, "."+base+".new-") // mode 0700
	if err != nil {
		return nil, fmt.Errorf("ca: %w", err)
	}
	defer os.RemoveAll(tmp) // a no-op once published
	if err := writeSynced(filepath.Join(tmp, filepath.Base(keyPath)), keyPEM, 0o600); err != nil {
		return nil, err
	}
	if err := writeSynced(filepath.Join(tmp, filepath.Base(crtPath)), a.certPEM, 0o644); err != nil {
		return nil, err
	}
	if err := publish(tmp, dir); err != nil {
		return nil, fmt.Errorf("ca: publishing %s (it must be empty or absent): %w", dir, err)
	}
	if d, err := os.Open(parent); err == nil {
		d.Sync()
		d.Close()
	}
	return a, nil
}

func writeSynced(path string, b []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("ca: %w", err)
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("ca: %w", err)
	}
	return nil
}

func load(keyPath, crtPath string) (*Authority, error) {
	st, err := os.Stat(keyPath)
	if err != nil {
		return nil, fmt.Errorf("ca: %w", err)
	}
	if st.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("ca: %s has mode %v; it must not be readable by group or others (chmod 600)", keyPath, st.Mode().Perm())
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("ca: %w", err)
	}
	kb, _ := pem.Decode(keyPEM)
	if kb == nil {
		return nil, fmt.Errorf("ca: %s is not PEM", keyPath)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(kb.Bytes)
	if err != nil {
		return nil, fmt.Errorf("ca: %s: %w", keyPath, err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("ca: %s is not an EC key", keyPath)
	}
	crtPEM, err := os.ReadFile(crtPath)
	if err != nil {
		return nil, fmt.Errorf("ca: %w", err)
	}
	cb, _ := pem.Decode(crtPEM)
	if cb == nil || cb.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("ca: %s is not a PEM certificate", crtPath)
	}
	a, err := fromDER(cb.Bytes, key)
	if err != nil {
		return nil, fmt.Errorf("ca: %s: %w", crtPath, err)
	}
	pub, ok := a.cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || !pub.Equal(&key.PublicKey) {
		return nil, fmt.Errorf("ca: %s does not match %s", crtPath, keyPath)
	}
	if !a.cert.IsCA {
		return nil, fmt.Errorf("ca: %s is not a CA certificate", crtPath)
	}
	return a, nil
}

func (a *Authority) CertPEM() []byte { return append([]byte(nil), a.certPEM...) }

func (a *Authority) Pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(a.cert)
	return p
}

// Leaf returns a certificate for host, from the cache while it has more than
// a day left.
func (a *Authority) Leaf(host string) (*tls.Certificate, error) {
	host, ip, err := normalise(host)
	if err != nil {
		return nil, err
	}
	now := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()
	if c, ok := a.leaves[host]; ok && c.Leaf.NotAfter.Sub(now) > leafRenewBefore {
		return c, nil
	}
	c, err := a.issue(host, ip, now)
	if err != nil {
		return nil, err
	}
	if _, ok := a.leaves[host]; !ok && len(a.leaves) >= maxLeaves {
		a.evict(now)
	}
	a.leaves[host] = c
	return c, nil
}

// evict drops expiring leaves, or failing that the one expiring soonest.
func (a *Authority) evict(now time.Time) {
	var oldest string
	for h, c := range a.leaves {
		if c.Leaf.NotAfter.Sub(now) <= leafRenewBefore {
			delete(a.leaves, h)
			continue
		}
		if oldest == "" || c.Leaf.NotAfter.Before(a.leaves[oldest].Leaf.NotAfter) {
			oldest = h
		}
	}
	if len(a.leaves) >= maxLeaves && oldest != "" {
		delete(a.leaves, oldest)
	}
}

func (a *Authority) issue(host string, ip net.IP, now time.Time) (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: host},
		NotBefore:             now.Add(-leafBackdate),
		NotAfter:              now.Add(leafLifetime),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	if ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, a.cert, key.Public(), a.key)
	if err != nil {
		return nil, fmt.Errorf("ca: issue %s: %w", host, err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{Certificate: [][]byte{der, a.cert.Raw}, PrivateKey: key, Leaf: leaf}, nil
}

// normalise lowercases host, drops a trailing dot and IPv6 brackets, and
// rejects anything that isn't a plain DNS name or IP literal.
func normalise(host string) (string, net.IP, error) {
	h := strings.TrimSuffix(strings.ToLower(trimBrackets(host)), ".")
	if ip := net.ParseIP(h); ip != nil {
		return ip.String(), ip, nil
	}
	if h == "" || len(h) > 253 {
		return "", nil, fmt.Errorf("ca: invalid host %q", host)
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 {
			return "", nil, fmt.Errorf("ca: invalid host %q", host)
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
				return "", nil, fmt.Errorf("ca: invalid host %q", host)
			}
		}
	}
	return h, nil, nil
}

func trimBrackets(h string) string {
	if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
		return h[1 : len(h)-1]
	}
	return h
}

func serial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return n
}
