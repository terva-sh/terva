#!/usr/bin/env bash
# A/B the two next-step asks: the one terva volunteers while you are idle, and
# the one it sends when you asked (/nextstep).
#
# WHY THIS IS NOT ab.sh. That harness drives `terva --json <prompt>` — an agent
# loop in print mode — and scores tool calls and final answers. But
# `suggest.next_step` is only ever called by the TUI (its animation tick and its
# /nextstep handler), so no scenario prompt can reach the ask. There is no arm
# for ab.sh to score, and running six unrelated scenarios would produce a green
# table that says nothing about this prompt.
#
# So the probe lives next to the code, as a gated live test that calls the REAL
# SuggestNextStep: packages/agent/workspace/nextstep_live_ab_test.go.
#
# The arms need no overlay. Both asks are selected by one param, so flipping
# `on_demand` IS the arm switch — same session, same transcript, same system
# prompt, same model, same cap, same reasoning-off request, by construction
# rather than by staging. What ab.sh needs --verify-only for, --verify proves
# here for free.
#
#   scripts/eval/nextstep-ab.sh --verify              # free: prints both asks
#   scripts/eval/nextstep-ab.sh                       # paid: 3 x 7 x 2 = 42 calls
#   REPS=3 scripts/eval/nextstep-ab.sh                # fewer
#   scripts/eval/nextstep-ab.sh --provider anthropic --model claude-sonnet-4-5
#
# Completions land in .eval/nextstep-ab/ (gitignored), so a table can always be
# re-read against the text that produced it.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

MODE=1
PROVIDER=""
MODEL=""
while [ $# -gt 0 ]; do
  case "$1" in
    --verify|--verify-only) MODE=verify; shift ;;
    --provider) PROVIDER="$2"; shift 2 ;;
    --model) MODEL="$2"; shift 2 ;;
    -h|--help) sed -n '2,30p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown flag: $1" >&2; exit 2 ;;
  esac
done

if [ "$MODE" = verify ]; then
  echo "verifying the arms — no model calls, nothing spent"
else
  reps="${REPS:-7}"
  echo "PAID RUN: 3 probes x $reps reps x 2 arms = $((3 * reps * 2)) short completions"
  echo "(each is one capped 200-token call with reasoning off and no tools)"
  echo
fi

# -count=1 defeats the test cache: a cached PASS would reprint an old table as a
# new measurement, which is the same failure ab.sh refuses when it will not
# resume into a results directory built from different arms.
TERVA_EVAL_NEXTSTEP_AB="$MODE" \
TERVA_EVAL_NEXTSTEP_REPS="${REPS:-}" \
TERVA_EVAL_PROVIDER="$PROVIDER" \
TERVA_EVAL_MODEL="$MODEL" \
  go test ./packages/agent/workspace/ \
    -run TestLiveNextStepAskAB -v -count=1 -timeout 30m 2>&1 |
  grep -vE '^(=== RUN|=== PAUSE|=== CONT|--- PASS|    --- )' |
  sed 's/^ *nextstep_live_ab_test\.go:[0-9]*: //'
exit "${PIPESTATUS[0]}"
