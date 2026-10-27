import sys
root = sys.argv[1]
p = root + '/api/zfsendpoint/v1/endpoint.proto'
s = open(p).read()

def sub(old, new):
    global s
    assert old in s, old[:80]
    s = s.replace(old, new, 1)

sub('''  // the parent dataset this endpoint manages
  string parent = 2;
  string version = 3;
}''', '''  // the parent dataset this endpoint manages
  string parent = 2;
  string version = 3;
  // The NFS server addresses (IPv4 or DNS names) clients mount this
  // endpoint's volumes from, in order (-nfs-server). Non-empty only with
  // CAPABILITY_FILE_EXPORT; a front end serving volumes over NFS needs one
  // (CreateVolumeRequest.nfs_export.server).
  repeated string nfs_servers = 4;
}''')
sub('''  string device_path = 8; // BLOCK, on the storage host
  string mountpoint = 9;  // FILESYSTEM
}''', '''  string device_path = 8; // BLOCK, on the storage host
  string mountpoint = 9;  // FILESYSTEM
  // FILESYSTEM, unset when the volume has neither: server is the NFS server
  // address recorded at create (set on the volume itself, never inherited
  // or received), fixed for the volume's life; clients and read_only are
  // its local sharenfs when that is in the endpoint's grammar, read fresh
  // (read_only: every client is read-only).
  NFSExport nfs = 10;
}

// A static NFS export, set with the volume at create.
message NFSExport {
  // IPv4 networks, each written with its prefix length and host bits zero
  // ("10.20.0.0/24", "10.20.0.5/32"), none broader than the endpoint's
  // -nfs-min-prefix, none overlapping another.
  repeated string clients = 1;
  bool read_only = 2;
  // One of GetCapabilitiesResponse.nfs_servers; empty is the first. A
  // committed volume keeps the server it was created with: another one
  // explicitly is ALREADY_EXISTS, an empty one returns the recorded one.
  string server = 3;
}''')
sub('''  map<string, string> properties = 9;
}''', '''  map<string, string> properties = 9;
  // FILESYSTEM only (BLOCK is INVALID_ARGUMENT), and only when the endpoint
  // advertises nfs_servers (else FAILED_PRECONDITION). The volume is
  // created with this export and committed once the NFS server serves
  // every client (else UNAVAILABLE, uncommitted, and a retry converges).
  // A repeat is compatible only with the same clients and mode (in any
  // order) and the same or no server.
  NFSExport nfs_export = 10;
}''')
open(p, 'w').write(s)
