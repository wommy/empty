# Edge: data protection (snapshot export, replication, CBT)

## Q1. Has any CSI driver implemented SnapshotMetadata?

Yes, one real driver and one demo. Nothing for ZFS.

- Spec: `SnapshotMetadata` has `GetMetadataAllocated` and `GetMetadataDelta` server streams, marked alpha (github:container-storage-interface/spec spec.md:425-433). A delta with CBT disabled returns FAILED_PRECONDITION (spec.md:3832).
- Sidecar: github:kubernetes-csi/external-snapshot-metadata README.md:13-23 describes a sidecar that fronts the CSI service for backup apps. The base-snapshot retention flag is in client/apis/snapshotmetadataservice/v1beta1/types.go:37-39.
- ceph-csi (RBD): implements it. docs/rbd/deploy.md:603-605 states this. `GetMetadataDelta` uses librbd diff-iterate by snap ID (internal/rbd/snap_diff.go:101-109; internal/rbd/sms_controllerserver.go:175-185). License: unverified.
- csi-driver-host-path: implements it, but only as a demo. pkg/hostpath/snapshotmetadata.go:29-30 says "for demo and CI testing purpose only ... should not be used as an example about how to write a real driver." It reads snapshot image files (snapshotmetadataserver.go:64-71) and supports block-mode volumes only (line 55-57).
- democratic-csi: vendors the proto only (csi_proto/csi-v1.11.0.proto:130-138). No implementation found. The exhaustive grep timed out, so this is a search-based negative.
- openebs/zfs-localpv: no hits.

## Q2. Can ZFS supply allocated and changed blocks for zvols?

No OpenZFS API returns block ranges for a zvol. The send stream is the only exact source.

- `zfs diff`: the entry point is cmd/zfs/zfs_main.c:8101. It maps objects to paths via dmu_diff (module/zfs/dmu_diff.c:157-231, which walks dnodes). A zvol has no namespace, so this gives no useful ranges. Whether it hard-rejects zvols is unverified.
- Incremental send stream: dmu_send.c emits WRITE, FREE and OBJECT_RANGE records (dump_free at :318; dump_object_range at :795; do_dump at :888). Parsing the stream without applying it would give changed ranges. Unverified: that zvol WRITE records carry usable offsets without a zvol-specific path.
- Full send for allocated ranges: holes are handled in send_reader_thread (dmu_send.c:1827-1831). Unverified: that a full zvol send yields exactly the allocated blocks.
- `lzc_send_space` (lib/libzfs_core/libzfs_core.c:1044-1048; contrib/pyzfs/libzfs_core/_libzfs_core.py:611-621) returns a byte estimate only, no ranges. Useful as a cheap "anything changed?" check.
- Bookmarks: `zfs_bookmark_phys_t` holds guid, creation_txg and redaction_obj (include/sys/dsl_bookmark.h:32-42). Incremental send from `#bm` needs no `@from` snapshot.
- Redacted send exists (`zbm_redaction_obj`). It is not a block-list API.
- zdb block walking and SEEK_DATA on the zvol device were not checked.

## Q3. Replication seams to expose

- zrepl (MIT, LICENSE line 1): the seam is the Receiver interface, where `Receive` takes control (`pdu.ReceiveReq`) and the send stream (`io.ReadCloser`) as separate arguments (internal/replication/logic/replication_logic.go:49-53). Transports are TCP, TLS and SSH (docs/index.rst:55). Not confirmed as gRPC.
- Resume tokens: `zfs send -t` from `receive_resume_token`, with the `@to` existence rule (docs/configuration/overview.rst:204-208). zrepl probes support in internal/zfs/resume_token.go:46-59.
- Holds and bookmarks: step holds and the `zrepl_CURSOR` bookmark keep bases alive (internal/replication/design.md:121-131, 11-15).
- Dry-run size: `zfs send -nvP -t` (internal/zfs/zfs_test.go:131-132).
- sanoid/syncoid (GPL-3.0; packages/gentoo/sys-fs/sanoid/sanoid-9999.ebuild:12): shell-out to `zfs send/recv` over SSH. Resume via token (syncoid:571-576). Use as reference only.

Seams for the endpoint:
1. Keep the stream on a raw byte channel, separate from control RPCs, as zrepl's Receive does.
2. Carry the resume token as an opaque field in the send call.
3. Create and own bookmarks, so the base survives pruning. This matches the external-snapshot-metadata base-retention flag.
4. Implement GetMetadataDelta by dry-parsing an incremental stream, and GetMetadataAllocated from a full stream. Feasibility unverified. Cost: a full stream read per call.

## GAPS

- No block-range API for zvols in OpenZFS. Stream parsing is the only exact route.
- No ZFS implementation of SnapshotMetadata found in the sources read.
- The only real driver implementation (ceph-csi) relies on librbd diff-iterate, which ZFS does not have.
- The endpoint can add value by serving SnapshotMetadata from stream parsing, with bookmark-pinned bases.
