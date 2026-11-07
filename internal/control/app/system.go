package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zaneGittins/skybreaker/internal/control/store"
	host "github.com/zaneGittins/skybreaker/internal/control/system"
)

var ErrMaintenance = errors.New("this project is being cleaned; try again shortly")
var ErrCleanupBusy = errors.New("cleanup needs a running sandbox with no active terminal, SSH connection, setup or scheduled job, and chats quiet for 15 minutes")

type storageCache struct {
	mu    sync.Mutex
	value host.Storage
}

func (a *App) maintenanceGate(project string) *sync.RWMutex {
	v, _ := a.maintenance.LoadOrStore(project, &sync.RWMutex{})
	return v.(*sync.RWMutex)
}

// beginActivity refuses new work while maintenance holds the project.
func (a *App) beginActivity(project string) (func(), error) {
	if _, err := a.project(project); err != nil {
		return nil, err
	}
	gate := a.maintenanceGate(project)
	if !gate.TryRLock() {
		return nil, ErrMaintenance
	}
	return gate.RUnlock, nil
}

const storageScript = `set -eu
cd /
size() { if [ -e "$2" ]; then n=$(du -s -B1 -- "$2") || exit 1; n=${n%%[[:space:]]*}; else n=0; fi; printf '%s %s\n' "$1" "$n"; }
size workspace /workspace
size worktrees /skybreaker/worktrees
size home /root
size docker /var/lib/docker
cache=$(GOTOOLCHAIN=local go env GOCACHE 2>/dev/null) || cache=/root/.cache/go-build
case "$cache" in /root/*) size go "$cache" ;; *) printf 'go 0\n' ;; esac
`

func parseStorage(out []byte) (host.Storage, error) {
	values := map[string]uint64{}
	for line := range strings.Lines(string(out)) {
		f := strings.Fields(line)
		if len(f) != 2 {
			return host.Storage{}, errors.New("invalid storage counters")
		}
		switch f[0] {
		case "workspace", "worktrees", "home", "docker", "go":
		default:
			return host.Storage{}, errors.New("unknown storage counter")
		}
		if _, ok := values[f[0]]; ok {
			return host.Storage{}, errors.New("duplicate storage counter")
		}
		n, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			return host.Storage{}, err
		}
		values[f[0]] = n
	}
	if len(values) != 5 || values["go"] > values["home"] {
		return host.Storage{}, errors.New("incomplete storage counters")
	}
	r := host.Storage{Workspace: values["workspace"], Worktrees: values["worktrees"], Home: values["home"] - values["go"], Docker: values["docker"], GoCache: values["go"], ScannedAt: time.Now().UTC()}
	for _, n := range []uint64{r.Workspace, r.Worktrees, r.Home, r.Docker, r.GoCache} {
		if ^uint64(0)-r.Total < n {
			return host.Storage{}, errors.New("storage counter overflow")
		}
		r.Total += n
	}
	return r, nil
}

func (a *App) scanStorage(ctx context.Context, project string) (host.Storage, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	out, err := a.sandbox.Exec(ctx, project, "timeout", "--signal=KILL", "40s", "sh", "-lc", storageScript)
	if err != nil {
		return host.Storage{}, errors.New("storage scan failed; the sandbox may be stopped or still starting")
	}
	r, err := parseStorage(out)
	if err != nil {
		return r, errors.New("the sandbox returned incomplete storage measurements")
	}
	return r, nil
}

func (a *App) storageEntry(project string) *storageCache {
	v, _ := a.storage.LoadOrStore(project, &storageCache{})
	return v.(*storageCache)
}

func (a *App) ProjectStorage(ctx context.Context, project string) (host.Storage, error) {
	if _, err := a.project(project); err != nil {
		return host.Storage{}, err
	}
	unlock, err := a.beginActivity(project)
	if err != nil {
		return host.Storage{}, err
	}
	defer unlock()
	c := a.storageEntry(project)
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.value.ScannedAt.IsZero() && time.Since(c.value.ScannedAt) < time.Minute {
		return c.value, nil
	}
	r, err := a.scanStorage(ctx, project)
	if err == nil {
		c.value = r
	}
	return r, err
}

func (a *App) CleanStorage(ctx context.Context, project string, opts host.Cleanup) (host.Cleaned, error) {
	var r host.Cleaned
	if _, err := a.project(project); err != nil {
		return r, err
	}
	if (!opts.Go && !opts.Docker) || opts.OlderThanDays < 1 || opts.OlderThanDays > 365 {
		return r, errors.New("choose a cache and an age between 1 and 365 days")
	}
	gate := a.maintenanceGate(project)
	if !gate.TryLock() {
		return r, ErrCleanupBusy
	}
	defer gate.Unlock()
	st, err := a.sandbox.Status(ctx, project)
	if err != nil || !st.Running {
		return r, ErrCleanupBusy
	}
	a.clonesMu.Lock()
	settingUp := a.clones[project] != nil
	a.clonesMu.Unlock()
	if settingUp || a.busy(ctx, project) {
		return r, ErrCleanupBusy
	}
	// Include all runs, even when newer completed runs fill the history page.
	for _, run := range a.store.ListRuns(project, 0) {
		if run.Status == store.RunRunning {
			return r, ErrCleanupBusy
		}
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 4*time.Minute)
	defer cancel()
	r.Before, err = a.scanStorage(ctx, project)
	if err != nil {
		return r, err
	}
	var script strings.Builder
	script.WriteString("set -eu\ncd /\nfailed=0\n")
	if opts.Go {
		script.WriteString(`(cache=$(GOTOOLCHAIN=local go env GOCACHE) || exit 1
case "$cache" in /root/*) GOTOOLCHAIN=local go clean -cache >/dev/null ;; *) echo 'Go cache is outside the managed home directory' >&2; exit 1 ;; esac) || failed=1
`)
	}
	if opts.Docker {
		age := strconv.Itoa(opts.OlderThanDays*24) + "h"
		fmt.Fprintf(&script, "docker builder prune --force --filter until=%s >/dev/null || failed=1\ndocker image prune --force --filter until=%s >/dev/null || failed=1\n", age, age)
	}
	script.WriteString("exit \"$failed\"\n")
	_, cleanErr := a.sandbox.Exec(ctx, project, "timeout", "--signal=KILL", "120s", "sh", "-lc", script.String())
	if cleanErr != nil {
		log.Printf("skybreaker: cleanup failed in %s", project)
		r.Error = "Cleanup did not finish. Some caches may have been cleared; scan storage before retrying."
	}
	c := a.storageEntry(project)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.value = host.Storage{}
	after, scanErr := a.scanStorage(ctx, project)
	if scanErr == nil {
		r.After = &after
		c.value = after
		if after.Total < r.Before.Total {
			r.Recovered = r.Before.Total - after.Total
		}
	} else if r.Error == "" {
		r.Error = "Cleanup finished, but the new storage total could not be measured."
	}
	a.Publish(Event{Type: "system", Project: project, Data: "cleanup"})
	return r, nil
}
