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

# Maintainer-only Forgejo PR targets (pr/pr-status/pr-merge/…). Same
# optional-import reason: the public tree ships without forge.just.
import? 'forge.just'

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

# 0.0.0 is a SENTINEL, not the version a local build reports. cmd/terva/main.go
# treats it as "nothing was stamped" and falls through to Go's module build
# info, so `just install` off an untagged commit reports a pseudo-version like
#   0.130.2-0.20260801072026-3b009656fe62 (140fa59, 2026-08-01T20:58:57Z)
# meaning "a dev build somewhere after v0.130.1". That leading semver is a
# PLACEHOLDER for the next cut, not a claim that it happened — the tree is ahead
# of v0.130.1 and the eventual tag could as easily be v0.131.0.
#
# Read the PARENTHESISED commit, never the leading semver: it comes from the
# ldflag below and is exactly what was built. The hash inside the pseudo-version
# is Go's own VCS stamp and in a git worktree names the PRIMARY checkout's HEAD —
# a different commit entirely. Only a goreleaser build from a tag reports a
# version worth quoting.
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

# Build a lean binary with every chat connector AND the remote-MCP HTTP transport
# tagged out (-tags terva_no_telegram,terva_no_discord,terva_no_mcp_http): no chat
# transports or SDKs, and no MCP egress (stdio MCP servers still work).
build-min:
    @mkdir -p bin
    go build -trimpath -tags terva_no_telegram,terva_no_discord,terva_no_mcp_http -ldflags "{{ldflags}}" -o bin/terva-min ./cmd/terva
    @echo "built bin/terva-min (no chat connectors, no remote-MCP HTTP)"

# Build and install the FULL terva from source into your Go bin (GOBIN, else
# GOPATH/bin) via `go install`. "Full" = every optional feature compiled in,
# including the `terva acp` editor run mode (-tags terva_acp) and the
# `terva web` browser control panel (-tags terva_web); telegram is in by
# default. This is the binary to point an ACP editor (Zed) at. For the lean
# variant, see `just build-min`. The web panel embeds the prebuilt client from
# packages/agent/web/client/dist (committed); run `just web-build` after
# changing the client.
install:
    go install -trimpath -tags terva_acp,terva_web,terva_scripting,terva_workflows -ldflags "{{ldflags}}" ./cmd/terva
    @dest="$(go env GOBIN)"; [ -n "$dest" ] || dest="$(go env GOPATH)/bin"; echo "installed terva (full, terva_acp,terva_web,terva_scripting,terva_workflows) -> $dest/terva"

# Like `just install` (full features, terva_acp, into GOBIN/GOPATH bin)
# but NON-STRIPPED: no `-s -w`, no `-trimpath`, so symbols and source
# paths survive for `sample`, pprof, and friends. Optimizations stay ON
# (unlike `just debug`, which adds -N -l for breakpoints) so a CPU
# profile reflects a real build. Reports as 0.0.0-debug so `--version`
# tells it apart from a release install. Adds -tags terva_pprof, which
# links the /debug/pprof endpoint (kept out of every other build); even
# here it stays off until you set TERVA_PPROF=localhost:6060.
install-dev:
    go install -tags terva_acp,terva_pprof,terva_web,terva_scripting,terva_workflows -ldflags "{{debug_ldflags}}" ./cmd/terva
    @dest="$(go env GOBIN)"; [ -n "$dest" ] || dest="$(go env GOPATH)/bin"; echo "installed terva (dev, non-stripped, terva_acp,terva_pprof,terva_web,terva_scripting,terva_workflows) -> $dest/terva"

# Run a self-contained dogfood scenario (a dir under testdata/scenarios/):
# builds the current tree, seeds a throwaway TERVA_HOME from the scenario's
# PRISTINE fixtures (auto-resets every run — no manual state cleanup), inherits
# your auth, trusts the workspace, and launches the TUI with the task preloaded.
# Interactive. `just dogfood` with no name lists the scenarios.
# See testdata/scenarios/<name>/README.md for per-scenario prereqs.
dogfood NAME='':
    ./scripts/dogfood.sh {{NAME}}

# Build the web control-panel client (Preact/Vite → packages/agent/web/client/dist),
# which the terva_web build embeds via go:embed. Commit the regenerated dist so
# a plain `go build -tags terva_web` (and CI) needs no JS toolchain. Requires
# Node.js; run after changing anything under packages/agent/web/client/src.
web-build:
    npm --prefix packages/agent/web/client ci
    # Catalog extraction + mirroring + the Vite build live in scripts/web-dist.sh
    # so this recipe and every determinism gate (web-check, ci-web-client, the
    # remote web-client job) regenerate the SAME tree. Commit the result; those
    # gates assert it matches a fresh build.
    ./scripts/web-dist.sh regen
    @echo "built web client -> packages/agent/web/client/dist (commit it)"

# Run the web client's unit tests (vitest over the pure store/transform logic).
# Always reinstalls, so it is the honest one to reach for when node_modules may
# be stale. `just ci` runs the same suite (ci-web-client) whenever npm is on the
# machine, reusing an existing node_modules; this forces the clean install.
web-test:
    npm --prefix packages/agent/web/client ci
    npm --prefix packages/agent/web/client test

# Fast web inner-loop gate: unit tests, typecheck, and i18n check (no build).
# Run this after touching packages/agent/web/client/src; `just web-check` is the
# complete pre-push gate.
web-check-fast:
    npm --prefix packages/agent/web/client ci
    npm --prefix packages/agent/web/client test
    npm --prefix packages/agent/web/client run typecheck
    npm --prefix packages/agent/web/client run i18n-check

# Verify the committed web assets (dist/ + mirrored catalogs) are the
# deterministic product of the current source: regenerate them exactly as
# `web-build` does, then assert git sees no change. A test-only source commit
# should pass this without a reviewer reading minified output.
web-verify-dist:
    npm --prefix packages/agent/web/client ci
    ./scripts/web-dist.sh check

# Tiered diff review (retro H6): split the changed files for a diff <spec> into
# source vs generated (git's linguist-generated attribute; see .gitattributes),
# print the source diff in full, and collapse the generated changes (hashed-asset
# rehashes, index.html/sw.js ref bumps, regenerated catalogs) to a one-line-per-
# asset summary. Never hides — the full diff is `git diff <spec>`. Complements
# `web-verify-dist`: that proves the generated tree is the faithful build of the
# source; this shows a reviewer just the source and names the generated delta.
#   just diff-review                 # working tree vs HEAD
#   just diff-review sothr-main      # working tree vs a ref
#   just diff-review main..feature   # a commit range
diff-review spec="HEAD":
    @scripts/diff-review.sh "{{spec}}"

# Complete local web gate (pre-push): unit tests, typecheck, i18n, catalog+dist
# regeneration with a determinism check, prod-dep audit (fails on high/critical),
# the tagged Go embed test, and a whitespace check. Installs node deps ONCE.
# Node.js required. `just ci` covers the vitest + dist-determinism subset when
# npm is on the machine; this gate adds typecheck, the i18n check, the audit,
# and the embed test — run it before pushing web-client changes.
web-check:
    @echo "== web-check: install =="
    npm --prefix packages/agent/web/client ci
    @echo "== web-check: unit tests =="
    npm --prefix packages/agent/web/client test
    @echo "== web-check: typecheck =="
    npm --prefix packages/agent/web/client run typecheck
    @echo "== web-check: i18n =="
    npm --prefix packages/agent/web/client run i18n-check
    @echo "== web-check: regenerate catalogs + dist, committed-asset determinism =="
    ./scripts/web-dist.sh check
    @echo "== web-check: prod-dep audit =="
    npm --prefix packages/agent/web/client audit --omit=dev --audit-level=high
    @echo "== web-check: tagged Go embed test =="
    go test -tags terva_web ./packages/agent/web
    @echo "== web-check: whitespace =="
    git diff --check
    @echo "== web-check: OK =="

# Opt-in real-browser smoke tests for the web control panel (Playwright driving
# a headless Chromium against the built dist/, with the backend WebSocket
# mocked). Deliberately NOT part of `just ci` or `web-check`: it downloads and
# runs a browser, which the standard-tools architecture keeps in recommended
# extension/MCP territory, not the core toolchain. Covers only durable flows
# that unit tests can't (render at two widths, pinned-vs-unpinned scroll,
# composer focus/keys, image paste/drop, overlay close, pane overflow on a
# phone — the last also under WebKit, where the settings bug lived). On Linux
# the browser install may need `--with-deps` (system libraries). See
# packages/agent/web/client/tests/smoke/README.md.
web-smoke:
    npm --prefix packages/agent/web/client ci
    npm --prefix packages/agent/web/client exec -- playwright install chromium webkit
    npm --prefix packages/agent/web/client run test:smoke

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

# Test a single package: `just test-pkg ./packages/provider/auth`. Quiet by
# default — failures still print in full, but passing tests are one `ok` line
# instead of a PASS per test (which floods an agent's context for no signal).
test-pkg PKG *ARGS:
    go test -race {{PKG}} {{ARGS}}

# Verbose variant: narrates every test as it runs.
test-pkg-v PKG *ARGS:
    go test -race -v {{PKG}} {{ARGS}}

# Type-check every build-tag surface, INCLUDING _test.go (go vet compiles test
# files, unlike `go build`). This is the missing rung between `test-pkg` (one
# untagged package) and `ci` (everything, race, minutes): it catches an interface
# change that breaks a tag-gated implementer the everyday `go test ./…` never
# compiles — e.g. a ctrlproto.WorkspaceService method added without updating the
# terva_web-only fakeWS, or the terva_acp carrier. Vet, not test, so it's seconds,
# not a race suite. `just ci` remains the full gate before a push.
#
# Fast pre-commit sanity across the build-tag surfaces (seconds, no race).
check:
    go vet ./packages/agent/...
    go vet -tags terva_web ./packages/agent/web/ ./packages/agent/
    go vet -tags terva_acp ./packages/agent/acp/ ./packages/agent/

# End-to-end harness only: builds the real binary, drives print/json
# modes against a fake provider. Included in `just test` / `just ci`
# via ./... — this target is for iterating on the harness itself.
test-e2e *ARGS:
    go test -v ./e2e/ {{ARGS}}

# Vet + gofmt check (non-mutating; the lint gate).
#
# gofmt runs over `scripts/go-sources.sh`, not over `.` — a filesystem walk
# reaches into the sibling worktrees under .claude/worktrees/ and reports
# another session's mid-edit file as this checkout's failure. See that script.
# (`go vet ./...` needs no such care: a worktree carries its own go.mod, and Go
# does not descend into a nested module.)
lint:
    go vet ./...
    @files="$(scripts/go-sources.sh)"; \
      test -n "$files" || { echo "no Go sources found — the file list is broken, not the tree"; exit 1; }; \
      bad="$(printf '%s\n' "$files" | xargs gofmt -l)"; \
      test -z "$bad" || { printf '%s\n' "$bad" >&2; echo "gofmt issues (run \`just fmt\`)"; exit 1; }
    # The i18n reference catalogs (locales/en.json + locales/prompts/en.json)
    # must match the wrapped T/P calls in packages/ and cmd/. Regenerate with
    # `go run ./cmd/terva-i18n-lint` and commit the result if this fails.
    go run ./cmd/terva-i18n-lint -check
    # Model-visible tool text stays in Simplified Technical English. This fails
    # on a finding .ste/baseline.json does not already hold — and equally on a
    # baseline entry that no longer fires, so the accepted set can only shrink.
    # See `just ste-lint` for the full report.
    go run ./cmd/terva-ste-lint -check -q

# Report every STE finding in the enrolled tool text, baseline included.
ste-lint *FLAGS:
    go run ./cmd/terva-ste-lint {{FLAGS}}

# Print the tool text the lint actually reads — the model's-eye view.
ste-lint-text:
    go run ./cmd/terva-ste-lint -list

# Re-record the accepted findings. Run after improving text the baseline holds,
# and commit the shrunk .ste/baseline.json with the change that earned it.
ste-lint-baseline:
    go run ./cmd/terva-ste-lint -write-baseline

# Format all Go sources in place.
#
# Same file list as `lint`, and here it is load-bearing rather than tidy: this
# WRITES. `gofmt -w .` reformatted files in sibling worktrees — editing another
# session's uncommitted work with nothing said and no way to notice.
fmt:
    @files="$(scripts/go-sources.sh)"; \
      test -n "$files" || { echo "no Go sources found — the file list is broken, not the tree"; exit 1; }; \
      printf '%s\n' "$files" | xargs gofmt -w

# Tidy go.mod / go.sum.
tidy:
    go mod tidy

# Reconcile the baked model catalog against models.dev and each provider's
# live /v1/models list, then gofmt the result. Prints what it would change;
# pass nothing to review, and commit the diff.
#
# A provider's /v1/models says WHICH models exist but not how big their
# context is (the OpenAI list schema carries no limits), so a model the
# catalog has never heard of falls back to a guessed window — which is how a
# million-token model gets budgeted as 32k. This pulls the real numbers in at
# build time, so the shipped binary needs no network to know them.
#
# NOT part of `ci`: it reaches the network, and the gate stays hermetic.
# Run it when a provider ships new models, and before a release.
models-sync:
    go run ./cmd/terva-models-sync -write
    gofmt -w packages/provider/catalog_builtin.go

# Report catalog drift without touching anything. Exits non-zero when the
# catalog is behind, so it can be run as a periodic check.
models-check:
    go run ./cmd/terva-models-sync

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

# The jsengine scripting consumer (behind -tags terva_scripting): build/
# vet/test the code_execution tool and its registration seam. The engine
# package itself (packages/agent/jsengine) is untagged, so the default
# `test` already covers it — this guards the tagged tool + build wiring,
# same discipline as ci-acp/ci-web. The tag is what links sobek into the
# binary; the default build stays interpreter-free.
#
# The workspace package is in here too, and not decoratively: code_execution's
# gate binding lives on the TOOL INSTANCE, so it is the session rebuild in
# workspace that can drop it — which it did, silently, until a user hit
# "code_execution is not wired to the approval gate in this session". Without
# the tag that tool does not exist, so an untagged run of the rebuild guard
# cannot see the field at all.
ci-scripting:
    go build -tags terva_scripting ./...
    go vet -tags terva_scripting ./packages/agent/tools/ ./packages/agent/build/ ./packages/agent/workspace/
    go test -tags terva_scripting -race -run 'CodeExecution|Scripting' ./packages/agent/tools/ ./packages/agent/build/
    go test -tags terva_scripting -race -run 'ToolChannel|RebuildTools' ./packages/agent/workspace/

# The workflow engine's CLI seam (behind -tags terva_workflows): build/vet/
# test the `terva workflow` subcommand wiring. The engine itself
# (packages/agent/workflow + the jsengine async profile) is untagged, so
# the default `test` already covers it — same discipline as ci-scripting.
ci-workflows:
    go build -tags terva_workflows ./...
    go vet -tags terva_workflows ./packages/agent/
    go test -tags terva_workflows -race -run 'Workflow' ./packages/agent/

# The web client's vitest suite + the committed-dist determinism check, when
# this machine has Node.
#
# This was deliberately left out of `just ci` once, on the grounds that ci is
# pure Go and must keep working on a machine with no Node at all. That property
# is worth keeping — and it is kept, below — but leaving the suite out ENTIRELY
# bought it at too high a price: a green `just ci` could ship a client that
# cannot talk to the daemon.
#
# It did. The panel's hello stopped requesting the `auth` method group, so every
# login call came back "method group not negotiated" and the Providers pane
# rendered buttons that could not work. `just ci` was green the whole time. The
# vitest assertion that pins the hello is what caught it — in the remote CI,
# after the push, which is the expensive place to find out.
#
# The determinism check exists for the same reason with a worse failure mode:
# the daemon serves the COMMITTED dist (go:embed), so a client-src change whose
# author forgot `just web-build` ships a stale panel with every test green —
# nothing red anywhere, just a panel that silently lacks the change.
#
# So: run it where Node exists, and SAY SO where it does not, rather than being
# quietly green. A gate that passes because it skipped the check is not a gate.
ci-web-client:
    @if command -v npm >/dev/null 2>&1; then \
        if [ ! -d packages/agent/web/client/node_modules ] || \
           [ packages/agent/web/client/package-lock.json -nt packages/agent/web/client/node_modules ]; then \
            npm --prefix packages/agent/web/client ci; \
        fi; \
        npm --prefix packages/agent/web/client test; \
        npm --prefix packages/agent/web/client run typecheck; \
        ./scripts/web-dist.sh check; \
    else \
        echo "ci-web-client: SKIPPED — no npm on this machine."; \
        echo "  The ci workflow DOES run vitest, the typecheck, and the dist determinism"; \
        echo "  check. If you"; \
        echo "  touched packages/agent/web/client/src, run 'just web-check' somewhere"; \
        echo "  with Node before you push."; \
    fi

# The Playwright smokes, on a DEV machine only. Node-, browser- and CI-gated.
#
# Why it is in `ci` at all: a green local gate should mean what a green remote
# one means. It did not. Two new buttons were given the class of an EXISTING
# control (.stage-steer-btn, .model-btn) — both are locators the smokes use, so
# they started resolving to two elements and every Stage drawer smoke failed a
# strict-mode check. `just ci` was green throughout, because the smokes were
# nowhere in it. That is the same hole ci-web-client was folded in to close, and
# this closes the other half of it.
#
# Why it SKIPS under CI: the remote gate has a dedicated "Web Client Smoke" job
# with a musl-chromium setup this recipe cannot reproduce, and running both
# would double a two-minute job for no extra signal. `CI` is the same signal
# playwright.config.ts already keys forbidOnly/retries/reporter on.
#
# Every skip is LOUD and says what to run instead. A quiet skip is exactly how
# a gate comes to certify a surface it never touched.
#
# And it REFUSES — nonzero, not a skip — when the preview port is already
# serving. playwright.config.ts sets reuseExistingServer locally (a deliberate
# speed choice: `just web-smoke` should not rebuild and re-serve for a run you
# are repeating), which means a leftover `vite preview` gets attached to instead
# of the current dist. The suite then reports on whatever bundle that process is
# holding, and the verdict is wrong in BOTH directions: green on stale-but-good
# code, red on new-and-good code. I hit the red half once while testing this
# recipe. A gate cannot be allowed to answer for a bundle it did not build, so
# this one declines to answer at all.
ci-web-smoke:
    @if [ -n "${CI:-}" ]; then \
        echo "ci-web-smoke: SKIPPED — under CI, where the 'Web Client Smoke' job owns this."; \
    elif ! command -v npm >/dev/null 2>&1; then \
        echo "ci-web-smoke: SKIPPED — no npm on this machine."; \
        echo "  The ci workflow DOES run the Playwright smokes. If you touched the"; \
        echo "  panel or Stage UI, run 'just web-smoke' somewhere with Node."; \
    elif [ ! -d packages/agent/web/client/node_modules ]; then \
        echo "ci-web-smoke: SKIPPED — packages/agent/web/client/node_modules is absent."; \
        echo "  Run 'npm --prefix packages/agent/web/client ci' first."; \
    elif ! (cd packages/agent/web/client && node -e "try{require('fs').accessSync(require('@playwright/test').chromium.executablePath())}catch(e){process.exit(1)}") >/dev/null 2>&1; then \
        echo "ci-web-smoke: SKIPPED — no Playwright browser on this machine."; \
        echo "  'just web-smoke' installs the browsers and runs the full suite;"; \
        echo "  after that this gate runs chromium-only in ~30s on every 'just ci'."; \
        echo "  Until then a green 'just ci' says NOTHING about the panel or Stage UI."; \
    elif node -e "const n=require('net'),s=n.connect(Number(process.env.SMOKE_PORT||4173),'127.0.0.1');s.on('connect',()=>{s.destroy();process.exit(0)});s.on('error',()=>process.exit(1));setTimeout(()=>{s.destroy();process.exit(1)},1500)" 2>/dev/null; then \
        echo "ci-web-smoke: REFUSED — 127.0.0.1:${SMOKE_PORT:-4173} is already serving."; \
        echo "  The suite reuses an existing server locally, so it would attach to that"; \
        echo "  one instead of building and serving the current dist — and report on"; \
        echo "  whichever bundle it is holding. That is wrong in both directions: green"; \
        echo "  on stale-but-good code, red on new-and-good code."; \
        echo "  Stop it (a leftover 'vite preview' is the usual culprit), or send this"; \
        echo "  run somewhere else with SMOKE_PORT=<free port>."; \
        exit 1; \
    else \
        npm --prefix packages/agent/web/client run test:smoke -- --project=chromium; \
    fi

# fmt-check + vet + race tests + connector tag-matrix build + acp + web tag
# build/test + the web client's vitest suite and dist determinism check
# (Node-gated) + the Playwright smokes (Node-, browser- and CI-gated) +
# terva_pprof tag build + public packaging drift check, as a pre-push gate.
ci: lint test ci-acp ci-web ci-scripting ci-workflows ci-web-client ci-web-smoke
    go build -tags terva_no_telegram,terva_no_discord ./...
    # terva_pprof guard: the profiling endpoint (cmd/terva/pprof.go) only
    # compiles under this tag, so the default build can't catch a break in
    # it — same reason ci-acp exists. install-dev is the only shipping use.
    go build -tags terva_pprof ./cmd/terva
    @if [ -x scripts/release.sh ]; then ./scripts/release.sh check-overlay; else echo "ci: SKIPPED check-overlay (scripts/release.sh absent on this tree)"; fi
    # A shipped doc that links into docs/plans, docs/architecture, … resolves
    # here and 404s on the public mirror. Only a gate catches that.
    @if [ -x scripts/release.sh ]; then ./scripts/release.sh check-links; else echo "ci: SKIPPED check-links (scripts/release.sh absent on this tree)"; fi
    # An internal hostname or remote that reaches the public tree used to
    # surface only at cut time — days after the commit that introduced it, and
    # once at the cost of a whole release cycle over a URL in a test fixture.
    # Build the public tree here and scrub it, so it fails on the commit that
    # leaks it rather than on the release that would have shipped it.
    @if [ -x scripts/release.sh ]; then ./scripts/release.sh check-scrub; else echo "ci: SKIPPED check-scrub (scripts/release.sh absent on this tree)"; fi

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
