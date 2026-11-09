package httpapi

import (
	"net/http"
	"strconv"
	"time"
)

// statsAuth guards /api/stats behind a bearer token separate from the metrics
// and internal tokens (least privilege, same pattern as metricsAuth). Missing
// token, wrong token, and an empty configured token (FLICK_STATS_TOKEN) all
// answer 404: the endpoint fails closed and hides its own existence.
func (s Server) statsAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.checkBearer(r.Header.Get("Authorization"), s.statsToken) {
			writeError(w, http.StatusNotFound, "not_found", "resource not found")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// getStats serves the admin usage report: a zero-filled daily counter series,
// all-time totals, and live inventory counts. The response carries counts
// only — the usage_counters table holds no IDs, IPs, or content metadata.
func (s Server) getStats(w http.ResponseWriter, r *http.Request) {
	if s.usageStore == nil {
		// A router built without a report store (tests, tooling) behaves exactly
		// like a deployment with the token unset: the endpoint stays invisible.
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	report, err := s.usageStore.Report(r.Context(), parseStatsDays(r), time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not build the usage report")
		return
	}
	writeJSON(w, http.StatusOK, report)
}

// parseStatsDays reads ?days=. A malformed value falls back to the default
// window instead of erroring: the admin page always sends a valid value, and
// a curl caller benefits from the tolerance. ClampReportDays maps values <=0
// to the default and caps the rest.
func parseStatsDays(r *http.Request) int {
	days, err := strconv.Atoi(r.URL.Query().Get("days"))
	if err != nil {
		return 0
	}
	return days
}
