// Package datastream provides a generic, in-memory, thread-safe pipe implementation.
// It allows one goroutine to write data of any type and another goroutine to read it,
// similar to an io.Pipe but for arbitrary data structures instead of just bytes.
//
// The pipe is safe for concurrent use. Either the reader or the writer can close
// the pipe at any time to signal termination to the other party.
package datastream

import (
	"context"
	"errors"
	"sync"
)

var (
	// ErrDataStreamClosed is the default error returned for operations on a closed data stream.
	ErrDataStreamClosed = errors.New("datastream is closed")
)

const (
	// DefaultBufferSize is the default capacity of the pipe's buffer if not otherwise specified.
	DefaultBufferSize = 1024
	// MaxBufferSize is the maximum allowed capacity of the pipe's buffer.
	MaxBufferSize = 2048
)

// Writer is the write-side of the data stream.
// Writes are blocking until the data is sent, the context is canceled, or the stream is closed.
type Writer[T any] interface {
	// Write sends a value to the stream. It blocks until the value is sent,
	// the context is canceled, or the stream is closed.
	Write(ctx context.Context, v *T) error
	Closer
}

// Reader is the read-side of the data stream.
// Reads are blocking until data is available, the context is canceled, or the stream is closed.
type Reader[T any] interface {
	// Read receives a value from the stream. It blocks until a value is available,
	// the context is canceled, or the stream is closed and the buffer is empty.
	Read(ctx context.Context) (*T, error)
	Closer
}

// Closer defines methods for closing the data stream.
// It is implemented by both Reader and Writer.
type Closer interface {
	// Close closes the data stream with a default error (ErrDataStreamClosed).
	Close()
	// CloseWithError closes the data stream with a specific error. The first
	// non-nil error used to close the stream is preserved and returned by
	// subsequent operations.
	CloseWithError(err error)
}

// New creates a new generic data stream and returns its Reader and Writer ends.
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

	var valueChan chan *T
	if opt.bufferSize < 1 {
		valueChan = make(chan *T) // Unbuffered
	} else {
		valueChan = make(chan *T, opt.bufferSize) // Buffered
	}

	p := dataStream[T]{
		valueChan: valueChan,
		once:      sync.Once{},
		done:      make(chan struct{}),
	}

	return &writer[T]{&p}, &reader[T]{&p}
}

// dataStream is the internal, shared state between the reader and writer.
type dataStream[T any] struct {
	valueChan chan *T         // The channel for passing data.
	done      chan struct{}   // A channel that is closed to signal termination to all parties.
	once      sync.Once       // Ensures the closing logic runs exactly once.
	err       dataStreamError // A thread-safe container for the closing error.
}

// read receives a value from the stream. It blocks until a value is available,
// the context is canceled, or the stream is closed. It is designed to be
// immediately responsive to a close signal via the 'done' channel.
func (ds *dataStream[T]) read(ctx context.Context) (*T, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-ds.done:
		// The stream is closed. Attempt one last non-blocking read to drain
		// any remaining items from the buffer.
		select {
		case v, ok := <-ds.valueChan:
			if !ok {
				return nil, ds.closeError()
			}
			return v, nil
		default:
			// Buffer is empty, and stream is closed.
			return nil, ds.closeError()
		}
	case v, ok := <-ds.valueChan:
		if !ok {
			// This case is hit if the channel is closed while the reader was waiting.
			return nil, ds.closeError()
		}
		return v, nil
	}
}

// write sends a value to the stream. It blocks until the value is sent,
// the context is canceled, or the stream is closed. It safely handles
// closure by listening on the 'done' channel to prevent panicking on a send
// to a closed channel.
func (ds *dataStream[T]) write(ctx context.Context, v *T) error {
	if v == nil {
		return errors.New("datastream: cannot write a nil value")
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-ds.done:
		return ds.closeError()
	case ds.valueChan <- v:
		return nil
	}
}

// close is the central, idempotent function to close the stream. It uses
// sync.Once to ensure that the shutdown sequence runs exactly once, regardless
// of how many times Close or CloseWithError is called.
func (ds *dataStream[T]) close(err error) {
	ds.once.Do(func() {
		if err == nil {
			err = ErrDataStreamClosed
		}

		ds.err.Store(err)
		// 1. Signal all parties to stop their operations.
		close(ds.done)
		// 2. Close the data channel to unblock any waiting readers.
		close(ds.valueChan)
	})
}

// closeError safely retrieves the error that was used to close the stream.
func (ds *dataStream[T]) closeError() error {
	return ds.err.Load()
}

// dataStreamError is a thread-safe container for storing the first error
// that closes the stream.
type dataStreamError struct {
	err error
	sync.Mutex
}

// Store atomically stores the first non-nil error. Subsequent calls are ignored.
func (dse *dataStreamError) Store(err error) {
	dse.Lock()
	defer dse.Unlock()

	if dse.err != nil {
		return
	}
	dse.err = err
}

// Load atomically retrieves the stored error.
func (dse *dataStreamError) Load() error {
	dse.Lock()
	defer dse.Unlock()
	return dse.err
}

// writer is the public-facing implementation of the Writer interface.
type writer[T any] struct {
	ds *dataStream[T]
}

// Write delegates the write operation to the underlying dataStream.
func (w *writer[T]) Write(ctx context.Context, v *T) error {
	return w.ds.write(ctx, v)
}

// Close closes the stream with a default error.
func (w *writer[T]) Close() {
	w.CloseWithError(nil)
}

// CloseWithError closes the stream with a specific error.
func (w *writer[T]) CloseWithError(err error) {
	w.ds.close(err)
}

// reader is the public-facing implementation of the Reader interface.
type reader[T any] struct {
	ds *dataStream[T]
}

// Read delegates the read operation to the underlying dataStream.
func (r *reader[T]) Read(ctx context.Context) (*T, error) {
	return r.ds.read(ctx)
}

// Close closes the stream with a default error.
func (r *reader[T]) Close() {
	r.CloseWithError(nil)
}

// CloseWithError closes the stream with a specific error.
func (r *reader[T]) CloseWithError(err error) {
	r.ds.close(err)
}
