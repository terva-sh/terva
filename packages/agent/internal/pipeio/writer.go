// Package pipeio serializes subprocess frames with cancellable writes.
package pipeio

import (
	"context"
	"io"
	"sync"
)

// Writer owns a subprocess stdin pipe. The pipe must support Close concurrent
// with Write, as os.File and io.PipeWriter do. abort must start process cleanup
// without waiting for the writer. Cancellation during a write destroys the
// stream because the peer may have received only part of the frame.
type Writer struct {
	pipe  io.WriteCloser
	gate  chan struct{}
	done  chan struct{}
	once  sync.Once
	abort func()
}

func New(pipe io.WriteCloser, abort func()) *Writer {
	return &Writer{pipe: pipe, gate: make(chan struct{}, 1), done: make(chan struct{}), abort: abort}
}

// Close releases an active write and all callers waiting for the writer.
// It never acquires the write gate.
func (w *Writer) Close() {
	w.once.Do(func() {
		close(w.done)
		_ = w.pipe.Close()
	})
}

// Write sends one complete frame. A caller cancelled while waiting for the
// gate leaves the stream intact and sends nothing.
func (w *Writer) Write(ctx context.Context, frame []byte) error {
	select {
	case w.gate <- struct{}{}:
		defer func() { <-w.gate }()
	case <-ctx.Done():
		return ctx.Err()
	case <-w.done:
		return io.ErrClosedPipe
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-w.done:
		return io.ErrClosedPipe
	default:
	}
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		w.Close()
		close(interrupted)
	})
	n, err := w.pipe.Write(frame)
	if !stop() {
		<-interrupted
		err = ctx.Err()
	}
	if err == nil && n != len(frame) {
		err = io.ErrShortWrite
	}
	if err != nil {
		w.Close()
		if w.abort != nil {
			w.abort()
		}
	}
	return err
}
