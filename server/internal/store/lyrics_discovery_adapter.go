package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"moesekai/server/internal/legacy"
	"moesekai/server/internal/lyricsdiscovery"
	"moesekai/server/internal/model"
)

const (
	LyricsDiscoveryShadowPolicyVersion = "shadow-v1"
	maxLyricsDiscoveryResultBytes      = 1 << 20
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

func (a *LyricsDiscoveryAdapter) Scan(ctx context.Context, _ lyricsdiscovery.ScanRequest) (lyricsdiscovery.ScanResult, error) {
	if ctx == nil {
		return lyricsdiscovery.ScanResult{}, errors.New("lyrics discovery scan requires context")
	}
	catalog, err := a.store.LyricsDiscoveryCatalogContext(ctx)
	if err != nil {
		return lyricsdiscovery.ScanResult{}, err
	}
	scheduled := 0
	for _, item := range catalog {
		if err := ctx.Err(); err != nil {
			return lyricsdiscovery.ScanResult{}, err
		}
		_, created, err := a.store.EnqueueLyricsDiscoveryJob(ctx, EnqueueLyricsDiscoveryJobParams{
			Kind: model.LyricsDiscoveryJobDiscover,
			Target: model.LyricsDiscoveryJobTarget{
				MusicID: item.MusicID, CatalogFingerprint: item.CatalogFingerprint, PolicyVersion: a.policyVersion,
			},
			MaxAttempts: a.maxAttempts,
		})
		if err != nil {
			return lyricsdiscovery.ScanResult{}, err
		}
		if created {
			scheduled++
		}
	}
	return lyricsdiscovery.ScanResult{Scheduled: scheduled}, nil
}

func (a *LyricsDiscoveryAdapter) Claim(ctx context.Context, request lyricsdiscovery.ClaimRequest) (lyricsdiscovery.Job, bool, error) {
	if ctx == nil {
		return lyricsdiscovery.Job{}, false, errors.New("lyrics discovery claim requires context")
	}
	job, err := a.store.ClaimLyricsDiscoveryJob(ctx, LyricsDiscoveryJobLease{
		Owner: request.WorkerID, Duration: request.LeaseDuration,
	})
	if errors.Is(err, ErrLyricsDiscoveryJobNotFound) {
		return lyricsdiscovery.Job{}, false, nil
	}
	if err != nil {
		return lyricsdiscovery.Job{}, false, err
	}
	if job.Kind != model.LyricsDiscoveryJobDiscover || job.Target.CatalogFingerprint == "" || job.Target.PolicyVersion == "" {
		_ = a.failClaimedJob(ctx, job, "invalid_job")
		return lyricsdiscovery.Job{}, false, lyricsdiscovery.NewError(lyricsdiscovery.CodeInvalidJob, nil)
	}
	identity, err := a.store.CatalogMusicIdentity(job.Target.MusicID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			_ = a.failClaimedJob(ctx, job, "invalid_job")
			return lyricsdiscovery.Job{}, false, lyricsdiscovery.NewError(lyricsdiscovery.CodeInvalidJob, err)
		}
		return lyricsdiscovery.Job{}, false, err
	}
	fingerprint := lyricsDiscoveryCatalogFingerprint(LyricsDiscoveryCatalogItem{
		MusicID: identity.MusicID, JapaneseTitle: identity.JapaneseTitle, ProducerMetadata: identity.ProducerMetadata,
	})
	if fingerprint != job.Target.CatalogFingerprint {
		_ = a.failClaimedJob(ctx, job, "source_drift")
		return lyricsdiscovery.Job{}, false, lyricsdiscovery.NewError(lyricsdiscovery.CodeSourceDrift, nil)
	}
	return lyricsdiscovery.Job{
		ID: strconv.FormatInt(job.ID, 10), LeaseToken: encodeLyricsDiscoveryLeaseToken(job.Version),
		Attempt: job.Attempts, MusicID: identity.MusicID, JapaneseTitle: identity.JapaneseTitle,
		ProducerMetadata: identity.ProducerMetadata,
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
	return a.store.CompleteLyricsDiscoveryShadowResult(ctx, LyricsDiscoveryShadowCompletion{
		JobID: jobID, LeaseOwner: completion.WorkerID, ExpectedVersion: version,
		CompletedAt: completion.CompletedAt, Result: completion.Result,
	})
}

func (a *LyricsDiscoveryAdapter) Retry(ctx context.Context, retry lyricsdiscovery.Retry) error {
	jobID, version, err := parseLyricsDiscoveryLease(retry.JobID, retry.LeaseToken, retry.WorkerID)
	if err != nil {
		return err
	}
	if retry.Attempt <= 0 || retry.NextAttemptAt.IsZero() || strings.TrimSpace(string(retry.Failure.Code)) == "" {
		return lyricsdiscovery.NewError(lyricsdiscovery.CodeInvalidJob, errors.New("invalid retry transition"))
	}
	_, err = a.store.RetryLyricsDiscoveryJob(ctx, jobID, retry.WorkerID, version, retry.NextAttemptAt, string(retry.Failure.Code))
	return err
}

func (a *LyricsDiscoveryAdapter) Fail(ctx context.Context, failure lyricsdiscovery.TerminalFailure) error {
	jobID, version, err := parseLyricsDiscoveryLease(failure.JobID, failure.LeaseToken, failure.WorkerID)
	if err != nil {
		return err
	}
	if failure.Attempt <= 0 || failure.FailedAt.IsZero() || strings.TrimSpace(string(failure.Failure.Code)) == "" {
		return lyricsdiscovery.NewError(lyricsdiscovery.CodeInvalidJob, errors.New("invalid terminal transition"))
	}
	_, err = a.store.TerminalFailLyricsDiscoveryJob(ctx, jobID, failure.WorkerID, version, failure.FailedAt, string(failure.Failure.Code))
	return err
}

func (a *LyricsDiscoveryAdapter) failClaimedJob(ctx context.Context, job model.LyricsDiscoveryJob, code string) error {
	_, err := a.store.TerminalFailLyricsDiscoveryJob(ctx, job.ID, job.LeaseOwner, job.Version, time.Now().UTC(), code)
	return err
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
	artifact, err := validateLyricsDiscoveryShadowResult(completion.Result)
	if err != nil {
		return err
	}
	completedAt := canonicalLyricsDiscoveryTime(completion.CompletedAt)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var job model.LyricsDiscoveryJob
	job, err = loadLyricsDiscoveryJobContext(ctx, tx, `WHERE job_id=?`, completion.JobID)
	if err != nil {
		return err
	}
	if job.State != model.LyricsDiscoveryJobLeased || job.LeaseOwner != owner || job.Version != completion.ExpectedVersion {
		if model.IsTerminalLyricsDiscoveryJobState(job.State) {
			return fmt.Errorf("%w: %s", ErrLyricsDiscoveryJobTerminal, job.State)
		}
		return ErrLyricsDiscoveryLeaseNotOwned
	}
	if !lyricsDiscoveryFingerprintPattern.MatchString(job.Target.CatalogFingerprint) || strings.TrimSpace(job.Target.PolicyVersion) == "" {
		return lyricsdiscovery.NewError(lyricsdiscovery.CodeInvalidJob, errors.New("job generation identity is invalid"))
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO lyrics_discovery_shadow_results
		(job_id, music_id, catalog_fingerprint, policy_version, outcome, candidate_count, result_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		job.ID, job.Target.MusicID, job.Target.CatalogFingerprint, job.Target.PolicyVersion,
		completion.Result.Outcome, completion.Result.CandidateCount, string(artifact), completedAt.UnixMilli()); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE lyrics_discovery_jobs
		SET state='succeeded', next_attempt_at=?, lease_owner=NULL, lease_expires_at=NULL,
			last_error_code=NULL, updated_at=?, completed_at=?, version=version+1
		WHERE job_id=? AND state='leased' AND lease_owner=? AND version=?`,
		completedAt.UnixMilli(), completedAt.UnixMilli(), completedAt.UnixMilli(), job.ID, owner, completion.ExpectedVersion)
	if err != nil {
		return err
	}
	if err := requireLyricsDiscoveryJobTransition(ctx, tx, result, job.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) TerminalFailLyricsDiscoveryJob(ctx context.Context, jobID int64, leaseOwner string, expectedVersion int64, failedAt time.Time, errorCode string) (model.LyricsDiscoveryJob, error) {
	if ctx == nil {
		return model.LyricsDiscoveryJob{}, errors.New("lyrics discovery terminal failure requires context")
	}
	owner := strings.TrimSpace(leaseOwner)
	if jobID <= 0 || expectedVersion <= 0 || owner == "" || len(owner) > maxLyricsDiscoveryLeaseOwnerBytes {
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
	result, err := tx.ExecContext(ctx, `UPDATE lyrics_discovery_jobs
		SET state='dead_letter', attempts=max_attempts, next_attempt_at=?, lease_owner=NULL, lease_expires_at=NULL,
			last_error_code=?, updated_at=?, completed_at=?, version=version+1
		WHERE job_id=? AND state='leased' AND lease_owner=? AND version=?`,
		failedAt.UnixMilli(), errorCode, failedAt.UnixMilli(), failedAt.UnixMilli(), jobID, owner, expectedVersion)
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
	if !validCount || len(result.Artifact) < 2 || len(result.Artifact) > maxLyricsDiscoveryResultBytes {
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
