package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/zaneGittins/skybreaker/internal/proxy/policy"
)

const good = `{
  "projects": {
    "orbit": {
      "repo": "zaneGittins/orbit",
      "allow": [
        {"host": "registry.npmjs.org", "mode": "tunnel"},
        {"host": "proxy.golang.org", "mode": "mitm", "methods": ["GET", "HEAD"]}
      ],
      "capabilities": ["github", "tracker", "k8s-prod"]
    },
    "bifract": {"repo": "zaneGittins/bifract", "capabilities": ["github"]}
  },
  "capabilities": {
    "github": [
      {"host": "github.com", "mode": "mitm",
       "inject": {"basic_auth": {"user": "x-access-token", "secret": "github_app"}},
       "rules": [
         {"match": "GET /{repo}.git/info/refs", "action": "auto"},
         {"match": "POST /{repo}.git/git-upload-pack", "action": "auto"},
         {"match": "POST /{repo}.git/git-receive-pack", "action": "auto", "refs": ["refs/heads/agent/*"]}
       ]},
      {"host": "api.github.com", "mode": "mitm", "inject": {"bearer": "github_app"},
       "rules": [{"match": "GET /repos/{repo}/**", "action": "auto"}, {"match": "PUT /repos/{repo}/pulls/*/merge", "action": "ask"}]}
    ],
    "tracker": [
      {"route": "/orbit", "upstream": "https://orbit.example.ts.net", "rules": [{"match": "POST /mcp", "action": "auto"}], "allow_private": ["100.101.1.2/32"]}
    ],
    "k8s-prod": [
      {"route": "/k8s/prod", "upstream": "https://abc.k8s.ondigitalocean.com", "inject": {"bearer": "k8s_prod"},
       "k8s": {"auto": ["get", "list", "watch"], "ask": ["create", "patch", "delete"], "deny": ["secrets", "pods/exec", "ns:kube-system"]}}
    ]
  }
}`

func load(t *testing.T, body string) (*Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "proxy.json")
	os.WriteFile(p, []byte(body), 0o600)
	return Load(p)
}

func mustLoad(t *testing.T, body string) *Config {
	t.Helper()
	c, err := load(t, body)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestResolveHosts(t *testing.T) {
	c := mustLoad(t, good)
	p, ok := c.Project("orbit")
	if !ok {
		t.Fatal("project missing")
	}
	gh := p.Host("GitHub.com.")
	if gh == nil || gh.Mode != ModeMITM || gh.Capability != "github" || gh.Inject.SecretName() != "github_app" {
		t.Fatalf("github.com rule = %+v", gh)
	}
	d := gh.Rules.Decide("POST", "/zaneGittins/orbit.git/git-receive-pack")
	if d.Action != policy.Auto || !slices.Equal(d.Rule.Refs, []string{"refs/heads/agent/*"}) {
		t.Errorf("receive-pack decision = %+v", d)
	}
	if d := gh.Rules.Decide("POST", "/zaneGittins/bifract.git/git-receive-pack"); d.Action != policy.Deny {
		t.Errorf("other repo push: %v, want deny", d.Action)
	}
	if npm := p.Host("registry.npmjs.org"); npm == nil || npm.Mode != ModeTunnel || npm.Rules != nil {
		t.Errorf("npm rule = %+v", npm)
	}
	gp := p.Host("proxy.golang.org")
	if gp.Rules.Decide("GET", "/x/@v/list").Action != policy.Auto || gp.Rules.Decide("POST", "/x").Action != policy.Deny {
		t.Error("methods shorthand not compiled to rules")
	}
	if !gp.AllowsPort(443) || gp.AllowsPort(22) {
		t.Error("default port should be 443 only")
	}
	if p.Host("example.com") != nil {
		t.Error("unlisted host resolved")
	}
	// bifract's {repo} expands to its own repo.
	b, _ := c.Project("bifract")
	if b.Host("github.com").Rules.Decide("GET", "/zaneGittins/bifract.git/info/refs").Action != policy.Auto {
		t.Error("bifract cannot fetch its own repo")
	}
	if b.Host("github.com").Rules.Decide("GET", "/zaneGittins/orbit.git/info/refs").Action != policy.Deny {
		t.Error("bifract can fetch orbit")
	}
	if b.Route("/orbit/mcp") != nil {
		t.Error("bifract sees a route it has no capability for")
	}
}

func TestResolveRoutes(t *testing.T) {
	c := mustLoad(t, good)
	p, _ := c.Project("orbit")
	r := p.Route("/orbit/mcp")
	if r == nil || r.Rules == nil || r.Upstream.Host != "orbit.example.ts.net" {
		t.Fatalf("orbit route = %+v", r)
	}
	if len(r.AllowPrivate) != 1 {
		t.Errorf("allow_private not parsed: %v", r.AllowPrivate)
	}
	if got := r.Strip("/orbit/mcp"); got != "/mcp" {
		t.Errorf("Strip = %q", got)
	}
	k := p.Route("/k8s/prod/api/v1/pods")
	if k == nil || k.K8s == nil || k.Strip("/k8s/prod/api/v1/pods") != "/api/v1/pods" || k.Strip("/k8s/prod") != "/" {
		t.Fatalf("k8s route = %+v", k)
	}
	for _, path := range []string{"/orbitX/mcp", "/k8s/production/api", "/k8s", "/"} {
		if p.Route(path) != nil {
			t.Errorf("Route(%q) matched", path)
		}
	}
}

func TestCheckSecrets(t *testing.T) {
	c := mustLoad(t, good)
	hosts := map[string][]string{
		"github_app": {"github.com", "api.github.com"},
		"k8s_prod":   {"abc.k8s.ondigitalocean.com"},
	}
	lookup := func(name string) ([]string, bool) { h, ok := hosts[name]; return h, ok }
	if err := c.CheckSecrets(lookup); err != nil {
		t.Fatal(err)
	}
	hosts["github_app"] = []string{"github.com"} // not bound to api.github.com
	if err := c.CheckSecrets(lookup); err == nil || !strings.Contains(err.Error(), "api.github.com") {
		t.Fatalf("unbound secret accepted: %v", err)
	}
	delete(hosts, "k8s_prod")
	if err := c.CheckSecrets(lookup); err == nil {
		t.Fatal("missing secret accepted")
	}
	// A bundle no project uses yet is checked too, so the mistake shows
	// when it's written, not when a project first takes it.
	unused := strings.Replace(good, `"tracker": [`, `"leak": [{"host": "evil.example", "mode": "mitm", "methods": ["GET"], "inject": {"bearer": "github_app"}}], "tracker": [`, 1)
	hosts["github_app"], hosts["k8s_prod"] = []string{"github.com", "api.github.com"}, []string{"abc.k8s.ondigitalocean.com"}
	if err := mustLoad(t, unused).CheckSecrets(lookup); err == nil || !strings.Contains(err.Error(), "leak") {
		t.Fatalf("unused bundle sending a secret off its hosts: %v", err)
	}
}

func TestInvalid(t *testing.T) {
	cases := map[string][2]string{
		"tunnel with methods":   {`{"host": "registry.npmjs.org", "mode": "tunnel"}`, `{"host": "registry.npmjs.org", "mode": "tunnel", "methods": ["GET"]}`},
		"tunnel with inject":    {`{"host": "registry.npmjs.org", "mode": "tunnel"}`, `{"host": "registry.npmjs.org", "mode": "tunnel", "inject": {"bearer": "x"}}`},
		"mitm without rules":    {`"mode": "mitm", "methods": ["GET", "HEAD"]`, `"mode": "mitm"`},
		"rules and methods":     {`"methods": ["GET", "HEAD"]`, `"methods": ["GET"], "rules": [{"match": "GET /**", "action": "auto"}]`},
		"bad mode":              {`"mode": "tunnel"`, `"mode": "socks"`},
		"wildcard host":         {`"registry.npmjs.org"`, `"*.npmjs.org"`},
		"host with port":        {`"registry.npmjs.org"`, `"registry.npmjs.org:443"`},
		"host with scheme":      {`"registry.npmjs.org"`, `"https://registry.npmjs.org"`},
		"duplicate host":        {`{"host": "registry.npmjs.org", "mode": "tunnel"}`, `{"host": "github.com", "mode": "tunnel"}`},
		"unknown capability":    {`"capabilities": ["github"]}`, `"capabilities": ["gitlab"]}`},
		"bad rule action":       {`"action": "ask"}]`, `"action": "maybe"}]`},
		"repo var without repo": {`"bifract": {"repo": "zaneGittins/bifract", `, `"bifract": {`},
		"route reserved":        {`"route": "/orbit"`, `"route": "/ca.crt"`},
		"route traversal":       {`"route": "/orbit"`, `"route": "/orbit/../ca.crt"`},
		"route trailing slash":  {`"route": "/orbit"`, `"route": "/orbit/"`},
		"route http upstream":   {`"https://orbit.example.ts.net"`, `"http://orbit.example.ts.net"`},
		"route upstream path":   {`"https://orbit.example.ts.net"`, `"https://orbit.example.ts.net/x?y"`},
		"route two kinds":       {`"rules": [{"match": "POST /mcp", "action": "auto"}],`, `"rules": [{"match": "POST /mcp", "action": "auto"}], "k8s": {"auto": ["get"]},`},
		"route no kind":         {`"rules": [{"match": "POST /mcp", "action": "auto"}],`, ``},
		"bad k8s policy":        {`"auto": ["get", "list", "watch"]`, `"auto": ["fly"]`},
		"bad allow_private":     {`"100.101.1.2/32"`, `"orbit"`},
		"bad project name":      {`"bifract": {`, `"Bifract!": {`},
		"wildcard repo":         {`"repo": "zaneGittins/bifract"`, `"repo": "zaneGittins/*"`},
		"repo with dot segment": {`"repo": "zaneGittins/bifract"`, `"repo": "zaneGittins/.."`},
		"repo without owner":    {`"repo": "zaneGittins/bifract"`, `"repo": "bifract"`},
		"host and route":        {`{"route": "/orbit",`, `{"host": "x.com", "route": "/orbit",`},
		"bad port":              {`"mode": "tunnel"}`, `"mode": "tunnel", "ports": [0]}`},
		"bad header inject":     {`{"bearer": "github_app"}`, `{"header": {"name": "Bad Header", "secret": "github_app"}}`},
		"two injects":           {`{"bearer": "github_app"}`, `{"bearer": "github_app", "header": {"name": "x-api-key", "secret": "github_app"}}`},
	}
	for name, sub := range cases {
		body := strings.Replace(good, sub[0], sub[1], 1)
		if body == good {
			t.Errorf("%s: substitution did not apply", name)
			continue
		}
		if _, err := load(t, body); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestLoadSecrets(t *testing.T) {
	p := filepath.Join(t.TempDir(), "secrets.json")
	os.WriteFile(p, []byte(`{"secrets": [{"name": "do_token", "kind": "static", "value": "dop_v1_x", "hosts": ["api.digitalocean.com"]}]}`), 0o600)
	specs, err := LoadSecrets(p)
	if err != nil || len(specs) != 1 || specs[0].Name != "do_token" {
		t.Fatalf("LoadSecrets = %+v, %v", specs, err)
	}
	os.Chmod(p, 0o644)
	if _, err := LoadSecrets(p); err == nil {
		t.Fatal("world-readable secrets file accepted")
	}
}

func TestUnusedCapabilitiesValidated(t *testing.T) {
	// A typo in a capability no project uses yet must still fail at load,
	// not on the day someone enables it.
	body := strings.Replace(good, `"capabilities": {`, `"capabilities": {
    "spare": [{"host": "api.example.com", "mode": "mitm", "rules": [{"match": "GET /**", "action": "perhaps"}]}],`, 1)
	if _, err := load(t, body); err == nil || !strings.Contains(err.Error(), "spare") {
		t.Fatalf("err = %v", err)
	}
	ok := strings.Replace(good, `"capabilities": {`, `"capabilities": {
    "spare": [{"host": "api.example.com", "mode": "mitm", "rules": [{"match": "GET /repos/{repo}/**", "action": "auto"}]},
              {"route": "/spare", "upstream": "https://spare.example.com", "rules": [{"match": "GET /**", "action": "auto"}]}],`, 1)
	if _, err := load(t, ok); err != nil {
		t.Fatalf("valid unused capability refused: %v", err)
	}
}
