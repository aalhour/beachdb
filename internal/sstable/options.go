package sstable

// options defines a set of configuration options for the database.
type options struct {
	syncOnClose bool // Should we sync the file?
	blockSize   int  // Size of each block inside the table
}

// WriterOption configures the SSTable writer.
type WriterOption func(*options)

// WithSync controls whether files are fsync'd on close.
func WithSync(syncOnClose bool) WriterOption {
	return func(o *options) {
		o.syncOnClose = syncOnClose
	}
}

// WithBlockSize controls the size of each block in the file.
func WithBlockSize(size int) WriterOption {
	return func(o *options) {
		o.blockSize = size
	}
}

func applyOptions(opts []WriterOption) *options {
	cfg := &options{
		// Set defaults
		syncOnClose: true,
		blockSize:   4096,
	}

	for _, opt := range opts {
		opt(cfg)
	}

	return cfg
}
