Context: Go "ZFS endpoint" at /home/user/empty (read README.md first, then internal/endpoint/{table,plan,apply,endpoint}.go,
api/zfsendpoint/v1/endpoint.proto). Do NOT modify /home/user/empty. Prior research is in /home/user/empty/docs/research/
(alignment.md, edges/, edges2/, adversarial/, dod/, dod-go/) -- build on it, do not redo it, and treat its claims as
unverified leads: part of your job is ADVERSARIAL -- try to break the specific prior claims you depend on.

The design must stay data-oriented: new state = new typed columns on Row (decoded once from zfs properties or
another single scan), new behavior = pure planner functions producing new Op kinds, effects only in apply.

Tools: GitHits via ToolSearch "select:mcp__GitHits__quick_start,mcp__GitHits__search,mcp__GitHits__grep,mcp__GitHits__research,mcp__GitHits__read"
(quick_start once). Local read-only clones: /tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/repos/
(openzfs incl. man, cmd, lib, module; democratic-csi; truenas-csi; zfs-localpv; synology-csi; csi-lib-iscsi; spec; pve-storage).
Cite path:line for every claim; "unverified" otherwise. Never execute downloaded code.

Output (<=600 words) to the file in your task with sections: CLAIMS CHECKED (prior claim -> HOLDS/BROKEN + evidence),
DESIGN (Row columns, Op kinds, planner signatures, proto changes), TEST PLAN (planner table tests + sim needs +
real-ZFS CI), RISKS. Final reply <=150 words.
