#!/bin/sh
# Installs (or updates) the skybreaker terminal client on Linux or macOS,
# into ~/.local/bin without sudo:
#
#   curl -fsSL https://github.com/zaneGittins/skybreaker/releases/latest/download/install-cli.sh | sh
#
# The download is checked against the release's checksums.txt.
# SKYBREAKER_VERSION pins a release (v1.2.0), SKYBREAKER_BIN picks another
# directory, and GITHUB_TOKEN, if set, authenticates downloads (a private
# fork needs it).

# All in a function, run on the last line: a download cut short runs nothing.
main() {
  set -eu

  REPO=zaneGittins/skybreaker
  DIR=${SKYBREAKER_BIN:-$HOME/.local/bin}

  case "$(uname -s)" in
    Linux) OS=linux ;;
    Darwin) OS=darwin ;;
    *) echo "The skybreaker client runs on Linux and macOS." >&2; exit 1 ;;
  esac
  case "$(uname -m)" in
    x86_64 | amd64) ARCH=amd64 ;;
    aarch64 | arm64) ARCH=arm64 ;;
    *) echo "No skybreaker build for $(uname -m)." >&2; exit 1 ;;
  esac
  ASSET=skybreaker-$OS-$ARCH
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
        { echo "Couldn't download $1." >&2; exit 1; }
    }
  fi

  if command -v sha256sum >/dev/null 2>&1; then SHA="sha256sum -c -"; else SHA="shasum -a 256 -c -"; fi

  echo "Downloading skybreaker ${SKYBREAKER_VERSION:-(latest)} for $OS/$ARCH..."
  fetch "$ASSET"
  fetch checksums.txt
  (cd "$TMP" && grep " $ASSET\$" checksums.txt | $SHA >/dev/null) ||
    { echo "$ASSET doesn't match the release's checksums.txt." >&2; exit 1; }

  mkdir -p "$DIR"
  chmod 755 "$TMP/$ASSET"
  mv -f "$TMP/$ASSET" "$DIR/skybreaker"
  echo "Installed $DIR/skybreaker"

  case ":$PATH:" in
    *":$DIR:"*) ;;
    *) echo "Add $DIR to your PATH, for example: echo 'export PATH=\"$DIR:\$PATH\"' >> ~/.profile" ;;
  esac
  found=$(command -v skybreaker 2>/dev/null || true)
  if [ -n "$found" ] && [ "$found" != "$DIR/skybreaker" ]; then
    echo "Note: $found comes first on your PATH."
  fi
  echo "Run skybreaker to pick a session. It finds your Skybreaker through Tailscale."
}

main "$@"
