package lyricsdiscovery

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWorkerShutdownJoinsLoop(t *testing.T) {
	store := newFakeStore()
	worker := newTestWorker(t, store, discoverFunc(func(context.Context, Job) (Result, error) {
		return Result{}, errors.New("unexpected discovery")
	}), Options{})

	if err := worker.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, time.Second, func() bool { return store.claimCount() > 0 })
	worker.Drain()
	waitDone(t, worker)

	status := worker.Status()
	if status.State != StateStopped || status.StoppedAt.IsZero() {
		t.Fatalf("shutdown status = %+v", status)
	}
}

func TestNoClaimsAfterDrain(t *testing.T) {
	store := newFakeStore()
	scanEntered := make(chan struct{})
	releaseScan := make(chan struct{})
	store.scanFn = func(context.Context, ScanRequest) (ScanResult, error) {
		close(scanEntered)
		<-releaseScan
		return ScanResult{}, nil
	}
	worker := newTestWorker(t, store, discoverFunc(func(context.Context, Job) (Result, error) {
		return Result{}, errors.New("unexpected discovery")
	}), Options{})
	if err := worker.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	<-scanEntered
	drained := make(chan struct{})
	go func() {
		worker.Drain()
		close(drained)
	}()
	select {
	case <-drained:
		t.Fatal("Drain returned while Scan admission was in progress")
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseScan)
	<-drained
	waitDone(t, worker)
	if got := store.claimCount(); got != 0 {
		t.Fatalf("claims after drain = %d, want 0", got)
	}
}

func TestRetryAndTerminalResults(t *testing.T) {
	t.Run("retry", func(t *testing.T) {
		store := newFakeStore()
		store.jobs = []Job{{ID: "retry-job", LeaseToken: "retry-lease", Attempt: 3, MusicID: 11}}
		clock := newStepClock(time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC), time.Second)
		worker := newTestWorker(t, store, discoverFunc(func(context.Context, Job) (Result, error) {
			return Result{}, NewError(CodeRateLimited, errors.New("upstream included a secret query string"))
		}), Options{
			Clock: clock,
			Jitter: func(upperBound time.Duration) time.Duration {
				if upperBound != 2*time.Second {
					t.Fatalf("jitter upper bound = %s, want 2s", upperBound)
				}
				return time.Second
			},
			RetryMin: time.Second,
			RetryMax: 10 * time.Second,
		})
		if err := worker.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		waitFor(t, time.Second, func() bool { return store.retryCount() == 1 })
		worker.Drain()
		waitDone(t, worker)

		retry := store.retryAt(0)
		if retry.Failure.Code != CodeRateLimited || !retry.Failure.Retryable ||
			retry.NextAttemptAt.Sub(retry.FailedAt) != 5*time.Second {
			t.Fatalf("retry record = %+v", retry)
		}
		if strings.Contains(retry.Failure.Message, "secret") {
			t.Fatalf("retry persisted unsanitized error: %+v", retry.Failure)
		}
		status := worker.Status()
		if status.Retried != 1 || len(status.Outcomes) != 1 || status.Outcomes[0].Error != "lyrics source rate limited" {
			t.Fatalf("retry status = %+v", status)
		}
	})

	t.Run("terminal", func(t *testing.T) {
		store := newFakeStore()
		store.jobs = []Job{{ID: "terminal-job", LeaseToken: "terminal-lease", Attempt: 1, MusicID: 12}}
		worker := newTestWorker(t, store, discoverFunc(func(context.Context, Job) (Result, error) {
			return Result{}, NewError(CodeRestricted, errors.New("raw source policy details"))
		}), Options{})
		if err := worker.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		waitFor(t, time.Second, func() bool { return store.failureCount() == 1 })
		worker.Drain()
		waitDone(t, worker)

		failure := store.failureAt(0)
		if failure.Failure.Code != CodeRestricted || failure.Failure.Retryable || failure.Failure.Message != "source cannot be used" {
			t.Fatalf("terminal record = %+v", failure)
		}
		status := worker.Status()
		if status.Terminal != 1 || len(status.Outcomes) != 1 || status.Outcomes[0].ErrorCode != CodeRestricted {
			t.Fatalf("terminal status = %+v", status)
		}
	})
}

func TestDuplicateStart(t *testing.T) {
	store := newFakeStore()
	worker := newTestWorker(t, store, discoverFunc(func(context.Context, Job) (Result, error) {
		return Result{}, errors.New("unexpected discovery")
	}), Options{})
	if err := worker.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := worker.Start(t.Context()); !errors.Is(err, ErrAlreadyStarted) {
		t.Fatalf("second Start error = %v, want %v", err, ErrAlreadyStarted)
	}
	worker.Cancel()
	waitDone(t, worker)
}

func TestStartAfterPreStartDrainOrCancel(t *testing.T) {
	drained := newTestWorker(t, newFakeStore(), discoverFunc(func(context.Context, Job) (Result, error) { return Result{}, nil }), Options{})
	drained.Drain()
	if err := drained.Start(t.Context()); !errors.Is(err, ErrDraining) {
		t.Fatalf("Start after Drain error = %v, want %v", err, ErrDraining)
	}
	drained.Wait()

	canceled := newTestWorker(t, newFakeStore(), discoverFunc(func(context.Context, Job) (Result, error) { return Result{}, nil }), Options{})
	canceled.Cancel()
	if err := canceled.Start(t.Context()); !errors.Is(err, ErrCanceled) {
		t.Fatalf("Start after Cancel error = %v, want %v", err, ErrCanceled)
	}
	canceled.Wait()
}

func TestCancellationDuringWork(t *testing.T) {
	store := newFakeStore()
	store.jobs = []Job{{ID: "blocking-job", LeaseToken: "blocking-lease", Attempt: 1, MusicID: 13}}
	started := make(chan struct{})
	canceled := make(chan struct{})
	worker := newTestWorker(t, store, discoverFunc(func(ctx context.Context, _ Job) (Result, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		return Result{}, ctx.Err()
	}), Options{})
	if err := worker.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	<-started
	worker.Cancel()
	waitDone(t, worker)
	select {
	case <-canceled:
	default:
		t.Fatal("discovery context was not canceled")
	}
	if store.completionCount()+store.retryCount()+store.failureCount() != 0 {
		t.Fatalf("canceled work wrote a queue result: complete=%d retry=%d fail=%d",
			store.completionCount(), store.retryCount(), store.failureCount())
	}
	if status := worker.Status(); status.State != StateCanceled {
		t.Fatalf("canceled worker status = %+v", status)
	}
}

func TestDrainAllowsActiveWorkToPersistOutcome(t *testing.T) {
	store := newFakeStore()
	store.jobs = []Job{{ID: "draining-job", LeaseToken: "draining-lease", Attempt: 1, MusicID: 15}}
	started := make(chan struct{})
	release := make(chan struct{})
	worker := newTestWorker(t, store, discoverFunc(func(context.Context, Job) (Result, error) {
		close(started)
		<-release
		return Result{Outcome: OutcomeNoCandidates}, nil
	}), Options{})
	if err := worker.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	<-started
	worker.Drain()
	close(release)
	waitDone(t, worker)
	if store.completionCount() != 1 || store.claimCount() != 1 {
		t.Fatalf("drained active work: claims=%d completions=%d", store.claimCount(), store.completionCount())
	}
}

func TestWorkerContractCannotWriteAuthoritativeLyrics(t *testing.T) {
	store := newFakeStore()
	store.jobs = []Job{{ID: "shadow-job", LeaseToken: "shadow-lease", Attempt: 1, MusicID: 14}}
	artifact := []byte(`{"candidateCount":1}`)
	worker := newTestWorker(t, store, discoverFunc(func(context.Context, Job) (Result, error) {
		return Result{Outcome: OutcomeCandidatesFound, CandidateCount: 1, Artifact: artifact}, nil
	}), Options{})
	if err := worker.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, time.Second, func() bool { return store.completionCount() == 1 })
	worker.Drain()
	waitDone(t, worker)

	completion := store.completionAt(0)
	if completion.Result.Outcome != OutcomeCandidatesFound || string(completion.Result.Artifact) != string(artifact) {
		t.Fatalf("shadow completion = %+v", completion)
	}
	artifact[0] = 'X'
	if string(completion.Result.Artifact) == string(artifact) {
		t.Fatal("completion retained discovery-owned artifact memory")
	}
}

func TestRuntimeWorkerIdentityIsUnique(t *testing.T) {
	first := newTestWorker(t, newFakeStore(), discoverFunc(func(context.Context, Job) (Result, error) { return Result{}, nil }), Options{})
	second := newTestWorker(t, newFakeStore(), discoverFunc(func(context.Context, Job) (Result, error) { return Result{}, nil }), Options{})
	if first.Status().WorkerID == "" || first.Status().WorkerID == second.Status().WorkerID {
		t.Fatalf("worker identities are not runtime-unique: %q %q", first.Status().WorkerID, second.Status().WorkerID)
	}
}

func TestJitterInjectionIsSerialized(t *testing.T) {
	var active atomic.Int32
	var overlapped atomic.Bool
	worker := newTestWorker(t, newFakeStore(), discoverFunc(func(context.Context, Job) (Result, error) { return Result{}, nil }), Options{
		RetryMin: time.Second,
		RetryMax: 20 * time.Second,
		Jitter: func(time.Duration) time.Duration {
			if active.Add(1) != 1 {
				overlapped.Store(true)
			}
			time.Sleep(time.Millisecond)
			active.Add(-1)
			return 0
		},
	})
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = worker.retryDelay(1)
		}()
	}
	wg.Wait()
	if overlapped.Load() {
		t.Fatal("jitter function was invoked concurrently")
	}
}

func TestClassifyUnknownErrorIsSanitizedRetryableInternal(t *testing.T) {
	failure := Classify(errors.New("database password=do-not-persist"))
	if failure.Code != CodeInternal || !failure.Retryable || failure.Message != "lyrics discovery failed" || strings.Contains(failure.Message, "password") {
		t.Fatalf("classified failure = %+v", failure)
	}
}

type discoverFunc func(context.Context, Job) (Result, error)

func (fn discoverFunc) Discover(ctx context.Context, job Job) (Result, error) {
	return fn(ctx, job)
}

type fakeStore struct {
	mu          sync.Mutex
	jobs        []Job
	scans       []ScanRequest
	claims      []ClaimRequest
	completions []Completion
	retries     []Retry
	failures    []TerminalFailure
	scanFn      func(context.Context, ScanRequest) (ScanResult, error)
}

func newFakeStore() *fakeStore {
	return &fakeStore{}
}

func (s *fakeStore) Scan(ctx context.Context, request ScanRequest) (ScanResult, error) {
	if s.scanFn != nil {
		return s.scanFn(ctx, request)
	}
	s.mu.Lock()
	s.scans = append(s.scans, request)
	s.mu.Unlock()
	return ScanResult{}, nil
}

func (s *fakeStore) Claim(_ context.Context, request ClaimRequest) (Job, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claims = append(s.claims, request)
	if len(s.jobs) == 0 {
		return Job{}, false, nil
	}
	job := s.jobs[0]
	s.jobs = s.jobs[1:]
	return job, true, nil
}

func (s *fakeStore) Complete(_ context.Context, completion Completion) error {
	completion.Result = cloneResult(completion.Result)
	s.mu.Lock()
	s.completions = append(s.completions, completion)
	s.mu.Unlock()
	return nil
}

func (s *fakeStore) Retry(_ context.Context, retry Retry) error {
	s.mu.Lock()
	s.retries = append(s.retries, retry)
	s.mu.Unlock()
	return nil
}

func (s *fakeStore) Fail(_ context.Context, failure TerminalFailure) error {
	s.mu.Lock()
	s.failures = append(s.failures, failure)
	s.mu.Unlock()
	return nil
}

func (s *fakeStore) claimCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.claims)
}

func (s *fakeStore) completionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.completions)
}

func (s *fakeStore) retryCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.retries)
}

func (s *fakeStore) failureCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.failures)
}

func (s *fakeStore) completionAt(index int) Completion {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.completions[index]
}

func (s *fakeStore) retryAt(index int) Retry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.retries[index]
}

func (s *fakeStore) failureAt(index int) TerminalFailure {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failures[index]
}

func newTestWorker(t *testing.T, store Store, discovery Discovery, overrides Options) *Worker {
	t.Helper()
	opts := Options{
		ScanInterval:  10 * time.Millisecond,
		LeaseDuration: time.Minute,
		IdleWait:      time.Millisecond,
		RetryMin:      time.Second,
		RetryMax:      time.Minute,
		Jitter:        func(time.Duration) time.Duration { return 0 },
	}
	if overrides.ScanInterval != 0 {
		opts.ScanInterval = overrides.ScanInterval
	}
	if overrides.LeaseDuration != 0 {
		opts.LeaseDuration = overrides.LeaseDuration
	}
	if overrides.IdleWait != 0 {
		opts.IdleWait = overrides.IdleWait
	}
	if overrides.RetryMin != 0 {
		opts.RetryMin = overrides.RetryMin
	}
	if overrides.RetryMax != 0 {
		opts.RetryMax = overrides.RetryMax
	}
	if overrides.Clock != nil {
		opts.Clock = overrides.Clock
	}
	if overrides.Jitter != nil {
		opts.Jitter = overrides.Jitter
	}
	worker, err := New(store, discovery, opts)
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

func waitDone(t *testing.T, worker *Worker) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		worker.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker Wait did not return")
	}
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition was not met before timeout")
		}
		time.Sleep(time.Millisecond)
	}
}

type stepClock struct {
	mu   sync.Mutex
	now  time.Time
	step time.Duration
}

func newStepClock(start time.Time, step time.Duration) *stepClock {
	return &stepClock{now: start, step: step}
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	current := c.now
	c.now = c.now.Add(c.step)
	return current
}

func (c *stepClock) NewTimer(duration time.Duration) Timer {
	return realClock{}.NewTimer(duration)
}
