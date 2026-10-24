package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
)

// ErrTaken is taking a browser someone else has.
var ErrTaken = errors.New("someone else is using this browser")

// Take gives you the browser: the agent's actions pause, the deny list is
// lifted, and frames of the page come on the channel until Release.
func (b *Browser) Take(ctx context.Context, by string) (<-chan Frame, error) {
	if by == "" {
		return nil, errors.New("the browser is taken by someone, not no one")
	}
	b.mu.Lock()
	if b.takenBy != "" {
		b.mu.Unlock()
		return nil, ErrTaken
	}
	b.takenBy = by
	b.frames = make(chan Frame, 4)
	frames := b.frames
	b.mu.Unlock()
	// No new action of the agent's starts now; wait out any running.
	b.act.Lock()
	b.act.Unlock()
	err := b.call(ctx, b.page, "Page.startScreencast", map[string]any{"format": "jpeg", "quality": 70, "maxWidth": 1280, "maxHeight": 900}, nil)
	if err != nil {
		b.Release(ctx)
		return nil, err
	}
	return frames, nil
}

// Release hands the browser back to the agent.
func (b *Browser) Release(ctx context.Context) {
	b.mu.Lock()
	if b.takenBy == "" {
		b.mu.Unlock()
		return
	}
	b.takenBy = ""
	if b.frames != nil {
		close(b.frames)
		b.frames = nil
	}
	b.mu.Unlock()
	b.call(ctx, b.page, "Page.stopScreencast", nil, nil)
	// What you opened, the agent mayn't: leave it a blank page instead.
	if u, err := b.current(ctx); err == nil && denied(u, b.deny()) {
		b.navigate(ctx, "about:blank")
	}
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
	if b.frames == nil {
		return
	}
	select {
	case b.frames <- f:
	default: // you're behind: skip a frame rather than stall the browser
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

var mouseTypes = map[string]bool{"mousePressed": true, "mouseReleased": true, "mouseMoved": true, "mouseWheel": true}
var keyTypes = map[string]bool{"keyDown": true, "keyUp": true, "rawKeyDown": true, "char": true}

func (b *Browser) Mouse(ctx context.Context, m Mouse) error {
	if err := b.taken(); err != nil {
		return err
	}
	if !mouseTypes[m.Type] {
		return fmt.Errorf("mouse event %q", m.Type)
	}
	if m.Button == "" {
		m.Button = "none"
	}
	return b.call(ctx, b.page, "Input.dispatchMouseEvent", map[string]any{"type": m.Type, "x": m.X, "y": m.Y, "button": m.Button,
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
