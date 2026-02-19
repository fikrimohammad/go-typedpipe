package typedpipe

// Option configures a Pipe created by New.
type Option func(*options)

// options holds the internal configuration for a Pipe.
type options struct {
	bufferSize int
}

// WithBufferSize sets the capacity of the pipe's internal buffer.
//
// If size <= 0, the pipe is unbuffered.
// If size exceeds MaxBufferSize, it is clamped to MaxBufferSize.
func WithBufferSize(size int) Option {
	return func(o *options) {
		o.bufferSize = size
	}
}
