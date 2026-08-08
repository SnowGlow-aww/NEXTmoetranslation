// Package lyricsdiscovery runs the shadow-mode scheduler and worker for lyrics
// source discovery. Its queue contract intentionally contains no draft-save or
// publication operations: discovery output can only be persisted as queue data.
package lyricsdiscovery

import (
	"context"
	"errors"
	"fmt"
	"time"

	"moesekai/server/internal/lyricssource"
)

// Store is the complete persistence surface available to the worker. Scan and
// Claim must only mutate discovery queue state and artifacts. Claim is also the
// lease-expiration boundary: before selecting work, an implementation must make
// expired leases eligible again and atomically assign a fresh lease token.
type Store interface {
	Scan(context.Context, ScanRequest) (ScanResult, error)
	Claim(context.Context, ClaimRequest) (Job, bool, error)
	Complete(context.Context, Completion) error
	Retry(context.Context, Retry) error
	Fail(context.Context, TerminalFailure) error
}

// Discovery executes source discovery without receiving an authoritative
// lyrics store. Any artifact it returns is persisted through Store.Complete.
type Discovery interface {
	Discover(context.Context, Job) (Result, error)
}

type ScanRequest struct {
	WorkerID string
	Now      time.Time
}

type ScanResult struct {
	Scheduled int
}

type ClaimRequest struct {
	WorkerID      string
	Now           time.Time
	LeaseDuration time.Duration
}

// Job is a queue-owned discovery input. Attempt is the one-based attempt number
// for the current claim. PerformerSegmentationPolicy must be derived from the
// current catalog rendition signals; the zero value fails closed. LeaseToken
// must be checked by every terminal queue mutation so a worker cannot finish a
// lease that has already expired.
type Job struct {
	ID                          string
	LeaseToken                  string
	Attempt                     int
	MusicID                     int
	JapaneseTitle               string
	ProducerMetadata            string
	Lyricist                    string
	Composer                    string
	Arranger                    string
	PerformerSegmentationPolicy lyricssource.PerformerSegmentationPolicy
}

type Outcome string

const (
	OutcomeCandidatesFound Outcome = "candidates_found"
	OutcomeNoCandidates    Outcome = "no_candidates"
	OutcomeAmbiguous       Outcome = "ambiguous"
)

func (o Outcome) valid() bool {
	switch o {
	case OutcomeCandidatesFound, OutcomeNoCandidates, OutcomeAmbiguous:
		return true
	default:
		return false
	}
}

// Result is shadow data only. Artifact is an opaque queue artifact owned by the
// integration adapter; the worker never interprets it or writes song lyrics.
type Result struct {
	Outcome        Outcome
	CandidateCount int
	Artifact       []byte
}

type Completion struct {
	JobID       string
	LeaseToken  string
	WorkerID    string
	CompletedAt time.Time
	Result      Result
}

type Retry struct {
	JobID         string
	LeaseToken    string
	WorkerID      string
	Attempt       int
	FailedAt      time.Time
	NextAttemptAt time.Time
	Failure       ClassifiedError
}

type TerminalFailure struct {
	JobID      string
	LeaseToken string
	WorkerID   string
	Attempt    int
	FailedAt   time.Time
	Failure    ClassifiedError
}

type ErrorCode string

const (
	CodeCanceled          ErrorCode = "canceled"
	CodeQueueUnavailable  ErrorCode = "queue_unavailable"
	CodeSourceUnavailable ErrorCode = "source_unavailable"
	CodeRateLimited       ErrorCode = "rate_limited"
	CodeTimeout           ErrorCode = "timeout"
	CodeTemporary         ErrorCode = "temporary"
	CodeInternal          ErrorCode = "internal"
	CodeInvalidJob        ErrorCode = "invalid_job"
	CodeInvalidResult     ErrorCode = "invalid_result"
	CodeNoMatch           ErrorCode = "no_match"
	CodeAmbiguous         ErrorCode = "ambiguous"
	CodeRestricted        ErrorCode = "restricted"
	CodeUnsupported       ErrorCode = "unsupported"
	CodeSourceDrift       ErrorCode = "source_drift"
)

// Error carries a stable code while keeping its underlying cause out of queue
// records and status snapshots.
type Error struct {
	Code ErrorCode
	Err  error
}

func (e *Error) Error() string {
	return safeMessage(e.Code)
}

func (e *Error) Unwrap() error {
	return e.Err
}

func NewError(code ErrorCode, cause error) *Error {
	return &Error{Code: code, Err: cause}
}

type ClassifiedError struct {
	Code      ErrorCode
	Retryable bool
	Message   string
}

// Classify maps discovery failures to a stable retry policy. Unknown failures
// are retried as internal errors; raw error strings are never returned.
func Classify(err error) ClassifiedError {
	if err == nil {
		return ClassifiedError{}
	}
	if errors.Is(err, context.Canceled) {
		return classified(CodeCanceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return classified(CodeTimeout)
	}
	var coded *Error
	if errors.As(err, &coded) && validCode(coded.Code) {
		return classified(coded.Code)
	}
	return classified(CodeInternal)
}

func classified(code ErrorCode) ClassifiedError {
	return ClassifiedError{Code: code, Retryable: retryable(code), Message: safeMessage(code)}
}

func retryable(code ErrorCode) bool {
	switch code {
	case CodeQueueUnavailable, CodeSourceUnavailable, CodeRateLimited, CodeTimeout, CodeTemporary, CodeInternal:
		return true
	default:
		return false
	}
}

func validCode(code ErrorCode) bool {
	switch code {
	case CodeCanceled, CodeQueueUnavailable, CodeSourceUnavailable, CodeRateLimited, CodeTimeout,
		CodeTemporary, CodeInternal, CodeInvalidJob, CodeInvalidResult, CodeNoMatch, CodeAmbiguous,
		CodeRestricted, CodeUnsupported, CodeSourceDrift:
		return true
	default:
		return false
	}
}

func safeMessage(code ErrorCode) string {
	switch code {
	case CodeCanceled:
		return "discovery work canceled"
	case CodeQueueUnavailable:
		return "discovery queue unavailable"
	case CodeSourceUnavailable:
		return "lyrics source unavailable"
	case CodeRateLimited:
		return "lyrics source rate limited"
	case CodeTimeout:
		return "lyrics discovery timed out"
	case CodeTemporary:
		return "temporary lyrics discovery failure"
	case CodeInvalidJob:
		return "invalid discovery job"
	case CodeInvalidResult:
		return "invalid discovery result"
	case CodeNoMatch:
		return "no acceptable source match"
	case CodeAmbiguous:
		return "source match is ambiguous"
	case CodeRestricted:
		return "source cannot be used"
	case CodeUnsupported:
		return "source format is unsupported"
	case CodeSourceDrift:
		return "source identity changed"
	default:
		return "lyrics discovery failed"
	}
}

var (
	ErrAlreadyStarted = errors.New("lyrics discovery worker already started")
	ErrDraining       = errors.New("lyrics discovery worker is draining")
	ErrCanceled       = errors.New("lyrics discovery worker is canceled")
)

// Clock and Timer make persisted retry timestamps and scheduler waits
// deterministic in tests.
type Clock interface {
	Now() time.Time
	NewTimer(time.Duration) Timer
}

type Timer interface {
	C() <-chan time.Time
	Stop() bool
}

type JitterFunc func(upperBound time.Duration) time.Duration

type Options struct {
	ScanInterval  time.Duration
	LeaseDuration time.Duration
	JobTimeout    time.Duration
	IdleWait      time.Duration
	RetryMin      time.Duration
	RetryMax      time.Duration
	Concurrency   int
	Clock         Clock
	Jitter        JitterFunc
}

const maxWorkerConcurrency = 16

func (o Options) validate() error {
	if o.ScanInterval <= 0 {
		return fmt.Errorf("scan interval must be positive")
	}
	if o.LeaseDuration <= 0 {
		return fmt.Errorf("lease duration must be positive")
	}
	if o.JobTimeout <= 0 {
		return fmt.Errorf("job timeout must be positive")
	}
	if o.JobTimeout >= o.LeaseDuration {
		return fmt.Errorf("job timeout must be shorter than lease duration")
	}
	if o.IdleWait <= 0 {
		return fmt.Errorf("idle wait must be positive")
	}
	if o.RetryMin <= 0 {
		return fmt.Errorf("retry minimum must be positive")
	}
	if o.RetryMax < o.RetryMin {
		return fmt.Errorf("retry maximum must be at least retry minimum")
	}
	if o.Concurrency < 1 || o.Concurrency > maxWorkerConcurrency {
		return fmt.Errorf("concurrency must be between 1 and %d", maxWorkerConcurrency)
	}
	return nil
}
