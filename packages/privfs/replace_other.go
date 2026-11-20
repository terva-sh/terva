//go:build !windows

package privfs

// replaceBusy reports false: a rename here does not wait for a reader of the
// old file, so no error of it ends with time.
func replaceBusy(error) bool { return false }
