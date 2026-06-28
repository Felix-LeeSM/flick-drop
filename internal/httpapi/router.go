package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"net"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/Felix-LeeSM/flick-drop/internal/events"
	"github.com/Felix-LeeSM/flick-drop/internal/secrets"
	"github.com/Felix-LeeSM/flick-drop/internal/telemetry"
)

// tracer instruments the HTTP server. With tracing off (no OTLP endpoint) it is
// OTel's no-op, so the middleware adds no measurable cost.
var tracer = otel.Tracer("github.com/Felix-LeeSM/flick-drop/internal/httpapi")

type Server struct {
	db                    *sql.DB
	secrets               *secrets.Store
	outbox                *events.OutboxStore
	newJobID              func() (string, error)
	payloadInlineMaxBytes int64
	maxFileBytes          int64
	allowedOrigin         string
	internalToken         string
	metricsToken          string
	openLimiter           *rateLimiter
	createLimiter         *rateLimiter
}

type Options struct {
	PayloadInlineMaxBytes int64
	MaxFileBytes          int64
	AllowedOrigin         string
	InternalToken         string
	MetricsToken          string
	OpenRatePerMinute     int
	CreateRatePerMinute   int
	TrustedProxies        []*net.IPNet
	OutboxStore           *events.OutboxStore
	NewJobID              func() (string, error)
}

func NewRouter(db *sql.DB, secretStore *secrets.Store, opts Options) http.Handler {
	payloadInlineMaxBytes := opts.PayloadInlineMaxBytes
	if payloadInlineMaxBytes <= 0 {
		payloadInlineMaxBytes = 1048576
	}
	maxFileBytes := opts.MaxFileBytes
	if maxFileBytes <= 0 {
		maxFileBytes = 52428800
	}

	server := Server{
		db:                    db,
		secrets:               secretStore,
		outbox:                opts.OutboxStore,
		newJobID:              events.NewJobID,
		payloadInlineMaxBytes: payloadInlineMaxBytes,
		maxFileBytes:          maxFileBytes,
		allowedOrigin:         strings.TrimRight(opts.AllowedOrigin, "/"),
		internalToken:         opts.InternalToken,
		metricsToken:          opts.MetricsToken,
		openLimiter:           newRateLimiter(opts.OpenRatePerMinute, opts.TrustedProxies),
		createLimiter:         newRateLimiter(opts.CreateRatePerMinute, opts.TrustedProxies),
	}
	if opts.NewJobID != nil {
		server.newJobID = opts.NewJobID
	}

	r := chi.NewRouter()
	r.Use(server.cors)
	r.Use(server.tracing)
	r.Get("/healthz", server.healthz)
	r.Get("/readyz", server.readyz)
	r.With(server.metricsAuth).Get("/metrics", server.metrics)
	r.Get("/api/config", server.getConfig)
	r.With(server.createLimiter.middleware).Post("/api/secrets", server.createSecret)
	r.Post("/api/secrets/{id}/finalize", server.finalizeSecret)
	r.Get("/api/secrets/{id}", server.getSecretMetadata)
	r.With(server.openLimiter.middleware).Post("/api/secrets/{id}/open", server.openSecret)
	r.Group(func(r chi.Router) {
		r.Use(server.internalAuth)
		r.Post("/internal/secrets/{id}/cleanup", server.cleanupSecret)
	})
	return r
}

func (s Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimRight(r.Header.Get("Origin"), "/")
		if s.allowedOrigin != "" && origin == s.allowedOrigin {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.Header().Set("Vary", "Origin")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// tracing starts a server span per request so a create/open/cleanup flow can be
// followed end to end (and, once #133 PR2 lands cross-process propagation,
// across the api↔worker boundary). Probe and scrape endpoints are skipped — they
// are health/monitoring noise, not request flows worth a span. Only method,
// matched route, and status are recorded; never secret content.
func (s Server) tracing(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz", "/readyz", "/metrics":
			next.ServeHTTP(w, r)
			return
		}

		ctx, span := tracer.Start(r.Context(), r.Method,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(attribute.String("http.request.method", r.Method)),
		)
		defer span.End()
		// Tracing disabled (no OTLP endpoint) or this span unsampled: skip the
		// response wrapper and route bookkeeping so the off-path stays free.
		if !span.IsRecording() {
			next.ServeHTTP(w, r)
			return
		}

		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r.WithContext(ctx))

		// chi populates RoutePattern only after routing the request; the
		// RouteContext is the same pointer chi installed upstream, so reading it
		// here yields the matched template (e.g. "/api/secrets/{id}/open") rather
		// than the high-cardinality concrete path.
		if rp := chi.RouteContext(r.Context()).RoutePattern(); rp != "" {
			span.SetName(r.Method + " " + rp)
			span.SetAttributes(attribute.String("http.route", rp))
		}
		status := ww.Status()
		if status == 0 {
			status = http.StatusOK
		}
		span.SetAttributes(attribute.Int("http.response.status_code", status))
		if status >= http.StatusInternalServerError {
			span.SetStatus(codes.Error, http.StatusText(status))
		}
	})
}

func (s Server) internalAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("X-Flick-Internal-Token")
		if !secureTokenEqual(got, s.internalToken) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "internal token is missing or invalid")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func secureTokenEqual(got, want string) bool {
	if got == "" || want == "" {
		return false
	}
	gotHash := sha256.Sum256([]byte(got))
	wantHash := sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(gotHash[:], wantHash[:]) == 1
}

// metricsAuth guards /metrics behind a bearer token separate from the internal
// token, so a Prometheus scraper credential cannot reach /internal/*. The token
// is a static pre-shared key (FLICK_METRICS_TOKEN); an empty configured token
// fails closed (401) so a misconfigured deploy never exposes metrics.
func (s Server) metricsAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.checkBearer(r.Header.Get("Authorization")) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "metrics token is missing or invalid")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// checkBearer reports whether header is "Bearer <token>" matching the configured
// metrics token. The scheme match is case-insensitive per RFC 6750; the token
// tail is compared in constant time.
func (s Server) checkBearer(header string) bool {
	const prefix = "bearer "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return false
	}
	return secureTokenEqual(header[len(prefix):], s.metricsToken)
}

func (s Server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s Server) readyz(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "database is not configured")
		return
	}
	if err := s.db.PingContext(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "database is not ready")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s Server) metrics(w http.ResponseWriter, r *http.Request) {
	telemetry.MetricsHandler().ServeHTTP(w, r)
}
