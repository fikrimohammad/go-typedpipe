package datastream

// Option configures a new data stream.
type Option func(*options)

// options holds the configuration for a data stream.
type options struct {
	bufferSize int
}

// WithBufferSize creates an Option to set the buffer size of the data stream.
func WithBufferSize(size int) Option {
	return func(o *options) {
		o.bufferSize = size
	}
}
