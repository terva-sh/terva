package agent

import (
	"io"
	"os"
)

// helpOut is where the per-subcommand help screens go. It is os.Stderr in a
// real run, and nothing about the product changes.
//
// The seam exists for the TEST log. `go test` buffers a package's output and
// prints ALL of it the moment any test in that package fails, so help text
// written by tests that PASSED lands on top of the one line somebody needs to
// read. This package emitted 581 lines that way — `terva project` help alone
// eleven times — around a single assertion failure, and a real CI failure was
// duly buried in it while someone scrolled looking for the cause.
//
// TestMain points this at io.Discard. A test that wants the text swaps in a
// buffer and restores it, the way any other package-level seam here is used.
var helpOut io.Writer = os.Stderr
