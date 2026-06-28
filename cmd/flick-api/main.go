package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/config"
	"github.com/Felix-LeeSM/flick-drop/internal/db"
	"github.com/Felix-LeeSM/flick-drop/internal/events"
	"github.com/Felix-LeeSM/flick-drop/internal/httpapi"
	"github.com/Felix-LeeSM/flick-drop/internal/secrets"
	"github.com/Felix-LeeSM/flick-drop/internal/storage"
	"github.com/Felix-LeeSM/flick-drop/internal/telemetry"
)

func main() {
	logger := telemetry.NewLogger()
	telemetry.SetStandardLogger(logger)

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

	conn, err := db.OpenSQLite(ctx, cfg.APIDBPath)
	if err != nil {
		log.Fatalf("open api database: %v", err)
	}
	defer conn.Close()

	if err := db.MigrateAPI(ctx, conn); err != nil {
		log.Fatalf("migrate api database: %v", err)
	}

	var objectStore storage.ObjectStore
	if cfg.S3.Enabled {
		objClient, err := storage.New(storage.Config{
			Enabled:         true,
			Endpoint:        cfg.S3.Endpoint,
			Region:          cfg.S3.Region,
			Bucket:          cfg.S3.Bucket,
			AccessKeyID:     cfg.S3.AccessKeyID,
			SecretAccessKey: cfg.S3.SecretAccessKey,
			PathStyle:       cfg.S3.PathStyle,
		})
		if err != nil {
			log.Fatalf("create object store: %v", err)
		}
		objectStore = objClient
		log.Printf("flick-api large-object storage enabled: bucket %s region %s", cfg.S3.Bucket, cfg.S3.Region)
	}
	// The ciphertext cap is the plaintext cap plus the AES-GCM tag and a safety
	// margin; finalize HEAD re-verifies against this.
	maxObjectBytes := cfg.MaxFileBytes + 4096
	secretStore, err := secrets.NewStore(conn, secrets.StoreOptions{
		PayloadInlineMaxBytes: cfg.PayloadInlineMaxBytes,
		MaxObjectBytes:        maxObjectBytes,
		MinTTLSeconds:         cfg.MinTTLSeconds,
		MaxTTLSeconds:         cfg.MaxTTLSeconds,
		Objects:               objectStore,
	})
	if err != nil {
		log.Fatalf("create secret store: %v", err)
	}
	outboxStore, err := events.NewOutboxStore(conn, cfg.NATSJobSubject)
	if err != nil {
		log.Fatalf("create outbox store: %v", err)
	}
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

	server := &http.Server{
		Addr: cfg.APIAddr,
		Handler: httpapi.NewRouter(conn, secretStore, httpapi.Options{
			PayloadInlineMaxBytes: cfg.PayloadInlineMaxBytes,
			MaxFileBytes:          cfg.MaxFileBytes,
			AllowedOrigin:         cfg.PublicBaseURL,
			InternalToken:         cfg.InternalToken,
			MetricsToken:          cfg.MetricsToken,
			OpenRatePerMinute:     cfg.OpenRatePerMinute,
			CreateRatePerMinute:   cfg.CreateRatePerMinute,
			TrustedProxies:        cfg.TrustedProxies,
			OutboxStore:           outboxStore,
		}),
	}

	reaper, err := secrets.NewReaper(conn, secretStore, outboxStore, secrets.ReaperOptions{
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
	case <-ctx.Done():
	}

	if !serverDone {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown api server: %v", err)
		}
		cancel()

		if err := <-serverErr; err != nil && err != http.ErrServerClosed {
			log.Fatalf("serve api: %v", err)
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
