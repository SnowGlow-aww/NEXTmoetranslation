package lyricsdiscovery

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"moesekai/server/internal/lyricssource"
	"moesekai/server/internal/model"
)

// FixedRevisionSource is the bounded exact-revision source surface available to
// the private Phase 2 fetch worker.
type FixedRevisionSource interface {
	FetchFixedCandidateRevision(context.Context, lyricssource.MusicIdentity, lyricssource.Candidate) (lyricssource.FixedRevision, error)
}

type FetchJob struct {
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
	FixedCandidate              lyricssource.Candidate
}

type FetchResult struct {
	Fixed        lyricssource.FixedRevision
	Evidence     []model.LyricsSourceEvidence
	Associations []model.LyricsSourceAssociation
}

type FetchCompletion struct {
	JobID       string
	LeaseToken  string
	WorkerID    string
	CompletedAt time.Time
	Result      FetchResult
}

type FetchStore interface {
	ClaimFetch(context.Context, ClaimRequest) (FetchJob, bool, error)
	CompleteFetch(context.Context, FetchCompletion) error
	RetryFetch(context.Context, Retry) error
	FailFetch(context.Context, TerminalFailure) error
}

type FetchExecutor struct {
	source FixedRevisionSource
}

func NewFetchExecutor(source FixedRevisionSource) (*FetchExecutor, error) {
	if source == nil {
		return nil, errors.New("lyrics source fixed-revision client is required")
	}
	return &FetchExecutor{source: source}, nil
}

func NewDefaultFetchExecutor() (*FetchExecutor, error) {
	registry, err := lyricssource.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	return NewFetchExecutor(registry)
}

func (e *FetchExecutor) Fetch(ctx context.Context, job FetchJob) (FetchResult, error) {
	if ctx == nil || job.MusicID <= 0 || job.FixedCandidate.PageID <= 0 || job.FixedCandidate.RevisionID <= 0 ||
		!lyricssource.HasCanonicalSHA1(job.FixedCandidate.SHA1) || strings.TrimSpace(job.JapaneseTitle) == "" ||
		strings.TrimSpace(job.ProducerMetadata) == "" {
		return FetchResult{}, NewError(CodeInvalidJob, nil)
	}
	fixed, err := e.source.FetchFixedCandidateRevision(ctx, lyricssource.MusicIdentity{
		MusicID: job.MusicID, JapaneseTitle: job.JapaneseTitle, ProducerMetadata: job.ProducerMetadata,
		Lyricist: job.Lyricist, Composer: job.Composer, Arranger: job.Arranger,
		PerformerSegmentationPolicy: job.PerformerSegmentationPolicy,
	}, job.FixedCandidate)
	if err != nil {
		return FetchResult{}, classifySourceError(err)
	}
	evidence := []model.LyricsSourceEvidence{
		{RuleID: "fixed_revision_identity", Gate: "identity", Outcome: "passed", Summary: fmt.Sprintf("page=%d revision=%d", fixed.PageID, fixed.RevisionID)},
		{RuleID: "catalog_identity", Gate: "identity", Outcome: "passed", Summary: "title and role-bound credit evidence matched"},
		{RuleID: "source_restrictions", Gate: "source_use", Outcome: "passed", Summary: "no supported no-reprint restriction detected"},
		{RuleID: "lyrics_section_parse", Gate: "parse", Outcome: "passed", Summary: strconv.Itoa(len(fixed.Lines)) + " extracted lines"},
	}
	return FetchResult{Fixed: fixed, Evidence: evidence}, nil
}

// FetchWorker owns a bounded set of private fetch consumers. It deliberately
// has no Scan method: discover workers enqueue exact jobs and this worker only
// claims the fetch_revision kind.
type FetchWorker struct {
	store    FetchStore
	executor *FetchExecutor
	opts     Options
	clock    Clock
	jitter   JitterFunc
	workerID string

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
	workerWG    sync.WaitGroup
}

func NewFetchWorker(store FetchStore, executor *FetchExecutor, opts Options) (*FetchWorker, error) {
	if store == nil || executor == nil {
		return nil, errors.New("lyrics source fetch store and executor are required")
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
	id, err := newWorkerID()
	if err != nil {
		return nil, err
	}
	return &FetchWorker{store: store, executor: executor, opts: opts, clock: clock, jitter: serializedJitter(jitter),
		workerID: strings.Replace(id, "lyrics-discovery-", "lyrics-source-fetch-", 1), state: StateNew,
		drainCh: make(chan struct{}), doneCh: make(chan struct{})}, nil
}

func (w *FetchWorker) Start(parent context.Context) error {
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
	w.state = StateRunning
	w.ctx, w.cancel = context.WithCancel(parent)
	go w.run()
	return nil
}

func (w *FetchWorker) Drain() {
	w.mu.Lock()
	if !w.draining {
		w.draining = true
		close(w.drainCh)
		if w.state != StateCanceled && w.state != StateStopped {
			w.state = StateDraining
		}
	}
	w.mu.Unlock()
	w.admissionMu.Lock()
	w.admissionMu.Unlock()
}

func (w *FetchWorker) Cancel() {
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
	cancel := w.cancel
	if !w.started {
		close(w.doneCh)
	}
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (w *FetchWorker) Wait() {
	w.mu.Lock()
	started, done := w.started, w.doneCh
	w.mu.Unlock()
	if started {
		<-done
	}
}

func (w *FetchWorker) State() LifecycleState {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.state
}

func (w *FetchWorker) run() {
	defer func() {
		w.workerWG.Wait()
		w.mu.Lock()
		if !w.canceled {
			w.state = StateStopped
		}
		close(w.doneCh)
		w.mu.Unlock()
	}()
	for range w.opts.Concurrency {
		w.workerWG.Add(1)
		go w.consume()
	}
}

func (w *FetchWorker) consume() {
	defer w.workerWG.Done()
	for {
		if w.closed() || !w.claimAndRun() || !w.wait(w.opts.IdleWait) {
			return
		}
	}
}

func (w *FetchWorker) claimAndRun() bool {
	w.admissionMu.Lock()
	if w.closed() {
		w.admissionMu.Unlock()
		return false
	}
	job, ok, err := w.store.ClaimFetch(w.ctx, ClaimRequest{WorkerID: w.workerID, Now: w.clock.Now(), LeaseDuration: w.opts.LeaseDuration})
	w.admissionMu.Unlock()
	if err != nil {
		return !w.contextStopped(err)
	}
	if !ok {
		return true
	}
	jobCtx, cancel := context.WithTimeout(w.ctx, w.opts.JobTimeout)
	result, fetchErr := w.executor.Fetch(jobCtx, job)
	cancel()
	if fetchErr == nil {
		err = w.store.CompleteFetch(w.ctx, FetchCompletion{JobID: job.ID, LeaseToken: job.LeaseToken, WorkerID: w.workerID,
			CompletedAt: w.clock.Now(), Result: result})
		if err != nil {
			return !w.contextStopped(err)
		}
		return true
	}
	failure := Classify(fetchErr)
	if failure.Code == CodeCanceled && w.closed() {
		return false
	}
	failedAt := w.clock.Now()
	if failure.Retryable {
		err = w.store.RetryFetch(w.ctx, Retry{JobID: job.ID, LeaseToken: job.LeaseToken, WorkerID: w.workerID, Attempt: job.Attempt,
			FailedAt: failedAt, NextAttemptAt: failedAt.Add(w.retryDelay(job.Attempt)), Failure: failure})
	} else {
		err = w.store.FailFetch(w.ctx, TerminalFailure{JobID: job.ID, LeaseToken: job.LeaseToken, WorkerID: w.workerID,
			Attempt: job.Attempt, FailedAt: failedAt, Failure: failure})
	}
	if err != nil {
		return !w.contextStopped(err)
	}
	return true
}

func (w *FetchWorker) retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	base := w.opts.RetryMin
	for step := 1; step < attempt && base < w.opts.RetryMax; step++ {
		base *= 2
		if base > w.opts.RetryMax {
			base = w.opts.RetryMax
		}
	}
	if base >= w.opts.RetryMax {
		return w.opts.RetryMax
	}
	bound := base / 2
	if remaining := w.opts.RetryMax - base; bound > remaining {
		bound = remaining
	}
	return base + w.jitter(bound)
}

func (w *FetchWorker) wait(duration time.Duration) bool {
	if duration <= 0 {
		return !w.closed()
	}
	timer := w.clock.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-w.ctx.Done():
		return false
	case <-w.drainCh:
		return false
	case <-timer.C():
		return !w.closed()
	}
}

func (w *FetchWorker) closed() bool {
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

func (w *FetchWorker) contextStopped(err error) bool {
	if errors.Is(err, context.Canceled) {
		return true
	}
	return w.closed()
}
