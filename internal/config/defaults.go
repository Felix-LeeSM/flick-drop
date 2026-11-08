package config

// Default config values; the single source of truth referenced by Load and the
// default-assertion tests.
const (
	defaultPayloadInlineMaxBytes int64 = 1048576   // 1 MiB
	defaultMaxFileBytes          int64 = 268435456 // 256 MiB
	defaultDefaultTTLSeconds     int   = 3600      // 1 hour
	defaultMinTTLSeconds         int   = 300       // 5 minutes
	defaultMaxTTLSeconds         int   = 604800    // 7 days
	defaultOpenRatePerMinute     int   = 10
	defaultCreateRatePerMinute   int   = 5
	defaultReaperIntervalSeconds int   = 60
	defaultReaperBatchSize       int   = 50
	defaultPipeRelayEnabled      bool  = true
	defaultPipeMaxConcurrent     int   = 100
	// Free-tier byte totals per object backend, in decimal GB (10^9 bytes) —
	// the same unit the providers bill in. Ceilings derive from these so the
	// margin reads as a fraction, not a magic number.
	ociAlwaysFreeObjectBytes int64 = 20_000_000_000 // OCI Always Free 20 GB
	r2FreeObjectBytes        int64 = 10_000_000_000 // R2 free 10 GB

	// Cumulative stored-bytes ceilings per backend: 90% of each free tier. The
	// remaining 10% absorbs the async-deletion gap (consumed rows keep their
	// object for a 35-minute grace; deleted rows wait for the worker). 0 means
	// unlimited. See docs/architecture/storage-model.md.
	defaultS3MaxTotalBytes int64 = ociAlwaysFreeObjectBytes * 9 / 10
	defaultR2MaxTotalBytes int64 = r2FreeObjectBytes * 9 / 10
)
