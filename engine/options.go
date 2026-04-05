package engine

// options defines a set of configuration options for the database.
type options struct {
	syncOnWrite       bool  // Should we fsync db writes?
	memtableFlushSize int64 // Flush threshold in bytes; 0 = no auto-flush
}

// Option configures how the database is opened.
type Option func(*options)

// WithSync controls whether writes are fsync'd.
func WithSync(sync bool) Option {
	return func(o *options) {
		o.syncOnWrite = sync
	}
}

// WithMemtableFlushSize controls whether memtables are flushed
// to disk automatically or not by setting a size (bytes) upper-bound.
func WithMemtableFlushSize(size int64) Option {
	return func(o *options) {
		o.memtableFlushSize = size
	}
}

func applyOptions(opts []Option) *options {
	cfg := &options{
		// Set defaults
		syncOnWrite: true,
	}

	for _, opt := range opts {
		opt(cfg)
	}

	return cfg
}
