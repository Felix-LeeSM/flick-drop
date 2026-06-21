package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"net"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Felix-LeeSM/flick-drop/internal/events"
	"github.com/Felix-LeeSM/flick-drop/internal/secrets"
)

type Server struct {
	db                    *sql.DB
	secrets               *secrets.Store
	outbox                *events.OutboxStore
	newJobID              func() (string, error)
	payloadInlineMaxBytes int64
	allowedOrigin         string
	internalToken         string
	openLimiter           *rateLimiter
	createLimiter         *rateLimiter
}

type Options struct {
	PayloadInlineMaxBytes int64
	AllowedOrigin         string
	InternalToken         string
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

	server := Server{
		db:                    db,
		secrets:               secretStore,
		outbox:                opts.OutboxStore,
		newJobID:              events.NewJobID,
		payloadInlineMaxBytes: payloadInlineMaxBytes,
		allowedOrigin:         strings.TrimRight(opts.AllowedOrigin, "/"),
		internalToken:         opts.InternalToken,
		openLimiter:           newRateLimiter(opts.OpenRatePerMinute, opts.TrustedProxies),
		createLimiter:         newRateLimiter(opts.CreateRatePerMinute, opts.TrustedProxies),
	}
	if opts.NewJobID != nil {
		server.newJobID = opts.NewJobID
	}

	r := chi.NewRouter()
	r.Use(server.cors)
	r.Get("/healthz", server.healthz)
	r.Get("/readyz", server.readyz)
	r.Get("/metrics", server.metrics)
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

func (s Server) metrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("# HELP flick_api_info Flick API process info\n# TYPE flick_api_info gauge\nflick_api_info 1\n"))
}
