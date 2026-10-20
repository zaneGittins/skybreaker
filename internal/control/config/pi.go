package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"

	policy "github.com/zaneGittins/skybreaker/internal/proxy/config"
)

// Pi sets pi up in sandboxes: model servers it reaches through broker
// routes, such as a model server on your own network.
type Pi struct {
	Providers map[string]*PiProvider `json:"providers" yaml:"providers"`
}

// PiProvider is a model server for pi, behind a broker route.
type PiProvider struct {
	Route string `json:"route" yaml:"route"` // the route, such as /llm
	// API is what the server speaks, as pi names it: openai-completions
	// (Ollama, vLLM, LM Studio, SGLang), openai-responses or
	// anthropic-messages; or llama.cpp, a llama.cpp router, whose models pi
	// finds and loads itself.
	API string `json:"api" yaml:"api"`
	// Path is where the API is under the route; default /v1, or the
	// route's root for llama.cpp.
	Path string `json:"path" yaml:"path"`
	// Models are the model IDs to offer; a llama.cpp router lists its own.
	Models []string `json:"models" yaml:"models"`
}

// LlamaCPP is the API name of a llama.cpp router.
const LlamaCPP = "llama.cpp"

var (
	piNameRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	piAPIRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,47}$`)
	piPathRe  = regexp.MustCompile(`^(/[A-Za-z0-9._~-]+)*$`)
	piModelRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,127}$`)
)

func (c *Config) validatePi() error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf("pi: "+format, a...)) }
	routes := map[string]bool{}
	note := func(es []*policy.Entry) {
		for _, e := range es {
			if e != nil && e.Route != "" {
				routes[e.Route] = true
			}
		}
	}
	for _, es := range c.Capabilities {
		note(es)
	}
	for _, p := range c.Projects {
		if p != nil {
			note(p.Allow)
		}
	}
	llamas := 0
	for _, name := range slices.Sorted(maps.Keys(c.Pi.Providers)) {
		p := c.Pi.Providers[name]
		switch {
		case p == nil:
			bad("provider %s is empty", name)
			continue
		case !piNameRe.MatchString(name):
			bad("provider name %q: use lowercase letters, digits, dashes and underscores", name)
		}
		if !routes[p.Route] {
			bad("provider %s: route %q isn't a route any bundle or project has", name, p.Route)
		}
		if !piAPIRe.MatchString(p.API) {
			bad("provider %s: api %q isn't one pi names (openai-completions, openai-responses, anthropic-messages, llama.cpp)", name, p.API)
		}
		if !piPathRe.MatchString(p.Path) {
			bad("provider %s: path %q must look like /v1", name, p.Path)
		}
		for _, m := range p.Models {
			if !piModelRe.MatchString(m) {
				bad("provider %s: %q isn't a model ID", name, m)
			}
		}
		if p.API == LlamaCPP {
			if llamas++; llamas > 1 {
				bad("only one provider can be a llama.cpp router")
			}
		} else if len(p.Models) == 0 {
			bad("provider %s: list its models (a llama.cpp router lists its own)", name)
		}
	}
	return errors.Join(errs...)
}

// PiConfig is pi's setup for a project's sandbox: models.json, for the
// sandbox's read-only /etc/skybreaker, and environment variables. The file
// names the token by variable ($SKYBREAKER_TOKEN, which pi expands), so it
// holds no secret; a llama.cpp router takes it in LLAMA_API_KEY. Both are
// nil for a project that doesn't run pi or reaches none of the servers.
func (c *Config) PiConfig(project, token string) ([]byte, map[string]string) {
	p := c.Projects[project]
	pp, ok := c.Policy.Project(project)
	if p == nil || !ok || !slices.Contains(p.Harnesses, "pi") {
		return nil, nil
	}
	broker := "http://broker:" + strconv.Itoa(int(c.Broker.Port))
	providers := map[string]any{}
	var env map[string]string
	for _, name := range slices.Sorted(maps.Keys(c.Pi.Providers)) {
		pr := c.Pi.Providers[name]
		if pp.Route(pr.Route) == nil {
			continue
		}
		if pr.API == LlamaCPP {
			env = map[string]string{"LLAMA_BASE_URL": broker + pr.Route + pr.Path, "LLAMA_API_KEY": token}
			continue
		}
		path := pr.Path
		if path == "" {
			path = "/v1"
		}
		models := []map[string]string{}
		for _, m := range pr.Models {
			models = append(models, map[string]string{"id": m})
		}
		providers[name] = map[string]any{
			"baseUrl": broker + pr.Route + path, "api": pr.API, "apiKey": "$SKYBREAKER_TOKEN", "models": models,
		}
	}
	if len(providers) == 0 {
		return nil, env
	}
	b, _ := json.MarshalIndent(map[string]any{"providers": providers}, "", "  ")
	return append(b, '\n'), env
}
