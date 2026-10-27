package endpoint

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/wommy/empty/internal/nfsref"
	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

func hostsOf(e Etab, p string) string {
	var out []string
	for _, c := range e[p] {
		m := c.mode()
		if c.crossmntOf() {
			m += "+crossmnt"
		}
		out = append(out, c.Host+"="+m)
	}
	return strings.Join(out, " ")
}

func TestSimCatalogue(t *testing.T) {
	vals := []string{
		"rw=10.0.0.5/32,ro=10.1.0.0/16", "on", "rw", "ro", "rw=", "rw=10.0.0.1/32:", "rw=10.0.0.1/32::10.0.0.2/32",
		"rw=@10.0.0.0/8", "ro=10.0.0.0/24,rw=10.0.0.0/24", "rw=10.0.0.0/24:10.0.0.0/24", "rw=10.0.0.0/24,crossmnt",
		"sec=krb5,rw=10.0.0.0/24", "sec,rw=10.0.0.1/32", "rw=#", "rw=10.0.0.1/32 0.0.0.0/0", "rw=10.0.0.0/33",
		"rw=10.0.0.0/024", "rw=10.0.0.5", "rw=node-a", "rw=*", "rw=10.0.0.1/32,no_root_squash", "bogus", "",
		"rw=10.0.0.1/32\n/etc *(rw)", "sync", "rw=\"10.0.0.1/32", "anonuid=x,rw=10.0.0.1/32", "rw=[::1]", "rw=[::1",
	}
	ctx := context.Background()
	for _, v := range vals {
		sim := zfstest.New("tank")
		z := zfs.New(sim)
		_ = z.CreateFilesystem(ctx, "tank/v/a", nil, true)
		serr := z.Set(ctx, "tank/v/a", map[string]string{"sharenfs": v})
		got, _ := sim.ReadEtab(ctx)
		simH := hostsOf(ParseEtab(got), diffPath)
		verr := nfsref.ValidateSharenfs(v)
		refH := "(zfs set refused)"
		if verr == nil {
			f, _ := nfsref.ShareFile("", diffPath, v)
			if v == "off" {
				f = ""
			}
			etab, _ := nfsref.ExportfsRA(nfsref.ExportsFiles{Dir: map[string][]byte{"zfs.exports": []byte(f)}}, nil, nil)
			refH = hostsOf(ParseEtab([]byte(etab)), diffPath)
		}
		simS := simH
		if serr != nil {
			simS = "(zfs set refused)"
		}
		mark := " "
		if simS != refH {
			mark = "≠"
		}
		fmt.Printf("%s | %q | sim: %s | ref: %s\n", mark, v, simS, refH)
	}
}
