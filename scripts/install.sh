#!/bin/sh
# Installs Skybreaker on this Linux host:
#
#   curl -fsSL https://github.com/zaneGittins/skybreaker/releases/latest/download/install.sh | sh
#
# It fetches skybreaker-setup for this machine, checks it against the
# release's checksums.txt, installs it to /usr/local/bin and runs it, which
# walks you through the rest. Arguments go to skybreaker-setup:
#
#   curl -fsSL .../install.sh | sh -s -- upgrade
#
# SKYBREAKER_VERSION pins a release (v1.2.0). GITHUB_TOKEN, if set,
# authenticates downloads (a private fork needs it) and is passed on to
# skybreaker-setup.

# All in a function, run on the last line: a download cut short runs nothing.
main() {
  set -eu

  REPO=zaneGittins/skybreaker
  BIN=/usr/local/bin/skybreaker-setup

  [ "$(uname -s)" = Linux ] || { echo "Skybreaker runs on Linux only." >&2; exit 1; }
  case "$(uname -m)" in
    x86_64 | amd64) ARCH=amd64 ;;
    aarch64 | arm64) ARCH=arm64 ;;
    *) echo "No Skybreaker build for $(uname -m)." >&2; exit 1 ;;
  esac
  ASSET=skybreaker-setup-linux-$ARCH
  TMP=$(mktemp -d)
  trap 'rm -rf "$TMP"' EXIT

  # asset_url NAME reads a release's JSON on stdin and prints the asset's API URL.
  asset_url() {
    tr -d '\n' | tr '{' '\n' | grep "\"name\": *\"$1\"" |
      sed -n 's/.*"url": *"\([^"]*\/releases\/assets\/[0-9]*\)".*/\1/p' | head -n 1
  }

  # fetch NAME downloads one release asset to $TMP/NAME.
  if [ -n "${GITHUB_TOKEN:-}" ]; then
    if [ -n "${SKYBREAKER_VERSION:-}" ]; then rel=tags/$SKYBREAKER_VERSION; else rel=latest; fi
    curl -fsSL -H "Authorization: Bearer $GITHUB_TOKEN" "https://api.github.com/repos/$REPO/releases/$rel" >"$TMP/release.json" ||
      { echo "Couldn't read the $rel release; check GITHUB_TOKEN." >&2; exit 1; }
    fetch() {
      url=$(asset_url "$1" <"$TMP/release.json")
      [ -n "$url" ] || { echo "The release has no $1." >&2; exit 1; }
      curl -fsSL -H "Authorization: Bearer $GITHUB_TOKEN" -H "Accept: application/octet-stream" -o "$TMP/$1" "$url"
    }
  else
    if [ -n "${SKYBREAKER_VERSION:-}" ]; then base=download/$SKYBREAKER_VERSION; else base=latest/download; fi
    fetch() {
      curl -fsSL -o "$TMP/$1" "https://github.com/$REPO/releases/$base/$1" ||
        { echo "Couldn't download $1. If the repository is private, set GITHUB_TOKEN." >&2; exit 1; }
    }
  fi

  echo "Downloading skybreaker-setup ${SKYBREAKER_VERSION:-(latest)} for linux/$ARCH..."
  fetch "$ASSET"
  fetch checksums.txt
  (cd "$TMP" && grep " $ASSET\$" checksums.txt | sha256sum -c - >/dev/null) ||
    { echo "$ASSET doesn't match the release's checksums.txt." >&2; exit 1; }
  chmod 755 "$TMP/$ASSET"

  SUDO=
  # TS_AUTHKEY carries an unattended install's Tailscale key through sudo.
  [ "$(id -u)" = 0 ] || SUDO="sudo --preserve-env=GITHUB_TOKEN,TS_AUTHKEY"
  $SUDO install -m 755 "$TMP/$ASSET" "$BIN"
  echo "Installed $BIN"
  echo

  # A pinned release keeps its own installer.
  [ -z "${SKYBREAKER_VERSION:-}" ] || set -- "$@" --skip-self-update
  # The wizard needs the terminal; stdin is this script when piped to sh.
  if (: </dev/tty) 2>/dev/null; then
    exec $SUDO "$BIN" "$@" </dev/tty
  fi
  exec $SUDO "$BIN" "$@"
}

main "$@"
