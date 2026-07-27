package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"moesekai/server/internal/model"
)

const (
	DefaultLyricsDiscoveryJobMaxAttempts = 8
	maxLyricsDiscoveryJobAttempts        = 100
	maxLyricsDiscoveryLeaseOwnerBytes    = 128
	maxLyricsDiscoveryErrorCodeBytes     = 64
	maxLyricsDiscoveryLeaseDuration      = 24 * time.Hour
	maxLyricsDiscoveryRetryDelay         = 30 * 24 * time.Hour
)

var (
	ErrLyricsDiscoveryJobNotFound   = errors.New("lyrics discovery job not found")
	ErrLyricsDiscoveryLeaseNotOwned = errors.New("lyrics discovery job lease not owned")
	ErrLyricsDiscoveryJobTerminal   = errors.New("lyrics discovery job is terminal")

	lyricsDiscoveryErrorCodePattern = regexp.MustCompile(`^[a-z0-9_]+$`)
)

type EnqueueLyricsDiscoveryJobParams struct {
	Kind        model.LyricsDiscoveryJobKind
	Target      model.LyricsDiscoveryJobTarget
	MaxAttempts int
	NotBefore   time.Time
}

type LyricsDiscoveryJobLease struct {
	Owner    string
	Duration time.Duration
}

func LyricsDiscoveryJobIdempotencyKey(kind model.LyricsDiscoveryJobKind, target model.LyricsDiscoveryJobTarget) (string, error) {
	if err := validateLyricsDiscoveryJobTarget(kind, target); err != nil {
		return "", err
	}
	canonical := fmt.Sprintf("v1\x00%s\x00%d\x00%d\x00%d\x00%d", kind, target.MusicID, target.PageID, target.RevisionID, target.ArtifactID)
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:]), nil
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
	key, err := LyricsDiscoveryJobIdempotencyKey(params.Kind, params.Target)
	if err != nil {
		return model.LyricsDiscoveryJob{}, false, err
	}
	maxAttempts := params.MaxAttempts
	if maxAttempts == 0 {
		maxAttempts = DefaultLyricsDiscoveryJobMaxAttempts
	}
	if maxAttempts < 1 || maxAttempts > maxLyricsDiscoveryJobAttempts {
		return model.LyricsDiscoveryJob{}, false, fmt.Errorf("max attempts must be between 1 and %d", maxLyricsDiscoveryJobAttempts)
	}
	now := time.Now().UTC()
	notBefore := params.NotBefore
	if notBefore.IsZero() {
		notBefore = now
	}
	notBefore = canonicalLyricsDiscoveryTime(notBefore)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.LyricsDiscoveryJob{}, false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO lyrics_discovery_jobs
		(idempotency_key, kind, state, music_id, page_id, revision_id, artifact_id,
		 attempts, max_attempts, next_attempt_at, created_at, updated_at, version)
		VALUES (?, ?, 'queued', ?, ?, ?, ?, 0, ?, ?, ?, ?, 1)
		ON CONFLICT(idempotency_key) DO NOTHING`,
		key, params.Kind, params.Target.MusicID, nullablePositiveInt(params.Target.PageID),
		nullablePositiveInt(params.Target.RevisionID), nullablePositiveInt64(params.Target.ArtifactID),
		maxAttempts, notBefore.UnixMilli(), now.UnixMilli(), now.UnixMilli())
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
	if err := tx.Commit(); err != nil {
		return model.LyricsDiscoveryJob{}, false, err
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
	now := time.Now().UTC()
	expiresAt := now.Add(lease.Duration)
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
		WHERE state='leased' AND lease_expires_at<=?`,
		now.UnixMilli(), now.UnixMilli(), now.UnixMilli(), now.UnixMilli()); err != nil {
		return model.LyricsDiscoveryJob{}, err
	}

	var jobID int64
	err = tx.QueryRowContext(ctx, `SELECT job_id FROM lyrics_discovery_jobs
		WHERE state IN ('queued', 'retry_wait') AND next_attempt_at<=? AND attempts<max_attempts
		ORDER BY next_attempt_at, job_id LIMIT 1`, now.UnixMilli()).Scan(&jobID)
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
		WHERE job_id=? AND state IN ('queued', 'retry_wait') AND next_attempt_at<=? AND attempts<max_attempts`,
		owner, expiresAt.UnixMilli(), now.UnixMilli(), jobID, now.UnixMilli())
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
	return s.transitionLeasedLyricsDiscoveryJob(ctx, jobID, leaseOwner, expectedVersion, model.LyricsDiscoveryJobSucceeded, time.Time{}, "")
}

func (s *Store) RetryLyricsDiscoveryJob(ctx context.Context, jobID int64, leaseOwner string, expectedVersion int64, notBefore time.Time, errorCode string) (model.LyricsDiscoveryJob, error) {
	if notBefore.IsZero() {
		return model.LyricsDiscoveryJob{}, errors.New("retry time is required")
	}
	if !notBefore.After(time.Now().UTC()) {
		return model.LyricsDiscoveryJob{}, errors.New("retry time must be in the future")
	}
	if notBefore.After(time.Now().UTC().Add(maxLyricsDiscoveryRetryDelay)) {
		return model.LyricsDiscoveryJob{}, fmt.Errorf("retry time must be within %s", maxLyricsDiscoveryRetryDelay)
	}
	return s.transitionLeasedLyricsDiscoveryJob(ctx, jobID, leaseOwner, expectedVersion, model.LyricsDiscoveryJobRetryWait, canonicalLyricsDiscoveryTime(notBefore), errorCode)
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
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.LyricsDiscoveryJob{}, err
	}
	defer tx.Rollback()
	var attempts, maxAttempts int
	if err := tx.QueryRowContext(ctx, `SELECT attempts, max_attempts FROM lyrics_discovery_jobs
		WHERE job_id=? AND state='leased' AND lease_owner=? AND version=?`,
		jobID, owner, expectedVersion).Scan(&attempts, &maxAttempts); err == sql.ErrNoRows {
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
	if _, err := tx.ExecContext(ctx, `UPDATE lyrics_discovery_jobs
		SET state=?, next_attempt_at=?, lease_owner=NULL, lease_expires_at=NULL,
			last_error_code=?, updated_at=?, completed_at=?, version=version+1
		WHERE job_id=? AND state='leased' AND lease_owner=? AND version=?`,
		nextState, now.UnixMilli(), errorCode, now.UnixMilli(), completedAt, jobID, owner, expectedVersion); err != nil {
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

func (s *Store) transitionLeasedLyricsDiscoveryJob(ctx context.Context, jobID int64, leaseOwner string, expectedVersion int64, nextState model.LyricsDiscoveryJobState, notBefore time.Time, errorCode string) (model.LyricsDiscoveryJob, error) {
	if ctx == nil {
		return model.LyricsDiscoveryJob{}, errors.New("lyrics discovery transition requires context")
	}
	owner := strings.TrimSpace(leaseOwner)
	if jobID <= 0 || expectedVersion <= 0 {
		return model.LyricsDiscoveryJob{}, errors.New("job ID and expected version must be positive")
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
	now := time.Now().UTC()
	nextAttemptAt := now
	var completedAt any
	if nextState == model.LyricsDiscoveryJobRetryWait {
		nextAttemptAt = notBefore
	} else {
		completedAt = now.UnixMilli()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.LyricsDiscoveryJob{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE lyrics_discovery_jobs
		SET state=?, next_attempt_at=?, lease_owner=NULL, lease_expires_at=NULL,
			last_error_code=?, updated_at=?, completed_at=?, version=version+1
		WHERE job_id=? AND state='leased' AND lease_owner=? AND version=?`,
		nextState, nextAttemptAt.UnixMilli(), nullableString(errorCode), now.UnixMilli(), completedAt,
		jobID, owner, expectedVersion)
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
	if !model.IsValidLyricsDiscoveryJobKind(kind) {
		return fmt.Errorf("invalid lyrics discovery job kind %q", kind)
	}
	if target.MusicID <= 0 {
		return errors.New("music ID must be positive")
	}
	if target.PageID < 0 || target.RevisionID < 0 || target.ArtifactID < 0 {
		return errors.New("page, revision, and artifact IDs cannot be negative")
	}
	switch kind {
	case model.LyricsDiscoveryJobDiscover:
		if target.PageID != 0 || target.RevisionID != 0 || target.ArtifactID != 0 {
			return errors.New("discover jobs only target music ID")
		}
	case model.LyricsDiscoveryJobFetchRevision:
		if target.PageID == 0 || target.RevisionID == 0 {
			return errors.New("fetch_revision jobs require page and revision IDs")
		}
	case model.LyricsDiscoveryJobRevalidatePinned:
		if target.PageID == 0 || target.ArtifactID == 0 {
			return errors.New("revalidate_pinned jobs require page and artifact IDs")
		}
	case model.LyricsDiscoveryJobRevalidateHead:
		if target.PageID == 0 || target.RevisionID != 0 {
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
		attempts, max_attempts, next_attempt_at, lease_owner, lease_expires_at, last_error_code,
		created_at, updated_at, completed_at, version`
	var job model.LyricsDiscoveryJob
	var pageID, revisionID, artifactID sql.NullInt64
	var leaseOwner, errorCode sql.NullString
	var nextAttemptAt, leaseExpiresAt, createdAt, updatedAt, completedAt sql.NullInt64
	err := query.QueryRowContext(ctx, `SELECT `+columns+` FROM lyrics_discovery_jobs `+suffix, args...).Scan(
		&job.ID, &job.IdempotencyKey, &job.Kind, &job.State, &job.Target.MusicID,
		&pageID, &revisionID, &artifactID, &job.Attempts, &job.MaxAttempts, &nextAttemptAt,
		&leaseOwner, &leaseExpiresAt, &errorCode, &createdAt, &updatedAt, &completedAt, &job.Version)
	if err == sql.ErrNoRows {
		return model.LyricsDiscoveryJob{}, ErrLyricsDiscoveryJobNotFound
	}
	if err != nil {
		return model.LyricsDiscoveryJob{}, err
	}
	job.Target.PageID = int(pageID.Int64)
	job.Target.RevisionID = int(revisionID.Int64)
	job.Target.ArtifactID = artifactID.Int64
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
