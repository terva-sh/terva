//go:build unix

package procenv

import (
	"os"
	"syscall"
)

// Harden applies cheap process-level hardening at startup. Today
// that is one thing: core dumps off (RLIMIT_CORE=0), because the
// process holds provider credentials (auth.json contents, OAuth
// tokens) in memory and a core file would write them to disk.
//
// TERVA_KEEP_CORE_DUMPS=1 skips it for crash debugging. Failures are
// ignored: hardening is best-effort and must never block startup.
func Harden() {
	if os.Getenv("TERVA_KEEP_CORE_DUMPS") == "1" {
		return
	}
	_ = syscall.Setrlimit(syscall.RLIMIT_CORE, &syscall.Rlimit{Cur: 0, Max: 0})
}
