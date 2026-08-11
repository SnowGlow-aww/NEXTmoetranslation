package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"strings"

	"moesekai/server/internal/legacy"

	"moesekai/server/internal/model"
)

func createLyricsSourceReviewTx(ctx context.Context, tx *sql.Tx, params createLyricsSourceReviewParams) (model.LyricsSourceReviewItem, bool, error) {
	provider, err := canonicalLyricsSourceProvider(params.Provider)
	if err != nil {
		return model.LyricsSourceReviewItem{}, false, err
	}
	if params.Kind != LyricsSourceReviewKindCandidate && params.Kind != LyricsSourceReviewKindArtifact {
		return model.LyricsSourceReviewItem{}, false, errors.New("invalid lyrics source review kind")
	}
	if params.MusicID <= 0 || !lyricsDiscoveryFingerprintPattern.MatchString(params.CatalogFingerprint) ||
		params.Priority < -1000 || params.Priority > 1000 || params.CreatedAt.IsZero() {
		return model.LyricsSourceReviewItem{}, false, errors.New("invalid lyrics source review identity")
	}
	if params.Kind == LyricsSourceReviewKindCandidate && params.AnalysisID != 0 ||
		params.Kind == LyricsSourceReviewKindArtifact && params.AnalysisID <= 0 {
		return model.LyricsSourceReviewItem{}, false, errors.New("invalid lyrics source review analysis")
	}
	if len(params.EvidenceJSON) < 2 || len(params.EvidenceJSON) > maxLyricsReviewEvidenceBytes || legacy.ValidateUniqueJSON(params.EvidenceJSON) != nil {
		return model.LyricsSourceReviewItem{}, false, errors.New("invalid lyrics source review evidence")
	}
	var evidenceObject map[string]json.RawMessage
	if json.Unmarshal(params.EvidenceJSON, &evidenceObject) != nil || evidenceObject == nil {
		return model.LyricsSourceReviewItem{}, false, errors.New("lyrics source review evidence must be an object")
	}
	reason := strings.TrimSpace(params.ReasonCode)
	if reason == "" || len(reason) > 128 || !lyricsDiscoveryErrorCodePattern.MatchString(reason) {
		return model.LyricsSourceReviewItem{}, false, errors.New("invalid lyrics source review reason")
	}
	createdAt := canonicalLyricsDiscoveryTime(params.CreatedAt)
	evidenceHash := sha256.Sum256(params.EvidenceJSON)
	domainPayload := fmt.Sprintf("v1\x00%s\x00%d\x00%d\x00%s\x00%s\x00%s\x00%s", params.Kind, params.AnalysisID,
		params.MusicID, params.CatalogFingerprint, model.LyricsReviewPolicyVersion, reason, hex.EncodeToString(evidenceHash[:]))
	if provider != model.LyricsSourceProviderVocaloidFandom {
		domainPayload = fmt.Sprintf("v2\x00%s\x00%s\x00%d\x00%d\x00%s\x00%s\x00%s\x00%s", provider, params.Kind,
			params.AnalysisID, params.MusicID, params.CatalogFingerprint, model.LyricsReviewPolicyVersion, reason,
			hex.EncodeToString(evidenceHash[:]))
	}
	domainHash := sha256.Sum256([]byte(domainPayload))
	domainKey := hex.EncodeToString(domainHash[:])

	// A current catalog generation never silently reuses an older pending item.
	if _, err := tx.ExecContext(ctx, `UPDATE lyrics_source_review_items
		SET state='superseded', completed_at=?, updated_at=?, version=version+1
		WHERE provider=? AND kind=? AND music_id=? AND state='pending' AND catalog_fingerprint<>?`,
		createdAt.UnixMilli(), createdAt.UnixMilli(), provider, params.Kind, params.MusicID, params.CatalogFingerprint); err != nil {
		return model.LyricsSourceReviewItem{}, false, err
	}
	identityGate, sourceUseGate, parseGate := "not_applicable", "not_applicable", "not_applicable"
	if params.Kind == LyricsSourceReviewKindArtifact {
		identityGate, sourceUseGate, parseGate = "pending", "pending", "pending"
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO lyrics_source_review_items
		(domain_key, kind, analysis_id, music_id, catalog_fingerprint, review_policy_version, reason_code,
		 evidence_json, state, identity_gate, source_use_gate, parse_gate, version, priority, created_at, updated_at, provider)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?, 1, ?, ?, ?, ?)
		ON CONFLICT(domain_key) DO NOTHING`, domainKey, params.Kind, nullablePositiveInt64(params.AnalysisID), params.MusicID,
		params.CatalogFingerprint, model.LyricsReviewPolicyVersion, reason, string(params.EvidenceJSON), identityGate,
		sourceUseGate, parseGate, params.Priority, createdAt.UnixMilli(), createdAt.UnixMilli(), provider)
	if err != nil {
		return model.LyricsSourceReviewItem{}, false, err
	}
	created, _ := result.RowsAffected()
	item, err := loadLyricsSourceReviewItemContext(ctx, tx, `WHERE domain_key=?`, domainKey)
	return item, created == 1, err
}

func invalidLyricsReviewRequest(message string) error {
	return fmt.Errorf("%w: %s", ErrLyricsSourceInvalidRequest, message)
}

func (s *Store) ListLyricsSourceReviews(ctx context.Context, filter LyricsSourceReviewFilter) (LyricsSourceReviewPage, error) {
	if ctx == nil {
		return LyricsSourceReviewPage{}, errors.New("lyrics source review list requires context")
	}
	limit := filter.Limit
	if !filter.LimitSet && limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 || !validLyricsSourceReviewFilter(filter.Kind, filter.State, filter.Gate) {
		return LyricsSourceReviewPage{}, invalidLyricsReviewRequest("invalid filter")
	}
	query := `SELECT r.review_id, r.kind, r.state, r.music_id, COALESCE(c.title_ja,''), r.catalog_fingerprint,
		r.reason_code, r.identity_gate, r.source_use_gate, r.parse_gate, r.version, r.priority,
		r.created_at, r.updated_at, r.completed_at
		FROM lyrics_source_review_items r LEFT JOIN catalog_music c ON c.music_id=r.music_id WHERE 1=1`
	args := []any{}
	if filter.Kind != "" {
		query += ` AND r.kind=?`
		args = append(args, filter.Kind)
	}
	if filter.State != "" {
		query += ` AND r.state=?`
		args = append(args, filter.State)
	}
	if filter.Gate != "" {
		if filter.Gate == LyricsSourceReviewGateOverall {
			query += ` AND r.kind='artifact_review' AND r.state='pending'`
		} else {
			column := map[string]string{LyricsSourceReviewGateIdentity: "identity_gate", LyricsSourceReviewGateSourceUse: "source_use_gate", LyricsSourceReviewGateParse: "parse_gate"}[filter.Gate]
			query += ` AND r.` + column + `='pending'`
		}
	}
	if filter.Cursor != "" {
		cursor, err := decodeLyricsReviewCursor(filter.Cursor)
		if err != nil {
			return LyricsSourceReviewPage{}, err
		}
		query += ` AND (r.priority<? OR (r.priority=? AND r.review_id>?))`
		args = append(args, cursor.Priority, cursor.Priority, cursor.ReviewID)
	}
	query += ` ORDER BY r.priority DESC, r.review_id ASC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return LyricsSourceReviewPage{}, err
	}
	defer rows.Close()
	items := make([]LyricsSourceReviewSummary, 0, limit+1)
	for rows.Next() {
		item, err := scanLyricsSourceReviewSummary(rows)
		if err != nil {
			return LyricsSourceReviewPage{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return LyricsSourceReviewPage{}, err
	}
	page := LyricsSourceReviewPage{Items: items}
	if len(items) > limit {
		last := items[limit-1]
		page.Items = items[:limit]
		page.NextCursor, _ = encodeLyricsReviewCursor(lyricsReviewCursor{Priority: last.Priority, ReviewID: last.ReviewID})
	}
	return page, nil
}

func validLyricsSourceReviewFilter(kind, state, gate string) bool {
	if kind != "" && kind != LyricsSourceReviewKindCandidate && kind != LyricsSourceReviewKindArtifact {
		return false
	}
	switch state {
	case "", "pending", "approved", "rejected", "superseded", "cancelled":
	default:
		return false
	}
	switch gate {
	case "", LyricsSourceReviewGateIdentity, LyricsSourceReviewGateSourceUse, LyricsSourceReviewGateParse, LyricsSourceReviewGateOverall:
		return true
	default:
		return false
	}
}

func encodeLyricsReviewCursor(cursor lyricsReviewCursor) (string, error) {
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeLyricsReviewCursor(value string) (lyricsReviewCursor, error) {
	if len(value) > 256 {
		return lyricsReviewCursor{}, invalidLyricsReviewRequest("invalid cursor")
	}
	payload, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || legacy.ValidateUniqueJSON(payload) != nil {
		return lyricsReviewCursor{}, invalidLyricsReviewRequest("invalid cursor")
	}
	var cursor lyricsReviewCursor
	if json.Unmarshal(payload, &cursor) != nil || cursor.ReviewID <= 0 || cursor.Priority < -1000 || cursor.Priority > 1000 {
		return lyricsReviewCursor{}, invalidLyricsReviewRequest("invalid cursor")
	}
	canonical, _ := encodeLyricsReviewCursor(cursor)
	if canonical != value {
		return lyricsReviewCursor{}, invalidLyricsReviewRequest("invalid cursor")
	}
	return cursor, nil
}

func (s *Store) GetLyricsSourceReviewDetail(ctx context.Context, reviewID int64) (LyricsSourceReviewDetail, error) {
	if ctx == nil || reviewID <= 0 {
		return LyricsSourceReviewDetail{}, invalidLyricsReviewRequest("positive review ID is required")
	}
	item, err := loadLyricsSourceReviewItemContext(ctx, s.db, `WHERE review_id=?`, reviewID)
	if err != nil {
		return LyricsSourceReviewDetail{}, err
	}
	detail := LyricsSourceReviewDetail{
		Review: summaryFromLyricsSourceReviewItem(item), Candidates: []model.LyricsSourceCandidateIdentity{},
		Associations: []LyricsSourceAssociationFact{}, Decisions: []LyricsSourceReviewDecisionFact{},
	}
	_ = s.db.QueryRowContext(ctx, `SELECT title_ja FROM catalog_music WHERE music_id=?`, item.MusicID).Scan(&detail.Review.Title)
	if item.Kind == LyricsSourceReviewKindCandidate {
		var evidence struct {
			Candidates []model.LyricsSourceCandidateIdentity `json:"candidates"`
		}
		if err := json.Unmarshal(item.EvidenceJSON, &evidence); err != nil || len(evidence.Candidates) > 8 {
			return LyricsSourceReviewDetail{}, errors.New("invalid stored candidate evidence")
		}
		if evidence.Candidates == nil {
			evidence.Candidates = []model.LyricsSourceCandidateIdentity{}
		}
		detail.Candidates = evidence.Candidates
	} else {
		artifact, analysis, err := loadLyricsSourceAnalysisFacts(ctx, s.db.DB, item.AnalysisID)
		if err != nil {
			return LyricsSourceReviewDetail{}, err
		}
		detail.Artifact, detail.Analysis = &artifact, &analysis
		associations, err := loadLyricsSourceAssociationFacts(ctx, s.db.DB, item.AnalysisID)
		if err != nil {
			return LyricsSourceReviewDetail{}, err
		}
		detail.Associations = associations
	}
	decisions, err := loadLyricsSourceDecisionFacts(ctx, s.db.DB, reviewID)
	if err != nil {
		return LyricsSourceReviewDetail{}, err
	}
	detail.Decisions = decisions
	return detail, nil
}
