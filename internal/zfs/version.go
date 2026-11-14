package zfs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Version is what `zfs version` reports: the userland tools and the loaded
// kernel module, e.g. "zfs-2.4.0-1" and "zfs-kmod-2.4.0-1".
type Version struct {
	Userland string
	Kernel   string
}

// Version runs `zfs version -j` (OpenZFS 2.3+), falling back to the plain
// two-line form for older userlands, which do not know -j.
//
// zfs_do_version (cmd/zfs/zfs_main.c) prints, with -j,
//
//	{"output_version": {...}, "zfs_version": {"userland": ZFS_META_ALIAS,
//	 "kernel": "zfs-kmod-" + /sys/module/zfs/version}}
//
// and without it ZFS_META_ALIAS and "zfs-kmod-<version>" on two lines. Both
// fail when the module is not loaded.
func (c *Client) Version(ctx context.Context) (Version, error) {
	out, err := c.run(ctx, "version", "", "version", "-j")
	if err == nil {
		return parseVersionJSON(out)
	}
	if ctx.Err() != nil {
		return Version{}, err
	}
	out, err2 := c.run(ctx, "version", "", "version")
	if err2 != nil {
		return Version{}, fmt.Errorf("zfs version -j: %w; zfs version: %w", err, err2)
	}
	return parseVersionText(out)
}

func parseVersionJSON(out []byte) (Version, error) {
	var doc struct {
		ZFSVersion *struct {
			Userland string `json:"userland"`
			Kernel   string `json:"kernel"`
		} `json:"zfs_version"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return Version{}, fmt.Errorf("zfs version -j: %w", err)
	}
	if doc.ZFSVersion == nil || doc.ZFSVersion.Userland == "" || doc.ZFSVersion.Kernel == "" {
		return Version{}, fmt.Errorf("zfs version -j: no zfs_version.userland/kernel in %q", out)
	}
	return Version{Userland: doc.ZFSVersion.Userland, Kernel: doc.ZFSVersion.Kernel}, nil
}

func parseVersionText(out []byte) (Version, error) {
	var v Version
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "zfs-kmod-"):
			v.Kernel = line
		case strings.HasPrefix(line, "zfs-"):
			v.Userland = line
		}
	}
	if v.Userland == "" || v.Kernel == "" {
		return Version{}, fmt.Errorf("zfs version: cannot read userland and kernel from %q", out)
	}
	return v, nil
}

// Require checks that both the userland and the kernel module are of the
// release series, e.g. "2.4" accepts zfs-2.4.0-1 and zfs-kmod-2.4.1, and
// refuses zfs-2.3.4, zfs-2.40.0, or a 2.4 userland on a 2.3 module.
func (v Version) Require(series string) error {
	var bad []string
	if !strings.HasPrefix(v.Userland, "zfs-"+series+".") {
		bad = append(bad, "userland "+v.Userland)
	}
	if !strings.HasPrefix(v.Kernel, "zfs-kmod-"+series+".") {
		bad = append(bad, "kernel module "+v.Kernel)
	}
	if len(bad) > 0 {
		return fmt.Errorf("OpenZFS %s.x is required for both userland and kernel module; found %s", series, strings.Join(bad, ", "))
	}
	return nil
}
