package config

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"

	"github.com/Felix-LeeSM/flick-drop/internal/secrets"
	"github.com/Felix-LeeSM/flick-drop/internal/storage"
)

// Auth is always a static key pair: the AWS SDK cannot speak OCI instance principal.
type StorageBackend struct {
	Name string
	S3   storage.Config
	// 0 = unlimited, allowed only for a single backend.
	MaxTotalBytes int64
}

type Config struct {
	Env                string
	PublicBaseURL      string
	InternalToken      string
	MetricsToken       string
	OTLPEndpoint       string
	InternalAPIBaseURL string
	APIAddr            string
	APIDatabaseURL     string
	WorkerDatabaseURL  string
	WorkerMetricsAddr  string
	// Empty keeps every rate limiter in process memory.
	ValkeyAddr string
	// HMAC key that hashes client networks before they become Valkey keys.
	RateLimitKeySalt string
	// Same on every API replica: a sender's replica dials the receiver's on it.
	InternalHTTPPort uint16
	// Advertised in Valkey for the stream listener; 127.0.0.1 outside Kubernetes.
	PodIP                 netip.Addr
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
	PipeRelayEnabled      bool
	PipeMaxConcurrent     int
	TrustedProxies        []*net.IPNet
	// Empty disables object storage: every payload stays inline.
	StorageBackends []StorageBackend
}

func Load() (Config, error) {
	cfg := Config{
		Env:           getenv("FLICK_ENV", "development"),
		PublicBaseURL: getenv("FLICK_PUBLIC_BASE_URL", "http://localhost:5173"),
		InternalToken: getenv("FLICK_INTERNAL_TOKEN", ""),
		MetricsToken:  getenv("FLICK_METRICS_TOKEN", ""),
		// Full OTLP/HTTP URL; empty disables tracing.
		OTLPEndpoint:       getenv("FLICK_OTLP_ENDPOINT", ""),
		InternalAPIBaseURL: getenv("FLICK_INTERNAL_API_BASE_URL", "http://localhost:8080"),
		APIAddr:            getenv("FLICK_API_ADDR", ":8080"),
		APIDatabaseURL:     getenv("FLICK_DATABASE_URL", ""),
		WorkerDatabaseURL:  getenv("FLICK_WORKER_DATABASE_URL", ""),
		// An explicitly empty value disables the listener.
		WorkerMetricsAddr:     getenv("FLICK_WORKER_METRICS_ADDR", ":9091"),
		ValkeyAddr:            getenv("FLICK_VALKEY_ADDR", ""),
		RateLimitKeySalt:      getenv("FLICK_RATELIMIT_KEY_SALT", ""),
		NATSURL:               getenv("FLICK_NATS_URL", "nats://127.0.0.1:4222"),
		NATSStream:            getenv("FLICK_NATS_STREAM", "FLICK_JOBS"),
		NATSJobSubject:        getenv("FLICK_NATS_JOB_SUBJECT", "flick.jobs"),
		PayloadInlineMaxBytes: defaultPayloadInlineMaxBytes,
		MaxFileBytes:          defaultMaxFileBytes,
		DefaultTTLSeconds:     defaultDefaultTTLSeconds,
		MinTTLSeconds:         defaultMinTTLSeconds,
		MaxTTLSeconds:         defaultMaxTTLSeconds,
		OpenRatePerMinute:     defaultOpenRatePerMinute,
		// Presigned upload issuance is a DoS amplifier, so cap it per client network.
		CreateRatePerMinute:   defaultCreateRatePerMinute,
		ReaperIntervalSeconds: defaultReaperIntervalSeconds,
		ReaperBatchSize:       defaultReaperBatchSize,
		PipeRelayEnabled:      defaultPipeRelayEnabled,
		PipeMaxConcurrent:     defaultPipeMaxConcurrent,
	}

	var err error
	if cfg.StorageBackends, err = loadStorageBackends(); err != nil {
		return Config{}, err
	}
	if cfg.PayloadInlineMaxBytes, err = envPositiveInt64("FLICK_PAYLOAD_INLINE_MAX_BYTES", cfg.PayloadInlineMaxBytes); err != nil {
		return Config{}, err
	}
	if cfg.PayloadInlineMaxBytes < secrets.MaxCiphertextBytes(0) {
		return Config{}, fmt.Errorf("FLICK_PAYLOAD_INLINE_MAX_BYTES must be at least %d bytes (an empty age file)", secrets.MaxCiphertextBytes(0))
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
	if cfg.PipeRelayEnabled, err = envBool("FLICK_PIPE_RELAY_ENABLED", cfg.PipeRelayEnabled); err != nil {
		return Config{}, err
	}
	if cfg.PipeMaxConcurrent, err = envPositiveInt("FLICK_PIPE_MAX_CONCURRENT", cfg.PipeMaxConcurrent); err != nil {
		return Config{}, err
	}
	internalPort, err := envPositiveInt("FLICK_INTERNAL_HTTP_PORT", defaultInternalHTTPPort)
	if err != nil {
		return Config{}, err
	}
	if internalPort > 65535 {
		return Config{}, fmt.Errorf("FLICK_INTERNAL_HTTP_PORT must be a TCP port (1..65535)")
	}
	cfg.InternalHTTPPort = uint16(internalPort)
	cfg.PodIP = netip.AddrFrom4([4]byte{127, 0, 0, 1})
	if raw := os.Getenv("FLICK_POD_IP"); raw != "" {
		// An IP literal only: the peer replica dials exactly this address.
		if cfg.PodIP, err = netip.ParseAddr(raw); err != nil {
			return Config{}, fmt.Errorf("FLICK_POD_IP must be an IP address: %w", err)
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
	if cfg.PipeMaxConcurrent <= 0 {
		return Config{}, fmt.Errorf("FLICK_PIPE_MAX_CONCURRENT must be a positive integer")
	}
	if cfg.ValkeyAddr != "" && cfg.RateLimitKeySalt == "" {
		return Config{}, fmt.Errorf("FLICK_RATELIMIT_KEY_SALT is required when FLICK_VALKEY_ADDR is set")
	}
	return cfg, nil
}

var legacyStorageEnv = []string{
	"FLICK_STORAGE_LARGE_BACKEND",
	"FLICK_S3_ENDPOINT", "FLICK_S3_REGION", "FLICK_S3_BUCKET", "FLICK_S3_ACCESS_KEY_ID",
	"FLICK_S3_SECRET_ACCESS_KEY", "FLICK_S3_PATH_STYLE", "FLICK_S3_MAX_TOTAL_BYTES",
	"FLICK_R2_ENABLED", "FLICK_R2_ENDPOINT", "FLICK_R2_REGION", "FLICK_R2_BUCKET", "FLICK_R2_ACCESS_KEY_ID",
	"FLICK_R2_SECRET_ACCESS_KEY", "FLICK_R2_PATH_STYLE", "FLICK_R2_MAX_TOTAL_BYTES",
}

// A backend name is stored in rows and delete events, so it must satisfy storage.ValidBackendName.
func loadStorageBackends() ([]StorageBackend, error) {
	// Refuse to boot an unmigrated deployment with object storage silently off.
	for _, key := range legacyStorageEnv {
		if os.Getenv(key) != "" {
			return nil, fmt.Errorf("%s is no longer read; move it to FLICK_STORAGE_BACKENDS and FLICK_STORAGE_<NAME>_* (docs/runbook/storage-backend-rename.md)", key)
		}
	}
	raw := strings.TrimSpace(os.Getenv("FLICK_STORAGE_BACKENDS"))
	if raw == "" {
		return nil, nil
	}
	var names []string
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		name := strings.TrimSpace(part)
		if !storage.ValidBackendName(name) {
			return nil, fmt.Errorf("FLICK_STORAGE_BACKENDS: %q is not a backend name (lower-case letter, then up to 15 lower-case letters or digits)", name)
		}
		if seen[name] {
			return nil, fmt.Errorf("FLICK_STORAGE_BACKENDS names %q twice", name)
		}
		seen[name] = true
		names = append(names, name)
	}
	var backends []StorageBackend
	for _, name := range names {
		prefix := "FLICK_STORAGE_" + strings.ToUpper(name) + "_"
		b := StorageBackend{
			Name: name,
			S3: storage.Config{
				Enabled:         true,
				Endpoint:        getenv(prefix+"ENDPOINT", ""),
				Region:          getenv(prefix+"REGION", "us-east-1"),
				Bucket:          getenv(prefix+"BUCKET", ""),
				AccessKeyID:     getenv(prefix+"ACCESS_KEY_ID", ""),
				SecretAccessKey: getenv(prefix+"SECRET_ACCESS_KEY", ""),
			},
		}
		var err error
		// MinIO, OCI S3-compat, and R2 all accept path-style addressing.
		if b.S3.PathStyle, err = envBool(prefix+"PATH_STYLE", true); err != nil {
			return nil, err
		}
		if b.MaxTotalBytes, err = envNonNegativeInt64(prefix+"MAX_TOTAL_BYTES", 0); err != nil {
			return nil, err
		}
		if b.S3.Bucket == "" {
			return nil, fmt.Errorf("%sBUCKET is required for storage backend %q", prefix, name)
		}
		if b.S3.AccessKeyID == "" || b.S3.SecretAccessKey == "" {
			return nil, fmt.Errorf("%sACCESS_KEY_ID and %sSECRET_ACCESS_KEY are required for storage backend %q", prefix, prefix, name)
		}
		backends = append(backends, b)
	}
	if len(backends) > 1 {
		for _, b := range backends {
			if b.MaxTotalBytes == 0 {
				return nil, fmt.Errorf("FLICK_STORAGE_%s_MAX_TOTAL_BYTES must be positive when several storage backends are configured (placement weighs each backend's remaining space)", strings.ToUpper(b.Name))
			}
		}
	}
	return backends, nil
}

// FLICK_PAYLOAD_INLINE_MAX_BYTES bounds the age file, so inline plaintext is smaller by the age overhead.
func (c Config) EffectivePayloadInlineMaxBytes() int64 {
	return secrets.MaxPlaintextBytes(c.PayloadInlineMaxBytes)
}

// Without object storage the inline path is the only route, so the ceiling drops to the
// inline threshold; advertising MaxFileBytes would answer 413 just past 1 MiB.
func (c Config) EffectiveMaxFileBytes() int64 {
	if inline := c.EffectivePayloadInlineMaxBytes(); len(c.StorageBackends) == 0 && c.MaxFileBytes > inline {
		return inline
	}
	return c.MaxFileBytes
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

// 0 means "unlimited" for storage ceilings.
func parseNonNegativeInt64(name, raw string) (int64, error) {
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer", name)
	}
	return value, nil
}

func envNonNegativeInt64(name string, cur int64) (int64, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return cur, nil
	}
	return parseNonNegativeInt64(name, raw)
}

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

// A bare IP gets an implicit /32 (v4) or /128 (v6).
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
