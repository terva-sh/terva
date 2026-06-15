# terva common dev tasks. Run `just` (or `just --list`) to see everything.
#
# These wrap the same `go` invocations the Makefile / goreleaser use —
# nothing here reimplements building or installing, it just drives the
# Go toolchain with the version metadata baked in.

set shell := ["bash", "-eu", "-o", "pipefail", "-c"]

# Maintainer-only release-cut targets (release-cut/-verify/-publish/…).
# Optional import: the public tree ships without release.just and this
# justfile still works there.
import? 'release.just'

# This is a hard fork (renamed terva). The flow is mostly OUTBOUND now:
# `just mirror-*` pushes the curated release branches to the public
# GitHub mirror. Upstream zot is occasional inspiration, consumed # rename:keep
# through `just upstream-merge`'s translation workflow when wanted —
# we are not hooking our cart to that horse.
upstream_url := "https://github.com/patriceckhart/zot.git" # rename:keep — upstream really is zot
# The mirror is a STAGING GATE: a local clone of github.com/terva-sh/terva
# whose origin is the real GitHub repo. release-publish lands releases in
# the clone; going live is an explicit push from inside it, so every
# release gets a local inspection step (docs/plans/release-process.md).
# Per-machine: TERVA_MIRROR_DIR overrides; the default assumes the
# conventional ~/workspace layout (set the env var where it differs).
mirror_url := env_var_or_default("TERVA_MIRROR_DIR", env_var_or_default("HOME", "~") + "/workspace/github.com/terva-sh/terva")

# Local/untagged builds ship as 0.0.0; release tags override this via goreleaser.
version := "0.0.0"
commit   := `git rev-parse --short HEAD 2>/dev/null || echo unknown`
date     := `date -u +%Y-%m-%dT%H:%M:%SZ`

# Stripped, trimmed release-style ldflags (matches Makefile + goreleaser).
ldflags := "-s -w -X main.version=" + version + " -X main.commit=" + commit + " -X main.date=" + date
# Debug build: keep symbols, disable inlining + optimizations so breakpoints land.
debug_ldflags := "-X main.version=" + version + "-debug -X main.commit=" + commit + " -X main.date=" + date

# List available recipes.
default:
    @just --list

# Start a dev terva straight from source (rebuilds each run). Pass args: `just run -p "hi"`.
run *ARGS:
    go run -ldflags "{{ldflags}}" ./cmd/terva {{ARGS}}

# Run a dev terva against a pre-seeded workspace: `just run-in /path/to/ws -p "go"`.
run-in DIR *ARGS:
    go run -ldflags "{{ldflags}}" ./cmd/terva --cwd "{{DIR}}" {{ARGS}}

# Unoptimized binary + Anthropic request dump to bin/terva-debug.log; runs in this
# terminal so the TUI works. Tail the dump in another shell with `just debug-log`.
# Start a dev terva with debug logging enabled.
debug *ARGS:
    @mkdir -p bin
    go build -gcflags="all=-N -l" -ldflags "{{debug_ldflags}}" -o bin/terva-debug ./cmd/terva
    @echo "terva debug build -> bin/terva-debug ; anthropic request dump -> bin/terva-debug.log"
    TERVA_DEBUG_ANTHROPIC="$PWD/bin/terva-debug.log" ./bin/terva-debug {{ARGS}}

# Tail the Anthropic request dump written by `just debug`.
debug-log:
    @touch bin/terva-debug.log
    tail -f bin/terva-debug.log

# Installs delve on first use and serves on 127.0.0.1:2345; attach with
# `dlv connect :2345` or your editor. The TUI runs through Delve's stdio here,
# so prefer `just debug` for plain interactive use and this for breakpoints.
# Run a dev terva under a Delve headless server for breakpoint debugging.
debug-dlv *ARGS:
    @command -v dlv >/dev/null 2>&1 || { echo "installing delve..."; go install github.com/go-delve/delve/cmd/dlv@latest; }
    dlv debug ./cmd/terva --headless --listen=127.0.0.1:2345 --api-version=2 --accept-multiclient -- {{ARGS}}

# Build a release-style binary to bin/terva.
build:
    @mkdir -p bin
    go build -trimpath -ldflags "{{ldflags}}" -o bin/terva ./cmd/terva
    @echo "built bin/terva ({{version}}, {{commit}})"

# Build a lean binary with every chat connector tagged out
# (-tags terva_no_telegram): no telegram transport linked in.
build-min:
    @mkdir -p bin
    go build -trimpath -tags terva_no_telegram -ldflags "{{ldflags}}" -o bin/terva-min ./cmd/terva
    @echo "built bin/terva-min (no chat connectors)"

# Build and install terva from source into your Go bin (GOBIN, else GOPATH/bin) via `go install`.
install:
    go install -trimpath -ldflags "{{ldflags}}" ./cmd/terva
    @dest="$(go env GOBIN)"; [ -n "$dest" ] || dest="$(go env GOPATH)/bin"; echo "installed terva -> $dest/terva"

# goreleaser drives real packaging (cross-compiled archives + checksums;
# CI snapshot job and the tag-triggered release workflow use the same
# config). These targets install goreleaser on first use.

# Validate .goreleaser.yaml.
goreleaser-check:
    @command -v goreleaser >/dev/null 2>&1 || { echo "installing goreleaser..."; go install github.com/goreleaser/goreleaser/v2@latest; }
    goreleaser check

# Full local snapshot release into dist/ (all targets, archives, checksums; no publish).
release-snapshot:
    @command -v goreleaser >/dev/null 2>&1 || { echo "installing goreleaser..."; go install github.com/goreleaser/goreleaser/v2@latest; }
    goreleaser release --snapshot --clean

# Run the full test suite with the race detector. Extra args pass through: `just test -run TestFoo`.
test *ARGS:
    go test -race ./... {{ARGS}}

# Faster tests without the race detector.
test-fast *ARGS:
    go test ./... {{ARGS}}

# Test a single package with verbose output: `just test-pkg ./packages/provider/auth`.
test-pkg PKG *ARGS:
    go test -race -v {{PKG}} {{ARGS}}

# End-to-end harness only: builds the real binary, drives print/json
# modes against a fake provider. Included in `just test` / `just ci`
# via ./... — this target is for iterating on the harness itself.
test-e2e *ARGS:
    go test -v ./e2e/ {{ARGS}}

# Vet + gofmt check (non-mutating; mirrors the Makefile lint target).
lint:
    go vet ./...
    @test -z "$(gofmt -l . | tee /dev/stderr)" || { echo "gofmt issues (run \`just fmt\`)"; exit 1; }

# Format all Go sources in place.
fmt:
    gofmt -w .

# Tidy go.mod / go.sum.
tidy:
    go mod tidy

# ACP run mode (behind -tags terva_acp): build/vet/test it. The default
# build only compiles the no-tag stub, so `test` can't cover the real
# package — this guards it from silent breakage.
ci-acp:
    go build -tags terva_acp ./...
    go vet -tags terva_acp ./packages/agent/...
    go test -tags terva_acp -race ./packages/agent/acp/... ./packages/agent/

# fmt-check + vet + race tests + connector tag-matrix build + acp tag
# build/test + public packaging drift check, as a pre-push gate.
ci: lint test ci-acp
    go build -tags terva_no_telegram ./...
    @if [ -x scripts/release.sh ]; then ./scripts/release.sh check-overlay; fi

# Print the version string the binary would report, built from source.
version:
    @go run -ldflags "{{ldflags}}" ./cmd/terva --version

# Remove build artifacts.
clean:
    rm -rf bin

# Ensure the `mirror` remote points at the public GitHub mirror (idempotent).
mirror-init:
    @if git remote get-url mirror >/dev/null 2>&1; then \
        git remote set-url mirror "{{mirror_url}}"; \
    else \
        git remote add mirror "{{mirror_url}}"; \
    fi
    @echo "mirror -> $(git remote get-url mirror)"

# Only `release` and `release-*` ever leave the building (the
# release-cut flow writes them — docs/plans/release-process.md);
# day-to-day history stays on the Forgejo. Tags are pushed explicitly
# (release-publish pushes pub/vX.Y.Z as the mirror's vX.Y.Z) — a
# blanket --tags would leak upstream's fetched tags. This target is
# the bare re-mirror; a normal release goes through release-publish.
# Push the curated release branches to the public GitHub mirror.
mirror-push: mirror-init
    @git push mirror 'refs/heads/release:refs/heads/release' 2>/dev/null \
        || echo "no local release branch yet — run the release-cut flow first"
    @git push mirror 'refs/heads/release-*:refs/heads/release-*' 2>/dev/null || true

# Ensure the `upstream` remote points at upstream zot (idempotent). # rename:keep
upstream-init:
    @if git remote get-url upstream >/dev/null 2>&1; then \
        git remote set-url upstream "{{upstream_url}}"; \
    else \
        git remote add upstream "{{upstream_url}}"; \
    fi
    @echo "upstream -> $(git remote get-url upstream)"

# Fetch upstream and show how far this branch sits from upstream/main.
# Informational; consuming upstream is occasional, not a cadence.
upstream-status: upstream-init
    # --no-tags: fetching upstream's tags is how zot's v0.103.1 leaked # rename:keep
    # into this repo (and fired a bogus Forgejo release). terva's own
    # versions live in upstream's future number space (0.104+,
    # docs/plans/release-process.md), so their tags must never land here.
    git fetch upstream --prune --no-tags
    @counts=$(git rev-list --left-right --count HEAD...upstream/main); \
        echo "ahead $(echo "$counts" | cut -f1) / behind $(echo "$counts" | cut -f2) vs upstream/main"
    @echo "--- upstream commits not yet merged ---"
    @git log --oneline --no-decorate HEAD..upstream/main || true

# Rebuilds the `upstream-translated` branch from upstream/main in a
# throwaway worktree, runs scripts/rename-upstream.sh over it (full
# naming map + module path), commits the translation, and merges THAT.
# Earlier translations merged the same way auto-resolve (both sides
# identical); real conflicts are yours, then `just ci` before pushing.
# Merge upstream/main through the rename translation (occasional).
upstream-merge: upstream-status
    #!/usr/bin/env bash
    set -euo pipefail
    test -z "$(git status --porcelain)" || { echo "working tree not clean; commit or stash first"; exit 1; }
    wt="$(mktemp -d)"
    trap 'git worktree remove --force "$wt" 2>/dev/null || true' EXIT
    git worktree add --force -B upstream-translated "$wt" upstream/main
    ./scripts/rename-upstream.sh --module-path terva.sh/terva "$wt"
    git -C "$wt" add -A
    if git -C "$wt" diff --cached --quiet; then
        echo "upstream tree needed no translation"
    else
        git -C "$wt" commit -m "chore: translate upstream@$(git rev-parse --short upstream/main) to the terva naming"
    fi
    git worktree remove --force "$wt"
    git merge --no-ff upstream-translated -m "Merge translated upstream/main into $(git rev-parse --abbrev-ref HEAD)"
