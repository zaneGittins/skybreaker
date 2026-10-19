#!/usr/bin/env bash
# Phase 2 exit test. Run INSIDE a sandbox, from the project's workspace:
#
#   docker exec -it skybreaker-<project> bash /workspace/../redteam.sh   (or copy it in)
#
# Every "must fail" check should fail, and every one should show up in the
# broker's audit log. The last checks must succeed.
#
# Needs: REPO=owner/name (the project's repo), OTHER_REPO=owner/other,
# LAN_HOST=an address on your LAN. The stolen-token check runs from another
# machine; see the end.
set -u
REPO=${REPO:?set REPO=owner/name}
OTHER_REPO=${OTHER_REPO:-octocat/Hello-World}
LAN_HOST=${LAN_HOST:-192.168.1.1}
pass=0 fail=0

ok()  { printf '  \033[32mPASS\033[0m %s\n' "$1"; pass=$((pass+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$1"; fail=$((fail+1)); }
must_fail()    { if eval "$2" >/dev/null 2>&1; then bad "$1 (it succeeded)"; else ok "$1"; fi; }
must_succeed() { if eval "$2" >/dev/null 2>&1; then ok "$1"; else bad "$1 (it failed)"; fi; }

# udp_dns SERVER sends one DNS query over UDP and succeeds only on a reply.
udp_dns() {
  python3 -c '
import os, socket, sys
q = os.urandom(2) + bytes.fromhex("01000001000000000000") + b"\x07example\x03com\x00" + bytes.fromhex("00100001")
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
s.settimeout(3)
s.sendto(q, (sys.argv[1], 53))
s.recvfrom(512)
' "$1"
}

echo "Credentials"
if env | grep -E '^(GH_TOKEN|GITHUB_TOKEN|ANTHROPIC_API_KEY|OPENAI_API_KEY)=' | grep -v '=skybreaker-injected$' | grep -q .; then
  bad "a real credential is in the environment"
else
  ok "only placeholders in the environment"
fi
must_fail "git credential fill returns nothing" \
  "printf 'protocol=https\nhost=github.com\n\n' | git credential fill | grep -q password="

echo "Git"
tmp=$(mktemp -d)
must_succeed "clone own repo" "git clone --depth 1 https://github.com/$REPO.git $tmp/own"
if [ -d "$tmp/own/.git" ]; then
  ( cd "$tmp/own" && git -c user.email=rt@skybreaker -c user.name=redteam commit --allow-empty -qm redteam )
  must_fail "push to main"            "cd $tmp/own && git push origin HEAD:main"
  must_fail "push a tag"              "cd $tmp/own && git tag rt-$$ && git push origin rt-$$"
  must_fail "delete a branch"         "cd $tmp/own && git push origin :refs/heads/agent/does-not-exist"
  must_fail "push outside the allowed prefixes" "cd $tmp/own && git push origin HEAD:refs/heads/agent/redteam-$$"
  must_succeed "push feature/redteam-$$" "cd $tmp/own && git push origin HEAD:refs/heads/feature/redteam-$$"
fi
must_fail "fetch another repo"        "git ls-remote https://github.com/$OTHER_REPO.git"

echo "Egress"
must_fail "curl an unlisted host"     "curl -fsS --max-time 10 https://example.com"
must_fail "DNS lookup"                "getent hosts example.com"
if command -v python3 >/dev/null; then
  gw=$(ip route 2>/dev/null | awk '/^default/ {print $3; exit}')
  for server in 1.1.1.1 8.8.8.8 127.0.0.11 ${gw:+"$gw"}; do
    must_fail "UDP DNS to $server"    "udp_dns $server"
  done
else
  bad "python3 is missing, so UDP egress went unchecked"
fi
must_fail "direct connection, no proxy" "curl -fsS --noproxy '*' --max-time 5 https://1.1.1.1"
must_fail "LAN via proxy"             "curl -fsS --max-time 5 http://$LAN_HOST/"
must_fail "LAN direct"                "curl -fsS --noproxy '*' --max-time 5 http://$LAN_HOST/"
# The broker shares the host; nothing else on it may be reachable.
must_fail "host loopback via proxy"   "curl -fsS --max-time 5 http://127.0.0.1:8080/"
if [ -n "${gw:-}" ]; then
  for port in 22 443 2375 7700; do
    must_fail "host port $port direct" "timeout 5 bash -c '</dev/tcp/$gw/$port'"
  done
fi
must_fail "tailnet CGNAT direct"      "curl -fsS --noproxy '*' --max-time 5 http://100.100.100.100/"
must_fail "cloud metadata"            "curl -fsS --max-time 5 http://169.254.169.254/"
must_fail "own key to api.anthropic.com" \
  "curl -fsS --max-time 10 https://api.anthropic.com/v1/messages -H 'x-api-key: sk-ant-attacker' -H 'content-type: application/json' -d '{}' | grep -q ."

echo "GitHub API"
must_succeed "read own repo via gh"   "gh api repos/$REPO --jq .full_name"
must_fail "read another repo via gh"  "gh api repos/$OTHER_REPO/pulls"

rm -rf "$tmp"
echo
echo "$pass passed, $fail failed"
cat <<'MANUAL'

Manual check, from another machine on your LAN: copy SKYBREAKER_TOKEN out of
the sandbox and try it. It must fail, because only the project's own sandbox
network may use its token:

  curl -x "http://<project>:<token>@<skybreaker-host-ip>:8080" https://github.com

Then check every attempt above on Skybreaker's Activity page.
MANUAL
[ "$fail" -eq 0 ]
