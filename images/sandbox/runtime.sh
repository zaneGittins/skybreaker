#!/bin/sh
# runc, as the inner dockerd and BuildKit see it: a thin wrapper around the
# real runc, installed ahead of it on PATH. Before a container is created it bind-mounts the sandbox's CA
# bundle (the public roots plus the broker's CA) over the image's own, and
# points the usual variables at it, so builds and containers trust the
# broker as the sandbox does: `RUN go mod download` and private git fetches
# work in any image, with no change to it. Nothing beyond what the sandbox
# trusts is trusted, and every request still goes through the broker.
#
# dockerd's containers start with `runc create` and BuildKit's steps (`docker
# compose build`, `docker buildx`) with `runc run`; everything else passes
# straight through.
set -eu

bundle=""
create=""
prev=""
for a in "$@"; do
	case "$prev" in --bundle | -b) bundle=$a ;; esac
	case "$a" in
	--bundle=*) bundle=${a#--bundle=} ;;
	create | run) create=1 ;;
	esac
	prev=$a
done

runc=${SKYBREAKER_RUNC:-/usr/sbin/runc}
ca=${SKYBREAKER_CA_BUNDLE:-/etc/ssl/certs/ca-certificates.crt}
cfg=$bundle/config.json
if [ -n "$create" ] && [ -f "$cfg" ] && [ -s "$ca" ] && command -v jq >/dev/null 2>&1 &&
	! jq -e 'any(.mounts[]?; .destination == "/etc/skybreaker/ca-bundle.crt")' "$cfg" >/dev/null; then
	root=$(jq -r '.root.path // "rootfs"' "$cfg")
	case "$root" in /*) ;; *) root=$bundle/$root ;; esac
	# Cover the bundle wherever the image keeps it (Debian, Ubuntu and
	# Alpine; Red Hat and Fedora), and always at a path of our own that the
	# variables name.
	dests="/etc/skybreaker/ca-bundle.crt"
	for d in /etc/ssl/certs/ca-certificates.crt /etc/pki/tls/certs/ca-bundle.crt; do
		if [ -e "$root$d" ] || [ -L "$root$d" ]; then
			dests="$dests $d"
		fi
	done
	if jq --arg ca "$ca" --arg dests "$dests" '
		.mounts += [($dests | split(" ")[] | {destination: ., type: "bind", source: $ca, options: ["rbind", "ro"]})]
		| .process.env as $env
		| .process.env += [
			("SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS", "REQUESTS_CA_BUNDLE", "CURL_CA_BUNDLE", "GIT_SSL_CAINFO")
			| select(. as $k | ($env // []) | map(startswith($k + "=")) | any | not)
			| . + "=/etc/skybreaker/ca-bundle.crt"
		]' "$cfg" >"$cfg.skybreaker"; then
		mv "$cfg.skybreaker" "$cfg"
	else
		rm -f "$cfg.skybreaker"
		echo "skybreaker runc: couldn't add the CA bundle; the container starts without it" >&2
	fi
fi
exec "$runc" "$@"
