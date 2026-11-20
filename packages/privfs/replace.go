package privfs

import (
	"os"
	"time"
)

// replaceWait is how long replace tries again. Go's own build tool waits the
// same time for the same errors (cmd/internal/robustio).
const replaceWait = 2 * time.Second

// replace renames tmp over path, the last step of an atomic write.
//
// 🚨 Windows refuses the rename while another handle holds the old file open,
// and Go opens a file with no leave to delete it. So a read of a record at
// the moment of its write made the write fail: a routine that had to pause
// stayed active. The reader is done in microseconds, so replace tries again.
// A system with no such refusal renames one time (replaceBusy).
func replace(tmp, path string) error {
	return replaceWith(os.Rename, replaceBusy, replaceWait, tmp, path)
}

// replaceWith is replace over a rename and a test of its error that the
// caller names. It returns the last error of rename once wait is over.
func replaceWith(rename func(from, to string) error, busy func(error) bool, wait time.Duration, tmp, path string) error {
	err := rename(tmp, path)
	pause := time.Millisecond
	for start := time.Now(); err != nil && busy(err) && time.Since(start)+pause < wait; {
		time.Sleep(pause)
		err = rename(tmp, path)
		// A reader that holds the file for long gets fewer tries, not more.
		if pause < 64*time.Millisecond {
			pause *= 2
		}
	}
	return err
}
