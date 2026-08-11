package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"moesekai/server/internal/lyricssource"
	"moesekai/server/internal/model"
)

const (
	DefaultLyricsDiscoveryJobMaxAttempts = 8
	maxLyricsDiscoveryJobAttempts        = 100
	maxLyricsDiscoveryLeaseOwnerBytes    = 128
	maxLyricsDiscoveryErrorCodeBytes     = 64
	maxLyricsDiscoveryFingerprintBytes   = 64
	maxLyricsDiscoveryPolicyVersionBytes = 64
	maxLyricsDiscoveryLeaseDuration      = 24 * time.Hour
	maxLyricsDiscoveryRetryDelay         = 30 * 24 * time.Hour
)

var (
	ErrLyricsDiscoveryJobNotFound   = errors.New("lyrics discovery job not found")
	ErrLyricsDiscoveryLeaseNotOwned = errors.New("lyrics discovery job lease not owned")
	ErrLyricsDiscoveryJobTerminal   = errors.New("lyrics discovery job is terminal")

	lyricsDiscoveryErrorCodePattern       = regexp.MustCompile(`^[a-z0-9_]+$`)
	lyricsDiscoverySHA1Pattern            = regexp.MustCompile(`^[0-9a-f]{40}$`)
	lyricsDiscoveryRenditionKeyPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	lyricsDiscoveryIndexEvidenceIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,255}$`)
)

type EnqueueLyricsDiscoveryJobParams struct {
	Provider       model.LyricsSourceProvider
	Kind           model.LyricsDiscoveryJobKind
	Target         model.LyricsDiscoveryJobTarget
	FixedCandidate *lyricssource.Candidate
	FixedIdentity  *model.LyricsSourceFixedIdentity
	MaxAttempts    int
	NotBefore      time.Time
}

type LyricsDiscoveryJobLease struct {
	Owner    string
	Duration time.Duration
	Provider model.LyricsSourceProvider
	Kind     model.LyricsDiscoveryJobKind
	Now      time.Time
}

const lyricsDiscoveryFixedCandidateSchemaVersion = 1

type lyricsDiscoveryFixedCandidateEnvelope struct {
	SchemaVersion int                    `json:"schemaVersion"`
	Candidate     lyricssource.Candidate `json:"candidate"`
}

func legacyLyricsDiscoveryCandidate(candidate *model.LyricsSourceCandidateIdentity) *lyricssource.Candidate {
	if candidate == nil {
		return nil
	}
	return &lyricssource.Candidate{
		PageID: candidate.PageID, RevisionID: candidate.RevisionID, SHA1: candidate.SHA1,
		Title: candidate.Title, CanonicalURL: candidate.CanonicalURL,
		Categories: append([]string{}, candidate.Categories...),
	}
}

func legacyLyricsDiscoveryCandidateIdentity(candidate *lyricssource.Candidate) *model.LyricsSourceCandidateIdentity {
	if candidate == nil {
		return nil
	}
	return &model.LyricsSourceCandidateIdentity{
		PageID: candidate.PageID, RevisionID: candidate.RevisionID, SHA1: candidate.SHA1,
		Title: candidate.Title, CanonicalURL: candidate.CanonicalURL,
		Categories: append([]string{}, candidate.Categories...),
	}
}

func cloneLyricsDiscoveryCandidate(candidate lyricssource.Candidate) lyricssource.Candidate {
	if candidate.Categories != nil {
		candidate.Categories = append([]string{}, candidate.Categories...)
	}
	if candidate.IndexEvidenceRefs != nil {
		candidate.IndexEvidenceRefs = append([]model.LyricsSourceIndexEvidenceRef{}, candidate.IndexEvidenceRefs...)
	}
	candidate.IndexEvidence = cloneLyricsIndexEvidenceSlice(candidate.IndexEvidence)
	return candidate
}

func lyricsDiscoveryCandidateIsProviderAware(candidate lyricssource.Candidate) bool {
	return candidate.Provider != "" || candidate.Origin != "" || candidate.Section != "" || candidate.RenditionKey != "" ||
		candidate.VersionReason != "" || candidate.IndexEvidenceRefs != nil
}

func validateProviderAwareLyricsDiscoveryCandidate(provider model.LyricsSourceProvider, candidate lyricssource.Candidate) error {
	if !lyricsDiscoveryCandidateIsProviderAware(candidate) {
		if provider != model.LyricsSourceProviderVocaloidFandom {
			return errors.New("non-Fandom fetch jobs require provider-aware candidate provenance")
		}
		return validateLyricsSourceCandidateIdentity(*legacyLyricsDiscoveryCandidateIdentity(&candidate))
	}
	if candidate.Provider != provider || candidate.IndexEvidence != nil ||
		!model.IsValidLyricsSourceCandidateVersionReasonCode(candidate.VersionReason) ||
		candidate.PageID <= 0 || candidate.RevisionID <= 0 || !lyricsDiscoverySHA1Pattern.MatchString(candidate.SHA1) ||
		candidate.Title == "" || candidate.Title != strings.TrimSpace(candidate.Title) || len(candidate.Title) > 2048 ||
		candidate.Section == "" || candidate.Section != strings.TrimSpace(candidate.Section) || len(candidate.Section) > 512 ||
		!lyricsDiscoveryRenditionKeyPattern.MatchString(candidate.RenditionKey) || candidate.Categories == nil ||
		len(candidate.IndexEvidenceRefs) == 0 || len(candidate.IndexEvidenceRefs) > 64 {
		return errors.New("fixed candidate provider, rendition, or source identity is invalid")
	}
	wantOrigin := model.LyricsSourceOriginVocaloidFandom
	if provider == model.LyricsSourceProviderMoegirl {
		wantOrigin = model.LyricsSourceOriginMoegirl
	}
	if candidate.Origin != wantOrigin ||
		validateProviderCanonicalLyricsSourceURL(provider, candidate.Origin, candidate.CanonicalURL, candidate.Title, candidate.RevisionID) != nil {
		return errors.New("fixed candidate provider origin or canonical URL is invalid")
	}
	categories, err := canonicalLyricsSourceStringSet(candidate.Categories, maxLyricsSourceCategories, maxLyricsSourceCategoryBytes)
	if err != nil || !sameStringSlice(categories, candidate.Categories) {
		return errors.New("fixed candidate categories are not canonical")
	}
	seenEvidence := make(map[string]struct{}, len(candidate.IndexEvidenceRefs))
	for _, reference := range candidate.IndexEvidenceRefs {
		if !lyricsDiscoveryIndexEvidenceIDPattern.MatchString(reference.EvidenceID) ||
			!lyricsDiscoveryFingerprintPattern.MatchString(reference.SHA256) {
			return errors.New("fixed candidate index evidence is invalid")
		}
		if _, exists := seenEvidence[reference.EvidenceID]; exists {
			return errors.New("fixed candidate index evidence is duplicated")
		}
		seenEvidence[reference.EvidenceID] = struct{}{}
	}
	return nil
}

func canonicalLyricsDiscoveryFixedCandidateForProvider(provider model.LyricsSourceProvider, legacy *model.LyricsSourceCandidateIdentity, candidate *lyricssource.Candidate, identity *model.LyricsSourceFixedIdentity) (string, *lyricssource.Candidate, error) {
	if candidate == nil {
		candidate = legacyLyricsDiscoveryCandidate(legacy)
	}
	if candidate == nil {
		return "", nil, nil
	}
	canonical := stripLyricsCandidateIndexEvidence(*candidate)
	if err := validateProviderAwareLyricsDiscoveryCandidate(provider, canonical); err != nil {
		return "", nil, err
	}
	legacyCandidate := legacyLyricsDiscoveryCandidateIdentity(&canonical)
	if legacy == nil || !sameLyricsSourceCandidateIdentity(*legacy, *legacyCandidate) {
		return "", nil, errors.New("fixed candidate does not match the legacy queue target identity")
	}
	if identity != nil {
		if err := model.ValidateLyricsSourceFixedIdentity(*identity); err != nil || identity.Provider != provider {
			return "", nil, errors.New("fixed identity provider does not match the job provider")
		}
		if !fixedIdentityMatchesLyricsDiscoveryCandidate(*identity, canonical) {
			return "", nil, errors.New("fixed candidate does not match the complete fixed identity")
		}
	}
	body, err := json.Marshal(lyricsDiscoveryFixedCandidateEnvelope{
		SchemaVersion: lyricsDiscoveryFixedCandidateSchemaVersion,
		Candidate:     canonical,
	})
	if err != nil {
		return "", nil, err
	}
	return string(body), &canonical, nil
}

func decodeLyricsDiscoveryFixedCandidate(value string) (*lyricssource.Candidate, error) {
	if value == "" {
		return nil, nil
	}
	var envelope lyricsDiscoveryFixedCandidateEnvelope
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil || envelope.SchemaVersion != lyricsDiscoveryFixedCandidateSchemaVersion {
		return nil, errors.New("invalid fixed candidate target")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("invalid fixed candidate target")
	}
	provider := envelope.Candidate.Provider
	if provider == "" {
		provider = model.LyricsSourceProviderVocaloidFandom
	}
	if err := validateProviderAwareLyricsDiscoveryCandidate(provider, envelope.Candidate); err != nil {
		return nil, errors.New("invalid fixed candidate target")
	}
	canonical := cloneLyricsDiscoveryCandidate(envelope.Candidate)
	return &canonical, nil
}

func fixedIdentityMatchesLyricsDiscoveryCandidate(identity model.LyricsSourceFixedIdentity, candidate lyricssource.Candidate) bool {
	return identity.Provider == candidate.Provider && identity.Origin == candidate.Origin &&
		identity.PageID == candidate.PageID && identity.RevisionID == candidate.RevisionID && identity.SHA1 == candidate.SHA1 &&
		identity.Title == candidate.Title && identity.CanonicalURL == candidate.CanonicalURL &&
		sameStringSlice(identity.Categories, candidate.Categories) && identity.Section == candidate.Section &&
		identity.RenditionKey == candidate.RenditionKey && sameIndexEvidenceRefs(identity.IndexEvidenceRefs, candidate.IndexEvidenceRefs)
}

func sameLyricsSourceCandidateIdentity(left, right model.LyricsSourceCandidateIdentity) bool {
	return left.PageID == right.PageID && left.RevisionID == right.RevisionID && left.SHA1 == right.SHA1 &&
		left.Title == right.Title && left.CanonicalURL == right.CanonicalURL && sameStringSlice(left.Categories, right.Categories)
}

func canonicalLyricsDiscoveryFixedIdentity(identity *model.LyricsSourceFixedIdentity) (string, error) {
	if identity == nil {
		return "", nil
	}
	if err := model.ValidateLyricsSourceFixedIdentity(*identity); err != nil {
		return "", err
	}
	body, err := json.Marshal(identity)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func sameLyricsDiscoveryJobTarget(left, right model.LyricsDiscoveryJobTarget) bool {
	leftCandidate, leftErr := json.Marshal(left.FixedCandidate)
	rightCandidate, rightErr := json.Marshal(right.FixedCandidate)
	return leftErr == nil && rightErr == nil && string(leftCandidate) == string(rightCandidate) &&
		left.MusicID == right.MusicID && left.PageID == right.PageID && left.RevisionID == right.RevisionID &&
		left.ArtifactID == right.ArtifactID && left.CatalogFingerprint == right.CatalogFingerprint &&
		left.PolicyVersion == right.PolicyVersion && left.ExpectedSHA1 == right.ExpectedSHA1
}

func LyricsDiscoveryJobIdempotencyKey(kind model.LyricsDiscoveryJobKind, target model.LyricsDiscoveryJobTarget) (string, error) {
	return lyricsDiscoveryJobIdempotencyKeyForProvider(model.LyricsSourceProviderVocaloidFandom, kind, target, nil, nil)
}

func lyricsDiscoveryJobIdempotencyKeyForProvider(provider model.LyricsSourceProvider, kind model.LyricsDiscoveryJobKind, target model.LyricsDiscoveryJobTarget, candidate *lyricssource.Candidate, fixedIdentity *model.LyricsSourceFixedIdentity) (string, error) {
	if !model.IsValidLyricsSourceProvider(provider) {
		return "", fmt.Errorf("invalid lyrics source provider %q", provider)
	}
	if err := validateLyricsDiscoveryJobTargetForProvider(provider, kind, target, candidate, fixedIdentity); err != nil {
		return "", err
	}
	version := "v3"
	providerPart := ""
	provenancePart := ""
	if provider != model.LyricsSourceProviderVocaloidFandom || fixedIdentity != nil ||
		(candidate != nil && lyricsDiscoveryCandidateIsProviderAware(*candidate)) {
		version = "v4"
		providerPart = string(provider) + "\x00"
		encodedCandidate, _, err := canonicalLyricsDiscoveryFixedCandidateForProvider(provider, target.FixedCandidate, candidate, fixedIdentity)
		if err != nil {
			return "", err
		}
		encodedIdentity, err := canonicalLyricsDiscoveryFixedIdentity(fixedIdentity)
		if err != nil {
			return "", err
		}
		provenancePart = encodedCandidate + "\x00" + encodedIdentity + "\x00"
	}
	canonical := fmt.Sprintf("%s\x00%s%s%s\x00%d\x00%d\x00%d\x00%d\x00%s\x00%s\x00%s",
		version, providerPart, provenancePart, kind, target.MusicID, target.PageID, target.RevisionID, target.ArtifactID,
		target.CatalogFingerprint, target.PolicyVersion, target.ExpectedSHA1)
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:]), nil
}

func canonicalLyricsSourceProvider(provider model.LyricsSourceProvider) (model.LyricsSourceProvider, error) {
	if provider == "" {
		provider = model.LyricsSourceProviderVocaloidFandom
	}
	if !model.IsValidLyricsSourceProvider(provider) {
		return "", fmt.Errorf("invalid lyrics source provider %q", provider)
	}
	return provider, nil
}

func SanitizeLyricsDiscoveryErrorCode(code string) string {
	code = strings.TrimSpace(strings.ToLower(code))
	if code == "" {
		return "unknown_error"
	}
	var sanitized strings.Builder
	lastUnderscore := false
	for _, char := range code {
		valid := (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9')
		if valid {
			sanitized.WriteRune(char)
			lastUnderscore = false
		} else if !lastUnderscore && sanitized.Len() > 0 {
			sanitized.WriteByte('_')
			lastUnderscore = true
		}
		if sanitized.Len() >= maxLyricsDiscoveryErrorCodeBytes {
			break
		}
	}
	result := strings.Trim(sanitized.String(), "_")
	if result == "" {
		return "unknown_error"
	}
	if len(result) > maxLyricsDiscoveryErrorCodeBytes {
		result = strings.TrimRight(result[:maxLyricsDiscoveryErrorCodeBytes], "_")
	}
	if result == "" {
		return "unknown_error"
	}
	return result
}

func (s *Store) EnqueueLyricsDiscoveryJob(ctx context.Context, params EnqueueLyricsDiscoveryJobParams) (model.LyricsDiscoveryJob, bool, error) {
	if ctx == nil {
		return model.LyricsDiscoveryJob{}, false, errors.New("lyrics discovery enqueue requires context")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.LyricsDiscoveryJob{}, false, err
	}
	defer tx.Rollback()
	job, created, err := enqueueLyricsDiscoveryJobTx(ctx, tx, params, time.Now().UTC())
	if err != nil {
		return model.LyricsDiscoveryJob{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return model.LyricsDiscoveryJob{}, false, err
	}
	return job, created, nil
}

func enqueueLyricsDiscoveryJobTx(ctx context.Context, tx *sql.Tx, params EnqueueLyricsDiscoveryJobParams, now time.Time) (model.LyricsDiscoveryJob, bool, error) {
	provider, err := canonicalLyricsSourceProvider(params.Provider)
	if err != nil {
		return model.LyricsDiscoveryJob{}, false, err
	}
	var suppliedEvidence []lyricssource.IndexEvidence
	if params.FixedCandidate != nil && params.FixedCandidate.IndexEvidence != nil {
		candidate := stripLyricsCandidateIndexEvidence(*params.FixedCandidate)
		compact, evidence, resolutionErr := canonicalLyricsIndexEvidenceResolution(
			[]lyricssource.Candidate{candidate}, params.FixedCandidate.IndexEvidence,
		)
		if resolutionErr != nil {
			return model.LyricsDiscoveryJob{}, false, resolutionErr
		}
		candidate = compact[0]
		params.FixedCandidate = &candidate
		suppliedEvidence = evidence
	}
	key, err := lyricsDiscoveryJobIdempotencyKeyForProvider(provider, params.Kind, params.Target, params.FixedCandidate, params.FixedIdentity)
	if err != nil {
		return model.LyricsDiscoveryJob{}, false, err
	}
	fixedCandidateJSON, fixedCandidate, err := canonicalLyricsDiscoveryFixedCandidateForProvider(
		provider, params.Target.FixedCandidate, params.FixedCandidate, params.FixedIdentity,
	)
	if err != nil {
		return model.LyricsDiscoveryJob{}, false, err
	}
	fixedIdentityJSON, err := canonicalLyricsDiscoveryFixedIdentity(params.FixedIdentity)
	if err != nil {
		return model.LyricsDiscoveryJob{}, false, err
	}
	provenanceStatus := "not_applicable"
	if params.Kind == model.LyricsDiscoveryJobFetchRevision {
		switch {
		case fixedIdentityJSON != "":
			provenanceStatus = "complete"
		case fixedCandidate != nil && lyricsDiscoveryCandidateIsProviderAware(*fixedCandidate):
			// Concrete candidate evidence is complete, while the final fetchedAt
			// identity does not exist until the exact fetch succeeds.
			provenanceStatus = "candidate_complete"
		default:
			provenanceStatus = "rebuild_required"
		}
	}
	maxAttempts := params.MaxAttempts
	if maxAttempts == 0 {
		maxAttempts = DefaultLyricsDiscoveryJobMaxAttempts
	}
	if maxAttempts < 1 || maxAttempts > maxLyricsDiscoveryJobAttempts {
		return model.LyricsDiscoveryJob{}, false, fmt.Errorf("max attempts must be between 1 and %d", maxLyricsDiscoveryJobAttempts)
	}
	now = now.UTC()
	notBefore := params.NotBefore
	if notBefore.IsZero() {
		notBefore = now
	}
	notBefore = canonicalLyricsDiscoveryTime(notBefore)
	if err := insertOrVerifyLyricsIndexEvidenceCollectionTx(ctx, tx, suppliedEvidence, now); err != nil {
		return model.LyricsDiscoveryJob{}, false, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO lyrics_discovery_jobs
		(idempotency_key, kind, state, music_id, page_id, revision_id, artifact_id,
		 catalog_fingerprint, policy_version, expected_sha1, fixed_candidate_json,
		 provider, fixed_identity_json, provenance_status,
		 attempts, max_attempts, next_attempt_at, created_at, updated_at, version)
		VALUES (?, ?, 'queued', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, 1)
		ON CONFLICT(idempotency_key) DO NOTHING`,
		key, params.Kind, params.Target.MusicID, nullablePositiveInt(params.Target.PageID),
		nullablePositiveInt(params.Target.RevisionID), nullablePositiveInt64(params.Target.ArtifactID),
		params.Target.CatalogFingerprint, params.Target.PolicyVersion, params.Target.ExpectedSHA1, fixedCandidateJSON,
		provider, fixedIdentityJSON, provenanceStatus, maxAttempts, notBefore.UnixMilli(), now.UnixMilli(), now.UnixMilli())
	if err != nil {
		return model.LyricsDiscoveryJob{}, false, err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return model.LyricsDiscoveryJob{}, false, err
	}
	job, err := loadLyricsDiscoveryJobContext(ctx, tx, `WHERE idempotency_key=?`, key)
	if err != nil {
		return model.LyricsDiscoveryJob{}, false, err
	}
	var storedProvider, storedFixedIdentity, storedProvenance string
	if err := tx.QueryRowContext(ctx, `SELECT provider,fixed_identity_json,provenance_status FROM lyrics_discovery_jobs WHERE job_id=?`, job.ID).
		Scan(&storedProvider, &storedFixedIdentity, &storedProvenance); err != nil {
		return model.LyricsDiscoveryJob{}, false, err
	}
	if storedProvider != string(provider) || storedFixedIdentity != fixedIdentityJSON || storedProvenance != provenanceStatus ||
		!sameLyricsDiscoveryJobTarget(job.Target, params.Target) {
		return model.LyricsDiscoveryJob{}, false, errors.New("lyrics discovery idempotency target conflicts with fixed candidate identity")
	}
	if fixedCandidate != nil && lyricsDiscoveryCandidateIsProviderAware(*fixedCandidate) {
		if err := linkLyricsDiscoveryJobEvidenceTx(ctx, tx, job.ID, *fixedCandidate, now); err != nil {
			return model.LyricsDiscoveryJob{}, false, err
		}
	}
	return job, rowsAffected == 1, nil
}

func (s *Store) GetLyricsDiscoveryJob(ctx context.Context, jobID int64) (model.LyricsDiscoveryJob, error) {
	if ctx == nil {
		return model.LyricsDiscoveryJob{}, errors.New("lyrics discovery get requires context")
	}
	if jobID <= 0 {
		return model.LyricsDiscoveryJob{}, errors.New("job ID must be positive")
	}
	return loadLyricsDiscoveryJobContext(ctx, s.db, `WHERE job_id=?`, jobID)
}

func (s *Store) ClaimLyricsDiscoveryJob(ctx context.Context, lease LyricsDiscoveryJobLease) (model.LyricsDiscoveryJob, error) {
	if ctx == nil {
		return model.LyricsDiscoveryJob{}, errors.New("lyrics discovery claim requires context")
	}
	owner, err := validateLyricsDiscoveryLease(lease)
	if err != nil {
		return model.LyricsDiscoveryJob{}, err
	}
	if lease.Kind != "" && !model.IsValidLyricsDiscoveryJobKind(lease.Kind) {
		return model.LyricsDiscoveryJob{}, fmt.Errorf("invalid lyrics discovery claim kind %q", lease.Kind)
	}
	provider, err := canonicalLyricsSourceProvider(lease.Provider)
	if err != nil {
		return model.LyricsDiscoveryJob{}, err
	}
	now := lease.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = canonicalLyricsDiscoveryTime(now)
	expiresAt := canonicalLyricsDiscoveryTime(now.Add(lease.Duration))
	kind := string(lease.Kind)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.LyricsDiscoveryJob{}, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `UPDATE lyrics_discovery_jobs
		SET state=CASE WHEN attempts >= max_attempts THEN 'dead_letter' ELSE 'retry_wait' END,
			next_attempt_at=?, lease_owner=NULL, lease_expires_at=NULL,
			last_error_code='lease_expired', updated_at=?,
			completed_at=CASE WHEN attempts >= max_attempts THEN ? ELSE NULL END,
			version=version+1
		WHERE state='leased' AND lease_expires_at<=? AND provider=? AND (?='' OR kind=?)`,
		now.UnixMilli(), now.UnixMilli(), now.UnixMilli(), now.UnixMilli(), provider, kind, kind); err != nil {
		return model.LyricsDiscoveryJob{}, err
	}

	var jobID int64
	err = tx.QueryRowContext(ctx, `SELECT job_id FROM lyrics_discovery_jobs
		WHERE state IN ('queued', 'retry_wait') AND next_attempt_at<=? AND attempts<max_attempts
			AND provider=? AND (?='' OR kind=?)
		ORDER BY next_attempt_at, job_id LIMIT 1`, now.UnixMilli(), provider, kind, kind).Scan(&jobID)
	if err == sql.ErrNoRows {
		if err := tx.Commit(); err != nil {
			return model.LyricsDiscoveryJob{}, err
		}
		return model.LyricsDiscoveryJob{}, ErrLyricsDiscoveryJobNotFound
	}
	if err != nil {
		return model.LyricsDiscoveryJob{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE lyrics_discovery_jobs
		SET state='leased', attempts=attempts+1, lease_owner=?, lease_expires_at=?,
			updated_at=?, completed_at=NULL, version=version+1
		WHERE job_id=? AND state IN ('queued', 'retry_wait') AND next_attempt_at<=? AND attempts<max_attempts
			AND provider=? AND (?='' OR kind=?)`,
		owner, expiresAt.UnixMilli(), now.UnixMilli(), jobID, now.UnixMilli(), provider, kind, kind)
	if err != nil {
		return model.LyricsDiscoveryJob{}, err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return model.LyricsDiscoveryJob{}, err
	}
	if rowsAffected != 1 {
		return model.LyricsDiscoveryJob{}, ErrLyricsDiscoveryJobNotFound
	}
	job, err := loadLyricsDiscoveryJobContext(ctx, tx, `WHERE job_id=?`, jobID)
	if err != nil {
		return model.LyricsDiscoveryJob{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.LyricsDiscoveryJob{}, err
	}
	return job, nil
}

func (s *Store) CompleteLyricsDiscoveryJob(ctx context.Context, jobID int64, leaseOwner string, expectedVersion int64) (model.LyricsDiscoveryJob, error) {
	return s.transitionLeasedLyricsDiscoveryJob(ctx, jobID, leaseOwner, expectedVersion, 0, model.LyricsDiscoveryJobSucceeded, time.Now().UTC(), time.Time{}, "")
}

func (s *Store) RetryLyricsDiscoveryJob(ctx context.Context, jobID int64, leaseOwner string, expectedVersion int64, expectedAttempt int, failedAt, notBefore time.Time, errorCode string) (model.LyricsDiscoveryJob, error) {
	if failedAt.IsZero() || failedAt.After(time.Now().UTC().Add(maxLyricsDiscoveryClockSkew)) {
		return model.LyricsDiscoveryJob{}, errors.New("invalid retry failure time")
	}
	failedAt = canonicalLyricsDiscoveryTime(failedAt)
	if notBefore.IsZero() {
		return model.LyricsDiscoveryJob{}, errors.New("retry time is required")
	}
	notBefore = canonicalLyricsDiscoveryTime(notBefore)
	if !notBefore.After(failedAt) {
		return model.LyricsDiscoveryJob{}, errors.New("retry time must be after failure time")
	}
	if notBefore.After(failedAt.Add(maxLyricsDiscoveryRetryDelay)) {
		return model.LyricsDiscoveryJob{}, fmt.Errorf("retry time must be within %s", maxLyricsDiscoveryRetryDelay)
	}
	return s.transitionLeasedLyricsDiscoveryJob(ctx, jobID, leaseOwner, expectedVersion, expectedAttempt, model.LyricsDiscoveryJobRetryWait, failedAt, notBefore, errorCode)
}

func (s *Store) FailLyricsDiscoveryJob(ctx context.Context, jobID int64, leaseOwner string, expectedVersion int64, errorCode string) (model.LyricsDiscoveryJob, error) {
	if ctx == nil {
		return model.LyricsDiscoveryJob{}, errors.New("lyrics discovery failure requires context")
	}
	owner := strings.TrimSpace(leaseOwner)
	if jobID <= 0 || expectedVersion <= 0 {
		return model.LyricsDiscoveryJob{}, errors.New("job ID and expected version must be positive")
	}
	if owner == "" || len(owner) > maxLyricsDiscoveryLeaseOwnerBytes {
		return model.LyricsDiscoveryJob{}, fmt.Errorf("lease owner must contain 1-%d bytes", maxLyricsDiscoveryLeaseOwnerBytes)
	}
	if strings.TrimSpace(errorCode) == "" {
		return model.LyricsDiscoveryJob{}, errors.New("error code is required")
	}
	errorCode = SanitizeLyricsDiscoveryErrorCode(errorCode)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.LyricsDiscoveryJob{}, err
	}
	defer tx.Rollback()
	now := canonicalLyricsDiscoveryTime(time.Now().UTC())
	var attempts, maxAttempts int
	if err := tx.QueryRowContext(ctx, `SELECT attempts, max_attempts FROM lyrics_discovery_jobs
		WHERE job_id=? AND state='leased' AND lease_owner=? AND version=? AND lease_expires_at>?`,
		jobID, owner, expectedVersion, now.UnixMilli()).Scan(&attempts, &maxAttempts); err == sql.ErrNoRows {
		var state model.LyricsDiscoveryJobState
		if stateErr := tx.QueryRowContext(ctx, `SELECT state FROM lyrics_discovery_jobs WHERE job_id=?`, jobID).Scan(&state); stateErr == sql.ErrNoRows {
			return model.LyricsDiscoveryJob{}, ErrLyricsDiscoveryJobNotFound
		} else if stateErr != nil {
			return model.LyricsDiscoveryJob{}, stateErr
		}
		if model.IsTerminalLyricsDiscoveryJobState(state) {
			return model.LyricsDiscoveryJob{}, fmt.Errorf("%w: %s", ErrLyricsDiscoveryJobTerminal, state)
		}
		return model.LyricsDiscoveryJob{}, ErrLyricsDiscoveryLeaseNotOwned
	} else if err != nil {
		return model.LyricsDiscoveryJob{}, err
	}
	nextState := model.LyricsDiscoveryJobRetryWait
	var completedAt any
	if attempts >= maxAttempts {
		nextState = model.LyricsDiscoveryJobDeadLetter
		completedAt = now.UnixMilli()
	}
	result, err := tx.ExecContext(ctx, `UPDATE lyrics_discovery_jobs
		SET state=?, next_attempt_at=?, lease_owner=NULL, lease_expires_at=NULL,
			last_error_code=?, updated_at=?, completed_at=?, version=version+1
		WHERE job_id=? AND state='leased' AND lease_owner=? AND version=? AND lease_expires_at>?`,
		nextState, now.UnixMilli(), errorCode, now.UnixMilli(), completedAt, jobID, owner, expectedVersion, now.UnixMilli())
	if err != nil {
		return model.LyricsDiscoveryJob{}, err
	}
	if err := requireLyricsDiscoveryJobTransition(ctx, tx, result, jobID); err != nil {
		return model.LyricsDiscoveryJob{}, err
	}
	job, err := loadLyricsDiscoveryJobContext(ctx, tx, `WHERE job_id=?`, jobID)
	if err != nil {
		return model.LyricsDiscoveryJob{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.LyricsDiscoveryJob{}, err
	}
	return job, nil
}

func (s *Store) CancelLyricsDiscoveryJob(ctx context.Context, jobID, expectedVersion int64) (model.LyricsDiscoveryJob, error) {
	if ctx == nil {
		return model.LyricsDiscoveryJob{}, errors.New("lyrics discovery cancel requires context")
	}
	if jobID <= 0 || expectedVersion <= 0 {
		return model.LyricsDiscoveryJob{}, errors.New("job ID and expected version must be positive")
	}
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.LyricsDiscoveryJob{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE lyrics_discovery_jobs
		SET state='cancelled', lease_owner=NULL, lease_expires_at=NULL, last_error_code=NULL,
			updated_at=?, completed_at=?, version=version+1
		WHERE job_id=? AND version=? AND state IN ('queued', 'retry_wait')`,
		now.UnixMilli(), now.UnixMilli(), jobID, expectedVersion)
	if err != nil {
		return model.LyricsDiscoveryJob{}, err
	}
	if err := requireLyricsDiscoveryJobTransition(ctx, tx, result, jobID); err != nil {
		return model.LyricsDiscoveryJob{}, err
	}
	job, err := loadLyricsDiscoveryJobContext(ctx, tx, `WHERE job_id=?`, jobID)
	if err != nil {
		return model.LyricsDiscoveryJob{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.LyricsDiscoveryJob{}, err
	}
	return job, nil
}

func (s *Store) transitionLeasedLyricsDiscoveryJob(ctx context.Context, jobID int64, leaseOwner string, expectedVersion int64, expectedAttempt int, nextState model.LyricsDiscoveryJobState, transitionedAt, notBefore time.Time, errorCode string) (model.LyricsDiscoveryJob, error) {
	if ctx == nil {
		return model.LyricsDiscoveryJob{}, errors.New("lyrics discovery transition requires context")
	}
	owner := strings.TrimSpace(leaseOwner)
	if jobID <= 0 || expectedVersion <= 0 || expectedAttempt < 0 {
		return model.LyricsDiscoveryJob{}, errors.New("job ID and expected version must be positive and expected attempt cannot be negative")
	}
	if owner == "" || len(owner) > maxLyricsDiscoveryLeaseOwnerBytes {
		return model.LyricsDiscoveryJob{}, fmt.Errorf("lease owner must contain 1-%d bytes", maxLyricsDiscoveryLeaseOwnerBytes)
	}
	if nextState != model.LyricsDiscoveryJobSucceeded && nextState != model.LyricsDiscoveryJobRetryWait && nextState != model.LyricsDiscoveryJobDeadLetter {
		return model.LyricsDiscoveryJob{}, fmt.Errorf("unsupported leased transition to %q", nextState)
	}
	if nextState != model.LyricsDiscoveryJobSucceeded {
		if strings.TrimSpace(errorCode) == "" {
			return model.LyricsDiscoveryJob{}, errors.New("error code is required")
		}
		errorCode = SanitizeLyricsDiscoveryErrorCode(errorCode)
		if !lyricsDiscoveryErrorCodePattern.MatchString(errorCode) {
			return model.LyricsDiscoveryJob{}, errors.New("invalid sanitized error code")
		}
	} else {
		errorCode = ""
	}
	transitionedAt = canonicalLyricsDiscoveryTime(transitionedAt)
	nextAttemptAt := transitionedAt
	var completedAt any
	if nextState == model.LyricsDiscoveryJobRetryWait {
		nextAttemptAt = notBefore
	} else {
		completedAt = transitionedAt.UnixMilli()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.LyricsDiscoveryJob{}, err
	}
	defer tx.Rollback()
	leaseCheckedAt := canonicalLyricsDiscoveryTime(time.Now().UTC())
	if transitionedAt.IsZero() || transitionedAt.After(leaseCheckedAt.Add(maxLyricsDiscoveryClockSkew)) {
		return model.LyricsDiscoveryJob{}, errors.New("invalid lyrics discovery transition time")
	}
	result, err := tx.ExecContext(ctx, `UPDATE lyrics_discovery_jobs
		SET state=?, next_attempt_at=?, lease_owner=NULL, lease_expires_at=NULL,
			last_error_code=?, updated_at=?, completed_at=?, version=version+1
		WHERE job_id=? AND state='leased' AND lease_owner=? AND version=? AND lease_expires_at>?
			AND (?=0 OR attempts=?)`,
		nextState, nextAttemptAt.UnixMilli(), nullableString(errorCode), transitionedAt.UnixMilli(), completedAt,
		jobID, owner, expectedVersion, leaseCheckedAt.UnixMilli(), expectedAttempt, expectedAttempt)
	if err != nil {
		return model.LyricsDiscoveryJob{}, err
	}
	if err := requireLyricsDiscoveryJobTransition(ctx, tx, result, jobID); err != nil {
		return model.LyricsDiscoveryJob{}, err
	}
	job, err := loadLyricsDiscoveryJobContext(ctx, tx, `WHERE job_id=?`, jobID)
	if err != nil {
		return model.LyricsDiscoveryJob{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.LyricsDiscoveryJob{}, err
	}
	return job, nil
}

func validateLyricsDiscoveryJobTarget(kind model.LyricsDiscoveryJobKind, target model.LyricsDiscoveryJobTarget) error {
	return validateLyricsDiscoveryJobTargetForProvider(model.LyricsSourceProviderVocaloidFandom, kind, target, nil, nil)
}

func validateLyricsDiscoveryJobTargetForProvider(provider model.LyricsSourceProvider, kind model.LyricsDiscoveryJobKind, target model.LyricsDiscoveryJobTarget, candidate *lyricssource.Candidate, fixedIdentity *model.LyricsSourceFixedIdentity) error {
	if !model.IsValidLyricsSourceProvider(provider) {
		return fmt.Errorf("invalid lyrics source provider %q", provider)
	}
	if !model.IsValidLyricsDiscoveryJobKind(kind) {
		return fmt.Errorf("invalid lyrics discovery job kind %q", kind)
	}
	if target.MusicID <= 0 {
		return errors.New("music ID must be positive")
	}
	if target.PageID < 0 || target.RevisionID < 0 || target.ArtifactID < 0 {
		return errors.New("page, revision, and artifact IDs cannot be negative")
	}
	if len(target.CatalogFingerprint) > maxLyricsDiscoveryFingerprintBytes || len(target.PolicyVersion) > maxLyricsDiscoveryPolicyVersionBytes {
		return errors.New("catalog fingerprint and policy version exceed safe limits")
	}
	if kind != model.LyricsDiscoveryJobFetchRevision && (candidate != nil || fixedIdentity != nil) {
		return errors.New("only fetch_revision jobs may carry a fixed candidate or identity")
	}
	switch kind {
	case model.LyricsDiscoveryJobDiscover:
		if target.PageID != 0 || target.RevisionID != 0 || target.ArtifactID != 0 || target.CatalogFingerprint == "" ||
			target.PolicyVersion == "" || target.ExpectedSHA1 != "" || target.FixedCandidate != nil {
			return errors.New("discover jobs require music ID, catalog fingerprint, and policy version only")
		}
	case model.LyricsDiscoveryJobFetchRevision:
		if target.PageID == 0 || target.RevisionID == 0 || !lyricsDiscoverySHA1Pattern.MatchString(target.ExpectedSHA1) ||
			target.CatalogFingerprint == "" || target.PolicyVersion == "" || target.FixedCandidate == nil ||
			target.FixedCandidate.PageID != target.PageID || target.FixedCandidate.RevisionID != target.RevisionID ||
			target.FixedCandidate.SHA1 != target.ExpectedSHA1 {
			return errors.New("fetch_revision jobs require a complete fixed candidate, catalog fingerprint, and policy version")
		}
		if _, _, err := canonicalLyricsDiscoveryFixedCandidateForProvider(provider, target.FixedCandidate, candidate, fixedIdentity); err != nil {
			return fmt.Errorf("fetch_revision jobs require a valid provider-scoped fixed candidate identity: %w", err)
		}
	case model.LyricsDiscoveryJobRevalidatePinned:
		if target.PageID == 0 || target.ArtifactID == 0 || target.ExpectedSHA1 != "" || target.FixedCandidate != nil {
			return errors.New("revalidate_pinned jobs require page and artifact IDs")
		}
	case model.LyricsDiscoveryJobRevalidateHead:
		if target.PageID == 0 || target.RevisionID != 0 || target.ExpectedSHA1 != "" || target.FixedCandidate != nil {
			return errors.New("revalidate_head jobs require page ID and no revision ID")
		}
	}
	return nil
}

func validateLyricsDiscoveryLease(lease LyricsDiscoveryJobLease) (string, error) {
	owner := strings.TrimSpace(lease.Owner)
	if owner == "" || len(owner) > maxLyricsDiscoveryLeaseOwnerBytes {
		return "", fmt.Errorf("lease owner must contain 1-%d bytes", maxLyricsDiscoveryLeaseOwnerBytes)
	}
	if lease.Duration <= 0 || lease.Duration > maxLyricsDiscoveryLeaseDuration {
		return "", fmt.Errorf("lease duration must be positive and at most %s", maxLyricsDiscoveryLeaseDuration)
	}
	return owner, nil
}

func requireLyricsDiscoveryJobTransition(ctx context.Context, tx *sql.Tx, result sql.Result, jobID int64) error {
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 1 {
		return nil
	}
	var state model.LyricsDiscoveryJobState
	if err := tx.QueryRowContext(ctx, `SELECT state FROM lyrics_discovery_jobs WHERE job_id=?`, jobID).Scan(&state); err == sql.ErrNoRows {
		return ErrLyricsDiscoveryJobNotFound
	} else if err != nil {
		return err
	}
	if model.IsTerminalLyricsDiscoveryJobState(state) {
		return fmt.Errorf("%w: %s", ErrLyricsDiscoveryJobTerminal, state)
	}
	return ErrLyricsDiscoveryLeaseNotOwned
}

type lyricsDiscoveryJobQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadLyricsDiscoveryJobContext(ctx context.Context, query lyricsDiscoveryJobQuery, suffix string, args ...any) (model.LyricsDiscoveryJob, error) {
	const columns = `job_id, idempotency_key, kind, state, music_id, page_id, revision_id, artifact_id,
		catalog_fingerprint, policy_version, expected_sha1, fixed_candidate_json,
		attempts, max_attempts, next_attempt_at, lease_owner, lease_expires_at, last_error_code,
		created_at, updated_at, completed_at, version`
	var job model.LyricsDiscoveryJob
	var pageID, revisionID, artifactID sql.NullInt64
	var fixedCandidateJSON string
	var leaseOwner, errorCode sql.NullString
	var nextAttemptAt, leaseExpiresAt, createdAt, updatedAt, completedAt sql.NullInt64
	err := query.QueryRowContext(ctx, `SELECT `+columns+` FROM lyrics_discovery_jobs `+suffix, args...).Scan(
		&job.ID, &job.IdempotencyKey, &job.Kind, &job.State, &job.Target.MusicID,
		&pageID, &revisionID, &artifactID, &job.Target.CatalogFingerprint, &job.Target.PolicyVersion, &job.Target.ExpectedSHA1,
		&fixedCandidateJSON, &job.Attempts, &job.MaxAttempts, &nextAttemptAt, &leaseOwner, &leaseExpiresAt, &errorCode,
		&createdAt, &updatedAt, &completedAt, &job.Version)
	if err == sql.ErrNoRows {
		return model.LyricsDiscoveryJob{}, ErrLyricsDiscoveryJobNotFound
	}
	if err != nil {
		return model.LyricsDiscoveryJob{}, err
	}
	job.Target.PageID = int(pageID.Int64)
	job.Target.RevisionID = int(revisionID.Int64)
	job.Target.ArtifactID = artifactID.Int64
	fixedCandidate, err := decodeLyricsDiscoveryFixedCandidate(fixedCandidateJSON)
	if err != nil {
		return model.LyricsDiscoveryJob{}, err
	}
	job.Target.FixedCandidate = legacyLyricsDiscoveryCandidateIdentity(fixedCandidate)
	job.LeaseOwner = leaseOwner.String
	job.LastErrorCode = errorCode.String
	job.NextAttemptAt = time.UnixMilli(nextAttemptAt.Int64).UTC()
	job.CreatedAt = time.UnixMilli(createdAt.Int64).UTC()
	job.UpdatedAt = time.UnixMilli(updatedAt.Int64).UTC()
	if leaseExpiresAt.Valid {
		job.LeaseExpiresAt = time.UnixMilli(leaseExpiresAt.Int64).UTC()
	}
	if completedAt.Valid {
		job.CompletedAt = time.UnixMilli(completedAt.Int64).UTC()
	}
	return job, nil
}

func canonicalLyricsDiscoveryTime(value time.Time) time.Time {
	return time.UnixMilli(value.UTC().UnixMilli()).UTC()
}

func nullablePositiveInt(value int) any {
	if value == 0 {
		return nil
	}
	return value
}

func nullablePositiveInt64(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func sameStringSlice(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sameIndexEvidenceRefs(left, right []model.LyricsSourceIndexEvidenceRef) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func loadLyricsDiscoveryFixedCandidateContext(ctx context.Context, query lyricsDiscoveryJobQuery, jobID int64) (*lyricssource.Candidate, error) {
	var value string
	if err := query.QueryRowContext(ctx, `SELECT fixed_candidate_json FROM lyrics_discovery_jobs WHERE job_id=?`, jobID).Scan(&value); err == sql.ErrNoRows {
		return nil, ErrLyricsDiscoveryJobNotFound
	} else if err != nil {
		return nil, err
	}
	return decodeLyricsDiscoveryFixedCandidate(value)
}

// GetLyricsDiscoveryJobProvenance exposes the provider-scoped target metadata
// that cannot be represented by the legacy model.LyricsDiscoveryJob shape.
func (s *Store) GetLyricsDiscoveryJobProvenance(ctx context.Context, jobID int64) (model.LyricsSourceProvider, *model.LyricsSourceFixedIdentity, string, error) {
	if ctx == nil || jobID <= 0 {
		return "", nil, "", errors.New("positive job ID and context are required")
	}
	var provider model.LyricsSourceProvider
	var identityJSON, status string
	if err := s.db.QueryRowContext(ctx, `SELECT provider,fixed_identity_json,provenance_status FROM lyrics_discovery_jobs WHERE job_id=?`, jobID).
		Scan(&provider, &identityJSON, &status); err == sql.ErrNoRows {
		return "", nil, "", ErrLyricsDiscoveryJobNotFound
	} else if err != nil {
		return "", nil, "", err
	}
	if !model.IsValidLyricsSourceProvider(provider) {
		return "", nil, "", errors.New("stored lyrics discovery provider is invalid")
	}
	if identityJSON == "" {
		return provider, nil, status, nil
	}
	identity, err := model.DecodeLyricsSourceFixedIdentity([]byte(identityJSON))
	if err != nil || identity.Provider != provider {
		return "", nil, "", errors.New("stored lyrics discovery fixed identity is invalid")
	}
	return provider, &identity, status, nil
}
