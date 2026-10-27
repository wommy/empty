import sys
# splice.py <file> <first line> <last line> <replacement file>: replace lines [first, last]
p, a, b, r = sys.argv[1], int(sys.argv[2]), int(sys.argv[3]), sys.argv[4]
lines = open(p).read().split('\n')
new = open(r).read().rstrip('\n').split('\n')
lines[a - 1:b] = new
open(p, 'w').write('\n'.join(lines))
