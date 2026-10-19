// Package controld assembles skybreaker: config, state, the broker,
// sandboxes, hooks, the scheduler and the UI, all in one process.
package controld

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zaneGittins/skybreaker/internal/board"
	"github.com/zaneGittins/skybreaker/internal/control/api"
	"github.com/zaneGittins/skybreaker/internal/control/app"
	"github.com/zaneGittins/skybreaker/internal/control/config"
	"github.com/zaneGittins/skybreaker/internal/control/hooks"
	"github.com/zaneGittins/skybreaker/internal/control/mcp"
	"github.com/zaneGittins/skybreaker/internal/control/netrules"
	"github.com/zaneGittins/skybreaker/internal/control/sandbox"
	"github.com/zaneGittins/skybreaker/internal/control/scheduler"
	"github.com/zaneGittins/skybreaker/internal/control/show"
	"github.com/zaneGittins/skybreaker/internal/control/store"
	"github.com/zaneGittins/skybreaker/internal/control/terminal"
	"github.com/zaneGittins/skybreaker/internal/notify"
	"github.com/zaneGittins/skybreaker/internal/notify/webpush"
	"github.com/zaneGittins/skybreaker/internal/proxy/broker"
	"github.com/zaneGittins/skybreaker/internal/proxy/netguard"
	"github.com/zaneGittins/skybreaker/internal/tsauth"
	"github.com/zaneGittins/skybreaker/internal/webutil"
	"github.com/zaneGittins/skybreaker/internal/wsock"
	"github.com/zaneGittins/skybreaker/web"
)

type Options struct {
	ConfigPath string
	Version    string
	// Tailnet starts Skybreaker's tsnet node; unused when tailscale is
	// disabled.
	Tailnet func(ctx context.Context, hostname, stateDir string) (tsauth.Network, error)
	// Runner runs the docker CLI; nil uses the real one.
	Runner sandbox.Runner
	// Spawn starts chat harness processes; nil uses the real one.
	Spawn app.Spawn
	// Ready, if set, is called with the bound addresses once serving.
	Ready func(ui, hooks, proxy net.Addr)
	// OnBroker, if set, sees the broker once it is open (TestUIDev
	// scripts requests through it).
	OnBroker func(*broker.Broker)
}

// tokens is the broker as the app sees it.
type tokens struct{ *broker.Broker }

func (t tokens) Mint(ctx context.Context, project string, ttl time.Duration) (app.Minted, error) {
	m, err := t.Broker.Mint(ctx, project, ttl)
	return app.Minted(m), err
}

func Run(ctx context.Context, o Options) error {
	cfg, err := config.Load(o.ConfigPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return err
	}
	// Workspaces are bind-mounted without nosuid (docker can't set it), and
	// the sandbox's root owns what it makes, so no other host user may reach
	// them.
	if err := os.Chmod(cfg.DataDir, 0o700); err != nil {
		return err
	}
	hookKey, err := loadOrCreateKey(filepath.Join(cfg.DataDir, "hook.key"))
	if err != nil {
		return err
	}
	runner := o.Runner
	if runner == nil {
		runner = sandbox.ExecRunner{}
	}
	sandboxes := &sandbox.Manager{R: runner}
	// The broker's secrets share this host with the agents; gVisor is what
	// keeps them apart.
	if err := sandboxes.CheckRuntime(ctx); err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(cfg.DataDir, "state.json"))
	if err != nil {
		return err
	}

	var uiNet tsauth.Network
	uiAddr := ":443"
	if cfg.Tailscale.Disabled {
		log.Printf("skybreaker: TAILSCALE DISABLED: development mode on loopback as %s", cfg.Users[0])
		uiNet, uiAddr = tsauth.Dev(tsauth.Peer{Node: "dev", Login: cfg.Users[0]}), cfg.Tailscale.DevListen
	} else {
		if o.Tailnet == nil {
			return errors.New("this build has no tailnet support")
		}
		if uiNet, err = o.Tailnet(ctx, cfg.Tailscale.Hostname, cfg.Tailscale.StateDir); err != nil {
			return fmt.Errorf("tailnet: %w", err)
		}
		defer uiNet.Close()
	}
	tailnet, _ := uiNet.(netguard.Tailnet)
	brk, err := broker.Open(broker.Options{
		DataDir: filepath.Join(cfg.DataDir, "broker"), SecretsFile: cfg.SecretsFile, Policy: cfg.Policy,
		// Only sandboxes use the broker, each from its own network.
		Sandbox: func(remote string) (string, bool) {
			ap, err := netip.ParseAddrPort(remote)
			if err != nil {
				return "", false
			}
			return cfg.ProjectOf(ap.Addr())
		},
		Block: cfg.Broker.Netguard.Block, Allow: cfg.Broker.Netguard.Allow, Tailnet: tailnet,
		Hold: cfg.Broker.Hold.D(), PendingTTL: cfg.Broker.PendingTTL.D(), ApprovedTTL: cfg.Broker.ApprovedTTL.D(),
		UIURL: cfg.UIURL,
	})
	if err != nil {
		return err
	}
	defer brk.Close()
	if o.OnBroker != nil {
		o.OnBroker(brk)
	}

	n, err := notify.FromConfig(cfg.Ntfy)
	if err != nil {
		return err
	}
	// Web push goes straight to the phones that asked for it, encrypted end
	// to end; ntfy, when configured, too.
	subject := cfg.UIURL
	if !strings.HasPrefix(subject, "https://") {
		subject = "mailto:skybreaker@localhost"
	}
	push, err := webpush.Open(filepath.Join(cfg.DataDir, "push"), subject)
	if err != nil {
		return err
	}
	n = notify.Multi{n, notify.Dedup(push, 10*time.Minute)}
	a, err := app.New(app.Deps{
		Config: cfg, Store: st, Sandbox: sandboxes, Broker: tokens{brk},
		HookKey: hookKey, Notify: n, UIURL: cfg.UIURL,
		ChatDir: filepath.Join(cfg.DataDir, "chats"), Spawn: o.Spawn,
	})
	if err != nil {
		return err
	}
	defer a.CloseChats()
	a.RecoverChats()

	// Every project has a board; the UI redraws when one changes.
	boards, err := board.Open(filepath.Join(cfg.DataDir, "board.json"))
	if err != nil {
		return err
	}
	var specs []board.Spec
	for name, p := range cfg.Projects {
		specs = append(specs, board.Spec{Slug: name, Key: p.Key})
	}
	if err := boards.Sync(specs); err != nil {
		return err
	}
	boards.OnChange = func(project, ref string) { a.Publish(app.Event{Type: "board", Project: project, Data: ref}) }
	// One MCP server for agents and for you: the board, and showing files
	// and images in a chat.
	tools := &mcp.Server{Name: "skybreaker", Version: o.Version, Instructions: board.Instructions,
		Tools: []func(mcp.Caller) []mcp.Tool{board.Tools(boards), show.Tools(a)}}

	hk := hooks.New(a.CheckHookToken, a.HookEvent)
	// Chats answer their harness's prompts in the conversation.
	hk.Direct = a.IsChat
	hk.AllowSource = func(project string, ip netip.Addr) bool {
		// A sandbox may only call in from its own network.
		return cfg.Subnet(project).Contains(ip) || (cfg.Tailscale.Disabled && ip.IsLoopback())
	}
	sched := scheduler.New(scheduler.Deps{
		Config: cfg, Store: st, Backend: a, Notify: n, UIURL: cfg.UIURL,
		OnUpdate: func(r store.Run) { a.Publish(app.Event{Type: "run", Project: r.Project, Data: r}) },
	})

	apiHandler, err := api.New(api.Deps{
		Config: cfg, Backend: a, Approvals: hk, Network: brk, Board: boards, MCP: tools, Runs: sched, Store: st, Identifier: uiNet, Push: push,
		Static: web.Skybreaker(), Version: o.Version,
		Upgrade: func(w http.ResponseWriter, r *http.Request) (terminal.Conn, error) { return wsock.Accept(w, r) },
		Bridge: func(ctx context.Context, c terminal.Conn, argv []string) error {
			return terminal.Bridge(ctx, c, terminal.StartPTY, argv)
		},
	})
	if err != nil {
		return err
	}

	ul, err := uiNet.ListenTLS(uiAddr)
	if err != nil {
		return fmt.Errorf("ui listener: %w", err)
	}
	hl, err := net.Listen("tcp", cfg.Sandbox.HookListen)
	if err != nil {
		ul.Close()
		return fmt.Errorf("hook listener: %w", err)
	}
	pl, err := net.Listen("tcp", cfg.Broker.Listen)
	if err != nil {
		ul.Close()
		hl.Close()
		return fmt.Errorf("broker listener: %w", err)
	}
	if cfg.Tailscale.Disabled {
		apiHandler = tsauth.LoopbackHost(apiHandler)
	}
	// Sandboxes reach Skybreaker itself on the hook listener: Claude's hooks,
	// and the MCP server, placed by path, network and hook token.
	fromSandbox := http.NewServeMux()
	fromSandbox.Handle("/mcp/", tools.Handler(a.MCPCaller))
	fromSandbox.Handle("/", hk)
	servers := []*http.Server{webutil.NewServer(ctx, apiHandler), webutil.NewServer(ctx, fromSandbox), webutil.NewServer(ctx, brk.Handler())}
	errs := make(chan error, 3)
	for i, ln := range []net.Listener{ul, hl, pl} {
		go func() {
			if err := servers[i].Serve(ln); !errors.Is(err, http.ErrServerClosed) {
				errs <- err
			}
		}()
	}
	sched.SetContext(ctx)
	sched.Reconcile()
	go sched.Loop(ctx)
	go brk.Notify(ctx, n, cfg.UIURL)
	go func() {
		// The UI redraws when a request waits or is decided.
		ch, cancel := brk.Subscribe()
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case x := <-ch:
				a.Publish(app.Event{Type: "request", Project: x.Project, Data: x.State})
			}
		}
	}()
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				a.ReapChats()
				a.SweepContainers(ctx)
			}
		}
	}()
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			a.RenewTokens(ctx)
			a.RefreshSandboxes(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	log.Printf("skybreaker %s: UI on %s, hooks on %s, broker on %s, %d projects, %d jobs", o.Version, ul.Addr(), hl.Addr(), pl.Addr(), len(cfg.Projects), len(cfg.Jobs))
	if o.Ready != nil {
		o.Ready(ul.Addr(), hl.Addr(), pl.Addr())
	}
	select {
	case <-ctx.Done():
	case err := <-errs:
		return err
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, s := range servers {
		s.Shutdown(shutdown)
	}
	sched.Wait()
	return nil
}

// Nftables renders the ruleset that pins every sandbox to the broker.
func Nftables(configPath string) (string, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return "", err
	}
	return netrules.Generate(netrules.Input{Sandboxes: cfg.Subnets(), ProxyPort: cfg.Broker.Port, HookPort: cfg.Sandbox.HookPort})
}

// loadOrCreateKey keeps the hook-token key across restarts, so sandboxes'
// settings stay valid.
func loadOrCreateKey(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		if len(b) < 32 {
			return nil, fmt.Errorf("%s is too short", path)
		}
		return b, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	b = make([]byte, 32)
	rand.Read(b)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return nil, err
	}
	return b, nil
}
