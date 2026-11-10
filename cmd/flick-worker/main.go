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

	// Bearer-gated /metrics so the scraped series carry the worker's job
	// counters alongside the API's. Disabled when the address is explicitly
	// empty; the token is the shared FLICK_METRICS_TOKEN.
	if cfg.WorkerMetricsAddr != "" {
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

	conn, err := db.OpenSQLite(ctx, cfg.WorkerDBPath)
	if err != nil {
		log.Fatalf("open worker database: %v", err)
	}
	defer conn.Close()

	if err := db.MigrateWorker(ctx, conn); err != nil {
		log.Fatalf("migrate worker database: %v", err)
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
	var objectDeleter worker.ObjectDeleter
	var secondaryDeleter worker.ObjectDeleter
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
		objectDeleter = objClient
		log.Printf("flick-worker object deletion enabled: bucket %s", cfg.S3.Bucket)
		if cfg.R2.Enabled {
			secondary, err := storage.New(storage.Config{
				Enabled:         true,
				Endpoint:        cfg.R2.Endpoint,
				Region:          cfg.R2.Region,
				Bucket:          cfg.R2.Bucket,
				AccessKeyID:     cfg.R2.AccessKeyID,
				SecretAccessKey: cfg.R2.SecretAccessKey,
				PathStyle:       cfg.R2.PathStyle,
			})
			if err != nil {
				log.Fatalf("create secondary object store: %v", err)
			}
			secondaryDeleter = secondary
			log.Printf("flick-worker secondary object backend enabled: bucket %s", cfg.R2.Bucket)
		}
	}
	cleanupHandler, err := worker.NewCleanupHandler(cleanupClient, objectDeleter, secondaryDeleter)
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
