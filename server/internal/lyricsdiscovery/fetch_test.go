package lyricsdiscovery

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"moesekai/server/internal/lyricssource"
)

type fixedRevisionSourceFunc func(context.Context, lyricssource.MusicIdentity, lyricssource.Candidate) (lyricssource.FixedRevision, error)

func (f fixedRevisionSourceFunc) FetchFixedCandidateRevision(ctx context.Context, identity lyricssource.MusicIdentity, candidate lyricssource.Candidate) (lyricssource.FixedRevision, error) {
	return f(ctx, identity, candidate)
}

type fakeFetchStore struct {
	mu          sync.Mutex
	jobs        []FetchJob
	claimFn     func(context.Context, ClaimRequest) (FetchJob, bool, error)
	claims      int
	completions []FetchCompletion
	retries     []Retry
	failures    []TerminalFailure
}

func (s *fakeFetchStore) ClaimFetch(ctx context.Context, request ClaimRequest) (FetchJob, bool, error) {
	s.mu.Lock()
	s.claims++
	claimFn := s.claimFn
	if claimFn == nil {
		defer s.mu.Unlock()
		if len(s.jobs) == 0 {
			return FetchJob{}, false, nil
		}
		job := s.jobs[0]
		s.jobs = s.jobs[1:]
		return job, true, nil
	}
	s.mu.Unlock()
	return claimFn(ctx, request)
}

func (s *fakeFetchStore) CompleteFetch(_ context.Context, completion FetchCompletion) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completions = append(s.completions, completion)
	return nil
}

func (s *fakeFetchStore) RetryFetch(_ context.Context, retry Retry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retries = append(s.retries, retry)
	return nil
}

func (s *fakeFetchStore) FailFetch(_ context.Context, failure TerminalFailure) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures = append(s.failures, failure)
	return nil
}

func (s *fakeFetchStore) counts() (claims, completed, retried, failed int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.claims, len(s.completions), len(s.retries), len(s.failures)
}

func testFetchOptions() Options {
	return Options{ScanInterval: time.Hour, LeaseDuration: time.Minute, JobTimeout: 30 * time.Second,
		IdleWait: time.Millisecond, RetryMin: time.Second, RetryMax: time.Minute, Concurrency: 1,
		Jitter: func(time.Duration) time.Duration { return 0 }}
}

func validFetchJob() FetchJob {
	return FetchJob{ID: "1", LeaseToken: "v2", Attempt: 1, MusicID: 10, JapaneseTitle: "合成試験曲",
		ProducerMetadata: "作詞者 | 作曲者 | 編曲者", Lyricist: "作詞者", Composer: "作曲者", Arranger: "編曲者",
		PerformerSegmentationPolicy: lyricssource.PerformerSegmentationSekaiEligible,
		FixedCandidate: lyricssource.Candidate{PageID: 12, RevisionID: 34, SHA1: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Title: "合成試験曲", CanonicalURL: "https://vocaloid.fandom.com/wiki/%E5%90%88%E6%88%90%E8%A9%A6%E9%A8%93%E6%9B%B2?oldid=34",
			Categories: []string{"Lyrics", "Songs"}}}
}

func TestFetchExecutorPassesExactRoleBoundIdentity(t *testing.T) {
	executor, err := NewFetchExecutor(fixedRevisionSourceFunc(func(_ context.Context, identity lyricssource.MusicIdentity,
		candidate lyricssource.Candidate) (lyricssource.FixedRevision, error) {
		expected := validFetchJob().FixedCandidate
		if identity.MusicID != 10 || identity.JapaneseTitle != "合成試験曲" || identity.Lyricist != "作詞者" ||
			identity.Composer != "作曲者" || identity.Arranger != "編曲者" ||
			identity.PerformerSegmentationPolicy != lyricssource.PerformerSegmentationSekaiEligible || candidate.PageID != expected.PageID ||
			candidate.RevisionID != expected.RevisionID || candidate.SHA1 != expected.SHA1 || candidate.Title != expected.Title ||
			candidate.CanonicalURL != expected.CanonicalURL || fmt.Sprint(candidate.Categories) != fmt.Sprint(expected.Categories) {
			t.Fatalf("identity=%+v candidate=%+v", identity, candidate)
		}
		return lyricssource.FixedRevision{PageID: candidate.PageID, RevisionID: candidate.RevisionID, SHA1: candidate.SHA1,
			PageTitle: candidate.Title, CanonicalURL: candidate.CanonicalURL, Categories: append([]string(nil), candidate.Categories...),
			Lines: []lyricssource.ExtractedLine{{Japanese: "歌詞"}}}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Fetch(context.Background(), validFetchJob())
	if err != nil || len(result.Evidence) != 4 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestFetchWorkerRunsBoundedConcurrentFetches(t *testing.T) {
	store := &fakeFetchStore{}
	for musicID := 1; musicID <= 6; musicID++ {
		job := validFetchJob()
		job.ID = fmt.Sprintf("job-%d", musicID)
		job.LeaseToken = fmt.Sprintf("lease-%d", musicID)
		job.MusicID = musicID
		store.jobs = append(store.jobs, job)
	}
	var active atomic.Int32
	var peak atomic.Int32
	release := make(chan struct{})
	executor, _ := NewFetchExecutor(fixedRevisionSourceFunc(func(_ context.Context, identity lyricssource.MusicIdentity, candidate lyricssource.Candidate) (lyricssource.FixedRevision, error) {
		current := active.Add(1)
		for {
			observed := peak.Load()
			if current <= observed || peak.CompareAndSwap(observed, current) {
				break
			}
		}
		<-release
		active.Add(-1)
		return lyricssource.FixedRevision{PageID: candidate.PageID, RevisionID: candidate.RevisionID, SHA1: candidate.SHA1,
			PageTitle: candidate.Title, CanonicalURL: candidate.CanonicalURL, Categories: append([]string(nil), candidate.Categories...),
			Lines: []lyricssource.ExtractedLine{{Japanese: fmt.Sprintf("歌詞-%d", identity.MusicID)}}}, nil
	}))
	opts := testFetchOptions()
	opts.Concurrency = 3
	worker, err := NewFetchWorker(store, executor, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, time.Second, func() bool { return active.Load() == 3 })
	if got := peak.Load(); got != 3 {
		t.Fatalf("peak fetch concurrency = %d, want 3", got)
	}
	close(release)
	waitFor(t, time.Second, func() bool {
		_, completed, _, _ := store.counts()
		return completed == 6
	})
	worker.Drain()
	worker.Wait()
	if got := peak.Load(); got != 3 {
		t.Fatalf("peak fetch concurrency after completion = %d, want 3", got)
	}
}

func TestFetchWorkerDrainWaitsForAdmittedClaimAndStopsFutureClaims(t *testing.T) {
	store := &fakeFetchStore{}
	claimEntered := make(chan struct{})
	releaseClaim := make(chan struct{})
	store.claimFn = func(context.Context, ClaimRequest) (FetchJob, bool, error) {
		close(claimEntered)
		<-releaseClaim
		return FetchJob{}, false, nil
	}
	executor, _ := NewFetchExecutor(fixedRevisionSourceFunc(func(context.Context, lyricssource.MusicIdentity, lyricssource.Candidate) (lyricssource.FixedRevision, error) {
		return lyricssource.FixedRevision{}, errors.New("unexpected fetch")
	}))
	worker, err := NewFetchWorker(store, executor, testFetchOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	<-claimEntered
	drained := make(chan struct{})
	go func() {
		worker.Drain()
		close(drained)
	}()
	select {
	case <-drained:
		t.Fatal("Drain returned while ClaimFetch admission was in progress")
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseClaim)
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("Drain did not return")
	}
	worker.Wait()
	claims, completed, retried, failed := store.counts()
	if claims != 1 || completed+retried+failed != 0 || worker.State() != StateStopped {
		t.Fatalf("claims=%d complete=%d retry=%d failed=%d state=%s", claims, completed, retried, failed, worker.State())
	}
}

func TestFetchWorkerCancelInterruptsActiveFetchWithoutQueueMutation(t *testing.T) {
	store := &fakeFetchStore{jobs: []FetchJob{validFetchJob()}}
	started := make(chan struct{})
	canceled := make(chan struct{})
	executor, _ := NewFetchExecutor(fixedRevisionSourceFunc(func(ctx context.Context, _ lyricssource.MusicIdentity, _ lyricssource.Candidate) (lyricssource.FixedRevision, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		return lyricssource.FixedRevision{}, ctx.Err()
	}))
	worker, err := NewFetchWorker(store, executor, testFetchOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	<-started
	worker.Cancel()
	worker.Wait()
	select {
	case <-canceled:
	default:
		t.Fatal("active fetch was not canceled")
	}
	_, completed, retried, failed := store.counts()
	if completed+retried+failed != 0 || worker.State() != StateCanceled {
		t.Fatalf("complete=%d retry=%d failed=%d state=%s", completed, retried, failed, worker.State())
	}
}
