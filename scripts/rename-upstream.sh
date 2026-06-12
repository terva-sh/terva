#!/usr/bin/env bash
# rename-upstream.sh — translate the upstream "zot" naming to "terva"
# across a tree (docs/plans/rename-terva.md). This is a MAINTAINED
# artifact, used twice:
#
#   1. the phase-2 rename cut over this repository, and
#   2. translating upstream commits during merges (phase 3+): fetch
#      upstream, run this over the upstream-translated branch, merge.
#
# Rules:
#   - Contents only. File/dir renames are deliberate one-time acts
#     (git mv) so history stays followable; this script never moves
#     files.
#   - Case map: zot→terva, Zot→Terva, ZOT→TERVA (substring-based, so
#     identifiers like zotHome→tervaHome translate too).
#   - Any line containing the literal marker `rename:keep` is left
#     untouched. That marker protects the compat seams (frozen wire
#     field names, ZOT_* env fallbacks, dual-read lists, auth-bound
#     identifiers) — and the identity test treats it as the same
#     grandfather list, so a kept line is also an allowed line.
#   - The Go module path `github.com/patriceckhart/zot` is preserved
#     by default (go.mod/go.sum are skipped entirely). Pass
#     --module-path NEW to rewrite it to NEW instead — the phase-3
#     flip, and the standing flag for translating upstream commits
#     after it (the justfile's upstream-merge passes it). With
#     --module-path, go.mod files are processed too (module/require/
#     replace lines and example module names); go.sum stays excluded
#     always (hashes; regenerate with go mod tidy).
#   - Idempotent: terva/Terva/TERVA contain no "zot" substring, so a
#     second run is a no-op.
#
# Usage: scripts/rename-upstream.sh [--module-path NEW] [DIR]
set -euo pipefail

MODULE_OLD="github.com/patriceckhart/zot"
MODULE_NEW=""
ROOT="."

while [ $# -gt 0 ]; do
  case "$1" in
    --module-path)
      MODULE_NEW="$2"
      shift 2
      ;;
    -h|--help)
      sed -n '2,30p' "$0"
      exit 0
      ;;
    *)
      ROOT="$1"
      shift
      ;;
  esac
done

# Paths never translated. envcompat and the wire packages (extproto,
# connproto) are bilingual/frozen BY DESIGN — their golden tests fail
# loudly if anyone translates them by hand. The plan and this script
# document the old names on purpose.
EXCLUDES=(
  -path "*/.git/*"
  -o -path "*/bin/*"
  -o -path "*/dist/*"
  -o -path "*/node_modules/*"
  -o -path "*/__pycache__/*"
  -o -name "go.sum"
  -o -path "*/scripts/rename-upstream.sh"
  -o -path "*/docs/plans/rename-terva.md"
  -o -path "*/docs/fork.md"
  -o -path "*/packages/envcompat/*"
  -o -path "*/packages/agent/extproto/*"
  -o -path "*/packages/agent/connproto/*"
)
# go.mod carries the module identity: untouched unless --module-path
# explicitly asks for the flip.
if [ -z "$MODULE_NEW" ]; then
  EXCLUDES+=(-o -name "go.mod")
fi

# Candidate files: text files that mention any spelling. grep -I skips
# binaries (checked-in example executables, the logo).
mapfile -t files < <(
  find "$ROOT" -type f ! \( "${EXCLUDES[@]}" \) -print0 |
    xargs -0 grep -Il -e zot -e Zot -e ZOT -- 2>/dev/null || true
)

if [ ${#files[@]} -eq 0 ]; then
  echo "rename-upstream: nothing to translate under $ROOT"
  exit 0
fi

export MODULE_OLD MODULE_NEW
perl -pi -e '
  next if /rename:keep/;
  my $sentinel = "\x00MOD\x00";
  s/\Q$ENV{MODULE_OLD}\E/$sentinel/g;
  s/zot/terva/g;
  s/Zot/Terva/g;
  s/ZOT/TERVA/g;
  my $mod = $ENV{MODULE_NEW} ne "" ? $ENV{MODULE_NEW} : $ENV{MODULE_OLD};
  s/\Q$sentinel\E/$mod/g;
' "${files[@]}"

echo "rename-upstream: translated ${#files[@]} files under $ROOT"
