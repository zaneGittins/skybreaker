package browser

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed page.js
var pageJS string

var (
	// ErrPaused is an agent's action while you have the browser.
	ErrPaused = errors.New("paused: the user is using the browser; try again later with browser_status")
	// ErrDenied is a page the agent may not open.
	ErrDenied = errors.New("that page is off limits to the assistant")
	// ErrSecret is typing into a password, one-time code or card field.
	ErrSecret = errors.New("that's a password, one-time code or card field: ask the user to take over the browser and type it themselves")
)

// Launch starts Chromium with args (its own, after the binary) and returns
// its DevTools pipe: what it writes, what it reads, and how to stop it.
type Launch func(ctx context.Context, args []string) (from io.ReadCloser, to io.WriteCloser, stop func(), err error)

// Options are one assistant's browser.
type Options struct {
	Launch Launch
	// Profile is the browser's own directory for logins and cookies, where
	// Chromium runs: never in the agent's sandbox.
	Profile string
	// Downloads is where Chromium saves files, where it runs.
	Downloads string
	// Proxy is the broker, e.g. http://10.89.4.1:8080; "" goes direct.
	Proxy string
	// Credentials are the broker's, asked for afresh on each challenge.
	Credentials func(ctx context.Context) (user, pass string, err error)
	// CASPKI is the broker CA's base64 SHA-256 SPKI hash: the one CA
	// trusted beyond Chromium's own roots.
	CASPKI string
	// Deny are pages the agent may not open, as host/path prefixes such
	// as myaccount.google.com/security. You may, while you have it.
	Deny []string
}

// DefaultDeny are account pages where a session could be turned into the
// account itself: passwords, recovery, two-step and app passwords.
var DefaultDeny = []string{
	"myaccount.google.com/security", "myaccount.google.com/signinoptions", "myaccount.google.com/apppasswords",
	"myaccount.google.com/recovery", "myaccount.google.com/two-step-verification", "myaccount.google.com/deleteaccount",
	"accounts.google.com/signin/v2/challenge/selection", "account.microsoft.com/security", "account.live.com/proofs",
	"appleid.apple.com", "github.com/settings",
}

// Browser is one running Chromium with a single page.
type Browser struct {
	opts Options
	conn *Conn
	stop func()
	page string // the page's target session

	target string // the page's target

	// act is held by an agent's action for as long as it runs, and taken
	// whole by Take, so nothing of the agent's runs while you have it.
	act     sync.RWMutex
	mu      sync.Mutex
	agent   map[string]any // how it introduces itself: see ordinary
	takenBy string
	held    int // the mouse buttons you hold down, as the buttons field counts them
	world   int // Skybreaker's isolated world in the page, 0 until made
	frames  chan Frame
	// watchers see the page too, without taking it.
	watchers map[*watcher]struct{}
	// cast is held to start or stop the frames, so a watch and a take-over
	// starting and ending together leave them running for whoever remains.
	cast    sync.Mutex
	loaded  chan struct{}
	dl      map[string]*download // by guid
	dlBegin chan string
}

type download struct {
	name  string
	state string // inProgress, completed, canceled
	done  chan struct{}
}

// Frame is one screencast image of the page, for you to see while you
// have the browser.
type Frame struct {
	JPEG          []byte
	Width, Height int
	URL           string
}

// pixelRatio is how many screen pixels Chromium draws each page pixel with.
const pixelRatio = 2

// Start launches Chromium and opens its page.
func Start(ctx context.Context, o Options) (*Browser, error) {
	args := []string{
		"--headless", "--no-sandbox", "--disable-gpu", "--no-first-run", "--no-default-browser-check",
		"--remote-debugging-pipe", "--user-data-dir=" + o.Profile,
		"--disable-background-networking", "--disable-sync", "--disable-component-update",
		"--force-webrtc-ip-handling-policy=disable_non_proxied_udp", "--window-size=1280,900",
		// Twice the pixels, so what you see as you take over is sharp on a
		// Retina screen or a phone; the agent's screenshots are scaled back.
		"--force-device-scale-factor=" + strconv.Itoa(pixelRatio),
		// Its frames go in /tmp, not /dev/shm: a container gets 64 MB of
		// that, which a few sharp frames fill, hanging the page.
		"--disable-dev-shm-usage",
		// Not "automated", and a language a person's browser has: sites,
		// Google's sign-in first, refuse a browser that looks like a bot.
		"--disable-blink-features=AutomationControlled", "--lang=en-US", "--accept-lang=en-US,en",
		// A 1920x1080 screen around the window, in its pixels: at twice the
		// pixels, Chromium would otherwise report half the window's size.
		"--screen-info={" + strconv.Itoa(1920*pixelRatio) + "x" + strconv.Itoa(1080*pixelRatio) + "}",
	}
	if o.Proxy != "" {
		args = append(args, "--proxy-server="+o.Proxy, "--proxy-bypass-list=<-loopback>")
	}
	if o.CASPKI != "" {
		args = append(args, "--ignore-certificate-errors-spki-list="+o.CASPKI)
	}
	args = append(args, "about:blank")
	from, to, stop, err := o.Launch(ctx, args)
	if err != nil {
		return nil, err
	}
	b := &Browser{opts: o, conn: NewConn(from, to), stop: stop, loaded: make(chan struct{}, 1), dl: map[string]*download{}, dlBegin: make(chan string, 4)}
	// Events wait until the page is set up: answering them needs to know
	// which page is the browser's own.
	if err := b.open(ctx); err != nil {
		b.Close()
		return nil, err
	}
	go b.events()
	return b, nil
}

// ordinary works out how the browser introduces itself as an ordinary
// Chrome: a headless one says HeadlessChrome in its user agent and its
// client hints, which Google's sign-in, among others, refuses.
func (b *Browser) ordinary(ctx context.Context) error {
	var v struct {
		Product   string `json:"product"`   // HeadlessChrome/153.0.8010.0
		UserAgent string `json:"userAgent"` // ... HeadlessChrome/153.0.8010.0 ...
	}
	if err := b.call(ctx, "", "Browser.getVersion", nil, &v); err != nil {
		return err
	}
	_, full, _ := strings.Cut(v.Product, "/")
	major, _, _ := strings.Cut(full, ".")
	brands := func(version string) []any {
		return []any{map[string]string{"brand": "Chromium", "version": version}, map[string]string{"brand": "Not_A Brand", "version": "8"}}
	}
	b.agent = map[string]any{
		"userAgent":      strings.ReplaceAll(v.UserAgent, "HeadlessChrome", "Chrome"),
		"acceptLanguage": "en-US,en;q=0.9",
		"platform":       "Linux x86_64",
		"userAgentMetadata": map[string]any{
			"brands": brands(major), "fullVersionList": brands(full), "fullVersion": full,
			"platform": "Linux", "platformVersion": "", "architecture": "x86", "bitness": "64", "model": "", "mobile": false, "wow64": false,
		},
	}
	return b.call(ctx, b.page, "Network.setUserAgentOverride", b.agent, nil)
}

func (b *Browser) open(ctx context.Context) error {
	var t struct {
		TargetID string `json:"targetId"`
	}
	if err := b.call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank"}, &t); err != nil {
		return err
	}
	var s struct {
		SessionID string `json:"sessionId"`
	}
	if err := b.call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": t.TargetID, "flatten": true}, &s); err != nil {
		return err
	}
	b.page, b.target = s.SessionID, t.TargetID
	if err := b.ordinary(ctx); err != nil {
		return err
	}
	for _, c := range []struct {
		method string
		params any
	}{
		// Every other page (a popup, a new tab) is held before it loads,
		// and closed: the agent has one page, under the deny list.
		{"Target.setAutoAttach", map[string]any{"autoAttach": true, "waitForDebuggerOnStart": true, "flatten": true}},
		{"Page.enable", nil},
		{"Fetch.enable", map[string]any{"handleAuthRequests": true, "patterns": []any{map[string]any{"urlPattern": "*", "requestStage": "Request"}}}},
		{"Browser.setDownloadBehavior", map[string]any{"behavior": "allowAndName", "downloadPath": b.opts.Downloads, "eventsEnabled": true}},
	} {
		session := b.page
		if strings.HasPrefix(c.method, "Browser.") || strings.HasPrefix(c.method, "Target.") {
			session = ""
		}
		if err := b.call(ctx, session, c.method, c.params, nil); err != nil {
			return err
		}
	}
	return nil
}

// Done is closed once Chromium has gone.
func (b *Browser) Done() <-chan struct{} { return b.conn.Done() }

// Close stops Chromium.
func (b *Browser) Close() {
	b.conn.Close()
	if b.stop != nil {
		b.stop()
	}
}

func (b *Browser) call(ctx context.Context, session, method string, params, out any) error {
	raw, err := b.conn.Call(ctx, session, method, params)
	if err != nil || out == nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

// events answers the browser: proxy logins, every request against the
// policy, screencast frames and downloads.
func (b *Browser) events() {
	ctx := context.Background()
	for {
		var e Event
		select {
		case e = <-b.conn.Events():
		case <-b.conn.Done():
			b.mu.Lock()
			if b.frames != nil {
				close(b.frames)
				b.frames = nil
			}
			b.mu.Unlock()
			return
		}
		switch e.Method {
		case "Target.attachedToTarget":
			go b.attached(ctx, e)
		case "Fetch.authRequired":
			go b.auth(ctx, e)
		case "Fetch.requestPaused":
			go b.paused(ctx, e)
		case "Page.loadEventFired":
			go b.cuePage(ctx)
			select {
			case b.loaded <- struct{}{}:
			default:
			}
		case "Page.screencastFrame":
			b.frame(ctx, e)
		case "Page.javascriptDialogOpening":
			// No one sees a headless page's alert, confirm or "leave this
			// page?": answer yes, or the page and every action on it hang.
			go b.call(ctx, e.Session, "Page.handleJavaScriptDialog", map[string]any{"accept": true}, nil)
		case "Browser.downloadWillBegin":
			var p struct{ GUID, SuggestedFilename string }
			json.Unmarshal(e.Params, &p)
			b.mu.Lock()
			b.dl[p.GUID] = &download{name: p.SuggestedFilename, state: "inProgress", done: make(chan struct{})}
			b.mu.Unlock()
			select {
			case b.dlBegin <- p.GUID:
			default:
			}
		case "Browser.downloadProgress":
			var p struct{ GUID, State string }
			json.Unmarshal(e.Params, &p)
			b.mu.Lock()
			if d := b.dl[p.GUID]; d != nil && d.state == "inProgress" && p.State != "inProgress" {
				d.state = p.State
				close(d.done)
			}
			b.mu.Unlock()
		}
	}
}

// attached answers a target held at its start: another page is closed
// before it loads; a frame or worker of the page gets the deny list too.
func (b *Browser) attached(ctx context.Context, e Event) {
	var p struct {
		SessionID  string `json:"sessionId"`
		TargetInfo struct {
			TargetID string `json:"targetId"`
			Type     string `json:"type"`
		} `json:"targetInfo"`
	}
	json.Unmarshal(e.Params, &p)
	if p.TargetInfo.TargetID == b.target {
		return
	}
	if p.TargetInfo.Type == "page" {
		b.call(ctx, "", "Target.closeTarget", map[string]any{"targetId": p.TargetInfo.TargetID}, nil)
		return
	}
	b.call(ctx, p.SessionID, "Fetch.enable", map[string]any{"handleAuthRequests": true, "patterns": []any{map[string]any{"urlPattern": "*", "requestStage": "Request"}}}, nil)
	// A frame or worker of the page introduces itself as the page does.
	b.call(ctx, p.SessionID, "Network.setUserAgentOverride", b.agent, nil)
	b.call(ctx, p.SessionID, "Runtime.runIfWaitingForDebugger", nil, nil)
}

func (b *Browser) auth(ctx context.Context, e Event) {
	var p struct {
		RequestID     string `json:"requestId"`
		AuthChallenge struct {
			Source string `json:"source"`
		} `json:"authChallenge"`
	}
	json.Unmarshal(e.Params, &p)
	resp := map[string]any{"response": "CancelAuth"}
	// Only the broker is answered; a site's own login prompt is the page's.
	if p.AuthChallenge.Source == "Proxy" && b.opts.Credentials != nil {
		if user, pass, err := b.opts.Credentials(ctx); err == nil {
			resp = map[string]any{"response": "ProvideCredentials", "username": user, "password": pass}
		}
	}
	b.call(ctx, e.Session, "Fetch.continueWithAuth", map[string]any{"requestId": p.RequestID, "authChallengeResponse": resp}, nil)
}

func (b *Browser) paused(ctx context.Context, e Event) {
	var p struct {
		RequestID string `json:"requestId"`
		Request   struct {
			URL string `json:"url"`
		} `json:"request"`
	}
	json.Unmarshal(e.Params, &p)
	if !b.Allowed(p.Request.URL) {
		b.call(ctx, e.Session, "Fetch.failRequest", map[string]any{"requestId": p.RequestID, "errorReason": "BlockedByClient"}, nil)
		return
	}
	b.call(ctx, e.Session, "Fetch.continueRequest", map[string]any{"requestId": p.RequestID}, nil)
}

// Allowed reports whether the browser may load u now: the deny list holds
// while the agent has it, not while you do.
func (b *Browser) Allowed(u string) bool {
	b.mu.Lock()
	taken := b.takenBy != ""
	b.mu.Unlock()
	return taken || !denied(u, b.deny())
}

func (b *Browser) deny() []string { return append(append([]string{}, DefaultDeny...), b.opts.Deny...) }

// denied matches u against host/path prefixes, by host and path segments.
func denied(u string, deny []string) bool {
	p, err := url.Parse(u)
	if err != nil {
		return true
	}
	if p.Scheme != "http" && p.Scheme != "https" {
		return false // the browser's own pages and data; navigation checks schemes
	}
	host := strings.TrimSuffix(strings.ToLower(p.Hostname()), ".")
	// Decoded, as the site will read it: %73ecurity is security. Google's
	// /u/<n>/ picks a signed-in account and leads to the same pages.
	clean := strings.ToLower(path.Clean("/" + p.Path))
	clean = accountPrefix.ReplaceAllString(clean, "/")
	for _, d := range deny {
		dh, dp, _ := strings.Cut(strings.ToLower(d), "/")
		if host != dh && !strings.HasSuffix(host, "."+dh) {
			continue
		}
		dp = "/" + dp
		if dp == "/" || clean == dp || strings.HasPrefix(clean, strings.TrimSuffix(dp, "/")+"/") {
			return true
		}
	}
	return false
}

var accountPrefix = regexp.MustCompile(`^/u/[0-9]+(/|$)`)

// begin starts an agent's action: refused while you have the browser, and,
// unless it's leaving the page, while the page is one the agent may not see
// (reached without a request, by the page's own script). done ends it.
func (b *Browser) begin(ctx context.Context, leaving bool) (done func(), err error) {
	b.act.RLock()
	b.mu.Lock()
	taken := b.takenBy != ""
	b.mu.Unlock()
	if taken {
		b.act.RUnlock()
		return nil, ErrPaused
	}
	if !leaving {
		u, err := b.current(ctx)
		if err == nil && denied(u, b.deny()) {
			b.navigate(ctx, "about:blank")
			err = ErrDenied
		}
		if err != nil {
			b.act.RUnlock()
			return nil, err
		}
	}
	return b.act.RUnlock, nil
}

// current is the page's address now, as its history has it.
func (b *Browser) current(ctx context.Context) (string, error) {
	var h struct {
		CurrentIndex int `json:"currentIndex"`
		Entries      []struct {
			URL string `json:"url"`
		} `json:"entries"`
	}
	if err := b.call(ctx, b.page, "Page.getNavigationHistory", nil, &h); err != nil {
		return "", err
	}
	if h.CurrentIndex < 0 || h.CurrentIndex >= len(h.Entries) {
		return "about:blank", nil
	}
	return h.Entries[h.CurrentIndex].URL, nil
}

// eval runs one of page.js's functions in a world of Skybreaker's own, so
// the page's scripts can't change what it sees.
func (b *Browser) eval(ctx context.Context, fn string, args ...any) (string, error) {
	if args == nil {
		args = []any{}
	}
	a, _ := json.Marshal(args)
	expr := "(" + pageJS + ")." + fn + "(..." + string(a) + ")"
	// A world lasts as long as its document: made again after a navigation.
	for range 2 {
		b.mu.Lock()
		world := b.world
		b.mu.Unlock()
		if world == 0 {
			w, err := b.makeWorld(ctx)
			if err != nil {
				return "", err
			}
			world = w
		}
		var out struct {
			Result struct {
				Value string `json:"value"`
			} `json:"result"`
			Exception *struct {
				Text      string `json:"text"`
				Exception struct {
					Description string `json:"description"`
				} `json:"exception"`
			} `json:"exceptionDetails"`
		}
		err := b.call(ctx, b.page, "Runtime.evaluate", map[string]any{"expression": expr, "contextId": world, "returnByValue": true}, &out)
		if err != nil {
			b.mu.Lock()
			b.world = 0
			b.mu.Unlock()
			continue
		}
		if out.Exception != nil {
			return "", fmt.Errorf("the page: %s %s", out.Exception.Text, out.Exception.Exception.Description)
		}
		return out.Result.Value, nil
	}
	return "", errors.New("the page went away: read it again")
}

func (b *Browser) makeWorld(ctx context.Context) (int, error) {
	var tree struct {
		FrameTree struct {
			Frame struct {
				ID string `json:"id"`
			} `json:"frame"`
		} `json:"frameTree"`
	}
	if err := b.call(ctx, b.page, "Page.getFrameTree", nil, &tree); err != nil {
		return 0, err
	}
	var world struct {
		ExecutionContextID int `json:"executionContextId"`
	}
	if err := b.call(ctx, b.page, "Page.createIsolatedWorld", map[string]any{"frameId": tree.FrameTree.Frame.ID, "worldName": "skybreaker"}, &world); err != nil {
		return 0, err
	}
	b.mu.Lock()
	b.world = world.ExecutionContextID
	b.mu.Unlock()
	return world.ExecutionContextID, nil
}

// Open loads a page, as the agent.
func (b *Browser) Open(ctx context.Context, u string) error {
	done, err := b.begin(ctx, true)
	if err != nil {
		return err
	}
	defer done()
	p, err := url.Parse(u)
	if err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
		return fmt.Errorf("open %q: only http and https addresses", u)
	}
	// The agent's own steps are held to the deny list even as you take over.
	if denied(u, b.deny()) {
		return ErrDenied
	}
	return b.navigate(ctx, u)
}

func (b *Browser) navigate(ctx context.Context, u string) error {
	b.drainLoaded()
	var r struct {
		ErrorText string `json:"errorText"`
	}
	if err := b.call(ctx, b.page, "Page.navigate", map[string]any{"url": u}, &r); err != nil {
		return err
	}
	if r.ErrorText != "" {
		if r.ErrorText == "net::ERR_BLOCKED_BY_CLIENT" {
			return ErrDenied
		}
		return fmt.Errorf("open %s: %s", u, r.ErrorText)
	}
	b.waitLoad(ctx)
	return nil
}

func (b *Browser) drainLoaded() {
	select {
	case <-b.loaded:
	default:
	}
}

// waitLoad waits a while for the page to load; a slow page is read as it is.
func (b *Browser) waitLoad(ctx context.Context) {
	t := time.NewTimer(15 * time.Second)
	defer t.Stop()
	select {
	case <-b.loaded:
	case <-t.C:
	case <-ctx.Done():
	}
	time.Sleep(300 * time.Millisecond) // let scripts draw
}

// Element is something on the page the agent can act on.
type Element struct {
	Ref     int    `json:"ref"`
	Tag     string `json:"tag"`
	Role    string `json:"role,omitempty"`
	Type    string `json:"type,omitempty"`
	Name    string `json:"name"`
	Href    string `json:"href,omitempty"`
	Secret  bool   `json:"secret,omitempty"`
	Checked *bool  `json:"checked,omitempty"`
}

// Page is what the agent reads: text, and what it can click or type into.
type Page struct {
	URL      string    `json:"url"`
	Title    string    `json:"title"`
	Text     string    `json:"text"`
	Cut      bool      `json:"cut"`
	Elements []Element `json:"elements"`
}

const maxText = 20 << 10

// Read returns the page as the agent sees it, with no field's contents.
func (b *Browser) Read(ctx context.Context) (Page, error) {
	done, err := b.begin(ctx, false)
	if err != nil {
		return Page{}, err
	}
	defer done()
	v, err := b.eval(ctx, "read", maxText)
	if err != nil {
		return Page{}, err
	}
	var p Page
	err = json.Unmarshal([]byte(v), &p)
	return p, err
}

type spot struct {
	X, Y     float64
	Secret   bool
	Editable bool
	Missing  bool
}

func (b *Browser) where(ctx context.Context, ref int) (spot, error) {
	v, err := b.eval(ctx, "where", ref)
	if err != nil {
		return spot{}, err
	}
	var s spot
	json.Unmarshal([]byte(v), &s)
	if s.Missing {
		return s, fmt.Errorf("no element %d: read the page again", ref)
	}
	return s, nil
}

func (b *Browser) click(ctx context.Context, x, y float64) error {
	for _, typ := range []string{"mouseMoved", "mousePressed", "mouseReleased"} {
		ev := map[string]any{"type": typ, "x": x, "y": y, "button": "left", "clickCount": 1}
		if typ == "mouseMoved" {
			ev["button"] = "none"
		}
		if err := b.call(ctx, b.page, "Input.dispatchMouseEvent", ev, nil); err != nil {
			return err
		}
	}
	return nil
}

// Click clicks an element from the last Read, as a person would.
func (b *Browser) Click(ctx context.Context, ref int) error {
	done, err := b.begin(ctx, false)
	if err != nil {
		return err
	}
	defer done()
	s, err := b.where(ctx, ref)
	if err != nil {
		return err
	}
	b.drainLoaded()
	b.cue(Cue{Kind: "click", X: s.X, Y: s.Y})
	if err := b.click(ctx, s.X, s.Y); err != nil {
		return err
	}
	b.settle(ctx)
	return nil
}

// settle gives a click time to start a navigation and that to load.
func (b *Browser) settle(ctx context.Context) {
	t := time.NewTimer(800 * time.Millisecond)
	defer t.Stop()
	select {
	case <-b.loaded:
		time.Sleep(300 * time.Millisecond)
	case <-t.C:
	case <-ctx.Done():
	}
}

// Type types into an element, never into a password, one-time code or
// card field: those are yours.
func (b *Browser) Type(ctx context.Context, ref int, text string, submit bool) error {
	done, err := b.begin(ctx, false)
	if err != nil {
		return err
	}
	defer done()
	s, err := b.where(ctx, ref)
	if err != nil {
		return err
	}
	if s.Secret {
		return ErrSecret
	}
	if !s.Editable {
		return fmt.Errorf("element %d isn't a field", ref)
	}
	b.cue(Cue{Kind: "type", X: s.X, Y: s.Y})
	if err := b.click(ctx, s.X, s.Y); err != nil {
		return err
	}
	// The click may have moved focus, by script, to a field that is secret.
	v, err := b.eval(ctx, "focused")
	if err != nil {
		return err
	}
	var f struct{ Secret bool }
	if json.Unmarshal([]byte(v), &f); f.Secret {
		return ErrSecret
	}
	if err := b.call(ctx, b.page, "Input.insertText", map[string]any{"text": text}, nil); err != nil {
		return err
	}
	if submit {
		b.drainLoaded()
		if err := b.key(ctx, "Enter", "Enter", 13, "\r"); err != nil {
			return err
		}
		b.settle(ctx)
	}
	return nil
}

func (b *Browser) key(ctx context.Context, key, code string, keyCode int, text string) error {
	for _, typ := range []string{"keyDown", "keyUp"} {
		ev := map[string]any{"type": typ, "key": key, "code": code, "windowsVirtualKeyCode": keyCode}
		if typ == "keyDown" && text != "" {
			ev["text"] = text
		}
		if err := b.call(ctx, b.page, "Input.dispatchKeyEvent", ev, nil); err != nil {
			return err
		}
	}
	return nil
}

// Scroll moves the page up or down by most of a screen.
func (b *Browser) Scroll(ctx context.Context, down bool) error {
	done, err := b.begin(ctx, false)
	if err != nil {
		return err
	}
	defer done()
	dy := 700.0
	if !down {
		dy = -dy
	}
	b.cue(Cue{Kind: "scroll"})
	return b.call(ctx, b.page, "Input.dispatchMouseEvent", map[string]any{"type": "mouseWheel", "x": 640, "y": 450, "deltaX": 0, "deltaY": dy}, nil)
}

// Back goes back a page.
func (b *Browser) Back(ctx context.Context) error {
	done, err := b.begin(ctx, true)
	if err != nil {
		return err
	}
	defer done()
	var h struct {
		CurrentIndex int `json:"currentIndex"`
		Entries      []struct {
			ID  int    `json:"id"`
			URL string `json:"url"`
		} `json:"entries"`
	}
	if err := b.call(ctx, b.page, "Page.getNavigationHistory", nil, &h); err != nil {
		return err
	}
	if h.CurrentIndex < 1 {
		return errors.New("there's no page to go back to")
	}
	prev := h.Entries[h.CurrentIndex-1]
	if denied(prev.URL, b.deny()) {
		return ErrDenied
	}
	b.drainLoaded()
	if err := b.call(ctx, b.page, "Page.navigateToHistoryEntry", map[string]any{"entryId": prev.ID}, nil); err != nil {
		return err
	}
	b.waitLoad(ctx)
	return nil
}

// Screenshot is a PNG of the page with password, code and card fields
// blurred.
func (b *Browser) Screenshot(ctx context.Context) ([]byte, error) {
	done, err := b.begin(ctx, false)
	if err != nil {
		return nil, err
	}
	defer done()
	if _, err := b.eval(ctx, "mask", true); err != nil {
		return nil, err
	}
	defer b.eval(ctx, "mask", false)
	var s struct {
		Data string `json:"data"`
	}
	// At the page's own size: twice the pixels would cost the agent more
	// and show it nothing it needs.
	var m struct {
		CSSVisualViewport struct{ ClientWidth, ClientHeight float64 } `json:"cssVisualViewport"`
	}
	if err := b.call(ctx, b.page, "Page.getLayoutMetrics", nil, &m); err != nil {
		return nil, err
	}
	v := m.CSSVisualViewport
	clip := map[string]any{"x": 0, "y": 0, "width": v.ClientWidth, "height": v.ClientHeight, "scale": 1.0 / pixelRatio}
	if err := b.call(ctx, b.page, "Page.captureScreenshot", map[string]any{"format": "png", "clip": clip}, &s); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(s.Data)
}

// Status is where the browser is and who has it.
type Status struct {
	URL     string `json:"url"`
	Title   string `json:"title"`
	TakenBy string `json:"taken_by,omitempty"`
}

func (b *Browser) Status(ctx context.Context) (Status, error) {
	var h struct {
		CurrentIndex int `json:"currentIndex"`
		Entries      []struct {
			URL   string `json:"url"`
			Title string `json:"title"`
		} `json:"entries"`
	}
	if err := b.call(ctx, b.page, "Page.getNavigationHistory", nil, &h); err != nil {
		return Status{}, err
	}
	b.mu.Lock()
	s := Status{TakenBy: b.takenBy}
	b.mu.Unlock()
	if h.CurrentIndex >= 0 && h.CurrentIndex < len(h.Entries) {
		s.URL, s.Title = h.Entries[h.CurrentIndex].URL, h.Entries[h.CurrentIndex].Title
	}
	return s, nil
}

// Downloaded is a file the browser saved, by its name where it runs.
type Downloaded struct {
	Name string // as the site named it
	Path string // in Downloads
}

// Download clicks an element that downloads a file and waits for it.
func (b *Browser) Download(ctx context.Context, ref int) (Downloaded, error) {
	done, err := b.begin(ctx, false)
	if err != nil {
		return Downloaded{}, err
	}
	s, err := b.where(ctx, ref)
	if err != nil {
		done()
		return Downloaded{}, err
	}
drain:
	for {
		select {
		case <-b.dlBegin:
		default:
			break drain
		}
	}
	b.cue(Cue{Kind: "click", X: s.X, Y: s.Y})
	err = b.click(ctx, s.X, s.Y)
	done() // the wait for the file needn't hold you up
	if err != nil {
		return Downloaded{}, err
	}
	var guid string
	select {
	case guid = <-b.dlBegin:
	case <-time.After(20 * time.Second):
		return Downloaded{}, errors.New("no download started")
	case <-ctx.Done():
		return Downloaded{}, ctx.Err()
	}
	b.mu.Lock()
	d := b.dl[guid]
	b.mu.Unlock()
	select {
	case <-d.done:
	case <-time.After(10 * time.Minute):
		return Downloaded{}, errors.New("the download took too long")
	case <-ctx.Done():
		return Downloaded{}, ctx.Err()
	}
	b.mu.Lock()
	state := d.state
	b.mu.Unlock()
	if state != "completed" {
		return Downloaded{}, fmt.Errorf("the download was %s", state)
	}
	return Downloaded{Name: d.name, Path: path.Join(b.opts.Downloads, guid)}, nil
}
