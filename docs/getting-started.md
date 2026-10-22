# Getting started

## Quick start

1. **Make a server.** A new Debian or Ubuntu VM with about 4 GB of memory and a few cores per active project.
2. **Install.** On the server, run the line under [Install](#install). It adds Docker, gVisor and nftables, then asks for your tailnet login, tailnet name and repo. Leave the auth key empty: once Skybreaker starts, the installer shows a link, and you approve the machine on your tailnet from it.
3. **Connect GitHub.** Open `https://skybreaker.<your-tailnet>.ts.net/settings`, choose **Create GitHub App** and install it on your repo.
4. **Start a chat.** Tap **+**, pick Claude or Codex, and sign in once.

To skip step 2's questions, paste [deploy/cloud-init.yaml](../deploy/cloud-init.yaml) into the server's user data when you create it, with your own values. It installs everything as the server boots.

## Requirements

- **A Linux host** with Docker. A VM is fine; allow about 4 GB of memory and a few cores per active project. On Debian and Ubuntu, the installer adds Docker if it's missing; elsewhere, [install it](https://docs.docker.com/engine/install/) first.
- **A Tailscale tailnet** with MagicDNS and HTTPS certificates turned on (admin console → DNS). Skybreaker joins it as its own machine, `skybreaker`, and serves the app over HTTPS with a certificate from Tailscale; without them, `https://skybreaker.<your-tailnet>.ts.net` fails with a TLS error. Skybreaker fetches the certificate as it starts, which can take a minute the first time. For a second Skybreaker on the same tailnet, give it a name of its own (`--hostname`, or the wizard's machine name): Tailscale gives a machine asking for a taken name another one, and the installer says so if it happens.
- **A GitHub account or organization** for the repos agents will work on. Skybreaker creates its GitHub App for you (see [GitHub](#github)).

gVisor is required, and the installer adds it if it's missing.

## Install

```sh
curl -fsSL https://github.com/zaneGittins/skybreaker/releases/latest/download/install.sh | sh
```

The installer asks for your tailnet login, your tailnet's name and your first repo. A Tailscale auth key (admin console → Settings → Keys) is optional: without one, the installer shows a sign-in link once Skybreaker starts and waits while you approve the machine. `sudo skybreaker-setup status` shows the link again if you miss it. Either way, turn off key expiry for the `skybreaker` machine in the admin console, or it drops off the tailnet when its key expires. Leave the GitHub App's fields empty to create the App afterwards in Settings, or give the ID and private key of one you made. It then installs gVisor and nftables, writes the config to `/etc/skybreaker`, validates it, installs the systemd units and sandbox image, and starts Skybreaker. If a step fails, fix the cause and run `sudo skybreaker-setup install` again; it picks up where it stopped.

For unattended installs, which need the auth key:

```sh
sudo TS_AUTHKEY=tskey-auth-... skybreaker-setup install --yes \
  --login you@github --tailnet tail1234.ts.net --repo you/project
```

Each sandbox is limited to 4 CPUs and 8 GB, or to the whole host if it's smaller; `sandbox.cpus` and `sandbox.memory` in the config change that, and a CPU limit above what the host has is lowered to fit.

Add `--app-id 123456 --app-key app.pem` to use a GitHub App you made yourself.

To install a specific version or prerelease, pass it to the script: `... | SKYBREAKER_VERSION=v0.1.0 sh`.

To install without the wizard, see [Install by hand](manual-install.md).

## GitHub

Open **Settings** and choose **Create GitHub App** (for your account or an organization). GitHub opens with the App filled in; confirm, and you come back to Settings to install it on your repos. Skybreaker keeps the App's key; you never copy a `.pem`.

- **App permissions:** `contents:write`, `pull_requests:write`, `issues:write` and `metadata:read`, plus `actions:read`, `checks:read` and `statuses:read` so agents can read CI runs, their logs and pull request checks. Never `workflows`, `administration` or `secrets`.
- **An App made before agents read CI** lacks the last three, and Settings says so. Add them under the App's permissions on GitHub, then accept them on its installation. Then add the two `actions` rules from [the example config](../deploy/skybreaker.example.yaml) above the `GET /repos/{repo}/**` rule in your config's `github` bundle, so agents can read job logs, and download artifacts once you approve.
- **Ruleset on `main`:** block direct pushes, force pushes and deletion, and keep the App off the bypass list.
- **Branches:** the broker only allows pushes to the branch globs in the project's rules (`feature/*`, `fix/*`, `docs/*` and `chore/*` by default), and refuses deletes and tags.

## First steps

Open `https://skybreaker.<your-tailnet>.ts.net`, tap **+** and pick Claude or Codex. Skybreaker supports three agents: Claude Code and Codex, as chats or in their terminal UIs, and pi in its terminal UI (see [pi and local models](#pi-and-local-models)).

- **Own branch:** by default each new session works in its own copy of the repository, on a branch of its own (`skybreaker/<session>`), so agents working side by side never touch each other's files. Untick **On its own branch** in the picker to work in the project's `/workspace` instead. A fresh copy has no ignored files such as `node_modules` or `.env`, so the agent installs what it needs. The branch shows under the chat's title, Changes shows that chat's work, and Ship it has the agent rename the branch, push it and open a pull request. When a chat ends, its copy is tidied away and its branch is kept. A copy with uncommitted work or ignored files (a `.env`, say) stays for the ten most recently ended; past that, its uncommitted work is saved first, but its ignored files go. A sandbox started by an earlier version restarts to make room for these copies once nothing in it is in use.
- **Sign in** once per project. For Claude, open the chat's terminal and run `/login`; Codex shows a sign-in code in the chat. Logins persist across restarts.
- **Notifications:** on a phone, add Skybreaker to the home screen, open it from there and tap the bell.
- **In a chat**, `/rename <title>` names it, `/color <colour>` tints it to tell it apart, and `/goal <text>` pins a goal and gives it to the agent. `/compact` summarises the conversation to free up context, `/fork` copies it into a new chat that goes its own way (on a branch of its own, with a copy of the files as they are), and `/clear` ends the chat and starts a fresh one with the same agent, mode, model and branch. `/prompt <name>` sends one of the prompts saved in Settings: type `/prompt ` to pick from them, and anything after the name is added below it. Other slash commands go to the agent as usual.
- **Attachments:** with nothing written, the send button is a paperclip that attaches photos and files to your next message, and a + beside them adds more; on a desktop you can also paste a screenshot or drop files on the chat. Up to 10 files, 10 MB each and 25 MB in all, and 256 MB per chat. Images go to the model as images, scaled down first if they're large, and every file is saved in the sandbox under `/root/.skybreaker/attachments/<session>` for the agent to open. Files untouched for a month are tidied away there, and put back if their chat carries on.
- **The ring** at the top right of a chat fills as the conversation uses up the model's context window, turning yellow past 60% and red past 85%. Tap it to compact, fork, start a new chat or end the session.
- **Boards:** each project has one, and agents use it through Skybreaker's MCP tools. To give your own Claude Code access to every board:

  ```sh
  claude mcp add --transport http --scope user skybreaker https://skybreaker.<your-tailnet>.ts.net/mcp
  ```

## From your terminal

The same `skybreaker` binary is a terminal client for your own Linux or Mac. It reaches Skybreaker over your tailnet as you, so there's nothing to sign in to.

```sh
curl -fsSL https://github.com/zaneGittins/skybreaker/releases/latest/download/install-cli.sh | sh
skybreaker              # pick a session and attach; Ctrl-b d brings you back
```

`skybreaker new <project> [harness]` starts a session, `skybreaker ls` lists them, and `skybreaker update` (or the same one-liner) updates the client. Picking a chat continues it in the harness's own terminal UI; your next message from the phone resumes the chat. The client finds the machine named `skybreaker` on your tailnet and remembers it in `~/.skybreaker/config.json`; `skybreaker connect <address>` sets another.

## VS Code

Desktop VS Code can work in a sandbox over SSH, with no Microsoft tunnel. Install the terminal client and VS Code's **Remote - SSH** extension, then on each computer:

```sh
skybreaker vscode setup     # once: makes this computer a key, adds it, writes ~/.ssh/config
skybreaker code <project>   # or Open in VS Code in the project's menu
```

The project is in `/workspace`, and sessions on their own branch are in `/skybreaker/worktrees/<session>`. A VS Code window is one more shell in the sandbox: it reaches only what the project's policy allows, through the broker. The sandbox can't download VS Code's server itself, so Remote - SSH downloads it on your computer and copies it over; set `"remote.downloadExtensionsLocally": true` in VS Code to install extensions the same way. Remove a computer's key in the same menu; its open windows disconnect.

## pi and local models

[pi](https://pi.dev) is the agent for your own API keys or models you run yourself. It runs in a project's terminal (or `skybreaker new <project> pi`) and as a scheduled job, and it gets Skybreaker's board and preview tools like the others. Sign in with `/login` in pi for a hosted provider, with that provider's API host in the project's policy.

For a model server on your own network (Ollama, a llama.cpp router, vLLM, LM Studio), add a broker route to it and tell pi about it in the config file:

```yaml
capabilities:
  local-models:
    - route: /llm
      upstream: http://192.168.1.50:11434   # plain HTTP is allowed only to an address in allow_private
      allow_private: [192.168.1.50]
      rules:
        - { match: "* /**", action: auto }

pi:
  providers:
    ollama:
      route: /llm
      api: openai-completions   # Ollama, vLLM, LM Studio, SGLang
      models: [qwen3-coder:30b]
```

Give a project the bundle and `pi` among its harnesses. Each time pi starts in that project's sandbox, it finds the server in its own `models.json`, with the sandbox's broker token (`$SKYBREAKER_TOKEN`, which pi reads from the environment) as its key, so the file holds no secret; pick the model with `/model`. Your own providers in pi's config stay as they are. For a llama.cpp router, `api: llama.cpp` uses pi's built-in support instead: the router lists its own models, and `/llama` loads them. Skybreaker's own address on the host is `127.0.0.1`, which a route may name in `allow_private` for a server on the same machine.

The route is the only way the sandbox reaches the server, and only the paths its rules allow. The broker's token stops at the broker; the server sees the request without it.

## Previews

To see what an agent is building, open the project's menu (or **Preview** on a desktop) and pick a port the sandbox is listening on. The page opens on its own address, `https://skybreaker.<your-tailnet>.ts.net:84xx`, apart from Skybreaker. Dev servers must listen on `0.0.0.0` rather than `localhost` (`vite --host 0.0.0.0`, `next dev -H 0.0.0.0`). If you use Tailscale ACLs, allow your login ports 8401-8499 on the `skybreaker` machine as well as 443. Agents can open a preview themselves with Skybreaker's `preview` tool, which puts a link in the chat.

A preview page loads only from its own server, so fonts or scripts from CDNs don't load there. Its cookies are `HttpOnly`, so frameworks that read a CSRF cookie from script (Django's `csrftoken`, `XSRF-TOKEN`) need it sent another way while previewed.

## Tools in the sandbox

The sandbox image has git, gh, Go, Node, Python, Docker, ripgrep, fd, jq, shellcheck, sqlite3 and Chromium with Playwright (`playwright screenshot <url> shot.png`). Agents install anything else themselves, under `/root`, which outlives the container:

```sh
mise use -g terraform doctl     # most CLIs, from their GitHub or HashiCorp releases
uv tool install ansible-core    # Python tools
```

The `tools` capability lets a project reach where these come from: PyPI, Ansible Galaxy, HashiCorp's releases and Playwright's browsers; the `github` capability adds public GitHub release downloads. `apt install` works too where a project allows Ubuntu's mirrors, but is lost when the sandbox restarts onto a new image.

For setup every container needs, commit `.skybreaker/setup.sh` to the repository. A new sandbox runs it as root in `/workspace` once the repository is cloned, logging to `/var/log/skybreaker-setup.log`, and chats and scheduled runs wait for it (up to 10 minutes). Terminals open at once.

## Usage

On a desktop, the home page shows the tokens your chats and scheduled runs used each day for the last two weeks, by harness, and how much of your Claude and Codex plans' 5-hour and weekly limits is used, as each last reported it. Only harnesses that reported anything appear. Terminal sessions report nothing to Skybreaker, so they aren't counted. The counts are kept for a month in `usage.json` in the data directory.

The sidebar collapses to icons with the button beside the logo; each browser remembers which you chose.

## Configuration

The base is `/etc/skybreaker/skybreaker.yaml` (users, projects, policy) and `secrets.yaml`. Each project lists what its sandbox may reach in `allow` and `capabilities` (bundles); anything else is denied. Restart `skybreaker` after editing. [deploy/skybreaker.example.yaml](../deploy/skybreaker.example.yaml) is a commented example.

Most of it can also be changed in the app, by the users in `admins` (everyone in `users` unless you list them):

- **Settings** creates the GitHub App, adds secrets (each bound to the hosts you list, and never shown again), makes bundles, and adds bundles and hosts to any project. Every change to what a project reaches is shown as the broker compiles it before you save.
  [Example: DigitalOcean](example-digitalocean.md) walks through a secret, a bundle and a grant, with screenshots.
- **New project** on the home page creates a project from the bundles there are.
- **Instructions** in Settings are what every agent in every project is told, in Markdown: Claude Code reads them as its managed `CLAUDE.md`, Codex as developer instructions and pi as part of its system prompt. A project's own `CLAUDE.md` or `AGENTS.md` still applies. They're saved in `settings/instructions.md`, mounted read-only into each sandbox, and reach sessions started after a save, with no restart.
- **Prompts** in Settings are reusable prompts for every chat in every project, sent with `/prompt <name>`. Each is sent as your own message, so it works the same with every agent. They're saved in `settings/prompts.json` and need no restart.

The app saves in the data directory, in `settings/` (`projects.json`, `access.json` and `app-secrets.json`), and only ever adds to the config file: what the file says is shown read-only, and a name in both is an error. Changes apply when Skybreaker restarts itself, once no agent is working. `app_settings: false` in the config file turns changes in the app off.

## Updating

```sh
sudo skybreaker-setup upgrade     # install the latest release
sudo skybreaker-setup rollback    # return to the previous version
sudo skybreaker-setup status
```

`upgrade` checks your config against the new version before changing anything, and restores the previous version if the new one doesn't stay up. Restarting stops running chats, so it asks first.
