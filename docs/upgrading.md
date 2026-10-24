# Upgrading

Run these on the Skybreaker host. A restart stops running chats; they pick up again on their next message.

## Upgrade

Check the [release notes](https://github.com/zaneGittins/skybreaker/releases) since your version (`sudo skybreaker-setup status` shows it) for config changes, then:

```sh
sudo skybreaker-setup upgrade
```

It checks your config against the new release, asks before restarting, and puts the old version back if the new one doesn't stay up. If it rejects your config, nothing has changed: edit `/etc/skybreaker/skybreaker.yaml` as the release notes say and run it again.

| Option | Does |
|---|---|
| `--yes` | Don't ask before restarting |
| `--release v0.9.0` | Install a specific release |

## Rebuild the sandbox image

The image rebuilds itself weekly. When the release notes say it changed, rebuild it now (takes several minutes):

```sh
sudo systemctl start skybreaker-sandbox-update
systemctl status skybreaker-sandbox-update     # "promoted" or "unchanged"
```

If the new agent CLIs fail the protocol check, the current image stays. Sandboxes move to the new image once idle for 15 minutes.

## Check it

```sh
sudo skybreaker-setup status
sudo journalctl -u skybreaker -n 50     # look for clone or start errors
```

On each computer with the terminal client, run `skybreaker update`.

## Roll back

```sh
sudo skybreaker-setup rollback
```

This restores the previous version's binary, after checking your config against it. Services and image files stay as they are.

## Installed by hand

From a checkout of the new release:

```sh
make
sudo bin/skybreaker check -config /etc/skybreaker/skybreaker.yaml   # must pass first
sudo install -m 755 bin/skybreaker /usr/local/bin/
sudo install -m 644 deploy/skybreaker.service deploy/skybreaker-nftables.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl restart skybreaker-nftables skybreaker
```

Then refresh the sandbox image. With the [weekly rebuild](manual-install.md#5-weekly-image-rebuild-optional):

```sh
sudo install -m 755 deploy/skybreaker-sandbox-update /usr/local/bin/
sudo cp -r images/sandbox/. /opt/skybreaker/image/
sudo systemctl start skybreaker-sandbox-update
```

Without it: `docker pull ghcr.io/zanegittins/skybreaker-sandbox:latest`, or `make image`.
