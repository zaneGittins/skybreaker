package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zaneGittins/skybreaker/internal/names"
	"github.com/zaneGittins/skybreaker/internal/wsock"
)

// Editors reach a sandbox as <project>.skybreaker over SSH: ssh runs
// `skybreaker ssh-proxy`, which carries the connection over the tailnet to
// Skybreaker, which relays it to the sandbox's sshd. The key is this
// machine's own, made for nothing else.

const (
	sshBegin  = "# >>> skybreaker (managed by `skybreaker vscode setup`) >>>"
	sshEnd    = "# <<< skybreaker <<<"
	sshSuffix = ".skybreaker"
)

// SSHBlock is the ~/.ssh/config block for *.skybreaker. Host keys aren't
// checked: the sandbox is root's to change, its sshd included, so its key
// would prove nothing; what you reach is decided by the tailnet connection
// to Skybreaker and the project in the relay's URL. Nothing of yours is
// forwarded in: no agent, no X11.
func SSHBlock(exe, key string) string {
	return sshBegin + "\n" +
		"Host *" + sshSuffix + "\n" +
		"  User root\n" +
		"  ProxyCommand " + shellQuote(exe) + " ssh-proxy %h\n" +
		"  IdentityFile " + sshQuote(key) + "\n" +
		"  IdentitiesOnly yes\n" +
		"  ForwardAgent no\n" +
		"  ForwardX11 no\n" +
		"  StrictHostKeyChecking no\n" +
		"  UserKnownHostsFile /dev/null\n" +
		"  LogLevel ERROR\n" +
		"  ServerAliveInterval 30\n" +
		sshEnd + "\n"
}

// checkPath refuses a path that can't be put in the config safely.
func checkPath(p string) error {
	if strings.ContainsAny(p, "'\"\\\n\r\x00%") {
		return fmt.Errorf("%q: can't be written into ~/.ssh/config; move it somewhere plainer", p)
	}
	return nil
}

func shellQuote(s string) string {
	if strings.ContainsAny(s, " \t$`&;|<>()*?[]{}~#!") {
		return "'" + s + "'"
	}
	return s
}

func sshQuote(s string) string {
	if strings.ContainsAny(s, " \t") {
		return `"` + s + `"`
	}
	return s
}

// UpsertSSHConfig puts block first in an ssh config, in place of any block
// of ours already there, keeping everything else as it was. First, because
// ssh keeps the first value it finds for each option.
func UpsertSSHConfig(config []byte, block string) ([]byte, error) {
	rest := config
	if i := bytes.Index(rest, []byte(sshBegin)); i >= 0 {
		j := bytes.Index(rest[i:], []byte(sshEnd))
		if j < 0 {
			return nil, errors.New("~/.ssh/config has the start of Skybreaker's block but not its end; fix or remove it, then run this again")
		}
		end := i + j + len(sshEnd)
		if end < len(rest) && rest[end] == '\n' {
			end++
		}
		rest = append(bytes.Clone(rest[:i]), rest[end:]...)
	}
	rest = bytes.TrimLeft(rest, "\n")
	if len(rest) == 0 {
		return []byte(block), nil
	}
	return append([]byte(block+"\n"), rest...), nil
}

// HostProject is the project in a host name ssh was given, orbit.skybreaker.
func HostProject(host string) (string, error) {
	p, ok := strings.CutSuffix(host, sshSuffix)
	if !ok || !names.Project(p) {
		return "", fmt.Errorf("%q isn't a Skybreaker host: use <project>%s", host, sshSuffix)
	}
	return p, nil
}

// SSHProxy carries ssh's connection to the project's sandbox: in is what
// ssh sends, out is what it reads. The bytes are the SSH protocol, for ssh
// alone; they never go to a terminal (see `skybreaker ssh-proxy`).
func (c *Client) SSHProxy(ctx context.Context, project string, in io.Reader, out io.Writer) error {
	dctx, cancel := context.WithTimeout(ctx, 60*time.Second) // the sandbox may be starting
	ws, err := wsock.Dial(dctx, c.wsEndpoint("api", "projects", project, "ssh"), http.Header{"Origin": {c.origin()}}, c.TLS)
	cancel()
	if err != nil {
		return err
	}
	defer ws.Close(1000, "")
	ctx, stop := context.WithCancel(ctx)
	defer stop()

	sent := make(chan error, 1)
	go func() {
		buf := make([]byte, 32<<10)
		for {
			n, err := in.Read(buf)
			if n > 0 {
				if werr := ws.Write(ctx, true, buf[:n]); werr != nil {
					sent <- werr
					return
				}
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					err = nil // ssh hung up
				}
				sent <- err
				return
			}
		}
	}()
	go func() {
		ping := time.NewTicker(pingEvery)
		defer ping.Stop()
		for {
			select {
			case <-ping.C:
				ws.Write(ctx, false, []byte(`{"type":"ping"}`))
			case <-ctx.Done():
				return
			}
		}
	}()
	got := make(chan error, 1)
	go func() {
		for {
			binary, b, err := ws.Read(ctx)
			if err != nil {
				if errors.Is(err, io.EOF) {
					err = nil
				}
				got <- err
				return
			}
			if !binary {
				continue
			}
			if _, err := out.Write(b); err != nil {
				got <- err
				return
			}
		}
	}()
	select {
	case err := <-sent:
		return err
	case err := <-got:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// AddSSHKey lets this machine's public key into every sandbox.
func (c *Client) AddSSHKey(ctx context.Context, name, key string) error {
	return c.do(ctx, http.MethodPost, c.endpoint("api", "ssh-keys"), map[string]string{"name": name, "key": key}, nil)
}

type vscodeEnv struct {
	home, exe, host string
	run             Runner
}

// setupVSCode makes this machine's key (once), adds it to Skybreaker and
// writes the ssh config block.
func setupVSCode(ctx context.Context, c *Client, e vscodeEnv, out io.Writer) error {
	dir := filepath.Join(e.home, ".skybreaker", "ssh")
	key := filepath.Join(dir, "id_ed25519")
	for _, p := range []string{e.exe, key} {
		if err := checkPath(p); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(key); errors.Is(err, os.ErrNotExist) {
		if _, err := e.run(ctx, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "skybreaker "+e.host, "-f", key); err != nil {
			return fmt.Errorf("ssh-keygen: %w (is OpenSSH installed?)", err)
		}
	}
	pub, err := os.ReadFile(key + ".pub")
	if err != nil {
		return err
	}
	if err := c.AddSSHKey(ctx, e.host, strings.TrimSpace(string(pub))); err != nil && !strings.Contains(err.Error(), "already added") {
		return fmt.Errorf("adding this machine's key: %w", err)
	}
	cfg := filepath.Join(e.home, ".ssh", "config")
	if err := writeSSHConfig(cfg, SSHBlock(e.exe, key)); err != nil {
		return err
	}
	fmt.Fprintf(out, "This machine's key is added (%s), and %s routes *%s through Skybreaker.\n", key+".pub", cfg, sshSuffix)
	if in, err := c.Inbox(ctx); err == nil && len(in.Projects) > 0 {
		fmt.Fprintln(out, "\nIn VS Code (with the Remote - SSH extension), Remote-SSH: Connect to Host…, then one of:")
		for _, p := range in.Projects {
			fmt.Fprintf(out, "  %s%s\n", Visible(p.Name), sshSuffix)
		}
		fmt.Fprintf(out, "\nor run `skybreaker code %s`. The project is in /workspace.\n", Visible(in.Projects[0].Name))
	}
	return nil
}

// writeSSHConfig puts the block in the ssh config, through a symlink if it
// is one (a dotfiles repo, say), keeping its mode.
func writeSSHConfig(path, block string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	mode := os.FileMode(0o600)
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	b, err := UpsertSSHConfig(old, block)
	if err != nil || bytes.Equal(b, old) {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-skybreaker-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(b)
	if err == nil {
		err = tmp.Chmod(mode)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
