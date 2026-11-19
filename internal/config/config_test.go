package config

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Felix-LeeSM/flick-drop/internal/secrets"
)

func TestLoadUsesDefaults(t *testing.T) {
	clearFlickEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.APIAddr != ":8080" {
		t.Fatalf("APIAddr = %q, want :8080", cfg.APIAddr)
	}
	if cfg.PublicBaseURL != "http://localhost:5173" {
		t.Fatalf("PublicBaseURL = %q, want http://localhost:5173", cfg.PublicBaseURL)
	}
	if cfg.InternalToken != "" {
		t.Fatalf("InternalToken = %q, want empty default", cfg.InternalToken)
	}
	if cfg.MetricsToken != "" {
		t.Fatalf("MetricsToken = %q, want empty default", cfg.MetricsToken)
	}
	if cfg.OTLPEndpoint != "" {
		t.Fatalf("OTLPEndpoint = %q, want empty default (tracing off)", cfg.OTLPEndpoint)
	}
	if cfg.InternalAPIBaseURL != "http://localhost:8080" {
		t.Fatalf("InternalAPIBaseURL = %q, want http://localhost:8080", cfg.InternalAPIBaseURL)
	}
	if cfg.APIDatabaseURL != "" {
		t.Fatalf("APIDatabaseURL = %q, want empty default", cfg.APIDatabaseURL)
	}
	if cfg.WorkerDatabaseURL != "" {
		t.Fatalf("WorkerDatabaseURL = %q, want empty default", cfg.WorkerDatabaseURL)
	}
	if cfg.NATSURL != "nats://127.0.0.1:4222" {
		t.Fatalf("NATSURL = %q, want nats://127.0.0.1:4222", cfg.NATSURL)
	}
	if cfg.NATSStream != "FLICK_JOBS" {
		t.Fatalf("NATSStream = %q, want FLICK_JOBS", cfg.NATSStream)
	}
	if cfg.NATSJobSubject != "flick.jobs" {
		t.Fatalf("NATSJobSubject = %q, want flick.jobs", cfg.NATSJobSubject)
	}
	if cfg.PayloadInlineMaxBytes != 1048576 {
		t.Fatalf("PayloadInlineMaxBytes = %d, want 1048576", cfg.PayloadInlineMaxBytes)
	}
	if cfg.MaxFileBytes != 268435456 {
		t.Fatalf("MaxFileBytes = %d, want 268435456", cfg.MaxFileBytes)
	}
	if cfg.DefaultTTLSeconds != 3600 {
		t.Fatalf("DefaultTTLSeconds = %d, want 3600", cfg.DefaultTTLSeconds)
	}
	if cfg.MinTTLSeconds != 300 {
		t.Fatalf("MinTTLSeconds = %d, want 300", cfg.MinTTLSeconds)
	}
	if cfg.MaxTTLSeconds != 604800 {
		t.Fatalf("MaxTTLSeconds = %d, want 604800", cfg.MaxTTLSeconds)
	}
	if cfg.OpenRatePerMinute != 10 {
		t.Fatalf("OpenRatePerMinute = %d, want 10", cfg.OpenRatePerMinute)
	}
	if cfg.CreateRatePerMinute != 5 {
		t.Fatalf("CreateRatePerMinute = %d, want 5", cfg.CreateRatePerMinute)
	}
	if !cfg.PipeRelayEnabled {
		t.Fatalf("PipeRelayEnabled = false, want true by default")
	}
	if cfg.PipeMaxConcurrent != 100 {
		t.Fatalf("PipeMaxConcurrent = %d, want 100", cfg.PipeMaxConcurrent)
	}
	if len(cfg.StorageBackends) != 0 {
		t.Fatalf("StorageBackends = %v, want none by default", cfg.StorageBackends)
	}
	if len(cfg.TrustedProxies) != 0 {
		t.Fatalf("TrustedProxies = %v, want empty by default", cfg.TrustedProxies)
	}
}

func TestLoadRejectsDefaultTTLBelowRange(t *testing.T) {
	clearFlickEnv(t)
	t.Setenv("FLICK_DEFAULT_TTL_SECONDS", "60") // below the 300s floor

	if _, err := Load(); err == nil {
		t.Fatal("expected config load error for default below min ttl")
	}
}

func TestLoadRejectsDefaultTTLAboveRange(t *testing.T) {
	clearFlickEnv(t)
	t.Setenv("FLICK_DEFAULT_TTL_SECONDS", "999999") // above the 604800s ceiling
	t.Setenv("FLICK_MAX_TTL_SECONDS", "604800")

	if _, err := Load(); err == nil {
		t.Fatal("expected config load error for default above max ttl")
	}
}

// Valkey 키는 솔트로 해시한 값만 담아야 하므로, 주소만 있고 솔트가 없으면 부팅하지 않는다.
func TestLoadRequiresRateLimitSaltWithValkey(t *testing.T) {
	clearFlickEnv(t)
	t.Setenv("FLICK_VALKEY_ADDR", "127.0.0.1:6379")

	if _, err := Load(); err == nil {
		t.Fatal("expected config load error for FLICK_VALKEY_ADDR without FLICK_RATELIMIT_KEY_SALT")
	}

	t.Setenv("FLICK_RATELIMIT_KEY_SALT", "deployment-salt")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load with salt: %v", err)
	}
	if cfg.ValkeyAddr != "127.0.0.1:6379" || cfg.RateLimitKeySalt != "deployment-salt" {
		t.Fatalf("valkey config = %q / %q", cfg.ValkeyAddr, cfg.RateLimitKeySalt)
	}
}

func clearFlickEnv(t *testing.T) {
	t.Helper()

	keys := []string{
		"FLICK_ENV",
		"FLICK_PUBLIC_BASE_URL",
		"FLICK_INTERNAL_TOKEN",
		"FLICK_METRICS_TOKEN",
		"FLICK_OTLP_ENDPOINT",
		"FLICK_INTERNAL_API_BASE_URL",
		"FLICK_API_ADDR",
		"FLICK_DATABASE_URL",
		"FLICK_WORKER_DATABASE_URL",
		"FLICK_VALKEY_ADDR",
		"FLICK_RATELIMIT_KEY_SALT",
		"FLICK_INTERNAL_HTTP_PORT",
		"FLICK_POD_IP",
		"FLICK_NATS_URL",
		"FLICK_NATS_STREAM",
		"FLICK_NATS_JOB_SUBJECT",
		"FLICK_PAYLOAD_INLINE_MAX_BYTES",
		"FLICK_MAX_FILE_BYTES",
		"FLICK_DEFAULT_TTL_SECONDS",
		"FLICK_MIN_TTL_SECONDS",
		"FLICK_MAX_TTL_SECONDS",
		"FLICK_OPEN_RATE_PER_MIN",
		"FLICK_CREATE_RATE_PER_MIN",
		"FLICK_PIPE_RELAY_ENABLED",
		"FLICK_PIPE_MAX_CONCURRENT",
		"FLICK_TRUSTED_PROXIES",
		"FLICK_STORAGE_BACKENDS",
	}
	keys = append(keys, legacyStorageEnv...)
	for _, key := range keys {
		t.Setenv(key, "")
	}
}

// TestLoadEnvOverrides pins each int/int64 env var to a distinct in-range value
// and asserts it lands in the right field. A copy-paste env-name typo in Load
// (or a missing mapping) makes at least one assertion flip.
func TestLoadEnvOverrides(t *testing.T) {
	clearFlickEnv(t)
	t.Setenv("FLICK_PAYLOAD_INLINE_MAX_BYTES", "8192")
	t.Setenv("FLICK_MAX_FILE_BYTES", "4096")
	// TTLs satisfy cross-validation: min <= default <= max.
	t.Setenv("FLICK_MIN_TTL_SECONDS", "60")
	t.Setenv("FLICK_DEFAULT_TTL_SECONDS", "120")
	t.Setenv("FLICK_MAX_TTL_SECONDS", "600")
	t.Setenv("FLICK_OPEN_RATE_PER_MIN", "7")
	t.Setenv("FLICK_CREATE_RATE_PER_MIN", "3")
	t.Setenv("FLICK_REAPER_INTERVAL_SECONDS", "30")
	t.Setenv("FLICK_REAPER_BATCH_SIZE", "11")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"PayloadInlineMaxBytes", cfg.PayloadInlineMaxBytes, int64(8192)},
		{"MaxFileBytes", cfg.MaxFileBytes, int64(4096)},
		{"MinTTLSeconds", cfg.MinTTLSeconds, 60},
		{"DefaultTTLSeconds", cfg.DefaultTTLSeconds, 120},
		{"MaxTTLSeconds", cfg.MaxTTLSeconds, 600},
		{"OpenRatePerMinute", cfg.OpenRatePerMinute, 7},
		{"CreateRatePerMinute", cfg.CreateRatePerMinute, 3},
		{"ReaperIntervalSeconds", cfg.ReaperIntervalSeconds, 30},
		{"ReaperBatchSize", cfg.ReaperBatchSize, 11},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

// TestEffectiveMaxFileBytes pins the advertised ceiling to what the server can
// actually store: the inline threshold while large-object storage is off, the
// configured limit once it is on.
func TestEffectiveMaxFileBytes(t *testing.T) {
	cases := []struct {
		name    string
		enabled bool
		inline  int64
		max     int64
		want    int64
	}{
		// The clamp leaves room for the age overhead: a 1 MiB age file holds
		// at most 1 MiB minus the 4096-byte header allowance, the 16-byte
		// payload nonce, and sixteen 16-byte chunk tags.
		{"large storage off clamps below the inline threshold", false, 1048576, 268435456, 1044208},
		{"large storage on keeps the configured limit", true, 1048576, 268435456, 268435456},
		{"limit already below the clamp is left alone", false, 1048576, 4096, 4096},
		{"limit inside the age margin is clamped", false, 1048576, 1048570, 1044208},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := Config{
				PayloadInlineMaxBytes: c.inline,
				MaxFileBytes:          c.max,
				StorageBackends:       storageIf(c.enabled),
			}
			if got := cfg.EffectiveMaxFileBytes(); got != c.want {
				t.Fatalf("EffectiveMaxFileBytes() = %d, want %d", got, c.want)
			}
		})
	}
}

// TestLoadInlineThresholdFitsEmptyAgeFile pins the lower bound: the inline
// threshold must hold at least an empty age file, and at exactly that size the
// advertised inline plaintext is zero bytes.
func TestLoadInlineThresholdFitsEmptyAgeFile(t *testing.T) {
	empty := secrets.MaxCiphertextBytes(0)
	for _, threshold := range []int64{empty - 1, empty, empty + 1} {
		t.Run(fmt.Sprint(threshold), func(t *testing.T) {
			clearFlickEnv(t)
			t.Setenv("FLICK_PAYLOAD_INLINE_MAX_BYTES", fmt.Sprint(threshold))
			cfg, err := Load()
			if threshold < empty {
				want := fmt.Sprintf("FLICK_PAYLOAD_INLINE_MAX_BYTES must be at least %d bytes (an empty age file)", empty)
				if err == nil || err.Error() != want {
					t.Fatalf("Load() error = %v, want %q", err, want)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load(): %v", err)
			}
			if got, want := cfg.EffectivePayloadInlineMaxBytes(), threshold-empty; got != want {
				t.Fatalf("EffectivePayloadInlineMaxBytes() = %d, want %d", got, want)
			}
		})
	}
}

func TestLoadPipeRelayConfig(t *testing.T) {
	t.Run("custom valid config", func(t *testing.T) {
		clearFlickEnv(t)
		t.Setenv("FLICK_PIPE_RELAY_ENABLED", "false")
		t.Setenv("FLICK_PIPE_MAX_CONCURRENT", "50")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load(): %v", err)
		}
		if cfg.PipeRelayEnabled {
			t.Fatalf("PipeRelayEnabled = true, want false")
		}
		if cfg.PipeMaxConcurrent != 50 {
			t.Fatalf("PipeMaxConcurrent = %d, want 50", cfg.PipeMaxConcurrent)
		}
	})

	t.Run("invalid boolean", func(t *testing.T) {
		clearFlickEnv(t)
		t.Setenv("FLICK_PIPE_RELAY_ENABLED", "not-a-bool")

		if _, err := Load(); err == nil {
			t.Fatal("expected error for invalid FLICK_PIPE_RELAY_ENABLED")
		}
	})

	t.Run("invalid max concurrent", func(t *testing.T) {
		clearFlickEnv(t)
		t.Setenv("FLICK_PIPE_MAX_CONCURRENT", "0")

		if _, err := Load(); err == nil {
			t.Fatal("expected error for zero FLICK_PIPE_MAX_CONCURRENT")
		}
	})

	// 쿠버네티스 밖(단일 호스트)에서는 스트림 리스너를 127.0.0.1:8081로 광고한다.
	t.Run("stream listener defaults", func(t *testing.T) {
		clearFlickEnv(t)

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load(): %v", err)
		}
		if cfg.InternalHTTPPort != 8081 || cfg.PodIP.String() != "127.0.0.1" {
			t.Fatalf("stream listener = %s:%d, want 127.0.0.1:8081", cfg.PodIP, cfg.InternalHTTPPort)
		}
	})

	t.Run("pod IP and port overrides", func(t *testing.T) {
		clearFlickEnv(t)
		t.Setenv("FLICK_POD_IP", "fd00::12")
		t.Setenv("FLICK_INTERNAL_HTTP_PORT", "9081")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load(): %v", err)
		}
		if cfg.InternalHTTPPort != 9081 || cfg.PodIP.String() != "fd00::12" {
			t.Fatalf("stream listener = %s:%d", cfg.PodIP, cfg.InternalHTTPPort)
		}
	})

	// 피어 레플리카가 이 값을 그대로 다이얼하므로 호스트 이름이나 범위 밖 포트는 기동에서 막는다.
	for name, env := range map[string][2]string{
		"pod IP host name": {"FLICK_POD_IP", "api.internal"},
		"port over 65535":  {"FLICK_INTERNAL_HTTP_PORT", "70000"},
		"port zero":        {"FLICK_INTERNAL_HTTP_PORT", "0"},
	} {
		t.Run(name, func(t *testing.T) {
			clearFlickEnv(t)
			t.Setenv(env[0], env[1])
			if _, err := Load(); err == nil {
				t.Fatalf("expected error for %s=%s", env[0], env[1])
			}
		})
	}
}

func storageIf(enabled bool) []StorageBackend {
	if !enabled {
		return nil
	}
	return []StorageBackend{{Name: "minio"}}
}

// storageEnv sets one backend's required FLICK_STORAGE_<NAME>_* values.
func storageEnv(t *testing.T, name, bucket string) {
	t.Helper()
	prefix := "FLICK_STORAGE_" + strings.ToUpper(name) + "_"
	t.Setenv(prefix+"BUCKET", bucket)
	t.Setenv(prefix+"ACCESS_KEY_ID", name+"-key")
	t.Setenv(prefix+"SECRET_ACCESS_KEY", name+"-secret")
}

// 이름 목록 순서대로 백엔드별 env를 읽고, 생략한 값은 기본값을 쓴다.
func TestLoadStorageBackendsReadsEachNamedBackend(t *testing.T) {
	// Given: 백엔드 둘과 각자의 설정
	clearFlickEnv(t)
	t.Setenv("FLICK_STORAGE_BACKENDS", " oci , r2 ")
	storageEnv(t, "oci", "flick-oci")
	storageEnv(t, "r2", "flick-r2")
	t.Setenv("FLICK_STORAGE_OCI_ENDPOINT", "https://ns.compat.objectstorage.example")
	t.Setenv("FLICK_STORAGE_OCI_MAX_TOTAL_BYTES", "18000000000")
	t.Setenv("FLICK_STORAGE_R2_ENDPOINT", "https://acct.r2.example")
	t.Setenv("FLICK_STORAGE_R2_REGION", "auto")
	t.Setenv("FLICK_STORAGE_R2_PATH_STYLE", "false")
	t.Setenv("FLICK_STORAGE_R2_MAX_TOTAL_BYTES", "9000000000")

	// When
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	// Then: 순서·이름·버킷·키·엔드포인트·리전·경로 방식·상한이 백엔드마다 맞다
	if len(cfg.StorageBackends) != 2 {
		t.Fatalf("StorageBackends = %+v, want two", cfg.StorageBackends)
	}
	oci, r2 := cfg.StorageBackends[0], cfg.StorageBackends[1]
	if oci.Name != "oci" || oci.S3.Bucket != "flick-oci" || oci.S3.AccessKeyID != "oci-key" || oci.S3.SecretAccessKey != "oci-secret" ||
		oci.S3.Endpoint != "https://ns.compat.objectstorage.example" || oci.S3.Region != "us-east-1" || !oci.S3.PathStyle || oci.MaxTotalBytes != 18000000000 {
		t.Fatalf("oci backend = %+v", oci)
	}
	if r2.Name != "r2" || r2.S3.Bucket != "flick-r2" || r2.S3.Endpoint != "https://acct.r2.example" || r2.S3.Region != "auto" ||
		r2.S3.PathStyle || r2.MaxTotalBytes != 9000000000 {
		t.Fatalf("r2 backend = %+v", r2)
	}
}

// 백엔드가 하나면 상한을 생략해도 되고(무제한), 여러 개면 모두 상한이 있어야 한다.
func TestLoadStorageBackendsCeilingRule(t *testing.T) {
	clearFlickEnv(t)
	t.Setenv("FLICK_STORAGE_BACKENDS", "minio")
	storageEnv(t, "minio", "flick-dev")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("single uncapped backend: %v", err)
	}
	if cfg.StorageBackends[0].MaxTotalBytes != 0 {
		t.Fatalf("MaxTotalBytes = %d, want 0 (unlimited)", cfg.StorageBackends[0].MaxTotalBytes)
	}

	t.Setenv("FLICK_STORAGE_BACKENDS", "minio,spare")
	storageEnv(t, "spare", "flick-spare")
	t.Setenv("FLICK_STORAGE_MINIO_MAX_TOTAL_BYTES", "1000")
	_, err = Load()
	if err == nil || !strings.Contains(err.Error(), "FLICK_STORAGE_SPARE_MAX_TOTAL_BYTES") {
		t.Fatalf("two backends, one uncapped: err = %v, want it to name FLICK_STORAGE_SPARE_MAX_TOTAL_BYTES", err)
	}
}

func TestLoadStorageBackendsRejectsBadValues(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"legacy underscore name", map[string]string{"FLICK_STORAGE_BACKENDS": "s3_object"}, "not a backend name"},
		{"upper-case name", map[string]string{"FLICK_STORAGE_BACKENDS": "OCI"}, "not a backend name"},
		{"empty entry", map[string]string{"FLICK_STORAGE_BACKENDS": "oci,"}, "not a backend name"},
		{"duplicate name", map[string]string{"FLICK_STORAGE_BACKENDS": "oci,oci"}, "twice"},
		{"missing bucket", map[string]string{"FLICK_STORAGE_BACKENDS": "oci", "FLICK_STORAGE_OCI_ACCESS_KEY_ID": "k", "FLICK_STORAGE_OCI_SECRET_ACCESS_KEY": "s"}, "FLICK_STORAGE_OCI_BUCKET"},
		{"missing keys", map[string]string{"FLICK_STORAGE_BACKENDS": "oci", "FLICK_STORAGE_OCI_BUCKET": "b"}, "FLICK_STORAGE_OCI_ACCESS_KEY_ID"},
		{"malformed path style", map[string]string{"FLICK_STORAGE_BACKENDS": "oci", "FLICK_STORAGE_OCI_BUCKET": "b", "FLICK_STORAGE_OCI_ACCESS_KEY_ID": "k", "FLICK_STORAGE_OCI_SECRET_ACCESS_KEY": "s", "FLICK_STORAGE_OCI_PATH_STYLE": "flase"}, "FLICK_STORAGE_OCI_PATH_STYLE"},
		{"negative ceiling", map[string]string{"FLICK_STORAGE_BACKENDS": "oci", "FLICK_STORAGE_OCI_BUCKET": "b", "FLICK_STORAGE_OCI_ACCESS_KEY_ID": "k", "FLICK_STORAGE_OCI_SECRET_ACCESS_KEY": "s", "FLICK_STORAGE_OCI_MAX_TOTAL_BYTES": "-1"}, "FLICK_STORAGE_OCI_MAX_TOTAL_BYTES"},
		// 옛 변수만 남은 배포가 저장소 없이 조용히 뜨지 않는다.
		{"legacy primary variable", map[string]string{"FLICK_S3_BUCKET": "b"}, "FLICK_S3_BUCKET is no longer read"},
		{"legacy secondary variable", map[string]string{"FLICK_R2_ENABLED": "true"}, "FLICK_R2_ENABLED is no longer read"},
		{"legacy backend switch", map[string]string{"FLICK_STORAGE_LARGE_BACKEND": "disabled"}, "FLICK_STORAGE_LARGE_BACKEND is no longer read"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clearFlickEnv(t)
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to mention %q", err, c.want)
			}
		})
	}
}
