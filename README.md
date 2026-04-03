# go-typedpipe

[![Go Reference](https://pkg.go.dev/badge/github.com/fikrimohammad/go-typedpipe.svg)](https://pkg.go.dev/github.com/fikrimohammad/go-typedpipe)
[![CI](https://github.com/fikrimohammad/go-typedpipe/actions/workflows/ci.yml/badge.svg)](https://github.com/fikrimohammad/go-typedpipe/actions/workflows/ci.yml)

`go-typedpipe` provides a generic, in-memory, concurrency-safe pipe for streaming typed values between goroutines.

It is conceptually similar to `io.Pipe`, but operates on values of any type `T` instead of `[]byte`. Unlike a plain `chan T`, it provides context-aware blocking, idempotent close with error propagation, and a drain guarantee — buffered values written before close remain readable after close.

It is a small synchronization primitive, not a queue or broker.
 
## Why not just use a channel?
 
A plain `chan T` works well for simple cases, but leaves several concerns to the caller:
 
| | `chan T` | `go-typedpipe` |
|---|---|---|
| Context-aware blocking | Manual `select` on every send/receive | Built into `Write` and `Read` |
| Close error propagation | Not supported | `CloseWithError` propagates to all consumers |
| Safe concurrent close | Panics on double-close | Idempotent, safe to call multiple times |
| Drain guarantee | Values may be lost after close | All buffered values remain readable after close |
| Consumer loop | Boilerplate `for range` or `select` | `ReadAll` encapsulates the loop |
---

## Installation

```bash
go get github.com/fikrimohammad/go-typedpipe
```

Requires Go 1.18 or later.

---

## Example

An HTTP scraper that fetches a list of URLs concurrently and processes the results as they arrive.

The scraper goroutine writes each scraped `Result` into the pipe as soon as it's ready. The consumer reads from the pipe and saves each result to a database. If saving fails, the consumer signals the scraper to stop — so no more URLs are fetched unnecessarily.

```go
type Result struct {
	URL        string
	StatusCode int
	Body       []byte
}

func scrape(ctx context.Context, urls []string, w typedpipe.Writer[Result]) {
	var wg sync.WaitGroup
	for _, url := range urls {
		wg.Add(1)
		go func(url string) {
			defer wg.Done()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				w.CloseWithError(fmt.Errorf("build request %s: %w", url, err))
				return
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				w.CloseWithError(fmt.Errorf("fetch %s: %w", url, err))
				return
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			w.Write(ctx, Result{
				URL:        url,
				StatusCode: resp.StatusCode,
				Body:       body,
			})
		}(url)
	}
	wg.Wait()
	w.Close()
}
```

### Using `ReadAll`

Use `ReadAll` for the straightforward consume-all case. The pipe is closed automatically when `ReadAll` returns, and `ErrPipeClosed` is handled internally so the caller only sees real errors.

```go
func main() {
	urls := []string{
		"https://example.com",
		"https://example.org",
		"https://example.net",
	}

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

### Using `Read`

Use `Read` when you need finer control between reads — for example, routing results differently based on the status code.

```go
func main() {
	urls := []string{
		"https://example.com",
		"https://example.org",
		"https://example.net",
	}

	ctx := context.Background()
	w, r := typedpipe.New[Result](typedpipe.WithBufferSize(len(urls)))

	go scrape(ctx, urls, w)

	for {
		result, err := r.Read(ctx)
		if err != nil {
			break
		}
		switch {
		case result.StatusCode == http.StatusOK:
			if err := saveToDatabase(result); err != nil {
				r.CloseWithError(fmt.Errorf("save %s: %w", result.URL, err))
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

### ReadAll

`ReadAll(ctx, fn)` is a convenience method on `Reader` that encapsulates the read loop:

* Calls `fn` for each value in order
* Returns `nil` when the pipe is closed normally — `ErrPipeClosed` is handled internally
* Returns a non-nil error if the pipe was closed with a custom error, the context was canceled, or `fn` returns an error
* When `fn` returns an error, closes the pipe with that error via `CloseWithError` before returning
* Always closes the pipe when it returns, so the caller does not need to call `Close` explicitly

Use `Read` when you need fine-grained control between reads (e.g. branching logic, integrating into a `select`). Use `ReadAll` for the straightforward consume-all case.

### Close

* `Close()` closes the pipe with `ErrPipeClosed`.
* `CloseWithError(err)` closes the pipe with a custom error. If `err` is nil, `ErrPipeClosed` is used.
* Both are idempotent — subsequent calls are no-ops.
* The first non-nil error wins and is returned to all future operations.

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

---

## License

MIT