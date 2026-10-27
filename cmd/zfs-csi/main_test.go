package main

import "testing"

func TestEndpointTargetIsAnAbsoluteSocketPath(t *testing.T) {
	t.Parallel()
	if err := endpointTarget("unix:///run/zfs-endpoint/endpoint.sock"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"unix://@zfs-endpoint", "unix:@zfs-endpoint", "unix://run/x.sock", "localhost:9000", "dns:///endpoint:9000"} {
		if endpointTarget(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
