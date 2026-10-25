package browser

import (
	"context"
	"encoding/json"
	"net/url"
	"time"
)

// Cue is news for those watching: where the agent clicked, typed or
// scrolled (X and Y in page pixels), a page loaded, or the browser taken
// over or handed back.
type Cue struct {
	Kind string  `json:"act"` // click, type, scroll, page, taken, free
	X    float64 `json:"x,omitempty"`
	Y    float64 `json:"y,omitempty"`
	Site string  `json:"site,omitempty"`
}

type watcher struct {
	frames chan Frame
	cues   chan Cue
}

// Watch shows you the page and what the agent does on it until ctx ends or
// the browser closes, when both channels close. Unlike Take, the agent
// carries on.
func (b *Browser) Watch(ctx context.Context) (<-chan Frame, <-chan Cue, error) {
	select {
	case <-b.Done():
		return nil, nil, ErrClosed
	default:
	}
	w := &watcher{frames: make(chan Frame, 4), cues: make(chan Cue, 16)}
	b.cast.Lock()
	b.mu.Lock()
	if b.watchers == nil {
		b.watchers = map[*watcher]struct{}{}
	}
	b.watchers[w] = struct{}{}
	first := len(b.watchers) == 1 && b.takenBy == ""
	if b.takenBy != "" {
		offer(w.cues, Cue{Kind: "taken"})
	}
	b.mu.Unlock()
	var err error
	if first {
		err = b.screencast(ctx, 1280*pixelRatio, 900*pixelRatio)
	}
	b.cast.Unlock()
	if err != nil {
		b.unwatch(w)
		return nil, nil, err
	}
	if f, ok := b.capture(ctx); ok {
		b.mu.Lock()
		if _, in := b.watchers[w]; in {
			offer(w.frames, f)
		}
		b.mu.Unlock()
	}
	go func() {
		select {
		case <-ctx.Done():
		case <-b.Done():
		}
		b.unwatch(w)
	}()
	return w.frames, w.cues, nil
}

// unwatch ends a watch; the frames stop once no one watches or has it.
func (b *Browser) unwatch(w *watcher) {
	b.cast.Lock()
	defer b.cast.Unlock()
	b.mu.Lock()
	if _, in := b.watchers[w]; in {
		delete(b.watchers, w)
		close(w.frames)
		close(w.cues)
	}
	last := len(b.watchers) == 0 && b.takenBy == ""
	b.mu.Unlock()
	if last {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		b.call(ctx, b.page, "Page.stopScreencast", nil, nil)
	}
}

func (b *Browser) watching() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.watchers)
}

func (b *Browser) cue(c Cue) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for w := range b.watchers {
		offer(w.cues, c)
	}
}

// cuePage tells those watching which site a page that loaded is on.
func (b *Browser) cuePage(ctx context.Context) {
	if b.watching() == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	u, _ := b.current(ctx)
	b.cue(Cue{Kind: "page", Site: SiteOf(u)})
}

// SiteOf is an address's host, never its path or query, which can carry
// what's on the page.
func SiteOf(u string) string {
	p, err := url.Parse(u)
	if err != nil || p.Scheme != "http" && p.Scheme != "https" {
		return ""
	}
	return p.Hostname()
}

// watchPoll is how often a watch looks for a browser that isn't running.
var watchPoll = 2 * time.Second

// LiveWatch shows an assistant's browser over ws, as find finds it, until
// ws goes away: waiting while none runs, and again after one closes.
// Frames go out as binary JPEGs; text messages are JSON:
//
//	{"t":"state","running":false}
//	{"t":"state","running":true,"taken":false,"site":"example.com"}
//	{"t":"size","w":1280,"h":900}
//	{"t":"cue", ...Cue}
func LiveWatch(ctx context.Context, ws WS, find func() (*Browser, bool)) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Nothing comes in but the view going away.
	go func() {
		for {
			if _, _, err := ws.Read(ctx); err != nil {
				cancel()
				return
			}
		}
	}()
	say := func(v any) {
		m, _ := json.Marshal(v)
		if ws.Write(ctx, false, m) != nil {
			cancel()
		}
	}
	idle := false
	for ctx.Err() == nil {
		b, ok := find()
		if ok && watchOne(ctx, ws, b, say) == nil {
			idle = false
			continue
		}
		if !idle {
			say(map[string]any{"t": "state", "running": false})
			idle = true
		}
		select {
		case <-ctx.Done():
		case <-time.After(watchPoll):
		}
	}
}

// watchOne shows one browser until it closes or ctx ends.
func watchOne(ctx context.Context, ws WS, b *Browser, say func(any)) error {
	frames, cues, err := b.Watch(ctx)
	if err != nil {
		return err
	}
	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	st, _ := b.Status(sctx)
	cancel()
	say(map[string]any{"t": "state", "running": true, "taken": st.TakenBy != "", "site": SiteOf(st.URL)})
	w, h := 0, 0
	for {
		select {
		case f, ok := <-frames:
			if !ok {
				return nil
			}
			if f.Width != w || f.Height != h {
				w, h = f.Width, f.Height
				say(map[string]any{"t": "size", "w": w, "h": h})
			}
			ws.Write(ctx, true, f.JPEG)
		case c, ok := <-cues:
			if !ok {
				return nil
			}
			say(struct {
				T string `json:"t"`
				Cue
			}{"cue", c})
		}
	}
}
