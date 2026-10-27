import sys
# mutant.py SIM OUT: the "copies doubles the thick reservation" belief
# (ncopies read from copies) in the Sim's create path, for showing VOL-12
# discriminates.
s = open(sys.argv[1]).read()
old = '!sparse || r == "auto" {\n\t\t\tn.props["refreservation"] = autoResv(n.props["volsize"], s.volblocksize(n))\n\t\t}\n\t}'
assert s.count(old) == 1
new = ('!sparse || r == "auto" {\n\t\t\tn.props["refreservation"] = autoResv(n.props["volsize"], s.volblocksize(n))\n'
       '\t\t\tif c, _ := strconv.ParseUint(props["copies"], 10, 64); c > 1 {\n'
       '\t\t\t\tr, _ := strconv.ParseUint(n.props["refreservation"], 10, 64)\n'
       '\t\t\t\tn.props["refreservation"] = strconv.FormatUint(r*c, 10)\n'
       '\t\t\t}\n\t\t}\n\t}')
open(sys.argv[2], 'w').write(s.replace(old, new))
