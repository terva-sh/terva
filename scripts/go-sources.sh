#!/usr/bin/env bash
# Print this checkout's own Go sources, one path per line.
#
# `gofmt -l .` and `gofmt -w .` walk the filesystem, which is the wrong universe.
# This project keeps git worktrees under .claude/worktrees/, so a plain walk
# reaches into a SIBLING CHECKOUT: `just lint` went red naming a file in another
# branch that the developer running it had never touched, and `just fmt`
# reformatted another session's work in place, unasked and unnoticed. Both are
# worse than a missing gate — one teaches everyone to disbelieve a red run, the
# other silently edits somebody else's tree.
#
# The Go-side guard tests already solved this (packages/testsupport/repowalk.go:
# any directory holding a .git entry is a separate checkout, and its contents are
# somebody else's source held to somebody else's conventions). This is the same
# rule for the shell side, delegated to the tool that already knows the answer
# rather than hand-written a fifth time.
#
# git is that tool, and it is stricter than the .gitignore line that happens to
# cover .claude/worktrees/: git does not descend into a nested checkout AT ALL,
# ignored or not. Verified — a repo containing an unignored inner repo with an
# unformatted file lists only its own.
#
# Both halves matter:
#   ls-files                       tracked sources
#   ls-files -o --exclude-standard new files, not yet added
# Dropping the second would let a brand-new file skip the format gate until the
# commit that adds it, which is exactly when nobody is looking at gofmt.
set -euo pipefail

cd "$(dirname "$0")/.."

# Sorted and de-duplicated: the two lists are disjoint today, but a caller
# passing the result to `gofmt -w` should never rewrite one file twice.
{ git ls-files -- '*.go'; git ls-files --others --exclude-standard -- '*.go'; } | sort -u
