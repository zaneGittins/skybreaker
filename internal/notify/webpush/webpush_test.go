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
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zaneGittins/skybreaker/internal/notify"
)

func b64(s string) []byte {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

// RFC 8291, Appendix A.
func TestEncryptMatchesRFC8291(t *testing.T) {
	asPriv, err := ecdh.P256().NewPrivateKey(b64("yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := encrypt([]byte("When I grow up, I want to be a watermelon"),
		b64("BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"),
		b64("BTBZMqHH6r4Tts7J_aSIgg"), asPriv, b64("DGv6ra1nlYgDCS1FRnbzlw"))
	if err != nil {
		t.Fatal(err)
	}
	want := "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
	if g := base64.RawURLEncoding.EncodeToString(got); g != want {
		t.Errorf("body\n got %s\nwant %s", g, want)
	}
}

func newService(t *testing.T) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir, "https://skybreaker.example.ts.net")
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func browserKeys(t *testing.T) (*ecdh.PrivateKey, Subscription) {
	t.Helper()
	k, _ := ecdh.P256().GenerateKey(randReader)
	auth := make([]byte, 16)
	randReader.Read(auth)
	return k, Subscription{
		Endpoint: "https://web.push.apple.com/QGuQyavXutnMH9",
		P256dh:   base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes()),
		Auth:     base64.RawURLEncoding.EncodeToString(auth),
	}
}

func TestKeyPersists(t *testing.T) {
	s, dir := newService(t)
	key := s.PublicKey()
	if len(b64(key)) != 65 {
		t.Fatalf("public key %q", key)
	}
	s2, err := Open(dir, "https://x")
	if err != nil || s2.PublicKey() != key {
		t.Errorf("reopened key %q, %v", s2.PublicKey(), err)
	}
	info, _ := os.Stat(filepath.Join(dir, "vapid.pem"))
	if info.Mode().Perm() != 0o600 {
		t.Errorf("key file mode %v", info.Mode())
	}
}

func TestSubscribeValidates(t *testing.T) {
	s, dir := newService(t)
	_, good := browserKeys(t)
	for _, bad := range []Subscription{
		{Endpoint: "http://web.push.apple.com/x", P256dh: good.P256dh, Auth: good.Auth},
		{Endpoint: "https://evil.example.com/x", P256dh: good.P256dh, Auth: good.Auth},
		{Endpoint: "https://web.push.apple.com.evil.com/x", P256dh: good.P256dh, Auth: good.Auth},
		{Endpoint: "https://169.254.169.254/x", P256dh: good.P256dh, Auth: good.Auth},
		{Endpoint: good.Endpoint, P256dh: "short", Auth: good.Auth},
		{Endpoint: good.Endpoint, P256dh: good.P256dh, Auth: "AAAA"},
	} {
		if err := s.Subscribe(bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	if err := s.Subscribe(good); err != nil {
		t.Fatal(err)
	}
	s.Subscribe(good) // again: no duplicate
	s2, _ := Open(dir, "https://x")
	if n := len(s2.Subscriptions()); n != 1 {
		t.Errorf("%d subscriptions after reopen", n)
	}
	s2.Unsubscribe(good.Endpoint)
	if n := len(s2.Subscriptions()); n != 0 {
		t.Errorf("%d after unsubscribe", n)
	}
}

func TestNotifySendsEncryptedVAPID(t *testing.T) {
	s, _ := newService(t)
	priv, sub := browserKeys(t)
	var got struct {
		auth, enc, ttl string
		body           []byte
		host           string
	}
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.auth, got.enc, got.ttl, got.host = r.Header.Get("Authorization"), r.Header.Get("Content-Encoding"), r.Header.Get("TTL"), r.Host
		got.body, _ = io.ReadAll(r.Body)
		w.WriteHeader(201)
	}))
	defer ts.Close()
	s.HTTP = ts.Client()
	s.allow = func(string) bool { return true }
	u, _ := url.Parse(ts.URL)
	sub.Endpoint = ts.URL + "/push/abc"
	if err := s.Subscribe(sub); err != nil {
		t.Fatal(err)
	}
	err := s.Notify(context.Background(), notify.Message{Title: "Approval waiting", Body: "A claude session in orbit is waiting for you.", Click: "https://skybreaker.example.ts.net/s/sabc"})
	if err != nil {
		t.Fatal(err)
	}
	if got.enc != "aes128gcm" || got.ttl == "" {
		t.Errorf("headers enc %q ttl %q", got.enc, got.ttl)
	}
	// The VAPID token is for this push service, signed by our key.
	tok, k, ok := strings.Cut(strings.TrimPrefix(got.auth, "vapid t="), ", k=")
	if !ok || k != s.PublicKey() {
		t.Fatalf("authorization %q", got.auth)
	}
	parts := strings.Split(tok, ".")
	var claims struct {
		Aud string `json:"aud"`
		Sub string `json:"sub"`
		Exp int64  `json:"exp"`
	}
	json.Unmarshal(b64(parts[1]), &claims)
	if claims.Aud != "https://"+u.Host || claims.Sub != "https://skybreaker.example.ts.net" || claims.Exp == 0 {
		t.Errorf("claims %+v", claims)
	}
	pub := b64(k)
	x, y := elliptic.Unmarshal(elliptic.P256(), pub)
	sig := b64(parts[2])
	h := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(&ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, h[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Error("VAPID signature doesn't verify")
	}
	// The browser can decrypt it.
	plain, err := decrypt(got.body, priv, b64(sub.Auth))
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]string
	json.Unmarshal(plain, &payload)
	if payload["title"] != "Approval waiting" || payload["url"] != "https://skybreaker.example.ts.net/s/sabc" || payload["tag"] == "" {
		t.Errorf("payload %s", plain)
	}
}

func TestGoneSubscriptionsAreDropped(t *testing.T) {
	s, _ := newService(t)
	_, sub := browserKeys(t)
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(410) }))
	defer ts.Close()
	s.HTTP, s.allow = ts.Client(), func(string) bool { return true }
	sub.Endpoint = ts.URL + "/gone"
	s.Subscribe(sub)
	s.Notify(context.Background(), notify.Message{Title: "x"})
	if n := len(s.Subscriptions()); n != 0 {
		t.Errorf("gone subscription kept: %d", n)
	}
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
