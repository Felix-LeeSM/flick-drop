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
	"github.com/Felix-LeeSM/flick-drop/internal/storage"
	"github.com/Felix-LeeSM/flick-drop/internal/telemetry"
	"github.com/Felix-LeeSM/flick-drop/internal/worker"
)

func main() {
	logger := telemetry.NewLogger()
	telemetry.SetStandardLogger(logger)

	// Exits without starting metrics or NATS.
	migrateOnly := len(os.Args) == 2 && os.Args[1] == "migrate"
	if len(os.Args) > 1 && !migrateOnly {
		log.Fatalf("usage: flick-worker [migrate]")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	shutdownTracing, err := telemetry.SetupTracing(ctx, telemetry.TracingOptions{
		ServiceName: "flick-worker",
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

	if cfg.WorkerMetricsAddr != "" && !migrateOnly {
		metricsServer := telemetry.NewMetricsServer(cfg.WorkerMetricsAddr, cfg.MetricsToken)
		go func() {
			log.Printf("flick-worker metrics listening on %s", cfg.WorkerMetricsAddr)
			if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatalf("serve worker metrics: %v", err)
			}
		}()
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = metricsServer.Shutdown(shutdownCtx)
		}()
	}

	if cfg.WorkerDatabaseURL == "" {
		log.Fatalf("FLICK_WORKER_DATABASE_URL is required: the worker owns the flick_worker PostgreSQL database")
	}

	conn, err := db.OpenPostgres(ctx, cfg.WorkerDatabaseURL)
	if err != nil {
		log.Fatalf("open worker database: %v", err)
	}
	defer conn.Close()

	if err := db.MigrateWorker(ctx, conn); err != nil {
		log.Fatalf("migrate worker database: %v", err)
	}
	if migrateOnly {
		log.Printf("flick-worker migrated the flick_worker database")
		return
	}

	receiptStore, err := worker.NewReceiptStore(conn)
	if err != nil {
		log.Fatalf("create receipt store: %v", err)
	}

	natsConn, err := events.ConnectNATS(cfg.NATSURL)
	if err != nil {
		log.Fatalf("connect nats: %v", err)
	}
	defer natsConn.Close()

	natsConsumer, err := events.NewNATSJetStreamConsumer(natsConn)
	if err != nil {
		log.Fatalf("create nats consumer: %v", err)
	}

	cleanupClient, err := worker.NewCleanupClient(worker.CleanupClientOptions{
		BaseURL:       cfg.InternalAPIBaseURL,
		InternalToken: cfg.InternalToken,
	})
	if err != nil {
		log.Fatalf("create cleanup client: %v", err)
	}
	objectDeleters := make(map[string]worker.ObjectDeleter, len(cfg.StorageBackends))
	for _, b := range cfg.StorageBackends {
		client, err := storage.New(b.S3)
		if err != nil {
			log.Fatalf("create object store %s: %v", b.Name, err)
		}
		objectDeleters[b.Name] = client
		log.Printf("flick-worker object deletion enabled on backend %s: bucket %s", b.Name, b.S3.Bucket)
	}
	cleanupHandler, err := worker.NewCleanupHandler(cleanupClient, objectDeleters)
	if err != nil {
		log.Fatalf("create cleanup handler: %v", err)
	}
	processor, err := worker.NewProcessor(receiptStore, cleanupHandler, worker.ProcessorOptions{Acknowledger: cleanupClient})
	if err != nil {
		log.Fatalf("create worker processor: %v", err)
	}

	consumer, err := worker.NewNATSConsumerAdapter(natsConsumer)
	if err != nil {
		log.Fatalf("create nats consumer adapter: %v", err)
	}
	runner, err := worker.NewConsumerRunner(consumer, processor, worker.RunnerOptions{
		Stream:  cfg.NATSStream,
		Subject: cfg.NATSJobSubject,
	})
	if err != nil {
		log.Fatalf("create worker runner: %v", err)
	}

	log.Printf("flick-worker consuming subject %s from stream %s", cfg.NATSJobSubject, cfg.NATSStream)
	if err := runner.Run(ctx); err != nil {
		log.Fatalf("run worker: %v", err)
	}
	log.Print("flick-worker stopped")
}
