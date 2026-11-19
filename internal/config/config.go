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

// StorageBackend is one S3-compatible object backend named in
// FLICK_STORAGE_BACKENDS. Its settings come from FLICK_STORAGE_<NAME>_* where
// <NAME> is the upper-cased name. Auth is always a static key pair because the
// AWS SDK cannot speak OCI instance principal directly.
type StorageBackend struct {
	Name string
	S3   storage.Config
	// MaxTotalBytes caps the cumulative object bytes placed on this backend
	// (0 = unlimited, allowed only for a single backend); see storage-model.md.
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
	// APIDatabaseURL is the PostgreSQL DSN for the flick_api logical database
	// (FLICK_DATABASE_URL, flick_api role). M15 PR 3 moved the API store to
	// Postgres; the api entrypoint fails fast when it is empty.
	APIDatabaseURL string
	// WorkerDatabaseURL is the PostgreSQL DSN for the flick_worker logical
	// database (FLICK_WORKER_DATABASE_URL, flick_worker role). M15 PR 2 moved
	// the worker store to Postgres; the worker entrypoint fails fast when it
	// is empty.
	WorkerDatabaseURL string
	// WorkerMetricsAddr is the worker's dedicated bearer-gated /metrics listen
	// address (FLICK_WORKER_METRICS_ADDR). Empty disables the listener.
	WorkerMetricsAddr string
	// ValkeyAddr is the Valkey host:port shared by API replicas for rate
	// limiting (FLICK_VALKEY_ADDR). Empty keeps every limiter in process memory.
	ValkeyAddr string
	// RateLimitKeySalt is the HMAC-SHA-256 key that hashes client networks
	// before they become Valkey keys (FLICK_RATELIMIT_KEY_SALT). Required when
	// ValkeyAddr is set.
	RateLimitKeySalt string
	// InternalHTTPPort is the live-transfer stream listener port, the same on
	// every API replica (FLICK_INTERNAL_HTTP_PORT, default 8081). A sender's
	// replica dials the receiver's replica on it.
	InternalHTTPPort uint16
	// PodIP is the IP this replica advertises in Valkey for the stream
	// listener (FLICK_POD_IP from the Kubernetes Downward API). Empty means a
	// single host outside Kubernetes and resolves to 127.0.0.1.
	PodIP          netip.Addr
	NATSURL        string
	NATSStream     string
	NATSJobSubject string
	// PayloadInlineMaxBytes bounds one encrypted secret; MaxFileBytes bounds the
	// file the browser may pick.
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
	// StorageBackends lists the object backends in FLICK_STORAGE_BACKENDS
	// order. Empty disables object storage: every payload stays inline.
	StorageBackends []StorageBackend
}

func Load() (Config, error) {
	cfg := Config{
		Env:           getenv("FLICK_ENV", "development"),
		PublicBaseURL: getenv("FLICK_PUBLIC_BASE_URL", "http://localhost:5173"),
		InternalToken: getenv("FLICK_INTERNAL_TOKEN", ""),
		MetricsToken:  getenv("FLICK_METRICS_TOKEN", ""),
		// Empty disables distributed tracing (no collector dependency); set to a
		// full OTLP/HTTP URL (e.g. http://otel-collector:4318) to enable it.
		OTLPEndpoint:       getenv("FLICK_OTLP_ENDPOINT", ""),
		InternalAPIBaseURL: getenv("FLICK_INTERNAL_API_BASE_URL", "http://localhost:8080"),
		APIAddr:            getenv("FLICK_API_ADDR", ":8080"),
		APIDatabaseURL:     getenv("FLICK_DATABASE_URL", ""),
		WorkerDatabaseURL:  getenv("FLICK_WORKER_DATABASE_URL", ""),
		// An explicitly empty value disables the worker metrics listener; the
		// deployment default binds :9091 (bearer-gated by FLICK_METRICS_TOKEN).
		WorkerMetricsAddr:     getenv("FLICK_WORKER_METRICS_ADDR", ":9091"),
		ValkeyAddr:            getenv("FLICK_VALKEY_ADDR", ""),
		RateLimitKeySalt:      getenv("FLICK_RATELIMIT_KEY_SALT", ""),
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
		// /api/secrets (presigned upload issuance) becomes a DoS amplifier once
		// large uploads bypass the server, so cap issuance per client network.
		CreateRatePerMinute: defaultCreateRatePerMinute,
		// The expiration reaper sweeps expired/orphan secrets out of the flick_api database. A
		// minute is frequent enough that rows never linger; the partial index
		// keeps the claim cheap even at high volume.
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

// loadStorageBackends reads FLICK_STORAGE_BACKENDS (comma-separated names) and
// each named backend's FLICK_STORAGE_<NAME>_* settings. A name must satisfy
// storage.ValidBackendName because it is stored in rows and delete events.
func loadStorageBackends() ([]StorageBackend, error) {
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

// EffectivePayloadInlineMaxBytes is the largest plaintext that still fits the
// inline SQLite path once encrypted. FLICK_PAYLOAD_INLINE_MAX_BYTES bounds the
// age file, so the plaintext the client may route inline is smaller by the age
// header allowance, payload nonce, and chunk tags.
func (c Config) EffectivePayloadInlineMaxBytes() int64 {
	return secrets.MaxPlaintextBytes(c.PayloadInlineMaxBytes)
}

// EffectiveMaxFileBytes is the largest plaintext this deployment will actually
// accept. With large-object storage disabled the inline path is the only route,
// so the ceiling drops to the inline threshold: advertising the full
// MaxFileBytes there would promise it and then answer 413 just past 1 MiB.
// With it enabled the configured limit stands, because the object cap is
// derived from MaxFileBytes plus the same age overhead.
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

// parseNonNegativeInt64 accepts 0 (documented as "unlimited" for the cumulative
// storage ceilings) but rejects negatives, which are a config typo.
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
