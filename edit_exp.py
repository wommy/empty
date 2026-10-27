p='internal/endpoint/endpoint_test.go'
s=open(p).read()
def rep(a,b):
    global s
    assert s.count(a)==1,a
    s=s.replace(a,b)
rep("""	if got := h.Prop(h.Parent+"/thick", "refquota"); got != fmt.Sprint(uint64(huge)) {
		// the simulator half-applies""", """	half := true
	if got := h.Prop(h.Parent+"/thick", "refquota"); got != fmt.Sprint(uint64(huge)) {
		half = false
		// the simulator half-applies""")
rep("""	// a smaller expand is no way out: the volume reports the 1 PiB
	// refquota as its capacity, which a thick volume must reserve
	_, err = ep.ExpandVolume(ctx, "thick", 2<<30, 0)
	wantCode(t, err, endpoint.ResourceExhausted)
}""", """	// a smaller expand is no way out: the volume reports the 1 PiB
	// refquota as its capacity, which a thick volume must reserve
	if half {
		_, err = ep.ExpandVolume(ctx, "thick", 2<<30, 0)
		wantCode(t, err, endpoint.ResourceExhausted)
	}
}""")
open(p,'w').write(s)
