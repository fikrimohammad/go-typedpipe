# go-typedpipe

[![Go Reference](https://pkg.go.dev/badge/github.com/fikrimohammad/go-typedpipe.svg)](https://pkg.go.dev/github.com/fikrimohammad/go-typedpipe)
[![CI](https://github.com/fikrimohammad/go-typedpipe/actions/workflows/ci.yml/badge.svg)](https://github.com/fikrimohammad/go-typedpipe/actions/workflows/ci.yml)

> `io.Pipe`, but for any type `T`.

A tiny, generic, concurrency-safe pipe for streaming typed values between goroutines — with context-aware blocking, backpressure, and clean shutdown semantics.

---

## Why not just `chan T`?

| | `chan T` | `go-typedpipe` |
|---|---|---|
| Context-aware blocking | ❌ | ✅ |
| Close with error | ❌ | ✅ |
| Drain buffered values after close | ❌ | ✅ |
| Idempotent close (no panic) | ❌ | ✅ |
| Backpressure | ✅ | ✅ |

---

## Install

```bash
go get github.com/fikrimohammad/go-typedpipe
```

Requires **Go 1.18+**.

---

## Quick start

```go
w, r, err := typedpipe.New[int]()
if err != nil {
    log.Fatal(err)
}

ctx := context.Background()

// Producer
go func() {
    for i := range 5 {
        w.Write(ctx, i)
    }
    w.Close()
}()

// Consumer
for {
    v, err := r.Read(ctx)
    if errors.Is(err, typedpipe.ErrPipeClosed) {
        break
    }
    fmt.Println(v) // 0, 1, 2, 3, 4
}
```

---

## Real-world: multi-stage pipeline

A common pattern — fan out rows from a database, process them in parallel, stream results downstream:

```go
// Stage 1: DB rows → typed pipe
dbWriter, dbReader, _ := typedpipe.New[Row]()
go func() {
    for rows.Next() {
        var row Row
        rows.StructScan(&row)
        dbWriter.Write(ctx, row)
    }
    dbWriter.Close()
}()

// Stage 2: fan-out processing (32 workers)
resultWriter, resultReader, _ := typedpipe.New[Result]()
var wg sync.WaitGroup
for range 32 {
    wg.Add(1)
    go func() {
        defer wg.Done()
        for {
            row, err := dbReader.Read(ctx)
            if errors.Is(err, typedpipe.ErrPipeClosed) {
                return
            }
            resultWriter.Write(ctx, process(row))
        }
    }()
}
go func() { wg.Wait(); resultWriter.Close() }()

// Stage 3: consume results
for {
    result, err := resultReader.Read(ctx)
    if errors.Is(err, typedpipe.ErrPipeClosed) {
        break
    }
    emit(result)
}
```

---

## Propagating errors

```go
go func() {
    if err := fetchData(ctx, w); err != nil {
        w.CloseWithError(fmt.Errorf("fetch failed: %w", err))
        return
    }
    w.Close()
}()

for {
    v, err := r.Read(ctx)
    if err != nil {
        log.Println(err) // "fetch failed: ..."
        break
    }
    process(v)
}
```

---

## Custom buffer size

```go
// Default: 64. Max: 2048. Size <= 0 → unbuffered (synchronous).
w, r, err := typedpipe.New[int](typedpipe.WithBufferSize(256))
```

---

## Semantics

**Write** blocks until the value is delivered, the context is cancelled, or the pipe is closed.

**Read** blocks until a value is available, the context is cancelled, or the pipe is closed and fully drained. Buffered values written before close are always returned before the close error.

**Close / CloseWithError** are idempotent — safe to call multiple times from any goroutine. First error wins. Either side (reader or writer) may close the pipe.

---

## License

MIT
