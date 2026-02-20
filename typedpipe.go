// Package typedpipe implements a generic, in-memory, concurrency-safe pipe.
//
// It provides a typed alternative to io.Pipe: values of type T can be passed
// between goroutines with backpressure and coordinated shutdown semantics.
//
// The pipe guarantees:
//   - Safe concurrent use.
//   - Blocking reads and writes.
//   - Idempotent close.
//   - Propagation of the first close error to all subsequent operations.
//   - Full drain of buffered values before returning a close error to readers.
//
// Shutdown semantics:
//
// Either side (Reader or Writer) may call Close/CloseWithError. When closed:
//  1. All in-progress and future Write calls return the close error immediately.
//  2. Read calls drain any buffered values, then return the close error.
//
// Note: Because both sides share a Closer, a reader may close the writer's
// side and vice versa. This matches io.Pipe semantics and is intentional.
package typedpipe

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

// ErrPipeClosed is returned by operations on a closed pipe when no custom
// error was provided to CloseWithError.
var ErrPipeClosed = errors.New("pipe is closed")

const (
	// DefaultBufferSize is the default channel capacity.
	DefaultBufferSize = 64

	// MaxBufferSize is the maximum allowed buffer capacity.
	// New returns an error if the requested size exceeds this value.
	MaxBufferSize = 2048
)

// Writer is the write side of the pipe.
type Writer[T any] interface {
	Write(ctx context.Context, v T) error
	Closer
}

// Reader is the read side of the pipe.
type Reader[T any] interface {
	Read(ctx context.Context) (T, error)
	Closer
}

// Closer is the shared shutdown interface.
// The first non-nil error passed to CloseWithError is retained;
// subsequent calls are no-ops.
type Closer interface {
	Close()
	CloseWithError(err error)
}

// New constructs a pipe and returns its Writer and Reader ends.
func New[T any](opts ...Option) (Writer[T], Reader[T], error) {
	opt := &options{bufferSize: DefaultBufferSize}
	for _, o := range opts {
		o(opt)
	}
	if opt.bufferSize > MaxBufferSize {
		return nil, nil, errors.New("typedpipe: bufferSize exceeds MaxBufferSize")
	}
	size := opt.bufferSize
	if size < 0 {
		size = 0
	}
	p := &pipe[T]{
		ch:   make(chan T, size),
		done: make(chan struct{}),
	}
	return &writer[T]{p}, &reader[T]{p}, nil
}

// pipe holds all shared state.
//
// Two channels carry distinct signals:
//   - ch carries values from writers to readers.
//   - done is closed once on shutdown, signalling all goroutines to stop.
//
// ch is never closed, which means writers never risk a send-on-closed panic.
// Readers drain ch via a non-blocking select each time done fires, returning
// one buffered item per Read call until the buffer is empty.
//
// write() uses a non-blocking pre-check on done before the blocking select.
// This gives shutdown priority over a buffered send: without it, Go's select
// picks randomly between <-done and ch<-v when both are ready, allowing a
// write to silently succeed after Close has been called.
type pipe[T any] struct {
	ch   chan T
	done chan struct{}
	once sync.Once
	err  pipeError
}

func (p *pipe[T]) write(ctx context.Context, v T) error {
	// Non-blocking priority check: if the pipe or context is already done,
	// return immediately before touching ch.
	select {
	case <-p.done:
		return p.err.Load()
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	select {
	case <-p.done:
		return p.err.Load()
	case <-ctx.Done():
		return ctx.Err()
	case p.ch <- v:
		return nil
	}
}

func (p *pipe[T]) read(ctx context.Context) (T, error) {
	var zero T
	select {
	case <-ctx.Done():
		return zero, ctx.Err()
	case v := <-p.ch:
		return v, nil
	case <-p.done:
		// Drain one buffered value if present; caller loops for the rest.
		select {
		case v := <-p.ch:
			return v, nil
		default:
			return zero, p.err.Load()
		}
	}
}

func (p *pipe[T]) close(err error) {
	p.once.Do(func() {
		if err == nil {
			err = ErrPipeClosed
		}
		p.err.Store(err)
		close(p.done)
	})
}

// pipeError stores the first close error atomically.
// Uses atomic.Value (Go 1.4+) rather than atomic.Pointer[T] (Go 1.19+)
// for Go 1.18 compatibility.
type pipeError struct {
	v atomic.Value // always holds *errorHolder
}

type errorHolder struct{ err error }

func (pe *pipeError) Store(err error) {
	pe.v.CompareAndSwap(nil, &errorHolder{err})
}

func (pe *pipeError) Load() error {
	if h, ok := pe.v.Load().(*errorHolder); ok {
		return h.err
	}
	return nil
}

type writer[T any] struct{ p *pipe[T] }

func (w *writer[T]) Write(ctx context.Context, v T) error { return w.p.write(ctx, v) }
func (w *writer[T]) Close()                               { w.p.close(nil) }
func (w *writer[T]) CloseWithError(err error)             { w.p.close(err) }

type reader[T any] struct{ p *pipe[T] }

func (r *reader[T]) Read(ctx context.Context) (T, error) { return r.p.read(ctx) }
func (r *reader[T]) Close()                              { r.p.close(nil) }
func (r *reader[T]) CloseWithError(err error)            { r.p.close(err) }
