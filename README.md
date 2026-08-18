# go-typedpipe

[![Go Reference](https://pkg.go.dev/badge/github.com/fikrimohammad/go-typedpipe.svg)](https://pkg.go.dev/github.com/fikrimohammad/go-typedpipe/v2)
[![CI](https://github.com/fikrimohammad/go-typedpipe/actions/workflows/ci.yml/badge.svg)](https://github.com/fikrimohammad/go-typedpipe/actions/workflows/ci.yml)

`go-typedpipe` provides a generic, in-memory, concurrency-safe pipe for streaming typed values between goroutines.

It is conceptually similar to `io.Pipe`, but operates on values of any type `T` instead of `[]byte`. Unlike a plain `chan T`, it provides context-aware blocking, idempotent close with error propagation, standard `io.Closer` compliance, buffer introspection, and a drain guarantee — buffered values written before close remain readable after close.

It is a small synchronization primitive, not a queue or broker.
 
## Why not just use a channel?
 
A plain `chan T` works well for simple cases, but leaves several concerns to the caller:
 
| | `chan T` | `go-typedpipe` |
|---|---|---|
| Context-aware blocking | Manual `select` on every send/receive | Built into `Write` and `Read` |
| Close error propagation | Not supported | `CloseWithError` propagates to all consumers |
| Safe concurrent close | Panics on double-close | Idempotent, safe to call multiple times |
| Drain guarantee | Values may be lost after close | All buffered values remain readable after close |
| Consumer loops | Boilerplate `for range` or `select` | `ReadAll` and Go 1.23+ `Values` iterators |
| Introspection | Only `len` / `cap` | `Len()`, `Cap()`, and `IsClosed()` |
| Interface compliance | Concrete channel type only | Implements standard `io.Closer` |

---

## Installation

```bash
go get github.com/fikrimohammad/go-typedpipe/v2
```

Requires Go 1.18 or later (Go 1.23+ for `Values` range-over-func iterators).

---

## Quick Start

```go
package main

import (
    "context"
    "fmt"
    "github.com/fikrimohammad/go-typedpipe/v2"
)

func main() {
    ctx := context.Background()
    w, r := typedpipe.New[int]()

    go func() {
        defer w.Close()
        _ = w.Write(ctx, 42)
    }()

    val, err := r.Read(ctx)
    fmt.Println(val, err) // 42, nil
}
```

---

## Real-World Example

An HTTP scraper that fetches a list of URLs concurrently and streams the results as they arrive.

The scraper goroutine writes each scraped `Result` into the pipe as soon as it's ready. The consumer reads from the pipe and saves each result to a database. If saving fails, the consumer closes the pipe with an error — signaling the scraper goroutines to abort early without wasting network bandwidth.

```go
type Result struct {
    URL        string
    StatusCode int
    Body       []byte
}
 
func scrape(ctx context.Context, urls []string, w typedpipe.Writer[Result]) {
    defer w.Close()
    var wg sync.WaitGroup
    for _, url := range urls {
        wg.Add(1)
        go func(url string) {
            defer wg.Done()
            req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
            if err != nil {
                _ = w.CloseWithError(fmt.Errorf("build request %s: %w", url, err))
                return
            }
            resp, err := http.DefaultClient.Do(req)
            if err != nil {
                _ = w.CloseWithError(fmt.Errorf("fetch %s: %w", url, err))
                return
            }
            defer resp.Body.Close()
            body, _ := io.ReadAll(resp.Body)
            _ = w.Write(ctx, Result{
                URL:        url,
                StatusCode: resp.StatusCode,
                Body:       body,
            })
        }(url)
    }
    wg.Wait()
}
```

### Pattern 1: Using Go 1.23+ Iterators (`Values`)

In Go 1.23+, consume the pipe natively with `for ... range` over `typedpipe.Values`. Breaking from the loop automatically closes the pipe:

```go
func main() {
    urls := []string{"https://example.com", "https://example.org", "https://example.net"}
    ctx := context.Background()
    w, r := typedpipe.New[Result](typedpipe.WithBufferSize(len(urls)))
    
    go scrape(ctx, urls, w)
    
    for result, err := range typedpipe.Values(ctx, r) {
        if err != nil {
            log.Fatal("scraper stopped with error:", err)
        }
        if err := saveToDatabase(result); err != nil {
            log.Printf("save %s failed: %v", result.URL, err)
            break // breaking early automatically closes the pipe
        }
        log.Printf("saved %s (%d)", result.URL, result.StatusCode)
    }
}
```

### Pattern 2: Using `ReadAll` (Callback Loop)

Use `ReadAll` for the straightforward consume-all case. The pipe is closed automatically when `ReadAll` returns, and normal close (`ErrPipeClosed`) is handled internally:

```go
func main() {
    urls := []string{"https://example.com", "https://example.org", "https://example.net"}
    ctx := context.Background()
    w, r := typedpipe.New[Result](typedpipe.WithBufferSize(len(urls)))
    
    go scrape(ctx, urls, w)
    
    err := r.ReadAll(ctx, func(result Result) error {
        if err := saveToDatabase(result); err != nil {
            return fmt.Errorf("save %s: %w", result.URL, err)
        }
        log.Printf("saved %s (%d)", result.URL, result.StatusCode)
        return nil
    })
    if err != nil {
        log.Fatal("scraper stopped:", err)
    }
}
```

### Pattern 3: Using `Read` (Fine-Grained Branching)

Use `Read` when you need fine control between reads (e.g. status-code routing or integrating into an outer `select`):

```go
func main() {
    urls := []string{"https://example.com", "https://example.org", "https://example.net"}
    ctx := context.Background()
    w, r := typedpipe.New[Result](typedpipe.WithBufferSize(len(urls)))
    
    go scrape(ctx, urls, w)
    
    for {
        result, err := r.Read(ctx)
        if err != nil {
            if !errors.Is(err, typedpipe.ErrPipeClosed) {
                log.Fatal("reader stopped:", err)
            }
            break
        }
        switch {
        case result.StatusCode == http.StatusOK:
            if err := saveToDatabase(result); err != nil {
                _ = r.CloseWithError(fmt.Errorf("save %s: %w", result.URL, err))
            }
            log.Printf("saved %s (%d)", result.URL, result.StatusCode)
        case result.StatusCode >= 500:
            log.Printf("server error %s (%d), retrying later", result.URL, result.StatusCode)
            scheduleRetry(result.URL)
        default:
            log.Printf("skipping %s (%d)", result.URL, result.StatusCode)
        }
    }
}
```

---

## Working with Slices & Reference Types (`[]byte`, `[]T`)

Because `go-typedpipe` is generic over `[T any]`, it can stream slices (e.g. `[]byte`, `[]Record`, or custom batches) with zero additional API complexity.

When passing reference types or slices through the pipe, keep the following Go concurrency rules in mind:

### 1. Avoid Reusing Scratch Buffers Across Writes
In Go, sending a slice through a channel passes the slice header by value while **sharing the backing array**. If the producer modifies or reuses the same buffer on subsequent iterations, concurrent readers will observe data races.

```go
// ❌ UNSAFE: Reusing scratch buffer in a buffered pipe
buf := make([]byte, 1024)
for {
    n, _ := src.Read(buf)
    w.Write(ctx, buf[:n]) // Bug: next src.Read overwrites data while reader is processing!
}

// ✅ SAFE: Allocate fresh slice, copy, or transfer ownership from a sync.Pool
chunk := make([]byte, n)
copy(chunk, buf[:n])
w.Write(ctx, chunk)
```

### 2. Choosing between `io.Pipe` and `typedpipe[[]byte]`
- Use **`io.Pipe`** for raw, continuous byte streaming into standard library interfaces (`http.Request.Body`, `gzip.Writer`, `tar`, `io.Copy`).
- Use **`typedpipe[[]byte]`** or **`typedpipe[[]T]`** for discrete message framing, packet queues, or batch processing (`[]Record`).

---

## How It Works (Under the Hood)

1. **Two-Channel Architecture**:
   - `ch chan T`: Carries data payload. **`ch` is never closed**, making it impossible for writers to trigger `panic: send on closed channel`.
   - `done chan struct{}`: Closed once on shutdown via `sync.Once`, signaling all goroutines simultaneously.
2. **Priority Pre-Checks**:
   - `Write` checks `done` and `ctx.Done()` in a non-blocking select before touching `ch`, preventing post-shutdown writes.
   - `Read` checks `ctx.Done()` before reading `ch`, ensuring immediate cancellation response.
3. **Drain Guarantee**:
   - When `done` is closed, `Read` drains remaining buffered items in FIFO order via a non-blocking check on `ch` before returning the terminal close error.
4. **Atomic First-Error Propagation**:
   - The first error passed to `CloseWithError` is stored atomically and broadcast to all readers and writers.

---

## Semantics

### Write

`Write(ctx, v)` blocks until:
* The value is delivered
* `ctx` is canceled
* The pipe is closed

Returns the stored close error if the pipe is closed, or `ctx.Err()` if the context is canceled.

> **Important:** Always close the pipe when the writer exits. The recommended pattern is:
> ```go
> go func() {
>     defer w.Close()
>     for _, v := range data {
>         if err := w.Write(ctx, v); err != nil {
>             return
>         }
>     }
> }()
> ```

### Read

`Read(ctx)` blocks until:
* A value is available
* `ctx` is canceled
* The pipe is closed and fully drained

After all buffered values are consumed, returns the stored close error.

### ReadAll

`ReadAll(ctx, fn)` encapsulates the consumer loop:
* Calls `fn` for each value in order
* Returns `nil` when the pipe is closed normally (`ErrPipeClosed`)
* Returns a non-nil error if closed with a custom error, context was canceled, or `fn` fails
* Closes the pipe automatically with the triggering error upon exit

### Values (Iterators, Go 1.23+)

`Values(ctx, r)` returns an `iter.Seq2[T, error]` yielding `(value, nil)` until drained.
Exiting the loop early (via `break` or `return`) closes the pipe automatically.

### Close & `io.Closer`

* `Close()` closes the pipe with `ErrPipeClosed`. Implements standard `io.Closer`.
* `CloseWithError(err)` closes the pipe with a custom error. If `err` is nil, `ErrPipeClosed` is used.
* Both are idempotent — subsequent calls are no-ops.
* The first non-nil error wins and is returned to all future operations.

### Introspection

Both `Writer` and `Reader` provide non-blocking inspection methods:
* `Len() int` — returns the number of buffered items currently in the pipe.
* `Cap() int` — returns the total buffer capacity of the pipe.
* `IsClosed() bool` — reports whether the pipe has been closed.

---

## Buffering
```go
w, r := typedpipe.New[int](
    typedpipe.WithBufferSize(128),
)
```

Buffer sizing and any upper-bound enforcement is left to the caller. A value of 0 or less produces an unbuffered pipe, where each `Write` blocks until a corresponding `Read` occurs. Default buffer size = `64`.

---

## Guarantees

* **Safe for concurrent use** — multiple goroutines may call `Read`, `Write`, and `Close` simultaneously.
* **No send-on-closed-channel panics** — the internal data channel is never closed.
* **Idempotent shutdown** — calling `Close` or `CloseWithError` multiple times is safe.
* **First error wins** — the close error is set once and never overwritten.
* **Full drain on close** — values written before close are fully readable after close, in order.
* **Backpressure** — `Write` blocks when the buffer is full, preventing unbounded memory growth.
* **Standard `io.Closer` compatibility** — `Writer` and `Reader` satisfy `io.Closer`.

---

## Benchmark

Benchmarked on Apple M4 Pro, Go 1.22:

```
goos: darwin
goarch: arm64
cpu: Apple M4 Pro
 
BenchmarkPipe_WriteRead/unbuffered-14           14642935      245.3 ns/op     0 B/op   0 allocs/op
BenchmarkPipe_WriteRead/buffer_64-14            27531162      128.9 ns/op     0 B/op   0 allocs/op
BenchmarkPipe_WriteRead/buffer_256-14           30460545      118.2 ns/op     0 B/op   0 allocs/op
BenchmarkPipe_WriteRead/buffer_1024-14          33059798      109.3 ns/op     0 B/op   0 allocs/op
 
BenchmarkPipe_ReadAll/unbuffered-14             14487556      243.2 ns/op     0 B/op   0 allocs/op
BenchmarkPipe_ReadAll/buffer_64-14              27784093      130.7 ns/op     0 B/op   0 allocs/op
BenchmarkPipe_ReadAll/buffer_256-14             30330379      116.8 ns/op     0 B/op   0 allocs/op
BenchmarkPipe_ReadAll/buffer_1024-14            33114181      108.6 ns/op     0 B/op   0 allocs/op
 
BenchmarkPipe_Values/unbuffered-14              14521020      244.8 ns/op     0 B/op   0 allocs/op
BenchmarkPipe_Values/buffer_64-14               27801210      129.5 ns/op     0 B/op   0 allocs/op

BenchmarkPipe_ConcurrentWriters/goroutines_2-14     21403360      169.5 ns/op     0 B/op   0 allocs/op
BenchmarkPipe_ConcurrentWriters/goroutines_8-14     15097734      226.0 ns/op     0 B/op   0 allocs/op
BenchmarkPipe_ConcurrentWriters/goroutines_32-14    10917823      311.0 ns/op     0 B/op   0 allocs/op
 
BenchmarkPipe_ConcurrentReaders/goroutines_2-14      9401684      374.0 ns/op     0 B/op   0 allocs/op
BenchmarkPipe_ConcurrentReaders/goroutines_8-14      5456012      656.2 ns/op     0 B/op   0 allocs/op
BenchmarkPipe_ConcurrentReaders/goroutines_32-14     4851226      751.3 ns/op     0 B/op   0 allocs/op
```

**Key observations:**

- **Zero allocations** across all benchmarks — no GC pressure regardless of throughput.
- **`Values` and `ReadAll` overhead is negligible** — virtually identical to raw `Read` at every buffer size.
- **Larger buffers improve throughput** — `buffer_1024` at ~109 ns/op vs unbuffered at ~245 ns/op, as writers block less frequently.
- **Concurrent readers degrade gracefully** — throughput scales predictably under contention without panics or data races.

---

## Use Cases

Appropriate for:

* Producer–consumer pipelines
* Worker coordination
* Structured streaming between goroutines
* Replacing `chan T` when context-aware operations and close error propagation are needed
* Streaming discrete batches or packet frames (`[]Record`, `[]byte`)

---

## License

MIT
