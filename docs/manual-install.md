# Install by hand

These are the steps `skybreaker-setup` performs, run from a checkout of this repository. Most installs should use the [installer](getting-started.md#install) instead.

## 1. Docker and gVisor

Install Docker, then gVisor from [gvisor.dev](https://gvisor.dev/docs/user_guide/install/). Register it as `runsc` with [deploy/docker-daemon.json](../deploy/docker-daemon.json); the sandboxes' inner dockerd needs its two `runtimeArgs`.

```sh
sudo cp deploy/docker-daemon.json /etc/docker/daemon.json   # or merge it into yours
sudo systemctl restart docker
docker info --format '{{json .Runtimes}}'                   # must list runsc
```

## 2. Build and install

```sh
make deps vendor && make
sudo useradd --system --home /var/lib/skybreaker --shell /usr/sbin/nologin --groups docker skybreaker
sudo install -m 755 bin/skybreaker /usr/local/bin/
sudo install -d -m 700 -o skybreaker /etc/skybreaker
docker pull ghcr.io/zanegittins/skybreaker-sandbox:latest   # or build it: make image
```

## 3. Configure

```sh
sudo cp deploy/skybreaker.example.yaml /etc/skybreaker/skybreaker.yaml
sudo cp deploy/secrets.example.yaml /etc/skybreaker/secrets.yaml
sudo chown skybreaker /etc/skybreaker/*.yaml && sudo chmod 600 /etc/skybreaker/*.yaml
```

In `skybreaker.yaml`, set:

- `users`: the tailnet logins allowed to use Skybreaker and answer approvals.
- `ui_url`: `https://skybreaker.<your-tailnet>.ts.net`.
- `broker.netguard.block`: your LAN, so agents can't reach it through the broker.
- `projects`: one per repo, with what its sandbox may reach in `allow` and `capabilities`.

In `secrets.yaml`, keep `github_app` with your App's ID and key path, and remove the examples you don't use. `skybreaker check -config /etc/skybreaker/skybreaker.yaml` validates both files.

## 4. Start

```sh
echo TS_AUTHKEY=tskey-auth-... | sudo tee /etc/skybreaker/env >/dev/null && sudo chmod 600 /etc/skybreaker/env
sudo install -m 644 deploy/skybreaker.service deploy/skybreaker-nftables.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now skybreaker-nftables skybreaker
```

The auth key is only used on first start. If you use Tailscale ACLs, allow your login to reach `skybreaker` on port 443.

## 5. Weekly image rebuild (optional)

Rebuilds the sandbox image with the newest harness CLIs and promotes it only if they still pass the protocol check.

```sh
sudo install -m 755 deploy/skybreaker-sandbox-update /usr/local/bin/
sudo mkdir -p /opt/skybreaker/image && sudo cp -r images/sandbox/. /opt/skybreaker/image/
sudo install -m 644 deploy/skybreaker-sandbox-update.service deploy/skybreaker-sandbox-update.timer /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now skybreaker-sandbox-update.timer
```
