# Security policy

## Reporting a vulnerability

Please report vulnerabilities privately through GitHub: open the repository's **Security** tab and choose **Report a vulnerability**. Don't open a public issue.

Include the version (`skybreaker version`), what an attacker needs (for example, code running in a sandbox), and steps to reproduce. You'll get an acknowledgement, and a fix is released as soon as it's ready, with credit if you'd like it.

## Supported versions

Only the latest release receives security fixes.

## Scope

The [security model](docs/security.md) describes what Skybreaker defends. In scope are any ways to break it, for example:

- reaching the host, the broker's secrets or another project from inside a sandbox;
- getting a request past the broker's policy, or a credential sent to a host it isn't bound to;
- approving, replaying or disguising a request without a listed user's approval;
- using the app or its API without being a listed user, or injecting script into it;
- getting escape sequences from a sandbox past the terminal client's filter.

Out of scope are the limits that document already lists (data leaving through allowed channels, harness logins inside the sandbox, actions an `auto` rule permits), and bugs in gVisor, Docker or Tailscale themselves, which should go to those projects.
