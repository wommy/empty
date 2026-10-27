#!/usr/bin/env bash
M=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave14ms/mutate.py
python3 $M MH1-scan-ignores-userrefs internal/endpoint/table.go '			Holds:          u(cHolds),\n' ''
python3 $M MH2-plan-ignores-holds internal/endpoint/plan.go '		if s.Holds > 0 {' '		if false {'
python3 $M MH3-fixpoint-reaps-held internal/endpoint/reconcile.go ' && clones[r.Name] == 0 && r.Holds == 0 })' ' && clones[r.Name] == 0 })'
python3 $M MC1-scan-ignores-children internal/endpoint/table.go '			rows[i].Children = kids' '			_ = kids'
python3 $M MC2-plan-ignores-children internal/endpoint/plan.go '	if len(row.Children) > 0 {' '	if false {'
