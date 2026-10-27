p='.github/workflows/nightly.yml'
s=open(p).read()
rep=[
('''  # the two sweeps are endpoint tests, the csi job runs the csi suite
  # against the same guest.
''','''  # the two sweeps are endpoint tests, the csi job runs the csi suite
  # against the same guest.
  # The csi job boots with ZFSVM_INITRD=1: through the BIOS, GRUB and the
  # disk's own initrd, unmasked, the fallback ci/real-zfs-vm.sh takes only
  # when the direct kernel boot fails, so a GRUB timeout or a slower boot
  # shows here first, not on the day the fast path breaks. The endpoint
  # job keeps the direct boot, as ci.yml.
'''),
('''          ZFS_CRASH_FULL: "1"
        run: ci/real-zfs-vm.sh stonking current 2.4
''','''          ZFS_CRASH_FULL: "1"
          ZFSVM_INITRD: ${{ matrix.suite == 'csi' && '1' || '' }}
        run: ci/real-zfs-vm.sh stonking current 2.4
'''),
]
for a,b in rep:
    assert a in s, a
    s=s.replace(a,b,1)
open(p,'w').write(s)
