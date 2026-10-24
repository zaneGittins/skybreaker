# Development

## Layout

```
cmd/skybreaker           the one binary
internal/proxy/...       the broker: policy, MITM, approvals, secrets, audit
internal/control/...     everything else: sandboxes, chats, sessions, hooks, scheduler, UI API
internal/notify/...      ntfy and web push
web/                     the UI, embedded (no build step)
deploy/                  example configs, systemd units, docker daemon.json, the sandbox updater
images/sandbox/          the sandbox image
scripts/redteam.sh       escape and exfiltration checks, run inside a sandbox
```

[ARCHITECTURE.md](ARCHITECTURE.md) has the full code map.

## Build

```sh
make               # vet, test, build: bin/skybreaker and bin/skybreaker-setup
```

Go fetches the modules pinned in `go.mod`, and xterm.js is in the repository; `make deps` and `make vendor` are for bumping them.

## Tests

```sh
go test -race ./...           # everything, plus the JS tests when node is installed
go vet ./... && gofmt -l .
go test -tags nodeps ./...    # without the tsnet and YAML modules
(cd setup && go vet ./... && go test -race ./...)   # the installer, its own module
```

Work test-first. The broker's tests run real TLS end to end with a broker CA, fake upstreams on a separate "public" CA and a real client going through the proxy. The chat drivers replay recordings of the real CLIs from `internal/control/chat/testdata`; when a harness changes its protocol, record a session there first, then fix the driver against it.

## Run it locally

Skybreaker has a loopback-only development mode (`tailscale.disabled: true`). It trusts every local caller as the first user, so the UI refuses non-loopback addresses. It still needs Docker with gVisor.

Copy [deploy/skybreaker.example.yaml](../deploy/skybreaker.example.yaml) to `skybreaker.dev.yaml` (ignored by git), set `users` to one login, keep a project or two, and replace its `tailscale` block with:

```yaml
tailscale: { disabled: true, dev_listen: "127.0.0.1:7700" }
```

```sh
bin/skybreaker serve -config skybreaker.dev.yaml       # http://127.0.0.1:7700
```

## Work on the UI without a sandbox

This serves the real control plane, chat manager and drivers with a scripted Docker and scripted Claude and Codex:

```sh
SKYBREAKER_UIDEV=127.0.0.1:8099 go test -run TestUIDev -timeout 0 ./internal/control/controld/
```

Open http://localhost:8099. Messages to Claude get a scripted turn with tools and a diff; one containing "approve" asks for approval, "ask" asks a question and "fail" fails. Codex asks you to sign in with a device code. Every few minutes skybreaker's sandbox asks to merge a pull request, through the real broker, so there's a network approval to look at.

## Rules worth keeping

[security.md](security.md) sets them out: deny by default, secrets bound to hosts in two places, gVisor or nothing, agent text into the UI only as text, no host git in agent worktrees.
