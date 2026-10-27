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

var versionTag = regexp.MustCompile(`:v\d+\.\d+\.\d+$`)

// The manifests parse, name this driver, need no attach, share one emptyDir
// CSI socket between zfs-csi and the sidecars, and give zfs-csi the host's
// endpoint directory and a -parent (so it dials the unit's socket). Images
// are pinned by version tag.
func TestManifests(t *testing.T) {
	t.Parallel()
	objs := k8sObjects(t)
	kinds := map[string]map[string]any{}
	for _, o := range objs {
		kinds[o["kind"].(string)] = o
	}
	for _, k := range []string{"Namespace", "ServiceAccount", "ClusterRole", "ClusterRoleBinding", "CSIDriver", "Deployment", "StorageClass", "VolumeSnapshotClass"} {
		if kinds[k] == nil {
			t.Errorf("no %s", k)
		}
	}

	drv := kinds["CSIDriver"]
	if get(drv, "metadata", "name") != zcsi.DefaultDriverName {
		t.Errorf("CSIDriver %v, want %s", get(drv, "metadata", "name"), zcsi.DefaultDriverName)
	}
	if get(drv, "spec", "attachRequired") != false || get(drv, "spec", "podInfoOnMount") != false {
		t.Errorf("CSIDriver spec %v: want attachRequired and podInfoOnMount false", get(drv, "spec"))
	}
	if get(kinds["StorageClass"], "provisioner") != zcsi.DefaultDriverName || get(kinds["VolumeSnapshotClass"], "driver") != zcsi.DefaultDriverName {
		t.Error("a class names another driver")
	}

	pod := get(kinds["Deployment"], "spec", "template", "spec")
	vols := map[string]any{}
	for _, v := range get(pod, "volumes").([]any) {
		vols[get(v, "name").(string)] = v
	}
	if get(vols["socket-dir"], "emptyDir") == nil {
		t.Error("socket-dir is not an emptyDir")
	}
	if get(vols["endpoint-dir"], "hostPath", "path") != unixsock.EndpointDir {
		t.Errorf("endpoint-dir hostPath %v, want %s", get(vols["endpoint-dir"], "hostPath", "path"), unixsock.EndpointDir)
	}

	var names []string
	for _, c := range get(pod, "containers").([]any) {
		name := get(c, "name").(string)
		names = append(names, name)
		image, _ := get(c, "image").(string)
		if !versionTag.MatchString(image) {
			t.Errorf("%s: image %q not pinned to a version tag", name, image)
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
			if v, ok := flagValue(args, "-parent"); !ok || v == "" {
				t.Errorf("zfs-csi has no -parent: %v", args)
			}
			if mounts["endpoint-dir"] != unixsock.EndpointDir {
				t.Errorf("zfs-csi: endpoint-dir at %q, want %s", mounts["endpoint-dir"], unixsock.EndpointDir)
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
