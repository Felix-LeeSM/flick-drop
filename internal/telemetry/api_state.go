package telemetry

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// apiStateScrapeTimeout bounds the database reads one /metrics scrape runs
// for the API-state gauges, so a slow database cannot hang the scrape.
const apiStateScrapeTimeout = 2 * time.Second

// APIStateQueries reads the API-state gauges from the flick_api database.
// The gauges used to be in-process counters that each replica moved on its
// own writes, so two replicas reported two different, drifting numbers; read
// at scrape time from the shared database, every replica reports the same
// cluster-wide value (dashboards aggregate with max(), not sum()).
type APIStateQueries struct {
	// PendingUploads counts pending_upload secrets
	// (internal/secrets/store.go:Store.CountPendingUploads).
	PendingUploads func(context.Context) (int, error)
	// OutboxPending counts outbox events still waiting to be published
	// (internal/events/outbox.go:OutboxStore.CountPending).
	OutboxPending func(context.Context) (int, error)
	// StorageBytesUsed sums object bytes per storage backend
	// (internal/db/storage_usage.go:StorageObjectBytes).
	StorageBytesUsed func(context.Context) (map[string]int64, error)
}

var (
	activeUploadsDesc = prometheus.NewDesc(
		"flick_active_uploads",
		"Pending large-secret uploads awaiting /finalize, read from the flick_api database at scrape time.",
		nil, nil,
	)
	outboxPendingDesc = prometheus.NewDesc(
		"flick_outbox_pending",
		"Outbox events waiting to be published, read from the flick_api database at scrape time.",
		nil, nil,
	)
	storageBytesUsedDesc = prometheus.NewDesc(
		"flick_storage_bytes_used",
		"Object bytes per backend, aggregated from the flick_api database at scrape time.",
		[]string{labelStorage}, nil,
	)
)

type apiStateCollector struct {
	queries APIStateQueries
}

// RegisterAPIStateCollector adds the API-state gauges to DefaultRegistry.
// Only the API process calls it (cmd/flick-api/main.go); the worker has no
// access to the flick_api database.
func RegisterAPIStateCollector(queries APIStateQueries) {
	DefaultRegistry.MustRegister(apiStateCollector{queries: queries})
}

func (c apiStateCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- activeUploadsDesc
	ch <- outboxPendingDesc
	ch <- storageBytesUsedDesc
}

// Collect reads each gauge from the database. A failed read drops only that
// series from this scrape (and logs it), so a database blip shows as a gap on
// one panel instead of failing the whole /metrics response.
func (c apiStateCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), apiStateScrapeTimeout)
	defer cancel()

	if n, err := c.queries.PendingUploads(ctx); err != nil {
		slog.Warn("metrics scrape could not read gauge", "metric", "flick_active_uploads", "error", err)
	} else {
		ch <- prometheus.MustNewConstMetric(activeUploadsDesc, prometheus.GaugeValue, float64(n))
	}
	if n, err := c.queries.OutboxPending(ctx); err != nil {
		slog.Warn("metrics scrape could not read gauge", "metric", "flick_outbox_pending", "error", err)
	} else {
		ch <- prometheus.MustNewConstMetric(outboxPendingDesc, prometheus.GaugeValue, float64(n))
	}
	usage, err := c.queries.StorageBytesUsed(ctx)
	if err != nil {
		slog.Warn("metrics scrape could not read gauge", "metric", "flick_storage_bytes_used", "error", err)
		return
	}
	// Keys are the configured backend names only (db.StorageObjectBytes), so
	// label cardinality is bounded by FLICK_STORAGE_BACKENDS.
	for backend, total := range usage {
		ch <- prometheus.MustNewConstMetric(storageBytesUsedDesc, prometheus.GaugeValue, float64(total), backend)
	}
}
