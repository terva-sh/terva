#!/usr/bin/env bash
# Run a self-contained dogfood scenario.
#
#   just dogfood <name>          # <name> is a directory under testdata/scenarios/
#
# It builds the CURRENT working tree (never a stale binary), stands up a
# throwaway TERVA_HOME seeded from the scenario's PRISTINE fixtures every run
# (so state never carries between runs — no manual resets), inherits your real
# auth via a symlink (no tokens copied), trusts the workspace, and launches the
# interactive TUI with the scenario's task preloaded.
#
# A scenario is a directory:
#   testdata/scenarios/<name>/
#     config.json     the terva config written to the scratch TERVA_HOME
#     prompt.txt      the task, preloaded into the composer via --task
#     workspace/      pristine fixtures, copied into a fresh workspace each run
#     README.md       what it exercises + any endpoint prereqs
#
# Env overrides:
#   TERVA_DOGFOOD_AUTH_HOME=<dir>   where your real auth.json lives, if it isn't
#                                   in a standard location
#   TERVA_DOGFOOD_DRY_RUN=1         set everything up and print it, don't launch
set -euo pipefail

name="${1:-}"
if [ -z "$name" ]; then
	echo "usage: just dogfood <scenario-name>" >&2
	echo "scenarios:" >&2
	ls "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/testdata/scenarios" 2>/dev/null | sed 's/^/  /' >&2 || true
	exit 2
fi

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
scenario="$repo/testdata/scenarios/$name"
[ -d "$scenario" ] || { echo "no such scenario: testdata/scenarios/$name" >&2; exit 1; }
[ -f "$scenario/config.json" ] || { echo "scenario missing config.json: $scenario" >&2; exit 1; }
[ -f "$scenario/prompt.txt" ] || { echo "scenario missing prompt.txt: $scenario" >&2; exit 1; }

# 1. Build the current tree — the whole point is testing what you're working on.
echo "==> building terva from the current tree"
( cd "$repo" && go build -o "$repo/bin/terva" ./cmd/terva )
bin="$repo/bin/terva"

# 2. Find your real terva home for the auth.json symlink (no tokens are copied).
real_home=""
for h in \
	"${TERVA_DOGFOOD_AUTH_HOME:-}" \
	"$HOME/Library/Application Support/terva" \
	"${XDG_STATE_HOME:-$HOME/.local/state}/terva" \
	"$HOME/.local/state/terva" \
	"$HOME/.config/terva"; do
	if [ -n "$h" ] && [ -f "$h/auth.json" ]; then real_home="$h"; break; fi
done
if [ -z "$real_home" ]; then
	echo "couldn't find your terva auth.json — log in once with terva, or set TERVA_DOGFOOD_AUTH_HOME" >&2
	exit 1
fi

# 3. Fresh throwaway home, re-seeded from PRISTINE fixtures every run. THIS is
#    what removes the per-run reset tax: the mutable copy is disposable, the
#    fixtures are read-only, so gemma is always back at its start value and there
#    are no leftover sessions.
home="${TMPDIR:-/tmp}/terva-dogfood-$name"
rm -rf "$home"
mkdir -p "$home/workspace"
cp "$scenario/config.json" "$home/config.json"
ln -sf "$real_home/auth.json" "$home/auth.json"
if [ -d "$scenario/workspace" ]; then
	cp -R "$scenario/workspace/." "$home/workspace/"
fi
echo "==> scenario '$name' ready: TERVA_HOME=$home (fresh; auth inherited from $real_home)"

if [ -n "${TERVA_DOGFOOD_DRY_RUN:-}" ]; then
	echo "==> dry run — set up, not launching"
	echo "    config model:  $(grep -o '"model"[^,]*' "$home/config.json" | head -1)"
	echo "    auth symlink:  $(readlink "$home/auth.json" 2>/dev/null || echo '(none)')"
	echo "    workspace:     $(ls "$home/workspace" 2>/dev/null | tr '\n' ' ')"
	echo "    task:          $scenario/prompt.txt"
	exit 0
fi

# 4. Trust the workspace (so project content loads cleanly) and launch with the
#    task preloaded — the composer opens filled in; you hit enter to start.
export TERVA_HOME="$home"
( cd "$home/workspace" && "$bin" trust >/dev/null 2>&1 || true )
cd "$home/workspace"
exec "$bin" --task "$scenario/prompt.txt"
