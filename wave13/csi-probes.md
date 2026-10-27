# Wave 13 CSI-layer probes (HEAD 0237666, copy at scratchpad/wave13/csi-copy)

Probes assert the CORRECT behaviour: all FAIL at HEAD, every pre-existing test in internal/csi and internal/endpoint passes.
Run: `go test ./internal/csi ./internal/endpoint -run Probe13 -v`
Files: internal/csi/probe13_test.go, internal/endpoint/probe13_plan_test.go, internal/endpoint/probe13_sim_test.go (all under csi-copy/).

See the SubagentHandback report for anomalies, per-item detail and the summary table.
