# Profiling terva

How to measure where the harness spends CPU and memory — chiefly the
interactive TUI, whose render loop is the usual suspect when terva pegs a
core while streaming. For what that loop actually does, see
[tui.md](tui.md); this doc is the tooling around it.

## TL;DR

```bash
just install-dev                                   # non-stripped, terva_pprof tag, into GOBIN
TERVA_PPROF=localhost:6060 terva                   # start with the endpoint live
# in another shell, while it's busy:
go tool pprof -http=: "$(command -v terva)" 'http://localhost:6060/debug/pprof/profile?seconds=25'
```

## The profiling build

The `net/http/pprof` endpoint is **off in every shipped build**. It
compiles in only under the `terva_pprof` build tag, and even then stays
dormant until you set the `TERVA_PPROF` environment variable. This is
deliberate: a heap or goroutine dump can expose in-memory secrets
(`auth.json`-class credentials) to anyone who can reach the port — the
same exposure `procenv.Harden()` guards against by disabling core dumps.

`just install-dev` is the one build that opts in. Compared to
`just install` it:

- adds `-tags terva_pprof` (links the `/debug/pprof/*` handlers),
- drops `-s -w` and `-trimpath` (keeps symbols and source paths, so
  profilers resolve function names and `list` can show source),
- keeps optimizations on (unlike `just debug`, which adds `-N -l` for
  breakpoints and would distort a CPU profile),
- stamps the version as `0.0.0-debug` so `terva --version` tells the dev
  binary apart from a release install.

The implementation is two tag-gated files: `cmd/terva/pprof.go`
(`//go:build terva_pprof`, the real endpoint) and
`cmd/terva/pprof_stub.go` (`//go:build !terva_pprof`, a no-op
`maybeStartPprof`). `just ci` builds the tagged file so it can't break
silently.

The `terva_pprof` tag also defaults the streaming **redraw cap** to
uncapped (normal builds cap at 30fps), so a CPU profile reflects every
frame the loop would paint rather than a throttled subset — any redundant
draw is then an optimization target. Set `TERVA_REDRAW_FPS` to impose a
cap in a profiling build, or to raise/disable it in a normal one (see
[tui.md](tui.md#redraw-rate)).

> Bind to **localhost only**. `TERVA_PPROF` takes a `host:port`; anything
> non-local exposes profiling data — and the secrets a heap dump may
> contain — to the network.

## Capturing a profile

Run terva with the endpoint live (it serves on a private goroutine and
writes nothing to the terminal, so it coexists with the TUI):

```bash
TERVA_PPROF=localhost:6060 terva
```

Then, from a second shell, while terva is in the state you want to
measure (e.g. mid-stream):

| What | Command |
|---|---|
| CPU (where time goes) | `go tool pprof -http=: "$(command -v terva)" 'http://localhost:6060/debug/pprof/profile?seconds=25'` |
| Heap / allocations | `go tool pprof -http=: "$(command -v terva)" 'http://localhost:6060/debug/pprof/allocs'` |
| Goroutine dump (find a spinning goroutine) | `curl -s localhost:6060/debug/pprof/goroutine?debug=2` |
| Execution trace (scheduler/GC churn) | `curl -o trace.out 'localhost:6060/debug/pprof/trace?seconds=5'` then `go tool trace trace.out` |

The CPU profile collects for its `seconds=` window, so start it and then
drive terva into the busy state so the window overlaps the spike. The
`-http=:` form opens an interactive flame graph in the browser; drop it
and use `top30` / `list <Func>` in the terminal instead.

## The free GC probe

Before reaching for pprof, `GODEBUG=gctrace=1` costs nothing and directly
answers "is this garbage collection?" — useful when CPU sawtooths
(spike, drop, spike) under load, the classic signature of a hot path
allocating faster than it needs to:

```bash
TERVA_PPROF=localhost:6060 GODEBUG=gctrace=1 terva 2>/tmp/terva-gctrace.log
```

Each GC cycle prints one line to the log (redirected so it doesn't
corrupt the TUI). Many cycles per second during streaming, with the
cumulative GC CPU percentage climbing into double digits, means the
redraw path is allocating too much — go find it in the `allocs` profile.

## Zero-instrument sampling (macOS)

For a 60-second triage without the pprof endpoint, macOS `sample` stack-
samples a running process. It needs symbols, so point it at a
**non-stripped** binary (`just install-dev`, or `just build` is stripped
— don't use a release install):

```bash
sample "$(pgrep -n terva)" 10 -file /tmp/terva.sample.txt
```

## Reading the result

Common hot spots in a TUI CPU profile and what they implicate:

- `github.com/alecthomas/chroma/...` (`Tokenise`, `Coalesce`) —
  syntax-highlighting work. Static blocks are cached
  (`packages/tui/highlight.go`); a *growing* streaming block misses the
  cache every frame.
- `terva.sh/terva/packages/tui` view/layout functions — full-transcript
  re-render. The streaming pacer (`paintPaceInterval`, ~16ms) and the
  120ms animation ticker (`packages/agent/modes/interactive.go`) both
  drive redraws while a turn is busy; redraws are throttled to ~16ms.
- `runtime.mallocgc` + `runtime.gcBgMarkWorker` near the top — allocation
  churn feeding GC; corroborate with the `allocs` profile and the
  `gctrace` log above.

See [tui.md](tui.md) for the render loop these point into.
