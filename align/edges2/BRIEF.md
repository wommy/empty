Context: we are building a Go "ZFS endpoint": a daemon on a ZFS host exposing typed, idempotent
volume/snapshot/clone/export operations over gRPC so CSI front ends stay thin. democratic-csi
(github:democratic-csi/democratic-csi) is the behavioral BASELINE only. Already decided:
zfs CLI behind a Runner seam with stderr classification (OpenZFS strings), name-keyed idempotency,
ZFS user properties as state (democratic-csi:* names for adoption), sharenfs/sharesmb for file
export, LIO/nvmet configfs for block export. Do not re-research those.

Tools: load GitHits with ToolSearch "select:mcp__GitHits__quick_start,mcp__GitHits__search,mcp__GitHits__grep,mcp__GitHits__research,mcp__GitHits__read,mcp__GitHits__resolve_target,mcp__GitHits__get_example"
then call quick_start once. Cite github:owner/repo path:line for every claim. Do not guess; say
"unverified" when you cannot confirm. Never execute downloaded code.

Output: write <=500 words to the file named in your task, with sections EDGES (boundary + what
crosses it), SEAMS (interfaces to cut + best existing implementation, with license), GAPS (what
nobody does well = where the endpoint can add value). Final reply: <=150-word summary.
