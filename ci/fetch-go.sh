#!/usr/bin/env bash
# Fetch the pinned Go toolchain into <dest>, checksum-verified: the same
# tarball actions/setup-go downloads, without its ~5s copy into the tool
# cache. Run in the background while the VM boots; writes <dest>/.ok or
# <dest>/.failed, and logs to <dest>.log.
#
# usage: GO_TARBALL_URL=... GO_TARBALL_SHA256=... ci/fetch-go.sh <dest>
set -euo pipefail
dest=${1:?destination}
exec >"$dest.log" 2>&1
rm -rf "$dest"
mkdir -p "$dest"
trap '[ -e "$dest/.ok" ] || touch "$dest/.failed"' EXIT
curl -fsSL --retry 3 -o "$dest.tgz" "${GO_TARBALL_URL:?}"
echo "${GO_TARBALL_SHA256:?}  $dest.tgz" | sha256sum -c -
tar xzf "$dest.tgz" -C "$dest"
rm -f "$dest.tgz"
"$dest/bin/go" version
touch "$dest/.ok"
