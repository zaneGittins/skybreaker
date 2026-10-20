package chat

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zaneGittins/skybreaker/internal/control/store"
)

// Proc is a running harness: its pipes, and how to end it.
type Proc struct {
	Stdin  io.WriteCloser
	Stdout io.Reader
	Wait   func() error  // after Stdout ends
	Kill   func()        // ends it now
	Stderr func() string // the end of its stderr, to explain a crash
}

// Starter starts argv for a session inside its sandbox.
type Starter func(ctx context.Context, s store.Session, argv []string, env map[string]string) (*Proc, error)

// Sessions is the part of the store the manager uses.
type Sessions interface {
	Session(id string) (store.Session, bool)
	UpdateSession(id string, f func(*store.Session)) error
}

type Config struct {
	Log      *Log
	Sessions Sessions
	Start    Starter
	// Options gives a session's start options (cwd, MCP); the manager adds
	// the mode and the conversation to resume.
	Options func(s store.Session) Options
	// Driver picks a session's driver; nil means New(s.Harness).
	Driver func(s store.Session) Driver
	// OnChange hears every change of a session's State.
	OnChange func(before, after store.Session)
	// IdleStop is how long an idle harness process lives (default 20m).
	IdleStop time.Duration
}

// Manager runs chat sessions' harness processes.
type Manager struct {
	c Config

	mu   sync.Mutex
	runs map[string]*run

	subMu sync.Mutex
	subs  map[string]map[chan Event]struct{}
}

// run is one session's process and what it's doing. mu serialises the
// driver, which isn't safe for concurrent use, and the writes to stdin.
type run struct {
	id string

	mu       sync.Mutex
	drv      Driver
	proc     *Proc
	in       chan []byte // lines for the process's writer
	gen      int
	exited   chan struct{}
	stopping bool
	working  bool
	login    bool
	models   []string          // the models the harness last listed
	asks     map[string]string // open ask → its title
	used     time.Time

	// jobs that may start the process run one at a time, in order.
	jobMu sync.Mutex
	jobs  []func()
	busy  bool
}

const subBuffer = 256

func NewManager(c Config) *Manager {
	if c.IdleStop == 0 {
		c.IdleStop = 20 * time.Minute
	}
	if c.Driver == nil {
		c.Driver = func(s store.Session) Driver { return New(s.Harness) }
	}
	if c.Options == nil {
		c.Options = func(store.Session) Options { return Options{Cwd: "/workspace"} }
	}
	return &Manager{c: c, runs: map[string]*run{}, subs: map[string]map[chan Event]struct{}{}}
}

var errNotChat = errors.New("not a chat session")

// chat returns a live chat session.
func (m *Manager) chat(id string) (store.Session, error) {
	s, ok := m.c.Sessions.Session(id)
	if !ok {
		return s, fmt.Errorf("session %q: not found", id)
	}
	if s.Mode != store.ModeChat {
		return s, errNotChat
	}
	if s.Status != store.StatusRunning {
		return s, errors.New("this session has ended")
	}
	return s, nil
}

func (m *Manager) run(id string) *run {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.runs[id]
	if r == nil {
		r = &run{id: id, asks: map[string]string{}}
		m.runs[id] = r
	}
	return r
}

// enqueue runs job after the session's earlier jobs.
func (r *run) enqueue(job func()) {
	r.jobMu.Lock()
	r.jobs = append(r.jobs, job)
	if r.busy {
		r.jobMu.Unlock()
		return
	}
	r.busy = true
	r.jobMu.Unlock()
	go func() {
		for {
			r.jobMu.Lock()
			if len(r.jobs) == 0 {
				r.busy = false
				r.jobMu.Unlock()
				return
			}
			j := r.jobs[0]
			r.jobs = r.jobs[1:]
			r.jobMu.Unlock()
			j()
		}
	}()
}

// Send records a message and delivers it, starting (or resuming) the
// harness first if it isn't running. It returns once the message is
// recorded; delivery failures show in the conversation.
func (m *Manager) Send(ctx context.Context, id, text string) error {
	s, err := m.chat(id)
	if err != nil {
		return err
	}
	text = strings.TrimRight(text, " \t\r\n")
	if strings.TrimSpace(text) == "" {
		return errors.New("empty message")
	}
	if len(text) > 200<<10 {
		return errors.New("message too long")
	}
	r := m.run(id)
	r.mu.Lock()
	r.used = time.Now()
	m.emit(r, Event{Kind: KindUser, ID: "u" + strconv.FormatInt(time.Now().UnixNano(), 36), Text: text})
	m.emit(r, Event{Kind: KindTurn, Turn: &Turn{State: TurnWorking}})
	r.mu.Unlock()
	if s.Title == "" {
		title := firstLine(text)
		title = strings.TrimSuffix(title, " …")
		if len(title) > 80 {
			title = strings.TrimSpace(clip(title, 77)[:77]) + "…"
		}
		m.c.Sessions.UpdateSession(id, func(x *store.Session) { x.Title = title })
	}
	r.enqueue(func() {
		if err := m.ensure(r); err != nil {
			r.mu.Lock()
			m.failTurn(r, "The agent didn't start: "+err.Error())
			r.mu.Unlock()
			return
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		lines, err := r.drv.Send(text)
		if err != nil {
			m.failTurn(r, err.Error())
			return
		}
		m.write(r, lines)
	})
	return nil
}

// failTurn ends the current turn with an error. Callers hold r.mu.
func (m *Manager) failTurn(r *run, msg string) {
	m.emit(r, Event{Kind: KindTurn, Turn: &Turn{State: TurnFailed, Error: msg}})
}

// ensure starts the session's harness unless it runs.
func (m *Manager) ensure(r *run) error {
	r.mu.Lock()
	running := r.proc != nil
	r.mu.Unlock()
	if running {
		return nil
	}
	s, err := m.chat(r.id)
	if err != nil {
		return err
	}
	drv := m.c.Driver(s)
	if drv == nil {
		return fmt.Errorf("harness %s has no chat driver", s.Harness)
	}
	opts := m.c.Options(s)
	opts.Mode, opts.Resume, opts.Model, opts.Fork = s.PermMode, s.Resume, s.ChosenModel, s.Fork
	if opts.Mode == "" {
		opts.Mode = ModeAsk
	}
	if opts.Resume == "" {
		opts.NewID = newUUID()
	}
	env := map[string]string{"SKYBREAKER_SESSION": s.ID}
	for k, v := range drv.Env() {
		env[k] = v
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	p, err := m.c.Start(ctx, s, drv.Argv(opts), env)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.drv, r.proc, r.stopping = drv, p, false
	r.gen++
	r.exited = make(chan struct{})
	r.used = time.Now()
	// Writes go through their own goroutine: a harness may answer before it
	// reads all it was sent, and the reader must keep reading meanwhile.
	r.in = make(chan []byte, 4096)
	go writer(p, r.in)
	go m.read(r, r.gen, p, r.exited)
	m.write(r, drv.Open(opts))
	return nil
}

// writer copies lines to the process's stdin until in closes, then closes
// stdin, which ends both harnesses cleanly.
func writer(p *Proc, in <-chan []byte) {
	defer p.Stdin.Close()
	for l := range in {
		if _, err := p.Stdin.Write(l); err != nil {
			for range in {
			}
			return
		}
	}
}

// write queues lines for the harness. Callers hold r.mu.
func (m *Manager) write(r *run, lines [][]byte) {
	if r.in == nil {
		return
	}
	for _, l := range lines {
		r.in <- append(l, '\n')
	}
}

// closeInput ends the process's input. Callers hold r.mu.
func (r *run) closeInput() {
	if r.in != nil {
		close(r.in)
		r.in = nil
	}
}

// maxLine bounds one line from a harness; longer ones are dropped.
const maxLine = 16 << 20

func (m *Manager) read(r *run, gen int, p *Proc, exited chan struct{}) {
	defer close(exited)
	br := bufio.NewReaderSize(p.Stdout, 256<<10)
	for {
		line, err := readLine(br)
		if len(line) > 0 {
			r.mu.Lock()
			if r.gen != gen {
				r.mu.Unlock()
				break
			}
			r.used = time.Now()
			out := r.drv.Read(line)
			m.write(r, out.Write)
			for _, e := range out.Events {
				m.emit(r, e)
			}
			if out.Resume != "" {
				m.c.Sessions.UpdateSession(r.id, func(x *store.Session) { x.Resume, x.Fork = out.Resume, false })
			}
			r.mu.Unlock()
		}
		if err != nil {
			break
		}
	}
	if p.Wait != nil {
		p.Wait()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.gen != gen {
		return
	}
	r.proc = nil
	r.closeInput()
	for id := range r.asks {
		m.emit(r, Event{Kind: KindAnswered, ID: id, Text: "withdrawn"})
	}
	if r.working {
		if r.stopping {
			m.emit(r, Event{Kind: KindTurn, Turn: &Turn{State: TurnInterrupted}})
		} else {
			msg := "The agent stopped unexpectedly."
			if p.Stderr != nil {
				if tail := strings.TrimSpace(p.Stderr()); tail != "" {
					msg += "\n" + clipTail(tail, 2000)
				}
			}
			m.failTurn(r, msg)
		}
	}
}

// readLine reads one line, dropping any longer than maxLine.
func readLine(br *bufio.Reader) ([]byte, error) {
	var buf []byte
	tooLong := false
	for {
		chunk, err := br.ReadSlice('\n')
		if !tooLong {
			if len(buf)+len(chunk) > maxLine {
				tooLong, buf = true, nil
			} else {
				buf = append(buf, chunk...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if tooLong {
			return nil, err
		}
		return bytes.TrimRight(buf, "\r\n"), err
	}
}

// emit stores an event (deltas aren't stored), sends it to subscribers and
// moves the session's state along. Callers hold r.mu.
func (m *Manager) emit(r *run, e Event) {
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	if e.Kind != KindDelta {
		stored, err := m.c.Log.Append(r.id, e)
		if err != nil {
			log.Printf("skybreaker: chat log %s: %v", r.id, err)
		}
		e = stored
	}
	m.publish(r.id, e)

	switch e.Kind {
	case KindAsk:
		r.asks[e.ID] = e.Ask.Title
		m.setState(r, store.ChatWaiting, e.Ask.Title, nil)
	case KindAnswered:
		delete(r.asks, e.ID)
		if len(r.asks) == 0 && r.working {
			m.setState(r, store.ChatWorking, "", nil)
		}
	case KindTurn:
		switch e.Turn.State {
		case TurnWorking:
			r.working = true
			if len(r.asks) == 0 {
				m.setState(r, store.ChatWorking, "", nil)
			}
		case TurnFailed:
			r.working = false
			if r.login {
				m.setState(r, store.ChatLogin, "Sign in to continue", nil)
			} else {
				m.setState(r, store.ChatFailed, firstLine(e.Turn.Error), nil)
			}
		default:
			r.working, r.login = false, false
			m.setState(r, store.ChatIdle, "", nil)
		}
	case KindLogin:
		switch {
		case e.Login.Done:
			r.login = false
			if !r.working {
				m.setState(r, store.ChatIdle, "", nil)
			}
		case e.Login.Needed:
			r.login = true
			m.setState(r, store.ChatLogin, "Sign in to continue", nil)
		}
	case KindInfo:
		if len(e.Info.Models) > 0 {
			r.models = r.models[:0]
			for _, md := range e.Info.Models {
				r.models = append(r.models, md.ID)
			}
		}
		m.setState(r, "", "", func(x *store.Session) {
			if e.Info.Model != "" {
				x.Model = e.Info.Model
			}
			if e.Info.Mode != "" {
				x.PermMode = e.Info.Mode
			}
		})
	}
}

// setState records a session's state (empty keeps it) and tells OnChange.
func (m *Manager) setState(r *run, state, note string, more func(*store.Session)) {
	var before, after store.Session
	err := m.c.Sessions.UpdateSession(r.id, func(x *store.Session) {
		before = *x
		if state != "" {
			x.State, x.Note = state, note
			x.Updated = time.Now().UTC()
		}
		if more != nil {
			more(x)
		}
		after = *x
	})
	if err == nil && before.State != after.State && m.c.OnChange != nil {
		m.c.OnChange(before, after)
	}
}

func (m *Manager) publish(id string, e Event) {
	m.subMu.Lock()
	defer m.subMu.Unlock()
	for ch := range m.subs[id] {
		select {
		case ch <- e:
		default:
			// Too slow: cut it off, so it reconnects and replays rather
			// than silently missing an event.
			delete(m.subs[id], ch)
			close(ch)
		}
	}
}

// Subscribe streams a session's events as they happen. The channel closes
// if the reader falls behind.
func (m *Manager) Subscribe(id string) (<-chan Event, func()) {
	ch := make(chan Event, subBuffer)
	m.subMu.Lock()
	if m.subs[id] == nil {
		m.subs[id] = map[chan Event]struct{}{}
	}
	m.subs[id][ch] = struct{}{}
	m.subMu.Unlock()
	return ch, func() {
		m.subMu.Lock()
		defer m.subMu.Unlock()
		if _, ok := m.subs[id][ch]; ok {
			delete(m.subs[id], ch)
			close(ch)
		}
		if len(m.subs[id]) == 0 {
			delete(m.subs, id)
		}
	}
}

// Events returns a session's stored events after seq.
func (m *Manager) Events(id string, after int64) ([]Event, error) {
	return m.c.Log.Since(id, after)
}

// Answer replies to an open ask.
func (m *Manager) Answer(ctx context.Context, id, askID string, a Answer) error {
	if _, err := m.chat(id); err != nil {
		return err
	}
	r := m.run(id)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.proc == nil {
		return errNoAsk
	}
	lines, err := r.drv.Answer(askID, a)
	if err != nil {
		return err
	}
	r.used = time.Now()
	m.write(r, lines)
	how := "denied"
	switch {
	case a.Allow && len(a.Answers) > 0:
		how = "answered"
	case a.Allow:
		how = "allowed"
	}
	m.emit(r, Event{Kind: KindAnswered, ID: askID, Text: how})
	return nil
}

// Interrupt stops the current turn, if there is one.
func (m *Manager) Interrupt(id string) error {
	if _, err := m.chat(id); err != nil {
		return err
	}
	r := m.run(id)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.proc == nil {
		if r.working {
			// The harness is still starting: interrupt once the message
			// it's starting for has gone in.
			r.enqueue(func() {
				r.mu.Lock()
				defer r.mu.Unlock()
				if r.proc != nil {
					m.write(r, r.drv.Interrupt())
				}
			})
		}
		return nil
	}
	m.write(r, r.drv.Interrupt())
	return nil
}

// SetMode changes how much the agent asks before acting.
func (m *Manager) SetMode(ctx context.Context, id, mode string) error {
	s, err := m.chat(id)
	if err != nil {
		return err
	}
	d := m.c.Driver(s)
	if d == nil {
		return errNotChat
	}
	if err := validMode(d, mode); err != nil {
		return err
	}
	r := m.run(id)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.proc != nil {
		lines, err := r.drv.SetMode(mode)
		if err != nil {
			return err
		}
		m.write(r, lines)
	}
	return m.c.Sessions.UpdateSession(id, func(x *store.Session) { x.PermMode = mode })
}

// SetModel switches the session to one of the models its harness listed.
// Before the harness has listed any, the choice waits for its next start.
func (m *Manager) SetModel(ctx context.Context, id, model string) error {
	s, err := m.chat(id)
	if err != nil {
		return err
	}
	if m.c.Driver(s) == nil {
		return errNotChat
	}
	if err := validModel(model); err != nil {
		return err
	}
	r := m.run(id)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.models != nil && !slices.Contains(r.models, model) {
		return fmt.Errorf("model %q is not one %s offers", model, s.Harness)
	}
	if r.proc != nil {
		lines, err := r.drv.SetModel(model)
		if err != nil {
			return err
		}
		m.write(r, lines)
	}
	return m.c.Sessions.UpdateSession(id, func(x *store.Session) { x.ChosenModel = model })
}

// Show puts a file in front of the user in a chat: it keeps a copy and adds
// a show event. The session must be a running chat.
func (m *Manager) Show(id string, sh Show, data []byte, ext string) error {
	if _, err := m.chat(id); err != nil {
		return err
	}
	name, err := m.c.Log.Keep(id, data, ext)
	if err != nil {
		return err
	}
	r := m.run(id)
	r.mu.Lock()
	defer r.mu.Unlock()
	m.emit(r, Event{Kind: KindShow, ID: name, Show: &sh})
	return nil
}

// Preview adds a preview card to the chat.
func (m *Manager) Preview(id string, p Preview) error {
	if _, err := m.chat(id); err != nil {
		return err
	}
	r := m.run(id)
	r.mu.Lock()
	defer r.mu.Unlock()
	m.emit(r, Event{Kind: KindPreview, Preview: &p})
	return nil
}

// Shown returns the kept copy of a file shown in a chat.
func (m *Manager) Shown(id, name string) ([]byte, error) {
	return m.c.Log.Kept(id, name)
}

// Login starts the harness's own sign-in. ErrTerminalLogin means it signs
// in only in its terminal UI.
func (m *Manager) Login(ctx context.Context, id string) error {
	s, err := m.chat(id)
	if err != nil {
		return err
	}
	probe := m.c.Driver(s)
	if probe == nil {
		return errNotChat
	}
	if _, err := probe.Login(); err != nil {
		return err
	}
	r := m.run(id)
	r.enqueue(func() {
		if err := m.ensure(r); err != nil {
			r.mu.Lock()
			m.emit(r, Event{Kind: KindLogin, Login: &Login{Needed: true, Error: err.Error()}})
			r.mu.Unlock()
			return
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		lines, err := r.drv.Login()
		if err != nil {
			m.emit(r, Event{Kind: KindLogin, Login: &Login{Needed: true, Error: err.Error()}})
			return
		}
		m.write(r, lines)
	})
	return nil
}

// Compact has the harness summarise the conversation to free its context
// window. It runs as a turn of its own, between the agent's turns.
func (m *Manager) Compact(ctx context.Context, id string) error {
	s, err := m.chat(id)
	if err != nil {
		return err
	}
	if s.Resume == "" {
		return errors.New("there's nothing to compact yet")
	}
	r := m.run(id)
	r.mu.Lock()
	if r.working || len(r.asks) > 0 {
		r.mu.Unlock()
		return errors.New("the agent is working; wait for it or stop it first")
	}
	r.used = time.Now()
	m.emit(r, Event{Kind: KindTurn, Turn: &Turn{State: TurnWorking}})
	r.mu.Unlock()
	r.enqueue(func() {
		if err := m.ensure(r); err != nil {
			r.mu.Lock()
			m.failTurn(r, "The agent didn't start: "+err.Error())
			r.mu.Unlock()
			return
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		lines, err := r.drv.Compact()
		if err != nil {
			m.failTurn(r, err.Error())
			return
		}
		m.write(r, lines)
	})
	return nil
}

// Copy starts session to's conversation as a copy of from's, for a fork.
func (m *Manager) Copy(from, to string) error { return m.c.Log.Copy(from, to) }

// Note adds a notice from Skybreaker to the conversation.
func (m *Manager) Note(id, text string) {
	r := m.run(id)
	r.mu.Lock()
	defer r.mu.Unlock()
	m.emit(r, Event{Kind: KindNotice, Text: text})
}

// Running reports whether a session's harness process is up.
func (m *Manager) Running(id string) bool {
	m.mu.Lock()
	r := m.runs[id]
	m.mu.Unlock()
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.proc != nil
}

// Stop ends a session's harness process: stdin closes, which ends both
// harnesses cleanly, and a process that lingers is killed.
func (m *Manager) Stop(id string) {
	m.mu.Lock()
	r := m.runs[id]
	m.mu.Unlock()
	if r == nil {
		return
	}
	r.mu.Lock()
	p, exited := r.proc, r.exited
	if p == nil {
		r.mu.Unlock()
		return
	}
	r.stopping = true
	r.closeInput()
	r.mu.Unlock()
	select {
	case <-exited:
		return
	case <-time.After(3 * time.Second):
	}
	if p.Kill != nil {
		p.Kill()
	}
	select {
	case <-exited:
	case <-time.After(3 * time.Second):
		log.Printf("skybreaker: chat %s: harness process did not exit", id)
	}
}

// Reap stops harness processes that have sat idle for IdleStop. A later
// message resumes the conversation in a new process.
func (m *Manager) Reap(now time.Time) {
	m.mu.Lock()
	var idle []string
	for id, r := range m.runs {
		r.mu.Lock()
		if r.proc != nil && !r.working && len(r.asks) == 0 && now.Sub(r.used) > m.c.IdleStop {
			idle = append(idle, id)
		}
		r.mu.Unlock()
	}
	m.mu.Unlock()
	for _, id := range idle {
		m.Stop(id)
	}
}

// Recover settles sessions that were mid-turn when the control plane last
// stopped: their processes went with it.
func (m *Manager) Recover(sessions []store.Session) {
	for _, s := range sessions {
		if s.Mode != store.ModeChat || (s.State != store.ChatWorking && s.State != store.ChatWaiting) {
			continue
		}
		r := m.run(s.ID)
		r.mu.Lock()
		events, _ := m.c.Log.Since(s.ID, 0)
		open := map[string]bool{}
		for _, e := range events {
			switch e.Kind {
			case KindAsk:
				open[e.ID] = true
			case KindAnswered:
				delete(open, e.ID)
			}
		}
		for id := range open {
			m.emit(r, Event{Kind: KindAnswered, ID: id, Text: "withdrawn"})
		}
		m.emit(r, Event{Kind: KindNotice, Text: "Skybreaker restarted while the agent was working. Send a message to pick up where it left off."})
		m.emit(r, Event{Kind: KindTurn, Turn: &Turn{State: TurnInterrupted}})
		r.mu.Unlock()
	}
}

// Forget stops a session's process and drops its log, for a deleted session.
func (m *Manager) Forget(id string) {
	m.Stop(id)
	m.mu.Lock()
	delete(m.runs, id)
	m.mu.Unlock()
	m.c.Log.Delete(id)
}

// Close stops every harness process.
func (m *Manager) Close() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.runs))
	for id := range m.runs {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Go(func() { m.Stop(id) })
	}
	wg.Wait()
}

func newUUID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
