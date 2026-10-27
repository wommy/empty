#!/usr/bin/env bash
# Runs once inside the guest during ci/vm/bake.sh (cloud-init runcmd, after
# the packages are installed). Trims the boot, hands the kernel, initrd and
# provenance to the host over 9p, and switches cloud-init off for good.
set -euxo pipefail
want=${1:?openzfs minor}

modprobe zfs
zfs version
zfs version | grep -Eq "^zfs-kmod-${want//./\\.}\." || { echo "zfs kmod is not $want.x" >&2; exit 1; }
for m in 9p 9pnet_virtio; do modprobe "$m"; done
printf '%s\n' zfs 9p 9pnet_virtio >/etc/modules-load.d/zfsci.conf

systemctl enable nfs-server.service zfsci.service
# what a test boot never needs; purge or mask only what exists
for p in snapd unattended-upgrades; do
  if dpkg -s "$p" >/dev/null 2>&1; then apt-get purge -y -qq "$p"; fi
done
for u in apt-daily.timer apt-daily-upgrade.timer motd-news.timer man-db.timer \
  e2scrub_all.timer fstrim.timer fwupd-refresh.timer update-notifier-download.timer \
  ModemManager.service multipathd.service multipathd.socket lxd-installer.socket; do
  if systemctl list-unit-files "$u" --no-legend | grep -q .; then systemctl mask "$u"; fi
done
apt-get clean
rm -rf /var/lib/apt/lists/*

mkdir -p /mnt/out
mount -t 9p -o trans=virtio,version=9p2000.L,msize=524288 out /mnt/out
k=$(uname -r)
cp "/boot/vmlinuz-$k" /mnt/out/vmlinuz
cp "/boot/initrd.img-$k" /mnt/out/initrd.img
# the cloud image's own command line, minus the loader's BOOT_IMAGE
sed -E 's/BOOT_IMAGE=[^ ]+ ?//' /proc/cmdline >/mnt/out/cmdline
mkdir -p /etc/zfsci
{
  cat /mnt/out/provenance.host
  echo "serial=$(sed -n 's/^serial: *//p' /etc/cloud/build.info)"
  echo "kernel=$k"
  zfs version | sed -n 's/^zfs-kmod-/zfs_kmod=/p; s/^zfs-\([0-9]\)/zfs=\1/p'
} >/etc/zfsci/provenance
cp /etc/zfsci/provenance /mnt/out/provenance

# every later boot is a test boot: no cloud-init, a fresh machine-id
touch /etc/cloud/cloud-init.disabled
truncate -s 0 /etc/machine-id
fstrim -av || true
sync
touch /mnt/out/baked
umount /mnt/out
