// Metrics are process-global Prometheus instruments, registered exactly once
// per process through DefaultRegistry. They carry only safe cardinality labels
// (kind, storage backend, job kind, outcome, reason) — never IDs, ciphertext,
// passphrases, or derived keys, per docs/architecture/security-model.md
// ("Logs and metrics must not include plaintext, passphrases, derived keys, or
// ciphertext bodies").
package telemetry

import (
	"net/http"

	"github.com/Felix-LeeSM/flick-drop/internal/storage"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// DefaultRegistry is the single Prometheus registry a process serves on
// /metrics. Today only the API process mounts /metrics; the worker process
// does not yet run an HTTP server, so worker-side counters (e.g.
// flick_jobs_processed_total) are not currently observable until the worker
// exposes its own /metrics.
var DefaultRegistry = prometheus.NewRegistry()

// metricLabels bounds the label cardinality the gate will ever see. Adding a
// label value without extending these would surface as an unbounded series.
const (
	labelKindStorageKind = "kind"
	labelStorage         = "storage"
	labelJobKind         = "kind"
	labelOutcome         = "outcome"
	labelReason          = "reason"
)

var (
	// SecretCreated counts secrets created, partitioned by content kind and
	// storage backend so inline-vs-S3 and text-vs-file volume is visible.
	SecretCreated = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "flick_secret_created_total",
			Help: "Secrets created, by content kind and storage backend.",
		},
		[]string{labelKindStorageKind, labelStorage},
	)

	// SecretOpened counts successful one-time opens (OpenTx returning a payload).
	// Failed access attempts are not counted here: they are an attack signal, not
	// a delivery signal, and surfacing them as a counter would invite abuse.
	SecretOpened = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "flick_secret_opened_total",
			Help: "Secrets successfully opened (consumed).",
		},
	)

	// SecretReaped counts rows hard-deleted by the expiration reaper, by why
	// they were reclaimable: "expired" (active past TTL) or "orphan"
	// (pending_upload never finalized). The two classes share a batch slot, so
	// this label shows which path dominates.
	SecretReaped = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "flick_secret_reaped_total",
			Help: "Secrets hard-deleted by the expiration reaper, by reason.",
		},
		[]string{labelReason},
	)

	// JobsProcessed counts worker jobs by kind and terminal outcome. "succeeded"
	// and "failed" are per-attempt; "dead" marks a job that exhausted retries and
	// entered the dead-letter table.
	JobsProcessed = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "flick_jobs_processed_total",
			Help: "Worker jobs processed, by kind and outcome.",
		},
		[]string{labelJobKind, labelOutcome},
	)

	// ActiveUploads gauges pending_upload secrets: staged by /api/secrets
	// (CreateLarge) but not yet confirmed by /finalize. It is Inc'd on stage and
	// Dec'd on both finalize-activate and reaper orphan reclaim, so it never
	// drifts as long as both paths are wired.
	ActiveUploads = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "flick_active_uploads",
			Help: "Pending large-secret uploads awaiting /finalize.",
		},
	)

	// StorageBytesUsed gauges each backend's cumulative object bytes as
	// aggregated from api.db (see db.StorageObjectBytes) — not a bucket scan.
	// The placement checks in requests.Reserve and secrets.CreateLarge refresh
	// it alongside every usage read, and process wiring seeds it at boot, so
	// scrapes see the same numbers the ceiling enforcement uses.
	StorageBytesUsed = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "flick_storage_bytes_used",
			Help: "Object bytes per backend, aggregated from api.db.",
		},
		[]string{labelStorage},
	)
)

func init() {
	DefaultRegistry.MustRegister(
		SecretCreated,
		SecretOpened,
		SecretReaped,
		JobsProcessed,
		ActiveUploads,
		StorageBytesUsed,
	)
}

// MetricsHandler serves the Prometheus text exposition format for
// DefaultRegistry. Mounted on /metrics by the API process (see
// internal/httpapi); the worker does not yet expose /metrics.
func MetricsHandler() http.Handler {
	return promhttp.HandlerFor(DefaultRegistry, promhttp.HandlerOpts{})
}

// SetStorageBytesUsed publishes the per-backend usage aggregates a ceiling
// check just read (see db.StorageObjectBytes). Keys are storage_backend
// column values; unknown keys would mint unbounded label series and are
// dropped.
func SetStorageBytesUsed(usage map[string]int64) {
	for backend, total := range usage {
		switch backend {
		case storage.PrimaryBackend, storage.SecondaryBackend:
			StorageBytesUsed.WithLabelValues(backend).Set(float64(total))
		}
	}
}
