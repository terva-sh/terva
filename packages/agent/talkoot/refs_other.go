//go:build !unix

package talkoot

// openNonblock is zero where the checkout cannot hold a named pipe that
// blocks an open.
const openNonblock = 0
