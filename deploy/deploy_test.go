// Package deploy holds no code: its tests pin the shipped unit and manifests
// to the binaries' conventions, so a rename on either side fails here.
package deploy

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	zcsi "github.com/wommy/empty/internal/csi"
	"github.com/wommy/empty/internal/endpoint"
	"github.com/wommy/empty/internal/unixsock"
)

// unitFile returns zfs-endpoint@.service's ExecStart arguments
// (continuation lines joined) and its RuntimeDirectory.
func unitFile(t *testing.T) (args []string, runtimeDir string) {
	t.Helper()
	f, err := os.Open(filepath.Join("systemd", "zfs-endpoint@.service"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var exec strings.Builder
	in := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if v, ok := strings.CutPrefix(line, "RuntimeDirectory="); ok {
			runtimeDir = v
		}
		if v, ok := strings.CutPrefix(line, "ExecStart="); ok {
			in, line = true, v
		}
		if !in {
			continue
		}
		cont := strings.HasSuffix(line, `\`)
		exec.WriteString(strings.TrimSuffix(line, `\`) + " ")
		if !cont {
			in = false
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Fields(exec.String()), runtimeDir
}

func flagValue(args []string, name string) (string, bool) {
	for i, a := range args {
		if v, ok := strings.CutPrefix(a, name+"="); ok {
			return v, true
		}
		if a == name && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

// One socket convention: the unit serves %i.sock (%i is the
// systemd-escaped parent, %I the parent) in its RuntimeDirectory, which is
// exactly unixsock.EndpointAddr(parent), the address zfs-endpoint's default
// -listen and zfs-csi -parent use.
func TestUnitServesTheConventionalSocket(t *testing.T) {
	t.Parallel()
	args, runtimeDir := unitFile(t)
	if len(args) == 0 {
		t.Fatal("no ExecStart")
	}
	if "/run/"+runtimeDir != unixsock.EndpointDir {
		t.Errorf("RuntimeDirectory=%s, want %s", runtimeDir, unixsock.EndpointDir)
	}
	if p, _ := flagValue(args, "-parent"); p != "%I" {
		t.Errorf("-parent %q, want %%I", p)
	}
	listen, ok := flagValue(args, "-listen")
	if !ok {
		t.Fatal("unit sets no -listen")
	}
	for _, parent := range []string{"tank/k8s/vols", "tank/my-vols", "pool/a b/c.d"} {
		got := strings.ReplaceAll(listen, "%i", unixsock.SystemdEscape(parent))
		if want := unixsock.EndpointAddr(parent); got != want {
			t.Errorf("unit serves %s for %s, binaries use %s", got, parent, want)
		}
	}
	// F2: the admin role (Reconcile, NFS export) on a socket of its own,
	// in a directory no pod mounts
	admin, ok := flagValue(args, "-admin-listen")
	if !ok {
		t.Fatal("unit sets no -admin-listen")
	}
	for _, parent := range []string{"tank/k8s/vols", "tank/my-vols"} {
		if got, want := strings.ReplaceAll(admin, "%i", unixsock.SystemdEscape(parent)), unixsock.EndpointAdminAddr(parent); got != want {
			t.Errorf("unit serves admin %s for %s, want %s", got, parent, want)
		}
	}
}

// NFS mode is one drop-in away (ADR 0009): the unit ends its ExecStart
// with an unbraced $ZFS_ENDPOINT_NFS, which systemd splits into words and
// drops when empty (the unit's default), and the shipped example drop-in
// sets only that variable, to -nfs-server= words. An operator never
// restates ExecStart, so the socket convention above still holds.
func TestUnitNFSDropIn(t *testing.T) {
	t.Parallel()
	const env = "ZFS_ENDPOINT_NFS"
	args, _ := unitFile(t)
	if len(args) == 0 || args[len(args)-1] != "$"+env {
		t.Errorf("ExecStart does not end with $%s: %q", env, args)
	}
	unit, _ := directives(t, filepath.Join("systemd", "zfs-endpoint@.service"))
	if !slices.Contains(unit["Environment"], env+"=") {
		t.Errorf("unit Environment=%q: want %s= (empty: not NFS mode)", unit["Environment"], env)
	}

	example := filepath.Join("systemd", "zfs-endpoint@.service.d", "nfs.conf.example")
	b, err := os.ReadFile(example)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "\n[Service]\n") {
		t.Errorf("%s has no [Service] section", example)
	}
	dropIn, _ := directives(t, example)
	if len(dropIn) != 1 || len(dropIn["Environment"]) != 1 {
		t.Fatalf("%s sets %v: want one Environment= line only", example, dropIn)
	}
	v, ok := strings.CutPrefix(dropIn["Environment"][0], env+"=")
	words := strings.Fields(v)
	if !ok || len(words) == 0 {
		t.Fatalf("%s: Environment=%s, want %s=-nfs-server=...", example, dropIn["Environment"][0], env)
	}
	for _, w := range words {
		a, ok := strings.CutPrefix(w, "-nfs-server=")
		if !ok || a == "" {
			t.Errorf("%s: %q is not -nfs-server=<address>", example, w)
		}
		// a placeholder: installed unedited, zfs-endpoint refuses it (78)
		// rather than advertise an address someone else may hold
		if endpoint.ValidNFSServer(a) == nil {
			t.Errorf("%s: %q is a valid address; ship a placeholder the endpoint refuses", example, a)
		}
	}
	// a template, not loaded as is: systemd reads only *.conf drop-ins
	if strings.HasSuffix(example, ".conf") {
		t.Errorf("%s would be loaded unedited", example)
	}
}

// k8sObjects decodes every document in deploy/k8s.
func k8sObjects(t *testing.T) []map[string]any {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("k8s", "*.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no manifests: %v", err)
	}
	var out []map[string]any
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		dec := yaml.NewDecoder(bytes.NewReader(b))
		for {
			var obj map[string]any
			err := dec.Decode(&obj)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatalf("%s: %v", f, err)
			}
			if obj == nil {
				continue
			}
			for _, k := range []string{"apiVersion", "kind", "metadata"} {
				if obj[k] == nil {
					t.Errorf("%s: a document has no %s", f, k)
				}
			}
			out = append(out, obj)
		}
	}
	return out
}

// Object names in deploy/k8s, by kind: each test selects the object it
// pins by name, so a second ClusterRole or ServiceAccount (the node
// plugin's) never silently replaces the controller's.
const (
	namespace      = "zfs-csi"
	controllerName = "zfs-csi-controller"
	nodeName       = "zfs-csi-node"
	className      = "zfs"
)

// object returns the one decoded document of that kind and name; it fails
// the test when there is none or more than one.
func object(t *testing.T, objs []map[string]any, kind, name string) map[string]any {
	t.Helper()
	var found []map[string]any
	for _, o := range objs {
		if o["kind"] == kind && get(o, "metadata", "name") == name {
			found = append(found, o)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d %s objects named %q, want one", len(found), kind, name)
	}
	return found[0]
}

// get walks a decoded document: string keys index maps, ints index lists.
func get(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, _ := v.(map[string]any)
			v = m[k]
		case int:
			l, _ := v.([]any)
			if k >= len(l) {
				return nil
			}
			v = l[k]
		}
	}
	return v
}

func strs(v any) []string {
	var out []string
	l, _ := v.([]any)
	for _, x := range l {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}

// versionTag is an image pinned to a version tag, and maybe also by
// digest (tag@sha256:...); pinnedDigest one pinned by both.
var (
	versionTag   = regexp.MustCompile(`:v\d+\.\d+\.\d+(@sha256:[0-9a-f]{64})?$`)
	pinnedDigest = regexp.MustCompile(`:v\d+\.\d+\.\d+@sha256:[0-9a-f]{64}$`)
)

// The manifests parse, name this driver, need no attach, share one emptyDir
// CSI socket between zfs-csi and the sidecars, and give zfs-csi the host's
// endpoint directory and a -parent (so it dials the unit's socket). Images
// are pinned by version tag, and the sidecars by digest too.
func TestManifests(t *testing.T) {
	t.Parallel()
	objs := k8sObjects(t)
	for _, k := range [][2]string{
		{"Namespace", namespace}, {"ServiceAccount", controllerName}, {"ClusterRole", controllerName},
		{"ClusterRoleBinding", controllerName}, {"Deployment", controllerName},
		{"StorageClass", className}, {"VolumeSnapshotClass", className},
	} {
		object(t, objs, k[0], k[1])
	}
	// one CSIDriver, this one: never csi-driver-nfs's nfs.csi.k8s.io, whose
	// node plugin runs here under this driver's name (node.yaml)
	var drivers []any
	for _, o := range objs {
		if o["kind"] == "CSIDriver" {
			drivers = append(drivers, get(o, "metadata", "name"))
		}
	}
	if len(drivers) != 1 || drivers[0] != zcsi.DefaultDriverName {
		t.Errorf("CSIDrivers %v, want only %s", drivers, zcsi.DefaultDriverName)
	}
	drv := object(t, objs, "CSIDriver", zcsi.DefaultDriverName)
	if get(drv, "spec", "attachRequired") != false || get(drv, "spec", "podInfoOnMount") != false {
		t.Errorf("CSIDriver spec %v: want attachRequired and podInfoOnMount false", get(drv, "spec"))
	}
	if get(object(t, objs, "StorageClass", className), "provisioner") != zcsi.DefaultDriverName ||
		get(object(t, objs, "VolumeSnapshotClass", className), "driver") != zcsi.DefaultDriverName {
		t.Error("a class names another driver")
	}

	pod := get(object(t, objs, "Deployment", controllerName), "spec", "template", "spec")
	vols := map[string]any{}
	for _, v := range get(pod, "volumes").([]any) {
		vols[get(v, "name").(string)] = v
	}
	if get(vols["socket-dir"], "emptyDir") == nil {
		t.Error("socket-dir is not an emptyDir")
	}
	hostDir, _ := get(vols["endpoint-dir"], "hostPath", "path").(string)
	if hostDir == unixsock.EndpointDir || !strings.HasPrefix(hostDir, unixsock.EndpointDir+"/sock/") {
		// F1: the endpoint directory holds every instance's locks and sockets
		t.Errorf("endpoint-dir hostPath %q, want one instance's socket directory under %s/sock/", hostDir, unixsock.EndpointDir)
	}
	// F2: no pod gets an admin socket
	for name, v := range vols {
		if p, _ := get(v, "hostPath", "path").(string); strings.HasPrefix(p+"/", unixsock.EndpointDir+"/admin/") {
			t.Errorf("volume %s mounts the admin socket directory %s", name, p)
		}
	}

	var names []string
	for _, c := range get(pod, "containers").([]any) {
		name := get(c, "name").(string)
		names = append(names, name)
		image, _ := get(c, "image").(string)
		if !versionTag.MatchString(image) {
			t.Errorf("%s: image %q not pinned to a version tag", name, image)
		}
		// the sidecars are released images: tag and digest. zfs-csi has
		// no published image yet, so its digest is a TODO in the manifest
		if name != "zfs-csi" && !pinnedDigest.MatchString(image) {
			t.Errorf("%s: image %q not pinned by digest as well as tag", name, image)
		}
		mounts := map[string]string{}
		for _, m := range get(c, "volumeMounts").([]any) {
			mounts[get(m, "name").(string)] = get(m, "mountPath").(string)
		}
		if mounts["socket-dir"] != "/csi" {
			t.Errorf("%s: socket-dir at %q, want /csi", name, mounts["socket-dir"])
		}
		args := strs(get(c, "args"))
		if name == "zfs-csi" {
			if v, _ := flagValue(args, "-csi-address"); v != "unix:///csi/csi.sock" {
				t.Errorf("zfs-csi -csi-address %q", v)
			}
			parent, ok := flagValue(args, "-parent")
			if !ok || parent == "" {
				t.Errorf("zfs-csi has no -parent: %v", args)
			}
			// the socket -parent dials, in the directory mounted from the
			// host at the same path
			if want := unixsock.EndpointSockDir(parent); hostDir != want || mounts["endpoint-dir"] != want {
				t.Errorf("zfs-csi: endpoint-dir %q at %q, want %s at the same path", hostDir, mounts["endpoint-dir"], want)
			}
			if slices.Contains(args, "-enable-block") {
				t.Error("the example enables block (ADR 0006: opt-in)")
			}
			continue
		}
		if v, _ := flagValue(args, "--csi-address"); v != "/csi/csi.sock" {
			t.Errorf("%s: --csi-address %q", name, v)
		}
	}
	want := []string{"zfs-csi", "csi-provisioner", "csi-resizer", "csi-snapshotter"}
	if !slices.Equal(names, want) {
		t.Errorf("containers %v, want %v", names, want)
	}
}

// directives returns the settings of a unit file or drop-in that are in
// effect, and the names of the settings that are commented out.
func directives(t *testing.T, path string) (active map[string][]string, commented []string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	active = map[string][]string{}
	cont := false
	for line := range strings.Lines(string(b)) {
		line = strings.TrimSpace(line)
		wasCont := cont
		cont = strings.HasSuffix(line, `\`)
		if wasCont {
			continue // an ExecStart argument line
		}
		if c, ok := strings.CutPrefix(line, "#"); ok {
			if k, _, ok := strings.Cut(c, "="); ok && k != "" && !strings.ContainsAny(k, " \t") {
				commented = append(commented, k)
			}
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			active[k] = append(active[k], v)
		}
	}
	return active, commented
}

// noNamespace are settings that create a private mount namespace (zfs
// mounts and exportfs would never reach the host or nfsd) or cut the
// network (exportfs resolves every export's hosts). None may appear in
// the unit or a drop-in, not even commented out as a suggestion.
var noNamespace = []string{
	"ProtectSystem", "ProtectHome", "PrivateTmp", "PrivateDevices",
	"ReadOnlyPaths", "ReadWritePaths", "InaccessiblePaths", "TemporaryFileSystem",
	"BindPaths", "BindReadOnlyPaths", "ProtectKernelTunables", "ProtectKernelModules",
	"ProtectKernelLogs", "ProtectControlGroups", "ProtectProc", "ProcSubset",
	"PrivateMounts", "MountFlags", "PrivateNetwork", "IPAddressDeny", "PrivateUsers",
}

// The unit and its hardening drop-in: audit lines are never rate-limited
// (F8), the hardening keeps mounts on the host (H-16), and what needs the
// real-ZFS VM leg's unit check does not cover (capabilities, the device
// allow-list, memory limits) ships commented out.
func TestUnitLint(t *testing.T) {
	t.Parallel()
	unit, _ := directives(t, filepath.Join("systemd", "zfs-endpoint@.service"))
	for k, want := range map[string]string{
		"LogRateLimitIntervalSec":  "0", // F8
		"RuntimeDirectory":         "zfs-endpoint",
		"RuntimeDirectoryMode":     "0700",
		"UMask":                    "0077",
		"NoNewPrivileges":          "yes",
		"RestartPreventExitStatus": "78",
	} {
		if got := unit[k]; !slices.Equal(got, []string{want}) {
			t.Errorf("unit %s=%q, want %q", k, got, want)
		}
	}
	dropIn, commented := directives(t, filepath.Join("systemd", "zfs-endpoint@.service.d", "hardening.conf"))
	// in effect, each checked under the unit on the VM leg
	// (ci/real-zfs-unit.sh, TestUnitCheckNamesTheFailingSetting)
	for _, k := range []string{"RestrictNamespaces", "KeyringMode", "LimitCORE", "ProtectHostname", "SystemCallFilter", "SystemCallErrorNumber"} {
		if len(dropIn[k]) != 1 {
			t.Errorf("drop-in %s=%q, want one setting in effect", k, dropIn[k])
		}
	}
	// what that check does not cover (NFS export, zvols, scan memory):
	// TODO in the drop-in
	for _, k := range []string{"CapabilityBoundingSet", "AmbientCapabilities", "ProtectClock", "DevicePolicy", "DeviceAllow", "MemoryHigh", "MemoryMax"} {
		if dropIn[k] != nil || unit[k] != nil {
			t.Errorf("%s is in effect, but is not verified on the VM leg", k)
		}
		if !slices.Contains(commented, k) {
			t.Errorf("the drop-in does not carry %s (commented out, with a TODO)", k)
		}
	}
	files, _ := filepath.Glob(filepath.Join("systemd", "*"))
	more, _ := filepath.Glob(filepath.Join("systemd", "*.d", "*"))
	for _, f := range append(files, more...) {
		if fi, err := os.Stat(f); err != nil || fi.IsDir() {
			continue
		}
		if strings.HasPrefix(filepath.Base(f), "zfs-endpoint-reconcile@") {
			continue // a socket client that mounts nothing (TestReconcileTimerIsReportOnly)
		}
		active, commented := directives(t, f)
		for _, k := range noNamespace {
			if active[k] != nil || slices.Contains(commented, k) {
				t.Errorf("%s sets %s: it creates a mount namespace or cuts the network", f, k)
			}
		}
	}
}
