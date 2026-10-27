package deploy

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// grant is one (apiGroup, resource) and the verbs a rule set gives it.
type grant map[[2]string][]string

func (g grant) add(group, resource string, verbs ...string) {
	k := [2]string{group, resource}
	for _, v := range verbs {
		if !slices.Contains(g[k], v) {
			g[k] = append(g[k], v)
		}
	}
}

// upstreamRBAC is the ClusterRole of each pinned sidecar's upstream
// deploy/kubernetes rbac.yaml at that tag (leases and the other
// namespaced Role rules are left out: no leader election, no capacity).
// Keyed by image tag: bumping a sidecar fails TestSidecarRBAC until this
// table is refreshed from the new tag.
var upstreamRBAC = map[string]func(grant){
	// external-provisioner v5.2.0 deploy/kubernetes/rbac.yaml
	"registry.k8s.io/sig-storage/csi-provisioner:v5.2.0": func(g grant) {
		g.add("", "persistentvolumes", "get", "list", "watch", "create", "patch", "delete")
		g.add("", "persistentvolumeclaims", "get", "list", "watch", "update")
		g.add("storage.k8s.io", "storageclasses", "get", "list", "watch")
		g.add("", "events", "list", "watch", "create", "update", "patch")
		g.add("snapshot.storage.k8s.io", "volumesnapshots", "get", "list")
		g.add("snapshot.storage.k8s.io", "volumesnapshotcontents", "get", "list")
		g.add("storage.k8s.io", "csinodes", "get", "list", "watch")
		g.add("", "nodes", "get", "list", "watch")
		g.add("storage.k8s.io", "volumeattachments", "get", "list", "watch")
	},
	// external-resizer v1.13.1 deploy/kubernetes/rbac.yaml
	"registry.k8s.io/sig-storage/csi-resizer:v1.13.1": func(g grant) {
		g.add("", "persistentvolumes", "get", "list", "watch", "patch")
		g.add("", "persistentvolumeclaims", "get", "list", "watch")
		g.add("", "pods", "get", "list", "watch") // --handle-volume-inuse-error
		g.add("", "persistentvolumeclaims/status", "patch")
		g.add("", "events", "list", "watch", "create", "update", "patch")
		g.add("storage.k8s.io", "volumeattributesclasses", "get", "list", "watch")
	},
	// external-snapshotter v8.2.0 deploy/kubernetes/csi-snapshotter/rbac-csi-snapshotter.yaml
	"registry.k8s.io/sig-storage/csi-snapshotter:v8.2.0": func(g grant) {
		g.add("", "events", "list", "watch", "create", "update", "patch")
		g.add("snapshot.storage.k8s.io", "volumesnapshotclasses", "get", "list", "watch")
		g.add("snapshot.storage.k8s.io", "volumesnapshotcontents", "get", "list", "watch", "update", "patch")
		g.add("snapshot.storage.k8s.io", "volumesnapshotcontents/status", "update", "patch")
		// group snapshots: behind a feature gate that is off by default
		g.add("groupsnapshot.storage.k8s.io", "volumegroupsnapshotclasses", "get", "list", "watch")
		g.add("groupsnapshot.storage.k8s.io", "volumegroupsnapshotcontents", "get", "list", "watch", "update", "patch")
		g.add("groupsnapshot.storage.k8s.io", "volumegroupsnapshotcontents/status", "update", "patch")
	},
}

// controller returns the decoded Deployment's pod spec, the
// ClusterRole and the Namespace.
func controller(t *testing.T) (pod any, role, ns map[string]any) {
	t.Helper()
	for _, o := range k8sObjects(t) {
		switch o["kind"] {
		case "Deployment":
			pod = get(o, "spec", "template", "spec")
		case "ClusterRole":
			role = o
		case "Namespace":
			ns = o
		}
	}
	if pod == nil || role == nil || ns == nil {
		t.Fatal("no Deployment, ClusterRole or Namespace")
	}
	return pod, role, ns
}

func containers(pod any) map[string]any {
	out := map[string]any{}
	for _, c := range get(pod, "containers").([]any) {
		out[get(c, "name").(string)] = c
	}
	return out
}

// The ClusterRole grants what each pinned sidecar's informers need (else
// the sidecar waits for a cache sync that never completes: csi-resizer
// without pods never expanded a volume) and nothing upstream does not
// grant (volumesnapshots update/patch were granted to nobody upstream).
func TestSidecarRBAC(t *testing.T) {
	t.Parallel()
	pod, role, _ := controller(t)
	have := grant{}
	for _, r := range get(role, "rules").([]any) {
		for _, group := range strs(get(r, "apiGroups")) {
			for _, res := range strs(get(r, "resources")) {
				have.add(group, res, strs(get(r, "verbs"))...)
			}
		}
	}
	need, allowed := grant{}, grant{}
	for name, c := range containers(pod) {
		if name == "zfs-csi" {
			continue
		}
		image, _ := get(c, "image").(string)
		image, _, _ = strings.Cut(image, "@")
		rules, ok := upstreamRBAC[image]
		if !ok {
			t.Errorf("%s: no upstream RBAC recorded for %s: copy its ClusterRole into upstreamRBAC", name, image)
			continue
		}
		rules(allowed)
		up := grant{}
		rules(up)
		args := strs(get(c, "args"))
		if name == "csi-resizer" && slices.Contains(args, "--handle-volume-inuse-error=false") {
			delete(up, [2]string{"", "pods"}) // no pod informer
		}
		for k := range up {
			if k[0] == "groupsnapshot.storage.k8s.io" && !slices.ContainsFunc(args, func(a string) bool {
				return strings.HasPrefix(a, "--feature-gates=") && strings.Contains(a, "CSIVolumeGroupSnapshot=true")
			}) {
				delete(up, k)
			}
		}
		for k, verbs := range up {
			need.add(k[0], k[1], verbs...)
		}
	}
	for k, verbs := range need {
		for _, v := range verbs {
			if !slices.Contains(have[k], v) {
				t.Errorf("ClusterRole lacks %s %q %s, which a sidecar's upstream RBAC grants", v, k[1], k[0])
			}
		}
	}
	for k, verbs := range have {
		for _, v := range verbs {
			if !slices.Contains(allowed[k], v) {
				t.Errorf("ClusterRole grants %s %q %s, which no sidecar's upstream RBAC does", v, k[1], k[0])
			}
		}
	}
	if vs := have[[2]string{"snapshot.storage.k8s.io", "volumesnapshots"}]; !slices.Equal(vs, []string{"get", "list"}) {
		t.Errorf("volumesnapshots verbs %v, want [get list] (the provisioner's restores)", vs)
	}
	resizer := strs(get(containers(pod)["csi-resizer"], "args"))
	if !slices.Contains(resizer, "--handle-volume-inuse-error=false") && !slices.Contains(have[[2]string{"", "pods"}], "watch") {
		t.Error("csi-resizer handles volume-in-use errors (the default) without pods get/list/watch: it never syncs")
	}
}

// The controller is pinned by a label a kubelet cannot set on its own Node
// (NodeRestriction), and the operator instructions name that label.
func TestNodePinLabel(t *testing.T) {
	t.Parallel()
	pod, _, _ := controller(t)
	const prefix = "node-restriction.kubernetes.io/"
	sel, _ := get(pod, "nodeSelector").(map[string]any)
	if len(sel) == 0 {
		t.Fatal("no nodeSelector: the controller must run on the ZFS host")
	}
	b, err := os.ReadFile(filepath.Join("k8s", "controller.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range sel {
		if !strings.HasPrefix(k, prefix) {
			t.Errorf("nodeSelector %s: a kubelet can set it on its own Node; want the %s prefix", k, prefix)
		}
		if line := "kubectl label node <host> " + k + "=" + v.(string); !strings.Contains(string(b), line) {
			t.Errorf("controller.yaml does not tell the operator %q", line)
		}
	}
	if a := get(pod, "affinity", "nodeAffinity"); a != nil {
		t.Errorf("node affinity %v: pin with the nodeSelector only, so the prefix check covers it", a)
	}
	if !strings.Contains(string(b), "exactly one node") {
		t.Error("controller.yaml does not say to label exactly one node")
	}
}

// Every container: requests and a memory limit (else BestEffort, first
// evicted on the storage host); a hostPath is mounted read-only; zfs-csi
// gets no service account token; the grace period outlasts zfs-csi's drain;
// the namespace admits the hostPath.
func TestPodHardening(t *testing.T) {
	t.Parallel()
	pod, _, ns := controller(t)
	vols := map[string]any{}
	for _, v := range get(pod, "volumes").([]any) {
		vols[get(v, "name").(string)] = v
	}
	hostPath := false
	for name, c := range containers(pod) {
		for _, k := range [][]string{{"requests", "cpu"}, {"requests", "memory"}, {"limits", "memory"}} {
			if get(c, "resources", k[0], k[1]) == nil {
				t.Errorf("%s: no resources.%s.%s", name, k[0], k[1])
			}
		}
		token := false
		for _, m := range get(c, "volumeMounts").([]any) {
			v := vols[get(m, "name").(string)]
			if get(v, "hostPath") != nil {
				hostPath = true
				if get(m, "readOnly") != true {
					t.Errorf("%s: hostPath %v mounted read-write", name, get(m, "name"))
				}
			}
			srcs, _ := get(v, "projected", "sources").([]any)
			for _, src := range srcs {
				if get(src, "serviceAccountToken") != nil {
					token = true
					if get(m, "mountPath") != "/var/run/secrets/kubernetes.io/serviceaccount" {
						t.Errorf("%s: token at %v, not where client-go's in-cluster config reads it", name, get(m, "mountPath"))
					}
				}
			}
		}
		if (name == "zfs-csi") == token {
			t.Errorf("%s: token mounted %v; want only the sidecars to have it", name, token)
		}
	}
	if get(pod, "automountServiceAccountToken") != false {
		t.Error("automountServiceAccountToken is not false: zfs-csi gets the token")
	}
	if hostPath {
		if l := get(ns, "metadata", "labels", "pod-security.kubernetes.io/enforce"); l != "privileged" {
			t.Errorf("namespace Pod Security enforce %v: baseline and restricted refuse the hostPath", l)
		}
	}

	src, err := os.ReadFile(filepath.Join("..", "cmd", "zfs-csi", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`fs\.Duration\("drain", (\d+) ?\* ?time\.Second`).FindSubmatch(src)
	if m == nil {
		t.Fatal("cannot find zfs-csi's -drain default")
	}
	secs, _ := strconv.Atoi(string(m[1]))
	drain := time.Duration(secs) * time.Second
	if v, ok := flagValue(strs(get(containers(pod)["zfs-csi"], "args")), "-drain"); ok {
		if drain, err = time.ParseDuration(v); err != nil {
			t.Fatal(err)
		}
	}
	grace, _ := get(pod, "terminationGracePeriodSeconds").(int)
	if time.Duration(grace)*time.Second <= drain {
		t.Errorf("terminationGracePeriodSeconds %d (unset: 30), want above zfs-csi's -drain %v", grace, drain)
	}
}
