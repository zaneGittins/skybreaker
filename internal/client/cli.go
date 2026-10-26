package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"time"

	"github.com/zaneGittins/skybreaker/internal/names"
)

const Usage = `skybreaker: your coding agents, from your terminal

  skybreaker                          pick a session and attach to it
  skybreaker ls                       list running sessions
  skybreaker attach <id>              attach to a session
  skybreaker new <project> [harness]  start a session and attach to it
  skybreaker new-project <folder> [name] [--harness h1,h2] [--yes]
                                      make a project from a folder, copying
                                      everything in it, dot files too
  skybreaker code <project>           open the project's sandbox in VS Code
  skybreaker vscode setup             let VS Code (Remote - SSH) on this machine into sandboxes
  skybreaker connect [address]        find or set your Skybreaker, and check you can reach it
  skybreaker update                   update this client
  skybreaker version

In a session, Ctrl-b d detaches and brings you back; ~. at the start of a
line leaves from this side if the session won't let go. Settings live in
~/.skybreaker/config.json; $SKYBREAKER_URL overrides them.

On the Skybreaker host:
  skybreaker serve | check | nftables [-config path]
`

// Commands are the client's commands, for the main package's dispatch.
var Commands = []string{"ls", "attach", "new", "new-project", "code", "vscode", "ssh-proxy", "connect", "update", "help", "-h", "--help"}

// Main runs a client command (no command is the picker) and returns the
// exit status.
func Main(ctx context.Context, args []string, version string) int {
	cmd := ""
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	err := command(ctx, cmd, args, version, os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "skybreaker: "+Visible(err.Error()))
		return 1
	}
	return 0
}

func connected(ctx context.Context) (*Client, error) {
	path, err := ConfigPath()
	if err != nil {
		return nil, err
	}
	u, found, err := Resolve(ctx, path, os.Getenv("SKYBREAKER_URL"), ExecRunner)
	if err != nil {
		return nil, err
	}
	if found {
		fmt.Fprintf(os.Stderr, "Found Skybreaker at %s (saved in %s; `skybreaker connect <address>` changes it).\n", u, path)
	}
	return New(u)
}

func command(ctx context.Context, cmd string, args []string, version string, out io.Writer) error {
	switch cmd {
	case "help", "-h", "--help":
		fmt.Fprint(out, Usage)
		return nil
	case "update":
		u, err := DefaultUpdater(version)
		if err != nil {
			return err
		}
		msg, err := u.Run(ctx)
		if err == nil {
			fmt.Fprintln(out, msg)
		}
		return err
	case "connect":
		return connect(ctx, args, out)
	}

	c, err := connected(ctx)
	if err != nil {
		return err
	}
	switch cmd {
	case "":
		m, err := c.Meta(ctx)
		if err != nil {
			return err
		}
		t, err := openTerminal()
		if err != nil {
			return err
		}
		defer t.close()
		return pick(ctx, c, t, m.User)
	case "ls":
		in, err := c.Inbox(ctx)
		if err != nil {
			return err
		}
		n := 0
		for _, e := range Entries(in) {
			if s := e.Session; s != nil {
				state := s.State
				if !s.Chat() {
					state = "terminal"
				}
				fmt.Fprintf(out, "%-12s  %-14s  %-8s  %-9s  %s\n", Visible(s.ID), Visible(s.Project), Visible(s.Harness), Visible(state), Visible(s.Title))
				n++
			}
		}
		if n == 0 {
			fmt.Fprintln(out, "No running sessions. `skybreaker` starts one.")
		}
		return nil
	case "attach":
		if len(args) != 1 {
			return errors.New("usage: skybreaker attach <id>")
		}
		in, err := c.Inbox(ctx)
		if err != nil {
			return err
		}
		e := Entry{Session: &Session{ID: args[0]}}
		for _, s := range in.Sessions {
			if s.ID == args[0] {
				e.Session = &s
			}
		}
		return attachOne(ctx, c, e)
	case "new":
		if len(args) < 1 || len(args) > 2 {
			return errors.New("usage: skybreaker new <project> [harness]")
		}
		in, err := c.Inbox(ctx)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(in.Projects, func(p Project) bool { return p.Name == args[0] })
		if i < 0 {
			return fmt.Errorf("no project %q", args[0])
		}
		e := Entry{Project: args[0]}
		if len(args) == 2 {
			e.Harness = args[1]
		} else if hs := in.Projects[i].Harnesses; len(hs) > 0 {
			e.Harness = hs[0]
		}
		return attachOne(ctx, c, e)
	case "new-project":
		interactive := isTerminal(int(os.Stdin.Fd())) && isTerminal(int(os.Stdout.Fd()))
		e, err := newProject(ctx, c, ExecRunner, args, os.Stdin, out, interactive)
		if err != nil || !interactive {
			if err == nil {
				fmt.Fprintf(out, "`skybreaker new %s` starts a session in it.\n", e.Project)
			}
			return err
		}
		return attachOne(ctx, c, e)
	case "ssh-proxy": // ssh's ProxyCommand, from `vscode setup`
		if len(args) != 1 {
			return errors.New("usage: skybreaker ssh-proxy <project>.skybreaker (ssh runs this; see `skybreaker vscode setup`)")
		}
		p, err := HostProject(args[0])
		if err != nil {
			return err
		}
		// Sandbox bytes reach a terminal only through a Filter; these are
		// SSH's, for ssh's pipe alone.
		if isTerminal(int(os.Stdout.Fd())) {
			return errors.New("ssh-proxy is for ssh's ProxyCommand, not a terminal; `skybreaker vscode setup` sets it up")
		}
		if err := c.SSHProxy(ctx, p, os.Stdin, os.Stdout); err != nil && ctx.Err() == nil {
			return err
		}
		return nil // ssh hung up on us
	case "vscode":
		if len(args) != 1 || args[0] != "setup" {
			return errors.New("usage: skybreaker vscode setup")
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		host, _ := os.Hostname()
		if host == "" {
			host = "this machine"
		}
		return setupVSCode(ctx, c, vscodeEnv{home: home, exe: exe, host: host, run: ExecRunner}, out)
	case "code":
		if len(args) != 1 {
			return errors.New("usage: skybreaker code <project>")
		}
		in, err := c.Inbox(ctx)
		if err != nil {
			return err
		}
		if !names.Project(args[0]) || !slices.ContainsFunc(in.Projects, func(p Project) bool { return p.Name == args[0] }) {
			return fmt.Errorf("no project %q", args[0])
		}
		return runCode("--remote", "ssh-remote+"+args[0]+sshSuffix, "/workspace")
	}
	return fmt.Errorf("unknown command %q; `skybreaker help` lists them", cmd)
}

// runCode runs VS Code's command-line launcher.
var runCode = func(args ...string) error {
	cmd := exec.Command("code", args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("code: %w (in VS Code, run \"Shell Command: Install 'code' command in PATH\"; `skybreaker vscode setup` comes first)", err)
	}
	return nil
}

func attachOne(ctx context.Context, c *Client, e Entry) error {
	id, err := target(ctx, c, e)
	if err != nil {
		return err
	}
	t, err := openTerminal()
	if err != nil {
		return err
	}
	err = t.attach(ctx, c, id, label(e))
	t.close()
	if err == nil {
		fmt.Println("Detached from " + label(e) + ".")
	}
	return err
}

func connect(ctx context.Context, args []string, out io.Writer) error {
	path, err := ConfigPath()
	if err != nil {
		return err
	}
	var u string
	switch len(args) {
	case 0:
		u, err = Discover(ctx, ExecRunner)
	case 1:
		u, err = NormalizeURL(args[0])
	default:
		return errors.New("usage: skybreaker connect [address]")
	}
	if err != nil {
		return err
	}
	c, err := New(u)
	if err != nil {
		return err
	}
	mctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	m, err := c.Meta(mctx)
	if err != nil {
		return fmt.Errorf("couldn't reach %s: %w", u, err)
	}
	if err := SaveConfig(path, Config{URL: u}); err != nil {
		return err
	}
	fmt.Fprintf(out, "Connected to %s as %s (Skybreaker %s).\n", u, Visible(m.User), Visible(m.Version))
	return nil
}
