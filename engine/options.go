package engine

// options defines a set of configuration options for the database.
type options struct {
	syncOnWrite bool // Should we fsync db writes?
}

// Option configures how the database is opened.
type Option func(*options)

// WithSync controls whether writes are fsync'd.
func WithSync(sync bool) Option {
	return func(o *options) {
		o.syncOnWrite = sync
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
