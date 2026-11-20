package telemetry

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const apiStateScrapeTimeout = 2 * time.Second

// Read at scrape time from the shared database so every replica reports the same
// cluster-wide value (dashboards aggregate with max(), not sum()).
type APIStateQueries struct {
	PendingUploads   func(context.Context) (int, error)
	OutboxPending    func(context.Context) (int, error)
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

// Only the API process calls it; the worker has no access to flick_api.
func RegisterAPIStateCollector(queries APIStateQueries) {
	DefaultRegistry.MustRegister(apiStateCollector{queries: queries})
}

func (c apiStateCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- activeUploadsDesc
	ch <- outboxPendingDesc
	ch <- storageBytesUsedDesc
}

// A failed read drops only that series from this scrape.
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
	// Keys are configured backend names only, bounding label cardinality.
	for backend, total := range usage {
		ch <- prometheus.MustNewConstMetric(storageBytesUsedDesc, prometheus.GaugeValue, float64(total), backend)
	}
}
