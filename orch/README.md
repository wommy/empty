# Orchestration as a state machine

Tables (data, one JSON object per line):
- agents.jsonl: every agent ever launched. Fields: id, name, lane (reader|writer), wave, status, branch, report, anomaly (bool), deps.
- queue.jsonl: work not yet launched. Fields: id, name, lane, priority (lower first), prompt (file#section), deps (ids that must be merged/done first), anomaly (bool).
- state.json: facts the judge cannot see itself: PR CI (green|red|pending), head sha, open owner questions.
- events.jsonl: append-only transition log (ts, id, from, to, note). Written only by orch.py.

Agent status FSM (orch.py refuses any other edge):
  queued -> running -> reported -> triaged -> done            (reader)
  queued -> running -> reported -> triaged -> merged | rejected  (writer)
  any -> abandoned (with a note)
A row is scored once lanes/ledger.jsonl has its id.

Judgment (judge.py: pure function of the tables, prints actions in priority order):
  0. PR CI red                              -> FIX (blocks launches, not triage)
  1. reported, not triaged (anomaly first)  -> TRIAGE
  2. writer triaged                          -> MERGE (or REJECT)
  3. done/merged/rejected, not in ledger     -> SCORE
  4. reader queue and readers < cap (5)      -> LAUNCH up to min(3, 5 - running) readers (priority, deps met)
  5. writer queue and writers < cap (3)      -> LAUNCH writers into free slots (priority, deps met)
  Caps (owner): total 8 = readers 5 + writers 3; the split keeps either lane from starving the other.
  6. open owner questions                    -> ASK
  7. events since the last checkpoint        -> CHECKPOINT (checkpoint.sh)
Anomalies (owner rule) sort first within every step.
Lanes never block each other: steps 4 and 5 are independent.
