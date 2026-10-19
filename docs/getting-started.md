# Getting started

## Requirements

- **A Linux host** with Docker. A VM is fine; allow about 4 GB of memory and a few cores per active project.
- **A Tailscale tailnet.** Skybreaker joins it as its own machine, `skybreaker`.
- **A GitHub App** installed on the repos agents will work on (see [GitHub](#github)).

gVisor is required, and the installer adds it if it's missing.

## Install

```sh
curl -fsSL https://github.com/zaneGittins/skybreaker/releases/latest/download/install.sh | sh
```

The installer asks for your tailnet login, a Tailscale auth key (admin console → Settings → Keys), the GitHub App's ID and private key, and your first repo. It then installs gVisor and nftables, writes the config to `/etc/skybreaker`, validates it, installs the systemd units and sandbox image, and starts Skybreaker. If a step fails, fix the cause and run `sudo skybreaker-setup install` again; it picks up where it stopped.

For unattended installs:

```sh
sudo TS_AUTHKEY=tskey-auth-... skybreaker-setup install --yes \
  --login you@github --tailnet tail1234.ts.net --app-id 123456 --app-key app.pem --repo you/project
```

To install a specific version or prerelease, pass it to the script: `... | SKYBREAKER_VERSION=v0.1.0 sh`.

To install without the wizard, see [Install by hand](manual-install.md).

## GitHub

- **App permissions:** `contents:write`, `pull_requests:write`, `issues:write` and `metadata:read`. Never `workflows`, `administration` or `secrets`.
- **Ruleset on `main`:** block direct pushes, force pushes and deletion, and keep the App off the bypass list.
- **Branches:** the broker only allows pushes to the branch globs in the project's rules (`feature/*`, `fix/*`, `docs/*` and `chore/*` by default), and refuses deletes and tags.

## First steps

Open `https://skybreaker.<your-tailnet>.ts.net`, tap **+** and pick Claude or Codex.

- **Sign in** once per project. For Claude, open the chat's terminal and run `/login`; Codex shows a sign-in code in the chat. Logins persist across restarts.
- **Notifications:** on a phone, add Skybreaker to the home screen, open it from there and tap the bell.
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

## Configuration

Everything lives in `/etc/skybreaker/skybreaker.yaml` (projects, policy, users) and `secrets.yaml`. Each project lists what its sandbox may reach in `allow` and `capabilities`; anything else is denied. Restart `skybreaker` after editing, and `skybreaker-nftables` too after adding a project. [deploy/skybreaker.example.yaml](../deploy/skybreaker.example.yaml) is a commented example.

## Updating

```sh
sudo skybreaker-setup upgrade     # install the latest release
sudo skybreaker-setup rollback    # return to the previous version
sudo skybreaker-setup status
```

`upgrade` checks your config against the new version before changing anything, and restores the previous version if the new one doesn't stay up. Restarting stops running chats, so it asks first.
