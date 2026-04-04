package typedpipe

import (
	"context"
	"testing"
)

// bufferSizes covers unbuffered, default, and large buffer scenarios.
var bufferSizes = []int{0, 64, 256, 1024}

// ── Write ─────────────────────────────────────────────────────────────────────

func BenchmarkPipe_Write(b *testing.B) {
	for _, size := range bufferSizes {
		b.Run(bufSize(size), func(b *testing.B) {
			// Use a large buffer so Write never blocks on the reader.
			w, r := New[int](WithBufferSize(b.N + 1))
			defer w.Close()
			ctx := context.Background()

			// Drain in background so the pipe never fills up.
			go func() {
				for {
					if _, err := r.Read(ctx); err != nil {
						return
					}
				}
			}()

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				w.Write(ctx, i) //nolint:errcheck
			}
		})
	}
}

// ── Read ──────────────────────────────────────────────────────────────────────

func BenchmarkPipe_Read(b *testing.B) {
	for _, size := range bufferSizes {
		b.Run(bufSize(size), func(b *testing.B) {
			w, r := New[int](WithBufferSize(b.N + 1))
			ctx := context.Background()

			// Pre-fill so Read never blocks on the writer.
			go func() {
				for i := 0; i < b.N; i++ {
					w.Write(ctx, i) //nolint:errcheck
				}
				w.Close()
			}()

			b.ResetTimer()
			b.ReportAllocs()

			for {
				if _, err := r.Read(ctx); err != nil {
					break
				}
			}
		})
	}
}

// ── WriteRead (paired) ────────────────────────────────────────────────────────

// BenchmarkPipe_WriteRead measures end-to-end throughput with a single
// producer and single consumer running concurrently.
func BenchmarkPipe_WriteRead(b *testing.B) {
	for _, size := range bufferSizes {
		b.Run(bufSize(size), func(b *testing.B) {
			w, r := New[int](WithBufferSize(size))
			ctx := context.Background()

			b.ResetTimer()
			b.ReportAllocs()

			go func() {
				for i := 0; i < b.N; i++ {
					if err := w.Write(ctx, i); err != nil {
						return
					}
				}
				w.Close()
			}()

			for {
				if _, err := r.Read(ctx); err != nil {
					break
				}
			}
		})
	}
}

// ── ReadAll ───────────────────────────────────────────────────────────────────

func BenchmarkPipe_ReadAll(b *testing.B) {
	for _, size := range bufferSizes {
		b.Run(bufSize(size), func(b *testing.B) {
			w, r := New[int](WithBufferSize(size))
			ctx := context.Background()

			b.ResetTimer()
			b.ReportAllocs()

			go func() {
				for i := 0; i < b.N; i++ {
					if err := w.Write(ctx, i); err != nil {
						return
					}
				}
				w.Close()
			}()

			r.ReadAll(ctx, func(int) error { return nil }) //nolint:errcheck
		})
	}
}

// ── concurrent writers ────────────────────────────────────────────────────────

func BenchmarkPipe_ConcurrentWriters(b *testing.B) {
	for _, goroutines := range []int{2, 8, 32} {
		b.Run(gCount(goroutines), func(b *testing.B) {
			w, r := New[int](WithBufferSize(256))
			ctx := context.Background()

			// Drain in background.
			go func() {
				for {
					if _, err := r.Read(ctx); err != nil {
						return
					}
				}
			}()

			b.ResetTimer()
			b.ReportAllocs()
			b.SetParallelism(goroutines)

			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					w.Write(ctx, 1) //nolint:errcheck
				}
			})

			w.Close()
		})
	}
}

// ── concurrent readers ────────────────────────────────────────────────────────

func BenchmarkPipe_ConcurrentReaders(b *testing.B) {
	for _, goroutines := range []int{2, 8, 32} {
		b.Run(gCount(goroutines), func(b *testing.B) {
			w, r := New[int](WithBufferSize(256))
			ctx := context.Background()

			// Feed in background.
			go func() {
				for {
					if err := w.Write(ctx, 1); err != nil {
						return
					}
				}
			}()

			b.ResetTimer()
			b.ReportAllocs()
			b.SetParallelism(goroutines)

			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					r.Read(ctx) //nolint:errcheck
				}
			})

			w.Close()
		})
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func bufSize(n int) string {
	if n == 0 {
		return "unbuffered"
	}
	switch n {
	case 64:
		return "buffer_64"
	case 256:
		return "buffer_256"
	case 1024:
		return "buffer_1024"
	default:
		return "buffer_custom"
	}
}

func gCount(n int) string {
	switch n {
	case 2:
		return "goroutines_2"
	case 8:
		return "goroutines_8"
	case 32:
		return "goroutines_32"
	default:
		return "goroutines_n"
	}
}
