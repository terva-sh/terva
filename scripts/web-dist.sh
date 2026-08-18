#!/bin/sh
# The ONE place that regenerates the web client's committed assets and (with
# `check`) asserts they are the deterministic product of the current source.
#
#   web-dist.sh regen  — extract the client's i18n catalog, mirror the
#                        canonical web/stage catalogs into the client bundle
#                        for offline/first-paint, and build dist/ (Vite).
#   web-dist.sh check  — regen, then fail unless git sees no change in the
#                        committed assets.
#
# `just web-build`, `just web-check`, `just ci` (ci-web-client), and the
# remote web-client CI job all funnel through this file. Before it existed
# the regenerate steps and the asset-path list were hand-copied across those
# gates, which is how a gate ends up asserting over a different tree than
# the one developers build.
#
# Callers own dependency installation (npm ci): each gate has its own install
# policy — always-clean, reuse-if-fresh, container-fresh — and this script
# must not decide it for them. POSIX sh on purpose: the CI containers are
# busybox until a step installs bash.
set -eu

CLIENT=packages/agent/web/client

# The committed assets the check asserts over. Space-separated on purpose —
# expanded unquoted below; none of these paths may ever contain a space.
ASSETS="$CLIENT/dist $CLIENT/src/locales packages/i18n/locales/web/en.json packages/i18n/locales/stage/en.json"

# The oldest Node this build tolerates. vite-plugin-pwa reaches for the GLOBAL
# `crypto` object, which Node added in v19. Below that the build dies inside a
# rollup worker with "crypto is not defined" — and only AFTER deleting
# dist/sw.js and dist/workbox-*.js, so the failure buries its cause in worker
# output and leaves the tree dirty as well as unbuilt. 20 rather than 19,
# because 19 was never an LTS line and 20 is the oldest one still supported.
#
# Worth a guard rather than a README line: Debian and Ubuntu still ship Node 18
# (end-of-life April 2025), so this is a fully patched machine that cannot build
# the panel while every Go gate on it passes. .mise.toml pins 22 for anyone
# using mise; this is the floor for everyone else.
NODE_MIN_MAJOR=20

require_node() {
    if ! command -v node >/dev/null 2>&1; then
        echo "web-dist: node is not on PATH — this build needs Node >= v$NODE_MIN_MAJOR" >&2
        exit 2
    fi
    major=$(node -p 'process.versions.node.split(".")[0]' 2>/dev/null) || major=''
    case "$major" in
    '' | *[!0-9]*)
        echo "web-dist: could not read a Node version from '$(command -v node)'" >&2
        exit 2
        ;;
    esac
    if [ "$major" -lt "$NODE_MIN_MAJOR" ]; then
        echo "web-dist: Node $(node --version) is too old — this build needs >= v$NODE_MIN_MAJOR." >&2
        echo "  vite-plugin-pwa uses the global 'crypto' object, which Node added in v19." >&2
        echo "  An older Node fails deep inside a rollup worker, having already deleted" >&2
        echo "  dist/sw.js and dist/workbox-*.js — 'git restore' them if you have hit that." >&2
        echo "  With mise installed, 'mise install' picks up the pin in .mise.toml." >&2
        exit 2
    fi
}

regen() {
    # Before anything is written or deleted: a version failure must cost a
    # message, not a dirty tree.
    require_node
    npm --prefix "$CLIENT" run i18n-extract
    mkdir -p "$CLIENT/src/locales/stage"
    for f in packages/i18n/locales/web/*.json; do
        [ -e "$f" ] || continue
        case "$f" in */en.json) continue ;; esac
        cp "$f" "$CLIENT/src/locales/"
    done
    for f in packages/i18n/locales/stage/*.json; do
        [ -e "$f" ] || continue
        case "$f" in */en.json) continue ;; esac
        cp "$f" "$CLIENT/src/locales/stage/"
    done
    npm --prefix "$CLIENT" run build
}

check() {
    regen
    # shellcheck disable=SC2086
    if git diff --quiet -- $ASSETS; then
        echo "web-dist check: OK — committed web assets match a fresh build"
    else
        echo "web-dist check: committed web assets DIFFER from a fresh build —"
        # shellcheck disable=SC2086
        git -c color.ui=never diff --stat -- $ASSETS
        echo "  hashed-asset renames + index.html/sw.js only -> run 'just web-build' and commit;"
        echo "  unrelated or churning files                  -> the build may be nondeterministic; investigate."
        exit 1
    fi
}

case "${1:-}" in
regen) regen ;;
check) check ;;
*)
    echo "usage: $0 regen|check" >&2
    exit 2
    ;;
esac
