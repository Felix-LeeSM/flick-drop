package config

// Default config values; the single source of truth referenced by Load and the
// default-assertion tests.
const (
	defaultPayloadInlineMaxBytes int64 = 1048576  // 1 MiB
	defaultMaxFileBytes          int64 = 52428800 // 50 MiB
	defaultDefaultTTLSeconds     int   = 3600     // 1 hour
	defaultMinTTLSeconds         int   = 300      // 5 minutes
	defaultMaxTTLSeconds         int   = 604800   // 7 days
	defaultOpenRatePerMinute     int   = 10
	defaultCreateRatePerMinute   int   = 5
	defaultReaperIntervalSeconds int   = 60
	defaultReaperBatchSize       int   = 50
)
