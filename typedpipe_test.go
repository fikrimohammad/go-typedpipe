package typedpipe

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func newPipe[T any](t *testing.T, opts ...Option) (Writer[T], Reader[T]) {
	t.Helper()
	w, r, err := New[T](opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return w, r
}

func bg() context.Context { return context.Background() }

// ── construction ──────────────────────────────────────────────────────────────

func TestNew(t *testing.T) {
	t.Run("rejects oversized buffer", func(t *testing.T) {
		_, _, err := New[int](WithBufferSize(MaxBufferSize + 1))
		if err == nil {
			t.Fatal("expected error for bufferSize > MaxBufferSize")
		}
	})

	t.Run("accepts MaxBufferSize exactly", func(t *testing.T) {
		_, _, err := New[int](WithBufferSize(MaxBufferSize))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

// ── basic read / write ────────────────────────────────────────────────────────

func TestReadWrite(t *testing.T) {
	w, r := newPipe[int](t)

	if err := w.Write(bg(), 42); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := r.Read(bg())
	if err != nil || got != 42 {
		t.Fatalf("Read = (%d, %v), want (42, nil)", got, err)
	}
}

func TestFIFOOrdering(t *testing.T) {
	const n = 64
	w, r := newPipe[int](t, WithBufferSize(n))

	for i := 0; i < n; i++ {
		w.Write(bg(), i)
	}
	for i := 0; i < n; i++ {
		got, err := r.Read(bg())
		if err != nil || got != i {
			t.Fatalf("Read[%d] = (%d, %v), want (%d, nil)", i, got, err, i)
		}
	}
}

// ── context cancellation ──────────────────────────────────────────────────────

func TestContextCancellation(t *testing.T) {
	t.Run("read respects canceled context", func(t *testing.T) {
		_, r := newPipe[int](t, WithBufferSize(0))
		ctx, cancel := context.WithCancel(bg())
		cancel()
		_, err := r.Read(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want context.Canceled", err)
		}
	})

	t.Run("write respects canceled context", func(t *testing.T) {
		w, _ := newPipe[int](t, WithBufferSize(0))
		ctx, cancel := context.WithCancel(bg())
		cancel()
		err := w.Write(ctx, 1)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want context.Canceled", err)
		}
	})

	t.Run("read unblocks when context canceled mid-wait", func(t *testing.T) {
		_, r := newPipe[int](t, WithBufferSize(0))
		ctx, cancel := context.WithCancel(bg())

		errc := make(chan error, 1)
		go func() { _, err := r.Read(ctx); errc <- err }()

		time.Sleep(10 * time.Millisecond)
		cancel()

		select {
		case err := <-errc:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("got %v, want context.Canceled", err)
			}
		case <-time.After(time.Second):
			t.Fatal("Read did not unblock after context cancel")
		}
	})
}

// ── close semantics ───────────────────────────────────────────────────────────

func TestClose(t *testing.T) {
	t.Run("writer close surfaces ErrPipeClosed to reader", func(t *testing.T) {
		w, r := newPipe[int](t)
		w.Close()
		_, err := r.Read(bg())
		if !errors.Is(err, ErrPipeClosed) {
			t.Fatalf("got %v, want ErrPipeClosed", err)
		}
	})

	t.Run("reader close surfaces ErrPipeClosed to writer", func(t *testing.T) {
		w, r := newPipe[int](t)
		r.Close()
		err := w.Write(bg(), 1)
		if !errors.Is(err, ErrPipeClosed) {
			t.Fatalf("got %v, want ErrPipeClosed", err)
		}
	})

	t.Run("custom error is propagated", func(t *testing.T) {
		sentinel := errors.New("sentinel")
		w, r := newPipe[int](t)
		w.CloseWithError(sentinel)
		_, err := r.Read(bg())
		if !errors.Is(err, sentinel) {
			t.Fatalf("got %v, want sentinel", err)
		}
	})

	t.Run("first error wins", func(t *testing.T) {
		first, second := errors.New("first"), errors.New("second")
		w, r := newPipe[int](t)
		w.CloseWithError(first)
		w.CloseWithError(second)
		_, err := r.Read(bg())
		if !errors.Is(err, first) {
			t.Fatalf("got %v, want first", err)
		}
	})

	t.Run("idempotent — multiple closes do not panic", func(t *testing.T) {
		w, r := newPipe[int](t)
		for i := 0; i < 5; i++ {
			w.Close()
			r.Close()
		}
	})

	t.Run("blocked read unblocks on close", func(t *testing.T) {
		w, r := newPipe[int](t, WithBufferSize(0))
		errc := make(chan error, 1)
		go func() { _, err := r.Read(bg()); errc <- err }()

		time.Sleep(10 * time.Millisecond)
		w.Close()

		select {
		case err := <-errc:
			if !errors.Is(err, ErrPipeClosed) {
				t.Errorf("got %v, want ErrPipeClosed", err)
			}
		case <-time.After(time.Second):
			t.Fatal("Read did not unblock after close")
		}
	})

	t.Run("blocked write unblocks on close", func(t *testing.T) {
		w, r := newPipe[int](t, WithBufferSize(0))
		errc := make(chan error, 1)
		go func() { errc <- w.Write(bg(), 1) }()

		time.Sleep(10 * time.Millisecond)
		r.Close()

		select {
		case err := <-errc:
			if !errors.Is(err, ErrPipeClosed) {
				t.Errorf("got %v, want ErrPipeClosed", err)
			}
		case <-time.After(time.Second):
			t.Fatal("Write did not unblock after close")
		}
	})
}

// ── drain guarantee ───────────────────────────────────────────────────────────

// TestDrain is the critical regression test for the original data-loss bug:
// items written before close must all be readable after close, in order.
func TestDrain(t *testing.T) {
	const n = 64
	w, r := newPipe[int](t, WithBufferSize(n))

	for i := 0; i < n; i++ {
		w.Write(bg(), i)
	}
	w.Close()

	for i := 0; i < n; i++ {
		got, err := r.Read(bg())
		if err != nil {
			t.Fatalf("Read[%d] returned error before buffer drained: %v", i, err)
		}
		if got != i {
			t.Errorf("Read[%d] = %d, want %d", i, got, i)
		}
	}

	_, err := r.Read(bg())
	if !errors.Is(err, ErrPipeClosed) {
		t.Fatalf("post-drain Read = %v, want ErrPipeClosed", err)
	}
}

// TestRace_ConcurrentWriters verifies many writers can send simultaneously
// without data races and that all messages are received.
func TestRace_ConcurrentWriters(t *testing.T) {
	const goroutines, each = 20, 50
	w, r := newPipe[int](t, WithBufferSize(goroutines*each))

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				w.Write(bg(), id*1000+i)
			}
		}(g)
	}

	var received int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, err := r.Read(bg()); err != nil {
				return
			}
			atomic.AddInt64(&received, 1)
		}
	}()

	wg.Wait()
	w.Close()
	<-done

	if got := int(atomic.LoadInt64(&received)); got != goroutines*each {
		t.Errorf("received %d messages, want %d", got, goroutines*each)
	}
}

// TestRace_ConcurrentReaders verifies many readers can drain simultaneously
// without races or double-reads.
func TestRace_ConcurrentReaders(t *testing.T) {
	const goroutines, total = 10, 500
	w, r := newPipe[int](t, WithBufferSize(total))

	for i := 0; i < total; i++ {
		w.Write(bg(), i)
	}
	w.Close()

	var received int64
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if _, err := r.Read(bg()); err != nil {
					return
				}
				atomic.AddInt64(&received, 1)
			}
		}()
	}
	wg.Wait()

	if got := int(atomic.LoadInt64(&received)); got != total {
		t.Errorf("received %d, want %d (items lost or duplicated)", got, total)
	}
}

// TestRace_CloseWhileReadingAndWriting fires close while both readers and
// writers are active — the most realistic shutdown scenario.
func TestRace_CloseWhileReadingAndWriting(t *testing.T) {
	const writers, readers = 10, 10
	w, r := newPipe[int](t, WithBufferSize(32))

	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if err := w.Write(bg(), 1); err != nil {
					return
				}
			}
		}()
	}
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if _, err := r.Read(bg()); err != nil {
					return
				}
			}
		}()
	}

	time.Sleep(20 * time.Millisecond)
	w.Close()
	wg.Wait()
}

// TestRace_ConcurrentClose ensures simultaneous Close calls from many
// goroutines don't race or double-close the internal done channel.
func TestRace_ConcurrentClose(t *testing.T) {
	w, r := newPipe[int](t)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				w.Close()
			} else {
				r.Close()
			}
		}(i)
	}
	wg.Wait()
}

// TestRace_ContextCancelAndClose races context cancellation against pipe close
// to ensure the error path has no data races.
func TestRace_ContextCancelAndClose(t *testing.T) {
	for i := 0; i < 200; i++ {
		w, r := newPipe[int](t, WithBufferSize(0))
		ctx, cancel := context.WithCancel(bg())
		go func() { cancel() }()
		go func() { w.Close() }()
		r.Read(ctx) //nolint:errcheck
	}
}
