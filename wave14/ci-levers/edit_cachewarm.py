import sys
p = sys.argv[1]
s = open(p).read()
start = s.index('      - name: look up the provisioned guest\n')
end = s.index('      - name: upload VM console\n')
new = '''      # one entry holds the guest and the qemu tree (ci.yml restores it in
      # one step). Three cases: the full key exists (nothing to do); only
      # the guest part matches (a runner image release: restore it, build a
      # fresh qemu tree, save under the new key, no reprovisioning); nothing
      # matches (provision)
      - name: look up the provisioned guest and qemu
        id: probe
        uses: actions/cache/restore@5a3ec84eff668545956fd18022155c47e93e2684 # v4.2.3
        with:
          path: |
            ${{ runner.temp }}/zfsvm-cache
            ${{ runner.temp }}/qemu
          key: ${{ steps.keys.outputs.vm }}
          restore-keys: ${{ steps.keys.outputs.vm-fallback }}
          lookup-only: true
      - name: restore the provisioned guest (another runner image's qemu)
        if: steps.probe.outputs.cache-hit != 'true' && steps.probe.outputs.cache-matched-key != ''
        uses: actions/cache/restore@5a3ec84eff668545956fd18022155c47e93e2684 # v4.2.3
        with:
          path: |
            ${{ runner.temp }}/zfsvm-cache
            ${{ runner.temp }}/qemu
          key: ${{ steps.keys.outputs.vm }}
          restore-keys: ${{ steps.keys.outputs.vm-fallback }}
      - name: set up qemu
        id: qemutree
        if: steps.probe.outputs.cache-hit != 'true'
        run: |
          # always a fresh tree for this runner image: a restored one came
          # from another image (ci.yml only verifies it, for speed)
          rm -rf "$RUNNER_TEMP/qemu"
          if ci/qemu-tree.sh "$RUNNER_TEMP/qemu"; then
            echo "$RUNNER_TEMP/qemu/bin" >>"$GITHUB_PATH"
            echo "ok=1" >>"$GITHUB_OUTPUT"
          else
            echo "::warning::qemu tree unusable; installing qemu from apt"
            # empty, so the guest is still saved; runs fall back to apt
            rm -rf "$RUNNER_TEMP/qemu" && mkdir -p "$RUNNER_TEMP/qemu"
            sudo apt-get update -qq
            sudo apt-get install -y -qq --no-install-recommends qemu-system-x86 qemu-utils
          fi
          test -s /usr/share/keyrings/ubuntu-cloudimage-keyring.gpg
      - name: enable kvm
        if: steps.probe.outputs.cache-hit != 'true' && steps.probe.outputs.cache-matched-key == ''
        run: |
          echo 'KERNEL=="kvm", GROUP="kvm", MODE="0666", OPTIONS+="static_node=kvm"' | sudo tee /etc/udev/rules.d/99-kvm4all.rules
          sudo udevadm control --reload-rules
          sudo udevadm trigger --name-match=kvm
          test -c /dev/kvm || { echo "::error::no /dev/kvm on this runner"; exit 1; }
          sudo apt-get update -qq
          sudo apt-get install -y -qq --no-install-recommends cloud-image-utils
      - name: provision, store and check the guest
        if: steps.probe.outputs.cache-hit != 'true' && steps.probe.outputs.cache-matched-key == ''
        env:
          ZFSVM_CACHE: ${{ runner.temp }}/zfsvm-cache
          ZFSVM_SUMS: ${{ steps.keys.outputs.sums }}
          ZFSVM_PROVISION_ONLY: "1"
        run: ci/real-zfs-vm.sh stonking current 2.4
      - name: check for a stored guest
        id: stored
        if: steps.probe.outputs.cache-hit != 'true'
        run: test -f "$RUNNER_TEMP/zfsvm-cache/ok" && echo "ok=1" >>"$GITHUB_OUTPUT" || true
      - name: save the provisioned guest and qemu
        id: saveguest
        if: steps.stored.outputs.ok == '1'
        uses: actions/cache/save@5a3ec84eff668545956fd18022155c47e93e2684 # v4.2.3
        with:
          path: |
            ${{ runner.temp }}/zfsvm-cache
            ${{ runner.temp }}/qemu
          key: ${{ steps.keys.outputs.vm }}
      - name: delete superseded guests on this branch
        # each is ~0.9 GB of the repository's 10 GB cache budget; this also
        # removes the v3 guest entries. The v3-era qemu-tree-* entries
        # (23 MB) age out unused after 7 days
        if: steps.saveguest.outcome == 'success'
        env:
          GH_TOKEN: ${{ github.token }}
          KEEP: ${{ steps.keys.outputs.vm }}
        run: |
          gh cache list --repo "$GITHUB_REPOSITORY" --ref "$GITHUB_REF" --key zfsvm- --limit 100 --json id,key \\
            --jq ".[] | select(.key != \\"$KEEP\\") | .id" |
            while read -r id; do gh cache delete --repo "$GITHUB_REPOSITORY" "$id" || true; done
'''
s = s[:start] + new + s[end:]
old = "# (~13s) and a stale Go build cache."
assert s.count(old) == 1
s = s.replace(old, "# (~13s) and a stale Go build cache. The guest and the qemu tree share one\n# cache entry, which ci.yml restores in one step.")
open(p, 'w').write(s)
