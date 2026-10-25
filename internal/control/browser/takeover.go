package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// ErrTaken is taking a browser someone else has.
var ErrTaken = errors.New("someone else is using this browser")

// Take gives you the browser: the agent's actions pause, the deny list is
// lifted, and frames of the page come on the channel until Release. Taking
// it again yourself, from a reload or another device, closes the frames of
// your old view, which then ends.
func (b *Browser) Take(ctx context.Context, by string) (<-chan Frame, error) {
	if by == "" {
		return nil, errors.New("the browser is taken by someone, not no one")
	}
	b.mu.Lock()
	if b.takenBy != "" && b.takenBy != by {
		b.mu.Unlock()
		return nil, ErrTaken
	}
	if b.frames != nil {
		close(b.frames)
	}
	b.takenBy, b.held = by, 0
	b.frames = make(chan Frame, 4)
	frames := b.frames
	b.mu.Unlock()
	out := (<-chan Frame)(frames)
	// No new action of the agent's starts now; wait out any running.
	b.act.Lock()
	b.act.Unlock()
	// The size a phone left is the old view's, not this one's.
	b.cast.Lock()
	b.call(ctx, b.page, "Emulation.clearDeviceMetricsOverride", nil, nil)
	err := b.screencast(ctx, 1280*pixelRatio, 900*pixelRatio)
	b.cast.Unlock()
	if err != nil {
		b.Release(ctx)
		return nil, err
	}
	if f, ok := b.capture(ctx); ok {
		b.mu.Lock()
		if b.frames == frames { // not handed back, or taken over again, meanwhile
			offer(frames, f)
		}
		b.mu.Unlock()
	}
	b.cue(Cue{Kind: "taken"})
	return out, nil
}

// capture is the page as it is now: a screencast only sends a frame when
// the page paints, and a sign-in form waiting for you may not.
func (b *Browser) capture(ctx context.Context) (Frame, bool) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var m struct {
		CSSVisualViewport struct{ ClientWidth, ClientHeight float64 } `json:"cssVisualViewport"`
	}
	var s struct {
		Data string `json:"data"`
	}
	if b.call(ctx, b.page, "Page.getLayoutMetrics", nil, &m) != nil ||
		b.call(ctx, b.page, "Page.captureScreenshot", map[string]any{"format": "jpeg", "quality": 80}, &s) != nil {
		return Frame{}, false
	}
	img, err := base64.StdEncoding.DecodeString(s.Data)
	if err != nil {
		return Frame{}, false
	}
	return Frame{JPEG: img, Width: int(m.CSSVisualViewport.ClientWidth), Height: int(m.CSSVisualViewport.ClientHeight)}, true
}

// Release hands the browser back to the agent.
func (b *Browser) Release(ctx context.Context) { b.release(ctx, nil) }

// release hands it back if frames are still the take-over's, or for any
// take-over when frames is nil: a view that was taken over from doesn't
// hand back the one that took it.
func (b *Browser) release(ctx context.Context, frames <-chan Frame) {
	b.mu.Lock()
	if b.takenBy == "" || frames != nil && (<-chan Frame)(b.frames) != frames {
		b.mu.Unlock()
		return
	}
	b.takenBy = ""
	if b.frames != nil {
		close(b.frames)
		b.frames = nil
	}
	b.mu.Unlock()
	b.cue(Cue{Kind: "free"})
	// A browser that stops answering mustn't hold the hand-back up forever.
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	b.cast.Lock()
	b.call(ctx, b.page, "Page.stopScreencast", nil, nil)
	b.call(ctx, b.page, "Emulation.clearDeviceMetricsOverride", nil, nil)
	// Those watching go on seeing it, at the agent's size again.
	if b.watching() > 0 {
		b.screencast(ctx, 1280*pixelRatio, 900*pixelRatio)
	}
	b.cast.Unlock()
	// What you opened, the agent mayn't: leave it a blank page instead.
	if u, err := b.current(ctx); err == nil && denied(u, b.deny()) {
		b.navigate(ctx, "about:blank")
	}
}

// screencast starts frames of the page, at most w by h, in place of any
// already coming: Chromium can still count a stopped one as running.
func (b *Browser) screencast(ctx context.Context, w, h int) error {
	start := func() error {
		return b.call(ctx, b.page, "Page.startScreencast", map[string]any{"format": "jpeg", "quality": 80, "maxWidth": w, "maxHeight": h}, nil)
	}
	err := start()
	for i := 0; err != nil && strings.Contains(err.Error(), "already active") && i < 5; i++ {
		b.call(ctx, b.page, "Page.stopScreencast", nil, nil)
		time.Sleep(50 * time.Millisecond)
		err = start()
	}
	return err
}

// Fit lays the page out for your screen, w by h CSS pixels, as a phone
// would, while you have the browser: a desktop page shrunk to a phone is
// too small to read or tap. Release gives the agent its own size back.
func (b *Browser) Fit(ctx context.Context, w, h int) error {
	if err := b.taken(); err != nil {
		return err
	}
	if w < 240 || w > 1600 || h < 240 || h > 2000 {
		return fmt.Errorf("can't fit the page to %dx%d", w, h)
	}
	err := b.call(ctx, b.page, "Emulation.setDeviceMetricsOverride", map[string]any{"width": w, "height": h, "deviceScaleFactor": pixelRatio, "mobile": true}, nil)
	if err != nil {
		return err
	}
	// Frames at the phone's own pixels, so the text is sharp.
	b.cast.Lock()
	defer b.cast.Unlock()
	b.call(ctx, b.page, "Page.stopScreencast", nil, nil)
	return b.screencast(ctx, w*pixelRatio, h*pixelRatio)
}

func (b *Browser) frame(ctx context.Context, e Event) {
	var p struct {
		Data      string `json:"data"`
		SessionID int    `json:"sessionId"`
		Metadata  struct {
			DeviceWidth  float64 `json:"deviceWidth"`
			DeviceHeight float64 `json:"deviceHeight"`
		} `json:"metadata"`
	}
	json.Unmarshal(e.Params, &p)
	go b.call(ctx, b.page, "Page.screencastFrameAck", map[string]any{"sessionId": p.SessionID}, nil)
	img, err := base64.StdEncoding.DecodeString(p.Data)
	if err != nil {
		return
	}
	f := Frame{JPEG: img, Width: int(p.Metadata.DeviceWidth), Height: int(p.Metadata.DeviceHeight)}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.frames != nil {
		offer(b.frames, f)
	}
	for w := range b.watchers {
		offer(w.frames, f)
	}
}

// offer queues a frame without waiting: a view that's behind loses the
// oldest one waiting, so the page as it is now always gets through.
func offer[T any](ch chan T, f T) {
	for {
		select {
		case ch <- f:
			return
		default:
		}
		select {
		case <-ch:
		default:
		}
	}
}

// taken checks you have the browser.
func (b *Browser) taken() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.takenBy == "" {
		return errors.New("take the browser first")
	}
	return nil
}

// Mouse is a mouse event of yours, in page pixels.
type Mouse struct {
	Type       string  `json:"type"` // mousePressed, mouseReleased, mouseMoved, mouseWheel
	X          float64 `json:"x"`
	Y          float64 `json:"y"`
	Button     string  `json:"button,omitempty"` // left, middle, right, none
	ClickCount int     `json:"click_count,omitempty"`
	DeltaX     float64 `json:"dx,omitempty"`
	DeltaY     float64 `json:"dy,omitempty"`
	Modifiers  int     `json:"modifiers,omitempty"`
}

// Key is a key event of yours.
type Key struct {
	Type      string `json:"type"` // keyDown, keyUp, char
	Key       string `json:"key"`
	Code      string `json:"code"`
	Text      string `json:"text,omitempty"`
	KeyCode   int    `json:"key_code,omitempty"`
	Modifiers int    `json:"modifiers,omitempty"`
}

// buttonBits are the buttons as a mouse event's buttons field counts them.
var buttonBits = map[string]int{"left": 1, "right": 2, "middle": 4}

var mouseTypes = map[string]bool{"mousePressed": true, "mouseReleased": true, "mouseMoved": true, "mouseWheel": true}
var keyTypes = map[string]bool{"keyDown": true, "keyUp": true, "rawKeyDown": true, "char": true}

func (b *Browser) Mouse(ctx context.Context, m Mouse) error {
	if err := b.taken(); err != nil {
		return err
	}
	if !mouseTypes[m.Type] {
		return fmt.Errorf("mouse event %q", m.Type)
	}
	// What's held down goes with every move, so the page sees a drag.
	b.mu.Lock()
	switch m.Type {
	case "mousePressed":
		b.held |= buttonBits[m.Button]
	case "mouseReleased":
		b.held &^= buttonBits[m.Button]
	case "mouseMoved":
		for name, bit := range buttonBits {
			if b.held&bit != 0 && m.Button == "" {
				m.Button = name
			}
		}
	}
	held := b.held
	b.mu.Unlock()
	if m.Button == "" {
		m.Button = "none"
	}
	return b.call(ctx, b.page, "Input.dispatchMouseEvent", map[string]any{"type": m.Type, "x": m.X, "y": m.Y, "button": m.Button, "buttons": held,
		"clickCount": m.ClickCount, "deltaX": m.DeltaX, "deltaY": m.DeltaY, "modifiers": m.Modifiers}, nil)
}

func (b *Browser) Key(ctx context.Context, k Key) error {
	if err := b.taken(); err != nil {
		return err
	}
	if !keyTypes[k.Type] {
		return fmt.Errorf("key event %q", k.Type)
	}
	ev := map[string]any{"type": k.Type, "key": k.Key, "code": k.Code, "windowsVirtualKeyCode": k.KeyCode, "modifiers": k.Modifiers}
	if k.Text != "" {
		ev["text"] = k.Text
	}
	return b.call(ctx, b.page, "Input.dispatchKeyEvent", ev, nil)
}

// Text types text of yours, such as pasted.
func (b *Browser) Text(ctx context.Context, s string) error {
	if err := b.taken(); err != nil {
		return err
	}
	return b.call(ctx, b.page, "Input.insertText", map[string]any{"text": s}, nil)
}

// Go opens an address of yours.
func (b *Browser) Go(ctx context.Context, u string) error {
	if err := b.taken(); err != nil {
		return err
	}
	p, err := url.Parse(u)
	if err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
		return fmt.Errorf("open %q: only http and https addresses", u)
	}
	return b.navigate(ctx, u)
}
