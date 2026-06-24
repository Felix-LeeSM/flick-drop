package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// S3Config configures the S3-compatible large-object store (MinIO dev, OCI
// S3-compat prod). Enabled is driven by FLICK_STORAGE_LARGE_BACKEND=s3; auth is
// always a static key pair because the AWS SDK cannot speak OCI instance
// principal directly.
type S3Config struct {
	Enabled         bool
	Endpoint        string
	Region          string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
	PathStyle       bool
}

type Config struct {
	Env                   string
	PublicBaseURL         string
	InternalToken         string
	InternalAPIBaseURL    string
	APIAddr               string
	APIDBPath             string
	WorkerDBPath          string
	NATSURL               string
	NATSStream            string
	NATSJobSubject        string
	PayloadInlineMaxBytes int64
	MaxFileBytes          int64
	DefaultTTLSeconds     int
	MinTTLSeconds         int
	MaxTTLSeconds         int
	OpenRatePerMinute     int
	CreateRatePerMinute   int
	ReaperIntervalSeconds int
	ReaperBatchSize       int
	TrustedProxies        []*net.IPNet
	S3                    S3Config
}

func Load() (Config, error) {
	cfg := Config{
		Env:                   getenv("FLICK_ENV", "development"),
		PublicBaseURL:         getenv("FLICK_PUBLIC_BASE_URL", "http://localhost:5173"),
		InternalToken:         getenv("FLICK_INTERNAL_TOKEN", ""),
		InternalAPIBaseURL:    getenv("FLICK_INTERNAL_API_BASE_URL", "http://localhost:8080"),
		APIAddr:               getenv("FLICK_API_ADDR", ":8080"),
		APIDBPath:             getenv("FLICK_API_DB_PATH", "./var/api.db"),
		WorkerDBPath:          getenv("FLICK_WORKER_DB_PATH", "./var/worker.db"),
		NATSURL:               getenv("FLICK_NATS_URL", "nats://127.0.0.1:4222"),
		NATSStream:            getenv("FLICK_NATS_STREAM", "FLICK_JOBS"),
		NATSJobSubject:        getenv("FLICK_NATS_JOB_SUBJECT", "flick.jobs"),
		PayloadInlineMaxBytes: defaultPayloadInlineMaxBytes,
		MaxFileBytes:          defaultMaxFileBytes,
		DefaultTTLSeconds:     defaultDefaultTTLSeconds,
		// TTL is a continuous range (5 minutes .. 7 days) so the browser can
		// offer an editable "custom" lifetime, not just fixed presets.
		MinTTLSeconds:     defaultMinTTLSeconds,
		MaxTTLSeconds:     defaultMaxTTLSeconds,
		OpenRatePerMinute: defaultOpenRatePerMinute,
		// /api/secrets (presigned POST issuance) becomes a DoS amplifier once
		// large uploads bypass the server, so cap issuance per client IP + path.
		CreateRatePerMinute: defaultCreateRatePerMinute,
		// The expiration reaper sweeps expired/orphan secrets out of api.db. A
		// minute is frequent enough that rows never linger; the partial index
		// keeps the claim cheap even at high volume.
		ReaperIntervalSeconds: defaultReaperIntervalSeconds,
		ReaperBatchSize:       defaultReaperBatchSize,
		S3: S3Config{
			Enabled:         getenv("FLICK_STORAGE_LARGE_BACKEND", "disabled") == "s3",
			Endpoint:        getenv("FLICK_S3_ENDPOINT", ""),
			Region:          getenv("FLICK_S3_REGION", "us-east-1"),
			Bucket:          getenv("FLICK_S3_BUCKET", ""),
			AccessKeyID:     getenv("FLICK_S3_ACCESS_KEY_ID", ""),
			SecretAccessKey: getenv("FLICK_S3_SECRET_ACCESS_KEY", ""),
			// MinIO and OCI S3-compat both use path-style addressing.
			// PathStyle is resolved below from FLICK_S3_PATH_STYLE.
			PathStyle: false,
		},
	}

	var err error
	if cfg.S3.PathStyle, err = envBool("FLICK_S3_PATH_STYLE", true); err != nil {
		return Config{}, err
	}
	if cfg.PayloadInlineMaxBytes, err = envPositiveInt64("FLICK_PAYLOAD_INLINE_MAX_BYTES", cfg.PayloadInlineMaxBytes); err != nil {
		return Config{}, err
	}
	if cfg.MaxFileBytes, err = envPositiveInt64("FLICK_MAX_FILE_BYTES", cfg.MaxFileBytes); err != nil {
		return Config{}, err
	}
	if cfg.DefaultTTLSeconds, err = envPositiveInt("FLICK_DEFAULT_TTL_SECONDS", cfg.DefaultTTLSeconds); err != nil {
		return Config{}, err
	}
	if cfg.MinTTLSeconds, err = envPositiveInt("FLICK_MIN_TTL_SECONDS", cfg.MinTTLSeconds); err != nil {
		return Config{}, err
	}
	if cfg.MaxTTLSeconds, err = envPositiveInt("FLICK_MAX_TTL_SECONDS", cfg.MaxTTLSeconds); err != nil {
		return Config{}, err
	}
	if cfg.OpenRatePerMinute, err = envPositiveInt("FLICK_OPEN_RATE_PER_MIN", cfg.OpenRatePerMinute); err != nil {
		return Config{}, err
	}
	if cfg.CreateRatePerMinute, err = envPositiveInt("FLICK_CREATE_RATE_PER_MIN", cfg.CreateRatePerMinute); err != nil {
		return Config{}, err
	}
	if cfg.ReaperIntervalSeconds, err = envPositiveInt("FLICK_REAPER_INTERVAL_SECONDS", cfg.ReaperIntervalSeconds); err != nil {
		return Config{}, err
	}
	if cfg.ReaperBatchSize, err = envPositiveInt("FLICK_REAPER_BATCH_SIZE", cfg.ReaperBatchSize); err != nil {
		return Config{}, err
	}
	if raw := os.Getenv("FLICK_TRUSTED_PROXIES"); raw != "" {
		cfg.TrustedProxies, err = parseTrustedProxies(raw)
		if err != nil {
			return Config{}, err
		}
	}

	if cfg.MinTTLSeconds <= 0 {
		return Config{}, fmt.Errorf("FLICK_MIN_TTL_SECONDS must be a positive integer")
	}
	if cfg.MaxTTLSeconds < cfg.MinTTLSeconds {
		return Config{}, fmt.Errorf("FLICK_MAX_TTL_SECONDS must be >= FLICK_MIN_TTL_SECONDS")
	}
	if cfg.DefaultTTLSeconds < cfg.MinTTLSeconds || cfg.DefaultTTLSeconds > cfg.MaxTTLSeconds {
		return Config{}, fmt.Errorf("FLICK_DEFAULT_TTL_SECONDS must be within FLICK_MIN_TTL_SECONDS..FLICK_MAX_TTL_SECONDS")
	}
	if cfg.OpenRatePerMinute <= 0 {
		return Config{}, fmt.Errorf("FLICK_OPEN_RATE_PER_MIN must be a positive integer")
	}
	if cfg.CreateRatePerMinute <= 0 {
		return Config{}, fmt.Errorf("FLICK_CREATE_RATE_PER_MIN must be a positive integer")
	}
	if cfg.ReaperIntervalSeconds <= 0 {
		return Config{}, fmt.Errorf("FLICK_REAPER_INTERVAL_SECONDS must be a positive integer")
	}
	if cfg.ReaperBatchSize <= 0 {
		return Config{}, fmt.Errorf("FLICK_REAPER_BATCH_SIZE must be a positive integer")
	}
	if cfg.S3.Enabled {
		if cfg.S3.Bucket == "" || cfg.S3.Region == "" {
			return Config{}, fmt.Errorf("FLICK_S3_BUCKET and FLICK_S3_REGION are required when FLICK_STORAGE_LARGE_BACKEND=s3")
		}
		if cfg.S3.AccessKeyID == "" || cfg.S3.SecretAccessKey == "" {
			return Config{}, fmt.Errorf("FLICK_S3_ACCESS_KEY_ID and FLICK_S3_SECRET_ACCESS_KEY are required when FLICK_STORAGE_LARGE_BACKEND=s3")
		}
	}

	return cfg, nil
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func parsePositiveInt(name, raw string) (int, error) {
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return value, nil
}

func parsePositiveInt64(name, raw string) (int64, error) {
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return value, nil
}

// envPositiveInt returns cur when name is unset; otherwise it parses name as a
// positive integer. Int fields are seeded with a default in the Config literal,
// so cur carries that default through when the env var is absent.
func envPositiveInt(name string, cur int) (int, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return cur, nil
	}
	return parsePositiveInt(name, raw)
}

func envPositiveInt64(name string, cur int64) (int64, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return cur, nil
	}
	return parsePositiveInt64(name, raw)
}

// envBool parses a "true"/"false" env var (case- and whitespace-insensitive).
// Empty falls back to fallback; any other value is an error so typos surface at
// load time instead of silently enabling/disabling a flag.
func envBool(name string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	if strings.EqualFold(raw, "true") {
		return true, nil
	}
	if strings.EqualFold(raw, "false") {
		return false, nil
	}
	return false, fmt.Errorf("%s must be \"true\" or \"false\", got %q", name, raw)
}

// parseTrustedProxies parses a comma-separated list of CIDRs (e.g.
// "10.0.0.0/8,127.0.0.1/32") whose direct peer may supply a client IP via
// X-Forwarded-For. A bare IP gets an implicit /32 (v4) or /128 (v6).
func parseTrustedProxies(raw string) ([]*net.IPNet, error) {
	parts := strings.Split(raw, ",")
	cidrs := make([]*net.IPNet, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			return nil, fmt.Errorf("FLICK_TRUSTED_PROXIES must not contain empty entries")
		}
		cidr, err := parseCIDR(trimmed)
		if err != nil {
			return nil, fmt.Errorf("FLICK_TRUSTED_PROXIES: %w", err)
		}
		cidrs = append(cidrs, cidr)
	}
	return cidrs, nil
}

func parseCIDR(raw string) (*net.IPNet, error) {
	if !strings.Contains(raw, "/") {
		ip := net.ParseIP(raw)
		if ip == nil {
			return nil, fmt.Errorf("invalid IP %q", raw)
		}
		if ip.To4() != nil {
			raw += "/32"
		} else {
			raw += "/128"
		}
	}
	_, cidr, err := net.ParseCIDR(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid CIDR %q: %w", raw, err)
	}
	return cidr, nil
}
