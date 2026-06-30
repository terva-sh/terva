//go:build terva_pprof

package modes

// defaultRedrawFPS is 0 (uncapped) under the terva_pprof tag: while
// profiling we want to see every frame the loop would paint, since any
// redundant draw is an optimization target. Opt into the cap per-run with
// TERVA_REDRAW_FPS to debug the capped behaviour.
const defaultRedrawFPS = 0
