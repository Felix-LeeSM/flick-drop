package config

import "testing"

func TestLoadUsesDefaults(t *testing.T) {
	clearBurnLinkEnv(t)

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
	if cfg.APIDBPath != "./var/api.db" {
		t.Fatalf("APIDBPath = %q, want ./var/api.db", cfg.APIDBPath)
	}
	if cfg.NATSURL != "nats://127.0.0.1:4222" {
		t.Fatalf("NATSURL = %q, want nats://127.0.0.1:4222", cfg.NATSURL)
	}
	if cfg.NATSStream != "BURNLINK_JOBS" {
		t.Fatalf("NATSStream = %q, want BURNLINK_JOBS", cfg.NATSStream)
	}
	if cfg.NATSJobSubject != "burnlink.jobs" {
		t.Fatalf("NATSJobSubject = %q, want burnlink.jobs", cfg.NATSJobSubject)
	}
	if cfg.PayloadInlineMaxBytes != 1048576 {
		t.Fatalf("PayloadInlineMaxBytes = %d, want 1048576", cfg.PayloadInlineMaxBytes)
	}
}

func TestLoadRejectsDefaultTTLOutsideAllowedSet(t *testing.T) {
	clearBurnLinkEnv(t)
	t.Setenv("BURNLINK_DEFAULT_TTL_SECONDS", "60")
	t.Setenv("BURNLINK_ALLOWED_TTL_SECONDS", "600,3600,86400")

	if _, err := Load(); err == nil {
		t.Fatal("expected config load error")
	}
}

func clearBurnLinkEnv(t *testing.T) {
	t.Helper()

	keys := []string{
		"BURNLINK_ENV",
		"BURNLINK_PUBLIC_BASE_URL",
		"BURNLINK_API_ADDR",
		"BURNLINK_API_DB_PATH",
		"BURNLINK_NATS_URL",
		"BURNLINK_NATS_STREAM",
		"BURNLINK_NATS_JOB_SUBJECT",
		"BURNLINK_PAYLOAD_INLINE_MAX_BYTES",
		"BURNLINK_DEFAULT_TTL_SECONDS",
		"BURNLINK_ALLOWED_TTL_SECONDS",
	}
	for _, key := range keys {
		t.Setenv(key, "")
	}
}
