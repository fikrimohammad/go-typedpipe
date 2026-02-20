# go-typedpipe

[![Go Reference](https://pkg.go.dev/badge/github.com/fikrimohammad/go-typedpipe.svg)](https://pkg.go.dev/github.com/fikrimohammad/go-typedpipe)
[![CI](https://github.com/fikrimohammad/go-typedpipe/actions/workflows/ci.yml/badge.svg)](https://github.com/fikrimohammad/go-typedpipe/actions/workflows/ci.yml)

`go-typedpipe` provides a generic, in-memory, concurrency-safe pipe for streaming typed values between goroutines.

It is conceptually similar to `io.Pipe`, but operates on values of any type `T` instead of `[]byte`. Unlike a plain `chan T`, it provides context-aware blocking, idempotent close with error propagation, and a drain guarantee — buffered values written before close remain readable after close.

It is a small synchronization primitive, not a queue or broker.

---

## Installation

```bash
go get github.com/fikrimohammad/go-typedpipe
```

Requires Go 1.18 or later.

---

## Example

```go
package main

import (
	"context"
	"fmt"
	"log"

	typedpipe "github.com/fikrimohammad/go-typedpipe"
)

func main() {
	w, r, err := typedpipe.New[int]()
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()

	go func() {
		for i := 0; i < 3; i++ {
			if err := w.Write(ctx, i); err != nil {
				log.Println("write:", err)
				return
			}
		}
		w.Close()
	}()

	for {
		v, err := r.Read(ctx)
		if err != nil {
			break
		}
		fmt.Println(v)
	}
	// Output:
	// 0
	// 1
	// 2
}
```

### Propagating errors with CloseWithError

```go
w, r, err := typedpipe.New[int]()
if err != nil {
	log.Fatal(err)
}

ctx := context.Background()

go func() {
	if err := fetchData(ctx, w); err != nil {
		// Signal the reader why the pipe stopped.
		w.CloseWithError(fmt.Errorf("fetch failed: %w", err))
		return
	}
	w.Close()
}()

for {
	v, err := r.Read(ctx)
	if err != nil {
		log.Println("reader stopped:", err) // fetch failed: ...
		break
	}
	process(v)
}
```

---

## Semantics

### Write

`Write(ctx, v)` blocks until:

* The value is delivered
* `ctx` is canceled
* The pipe is closed

Returns the stored close error if the pipe is closed, or `ctx.Err()` if the context is canceled.

### Read

`Read(ctx)` blocks until:

* A value is available
* `ctx` is canceled
* The pipe is closed and fully drained

After all buffered values are consumed, returns the stored close error.

### Close

* `Close()` closes the pipe with `ErrPipeClosed`.
* `CloseWithError(err)` closes the pipe with a custom error. If `err` is nil, `ErrPipeClosed` is used.
* Both are idempotent — subsequent calls are no-ops.
* The first non-nil error wins and is returned to all future operations.

---

## Buffering

| | Value |
|---|---|
| Default buffer size | `64` |
| Maximum buffer size | `2048` |

```go
w, r, err := typedpipe.New[int](
	typedpipe.WithBufferSize(128),
)
if err != nil {
	log.Fatal(err)
}
```

A buffer size of 0 or less produces an unbuffered pipe, where each `Write` blocks until a corresponding `Read` occurs.

---

## Guarantees

* **Safe for concurrent use** — multiple goroutines may call `Read`, `Write`, and `Close` simultaneously.
* **No send-on-closed-channel panics** — the internal channel is never closed; shutdown is signalled separately.
* **Idempotent shutdown** — calling `Close` or `CloseWithError` multiple times is safe.
* **First error wins** — the close error is set once and never overwritten.
* **Full drain on close** — values written before close are fully readable after close, in order.
* **Backpressure** — `Write` blocks when the buffer is full, preventing unbounded memory growth.

---

## Use Cases

Appropriate for:

* Producer–consumer pipelines
* Worker coordination
* Structured streaming between goroutines
* Replacing `chan T` when context-aware operations and close error propagation are needed

Not intended for:

* Broadcast or fan-out (single reader only)
* Durable messaging
* Cross-process communication

---

## License

MIT