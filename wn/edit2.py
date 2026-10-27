p='ci/real-zfs-weights.txt'
L=open(p).read().split('\n')
out=[]
for l in L:
    if l.startswith("# note: part 0's tests here"):
        continue  # superseded by the per-part normalization
    if l in ('TestDeleteVolumeLockPassesAreBounded 0.01','TestReconcileDefaultMaxDeletionsWithDigest 0.01'):
        l=l[:-4]+'0.00'
    if l=='TestDestroyNeverHitsAReplacedDataset 1.42':
        l='TestDestroyNeverHitsAReplacedDataset 1.52'
    out.append(l)
i=max(j for j,l in enumerate(out) if l.startswith('# note:'))
out.insert(i+1,"# note: TestDestroyNeverHitsAReplacedDataset's ADR 0010 +0.10 (the note above; its rollback-create case postdates the source runs) re-derived on these normalized weights: measured 1.42 -> 1.52 (its raw cost/nproc averages 1.37 s over the four runs, so normalization scales it by 1.04 and the ~0.4 test-s / nproc 4 = 0.10 s stays 0.10).")
open(p,'w').write('\n'.join(out))
