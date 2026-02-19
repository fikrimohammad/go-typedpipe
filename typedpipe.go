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
package typedpipe

import (
	"context"
	"errors"
	"sync"
)

var (
	// ErrPipeClosed is returned when an operation is performed on a closed pipe.
	// It is used when Close() is called without a custom error.
	ErrPipeClosed = errors.New("pipe is closed")
)

const (
	// DefaultBufferSize is the default channel capacity.
	DefaultBufferSize = 64

	// MaxBufferSize caps the maximum allowed buffer size to prevent
	// excessive memory allocation.
	MaxBufferSize = 2048
)

// Writer represents the write side of the pipe.
//
// Write blocks until:
//   - The value is delivered,
//   - The context is canceled, or
//   - The pipe is closed.
//
// Writer is safe for concurrent use.
type Writer[T any] interface {
	Write(ctx context.Context, v T) error
	Closer
}

// Reader represents the read side of the pipe.
//
// Read blocks until:
//   - A value is available,
//   - The context is canceled, or
//   - The pipe is closed and fully drained.
//
// Reader is safe for concurrent use.
type Reader[T any] interface {
	Read(ctx context.Context) (T, error)
	Closer
}

// Closer defines shutdown semantics shared by Reader and Writer.
//
// CloseWithError preserves the first non-nil error. Subsequent calls are ignored.
type Closer interface {
	// Close closes the pipe using ErrPipeClosed.
	Close()

	// CloseWithError closes the pipe with a custom error.
	// The first non-nil error is retained and returned to all blocked
	// and future operations.
	CloseWithError(err error)
}

// New constructs a pipe and returns its Writer and Reader ends.
//
// Options may be used to configure buffering.
// If bufferSize <= 0, the pipe behaves as unbuffered.
func New[T any](opts ...Option) (Writer[T], Reader[T]) {
	opt := &options{
		bufferSize: DefaultBufferSize,
	}

	for _, optFn := range opts {
		optFn(opt)
	}

	if opt.bufferSize > MaxBufferSize {
		opt.bufferSize = MaxBufferSize
	}

	var valueChan chan T
	if opt.bufferSize < 1 {
		valueChan = make(chan T)
	} else {
		valueChan = make(chan T, opt.bufferSize)
	}

	p := pipe[T]{
		valueChan: valueChan,
		done:      make(chan struct{}),
	}

	return &writer[T]{&p}, &reader[T]{&p}
}

// pipe contains shared state between Reader and Writer.
//
// Shutdown ordering:
//  1. close(done) signals termination.
//  2. close(valueChan) unblocks readers.
//
// once guarantees idempotent shutdown.
type pipe[T any] struct {
	valueChan chan T
	done      chan struct{}
	once      sync.Once
	err       pipeError
}

// read receives a value from the pipe.
//
// It prioritizes:
//   - Context cancellation,
//   - Shutdown signal,
//   - Incoming data.
//
// When closed, it attempts a final non-blocking drain of valueChan
// before returning the stored close error.
func (p *pipe[T]) read(ctx context.Context) (T, error) {
	var zero T

	select {
	case <-ctx.Done():
		return zero, ctx.Err()
	case <-p.done:
		// Pipe is closed. Drain remaining buffered value if present.
		select {
		case v, ok := <-p.valueChan:
			if !ok {
				return zero, p.closeError()
			}
			return v, nil
		default:
			return zero, p.closeError()
		}
	case v, ok := <-p.valueChan:
		if !ok {
			return zero, p.closeError()
		}
		return v, nil
	}
}

// write sends a value to the pipe.
//
// It avoids panics from sending on a closed channel by selecting
// on the done channel before attempting sending the value to channel.
func (p *pipe[T]) write(ctx context.Context, v T) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		return p.closeError()
	case p.valueChan <- v:
		return nil
	}
}

// close performs idempotent shutdown.
//
// The first non-nil error is stored and becomes the terminal error
// observed by all blocked and future operations.
func (p *pipe[T]) close(err error) {
	p.once.Do(func() {
		if err == nil {
			err = ErrPipeClosed
		}

		p.err.Store(err)

		close(p.done)
		close(p.valueChan)
	})
}

// closeError returns the stored shutdown error.
func (p *pipe[T]) closeError() error {
	return p.err.Load()
}

// pipeError stores the first close error safely.
//
// A mutex is used instead of sync/atomic.Value to avoid allocations
// and preserve minimalism.
type pipeError struct {
	err error
	sync.Mutex
}

// Store records the first non-nil error.
func (pe *pipeError) Store(err error) {
	pe.Lock()
	defer pe.Unlock()

	if pe.err != nil {
		return
	}
	pe.err = err
}

// Load retrieves the stored error.
func (pe *pipeError) Load() error {
	pe.Lock()
	defer pe.Unlock()
	return pe.err
}

// writer implements Writer.
type writer[T any] struct {
	p *pipe[T]
}

// Write forwards to the underlying pipe.
func (w *writer[T]) Write(ctx context.Context, v T) error {
	return w.p.write(ctx, v)
}

// Close closes with the default error.
func (w *writer[T]) Close() {
	w.CloseWithError(nil)
}

// CloseWithError closes with a custom error.
func (w *writer[T]) CloseWithError(err error) {
	w.p.close(err)
}

// reader implements Reader.
type reader[T any] struct {
	p *pipe[T]
}

// Read forwards to the underlying pipe.
func (r *reader[T]) Read(ctx context.Context) (T, error) {
	return r.p.read(ctx)
}

// Close closes with the default error.
func (r *reader[T]) Close() {
	r.CloseWithError(nil)
}

// CloseWithError closes with a custom error.
func (r *reader[T]) CloseWithError(err error) {
	r.p.close(err)
}
