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

1. **Caller.** The connection must come from a sandbox network, carrying a live token minted for that project. Tokens are rechecked on every request inside MITM connections, and revoked tokens' connections are closed.
2. **Destination.** The host must be in the project's `allow` list or capabilities. A **tunnel** splices bytes, **mitm** terminates TLS with the broker's CA, and a **route** is a reverse-proxy path on the broker.
3. **Rules.** The first matching rule decides `auto`, `ask` or `deny`; no match is a denial. Requests with their own credentials, impersonation headers, ambiguous paths (`..`, `//`, encoded slashes) and protocol upgrades are refused first. GraphQL rules parse the operation (`internal/proxy/graphql`) and Kubernetes routes the verb and resource. Content rules read pushed commits from the pack as it streams (`gitproto.ReadPush`, gated in `server/content.go`) and pull request titles and bodies from REST or GraphQL. An `ask` is bound to a hash of the exact request, headers included, and held for approval.
4. **Credentials.** The token is checked again (it may have been revoked while held), then the broker strips placeholders and adds the secret, only if the secret lists the upstream host. Revoking a token also cancels its requests still in flight.
5. **Connect.** Resolved addresses are checked against loopback, link-local, metadata, private and tailnet ranges. Tailnet upstreams a route allows are dialled through Skybreaker's own Tailscale node.

## Projects

The config file is the base; the app adds to it from three files in `<data_dir>/settings` (`config.Paths`; the service can't write `/etc`): `projects.json` (projects created in the app), `access.json` (bundles made in Settings, and bundles and hosts added to any project) and `app-secrets.json` (secrets added in Settings, and the GitHub App created there, merged with the secrets file by `secrets.Merge`). Every change goes through `config.Edit`: it stages the files, loads the whole configuration from them, runs the broker's check (`broker.Check`) and only then renames them into place, returning the configuration before and after so the API can show what each project gains and loses (`ProjectPolicy.Reaches` and `DiffReaches` in `proxy/config`). `api/settings.go` serves the Settings page and the GitHub manifest flow (`githubapp.Manifest`, `Convert`); `controld`'s `settings` saves, audits (`broker.LogConfig`) and asks for a restart. Projects and policy fan out into the broker's compiled policy and secrets, sandbox networks, boards and previews, so Skybreaker restarts itself to apply a change (`controld.ErrRestart`, re-executed by `main`), once no chat is working. The nftables ruleset covers the whole sandbox range, so a new project needs no new rules.

## Sandboxes

Each project gets a long-lived `runsc` container, `skybreaker-<project>`, on its own bridge network. Its environment points `HTTPS_PROXY` at the broker on the bridge gateway, with no DNS and the broker's CA mounted. Starting a sandbox mints its token and revokes earlier ones.

The sandbox runs its own dockerd for builds and `docker compose`, storing images in the project's `docker/` directory on disk. Inner containers have no DNS either and reach the broker through the sandbox. A `runc` wrapper mounts the sandbox's CA bundle into every container and build step, so they trust the broker without image changes.

A new container runs the repository's `.skybreaker/setup.sh` after the clone, in the same background run (`App.WaitWorkspace`), so chats and scheduled runs start once it's done. Only `/root`, `/workspace` and inner Docker storage persist, so the image puts what agents install with mise and uv (under `/root`) last on `PATH`, behind the harnesses' own tools.

The host never reads sandbox-writable paths directly: workspaces, homes and inner Docker storage are never read, and scheduled runs' output is read through `os.Root`.

## Chats

A chat runs the harness's structured mode (`claude -p --output-format stream-json`, `codex app-server`, `skybreaker-pi --mode rpc`) through `docker exec -i`. A `chat.Driver` translates each protocol into `chat.Event`s; the `Manager` stores them in a per-chat log and streams them to the UI over server-sent events. "Open in terminal" resumes the same conversation in tmux. Terminals, resumes and scheduled runs get Skybreaker's MCP server through their manifest's `{mcp_config}` (Claude's and pi's config file) or `{mcp_url}` (Codex's `-c mcp_servers.skybreaker` override; a Codex run also approves its tools, since `codex exec` can't ask). A job setting its harness's headless command has no place for (`budget_usd`, `allowed_tools`) is listed as ignored on the Runs tab, and a board job on a harness given no MCP server fails each run saying why; neither stops Skybreaker starting. Drivers never name models and ignore what they don't recognise; a weekly image rebuild promotes new CLI versions only if they still speak the protocols the drivers use. pi runs in chats, terminals and jobs through `skybreaker-pi` (`images/sandbox`), which merges Skybreaker's MCP server and the model servers in `pi.providers` (`config.PiConfig`, written to the sandbox's read-only `/etc/skybreaker/pi-models.json`) into pi's own config inside the sandbox; the host never writes into a sandbox's home. That config is shared by every pi in the sandbox, so its Skybreaker server names the calling chat in an `X-Skybreaker-Session` header from `$SKYBREAKER_SESSION` rather than in the URL. A pi turn ends at `agent_settled`, not `agent_end`, since pi can carry on by itself after a run (retries, compaction, queued messages); extensions' dialogs become asks.

A message can carry attachments (`chat/attach.go`). The app starts the sandbox if it isn't running and writes each file through `docker exec -i` to `/root/.skybreaker/attachments/<session>/`, in the home that outlives the container, tidying files older than a month as it goes; only then does the `Manager` keep copies in the chat's storage, on a shelf with its own quota beside the files agents show. The harness gets the message with each file's path appended, and images as image input as well, each up to the 3.75 MB that Claude's API takes as base64 and 12 MB a message: content blocks for Claude, data URLs for Codex, `images` for pi. A model without image input still has the path. The web app scales images down to 2000 pixels and 3.75 MB before sending.

A session can work on its own branch (`app/worktree.go`): a git worktree in the sandbox's `/skybreaker/worktrees/<session>`, mounted from the project's `worktrees/` directory, on `skybreaker/<session>` from the tip of the project's branch. It's made on the first start, and the chat, its terminal and a `/clear`ed successor all run there; Changes and `show_file` read from it. A fork gets a new worktree from a snapshot of the original's files, uncommitted work included, made with a separate index so the original is untouched. Once every session in a worktree has ended it's tidied: a clean one is removed (and its branch, if nothing was committed); one with uncommitted work or ignored files stays for the ten most recently ended, and past that its work is committed to `refs/skybreaker/wip/<session>` first, where a fork or a carried-on chat finds it again. A detached HEAD is kept on the session's branch. Anything tidying can't check, it leaves for its next pass. All of this is git run in the sandbox.

Subagents are tool cards of kind `agent`, their steps nested under them by `Tool.Parent`, and a card stays `running` until the subagent ends, which can be after the turn that started it: Claude's background agents end with a `task_notification`, and Codex's run in threads of their own on the same app-server connection, which the driver follows by thread ID, so their messages and turns never pass for the main thread's. pi has none built in; an extension's subagent tool (named `subagent`, `agent` or `task`) is a card that runs until the tool returns, its progress shown as the card's output rather than as nested steps. The `Manager` keeps a chat `working` while any subagent card runs (the inbox says how many), so it isn't reaped, its sandbox isn't stopped and "finished" is pushed once the last one ends; a harness that exits, or a restart, ends the cards it left running.

## Previews

Each project has a preview origin, a port of Skybreaker's tailnet name assigned once (`store.PreviewPort`) and never reused for another project. `preview.Handler` serves it to listed users, proxying to the port you chose on the sandbox's fixed address (`sandbox.Addr`). It admits only its own origin's requests and navigations, namespaces cookies per project and strips CORS headers. The dev server sees itself addressed as `localhost`. The app lists the sandbox's listening ports from its `/proc/net/tcp`.

## Terminal client

Run without a server command, `skybreaker` is a client for your own machine. It finds the server through the local Tailscale and is known to it by your tailnet login, like the browser. It lists sessions from the inbox and attaches over the same WebSocket as the browser's terminal, switching a chat to its terminal first. A sandbox's bytes reach a real terminal emulator here, not xterm.js, so `client.Filter` drops every OSC, DCS, APC, PM and SOS string and C1 control before writing them.

## VS Code over SSH

Each sandbox runs sshd. `skybreaker vscode setup` makes the computer a key, adds it through `POST /api/ssh-keys` (`sshkeys.Store`, which rewrites it in canonical form) and puts a `Host *.skybreaker` block first in `~/.ssh/config`, whose `ProxyCommand` is `skybreaker ssh-proxy`. That carries ssh's bytes over a WebSocket to `/api/projects/{p}/ssh`, which starts the sandbox and relays to its sshd on `sandbox.Addr:22`. `app.SetAuthorizedKeys` writes the keys into every project's `etc/`, and sshd reads them on each login through `AuthorizedKeysCommand`. Deleting a key cuts the open relays.

## Instructions and usage

Every agent's instructions are `settings/instructions.md`, set in Settings by admins (`PUT /api/instructions`, audited). `app.SetInstructions` writes them into each project's `etc/` as `instructions.md` and as `codex-managed-config.toml` (`developer_instructions`), each mounted read-only on its own: at `/etc/claude-code/CLAUDE.md` for Claude Code and `/etc/codex/managed_config.toml` for Codex. They're rewritten in place, since a rename would leave the mount on the old file. `skybreaker-pi` passes the file to pi with `--append-system-prompt`.

Canned prompts are `settings/prompts.json`, set in Settings by admins (`PUT /api/prompts`, audited) and read by anyone (`GET /api/prompts`). They never reach a sandbox: the chat page expands `/prompt <name>` into the prompt's text and sends it as an ordinary message, so no harness needs to know about them.

The chat drivers report tokens (`Out.Usage`: Claude's per-model result usage as it grows, Codex's `thread/tokenUsage/updated`) and plan limits (`Out.Limits`: Claude's `rate_limit_event`, Codex's `account/rateLimits/read` and updates). `usage.Store` keeps them by UTC hour for a month in `usage.json`, with scheduled runs' totals from their logs, and `/api/usage` serves the home page's rail.

## Boards and MCP

`board.Store` keeps every project's items in `board.json`. Skybreaker's MCP server exposes them to agents on the sandbox's hook listener, identified by network and hook token and scoped to their own project, and to you at `/mcp` on the app.

## Development

`TestUIDev` serves the real server with scripted harnesses and no sandboxes; see [development.md](development.md).
