package endpoint_test

import (
	"context"
	"strings"
	"testing"

	"github.com/wommy/empty/internal/endpoint"
	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

// ADR 0003: the endpoint's state lives under its own user-property
// namespace by default; democratic-csi's is the adoption mode.
func TestDefaultPropertyPrefixIsOurOwn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sim := zfstest.New("tank")
	ep, err := endpoint.New(zfs.New(sim), endpoint.Config{Parent: "tank"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "pvc"}); err != nil {
		t.Fatal(err)
	}
	if sim.Prop("tank/pvc", "zfs-endpoint:managed_resource") != "true" || sim.Prop("tank/pvc", "democratic-csi:managed_resource") != "" {
		t.Fatal("default prefix is not zfs-endpoint")
	}
}

// A parent is refused at construction when a 128-byte CSI name would not
// fit under it (zfs names are at most 255 bytes); at the bound, the longest
// volume and snapshot ids still work.
func TestOverlongParentIsRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sim := zfstest.New("tank")
	z := zfs.New(sim)
	// 126 = 255 - len("/") - 128 (endpoint.MaxParentLen, spelled out so the
	// test also builds, and fails, against a tree without it)
	at := "tank/" + strings.Repeat("p", 126-len("tank/"))
	over := at + "q"
	for _, p := range []string{at, over} {
		if err := z.CreateFilesystem(ctx, p, nil, true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := endpoint.New(z, endpoint.Config{Parent: over}); err == nil {
		t.Fatalf("parent of %d bytes accepted", len(over))
	}
	ep, err := endpoint.New(z, endpoint.Config{Parent: at})
	if err != nil {
		t.Fatal(err)
	}
	name := strings.Repeat("v", 128)
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: name}); err != nil {
		t.Fatalf("128-byte name under a %d-byte parent: %v", len(at), err)
	}
	// the longest snapshot id: a 91-byte volume, "@", a hashed component
	short := strings.Repeat("w", 91)
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: short}); err != nil {
		t.Fatal(err)
	}
	if s, err := ep.CreateSnapshot(ctx, strings.Repeat("s", 128), short); err != nil || len(s.ID) != 128 {
		t.Fatalf("snapshot: %+v %v", s, err)
	}
}

// A prefix is refused at startup unless every name built from it is a
// valid OpenZFS user property (zfs_prop.c zfs_prop_user: a-z 0-9 - _ . :;
// zfs_check_userprops: shorter than ZAP_MAXNAMELEN, 256). zfs would
// otherwise refuse each create with a bad property name, after startup.
func TestPropertyPrefixIsValidated(t *testing.T) {
	t.Parallel()
	sim := zfstest.New("tank")
	for _, p := range []string{"democratic-csi", "zfs-endpoint", "com.example", "a:b", "x_1", strings.Repeat("a", 224)} {
		if _, err := endpoint.New(zfs.New(sim), endpoint.Config{Parent: "tank", PropertyPrefix: p}); err != nil {
			t.Errorf("%q: %v", p, err)
		}
	}
	for _, p := range []string{"Democratic-CSI", "has space", "a/b", "a@b", "é", "x\n", strings.Repeat("a", 225)} {
		if _, err := endpoint.New(zfs.New(sim), endpoint.Config{Parent: "tank", PropertyPrefix: p}); err == nil {
			t.Errorf("%q accepted", p)
		}
	}
}
