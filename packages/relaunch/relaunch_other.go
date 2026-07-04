//go:build !unix

package relaunch

// supported: platforms without exec(2) (e.g. Windows) cannot replace the
// process image in place; Trigger fails preflight with ErrUnsupported.
const supported = false

func realExec() error { return ErrUnsupported }
