package setup

import (
	"fmt"
	"io"
	"time"
)

// Progress prints an install's numbered steps. On a terminal a slow step
// shows a spinner with its elapsed time that collapses into one result line;
// elsewhere (logs, pipes) each step is a plain "[~]" line then its result.
type Progress struct {
	W   io.Writer
	TTY bool

	total, n int
	cur      *stepState
}

type stepState struct {
	n       int
	label   string
	start   time.Time
	stop    chan struct{}
	stopped chan struct{}
	drew    bool // read only after stopped is closed
}

var spinnerFrames = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

const (
	spinnerEvery = 100 * time.Millisecond
	spinnerDelay = 120 * time.Millisecond // quicker steps print only their result
)

// Steps starts a numbered sequence of total steps.
func (p *Progress) Steps(total int) {
	p.abandon()
	p.total, p.n = total, 0
}

// Step begins a step; Done, Warn or Fail ends it.
func (p *Progress) Step(label string) {
	p.abandon()
	p.n++
	s := &stepState{n: p.n, label: label, start: time.Now()}
	p.cur = s
	if !p.TTY {
		fmt.Fprintf(p.W, "[%s] %s%s\n", DimStyle.Render("~"), p.tag(s.n), label)
		return
	}
	s.stop, s.stopped = make(chan struct{}), make(chan struct{})
	go p.spin(s)
}

func (p *Progress) Done(msg string) { p.end(OKStyle.Render("+"), msg, true) }
func (p *Progress) Warn(msg string) { p.end(WarnStyle.Render("!"), msg, false) }
func (p *Progress) Fail(msg string) { p.end(ErrorStyle.Render("x"), msg, false) }

// Note prints a line under the current step's result.
func (p *Progress) Note(msg string) { fmt.Fprintf(p.W, "    %s\n", DimStyle.Render(msg)) }

func (p *Progress) tag(n int) string {
	if p.total <= 0 || n > p.total {
		return ""
	}
	return DimStyle.Render(fmt.Sprintf("[%d/%d] ", n, p.total))
}

func (p *Progress) end(marker, msg string, elapsed bool) {
	s := p.cur
	if s == nil {
		fmt.Fprintf(p.W, "[%s] %s\n", marker, msg)
		return
	}
	p.abandon()
	suffix := ""
	if e := time.Since(s.start); elapsed && e >= time.Second {
		suffix = " " + DimStyle.Render("("+e.Round(100*time.Millisecond).String()+")")
	}
	fmt.Fprintf(p.W, "[%s] %s%s%s\n", marker, p.tag(s.n), msg, suffix)
}

// abandon stops the current step's spinner, if any, and clears its line.
func (p *Progress) abandon() {
	s := p.cur
	if s == nil {
		return
	}
	p.cur = nil
	if s.stop == nil {
		return
	}
	close(s.stop)
	<-s.stopped
	if s.drew {
		fmt.Fprint(p.W, "\r\033[K")
	}
}

func (p *Progress) spin(s *stepState) {
	defer close(s.stopped)
	select {
	case <-s.stop:
		return
	case <-time.After(spinnerDelay):
	}
	for i := 0; ; i++ {
		fmt.Fprintf(p.W, "\r[%s] %s%s %s\033[K", AccentStyle.Render(string(spinnerFrames[i%len(spinnerFrames)])),
			p.tag(s.n), s.label, DimStyle.Render(time.Since(s.start).Round(time.Second).String()))
		s.drew = true
		select {
		case <-s.stop:
			return
		case <-time.After(spinnerEvery):
		}
	}
}
