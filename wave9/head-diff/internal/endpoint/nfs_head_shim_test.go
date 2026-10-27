package endpoint

// HEAD has no exposure/covers checks: the verifier is serves (Publish) and
// Lookup (Unpublish) alone.
func (t Etab) exposure(path string, want []NFSClient) []string { return nil }
func (t Etab) covers(path, host string, want []NFSClient) []string {
	return nil
}

func publishWant(row Row, c NFSClient) []NFSClient {
	var cs []NFSClient
	if row.NFSClientsSet {
		cs, _ = decodeClients(row.NFSClients)
	}
	if _, ok := overlap(cs, c); ok {
		return cs
	}
	return sortClients(append(cs, c))
}

func unpublishWant(row Row, host string) []NFSClient {
	var cs []NFSClient
	if row.NFSClientsSet {
		cs, _ = decodeClients(row.NFSClients)
	}
	var out []NFSClient
	for _, x := range cs {
		if p, _ := x.prefix(); p.String() != host {
			out = append(out, x)
		}
	}
	return out
}

func (c NFSClient) crossmntOf() bool { return false }
