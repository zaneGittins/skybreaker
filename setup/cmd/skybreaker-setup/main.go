// Command skybreaker-setup installs, upgrades and rolls back Skybreaker on
// this host.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/x/term"

	"github.com/zaneGittins/skybreaker/setup"
)

const usage = `skybreaker-setup: install and update Skybreaker on this host

Usage:
  sudo skybreaker-setup [install]   set up Skybreaker (a short wizard; run again to repair)
  sudo skybreaker-setup upgrade     install the latest release, rolling back if it doesn't stay up
  sudo skybreaker-setup rollback    go back to the version before the last upgrade
  sudo skybreaker-setup status      versions, services and the address
  skybreaker-setup version

Install without the wizard (all of these, plus TS_AUTHKEY in the environment):
  --login you@github --tailnet tail1234.ts.net --app-id 123456 --app-key app.pem --repo you/project

GITHUB_TOKEN, if set, authenticates downloads from GitHub (needed for a private fork).
`

func main() {
	cmd, args := "", os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("skybreaker-setup", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	var (
		release   = fs.String("release", "", "the release to install, such as v1.2.0 (default: this installer's own)")
		yes       = fs.Bool("yes", false, "don't ask: use the flags below and defaults for anything else")
		skipSelf  = fs.Bool("skip-self-update", false, "don't update skybreaker-setup first")
		login     = fs.String("login", "", "tailnet logins allowed in, comma-separated")
		tailnet   = fs.String("tailnet", "", "the tailnet's DNS name, such as tail1234.ts.net")
		appID     = fs.String("app-id", "", "the GitHub App's ID")
		appKey    = fs.String("app-key", "", "the GitHub App's private key file")
		repo      = fs.String("repo", "", "the first project's repository, owner/name")
		branch    = fs.String("branch", "", "its branch (default main)")
		harnesses = fs.String("harnesses", "", "its harnesses (default claude,codex)")
		gitName   = fs.String("git-name", "", "commit author name")
		gitEmail  = fs.String("git-email", "", "commit author email")
		block     = fs.String("block", "", "LAN ranges kept from agents (default: the host's own networks)")
		image     = fs.String("image", "", "the sandbox image (default "+setup.DefaultImage+")")
	)
	fs.Parse(args)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	tty := term.IsTerminal(os.Stdout.Fd())
	h := &setup.Host{
		Layout: setup.DefaultLayout, Cmd: setup.ExecRunner{}, Releases: setup.DefaultReleases(),
		Out: &setup.Progress{W: os.Stdout, TTY: tty}, Arch: runtime.GOARCH, Version: setup.Version,
		Sleep: time.Sleep, Settle: 8 * time.Second,
	}
	if *release != "" {
		h.Version = *release
	}
	if cmd == "" {
		cmd = "install"
		if h.Installed() {
			cmd = "status"
		}
	}

	switch cmd {
	case "version", "--version":
		fmt.Println("skybreaker-setup", setup.Version)
		return
	case "help", "-h", "--help":
		fmt.Print(usage)
		return
	case "install", "upgrade", "rollback", "status":
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if runtime.GOOS != "linux" {
		fatal(errors.New("Skybreaker runs on Linux only"))
	}
	if os.Geteuid() != 0 {
		fatal(errors.New("run it as root: sudo skybreaker-setup " + cmd))
	}
	if !*skipSelf && *release == "" && (cmd == "install" || cmd == "upgrade") {
		setup.SelfUpdate(ctx, h.Releases, runtime.GOARCH, h.Out, append([]string{cmd}, args...))
	}

	switch cmd {
	case "status":
		h.Status(ctx, os.Stdout)
		fmt.Println(setup.DimStyle.Render("\n  sudo skybreaker-setup upgrade   for the latest release"))
	case "rollback":
		fatal(h.Rollback(ctx))
	case "upgrade":
		fmt.Println(setup.Banner())
		err := h.Upgrade(ctx, func(from, to string) bool {
			return *yes || ask(fmt.Sprintf("Restart onto %s? Running chats stop and pick up again after.", to))
		})
		if errors.Is(err, setup.ErrCancelled) {
			fmt.Println("Nothing changed.")
			return
		}
		fatal(err)
	case "install":
		install(ctx, h, *yes || !tty, func(a *setup.Answers) {
			set := func(dst *string, v string) {
				if v != "" {
					*dst = v
				}
			}
			if *login != "" {
				a.Users = setup.SplitList(*login)
			}
			set(&a.Tailnet, *tailnet)
			set(&a.AuthKey, os.Getenv("TS_AUTHKEY"))
			set(&a.AppID, *appID)
			set(&a.AppKey, *appKey)
			set(&a.Repo, *repo)
			set(&a.Branch, *branch)
			if *harnesses != "" {
				a.Harnesses = setup.SplitList(*harnesses)
			}
			set(&a.GitName, *gitName)
			set(&a.GitEmail, *gitEmail)
			if *block != "" {
				a.Block = setup.SplitList(*block)
			}
			set(&a.Image, *image)
		})
	}
}

func install(ctx context.Context, h *setup.Host, unattended bool, flags func(*setup.Answers)) {
	checks := h.Prereqs(ctx)
	var a *setup.Answers
	switch {
	case h.Installed():
		fmt.Println(setup.Banner())
		fmt.Printf("Keeping the config in %s and finishing the install around it.\n\n", h.Etc)
	case unattended:
		fmt.Println(setup.Banner())
		d := h.Defaults(ctx)
		flags(&d)
		if err := d.Validate(); err != nil {
			fatal(err)
		}
		a = &d
	default:
		d := h.Defaults(ctx)
		flags(&d)
		var err error
		if a, err = setup.RunWizard(d, checks); err != nil {
			if errors.Is(err, setup.ErrCancelled) {
				fmt.Println("Nothing changed.")
				return
			}
			fatal(err)
		}
		fmt.Println(setup.Banner())
	}
	if err := h.Install(ctx, a, checks); err != nil {
		fmt.Fprintln(os.Stderr, "\n"+setup.ErrorStyle.Render("Error: ")+err.Error())
		if h.Installed() {
			fmt.Fprintln(os.Stderr, setup.DimStyle.Render("\nFix it and run sudo skybreaker-setup install again; it keeps your config."))
		}
		os.Exit(1)
	}
	fmt.Println()
	h.Status(ctx, os.Stdout)
	fmt.Println(setup.PanelStyle.Render(strings.Join([]string{
		setup.LabelStyle.Render("Next"),
		"",
		"1. Open the address above from a device logged in to your tailnet.",
		"   With Tailscale ACLs, let your login reach skybreaker on port 443.",
		"2. Tap + and pick Claude or Codex. Each signs in once per project.",
		"3. On a phone, add it to your home screen and tap the bell for",
		"   approvals and finished work.",
		"",
		setup.DimStyle.Render("Config: /etc/skybreaker/skybreaker.yaml · logs: journalctl -u skybreaker -f"),
		setup.DimStyle.Render("Update: sudo skybreaker-setup upgrade"),
	}, "\n")))
}

func ask(q string) bool {
	fmt.Printf("%s %s ", setup.PromptStyle.Render("?"), q+" [Y/n]")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "" || line == "y" || line == "yes"
}

func fatal(err error) {
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, "\n"+setup.ErrorStyle.Render("Error: ")+err.Error())
	os.Exit(1)
}
