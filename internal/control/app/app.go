// Package app is the control plane's core: it starts sandboxes with a fresh
// broker token and the hook settings, runs harness sessions as tmux
// sessions inside them, and fans events out to the UI and notifications.
package app

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zaneGittins/skybreaker/internal/control/chat"
	"github.com/zaneGittins/skybreaker/internal/control/config"
	"github.com/zaneGittins/skybreaker/internal/control/harness"
	"github.com/zaneGittins/skybreaker/internal/control/hooks"
	"github.com/zaneGittins/skybreaker/internal/control/preview"
	"github.com/zaneGittins/skybreaker/internal/control/sandbox"
	"github.com/zaneGittins/skybreaker/internal/control/store"
	"github.com/zaneGittins/skybreaker/internal/control/tmux"
	"github.com/zaneGittins/skybreaker/internal/control/usage"
	"github.com/zaneGittins/skybreaker/internal/notify"
)

var ErrNotFound = errors.New("not found")

// Broker mints and revokes the session tokens sandboxes use as proxy
// credentials, and holds the CA they trust.
type Broker interface {
	Mint(ctx context.Context, project string, ttl time.Duration) (Minted, error)
	Revoke(ctx context.Context, id string) error
	RevokeProject(ctx context.Context, project string) error
	CA(ctx context.Context) ([]byte, error)
}

// Minted is a new session token.
type Minted struct {
	ID      string
	Token   string
	Expires time.Time
}

type Deps struct {
	Config  *config.Config
	Store   *store.Store
	Sandbox *sandbox.Manager
	Broker  Broker
	HookKey []byte // HMAC key for per-project hook tokens
	Notify  notify.Notifier
	UIURL   string // this UI's URL, for notification links
	// ChatDir holds chat sessions' event logs; empty keeps them in memory.
	ChatDir string
	// Spawn starts a chat harness's exec; nil uses SpawnExec.
	Spawn Spawn
	// Usage counts the tokens chats report; nil counts nothing.
	Usage *usage.Store
}

// Event is what the UI's live stream carries.
type Event struct {
	Type    string    `json:"type"` // hook, session, sandbox, run
	Project string    `json:"project,omitempty"`
	Session string    `json:"session,omitempty"`
	Data    any       `json:"data,omitempty"`
	Time    time.Time `json:"time"`
}

type App struct {
	cfg       *config.Config
	store     *store.Store
	sandbox   *sandbox.Manager
	broker    Broker
	hookKey   []byte
	notify    notify.Notifier
	uiURL     string
	now       func() time.Time
	readyPoll time.Duration // how often waitReady checks the sandbox
	usage     *usage.Store

	locksMu sync.Mutex
	locks   map[string]*sync.Mutex // per project: serialises its starts and stops

	subMu sync.Mutex
	subs  map[chan Event]struct{}

	chat *chat.Manager
	bg   sync.WaitGroup // background sandbox starts and container stops

	sweptMu sync.Mutex
	swept   map[string]bool // projects whose inner containers were stopped this quiet spell

	clonesMu sync.Mutex
	clones   map[string]*cloneRun // per project: the workspace clone under way
	spawn    Spawn

	sshMu   sync.Mutex
	sshKeys []byte // authorized_keys for every sandbox's sshd

	instrMu      sync.Mutex
	instructions string // every agent's, set in the app
	dial         func(ctx context.Context, addr string) (net.Conn, error)
}

const (
	// SandboxTokenTTL bounds how long a sandbox's broker token lives. A
	// sandbox restart mints a new one and revokes the old.
	SandboxTokenTTL = 30 * 24 * time.Hour
	// MCPPath is where the sandbox sees the config naming Skybreaker's MCP
	// server (the board and its other tools) for terminals and scheduled
	// runs. Chats get their own URL, which names the chat.
	MCPPath = "/etc/skybreaker/mcp.json"
)

func New(d Deps) (*App, error) {
	if d.Config == nil || d.Store == nil || d.Sandbox == nil || d.Broker == nil {
		return nil, errors.New("app: missing dependency")
	}
	if len(d.HookKey) < 16 {
		return nil, errors.New("app: hook key too short")
	}
	if d.Notify == nil {
		d.Notify = notify.Nop{}
	}
	if d.Spawn == nil {
		d.Spawn = SpawnExec
	}
	a := &App{
		cfg: d.Config, store: d.Store, sandbox: d.Sandbox, broker: d.Broker, hookKey: d.HookKey,
		notify: d.Notify, uiURL: strings.TrimSuffix(d.UIURL, "/"), now: time.Now, readyPoll: time.Second, subs: map[chan Event]struct{}{},
		locks: map[string]*sync.Mutex{}, spawn: d.Spawn, usage: d.Usage,
		dial: func(ctx context.Context, addr string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", addr)
		},
	}
	m, err := a.newChatManager(d.ChatDir)
	if err != nil {
		return nil, err
	}
	a.chat = m
	// A session dropped as too old takes its chat log and kept files with
	// it. Apart from the caller, which may hold locks Forget would take.
	d.Store.OnPrune(func(id string) { go m.Forget(id) })
	return a, nil
}

func (a *App) project(name string) (*config.Project, error) {
	p, ok := a.cfg.Projects[name]
	if !ok || p == nil {
		return nil, fmt.Errorf("project %q: %w", name, ErrNotFound)
	}
	return p, nil
}

// ProjectDir is the host directory holding a project's workspace, home,
// read-only etc and run logs.
func (a *App) ProjectDir(project string) string {
	return filepath.Join(a.cfg.DataDir, "projects", project)
}

// HookToken authenticates a sandbox's hook calls. It's derived, so nothing
// needs storing, and differs per project so one sandbox can't answer as
// another.
func (a *App) HookToken(project string) string {
	m := hmac.New(sha256.New, a.hookKey)
	m.Write([]byte("hook:" + project))
	return hex.EncodeToString(m.Sum(nil))[:40]
}

// MCPURL is where a sandbox reaches Skybreaker's MCP server for one session
// ("-" for none).
func (a *App) MCPURL(project, session string) string {
	return a.cfg.MCPURL(project, a.HookToken(project), session)
}

// ProjectMCP is how terminals and scheduled runs reach Skybreaker's MCP
// server: as a project's sandbox, naming no chat.
func (a *App) ProjectMCP(project string) harness.MCP {
	return harness.MCP{Config: MCPPath, URL: a.MCPURL(project, "-")}
}

func (a *App) CheckHookToken(project, token string) bool {
	if _, ok := a.cfg.Projects[project]; !ok || token == "" {
		return false
	}
	return hmac.Equal([]byte(token), []byte(a.HookToken(project)))
}

// --- sandboxes --------------------------------------------------------------

func (a *App) SandboxState(ctx context.Context, project string) (sandbox.State, error) {
	if _, err := a.project(project); err != nil {
		return sandbox.State{}, err
	}
	return a.sandbox.Status(ctx, project)
}

// startTimeout bounds a sandbox start or stop, which may include an image
// pull. They run detached from the caller's context: a browser closing its
// request must not kill docker run halfway.
const startTimeout = 15 * time.Minute

// lockProject serialises starts and stops of one project; other projects
// proceed in parallel.
func (a *App) lockProject(project string) (unlock func()) {
	a.locksMu.Lock()
	m := a.locks[project]
	if m == nil {
		m = &sync.Mutex{}
		a.locks[project] = m
	}
	a.locksMu.Unlock()
	m.Lock()
	return m.Unlock
}

// StartSandbox makes sure the project's sandbox runs. A new container gets a
// fresh broker token, the broker CA, the hook settings and the MCP
// config; a token held for an earlier container is revoked first.
func (a *App) StartSandbox(ctx context.Context, project string) error {
	p, err := a.project(project)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), startTimeout)
	defer cancel()
	defer a.lockProject(project)()
	st, err := a.sandbox.Status(ctx, project)
	if err != nil {
		return err
	}
	if st.Running {
		return nil
	}
	dir := a.ProjectDir(project)
	if err := a.writeEtc(ctx, project, p); err != nil {
		return err
	}
	// One live token per project: revoke every earlier one, including any
	// whose ref was lost.
	if err := a.broker.RevokeProject(ctx, project); err != nil {
		log.Printf("skybreaker: revoke old tokens for %s: %v", project, err)
	}
	a.store.DeleteToken(project)
	m, err := a.broker.Mint(ctx, project, SandboxTokenTTL)
	if err != nil {
		return err
	}
	// Never count on a token outliving what was asked for.
	minted := a.now()
	expires := minted.Add(SandboxTokenTTL)
	if !m.Expires.IsZero() && m.Expires.Before(expires) {
		expires = m.Expires
	}
	if err := a.store.SetToken(project, store.TokenRef{ID: m.ID, Minted: minted, Expires: expires}); err != nil {
		a.broker.Revoke(ctx, m.ID)
		return err
	}
	// pi finds a llama.cpp router through its own variables; the
	// project's env, written by you, wins.
	_, env := a.cfg.PiConfig(project, m.Token)
	env = maps.Clone(env)
	if env == nil {
		env = map[string]string{}
	}
	maps.Copy(env, p.Env)
	spec := sandbox.Spec{
		Project: project, Image: a.cfg.Sandbox.Image,
		Subnet: a.cfg.Subnet(project), ProxyPort: a.cfg.Broker.Port, Token: m.Token, DataDir: dir,
		Env: env, Memory: a.cfg.Sandbox.Memory, CPUs: a.cfg.Sandbox.CPUs,
	}
	if err := a.sandbox.Start(ctx, spec); err != nil {
		a.broker.Revoke(ctx, m.ID)
		a.store.DeleteToken(project)
		return err
	}
	if err := a.EnsureTmux(ctx, project); err != nil {
		log.Printf("skybreaker: %v", err)
	}
	a.Publish(Event{Type: "sandbox", Project: project, Data: "started"})
	if p.Repo != "" {
		a.clone(project, p.Repo)
	}
	return nil
}

// readyMarker is touched by the sandbox entrypoint once the broker CA is in
// the system trust store.
const readyMarker = "/run/skybreaker-ready"

// waitReady waits for the sandbox entrypoint to finish, giving up if the
// sandbox stops meanwhile.
func (a *App) waitReady(ctx context.Context, project string) error {
	for {
		if _, err := a.sandbox.Exec(ctx, project, "test", "-e", readyMarker); err == nil {
			return nil
		}
		if st, err := a.sandbox.Status(ctx, project); err != nil || !st.Running {
			return fmt.Errorf("sandbox %s stopped before it was ready", project)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("sandbox %s never became ready: %w", project, ctx.Err())
		case <-time.After(a.readyPoll):
		}
	}
}

// cloneRun is a workspace clone: done closes once err is set.
type cloneRun struct {
	done chan struct{}
	err  error
}

// clone clones the project's repo into its workspace in the background, or
// joins the clone already under way, so a sandbox's start and a chat
// waiting on it never clone at once. A failed clone is forgotten, so the
// next start tries again.
func (a *App) clone(project, repo string) *cloneRun {
	a.clonesMu.Lock()
	defer a.clonesMu.Unlock()
	if c := a.clones[project]; c != nil {
		return c
	}
	if a.clones == nil {
		a.clones = map[string]*cloneRun{}
	}
	c := &cloneRun{done: make(chan struct{})}
	a.clones[project] = c
	go func() {
		c.err = a.cloneWorkspace(project, repo)
		if c.err == nil {
			a.setupWorkspace(project)
		}
		a.clonesMu.Lock()
		delete(a.clones, project)
		a.clonesMu.Unlock()
		close(c.done)
	}()
	return c
}

// WaitWorkspace waits for the clone and setup a new sandbox starts in the
// background, if one is under way, so an agent starts with the tools the
// setup script installs.
func (a *App) WaitWorkspace(ctx context.Context, project string) error {
	a.clonesMu.Lock()
	c := a.clones[project]
	a.clonesMu.Unlock()
	if c == nil {
		return nil
	}
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("sandbox %s is still being set up: %w", project, ctx.Err())
	}
}

// setupTimeout bounds the repo's setup script.
const setupTimeout = 10 * time.Minute

// setupLog is where the setup script's output goes, inside the sandbox.
const setupLog = "/var/log/skybreaker-setup.log"

// setupScript runs the repo's .skybreaker/setup.sh, as root in the
// workspace, once per container: a new container starts from the image, so
// what the script put outside the home and workspace is gone. It prints
// "ran" when the script ran and succeeded.
const setupScript = `test -e /run/skybreaker-setup && exit 0
touch /run/skybreaker-setup
cd /workspace && test -f .skybreaker/setup.sh || exit 0
bash .skybreaker/setup.sh </dev/null >"$1" 2>&1 && echo ran`

// setupWorkspace runs the repo's setup script, saying how it went. A
// failure doesn't stop agents starting.
func (a *App) setupWorkspace(project string) {
	ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
	defer cancel()
	out, err := a.sandbox.Exec(ctx, project, "sh", "-c", setupScript, "sh", setupLog)
	switch {
	case err != nil:
		log.Printf("skybreaker: setup in %s: %v", project, err)
		a.Publish(Event{Type: "sandbox", Project: project, Data: "setup failed: see " + setupLog + " in the sandbox"})
	case strings.TrimSpace(string(out)) == "ran":
		a.Publish(Event{Type: "sandbox", Project: project, Data: "setup done"})
	}
}

// cloneWorkspace clones the project's repo into its workspace, through the
// broker like every other request. It waits for the entrypoint first:
// git's TLS check needs the broker CA installed.
func (a *App) cloneWorkspace(project, repo string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if err := a.waitReady(ctx, project); err != nil {
		log.Printf("skybreaker: %v", err)
		a.Publish(Event{Type: "sandbox", Project: project, Data: "clone failed: " + err.Error()})
		return err
	}
	if _, err := a.sandbox.Exec(ctx, project, "sh", "-c", cloneScript, "sh", "https://github.com/"+repo+".git", workspaceDir); err != nil {
		log.Printf("skybreaker: clone %s into %s: %v", repo, project, err)
		a.Publish(Event{Type: "sandbox", Project: project, Data: "clone failed: " + err.Error()})
		return gitFailed("cloning "+repo+" into "+workspaceDir, err)
	}
	a.Publish(Event{Type: "sandbox", Project: project, Data: "workspace ready"})
	return nil
}

// cloneScript clones repo $1 into workspace $2 unless it holds one, never
// running repo hooks. A workspace with files in it already (made before
// the clone, or by an agent) is checked out around them: read-tree refuses
// to overwrite any, and .git goes into place only once that's done, so a
// refusal leaves the workspace as it was.
const cloneScript = `set -e
test -d "$2/.git" && exit 0
if [ -z "$(ls -A "$2")" ]; then exec git -c core.hooksPath=/dev/null clone --quiet "$1" "$2"; fi
t=$(mktemp -d "$2/.skybreaker-clone.XXXXXX")
trap 'rm -rf "$t"' EXIT
git -c core.hooksPath=/dev/null clone --quiet --no-checkout "$1" "$t"
git -c core.hooksPath=/dev/null --git-dir="$t/.git" --work-tree="$2" read-tree -m -u HEAD
mv "$t/.git" "$2/.git"`

// RunsSession is the tmux session that holds scheduled runs' windows. A
// sleeper keeps it alive between runs, and keeps the tmux server up for
// viewers.
const RunsSession = "skybreaker-runs"

// EnsureTmux starts the sandbox's tmux server with the options every session
// relies on, unless RunsSession shows it's up. tmux exits with its last
// session, options and all, so the sleeper in RunsSession keeps it.
func (a *App) EnsureTmux(ctx context.Context, project string) error {
	if _, err := a.sandbox.Exec(ctx, project, tmux.Has(RunsSession)...); err == nil {
		return nil
	}
	sleeper := tmux.NewSession(RunsSession, "/workspace", nil, []string{"sh", "-c", "exec sleep infinity"})
	if _, err := a.sandbox.Exec(ctx, project, sleeper...); err != nil {
		// A concurrent caller may have just started it.
		if _, herr := a.sandbox.Exec(ctx, project, tmux.Has(RunsSession)...); herr != nil {
			return fmt.Errorf("start tmux in %s: %w", project, err)
		}
	}
	for _, o := range tmux.EnsureServerOptions() {
		if _, err := a.sandbox.Exec(ctx, project, o...); err != nil {
			log.Printf("skybreaker: tmux option %v in %s: %v", o, project, err)
		}
	}
	return nil
}

func (a *App) writeEtc(ctx context.Context, project string, p *config.Project) error {
	etc := filepath.Join(a.ProjectDir(project), "etc")
	if err := os.MkdirAll(etc, 0o700); err != nil {
		return err
	}
	ca, err := a.broker.CA(ctx)
	if err != nil {
		return fmt.Errorf("broker CA: %w", err)
	}
	settings, err := hooks.ClaudeSettings(a.cfg.HookURL(project, a.HookToken(project)))
	if err != nil {
		return err
	}
	files := map[string][]byte{
		"ca.crt": ca, "claude-managed-settings.json": settings,
		"mcp.json":        []byte(chat.MCPConfig(a.MCPURL(project, "-"))),
		"authorized_keys": a.authorizedKeys(),
	}
	// pi's model servers, which skybreaker-pi merges into pi's own
	// config in the sandbox; a stale one goes when the project has none.
	if b, _ := a.cfg.PiConfig(project, ""); b != nil {
		files[piModels] = b
	} else if err := os.Remove(filepath.Join(etc, piModels)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for name, b := range instructionFiles(a.currentInstructions()) {
		files[name] = b
	}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(etc, name), b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// piModels is pi's model servers, in the sandbox's /etc/skybreaker.
const piModels = "pi-models.json"

const (
	// tokenRenewAt is how close to expiry a running sandbox is restarted
	// with a fresh token; with live sessions it waits until tokenLastDay.
	tokenRenewAt = 3 * 24 * time.Hour
	tokenLastDay = 24 * time.Hour
)

// RenewTokens restarts running sandboxes whose broker token is about to
// expire. The token is in the container's environment, so a restart is the
// only way to replace it; sessions are spared until the last day.
func (a *App) RenewTokens(ctx context.Context) {
	for _, project := range slices.Sorted(maps.Keys(a.cfg.Projects)) {
		ref, ok := a.store.Token(project)
		if !ok {
			continue
		}
		expires := ref.Expires
		if expires.IsZero() {
			expires = ref.Minted.Add(SandboxTokenTTL)
		}
		left := expires.Sub(a.now())
		if left > tokenRenewAt {
			continue
		}
		if st, err := a.sandbox.Status(ctx, project); err != nil || !st.Running {
			continue
		}
		if left > tokenLastDay {
			sessions, _ := a.Sessions(ctx, project)
			if slices.ContainsFunc(sessions, func(s store.Session) bool { return s.Status == store.StatusRunning }) {
				a.Publish(Event{Type: "sandbox", Project: project, Data: fmt.Sprintf("broker token expires in %s; restart the sandbox soon", left.Round(time.Hour))})
				continue
			}
		}
		log.Printf("skybreaker: restarting %s to renew its broker token (%s left)", project, left.Round(time.Minute))
		if err := a.StopSandbox(ctx, project); err != nil {
			log.Printf("skybreaker: renew %s: %v", project, err)
			continue
		}
		if err := a.StartSandbox(ctx, project); err != nil {
			log.Printf("skybreaker: renew %s: %v", project, err)
			continue
		}
		a.Publish(Event{Type: "sandbox", Project: project, Data: "restarted to renew its broker token"})
	}
}

// StopSandbox stops the container and revokes its broker token.
func (a *App) StopSandbox(ctx context.Context, project string) error {
	if _, err := a.project(project); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), startTimeout)
	defer cancel()
	a.stopChats(project)
	defer a.lockProject(project)()
	if err := a.sandbox.Stop(ctx, project); err != nil {
		return err
	}
	if err := a.broker.RevokeProject(ctx, project); err != nil {
		return fmt.Errorf("sandbox stopped but its token was not revoked: %w", err)
	}
	a.store.DeleteToken(project)
	now := time.Now()
	for _, s := range a.store.ListSessions(project) {
		// A chat is a conversation, not a process: it resumes in the next
		// sandbox.
		if s.Status == store.StatusRunning && s.Mode != store.ModeChat {
			a.store.UpdateSession(s.ID, func(x *store.Session) { x.Status, x.Ended = store.StatusExited, &now })
		}
	}
	a.Publish(Event{Type: "sandbox", Project: project, Data: "stopped"})
	return nil
}

// Exec runs a command in a project's sandbox.
func (a *App) Exec(ctx context.Context, project string, args ...string) ([]byte, error) {
	return a.sandbox.Exec(ctx, project, args...)
}

// --- sessions ---------------------------------------------------------------

func newSessionID() string {
	b := make([]byte, 5)
	rand.Read(b)
	return "s" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
}

// NewSession starts a harness TUI in a new tmux session in the project's
// sandbox, starting the sandbox if needed.
func (a *App) NewSession(ctx context.Context, project, harnessName, title string, opts NewOptions) (store.Session, error) {
	p, err := a.project(project)
	if err != nil {
		return store.Session{}, err
	}
	if !slices.Contains(p.Harnesses, harnessName) {
		return store.Session{}, fmt.Errorf("harness %q is not enabled for %s", harnessName, project)
	}
	m, ok := a.cfg.Harnesses.Get(harnessName)
	if !ok {
		return store.Session{}, fmt.Errorf("harness %q: %w", harnessName, ErrNotFound)
	}
	s := store.Session{ID: newSessionID(), Project: project, Harness: harnessName, Title: title, Created: time.Now().UTC(), Status: store.StatusRunning}
	// First, as it may restart the sandbox.
	if err := a.placeSession(ctx, &s, opts); err != nil {
		return store.Session{}, err
	}
	if err := a.StartSandbox(ctx, project); err != nil {
		return store.Session{}, err
	}
	if err := a.EnsureTmux(ctx, project); err != nil {
		return store.Session{}, err
	}
	if s.Worktree != "" {
		if err := a.waitReady(ctx, project); err != nil {
			return store.Session{}, err
		}
	}
	// Saved once its tmux session runs, so the reconciliation in Sessions
	// never takes it for one that ended; its worktree is held until then,
	// so tidying never takes that for an orphan.
	err = a.ensureWorktree(ctx, &s, func() error {
		if _, err := a.sandbox.Exec(ctx, project, tmux.NewSession(s.ID, workdir(s), a.sessionEnv(s, m), m.TUIArgs(a.ProjectMCP(project)))...); err != nil {
			return fmt.Errorf("start %s session: %w", harnessName, err)
		}
		return a.store.CreateSession(s)
	})
	if err != nil {
		return store.Session{}, err
	}
	a.Publish(Event{Type: "session", Project: project, Session: s.ID, Data: "started"})
	return s, nil
}

// sessionEnv is a terminal session's environment.
func (a *App) sessionEnv(s store.Session, m harness.Manifest) map[string]string {
	env := map[string]string{"SKYBREAKER_SESSION": s.ID}
	maps.Copy(env, m.Env)
	return env
}

func (a *App) session(id string) (store.Session, error) {
	s, ok := a.store.Session(id)
	if !ok || !tmux.ValidName(id) {
		return store.Session{}, fmt.Errorf("session %q: %w", id, ErrNotFound)
	}
	return s, nil
}

func (a *App) KillSession(ctx context.Context, id string) error {
	s, err := a.session(id)
	if err != nil {
		return err
	}
	if s.Mode == store.ModeChat {
		a.killChat(ctx, s)
		a.Publish(Event{Type: "session", Project: s.Project, Session: id, Data: "killed"})
		a.sessionEnded(s)
		return nil
	}
	if _, err := a.sandbox.Exec(ctx, s.Project, tmux.Kill(id)...); err != nil && !strings.Contains(err.Error(), "can't find session") && !noTmuxServer(err) {
		return err
	}
	// An exited session only loses its screen; it keeps how it ended.
	now := time.Now().UTC()
	a.store.UpdateSession(id, func(x *store.Session) {
		if x.Status == store.StatusRunning {
			x.Status, x.Ended = store.StatusKilled, &now
		}
	})
	a.Publish(Event{Type: "session", Project: s.Project, Session: id, Data: "killed"})
	a.sessionEnded(s)
	return nil
}

// keepScreens is how many ended sessions of a project keep their last
// screen in tmux. The UI lists as many.
const keepScreens = 10

// Sessions lists a project's sessions. A running session whose panes have
// all exited is marked exited, keeping its screen; one whose tmux session
// has gone is marked exited without. Screens beyond the newest keepScreens,
// and dead tmux sessions skybreaker doesn't know, are closed.
func (a *App) Sessions(ctx context.Context, project string) ([]store.Session, error) {
	if _, err := a.project(project); err != nil {
		return nil, err
	}
	type tsess struct {
		live bool
		exit *int // of the first pane, the harness's
	}
	ts := map[string]*tsess{}
	st, err := a.sandbox.Status(ctx, project)
	if err != nil {
		return nil, err
	}
	if st.Running {
		out, err := a.sandbox.Exec(ctx, project, tmux.ListPanes()...)
		if err != nil && !noTmuxServer(err) {
			return nil, err
		}
		panes, err := tmux.ParsePanes(string(out))
		if err != nil {
			// The sandbox controls this output: never let it end sessions.
			return nil, err
		}
		for _, p := range panes {
			t := ts[p.Session]
			if t == nil {
				t = &tsess{exit: p.Exit}
				ts[p.Session] = t
			}
			t.live = t.live || !p.Dead
		}
	}
	now := time.Now().UTC()
	known, screens := map[string]bool{}, 0
	for _, s := range a.store.ListSessions(project) {
		known[s.ID] = true
		if s.Mode == store.ModeChat {
			continue // not a tmux session; package chat tracks it
		}
		t, ok := ts[s.ID]
		if s.Status == store.StatusRunning && !(ok && t.live) {
			a.store.UpdateSession(s.ID, func(x *store.Session) {
				x.Status, x.Ended = store.StatusExited, &now
				if ok {
					x.Exit = t.exit
				}
			})
		}
		if ok && !t.live {
			if screens++; screens > keepScreens && a.closeScreen(ctx, project, s.ID) {
				delete(ts, s.ID)
			}
		}
	}
	for name, t := range ts {
		if !t.live && !known[name] {
			a.closeScreen(ctx, project, name)
		}
	}
	list := a.store.ListSessions(project)
	for i, s := range list {
		if t, ok := ts[s.ID]; ok && !t.live {
			list[i].Screen = true
		}
	}
	return list, nil
}

// closeScreen kills a dead tmux session. Names come from the sandbox, so
// only valid ones are passed on.
func (a *App) closeScreen(ctx context.Context, project, name string) bool {
	if !tmux.ValidName(name) {
		return false
	}
	_, err := a.sandbox.Exec(ctx, project, tmux.Kill(name)...)
	return err == nil
}

// noTmuxServer recognises "there is no tmux server", which tmux words
// differently across versions.
func noTmuxServer(err error) bool {
	m := err.Error()
	return strings.Contains(m, "no server running") || strings.Contains(m, "error connecting to")
}

// Snapshot returns the last lines of a session's screen, for the feed.
func (a *App) Snapshot(ctx context.Context, id string, lines int) (string, error) {
	s, err := a.session(id)
	if err != nil {
		return "", err
	}
	if lines <= 0 || lines > 2000 {
		lines = 40
	}
	out, err := a.sandbox.Exec(ctx, s.Project, tmux.Capture("="+id+":", lines)...)
	return string(out), err
}

// History is the part of a session's history a viewer doesn't have yet.
// Size is tmux's history size, in rows; a viewer that has Size rows passes
// it back as since. Text holds the new rows, or, with Replace, the last
// historyRows of them, when the viewer's copy can't be extended: a first
// load, a resize that reflowed the history, or rows rolling off the limit.
type History struct {
	Size    int    `json:"size"`
	Replace bool   `json:"replace"`
	Text    string `json:"text"`
}

const historyRows = 3000

// History reads the history rows added since a viewer's last read (since,
// -1 for none).
func (a *App) History(ctx context.Context, id string, since int) (History, error) {
	s, err := a.session(id)
	if err != nil {
		return History{}, err
	}
	out, err := a.sandbox.Exec(ctx, s.Project, tmux.History("="+id+":", historyRows)...)
	if err != nil {
		return History{}, err
	}
	head, rest, _ := strings.Cut(string(out), "\n")
	size, err := strconv.Atoi(strings.TrimSpace(head))
	if err != nil {
		return History{}, fmt.Errorf("history size %q: %w", head, err)
	}
	var rows []string
	if rest = strings.TrimSuffix(rest, "\n"); rest != "" {
		rows = strings.Split(rest, "\n")
	}
	h := History{Size: size}
	if since < 0 || since > size || size >= tmux.HistoryLimit || size-since > len(rows) {
		h.Replace = true
		h.Text = strings.Join(rows, "\n")
		return h, nil
	}
	h.Text = strings.Join(rows[len(rows)-(size-since):], "\n")
	return h, nil
}

// AttachArgv is the command a terminal viewer runs under its own PTY.
func (a *App) AttachArgv(id string) ([]string, error) {
	s, err := a.session(id)
	if err != nil {
		return nil, err
	}
	return a.sandbox.ExecArgv(s.Project, tmux.Attach(id)...), nil
}

// gitSafe are flags for every git command the control plane runs in an
// agent's repository: repo config can name programs to run (fsmonitor,
// hooks, diff drivers, textconv), and these never do. Clean and smudge
// filters can't be turned off by name, so commands that read or write files
// may still run one; they run in the sandbox, as the agent could anyway,
// and what they print reaches the user only cut short (gitFailed).
var gitSafe = []string{"-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "-c", "diff.external=", "--no-pager"}

func GitArgs(dir string, args ...string) []string {
	out := append([]string{"git", "-C", dir}, gitSafe...)
	return append(out, args...)
}

// Diff shows uncommitted changes in the project's workspace. It runs inside
// the sandbox, never with host git.
func (a *App) Diff(ctx context.Context, project string) (string, error) {
	if _, err := a.project(project); err != nil {
		return "", err
	}
	out, err := a.sandbox.Exec(ctx, project, GitArgs("/workspace", "diff", "--no-ext-diff", "--no-textconv", "--no-color", "HEAD")...)
	if errors.Is(err, sandbox.ErrOutputLimit) {
		err = nil // cut off by the runner: truncated below like any long diff
	}
	const max = 2 << 20
	if len(out) > max {
		out = append(out[:max], "\n… diff truncated"...)
	}
	return string(out), err
}

// --- events -----------------------------------------------------------------

func (a *App) Publish(ev Event) {
	if ev.Time.IsZero() {
		ev.Time = time.Now().UTC()
	}
	a.subMu.Lock()
	defer a.subMu.Unlock()
	for ch := range a.subs {
		select {
		case ch <- ev:
		default: // a slow viewer drops events rather than stalling the app
		}
	}
}

func (a *App) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 64)
	a.subMu.Lock()
	a.subs[ch] = struct{}{}
	a.subMu.Unlock()
	return ch, func() {
		a.subMu.Lock()
		delete(a.subs, ch)
		a.subMu.Unlock()
	}
}

// HookEvent is the hooks handler's callback: it reaches the UI live, and
// permission requests, notifications and stops buzz the phone with a
// content-free message.
func (a *App) HookEvent(ev hooks.Event) {
	a.Publish(Event{Type: "hook", Project: ev.Project, Session: ev.Session, Data: ev, Time: ev.Time})
	m := a.notificationFor(ev)
	if m.ID == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := a.notify.Notify(ctx, m); err != nil {
			log.Printf("skybreaker: notify: %v", err)
		}
	}()
}

// notificationFor builds the push for a hook event. IDs are per project and
// minute, so the deduper turns a burst (or a hostile flood) into one buzz.
func (a *App) notificationFor(ev hooks.Event) notify.Message {
	bucket := ev.Project + "-" + strconv.FormatInt(ev.Time.Unix()/60, 10)
	var m notify.Message
	switch ev.Kind {
	case hooks.KindPermission:
		m = notify.Message{ID: "perm-" + bucket, Title: "Approval waiting", Body: "An agent in " + ev.Project + " is waiting for you.", Priority: 4}
	case hooks.KindNotification:
		m = notify.Message{ID: "note-" + bucket, Title: "Agent needs attention", Body: "A session in " + ev.Project + " needs you."}
	case hooks.KindStop:
		m = notify.Message{ID: "stop-" + bucket, Title: "Agent finished", Body: "A session in " + ev.Project + " is done.", Priority: 2}
	default:
		return m
	}
	m.Click = a.uiURL + "/p/" + ev.Project
	return m
}

// Listening lists the TCP ports something in the project's sandbox listens
// on, for the preview. It reads /proc/net inside the sandbox; whatever the
// sandbox prints is parsed defensively and bounded.
func (a *App) Listening(ctx context.Context, project string) ([]preview.Listening, error) {
	if _, err := a.project(project); err != nil {
		return nil, err
	}
	out, err := a.sandbox.Exec(ctx, project, "sh", "-c", "cat /proc/net/tcp /proc/net/tcp6 2>/dev/null; true")
	if err != nil {
		return nil, err
	}
	return preview.ParseListening(out), nil
}

// AgentsWorking reports whether any chat is mid-turn or waiting on you:
// what restarting Skybreaker would cut short. Terminals live in tmux and
// survive it.
func (a *App) AgentsWorking() bool {
	for _, s := range a.store.ListSessions("") {
		if s.Status == store.StatusRunning && s.Mode == store.ModeChat && (s.State == store.ChatWorking || s.State == store.ChatWaiting) {
			return true
		}
	}
	return false
}
