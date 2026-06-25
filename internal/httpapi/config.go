package httpapi

import "net/http"

type configResponse struct {
	PayloadInlineMaxBytes int64 `json:"payload_inline_max_bytes"`
	MaxFileBytes          int64 `json:"max_file_bytes"`
}

// getConfig exposes the client-facing size limits so the browser can size-gate
// file uploads and route large files to the S3 path (ciphertext omitted ->
// presigned POST -> /finalize). The values are advisory: the server re-enforces
// both limits, so a tampered client value cannot bypass them. Kept flat so
// future fields (e.g. TTL bounds) can be added non-destructively.
func (s Server) getConfig(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(w, http.StatusOK, configResponse{
		PayloadInlineMaxBytes: s.payloadInlineMaxBytes,
		MaxFileBytes:          s.maxFileBytes,
	})
}
