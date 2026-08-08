package store

import (
	"context"

	"database/sql"

	"encoding/json"
	"errors"

	"time"

	"moesekai/server/internal/model"
)

type lyricsSourceReviewScanner interface {
	Scan(...any) error
}

func scanLyricsSourceReviewSummary(scanner lyricsSourceReviewScanner) (LyricsSourceReviewSummary, error) {
	var item LyricsSourceReviewSummary
	var createdAt, updatedAt int64
	var completedAt sql.NullInt64
	err := scanner.Scan(&item.ReviewID, &item.Kind, &item.State, &item.MusicID, &item.Title, &item.CatalogFingerprint,
		&item.ReasonCode, &item.IdentityGate, &item.SourceUseGate, &item.ParseGate, &item.Version, &item.Priority,
		&createdAt, &updatedAt, &completedAt)
	if err != nil {
		return LyricsSourceReviewSummary{}, err
	}
	item.CreatedAt, item.UpdatedAt = time.UnixMilli(createdAt).UTC(), time.UnixMilli(updatedAt).UTC()
	if completedAt.Valid {
		item.CompletedAt = time.UnixMilli(completedAt.Int64).UTC()
	}
	return item, nil
}

func summaryFromLyricsSourceReviewItem(item model.LyricsSourceReviewItem) LyricsSourceReviewSummary {
	return LyricsSourceReviewSummary{ReviewID: item.ReviewID, Kind: item.Kind, State: item.State, MusicID: item.MusicID,
		CatalogFingerprint: item.CatalogFingerprint, ReasonCode: item.ReasonCode, IdentityGate: item.IdentityGate,
		SourceUseGate: item.SourceUseGate, ParseGate: item.ParseGate, Version: item.Version, Priority: item.Priority,
		CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt, CompletedAt: item.CompletedAt}
}

type lyricsSourceReviewQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadLyricsSourceReviewItemAtVersion(ctx context.Context, query lyricsSourceReviewQuery, reviewID, resultVersion int64) (model.LyricsSourceReviewItem, error) {
	item, err := loadLyricsSourceReviewItemContext(ctx, query, `WHERE review_id=?`, reviewID)
	if err != nil {
		return item, err
	}
	if item.Version < resultVersion {
		return model.LyricsSourceReviewItem{}, ErrLyricsSourceArtifactConflict
	}
	if item.Version == resultVersion {
		return item, nil
	}
	item.State = LyricsSourceReviewStatePending
	if item.Kind == LyricsSourceReviewKindCandidate {
		item.IdentityGate, item.SourceUseGate, item.ParseGate = "not_applicable", "not_applicable", "not_applicable"
	} else {
		item.IdentityGate, item.SourceUseGate, item.ParseGate = "pending", "pending", "pending"
	}
	item.CompletedAt = time.Time{}
	rows, err := query.QueryContext(ctx, `SELECT gate, decision, result_version, decided_at
		FROM lyrics_source_review_decisions WHERE review_id=? AND result_version<=? ORDER BY result_version`, reviewID, resultVersion)
	if err != nil {
		return model.LyricsSourceReviewItem{}, err
	}
	defer rows.Close()
	seen := false
	for rows.Next() {
		var gate, decision string
		var version, decidedAt int64
		if err := rows.Scan(&gate, &decision, &version, &decidedAt); err != nil {
			return model.LyricsSourceReviewItem{}, err
		}
		seen = true
		item.Version = version
		item.UpdatedAt = time.UnixMilli(decidedAt).UTC()
		if gate == "candidate" {
			if decision == "selected" {
				item.State = LyricsSourceReviewStateApproved
			} else {
				item.State = LyricsSourceReviewStateRejected
			}
			item.CompletedAt = item.UpdatedAt
			continue
		}
		if gate == LyricsSourceReviewGateOverall {
			item.IdentityGate, item.SourceUseGate, item.ParseGate = decision, decision, decision
		} else {
			setReviewGate(&item, gate, decision)
		}
		if decision == LyricsSourceReviewDecisionRejected {
			item.State, item.CompletedAt = LyricsSourceReviewStateRejected, item.UpdatedAt
		} else if item.IdentityGate == "approved" && item.SourceUseGate == "approved" && item.ParseGate == "approved" {
			item.State, item.CompletedAt = LyricsSourceReviewStateApproved, item.UpdatedAt
		}
	}
	if err := rows.Err(); err != nil {
		return model.LyricsSourceReviewItem{}, err
	}
	if !seen || item.Version != resultVersion {
		return model.LyricsSourceReviewItem{}, ErrLyricsSourceArtifactConflict
	}
	return item, nil
}

func loadLyricsSourceReviewItemContext(ctx context.Context, query lyricsSourceReviewQuery, suffix string, args ...any) (model.LyricsSourceReviewItem, error) {
	var item model.LyricsSourceReviewItem
	var analysisID sql.NullInt64
	var evidence string
	var createdAt, updatedAt int64
	var completedAt sql.NullInt64
	err := query.QueryRowContext(ctx, `SELECT review_id, domain_key, kind, analysis_id, music_id, catalog_fingerprint,
		review_policy_version, reason_code, evidence_json, state, identity_gate, source_use_gate, parse_gate,
		version, priority, created_at, updated_at, completed_at FROM lyrics_source_review_items `+suffix, args...).Scan(
		&item.ReviewID, &item.DomainKey, &item.Kind, &analysisID, &item.MusicID, &item.CatalogFingerprint,
		&item.ReviewPolicyVersion, &item.ReasonCode, &evidence, &item.State, &item.IdentityGate, &item.SourceUseGate,
		&item.ParseGate, &item.Version, &item.Priority, &createdAt, &updatedAt, &completedAt)
	if err == sql.ErrNoRows {
		return model.LyricsSourceReviewItem{}, ErrLyricsSourceReviewNotFound
	}
	if err != nil {
		return model.LyricsSourceReviewItem{}, err
	}
	item.AnalysisID, item.EvidenceJSON = analysisID.Int64, []byte(evidence)
	item.CreatedAt, item.UpdatedAt = time.UnixMilli(createdAt).UTC(), time.UnixMilli(updatedAt).UTC()
	if completedAt.Valid {
		item.CompletedAt = time.UnixMilli(completedAt.Int64).UTC()
	}
	return item, nil
}

func loadLyricsSourceAnalysisFacts(ctx context.Context, db *sql.DB, analysisID int64) (LyricsSourceArtifactFact, LyricsSourceAnalysisFact, error) {
	var artifact LyricsSourceArtifactFact
	var analysis LyricsSourceAnalysisFact
	var categories, evidence, restrictions, selectedVersion, performers, lines string
	var fetchedAt int64
	err := db.QueryRowContext(ctx, `SELECT a.matching_policy_version, a.restriction_policy_version,
		a.extractor_version, a.match_outcome, a.restriction_outcome, a.extraction_outcome, a.matching_evidence_json,
		a.restriction_rule_ids_json, a.selected_version_json, a.performers_json, a.ruby_generator_version,
		a.extracted_lines_json, r.source_type, r.source_origin, r.page_id, r.revision_id, r.page_title,
		r.canonical_revision_url, r.mediawiki_sha1, r.categories_json, r.first_fetched_at
		FROM lyrics_source_analyses a JOIN lyrics_source_artifacts r ON r.artifact_id=a.artifact_id WHERE a.analysis_id=?`, analysisID).Scan(
		&analysis.MatchingPolicyVersion, &analysis.RestrictionPolicyVersion, &analysis.ExtractorVersion,
		&analysis.MatchOutcome, &analysis.RestrictionOutcome, &analysis.ExtractionOutcome, &evidence, &restrictions,
		&selectedVersion, &performers, &analysis.RubyGeneratorVersion, &lines,
		&artifact.SourceType, &artifact.SourceOrigin, &artifact.PageID, &artifact.RevisionID,
		&artifact.PageTitle, &artifact.CanonicalRevisionURL, &artifact.MediaWikiSHA1, &categories, &fetchedAt)
	if err != nil {
		return LyricsSourceArtifactFact{}, LyricsSourceAnalysisFact{}, err
	}
	if json.Unmarshal([]byte(categories), &artifact.Categories) != nil || json.Unmarshal([]byte(evidence), &analysis.MatchingEvidence) != nil ||
		json.Unmarshal([]byte(restrictions), &analysis.RestrictionRuleIDs) != nil ||
		json.Unmarshal([]byte(selectedVersion), &analysis.SelectedVersion) != nil ||
		json.Unmarshal([]byte(performers), &analysis.Performers) != nil || json.Unmarshal([]byte(lines), &analysis.ExtractedLines) != nil {
		return LyricsSourceArtifactFact{}, LyricsSourceAnalysisFact{}, errors.New("invalid stored lyrics source analysis")
	}
	canonicalPerformers, canonicalRubyVersion, canonicalLines, err := canonicalizeLyricsSourceAnalysisMetadata(
		analysis.SelectedVersion, analysis.Performers, analysis.RubyGeneratorVersion, analysis.ExtractedLines,
	)
	if err != nil {
		return LyricsSourceArtifactFact{}, LyricsSourceAnalysisFact{}, errors.New("unsafe stored lyrics source analysis metadata")
	}
	analysis.Performers = canonicalPerformers
	analysis.RubyGeneratorVersion = canonicalRubyVersion
	analysis.ExtractedLines = canonicalLines
	artifact.FirstFetchedAt = time.UnixMilli(fetchedAt).UTC()
	return artifact, analysis, nil
}

func loadLyricsSourceAssociationFacts(ctx context.Context, db *sql.DB, analysisID int64) ([]LyricsSourceAssociationFact, error) {
	rows, err := db.QueryContext(ctx, `SELECT music_id, catalog_fingerprint, kind FROM lyrics_source_associations
		WHERE analysis_id=? ORDER BY kind, music_id`, analysisID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []LyricsSourceAssociationFact{}
	for rows.Next() {
		var item LyricsSourceAssociationFact
		if err := rows.Scan(&item.MusicID, &item.CatalogFingerprint, &item.Kind); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func loadLyricsSourceDecisionFacts(ctx context.Context, db *sql.DB, reviewID int64) ([]LyricsSourceReviewDecisionFact, error) {
	rows, err := db.QueryContext(ctx, `SELECT decision_id, gate, decision, selected_candidate_json, actor, note,
		expected_version, result_version, decided_at FROM lyrics_source_review_decisions WHERE review_id=? ORDER BY decision_id`, reviewID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []LyricsSourceReviewDecisionFact{}
	for rows.Next() {
		var item LyricsSourceReviewDecisionFact
		var selected sql.NullString
		var decidedAt int64
		if err := rows.Scan(&item.DecisionID, &item.Gate, &item.Decision, &selected, &item.Actor, &item.Note,
			&item.ExpectedVersion, &item.ResultVersion, &decidedAt); err != nil {
			return nil, err
		}
		if selected.Valid {
			var candidate model.LyricsSourceCandidateIdentity
			if err := json.Unmarshal([]byte(selected.String), &candidate); err != nil {
				return nil, err
			}
			item.SelectedCandidate = &candidate
		}
		item.DecidedAt = time.UnixMilli(decidedAt).UTC()
		result = append(result, item)
	}
	return result, rows.Err()
}
