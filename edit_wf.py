def edit(p, pairs):
    s = open(p).read()
    for a, b in pairs:
        assert s.count(a) == 1, (p, a)
        s = s.replace(a, b)
    open(p, 'w').write(s)

edit('.github/workflows/ci.yml', [
("""      - name: check for a stored guest
""", """      - name: upload zfsdiff replay report
        # every run: TestZFSDiffReplay reports real pool vs simulator vs
        # reference model divergences without failing the job (the job
        # summary has the verdicts; the full replay is in nightly.yml)
        if: always()
        uses: actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02 # v4.6.2
        with:
          name: zfsdiff-report
          path: ${{ runner.temp }}/zfsvm/out/zfsdiff.log
          if-no-files-found: warn
          retention-days: 30
      - name: check for a stored guest
"""),
])

edit('.github/workflows/nightly.yml', [
("""          - { name: FuzzNFSEtabParse, pkg: ./internal/endpoint }
""", """          - { name: FuzzNFSEtabParse, pkg: ./internal/endpoint }
          # the reference model of zfs(8) against the simulator
          # (internal/zfs/zfsdiff, wave9 zfs-diff)
          - { name: FuzzDiff, pkg: ./internal/zfs/zfsdiff }
"""),
("""  # The scale tests and the model-based FSM explorer.""", """  # The real-pool replay of the whole zfsdiff corpus (100 sequences, ~1,230
  # zfs invocations, estimated 15-30s): too slow for ADR 0005's PR budget,
  # where the VM replays zfsdiff.PRSample. It runs the same VM suite as
  # ci.yml with ZFSDIFF_FULL=1. Divergences are reported, not failed, until
  # the three ambiguities are settled (ZFSDIFF_STRICT); a harness, cleanup
  # or suite failure files an issue.
  real-zfs-full:
    runs-on: ubuntu-24.04
    timeout-minutes: 30
    permissions:
      contents: read
      issues: write
    steps:
      - uses: actions/checkout@11d5960a326750d5838078e36cf38b85af677262 # v4.4.0
        with:
          persist-credentials: false
      - uses: ./.github/actions/setup-go
        with:
          cache-name: nightly-real-zfs
      - name: enable kvm
        run: |
          echo 'KERNEL=="kvm", GROUP="kvm", MODE="0666", OPTIONS+="static_node=kvm"' | sudo tee /etc/udev/rules.d/99-kvm4all.rules
          sudo udevadm control --reload-rules
          sudo udevadm trigger --name-match=kvm
          test -c /dev/kvm || { echo "::error::no /dev/kvm on this runner"; exit 1; }
      - name: resolve the cloud image
        # the same cache keys as ci.yml's real-zfs job, restored only
        id: image
        run: |
          f=stonking-server-cloudimg-amd64.img
          sum=$(curl -fsSL --retry 3 https://cloud-images.ubuntu.com/stonking/current/SHA256SUMS | awk -v f="$f" '$2 == f || $2 == "*" f { print $1 }')
          test -n "$sum"
          echo "key=zfsvm-v2-stonking-$sum-$(date -u +%G-W%V)-${{ hashFiles('ci/real-zfs-cloud-config.yaml') }}" >>"$GITHUB_OUTPUT"
          echo "qemu=qemu-tree-$ImageOS-$ImageVersion-${{ hashFiles('ci/qemu-tree.sh') }}" >>"$GITHUB_OUTPUT"
      - name: restore the provisioned guest
        id: guest
        uses: actions/cache/restore@5a3ec84eff668545956fd18022155c47e93e2684 # v4.2.3
        with:
          path: ${{ runner.temp }}/zfsvm-cache
          key: ${{ steps.image.outputs.key }}
      - name: restore qemu
        uses: actions/cache/restore@5a3ec84eff668545956fd18022155c47e93e2684 # v4.2.3
        with:
          path: ${{ runner.temp }}/qemu
          key: ${{ steps.image.outputs.qemu }}
      - name: set up qemu
        run: |
          if ci/qemu-tree.sh "$RUNNER_TEMP/qemu"; then
            echo "$RUNNER_TEMP/qemu/bin" >>"$GITHUB_PATH"
          else
            rm -rf "$RUNNER_TEMP/qemu"
            sudo apt-get update -qq
            sudo apt-get install -y -qq --no-install-recommends qemu-system-x86 qemu-utils
          fi
          test -s /usr/share/keyrings/ubuntu-cloudimage-keyring.gpg
      - name: install cloud-image-utils (cold guest only)
        if: steps.guest.outputs.cache-hit != 'true'
        run: |
          sudo apt-get update -qq
          sudo apt-get install -y -qq --no-install-recommends cloud-image-utils
      - name: VM suite with the full zfsdiff replay
        id: replay
        env:
          ZFSVM_CACHE: ${{ runner.temp }}/zfsvm-cache
          ZFSDIFF_FULL: "1"
        run: ci/real-zfs-vm.sh stonking current 2.4
      - name: upload zfsdiff replay report
        if: always()
        uses: actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02 # v4.6.2
        with:
          name: zfsdiff-report-full
          path: |
            ${{ runner.temp }}/zfsvm/out/*.log
            ${{ runner.temp }}/zfsvm/*.log
          if-no-files-found: warn
          retention-days: 30
      - name: file issue
        if: failure() && steps.replay.conclusion == 'failure'
        env:
          GH_TOKEN: ${{ github.token }}
          RUN_URL: ${{ github.server_url }}/${{ github.repository }}/actions/runs/${{ github.run_id }}
        run: |
          {
            echo "Nightly real-ZFS VM suite with the full zfsdiff replay failed: $RUN_URL"
            echo
            echo "The zfsdiff-report-full artifact has the replay report and the shard logs."
            echo; echo '```'; tail -n 1 "$RUNNER_TEMP/zfsvm/out/zfsdiff.log" 2>/dev/null || echo "no zfsdiff.log"; echo '```'
          } > "$RUNNER_TEMP/issue.md"
          gh issue create --repo "$GITHUB_REPOSITORY" --title "nightly real-ZFS failure" --body-file "$RUNNER_TEMP/issue.md" --label needs-triage

  # The scale tests and the model-based FSM explorer."""),
])
