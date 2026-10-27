import os
os.chdir('/home/user/empty/.claude/worktrees/agent-a031c2bb39fd727ca')
p = 'internal/endpoint/bounds_test.go'
s = open(p).read()
start = s.index("// H-10: the API's word on missing ids is the bound above.")
end = s.index('// H-10: the desired set itself is capped')
s = s[:start] + s[end:]
s = s.replace('\t"fmt"\n\t"os"\n\t"strings"', '\t"fmt"\n\t"strings"', 1)
open(p, 'w').write(s)

p = 'internal/server/server_test.go'
s = open(p).read()
old = 'import (\n\t"context"\n\t"net"\n\t"slices"\n\t"testing"\n'
assert s.count(old) == 1
s = s.replace(old, 'import (\n\t"context"\n\t"fmt"\n\t"net"\n\t"os"\n\t"slices"\n\t"strings"\n\t"testing"\n', 1)
test = '''// H-10: the API's word on missing desired ids is the bound Reconcile
// keeps. The proto comment on desired_volume_ids said every missing id
// is reported; a caller reading it would take an id absent from skipped
// for one that names a volume. The bound is read from an answer, so the
// comment follows the code. Here, not in internal/endpoint: the real-ZFS
// leg runs that binary without the source tree.
func TestProtoDocumentsTheMissingIDBound(t *testing.T) {
	t.Parallel()
	ids := make([]string, 5000)
	for i := range ids {
		ids[i] = fmt.Sprintf("gone-%05d", i)
	}
	r, err := client(t).Reconcile(context.Background(), &pb.ReconcileRequest{DesiredVolumeIds: ids, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	kept := 0
	for _, s := range r.GetSkipped() {
		if s.GetReason() == endpoint.SkipDesiredMissing && !strings.HasPrefix(s.GetTarget(), "(") {
			kept++
		}
	}
	if kept == 0 || kept == len(ids) {
		t.Fatalf("%d of %d missing ids reported: no bound to document", kept, len(ids))
	}
	b, err := os.ReadFile("../../api/zfsendpoint/v1/endpoint.proto")
	if err != nil {
		t.Fatal(err)
	}
	_, msg, _ := strings.Cut(string(b), "message ReconcileRequest {")
	doc, _, _ := strings.Cut(msg, "repeated string desired_volume_ids")
	doc = strings.Join(strings.Fields(strings.ReplaceAll(doc, "//", "")), " ")
	for _, want := range []string{fmt.Sprintf("first %d", kept), "sorted", `"(N more)"`, fmt.Sprintf("more than %d ids", endpoint.MaxDesiredVolumeIDs)} {
		if !strings.Contains(doc, want) {
			t.Errorf("desired_volume_ids comment lacks %q:\\n%s", want, doc)
		}
	}
}

func TestReconcile(t *testing.T) {'''
assert s.count('func TestReconcile(t *testing.T) {') == 1
s = s.replace('func TestReconcile(t *testing.T) {', test, 1)
open(p, 'w').write(s)
