set -euo pipefail
banner() { (exec 3<>/dev/tcp/127.0.0.1/$PORT && IFS= read -r -t "$1" -n 4 b <&3 && [ "$b" = SSH- ]) 2>/dev/null; }
s=$EPOCHREALTIME; banner 0.3 && echo up || echo "down rc=$?"; echo "took $(echo "$EPOCHREALTIME - $s" | bc)"
x=0; for i in 1 2 3; do banner 0.3 && { x=1; break; }; done; echo "loop survived x=$x"
