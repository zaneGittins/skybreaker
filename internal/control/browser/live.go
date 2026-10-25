package browser

import (
	"context"
	"encoding/json"
	"time"
)

// WS is a WebSocket to the person taking over.
type WS interface {
	Read(ctx context.Context) (binary bool, data []byte, err error)
	Write(ctx context.Context, binary bool, data []byte) error
	Close(code int, reason string) error
}

// liveIdle ends a take-over nobody touches.
const liveIdle = 15 * time.Minute

// Live hands the browser to by over ws until they're done, go quiet or go
// away. Frames go out as binary JPEGs; text messages carry state and
// errors. In come their mouse, keys, text and addresses, as JSON:
//
//	{"t":"mouse", ...Mouse} {"t":"key", ...Key} {"t":"text","s":"..."}
//	{"t":"go","url":"..."} {"t":"fit","w":390,"h":700} {"t":"done"}
func Live(ctx context.Context, ws WS, b *Browser, by string) error {
	frames, err := b.Take(ctx, by)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer b.Release(context.WithoutCancel(ctx))
	say := func(v any) {
		m, _ := json.Marshal(v)
		ws.Write(ctx, false, m)
	}
	say(map[string]any{"t": "state", "taken": true})
	go func() {
		w, h := 0, 0
		for f := range frames {
			if f.Width != w || f.Height != h {
				w, h = f.Width, f.Height
				say(map[string]any{"t": "size", "w": w, "h": h})
			}
			if ws.Write(ctx, true, f.JPEG) != nil {
				cancel()
				return
			}
		}
	}()
	in := make(chan []byte)
	go func() {
		defer close(in)
		for {
			binary, data, err := ws.Read(ctx)
			if err != nil {
				return
			}
			if !binary {
				select {
				case in <- data:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	idle := time.NewTimer(liveIdle)
	defer idle.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-b.Done():
			say(map[string]any{"t": "error", "error": "the browser closed"})
			return nil
		case <-idle.C:
			say(map[string]any{"t": "state", "taken": false, "why": "idle"})
			return nil
		case m, ok := <-in:
			if !ok {
				return nil
			}
			idle.Reset(liveIdle)
			if done := handle(ctx, b, m, say); done {
				say(map[string]any{"t": "state", "taken": false})
				return nil
			}
		}
	}
}

func handle(ctx context.Context, b *Browser, m []byte, say func(any)) (done bool) {
	var head struct {
		T   string `json:"t"`
		S   string `json:"s"`
		URL string `json:"url"`
		W   int    `json:"w"`
		H   int    `json:"h"`
	}
	if json.Unmarshal(m, &head) != nil {
		say(map[string]any{"t": "error", "error": "not JSON"})
		return false
	}
	var err error
	switch head.T {
	case "done":
		return true
	case "mouse":
		var e Mouse
		if err = json.Unmarshal(m, &e); err == nil {
			err = b.Mouse(ctx, e)
		}
	case "key":
		var e Key
		if err = json.Unmarshal(m, &e); err == nil {
			err = b.Key(ctx, e)
		}
	case "text":
		err = b.Text(ctx, head.S)
	case "go":
		err = b.Go(ctx, head.URL)
	case "fit":
		err = b.Fit(ctx, head.W, head.H)
	default:
		say(map[string]any{"t": "error", "error": "unknown message " + head.T})
		return false
	}
	if err != nil {
		say(map[string]any{"t": "error", "error": err.Error()})
	}
	return false
}
