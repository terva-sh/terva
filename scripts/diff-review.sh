#!/usr/bin/env bash
#
# scripts/diff-review.sh — tiered diff review (retrospective H6).
#
# Split the changed files for a diff <spec> into *source* and *generated* (git's
# linguist-generated attribute, set in .gitattributes), print the source diff in
# full, and collapse the generated changes to a one-line-per-asset summary — so
# a reviewer reads the real change, not minified/hashed build output. It never
# hides: the complete diff is always `git diff <spec>`, echoed at the end as the
# escape hatch. Composes with `just web-verify-dist`, which separately proves the
# committed generated tree is the faithful build of the current source.
#
# Usage:
#   scripts/diff-review.sh                 # working tree vs HEAD (default)
#   scripts/diff-review.sh sothr-main      # working tree vs a ref
#   scripts/diff-review.sh main..feature   # a commit range
#
set -euo pipefail

spec="${1:-HEAD}"

# name-status for the spec: <STATUS>\t<path> (renames: R100\t<old>\t<new>, so the
# last tab-field is the live path in every case).
ns="$(git diff --name-status "$spec")"
if [ -z "$ns" ]; then
    printf 'diff-review: no changes for %s\n' "$spec"
    exit 0
fi

paths="$(printf '%s\n' "$ns" | awk -F'\t' 'NF { print $NF }')"

# Which changed paths git considers generated (linguist-generated=true).
generated="$(printf '%s\n' "$paths" \
    | git check-attr --stdin linguist-generated \
    | awk -F': ' '$3 == "true" { print $1 }')"

# Source = changed paths that are NOT generated.
source="$(comm -23 \
    <(printf '%s\n' "$paths" | sort -u) \
    <(printf '%s\n' "$generated" | grep -v '^$' | sort -u))"

# ---- Source view: the full diff, the part a reviewer actually reads. ----
printf '=== source changes ===\n'
if [ -z "$source" ]; then
    printf '(none — every changed file is generated)\n'
else
    # shellcheck disable=SC2086
    git -c color.ui=auto diff "$spec" -- $source
fi

# ---- Generated summary: collapsed, but nothing summarized away silently. ----
printf '\n=== generated changes (collapsed) ===\n'
if [ -z "$generated" ]; then
    printf '(none)\n'
else
    # Pass the generated-path set as a first awk input (FNR==NR) rather than via
    # -v: a -v value carrying newlines is mishandled by macOS/BWK awk.
    awk -F'\t' '
    function basename(p,   b) { b = p; sub(/.*\//, "", b); return b }
    FNR == NR { if ($0 != "") isgen[$0] = 1; next }
    NF {
        status = $1
        path = $NF
        if (!(path in isgen)) next

        base = basename(path)
        # Recognize a content-hashed asset (Vite `name-<hash>.ext`, workbox
        # `workbox-<hash>.js`) WITHOUT regex intervals so macOS awk and gawk
        # agree: split off the extension, then the trailing "-segment", and
        # accept it as a hash only when that segment is long+alnum.
        dot = ext = namepart = ""
        if (base ~ /\./ && base ~ /-/) {
            namepart = base; ext = base
            sub(/\.[^.]*$/, "", namepart)   # name without final extension
            sub(/^.*\./, "", ext)           # final extension
            dash = namepart
            sub(/^.*-/, "", dash)           # segment after the last dash
            if (length(dash) >= 6 && dash ~ /^[A-Za-z0-9_]+$/) {
                stem = path
                sub(/-[A-Za-z0-9_]+\.[^.]*$/, "-*." ext, stem)
                if (status ~ /^A/)      added[stem] = dash
                else if (status ~ /^D/) deleted[stem] = dash
                else                    other[path] = status
                next
            }
        }
        # Stable-named build files whose only job is to reference the hashes.
        if (base == "index.html" || base == "sw.js" || base == "registerSW.js" || base == "manifest.webmanifest") {
            refs[path] = status
            next
        }
        other[path] = status
    }
    END {
        any = 0
        for (s in added) {
            if (s in deleted) { printf "  rehashed  %s  (%s -> %s)\n", s, deleted[s], added[s]; delete deleted[s] }
            else              { printf "  added     %s  (%s)\n", s, added[s] }
            any = 1
        }
        for (s in deleted) { printf "  removed   %s  (%s)\n", s, deleted[s]; any = 1 }
        refline = ""
        for (p in refs) refline = refline (refline ? ", " : "") p
        if (refline != "") { printf "  refs      %s  (updated to match)\n", refline; any = 1 }
        first = 1
        for (p in other) {
            if (first) { printf "  other generated (not a clean rehash — collapsed here, see full diff):\n"; first = 0 }
            printf "    %-6s  %s\n", other[p], p
            any = 1
        }
        if (!any) printf "(none)\n"
    }' <(printf '%s\n' "$generated") <(printf '%s\n' "$ns")
fi

printf '\nFull diff:  git diff %s\n' "$spec"
