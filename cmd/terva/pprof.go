//go:build terva_pprof

package main

import (
	"fmt"
	"net/http"
	_ "net/http/pprof" // registers /debug/pprof/* on http.DefaultServeMux
	"os"
)

// maybeStartPprof serves net/http/pprof's /debug/pprof/* handlers when
// TERVA_PPROF=host:port is set (use localhost:6060). It runs on a private
// goroutine and writes nothing to the terminal, so it coexists with the TUI.
//
// This file is compiled in only under the `terva_pprof` build tag (see
// `just install-dev`); standard and release builds get the no-op in
// pprof_stub.go and never link the profiling endpoints at all. That is
// deliberate: a heap or goroutine dump can expose in-memory secrets
// (auth.json) to anyone who can reach the port — the same exposure
// procenv.Harden() guards against by killing core dumps. So the endpoint
// is double-gated (build tag AND env var) and binds wherever you point
// it: keep that localhost.
func maybeStartPprof() {
	addr := os.Getenv("TERVA_PPROF")
	if addr == "" {
		return
	}
	go func() {
		// ListenAndServe returns only on error; report a failed bind but
		// never take down the session over a debug listener.
		if err := http.ListenAndServe(addr, nil); err != nil {
			fmt.Fprintln(os.Stderr, "terva: pprof:", err)
		}
	}()
}
