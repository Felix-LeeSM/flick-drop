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
		PayloadInlineMaxBytes: 1048576,
		MaxFileBytes:          52428800,
		DefaultTTLSeconds:     3600,
		// TTL is a continuous range (5 minutes .. 7 days) so the browser can
		// offer an editable "custom" lifetime, not just fixed presets.
		MinTTLSeconds:     300,
		MaxTTLSeconds:     604800,
		OpenRatePerMinute: 10,
		// /api/secrets (presigned POST issuance) becomes a DoS amplifier once
		// large uploads bypass the server, so cap issuance per client IP + path.
		CreateRatePerMinute: 5,
		// The expiration reaper sweeps expired/orphan secrets out of api.db. A
		// minute is frequent enough that rows never linger; the partial index
		// keeps the claim cheap even at high volume.
		ReaperIntervalSeconds: 60,
		ReaperBatchSize:       50,
		S3: S3Config{
			Enabled:         getenv("FLICK_STORAGE_LARGE_BACKEND", "disabled") == "s3",
			Endpoint:        getenv("FLICK_S3_ENDPOINT", ""),
			Region:          getenv("FLICK_S3_REGION", "us-east-1"),
			Bucket:          getenv("FLICK_S3_BUCKET", ""),
			AccessKeyID:     getenv("FLICK_S3_ACCESS_KEY_ID", ""),
			SecretAccessKey: getenv("FLICK_S3_SECRET_ACCESS_KEY", ""),
			// MinIO and OCI S3-compat both use path-style addressing.
			PathStyle: getenv("FLICK_S3_PATH_STYLE", "true") != "false",
		},
	}

	var err error
	if raw := os.Getenv("FLICK_PAYLOAD_INLINE_MAX_BYTES"); raw != "" {
		cfg.PayloadInlineMaxBytes, err = parsePositiveInt64("FLICK_PAYLOAD_INLINE_MAX_BYTES", raw)
		if err != nil {
			return Config{}, err
		}
	}
	if raw := os.Getenv("FLICK_MAX_FILE_BYTES"); raw != "" {
		cfg.MaxFileBytes, err = parsePositiveInt64("FLICK_MAX_FILE_BYTES", raw)
		if err != nil {
			return Config{}, err
		}
	}
	if raw := os.Getenv("FLICK_DEFAULT_TTL_SECONDS"); raw != "" {
		cfg.DefaultTTLSeconds, err = parsePositiveInt("FLICK_DEFAULT_TTL_SECONDS", raw)
		if err != nil {
			return Config{}, err
		}
	}
	if raw := os.Getenv("FLICK_MIN_TTL_SECONDS"); raw != "" {
		cfg.MinTTLSeconds, err = parsePositiveInt("FLICK_MIN_TTL_SECONDS", raw)
		if err != nil {
			return Config{}, err
		}
	}
	if raw := os.Getenv("FLICK_MAX_TTL_SECONDS"); raw != "" {
		cfg.MaxTTLSeconds, err = parsePositiveInt("FLICK_MAX_TTL_SECONDS", raw)
		if err != nil {
			return Config{}, err
		}
	}
	if raw := os.Getenv("FLICK_OPEN_RATE_PER_MIN"); raw != "" {
		cfg.OpenRatePerMinute, err = parsePositiveInt("FLICK_OPEN_RATE_PER_MIN", raw)
		if err != nil {
			return Config{}, err
		}
	}
	if raw := os.Getenv("FLICK_CREATE_RATE_PER_MIN"); raw != "" {
		cfg.CreateRatePerMinute, err = parsePositiveInt("FLICK_CREATE_RATE_PER_MIN", raw)
		if err != nil {
			return Config{}, err
		}
	}
	if raw := os.Getenv("FLICK_REAPER_INTERVAL_SECONDS"); raw != "" {
		cfg.ReaperIntervalSeconds, err = parsePositiveInt("FLICK_REAPER_INTERVAL_SECONDS", raw)
		if err != nil {
			return Config{}, err
		}
	}
	if raw := os.Getenv("FLICK_REAPER_BATCH_SIZE"); raw != "" {
		cfg.ReaperBatchSize, err = parsePositiveInt("FLICK_REAPER_BATCH_SIZE", raw)
		if err != nil {
			return Config{}, err
		}
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
