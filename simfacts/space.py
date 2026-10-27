import sys,re
cur_p, proto_p = sys.argv[1], sys.argv[2]
cur = open(cur_p).read(); proto = open(proto_p).read()
def seg(text, start, end):
    i = text.index(start); j = text.index(end, i)
    return text[i:j]
# spaceCheck switch body: from 'case "refreservation", "reservation":' / 'case "refreservation":' up to the end of spaceCheck
cs = seg(cur, '\tcase "refreservation", "reservation":', '\treturn ""\n}\n\n// createSpace')
ps = seg(proto, '\tcase "refreservation":\n\t\t// unique', '\treturn ""\n}\n\n// createSpace')
cur = cur.replace(cs, ps)
cd = seg(cur, '// dirUsed approximates', '// held is zfs_destroy_snaps_nvl')
pd = seg(proto, '// dirUsed is a dataset', '// held is zfs_destroy_snaps_nvl')
cur = cur.replace(cd, pd)
cur = cur.replace('\t"maps"\n', '\t"maps"\n\t"math"\n', 1)
open(cur_p,'w').write(cur)
