package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/Felix-LeeSM/burn-links/internal/config"
	"github.com/Felix-LeeSM/burn-links/internal/db"
	"github.com/Felix-LeeSM/burn-links/internal/events"
	"github.com/Felix-LeeSM/burn-links/internal/worker"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
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
	cleanupHandler, err := worker.NewCleanupHandler(cleanupClient)
	if err != nil {
		log.Fatalf("create cleanup handler: %v", err)
	}
	processor, err := worker.NewProcessor(receiptStore, cleanupHandler, worker.ProcessorOptions{})
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

	log.Printf("burnlink-worker consuming subject %s from stream %s", cfg.NATSJobSubject, cfg.NATSStream)
	if err := runner.Run(ctx); err != nil {
		log.Fatalf("run worker: %v", err)
	}
	log.Print("burnlink-worker stopped")
}
