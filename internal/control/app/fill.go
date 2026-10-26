package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"time"
)

// ErrWorkspaceFull is copying a folder into a workspace that has files.
var ErrWorkspaceFull = errors.New("the workspace isn't empty, so nothing was copied into it")

// originScript points the copied repo in workspace $1 at $2, GitHub over
// HTTPS, the way the broker reaches it.
const originScript = `cd "$1" && test -d .git || exit 0
git remote set-url origin "$2" 2>/dev/null || git remote add origin "$2"`

// FillWorkspace unpacks a gzipped tar into the project's empty workspace,
// in place of its clone: it holds the clone's place, so a sandbox start
// or a chat waits for it. A repo's clone then fills in only what the copy
// lacks, and the setup script runs as after a clone.
func (a *App) FillWorkspace(ctx context.Context, project string, archive io.Reader) error {
	p, err := a.project(project)
	if err != nil {
		return err
	}
	if a.cfg.Assistant(project) {
		return ErrNoRepo
	}
	a.clonesMu.Lock()
	if a.clones[project] != nil {
		a.clonesMu.Unlock()
		return ErrWorkspaceFull
	}
	if a.clones == nil {
		a.clones = map[string]*cloneRun{}
	}
	c := &cloneRun{done: make(chan struct{})}
	a.clones[project] = c
	a.clonesMu.Unlock()
	finish := func(err error) error {
		c.err = err
		a.clonesMu.Lock()
		delete(a.clones, project)
		a.clonesMu.Unlock()
		close(c.done)
		return err
	}

	if err := a.StartSandbox(ctx, project); err != nil {
		return finish(err)
	}
	if err := a.waitReady(ctx, project); err != nil {
		return finish(err)
	}
	out, err := a.sandbox.Exec(ctx, project, "ls", "-A", workspaceDir)
	if err != nil {
		return finish(err)
	}
	if strings.TrimSpace(string(out)) != "" {
		return finish(ErrWorkspaceFull)
	}
	if _, err := a.sandbox.ExecInput(ctx, project, archive, 0, "tar", "-xzf", "-", "--no-same-owner", "-C", workspaceDir); err != nil {
		// It was empty, so clearing it can't lose anything.
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		if _, cerr := a.sandbox.Exec(cctx, project, "find", workspaceDir, "-mindepth", "1", "-delete"); cerr != nil {
			log.Printf("skybreaker: clear %s after a failed copy: %v", project, cerr)
		}
		return finish(fmt.Errorf("copying into %s: %w", workspaceDir, err))
	}
	go func() {
		var err error
		if p.Repo != "" {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			if _, err := a.sandbox.Exec(ctx, project, "sh", "-c", originScript, "sh", workspaceDir, "https://github.com/"+p.Repo+".git"); err != nil {
				log.Printf("skybreaker: set origin in %s: %v", project, err)
			}
			cancel()
			err = a.cloneWorkspace(project, p.Repo)
		} else {
			a.Publish(Event{Type: "sandbox", Project: project, Data: "workspace ready"})
		}
		if err == nil {
			a.setupWorkspace(project)
		}
		finish(err)
	}()
	return nil
}
