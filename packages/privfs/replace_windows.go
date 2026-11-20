//go:build windows

package privfs

import (
	"errors"
	"syscall"

	"golang.org/x/sys/windows"
)

// replaceBusy reports an error of a rename that a short wait can end:
// another handle holds one of the two files. Windows gives the first error
// for a file that a reader holds open, and the second for a file that a
// scanner of new files holds.
//
// ⚠️ A file that the account may not write gets the first error too, so that
// write fails after the whole of replaceWait.
func replaceBusy(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	return errno == windows.ERROR_ACCESS_DENIED || errno == windows.ERROR_SHARING_VIOLATION
}
