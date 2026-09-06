package pipeio

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"
)

func result(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("pipe caller did not return")
		return nil
	}
}

func TestCancellationDuringWriteClosesStream(t *testing.T) {
	r, p := io.Pipe()
	defer r.Close()
	aborted := make(chan struct{})
	w := New(p, func() { close(aborted) })
	defer w.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Write(ctx, []byte("partial frame\n")) }()
	// Reading one byte proves the write started and cannot yet complete.
	if _, err := io.ReadFull(r, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	waiter := make(chan error, 1)
	go func() { waiter <- w.Write(context.Background(), []byte("later\n")) }()
	cancel()
	if err := result(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("active write = %v", err)
	}
	if err := result(t, waiter); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("waiting write = %v", err)
	}
	select {
	case <-aborted:
	default:
		t.Fatal("partial frame did not start process cleanup")
	}
}

func TestWaitingCancellationPreservesStream(t *testing.T) {
	r, p := io.Pipe()
	defer r.Close()
	w := New(p, func() { t.Error("waiting cancellation aborted the stream") })
	defer w.Close()
	done := make(chan error, 1)
	go func() { done <- w.Write(context.Background(), []byte("first\n")) }()
	if _, err := io.ReadFull(r, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	waiter := make(chan error, 1)
	go func() { waiter <- w.Write(ctx, []byte("cancelled\n")) }()
	cancel()
	if err := result(t, waiter); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting write = %v", err)
	}
	buf := make([]byte, len("irst\n"))
	if _, err := io.ReadFull(r, buf); err != nil || string(buf) != "irst\n" {
		t.Fatalf("remaining frame = %q, %v", buf, err)
	}
	if err := result(t, done); err != nil {
		t.Fatal(err)
	}
	go func() { done <- w.Write(context.Background(), []byte("next\n")) }()
	if _, err := io.ReadFull(r, buf); err != nil || string(buf) != "next\n" {
		t.Fatalf("next frame = %q, %v", buf, err)
	}
	if err := result(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentFramesAndClose(t *testing.T) {
	r, p := io.Pipe()
	defer r.Close()
	w := New(p, nil)
	defer w.Close()
	const count = 40
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := w.Write(context.Background(), []byte(fmt.Sprintf("frame-%d\n", i))); err != nil {
				t.Error(err)
			}
		}(i)
	}
	go func() { wg.Wait(); w.Close() }()
	sc := bufio.NewScanner(r)
	seen := map[string]bool{}
	for sc.Scan() {
		seen[sc.Text()] = true
	}
	for i := 0; i < count; i++ {
		if !seen[fmt.Sprintf("frame-%d", i)] {
			t.Fatalf("missing or interleaved frame %d", i)
		}
	}
	if err := w.Write(context.Background(), []byte("closed\n")); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("write after Close = %v", err)
	}
}
