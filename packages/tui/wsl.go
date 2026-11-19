package tui

import (
	"os"
	"runtime"
	"strings"
)

// IsWSL reports whether this process runs under the Windows Subsystem for
// Linux.
//
// It matters wherever terva reaches for the desktop. A WSL distribution is
// Linux to the Go runtime and has no Linux desktop behind it: the clipboard is
// Windows' own, and so is whatever opens a file. The environment variables are
// the cheap answer and the kernel release is the one that survives a sudo or a
// scrubbed environment.
func IsWSL() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	if os.Getenv("WSL_INTEROP") != "" || os.Getenv("WSL_DISTRO_NAME") != "" {
		return true
	}
	b, _ := os.ReadFile("/proc/sys/kernel/osrelease")
	return strings.Contains(strings.ToLower(string(b)), "microsoft")
}
