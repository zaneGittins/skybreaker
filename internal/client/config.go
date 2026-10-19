package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// Config is ~/.skybreaker/config.json. It usually holds only what
// discovery found, so you never have to write it.
type Config struct {
	URL string `json:"url"`
}

func ConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".skybreaker", "config.json"), nil
}

// LoadConfig reads the config; a missing file is an empty config.
func LoadConfig(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

func SaveConfig(path string, c Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// NormalizeURL turns what you type (a host name will do) into the
// server's base URL: http or https, a host, nothing else.
func NormalizeURL(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("no server address")
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", err
	}
	switch {
	case u.Scheme != "https" && u.Scheme != "http":
		return "", fmt.Errorf("%s: the address must be https:// (or http:// for a local development server)", s)
	case u.Host == "":
		return "", fmt.Errorf("%s: no host", s)
	case u.User != nil, u.Path != "" && u.Path != "/", u.RawQuery != "", u.Fragment != "":
		return "", fmt.Errorf("%s: give only the server's address, such as https://skybreaker.your-tailnet.ts.net", s)
	}
	return u.Scheme + "://" + u.Host, nil
}

// Runner runs a command and returns its output.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

func tailscaleCLIs() []string {
	names := []string{"tailscale"}
	if runtime.GOOS == "darwin" {
		names = append(names, "/Applications/Tailscale.app/Contents/MacOS/Tailscale")
	}
	return names
}

const connectHint = "run `skybreaker connect <address>` with its address, such as skybreaker.your-tailnet.ts.net"

// Discover asks the local Tailscale for the machine named skybreaker in your
// own tailnet and returns its address. A machine of that name shared in from
// another tailnet is never it: your keystrokes would go to its owner.
func Discover(ctx context.Context, run Runner) (string, error) {
	var out []byte
	var err error
	for _, name := range tailscaleCLIs() {
		if out, err = run(ctx, name, "status", "--json"); err == nil {
			break
		}
	}
	if err != nil {
		return "", fmt.Errorf("couldn't ask Tailscale where Skybreaker is (%v); %s", err, connectHint)
	}
	var st struct {
		MagicDNSSuffix string
		Peer           map[string]struct {
			DNSName    string
			ShareeNode bool // shared in from another tailnet
		}
	}
	if err := json.Unmarshal(out, &st); err != nil {
		return "", fmt.Errorf("couldn't read `tailscale status`: %v; %s", err, connectHint)
	}
	suffix := strings.Trim(st.MagicDNSSuffix, ".")
	if suffix == "" {
		return "", errors.New("Tailscale didn't say which tailnet this is (is MagicDNS on?); " + connectHint)
	}
	want := "skybreaker." + suffix
	for _, k := range slices.Sorted(maps.Keys(st.Peer)) {
		p := st.Peer[k]
		if !p.ShareeNode && strings.EqualFold(strings.TrimSuffix(p.DNSName, "."), want) {
			return "https://" + want, nil
		}
	}
	return "", errors.New("no machine named skybreaker on your tailnet; " + connectHint)
}

// Resolve finds the server: $SKYBREAKER_URL (env), then the saved config,
// then discovery, which is saved for next time; found reports that, so the
// caller can say where it went.
func Resolve(ctx context.Context, path, env string, run Runner) (u string, found bool, err error) {
	if env != "" {
		u, err = NormalizeURL(env)
		return u, false, err
	}
	c, err := LoadConfig(path)
	if err != nil {
		return "", false, err
	}
	if c.URL != "" {
		u, err = NormalizeURL(c.URL)
		return u, false, err
	}
	if u, err = Discover(ctx, run); err != nil {
		return "", false, err
	}
	return u, true, SaveConfig(path, Config{URL: u})
}
