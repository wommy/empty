#!/usr/bin/env bash
# stub-keys.sh <repo>: runs the key lines of .github/actions/real-zfs-keys
# (no network: a fake SHA256SUMS) and checks that vm-fallback is a prefix of
# vm and that vm stays the same across two calls with the same inputs
set -euo pipefail
repo=$1
cd "$repo"
t=$(mktemp -d)
trap 'rm -rf "$t"' EXIT
SERIES=stonking
printf 'abc123  *%s-server-cloudimg-amd64.img\n' "$SERIES" >"$t/SHA256SUMS"
run() {
  local ImageOS=$1 ImageVersion=$2 GITHUB_OUTPUT=$t/out
  : >"$GITHUB_OUTPUT"
  export ImageOS ImageVersion GITHUB_OUTPUT SERIES
  # the action's lines from `f=` to the vm-fallback echo, verbatim
  awk '/^        f=\$SERIES/ { p = 1 } p { sub(/^        /, ""); print } /vm-fallback=/ { p = 0 }' \
    .github/actions/real-zfs-keys/action.yml >"$t/keys.sh"
  d=$t bash -c 'd=$0; set -euo pipefail; . "$1"' "$t" "$t/keys.sh"
  cat "$GITHUB_OUTPUT"
}
a=$(run ubuntu24 20261020.1)
b=$(run ubuntu24 20261020.1)
c=$(run ubuntu24 20261027.2)
echo "$a"
[ "$a" = "$b" ] || { echo "FAIL: keys differ for the same inputs"; exit 1; }
vm=$(sed -n 's/^vm=//p' <<<"$a")
fb=$(sed -n 's/^vm-fallback=//p' <<<"$a")
vm2=$(sed -n 's/^vm=//p' <<<"$c")
fb2=$(sed -n 's/^vm-fallback=//p' <<<"$c")
case $vm in "$fb"*) ;; *) echo "FAIL: fallback $fb is not a prefix of $vm"; exit 1 ;; esac
[ "$vm" != "$vm2" ] || { echo "FAIL: a runner image bump did not change the key"; exit 1; }
[ "$fb" = "$fb2" ] || { echo "FAIL: a runner image bump changed the guest prefix"; exit 1; }
case $vm2 in "$fb"*) ;; *) echo "FAIL: new image key does not fall back to the old guest"; exit 1 ;; esac
echo "PASS: vm key stable, fallback prefix shared across runner images"
