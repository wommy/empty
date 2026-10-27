p='deploy/deploy_test.go'
s=open(p).read()
def rep(a,b):
    global s
    assert s.count(a)==1, a
    s=s.replace(a,b)
rep('''// get walks a decoded document''','''// Object names in deploy/k8s, by kind: each test selects the object it
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

// get walks a decoded document''')
rep('''	objs := k8sObjects(t)
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

	pod := get(kinds["Deployment"], "spec", "template", "spec")''','''	objs := k8sObjects(t)
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

	pod := get(object(t, objs, "Deployment", controllerName), "spec", "template", "spec")''')
open(p,'w').write(s)

p='deploy/k8s_test.go'
s=open(p).read()
rep('''// controller returns the decoded Deployment's pod spec, the
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
}''','''// controller returns the controller Deployment's pod spec, its
// ClusterRole and the Namespace, each selected by name.
func controller(t *testing.T) (pod any, role, ns map[string]any) {
	t.Helper()
	objs := k8sObjects(t)
	pod = get(object(t, objs, "Deployment", controllerName), "spec", "template", "spec")
	return pod, object(t, objs, "ClusterRole", controllerName), object(t, objs, "Namespace", namespace)
}''')
open(p,'w').write(s)
