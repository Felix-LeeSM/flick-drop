package telemetry

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var DefaultRegistry = prometheus.NewRegistry()

const (
	labelKindStorageKind = "kind"
	labelStorage         = "storage"
	labelJobKind         = "kind"
	labelOutcome         = "outcome"
	labelReason          = "reason"
	labelLimiter         = "limiter"
)

var (
	SecretCreated = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "flick_secret_created_total",
			Help: "Secrets created, by content kind and storage backend.",
		},
		[]string{labelKindStorageKind, labelStorage},
	)

	// Failed access attempts are deliberately not counted: an attack signal, not delivery.
	SecretOpened = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "flick_secret_opened_total",
			Help: "Secrets successfully opened (consumed).",
		},
	)

	// "expired" (active past TTL) or "orphan" (pending_upload never finalized).
	SecretReaped = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "flick_secret_reaped_total",
			Help: "Secrets hard-deleted by the expiration reaper, by reason.",
		},
		[]string{labelReason},
	)

	// "succeeded"/"failed" are per-attempt; "dead" exhausted retries.
	JobsProcessed = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "flick_jobs_processed_total",
			Help: "Worker jobs processed, by kind and outcome.",
		},
		[]string{labelJobKind, labelOutcome},
	)

	RequestCreated = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "flick_request_created_total",
			Help: "Request links created by owners.",
		},
	)

	RequestSubmitted = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "flick_request_submitted_total",
			Help: "Request links that received a submission (inline or finalized object).",
		},
	)

	RequestConsumed = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "flick_request_consumed_total",
			Help: "Request submissions opened (consumed) by their owner.",
		},
	)

	OutboxPublished = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "flick_outbox_published_total",
			Help: "Outbox events published to NATS, by outcome.",
		},
		[]string{labelOutcome},
	)

	// Non-zero means replicas each enforce their own per-process budget.
	RateLimitFallbacks = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "flick_ratelimit_fallback_total",
			Help: "Rate-limit decisions made in process memory because Valkey was unavailable, by limiter.",
		},
		[]string{labelLimiter},
	)
)

func init() {
	DefaultRegistry.MustRegister(
		SecretCreated,
		SecretOpened,
		SecretReaped,
		JobsProcessed,
		RequestCreated,
		RequestSubmitted,
		RequestConsumed,
		OutboxPublished,
		RateLimitFallbacks,
	)
}

func MetricsHandler() http.Handler {
	return promhttp.HandlerFor(DefaultRegistry, promhttp.HandlerOpts{})
}

// Empty configured token fails closed with 401.
func NewMetricsServer(addr, token string) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		const prefix = "bearer "
		header := r.Header.Get("Authorization")
		ok := len(header) >= len(prefix) && strings.EqualFold(header[:len(prefix)], prefix) && checkBearerToken(header[len(prefix):], token)
		if !ok {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		MetricsHandler().ServeHTTP(w, r)
	})
	return &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
}

// Same constant-time digest compare as httpapi.secureTokenEqual.
func checkBearerToken(got, want string) bool {
	if got == "" || want == "" {
		return false
	}
	gotHash := sha256.Sum256([]byte(got))
	wantHash := sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(gotHash[:], wantHash[:]) == 1
}
