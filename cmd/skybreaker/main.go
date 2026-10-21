// Command skybreaker is the whole of Skybreaker. On the host, `serve` runs
// it: gVisor sandboxes, the credential broker they reach the world through,
// chats and terminals, approvals, the scheduler and the UI. On your own
// machine, the same binary is the terminal client (package client).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"slices"
	"syscall"

	"github.com/zaneGittins/skybreaker/internal/client"
	"github.com/zaneGittins/skybreaker/internal/control/controld"
)

var Version = "dev"

func main() {
	if len(os.Args) < 2 || slices.Contains(client.Commands, os.Args[1]) {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
		code := client.Main(ctx, os.Args[1:], Version)
		stop()
		os.Exit(code)
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	def := os.Getenv("SKYBREAKER_CONFIG")
	if def == "" {
		def = "/etc/skybreaker/skybreaker.yaml"
	}
	cfg := fs.String("config", def, "config file (.yaml or .json)")
	fs.Parse(os.Args[2:])

	switch os.Args[1] {
	case "serve":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		err := controld.Run(ctx, controld.Options{ConfigPath: *cfg, Version: Version, Tailnet: tailnet})
		if errors.Is(err, controld.ErrRestart) {
			// Start again in place, with the new project: same binary,
			// arguments and environment.
			stop()
			exe, xerr := os.Executable()
			if xerr == nil {
				xerr = syscall.Exec(exe, os.Args, os.Environ())
			}
			log.Fatalf("skybreaker: couldn't restart: %v", xerr)
		}
		if err != nil {
			log.Fatal(err)
		}
	case "check":
		if err := controld.Check(*cfg); err != nil {
			log.Fatal(err)
		}
		fmt.Println("ok")
	case "nftables":
		out, err := controld.Nftables(*cfg)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Print(out)
	case "version", "--version", "-v":
		fmt.Println("skybreaker", Version)
	default:
		fmt.Fprint(os.Stderr, client.Usage)
		os.Exit(2)
	}
}
