import os
os.chdir('/home/user/empty/.claude/worktrees/agent-af94e460282111105')
p = 'ci/real-zfs-vm.sh'
s = open(p).read()
old = '''echo "::group::systemd-analyze"
'''
new = '''# the guest's CPU and pressure over the test phase (ci/real-zfs-guest.sh):
# what decides whether splitting the job across two runners would pay
if [ -s "$o/load.txt" ]; then
  echo "::group::guest load (test phase): $(awk 'NR == 2' "$o/load.txt")"
  cat "$o/load.txt"
  cat "$o/vmstat.txt" 2>/dev/null || true
  echo "::endgroup::"
  { echo '<details><summary>guest load over the test phase (out/load.txt)</summary>'
    echo; echo '```'; cat "$o/load.txt"; echo '```'; echo "</details>"; echo; } >>"$summary"
fi
echo "::group::systemd-analyze"
'''
assert s.count(old) == 1
s = s.replace(old, new)
open(p, 'w').write(s)
p = '.github/workflows/ci.yml'
s = open(p).read()
old = '''            ${{ runner.temp }}/zfsvm/out/endpoint.list
          if-no-files-found: warn
'''
new = '''            ${{ runner.temp }}/zfsvm/out/endpoint.list
            ${{ runner.temp }}/zfsvm/out/load.txt
            ${{ runner.temp }}/zfsvm/out/vmstat.txt
          if-no-files-found: warn
'''
assert s.count(old) == 1
s = s.replace(old, new)
old = '''        # test-times.tsv + endpoint.list feed ci/real-zfs-weights.sh
'''
assert s.count(old) == 1
s = s.replace(old, '''        # test-times.tsv + endpoint.list feed ci/real-zfs-weights.sh;
        # load.txt + vmstat.txt: the guest's CPU and pressure in the tests
''')
open(p, 'w').write(s)
