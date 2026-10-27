import sys
dst, src = sys.argv[1], sys.argv[2]
open(dst, 'a').write(open(src).read())
