package httpapi

import "net/http"

type configResponse struct {
	PayloadInlineMaxBytes int64 `json:"payload_inline_max_bytes"`
	MaxFileBytes          int64 `json:"max_file_bytes"`
	PipeRelayEnabled      bool  `json:"pipe_relay_enabled"`
}

// Advisory only: the server re-enforces both limits.
func (s Server) getConfig(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(w, http.StatusOK, configResponse{
		PayloadInlineMaxBytes: s.advertisedInlineMaxBytes,
		MaxFileBytes:          s.maxFileBytes,
		PipeRelayEnabled:      s.pipeCoordinator != nil,
	})
}
