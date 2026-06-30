//go:build !terva_pprof

package main

// maybeStartPprof is a no-op in standard builds. The net/http/pprof
// endpoints are linked in only under the `terva_pprof` tag (see pprof.go
// and `just install-dev`), keeping the profiling surface — and its
// heap/goroutine dump footgun — out of release binaries entirely.
func maybeStartPprof() {}
