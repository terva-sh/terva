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

regen() {
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
