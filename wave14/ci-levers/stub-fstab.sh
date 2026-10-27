#!/usr/bin/env bash
# stub-fstab.sh <repo>: the cloud-config's fstab command, extracted from the
# YAML and run (via sh, as cloud-init runs a string runcmd) on a sample
# Ubuntu cloud-image fstab with /etc/fstab rewritten to a temp file
set -euo pipefail
repo=$1
here=$(cd "$(dirname "$0")" && pwd)
t=$(mktemp -d)
trap 'rm -rf "$t"' EXIT
cp "$here/fstab.sample" "$t/fstab"
cmd=$(python3 -I -c 'import yaml,sys; d=yaml.safe_load(open(sys.argv[1]).read().replace("@SSH_KEY@","k")); print([c for c in d["runcmd"] if isinstance(c,str) and "/etc/fstab" in c][0])' "$repo/ci/real-zfs-cloud-config.yaml")
sh -c "${cmd//\/etc\/fstab/$t/fstab}"
sh -c "${cmd//\/etc\/fstab/$t/fstab}" # idempotent: a second run adds nothing
cat "$t/fstab"
awk '$2 == "/" && $4 ~ /noauto/ { bad = "root got noauto" }
     $2 == "/boot" && $1 == "LABEL=BOOT" && $4 != "defaults,noauto" { bad = "/boot: " $4 }
     $2 == "/boot/efi" && $1 == "LABEL=UEFI" && $4 != "umask=0077,noauto" { bad = "/boot/efi: " $4 }
     $2 == "/boot/efi" && $1 == "/dev/x" && $4 != "noauto,umask=0077" { bad = "already noauto line changed: " $4 }
     /^# \/boot x$/ { c = 1 }
     END { if (!c) bad = "comment lost"; if (bad) { print "FAIL: " bad; exit 1 } }' "$t/fstab"
echo "PASS: stub-fstab"
