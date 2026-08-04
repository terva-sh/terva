#!/usr/bin/env bash
#
# A/B two versions of terva's MODEL-FACING TEXT against a live model, and
# report whether the wording changes what the model does.
#
# Model-facing text is the tool descriptions, the schema field descriptions,
# and the prompts -- everything the model reads that is not the user's own
# words. It is the highest-leverage text in the repo (a recorded session had
# the model pin raati to level 1 for its entire run because the description
# never offered the alternative) and "the new wording reads better" is not
# evidence that it works better. This turns a rewording into a measurement.
#
# An ARM is a binary plus a set of i18n catalog overlays, plus an optional
# per-arm AGENTS.md. Everything else -- workspace, prompt, system prompt,
# seeded state, model, flags -- is identical between the two, so anything
# that differs in the result is the text.
#
#   # tool descriptions: one binary, two overlays
#   scripts/eval/ab.sh --a-overlay tools=scripts/eval/overlays/pre-ste-2026-08.json \
#       -- --provider anthropic --model claude-sonnet-5
#
#   # prompts: same mechanism, different catalog
#   scripts/eval/ab.sh --a-overlay prompts=scripts/eval/overlays/my-prompts.json -- ...
#
#   # schema fields: NOT overlayable, so arm a needs its own binary
#   scripts/eval/build-arm.sh oldschema --patch '...'
#   scripts/eval/ab.sh --a-bin .eval/bin/terva-oldschema -- ...
#
#   # project instructions: per-arm AGENTS.md, served from the arm's
#   # TERVA_HOME. MUST run with --work outside this repo (see --a-agents).
#   # The old arm comes out of git history, never a committed copy: AGENTS.md
#   # is a private, release-excluded file, and a snapshot of it under
#   # scripts/ would ship its content to the public mirror.
#   git show <pre-conversion-ref>:AGENTS.md > /tmp/agents-old.md
#   scripts/eval/ab.sh --a-agents /tmp/agents-old.md \
#       --b-agents AGENTS.md --work ~/.cache/terva-eval/agents-md -- ...
#
# Everything after `--` is passed to terva, so that is where the model goes.
# REPEATS (default 5) sets runs per scenario per arm.
#
# WHAT IT COSTS. Each scenario run is an agent loop, not one request -- an
# uncapped run averaged 4.2 model requests per scenario and one hit 29. The
# loop is capped at --max-steps 3 for that reason. Budget roughly
#   scenarios x repeats x 2 arms x 3 requests
# requests, each carrying the whole tools block (~8-9k tokens, mostly cache
# reads after the first per arm). Six scenarios at 5 repeats came to ~146
# model requests.
set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
BIN="${BIN:-$ROOT/bin/terva}"
REPEATS="${REPEATS:-5}"
WORK="${WORK:-$ROOT/.eval}"

# Source of truth: i18n.KeyedCatalogs() in packages/i18n/keyed.go. A typo here
# would write an overlay to a path nothing reads, and the arm would silently
# serve the shipped English while reporting that it served the overlay.
CATALOGS="tools prompts help"

A_BIN=""; B_BIN=""; A_OVERLAYS=(); B_OVERLAYS=(); A_AGENTS=""; B_AGENTS=""; ONLY=""; VERIFY_ONLY=0
while [ $# -gt 0 ]; do
  case "$1" in
    --a-bin) A_BIN="$2"; shift 2 ;;
    --b-bin) B_BIN="$2"; shift 2 ;;
    --a-overlay) A_OVERLAYS+=("$2"); shift 2 ;;
    --b-overlay) B_OVERLAYS+=("$2"); shift 2 ;;
    --overlay) A_OVERLAYS+=("tools=$2"); shift 2 ;;   # the common case, spelled short
    # Per-arm AGENTS.md, served from the arm's TERVA_HOME (the global slot).
    # This is how project-instruction text gets a clean A/B: the workspace and
    # binary stay identical and only the instruction file differs. CAUTION:
    # readAgentsContext also walks every parent of the workspace, and the
    # default WORK sits inside this repo — so an AGENTS.md experiment must run
    # with --work OUTSIDE the repo tree, or the checkout's own AGENTS.md rides
    # along in both arms and the arm text is contaminated. The pre-flight
    # shows the contamination as an agents-md row bigger than the seed.
    --a-agents) A_AGENTS="$2"; shift 2 ;;
    --b-agents) B_AGENTS="$2"; shift 2 ;;
    --work) WORK="$2"; shift 2 ;;
    # Deepen one scenario. n=5 across a suite answers "did anything obviously
    # break"; a scenario that then shows a difference needs its own repeats
    # before the difference means anything, and paying for the whole suite
    # again to get them is waste.
    --only) ONLY="$2"; shift 2 ;;
    --verify-only) VERIFY_ONLY=1; shift ;;
    --) shift; break ;;
    -h|--help) sed -n '2,37p' "$0"; exit 0 ;;
    *) echo "unknown flag: $1 (terva flags go after --)" >&2; exit 2 ;;
  esac
done

A_BIN="${A_BIN:-$BIN}"; B_BIN="${B_BIN:-$BIN}"
for b in "$A_BIN" "$B_BIN"; do
  [ -x "$b" ] || { echo "no terva binary at $b -- run \`just build\` or build-arm.sh first" >&2; exit 2; }
  # A stale default binary is the quiet way to run a three-variable
  # experiment. bin/terva is whatever you last built, and if its compiled-in
  # English predates the catalog it also embeds, then an overlay arm and a
  # no-overlay arm differ in text you never touched: an empty overlay moved
  # session_inspect by 95 bytes against a binary two commits behind.
  stale="$(find "$ROOT/packages" "$ROOT/cmd" -name '*.go' -newer "$b" -print 2>/dev/null | head -1)"
  [ -n "$stale" ] && echo "warning: $b is older than $stale -- rebuild before trusting this" >&2
done

# Two arms that resolve to the same text compare a thing with itself and
# report a confident "no change". Refuse rather than run it.
if [ "$A_BIN" = "$B_BIN" ] && [ ${#A_OVERLAYS[@]} -eq 0 ] && [ ${#B_OVERLAYS[@]} -eq 0 ] \
   && [ "$A_AGENTS" = "$B_AGENTS" ]; then
  echo "arms are identical: same binary, no overlay, same AGENTS.md on both sides." >&2
  echo "give --a-overlay CATALOG=FILE, or --a-bin/--b-bin, or --a-agents/--b-agents." >&2
  exit 2
fi

# home-a and home-b, and never home-a/home-baseline: TERVA_HOME is
# interpolated into the docs and examples hints, so the two arms' home paths
# must be the SAME LENGTH or the system prompt differs by the difference and
# the arms are no longer comparable.
OUT="$WORK/results"
mkdir -p "$OUT" "$WORK/home-a" "$WORK/home-b"

install_overlays() {   # arm, specs...
  local arm="$1"; shift
  rm -rf "$WORK/home-$arm/locales"
  local spec cat file
  for spec in "$@"; do
    cat="${spec%%=*}"; file="${spec#*=}"
    case " $CATALOGS " in *" $cat "*) ;; *)
      echo "unknown catalog '$cat' (have: $CATALOGS)" >&2; exit 2 ;;
    esac
    [ -f "$file" ] || { echo "no such overlay: $file" >&2; exit 2; }
    mkdir -p "$WORK/home-$arm/locales/$cat"
    cp "$file" "$WORK/home-$arm/locales/$cat/en.json"
  done
}
install_overlays a ${A_OVERLAYS+"${A_OVERLAYS[@]}"}
install_overlays b ${B_OVERLAYS+"${B_OVERLAYS[@]}"}

# Like the locale overlays: always reset, install only what this run asked
# for. A file left by an earlier experiment would silently join this one.
install_agents() {   # arm, file-or-empty
  rm -f "$WORK/home-$1/AGENTS.md"
  [ -z "$2" ] && return
  [ -f "$2" ] || { echo "no such AGENTS.md arm file: $2" >&2; exit 2; }
  cp "$2" "$WORK/home-$1/AGENTS.md"
}
install_agents a "$A_AGENTS"
install_agents b "$B_AGENTS"

# Credentials: symlink rather than copy, so no secret is duplicated into a
# scratch directory that outlives the run. A token refresh writes through to
# the real file, which is where it belongs.
HOME_REAL="${TERVA_HOME:-$HOME/Library/Application Support/terva}"
[ -d "$HOME_REAL" ] || HOME_REAL="$HOME/.terva"
for arm in a b; do
  [ -e "$HOME_REAL/auth.json" ] && ln -sf "$HOME_REAL/auth.json" "$WORK/home-$arm/auth.json"
done

# No mapfile: macOS ships bash 3.2, where it does not exist.
IDS=()
while IFS= read -r line; do IDS+=("$line"); done < <(python3 -c "
import json
ids=[s['id'] for s in json.load(open('$HERE/scenarios.json'))['scenarios']]
only=[x for x in '$ONLY'.split(',') if x]
if only:
    for x in only:
        if x not in ids: raise SystemExit('no such scenario: '+x)
    ids=[i for i in ids if i in only]
print('\n'.join(ids))")
[ ${#IDS[@]} -gt 0 ] || { echo "no scenarios selected" >&2; exit 2; }

describe_arm() {   # arm, bin, agents, specs...
  local arm="$1" bin="$2" agents="$3"; shift 3
  local what=""
  [ "$bin" != "$BIN" ] && what="bin=$(basename "$bin")"
  [ -n "$agents" ] && what="$what${what:+, }agents=$(basename "$agents")"
  [ $# -gt 0 ] && what="$what${what:+, }overlay $*"
  echo "arm $arm:   ${what:-shipped English, default binary}"
}
describe_arm a "$A_BIN" "$A_AGENTS" ${A_OVERLAYS+"${A_OVERLAYS[@]}"}
describe_arm b "$B_BIN" "$B_AGENTS" ${B_OVERLAYS+"${B_OVERLAYS[@]}"}
echo "terva:   $*"
echo "runs:    ${#IDS[@]} scenarios x $REPEATS repeats x 2 arms = $(( ${#IDS[@]} * REPEATS * 2 ))"
echo

# A results directory that cannot say what it compared is a directory of
# numbers. Write the arms down next to them -- and refuse to resume into a
# directory whose numbers came from different text.
#
# This guard is the one the harness cannot do without. Runs resume by keeping
# any transcript already on disk, so pointing a second experiment at the same
# directory serves last week's arm-a transcripts as this week's arm a: a
# scored, plausible, entirely fictional comparison, with nothing on screen to
# suggest anything went wrong.
python3 -c "
import json,os,sys
path=sys.argv[8]
now={'a':{'bin':sys.argv[1],'overlays':sys.argv[3].split(),'agents':sys.argv[9]},
     'b':{'bin':sys.argv[2],'overlays':sys.argv[4].split(),'agents':sys.argv[10]},
     'terva_args':sys.argv[5]}
if os.path.exists(path):
    was=json.load(open(path))
    keys=('a','b','terva_args')
    if any(was.get(k)!=now[k] for k in keys):
        sys.exit('results in %s came from different arms:\n  was: %s\n  now: %s\n'
                 'resuming would score the old transcripts as this run.\n'
                 'pass --work .eval/<a-new-name>, or delete that directory.'
                 % (os.path.dirname(path),
                    json.dumps({k:was.get(k) for k in keys}),
                    json.dumps({k:now[k] for k in keys})))
    now['scenarios']=sorted(set(was.get('scenarios',[]))|set(sys.argv[7].split()))
else:
    # Transcripts with no manifest beside them predate this guard, or were
    # written by hand. Either way nothing records which text produced them,
    # so they cannot be resumed into -- an unlabelled arm is not a control.
    stray=[f for f in os.listdir(os.path.dirname(path)) if f.endswith('.ndjson')]
    if stray:
        sys.exit('%s holds %d transcript(s) but no manifest, so what produced '
                 'them is unknown.\nresuming would mix them into this run. '
                 'pass --work .eval/<a-new-name>, or delete that directory.'
                 % (os.path.dirname(path), len(stray)))
    now['scenarios']=sys.argv[7].split()
now['repeats']=int(sys.argv[6])
json.dump(now, open(path,'w'), indent=2)" \
  "$A_BIN" "$B_BIN" "${A_OVERLAYS[*]:-}" "${B_OVERLAYS[*]:-}" "$*" "$REPEATS" "${IDS[*]}" \
  "$OUT/manifest.json" "$A_AGENTS" "$B_AGENTS" || exit 3

# PRE-FLIGHT: show where the arms actually differ, as the model will see it.
# --dump-prompt makes no model call, so this is free, and it is the cheapest
# moment to catch an arm that did not apply, applied in the wrong place, or
# applied more widely than intended. Two earlier arms in this harness were
# wrong in exactly those ways and both scored a clean-looking result.
# When a selected scenario seeds memory, seed one entry for the dump too: the
# policy block renders only when a scope is non-empty, so without this an
# overlay on memory.policy would pre-flight as silence — indistinguishable
# from an arm that did not apply.
SEEDS_MEM="$(python3 -c "
import json
sel=set('${IDS[*]}'.split())
print(int(any(s.get('seed_memory') for s in
      json.load(open('$HERE/scenarios.json'))['scenarios'] if s['id'] in sel)))")"
for arm in a b; do
  armbin="$A_BIN"; [ "$arm" = b ] && armbin="$B_BIN"
  vws="$WORK/verify"; rm -rf "$vws"; mkdir -p "$vws"
  rm -rf "$WORK/home-$arm/memory"
  if [ "$SEEDS_MEM" = 1 ]; then
    mkdir -p "$WORK/home-$arm/memory"
    printf -- '- preflight probe entry\n' > "$WORK/home-$arm/memory/user.md"
  fi
  TERVA_HOME="$WORK/home-$arm" "$armbin" --cwd "$vws" --json \
    --dump-prompt=sizes "$@" "verify" > "$OUT/sizes-$arm.txt" 2>/dev/null
done
python3 "$HERE/arm-diff.py" "$OUT/sizes-a.txt" "$OUT/sizes-b.txt"
diffrc=$?
# An AGENTS.md that reaches the prompt through the workspace's parent walk is
# in BOTH arms, so the arm diff cannot show it — say so, because ambient
# instruction text shapes every scenario it touches.
if [ -z "$A_AGENTS$B_AGENTS" ] && grep -q "agents-md" "$OUT/sizes-a.txt" 2>/dev/null; then
  echo "note: an ambient AGENTS.md rides in both arms (the workspace sits under a directory that has one)."
fi
if [ "$VERIFY_ONLY" = 1 ]; then exit $diffrc; fi
if [ $diffrc -ne 0 ]; then
  printf 'run anyway? [y/N] '
  read -r yn; case "$yn" in y|Y) ;; *) echo "stopped."; exit 3 ;; esac
fi
echo

for arm in a b; do
  armbin="$A_BIN"; [ "$arm" = b ] && armbin="$B_BIN"
  for id in "${IDS[@]}"; do
    prompt="$(python3 -c "
import json
for s in json.load(open('$HERE/scenarios.json'))['scenarios']:
    if s['id']=='$id': print(s['prompt'])")"
    for r in $(seq 1 "$REPEATS"); do
      f="$OUT/$arm.$id.$r.ndjson"
      [ -s "$f" ] && { printf 'x'; continue; }   # resume: keep what we paid for
      # A fresh workspace per run. A scenario must not see a file an earlier
      # scenario wrote, or the second arm answers a different question.
      ws="$WORK/ws"; rm -rf "$ws"; mkdir -p "$ws/out"
      printf 'recieve this\n' > "$ws/notes.txt"
      : > "$ws/diagram.png"; : > "$ws/out/chart.png"

      # seed_git makes the workspace a git repository with the seed state
      # committed, for scenarios that probe commit/push behaviour. The model's
      # edit is then the only dirty state, and any commit is the model's own
      # decision.
      seedgit="$(python3 -c "
import json
for s in json.load(open('$HERE/scenarios.json'))['scenarios']:
    if s['id']=='$id': print(1 if s.get('seed_git') else 0)")"
      if [ "$seedgit" = 1 ]; then
        git -C "$ws" init -q -b main
        git -C "$ws" -c user.email=eval@local -c user.name=eval add -A
        git -C "$ws" -c user.email=eval@local -c user.name=eval commit -qm seed
      fi

      # Memory is durable BY DESIGN, which for an A/B means one run's save
      # leaks into the next run's prompt: the injected block grows run over
      # run, and arm a's twentieth repeat answers a different question than
      # its first. Reset it every run; re-seed when the scenario asks.
      #
      # seed_memory is a flat list of USER-scope entries (memory/user.md, a
      # fixed path). Project scope would mean reimplementing core.ProjectKey
      # here; a scenario that needs it should earn that code, not inherit it.
      # Seeding matters beyond the entries themselves: the memory POLICY block
      # renders only when a scope is non-empty, so a scenario probing the
      # policy's wording must seed at least one entry or the text under test
      # is absent from the prompt entirely.
      rm -rf "$WORK/home-$arm/memory"
      seedmem="$(python3 -c "
import json
for s in json.load(open('$HERE/scenarios.json'))['scenarios']:
    if s['id']=='$id': print(json.dumps(s.get('seed_memory') or ''))" )"
      if [ "$seedmem" != '""' ]; then
        mkdir -p "$WORK/home-$arm/memory"
        python3 -c "
import json,sys
open(sys.argv[2],'w').write(''.join('- '+e+'\n' for e in json.loads(sys.argv[1])))" \
          "$seedmem" "$WORK/home-$arm/memory/user.md"
      fi

      # Some scenarios need state that already exists -- a task board with work
      # on it, say. Seeding beats having the model create the state first: a
      # create step doubles the cost, and the wording of the tool that creates
      # it becomes a second variable in a two-arm experiment.
      #
      # The task store names its board after the SESSION ID, which terva
      # generates as a fresh uuid -- so seeding a board means pinning the id
      # too. --session takes a PATH and opens it if it exists, so writing a
      # one-line session file with an id we chose gets both: terva adopts that
      # id, and the board we wrote at tasks-<id>.json is the one it reads.
      sess=""
      seed="$(python3 -c "
import json
for s in json.load(open('$HERE/scenarios.json'))['scenarios']:
    if s['id']=='$id': print(json.dumps(s.get('seed_tasks') or ''))" )"
      if [ "$seed" != '""' ]; then
        sid="eval-$id"
        sess="$WORK/home-$arm/seed-$id.jsonl"
        mkdir -p "$WORK/home-$arm/tasks"
        python3 -c "
import json,sys
sid, seed, sesspath, boardpath, cwd = sys.argv[1], json.loads(sys.argv[2]), sys.argv[3], sys.argv[4], sys.argv[5]
# One meta row is a complete session file: OpenSession reads the id from it.
json.dump({'type':'meta','meta':{'id':sid,'cwd':cwd,'started':'2026-01-01T00:00:00Z','format_version':1},
           'at':'2026-01-01T00:00:00Z'}, open(sesspath,'w'))
open(sesspath,'a').write('\n')
tasks=[dict(t, id=str(i+1), created_at='2026-01-01T00:00:00Z',
            updated_at='2026-01-01T00:00:00Z',
            active_form=t.get('active_form', t['title']))
       for i,t in enumerate(seed)]
json.dump({'tasks':tasks,'next_id':len(tasks)+1}, open(boardpath,'w'))" \
          "$sid" "$seed" "$sess" "$WORK/home-$arm/tasks/tasks-$sid.json" "$ws"
      fi
      # --approval workspace, NOT plan: plan refuses the mutating tools, so the
      # model never gets to choose between write and edit -- it just thrashes
      # looking for a way around the refusal, and the transcript records
      # recovery behaviour instead of the decision under test.
      TERVA_HOME="$WORK/home-$arm" "$armbin" --cwd "$ws" \
        ${sess:+--session "$sess"} \
        --approval workspace --max-steps 3 "$@" --json "$prompt" \
        > "$f" 2>"$OUT/$arm.$id.$r.err"
      printf '.'
    done
  done
  echo " [arm $arm done]"
done

echo
python3 "$HERE/score.py" "$OUT"
