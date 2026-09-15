package testsupport

import (
	"net"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// SocketDir exists for exactly one reason, so the test asserts that reason
// directly rather than a proxy for it: a socket with a realistic name can be
// BOUND inside what it returns.
//
// Checking the length instead would encode today's budget as a number and go
// stale the moment a caller's filenames grow. Binding is the property; the
// kernel is the judge of it.
func TestASocketCanBeBoundInsideSocketDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix socket path limits are not the constraint on Windows")
	}
	dir := SocketDir(t)

	// As long as the longest name a caller is known to use: the tenant
	// supervisor's "t-" + 32 hex + ".sock".
	name := "t-" + strings.Repeat("a", 16) + ".sock"
	path := filepath.Join(dir, name)

	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("could not bind a %d-byte socket path under SocketDir: %v", len(path), err)
	}
	_ = ln.Close()
}
