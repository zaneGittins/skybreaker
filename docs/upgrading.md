# Upgrading

Run these on the Skybreaker host. Restarting stops running chats; they pick up again on their next message.

## 1. Read the release notes

Check the [releases](https://github.com/zaneGittins/skybreaker/releases) between your version and the latest. Each one says what to change in your config, if anything, and whether the sandbox image changed.

```sh
sudo skybreaker-setup status      # shows the installed version
```

## 2. Upgrade Skybreaker

```sh
sudo skybreaker-setup upgrade
```

This updates `skybreaker-setup` itself, downloads the release, checks your config against it, asks before restarting, then restarts. If the new version doesn't stay up, it puts the old one back.

If it says the release rejects your config, nothing was changed. Edit `/etc/skybreaker/skybreaker.yaml` as the release notes say and run `upgrade` again.

To skip the question, add `--yes`. To install a particular release, add `--release v0.9.0`.

## 3. Rebuild the sandbox image

```sh
sudo systemctl start skybreaker-sandbox-update
systemctl status skybreaker-sandbox-update     # "promoted" or "unchanged"
```

This builds the image from the release's files with the newest agent CLIs, and returns when it's done, after several minutes. It runs weekly by itself; run it now when the release notes say the image changed. If the agents' CLIs fail the check, the current image stays.

Sandboxes move onto the new image by themselves once idle (no open terminal, scheduled run or chat in the last 15 minutes).

## 4. Check it

```sh
sudo skybreaker-setup status
sudo journalctl -u skybreaker -n 50     # look for clone or start errors
```

## 5. Update the terminal client

On each machine where you use the `skybreaker` client:

```sh
skybreaker update
```

## Rolling back

```sh
sudo skybreaker-setup rollback
```

This goes back to the version before the last upgrade, after checking your config against it. It swaps the binary only; the services and the image's files stay as the upgrade left them.

## Installed by hand

From a checkout of the new release:

```sh
make
sudo bin/skybreaker check -config /etc/skybreaker/skybreaker.yaml   # must pass before you go on
sudo install -m 755 bin/skybreaker /usr/local/bin/
sudo install -m 644 deploy/skybreaker.service deploy/skybreaker-nftables.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl restart skybreaker-nftables skybreaker
```

Then the sandbox image. With the weekly rebuild ([step 5](manual-install.md#5-weekly-image-rebuild-optional)), refresh its files and run it:

```sh
sudo install -m 755 deploy/skybreaker-sandbox-update /usr/local/bin/
sudo cp -r images/sandbox/. /opt/skybreaker/image/
sudo systemctl start skybreaker-sandbox-update
```

Without it, `docker pull ghcr.io/zanegittins/skybreaker-sandbox:latest`, or `make image`.
