package typedpipe

// Option configures a pipe at construction time.
type Option func(*options)

type options struct {
	bufferSize int
}

// WithBufferSize sets the pipe's internal channel capacity.
// A value <= 0 produces an unbuffered (synchronous) pipe.
// Values > MaxBufferSize cause New() to return an error.
func WithBufferSize(n int) Option {
	return func(o *options) {
		o.bufferSize = n
	}
}
