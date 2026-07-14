//go:build !linux

package build

// webTokenInProcEnviron is a no-op off Linux: /proc/<pid>/environ is a Linux
// interface, and Linux is the deployment target this note addresses. macOS and
// Windows expose process environments differently; see the Linux implementation
// for the mechanism the warning describes.
func webTokenInProcEnviron() bool { return false }
