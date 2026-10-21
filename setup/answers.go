package setup

import (
	"bytes"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"text/template"
	"unicode"

	"github.com/zaneGittins/skybreaker/internal/names"
)

// DefaultImage is the sandbox image CI publishes weekly.
const DefaultImage = "ghcr.io/zanegittins/skybreaker-sandbox:latest"

// Harnesses are the built-in harnesses a project may enable.
var Harnesses = []string{"claude", "codex", "pi"}

// Answers is everything an install asks for.
type Answers struct {
	Users   []string // tailnet logins allowed in, such as you@github
	Tailnet string   // the tailnet's DNS name, such as tail1234.ts.net
	AuthKey string   // a Tailscale auth key, for the first start only; empty to approve the machine from a link

	AppID  string // the GitHub App's ID; empty with AppKey to create it in Settings later
	AppKey string // path to its private key on this host

	Repo      string // the first project's owner/name
	Branch    string
	Harnesses []string
	GitName   string // optional commit author
	GitEmail  string

	Block []string // the LAN, kept from agents on top of the built-in private ranges
	Image string
}

var (
	tailnetRe = regexp.MustCompile(`^[a-z0-9-]+(\.[a-z0-9-]+)+$`)
	appIDRe   = regexp.MustCompile(`^[0-9]{1,12}$`)
	branchRe  = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._/-]{0,199}$`)
)

// ValidateTailscale checks the wizard's Tailscale page.
func (a *Answers) ValidateTailscale() error {
	var errs []error
	if len(a.Users) == 0 {
		errs = append(errs, errors.New("tailnet login: enter at least one, such as you@github"))
	}
	for _, u := range a.Users {
		if !strings.Contains(u, "@") || strings.ContainsFunc(u, unicode.IsSpace) || !plain(u) {
			errs = append(errs, fmt.Errorf("tailnet login %q: use the form you@github or you@example.com", u))
		}
	}
	if !tailnetRe.MatchString(a.Tailnet) {
		errs = append(errs, errors.New("tailnet name: use its DNS name, such as tail1234.ts.net (admin console, DNS)"))
	}
	if a.AuthKey != "" && (!strings.HasPrefix(a.AuthKey, "tskey-") || strings.ContainsFunc(a.AuthKey, func(r rune) bool { return unicode.IsSpace(r) || !unicode.IsPrint(r) })) {
		errs = append(errs, errors.New("auth key: paste a key from the admin console (Settings, Keys); it starts with tskey-. Or leave it empty"))
	}
	return errors.Join(errs...)
}

// AppLater reports whether the GitHub App is left to Skybreaker's
// Settings, which create it from a manifest in one click.
func (a *Answers) AppLater() bool { return a.AppID == "" && a.AppKey == "" }

// ValidateGitHub checks the wizard's GitHub App page.
func (a *Answers) ValidateGitHub() error {
	if a.AppLater() {
		return nil
	}
	var errs []error
	if !appIDRe.MatchString(a.AppID) {
		errs = append(errs, errors.New("App ID: the number on the App's settings page"))
	}
	if b, err := os.ReadFile(a.AppKey); err != nil {
		errs = append(errs, fmt.Errorf("private key: %v", err))
	} else if blk, _ := pem.Decode(b); blk == nil || !strings.Contains(blk.Type, "PRIVATE KEY") {
		errs = append(errs, fmt.Errorf("private key: %s is not a PEM private key", a.AppKey))
	}
	return errors.Join(errs...)
}

// ValidateProject checks the wizard's project page.
func (a *Answers) ValidateProject() error {
	var errs []error
	if !names.Repo(a.Repo) {
		errs = append(errs, errors.New("repo: use owner/name, such as you/project"))
	} else if !names.Project(ProjectName(a.Repo)) {
		errs = append(errs, fmt.Errorf("repo: can't name a project after %s", a.Repo))
	}
	if !branchRe.MatchString(a.Branch) || strings.Contains(a.Branch, "..") {
		errs = append(errs, errors.New("branch: a plain branch name, such as main"))
	}
	if len(a.Harnesses) == 0 {
		errs = append(errs, errors.New("harnesses: choose at least one"))
	}
	for _, h := range a.Harnesses {
		if !slices.Contains(Harnesses, h) {
			errs = append(errs, fmt.Errorf("harness %q: choose from %s", h, strings.Join(Harnesses, ", ")))
		}
	}
	if !plain(a.GitName) {
		errs = append(errs, errors.New("git author name: no control characters"))
	}
	if a.GitEmail != "" && (!strings.Contains(a.GitEmail, "@") || strings.ContainsFunc(a.GitEmail, unicode.IsSpace) || !plain(a.GitEmail)) {
		errs = append(errs, errors.New("git author email: such as you@example.com"))
	}
	return errors.Join(errs...)
}

func (a *Answers) Validate() error {
	var errs []error
	for _, b := range a.Block {
		if _, err := netip.ParsePrefix(b); err != nil {
			if _, err := netip.ParseAddr(b); err != nil {
				errs = append(errs, fmt.Errorf("network: %q is not an address or CIDR", b))
			}
		}
	}
	if a.Image == "" || !plain(a.Image) {
		errs = append(errs, errors.New("sandbox image: required"))
	}
	return errors.Join(a.ValidateTailscale(), a.ValidateGitHub(), a.ValidateProject(), errors.Join(errs...))
}

// plain reports whether s has no control or invisible formatting
// characters.
func plain(s string) bool {
	return !strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) })
}

// ProjectName derives a project name from a repo: lowercase, with anything
// but letters and digits folded to single dashes.
func ProjectName(repo string) string {
	_, name, _ := strings.Cut(repo, "/")
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
		} else {
			dash = true
		}
	}
	s := b.String()
	if len(s) > 40 {
		s = strings.TrimRight(s[:40], "-")
	}
	return s
}

// UIURL is where Skybreaker answers on the tailnet.
func (a *Answers) UIURL() string { return "https://skybreaker." + a.Tailnet }

// File is one file an install writes.
type File struct {
	Path string
	Data []byte
	Mode os.FileMode
}

// Files renders the config, the secrets, the Tailscale auth key (an empty
// env file without one) and a copy of the App's private key, all readable
// by the skybreaker user alone.
func (a *Answers) Files(l Layout) ([]File, error) {
	if err := a.Validate(); err != nil {
		return nil, err
	}
	var key []byte
	if !a.AppLater() {
		var err error
		if key, err = os.ReadFile(a.AppKey); err != nil {
			return nil, err
		}
	}
	caps := []string{"github", "tools"}
	for _, h := range []string{"claude", "codex"} {
		if slices.Contains(a.Harnesses, h) {
			caps = append(caps, h+"-login")
		}
	}
	var env [][2]string
	if a.GitName != "" {
		env = append(env, [2]string{"GIT_AUTHOR_NAME", a.GitName}, [2]string{"GIT_COMMITTER_NAME", a.GitName})
	}
	if a.GitEmail != "" {
		env = append(env, [2]string{"GIT_AUTHOR_EMAIL", a.GitEmail}, [2]string{"GIT_COMMITTER_EMAIL", a.GitEmail})
	}
	block := a.Block
	if block == nil {
		block = []string{}
	}
	data := map[string]any{
		"A": a, "Data": l.Data, "SecretsFile": l.Secrets(), "KeyFile": l.AppKey(), "UIURL": a.UIURL(),
		"Project": ProjectName(a.Repo), "Capabilities": caps, "Env": env, "Block": block,
	}
	var cfg, sec bytes.Buffer
	if err := configTmpl.Execute(&cfg, data); err != nil {
		return nil, err
	}
	if err := secretsTmpl.Execute(&sec, data); err != nil {
		return nil, err
	}
	files := []File{
		{l.Config(), cfg.Bytes(), 0o600},
		{l.Secrets(), sec.Bytes(), 0o600},
		{filepath.Join(l.Etc, "env"), nil, 0o600},
	}
	if a.AuthKey != "" {
		files[2].Data = []byte("TS_AUTHKEY=" + a.AuthKey + "\n")
	}
	if key != nil {
		files = append(files, File{l.AppKey(), key, 0o600})
	}
	return files, nil
}

// q quotes a value as a JSON string, which YAML reads as the same string.
func q(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

var funcs = template.FuncMap{"q": q}

var configTmpl = template.Must(template.New("config").Funcs(funcs).Parse(`# Skybreaker's config, written by skybreaker-setup. After an edit:
#   sudo skybreaker check && sudo systemctl restart skybreaker
# Projects can also be added in the app, which keeps them in projects.json.
# deploy/skybreaker.example.yaml in the repository shows every option.
# Secrets live in secrets.yaml next to it; this file names them, never holds them.

data_dir: {{q .Data}}
# Tailnet logins allowed to use Skybreaker and answer approvals.
users: {{q .A.Users}}
ui_url: {{q .UIURL}}
tailscale:
  hostname: skybreaker
secrets_file: {{q .SecretsFile}}

sandbox:
  image: {{q .A.Image}}
  memory: 8g
  cpus: "4"

broker:
  netguard:
    # Your LAN, kept from agents on top of the built-in private ranges.
    block: {{q .Block}}

projects:
  {{.Project}}:
    repo: {{q .A.Repo}}
    branch: {{q .A.Branch}}
    harnesses: {{q .A.Harnesses}}
{{- if .Env}}
    env:
{{- range .Env}}
      {{index . 0}}: {{q (index . 1)}}
{{- end}}
{{- end}}
    # What this sandbox may reach. Nothing else gets out.
    allow:
      - { host: registry.npmjs.org, mode: tunnel }
      - { host: proxy.golang.org, mode: tunnel }
      - { host: sum.golang.org, mode: tunnel }
      # proxy.golang.org redirects module zips here; only its own bucket.
      - host: storage.googleapis.com
        mode: mitm
        rules:
          - { match: "GET /proxy-golang-org-prod/**", action: auto }
    capabilities: {{q .Capabilities}}

# Capabilities are named bundles of hosts and routes that projects opt into.
# {repo} is the project's own repo.
capabilities:
  github:
    - host: github.com
      mode: mitm
      inject: { basic_auth: { user: x-access-token, secret: github_app } }
      rules:
        # git smart HTTP. Remotes must end in .git.
        - { match: "GET /{repo}.git/info/refs", action: auto }
        - { match: "POST /{repo}.git/git-upload-pack", action: auto }
        - { match: "POST /{repo}.git/git-receive-pack", action: auto, refs: ["refs/heads/feature/*", "refs/heads/fix/*", "refs/heads/docs/*", "refs/heads/chore/*"] }
        # Public release downloads, for tools agents install (mise, curl).
        # The token is narrowed to {repo}, so other private repos stay out.
        - { match: "GET /*/*/releases/download/**", action: auto, follow_redirects: ["release-assets.githubusercontent.com", "objects.githubusercontent.com"] }
        - { match: "GET /*/*/releases/latest/download/*", action: auto, follow_redirects: ["github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com"] }
    - host: api.github.com
      mode: mitm
      inject: { bearer: github_app }
      rules:
        - { match: "PUT /repos/{repo}/pulls/*/merge", action: ask }
        - { match: "DELETE /**", action: deny }
        # CI: runs, checks and job logs. GitHub serves logs and artifacts
        # from its storage by redirect, which the broker follows itself,
        # without credentials, to these hosts only. Artifacts can hold build
        # output with credentials baked in, so they wait for your approval.
        - { match: "GET /repos/{repo}/actions/artifacts/*/*", action: ask, follow_redirects: ["*.blob.core.windows.net", "*.actions.githubusercontent.com"] }
        - { match: "GET /repos/{repo}/actions/**", action: auto, follow_redirects: ["*.blob.core.windows.net", "*.actions.githubusercontent.com"] }
        - { match: "GET /repos/{repo}/**", action: auto }
        # Release and attestation lookups, for mise.
        - { match: "GET /repos/*/*/releases", action: auto }
        - { match: "GET /repos/*/*/releases/**", action: auto }
        - { match: "GET /repos/*/*/attestations/**", action: auto }
        - { match: "POST /repos/{repo}/pulls", action: auto }
        - { match: "POST /repos/{repo}/issues/*/comments", action: auto }
        - { match: "PATCH /repos/{repo}/pulls/*", action: auto }
        # gh uses GraphQL. Queries, and the mutations that open, edit and comment
        # on pull requests and issues, run; any other mutation (gh pr merge's
        # mergePullRequest, deleteRef, ...) waits for your approval.
        - match: "POST /graphql"
          action: auto
          graphql:
            mutations: [createPullRequest, updatePullRequest, markPullRequestReadyForReview, addComment, createIssue]
            other: ask
        - { match: "GET /user", action: auto }

  # Where agents install tools from: Python packages (uv, pip), Ansible
  # collections, HashiCorp's releases (mise) and Playwright's browsers.
  # GitHub releases come with the github capability.
  tools:
    - { host: pypi.org, mode: tunnel }
    - { host: files.pythonhosted.org, mode: tunnel }
    - { host: galaxy.ansible.com, mode: tunnel }
    - { host: releases.hashicorp.com, mode: mitm, methods: [GET, HEAD] }
    - { host: cdn.playwright.dev, mode: mitm, methods: [GET, HEAD] }
    - { host: playwright.download.prss.microsoft.com, mode: mitm, methods: [GET, HEAD] }

  # Claude and ChatGPT subscriptions: the harnesses sign in themselves and
  # keep their tokens in the sandbox's home.
  claude-login:
    - { host: claude.ai, mode: tunnel }
    - { host: platform.claude.com, mode: tunnel }
    - { host: console.anthropic.com, mode: tunnel }
    - { host: api.anthropic.com, mode: tunnel }
    - { host: statsig.anthropic.com, mode: tunnel }
    - host: raw.githubusercontent.com
      mode: mitm
      rules:
        - { match: "GET /anthropics/claude-code/**", action: auto }
  codex-login:
    - { host: auth.openai.com, mode: tunnel }
    - { host: chatgpt.com, mode: tunnel }
    - { host: api.openai.com, mode: tunnel }

  # A model server on your own network for pi (Ollama, llama.cpp, vLLM):
  # give a project this bundle and list the server under pi below.
  # local-models:
  #   - route: /llm
  #     upstream: http://192.168.1.50:11434   # plain HTTP only to an address in allow_private
  #     allow_private: [192.168.1.50]
  #     rules:
  #       - { match: "* /**", action: auto }

# pi in each sandbox is told about these, through the routes above.
# pi:
#   providers:
#     local:
#       route: /llm
#       api: openai-completions   # Ollama, vLLM, LM Studio; or llama.cpp for a llama.cpp router
#       models: [qwen3-coder:30b]
`))

var secretsTmpl = template.Must(template.New("secrets").Funcs(funcs).Parse(`# Skybreaker's secrets, written by skybreaker-setup. Readable by the
# skybreaker user only. Every secret lists the hosts it may be sent to; a
# request to any other host gets nothing injected and fails closed.
secrets:
  - name: github_app
    kind: github_app
    hosts: [github.com, api.github.com]
    github_app:
{{- if .A.AppLater}}
      # Created in Skybreaker: Settings, GitHub. Until then git and the
      # GitHub API get no credential.
      from_app: true
{{- else}}
      app_id: {{q .A.AppID}}
      key_file: {{q .KeyFile}}
{{- end}}
`))
