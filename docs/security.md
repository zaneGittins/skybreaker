# Security model

Coding agents read untrusted input all day (issues, web pages, dependencies), and any of it can steer them. Skybreaker therefore treats every agent as potentially hostile and asks of each design decision: what could a hostile agent do from here?

## Principles

1. **Agents never hold credentials.** The broker adds them to outgoing requests, and only for the hosts each secret is bound to.
2. **Standard tools draw the boundary.** gVisor, nftables and Tailscale contain the agents; Skybreaker's own code decides policy within that boundary.
3. **Deny by default.** An unmatched rule, unknown host, missing token or unbound secret is a denial.
4. **Brokering prevents theft, not misuse.** Narrow token scopes, GitHub rulesets and approvals limit what an agent can do with access it legitimately has.

## Isolation

Each project runs in its own gVisor (`runsc`) sandbox, holding the repo, toolchains, harness CLIs and an inner dockerd. Skybreaker, the broker's CA key, secrets, policy and the audit log stay on the host, outside every sandbox.

- **gVisor** implements the Linux kernel in user space, so an escape requires a gVisor bug and then a host-kernel bug. Skybreaker refuses to start without `runsc` and never runs a sandbox under another runtime.
- **Network.** Sandboxes have no DNS, and nftables lets them reach only two ports on their bridge gateway: the broker and the hook listener. Nothing is forwarded, and those ports refuse non-sandbox sources.
- **Inner containers** (builds, `docker compose`) follow the same rules: no DNS, the broker as their only way out, seen by it as the sandbox.

**The single-host trade-off.** The broker shares a host with the sandboxes, so a sandbox escape reaches its secrets. For most setups the simpler deployment is worth it: the realistic threats (misuse of allowed access, a malicious pull request) are the same either way. If you give the broker production credentials, use the narrowest tokens available, make writes `ask`, or keep production on a separate machine.

## The broker

Sandboxes reach the outside only through `HTTPS_PROXY=http://<project>:<token>@broker:8080`.

| Check | Rule |
|---|---|
| Caller | The request must come from the project's sandbox network with a live token minted for that project. Revocation takes effect immediately, including on open connections. |
| Destination | The host must be on the project's list. Each host is a **tunnel** (opaque), **mitm** (TLS terminated so rules can see method and path) or a **route** (a reverse-proxy path, for clients that pin their own CA). |
| Request | Each method and path is `auto`, `ask` or `deny`; unmatched requests are denied. Requests carrying their own credentials are refused. `refs:` restricts git pushes to branch globs; `k8s:` rules decide on Kubernetes verbs and resources. |
| Credentials | A secret is added only if it lists the upstream host. An unbound secret would let an agent read it back through an echo server. |
| Connection | Resolved addresses are checked at connect time against loopback, link-local, metadata, private ranges, the tailnet and your LAN. |
| Audit | Every decision is logged and shown on the Activity page. |

## Approvals

An `ask` request is held for up to 30 seconds. If it isn't approved in time, the agent receives a 403 with an approval ID, and an identical retry succeeds once approved.

- Approvals are bound to a hash of the exact request (project, method, host, path, query, port, route, secret, content type and body) and are single-use.
- The card shows the broker's own parse of the request, never the agent's description. Characters that could hide or reorder text (bidi, zero-width, controls) are shown as `<U+XXXX>`.
- Only the logins in `users` can view or answer approvals, and sandboxes cannot reach the UI.

Harness approvals (whether Claude may run a command inside its sandbox) are a convenience, not a security boundary.

## Boards

Agents reach their project's board through Skybreaker's MCP server, identified by sandbox network and hook token. An agent can read and write only its own project's board, so it can't plant instructions for other projects' agents. It can't mark items Done or take over claimed items. Scheduled runs are told to treat item text as a task description, not as instructions.

## Protecting `main`

- **A GitHub ruleset** blocks direct pushes, force pushes and deletion, with the App off its bypass list. This is the backstop.
- **The broker** refuses pushes outside the allowed branch globs, deletes and tags. It can't see history, so force pushes rely on the ruleset.
- **The GitHub App** is never granted `workflows`, `administration` or `secrets`.

## Limits

- **Allowed channels can leak data.** An agent can put data in a pushed branch, a PR body or a model prompt. Keep allow lists tight and review branches before merging.
- **Harness logins live in the sandbox.** A hostile agent can read Claude and ChatGPT subscription tokens. Where that matters, use broker-injected API keys (the `llm-anthropic` capability).
- **`auto` rules are trusted.** Anything a rule auto-approves, a hostile agent can do. Make destructive actions `ask` or `deny`.

## Response and testing

The Activity page lists each sandbox's token: revoke one to cut that sandbox off, or use the kill switch to cut off all of them. A restart issues a fresh token.

`scripts/redteam.sh` runs inside a sandbox and attempts the obvious escapes: credentials in the environment, pushing to `main` or another repo, unlisted hosts, DNS, the LAN, the tailnet, cloud metadata, other host ports and foreign API keys. Each must fail and appear in the audit log.
