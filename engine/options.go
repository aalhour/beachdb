package engine

// options defines a set of configuration options for the database.
type options struct {
	syncOnWrite       bool  // Should we fsync db writes?
	memtableFlushSize int64 // Flush threshold in bytes; 0 = no auto-flush
	sstBlockSize      int   // SSTable block size in bytes; 0 = use default (4096)
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

// WithSSTBlockSize sets the target block size for SSTable writers.
// Each data block will be flushed to disk once it reaches approximately
// this many bytes. Must be > 0. Defaults to 4096 bytes.
func WithSSTBlockSize(size int) Option {
	return func(o *options) {
		o.sstBlockSize = size
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
