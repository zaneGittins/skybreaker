// Command skybreaker is the whole of Skybreaker: gVisor sandboxes, the
// credential broker they reach the world through, chats and terminals,
// approvals, the scheduler and the UI.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/zaneGittins/skybreaker/internal/control/controld"
)

var Version = "0.0.1"

const usage = `skybreaker: a self-hosted host for coding agents

Usage:
  skybreaker serve    [-config path]   run Skybreaker
  skybreaker check    [-config path]   validate the config and its secrets without starting
  skybreaker nftables [-config path]   print the nftables ruleset pinning sandboxes to the broker
  skybreaker version

The config path defaults to $SKYBREAKER_CONFIG, then /etc/skybreaker/skybreaker.yaml.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
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
		if err := controld.Run(ctx, controld.Options{ConfigPath: *cfg, Version: Version, Tailnet: tailnet}); err != nil {
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
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}
