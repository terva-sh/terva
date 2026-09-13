package agent

import (
	"io"
	"os"
	"testing"
)

// TestMain silences the subcommand help screens for the whole package.
//
// 🪤 Not cosmetic. `go test` prints a package's ENTIRE buffered output when any
// one test in it fails, so seventeen commands' help text — written by tests
// that passed — arrives as several hundred lines wrapped around the single
// assertion that actually failed. That happened on a real CI run: one
// `--- FAIL` line sat in the middle of 581 lines of usage text, and finding it
// meant scrolling past `terva project` help eleven times over.
//
// A test that needs the text swaps helpOut for a buffer and restores it. It
// must not reach for os.Stderr to do that: the writes no longer go there.
func TestMain(m *testing.M) {
	helpOut = io.Discard
	os.Exit(m.Run())
}
