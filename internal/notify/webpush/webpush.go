// Package webpush sends notifications straight to the browsers that asked
// for them (the Web Push protocol: RFC 8030, encrypted per RFC 8291, sent
// with a VAPID key per RFC 8292). The push service in between, Apple's for
// an iPhone, sees only ciphertext. Payloads stay as content-free as the
// rest of package notify's: a title, a line and a link.
package webpush

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/zaneGittins/skybreaker/internal/notify"
)

var randReader = rand.Reader

// Subscription is what a browser's PushManager hands back: where to send,
// and its keys.
type Subscription struct {
	Endpoint string    `json:"endpoint"`
	P256dh   string    `json:"p256dh"`
	Auth     string    `json:"auth"`
	Added    time.Time `json:"added,omitzero"`
}

// Service holds the VAPID key and the subscriptions, under dir.
type Service struct {
	dir     string
	subject string
	key     *ecdsa.PrivateKey
	HTTP    *http.Client
	allow   func(host string) bool

	mu   sync.Mutex
	subs []Subscription
}

// pushHosts are the browser vendors' push services. Endpoints come from
// the browser; anywhere else is refused, so a subscription can't aim the
// control plane at an arbitrary host.
func pushHost(host string) bool {
	for _, suffix := range []string{".push.apple.com", ".notify.windows.com"} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return slices.Contains([]string{"fcm.googleapis.com", "updates.push.services.mozilla.com", "push.services.mozilla.com"}, host)
}

// Open loads (or makes) the VAPID key and the saved subscriptions. subject
// identifies the sender to push services: the UI's URL.
func Open(dir, subject string) (*Service, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Service{dir: dir, subject: subject, HTTP: &http.Client{Timeout: 15 * time.Second}, allow: pushHost}
	keyPath := filepath.Join(dir, "vapid.pem")
	b, err := os.ReadFile(keyPath)
	switch {
	case err == nil:
		blk, _ := pem.Decode(b)
		if blk == nil {
			return nil, fmt.Errorf("webpush: %s is not PEM", keyPath)
		}
		if s.key, err = x509.ParseECPrivateKey(blk.Bytes); err != nil {
			return nil, fmt.Errorf("webpush: %s: %w", keyPath, err)
		}
	case errors.Is(err, os.ErrNotExist):
		if s.key, err = ecdsa.GenerateKey(elliptic.P256(), randReader); err != nil {
			return nil, err
		}
		der, _ := x509.MarshalECPrivateKey(s.key)
		if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600); err != nil {
			return nil, err
		}
	default:
		return nil, err
	}
	if b, err := os.ReadFile(filepath.Join(dir, "subscriptions.json")); err == nil {
		json.Unmarshal(b, &s.subs)
	}
	return s, nil
}

// PublicKey is the VAPID public key for PushManager.subscribe, base64url.
func (s *Service) PublicKey() string {
	pub, _ := s.key.PublicKey.ECDH()
	return base64.RawURLEncoding.EncodeToString(pub.Bytes())
}

func (s *Service) save() error {
	b, _ := json.MarshalIndent(s.subs, "", " ")
	tmp := filepath.Join(s.dir, ".subscriptions.json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.dir, "subscriptions.json"))
}

// Subscribe adds (or refreshes) a browser's subscription.
func (s *Service) Subscribe(sub Subscription) error {
	u, err := url.Parse(sub.Endpoint)
	if err != nil || u.Scheme != "https" || u.User != nil || !s.allow(u.Hostname()) || (u.Port() != "" && pushHost(u.Hostname())) {
		return errors.New("webpush: not a known push service")
	}
	if k, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(sub.P256dh, "=")); err != nil || len(k) != 65 {
		return errors.New("webpush: bad p256dh key")
	}
	if a, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(sub.Auth, "=")); err != nil || len(a) != 16 {
		return errors.New("webpush: bad auth secret")
	}
	sub.P256dh, sub.Auth = strings.TrimRight(sub.P256dh, "="), strings.TrimRight(sub.Auth, "=")
	sub.Added = time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subs = slices.DeleteFunc(s.subs, func(x Subscription) bool { return x.Endpoint == sub.Endpoint })
	s.subs = append(s.subs, sub)
	if len(s.subs) > 20 {
		s.subs = s.subs[len(s.subs)-20:]
	}
	return s.save()
}

func (s *Service) Unsubscribe(endpoint string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subs = slices.DeleteFunc(s.subs, func(x Subscription) bool { return x.Endpoint == endpoint })
	return s.save()
}

func (s *Service) Subscriptions() []Subscription {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.subs)
}

// Notify sends m to every subscription. One whose push service says it's
// gone (404, 410) is dropped.
func (s *Service) Notify(ctx context.Context, m notify.Message) error {
	tag := m.Click
	if tag == "" {
		tag = m.ID
	}
	payload, _ := json.Marshal(map[string]string{"title": m.Title, "body": m.Body, "url": m.Click, "tag": tag})
	var errs []error
	for _, sub := range s.Subscriptions() {
		gone, err := s.send(ctx, sub, payload, m.Priority >= 4)
		if gone {
			s.Unsubscribe(sub.Endpoint)
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Service) send(ctx context.Context, sub Subscription, payload []byte, urgent bool) (gone bool, err error) {
	u, err := url.Parse(sub.Endpoint)
	if err != nil || u.Scheme != "https" || !s.allow(u.Hostname()) {
		return true, errors.New("webpush: bad endpoint")
	}
	asPriv, err := ecdh.P256().GenerateKey(randReader)
	if err != nil {
		return false, err
	}
	salt := make([]byte, 16)
	randReader.Read(salt)
	p256dh, _ := base64.RawURLEncoding.DecodeString(sub.P256dh)
	auth, _ := base64.RawURLEncoding.DecodeString(sub.Auth)
	body, err := encrypt(payload, p256dh, auth, asPriv, salt)
	if err != nil {
		return true, err
	}
	jwt, err := s.vapid(u.Scheme + "://" + u.Host)
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("TTL", "86400")
	req.Header.Set("Urgency", map[bool]string{true: "high", false: "normal"}[urgent])
	req.Header.Set("Authorization", "vapid t="+jwt+", k="+s.PublicKey())
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return false, fmt.Errorf("webpush: %w", err)
	}
	resp.Body.Close()
	switch {
	case resp.StatusCode == 404 || resp.StatusCode == 410:
		return true, nil
	case resp.StatusCode/100 != 2:
		return false, fmt.Errorf("webpush: %s: status %d", u.Host, resp.StatusCode)
	}
	return false, nil
}

// vapid signs the RFC 8292 token for one push service.
func (s *Service) vapid(aud string) (string, error) {
	enc := base64.RawURLEncoding
	head := enc.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, _ := json.Marshal(map[string]any{"aud": aud, "exp": time.Now().Add(12 * time.Hour).Unix(), "sub": s.subject})
	signing := head + "." + enc.EncodeToString(claims)
	h := sha256.Sum256([]byte(signing))
	r, sv, err := ecdsa.Sign(randReader, s.key, h[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	sv.FillBytes(sig[32:])
	return signing + "." + enc.EncodeToString(sig), nil
}

// encrypt is RFC 8291's aes128gcm content coding, in one record.
func encrypt(plain, uaPublic, authSecret []byte, asPriv *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	ua, err := ecdh.P256().NewPublicKey(uaPublic)
	if err != nil {
		return nil, fmt.Errorf("webpush: browser key: %w", err)
	}
	secret, err := asPriv.ECDH(ua)
	if err != nil {
		return nil, err
	}
	asPublic := asPriv.PublicKey().Bytes()
	prkKey, err := hkdf.Extract(sha256.New, secret, authSecret)
	if err != nil {
		return nil, err
	}
	ikm, err := hkdf.Expand(sha256.New, prkKey, "WebPush: info\x00"+string(uaPublic)+string(asPublic), 32)
	if err != nil {
		return nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	// The last (only) record ends with the 0x02 delimiter.
	sealed := gcm.Seal(nil, nonce, append(append([]byte(nil), plain...), 2), nil)
	head := make([]byte, 0, 21+len(asPublic))
	head = append(head, salt...)
	head = binary.BigEndian.AppendUint32(head, 4096)
	head = append(head, byte(len(asPublic)))
	head = append(head, asPublic...)
	return append(head, sealed...), nil
}

// decrypt is the browser's side of encrypt, for tests.
func decrypt(body []byte, uaPriv *ecdh.PrivateKey, authSecret []byte) ([]byte, error) {
	if len(body) < 21 {
		return nil, errors.New("short")
	}
	salt, idlen := body[:16], int(body[20])
	if len(body) < 21+idlen {
		return nil, errors.New("short")
	}
	asPublic := body[21 : 21+idlen]
	as, err := ecdh.P256().NewPublicKey(asPublic)
	if err != nil {
		return nil, err
	}
	secret, err := uaPriv.ECDH(as)
	if err != nil {
		return nil, err
	}
	prkKey, _ := hkdf.Extract(sha256.New, secret, authSecret)
	ikm, _ := hkdf.Expand(sha256.New, prkKey, "WebPush: info\x00"+string(uaPriv.PublicKey().Bytes())+string(asPublic), 32)
	prk, _ := hkdf.Extract(sha256.New, ikm, salt)
	cek, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, body[21+idlen:], nil)
	if err != nil {
		return nil, err
	}
	return bytes.TrimRight(plain[:len(plain)-1], "\x00"), nil
}
