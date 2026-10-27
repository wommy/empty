import sys
p = sys.argv[1]
s = open(p).read()
old = '''	snapsOfVol  map[string][]int // volume id -> snapshot rows'''
new = '''	snapsOfVol  map[string][2]int // volume id -> its snapshots, rows[lo:hi]'''
assert old in s; s = s.replace(old, new)
old = '''	t := Table{rows: rows, snapsOfVol: map[string][]int{}, clonesOfSrc: map[string][]int{}, covered: map[string]bool{}}
	for _, v := range vols {
		t.covered[v] = true
	}
	for i, r := range rows {
		if vol, _, ok := strings.Cut(r.ID, "@"); ok {
			t.snapsOfVol[vol] = append(t.snapsOfVol[vol], i)
		}
		if r.Origin != "" {
			t.clonesOfSrc[r.Origin] = append(t.clonesOfSrc[r.Origin], i)
		}
	}
	return t
}'''
new = '''	t := Table{rows: rows, clonesOfSrc: map[string][]int{}, covered: map[string]bool{}}
	for _, v := range vols {
		t.covered[v] = true
	}
	// In ID order every id starting with "<vol>@" sits in one run, so a
	// volume's snapshots are a range of rows: no slice per volume.
	runs := 0
	for i := 0; i < len(rows); i = snapRun(rows, i) {
		if strings.Contains(rows[i].ID, "@") {
			runs++
		}
	}
	t.snapsOfVol = make(map[string][2]int, runs)
	for i := 0; i < len(rows); {
		j := snapRun(rows, i)
		if vol, _, ok := strings.Cut(rows[i].ID, "@"); ok {
			t.snapsOfVol[vol] = [2]int{i, j}
		}
		i = j
	}
	for i, r := range rows {
		if r.Origin != "" {
			t.clonesOfSrc[r.Origin] = append(t.clonesOfSrc[r.Origin], i)
		}
	}
	return t
}

// snapRun returns the end of the run of snapshots of one volume starting
// at rows[i], or i+1 if rows[i] is not a snapshot.
func snapRun(rows []Row, i int) int {
	vol, _, ok := strings.Cut(rows[i].ID, "@")
	j := i + 1
	if !ok {
		return j
	}
	for j < len(rows) {
		id := rows[j].ID
		if len(id) <= len(vol) || id[len(vol)] != '@' || id[:len(vol)] != vol {
			break
		}
		j++
	}
	return j
}'''
assert old in s; s = s.replace(old, new)
old = '''func (t Table) SnapshotsOf(id string) iter.Seq[Row] { return t.pick(t.snapsOfVol[id]) }'''
new = '''func (t Table) SnapshotsOf(id string) iter.Seq[Row] {
	r := t.snapsOfVol[id] // absent: {0, 0}, no rows
	return slices.Values(t.rows[r[0]:r[1]])
}'''
assert old in s; s = s.replace(old, new)
open(p, 'w').write(s)
