package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"moesekai/server/internal/legacy"
	"moesekai/server/internal/lyricsdiscovery"
	"moesekai/server/internal/lyricssource"
	"moesekai/server/internal/model"
)

const (
	LyricsDiscoveryShadowPolicyVersion = "shadow-v1"
	maxLyricsDiscoveryClockSkew        = time.Minute
)

var lyricsDiscoveryFingerprintPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

var _ lyricsdiscovery.Store = (*LyricsDiscoveryAdapter)(nil)

// LyricsDiscoveryAdapter is the only production persistence surface exposed to
// the shadow worker. It can mutate discovery queue/result rows, but it has no
// draft, publication, projection, or public-file operation.
type LyricsDiscoveryAdapter struct {
	store         *Store
	policyVersion string
	maxAttempts   int
}

func NewLyricsDiscoveryAdapter(store *Store, policyVersion string, maxAttempts int) (*LyricsDiscoveryAdapter, error) {
	if store == nil {
		return nil, errors.New("lyrics discovery adapter requires store")
	}
	policyVersion = strings.TrimSpace(policyVersion)
	if policyVersion == "" || len(policyVersion) > maxLyricsDiscoveryPolicyVersionBytes {
		return nil, fmt.Errorf("lyrics discovery policy version must contain 1-%d bytes", maxLyricsDiscoveryPolicyVersionBytes)
	}
	if maxAttempts == 0 {
		maxAttempts = DefaultLyricsDiscoveryJobMaxAttempts
	}
	if maxAttempts < 1 || maxAttempts > maxLyricsDiscoveryJobAttempts {
		return nil, fmt.Errorf("lyrics discovery max attempts must be between 1 and %d", maxLyricsDiscoveryJobAttempts)
	}
	return &LyricsDiscoveryAdapter{store: store, policyVersion: policyVersion, maxAttempts: maxAttempts}, nil
}

func (a *LyricsDiscoveryAdapter) Scan(ctx context.Context, request lyricsdiscovery.ScanRequest) (lyricsdiscovery.ScanResult, error) {
	if ctx == nil {
		return lyricsdiscovery.ScanResult{}, errors.New("lyrics discovery scan requires context")
	}
	catalog, err := a.store.LyricsDiscoveryCatalogContext(ctx)
	if err != nil {
		return lyricsdiscovery.ScanResult{}, err
	}
	targets := model.ClassifyCatalogLyricsTargets(func() []model.CatalogLyricsGroupingRecord {
		records := make([]model.CatalogLyricsGroupingRecord, 0, len(catalog))
		for _, item := range catalog {
			records = append(records, model.CatalogLyricsGroupingRecord{MusicID: item.MusicID, Fingerprint: item.CatalogFingerprint, Evidence: item.Evidence})
		}
		return records
	}())
	targetByMusicID := make(map[int]model.CatalogLyricsTarget, len(targets))
	for _, target := range targets {
		targetByMusicID[target.MusicID] = target
	}
	now := request.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	tx, err := a.store.db.BeginTx(ctx, nil)
	if err != nil {
		return lyricsdiscovery.ScanResult{}, err
	}
	defer tx.Rollback()
	scheduled := 0
	for _, item := range catalog {
		if err := ctx.Err(); err != nil {
			return lyricsdiscovery.ScanResult{}, err
		}
		target := targetByMusicID[item.MusicID]
		if target.Disposition == model.LyricsCatalogTargetFullTarget {
			_, created, err := enqueueLyricsDiscoveryJobTx(ctx, tx, EnqueueLyricsDiscoveryJobParams{
				Kind: model.LyricsDiscoveryJobDiscover,
				Target: model.LyricsDiscoveryJobTarget{
					MusicID: item.MusicID, CatalogFingerprint: item.CatalogFingerprint, PolicyVersion: a.policyVersion,
				},
				MaxAttempts: a.maxAttempts,
			}, now)
			if err != nil {
				return lyricsdiscovery.ScanResult{}, err
			}
			if created {
				scheduled++
			}
			continue
		}
		if target.Disposition == model.LyricsCatalogTargetReview {
			evidence, err := json.Marshal(map[string]any{"candidates": []any{}, "catalogReasonCode": target.ReasonCode})
			if err != nil {
				return lyricsdiscovery.ScanResult{}, err
			}
			if _, _, err := createLyricsSourceReviewTx(ctx, tx, createLyricsSourceReviewParams{
				Kind: LyricsSourceReviewKindCandidate, MusicID: item.MusicID, CatalogFingerprint: item.CatalogFingerprint,
				ReasonCode: target.ReasonCode, EvidenceJSON: evidence, CreatedAt: now,
			}); err != nil {
				return lyricsdiscovery.ScanResult{}, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return lyricsdiscovery.ScanResult{}, err
	}
	return lyricsdiscovery.ScanResult{Scheduled: scheduled}, nil
}

func (a *LyricsDiscoveryAdapter) Claim(ctx context.Context, request lyricsdiscovery.ClaimRequest) (lyricsdiscovery.Job, bool, error) {
	if ctx == nil {
		return lyricsdiscovery.Job{}, false, errors.New("lyrics discovery claim requires context")
	}
	job, err := a.store.ClaimLyricsDiscoveryJob(ctx, LyricsDiscoveryJobLease{
		Owner: request.WorkerID, Duration: request.LeaseDuration,
		Kind: model.LyricsDiscoveryJobDiscover, Now: request.Now,
	})
	if errors.Is(err, ErrLyricsDiscoveryJobNotFound) {
		return lyricsdiscovery.Job{}, false, nil
	}
	if err != nil {
		return lyricsdiscovery.Job{}, false, err
	}
	if job.Kind != model.LyricsDiscoveryJobDiscover || job.Target.CatalogFingerprint == "" || job.Target.PolicyVersion == "" {
		if failErr := a.failClaimedJob(ctx, job, "invalid_job"); failErr != nil {
			return lyricsdiscovery.Job{}, false, fmt.Errorf("dead-letter invalid discovery job: %w", failErr)
		}
		return lyricsdiscovery.Job{}, false, lyricsdiscovery.NewError(lyricsdiscovery.CodeInvalidJob, nil)
	}
	identity, err := a.store.CatalogMusicIdentity(job.Target.MusicID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			if failErr := a.failClaimedJob(ctx, job, "invalid_job"); failErr != nil {
				return lyricsdiscovery.Job{}, false, fmt.Errorf("dead-letter discovery job with missing catalog identity: %w", failErr)
			}
			return lyricsdiscovery.Job{}, false, lyricsdiscovery.NewError(lyricsdiscovery.CodeInvalidJob, err)
		}
		return lyricsdiscovery.Job{}, false, err
	}
	fingerprint := identity.CatalogFingerprint
	if fingerprint == "" {
		fingerprint, _ = model.CatalogLyricsEvidenceFingerprint(model.CatalogLyricsEvidence{
			Title: identity.JapaneseTitle, Lyricist: identity.Lyricist, Composer: identity.Composer, Arranger: identity.Arranger,
			Assetbundle: identity.AssetbundleName, VersionHint: identity.VersionHint, LyricsVersion: identity.LyricsVersion,
			Vocals: identity.Vocals, Presence: model.CatalogEvidencePresence{
				Lyricist: identity.Lyricist != "", Composer: identity.Composer != "", Arranger: identity.Arranger != "",
				Assetbundle: identity.AssetbundleName != "", VersionHint: identity.VersionHint != "",
				LyricsVersion: identity.LyricsVersionKnown, Vocals: len(identity.Vocals) > 0,
			},
		})
	}
	if fingerprint != job.Target.CatalogFingerprint {
		if failErr := a.failClaimedJob(ctx, job, "source_drift"); failErr != nil {
			return lyricsdiscovery.Job{}, false, fmt.Errorf("dead-letter source-drifted discovery job: %w", failErr)
		}
		return lyricsdiscovery.Job{}, false, lyricsdiscovery.NewError(lyricsdiscovery.CodeSourceDrift, nil)
	}
	return lyricsdiscovery.Job{
		ID: strconv.FormatInt(job.ID, 10), LeaseToken: encodeLyricsDiscoveryLeaseToken(job.Version),
		Attempt: job.Attempts, MusicID: identity.MusicID, JapaneseTitle: identity.JapaneseTitle,
		ProducerMetadata: identity.ProducerMetadata, Lyricist: identity.Lyricist, Composer: identity.Composer,
		Arranger:                    identity.Arranger,
		PerformerSegmentationPolicy: lyricssource.PerformerSegmentationPolicyFromCatalogVocals(identity.Vocals),
	}, true, nil
}

func (a *LyricsDiscoveryAdapter) Complete(ctx context.Context, completion lyricsdiscovery.Completion) error {
	jobID, version, err := parseLyricsDiscoveryLease(completion.JobID, completion.LeaseToken, completion.WorkerID)
	if err != nil {
		return err
	}
	if completion.CompletedAt.IsZero() {
		return lyricsdiscovery.NewError(lyricsdiscovery.CodeInvalidResult, errors.New("completion time is required"))
	}
	candidates, indexEvidence, err := decodeLyricsDiscoveryArtifact(
		completion.Result.Artifact, completion.Result.CandidateCount,
	)
	if err != nil {
		return lyricsdiscovery.NewError(lyricsdiscovery.CodeInvalidResult, err)
	}
	if _, err := validateLyricsDiscoveryShadowResult(completion.Result); err != nil {
		return err
	}
	return a.store.CompleteLyricsDiscoveryResult(ctx, CompleteLyricsDiscoveryResultParams{
		JobID: jobID, LeaseOwner: completion.WorkerID, ExpectedVersion: version,
		CompletedAt: completion.CompletedAt, ShadowResult: completion.Result,
		Candidates: candidates, IndexEvidence: indexEvidence,
	})
}

func (a *LyricsDiscoveryAdapter) Retry(ctx context.Context, retry lyricsdiscovery.Retry) error {
	jobID, version, err := parseLyricsDiscoveryLease(retry.JobID, retry.LeaseToken, retry.WorkerID)
	if err != nil {
		return err
	}
	if retry.Attempt <= 0 || retry.FailedAt.IsZero() || retry.NextAttemptAt.IsZero() || !isValidLyricsDiscoveryErrorCode(retry.Failure.Code) {
		return lyricsdiscovery.NewError(lyricsdiscovery.CodeInvalidJob, errors.New("invalid retry transition"))
	}
	_, err = a.store.RetryLyricsDiscoveryJob(ctx, jobID, retry.WorkerID, version, retry.Attempt, retry.FailedAt, retry.NextAttemptAt, string(retry.Failure.Code))
	return err
}

func (a *LyricsDiscoveryAdapter) Fail(ctx context.Context, failure lyricsdiscovery.TerminalFailure) error {
	jobID, version, err := parseLyricsDiscoveryLease(failure.JobID, failure.LeaseToken, failure.WorkerID)
	if err != nil {
		return err
	}
	if failure.Attempt <= 0 || failure.FailedAt.IsZero() || !isValidLyricsDiscoveryErrorCode(failure.Failure.Code) {
		return lyricsdiscovery.NewError(lyricsdiscovery.CodeInvalidJob, errors.New("invalid terminal transition"))
	}
	_, err = a.store.TerminalFailLyricsDiscoveryJob(ctx, jobID, failure.WorkerID, version, failure.Attempt, failure.FailedAt, string(failure.Failure.Code))
	return err
}

func (a *LyricsDiscoveryAdapter) failClaimedJob(ctx context.Context, job model.LyricsDiscoveryJob, code string) error {
	_, err := a.store.TerminalFailLyricsDiscoveryJob(ctx, job.ID, job.LeaseOwner, job.Version, job.Attempts, time.Now().UTC(), code)
	return err
}

func isValidLyricsDiscoveryErrorCode(code lyricsdiscovery.ErrorCode) bool {
	return lyricsdiscovery.Classify(lyricsdiscovery.NewError(code, nil)).Code == code
}

func encodeLyricsDiscoveryLeaseToken(version int64) string {
	return "v" + strconv.FormatInt(version, 10)
}

func parseLyricsDiscoveryLease(jobIDValue, token, workerID string) (int64, int64, error) {
	jobID, err := strconv.ParseInt(jobIDValue, 10, 64)
	if err != nil || jobID <= 0 || strconv.FormatInt(jobID, 10) != jobIDValue {
		return 0, 0, lyricsdiscovery.NewError(lyricsdiscovery.CodeInvalidJob, errors.New("invalid job ID"))
	}
	if !strings.HasPrefix(token, "v") || len(token) < 2 {
		return 0, 0, lyricsdiscovery.NewError(lyricsdiscovery.CodeInvalidJob, errors.New("invalid lease token"))
	}
	version, err := strconv.ParseInt(token[1:], 10, 64)
	if err != nil || version <= 0 || encodeLyricsDiscoveryLeaseToken(version) != token || strings.TrimSpace(workerID) == "" {
		return 0, 0, lyricsdiscovery.NewError(lyricsdiscovery.CodeInvalidJob, errors.New("invalid lease identity"))
	}
	return jobID, version, nil
}

type LyricsDiscoveryShadowCompletion struct {
	JobID           int64
	LeaseOwner      string
	ExpectedVersion int64
	CompletedAt     time.Time
	Result          lyricsdiscovery.Result
}

func (s *Store) CompleteLyricsDiscoveryShadowResult(ctx context.Context, completion LyricsDiscoveryShadowCompletion) error {
	if ctx == nil {
		return errors.New("lyrics discovery completion requires context")
	}
	owner := strings.TrimSpace(completion.LeaseOwner)
	if completion.JobID <= 0 || completion.ExpectedVersion <= 0 || owner == "" || len(owner) > maxLyricsDiscoveryLeaseOwnerBytes {
		return lyricsdiscovery.NewError(lyricsdiscovery.CodeInvalidJob, errors.New("invalid completion lease"))
	}
	if completion.CompletedAt.IsZero() || completion.CompletedAt.After(time.Now().UTC().Add(maxLyricsDiscoveryClockSkew)) {
		return lyricsdiscovery.NewError(lyricsdiscovery.CodeInvalidResult, errors.New("invalid completion time"))
	}
	if _, err := validateLyricsDiscoveryShadowResult(completion.Result); err != nil {
		return err
	}
	candidates, indexEvidence, err := decodeLyricsDiscoveryArtifact(
		completion.Result.Artifact, completion.Result.CandidateCount,
	)
	if err != nil {
		return lyricsdiscovery.NewError(lyricsdiscovery.CodeInvalidResult, err)
	}
	artifact, err := canonicalStoredLyricsDiscoveryArtifact(candidates)
	if err != nil {
		return lyricsdiscovery.NewError(lyricsdiscovery.CodeInvalidResult, err)
	}
	completedAt := canonicalLyricsDiscoveryTime(completion.CompletedAt)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	leaseCheckedAt := canonicalLyricsDiscoveryTime(time.Now().UTC())
	var job model.LyricsDiscoveryJob
	job, err = loadLyricsDiscoveryJobContext(ctx, tx, `WHERE job_id=?`, completion.JobID)
	if err != nil {
		return err
	}
	if job.State != model.LyricsDiscoveryJobLeased || job.LeaseOwner != owner || job.Version != completion.ExpectedVersion || !job.LeaseExpiresAt.After(leaseCheckedAt) {
		if model.IsTerminalLyricsDiscoveryJobState(job.State) {
			return fmt.Errorf("%w: %s", ErrLyricsDiscoveryJobTerminal, job.State)
		}
		return ErrLyricsDiscoveryLeaseNotOwned
	}
	if !lyricsDiscoveryFingerprintPattern.MatchString(job.Target.CatalogFingerprint) || strings.TrimSpace(job.Target.PolicyVersion) == "" {
		return lyricsdiscovery.NewError(lyricsdiscovery.CodeInvalidJob, errors.New("job generation identity is invalid"))
	}
	if err := insertOrVerifyLyricsIndexEvidenceCollectionTx(ctx, tx, indexEvidence, completedAt); err != nil {
		return err
	}
	inserted, err := tx.ExecContext(ctx, `INSERT INTO lyrics_discovery_shadow_results
		(job_id, music_id, catalog_fingerprint, policy_version, outcome, candidate_count, result_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		job.ID, job.Target.MusicID, job.Target.CatalogFingerprint, job.Target.PolicyVersion,
		completion.Result.Outcome, completion.Result.CandidateCount, string(artifact), completedAt.UnixMilli())
	if err != nil {
		return err
	}
	resultID, err := inserted.LastInsertId()
	if err != nil {
		return err
	}
	if err := linkLyricsDiscoveryResultEvidenceTx(ctx, tx, resultID, indexEvidence); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE lyrics_discovery_jobs
		SET state='succeeded', next_attempt_at=?, lease_owner=NULL, lease_expires_at=NULL,
			last_error_code=NULL, updated_at=?, completed_at=?, version=version+1
		WHERE job_id=? AND state='leased' AND lease_owner=? AND version=? AND lease_expires_at>?`,
		completedAt.UnixMilli(), completedAt.UnixMilli(), completedAt.UnixMilli(), job.ID, owner, completion.ExpectedVersion, leaseCheckedAt.UnixMilli())
	if err != nil {
		return err
	}
	if err := requireLyricsDiscoveryJobTransition(ctx, tx, result, job.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) TerminalFailLyricsDiscoveryJob(ctx context.Context, jobID int64, leaseOwner string, expectedVersion int64, expectedAttempt int, failedAt time.Time, errorCode string) (model.LyricsDiscoveryJob, error) {
	if ctx == nil {
		return model.LyricsDiscoveryJob{}, errors.New("lyrics discovery terminal failure requires context")
	}
	owner := strings.TrimSpace(leaseOwner)
	if jobID <= 0 || expectedVersion <= 0 || expectedAttempt <= 0 || owner == "" || len(owner) > maxLyricsDiscoveryLeaseOwnerBytes {
		return model.LyricsDiscoveryJob{}, errors.New("invalid lyrics discovery terminal lease")
	}
	if failedAt.IsZero() || failedAt.After(time.Now().UTC().Add(maxLyricsDiscoveryClockSkew)) {
		return model.LyricsDiscoveryJob{}, errors.New("invalid terminal failure time")
	}
	if strings.TrimSpace(errorCode) == "" {
		return model.LyricsDiscoveryJob{}, errors.New("error code is required")
	}
	errorCode = SanitizeLyricsDiscoveryErrorCode(errorCode)
	failedAt = canonicalLyricsDiscoveryTime(failedAt)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.LyricsDiscoveryJob{}, err
	}
	defer tx.Rollback()
	leaseCheckedAt := canonicalLyricsDiscoveryTime(time.Now().UTC())
	result, err := tx.ExecContext(ctx, `UPDATE lyrics_discovery_jobs
		SET state='dead_letter', attempts=max_attempts, next_attempt_at=?, lease_owner=NULL, lease_expires_at=NULL,
			last_error_code=?, updated_at=?, completed_at=?, version=version+1
		WHERE job_id=? AND state='leased' AND lease_owner=? AND version=? AND attempts=? AND lease_expires_at>?`,
		failedAt.UnixMilli(), errorCode, failedAt.UnixMilli(), failedAt.UnixMilli(), jobID, owner, expectedVersion, expectedAttempt, leaseCheckedAt.UnixMilli())
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

func validateLyricsDiscoveryShadowResult(result lyricsdiscovery.Result) ([]byte, error) {
	validCount := (result.Outcome == lyricsdiscovery.OutcomeCandidatesFound && result.CandidateCount == 1) ||
		(result.Outcome == lyricsdiscovery.OutcomeNoCandidates && result.CandidateCount == 0) ||
		(result.Outcome == lyricsdiscovery.OutcomeAmbiguous && result.CandidateCount > 1)
	if !validCount || result.CandidateCount > lyricsdiscovery.MaxCandidateArtifactCandidates ||
		len(result.Artifact) < 2 || len(result.Artifact) > maxLyricsDiscoveryTransportBytes {
		return nil, lyricsdiscovery.NewError(lyricsdiscovery.CodeInvalidResult, nil)
	}
	if err := legacy.ValidateUniqueJSON(result.Artifact); err != nil {
		return nil, lyricsdiscovery.NewError(lyricsdiscovery.CodeInvalidResult, err)
	}
	trimmed := bytes.TrimSpace(result.Artifact)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return nil, lyricsdiscovery.NewError(lyricsdiscovery.CodeInvalidResult, errors.New("shadow result must be a JSON object"))
	}
	return append([]byte(nil), result.Artifact...), nil
}
