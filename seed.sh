S=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad
export PATH=$S/tc/golang.org/toolchain@v0.0.1-go1.27.3.linux-amd64/bin:$PATH GOTOOLCHAIN=local
cd /home/user/empty/.claude/worktrees/agent-a12cc5177ef7ae6a6
want=$(grep -rhoE '^func Fuzz[A-Za-z0-9_]+' --include='*_test.go' . | awk '{print $2}' | sort)
echo "want: $want"
time go test -count=1 -run '^Fuzz' -v ./... > $S/seed.log 2>&1
got=$(grep -oE '^--- PASS: Fuzz[A-Za-z0-9_]+' $S/seed.log | awk '{print $3}' | sort)
echo "got: $got"
test "$want" = "$got" && echo MATCH
