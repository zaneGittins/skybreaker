package controld

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/zaneGittins/skybreaker/internal/board"
	"github.com/zaneGittins/skybreaker/internal/control/api"
	"github.com/zaneGittins/skybreaker/internal/control/app"
	"github.com/zaneGittins/skybreaker/internal/control/config"
	"github.com/zaneGittins/skybreaker/internal/proxy/broker"
	policy "github.com/zaneGittins/skybreaker/internal/proxy/config"
	"github.com/zaneGittins/skybreaker/internal/proxy/githubapp"
)

// ErrRestart is Run asking to be run again, to pick up a project or a
// setting changed in the app. Both fan out into the broker's policy and
// secrets, the sandbox networks, boards and previews, so a fresh start is
// how they land.
var ErrRestart = errors.New("skybreaker: restarting to apply a change made in the app")

// settings saves what admins change in the app, and creates projects.
type settings struct {
	configPath   string
	secrets      string // the secrets file
	instructions string // every agent's instructions
	prompts      string // the canned prompts
	app          *app.App
	broker       *broker.Broker
	boards       *board.Store
	restart      func() // when no agent is working
	pending      atomic.Bool
}

// check is what a change must pass before it's saved: the broker's secret
// bindings, and board keys that every project's board can take (a clash
// would otherwise only fail the restart).
func (d *settings) check(c *config.Config) error {
	if err := broker.Check(c.Policy, c.SecretsFile, c.AppSecretsFile); err != nil {
		return err
	}
	return d.boards.CheckSync(boardSpecs(c))
}

// saved records a change and restarts to apply it once no agent is working.
func (d *settings) saved(by, what string) string {
	d.broker.LogConfig(by, what)
	d.pending.Store(true)
	when := "now"
	if d.app.AgentsWorking() {
		when = "when_idle"
	}
	d.restart()
	return when
}

func (d *settings) Apply(_ context.Context, by, what string, change func(*config.AppFiles) error, dry bool) (api.Applied, error) {
	before, after, err := config.Edit(d.configPath, change, d.check, dry)
	if err != nil {
		return api.Applied{}, err
	}
	out := api.Applied{Before: before, After: after}
	if !dry {
		out.Restart = d.saved(by, what)
	}
	return out, nil
}

func (d *settings) Saved() (*config.Config, config.AppFiles, error) {
	c, err := config.Load(d.configPath)
	if err != nil {
		return nil, config.AppFiles{}, err
	}
	f, err := config.ReadAppFiles(c.AppPaths())
	return c, f, err
}

func (d *settings) Pending() bool { return d.pending.Load() }

func (d *settings) FileSecrets() ([]api.FileSecret, error) {
	if d.secrets == "" {
		return nil, nil
	}
	specs, err := policy.LoadSecrets(d.secrets)
	if err != nil {
		return nil, err
	}
	var out []api.FileSecret
	for _, sp := range specs {
		f := api.FileSecret{Name: sp.Name, Kind: sp.Kind, Hosts: sp.Hosts}
		if g := sp.GitHubApp; g != nil {
			f.FromApp, f.AppID = g.FromApp, g.AppID
		}
		out = append(out, f)
	}
	return out, nil
}

func (d *settings) SecretsUsed() map[string]time.Time { return d.broker.SecretsUsed() }

func (d *settings) GitHubInstallURL(ctx context.Context) (string, error) {
	return d.broker.GitHubInstallURL(ctx)
}

func (d *settings) Instructions() (string, error) {
	return config.ReadInstructions(d.instructions)
}

func (d *settings) SetInstructions(by, text string) error {
	if err := config.WriteInstructions(d.instructions, text); err != nil {
		return err
	}
	d.broker.LogConfig(by, "changed the agents' instructions")
	return d.app.SetInstructions(config.CleanInstructions(text))
}

func (d *settings) Prompts() ([]config.Prompt, error) {
	return config.ReadPrompts(d.prompts)
}

func (d *settings) SetPrompts(by string, ps []config.Prompt) error {
	if err := config.WritePrompts(d.prompts, ps); err != nil {
		return err
	}
	d.broker.LogConfig(by, "changed the prompts")
	return nil
}

func (d *settings) ConvertGitHubApp(ctx context.Context, code string) (githubapp.Created, error) {
	return githubapp.Convert(ctx, &http.Client{Timeout: 30 * time.Second}, "", code)
}

func (d *settings) AddProject(ctx context.Context, by, name string, a config.Added) (api.AddedProject, error) {
	if err := config.AddProject(d.configPath, name, a, d.check); err != nil {
		return api.AddedProject{}, err
	}
	out := api.AddedProject{Name: name, Restart: d.saved(by, "created project "+name+" for "+a.Repo)}
	gctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	installed, url, err := d.broker.GitHubAccess(gctx, a.Repo)
	cancel()
	out.GitHub = &api.GitHubAccess{Installed: installed, InstallURL: url}
	if err != nil {
		out.GitHub.Error = err.Error()
	}
	return out, nil
}
