package httpapi

import "time"

// timeFormat is RFC3339 with nanosecond precision, the shape every
// Flick-Expires-At header and expires_at JSON field uses. formatUTC renders
// scanned timestamptz values in UTC: pgx v5 decodes timestamptz into the Go
// process location, and the HTTP contract emits UTC ("Z") timestamps.
const timeFormat = "2006-01-02T15:04:05.999999999Z07:00"

func formatUTC(t time.Time) string {
	return t.UTC().Format(timeFormat)
}
