package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"time"
)

const Usage = `skybreaker: your coding agents, from your terminal

  skybreaker                          pick a session and attach to it
  skybreaker ls                       list running sessions
  skybreaker attach <id>              attach to a session
  skybreaker new <project> [harness]  start a session and attach to it
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
var Commands = []string{"ls", "attach", "new", "connect", "update", "help", "-h", "--help"}

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
	}
	return fmt.Errorf("unknown command %q; `skybreaker help` lists them", cmd)
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
	err = t.attach(ctx, c, id)
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
