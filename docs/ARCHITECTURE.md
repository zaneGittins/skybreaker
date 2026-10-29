# Architecture

Skybreaker is one Go binary, `skybreaker`, running on one Linux host. It serves the app over Tailscale, runs a gVisor sandbox per project, and brokers every request those sandboxes make. The threat model is in [security.md](security.md).

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="img/architecture-dark.svg">
    <img src="img/architecture-light.svg" alt="Your phone reaches the app over Tailscale. The app drives agents in a gVisor sandbox per project. Every request from a sandbox leaves through the broker, which adds credentials." width="100%">
  </picture>
</p>

## Code map

| Path | Role |
|---|---|
| `cmd/skybreaker` | Entry point: `serve`, `check`, `nftables`, `version`. |
| `internal/control/controld` | Assembles the server; start here. |
| `internal/control/app` | Sandboxes, broker tokens, chats, terminals, review. |
| `internal/control/chat` | Harness drivers (Claude stream-json, Codex app-server, pi RPC), event log, process manager. |
| `internal/control/api` | The UI's HTTP API and server-sent events. |
| `internal/control/sandbox`, `netrules` | Container specs under `runsc`, and the nftables ruleset. |
| `internal/control/mcp`, `internal/board` | Skybreaker's MCP server and the per-project boards. |
| `internal/assistant`, `internal/control/browser` | Assistants' look, personality, memory and proposals; their browser, driven over Chromium's DevTools pipe. |
| `internal/control/scheduler`, `hooks`, `terminal` | Scheduled runs, Claude Code hooks, WebSocket terminals. |
| `internal/control/preview` | Previews of a sandbox's web server, each on its project's own origin. |
| `internal/control/sshkeys` | SSH keys allowed into sandboxes, for VS Code. |
| `internal/control/usage` | Tokens chats and runs report, by hour, and each harness's plan limits. |
| `internal/proxy/broker` | The broker, assembled in-process. |
| `internal/proxy/server` | The request path: tunnel, MITM and routes. |
| `internal/proxy/policy`, `gitproto`, `graphql`, `k8s` | Rule matching, git push ref checks and pack reading, GraphQL operations, Kubernetes attributes. |
| `internal/proxy/content` | Commit and pull request rules: lengths and forbidden patterns. |
| `internal/proxy/approval`, `secrets`, `netguard`, `audit` | Approvals, host-bound secrets, connect-time address checks, audit log. |
| `internal/tsauth` | Tailnet identity; `tsnetsrv` is the Tailscale adapter. |
| `internal/client` | The terminal client: session picker, attach, VS Code over SSH, self-update. |
| `web/` | The UI: plain JavaScript and CSS, embedded, no build step. |
| `setup/` | `skybreaker-setup`, the installer, in its own module. |
| `images/sandbox`, `deploy/` | The sandbox image, systemd units and example configs. |

The server uses the standard library except for two adapters: `tsauth/tsnetsrv` (Tailscale) and `configfile/yamlfmt` (YAML). Everything else is tested without them (`go test -tags nodeps ./...`).

## Request path

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="img/request-path-dark.svg">
    <img src="img/request-path-light.svg" alt="Every request passes caller, destination, rules, credentials and connect checks; each can deny, and every decision is audited." width="100%">
  </picture>
</p>

1. **Caller.** From a sandbox network, with a live token for that project. Tokens are rechecked on every request, and revoking one closes its connections.
2. **Destination.** In the project's `allow` list or capabilities, as a **tunnel** (bytes spliced), **mitm** (TLS terminated with the broker's CA) or **route** (a reverse-proxy path).
3. **Rules.** The first matching rule decides `auto`, `ask` or `deny`; no match is a denial. Own credentials, impersonation headers, ambiguous paths and protocol upgrades are refused first. GraphQL, Kubernetes and content rules (`gitproto.ReadPush`, `server/content.go`) parse further. An `ask` is held, bound to a hash of the exact request.
4. **Credentials.** The token is checked again, then the secret is added only if it lists the upstream host.
5. **Connect.** Resolved addresses are checked against loopback, link-local, metadata, private and tailnet ranges.

## Settings and restarts

The config file is the base. The app adds to it from `<data_dir>/settings` (`config.Paths`), since the service can't write `/etc`:

| File | Holds |
|---|---|
| `projects.json` | Projects created in the app |
| `access.json` | Bundles, and bundles and hosts granted to projects |
| `app-secrets.json` | Secrets and the GitHub App, merged with the secrets file by `secrets.Merge` |
| `instructions.md`, `prompts.json`, `chat-modes.json` | Agent instructions, saved prompts, default chat modes |
| `routines.json` | Schedules made in the app (assistants' routines, projects' jobs), merged into the jobs |

Every change goes through `config.Edit`: stage the files, load the whole config, run `broker.Check`, then rename into place. It returns the config before and after, so the UI can show what each project gains and loses (`DiffReaches`). Policy feeds the broker, sandbox networks, boards and previews, so access changes restart Skybreaker (`controld.ErrRestart`) once no chat is working. Instructions, prompts and chat modes apply without a restart.

## Sandboxes

Each project gets a long-lived `runsc` container, `skybreaker-<project>`, on its own bridge network, with `HTTPS_PROXY` set to the broker, no DNS and the broker's CA mounted. Starting a sandbox mints its token and revokes earlier ones.

- **Inner Docker.** The sandbox runs its own dockerd. Inner containers follow the same network rules, and a `runc` wrapper mounts the CA into each so they trust the broker.
- **Persistence.** Only `/root`, `/workspace` and inner Docker storage persist. Tools agents install with mise and uv live under `/root`.
- **Setup.** A new container runs the repo's `.skybreaker/setup.sh` after the clone; chats wait for it (`App.WaitWorkspace`).
- **No reads from sandbox paths.** The host never reads workspaces, homes or inner Docker storage, and reads scheduled runs' output through `os.Root`.

## Chats

A chat runs the harness's structured mode through `docker exec -i`:

| Harness | Mode |
|---|---|
| Claude Code | `claude -p --output-format stream-json` |
| Codex | `codex app-server` |
| pi | `skybreaker-pi --mode rpc` |

A `chat.Driver` translates each protocol into `chat.Event`s. The `Manager` stores them in a per-chat log and streams them to the UI over server-sent events. Drivers never name models and ignore what they don't recognise; the weekly image rebuild promotes new CLIs only if they still speak these protocols.

- **MCP.** Terminals, resumes and scheduled runs get Skybreaker's MCP server through the manifest's `{mcp_config}` or `{mcp_url}`.
- **pi.** `skybreaker-pi` merges Skybreaker's MCP server and `pi.providers` (from the read-only `/etc/skybreaker/pi-models.json`) into pi's config inside the sandbox. A pi turn ends at `agent_settled`, since pi can carry on by itself after `agent_end`.
- **Attachments** (`chat/attach.go`). Written into `/root/.skybreaker/attachments/<session>/`, with the path appended to the message. Images within size limits also go to the model as image input. Files untouched for a month are tidied and restored if the chat carries on.
- **Subagents** are tool cards of kind `agent`, with their steps nested by `Tool.Parent`. A chat stays `working` while any subagent runs, so it isn't reaped or stopped early.
- **Instructions** are mounted read-only into each sandbox: `/etc/claude-code/CLAUDE.md` for Claude, `/etc/codex/managed_config.toml` for Codex, `--append-system-prompt` for pi. Saved prompts never reach a sandbox; the chat page expands `/prompt <name>` into an ordinary message.
- **Usage.** Drivers report tokens and plan limits. `usage.Store` keeps them by hour for a month and `/api/usage` serves them.

## Branches

A session on its own branch (`app/worktree.go`) gets a git worktree at `/skybreaker/worktrees/<session>` on `skybreaker/<session>`, from the tip of the project's branch. Its chat, terminal and `/clear`ed successor all run there. A fork gets a new worktree from a snapshot of the original's files.

When every session in a worktree has ended, it's tidied: a clean one is removed, and one with uncommitted work stays for the ten most recently ended, then its work is committed to `refs/skybreaker/wip/<session>`. Every five minutes `EndMerged` ends idle, read chats whose branch has landed, by ancestry, `git cherry` or patch ID (squash merges). An ended chat reopens when sent a message (`ReopenChat`). All of this is git run inside the sandbox.

## Previews

Each project has a preview origin on a port of its own (`store.PreviewPort`), never reused. `preview.Handler` serves it to listed users, proxying to the chosen port on the sandbox (`sandbox.Addr`). It admits only same-origin requests, namespaces cookies per project and strips CORS headers. Listening ports come from the sandbox's `/proc/net/tcp`.

## Terminal client and VS Code

The `skybreaker` client finds the server through the local Tailscale and is known by your tailnet login. It attaches over the same WebSocket as the browser's terminal. Its output goes to a real terminal emulator, so `client.Filter` drops OSC, DCS, APC, PM and SOS strings and C1 controls.

For VS Code, `skybreaker vscode setup` adds a key (`sshkeys.Store`) and a `Host *.skybreaker` block to `~/.ssh/config` whose `ProxyCommand` is `skybreaker ssh-proxy`. That relays ssh over a WebSocket to `/api/projects/{p}/ssh`, then to the sandbox's sshd. Deleting a key cuts its open relays.

## Boards and MCP

`board.Store` keeps every project's items in `board.json`. Skybreaker's MCP server exposes them to agents on the sandbox's hook listener, scoped to their own project, and to you at `/mcp`.

## Assistants

An assistant is a project of kind `assistant`: no repo, so no clone, worktrees, review or `setup.sh`, and its scheduled runs work in `/workspace`.

- **State.** `assistant.Store` keeps each one's look, personality versions, memories, proposals and updates in `assistants.json`, on the host only.
- **Instructions.** `assistant.Instructions` renders who it is, its personality and an index of its newest memories. `app.RefreshInstructions` appends it to every agent's instructions in the project's `etc/` whenever it changes, so Claude, Codex and pi all read it.
- **MCP.** Its own sandbox gets `recall`, `propose_memory`, `propose_routine` and `post_update`. A proposal takes effect only from its card in the app (`/api/assistants/{a}/proposals/{id}/...`).
- **Routines** are jobs in `routines.json`, saved through `config.Edit` like projects. Runs keep their final message (`store.Run.Summary`) for the brief.

**Its browser** (`app.Browser`) is a second container, `skybreaker-<a>-browser`, from the sandbox image, on the assistant's network at its own address (`sandbox.BrowserSpec`). Its profile, `<data>/assistants/<a>/browser`, is mounted there alone. Chromium runs through `docker exec -i` with `--remote-debugging-pipe` on the exec's stdin and stdout, so nothing listens. `browser.Browser`:

- answers the broker's 407 from `Fetch.authRequired` with a broker token of its own, minting another when one stops working;
- trusts the broker's CA by SPKI pin, beside Chromium's own roots;
- checks every request against the deny list in `Fetch.requestPaused`;
- reads pages and checks fields in an isolated world, never returning what a field holds.

`browser.Tools` gives the agent open, read, click, type, scroll, back, screenshot, download, takeover and status. Screenshots and downloads reach `/workspace` through the sandbox (`app.SaveToWorkspace`, `CopyDownload`). `GET /api/assistants/{a}/browser/live` hands the browser to you over a WebSocket (`browser.Live`): JPEG screencast frames out, your input in, the agent's tools paused. It stops after half an hour unused, or with the sandbox.

## Development

`TestUIDev` serves the real server with scripted harnesses and no sandboxes; see [development.md](development.md).
