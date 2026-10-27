package endpoint

import "context"

// ScanWhole is the whole-parent scan: the Table whose ownership verdicts
// the differential in owndiff_test.go checks.
func (e *Endpoint) ScanWhole(ctx context.Context) (Table, error) { return e.scan(ctx, "") }
