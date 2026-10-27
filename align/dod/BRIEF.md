Context: a Go "ZFS endpoint" daemon (code at /home/user/empty, read internal/endpoint/{table,plan,apply,endpoint}.go
first; do not modify it). It is data-oriented: one `zfs get -Hp` scan -> Table of typed Rows; pure
Planners (Table, request) -> Plan (ops as plain data) | error; apply executes ops, each op carrying
its replay tolerance. Locks are in-process keyed mutexes. Question family: how should this best be
COORDINATED, rooted in data-oriented design (state as tables, transformations as pure functions,
effects at the edge, batch over per-item, avoid hidden object state).

Tools: GitHits via ToolSearch "select:mcp__GitHits__quick_start,mcp__GitHits__search,mcp__GitHits__grep,mcp__GitHits__research,mcp__GitHits__read,mcp__GitHits__resolve_target"
(call quick_start once). Local read-only OpenZFS checkout incl. man pages and cmd/zfs:
/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/repos/openzfs . Cite
path:line for every claim; say "unverified" otherwise. Never execute downloaded code.

Output (<=500 words) to the file named in your task: FINDINGS (cited), RECOMMENDATION for this
codebase (concrete: which type/func changes), RISKS. Final reply <=150 words.
