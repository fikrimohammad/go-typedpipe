//go:build go1.23

package typedpipe

import (
	"context"
	"errors"
	"iter"
)

// Values returns an iterator over items read from the reader.
// It yields (value, nil) for each item received from the pipe.
//
// If the pipe is closed normally (ErrPipeClosed), the iteration terminates cleanly.
// If a custom close error, context cancellation, or read error occurs,
// it yields (zero, err) and terminates.
//
// The reader is automatically closed when the iteration completes or is
// terminated early (e.g. via a break statement in the range loop).
func Values[T any](ctx context.Context, r Reader[T]) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var retErr error
		defer func() {
			if retErr != nil && !errors.Is(retErr, ErrPipeClosed) {
				_ = r.CloseWithError(retErr)
			} else {
				_ = r.Close()
			}
		}()

		for {
			v, err := r.Read(ctx)
			if err != nil {
				if errors.Is(err, ErrPipeClosed) {
					return
				}
				retErr = err
				yield(v, err)
				return
			}
			if !yield(v, nil) {
				return
			}
		}
	}
}
