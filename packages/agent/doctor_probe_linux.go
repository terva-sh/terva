//go:build linux

package agent

import (
	"fmt"
	"os"
	"strings"
	"syscall"
)

// noNewPrivs reports the process's no_new_privs bit, read from
// /proc/self/status. When set, a subsequent execve can never gain privileges —
// which also means a root-owned setuid sudo cannot elevate, the exact failure
// mode that motivated TW-004. This reads only the flag, never a secret.
func noNewPrivs() string {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return "unknown (/proc/self/status unreadable)"
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		v, ok := strings.CutPrefix(line, "NoNewPrivs:")
		if !ok {
			continue
		}
		switch strings.TrimSpace(v) {
		case "0":
			return "no (new privileges allowed; setuid sudo can elevate)"
		case "1":
			return "yes (new privileges blocked; setuid sudo cannot elevate)"
		default:
			return "unknown (unexpected value)"
		}
	}
	return "unknown (field absent)"
}

// coreDumpStatus reports whether core dumps are disabled (RLIMIT_CORE soft
// limit 0), which procenv.Harden() sets at startup so an in-memory credential
// never lands in a crash file.
func coreDumpStatus() string {
	var rl syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_CORE, &rl); err != nil {
		return "unknown (getrlimit failed)"
	}
	switch rl.Cur {
	case 0:
		return "disabled (RLIMIT_CORE=0)"
	case ^uint64(0):
		return "enabled (RLIMIT_CORE=unlimited)"
	default:
		return fmt.Sprintf("enabled (RLIMIT_CORE soft=%d)", rl.Cur)
	}
}
