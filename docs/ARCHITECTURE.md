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
| `internal/control/chat` | Harness drivers (Claude stream-json, Codex app-server), event log, process manager. |
| `internal/control/api` | The UI's HTTP API and server-sent events. |
| `internal/control/sandbox`, `netrules` | Container specs under `runsc`, and the nftables ruleset. |
| `internal/control/mcp`, `internal/board` | Skybreaker's MCP server and the per-project boards. |
| `internal/control/scheduler`, `hooks`, `terminal` | Scheduled runs, Claude Code hooks, WebSocket terminals. |
| `internal/proxy/broker` | The broker, assembled in-process. |
| `internal/proxy/server` | The request path: tunnel, MITM and routes. |
| `internal/proxy/policy`, `gitproto`, `k8s` | Rule matching, git push ref checks, Kubernetes attributes. |
| `internal/proxy/approval`, `secrets`, `netguard`, `audit` | Approvals, host-bound secrets, connect-time address checks, audit log. |
| `internal/tsauth` | Tailnet identity; `tsnetsrv` is the Tailscale adapter. |
| `internal/client` | The terminal client: session picker, attach, self-update. |
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
3. **Rules.** The first matching rule decides `auto`, `ask` or `deny`; no match is a denial. Requests with their own credentials, ambiguous paths (`..`, `//`, encoded slashes) and protocol upgrades are refused first. An `ask` is bound to a hash of the exact request and held for approval.
4. **Credentials.** The broker strips placeholders and adds the secret, only if the secret lists the upstream host.
5. **Connect.** Resolved addresses are checked against loopback, link-local, metadata, private and tailnet ranges. Tailnet upstreams a route allows are dialled through Skybreaker's own Tailscale node.

## Sandboxes

Each project gets a long-lived `runsc` container, `skybreaker-<project>`, on its own bridge network. Its environment points `HTTPS_PROXY` at the broker on the bridge gateway, with no DNS and the broker's CA mounted. Starting a sandbox mints its token and revokes earlier ones.

The sandbox runs its own dockerd for builds and `docker compose`, storing images in the project's `docker/` directory on disk. Inner containers have no DNS either and reach the broker through the sandbox. A `runc` wrapper mounts the sandbox's CA bundle into every container and build step, so they trust the broker without image changes.

The host never reads sandbox-writable paths directly: workspaces, homes and inner Docker storage are never read, and scheduled runs' output is read through `os.Root`.

## Chats

A chat runs the harness's structured mode (`claude -p --output-format stream-json`, `codex app-server`) through `docker exec -i`. A `chat.Driver` translates each protocol into `chat.Event`s; the `Manager` stores them in a per-chat log and streams them to the UI over server-sent events. "Open in terminal" resumes the same conversation in tmux. Drivers never name models and ignore what they don't recognise; a weekly image rebuild promotes new CLI versions only if they still speak the protocols the drivers use.

## Terminal client

Run without a server command, `skybreaker` is a client for your own machine. It finds the server through the local Tailscale and is known to it by your tailnet login, like the browser. It lists sessions from the inbox and attaches over the same WebSocket as the browser's terminal, switching a chat to its terminal first. A sandbox's bytes reach a real terminal emulator here, not xterm.js, so `client.Filter` drops every OSC, DCS, APC, PM and SOS string and C1 control before writing them.

## Boards and MCP

`board.Store` keeps every project's items in `board.json`. Skybreaker's MCP server exposes them to agents on the sandbox's hook listener, identified by network and hook token and scoped to their own project, and to you at `/mcp` on the app.

## Development

`TestUIDev` serves the real server with scripted harnesses and no sandboxes; see [development.md](development.md).
