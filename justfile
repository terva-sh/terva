# terva common dev tasks. Run `just` (or `just --list`) to see everything.
#
# These wrap the same `go` invocations goreleaser uses —
# nothing here reimplements building or installing, it just drives the
# Go toolchain with the version metadata baked in.

set shell := ["bash", "-eu", "-o", "pipefail", "-c"]

# Maintainer-only release-cut targets (release-cut/-verify/-publish/…).
# Optional import: the public tree ships without release.just and this
# justfile still works there.
import? 'release.just'

# This is a hard fork (renamed terva). The flow is OUTBOUND only:
# `just mirror-*` pushes the curated release branches to the public
# GitHub mirror. Upstream tracking was retired in July 2026 (the
# control-plane rearchitecture made the codebases fundamentally
# different); should a specific upstream change ever be worth a manual
# port, scripts/rename-upstream.sh still translates their naming and
# module path onto ours.
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

# Stripped, trimmed release-style ldflags (matches goreleaser).
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
# (-tags terva_no_telegram,terva_no_discord): no chat transports or SDKs linked in.
build-min:
    @mkdir -p bin
    go build -trimpath -tags terva_no_telegram,terva_no_discord -ldflags "{{ldflags}}" -o bin/terva-min ./cmd/terva
    @echo "built bin/terva-min (no chat connectors)"

# Build and install the FULL terva from source into your Go bin (GOBIN, else
# GOPATH/bin) via `go install`. "Full" = every optional feature compiled in,
# including the `terva acp` editor run mode (-tags terva_acp) and the
# `terva web` browser control panel (-tags terva_web); telegram is in by
# default. This is the binary to point an ACP editor (Zed) at. For the lean
# variant, see `just build-min`. The web panel embeds the prebuilt client from
# packages/agent/web/client/dist (committed); run `just web-build` after
# changing the client.
install:
    go install -trimpath -tags terva_acp,terva_web -ldflags "{{ldflags}}" ./cmd/terva
    @dest="$(go env GOBIN)"; [ -n "$dest" ] || dest="$(go env GOPATH)/bin"; echo "installed terva (full, terva_acp,terva_web) -> $dest/terva"

# Like `just install` (full features, terva_acp, into GOBIN/GOPATH bin)
# but NON-STRIPPED: no `-s -w`, no `-trimpath`, so symbols and source
# paths survive for `sample`, pprof, and friends. Optimizations stay ON
# (unlike `just debug`, which adds -N -l for breakpoints) so a CPU
# profile reflects a real build. Reports as 0.0.0-debug so `--version`
# tells it apart from a release install. Adds -tags terva_pprof, which
# links the /debug/pprof endpoint (kept out of every other build); even
# here it stays off until you set TERVA_PPROF=localhost:6060.
install-dev:
    go install -tags terva_acp,terva_pprof,terva_web -ldflags "{{debug_ldflags}}" ./cmd/terva
    @dest="$(go env GOBIN)"; [ -n "$dest" ] || dest="$(go env GOPATH)/bin"; echo "installed terva (dev, non-stripped, terva_acp,terva_pprof,terva_web) -> $dest/terva"

# Build the web control-panel client (Preact/Vite → packages/agent/web/client/dist),
# which the terva_web build embeds via go:embed. Commit the regenerated dist so
# a plain `go build -tags terva_web` (and CI) needs no JS toolchain. Requires
# Node.js; run after changing anything under packages/agent/web/client/src.
web-build:
    npm --prefix packages/agent/web/client ci
    # Extract the client's translatable strings into the reference catalog
    # (packages/i18n/locales/web/en.json), then mirror the canonical web
    # catalogs into the client bundle for offline/first-paint (the daemon serves
    # the overlay-merged copy at runtime). Regenerated here, like dist — commit
    # the result; there is no Node in `just ci` to gate it.
    npm --prefix packages/agent/web/client run i18n-extract
    @mkdir -p packages/agent/web/client/src/locales
    @for f in packages/i18n/locales/web/*.json; do case "$f" in */en.json) ;; *) cp "$f" packages/agent/web/client/src/locales/;; esac; done
    npm --prefix packages/agent/web/client run build
    @echo "built web client -> packages/agent/web/client/dist (commit it)"

# Run the web client's unit tests (vitest over the pure store/transform logic).
# Local-only, like web-build: there is no Node in `just ci`, so this is a
# developer gate you run after changing packages/agent/web/client/src.
web-test:
    npm --prefix packages/agent/web/client ci
    npm --prefix packages/agent/web/client test

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
    # The acp/web surfaces are tag-gated: without these lines the everyday
    # test run silently skips them ("[no test files]") and only `just ci`
    # or the CI workflow would catch a regression.
    go test -tags terva_acp -race ./packages/agent/acp/... {{ARGS}}
    go test -tags terva_web -race ./packages/agent/web/ {{ARGS}}

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

# Vet + gofmt check (non-mutating; the lint gate).
lint:
    go vet ./...
    @test -z "$(gofmt -l . | tee /dev/stderr)" || { echo "gofmt issues (run \`just fmt\`)"; exit 1; }
    # The i18n reference catalogs (locales/en.json + locales/prompts/en.json)
    # must match the wrapped T/P calls in packages/ and cmd/. Regenerate with
    # `go run ./cmd/terva-i18n-lint` and commit the result if this fails.
    go run ./cmd/terva-i18n-lint -check

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

# Web control panel (behind -tags terva_web): build/vet/test it. The default
# build only compiles the no-tag stub, so `test` can't cover the WS carrier —
# this guards it, same discipline as ci-acp. Uses the committed client dist
# (no Node.js needed); rebuild that with `just web-build`.
ci-web:
    go build -tags terva_web ./...
    go vet -tags terva_web ./packages/agent/web/
    go test -tags terva_web -race ./packages/agent/web/ ./packages/agent/

# fmt-check + vet + race tests + connector tag-matrix build + acp + web tag
# build/test + terva_pprof tag build + public packaging drift check, as
# a pre-push gate.
ci: lint test ci-acp ci-web
    go build -tags terva_no_telegram,terva_no_discord ./...
    # terva_pprof guard: the profiling endpoint (cmd/terva/pprof.go) only
    # compiles under this tag, so the default build can't catch a break in
    # it — same reason ci-acp exists. install-dev is the only shipping use.
    go build -tags terva_pprof ./cmd/terva
    @if [ -x scripts/release.sh ]; then ./scripts/release.sh check-overlay; fi

# Pre-release gate for a public cut: the full local CI, then the manual
# reminders for what can only be verified on GitHub. The public release
# workflow hard-gates publishing on the public CI overlay (Windows/macOS +
# web-client) passing for the tagged commit, so the tag must be pushed only
# after that overlay is green on the release-branch tip.
release-preflight-public: ci
    @echo ""
    @echo "Local gate passed. Before pushing the release tag, confirm on GitHub:"
    @echo "  - the public 'ci' workflow (Windows/macOS matrix + web-client vitest)"
    @echo "    is GREEN on the release-branch tip you are about to tag;"
    @echo "  - the release workflow refuses to publish otherwise (verify-ci gate)."
    @echo "Then push pub/vX.Y.Z. See scripts/release-overlay/.github/workflows/."

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
