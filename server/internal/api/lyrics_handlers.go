package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"moesekai/server/internal/lyricssource"
	"moesekai/server/internal/model"
	"moesekai/server/internal/store"
)

func (s *Server) handleCatalogMusic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(query) > 200 {
		writeContractError(w, http.StatusBadRequest, "invalid_query", []string{"q exceeds 200 characters"}, nil)
		return
	}
	newlyWritten := true
	if raw := r.URL.Query().Get("newlyWritten"); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			writeContractError(w, http.StatusBadRequest, "invalid_query", []string{"newlyWritten must be true or false"}, nil)
			return
		}
		newlyWritten = value
	}
	limit, cursor, ok := pageParams(w, r)
	if !ok {
		return
	}
	result, err := s.store.CatalogMusic(query, newlyWritten, limit, cursor)
	if err != nil {
		writeContractError(w, http.StatusInternalServerError, "internal_error", nil, nil)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleCatalogCharacters(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	result, err := s.store.CatalogPerformers()
	if err != nil {
		writeContractError(w, http.StatusInternalServerError, "internal_error", nil, nil)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleLyricsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	limit, cursor, ok := pageParams(w, r)
	if !ok {
		return
	}
	result, err := s.store.ListLyrics(limit, cursor)
	if err != nil {
		writeContractError(w, http.StatusInternalServerError, "internal_error", nil, nil)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleLyricsDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	musicID, ok := positiveIntQuery(w, r, "musicId")
	if !ok {
		return
	}
	result, err := s.store.GetLyrics(musicID)
	if err == store.ErrLyricsNotFound {
		writeContractError(w, http.StatusNotFound, "not_found", nil, nil)
		return
	}
	if err != nil {
		writeContractError(w, http.StatusInternalServerError, "internal_error", nil, nil)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleLyricsSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var request model.SongLyrics
	if !decodeBody(w, r, &request) {
		return
	}
	result, err := s.store.SaveLyrics(request, currentUser(r))
	if err != nil {
		writeLyricsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleLyricsPublish(w http.ResponseWriter, r *http.Request) {
	s.handleLyricsPublication(w, r, true)
}

func (s *Server) handleLyricsUnpublish(w http.ResponseWriter, r *http.Request) {
	s.handleLyricsPublication(w, r, false)
}

func (s *Server) handleLyricsPublication(w http.ResponseWriter, r *http.Request, publish bool) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var request struct {
		MusicID  int `json:"musicId"`
		Revision int `json:"revision"`
	}
	if !decodeBody(w, r, &request) {
		return
	}
	if request.MusicID <= 0 || request.Revision <= 0 {
		writeContractError(w, http.StatusUnprocessableEntity, "incomplete_publication", []string{"musicId and revision must be positive"}, nil)
		return
	}
	var result model.SongLyrics
	var err error
	if publish {
		result, err = s.store.PublishLyrics(request.MusicID, request.Revision, currentUser(r))
	} else {
		result, err = s.store.UnpublishLyrics(request.MusicID, request.Revision, currentUser(r))
	}
	if err != nil {
		writeLyricsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleLyricsSourceSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	musicID, ok := positiveIntQuery(w, r, "musicId")
	if !ok {
		return
	}
	identity, ok := s.lyricsSourceIdentity(w, musicID)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	items, err := s.lyricsSrc.Search(ctx, identity)
	if err != nil {
		writeLyricsSourceError(w, err)
		return
	}
	if items == nil {
		items = []lyricssource.Candidate{}
	}
	if err := s.store.RecordAudit(currentUser(r), "lyrics.source.search", fmt.Sprintf("musicId=%d candidates=%d", musicID, len(items))); err != nil {
		writeContractError(w, http.StatusInternalServerError, "internal_error", nil, nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleLyricsSourcePreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var request struct {
		MusicID    int `json:"musicId"`
		PageID     int `json:"pageId"`
		RevisionID int `json:"revisionId"`
	}
	if !decodeBody(w, r, &request) {
		return
	}
	if request.MusicID <= 0 || request.PageID <= 0 || request.RevisionID <= 0 {
		writeContractError(w, http.StatusBadRequest, "invalid_source_request",
			[]string{"musicId, pageId, and revisionId must be positive integers"}, nil)
		return
	}
	identity, ok := s.lyricsSourceIdentity(w, request.MusicID)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	preview, err := s.lyricsSrc.Preview(ctx, identity, request.PageID, request.RevisionID)
	if err != nil {
		writeLyricsSourceError(w, err)
		return
	}
	if err := s.store.RecordAudit(currentUser(r), "lyrics.source.preview",
		fmt.Sprintf("musicId=%d pageId=%d revisionId=%d", request.MusicID, request.PageID, request.RevisionID)); err != nil {
		writeContractError(w, http.StatusInternalServerError, "internal_error", nil, nil)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

func (s *Server) lyricsSourceIdentity(w http.ResponseWriter, musicID int) (lyricssource.MusicIdentity, bool) {
	identity, err := s.store.CatalogMusicIdentity(musicID)
	if err == sql.ErrNoRows {
		writeContractError(w, http.StatusNotFound, "not_found", nil, nil)
		return lyricssource.MusicIdentity{}, false
	}
	if err != nil {
		writeContractError(w, http.StatusInternalServerError, "internal_error", nil, nil)
		return lyricssource.MusicIdentity{}, false
	}
	if strings.TrimSpace(identity.ProducerMetadata) == "" {
		writeContractError(w, http.StatusUnprocessableEntity, "source_identity_incomplete",
			[]string{"catalog producer metadata is required before source matching"}, nil)
		return lyricssource.MusicIdentity{}, false
	}
	return lyricssource.MusicIdentity{
		MusicID: identity.MusicID, JapaneseTitle: identity.JapaneseTitle,
		ProducerMetadata: identity.ProducerMetadata,
	}, true
}

func writeLyricsSourceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, lyricssource.ErrRevisionChanged):
		writeContractError(w, http.StatusUnprocessableEntity, "source_drift",
			[]string{"the selected source revision changed"}, nil)
	case errors.Is(err, lyricssource.ErrAmbiguous):
		writeContractError(w, http.StatusUnprocessableEntity, "source_identity_mismatch",
			[]string{"the selected page no longer matches the catalog identity"}, nil)
	case errors.Is(err, lyricssource.ErrRestrictedReprint):
		writeContractError(w, http.StatusUnprocessableEntity, "source_restricted",
			[]string{"the source page prohibits reprints"}, nil)
	case errors.Is(err, lyricssource.ErrMissingLyrics), errors.Is(err, lyricssource.ErrUnsupportedTable):
		writeContractError(w, http.StatusUnprocessableEntity, "source_unsupported",
			[]string{"the source lyrics cannot be extracted safely"}, nil)
	default:
		writeContractError(w, http.StatusBadGateway, "source_unavailable", nil, nil)
	}
}

func writeLyricsError(w http.ResponseWriter, err error) {
	var contractErr *store.LyricsContractError
	if errors.As(err, &contractErr) {
		status := http.StatusUnprocessableEntity
		if contractErr.Code == "revision_conflict" {
			status = http.StatusConflict
		}
		writeContractError(w, status, contractErr.Code, contractErr.Details, contractErr.Current)
		return
	}
	if err == store.ErrLyricsNotFound || err == sql.ErrNoRows {
		writeContractError(w, http.StatusNotFound, "not_found", nil, nil)
		return
	}
	writeContractError(w, http.StatusInternalServerError, "internal_error", nil, nil)
}

func writeContractError(w http.ResponseWriter, status int, code string, details []string, current *model.SongLyrics) {
	body := map[string]any{"error": code}
	if len(details) > 0 {
		body["details"] = details
	}
	if current != nil {
		body["current"] = current
	}
	writeJSON(w, status, body)
}

func positiveIntQuery(w http.ResponseWriter, r *http.Request, key string) (int, bool) {
	value, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil || value <= 0 {
		writeContractError(w, http.StatusBadRequest, "invalid_query", []string{key + " must be a positive integer"}, nil)
		return 0, false
	}
	return value, true
}

func pageParams(w http.ResponseWriter, r *http.Request) (int, int, bool) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 100 {
			writeContractError(w, http.StatusBadRequest, "invalid_query", []string{"limit must be between 1 and 100"}, nil)
			return 0, 0, false
		}
		limit = value
	}
	cursor := 0
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			writeContractError(w, http.StatusBadRequest, "invalid_query", []string{"cursor must be a non-negative musicId"}, nil)
			return 0, 0, false
		}
		cursor = value
	}
	return limit, cursor, true
}
