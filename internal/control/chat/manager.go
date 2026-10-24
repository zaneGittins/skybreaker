package chat

import (
	"bufio"
	"bytes"
	"cmp"
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
	"sync/atomic"
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
	// Attach writes a message's attachments into the sandbox.
	Attach Attacher
	// Options gives a session's start options (cwd, MCP); the manager adds
	// the mode and the conversation to resume.
	Options func(s store.Session) Options
	// Driver picks a session's driver; nil means New(s.Harness).
	Driver func(s store.Session) Driver
	// OnChange hears every change of a session's State.
	OnChange func(before, after store.Session)
	// OnUsage hears the tokens a session's harness reports using, and the
	// account limits it reports (either may be nil).
	OnUsage func(id string, u *Usage, limits []Limit)
	// IdleStop is how long an idle harness process lives (default 20m).
	IdleStop time.Duration
}

// Manager runs chat sessions' harness processes.
type Manager struct {
	c Config

	// ctx ends with Close, which waits for jobs: no harness starts once
	// it has returned.
	ctx    context.Context
	cancel context.CancelFunc
	jobs   sync.WaitGroup

	mu     sync.Mutex
	runs   map[string]*run
	closed bool

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
	in       chan []byte   // lines for the process's writer
	queued   *atomic.Int64 // bytes in, not yet written
	gen      int
	exited   chan struct{}
	stopping bool
	working  bool
	login    bool
	models   []string          // the models the harness last listed
	listed   map[string]Model  // the same, by ID and by what they resolve to
	asks     map[string]string // open ask → its title
	tools    map[string]Tool   // tool cards still running, by ID; subagents among them can outlast the turn
	used     time.Time

	// jobs that may start the process run one at a time, in order.
	jobMu sync.Mutex
	jobs  []func()
	busy  bool

	// sendMu takes one message at a time from its files to its job, so
	// messages keep their order and a quota checked holds until used.
	sendMu sync.Mutex
	// imaging is the bytes of images in messages waiting for their job.
	imaging atomic.Int64
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
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{c: c, ctx: ctx, cancel: cancel, runs: map[string]*run{}, subs: map[string]map[chan Event]struct{}{}}
}

var errClosed = errors.New("Skybreaker is shutting down")

var errNotChat = errors.New("not a chat session")

// chat returns a live chat session.
func (m *Manager) chat(id string) (store.Session, error) {
	s, ok := m.c.Sessions.Session(id)
	if !ok {
		return s, fmt.Errorf("session %q: %w", id, store.ErrNotFound)
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
		r = &run{id: id, asks: map[string]string{}, tools: map[string]Tool{}}
		m.runs[id] = r
	}
	return r
}

// enqueue runs job after the session's earlier jobs, unless the manager
// has closed.
func (m *Manager) enqueue(r *run, job func()) {
	r.jobMu.Lock()
	r.jobs = append(r.jobs, job)
	if r.busy {
		r.jobMu.Unlock()
		return
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		r.jobs = nil
		r.jobMu.Unlock()
		return
	}
	m.jobs.Add(1)
	m.mu.Unlock()
	r.busy = true
	r.jobMu.Unlock()
	go func() {
		defer m.jobs.Done()
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
// harness first if it isn't running. Files go into the sandbox first, and
// images among them to the model too. It returns once the message is
// recorded; delivery failures show in the conversation.
func (m *Manager) Send(ctx context.Context, id, text string, files ...File) error {
	s, err := m.chat(id)
	if err != nil {
		return err
	}
	text = strings.TrimRight(text, " \t\r\n")
	if strings.TrimSpace(text) == "" {
		text = ""
		if len(files) == 0 {
			return errors.New("empty message")
		}
	}
	if len(text) > 200<<10 {
		return errors.New("message too long")
	}
	r := m.run(id)
	r.sendMu.Lock()
	defer r.sendMu.Unlock()
	if waiting := r.imaging.Load(); waiting > 0 && waiting+int64(filesSize(files)) > maxWaitingImages {
		return errors.New("the agent hasn't taken the images already sent; send these once it has")
	}
	atts, note, images, err := m.attach(ctx, s, files)
	if err != nil {
		return err
	}
	imaging := int64(0)
	for _, im := range images {
		imaging += int64(len(im.Data))
	}
	r.imaging.Add(imaging)
	r.mu.Lock()
	r.used = time.Now()
	m.emit(r, Event{Kind: KindUser, ID: "u" + strconv.FormatInt(time.Now().UnixNano(), 36), Text: text, Attach: atts})
	m.emit(r, Event{Kind: KindTurn, Turn: &Turn{State: TurnWorking}})
	r.mu.Unlock()
	if s.Title == "" {
		title := firstLine(text)
		if title == "" {
			title = atts[0].Name
		}
		title = strings.TrimSuffix(title, " …")
		if len(title) > 80 {
			title = strings.TrimSpace(clip(title, 77)[:77]) + "…"
		}
		m.c.Sessions.UpdateSession(id, func(x *store.Session) { x.Title = title })
	}
	if note != "" {
		text = strings.TrimPrefix(text+"\n\n"+note, "\n\n")
	}
	m.enqueue(r, func() {
		defer r.imaging.Add(-imaging)
		if err := m.ensure(r); err != nil {
			r.mu.Lock()
			m.failTurn(r, "The agent didn't start: "+err.Error())
			r.mu.Unlock()
			return
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		lines, err := r.drv.Send(text, images...)
		if err != nil {
			m.failTurn(r, err.Error())
			return
		}
		m.write(r, lines)
	})
	return nil
}

// maxWaitingImages bounds the images held for a chat whose agent hasn't
// started or taken its earlier messages yet.
var maxWaitingImages int64 = 64 << 20

func filesSize(files []File) (n int) {
	for _, f := range files {
		n += len(f.Data)
	}
	return n
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
	opts.Mode, opts.Resume, opts.Model, opts.Effort, opts.Fork = s.PermMode, s.Resume, s.ChosenModel, s.ChosenEffort, s.Fork
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
	if m.ctx.Err() != nil {
		return errClosed
	}
	ctx, cancel := context.WithTimeout(m.ctx, 15*time.Minute)
	defer cancel()
	p, err := m.c.Start(ctx, s, drv.Argv(opts), env)
	if err != nil {
		return err
	}
	if m.ctx.Err() != nil {
		// Closed while it started: Close only stops what it can see.
		if p.Kill != nil {
			p.Kill()
		}
		return errClosed
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
	r.queued = new(atomic.Int64)
	go writer(p, r.in, r.queued)
	go m.read(r, r.gen, p, r.exited)
	m.write(r, drv.Open(opts))
	return nil
}

// writer copies lines to the process's stdin until in closes, then closes
// stdin, which ends both harnesses cleanly.
func writer(p *Proc, in <-chan []byte, queued *atomic.Int64) {
	defer p.Stdin.Close()
	for l := range in {
		_, err := p.Stdin.Write(l)
		queued.Add(-int64(len(l)))
		if err != nil {
			for l := range in {
				queued.Add(-int64(len(l)))
			}
			return
		}
	}
}

// maxQueued bounds what waits for a harness to read it: images make
// messages large, and a harness that stops reading would hold them all.
const maxQueued = 64 << 20

// write queues lines for the harness, never waiting: callers hold r.mu,
// and a harness that has stopped reading its input would hold it, and
// then everything that takes it, for good. Its input is closed instead,
// which ends a harness that's well.
func (m *Manager) write(r *run, lines [][]byte) {
	for _, l := range lines {
		if r.in == nil {
			return
		}
		n := int64(len(l)) + 1 // and its newline
		if r.queued.Load()+n > maxQueued {
			log.Printf("skybreaker: chat %s: the harness isn't reading its input; closing it", r.id)
			r.closeInput()
			return
		}
		r.queued.Add(n)
		select {
		case r.in <- append(l, '\n'):
		default:
			log.Printf("skybreaker: chat %s: the harness isn't reading its input; closing it", r.id)
			r.closeInput()
		}
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
			if (out.Usage != nil || out.Limits != nil) && m.c.OnUsage != nil {
				m.c.OnUsage(r.id, out.Usage, out.Limits)
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
	// Tool calls die with the harness, subagents with them. Their cards
	// end before the turn does, out of r.tools, so they don't settle it.
	open, agents := r.tools, r.agents()
	r.tools = map[string]Tool{}
	for id, t := range open {
		t.Status = StatusFailed
		if r.stopping {
			t.Status = StatusDeclined
		}
		m.emit(r, Event{Kind: KindTool, ID: id, Tool: &t})
	}
	if !r.working && agents > 0 && r.stopping {
		m.settle(r, true)
	}
	// Subagents that were still working died with it, turn or no turn.
	if r.working || agents > 0 && !r.stopping {
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
		m.setState(r, store.ChatWaiting, waitNote(e.Ask), nil)
	case KindAnswered:
		delete(r.asks, e.ID)
		switch {
		case len(r.asks) > 0:
		case r.working:
			m.setState(r, store.ChatWorking, "", nil)
		case !r.login:
			m.settle(r, false) // a subagent's ask, between turns
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
				// Subagents still at work keep it working; the failure
				// has been told.
				if r.agents() > 0 {
					m.settle(r, true)
				}
			}
		default:
			r.working, r.login = false, false
			m.settle(r, true)
		}
	case KindTool:
		before := r.agents()
		if e.Tool.Status == StatusRunning {
			r.tools[e.ID] = *e.Tool
		} else {
			delete(r.tools, e.ID)
		}
		if r.agents() != before && !r.working && len(r.asks) == 0 && !r.login {
			m.settle(r, false)
		}
	case KindLogin:
		switch {
		case e.Login.Done:
			r.login = false
			if !r.working {
				m.settle(r, true)
			}
		case e.Login.Needed:
			r.login = true
			m.setState(r, store.ChatLogin, "Sign in to continue", nil)
		}
	case KindInfo:
		if len(e.Info.Models) > 0 {
			r.models = r.models[:0]
			r.listed = map[string]Model{}
			for _, md := range e.Info.Models {
				r.models = append(r.models, md.ID)
				r.listed[md.ID] = md
			}
			// An alias's target finds the first model resolving to it.
			for _, md := range e.Info.Models {
				if _, ok := r.listed[md.Resolved]; md.Resolved != "" && !ok {
					r.listed[md.Resolved] = md
				}
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

// agents counts the subagents still running. Callers hold r.mu.
func (r *run) agents() int {
	n := 0
	for _, t := range r.tools {
		if t.Kind == ToolAgent {
			n++
		}
	}
	return n
}

// settle sets the state of a session whose turn isn't running: working
// while its subagents are, else idle. Only a turn's end replaces a failed
// turn's state. Callers hold r.mu.
func (m *Manager) settle(r *run, turnEnded bool) {
	if s, _ := m.c.Sessions.Session(r.id); s.State == store.ChatFailed && !turnEnded {
		return
	}
	switch n := r.agents(); {
	case n == 1:
		m.setState(r, store.ChatWorking, "1 agent working", nil)
	case n > 1:
		m.setState(r, store.ChatWorking, strconv.Itoa(n)+" agents working", nil)
	default:
		m.setState(r, store.ChatIdle, "", nil)
	}
}

// setState records a session's state (empty keeps it) and tells OnChange.
// waitNote is what the inbox says a chat waits on: an approval and for
// what, or the question it asks.
func waitNote(a *Ask) string {
	if a.Kind == AskQuestion {
		if len(a.Questions) > 0 && a.Questions[0].Question != "" {
			return clip("Question: "+a.Questions[0].Question, 200)
		}
		return "A question for you"
	}
	return clip("Needs approval: "+a.Title, 200)
}

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
	changed := before.State != after.State || after.State == store.ChatWorking && before.Note != after.Note
	if err == nil && changed && m.c.OnChange != nil {
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
			m.enqueue(r, func() {
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

// change makes a setting change to a chat: f checks it against the
// session, tells a running harness, and says what to save. The session is
// read under the run's lock, so changes one after another build on each
// other.
func (m *Manager) change(id string, f func(r *run, s store.Session) (func(*store.Session), error)) error {
	if _, err := m.chat(id); err != nil {
		return err
	}
	r := m.run(id)
	r.mu.Lock()
	defer r.mu.Unlock()
	s, err := m.chat(id)
	if err != nil {
		return err
	}
	if m.c.Driver(s) == nil {
		return errNotChat
	}
	save, err := f(r, s)
	if err != nil {
		return err
	}
	return m.c.Sessions.UpdateSession(id, save)
}

// tell writes lines to a running harness; a stopped one gets the change
// from the session when it next starts.
func (m *Manager) tell(r *run, lines func() ([][]byte, error)) error {
	if r.proc == nil {
		return nil
	}
	l, err := lines()
	if err != nil {
		return err
	}
	m.write(r, l)
	return nil
}

// SetMode changes how much the agent asks before acting.
func (m *Manager) SetMode(ctx context.Context, id, mode string) error {
	return m.change(id, func(r *run, s store.Session) (func(*store.Session), error) {
		if err := validMode(m.c.Driver(s), mode); err != nil {
			return nil, err
		}
		if err := m.tell(r, func() ([][]byte, error) { return r.drv.SetMode(mode) }); err != nil {
			return nil, err
		}
		return func(x *store.Session) { x.PermMode = mode }, nil
	})
}

// SetModel switches the session to one of the models its harness listed.
// Before the harness has listed any, the choice waits for its next start.
// The effort level goes with it, fitted to the new model, since some
// harnesses (pi) reset it on a switch.
func (m *Manager) SetModel(ctx context.Context, id, model string) error {
	if err := validModel(model); err != nil {
		return err
	}
	return m.change(id, func(r *run, s store.Session) (func(*store.Session), error) {
		if r.models != nil && !slices.Contains(r.models, model) {
			return nil, fmt.Errorf("model %q is not one %s offers", model, s.Harness)
		}
		effort := fitEffort(r.listed[model], s.ChosenEffort)
		err := m.tell(r, func() ([][]byte, error) {
			lines, err := r.drv.SetModel(model)
			if err != nil || effort == "" {
				return lines, err
			}
			more, err := r.drv.SetEffort(effort)
			return append(lines, more...), err
		})
		if err != nil {
			return nil, err
		}
		return func(x *store.Session) { x.ChosenModel, x.ChosenEffort = model, effort }, nil
	})
}

// fitEffort is the level to run a model at, given the one chosen: kept if
// the model has it or hasn't said, else the model's default, else kept for
// the harness to fit to the model as it does (Claude).
func fitEffort(md Model, chosen string) string {
	if chosen == "" || !md.EffortsKnown || slices.Contains(md.Efforts, chosen) || md.DefaultEffort == "" {
		return chosen
	}
	return md.DefaultEffort
}

// SetEffort switches the session to one of the effort levels its model
// listed. Before the harness has listed them, the choice waits for its
// next start.
func (m *Manager) SetEffort(ctx context.Context, id, effort string) error {
	if err := validEffort(effort); err != nil {
		return err
	}
	return m.change(id, func(r *run, s store.Session) (func(*store.Session), error) {
		model := cmp.Or(s.ChosenModel, s.Model)
		if md := r.listed[model]; md.EffortsKnown && !slices.Contains(md.Efforts, effort) {
			return nil, fmt.Errorf("%s doesn't offer %q effort", cmp.Or(md.Name, model), effort)
		}
		if err := m.tell(r, func() ([][]byte, error) { return r.drv.SetEffort(effort) }); err != nil {
			return nil, err
		}
		return func(x *store.Session) { x.ChosenEffort = effort }, nil
	})
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
	m.enqueue(r, func() {
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
	m.enqueue(r, func() {
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
		if r.proc != nil && !r.working && len(r.asks) == 0 && r.agents() == 0 && now.Sub(r.used) > m.c.IdleStop {
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
		// Tool calls and subagents went with the process too.
		running := map[string]Tool{}
		for _, e := range events {
			if e.Kind == KindTool && e.Tool != nil {
				if e.Tool.Status == StatusRunning {
					running[e.ID] = *e.Tool
				} else {
					delete(running, e.ID)
				}
			}
		}
		for id, t := range running {
			t.Status = StatusDeclined
			m.emit(r, Event{Kind: KindTool, ID: id, Tool: &t})
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
	m.closed = true
	m.mu.Unlock()
	m.cancel()
	m.jobs.Wait()
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
