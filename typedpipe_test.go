package typedpipe

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Verify interface implementations
var (
	_ io.Closer = Writer[int](nil)
	_ io.Closer = Reader[int](nil)
)

// ── helpers ───────────────────────────────────────────────────────────────────

func newPipe[T any](t *testing.T, opts ...Option) (Writer[T], Reader[T]) {
	t.Helper()
	return New[T](opts...)
}

func bg() context.Context { return context.Background() }

// ── construction ──────────────────────────────────────────────────────────────

func TestNew(t *testing.T) {
	t.Run("default buffer size", func(t *testing.T) {
		w, r := New[int]()
		defer w.Close()
		if w.Cap() != DefaultBufferSize || r.Cap() != DefaultBufferSize {
			t.Fatalf("Cap = %d, want %d", w.Cap(), DefaultBufferSize)
		}
	})

	t.Run("large buffer size is allowed", func(t *testing.T) {
		w, r := New[int](WithBufferSize(1 << 20)) // 1M — no cap enforced
		defer w.Close()
		if w.Cap() != 1<<20 || r.Cap() != 1<<20 {
			t.Fatalf("Cap = %d, want %d", w.Cap(), 1<<20)
		}
	})

	t.Run("unbuffered pipe via zero size", func(t *testing.T) {
		w, r := New[int](WithBufferSize(0))
		defer w.Close()
		if w.Cap() != 0 || r.Cap() != 0 {
			t.Fatalf("Cap = %d, want 0", w.Cap())
		}
	})

	t.Run("unbuffered pipe via negative size", func(t *testing.T) {
		w, r := New[int](WithBufferSize(-5))
		defer w.Close()
		if w.Cap() != 0 || r.Cap() != 0 {
			t.Fatalf("Cap = %d, want 0", w.Cap())
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
		_ = w.Write(bg(), i)
	}
	for i := 0; i < n; i++ {
		got, err := r.Read(bg())
		if err != nil || got != i {
			t.Fatalf("Read[%d] = (%d, %v), want (%d, nil)", i, got, err, i)
		}
	}
}

// ── introspection ─────────────────────────────────────────────────────────────

func TestIntrospection(t *testing.T) {
	w, r := newPipe[int](t, WithBufferSize(4))
	if w.Len() != 0 || r.Len() != 0 {
		t.Fatalf("Len = %d, want 0", w.Len())
	}
	if w.Cap() != 4 || r.Cap() != 4 {
		t.Fatalf("Cap = %d, want 4", w.Cap())
	}
	if w.IsClosed() || r.IsClosed() {
		t.Fatal("IsClosed = true before close, want false")
	}

	_ = w.Write(bg(), 10)
	_ = w.Write(bg(), 20)
	if w.Len() != 2 || r.Len() != 2 {
		t.Fatalf("Len = %d, want 2", w.Len())
	}

	_ = w.Close()
	if !w.IsClosed() || !r.IsClosed() {
		t.Fatal("IsClosed = false after close, want true")
	}

	// Drain
	val, err := r.Read(bg())
	if err != nil || val != 10 {
		t.Fatalf("Read = (%d, %v), want (10, nil)", val, err)
	}
	if r.Len() != 1 {
		t.Fatalf("Len = %d, want 1", r.Len())
	}

	val, err = r.Read(bg())
	if err != nil || val != 20 {
		t.Fatalf("Read = (%d, %v), want (20, nil)", val, err)
	}
	if r.Len() != 0 {
		t.Fatalf("Len = %d, want 0", r.Len())
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
		_ = w.Close()
		_, err := r.Read(bg())
		if !errors.Is(err, ErrPipeClosed) {
			t.Fatalf("got %v, want ErrPipeClosed", err)
		}
	})

	t.Run("reader close surfaces ErrPipeClosed to writer", func(t *testing.T) {
		w, r := newPipe[int](t)
		_ = r.Close()
		err := w.Write(bg(), 1)
		if !errors.Is(err, ErrPipeClosed) {
			t.Fatalf("got %v, want ErrPipeClosed", err)
		}
	})

	t.Run("custom error is propagated", func(t *testing.T) {
		sentinel := errors.New("sentinel")
		w, r := newPipe[int](t)
		_ = w.CloseWithError(sentinel)
		_, err := r.Read(bg())
		if !errors.Is(err, sentinel) {
			t.Fatalf("got %v, want sentinel", err)
		}
	})

	t.Run("first error wins", func(t *testing.T) {
		first, second := errors.New("first"), errors.New("second")
		w, r := newPipe[int](t)
		_ = w.CloseWithError(first)
		_ = w.CloseWithError(second)
		_, err := r.Read(bg())
		if !errors.Is(err, first) {
			t.Fatalf("got %v, want first", err)
		}
	})

	t.Run("idempotent — multiple closes do not panic", func(t *testing.T) {
		w, r := newPipe[int](t)
		for i := 0; i < 5; i++ {
			_ = w.Close()
			_ = r.Close()
		}
	})

	t.Run("blocked read unblocks on close", func(t *testing.T) {
		w, r := newPipe[int](t, WithBufferSize(0))
		errc := make(chan error, 1)
		go func() { _, err := r.Read(bg()); errc <- err }()

		time.Sleep(10 * time.Millisecond)
		_ = w.Close()

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
		_ = r.Close()

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
		_ = w.Write(bg(), i)
	}
	_ = w.Close()

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
				_ = w.Write(bg(), id*1000+i)
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
	_ = w.Close()
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
		_ = w.Write(bg(), i)
	}
	_ = w.Close()

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
	_ = w.Close()
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
				_ = w.Close()
			} else {
				_ = r.Close()
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
		go func() { _ = w.Close() }()
		_, _ = r.Read(ctx)
	}
}

// ── ReadAll ───────────────────────────────────────────────────────────────────

func TestReadAll(t *testing.T) {
	t.Run("collects all values and returns nil on normal close", func(t *testing.T) {
		const n = 64
		w, r := newPipe[int](t, WithBufferSize(n))
		for i := 0; i < n; i++ {
			_ = w.Write(bg(), i)
		}
		_ = w.Close()

		var got []int
		err := r.ReadAll(bg(), func(v int) error {
			got = append(got, v)
			return nil
		})
		if err != nil {
			t.Fatalf("got err %v, want nil", err)
		}
		if len(got) != n {
			t.Fatalf("got %d items, want %d", len(got), n)
		}
		for i, v := range got {
			if v != i {
				t.Fatalf("got[%d] = %d, want %d", i, v, i)
			}
		}
	})

	t.Run("returns context error when context canceled", func(t *testing.T) {
		_, r := newPipe[int](t, WithBufferSize(0))
		ctx, cancel := context.WithCancel(bg())
		cancel()
		err := r.ReadAll(ctx, func(int) error { return nil })
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want context.Canceled", err)
		}
	})

	t.Run("propagates context cancellation to waiting writer", func(t *testing.T) {
		w, r := newPipe[int](t, WithBufferSize(0))
		ctx, cancel := context.WithCancel(bg())

		writeErr := make(chan error, 1)
		go func() {
			// First write is consumed by ReadAll
			if err := w.Write(bg(), 1); err != nil {
				writeErr <- err
				return
			}
			// Second write will block until ReadAll exits and closes pipe with context.Canceled
			writeErr <- w.Write(bg(), 2)
		}()

		err := r.ReadAll(ctx, func(v int) error {
			if v == 1 {
				cancel()
			}
			return nil
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ReadAll got %v, want context.Canceled", err)
		}

		select {
		case wErr := <-writeErr:
			if !errors.Is(wErr, context.Canceled) {
				t.Fatalf("writer got %v, want context.Canceled", wErr)
			}
		case <-time.After(time.Second):
			t.Fatal("writer did not unblock")
		}
	})

	t.Run("returns custom close error", func(t *testing.T) {
		sentinel := errors.New("sentinel")
		w, r := newPipe[int](t)
		_ = w.CloseWithError(sentinel)
		err := r.ReadAll(bg(), func(int) error { return nil })
		if !errors.Is(err, sentinel) {
			t.Fatalf("got %v, want sentinel", err)
		}
	})

	t.Run("stops and returns fn error", func(t *testing.T) {
		w, r := newPipe[int](t)
		go func() {
			for i := 0; ; i++ {
				if err := w.Write(bg(), i); err != nil {
					return
				}
			}
		}()

		fnErr := errors.New("fn error")
		err := r.ReadAll(bg(), func(int) error { return fnErr })
		if !errors.Is(err, fnErr) {
			t.Fatalf("got %v, want fnErr", err)
		}
	})
}
