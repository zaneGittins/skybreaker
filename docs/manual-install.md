# Install by hand

The steps the [installer](getting-started.md#1-install) runs, from a checkout of this repository. Use this only if you can't use the installer.

## 1. Docker and gVisor

Install [Docker](https://docs.docker.com/engine/install/) and [gVisor](https://gvisor.dev/docs/user_guide/install/), then register gVisor as `runsc`:

```sh
sudo cp deploy/docker-daemon.json /etc/docker/daemon.json   # or merge into yours; keep both runtimeArgs
sudo systemctl restart docker
docker info --format '{{json .Runtimes}}'                   # must list runsc
```

## 2. Build and install

```sh
make
sudo useradd --system --home /var/lib/skybreaker --shell /usr/sbin/nologin --groups docker skybreaker
sudo install -m 755 bin/skybreaker /usr/local/bin/
sudo install -d -m 700 -o skybreaker /etc/skybreaker
docker pull ghcr.io/zanegittins/skybreaker-sandbox:latest   # or: make image
```

## 3. Configure

```sh
sudo cp deploy/skybreaker.example.yaml /etc/skybreaker/skybreaker.yaml
sudo cp deploy/secrets.example.yaml /etc/skybreaker/secrets.yaml
sudo chown skybreaker /etc/skybreaker/*.yaml && sudo chmod 600 /etc/skybreaker/*.yaml
```

In `skybreaker.yaml`, set:

| Key | Value |
|---|---|
| `users` | Tailnet logins allowed in |
| `ui_url` | `https://skybreaker.<your-tailnet>.ts.net` (needs MagicDNS and HTTPS certificates) |
| `broker.netguard.block` | Your LAN ranges, kept from agents |
| `projects` | One per repo, with what it may reach in `allow` and `capabilities` |

In `secrets.yaml`, set `github_app` to your App's ID and key path, and remove the examples you don't use. Then validate both:

```sh
sudo skybreaker check -config /etc/skybreaker/skybreaker.yaml
```

## 4. Start

```sh
echo TS_AUTHKEY=tskey-auth-... | sudo tee /etc/skybreaker/env >/dev/null && sudo chmod 600 /etc/skybreaker/env
sudo install -m 644 deploy/skybreaker.service deploy/skybreaker-nftables.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now skybreaker-nftables skybreaker
```

The auth key is used only on first start. With Tailscale ACLs, allow your login to reach `skybreaker` on port 443.

## 5. Weekly image rebuild (optional)

Rebuilds the sandbox image with the newest agent CLIs, and promotes it only if they pass the protocol check.

```sh
sudo install -m 755 deploy/skybreaker-sandbox-update /usr/local/bin/
sudo mkdir -p /opt/skybreaker/image && sudo cp -r images/sandbox/. /opt/skybreaker/image/
sudo install -m 644 deploy/skybreaker-sandbox-update.service deploy/skybreaker-sandbox-update.timer /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now skybreaker-sandbox-update.timer
```
