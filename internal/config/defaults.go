package config

// Source of truth for scripts/ci/ttl-drift.sh.
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
	defaultInternalHTTPPort      int   = 8081
)
