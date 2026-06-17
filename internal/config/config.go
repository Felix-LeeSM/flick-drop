package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Env                   string
	APIAddr               string
	APIDBPath             string
	PayloadInlineMaxBytes int64
	DefaultTTLSeconds     int
	AllowedTTLSeconds     []int
}

func Load() (Config, error) {
	cfg := Config{
		Env:                   getenv("BURNLINK_ENV", "development"),
		APIAddr:               getenv("BURNLINK_API_ADDR", ":8080"),
		APIDBPath:             getenv("BURNLINK_API_DB_PATH", "./var/api.db"),
		PayloadInlineMaxBytes: 1048576,
		DefaultTTLSeconds:     3600,
		AllowedTTLSeconds:     []int{600, 3600, 86400},
	}

	var err error
	if raw := os.Getenv("BURNLINK_PAYLOAD_INLINE_MAX_BYTES"); raw != "" {
		cfg.PayloadInlineMaxBytes, err = parsePositiveInt64("BURNLINK_PAYLOAD_INLINE_MAX_BYTES", raw)
		if err != nil {
			return Config{}, err
		}
	}
	if raw := os.Getenv("BURNLINK_DEFAULT_TTL_SECONDS"); raw != "" {
		cfg.DefaultTTLSeconds, err = parsePositiveInt("BURNLINK_DEFAULT_TTL_SECONDS", raw)
		if err != nil {
			return Config{}, err
		}
	}
	if raw := os.Getenv("BURNLINK_ALLOWED_TTL_SECONDS"); raw != "" {
		cfg.AllowedTTLSeconds, err = parseAllowedTTLs(raw)
		if err != nil {
			return Config{}, err
		}
	}

	if !containsInt(cfg.AllowedTTLSeconds, cfg.DefaultTTLSeconds) {
		return Config{}, fmt.Errorf("BURNLINK_DEFAULT_TTL_SECONDS must be present in BURNLINK_ALLOWED_TTL_SECONDS")
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

func parseAllowedTTLs(raw string) ([]int, error) {
	parts := strings.Split(raw, ",")
	values := make([]int, 0, len(parts))
	seen := make(map[int]struct{}, len(parts))

	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			return nil, fmt.Errorf("BURNLINK_ALLOWED_TTL_SECONDS must not contain empty entries")
		}
		value, err := parsePositiveInt("BURNLINK_ALLOWED_TTL_SECONDS", trimmed)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[value]; ok {
			return nil, fmt.Errorf("BURNLINK_ALLOWED_TTL_SECONDS contains duplicate value %d", value)
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}

	return values, nil
}

func containsInt(values []int, needle int) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}
