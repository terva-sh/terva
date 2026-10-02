package desktop

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

type fakeWindow struct {
	show   func(string, string) error
	closed chan struct{}
	once   sync.Once
}

func (w *fakeWindow) Show(origin, token string) error { return w.show(origin, token) }
func (w *fakeWindow) Wait() error                     { <-w.closed; return nil }
func (w *fakeWindow) Quit()                           { w.once.Do(func() { close(w.closed) }) }

func TestOwnedListenerClosesWithWindow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var address string
	w := &fakeWindow{closed: make(chan struct{})}
	w.show = func(origin, token string) error {
		address = origin
		if token != "process-token" {
			t.Fatal("lost bootstrap token")
		}
		conn, err := net.Dial("tcp", origin)
		if err != nil {
			return err
		}
		conn.Close()
		w.Quit()
		return nil
	}
	err := Run(ctx, w, "process-token", func(ctx context.Context, ready func(string)) error {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return err
		}
		defer ln.Close()
		ready(ln.Addr().String())
		<-ctx.Done()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if conn, err := net.DialTimeout("tcp", address, time.Second); err == nil {
		conn.Close()
		t.Fatal("listener survived window closure")
	}
}

func TestStartupAndWindowFailuresDrainServer(t *testing.T) {
	for _, phase := range []string{"server", "window", "cancel-before-ready", "cancel-after-show", "server-after-show"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			failure := errors.New("startup failed")
			cleaned := make(chan struct{})
			shown := make(chan struct{})
			w := &fakeWindow{closed: make(chan struct{})}
			w.show = func(string, string) error {
				close(shown)
				if phase == "window" {
					return failure
				}
				if phase == "cancel-after-show" {
					cancel()
				}
				return nil
			}
			err := Run(ctx, w, "", func(ctx context.Context, ready func(string)) error {
				defer close(cleaned)
				if phase == "server" {
					return failure
				}
				if phase == "cancel-before-ready" {
					cancel()
					<-ctx.Done()
					return nil
				}
				ready("http://127.0.0.1:1")
				if phase == "server-after-show" {
					<-shown
					return failure
				}
				<-ctx.Done()
				return nil
			})
			select {
			case <-cleaned:
			default:
				t.Fatal("server cleanup incomplete")
			}
			if phase == "server" || phase == "window" || phase == "server-after-show" {
				if !errors.Is(err, failure) {
					t.Fatalf("got %v, want failure", err)
				}
			}
		})
	}
}
