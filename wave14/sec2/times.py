import json, sys
new = {'TestReconcileNeedsADigestToCollectUndesired', 'TestShareWithGroupOpensOnlyTheSocketAndItsDirectory', 'TestSocketGroup',
       'TestProtoDocumentsTheMissingIDBound', 'TestHealthWarnsOfALaterCrossmntAncestor', 'TestStartupWarnsOfATraversableMountpoint',
       'TestProbeReportsTheCodeItGot', 'TestUnitCheckReadsTheDropIn', 'TestUnitCheckNamesTheFailingSetting'}
tot = 0
pk = {}
for l in open(sys.argv[1]):
    try:
        e = json.loads(l)
    except ValueError:
        continue
    if e.get('Action') in ('pass', 'fail') and e.get('Test') in new:
        print(e['Action'], e['Test'], e['Elapsed'])
        tot += e['Elapsed']
    if e.get('Action') in ('pass', 'fail') and 'Test' not in e:
        pk[e['Package']] = (e['Action'], e.get('Elapsed'))
print('sum', round(tot, 2))
for k, v in pk.items():
    print(k, v)
