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
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	conn, err := db.OpenSQLite(ctx, cfg.APIDBPath)
	if err != nil {
		log.Fatalf("open api database: %v", err)
	}
	defer conn.Close()

	if err := db.MigrateAPI(ctx, conn); err != nil {
		log.Fatalf("migrate api database: %v", err)
	}

	secretStore, err := secrets.NewStore(conn, secrets.StoreOptions{
		PayloadInlineMaxBytes: cfg.PayloadInlineMaxBytes,
		MinTTLSeconds:         cfg.MinTTLSeconds,
		MaxTTLSeconds:         cfg.MaxTTLSeconds,
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
			AllowedOrigin:         cfg.PublicBaseURL,
			InternalToken:         cfg.InternalToken,
			OutboxStore:           outboxStore,
		}),
	}

	publisherErr := make(chan error, 1)
	go func() {
		log.Printf("flick-api publishing outbox subject %s to stream %s", cfg.NATSJobSubject, cfg.NATSStream)
		publisherErr <- events.RunOutboxPublisher(ctx, outboxPublisher, events.OutboxPublisherLoopOptions{
			Logf: log.Printf,
		})
	}()

	serverErr := make(chan error, 1)
	go func() {
		log.Printf("flick-api listening on %s", cfg.APIAddr)
		serverErr <- server.ListenAndServe()
	}()

	serverDone := false
	publisherDone := false
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

	log.Print("flick-api stopped")
}
