// Package procgrouptest holds the liveness checks that the process-group
// teardown tests share. Only tests import it.
//
// The checks are Unix-only. This file carries no build constraint, so the
// package still exists, empty, on Windows.
package procgrouptest
