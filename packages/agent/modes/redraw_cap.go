//go:build !terva_pprof

package modes

// defaultRedrawFPS caps streaming/animation repaints at 30fps in normal
// builds (see resolveBusyRedrawInterval). Profiling builds set this to 0
// (uncapped) in redraw_cap_pprof.go so every redundant draw is visible in
// a CPU profile. Override either with TERVA_REDRAW_FPS.
const defaultRedrawFPS = 30
