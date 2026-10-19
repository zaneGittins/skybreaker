package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeURL(t *testing.T) {
	for in, want := range map[string]string{
		"skybreaker.tail1234.ts.net":          "https://skybreaker.tail1234.ts.net",
		"https://skybreaker.tail1234.ts.net/": "https://skybreaker.tail1234.ts.net",
		"http://127.0.0.1:7700":               "http://127.0.0.1:7700",
	} {
		if got, err := NormalizeURL(in); err != nil || got != want {
			t.Errorf("NormalizeURL(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "ftp://x", "https://user:pw@x", "https://x/api", "https://x?q=1", "https://"} {
		if got, err := NormalizeURL(bad); err == nil {
			t.Errorf("NormalizeURL(%q) = %q, want an error", bad, got)
		}
	}
}

func TestConfigRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".skybreaker", "config.json")
	if c, err := LoadConfig(path); err != nil || c.URL != "" {
		t.Fatalf("missing config: %+v %v", c, err)
	}
	if err := SaveConfig(path, Config{URL: "https://skybreaker.tail1234.ts.net"}); err != nil {
		t.Fatal(err)
	}
	if c, err := LoadConfig(path); err != nil || c.URL != "https://skybreaker.tail1234.ts.net" {
		t.Fatalf("loaded %+v %v", c, err)
	}
	fi, _ := os.Stat(path)
	di, _ := os.Stat(filepath.Dir(path))
	if fi.Mode().Perm() != 0o600 || di.Mode().Perm() != 0o700 {
		t.Fatalf("modes %v %v", fi.Mode().Perm(), di.Mode().Perm())
	}
	os.WriteFile(path, []byte("{nope"), 0o600)
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("bad JSON accepted")
	}
}

const status = `{"MagicDNSSuffix":"tail1234.ts.net",
 "Self":{"HostName":"laptop","DNSName":"laptop.tail1234.ts.net."},
 "Peer":{
  "a":{"HostName":"skybreaker-host","DNSName":"skybreaker-host.tail1234.ts.net.","Online":true},
  "b":{"HostName":"skybreaker","DNSName":"skybreaker.tail1234.ts.net.","Online":true}}}`

func TestDiscover(t *testing.T) {
	ctx := context.Background()
	run := func(out string, err error) Runner {
		return func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if !strings.Contains(strings.ToLower(name), "tailscale") || strings.Join(args, " ") != "status --json" {
				t.Fatalf("ran %s %v", name, args)
			}
			return []byte(out), err
		}
	}
	if u, err := Discover(ctx, run(status, nil)); err != nil || u != "https://skybreaker.tail1234.ts.net" {
		t.Fatalf("discover: %q %v", u, err)
	}
	none := `{"MagicDNSSuffix":"tail1234.ts.net","Peer":{"a":{"HostName":"skybreaker-host","DNSName":"skybreaker-host.tail1234.ts.net."}}}`
	if _, err := Discover(ctx, run(none, nil)); err == nil || !strings.Contains(err.Error(), "skybreaker connect") {
		t.Fatalf("no machine: %v", err)
	}
	if _, err := Discover(ctx, run("", errors.New("not found"))); err == nil || !strings.Contains(err.Error(), "skybreaker connect") {
		t.Fatalf("no tailscale: %v", err)
	}
}

func TestResolveOrder(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "config.json")
	disc := func(ctx context.Context, name string, args ...string) ([]byte, error) { return []byte(status), nil }

	// Nothing saved: discovered, then remembered.
	u, err := Resolve(ctx, path, "", disc)
	if err != nil || u != "https://skybreaker.tail1234.ts.net" {
		t.Fatalf("discovered %q %v", u, err)
	}
	if c, _ := LoadConfig(path); c.URL != u {
		t.Fatalf("not saved: %+v", c)
	}
	// Saved beats discovery; the environment beats both.
	SaveConfig(path, Config{URL: "https://other.tail1234.ts.net"})
	if u, _ := Resolve(ctx, path, "", disc); u != "https://other.tail1234.ts.net" {
		t.Fatalf("saved: %q", u)
	}
	if u, _ := Resolve(ctx, path, "box.example.ts.net", disc); u != "https://box.example.ts.net" {
		t.Fatalf("env: %q", u)
	}
}
