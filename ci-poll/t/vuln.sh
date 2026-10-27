#!/usr/bin/env bash
set -euo pipefail
vuln='root=/dev/vda1 ro mitigations=off init_on_alloc=0
/sys/devices/system/cpu/vulnerabilities/meltdown:Not affected
/sys/devices/system/cpu/vulnerabilities/retbleed:Vulnerable
/sys/devices/system/cpu/vulnerabilities/spectre_v2:Vulnerable; IBPB: disabled; STIBP: disabled
/sys/devices/system/cpu/vulnerabilities/mds:Mitigation: Clear CPU buffers'
vulnsum="$(awk '/:Vulnerable/ { n++ } END { print n + 0 }' <<<"$vuln") of $(awk '/vulnerabilities\// { n++ } END { print n + 0 }' <<<"$vuln") vulnerable"
echo "$vulnsum"
vuln=''
vulnsum="$(awk '/:Vulnerable/ { n++ } END { print n + 0 }' <<<"$vuln") of $(awk '/vulnerabilities\// { n++ } END { print n + 0 }' <<<"$vuln") vulnerable"
echo "$vulnsum"
