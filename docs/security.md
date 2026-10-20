# Security model

Coding agents read untrusted input all day (issues, web pages, dependencies), and any of it can steer them. Skybreaker treats every agent as potentially hostile.

## Principles

1. **Agents never hold credentials.** The broker adds them to outgoing requests, and only for the hosts each secret is bound to.
2. **Standard tools draw the boundary.** gVisor, nftables and Tailscale contain the agents; Skybreaker's own code decides policy within it.
3. **Deny by default.** An unmatched rule, unknown host, missing token or unbound secret is a denial.
4. **Brokering prevents theft, not misuse.** Narrow token scopes, GitHub rulesets and approvals limit what an agent can do with access it legitimately has.

## Isolation

Each project runs in its own gVisor (`runsc`) sandbox with the repo, toolchains, harness CLIs and an inner dockerd. Skybreaker, the broker's CA key, secrets, policy and the audit log stay on the host.

- **gVisor.** An escape needs a gVisor bug and then a host-kernel bug. Skybreaker refuses to start without `runsc`.
- **Network.** Sandboxes have no DNS. nftables lets them reach only the broker and the hook listener on their bridge gateway; nothing is forwarded. Inner containers follow the same rules.
- **SSH.** sshd in each sandbox accepts public keys only, and is reachable only through Skybreaker's relay for listed users. Removing a key cuts every open relay. The client forwards no agent or X11.
- **Your terminal.** The terminal client strips escape strings (OSC, DCS, APC, PM, SOS, C1) from sandbox output, so an agent can't write your clipboard or plant links.

## The broker

Sandboxes reach the outside only through `HTTPS_PROXY=http://<project>:<token>@broker:8080`.

| Check | Rule |
|---|---|
| Caller | From the project's sandbox network, with a live token for that project. Revocation applies to open connections too. |
| Destination | On the project's list, as a **tunnel** (opaque TLS, server name must match), **mitm** (TLS terminated so rules see method and path) or **route** (a reverse-proxy path). Plain HTTP only to a route's `allow_private` address, never with a secret. |
| Request | Each method and path is `auto`, `ask` or `deny`. Requests carrying their own credentials are refused. `refs:`, `graphql:`, `k8s:` and `content_rules` narrow git pushes, GraphQL operations, Kubernetes verbs, and commit and pull request text. A read rule's `follow_redirects` lists the hosts the broker may follow the upstream's redirects to (GitHub's log storage), fetching them itself over https to a public address with no credentials, so the sandbox never reaches them; it's set in the config file only, and each hop is audited. |
| Credentials | A secret is added only if it lists the upstream host, so it can't be echoed back from elsewhere. |
| Connection | Resolved addresses are checked against loopback, link-local, metadata, private ranges, the tailnet and your LAN. |
| Audit | Every decision is logged and shown on the Activity page. |

## Approvals

An `ask` request is held for up to 30 seconds; after that the agent gets a 403 with an approval ID, and an identical retry succeeds once approved. Approvals are single-use and bound to a hash of the exact request, headers and body included. The card shows the broker's own parse of the request, with hidden characters revealed. Only logins in `users` can answer them.

Harness approvals (whether Claude may run a command inside its sandbox) are a convenience, not a security boundary.

## The app

Sandboxes can't reach the app, so the risks are a hijacked session or a listed user who shouldn't make changes.

- **Settings only add.** The app writes to the data directory, never the config file. It can add exact hosts, rules, secrets, bundles and projects; routes, `allow_private`, CA files, Kubernetes rules and `admins` stay file-only. `app_settings: false` turns it off.
- **Secrets keep their hosts.** A secret's hosts change only when its value is pasted again. Values are never shown.
- **Changes are checked and audited.** Each is compiled against the whole config before saving, shown as what each project gains and loses, and logged by who made it.
- **Previews** run on a separate origin per project, for listed users only, with a CSP limiting the page to its own server and cookies signed by Skybreaker.
- **Boards.** An agent reads and writes only its own project's board, and can't mark items Done or take over claimed items.

## Protecting `main`

- **A GitHub ruleset** blocks direct pushes, force pushes and deletion, with the App off its bypass list. This is the backstop.
- **The broker** refuses pushes outside the allowed branch globs, deletes and tags, and checks commit messages and pull request text against `content_rules`.
- **The GitHub App** gets only `contents`, `pull_requests` and `issues` (write) and `metadata`, `actions`, `checks` and `statuses` (read). Never `workflows`, `administration` or `secrets`.
- **CI can read back what it prints.** Agents can push code that CI runs and then read its logs, so keep deploy and release secrets in protected environments that pushed branches don't get. Artifact downloads wait for your approval.

## Limits

- **One host.** The broker shares a host with the sandboxes, so a sandbox escape reaches its secrets. For production credentials, use the narrowest tokens, make writes `ask`, or use a separate machine.
- **Allowed channels can leak data.** An agent can put data in a branch, a PR body or a model prompt. Keep allow lists tight and review before merging.
- **Harness logins live in the sandbox.** Subscription tokens are readable by the agent. Use broker-injected API keys (`llm-anthropic`) where that matters.
- **`auto` rules are trusted.** Anything auto-approved, a hostile agent can do. Make destructive actions `ask` or `deny`.
- **Previews can navigate away.** A page can still send data in a URL by navigating, or over WebRTC. Close previews you don't trust.
- **Content rules don't cover** tunnelled hosts, commits made through GitHub's API, or issues and comments.

## Response and testing

Revoke a sandbox's token on the Activity page, or use the kill switch to cut off all of them. A restart issues fresh tokens.

`scripts/redteam.sh` runs inside a sandbox and attempts the obvious escapes: environment credentials, pushing to `main`, unlisted hosts, DNS, the LAN, the tailnet, cloud metadata and foreign API keys. Each must fail and appear in the audit log.
