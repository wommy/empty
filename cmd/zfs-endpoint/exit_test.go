package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
)

// The process exits after one JSON "exit" line at ERROR carrying the exit
// code the README's table documents.
func TestExitLine(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		err  error
		code int
	}{
		{configErr("-parent is required"), 78},
		{tempErr("parent dataset tank/k8s is not available after 1m0s"), 75},
		{&exitError{exitFindings, errors.New("reconcile on x: 2 findings")}, 3},
		{errors.New("serve: boom"), 1},
	} {
		var buf bytes.Buffer
		if got := exitLine(slog.New(slog.NewJSONHandler(&buf, nil)), tc.err); got != tc.code {
			t.Errorf("%v: exit %d, want %d", tc.err, got, tc.code)
		}
		var rec struct {
			Level, Msg, Error string
			ExitCode          int `json:"exit_code"`
		}
		if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
			t.Fatalf("%v: not one JSON line: %q", tc.err, buf.String())
		}
		if rec.Level != "ERROR" || rec.Msg != "exit" || rec.ExitCode != tc.code || rec.Error != tc.err.Error() {
			t.Errorf("%v: logged %s", tc.err, buf.String())
		}
	}
}
