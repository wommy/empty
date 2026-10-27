#!/usr/bin/env bash
set -eo pipefail
want=2.4
for ver in $'zfs-2.4.4-1ubuntu4\nzfs-kmod-2.4.4-1ubuntu4' $'zfs-2.4.4-1ubuntu5\nzfs-kmod-2.4.4-1ubuntu4' \
  $'zfs-2.4.5-1ubuntu1\nzfs-kmod-2.4.4-1ubuntu4' $'zfs-2.4.4-1ubuntu4\nThe ZFS modules are not loaded.' $'zfs-2.4.4\nzfs-kmod-2.4.4' \
  $'zfs-2.4.0~rc1-0ubuntu1\nzfs-kmod-2.4.0~rc1-0ubuntu2'; do
  (
    grep -Eq "^zfs-${want//./\\.}\." <<<"$ver" && grep -Eq "^zfs-kmod-${want//./\\.}\." <<<"$ver" \
      || { echo "expected OpenZFS $want.x userland and kmod, got: $(tr '\n' ' ' <<<"$ver")"; exit 1; }
    urev=$(sed -n 's/^zfs-\([0-9]\)/\1/p' <<<"$ver") krev=$(sed -n 's/^zfs-kmod-//p' <<<"$ver")
    [ -n "$urev" ] && [ "${urev%%-*}" = "${krev%%-*}" ] \
      || { echo "userland and kmod OpenZFS versions differ (${urev%%-*} and ${krev%%-*})"; exit 1; }
    echo "zfs userland $urev, kmod $krev"
    [ "$urev" = "$krev" ] || echo "::warning::zfs userland $urev and kmod $krev: the same OpenZFS ${urev%%-*}, different distribution revisions"
  ) || echo "  -> rc=$?"
done
