package controld

import (
	"context"
	"errors"
	"time"

	"github.com/zaneGittins/skybreaker/internal/control/api"
	"github.com/zaneGittins/skybreaker/internal/control/app"
	"github.com/zaneGittins/skybreaker/internal/control/config"
	"github.com/zaneGittins/skybreaker/internal/proxy/broker"
)

// ErrRestart is Run asking to be run again, to pick up a project created
// in the app. The project list fans out into the broker's policy, the
// sandbox networks, boards and previews, so a fresh start is how it lands.
var ErrRestart = errors.New("skybreaker: restarting to add a project")

// adder creates projects from the app.
type adder struct {
	configPath string
	app        *app.App
	broker     *broker.Broker
	restart    func() // when no agent is working
}

func (d *adder) AddProject(ctx context.Context, name string, a config.Added) (api.AddedProject, error) {
	check := func(c *config.Config) error { return broker.Check(c.Policy, c.SecretsFile) }
	if err := config.AddProject(d.configPath, name, a, check); err != nil {
		return api.AddedProject{}, err
	}
	out := api.AddedProject{Name: name, Restart: "now"}
	if d.app.AgentsWorking() {
		out.Restart = "when_idle"
	}
	gctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	installed, url, err := d.broker.GitHubAccess(gctx, a.Repo)
	cancel()
	out.GitHub = &api.GitHubAccess{Installed: installed, InstallURL: url}
	if err != nil {
		out.GitHub.Error = err.Error()
	}
	d.restart()
	return out, nil
}
