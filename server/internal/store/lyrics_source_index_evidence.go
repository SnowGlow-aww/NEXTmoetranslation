package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"moesekai/server/internal/legacy"
	"moesekai/server/internal/lyricsdiscovery"
	"moesekai/server/internal/lyricssource"
	"moesekai/server/internal/model"
)

const (
	maxLyricsDiscoveryTransportBytes   = 4 << 20
	maxLyricsDiscoveryRawEvidenceBytes = 2 << 20
	maxLyricsDiscoveryCompactBytes     = 1 << 20
)

type lyricsDiscoveryStoredResultEnvelope struct {
	SchemaVersion int                      `json:"schemaVersion"`
	Candidates    []lyricssource.Candidate `json:"candidates"`
}

type lyricsIndexEvidenceKey struct {
	provider   model.LyricsSourceProvider
	evidenceID string
}

type lyricsIndexEvidenceQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func cloneLyricsIndexEvidence(evidence lyricssource.IndexEvidence) lyricssource.IndexEvidence {
	evidence.Categories = append([]string(nil), evidence.Categories...)
	evidence.Raw = append([]byte(nil), evidence.Raw...)
	return evidence
}

func cloneLyricsIndexEvidenceSlice(input []lyricssource.IndexEvidence) []lyricssource.IndexEvidence {
	if input == nil {
		return nil
	}
	result := make([]lyricssource.IndexEvidence, len(input))
	for index := range input {
		result[index] = cloneLyricsIndexEvidence(input[index])
	}
	return result
}

func sameLyricsIndexEvidence(left, right lyricssource.IndexEvidence) bool {
	return left.EvidenceID == right.EvidenceID && left.SHA256 == right.SHA256 && left.Kind == right.Kind &&
		left.Provider == right.Provider && left.Origin == right.Origin && left.PageID == right.PageID &&
		left.RevisionID == right.RevisionID && left.RevisionTimestamp == right.RevisionTimestamp &&
		left.MediaWikiSHA1 == right.MediaWikiSHA1 && left.Title == right.Title &&
		left.CanonicalURL == right.CanonicalURL && sameStringSlice(left.Categories, right.Categories) &&
		left.CanonicalRequestURL == right.CanonicalRequestURL && left.FetchedAt == right.FetchedAt &&
		left.RawSHA256 == right.RawSHA256 && bytes.Equal(left.Raw, right.Raw)
}

func stripLyricsCandidateIndexEvidence(candidate lyricssource.Candidate) lyricssource.Candidate {
	candidate = cloneLyricsDiscoveryCandidate(candidate)
	candidate.IndexEvidence = nil
	return candidate
}

func canonicalLyricsIndexEvidenceResolution(
	candidates []lyricssource.Candidate,
	evidence []lyricssource.IndexEvidence,
) ([]lyricssource.Candidate, []lyricssource.IndexEvidence, error) {
	if candidates == nil || evidence == nil || len(candidates) > lyricsdiscovery.MaxCandidateArtifactCandidates ||
		len(evidence) > lyricsdiscovery.MaxCandidateArtifactEvidence {
		return nil, nil, errors.New("discovery evidence envelope is incomplete or exceeds safe limits")
	}
	byKey := make(map[lyricsIndexEvidenceKey]lyricssource.IndexEvidence, len(evidence))
	used := make(map[lyricsIndexEvidenceKey]bool, len(evidence))
	canonicalEvidence := make([]lyricssource.IndexEvidence, len(evidence))
	totalRawEvidenceBytes := 0
	for index := range evidence {
		item := cloneLyricsIndexEvidence(evidence[index])
		if len(item.Raw) > maxLyricsDiscoveryRawEvidenceBytes-totalRawEvidenceBytes {
			return nil, nil, errors.New("discovery evidence exceeds aggregate raw-byte limit")
		}
		totalRawEvidenceBytes += len(item.Raw)
		if item.Kind == lyricssource.IndexEvidenceKindMediaWikiSearchResponse && item.Categories == nil {
			item.Categories = []string{}
		}
		key := lyricsIndexEvidenceKey{provider: item.Provider, evidenceID: item.EvidenceID}
		if !model.IsValidLyricsSourceProvider(item.Provider) || item.EvidenceID == "" {
			return nil, nil, errors.New("discovery index evidence identity is invalid")
		}
		if _, duplicate := byKey[key]; duplicate {
			return nil, nil, errors.New("discovery index evidence resolves more than once")
		}
		byKey[key] = item
		canonicalEvidence[index] = item
	}

	hydrated := make([]lyricssource.Candidate, len(candidates))
	for index := range candidates {
		candidate := cloneLyricsDiscoveryCandidate(candidates[index])
		if candidate.IndexEvidence != nil {
			return nil, nil, errors.New("candidate embeds index evidence instead of artifact-level resolutions")
		}
		candidate.IndexEvidence = make([]lyricssource.IndexEvidence, len(candidate.IndexEvidenceRefs))
		for referenceIndex, reference := range candidate.IndexEvidenceRefs {
			key := lyricsIndexEvidenceKey{provider: candidate.Provider, evidenceID: reference.EvidenceID}
			item, found := byKey[key]
			if !found || item.SHA256 != reference.SHA256 || item.RawSHA256 != reference.SHA256 {
				return nil, nil, errors.New("candidate index evidence reference does not resolve exactly once")
			}
			candidate.IndexEvidence[referenceIndex] = cloneLyricsIndexEvidence(item)
			used[key] = true
		}
		hydrated[index] = candidate
	}
	if len(used) != len(byKey) {
		return nil, nil, errors.New("discovery artifact contains orphan index evidence")
	}
	if err := lyricssource.ValidateCandidatesIndexEvidence(hydrated); err != nil {
		return nil, nil, err
	}
	compact := make([]lyricssource.Candidate, len(hydrated))
	for index := range hydrated {
		compact[index] = stripLyricsCandidateIndexEvidence(hydrated[index])
	}
	sort.Slice(canonicalEvidence, func(i, j int) bool {
		if canonicalEvidence[i].Provider != canonicalEvidence[j].Provider {
			return canonicalEvidence[i].Provider < canonicalEvidence[j].Provider
		}
		return canonicalEvidence[i].EvidenceID < canonicalEvidence[j].EvidenceID
	})
	return compact, canonicalEvidence, nil
}

func decodeLyricsDiscoveryArtifact(resultArtifact []byte, expectedCandidateCount int) ([]lyricssource.Candidate, []lyricssource.IndexEvidence, error) {
	if len(resultArtifact) < 2 || len(resultArtifact) > maxLyricsDiscoveryTransportBytes {
		return nil, nil, errors.New("discovery artifact exceeds transport limit")
	}
	resolved, err := lyricsdiscovery.DecodeCandidateArtifact(resultArtifact)
	if err != nil {
		return nil, nil, err
	}
	if len(resolved) != expectedCandidateCount {
		return nil, nil, errors.New("discovery artifact candidate count does not match its result")
	}
	compact := make([]lyricssource.Candidate, len(resolved))
	byID := make(map[string]lyricssource.IndexEvidence)
	for index, candidate := range resolved {
		compact[index] = stripLyricsCandidateIndexEvidence(candidate)
		for _, item := range candidate.IndexEvidence {
			if item.Kind == lyricssource.IndexEvidenceKindMediaWikiSearchResponse && item.Categories == nil {
				item.Categories = []string{}
			}
			if existing, found := byID[item.EvidenceID]; found && !sameLyricsIndexEvidence(existing, item) {
				return nil, nil, errors.New("discovery artifact evidence ID has conflicting resolutions")
			}
			byID[item.EvidenceID] = cloneLyricsIndexEvidence(item)
		}
	}
	evidenceIDs := make([]string, 0, len(byID))
	for evidenceID := range byID {
		evidenceIDs = append(evidenceIDs, evidenceID)
	}
	sort.Strings(evidenceIDs)
	evidence := make([]lyricssource.IndexEvidence, 0, len(evidenceIDs))
	for _, evidenceID := range evidenceIDs {
		evidence = append(evidence, byID[evidenceID])
	}
	return compact, evidence, nil
}

func sameCompactLyricsDiscoveryCandidates(left, right []lyricssource.Candidate) bool {
	if len(left) != len(right) || left == nil != (right == nil) {
		return false
	}
	for index := range left {
		leftBody, leftErr := json.Marshal(stripLyricsCandidateIndexEvidence(left[index]))
		rightBody, rightErr := json.Marshal(stripLyricsCandidateIndexEvidence(right[index]))
		if leftErr != nil || rightErr != nil || !bytes.Equal(leftBody, rightBody) {
			return false
		}
	}
	return true
}

func sameLyricsIndexEvidenceCollection(left, right []lyricssource.IndexEvidence) bool {
	if len(left) != len(right) || left == nil != (right == nil) {
		return false
	}
	for index := range left {
		if !sameLyricsIndexEvidence(left[index], right[index]) {
			return false
		}
	}
	return true
}

func canonicalStoredLyricsDiscoveryArtifact(candidates []lyricssource.Candidate) ([]byte, error) {
	compact := make([]lyricssource.Candidate, len(candidates))
	for index := range candidates {
		compact[index] = stripLyricsCandidateIndexEvidence(candidates[index])
	}
	body, err := json.Marshal(lyricsDiscoveryStoredResultEnvelope{
		SchemaVersion: lyricsdiscovery.CandidateArtifactSchemaVersion,
		Candidates:    compact,
	})
	if err != nil {
		return nil, err
	}
	if len(body) > maxLyricsDiscoveryCompactBytes {
		return nil, errors.New("compact discovery candidate projection exceeds safe storage limit")
	}
	return body, legacy.ValidateUniqueJSON(body)
}

func insertOrVerifyLyricsIndexEvidenceTx(
	ctx context.Context,
	tx *sql.Tx,
	evidence lyricssource.IndexEvidence,
	createdAt time.Time,
) error {
	categoriesJSON, err := json.Marshal(evidence.Categories)
	if err != nil {
		return err
	}
	createdAt = canonicalLyricsDiscoveryTime(createdAt)
	_, err = tx.ExecContext(ctx, `INSERT INTO lyrics_source_index_evidence
		(provider,evidence_id,sha256,kind,origin,page_id,revision_id,revision_timestamp,mediawiki_sha1,page_title,
		 canonical_revision_url,categories_json,canonical_request_url,fetched_at,raw_bytes,raw_byte_count,
		 raw_sha256,created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(provider,evidence_id) DO NOTHING`, evidence.Provider, evidence.EvidenceID, evidence.SHA256,
		evidence.Kind, evidence.Origin, nullablePositiveInt(evidence.PageID), nullablePositiveInt(evidence.RevisionID),
		evidence.RevisionTimestamp, evidence.MediaWikiSHA1, evidence.Title, evidence.CanonicalURL, string(categoriesJSON),
		evidence.CanonicalRequestURL, evidence.FetchedAt, evidence.Raw, len(evidence.Raw), evidence.RawSHA256,
		createdAt.UnixMilli())
	if err != nil {
		return err
	}
	stored, err := loadLyricsIndexEvidenceContext(ctx, tx, evidence.Provider, evidence.EvidenceID)
	if err != nil {
		return err
	}
	if !sameLyricsIndexEvidence(stored, evidence) {
		return ErrLyricsSourceArtifactConflict
	}
	return nil
}

func insertOrVerifyLyricsIndexEvidenceCollectionTx(
	ctx context.Context,
	tx *sql.Tx,
	evidence []lyricssource.IndexEvidence,
	createdAt time.Time,
) error {
	for _, item := range evidence {
		if err := insertOrVerifyLyricsIndexEvidenceTx(ctx, tx, item, createdAt); err != nil {
			return err
		}
	}
	return nil
}

func loadLyricsIndexEvidenceContext(
	ctx context.Context,
	query lyricsIndexEvidenceQuery,
	provider model.LyricsSourceProvider,
	evidenceID string,
) (lyricssource.IndexEvidence, error) {
	var evidence lyricssource.IndexEvidence
	var categoriesJSON string
	var pageID, revisionID sql.NullInt64
	var raw []byte
	err := query.QueryRowContext(ctx, `SELECT evidence_id,sha256,kind,provider,origin,page_id,revision_id,
		revision_timestamp,mediawiki_sha1,page_title,canonical_revision_url,categories_json,canonical_request_url,
		fetched_at,raw_bytes,raw_sha256 FROM lyrics_source_index_evidence WHERE provider=? AND evidence_id=?`, provider, evidenceID).
		Scan(&evidence.EvidenceID, &evidence.SHA256, &evidence.Kind, &evidence.Provider, &evidence.Origin,
			&pageID, &revisionID, &evidence.RevisionTimestamp, &evidence.MediaWikiSHA1, &evidence.Title,
			&evidence.CanonicalURL, &categoriesJSON, &evidence.CanonicalRequestURL, &evidence.FetchedAt, &raw,
			&evidence.RawSHA256)
	if err != nil {
		return lyricssource.IndexEvidence{}, err
	}
	if pageID.Valid {
		evidence.PageID = int(pageID.Int64)
	}
	if revisionID.Valid {
		evidence.RevisionID = int(revisionID.Int64)
	}
	if err := json.Unmarshal([]byte(categoriesJSON), &evidence.Categories); err != nil {
		return lyricssource.IndexEvidence{}, err
	}
	evidence.Raw = append([]byte(nil), raw...)
	return evidence, nil
}

func loadLyricsIndexEvidenceForCandidateContext(
	ctx context.Context,
	query lyricsIndexEvidenceQuery,
	candidate lyricssource.Candidate,
) (lyricssource.Candidate, error) {
	candidate = stripLyricsCandidateIndexEvidence(candidate)
	candidate.IndexEvidence = make([]lyricssource.IndexEvidence, len(candidate.IndexEvidenceRefs))
	for index, reference := range candidate.IndexEvidenceRefs {
		evidence, err := loadLyricsIndexEvidenceContext(ctx, query, candidate.Provider, reference.EvidenceID)
		if err != nil {
			return lyricssource.Candidate{}, err
		}
		if evidence.SHA256 != reference.SHA256 || evidence.RawSHA256 != reference.SHA256 {
			return lyricssource.Candidate{}, errors.New("stored candidate index evidence digest does not resolve")
		}
		candidate.IndexEvidence[index] = evidence
	}
	if err := lyricssource.ValidateCandidateIndexEvidence(candidate); err != nil {
		return lyricssource.Candidate{}, err
	}
	return candidate, nil
}

func linkLyricsDiscoveryResultEvidenceTx(
	ctx context.Context,
	tx *sql.Tx,
	resultID int64,
	evidence []lyricssource.IndexEvidence,
) error {
	for position, item := range evidence {
		if _, err := tx.ExecContext(ctx, `INSERT INTO lyrics_discovery_result_index_evidence
			(result_id,position,provider,evidence_id,sha256) VALUES (?,?,?,?,?)`, resultID, position,
			item.Provider, item.EvidenceID, item.SHA256); err != nil {
			return err
		}
	}
	return nil
}

func linkLyricsSourceReviewEvidenceTx(
	ctx context.Context,
	tx *sql.Tx,
	reviewID int64,
	evidence []lyricssource.IndexEvidence,
) error {
	for position, item := range evidence {
		if _, err := tx.ExecContext(ctx, `INSERT INTO lyrics_source_review_index_evidence
			(review_id,position,provider,evidence_id,sha256) VALUES (?,?,?,?,?)`, reviewID, position,
			item.Provider, item.EvidenceID, item.SHA256); err != nil {
			return err
		}
	}
	return nil
}

func verifyLyricsSourceReviewCandidateEvidence(
	ctx context.Context,
	query lyricsIndexEvidenceQuery,
	reviewID int64,
	candidate lyricssource.Candidate,
) error {
	for _, reference := range candidate.IndexEvidenceRefs {
		var matches int
		if err := query.QueryRowContext(ctx, `SELECT COUNT(*) FROM lyrics_source_review_index_evidence
			WHERE review_id=? AND provider=? AND evidence_id=? AND sha256=?`, reviewID, candidate.Provider,
			reference.EvidenceID, reference.SHA256).Scan(&matches); err != nil {
			return err
		}
		if matches != 1 {
			return errors.New("selected candidate index evidence does not resolve through its review")
		}
	}
	return nil
}

func linkLyricsDiscoveryJobEvidenceTx(
	ctx context.Context,
	tx *sql.Tx,
	jobID int64,
	candidate lyricssource.Candidate,
	createdAt time.Time,
) error {
	createdAt = canonicalLyricsDiscoveryTime(createdAt)
	for position, reference := range candidate.IndexEvidenceRefs {
		evidence, err := loadLyricsIndexEvidenceContext(ctx, tx, candidate.Provider, reference.EvidenceID)
		if err != nil || evidence.SHA256 != reference.SHA256 {
			return errors.New("fetch job index evidence reference is unresolved")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO lyrics_discovery_job_index_evidence
			(job_id,position,provider,evidence_id,sha256,created_at) VALUES (?,?,?,?,?,?)
			ON CONFLICT(job_id,position) DO NOTHING`, jobID, position, candidate.Provider,
			reference.EvidenceID, reference.SHA256, createdAt.UnixMilli()); err != nil {
			return err
		}
	}
	return verifyLyricsDiscoveryJobEvidenceLinks(ctx, tx, jobID, candidate)
}

func verifyLyricsDiscoveryJobEvidenceLinks(
	ctx context.Context,
	query lyricsIndexEvidenceQuery,
	jobID int64,
	candidate lyricssource.Candidate,
) error {
	for position, reference := range candidate.IndexEvidenceRefs {
		var provider model.LyricsSourceProvider
		var evidenceID, sha256Value string
		if err := query.QueryRowContext(ctx, `SELECT provider,evidence_id,sha256
			FROM lyrics_discovery_job_index_evidence WHERE job_id=? AND position=?`, jobID, position).
			Scan(&provider, &evidenceID, &sha256Value); err != nil {
			return err
		}
		if provider != candidate.Provider || evidenceID != reference.EvidenceID || sha256Value != reference.SHA256 {
			return ErrLyricsSourceArtifactConflict
		}
	}
	var count int
	if err := query.QueryRowContext(ctx, `SELECT COUNT(*) FROM lyrics_discovery_job_index_evidence WHERE job_id=?`, jobID).Scan(&count); err != nil {
		return err
	}
	if count != len(candidate.IndexEvidenceRefs) {
		return ErrLyricsSourceArtifactConflict
	}
	return nil
}

func loadLyricsDiscoveryFixedCandidateWithEvidenceContext(
	ctx context.Context,
	query lyricsDiscoveryJobQuery,
	jobID int64,
) (*lyricssource.Candidate, error) {
	candidate, err := loadLyricsDiscoveryFixedCandidateContext(ctx, query, jobID)
	if err != nil || candidate == nil {
		return candidate, err
	}
	if !lyricsDiscoveryCandidateIsProviderAware(*candidate) {
		return nil, errors.New("legacy fixed candidate requires provenance rebuild")
	}
	if err := verifyLyricsDiscoveryJobEvidenceLinks(ctx, query, jobID, *candidate); err != nil {
		return nil, err
	}
	hydrated, err := loadLyricsIndexEvidenceForCandidateContext(ctx, query, *candidate)
	if err != nil {
		return nil, err
	}
	return &hydrated, nil
}

func insertLyricsSourceRenditionEvidenceLinksTx(
	ctx context.Context,
	tx *sql.Tx,
	renditionID int64,
	identity model.LyricsSourceFixedIdentity,
) error {
	for position, reference := range identity.IndexEvidenceRefs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO lyrics_source_rendition_index_evidence
			(rendition_id,position,provider,evidence_id,sha256) VALUES (?,?,?,?,?)
			ON CONFLICT(rendition_id,position) DO NOTHING`, renditionID, position,
			identity.Provider, reference.EvidenceID, reference.SHA256); err != nil {
			return err
		}
		var provider model.LyricsSourceProvider
		var evidenceID, digest string
		if err := tx.QueryRowContext(ctx, `SELECT provider,evidence_id,sha256
			FROM lyrics_source_rendition_index_evidence WHERE rendition_id=? AND position=?`, renditionID, position).
			Scan(&provider, &evidenceID, &digest); err != nil {
			return err
		}
		if provider != identity.Provider || evidenceID != reference.EvidenceID || digest != reference.SHA256 {
			return ErrLyricsSourceArtifactConflict
		}
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM lyrics_source_rendition_index_evidence WHERE rendition_id=?`, renditionID).Scan(&count); err != nil {
		return err
	}
	if count != len(identity.IndexEvidenceRefs) {
		return ErrLyricsSourceArtifactConflict
	}
	return nil
}

func sha256Hex(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

func parseCanonicalLyricsEvidenceFetchedAt(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || value == "" || !strings.HasSuffix(value, "Z") || parsed.Unix() <= 0 ||
		parsed.UTC().Format(time.RFC3339Nano) != value {
		return time.Time{}, fmt.Errorf("invalid canonical index evidence fetchedAt")
	}
	return parsed.UTC(), nil
}
