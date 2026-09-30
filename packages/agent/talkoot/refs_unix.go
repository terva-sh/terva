//go:build unix

package talkoot

import "syscall"

// openNonblock opens a named pipe without waiting for a writer, so a FIFO a
// member planted in the checkout cannot hang the read. A regular file ignores
// the flag.
const openNonblock = syscall.O_NONBLOCK
