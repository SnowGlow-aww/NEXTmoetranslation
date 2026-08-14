package api

import (
	"context"
	"net/http"
	"time"

	"moesekai/server/internal/translator"
)

const eventAssociationRequestTimeout = 15 * time.Second

// handleEventAssociations exposes stable masterdata relations used only for
// activity-name filtering in the console. It never mutates translation data.
func (s *Server) handleEventAssociations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s.translator == nil {
		writeJSON(w, http.StatusOK, translator.EventAssociationIndex{Categories: map[string]map[string][]int{}})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), eventAssociationRequestTimeout)
	defer cancel()
	index, err := s.translator.EventAssociations(ctx)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "event associations unavailable")
		return
	}
	writeJSON(w, http.StatusOK, index)
}
