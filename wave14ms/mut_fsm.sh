#!/usr/bin/env bash
M=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave14ms/mutate.py
export FSM_DEPTH=3
python3 $M AN09-classify-reverted internal/endpoint/fsm_classify_test.go '		if _, ours := r.OwnedCloneSource(); ours {' '		if _, ours := r.OwnedCloneSource(); false && ours {' -tags nightly -run '^(TestExplore|TestClassify)'
python3 $M MH2-plan-ignores-holds internal/endpoint/plan.go '		if s.Holds > 0 {' '		if false {' -tags nightly -run '^TestExplore'
python3 $M MC2-plan-ignores-children internal/endpoint/plan.go '	if len(row.Children) > 0 {' '	if false {' -tags nightly -run '^TestExplore'
python3 $M MC1-scan-ignores-children internal/endpoint/table.go '			rows[i].Children = kids' '			_ = kids' -tags nightly -run '^TestExplore'
python3 $M MH1-scan-ignores-userrefs internal/endpoint/table.go '			Holds:          u(cHolds),' '			Holds:          0 * u(cHolds),' -tags nightly -run '^TestExplore'
