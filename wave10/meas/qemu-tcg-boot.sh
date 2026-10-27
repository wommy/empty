#!/usr/bin/env bash
# Boot the extracted qemu tree under TCG (no /dev/kvm here) far enough to
# prove the wrapper finds SeaBIOS, the VGA BIOS and the virtio-net iPXE ROM.
set -uo pipefail
q=$1/bin/qemu-system-x86_64
log=$(dirname "$0")/seabios.log
rm -f "$log"
timeout 6 "$q" -machine q35,accel=tcg -m 256 -nodefaults -display none -no-reboot \
  -netdev user,id=n0 -device virtio-net-pci,netdev=n0,mac=52:54:00:12:34:56 -device VGA \
  -chardev file,id=dbg,path="$log" -device isa-debugcon,iobase=0x402,chardev=dbg
echo "qemu exit $? (124 = still running at the timeout: good)"
head -n 3 "$log"
grep -iE 'ipxe|virtio|vga|Booting from|No bootable|option rom' "$log" | head -n 12
