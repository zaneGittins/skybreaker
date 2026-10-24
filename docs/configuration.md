# Configuration

Skybreaker reads two files: `/etc/skybreaker/skybreaker.yaml` (users, projects, policy) and `/etc/skybreaker/secrets.yaml`. [skybreaker.example.yaml](../deploy/skybreaker.example.yaml) is a commented example. Restart after editing:

```sh
sudo systemctl restart skybreaker
```

Most changes can be made in the app instead, under **Settings**.

## In the app

| Setting | What it's for |
|---|---|
| **Instructions** | What every agent is told, in Markdown. Applies alongside a repo's own `CLAUDE.md` or `AGENTS.md`. |
| **Prompts** | Reusable prompts, sent in any chat with `/prompt <name>`. |
| **Chat mode** | The mode new chats start in, such as Full auto, with an override per project. |
| **GitHub** | Creates and installs the GitHub App. |
| **Secrets** | API keys, each bound to the hosts it may be sent to. Never shown again. |
| **Bundles** | A host, its request rules and the secret to add. |
| **Project access** | Which bundles and hosts each project may reach. |

Settings in the app only add to the config file, which is shown read-only. Access changes apply when Skybreaker restarts itself, once no agent is working. Instructions, prompts and chat mode apply at once. Only users in `admins` can change settings (everyone in `users` by default). `app_settings: false` turns this off.

## Giving agents an API

Each project reaches only what it's granted; everything else is denied. To give agents an API:

1. Add the token as a **secret**, bound to the API's host.
2. Make a **bundle**: the host, rules for which requests run, wait for your approval or are refused, and the secret.
3. Grant the bundle to a project under **Project access**.

[Example: DigitalOcean](example-digitalocean.md) walks through it with screenshots.

Agents install their own tools with `mise` and `uv`. Grant a project the `tools` bundle so they can reach PyPI and the release downloads those use.

## GitHub

The App created in Settings gets `contents`, `pull_requests` and `issues` (write), and `metadata`, `actions`, `checks` and `statuses` (read), so agents can read CI. Never `workflows`, `administration` or `secrets`.

Agents may push only to `feature/*`, `fix/*`, `docs/*` and `chore/*` by default, and never delete branches or push tags. Protect `main` with a GitHub ruleset as well, with the App off its bypass list.

If Settings says your App lacks permissions, add them on GitHub, accept them on the installation, then add the two `actions` rules from the example config above `GET /repos/{repo}/**` in your `github` bundle.

## Assistants

An assistant made in the app needs nothing here. In the config file, it's a project with `kind: assistant` and no repo:

```yaml
projects:
  pip:
    kind: assistant
    harnesses: [claude, codex]
    capabilities: [tools]
```

Its routines are made in the app and kept in `<data_dir>/settings/routines.json`.

## Sandbox limits

Each sandbox gets up to 4 CPUs and 8 GB, or the whole host if it's smaller:

```yaml
sandbox:
  cpus: "4"
  memory: 8g
```

## Local models with pi

[pi](https://pi.dev) runs on your own API keys or models on your network (Ollama, llama.cpp, vLLM, LM Studio). Add a route to the model server and tell pi about it:

```yaml
capabilities:
  local-models:
    - route: /llm
      upstream: http://192.168.1.50:11434
      allow_private: [192.168.1.50]
      rules:
        - { match: "* /**", action: auto }

pi:
  providers:
    ollama:
      route: /llm
      api: openai-completions   # Ollama, vLLM, LM Studio, SGLang; llama.cpp for a llama.cpp router
      models: [qwen3-coder:30b]
```

Then give the project the `local-models` bundle and add `pi` to its harnesses. Pick the model with `/model` in pi. The sandbox reaches the server only through this route, and no key is stored in it.

For a hosted provider, run `/login` in pi and allow that provider's API host in the project's policy.
