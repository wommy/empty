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

	zcsi "github.com/wommy/empty/internal/csi"
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
	// livenessprobe v2.19.0 calls no Kubernetes API (deploy has no RBAC)
	"registry.k8s.io/sig-storage/livenessprobe:v2.19.0": func(grant) {},
}

// controller returns the controller Deployment's pod spec, its
// ClusterRole and the Namespace, each selected by name.
func controller(t *testing.T) (pod any, role, ns map[string]any) {
	t.Helper()
	objs := k8sObjects(t)
	pod = get(object(t, objs, "Deployment", controllerName), "spec", "template", "spec")
	return pod, object(t, objs, "ClusterRole", controllerName), object(t, objs, "Namespace", namespace)
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
// and the liveness probe get no service account token; the grace period
// outlasts zfs-csi's drain; the namespace admits the hostPath.
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
		// zfs-csi and the liveness probe call no Kubernetes API
		if apiCaller := name != "zfs-csi" && name != "liveness-probe"; apiCaller != token {
			t.Errorf("%s: token mounted %v; want only the provisioner, resizer and snapshotter to have it", name, token)
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

// upstreamNodeRBAC is what csi-driver-nfs's deploy/<tag>/rbac-csi-nfs.yaml
// grants its node service account (csi-nfs-node-sa), keyed by the pinned
// nfsplugin image tag: bumping it fails TestNodeDaemonSet until this table
// is refreshed from the new tag. At v4.13.6 no binding names the node
// account: the node plugin, the registrar and the liveness probe call no
// Kubernetes API.
var upstreamNodeRBAC = map[string]func(grant){
	"registry.k8s.io/sig-storage/nfsplugin:v4.13.6": func(grant) {},
}

// The node plugin is csi-driver-nfs's node DaemonSet (ADR 0004, ADR 0009)
// under this driver's name: kubelet finds it by the registration path, and
// csi-driver-nfs's own CSIDriver is not shipped (TestManifests). Images
// are pinned by tag and digest; every container has requests and a memory
// limit; only the nfs container is privileged (it mounts, with
// Bidirectional propagation into the pods directory), and the others drop
// every capability, keep a read-only root and mount the plugin socket
// read-only; no container gets a service account token, and the node
// account is granted no more than upstream grants its own.
func TestNodeDaemonSet(t *testing.T) {
	t.Parallel()
	objs := k8sObjects(t)
	ds := object(t, objs, "DaemonSet", nodeName)
	if get(ds, "metadata", "namespace") != namespace {
		t.Errorf("DaemonSet namespace %v, want %s", get(ds, "metadata", "namespace"), namespace)
	}
	pod := get(ds, "spec", "template", "spec")
	if get(pod, "serviceAccountName") != nodeName {
		t.Errorf("serviceAccountName %v, want %s", get(pod, "serviceAccountName"), nodeName)
	}
	object(t, objs, "ServiceAccount", nodeName)
	if get(pod, "automountServiceAccountToken") != false {
		t.Error("automountServiceAccountToken is not false: the node containers call no Kubernetes API")
	}
	// csi-driver-nfs: "original nfs connection would be broken without
	// hostNetwork"; ADR 0001 v2 D8 relies on mounts using host networking
	if get(pod, "hostNetwork") != true || get(pod, "dnsPolicy") != "ClusterFirstWithHostNet" {
		t.Errorf("hostNetwork %v, dnsPolicy %v: want true, ClusterFirstWithHostNet", get(pod, "hostNetwork"), get(pod, "dnsPolicy"))
	}
	if l := get(object(t, objs, "Namespace", namespace), "metadata", "labels", "pod-security.kubernetes.io/enforce"); l != "privileged" {
		t.Errorf("namespace Pod Security enforce %v: baseline and restricted refuse a privileged node plugin", l)
	}

	vols := map[string]any{}
	for _, v := range get(pod, "volumes").([]any) {
		vols[get(v, "name").(string)] = v
	}
	pluginDir := "/var/lib/kubelet/plugins/" + zcsi.DefaultDriverName
	for name, want := range map[string]string{
		"socket-dir":       pluginDir,
		"registration-dir": "/var/lib/kubelet/plugins_registry",
		"pods-mount-dir":   "/var/lib/kubelet/pods",
	} {
		if p := get(vols[name], "hostPath", "path"); p != want {
			t.Errorf("volume %s: hostPath %v, want %s", name, p, want)
		}
	}

	cs := containers(pod)
	var names []string
	for _, c := range get(pod, "containers").([]any) {
		names = append(names, get(c, "name").(string))
	}
	if want := []string{"nfs", "node-driver-registrar", "liveness-probe"}; !slices.Equal(names, want) {
		t.Fatalf("containers %v, want %v", names, want)
	}
	for name, c := range cs {
		image, _ := get(c, "image").(string)
		if !pinnedDigest.MatchString(image) {
			t.Errorf("%s: image %q not pinned by tag and digest", name, image)
		}
		for _, k := range [][]string{{"requests", "cpu"}, {"requests", "memory"}, {"limits", "memory"}} {
			if get(c, "resources", k[0], k[1]) == nil {
				t.Errorf("%s: no resources.%s.%s", name, k[0], k[1])
			}
		}
		sc := get(c, "securityContext")
		mounts := map[string]any{}
		for _, m := range get(c, "volumeMounts").([]any) {
			mounts[get(m, "name").(string)] = m
		}
		if name == "nfs" {
			if get(sc, "privileged") != true {
				t.Error("nfs: not privileged: it mounts NFS and propagates the mounts to kubelet")
			}
			if get(mounts["pods-mount-dir"], "mountPropagation") != "Bidirectional" {
				t.Error("nfs: the pods directory is not mounted Bidirectional: pods would not see the volume")
			}
			continue
		}
		if get(sc, "privileged") == true || get(sc, "allowPrivilegeEscalation") != false ||
			get(sc, "readOnlyRootFilesystem") != true || !slices.Equal(strs(get(sc, "capabilities", "drop")), []string{"ALL"}) {
			t.Errorf("%s: securityContext %v: want unprivileged, no escalation, a read-only root, every capability dropped", name, sc)
		}
		for mname, m := range mounts {
			// the registrar writes its registration socket; nothing else
			// outside the nfs container writes to the host
			if get(m, "readOnly") != true && (name != "node-driver-registrar" || mname != "registration-dir") {
				t.Errorf("%s: %s mounted read-write", name, mname)
			}
			if get(m, "mountPropagation") != nil {
				t.Errorf("%s: %s has mount propagation", name, mname)
			}
		}
	}

	nfsArgs := strs(get(cs["nfs"], "args"))
	if v, _ := flagValue(nfsArgs, "--drivername"); v != zcsi.DefaultDriverName {
		t.Errorf("nfs --drivername %q, want %s", v, zcsi.DefaultDriverName)
	}
	if v, _ := flagValue(nfsArgs, "--endpoint"); v != "unix:///csi/csi.sock" {
		t.Errorf("nfs --endpoint %q, want unix:///csi/csi.sock", v)
	}
	if v, _ := flagValue(nfsArgs, "--nodeid"); v != "$(NODE_ID)" {
		t.Errorf("nfs --nodeid %q, want $(NODE_ID)", v)
	}
	for _, e := range get(cs["nfs"], "env").([]any) {
		if get(e, "name") == "NODE_ID" && get(e, "valueFrom", "fieldRef", "fieldPath") != "spec.nodeName" {
			t.Errorf("NODE_ID from %v, want spec.nodeName", get(e, "valueFrom"))
		}
	}
	for _, name := range []string{"nfs", "node-driver-registrar", "liveness-probe"} {
		var mount any
		for _, m := range get(cs[name], "volumeMounts").([]any) {
			if get(m, "name") == "socket-dir" {
				mount = get(m, "mountPath")
			}
		}
		if mount != "/csi" {
			t.Errorf("%s: socket-dir at %v, want /csi", name, mount)
		}
	}
	reg := strs(get(cs["node-driver-registrar"], "args"))
	if v, _ := flagValue(reg, "--kubelet-registration-path"); v != pluginDir+"/csi.sock" {
		t.Errorf("registrar --kubelet-registration-path %q, want %s/csi.sock", v, pluginDir)
	}
	// both hostNetwork: a port shared with upstream csi-driver-nfs's
	// node plugin (29653) would collide on a node running both
	probe := strs(get(cs["liveness-probe"], "args"))
	ep, _ := flagValue(probe, "--http-endpoint")
	_, port, _ := strings.Cut(ep, ":")
	if !strings.HasPrefix(ep, "localhost:") || port == "29653" {
		t.Errorf("liveness-probe --http-endpoint %q: want localhost, not upstream's port 29653", ep)
	}
	if p := get(cs["nfs"], "livenessProbe", "httpGet", "port"); strconv.Itoa(intOf(p)) != port {
		t.Errorf("nfs livenessProbe port %v, liveness-probe serves %s", p, port)
	}

	// RBAC: whatever names the node account grants no more than upstream
	// grants its own
	image, _, _ := strings.Cut(get(cs["nfs"], "image").(string), "@")
	rules, ok := upstreamNodeRBAC[image]
	if !ok {
		t.Fatalf("no upstream node RBAC recorded for %s: copy what its rbac-csi-nfs.yaml binds csi-nfs-node-sa to into upstreamNodeRBAC", image)
	}
	allowed := grant{}
	rules(allowed)
	roles := map[string]map[string]any{}
	for _, o := range objs {
		if k := o["kind"]; k == "ClusterRole" || k == "Role" {
			roles[k.(string)+"/"+get(o, "metadata", "name").(string)] = o
		}
	}
	for _, o := range objs {
		if k := o["kind"]; k != "ClusterRoleBinding" && k != "RoleBinding" {
			continue
		}
		for _, s := range get(o, "subjects").([]any) {
			if get(s, "kind") != "ServiceAccount" || get(s, "name") != nodeName {
				continue
			}
			role := roles[get(o, "roleRef", "kind").(string)+"/"+get(o, "roleRef", "name").(string)]
			if role == nil {
				t.Errorf("%s %v binds the node account to a role not shipped here", o["kind"], get(o, "metadata", "name"))
				continue
			}
			for _, r := range get(role, "rules").([]any) {
				for _, group := range strs(get(r, "apiGroups")) {
					for _, res := range strs(get(r, "resources")) {
						for _, v := range strs(get(r, "verbs")) {
							if !slices.Contains(allowed[[2]string{group, res}], v) {
								t.Errorf("the node account gets %s %q %s, which upstream does not grant its node account", v, res, group)
							}
						}
					}
				}
			}
		}
	}

	// ADR 0001 v2 D8: F6 is closed by deployment, and the operator reads it here
	b, err := os.ReadFile(filepath.Join("k8s", "node.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"2049", "AUTH_SYS", "ADR 0001"} {
		if !strings.Contains(string(b), s) {
			t.Errorf("node.yaml does not tell the operator about F6 (%q)", s)
		}
	}
}

func intOf(v any) int {
	n, _ := v.(int)
	return n
}

// The controller pod surfaces the endpoint's runtime health: the
// sidecars call Probe only at startup, so a pool that goes NOT_SERVING
// later was invisible from Kubernetes. The upstream livenessprobe sidecar
// serves Probe over HTTP, at the version and digest node.yaml pins,
// hardened as the other sidecars (TestPodHardening covers resources and
// the token), on a port node.yaml's does not use; zfs-csi's
// readinessProbe reads it. Readiness only, no livenessProbe (ADR 0008
// addendum): restarting zfs-csi cannot fix the endpoint, and the sidecars
// exit when its socket goes away. The window outlasts an endpoint restart
// (seconds) so that does not flip the pod.
func TestControllerHealthProbe(t *testing.T) {
	t.Parallel()
	objs := k8sObjects(t)
	pod, _, _ := controller(t)
	cs := containers(pod)
	lp, ok := cs["liveness-probe"]
	if !ok {
		t.Fatal("controller pod has no liveness-probe container: a runtime NOT_SERVING is invisible")
	}
	node := containers(get(object(t, objs, "DaemonSet", nodeName), "spec", "template", "spec"))
	if img := get(lp, "image"); img != get(node["liveness-probe"], "image") {
		t.Errorf("liveness-probe image %v, want node.yaml's pinned %v", img, get(node["liveness-probe"], "image"))
	}
	sc := get(lp, "securityContext")
	if get(sc, "privileged") == true || get(sc, "allowPrivilegeEscalation") != false ||
		get(sc, "readOnlyRootFilesystem") != true || !slices.Equal(strs(get(sc, "capabilities", "drop")), []string{"ALL"}) {
		t.Errorf("liveness-probe securityContext %v: want no escalation, a read-only root, every capability dropped", sc)
	}
	for _, m := range get(lp, "volumeMounts").([]any) {
		if get(m, "readOnly") != true {
			t.Errorf("liveness-probe: %v mounted read-write", get(m, "name"))
		}
	}

	args := strs(get(lp, "args"))
	ep, _ := flagValue(args, "--http-endpoint")
	host, port, _ := strings.Cut(ep, ":")
	nodeEP, _ := flagValue(strs(get(node["liveness-probe"], "args")), "--http-endpoint")
	_, nodePort, _ := strings.Cut(nodeEP, ":")
	if port == "" || port == nodePort || port == "29653" {
		t.Errorf("liveness-probe --http-endpoint %q: want a port of its own, not node.yaml's %s or csi-driver-nfs's 29653", ep, nodePort)
	}
	// kubelet probes the pod IP from the host: localhost would be the
	// node's, and on the pod network it serves nothing there
	if get(pod, "hostNetwork") == true || host != "" {
		t.Errorf("liveness-probe --http-endpoint %q (hostNetwork %v): want :<port> on the pod network", ep, get(pod, "hostNetwork"))
	}
	probeTimeout := 0 * time.Second
	if v, ok := flagValue(args, "--probe-timeout"); ok {
		probeTimeout, _ = time.ParseDuration(v)
	}

	z := cs["zfs-csi"]
	if lv := get(z, "livenessProbe"); lv != nil {
		t.Errorf("zfs-csi livenessProbe %v: restarting zfs-csi cannot fix the endpoint (ADR 0008); use readiness", lv)
	}
	rp := get(z, "readinessProbe")
	if rp == nil {
		t.Fatal("zfs-csi has no readinessProbe")
	}
	if get(rp, "httpGet", "path") != "/healthz" || strconv.Itoa(intOf(get(rp, "httpGet", "port"))) != port || get(rp, "httpGet", "host") != nil {
		t.Errorf("zfs-csi readinessProbe %v: want httpGet /healthz on the pod IP, port %s", get(rp, "httpGet"), port)
	}
	period, fail, timeout := intOf(get(rp, "periodSeconds")), intOf(get(rp, "failureThreshold")), intOf(get(rp, "timeoutSeconds"))
	if period == 0 || fail == 0 || timeout == 0 {
		t.Fatalf("zfs-csi readinessProbe %v: set periodSeconds, failureThreshold and timeoutSeconds", rp)
	}
	if window := period * fail; window < 30 || window > 120 {
		t.Errorf("readiness window %ds: want 30-120s, past an endpoint restart, well inside a stuck endpoint", window)
	}
	if time.Duration(timeout)*time.Second <= probeTimeout {
		t.Errorf("readinessProbe timeoutSeconds %d not above liveness-probe --probe-timeout %v", timeout, probeTimeout)
	}
}
