#!/usr/bin/env bash
#
# Build a BINARY arm for the model-facing-text A/B.
#
#   scripts/eval/build-arm.sh <name> [--ref <gitref>] [--patch <shell command>]
#
# Produces .eval/bin/terva-<name>, which you then hand to ab.sh:
#
#   scripts/eval/build-arm.sh oldschema --ref HEAD \
#     --patch 'python3 scripts/eval/revert-schema-descriptions.py \
#              --from 091af729 packages/agent/tools/session_inspect.go'
#   scripts/eval/ab.sh --a-bin .eval/bin/terva-oldschema -- --provider anthropic ...
#
# Most model-facing text does NOT need this. Anything routed through an i18n
# keyed catalog -- tool descriptions (i18n.D), prompts (i18n.P), help (i18n.H)
# -- is swappable at runtime with an overlay, so both arms run one binary and
# the comparison is airtight by construction. Reach for a binary arm only for
# text the catalogs cannot reach: schema field descriptions, hardcoded strings,
# anything assembled in Go.
#
# The arm is built in a detached worktree, never in your tree. Two reasons:
# the build cannot pick up an unrelated uncommitted edit and quietly become a
# three-variable experiment, and the arm source stays on disk afterwards so
# "what actually differed" is a diff you can read rather than a claim.
#
# --ref defaults to HEAD, so an arm is built from COMMITTED state. If you are
# evaluating a change you have not committed, commit it to a branch and name
# that branch; a working-tree build is not reproducible a week later.
set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"

NAME=""; REF="HEAD"; PATCH=""
while [ $# -gt 0 ]; do
  case "$1" in
    --ref)   REF="$2"; shift 2 ;;
    --patch) PATCH="$2"; shift 2 ;;
    -h|--help) sed -n '2,32p' "$0"; exit 0 ;;
    -*) echo "unknown flag: $1" >&2; exit 2 ;;
    *)  NAME="$1"; shift ;;
  esac
done
[ -n "$NAME" ] || { echo "usage: build-arm.sh <name> [--ref R] [--patch CMD]" >&2; exit 2; }

git -C "$ROOT" rev-parse --verify "$REF^{commit}" >/dev/null 2>&1 \
  || { echo "no such ref: $REF" >&2; exit 2; }

SRC="$ROOT/.eval/src/$NAME"
BIN="$ROOT/.eval/bin/terva-$NAME"
mkdir -p "$ROOT/.eval/bin" "$ROOT/.eval/src"

# Recreate rather than reuse: a stale worktree carrying last week's patch
# builds a binary that no longer matches the arm you think you named.
if [ -d "$SRC" ]; then
  git -C "$ROOT" worktree remove --force "$SRC" 2>/dev/null || rm -rf "$SRC"
fi
git -C "$ROOT" worktree add --detach "$SRC" "$REF" >/dev/null || exit 1
echo "arm $NAME: worktree at $SRC ($(git -C "$SRC" rev-parse --short HEAD))"

if [ -n "$PATCH" ]; then
  # The patch runs with cwd inside the arm, but EVAL_ROOT points back at your
  # tree: an arm built at an old ref does not contain today's patch scripts,
  # so `python3 $EVAL_ROOT/scripts/eval/...` is how a patch reaches them.
  ( cd "$SRC" && EVAL_ROOT="$ROOT" eval "$PATCH" ) \
    || { echo "patch failed -- arm not built" >&2; exit 1; }
  changed="$(git -C "$SRC" status --porcelain | wc -l | tr -d ' ')"
  # A patch that applied to nothing builds an arm identical to its control,
  # which then scores as a confident "no change" from a run that compared
  # one binary with itself.
  [ "$changed" -gt 0 ] || { echo "patch changed no file -- arm not built" >&2; exit 1; }
  echo "arm $NAME: patch touched $changed file(s)"
  git -C "$SRC" --no-pager diff --stat
fi

( cd "$SRC" && go build -o "$BIN" ./cmd/terva ) || exit 1
echo "arm $NAME: $BIN"
echo
echo "read the arm's diff any time with:  git -C $SRC --no-pager diff"
