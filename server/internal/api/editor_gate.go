package api

import (
	"context"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"

	"moesekai/server/internal/editorgate"
)

const loadedProducerStateHeader = "X-Moe-Loaded-Producer-State"

type acceptedEditorStatusContextKey struct{}

func withAcceptedEditorStatus(r *http.Request, status editorgate.Status) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), acceptedEditorStatusContextKey{}, status))
}

func acceptedEditorStatus(r *http.Request) (editorgate.Status, bool) {
	status, ok := r.Context().Value(acceptedEditorStatusContextKey{}).(editorgate.Status)
	return status, ok
}

func (s *Server) handleEditorGateStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, s.editorGate.Status())
}

func (s *Server) strictContentMutation(next http.HandlerFunc) http.HandlerFunc {
	return s.strictEditorMutation(func(w http.ResponseWriter, r *http.Request) {
		release, err := s.store.LockContentSharedContext(r.Context())
		if err != nil {
			writeErr(w, http.StatusServiceUnavailable, "request canceled")
			return
		}
		defer release()
		next(w, r)
	})
}

func (s *Server) strictEditorMutation(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		instanceID, generation, ok := parseLoadedProducerState(w, r)
		if !ok {
			return
		}
		release, status, current := s.editorGate.BeginStrictEditor(instanceID, generation)
		if !current {
			writeJSON(w, http.StatusConflict, status)
			return
		}
		defer release()
		next(w, withAcceptedEditorStatus(r, status))
	}
}

func parseLoadedProducerState(w http.ResponseWriter, r *http.Request) (string, uint64, bool) {
	values := r.Header.Values(loadedProducerStateHeader)
	if len(values) == 0 {
		writeErr(w, http.StatusPreconditionRequired, "loaded producer state required")
		return "", 0, false
	}
	if len(values) != 1 {
		writeErr(w, http.StatusBadRequest, "invalid loaded producer state")
		return "", 0, false
	}
	parts := strings.Split(values[0], ":")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		writeErr(w, http.StatusBadRequest, "invalid loaded producer state")
		return "", 0, false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(decoded) == 0 || base64.RawURLEncoding.EncodeToString(decoded) != parts[0] {
		writeErr(w, http.StatusBadRequest, "invalid loaded producer state")
		return "", 0, false
	}
	completedGeneration, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil || completedGeneration > editorgate.MaxSafeCounter || strconv.FormatUint(completedGeneration, 10) != parts[1] {
		writeErr(w, http.StatusBadRequest, "invalid loaded producer state")
		return "", 0, false
	}
	return parts[0], completedGeneration, true
}
