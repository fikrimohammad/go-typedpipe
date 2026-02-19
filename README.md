# go-typedpipe

`go-typedpipe` provides a generic, in-memory, concurrency-safe pipe.

It connects a `Writer[T]` and a `Reader[T]` for streaming typed values between goroutines with:

* Blocking semantics
* Backpressure
* Context cancellation
* Idempotent close with error propagation

It is a small synchronization primitive, not a queue or broker.

---

## Installation

```bash
go get github.com/fikrimohammad/go-typedpipe
```

Requires Go 1.18 or later.

---

## Overview

`go-typedpipe` is conceptually similar to `io.Pipe`, but operates on values of type `T` instead of `[]byte`.

A pipe consists of:

* A `Writer[T]`
* A `Reader[T]`

Either side may close the pipe. The first non-nil close error is retained and returned by subsequent operations.

---

## Example

```go
w, r := typedpipe.New[int]()

ctx := context.Background()

go func() {
	for i := 0; i < 3; i++ {
		_ = w.Write(ctx, i)
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
```

---

## Semantics

### Write

`Write(ctx, v)` blocks until:

* The value is delivered
* `ctx` is canceled
* The pipe is closed

If the pipe is closed, `Write` returns the stored close error.

---

### Read

`Read(ctx)` blocks until:

* A value is available
* `ctx` is canceled
* The pipe is closed and fully drained

After buffered values are consumed, `Read` returns the stored close error.

---

### Close

* `Close()` closes the pipe with `ErrPipeClosed`.
* `CloseWithError(err)` closes the pipe with `err`.
* Close is idempotent.
* The first non-nil error wins.

---

## Buffering

Default buffer size: `64`
Maximum buffer size: `2048`

Configure:

```go
w, r := typedpipe.New[int](
	typedpipe.WithBufferSize(128),
)
```

If the buffer size is ≤ 0, the pipe is unbuffered.

---

## Use Cases

Appropriate for:

* Producer–consumer pipelines
* Worker coordination
* Structured streaming between goroutines
* Replacing `chan T` when explicit close error and context-aware operations are required

Not intended for:

* Broadcast or fan-out
* Durable messaging
* Cross-process communication

---

## Guarantees

* Safe for concurrent use
* No send-on-closed-channel panics
* Idempotent shutdown
* Backpressure by default

---

## License

MIT
