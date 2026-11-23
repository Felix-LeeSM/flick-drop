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
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/Felix-LeeSM/flick-drop/internal/events"
	"github.com/Felix-LeeSM/flick-drop/internal/piperelay"
	"github.com/Felix-LeeSM/flick-drop/internal/requests"
	"github.com/Felix-LeeSM/flick-drop/internal/secrets"
	"github.com/Felix-LeeSM/flick-drop/internal/telemetry"
)

var defaultTracer = otel.Tracer("github.com/Felix-LeeSM/flick-drop/internal/httpapi")

type Server struct {
	db                       *sql.DB
	secrets                  *secrets.Store
	requests                 *requests.Store
	outbox                   *events.OutboxStore
	newJobID                 func() (string, error)
	payloadInlineMaxBytes    int64
	advertisedInlineMaxBytes int64
	maxFileBytes             int64
	allowedOrigin            string
	internalToken            string
	metricsToken             string
	openLimiter              *rateLimiter
	secretCreateLimiter      *rateLimiter
	requestCreateLimiter     *rateLimiter
	managementLimiter        *rateLimiter
	requestLimiter           *rateLimiter
	pipeCoordinator          *piperelay.Coordinator
	pipeInitLimiter          *rateLimiter
	pipeJoinLimiter          *rateLimiter
	pipeStatusLimiter        *rateLimiter
	tracer                   trace.Tracer
	// A func keeps the nats package out of httpapi's imports.
	natsConnected func() bool
}

type Options struct {
	// AdvertisedInlineMaxBytes is the plaintext equivalent (smaller by the AEAD tag) for /api/config.
	PayloadInlineMaxBytes    int64
	AdvertisedInlineMaxBytes int64
	MaxFileBytes             int64
	AllowedOrigin            string
	InternalToken            string
	MetricsToken             string
	OpenRatePerMinute        int
	CreateRatePerMinute      int
	TrustedProxies           []*net.IPNet
	Valkey                   *redis.Client
	RateLimitKeySalt         string
	OutboxStore              *events.OutboxStore
	NewJobID                 func() (string, error)
	NATSConnected            func() bool
	RequestStore             *requests.Store
	// Nil disables live transfer (503).
	PipeCoordinator *piperelay.Coordinator
	Tracer          trace.Tracer
	TracerProvider  trace.TracerProvider
}

func NewRouter(db *sql.DB, secretStore *secrets.Store, opts Options) http.Handler {
	payloadInlineMaxBytes := opts.PayloadInlineMaxBytes
	if payloadInlineMaxBytes <= 0 {
		payloadInlineMaxBytes = 1048576
	}
	advertisedInlineMaxBytes := opts.AdvertisedInlineMaxBytes
	if advertisedInlineMaxBytes <= 0 {
		advertisedInlineMaxBytes = payloadInlineMaxBytes
	}
	maxFileBytes := opts.MaxFileBytes
	if maxFileBytes <= 0 {
		maxFileBytes = 268435456
	}

	tracer := opts.Tracer
	if tracer == nil {
		if opts.TracerProvider != nil {
			tracer = opts.TracerProvider.Tracer("github.com/Felix-LeeSM/flick-drop/internal/httpapi")
		} else {
			tracer = defaultTracer
		}
	}

	var shared *sharedBuckets
	if opts.Valkey != nil {
		shared = newSharedBuckets(opts.Valkey, opts.RateLimitKeySalt)
	}
	limiter := func(name string, perMinute int, scope rateLimitScope) *rateLimiter {
		rl := newRateLimiter(perMinute, opts.TrustedProxies, scope)
		rl.name, rl.shared = name, shared
		return rl
	}

	server := Server{
		db:                       db,
		secrets:                  secretStore,
		requests:                 opts.RequestStore,
		outbox:                   opts.OutboxStore,
		newJobID:                 events.NewJobID,
		payloadInlineMaxBytes:    payloadInlineMaxBytes,
		advertisedInlineMaxBytes: advertisedInlineMaxBytes,
		maxFileBytes:             maxFileBytes,
		allowedOrigin:            strings.TrimRight(opts.AllowedOrigin, "/"),
		internalToken:            opts.InternalToken,
		metricsToken:             opts.MetricsToken,
		openLimiter:              limiter("open", opts.OpenRatePerMinute, perResourceID),
		secretCreateLimiter:      limiter("secret_create", opts.CreateRatePerMinute, perNetwork),
		requestCreateLimiter:     limiter("request_create", opts.CreateRatePerMinute, perNetwork),
		managementLimiter:        limiter("management", opts.OpenRatePerMinute, perResourceID),
		// One bucket across request IDs: rotating IDs must not bypass the limit or allocate buckets.
		requestLimiter:    limiter("request", opts.OpenRatePerMinute, perNetwork),
		pipeCoordinator:   opts.PipeCoordinator,
		pipeInitLimiter:   limiter("pipe_init", opts.CreateRatePerMinute, perNetwork),
		pipeJoinLimiter:   limiter("pipe_join", opts.OpenRatePerMinute, perNetwork),
		pipeStatusLimiter: limiter("pipe_status", pipeStatusRatePerMinute, perNetwork),
		tracer:            tracer,
	}
	if opts.NewJobID != nil {
		server.newJobID = opts.NewJobID
	}
	server.natsConnected = opts.NATSConnected

	r := chi.NewRouter()
	// Outermost: a panic becomes a logged 500 instead of a dropped connection.
	r.Use(middleware.Recoverer)
	r.Use(server.cors)
	r.Use(server.tracing)
	r.Get("/healthz", server.healthz)
	r.Get("/readyz", server.readyz)
	r.With(server.metricsAuth).Get("/metrics", server.metrics)
	r.Get("/api/config", server.getConfig)
	r.With(server.secretCreateLimiter.middleware).Post("/api/secrets", server.createSecret)
	r.Post("/api/secrets/{id}/finalize", server.finalizeSecret)
	r.Get("/api/secrets/{id}", server.getSecretMetadata)
	r.With(noStore, server.managementLimiter.middleware).Get("/api/secrets/{id}/management", server.getManagementStatus)
	r.With(noStore, server.managementLimiter.middleware).Post("/api/secrets/{id}/revoke", server.revokeSecret)
	r.With(server.openLimiter.middleware).Post("/api/secrets/{id}/open", server.openSecret)
	r.Route("/api/requests", func(r chi.Router) {
		r.Use(server.requestReady)
		r.With(server.requestCreateLimiter.middleware).Post("/", server.createRequest)
		r.Group(func(r chi.Router) {
			r.Use(server.requestLimiter.middleware)
			r.Get("/{id}", server.getRequestInstructions)
			r.Post("/{id}/submit", server.submitRequest)
			r.Post("/{id}/upload", server.reserveRequest)
			r.Post("/{id}/finalize", server.finalizeRequest)
			r.Post("/{id}/abandon", server.abandonRequest)
			r.Post("/{id}/attempt", server.getRequestAttempt)
			r.Get("/{id}/owner", server.getRequestOwner)
			r.Post("/{id}/open", server.openRequest)
			r.Post("/{id}/revoke", server.revokeRequest)
		})
	})
	registerPipeRoutes := func(r chi.Router) {
		r.With(server.pipeInitLimiter.middleware).Post("/init", server.initPipe)
		// /join is the only call accepting a bare 6-digit code, so it carries the open-rate limit.
		// /status long-polls hold a goroutine and socket each, hence their own per-network limit.
		r.With(server.pipeJoinLimiter.middleware).Post("/join", server.joinPipe)
		r.With(server.pipeStatusLimiter.middleware).Get("/status", server.statusPipe)
		r.Get("/receive", server.receivePipe)
		r.Post("/leave", server.leavePipe)
		r.Get("/wait", server.waitPipe)
		r.Post("/select", server.selectPipe)
		r.Post("/decline", server.declinePipe)
		r.Post("/cancel", server.cancelPipe)
		r.Post("/send", server.sendPipe)
	}
	r.Route("/api/pipe", registerPipeRoutes)
	r.Route("/api/v1/pipe", registerPipeRoutes)
	r.Group(func(r chi.Router) {
		r.Use(server.internalAuth)
		r.Post("/internal/secrets/{id}/cleanup", server.cleanupSecret)
		r.Post("/internal/object-reconciliation/ack", server.acknowledgeObjectCleanup)
	})
	return r
}

func (s Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The only unconditional middleware for /api, which ingress routes past nginx's headers.
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.URL.Path == "/api/requests" || strings.HasPrefix(r.URL.Path, "/api/requests/") || strings.HasPrefix(r.URL.Path, "/api/pipe/") || strings.HasPrefix(r.URL.Path, "/api/v1/pipe/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		origin := strings.TrimRight(r.Header.Get("Origin"), "/")
		if s.allowedOrigin != "" && origin == s.allowedOrigin {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Flick-Pipe-Code, X-Flick-Pipe-Sender, X-Flick-Pipe-Connection, Flick-Kind, Flick-Encrypted-Filename")
			w.Header().Set("Access-Control-Expose-Headers", "Flick-Kind, Flick-Content-Type, Flick-Encrypted-Filename, Flick-Wrapped-Identity, Flick-Expires-At")
			w.Header().Set("Vary", "Origin")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// Only method, matched route, and status are recorded; never secret content.
func (s Server) tracing(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz", "/readyz", "/metrics":
			next.ServeHTTP(w, r)
			return
		}

		tr := s.tracer
		if tr == nil {
			tr = defaultTracer
		}
		ctx, span := tr.Start(r.Context(), r.Method,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(attribute.String("http.request.method", r.Method)),
		)
		defer span.End()
		if !span.IsRecording() {
			next.ServeHTTP(w, r)
			return
		}

		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r.WithContext(ctx))

		// RoutePattern is populated only after routing; reading it here yields the template, not the concrete path.
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

// Separate from the internal token so a scraper credential cannot reach /internal/*. Empty token fails closed.
func (s Server) metricsAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.checkBearer(r.Header.Get("Authorization"), s.metricsToken) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "metrics token is missing or invalid")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Scheme is case-insensitive per RFC 6750; the token is compared in constant time.
func (s Server) checkBearer(header, token string) bool {
	const prefix = "bearer "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return false
	}
	return secureTokenEqual(header[len(prefix):], token)
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
	// A down broker backs the outbox up silently, so /readyz must fail closed on it.
	if s.natsConnected != nil && !s.natsConnected() {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "message broker is not ready")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s Server) metrics(w http.ResponseWriter, r *http.Request) {
	telemetry.MetricsHandler().ServeHTTP(w, r)
}
