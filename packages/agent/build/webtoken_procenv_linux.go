//go:build linux

package build

import (
	"bytes"
	"os"
)

// webTokenInProcEnviron reports whether WebTokenEnv is still recoverable from
// this process's /proc/<pid>/environ.
//
// The env-route scrub (os.Unsetenv in webTokenFromEnv) rewrites glibc's
// environ[] — the array os.Environ() copies — so a child spawned with
// cmd.Env = os.Environ() no longer sees the token. It does NOT, and cannot,
// move the kernel's env_start/env_end or overwrite the KEY=VALUE bytes the
// kernel wrote to the initial process stack at execve. /proc/<pid>/environ
// dumps exactly that stack region, so the value survives the scrub there and
// any same-UID process (the agent's own shell included) can read it back.
//
// Presence is decided by env-var name only; the value is never read or
// returned. A missing /proc (some containers, hidepid) reads as "not exposed":
// there is nothing this warning can add.
func webTokenInProcEnviron() bool {
	b, err := os.ReadFile("/proc/self/environ")
	if err != nil {
		return false
	}
	prefix := []byte(WebTokenEnv + "=")
	for entry := range bytes.SplitSeq(b, []byte{0}) {
		if bytes.HasPrefix(entry, prefix) && len(entry) > len(prefix) {
			return true
		}
	}
	return false
}
