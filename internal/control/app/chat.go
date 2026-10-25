package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/http"
	"net/netip"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/zaneGittins/skybreaker/internal/control/chat"
	"github.com/zaneGittins/skybreaker/internal/control/config"
	"github.com/zaneGittins/skybreaker/internal/control/mcp"
	"github.com/zaneGittins/skybreaker/internal/control/sandbox"
	"github.com/zaneGittins/skybreaker/internal/control/store"
	"github.com/zaneGittins/skybreaker/internal/control/tmux"
	"github.com/zaneGittins/skybreaker/internal/notify"
)

// Spawn starts a command on the host with pipes, for a chat harness's
// `docker exec -i`.
type Spawn func(ctx context.Context, argv []string) (*chat.Proc, error)

// SpawnExec is the real Spawn. The process outlives the request that
// started it, so it isn't tied to ctx.
func SpawnExec(_ context.Context, argv []string) (*chat.Proc, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	tail := &tailBuffer{max: 8 << 10}
	cmd.Stderr = tail
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &chat.Proc{
		Stdin: stdin, Stdout: stdout, Wait: cmd.Wait,
		Kill:   func() { cmd.Process.Kill() },
		Stderr: tail.String,
	}, nil
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	b   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		t.b = append([]byte(nil), t.b[len(t.b)-t.max:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}

// pidWrapper records the harness's PID inside the sandbox before it
// becomes the harness, so it can be killed there: killing the host's
// docker exec client leaves the process in the container running.
const pidWrapper = `mkdir -p /tmp/skybreaker-chat && echo $$ > "/tmp/skybreaker-chat/$0.pid" && exec "$@"`

func (a *App) newChatManager(dir string) (*chat.Manager, error) {
	l, err := chat.OpenLog(dir)
	if err != nil {
		return nil, err
	}
	return chat.NewManager(chat.Config{
		Log: l, Sessions: a.store, Start: a.startChat, Attach: a.attach, Options: a.chatOptions,
		Driver: func(s store.Session) chat.Driver {
			m, ok := a.cfg.Harnesses.Get(s.Harness)
			if !ok || m.Chat == "" {
				return nil
			}
			return chat.New(m.Chat)
		},
		OnChange: a.chatChanged,
		OnUsage:  a.chatUsage,
	}), nil
}

func (a *App) chatOptions(s store.Session) chat.Options {
	return chat.Options{Cwd: workdir(s), MCP: a.MCPURL(s.Project, s.ID)}
}

// startChat runs a chat's harness in its sandbox, starting the sandbox
// first if need be. A terminal the chat was continued in is closed: one
// conversation can't have two harnesses writing to it.
func (a *App) startChat(ctx context.Context, s store.Session, argv []string, env map[string]string) (*chat.Proc, error) {
	if err := a.StartSandbox(ctx, s.Project); err != nil {
		return nil, err
	}
	if err := a.waitReady(ctx, s.Project); err != nil {
		return nil, err
	}
	if err := a.ensureWorktree(ctx, &s, nil); err != nil {
		return nil, err
	}
	if err := a.WaitWorkspace(ctx, s.Project); err != nil {
		return nil, err
	}
	a.restoreAttachments(ctx, s)
	if s.Terminal != "" {
		a.closeChatTerminal(ctx, s)
		a.chat.Note(s.ID, "Back in the chat. The agent remembers what happened in the terminal, but it isn't shown here.")
	}
	full := a.sandbox.ExecPipeArgv(s.Project, workdir(s), env, append([]string{"sh", "-c", pidWrapper, s.ID}, argv...)...)
	p, err := a.spawn(ctx, full)
	if err != nil {
		return nil, err
	}
	kill := p.Kill
	project, id := s.Project, s.ID
	p.Kill = func() {
		kctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		a.sandbox.Exec(kctx, project, "sh", "-c", `kill -TERM "$(cat "/tmp/skybreaker-chat/$0.pid")" 2>/dev/null; true`, id)
		if kill != nil {
			kill()
		}
	}
	return p, nil
}

// NewOptions are where a new session works.
type NewOptions struct {
	// OwnBranch gives it a git worktree and branch of its own, so it
	// doesn't share the workspace with the project's other sessions.
	OwnBranch bool
	// Continues names a session of the project's whose worktree it takes
	// on, as a chat cleared to start afresh on the same work does.
	Continues string
}

// placeSession sets where new session s works, from opts. A sandbox that's
// up but can't hold worktrees is refused now rather than on the first start.
func (a *App) placeSession(ctx context.Context, s *store.Session, opts NewOptions) error {
	if opts.Continues != "" {
		from, ok := a.store.Session(opts.Continues)
		if !ok || from.Project != s.Project {
			return fmt.Errorf("session %q: %w", opts.Continues, ErrNotFound)
		}
		s.Worktree, s.Branch, s.BranchFrom = from.Worktree, from.Branch, from.BranchFrom
		return nil
	}
	if !opts.OwnBranch {
		return nil
	}
	if _, err := a.repoProject(s.Project); err != nil {
		return err
	}
	if st, err := a.sandbox.Status(ctx, s.Project); err == nil && st.Running && !a.holdsWorktrees(ctx, s.Project) {
		// Started by an earlier Skybreaker: restart it onto one that can,
		// unless that would interrupt something.
		if a.busy(ctx, s.Project) {
			return ErrNoWorktrees
		}
		if err := a.StopSandbox(ctx, s.Project); err != nil {
			return err
		}
		if err := a.StartSandbox(ctx, s.Project); err != nil {
			return err
		}
		a.Publish(Event{Type: "sandbox", Project: s.Project, Data: "restarted so sessions can have their own branch"})
	}
	s.Worktree = worktreeOf(s.ID)
	s.Branch = ownBranch(s.Worktree)
	return nil
}

// NewChat starts a chat session: the harness runs in its structured mode
// and the UI draws the conversation. The sandbox and harness start on the
// first message, or in the background now, so that message is quick. Its
// own worktree, if it has one, is made on that first start.
func (a *App) NewChat(ctx context.Context, project, harnessName string, opts NewOptions) (store.Session, error) {
	return a.newChat(ctx, project, harnessName, opts, nil)
}

// newChat is NewChat, with prepare to ready the chat before it's saved.
// Only an assistant's thread and tasks, which prepare theirs, make its
// chats: another would take the thread's place.
func (a *App) newChat(ctx context.Context, project, harnessName string, opts NewOptions, prepare func(*store.Session) error) (store.Session, error) {
	if err := a.enabled(project, harnessName); err != nil {
		return store.Session{}, err
	}
	m, ok := a.cfg.Harnesses.Get(harnessName)
	if !ok {
		return store.Session{}, fmt.Errorf("harness %q: %w", harnessName, ErrNotFound)
	}
	if m.Chat == "" {
		return store.Session{}, fmt.Errorf("harness %s runs only in a terminal", harnessName)
	}
	now := time.Now().UTC()
	s := store.Session{ID: newSessionID(), Project: project, Harness: harnessName, Created: now, Status: store.StatusRunning,
		Mode: store.ModeChat, State: store.ChatIdle, PermMode: a.startMode(project, m.Chat), Updated: now, Seen: now}
	if err := a.placeSession(ctx, &s, opts); err != nil {
		return store.Session{}, err
	}
	if _, err := a.assistantProject(project); err == nil && prepare == nil {
		return store.Session{}, errNotThread
	}
	if prepare != nil {
		if err := prepare(&s); err != nil {
			a.chat.Forget(s.ID)
			return store.Session{}, err
		}
	}
	if err := a.store.CreateSession(s); err != nil {
		a.chat.Forget(s.ID)
		return store.Session{}, err
	}
	a.Publish(Event{Type: "session", Project: project, Session: s.ID, Data: "started"})
	a.bg.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
		defer cancel()
		if err := a.StartSandbox(ctx, project); err != nil {
			log.Printf("skybreaker: warm sandbox %s: %v", project, err)
		}
	})
	return s, nil
}

// ChatFork starts a chat that carries on from a copy of another's
// conversation, which stays as it was. The copy shows the conversation so
// far; its harness forks the original's on the first message. An ended
// chat can be forked, to pick it up again. A chat on its own branch forks
// onto a new one, with a copy of its files as they are now, uncommitted
// work included.
func (a *App) ChatFork(ctx context.Context, id string) (store.Session, error) {
	s, err := a.chatSession(id)
	if err != nil {
		return store.Session{}, err
	}
	if _, err := a.assistantProject(s.Project); err == nil {
		return store.Session{}, errNotThread
	}
	if s.Resume == "" {
		return store.Session{}, errors.New("there's nothing to fork yet: send a message first")
	}
	if s.Status == store.StatusRunning && (s.State == store.ChatWorking || s.State == store.ChatWaiting) {
		return store.Session{}, ErrBusy
	}
	if err := a.enabled(s.Project, s.Harness); err != nil {
		return store.Session{}, err
	}
	now := time.Now().UTC()
	f := store.Session{ID: newSessionID(), Project: s.Project, Harness: s.Harness, Created: now, Status: store.StatusRunning,
		Mode: store.ModeChat, State: store.ChatIdle, PermMode: s.PermMode, ChosenModel: s.ChosenModel, ChosenEffort: s.ChosenEffort, Model: s.Model,
		Resume: s.Resume, Fork: true, Color: s.Color, Goal: s.Goal, Updated: now, Seen: now}
	if s.Title != "" {
		f.Title = s.Title + " (fork)"
	}
	if s.Worktree != "" {
		if err := a.forkWorktree(ctx, s, &f); err != nil {
			return store.Session{}, err
		}
	}
	if f.PermMode == "" {
		f.PermMode = chat.ModeAsk
	}
	if err := a.chat.Copy(s.ID, f.ID); err != nil {
		a.chat.Forget(f.ID)
		a.dropWorktree(f)
		return store.Session{}, err
	}
	if err := a.store.CreateSession(f); err != nil {
		a.chat.Forget(f.ID)
		a.dropWorktree(f)
		return store.Session{}, err
	}
	name := s.Title
	if name == "" {
		name = "the original chat"
	}
	a.chat.Note(f.ID, "Forked from "+name+". From here on, the two go their own ways.")
	a.Publish(Event{Type: "session", Project: f.Project, Session: f.ID, Data: "started"})
	return f, nil
}

// enabled refuses a harness the project doesn't enable (any more).
func (a *App) enabled(project, harnessName string) error {
	p, err := a.project(project)
	if err != nil {
		return err
	}
	if !slices.Contains(p.Harnesses, harnessName) {
		return fmt.Errorf("harness %q is not enabled for %s", harnessName, project)
	}
	return nil
}

// busyErr is ErrBusy in words of its own.
type busyErr string

func (e busyErr) Error() string      { return string(e) }
func (busyErr) Is(target error) bool { return target == ErrBusy }

// ReopenChat carries an ended chat on, as it was: its harness resumes the
// conversation, and its worktree comes back, on the next message.
func (a *App) ReopenChat(ctx context.Context, id string) (store.Session, error) {
	if _, err := a.chatSession(id); err != nil {
		return store.Session{}, err
	}
	defer a.lockSession(id)()
	return a.reopen(id)
}

// reopen is ReopenChat for a caller holding the chat's lock.
func (a *App) reopen(id string) (store.Session, error) {
	s, err := a.chatSession(id)
	if err != nil || s.Status == store.StatusRunning {
		return s, err
	}
	if err := a.enabled(s.Project, s.Harness); err != nil {
		return store.Session{}, err
	}
	if s.CarriedOn != "" {
		return store.Session{}, busyErr("this conversation carried on in a newer chat: continue it in the assistant's thread")
	}
	if s.Worktree != "" {
		// Waits out a tidy under way, so it can't take the worktree from
		// under the chat once it's live.
		defer a.lockProject(s.Project + "/worktrees")()
		for _, x := range a.store.ListSessions(s.Project) {
			if x.ID != s.ID && x.Worktree == s.Worktree && x.Status == store.StatusRunning {
				return store.Session{}, busyErr("another chat carries on in this one's branch: fork this one to pick it up")
			}
		}
	}
	a.store.UpdateSession(id, func(x *store.Session) { x.Status, x.Ended, x.Exit = store.StatusRunning, nil, nil })
	s, _ = a.store.Session(id)
	a.Publish(Event{Type: "session", Project: s.Project, Session: id, Data: "started"})
	return s, nil
}

// ChatCompact has a chat's harness summarise its conversation, to free
// its context window.
func (a *App) ChatCompact(ctx context.Context, id string) error {
	if _, err := a.chatSession(id); err != nil {
		return err
	}
	return a.chat.Compact(ctx, id)
}

// chatSession returns a chat session, or an error for anything else.
func (a *App) chatSession(id string) (store.Session, error) {
	s, err := a.session(id)
	if err != nil {
		return s, err
	}
	if s.Mode != store.ModeChat {
		return s, fmt.Errorf("session %s is a terminal", id)
	}
	return s, nil
}

// ChatSend sends a chat a message, carrying it on first if it had ended.
func (a *App) ChatSend(ctx context.Context, id, text string, files ...chat.File) error {
	return a.chatSend(id, func() error { return a.chat.Send(ctx, id, text, files...) })
}

// ChatHandback tells a chat you've handed its browser back, on site.
func (a *App) ChatHandback(ctx context.Context, id, text, site string) error {
	return a.chatSend(id, func() error { return a.chat.Handback(ctx, id, text, site) })
}

func (a *App) chatSend(id string, send func() error) error {
	if _, err := a.chatSession(id); err != nil {
		return err
	}
	defer a.lockSession(id)()
	if _, err := a.reopen(id); err != nil {
		return err
	}
	a.store.UpdateSession(id, func(x *store.Session) {
		x.Seen = time.Now().UTC()
		x.Pull.Rounds, x.Pull.Paused = 0, false
	})
	return send()
}

func (a *App) ChatAnswer(ctx context.Context, id, ask string, ans chat.Answer) error {
	if _, err := a.chatSession(id); err != nil {
		return err
	}
	return a.chat.Answer(ctx, id, ask, ans)
}

func (a *App) ChatInterrupt(ctx context.Context, id string) error {
	if _, err := a.chatSession(id); err != nil {
		return err
	}
	return a.chat.Interrupt(id)
}

func (a *App) ChatMode(ctx context.Context, id, mode string) error {
	if _, err := a.chatSession(id); err != nil {
		return err
	}
	return a.chat.SetMode(ctx, id, mode)
}

// ChatModel switches a chat to one of the models its harness lists.
func (a *App) ChatModel(ctx context.Context, id, model string) (store.Session, error) {
	if _, err := a.chatSession(id); err != nil {
		return store.Session{}, err
	}
	if err := a.chat.SetModel(ctx, id, model); err != nil {
		return store.Session{}, err
	}
	return a.chatSession(id)
}

// ChatEffort switches a chat to one of the effort levels its model lists.
func (a *App) ChatEffort(ctx context.Context, id, effort string) (store.Session, error) {
	if _, err := a.chatSession(id); err != nil {
		return store.Session{}, err
	}
	if err := a.chat.SetEffort(ctx, id, effort); err != nil {
		return store.Session{}, err
	}
	return a.chatSession(id)
}

// ChatLogin starts the harness's sign-in. Harnesses that sign in only in
// their terminal UI return chat.ErrTerminalLogin; the UI opens the
// terminal instead.
func (a *App) ChatLogin(ctx context.Context, id string) error {
	if _, err := a.chatSession(id); err != nil {
		return err
	}
	return a.chat.Login(ctx, id)
}

func (a *App) ChatEvents(id string, after int64) ([]chat.Event, error) {
	if _, err := a.chatSession(id); err != nil {
		return nil, err
	}
	return a.chat.Events(id, after)
}

func (a *App) ChatSubscribe(id string) (<-chan chat.Event, func()) { return a.chat.Subscribe(id) }

// MarkSeen records that the user has looked at a session, which takes a
// finished one out of the inbox's "for you" list.
func (a *App) MarkSeen(id string) error {
	if _, err := a.session(id); err != nil {
		return err
	}
	return a.store.UpdateSession(id, func(x *store.Session) { x.Seen = time.Now().UTC() })
}

// startMode is the permission mode a new chat starts in: the one set in
// the app for its project, else the default, else ask. One its harness
// lacks falls back to the harness's safest.
func (a *App) startMode(project, driver string) string {
	set, err := config.ReadChatModes(a.cfg.ChatModes())
	if err != nil {
		log.Printf("skybreaker: chat modes: %v", err)
	}
	modes := chat.New(driver).Modes()
	if mode := set.For(project); slices.Contains(modes, mode) {
		return mode
	}
	return modes[0]
}

// ChatModes lists the permission modes a harness's chat has.
func (a *App) ChatModes(harnessName string) []string {
	m, ok := a.cfg.Harnesses.Get(harnessName)
	if !ok || m.Chat == "" {
		return nil
	}
	return chat.New(m.Chat).Modes()
}

var ErrBusy = errors.New("the agent is working; stop it first")

// ChatTerminal continues a chat in its harness's terminal UI, resumed on
// the same conversation, for sign-ins and anything the chat can't do. The
// chat's process stops first; the next message closes the terminal and
// resumes the chat.
func (a *App) ChatTerminal(ctx context.Context, id string) (store.Session, error) {
	s, err := a.chatSession(id)
	if err != nil {
		return store.Session{}, err
	}
	if s.Status != store.StatusRunning {
		return store.Session{}, errors.New("this session has ended")
	}
	if s.State == store.ChatWorking || s.State == store.ChatWaiting {
		return store.Session{}, ErrBusy
	}
	if s.Fork {
		// Its terminal would resume the original, not a copy.
		return store.Session{}, errors.New("send the fork a message first, then open its terminal")
	}
	if s.Terminal != "" {
		if t, ok := a.store.Session(s.Terminal); ok && t.Status == store.StatusRunning {
			if _, err := a.sandbox.Exec(ctx, s.Project, tmux.Has(t.ID)...); err == nil {
				return t, nil
			}
		}
	}
	m, ok := a.cfg.Harnesses.Get(s.Harness)
	if !ok {
		return store.Session{}, fmt.Errorf("harness %q: %w", s.Harness, ErrNotFound)
	}
	a.chat.Stop(id)
	if err := a.StartSandbox(ctx, s.Project); err != nil {
		return store.Session{}, err
	}
	if err := a.EnsureTmux(ctx, s.Project); err != nil {
		return store.Session{}, err
	}
	if err := a.ensureWorktree(ctx, &s, nil); err != nil {
		return store.Session{}, err
	}
	t := store.Session{ID: newSessionID(), Project: s.Project, Harness: s.Harness, Title: s.Title, Created: time.Now().UTC(), Status: store.StatusRunning, Parent: s.ID,
		Worktree: s.Worktree, Branch: s.Branch, BranchFrom: s.BranchFrom}
	if _, err := a.sandbox.Exec(ctx, s.Project, tmux.NewSession(t.ID, workdir(s), a.sessionEnv(t, m), m.ResumeArgs(s.Resume, a.ProjectMCP(s.Project)))...); err != nil {
		return store.Session{}, fmt.Errorf("start %s terminal: %w", s.Harness, err)
	}
	if err := a.store.CreateSession(t); err != nil {
		return store.Session{}, err
	}
	a.store.UpdateSession(id, func(x *store.Session) { x.Terminal = t.ID })
	a.chat.Note(id, "Continued in the terminal.")
	return t, nil
}

// closeChatTerminal ends the terminal a chat was continued in.
func (a *App) closeChatTerminal(ctx context.Context, s store.Session) {
	if s.Terminal == "" {
		return
	}
	if tmux.ValidName(s.Terminal) {
		a.sandbox.Exec(ctx, s.Project, tmux.Kill(s.Terminal)...)
	}
	now := time.Now().UTC()
	a.store.UpdateSession(s.Terminal, func(x *store.Session) {
		if x.Status == store.StatusRunning {
			x.Status, x.Ended = store.StatusKilled, &now
		}
	})
	a.store.UpdateSession(s.ID, func(x *store.Session) { x.Terminal = "" })
}

// killChat ends a chat: its process stops and its terminal closes. The
// conversation stays readable.
func (a *App) killChat(ctx context.Context, s store.Session) {
	a.chat.Stop(s.ID)
	a.closeChatTerminal(ctx, s)
	now := time.Now().UTC()
	a.store.UpdateSession(s.ID, func(x *store.Session) {
		if x.Status == store.StatusRunning {
			x.Status, x.Ended = store.StatusKilled, &now
		}
		if x.State == store.ChatWorking || x.State == store.ChatWaiting {
			x.State = store.ChatIdle
		}
	})
}

// endChat ends chat s and tidies after it. Callers hold its lock.
func (a *App) endChat(ctx context.Context, s store.Session) {
	a.killChat(ctx, s)
	if s.Task != "" {
		a.bg.Go(func() { a.taskCard(s.ID) })
	}
	a.Publish(Event{Type: "session", Project: s.Project, Session: s.ID, Data: "killed"})
	a.sessionEnded(s)
}

// chatUsage counts what a chat's harness reports using, under the
// harness's name, and tells the UI.
func (a *App) chatUsage(id string, u *chat.Usage, limits []chat.Limit) {
	s, ok := a.store.Session(id)
	if a.usage == nil || !ok {
		return
	}
	if u != nil {
		a.usage.Add(s.Harness, *u)
	}
	if limits != nil {
		a.usage.SetLimits(s.Harness, limits)
	}
	a.Publish(Event{Type: "usage", Project: s.Project})
}

// chatChanged tells the UI a chat's state moved, and buzzes the phone when
// it now needs the user, or finished. The push names the harness and
// project, never what the agent said.
func (a *App) chatChanged(before, after store.Session) {
	a.Publish(Event{Type: "session", Project: after.Project, Session: after.ID, Data: after.State})
	a.taskChanged(before, after)
	var m notify.Message
	who := "A " + after.Harness + " session in " + after.Project
	bucket := after.ID + "-" + strconv.FormatInt(time.Now().Unix()/60, 10)
	switch {
	case after.State == store.ChatWaiting:
		m = notify.Message{ID: "chat-wait-" + bucket, Title: "Approval waiting", Body: who + " is waiting for you.", Priority: 4}
	case after.State == store.ChatLogin:
		m = notify.Message{ID: "chat-login-" + bucket, Title: "Sign-in needed", Body: who + " needs signing in.", Priority: 4}
	case after.State == store.ChatFailed:
		m = notify.Message{ID: "chat-fail-" + bucket, Title: "Agent stopped", Body: who + " hit a problem."}
	case after.State == store.ChatIdle && before.State == store.ChatWorking:
		m = notify.Message{ID: "chat-done-" + bucket, Title: "Agent finished", Body: who + " is done.", Priority: 2}
	default:
		return
	}
	m.Click = a.uiURL + "/s/" + after.ID
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := a.notify.Notify(ctx, m); err != nil {
			log.Printf("skybreaker: notify: %v", err)
		}
	}()
}

// ReapChats stops chat harnesses that have sat idle; their conversations
// resume on the next message.
func (a *App) ReapChats() { a.chat.Reap(time.Now()) }

// RecoverChats settles chats that were mid-turn when skybreaker last stopped.
func (a *App) RecoverChats() { a.chat.Recover(a.store.ListSessions("")) }

// CloseChats stops every chat harness, and waits for background starts,
// for shutdown.
func (a *App) CloseChats() {
	a.chat.Close()
	a.bg.Wait()
}

// IsChat reports whether session is a chat in project: the hook handler
// lets those sessions' permission prompts through to the chat.
func (a *App) IsChat(project, session string) bool {
	s, ok := a.store.Session(session)
	return ok && s.Project == project && s.Mode == store.ModeChat
}

// chatSessionsOf lists a project's chat sessions.
func (a *App) chatSessionsOf(project string) []store.Session {
	var out []store.Session
	for _, s := range a.store.ListSessions(project) {
		if s.Mode == store.ModeChat {
			out = append(out, s)
		}
	}
	return out
}

// stopChats stops a project's chat harnesses, before its sandbox goes.
func (a *App) stopChats(project string) {
	var wg sync.WaitGroup
	for _, s := range a.chatSessionsOf(project) {
		wg.Go(func() { a.chat.Stop(s.ID) })
	}
	wg.Wait()
}

// --- review ---------------------------------------------------------------

// Review is what a project's workspace has changed against the branch it
// started from: commits not yet on the base, and the diff, uncommitted work
// and new files included.
type Review struct {
	Branch  string       `json:"branch"`
	Base    string       `json:"base,omitempty"`
	Commits []Commit     `json:"commits"`
	Files   []FileChange `json:"files"`
	Diff    string       `json:"diff"`
	Clean   bool         `json:"clean"`
}

type Commit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
	When    string `json:"when"`
}

type FileChange struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	New     bool   `json:"new,omitempty"`
}

const maxReviewDiff = 2 << 20

// Review is what the project's workspace has changed. It runs git in the
// sandbox, never on the host.
func (a *App) Review(ctx context.Context, project string) (Review, error) {
	p, err := a.repoProject(project)
	if err != nil {
		return Review{}, err
	}
	return a.review(ctx, project, p.Branch, workspaceDir)
}

// ReviewSession is what a session has changed: in its own worktree, or the
// workspace for a session without one.
func (a *App) ReviewSession(ctx context.Context, id string) (Review, error) {
	s, err := a.session(id)
	if err != nil {
		return Review{}, err
	}
	p, err := a.repoProject(s.Project)
	if err != nil {
		return Review{}, err
	}
	if s.Worktree == "" {
		return a.review(ctx, s.Project, p.Branch, workspaceDir)
	}
	if st, err := a.sandbox.Status(ctx, s.Project); err != nil || !st.Running {
		return Review{}, errors.New("the project's sandbox is stopped: start it to see this chat's changes")
	}
	if ok, err := a.present(ctx, s.Project, s.Worktree+"/.git"); err != nil {
		return Review{}, err
	} else if !ok {
		if s.Status == store.StatusRunning {
			return Review{}, errors.New("this chat's branch is made when its agent first starts: send it a message")
		}
		return Review{}, fmt.Errorf("this chat ended and its copy of the repository was tidied away; its work is on branch %s", s.Branch)
	}
	return a.review(ctx, s.Project, p.Branch, s.Worktree)
}

func (a *App) review(ctx context.Context, project, defaultBranch, dir string) (Review, error) {
	git := func(args ...string) (string, error) {
		out, err := a.sandbox.Exec(ctx, project, GitArgs(dir, args...)...)
		if errors.Is(err, sandbox.ErrOutputLimit) {
			err = nil // a diff cut off by the runner is truncated below
		}
		return string(out), err
	}
	branch, err := git("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return Review{}, fmt.Errorf("the workspace isn't a git repository yet: %w", err)
	}
	r := Review{Branch: strings.TrimSpace(branch), Commits: []Commit{}, Files: []FileChange{}}
	// The base: where this branch left the project's branch on the remote.
	base := ""
	for _, ref := range []string{"@{upstream}", "origin/" + defaultBranch, "origin/HEAD", "origin/main", "origin/master"} {
		if r.Branch != "HEAD" && ref == "@{upstream}" {
			if up, err := git("rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); err == nil && strings.TrimSpace(up) != "origin/"+r.Branch {
				continue // a feature branch tracking itself: compare with the default branch
			}
		}
		if mb, err := git("merge-base", ref, "HEAD"); err == nil && strings.TrimSpace(mb) != "" {
			base = strings.TrimSpace(mb)
			r.Base = ref
			switch ref {
			case "@{upstream}":
				r.Base = "upstream"
			case "origin/HEAD":
				// origin/HEAD stands for the remote's default branch: say which.
				if b, err := git("rev-parse", "--abbrev-ref", "origin/HEAD"); err == nil && strings.TrimSpace(b) != "" && strings.TrimSpace(b) != "origin/HEAD" {
					r.Base = strings.TrimSpace(b)
				}
			}
			break
		}
	}
	diffFrom := "HEAD"
	if base != "" {
		diffFrom = base
		if log, err := git("log", "--format=%h%x1f%s%x1f%cI", "--max-count=50", base+"..HEAD"); err == nil {
			for line := range strings.SplitSeq(strings.TrimSpace(log), "\n") {
				if f := strings.Split(line, "\x1f"); len(f) == 3 {
					r.Commits = append(r.Commits, Commit{SHA: f[0], Subject: f[1], When: f[2]})
				}
			}
		}
	}
	diffArgs := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--find-renames"}
	if numstat, err := git(append(diffArgs, "--numstat", diffFrom)...); err == nil {
		for line := range strings.SplitSeq(strings.TrimSpace(numstat), "\n") {
			f := strings.SplitN(line, "\t", 3)
			if len(f) != 3 {
				continue
			}
			add, _ := strconv.Atoi(f[0])
			del, _ := strconv.Atoi(f[1])
			r.Files = append(r.Files, FileChange{Path: f[2], Added: add, Removed: del})
		}
	}
	diff, _ := git(append(diffArgs, diffFrom)...)
	var b strings.Builder
	b.WriteString(diff)
	// New files aren't in git diff until added; show each as a whole-file
	// addition, without touching the agent's index.
	if untracked, err := git("ls-files", "--others", "--exclude-standard", "-z"); err == nil {
		files := strings.Split(strings.TrimRight(untracked, "\x00"), "\x00")
		for i, f := range files {
			if f == "" {
				continue
			}
			if i >= 50 {
				fmt.Fprintf(&b, "\n… and %d more new files\n", len(files)-i)
				break
			}
			// --no-index exits 1 when the files differ, which they do.
			d, _ := git("diff", "--no-ext-diff", "--no-textconv", "--no-color", "--no-index", "--", "/dev/null", f)
			added, _ := countLines(d)
			r.Files = append(r.Files, FileChange{Path: f, Added: added, New: true})
			b.WriteString(d)
			if b.Len() > maxReviewDiff {
				break
			}
		}
	}
	r.Diff = b.String()
	if len(r.Diff) > maxReviewDiff {
		r.Diff = r.Diff[:maxReviewDiff] + "\n… diff truncated\n"
	}
	r.Clean = len(r.Files) == 0 && len(r.Commits) == 0
	return r, nil
}

func countLines(diff string) (added, removed int) {
	for line := range strings.SplitSeq(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			removed++
		}
	}
	return
}

// idleFor is how long a sandbox's chats must have been quiet before it is
// restarted onto a new image.
const idleFor = 15 * time.Minute

// RefreshSandboxes restarts running sandboxes whose image the weekly
// rebuild has replaced, or that an earlier Skybreaker started without a
// place for worktrees, once nothing in them is in use: no live terminal,
// no scheduled run, no chat working or touched lately. Chats resume in the
// new sandbox on their next message.
func (a *App) RefreshSandboxes(ctx context.Context) {
	for _, project := range slices.Sorted(maps.Keys(a.cfg.Projects)) {
		stale, err := a.sandbox.ImageStale(ctx, project, a.cfg.Sandbox.Image)
		if err != nil {
			continue
		}
		if st, err := a.sandbox.Status(ctx, project); err != nil || !st.Running || a.busy(ctx, project) {
			continue
		}
		if !stale && a.holdsWorktrees(ctx, project) {
			continue
		}
		why := "onto the new sandbox image"
		if !stale {
			why = "so sessions can have their own branch"
		}
		log.Printf("skybreaker: restarting %s %s", project, why)
		if err := a.StopSandbox(ctx, project); err != nil {
			log.Printf("skybreaker: refresh %s: %v", project, err)
			continue
		}
		if err := a.StartSandbox(ctx, project); err != nil {
			log.Printf("skybreaker: refresh %s: %v", project, err)
			continue
		}
		a.Publish(Event{Type: "sandbox", Project: project, Data: "restarted " + why})
	}
}

// SweepContainers stops what agents left running in a project's inner
// Docker once nothing in the project has been in use for idleFor: no live
// terminal, no scheduled run, no chat working or touched lately. Each quiet
// spell is swept once.
func (a *App) SweepContainers(ctx context.Context) {
	for _, project := range slices.Sorted(maps.Keys(a.cfg.Projects)) {
		if st, err := a.sandbox.Status(ctx, project); err != nil || !st.Running {
			continue
		}
		a.sweptMu.Lock()
		done := a.swept[project]
		a.sweptMu.Unlock()
		if a.busy(ctx, project) {
			a.markSwept(project, false)
			continue
		}
		if !done {
			a.stopContainers(ctx, project)
			a.markSwept(project, true)
		}
	}
}

func (a *App) markSwept(project string, v bool) {
	a.sweptMu.Lock()
	defer a.sweptMu.Unlock()
	if a.swept == nil {
		a.swept = map[string]bool{}
	}
	a.swept[project] = v
}

// sessionEnded tidies away the worktree of a session that just ended, if
// it had one, and stops a project's containers when it was the last live
// session and no scheduled run is going.
func (a *App) sessionEnded(s store.Session) {
	project := s.Project
	if s.Worktree != "" {
		a.bg.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			st, err := a.sandbox.Status(ctx, project)
			cancel()
			if err == nil && st.Running {
				a.tidyWorktrees(context.Background(), project, false)
			}
		})
	}
	for _, x := range a.store.ListSessions(project) {
		if x.Status == store.StatusRunning {
			return
		}
	}
	for _, r := range a.store.ListRuns(project, 50) {
		if r.Status == store.RunRunning {
			return
		}
	}
	a.bg.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		a.stopContainers(ctx, project)
	})
}

// stopContainers stops every container running in a project's inner
// Docker. Stopped, not removed, so an agent or you can still look at them.
func (a *App) stopContainers(ctx context.Context, project string) {
	out, err := a.sandbox.Exec(ctx, project, "docker", "ps", "-q")
	if err != nil {
		return
	}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return
	}
	if _, err := a.sandbox.Exec(ctx, project, append([]string{"docker", "stop", "-t", "10"}, ids...)...); err != nil {
		log.Printf("skybreaker: stop containers in %s: %v", project, err)
		return
	}
	log.Printf("skybreaker: stopped %d containers in %s, which nothing was using", len(ids), project)
}

// busy reports whether anything in a project's sandbox is in use.
func (a *App) busy(ctx context.Context, project string) bool {
	for _, r := range a.store.ListRuns(project, 50) {
		if r.Status == store.RunRunning {
			return true
		}
	}
	sessions, err := a.Sessions(ctx, project)
	if err != nil {
		return true
	}
	for _, s := range sessions {
		if s.Status != store.StatusRunning {
			continue
		}
		if s.Mode != store.ModeChat {
			return true // a live terminal would lose its process
		}
		if s.State == store.ChatWorking || s.State == store.ChatWaiting || time.Since(s.Updated) < idleFor || a.chat.Running(s.ID) && time.Since(s.Seen) < idleFor {
			return true
		}
	}
	return false
}

// MCPCaller places a request to the sandboxes' MCP mount,
// /mcp/<project>/<hook token>/<session or ->: it must come from that
// project's own sandbox network and carry its hook token, and a session it
// names must be one of that project's. A URL naming none can name it in
// X-Skybreaker-Session, as pi's config, shared by its sessions, does; a
// session there that isn't the project's names none.
func (a *App) MCPCaller(r *http.Request) (mcp.Caller, error) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/mcp/"), "/")
	if len(parts) != 3 {
		return mcp.Caller{}, ErrNotFound
	}
	project, token, session := parts[0], parts[1], parts[2]
	if h := r.Header.Get("X-Skybreaker-Session"); session == "-" && h != "" {
		if s, ok := a.store.Session(h); ok && s.Project == project {
			session = h
		}
	}
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return mcp.Caller{}, err
	}
	if from, ok := a.cfg.ProjectOf(ap.Addr()); !ok || from != project || !a.CheckHookToken(project, token) {
		return mcp.Caller{}, errors.New("mcp: not this project's sandbox")
	}
	c := mcp.Caller{Project: project, Session: session}
	if session != "-" {
		s, ok := a.store.Session(session)
		if !ok || s.Project != project {
			return mcp.Caller{}, errors.New("mcp: not this project's session")
		}
		c.Work = "chat:" + s.ID
		if s.Worktree != "" {
			c.Work = "worktree:" + s.Worktree
		}
	}
	return c, nil
}

// ChatColors are the colours a chat can be given, to tell sessions apart.
var ChatColors = []string{"sun", "titan", "mars", "aurora", "stormlight", "voidlight", "venus", "comet"}

const (
	maxTitle = 200
	maxGoal  = 500
)

// SessionEdit changes what a session is called, its colour and its goal;
// a nil field is left as it is, and an empty colour or goal clears it.
type SessionEdit struct {
	Title *string `json:"title"`
	Color *string `json:"color"`
	Goal  *string `json:"goal"`
}

// CheckEdit refuses an edit the UI shouldn't store.
func CheckEdit(e SessionEdit) error {
	if e.Title != nil {
		t := strings.TrimSpace(*e.Title)
		if t == "" {
			return errors.New("a title can't be empty")
		}
		if utf8.RuneCountInString(t) > maxTitle {
			return fmt.Errorf("a title is at most %d characters", maxTitle)
		}
	}
	if e.Color != nil && *e.Color != "" && !slices.Contains(ChatColors, *e.Color) {
		return fmt.Errorf("colours are %s", strings.Join(ChatColors, ", "))
	}
	if e.Goal != nil && utf8.RuneCountInString(strings.TrimSpace(*e.Goal)) > maxGoal {
		return fmt.Errorf("a goal is at most %d characters", maxGoal)
	}
	return nil
}

// EditSession renames a session, colours it or sets its goal.
func (a *App) EditSession(id string, e SessionEdit) (store.Session, error) {
	if err := CheckEdit(e); err != nil {
		return store.Session{}, err
	}
	if _, ok := a.store.Session(id); !ok {
		return store.Session{}, fmt.Errorf("session %s: %w", id, ErrNotFound)
	}
	err := a.store.UpdateSession(id, func(s *store.Session) {
		if e.Title != nil {
			s.Title = strings.TrimSpace(*e.Title)
		}
		if e.Color != nil {
			s.Color = *e.Color
		}
		if e.Goal != nil {
			s.Goal = strings.TrimSpace(*e.Goal)
		}
	})
	if err != nil {
		return store.Session{}, err
	}
	s, _ := a.store.Session(id)
	a.Publish(Event{Type: "session", Project: s.Project, Session: id, Data: "edited"})
	return s, nil
}
