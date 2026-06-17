package main

import (
	"context"
	"log"
	"net/http"

	"github.com/Felix-LeeSM/burn-links/internal/config"
	"github.com/Felix-LeeSM/burn-links/internal/db"
	"github.com/Felix-LeeSM/burn-links/internal/httpapi"
	"github.com/Felix-LeeSM/burn-links/internal/secrets"
)

func main() {
	ctx := context.Background()

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
		AllowedTTLSeconds:     cfg.AllowedTTLSeconds,
	})
	if err != nil {
		log.Fatalf("create secret store: %v", err)
	}

	server := &http.Server{
		Addr: cfg.APIAddr,
		Handler: httpapi.NewRouter(conn, secretStore, httpapi.Options{
			PayloadInlineMaxBytes: cfg.PayloadInlineMaxBytes,
			AllowedOrigin:         cfg.PublicBaseURL,
			InternalToken:         cfg.InternalToken,
		}),
	}

	log.Printf("burnlink-api listening on %s", cfg.APIAddr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("serve api: %v", err)
	}
}
