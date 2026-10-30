package zfs

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestAuditLogsOnlyMutations(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	r := Audit(scriptRunner{"get -Hp type tank": "", "destroy -r tank/k8s/v": ""}, log)
	ctx := context.Background()
	r.Run(ctx, "zfs", "get", "-Hp", "type", "tank")
	r.Run(ctx, "zfs", "destroy", "-r", "tank/k8s/v")
	r.Run(ctx, "zfs", "destroy", "tank/k8s/missing")

	var msgs []string
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var rec struct {
			Msg   string   `json:"msg"`
			Level string   `json:"level"`
			Argv  []string `json:"argv"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatal(err)
		}
		msgs = append(msgs, rec.Level+" "+rec.Msg+" "+strings.Join(rec.Argv, " "))
	}
	want := []string{
		"INFO zfs exec zfs destroy -r tank/k8s/v",
		"INFO zfs done zfs destroy -r tank/k8s/v",
		"INFO zfs exec zfs destroy tank/k8s/missing",
		"WARN zfs failed zfs destroy tank/k8s/missing",
	}
	if strings.Join(msgs, "\n") != strings.Join(want, "\n") {
		t.Fatalf("logged\n%s\nwant\n%s", strings.Join(msgs, "\n"), strings.Join(want, "\n"))
	}
}
