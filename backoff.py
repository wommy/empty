import sys
for p in sys.argv[1:]:
    s = open(p).read()
    old = '''	conn, err := grpc.NewClient("unix://"+sock, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	c := pb.NewEndpointServiceClient(conn)'''
    new = '''	// a dial before the socket exists is retried after BaseDelay, not
	// grpc's default 1s
	conn, err := grpc.NewClient("unix://"+sock, grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithConnectParams(grpc.ConnectParams{Backoff: backoff.Config{BaseDelay: time.Millisecond, Multiplier: 2, MaxDelay: 10 * time.Millisecond}}))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	c := pb.NewEndpointServiceClient(conn)'''
    assert old in s, p
    s = s.replace(old, new)
    s = s.replace('''	"google.golang.org/grpc"
''', '''	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
''', 1)
    open(p, 'w').write(s)
