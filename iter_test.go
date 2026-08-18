//go:build go1.23

package typedpipe

import (
	"context"
	"errors"
	"testing"
)

func TestValues(t *testing.T) {
	t.Run("iterates all values on normal close", func(t *testing.T) {
		w, r := newPipe[int](t, WithBufferSize(10))
		for i := 0; i < 5; i++ {
			_ = w.Write(bg(), i)
		}
		_ = w.Close()

		var got []int
		for v, err := range Values(bg(), r) {
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			got = append(got, v)
		}

		if len(got) != 5 {
			t.Fatalf("got %d values, want 5", len(got))
		}
		for i, v := range got {
			if v != i {
				t.Errorf("got[%d] = %d, want %d", i, v, i)
			}
		}
	})

	t.Run("early break closes pipe and unblocks writer", func(t *testing.T) {
		w, r := newPipe[int](t, WithBufferSize(0))
		writeErr := make(chan error, 1)

		go func() {
			// First write succeeds
			if err := w.Write(bg(), 1); err != nil {
				writeErr <- err
				return
			}
			// Second write should block until reader closes upon break
			writeErr <- w.Write(bg(), 2)
		}()

		var count int
		for v, err := range Values(bg(), r) {
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if v == 1 {
				count++
				break // Stop after first item
			}
		}

		if count != 1 {
			t.Fatalf("got count %d, want 1", count)
		}

		err := <-writeErr
		if !errors.Is(err, ErrPipeClosed) {
			t.Fatalf("writer got %v, want ErrPipeClosed", err)
		}
	})

	t.Run("yields custom close error", func(t *testing.T) {
		sentinel := errors.New("custom error")
		w, r := newPipe[int](t)
		_ = w.CloseWithError(sentinel)

		var errs []error
		for _, err := range Values(bg(), r) {
			if err != nil {
				errs = append(errs, err)
			}
		}

		if len(errs) != 1 || !errors.Is(errs[0], sentinel) {
			t.Fatalf("got errs %v, want [%v]", errs, sentinel)
		}
	})

	t.Run("yields context cancellation error", func(t *testing.T) {
		_, r := newPipe[int](t, WithBufferSize(0))
		ctx, cancel := context.WithCancel(bg())
		cancel()

		var errs []error
		for _, err := range Values(ctx, r) {
			if err != nil {
				errs = append(errs, err)
			}
		}

		if len(errs) != 1 || !errors.Is(errs[0], context.Canceled) {
			t.Fatalf("got errs %v, want context.Canceled", errs)
		}
	})
}
