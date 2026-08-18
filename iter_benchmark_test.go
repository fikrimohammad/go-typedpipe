//go:build go1.23

package typedpipe

import (
	"context"
	"testing"
)

// ── Values (Iterators) ────────────────────────────────────────────────────────

func BenchmarkPipe_Values(b *testing.B) {
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
				_ = w.Close()
			}()

			for _, err := range Values(ctx, r) {
				if err != nil {
					break
				}
			}
		})
	}
}
