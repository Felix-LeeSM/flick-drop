package config

import "testing"

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
	if cfg.InternalAPIBaseURL != "http://localhost:8080" {
		t.Fatalf("InternalAPIBaseURL = %q, want http://localhost:8080", cfg.InternalAPIBaseURL)
	}
	if cfg.APIDBPath != "./var/api.db" {
		t.Fatalf("APIDBPath = %q, want ./var/api.db", cfg.APIDBPath)
	}
	if cfg.WorkerDBPath != "./var/worker.db" {
		t.Fatalf("WorkerDBPath = %q, want ./var/worker.db", cfg.WorkerDBPath)
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
	if cfg.MaxFileBytes != 52428800 {
		t.Fatalf("MaxFileBytes = %d, want 52428800", cfg.MaxFileBytes)
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
	if cfg.S3.Enabled {
		t.Fatalf("S3.Enabled = true, want false by default")
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

func clearFlickEnv(t *testing.T) {
	t.Helper()

	keys := []string{
		"FLICK_ENV",
		"FLICK_PUBLIC_BASE_URL",
		"FLICK_INTERNAL_TOKEN",
		"FLICK_INTERNAL_API_BASE_URL",
		"FLICK_API_ADDR",
		"FLICK_API_DB_PATH",
		"FLICK_WORKER_DB_PATH",
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
		"FLICK_TRUSTED_PROXIES",
		"FLICK_STORAGE_LARGE_BACKEND",
		"FLICK_S3_ENDPOINT",
		"FLICK_S3_REGION",
		"FLICK_S3_BUCKET",
		"FLICK_S3_ACCESS_KEY_ID",
		"FLICK_S3_SECRET_ACCESS_KEY",
		"FLICK_S3_PATH_STYLE",
	}
	for _, key := range keys {
		t.Setenv(key, "")
	}
}

func TestLoadS3PathStyle(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", true},
		{"true", true},
		{"True", true},
		{"TRUE", true},
		{"  true ", true},
		{"false", false},
		{"False", false},
		{"FALSE", false},
		{"  false ", false},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			clearFlickEnv(t)
			if c.in != "" {
				t.Setenv("FLICK_S3_PATH_STYLE", c.in)
			}
			cfg, err := Load()
			if err != nil {
				t.Fatalf("FLICK_S3_PATH_STYLE=%q: %v", c.in, err)
			}
			if cfg.S3.PathStyle != c.want {
				t.Fatalf("FLICK_S3_PATH_STYLE=%q: PathStyle=%v, want %v", c.in, cfg.S3.PathStyle, c.want)
			}
		})
	}
}

func TestLoadS3PathStyleRejectsGarbage(t *testing.T) {
	clearFlickEnv(t)
	t.Setenv("FLICK_S3_PATH_STYLE", "flase")

	if _, err := Load(); err == nil {
		t.Fatal("expected config load error for malformed FLICK_S3_PATH_STYLE")
	}
}

// TestLoadEnvOverrides pins each int/int64 env var to a distinct in-range value
// and asserts it lands in the right field. A copy-paste env-name typo in Load
// (or a missing mapping) makes at least one assertion flip.
func TestLoadEnvOverrides(t *testing.T) {
	clearFlickEnv(t)
	t.Setenv("FLICK_PAYLOAD_INLINE_MAX_BYTES", "2048")
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
		{"PayloadInlineMaxBytes", cfg.PayloadInlineMaxBytes, int64(2048)},
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
