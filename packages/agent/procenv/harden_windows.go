//go:build windows

package procenv

// Harden is a no-op on Windows: crash dumps go through WER, which is
// system policy rather than a per-process rlimit, and suppressing it
// (SetErrorMode/WerAddExcludedApplication) costs more complexity than
// the leak risk justifies today.
func Harden() {}
