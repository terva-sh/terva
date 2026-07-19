package mcp

import "os/exec"

// stdioCmd exposes the subprocess behind a stdio transport for white-box tests
// that assert on the child's env and cwd, or kill it to simulate a crash — the
// behaviour those checks verify moved from Client into stdioTransport in the
// transport-seam refactor. Test-only (this file is _test.go), so it never ships
// in the binary. Returns nil for a non-stdio client.
func (c *Client) stdioCmd() *exec.Cmd {
	if st, ok := c.transport.(*stdioTransport); ok {
		return st.cmd
	}
	return nil
}
