package api

import (
	"errors"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"moesekai/server/internal/model"
	"moesekai/server/internal/store"
)

type lyricsSourceReviewMutationResponse struct {
	ReviewID      int64  `json:"reviewId"`
	State         string `json:"state"`
	IdentityGate  string `json:"identityGate"`
	SourceUseGate string `json:"sourceUseGate"`
	ParseGate     string `json:"parseGate"`
	Version       int64  `json:"version"`
	Replayed      bool   `json:"replayed"`
}

type lyricsSourceReviewBatchMutationItem struct {
	ReviewID int64  `json:"reviewId"`
	State    string `json:"state"`
	Version  int64  `json:"version"`
}

type lyricsSourceReviewBatchMutationResponse struct {
	Items    []lyricsSourceReviewBatchMutationItem `json:"items"`
	Replayed bool                                  `json:"replayed"`
}

type lyricsSourceReviewImportResponse struct {
	ReviewID int64            `json:"reviewId"`
	Lyrics   model.SongLyrics `json:"lyrics"`
	Changed  bool             `json:"changed"`
}

func (s *Server) handleLyricsSourceReviews(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !lyricsSourceReviewListQueryAllowed(r) {
		writeContractError(w, http.StatusBadRequest, "invalid_query", nil, nil)
		return
	}
	query := r.URL.Query()
	limit := 0
	limitValues, limitSet := query["limit"]
	if limitSet {
		raw := limitValues[0]
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 || strconv.Itoa(parsed) != raw {
			writeContractError(w, http.StatusBadRequest, "invalid_query", nil, nil)
			return
		}
		limit = parsed
	}
	page, err := s.store.ListLyricsSourceReviews(r.Context(), store.LyricsSourceReviewFilter{
		Kind: query.Get("kind"), State: query.Get("state"), Gate: query.Get("gate"),
		Limit: limit, LimitSet: limitSet, Cursor: query.Get("cursor"),
	})
	if err != nil {
		writeLyricsSourceReviewError(w, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleLyricsSourceReviewDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if len(r.URL.Query()) != 1 || len(r.URL.Query()["reviewId"]) != 1 {
		writeContractError(w, http.StatusBadRequest, "invalid_query", nil, nil)
		return
	}
	reviewID, err := strconv.ParseInt(r.URL.Query().Get("reviewId"), 10, 64)
	if err != nil || reviewID <= 0 || strconv.FormatInt(reviewID, 10) != r.URL.Query().Get("reviewId") {
		writeContractError(w, http.StatusBadRequest, "invalid_query", nil, nil)
		return
	}
	detail, err := s.store.GetLyricsSourceReviewDetail(r.Context(), reviewID)
	if err != nil {
		writeLyricsSourceReviewError(w, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handleLyricsSourceReviewDecision(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !requireJSONContentType(w, r) {
		return
	}
	type batchItem struct {
		ReviewID        int64 `json:"reviewId"`
		ExpectedVersion int64 `json:"expectedVersion"`
	}
	var request struct {
		ReviewID        *int64       `json:"reviewId"`
		Gate            string       `json:"gate"`
		Decision        string       `json:"decision"`
		ExpectedVersion *int64       `json:"expectedVersion"`
		Items           *[]batchItem `json:"items"`
		IdempotencyKey  string       `json:"idempotencyKey"`
		Note            string       `json:"note"`
	}
	if !decodeBody(w, r, &request) {
		return
	}
	if request.Note != "" {
		writeContractError(w, http.StatusBadRequest, "invalid_request", nil, nil)
		return
	}
	isSingle := request.ReviewID != nil || request.ExpectedVersion != nil
	isBatch := request.Items != nil
	if isSingle == isBatch {
		writeContractError(w, http.StatusBadRequest, "invalid_request", nil, nil)
		return
	}
	if isBatch {
		items := make([]store.LyricsSourceReviewBatchItem, len(*request.Items))
		for index, item := range *request.Items {
			items[index] = store.LyricsSourceReviewBatchItem{ReviewID: item.ReviewID, ExpectedVersion: item.ExpectedVersion}
		}
		result, err := s.store.DecideLyricsSourceReviewBatch(r.Context(), store.LyricsSourceReviewBatchDecisionParams{
			Gate: request.Gate, Decision: request.Decision, Items: items, Actor: currentUser(r),
			IdempotencyKey: request.IdempotencyKey, Note: request.Note,
		})
		if err != nil {
			writeLyricsSourceReviewBatchError(w, err, result.Conflicts)
			return
		}
		responseItems := make([]lyricsSourceReviewBatchMutationItem, len(result.Items))
		for index, item := range result.Items {
			responseItems[index] = lyricsSourceReviewBatchMutationItem{ReviewID: item.ReviewID, State: item.State, Version: item.Version}
		}
		writeJSON(w, http.StatusOK, lyricsSourceReviewBatchMutationResponse{Items: responseItems, Replayed: result.Replayed})
		return
	}
	if request.ReviewID == nil || request.ExpectedVersion == nil {
		writeContractError(w, http.StatusBadRequest, "invalid_request", nil, nil)
		return
	}
	item, replayed, err := s.store.DecideLyricsSourceReview(r.Context(), store.LyricsSourceReviewDecisionParams{
		ReviewID: *request.ReviewID, Gate: request.Gate, Decision: request.Decision, ExpectedVersion: *request.ExpectedVersion,
		Actor: currentUser(r), IdempotencyKey: request.IdempotencyKey, Note: request.Note,
	})
	if err != nil {
		writeLyricsSourceReviewError(w, err, &item)
		return
	}
	writeJSON(w, http.StatusOK, lyricsSourceReviewMutationDTO(item, replayed))
}

func (s *Server) handleLyricsSourceCandidateSelection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !requireJSONContentType(w, r) {
		return
	}
	var request struct {
		ReviewID          int64                                `json:"reviewId"`
		CandidateIdentity *model.LyricsSourceCandidateIdentity `json:"candidateIdentity"`
		Exclude           bool                                 `json:"exclude"`
		ExpectedVersion   int64                                `json:"expectedVersion"`
		IdempotencyKey    string                               `json:"idempotencyKey"`
		Note              string                               `json:"note"`
	}
	if !decodeBody(w, r, &request) {
		return
	}
	if request.Note != "" {
		writeContractError(w, http.StatusBadRequest, "invalid_request", nil, nil)
		return
	}
	item, replayed, err := s.store.SelectLyricsSourceCandidate(r.Context(), store.LyricsSourceCandidateSelectionParams{
		ReviewID: request.ReviewID, CandidateIdentity: request.CandidateIdentity, Exclude: request.Exclude,
		ExpectedVersion: request.ExpectedVersion, Actor: currentUser(r), IdempotencyKey: request.IdempotencyKey, Note: request.Note,
	})
	if err != nil {
		writeLyricsSourceReviewError(w, err, &item)
		return
	}
	writeJSON(w, http.StatusOK, lyricsSourceReviewMutationDTO(item, replayed))
}

func (s *Server) handleLyricsSourceReviewImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !requireJSONContentType(w, r) {
		return
	}
	var request struct {
		ReviewID int64 `json:"reviewId"`
	}
	if !decodeBody(w, r, &request) {
		return
	}
	if request.ReviewID <= 0 {
		writeContractError(w, http.StatusBadRequest, "invalid_request", nil, nil)
		return
	}
	lyrics, changed, err := s.store.ImportApprovedLyricsSource(r.Context(), request.ReviewID, currentUser(r))
	if err != nil {
		writeLyricsSourceReviewImportError(w, err)
		return
	}
	if changed {
		s.broadcastLyricsUpdated(lyrics, "", currentUser(r))
	}
	writeJSON(w, http.StatusOK, lyricsSourceReviewImportResponse{ReviewID: request.ReviewID, Lyrics: lyrics, Changed: changed})
}

func lyricsSourceReviewListQueryAllowed(r *http.Request) bool {
	allowed := map[string]bool{"kind": true, "state": true, "gate": true, "limit": true, "cursor": true}
	for key, values := range r.URL.Query() {
		if !allowed[key] || len(values) != 1 {
			return false
		}
	}
	return true
}

func requireJSONContentType(w http.ResponseWriter, r *http.Request) bool {
	values := r.Header.Values("Content-Type")
	if len(values) != 1 {
		writeContractError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", nil, nil)
		return false
	}
	mediaType, params, err := mime.ParseMediaType(values[0])
	if err != nil || mediaType != "application/json" {
		writeContractError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", nil, nil)
		return false
	}
	for key, value := range params {
		if !strings.EqualFold(key, "charset") || !strings.EqualFold(value, "utf-8") {
			writeContractError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", nil, nil)
			return false
		}
	}
	return true
}

func lyricsSourceReviewMutationDTO(item model.LyricsSourceReviewItem, replayed bool) lyricsSourceReviewMutationResponse {
	return lyricsSourceReviewMutationResponse{ReviewID: item.ReviewID, State: item.State, IdentityGate: item.IdentityGate,
		SourceUseGate: item.SourceUseGate, ParseGate: item.ParseGate, Version: item.Version, Replayed: replayed}
}

func writeLyricsSourceReviewImportError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrLyricsSourceInvalidRequest):
		writeContractError(w, http.StatusBadRequest, "invalid_request", nil, nil)
	case errors.Is(err, store.ErrLyricsSourceReviewNotFound):
		writeContractError(w, http.StatusNotFound, "not_found", nil, nil)
	case errors.Is(err, store.ErrLyricsSourceReviewNotApproved):
		writeContractError(w, http.StatusUnprocessableEntity, "source_not_approved", nil, nil)
	case errors.Is(err, store.ErrLyricsAlreadySaved):
		writeContractError(w, http.StatusConflict, "lyrics_already_saved", nil, nil)
	case errors.Is(err, store.ErrLyricsSourcePerformerMapping), errors.Is(err, store.ErrLyricsSourceArtifactConflict):
		writeContractError(w, http.StatusUnprocessableEntity, "source_drift", nil, nil)
	default:
		writeLyricsError(w, err)
	}
}

func writeLyricsSourceReviewBatchError(w http.ResponseWriter, err error, conflicts []store.LyricsSourceReviewBatchConflict) {
	switch {
	case errors.Is(err, store.ErrLyricsSourceInvalidRequest):
		writeContractError(w, http.StatusBadRequest, "invalid_request", nil, nil)
	case errors.Is(err, store.ErrLyricsSourceIdempotency):
		writeContractError(w, http.StatusConflict, "idempotency_conflict", nil, nil)
	case errors.Is(err, store.ErrLyricsSourceReviewConflict):
		body := map[string]any{"error": "revision_conflict"}
		if len(conflicts) > 0 {
			body["conflicts"] = conflicts
		}
		writeJSON(w, http.StatusConflict, body)
	default:
		writeContractError(w, http.StatusInternalServerError, "internal_error", nil, nil)
	}
}

func writeLyricsSourceReviewError(w http.ResponseWriter, err error, current *model.LyricsSourceReviewItem) {
	switch {
	case errors.Is(err, store.ErrLyricsSourceInvalidRequest):
		writeContractError(w, http.StatusBadRequest, "invalid_request", nil, nil)
	case errors.Is(err, store.ErrLyricsSourceReviewNotFound):
		writeContractError(w, http.StatusNotFound, "not_found", nil, nil)
	case errors.Is(err, store.ErrLyricsSourceIdempotency):
		writeContractError(w, http.StatusConflict, "idempotency_conflict", nil, nil)
	case errors.Is(err, store.ErrLyricsSourceReviewConflict):
		body := map[string]any{"error": "revision_conflict"}
		if current != nil && current.ReviewID > 0 {
			body["current"] = lyricsSourceReviewMutationDTO(*current, false)
		}
		writeJSON(w, http.StatusConflict, body)
	default:
		writeContractError(w, http.StatusInternalServerError, "internal_error", nil, nil)
	}
}
