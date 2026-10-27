#!/usr/bin/env bash
# TestModel at its PR size (64 sequences) against single-guard mutants.
M=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave14ms/mutate.py
R=(-run '^TestModel$')
python3 $M MH1-scan-ignores-userrefs internal/endpoint/table.go '			Holds:          u(cHolds),' '			Holds:          0 * u(cHolds),' "${R[@]}"
python3 $M MH2-plan-ignores-holds internal/endpoint/plan.go '		if s.Holds > 0 {' '		if false {' "${R[@]}"
python3 $M MH3-fixpoint-reaps-held internal/endpoint/reconcile.go ' && clones[r.Name] == 0 && r.Holds == 0 })' ' && clones[r.Name] == 0 })' "${R[@]}"
python3 $M MC1-scan-ignores-children internal/endpoint/table.go '			rows[i].Children = kids' '			_ = kids' "${R[@]}"
python3 $M MC2-plan-ignores-children internal/endpoint/plan.go '	if len(row.Children) > 0 {' '	if false {' "${R[@]}"
python3 $M M1-name-guard internal/endpoint/table.go 'r.Managed && r.VolumeName == r.ID' 'r.Managed' "${R[@]}"
python3 $M M2-clone-src-committed internal/endpoint/plan.go '		if !ok || !src.ServedVolume() {\n			return nil, errorf(NotFound, "source volume %s not found", srcID)' '		if !ok || !src.OwnedVolume() {\n			return nil, errorf(NotFound, "source volume %s not found", srcID)' "${R[@]}"
python3 $M M3-received-is-local internal/endpoint/table.go 'if p := cell(k); p.Local() {' 'if p := cell(k); p.Local() || p.Source == "received" {' "${R[@]}"
python3 $M M4-clone-source-unstamped-ours internal/endpoint/table.go '		r.Managed && r.SnapshotName == "" && r.CloneSourceFor == clone' '		r.SnapshotName == ""' "${R[@]}"
python3 $M M5-snapshot-source-check internal/endpoint/table.go '(r.SnapshotSource == "" || r.SnapshotSource == vol)' 'true' "${R[@]}"
