package zfs

import "testing"

func FuzzClassify(f *testing.F) {
	for _, p := range patterns {
		f.Add("cannot do 'x': " + p.text)
	}
	f.Fuzz(func(t *testing.T, s string) {
		k := classify(s)
		if k < KindUnknown || k > kindLast {
			t.Fatalf("kind %d", k)
		}
	})
}
