package app

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zaneGittins/skybreaker/internal/control/browser"
	"github.com/zaneGittins/skybreaker/internal/control/sandbox"
)

// ErrNoBrowser is asking a coding project for a browser: only assistants
// have one.
var ErrNoBrowser = errors.New("only an assistant has a browser")

const (
	// browserIdle is how long a browser nobody uses keeps running.
	browserIdle = 30 * time.Minute
	// maxDownload bounds a file the browser hands the agent.
	maxDownload   = 200 << 20
	browserMemory = "2g"
)

// browserTokenTTL is how long a browser's broker token lasts; a longer
// browser mints another.
const browserTokenTTL = 24 * time.Hour

type liveBrowser struct {
	b    *browser.Browser
	mu   sync.Mutex
	used time.Time
	// The broker token it answers challenges with, by value and ID, and
	// when it last did.
	token, tokenID string
	answered       time.Time
}

// credentials answers the broker's challenge. Chromium keeps a login once
// it works, so a challenge soon after an answer means the token stopped
// working (the sandbox's restart revokes the project's): mint another.
func (lb *liveBrowser) credentials(now time.Time, mint func() (Minted, error), revoke func(id string)) (string, error) {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	if lb.token != "" && now.Sub(lb.answered) > 5*time.Second {
		lb.answered = now
		return lb.token, nil
	}
	m, err := mint()
	if err != nil {
		return "", err
	}
	if lb.tokenID != "" {
		revoke(lb.tokenID)
	}
	lb.token, lb.tokenID, lb.answered = m.Token, m.ID, now
	return m.Token, nil
}

type browsers struct {
	mu       sync.Mutex
	m        map[string]*liveBrowser
	starting map[string]*sync.Mutex // one start at a time, per assistant
}

// Browser is an assistant's browser, started on first use: a container
// beside its sandbox, Chromium driven over a pipe, its traffic through the
// broker under the assistant's policy.
func (a *App) Browser(ctx context.Context, project string) (*browser.Browser, error) {
	if _, err := a.project(project); err != nil {
		return nil, err
	}
	if !a.cfg.Assistant(project) {
		return nil, ErrNoBrowser
	}
	a.browsers.mu.Lock()
	if a.browsers.m == nil {
		a.browsers.m, a.browsers.starting = map[string]*liveBrowser{}, map[string]*sync.Mutex{}
	}
	one := a.browsers.starting[project]
	if one == nil {
		one = &sync.Mutex{}
		a.browsers.starting[project] = one
	}
	a.browsers.mu.Unlock()
	one.Lock()
	defer one.Unlock()
	a.browsers.mu.Lock()
	lb := a.browsers.m[project]
	a.browsers.mu.Unlock()
	if lb != nil {
		select {
		case <-lb.b.Done():
		default:
			lb.mu.Lock()
			lb.used = time.Now()
			lb.mu.Unlock()
			return lb.b, nil
		}
	}
	lb, err := a.startBrowser(ctx, project)
	if err != nil {
		return nil, err
	}
	a.browsers.mu.Lock()
	a.browsers.m[project] = lb
	a.browsers.mu.Unlock()
	return lb.b, nil
}

func (a *App) startBrowser(ctx context.Context, project string) (*liveBrowser, error) {
	// The sandbox's network and the broker's view of the project come
	// with its sandbox.
	if err := a.StartSandbox(ctx, project); err != nil {
		return nil, err
	}
	spec := sandbox.BrowserSpec{Project: project, Image: a.cfg.Sandbox.Image, Subnet: a.cfg.Subnet(project),
		Profile: filepath.Join(a.cfg.DataDir, "assistants", project, "browser"), Memory: browserMemory}
	if err := a.sandbox.StartBrowser(ctx, spec); err != nil {
		return nil, err
	}
	pin, err := a.caPin(ctx)
	if err != nil {
		return nil, err
	}
	lb := &liveBrowser{used: time.Now()}
	o := browser.Options{
		Launch:    a.launchChromium(project),
		Profile:   "/profile",
		Downloads: "/downloads",
		Proxy:     "http://" + sandbox.Gateway(spec.Subnet).String() + ":" + strconv.Itoa(int(a.cfg.Broker.Port)),
		CASPKI:    pin,
		Credentials: func(ctx context.Context) (string, string, error) {
			tok, err := lb.credentials(time.Now(), func() (Minted, error) { return a.broker.Mint(ctx, project, browserTokenTTL) },
				func(id string) { a.broker.Revoke(ctx, id) })
			return project, tok, err
		},
	}
	if a.browserOpts != nil {
		a.browserOpts(&o)
	}
	sctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	b, err := browser.Start(sctx, o)
	if err != nil {
		a.sandbox.StopBrowser(context.WithoutCancel(ctx), project)
		return nil, fmt.Errorf("start the browser: %w", err)
	}
	lb.b = b
	go a.watchBrowser(project, lb)
	return lb, nil
}

// watchBrowser closes a browser left idle, and tidies up once it's gone.
func (a *App) watchBrowser(project string, lb *liveBrowser) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-lb.b.Done():
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			lb.mu.Lock()
			id := lb.tokenID
			lb.mu.Unlock()
			if id != "" {
				a.broker.Revoke(ctx, id)
			}
			if err := a.sandbox.StopBrowser(ctx, project); err != nil {
				log.Printf("skybreaker: stop %s's browser: %v", project, err)
			}
			cancel()
			a.browsers.mu.Lock()
			if a.browsers.m[project] == lb {
				delete(a.browsers.m, project)
			}
			a.browsers.mu.Unlock()
			return
		case <-t.C:
			lb.mu.Lock()
			idle := time.Since(lb.used) > browserIdle
			lb.mu.Unlock()
			if st, err := lb.b.Status(context.Background()); idle && err == nil && st.TakenBy == "" {
				lb.b.Close()
			}
		}
	}
}

// BrowserStatus is where an assistant's browser is, if one runs; it never
// starts one.
func (a *App) BrowserStatus(ctx context.Context, project string) (browser.Status, bool) {
	a.browsers.mu.Lock()
	lb := a.browsers.m[project]
	a.browsers.mu.Unlock()
	if lb == nil {
		return browser.Status{}, false
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	st, err := lb.b.Status(ctx)
	return st, err == nil
}

// StopBrowser closes an assistant's browser, if it runs.
func (a *App) StopBrowser(project string) {
	a.browsers.mu.Lock()
	lb := a.browsers.m[project]
	a.browsers.mu.Unlock()
	if lb != nil {
		lb.b.Close()
	}
}

// launchChromium starts Chromium in the browser container over docker exec
// -i: its DevTools pipe is the exec's stdin and stdout.
func (a *App) launchChromium(project string) browser.Launch {
	return func(ctx context.Context, args []string) (io.ReadCloser, io.WriteCloser, func(), error) {
		argv := a.sandbox.BrowserChromium(project, args...)
		cmd := exec.Command(argv[0], argv[1:]...)
		in, err := cmd.StdinPipe()
		if err != nil {
			return nil, nil, nil, err
		}
		out, err := cmd.StdoutPipe()
		if err != nil {
			return nil, nil, nil, err
		}
		if err := cmd.Start(); err != nil {
			return nil, nil, nil, err
		}
		go cmd.Wait()
		return out, in, func() {
			cmd.Process.Kill()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			a.sandbox.StopBrowser(ctx, project)
		}, nil
	}
}

// caPin is the broker CA's SPKI hash, the one CA the browser trusts beyond
// its own roots.
func (a *App) caPin(ctx context.Context) (string, error) {
	b, err := a.broker.CA(ctx)
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return "", errors.New("broker CA isn't PEM")
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("broker CA: %w", err)
	}
	sum := sha256.Sum256(c.RawSubjectPublicKeyInfo)
	return base64.StdEncoding.EncodeToString(sum[:]), nil
}

// SaveToWorkspace writes data at rel under the assistant's /workspace,
// through the sandbox, and returns its path there.
func (a *App) SaveToWorkspace(ctx context.Context, project, rel string, data []byte) (string, error) {
	dst := "/workspace/" + strings.TrimPrefix(filepath.Clean("/"+rel), "/")
	_, err := a.sandbox.ExecInput(ctx, project, data, 0, "sh", "-c", `mkdir -p "$(dirname "$1")" && cat > "$1"`, "sh", dst)
	if err != nil {
		return "", err
	}
	return dst, nil
}

// CopyDownload moves a file the browser saved into the assistant's
// workspace, under downloads/, from one container to the other.
func (a *App) CopyDownload(ctx context.Context, project string, d browser.Downloaded) (string, error) {
	// Gone from the browser however the copy ends, so they don't pile up.
	defer a.sandbox.BrowserExec(context.WithoutCancel(ctx), project, 0, "rm", "-f", "--", d.Path)
	data, err := a.sandbox.BrowserExec(ctx, project, maxDownload+1, "cat", "--", d.Path)
	if err != nil {
		return "", err
	}
	if len(data) > maxDownload {
		return "", fmt.Errorf("%s is larger than %d MB", d.Name, maxDownload>>20)
	}
	return a.SaveToWorkspace(ctx, project, "downloads/"+downloadName(d.Name), data)
}

// downloadName is a site's name for a file, safe as one path element.
func downloadName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_', r == ' ':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.TrimLeft(strings.TrimSpace(b.String()), ".")
	if len(out) > 100 {
		out = out[:100]
	}
	if out == "" {
		return "download"
	}
	return out
}
