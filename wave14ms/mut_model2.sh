#!/usr/bin/env bash
M=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave14ms/mutate.py
export MODEL_SEQS=${MODEL_SEQS:-2000}
R=(-v -run '^TestModel$')
python3 $M MH2-plan-ignores-holds internal/endpoint/plan.go '		if s.Holds > 0 {' '		if false {' "${R[@]}"
python3 $M MC2-plan-ignores-children internal/endpoint/plan.go '	if len(row.Children) > 0 {' '	if false {' "${R[@]}"
python3 $M M2-clone-src-committed internal/endpoint/plan.go '		if !ok || !src.ServedVolume() {\n			return nil, errorf(NotFound, "source volume %s not found", srcID)' '		if !ok || !src.OwnedVolume() {\n			return nil, errorf(NotFound, "source volume %s not found", srcID)' "${R[@]}"
