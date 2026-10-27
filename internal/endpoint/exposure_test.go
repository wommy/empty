package endpoint

import (
	"slices"
	"testing"
)

// F5: a crossmnt export of the parent's mountpoint or of any ancestor
// serves every volume under it to that line's clients as soon as the
// volume is created. A crossmnt line elsewhere (a sibling with a shared
// prefix, a child) or a line without crossmnt does not.
func TestExposedByAncestor(t *testing.T) {
	t.Parallel()
	const mount = "/tank/k8s/vols"
	for _, tc := range []struct {
		name, etab string
		want       []string
	}{
		{"root crossmnt", "/\t*(rw,sync,crossmnt,no_subtree_check)\n", []string{"/ *(rw,crossmnt)"}},
		{"pool sharenfs=on", "/tank\t*(rw,sync,wdelay,hide,crossmnt,sec=sys,secure,root_squash,no_all_squash)\n", []string{"/tank *(rw,crossmnt)"}},
		{"the parent itself", "/tank/k8s/vols\t10.0.0.0/24(ro,crossmnt)\n", []string{"/tank/k8s/vols 10.0.0.0/24(ro,crossmnt)"}},
		{"escaped ancestor", "/tank/k\\070s\t*(rw,crossmnt)\n", []string{"/tank/k8s *(rw,crossmnt)"}},
		{"sibling sharing a prefix", "/tank/k8s/vols2\t*(rw,crossmnt)\n", nil},
		{"a volume below", "/tank/k8s/vols/pvc-a\t*(rw,crossmnt)\n", nil},
		{"ancestor without crossmnt", "/tank\t*(rw,sync,no_subtree_check)\n/tank/k8s\t10.0.0.0/24(rw,nocrossmnt)\n", nil},
		{"our own exports", "/tank/k8s/vols/pvc-a\t10.0.0.0/24(rw,sync,wdelay,hide,nocrossmnt,sec=sys,secure,root_squash,no_all_squash,no_subtree_check,mountpoint)\n", nil},
		{"empty", "", nil},
	} {
		if got := ExposedByAncestor([]byte(tc.etab), mount); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}
