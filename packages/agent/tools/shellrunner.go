package tools

import (
	"context"
	"io"
	"os"
	"os/exec"
)

// ShellRunner runs one script for the bash tool. The host runner starts the
// resolved shell as a process. An in-process runner interprets the script
// inside terva, which is the only option where no process can start (iOS).
//
// The bash tool keeps everything around the run for every runner: the command
// checks, the timeout, the capture and spill of output, and the footer.
type ShellRunner interface {
	// ShellName names the interpreter in the tool description.
	ShellName() string
	// Notes is an extra paragraph for the tool description, or "".
	Notes() string
	// Run runs req.Script and returns its exit status. It writes stdout and
	// stderr, merged, to req.Output, and returns once nothing more will be
	// written. It stops the script when ctx ends. An error means the script
	// could not start.
	Run(ctx context.Context, req ShellRequest) (int, error)
}

// ShellRequest is one script for a ShellRunner.
type ShellRequest struct {
	Script string
	Dir    string
	// Env holds the terva variables. They win over inherited duplicates.
	Env map[string]string
	// Output receives stdout and stderr, merged. It is safe for concurrent use.
	Output io.Writer
}

// hostShell runs a script under the resolved host shell.
type hostShell struct{}

func (hostShell) ShellName() string { return shellName() }
func (hostShell) Notes() string     { return "" }

func (hostShell) Run(ctx context.Context, req ShellRequest) (int, error) {
	cmd := newShellCmd(ctx, req.Script)
	cmd.Dir = req.Dir
	// Inherit the process environment, then append terva facts so they win
	// over any inherited duplicate (Go uses the last value for a repeated key).
	cmd.Env = os.Environ()
	for _, k := range sortedKeys(req.Env) {
		cmd.Env = append(cmd.Env, k+"="+req.Env[k])
	}
	setProcessGroup(cmd)

	// Capture merged stdout+stderr with line-by-line streaming.
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		return 0, err
	}
	done := make(chan struct{})

	// Watch for context cancellation and kill the entire process
	// group immediately. exec.CommandContext only kills the direct
	// process, but child processes (e.g. grep spawned by the shell)
	// keep the output pipe open and block cmd.Wait() indefinitely.
	go func() {
		select {
		case <-ctx.Done():
			killProcessGroup(cmd)
			// Close the write end so the reader goroutine unblocks.
			pw.Close()
		case <-done:
		}
	}()
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, err := pr.Read(buf)
			if n > 0 {
				_, _ = req.Output.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()

	waitErr := cmd.Wait()
	pw.Close()
	<-done

	if waitErr != nil {
		if ee, ok := waitErr.(*exec.ExitError); ok {
			return ee.ExitCode(), nil
		}
		return -1, nil
	}
	return 0, nil
}
