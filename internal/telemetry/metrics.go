// Metrics are process-global Prometheus instruments, registered exactly once
// per process through DefaultRegistry. They carry only safe cardinality labels
// (kind, storage backend, job kind, outcome, reason) — never IDs, ciphertext,
// passphrases, or derived keys, per docs/architecture/security-model.md
// ("Logs and metrics must not include plaintext, passphrases, derived keys, or
// ciphertext bodies").
package telemetry

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// DefaultRegistry is the single Prometheus registry a process serves on
// /metrics. The API and worker each have their own process, so each serves
// only the instruments it increments.
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
	// drifts as long as both paths are wired. A bucket-size gauge is intentionally
	// omitted: computing it needs a full S3 ListObjectsV2 scan on every scrape,
	// which is too costly for a health endpoint — deferred until a dedicated
	// inventory sweep is justified.
	ActiveUploads = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "flick_active_uploads",
			Help: "Pending large-secret uploads awaiting /finalize.",
		},
	)
)

func init() {
	DefaultRegistry.MustRegister(
		SecretCreated,
		SecretOpened,
		SecretReaped,
		JobsProcessed,
		ActiveUploads,
	)
}

// MetricsHandler serves the Prometheus text exposition format for
// DefaultRegistry. Mounted on /metrics by the API and worker.
func MetricsHandler() http.Handler {
	return promhttp.HandlerFor(DefaultRegistry, promhttp.HandlerOpts{})
}
