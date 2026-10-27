package endpoint_test

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/wommy/empty/internal/endpoint"
)

// Property round trip, as a table generated from the allowlist itself:
// every (kind, property, value) that ValidateProperties accepts is created,
// read back and retried. On the simulator this checks the endpoint's
// canonical tables against the simulator's copy of zfs_prop.c; with
// ZFS_TEST_POOL it checks them against the real OpenZFS, for every value,
// not the 7 the contract tests happen to use.
//
// The candidate values are every name in OpenZFS 2.4's index tables for
// these properties (zfs_prop.c), the 2.2 aliases, and every power of two
// from 512 up to 16M; the allowlist decides which become rows.

type propRow struct {
	kind       endpoint.VolumeKind
	key, value string
	canon      string
}

func allowlistRows() []propRow {
	words := []string{"on", "off", "lzjb", "gzip", "zle", "lz4", "zstd", "zstd-fast", "standard", "always", "disabled",
		"latency", "throughput", "verify", "sa", "dir", "posix", "nfsv4", "noacl", "posixacl", "hidden", "visible",
		"1", "2", "3", "sha256", "skein", "edonr", "blake3", "restricted", "passthrough"}
	for i := 1; i <= 9; i++ {
		words = append(words, "gzip-"+strconv.Itoa(i))
	}
	for i := 1; i <= 19; i++ {
		words = append(words, "zstd-"+strconv.Itoa(i))
	}
	for _, i := range []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 20, 30, 40, 50, 60, 70, 80, 90, 100, 500, 1000} {
		words = append(words, "zstd-fast-"+strconv.Itoa(i))
	}
	for n := 512; n <= 16<<20; n *= 2 {
		words = append(words, strconv.Itoa(n))
		if n >= 1024 {
			words = append(words, strconv.Itoa(n>>10)+"K")
		}
	}
	keys := []string{"compression", "recordsize", "atime", "relatime", "sync", "logbias", "dedup", "xattr",
		"acltype", "snapdir", "copies", "volblocksize"}
	var rows []propRow
	for _, kind := range []endpoint.VolumeKind{endpoint.Filesystem, endpoint.Block} {
		for _, k := range keys {
			seen := map[string]bool{}
			for _, v := range words {
				canon, err := endpoint.ValidateProperties(kind, map[string]string{k: v})
				if err != nil || seen[k+"="+v] {
					continue
				}
				seen[k+"="+v] = true
				rows = append(rows, propRow{kind, k, v, canon[k]})
			}
		}
	}
	return rows
}

// propRTShard reads PROPRT_SHARD=<i>/<n>: this process runs the rows whose
// index is i mod n. The real-ZFS VM (ci/real-zfs-guest.sh) runs the test in
// each of its n endpoint shards, so its ~200 real-pool round trips spread
// over n processes instead of one; ci/check-split.sh checks that every row
// ran in exactly one shard. Unset, it runs every row (0/1).
func propRTShard(t *testing.T) (shard, n int) {
	v := os.Getenv("PROPRT_SHARD")
	if v == "" {
		return 0, 1
	}
	a, b, ok := strings.Cut(v, "/")
	shard, err1 := strconv.Atoi(a)
	n, err2 := strconv.Atoi(b)
	if !ok || err1 != nil || err2 != nil || n < 1 || shard < 0 || shard >= n {
		t.Fatalf("PROPRT_SHARD=%q: want <i>/<n> with 0 <= i < n", v)
	}
	return shard, n
}

func TestPropertyRoundTrip(t *testing.T) {
	t.Parallel()
	rows := allowlistRows()
	if len(rows) < 100 {
		t.Fatalf("only %d allowlisted values found: the candidate list is stale", len(rows))
	}
	shard, n := propRTShard(t)
	mine := 0
	for i := range rows {
		if i%n == shard {
			mine++
		}
	}
	// ci/check-split.sh reads this line from every shard's log
	t.Logf("PROPRT_SHARD %d/%d: %d of %d rows", shard, n, mine, len(rows))
	ep, h := setup(t)
	for i, r := range rows {
		if i%n != shard {
			continue
		}
		t.Run(fmt.Sprintf("%s/%s=%s", r.kind, r.key, r.value), func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			req := endpoint.CreateVolumeRequest{Name: fmt.Sprintf("p%03d", i), Kind: r.kind, CapacityBytes: 1 << 20,
				Sparse: r.kind == endpoint.Block, Properties: map[string]string{r.key: r.value}}
			if _, err := ep.CreateVolume(ctx, req); err != nil {
				t.Fatalf("create: %v", err)
			}
			ds := h.Parent + "/" + req.Name
			// volblocksize is fixed at create: zfs prints its source as "-"
			// ("default" at the default size), never "local"
			if got, src := h.propSource(t, ds, r.key); got != r.canon || src != "local" && r.key != "volblocksize" {
				t.Errorf("reads back %q (%s), the endpoint expects %q (local)", got, src, r.canon)
			}
			if _, err := ep.CreateVolume(ctx, req); err != nil {
				t.Errorf("identical retry: %v", err)
			}
		})
	}
}

// The table's own shape: each property the allowlist names has rows, so a
// rule that stops accepting anything shows up here, not as a quiet gap.
func TestPropertyRoundTripCoversEveryKey(t *testing.T) {
	t.Parallel()
	byKey := map[string]int{}
	for _, r := range allowlistRows() {
		byKey[r.key]++
	}
	var missing []string
	for _, k := range []string{"compression", "recordsize", "atime", "relatime", "sync", "logbias", "dedup", "xattr",
		"acltype", "snapdir", "copies", "volblocksize"} {
		if byKey[k] == 0 {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("no allowlisted value for %v", missing)
	}
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		t.Logf("%-13s %3d values", k, byKey[k])
	}
}
