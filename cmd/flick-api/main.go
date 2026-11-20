package main

import (
	"context"
	"log"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Felix-LeeSM/flick-drop/internal/config"
	"github.com/Felix-LeeSM/flick-drop/internal/db"
	"github.com/Felix-LeeSM/flick-drop/internal/events"
	"github.com/Felix-LeeSM/flick-drop/internal/httpapi"
	"github.com/Felix-LeeSM/flick-drop/internal/piperelay"
	"github.com/Felix-LeeSM/flick-drop/internal/requests"
	"github.com/Felix-LeeSM/flick-drop/internal/secrets"
	"github.com/Felix-LeeSM/flick-drop/internal/storage"
	"github.com/Felix-LeeSM/flick-drop/internal/telemetry"
)

// Must stay under terminationGracePeriodSeconds (60s) minus the 5s preStop sleep.
const shutdownTimeout = 50 * time.Second

func main() {
	logger := telemetry.NewLogger()
	telemetry.SetStandardLogger(logger)

	// Exits before storage, NATS, the reconciler, or listeners start.
	migrateOnly := len(os.Args) == 2 && os.Args[1] == "migrate"
	if len(os.Args) > 1 && !migrateOnly {
		log.Fatalf("usage: flick-api [migrate]")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	shutdownTracing, err := telemetry.SetupTracing(ctx, telemetry.TracingOptions{
		ServiceName: "flick-api",
		Endpoint:    cfg.OTLPEndpoint,
	})
	if err != nil {
		log.Fatalf("setup tracing: %v", err)
	}
	defer func() {
		flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTracing(flushCtx); err != nil {
			log.Printf("shutdown tracing: %v", err)
		}
	}()

	conn, err := db.OpenPostgres(ctx, cfg.APIDatabaseURL)
	if err != nil {
		log.Fatalf("open api database: %v", err)
	}
	defer conn.Close()

	if err := db.MigrateAPI(ctx, conn); err != nil {
		log.Fatalf("migrate api database: %v", err)
	}
	if migrateOnly {
		log.Printf("flick-api migrated the flick_api database")
		return
	}

	var objectStore *storage.Registry
	if len(cfg.StorageBackends) > 0 {
		backends := make([]storage.Backend, 0, len(cfg.StorageBackends))
		for _, b := range cfg.StorageBackends {
			client, err := storage.New(b.S3)
			if err != nil {
				log.Fatalf("create object store %s: %v", b.Name, err)
			}
			backends = append(backends, storage.Backend{Name: b.Name, Store: client, MaxTotalBytes: b.MaxTotalBytes})
			log.Printf("flick-api object backend %s enabled: bucket %s region %s", b.Name, b.S3.Bucket, b.S3.Region)
		}
		objectStore, err = storage.NewRegistry(backends...)
		if err != nil {
			log.Fatalf("create object registry: %v", err)
		}
	}
	outboxStore, err := events.NewOutboxStore(conn, cfg.NATSJobSubject)
	if err != nil {
		log.Fatalf("create outbox store: %v", err)
	}
	maxObjectBytes := secrets.MaxCiphertextBytes(cfg.MaxFileBytes)
	secretStore, err := secrets.NewStore(conn, secrets.StoreOptions{
		PayloadInlineMaxBytes: cfg.PayloadInlineMaxBytes,
		MaxObjectBytes:        maxObjectBytes,
		MinTTLSeconds:         cfg.MinTTLSeconds,
		MaxTTLSeconds:         cfg.MaxTTLSeconds,
		Objects:               objectStore,
		Outbox:                outboxStore,
	})
	if err != nil {
		log.Fatalf("create secret store: %v", err)
	}
	telemetry.RegisterAPIStateCollector(telemetry.APIStateQueries{
		PendingUploads: secretStore.CountPendingUploads,
		OutboxPending:  outboxStore.CountPending,
		StorageBytesUsed: func(ctx context.Context) (map[string]int64, error) {
			var names []string
			if objectStore != nil {
				names = objectStore.Names()
			}
			return db.StorageObjectBytes(ctx, conn, time.Now().UTC(), names)
		},
	})
	natsConn, err := events.ConnectNATS(cfg.NATSURL)
	if err != nil {
		log.Fatalf("connect nats: %v", err)
	}
	defer natsConn.Close()

	natsPublisher, err := events.NewNATSJetStreamPublisher(natsConn)
	if err != nil {
		log.Fatalf("create nats publisher: %v", err)
	}
	if err := natsPublisher.EnsureStream(ctx, cfg.NATSStream, cfg.NATSJobSubject); err != nil {
		log.Fatalf("ensure nats stream: %v", err)
	}
	outboxPublisher, err := events.NewOutboxPublisher(outboxStore, natsPublisher, events.OutboxPublisherOptions{})
	if err != nil {
		log.Fatalf("create outbox publisher: %v", err)
	}

	requestStore, err := requests.NewStore(conn, requests.Options{
		Objects:               objectStore,
		Outbox:                outboxStore,
		PayloadInlineMaxBytes: cfg.PayloadInlineMaxBytes,
		MaxObjectBytes:        maxObjectBytes,
		MinTTLSeconds:         cfg.MinTTLSeconds,
		DefaultTTLSeconds:     cfg.DefaultTTLSeconds,
		MaxTTLSeconds:         cfg.MaxTTLSeconds,
	})
	if err != nil {
		log.Fatalf("create request store: %v", err)
	}

	// Valkey is optional: without it each replica keeps in-process rate-limit buckets, and /readyz never depends on it.
	var valkey *redis.Client
	if cfg.ValkeyAddr != "" {
		valkey = httpapi.NewValkeyClient(cfg.ValkeyAddr)
		defer valkey.Close()
		log.Printf("flick-api rate limiter shares buckets through valkey")
	}

	// Live transfer needs Valkey: either side may land on any replica. Without it /api/pipe/* answers 503.
	var pipeCoordinator *piperelay.Coordinator
	var streamServer *http.Server
	switch {
	case cfg.PipeRelayEnabled && valkey == nil:
		log.Printf("flick-api live pipe relay disabled: FLICK_VALKEY_ADDR is empty")
	case cfg.PipeRelayEnabled:
		pipeCoordinator, err = piperelay.NewCoordinator(piperelay.CoordinatorOptions{
			Valkey:        valkey,
			KeySalt:       cfg.RateLimitKeySalt,
			InternalToken: cfg.InternalToken,
			AdvertiseIP:   cfg.PodIP,
			InternalPort:  cfg.InternalHTTPPort,
			MaxConcurrent: cfg.PipeMaxConcurrent,
		})
		if err != nil {
			log.Fatalf("create live pipe relay: %v", err)
		}
		defer pipeCoordinator.Close()
		streamServer = &http.Server{
			// Never bind every interface.
			Addr:              netip.AddrPortFrom(cfg.PodIP, cfg.InternalHTTPPort).String(),
			Handler:           pipeCoordinator.InternalHandler(),
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       120 * time.Second,
			MaxHeaderBytes:    64 << 10,
			// No Read/WriteTimeout: the stream handler sets its own deadlines (internal/piperelay/stream.go).
		}
		log.Printf("flick-api live pipe relay enabled (max_concurrent=%d, stream listener %s)", cfg.PipeMaxConcurrent, streamServer.Addr)
	}

	server := &http.Server{
		Addr: cfg.APIAddr,
		Handler: httpapi.NewRouter(conn, secretStore, httpapi.Options{
			PayloadInlineMaxBytes:    cfg.PayloadInlineMaxBytes,
			AdvertisedInlineMaxBytes: cfg.EffectivePayloadInlineMaxBytes(),
			MaxFileBytes:             cfg.EffectiveMaxFileBytes(),
			AllowedOrigin:            cfg.PublicBaseURL,
			InternalToken:            cfg.InternalToken,
			MetricsToken:             cfg.MetricsToken,
			OpenRatePerMinute:        cfg.OpenRatePerMinute,
			CreateRatePerMinute:      cfg.CreateRatePerMinute,
			TrustedProxies:           cfg.TrustedProxies,
			Valkey:                   valkey,
			RateLimitKeySalt:         cfg.RateLimitKeySalt,
			OutboxStore:              outboxStore,
			NATSConnected:            natsConn.IsConnected,
			RequestStore:             requestStore,
			PipeCoordinator:          pipeCoordinator,
		}),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
		// No WriteTimeout: a large /open streams ciphertext and sets its own deadline (secrets.OpenStreamTimeout).
		// ReadHeaderTimeout is the Slowloris defense.
	}

	reaper, err := secrets.NewReaper(conn, secretStore, outboxStore, secrets.ReaperOptions{
		Requests:  requestStore,
		BatchSize: cfg.ReaperBatchSize,
	})
	if err != nil {
		log.Fatalf("create reaper: %v", err)
	}

	publisherErr := make(chan error, 1)
	go func() {
		log.Printf("flick-api publishing outbox subject %s to stream %s", cfg.NATSJobSubject, cfg.NATSStream)
		publisherErr <- events.RunOutboxPublisher(ctx, outboxPublisher, events.OutboxPublisherLoopOptions{
			Logf: log.Printf,
		})
	}()

	reaperErr := make(chan error, 1)
	go func() {
		log.Printf("flick-api expiration reaper started (interval=%ds batch=%d)", cfg.ReaperIntervalSeconds, cfg.ReaperBatchSize)
		reaperErr <- secrets.RunReaper(ctx, reaper, secrets.ReaperLoopOptions{
			Interval: time.Duration(cfg.ReaperIntervalSeconds) * time.Second,
			Logf:     log.Printf,
		})
	}()

	serverErr := make(chan error, 1)
	go func() {
		log.Printf("flick-api listening on %s", cfg.APIAddr)
		serverErr <- server.ListenAndServe()
	}()
	streamErr := make(chan error, 1)
	if streamServer != nil {
		go func() { streamErr <- streamServer.ListenAndServe() }()
	}

	serverDone := false
	publisherDone := false
	reaperDone := false
	select {
	case err := <-serverErr:
		serverDone = true
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("serve api: %v", err)
		}
		stop()
	case err := <-publisherErr:
		publisherDone = true
		if err != nil {
			log.Fatalf("run outbox publisher: %v", err)
		}
		stop()
	case err := <-reaperErr:
		reaperDone = true
		if err != nil {
			log.Fatalf("run reaper: %v", err)
		}
		stop()
	case err := <-streamErr:
		log.Fatalf("serve live pipe stream listener: %v", err)
	case <-ctx.Done():
	}

	// Order matters: long-polls answer first so browsers re-poll another replica, then the API drains;
	// the stream listener closes last because a draining /receive needs the sender's replica to dial in.
	if pipeCoordinator != nil {
		pipeCoordinator.Drain()
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if !serverDone {
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown api server: %v", err)
		}

		if err := <-serverErr; err != nil && err != http.ErrServerClosed {
			log.Fatalf("serve api: %v", err)
		}
	}
	if streamServer != nil {
		if err := streamServer.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown live pipe stream listener: %v", err)
		}
	}
	if !publisherDone {
		if err := <-publisherErr; err != nil {
			log.Fatalf("run outbox publisher: %v", err)
		}
	}
	if !reaperDone {
		if err := <-reaperErr; err != nil {
			log.Fatalf("run reaper: %v", err)
		}
	}

	log.Print("flick-api stopped")
}
