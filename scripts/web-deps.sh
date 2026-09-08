#!/bin/sh
# The ONE place that decides whether the web client's node_modules is usable,
# and the only place that installs it.
#
#   web-deps.sh install  npm ci, then stamp the tree with the toolchain that
#                        built it. The always-clean policy.
#   web-deps.sh ensure   install only if the tree is stale. The reuse-if-fresh
#                        policy `just ci` wants, so a gate with nothing to do
#                        costs nothing.
#   web-deps.sh status   say whether the tree is stale and why. Exit 0 when it
#                        is fresh, 3 when it is stale. Installs nothing.
#
# Each gate still picks its own policy, exactly as scripts/web-dist.sh says it
# must. What moves here is the DEFINITION of stale, which no gate should own
# privately.
#
# The definition needed widening. It used to be "node_modules is absent, or the
# lock file is newer", a rule that only ever watches the repository. It cannot
# see the other half of an install, which is the machine that performed it. A
# Node upgrade therefore left a tree installed by the old Node in place forever,
# because nothing in git had changed.
#
# That is not hypothetical. Node 25 rewrote which Web Storage globals it defines
# at startup, the client's vitest suite died on it with 29 failures, and a
# reinstall was among the first things ruled out. The evidence for ruling it out
# was that node_modules looked current, which was true and meant nothing. The
# stamp is what makes that evidence real.
#
# A bare `npm --prefix packages/agent/web/client ci` leaves no stamp, so the
# next `ensure` reinstalls once. That is the safe direction and it is cheap. Use
# this script instead and it does not happen.
#
# POSIX sh on purpose: the CI containers are busybox until a step installs bash.
set -eu

CLIENT=packages/agent/web/client
STAMP_BASENAME=.terva-deps-stamp

usage() {
    echo "usage: web-deps.sh <install|ensure|status> [--dir DIR]" >&2
    exit 2
}

MODE=""
case "${1:-}" in
    install | ensure | status)
        MODE="$1"
        shift
        ;;
    *) usage ;;
esac

while [ $# -gt 0 ]; do
    case "$1" in
        # Tests point this at a fixture. Nothing else should need it.
        --dir)
            [ $# -ge 2 ] || usage
            CLIENT="$2"
            shift 2
            ;;
        *) usage ;;
    esac
done

STAMP="$CLIENT/node_modules/$STAMP_BASENAME"

require_npm() {
    if ! command -v npm >/dev/null 2>&1; then
        echo "web-deps: npm is not on PATH" >&2
        exit 2
    fi
}

# current_stamp prints the toolchain facts that decide the shape of an install.
#
# Every field is compared, and any difference is stale. Node and npm because
# they resolve and run the tree. The platform and the architecture because npm
# installs binaries for exactly one of them: the optional esbuild and rollup
# packages differ per target, so a machine that moves from Rosetta to native
# arm64 has a node_modules full of the wrong executables and a lock file that
# still matches.
#
# A Node PATCH bump also invalidates the tree here, which is stricter than ABI
# alone demands. It is deliberate. The bug that prompted this was a behavior
# change in the runtime, not a broken binding, and the cost of being wrong in
# this direction is one 20-second install.
current_stamp() {
    printf 'node %s\n' "$(node --version)"
    printf 'npm %s\n' "$(npm --version)"
    printf 'platform %s\n' "$(uname -s)"
    printf 'arch %s\n' "$(uname -m)"
}

# stale_reason prints why the tree needs an install, or nothing when it is fresh.
stale_reason() {
    if [ ! -d "$CLIENT/node_modules" ]; then
        echo "node_modules is absent"
        return
    fi
    if [ "$CLIENT/package-lock.json" -nt "$CLIENT/node_modules" ]; then
        echo "package-lock.json is newer than node_modules"
        return
    fi
    if [ ! -f "$STAMP" ]; then
        echo "no toolchain stamp: node_modules predates this check, or a bare 'npm ci' wrote it"
        return
    fi

    # Field by field, so the reason names the thing that moved. A diff of the
    # whole stamp would report "it changed" and leave the reader to spot which
    # line, which is the entire question they are asking.
    current_stamp | while IFS=' ' read -r field want; do
        have="$(awk -v f="$field" '$1 == f { print $2; exit }' "$STAMP")"
        if [ "$have" != "$want" ]; then
            if [ -z "$have" ]; then
                echo "the stamp has no $field field (this machine has $want)"
            else
                echo "$field changed: node_modules was installed with $have, this machine has $want"
            fi
            break
        fi
    done
}

install() {
    require_npm
    npm --prefix "$CLIENT" ci
    # After the install, never before. A stamp written first would survive a
    # failed install and certify a tree that was never built.
    current_stamp >"$STAMP"
}

case "$MODE" in
    install)
        install
        ;;
    ensure)
        require_npm
        reason="$(stale_reason)"
        if [ -n "$reason" ]; then
            echo "web-deps: reinstalling, because $reason"
            install
        fi
        ;;
    status)
        require_npm
        reason="$(stale_reason)"
        if [ -n "$reason" ]; then
            echo "stale: $reason"
            exit 3
        fi
        echo "fresh"
        ;;
esac
