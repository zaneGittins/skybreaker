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
- **In a chat**, `/rename <title>` names it, `/color <colour>` tints it to tell it apart, and `/goal <text>` pins a goal and gives it to the agent. Other slash commands go to the agent as usual.
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

The project is in `/workspace`. A VS Code window is one more shell in the sandbox: it reaches only what the project's policy allows, through the broker. The sandbox can't download VS Code's server itself, so Remote - SSH downloads it on your computer and copies it over; set `"remote.downloadExtensionsLocally": true` in VS Code to install extensions the same way. Remove a computer's key in the same menu; its open windows disconnect.

## Previews

To see what an agent is building, open the project's menu (or **Preview** on a desktop) and pick a port the sandbox is listening on. The page opens on its own address, `https://skybreaker.<your-tailnet>.ts.net:84xx`, apart from Skybreaker. Dev servers must listen on `0.0.0.0` rather than `localhost` (`vite --host 0.0.0.0`, `next dev -H 0.0.0.0`). If you use Tailscale ACLs, allow your login ports 8401-8499 on the `skybreaker` machine as well as 443. Agents can open a preview themselves with Skybreaker's `preview` tool, which puts a link in the chat.

A preview page loads only from its own server, so fonts or scripts from CDNs don't load there. Its cookies are `HttpOnly`, so frameworks that read a CSRF cookie from script (Django's `csrftoken`, `XSRF-TOKEN`) need it sent another way while previewed.

## Configuration

Everything lives in `/etc/skybreaker/skybreaker.yaml` (projects, policy, users) and `secrets.yaml`. Each project lists what its sandbox may reach in `allow` and `capabilities`; anything else is denied. Restart `skybreaker` after editing. Projects can also be created in the app (**New project** on the home page), from the capability bundles this file defines; those are kept in `projects.json` next to it. [deploy/skybreaker.example.yaml](../deploy/skybreaker.example.yaml) is a commented example.

## Updating

```sh
sudo skybreaker-setup upgrade     # install the latest release
sudo skybreaker-setup rollback    # return to the previous version
sudo skybreaker-setup status
```

`upgrade` checks your config against the new version before changing anything, and restores the previous version if the new one doesn't stay up. Restarting stops running chats, so it asks first.
