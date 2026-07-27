package lyricsdiscovery

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	mathrand "math/rand/v2"
	"sync"
	"time"
)

const maxStatusOutcomes = 16

type LifecycleState string

const (
	StateNew      LifecycleState = "new"
	StateRunning  LifecycleState = "running"
	StateDraining LifecycleState = "draining"
	StateCanceled LifecycleState = "canceled"
	StateStopped  LifecycleState = "stopped"
)

type WorkOutcome struct {
	JobID          string    `json:"jobId"`
	MusicID        int       `json:"musicId"`
	Attempt        int       `json:"attempt"`
	Disposition    string    `json:"disposition"`
	Outcome        Outcome   `json:"outcome,omitempty"`
	CandidateCount int       `json:"candidateCount,omitempty"`
	ErrorCode      ErrorCode `json:"errorCode,omitempty"`
	Error          string    `json:"error,omitempty"`
	NextAttemptAt  time.Time `json:"nextAttemptAt,omitempty"`
	FinishedAt     time.Time `json:"finishedAt"`
}

type Status struct {
	WorkerID       string         `json:"workerId"`
	State          LifecycleState `json:"state"`
	StartedAt      time.Time      `json:"startedAt,omitempty"`
	DrainStartedAt time.Time      `json:"drainStartedAt,omitempty"`
	StoppedAt      time.Time      `json:"stoppedAt,omitempty"`
	LastScanAt     time.Time      `json:"lastScanAt,omitempty"`
	LastClaimAt    time.Time      `json:"lastClaimAt,omitempty"`
	Scans          uint64         `json:"scans"`
	Scheduled      uint64         `json:"scheduled"`
	Claimed        uint64         `json:"claimed"`
	Completed      uint64         `json:"completed"`
	Retried        uint64         `json:"retried"`
	Terminal       uint64         `json:"terminal"`
	Outcomes       []WorkOutcome  `json:"outcomes"`
	LastErrorCode  ErrorCode      `json:"lastErrorCode,omitempty"`
	LastError      string         `json:"lastError,omitempty"`
}

// Worker owns one scanner/consumer loop. Drain closes admission to Scan and
// Claim while allowing a currently claimed job to finish. Cancel interrupts
// waits and in-flight discovery. Wait joins every worker goroutine.
type Worker struct {
	store     Store
	discovery Discovery
	opts      Options
	clock     Clock
	jitter    JitterFunc
	workerID  string

	admissionMu sync.Mutex
	mu          sync.Mutex
	state       LifecycleState
	ctx         context.Context
	cancel      context.CancelFunc
	drainCh     chan struct{}
	doneCh      chan struct{}
	started     bool
	draining    bool
	canceled    bool
	status      Status
}

func New(store Store, discovery Discovery, opts Options) (*Worker, error) {
	if store == nil {
		return nil, errors.New("lyrics discovery store is required")
	}
	if discovery == nil {
		return nil, errors.New("lyrics discovery executor is required")
	}
	if err := opts.validate(); err != nil {
		return nil, err
	}
	clock := opts.Clock
	if clock == nil {
		clock = realClock{}
	}
	jitter := opts.Jitter
	if jitter == nil {
		jitter = randomJitter
	}
	workerID, err := newWorkerID()
	if err != nil {
		return nil, fmt.Errorf("generate lyrics discovery worker identity: %w", err)
	}
	return &Worker{
		store:     store,
		discovery: discovery,
		opts:      opts,
		clock:     clock,
		jitter:    serializedJitter(jitter),
		workerID:  workerID,
		state:     StateNew,
		drainCh:   make(chan struct{}),
		doneCh:    make(chan struct{}),
		status: Status{
			WorkerID: workerID,
			State:    StateNew,
			Outcomes: []WorkOutcome{},
		},
	}, nil
}

func (w *Worker) Start(parent context.Context) error {
	if parent == nil {
		parent = context.Background()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started {
		return ErrAlreadyStarted
	}
	if w.canceled {
		return ErrCanceled
	}
	if w.draining {
		return ErrDraining
	}
	w.started = true
	w.ctx, w.cancel = context.WithCancel(parent)
	now := w.clock.Now()
	w.state = StateRunning
	w.status.State = StateRunning
	w.status.StartedAt = now
	go w.run()
	return nil
}

// Drain is idempotent. It prevents every future Scan and Claim, including when
// called before Start. It does not cancel a job that is already executing.
func (w *Worker) Drain() {
	w.mu.Lock()
	if w.draining {
		w.mu.Unlock()
		return
	}
	w.draining = true
	close(w.drainCh)
	if w.state != StateCanceled && w.state != StateStopped {
		w.state = StateDraining
		w.status.State = StateDraining
		w.status.DrainStartedAt = w.clock.Now()
	}
	w.mu.Unlock()

	// Wait for a Scan or Claim admitted before the drain marker to leave its
	// store call. The marker is closed first so the loop cannot win the mutex and
	// admit a later call while Drain is waiting.
	w.admissionMu.Lock()
	w.admissionMu.Unlock()
}

// Cancel is idempotent and interrupts scheduler waits and in-flight discovery.
func (w *Worker) Cancel() {
	w.mu.Lock()
	if w.canceled {
		w.mu.Unlock()
		return
	}
	w.canceled = true
	if !w.draining {
		w.draining = true
		close(w.drainCh)
	}
	w.state = StateCanceled
	w.status.State = StateCanceled
	cancel := w.cancel
	started := w.started
	if !started {
		w.status.StoppedAt = w.clock.Now()
		close(w.doneCh)
	}
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (w *Worker) Wait() {
	w.mu.Lock()
	started := w.started
	done := w.doneCh
	w.mu.Unlock()
	if started {
		<-done
	}
}

func (w *Worker) Status() Status {
	w.mu.Lock()
	defer w.mu.Unlock()
	status := w.status
	status.Outcomes = append([]WorkOutcome(nil), w.status.Outcomes...)
	return status
}

func (w *Worker) run() {
	defer func() {
		w.mu.Lock()
		if w.canceled {
			w.state = StateCanceled
			w.status.State = StateCanceled
		} else {
			w.state = StateStopped
			w.status.State = StateStopped
		}
		w.status.StoppedAt = w.clock.Now()
		close(w.doneCh)
		w.mu.Unlock()
	}()

	if !w.scanAndRecord() {
		return
	}
	nextScan := w.clock.Now().Add(w.opts.ScanInterval)
	for {
		if w.admissionClosed() {
			return
		}
		if !w.claimAndRun() {
			return
		}
		if w.admissionClosed() {
			return
		}
		now := w.clock.Now()
		if !now.Before(nextScan) {
			if !w.scanAndRecord() {
				return
			}
			nextScan = w.clock.Now().Add(w.opts.ScanInterval)
		}
		wait := w.opts.IdleWait
		if untilScan := nextScan.Sub(w.clock.Now()); untilScan < wait {
			wait = untilScan
		}
		if wait < 0 {
			wait = 0
		}
		if !w.wait(wait) {
			return
		}
	}
}

func (w *Worker) scanAndRecord() bool {
	w.admissionMu.Lock()
	if w.admissionClosed() {
		w.admissionMu.Unlock()
		return false
	}
	now := w.clock.Now()
	result, err := w.store.Scan(w.ctx, ScanRequest{WorkerID: w.workerID, Now: now})
	w.admissionMu.Unlock()
	if err != nil {
		if w.contextStopped(err) {
			return false
		}
		w.recordInfrastructureError(err)
		return true
	}
	w.mu.Lock()
	w.status.LastScanAt = now
	w.status.Scans++
	if result.Scheduled > 0 {
		w.status.Scheduled += uint64(result.Scheduled)
	}
	w.clearLastErrorLocked()
	w.mu.Unlock()
	return true
}

func (w *Worker) claimAndRun() bool {
	w.admissionMu.Lock()
	if w.admissionClosed() {
		w.admissionMu.Unlock()
		return false
	}
	now := w.clock.Now()
	job, ok, err := w.store.Claim(w.ctx, ClaimRequest{
		WorkerID:      w.workerID,
		Now:           now,
		LeaseDuration: w.opts.LeaseDuration,
	})
	w.admissionMu.Unlock()
	if err != nil {
		if w.contextStopped(err) {
			return false
		}
		w.recordInfrastructureError(err)
		return true
	}
	if !ok {
		return true
	}
	w.mu.Lock()
	w.status.LastClaimAt = now
	w.status.Claimed++
	w.clearLastErrorLocked()
	w.mu.Unlock()

	result, discoverErr := w.discovery.Discover(w.ctx, job)
	if discoverErr == nil && !validResult(result) {
		discoverErr = NewError(CodeInvalidResult, nil)
	}
	if discoverErr == nil {
		finishedAt := w.clock.Now()
		err = w.store.Complete(w.ctx, Completion{
			JobID: job.ID, LeaseToken: job.LeaseToken, WorkerID: w.workerID,
			CompletedAt: finishedAt, Result: cloneResult(result),
		})
		if err != nil {
			if w.contextStopped(err) {
				return false
			}
			w.recordInfrastructureError(err)
			return true
		}
		w.recordOutcome(WorkOutcome{
			JobID: job.ID, MusicID: job.MusicID, Attempt: job.Attempt,
			Disposition: "completed", Outcome: result.Outcome,
			CandidateCount: result.CandidateCount, FinishedAt: finishedAt,
		})
		return true
	}

	failure := Classify(discoverErr)
	if failure.Code == CodeCanceled && w.admissionClosed() {
		return false
	}
	failedAt := w.clock.Now()
	if failure.Retryable {
		nextAttemptAt := failedAt.Add(w.retryDelay(job.Attempt))
		err = w.store.Retry(w.ctx, Retry{
			JobID: job.ID, LeaseToken: job.LeaseToken, WorkerID: w.workerID, Attempt: job.Attempt,
			FailedAt: failedAt, NextAttemptAt: nextAttemptAt, Failure: failure,
		})
		if err != nil {
			if w.contextStopped(err) {
				return false
			}
			w.recordInfrastructureError(err)
			return true
		}
		w.recordOutcome(WorkOutcome{
			JobID: job.ID, MusicID: job.MusicID, Attempt: job.Attempt, Disposition: "retry",
			ErrorCode: failure.Code, Error: failure.Message, NextAttemptAt: nextAttemptAt, FinishedAt: failedAt,
		})
		return true
	}

	err = w.store.Fail(w.ctx, TerminalFailure{
		JobID: job.ID, LeaseToken: job.LeaseToken, WorkerID: w.workerID, Attempt: job.Attempt,
		FailedAt: failedAt, Failure: failure,
	})
	if err != nil {
		if w.contextStopped(err) {
			return false
		}
		w.recordInfrastructureError(err)
		return true
	}
	w.recordOutcome(WorkOutcome{
		JobID: job.ID, MusicID: job.MusicID, Attempt: job.Attempt, Disposition: "terminal",
		ErrorCode: failure.Code, Error: failure.Message, FinishedAt: failedAt,
	})
	return true
}

func (w *Worker) retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	base := w.opts.RetryMin
	for step := 1; step < attempt && base < w.opts.RetryMax; step++ {
		if base > w.opts.RetryMax/2 {
			base = w.opts.RetryMax
			break
		}
		base *= 2
	}
	if base > w.opts.RetryMax {
		base = w.opts.RetryMax
	}
	jitterBound := base / 2
	if jitterBound <= 0 {
		return base
	}
	remaining := w.opts.RetryMax - base
	if jitterBound > remaining {
		jitterBound = remaining
	}
	if jitterBound <= 0 {
		return base
	}
	jitter := w.jitter(jitterBound)
	if jitter < 0 {
		jitter = 0
	}
	if jitter > jitterBound {
		jitter = jitterBound
	}
	return base + jitter
}

func (w *Worker) wait(duration time.Duration) bool {
	if duration <= 0 {
		return !w.admissionClosed()
	}
	timer := w.clock.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-w.ctx.Done():
		return false
	case <-w.drainCh:
		return false
	case <-timer.C():
		return !w.admissionClosed()
	}
}

func (w *Worker) admissionClosed() bool {
	select {
	case <-w.drainCh:
		return true
	default:
	}
	select {
	case <-w.ctx.Done():
		return true
	default:
		return false
	}
}

func (w *Worker) contextStopped(err error) bool {
	if errors.Is(err, context.Canceled) {
		return true
	}
	select {
	case <-w.ctx.Done():
		return true
	default:
		return false
	}
}

func (w *Worker) recordInfrastructureError(err error) {
	failure := Classify(err)
	if failure.Code == CodeCanceled || failure.Code == CodeTimeout {
		select {
		case <-w.ctx.Done():
			return
		default:
		}
	}
	if failure.Code == CodeInternal {
		failure = classified(CodeQueueUnavailable)
	}
	w.mu.Lock()
	w.status.LastErrorCode = failure.Code
	w.status.LastError = failure.Message
	w.mu.Unlock()
}

func (w *Worker) recordOutcome(outcome WorkOutcome) {
	w.mu.Lock()
	switch outcome.Disposition {
	case "completed":
		w.status.Completed++
	case "retry":
		w.status.Retried++
	case "terminal":
		w.status.Terminal++
	}
	w.status.Outcomes = append(w.status.Outcomes, outcome)
	if overflow := len(w.status.Outcomes) - maxStatusOutcomes; overflow > 0 {
		copy(w.status.Outcomes, w.status.Outcomes[overflow:])
		w.status.Outcomes = w.status.Outcomes[:maxStatusOutcomes]
	}
	w.clearLastErrorLocked()
	w.mu.Unlock()
}

func (w *Worker) clearLastErrorLocked() {
	w.status.LastErrorCode = ""
	w.status.LastError = ""
}

func validResult(result Result) bool {
	if !result.Outcome.valid() || result.CandidateCount < 0 {
		return false
	}
	if result.Outcome == OutcomeCandidatesFound && result.CandidateCount == 0 {
		return false
	}
	if result.Outcome != OutcomeCandidatesFound && result.CandidateCount != 0 {
		return false
	}
	return true
}

func cloneResult(result Result) Result {
	result.Artifact = append([]byte(nil), result.Artifact...)
	return result
}

func serializedJitter(jitter JitterFunc) JitterFunc {
	var mu sync.Mutex
	return func(upperBound time.Duration) time.Duration {
		mu.Lock()
		defer mu.Unlock()
		return jitter(upperBound)
	}
}

func randomJitter(upperBound time.Duration) time.Duration {
	if upperBound <= 0 {
		return 0
	}
	if upperBound == time.Duration(math.MaxInt64) {
		return time.Duration(mathrand.Int64())
	}
	return time.Duration(mathrand.Int64N(int64(upperBound) + 1))
}

func newWorkerID() (string, error) {
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return "lyrics-discovery-" + hex.EncodeToString(random[:]), nil
}

type realClock struct{}

func (realClock) Now() time.Time {
	return time.Now().UTC()
}

func (realClock) NewTimer(duration time.Duration) Timer {
	return realTimer{timer: time.NewTimer(duration)}
}

type realTimer struct {
	timer *time.Timer
}

func (t realTimer) C() <-chan time.Time {
	return t.timer.C
}

func (t realTimer) Stop() bool {
	return t.timer.Stop()
}
