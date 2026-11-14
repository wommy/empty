# ZFS encryption and key management

## EDGES
Key bytes enter OpenZFS only in `get_key_material()` (github:openzfs/zfs@a128b8a0 lib/libzfs/libzfs_crypto.c:670-700), selected by `keylocation`:
- `prompt`: TTY, else stdin (libzfs_crypto.c:691-699). Keys on argv are discouraged (man/man7/zfsprops.7:1243-1247). Receive cannot set prompt because stdin carries the stream (man/man8/zfs-receive.8:144-151).
- `file:///abs/path`: read from file.
- `https://` / `http://`: libcurl GET, 30s timeout, client cert from `SSL_CLIENT_*` env (libzfs_crypto.c:602-645). ENOSYS if built without curl (649-651).
- `keyformat`: passphrase (8-512 bytes, PBKDF2) or raw/hex at 32 bytes (zfsprops.7:1203-1218).

Drivers:
- openebs/zfs-localpv: `encryption`, `keyformat`, `keylocation` on the ZFSVolume CR (github:openebs/zfs-localpv@8759e507 pkg/apis/openebs.io/zfs/v1/zfsvolume.go:157-165; pkg/builder/volbuilder/build.go:110-123). Test uses `encryption=aes-256-gcm` and a file URL (pkg/zfs/volume_test.go:153), so the key stays in a host file. Clone args drop encryption (CHANGELOG.md:208).
- truenas/truenas-csi: StorageClass `encryption`, `encryption.algorithm` (AES-256-GCM default), `encryption.passphrase`, `encryption.key`, `encryption.generateKey` (README.md:277-285). Sent as `EncryptionOptions` JSON (pkg/client/storage.go:128-141). The passphrase sits in StorageClass params. Only the API key uses a Secret ref (charts/truenas-csi/templates/_helpers.tpl:154-156).
- truenas/middleware: `pool.dataset` takes `encryption_options` (src/middlewared/middlewared/api/v25_10_0/pool_dataset.py:324). Precedence is passphrase, then generateKey, then key (plugins/zfs/create_rules.py:144-158). `change-key` requires keylocation unless a key is given (plugins/zfs/encryption.py:80-88).
- democratic-csi: no ZFS-native encryption. Grep for zfs with encrypt/keyformat/keylocation returned nothing (github:democratic-csi/democratic-csi@69c7ebcd). Only ObjectiveFS passphrase (src/utils/objectivefs.js:65).

## OPERATIONS AROUND IT
- Clones inherit the encryption root. `zfs_crypto_clone_check` rejects keyformat, keylocation, encryption, and pbkdf2iters on clone (libzfs_crypto.c:1159-1182). After `change-key -i` on a child, its clones move to the parent root (tests/zfs-tests/tests/functional/cli_root/zfs_change-key/zfs_change-key_clones.ksh:59-75).
- Raw sends (`send -w`) are received as-is, never re-encrypted or decrypted (zfs-receive.8:135-138). Raw and non-raw incrementals may not mix (155-157). Raw plus redacted is refused (zfs-send.8:589).
- Rotation: `lzc_change_key` (lib/libzfs_core/libzfs_core.c:1867-1891) with NEW_KEY, INHERIT, FORCE_* (include/sys/dsl_crypt.h:75-78) rewraps the master key. Data is not re-encrypted (zfs-load-key.8:232-234). The old wrapped master key stays on disk (zfs-load-key.8:183-186), so rotation is not a compromise remedy. Snapshot handling: unverified. They share the master key, but I found no snapshot-specific rewrap path.
- Boot: `etc/init.d/zfs-load-key.in:35-59` runs after zfs-import and before zfs-mount. It loads only roots with keystatus=unavailable and keylocation not prompt or none. The systemd path is zfs-mount-generator emitting `zfs-load-key@<root>.service` per root (man/man8/zfs-mount-generator.8.in:56-72). Whether https keys wait for network: unverified.

## SEAMS
- KMS: `keylocation=https://...`. OpenZFS already does the GET and client-cert auth, so an endpoint can serve per-dataset keys with no ZFS change. Set at create, or later via `change-key -o keylocation`.
- Local write path: stdin into `zfs create -o keylocation=prompt` or `zfs load-key`, never argv. A 0600 tmpfs file is the fallback.
- CSI: `csi.storage.k8s.io/provisioner-secret-name` and `-namespace` (github:kubernetes-csi/external-provisioner@ee7abf31 pkg/controller/controller.go:90-91) carry the KMS or passphrase credential as a Secret reference. None of the drivers above use it for encryption.
- Best key model: truenas middleware create_rules.py. License: OpenZFS is CDDL-1.0 (zfs-load-key.8:1). The others: unverified.

## GAPS
- No surveyed driver offers a KMS or https keylocation.
- TrueNAS puts passphrases in StorageClass params, where they leak into Kubernetes objects.
- No surveyed driver is shown calling change-key or rotation (unverified: not grepped).
- Ordering of https key loads at boot is undocumented here.
