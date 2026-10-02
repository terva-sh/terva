// Package desktop coordinates an owned server with a native window.
package desktop

import "context"

// Window runs its native event loop on the caller's thread.
type Window interface {
	Show(origin, token string) error
	Wait() error
	Quit()
}

// Run waits for listener readiness before opening a window and drains the server on exit.
func Run(ctx context.Context, window Window, token string, serve func(context.Context, func(string)) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, func(origin string) {
			select {
			case ready <- origin:
			case <-ctx.Done():
			}
		})
	}()
	var origin string
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		cancel()
		<-done
		return ctx.Err()
	case origin = <-ready:
	}
	if err := window.Show(origin, token); err != nil {
		cancel()
		<-done
		return err
	}
	serverResult := make(chan error, 1)
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		select {
		case err := <-done:
			serverResult <- err
			window.Quit()
		case <-ctx.Done():
			window.Quit()
		}
	}()
	windowErr := window.Wait()
	cancel()
	<-watchDone
	var serverErr error
	select {
	case serverErr = <-serverResult:
	default:
		serverErr = <-done
	}
	if windowErr != nil {
		return windowErr
	}
	return serverErr
}
