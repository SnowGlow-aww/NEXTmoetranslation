package store

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"moesekai/server/internal/db"
	"moesekai/server/internal/model"
)

func openLyricsDiscoveryJobStore(t *testing.T, path string) (*Store, *db.DB) {
	t.Helper()
	database, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return New(database), database
}

func discoveryTarget(musicID int) model.LyricsDiscoveryJobTarget {
	return model.LyricsDiscoveryJobTarget{
		MusicID: musicID, CatalogFingerprint: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", PolicyVersion: "shadow-v1",
	}
}

func enqueueLyricsDiscoveryJob(t *testing.T, s *Store, kind model.LyricsDiscoveryJobKind, target model.LyricsDiscoveryJobTarget, maxAttempts int) model.LyricsDiscoveryJob {
	t.Helper()
	job, created, err := s.EnqueueLyricsDiscoveryJob(context.Background(), EnqueueLyricsDiscoveryJobParams{
		Kind: kind, Target: target, MaxAttempts: maxAttempts,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("job was unexpectedly deduplicated")
	}
	return job
}

func claimLyricsDiscoveryJob(t *testing.T, s *Store, owner string, duration time.Duration) model.LyricsDiscoveryJob {
	t.Helper()
	job, err := s.ClaimLyricsDiscoveryJob(context.Background(), LyricsDiscoveryJobLease{Owner: owner, Duration: duration})
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func TestLyricsDiscoveryJobEnqueueDeduplicatesCanonicalTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "enqueue-dedupe.db")
	s, database := openLyricsDiscoveryJobStore(t, path)
	defer database.Close()
	target := model.LyricsDiscoveryJobTarget{
		MusicID: 41, PageID: 83, RevisionID: 127, ArtifactID: 169,
		CatalogFingerprint: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", PolicyVersion: "shadow-v1",
	}

	first, created, err := s.EnqueueLyricsDiscoveryJob(context.Background(), EnqueueLyricsDiscoveryJobParams{
		Kind: model.LyricsDiscoveryJobFetchRevision, Target: target, MaxAttempts: 5,
	})
	if err != nil || !created {
		t.Fatalf("first enqueue created=%t err=%v", created, err)
	}
	second, created, err := s.EnqueueLyricsDiscoveryJob(context.Background(), EnqueueLyricsDiscoveryJobParams{
		Kind: model.LyricsDiscoveryJobFetchRevision, Target: target, MaxAttempts: 99,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created || second.ID != first.ID || second.IdempotencyKey != first.IdempotencyKey || second.MaxAttempts != 5 {
		t.Fatalf("dedupe first=%+v second=%+v created=%t", first, second, created)
	}
	key, err := LyricsDiscoveryJobIdempotencyKey(model.LyricsDiscoveryJobFetchRevision, target)
	if err != nil || key != first.IdempotencyKey || len(key) != 64 {
		t.Fatalf("idempotency key=%q err=%v", key, err)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM lyrics_discovery_jobs`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("queue rows=%d err=%v", count, err)
	}
}

func TestLyricsDiscoveryJobClaimIsAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atomic-claim.db")
	s, database := openLyricsDiscoveryJobStore(t, path)
	defer database.Close()
	queued := enqueueLyricsDiscoveryJob(t, s, model.LyricsDiscoveryJobDiscover, discoveryTarget(7), 3)

	start := make(chan struct{})
	type result struct {
		job model.LyricsDiscoveryJob
		err error
	}
	results := make(chan result, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for _, owner := range []string{"worker_a", "worker_b"} {
		go func(owner string) {
			ready.Done()
			<-start
			job, err := s.ClaimLyricsDiscoveryJob(context.Background(), LyricsDiscoveryJobLease{Owner: owner, Duration: time.Minute})
			results <- result{job: job, err: err}
		}(owner)
	}
	ready.Wait()
	close(start)

	var claimed model.LyricsDiscoveryJob
	claimedCount, emptyCount := 0, 0
	for range 2 {
		result := <-results
		switch {
		case result.err == nil:
			claimed = result.job
			claimedCount++
		case errors.Is(result.err, ErrLyricsDiscoveryJobNotFound):
			emptyCount++
		default:
			t.Fatalf("claim error=%v", result.err)
		}
	}
	if claimedCount != 1 || emptyCount != 1 || claimed.ID != queued.ID || claimed.State != model.LyricsDiscoveryJobLeased || claimed.Attempts != 1 {
		t.Fatalf("claimed=%+v successes=%d empty=%d", claimed, claimedCount, emptyCount)
	}
}

func TestLyricsDiscoveryJobLeaseOwnershipAndCompletion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lease-ownership.db")
	s, database := openLyricsDiscoveryJobStore(t, path)
	defer database.Close()
	enqueueLyricsDiscoveryJob(t, s, model.LyricsDiscoveryJobDiscover, discoveryTarget(11), 3)
	leased := claimLyricsDiscoveryJob(t, s, "worker_a", time.Minute)

	if _, err := s.CompleteLyricsDiscoveryJob(context.Background(), leased.ID, "worker_b", leased.Version); !errors.Is(err, ErrLyricsDiscoveryLeaseNotOwned) {
		t.Fatalf("wrong owner completion error=%v", err)
	}
	current, err := s.GetLyricsDiscoveryJob(context.Background(), leased.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != model.LyricsDiscoveryJobLeased || current.LeaseOwner != "worker_a" || current.Version != leased.Version {
		t.Fatalf("wrong owner changed job=%+v", current)
	}

	completed, err := s.CompleteLyricsDiscoveryJob(context.Background(), leased.ID, "worker_a", leased.Version)
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != model.LyricsDiscoveryJobSucceeded || completed.LeaseOwner != "" || completed.LeaseExpiresAt.IsZero() == false || completed.CompletedAt.IsZero() || completed.Version != leased.Version+1 {
		t.Fatalf("completed job=%+v", completed)
	}
	if _, err := s.CompleteLyricsDiscoveryJob(context.Background(), leased.ID, "worker_a", leased.Version); !errors.Is(err, ErrLyricsDiscoveryJobTerminal) {
		t.Fatalf("repeat completion error=%v", err)
	}
}

func TestLyricsDiscoveryJobRetrySchedulingAndTerminalState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "retry-terminal.db")
	s, database := openLyricsDiscoveryJobStore(t, path)
	defer database.Close()
	enqueueLyricsDiscoveryJob(t, s, model.LyricsDiscoveryJobFetchRevision,
		model.LyricsDiscoveryJobTarget{MusicID: 13, PageID: 17, RevisionID: 19}, 2)
	leased := claimLyricsDiscoveryJob(t, s, "worker", time.Minute)
	failedAt := time.Now().UTC()
	notBefore := failedAt.Add(300 * time.Millisecond)
	retry, err := s.RetryLyricsDiscoveryJob(context.Background(), leased.ID, "worker", leased.Version, leased.Attempts, failedAt, notBefore, "HTTP 503: retry later")
	if err != nil {
		t.Fatal(err)
	}
	if retry.State != model.LyricsDiscoveryJobRetryWait || retry.LastErrorCode != "http_503_retry_later" || retry.Attempts != 1 || retry.NextAttemptAt.Before(notBefore.Add(-time.Millisecond)) {
		t.Fatalf("retry job=%+v", retry)
	}
	if _, err := s.ClaimLyricsDiscoveryJob(context.Background(), LyricsDiscoveryJobLease{Owner: "early", Duration: time.Minute}); !errors.Is(err, ErrLyricsDiscoveryJobNotFound) {
		t.Fatalf("early claim error=%v", err)
	}
	time.Sleep(time.Until(notBefore) + 25*time.Millisecond)
	second := claimLyricsDiscoveryJob(t, s, "worker_2", time.Minute)
	if second.ID != leased.ID || second.Attempts != 2 || second.Version != retry.Version+1 {
		t.Fatalf("second lease=%+v retry=%+v", second, retry)
	}
	dead, err := s.FailLyricsDiscoveryJob(context.Background(), second.ID, "worker_2", second.Version, "restricted reprint")
	if err != nil {
		t.Fatal(err)
	}
	if dead.State != model.LyricsDiscoveryJobDeadLetter || dead.LastErrorCode != "restricted_reprint" || dead.CompletedAt.IsZero() {
		t.Fatalf("dead-letter job=%+v", dead)
	}
	if _, err := s.ClaimLyricsDiscoveryJob(context.Background(), LyricsDiscoveryJobLease{Owner: "later", Duration: time.Minute}); !errors.Is(err, ErrLyricsDiscoveryJobNotFound) {
		t.Fatalf("terminal job was claimable: %v", err)
	}
}

func TestLyricsDiscoveryJobFailureRequeuesUntilAttemptsExhausted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "failure-requeue.db")
	s, database := openLyricsDiscoveryJobStore(t, path)
	defer database.Close()
	enqueueLyricsDiscoveryJob(t, s, model.LyricsDiscoveryJobDiscover, discoveryTarget(21), 2)
	first := claimLyricsDiscoveryJob(t, s, "worker_1", time.Minute)
	retry, err := s.FailLyricsDiscoveryJob(context.Background(), first.ID, "worker_1", first.Version, "temporary upstream error")
	if err != nil {
		t.Fatal(err)
	}
	if retry.State != model.LyricsDiscoveryJobRetryWait || retry.LastErrorCode != "temporary_upstream_error" || !retry.CompletedAt.IsZero() {
		t.Fatalf("retryable failure=%+v", retry)
	}
	second := claimLyricsDiscoveryJob(t, s, "worker_2", time.Minute)
	dead, err := s.FailLyricsDiscoveryJob(context.Background(), second.ID, "worker_2", second.Version, "still unavailable")
	if err != nil {
		t.Fatal(err)
	}
	if dead.State != model.LyricsDiscoveryJobDeadLetter || dead.LastErrorCode != "still_unavailable" || dead.CompletedAt.IsZero() || dead.Attempts != 2 {
		t.Fatalf("exhausted failure=%+v", dead)
	}
}

func TestLyricsDiscoveryJobExpiredLeaseRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "expired-lease.db")
	s, database := openLyricsDiscoveryJobStore(t, path)
	defer database.Close()
	enqueueLyricsDiscoveryJob(t, s, model.LyricsDiscoveryJobRevalidateHead,
		model.LyricsDiscoveryJobTarget{MusicID: 23, PageID: 29}, 2)
	first := claimLyricsDiscoveryJob(t, s, "crashed_worker", 25*time.Millisecond)
	time.Sleep(40 * time.Millisecond)
	second := claimLyricsDiscoveryJob(t, s, "recovery_worker", time.Minute)
	if second.ID != first.ID || second.Attempts != 2 || second.LeaseOwner != "recovery_worker" || second.LastErrorCode != "lease_expired" || second.Version != first.Version+2 {
		t.Fatalf("recovered job first=%+v second=%+v", first, second)
	}

	path2 := filepath.Join(t.TempDir(), "expired-final-attempt.db")
	s2, database2 := openLyricsDiscoveryJobStore(t, path2)
	defer database2.Close()
	enqueueLyricsDiscoveryJob(t, s2, model.LyricsDiscoveryJobDiscover, discoveryTarget(31), 1)
	lastLease := claimLyricsDiscoveryJob(t, s2, "crashed_final_worker", 25*time.Millisecond)
	time.Sleep(40 * time.Millisecond)
	if _, err := s2.ClaimLyricsDiscoveryJob(context.Background(), LyricsDiscoveryJobLease{Owner: "cannot_recover", Duration: time.Minute}); !errors.Is(err, ErrLyricsDiscoveryJobNotFound) {
		t.Fatalf("exhausted job claim error=%v", err)
	}
	dead, err := s2.GetLyricsDiscoveryJob(context.Background(), lastLease.ID)
	if err != nil {
		t.Fatal(err)
	}
	if dead.State != model.LyricsDiscoveryJobDeadLetter || dead.LastErrorCode != "lease_expired" || dead.CompletedAt.IsZero() || dead.Attempts != 1 {
		t.Fatalf("expired final attempt=%+v", dead)
	}
}

func TestLyricsDiscoveryJobExpiredLeaseRejectsTerminalMutationsBeforeRecovery(t *testing.T) {
	for index, test := range []struct {
		name   string
		mutate func(*Store, model.LyricsDiscoveryJob) error
	}{
		{name: "complete", mutate: func(s *Store, job model.LyricsDiscoveryJob) error {
			_, err := s.CompleteLyricsDiscoveryJob(context.Background(), job.ID, job.LeaseOwner, job.Version)
			return err
		}},
		{name: "retry", mutate: func(s *Store, job model.LyricsDiscoveryJob) error {
			failedAt := time.Now().UTC()
			_, err := s.RetryLyricsDiscoveryJob(context.Background(), job.ID, job.LeaseOwner, job.Version, job.Attempts, failedAt, failedAt.Add(time.Minute), "temporary")
			return err
		}},
		{name: "fail", mutate: func(s *Store, job model.LyricsDiscoveryJob) error {
			_, err := s.FailLyricsDiscoveryJob(context.Background(), job.ID, job.LeaseOwner, job.Version, "temporary")
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "expired-terminal-mutation.db")
			s, database := openLyricsDiscoveryJobStore(t, path)
			defer database.Close()
			queued := enqueueLyricsDiscoveryJob(t, s, model.LyricsDiscoveryJobDiscover, discoveryTarget(100+index), 3)
			leased, err := s.ClaimLyricsDiscoveryJob(context.Background(), LyricsDiscoveryJobLease{
				Owner: test.name, Duration: time.Minute, Kind: model.LyricsDiscoveryJobDiscover,
			})
			if err != nil || leased.ID != queued.ID {
				t.Fatalf("claim job=%+v queued=%+v err=%v", leased, queued, err)
			}
			if _, err := database.Exec(`UPDATE lyrics_discovery_jobs SET lease_expires_at=? WHERE job_id=?`, time.Now().UTC().Add(-time.Second).UnixMilli(), leased.ID); err != nil {
				t.Fatal(err)
			}
			if err := test.mutate(s, leased); !errors.Is(err, ErrLyricsDiscoveryLeaseNotOwned) {
				t.Fatalf("expired %s error=%v", test.name, err)
			}
			stored, err := s.GetLyricsDiscoveryJob(context.Background(), leased.ID)
			if err != nil || stored.State != model.LyricsDiscoveryJobLeased || stored.Version != leased.Version {
				t.Fatalf("expired %s changed job=%+v err=%v", test.name, stored, err)
			}
		})
	}
}

func TestLyricsDiscoveryJobTerminalMutationRechecksExpiryAfterWriterWait(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lease-writer-wait.db")
	s, database := openLyricsDiscoveryJobStore(t, path)
	defer database.Close()
	enqueueLyricsDiscoveryJob(t, s, model.LyricsDiscoveryJobDiscover, discoveryTarget(59), 3)
	leased := claimLyricsDiscoveryJob(t, s, "worker", 250*time.Millisecond)

	blocker, err := database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blocker.Exec(`UPDATE lyrics_discovery_jobs SET updated_at=updated_at WHERE job_id=?`, leased.ID); err != nil {
		_ = blocker.Rollback()
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := s.CompleteLyricsDiscoveryJob(context.Background(), leased.ID, leased.LeaseOwner, leased.Version)
		result <- err
	}()
	time.Sleep(time.Until(leased.LeaseExpiresAt) + 75*time.Millisecond)
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, ErrLyricsDiscoveryLeaseNotOwned) {
		t.Fatalf("post-wait expired completion error=%v", err)
	}
	stored, err := s.GetLyricsDiscoveryJob(context.Background(), leased.ID)
	if err != nil || stored.State != model.LyricsDiscoveryJobLeased || stored.Version != leased.Version {
		t.Fatalf("post-wait expired completion changed job=%+v err=%v", stored, err)
	}
}

func TestLyricsDiscoveryJobClaimFiltersKindAndUsesSuppliedTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claim-kind-time.db")
	s, database := openLyricsDiscoveryJobStore(t, path)
	defer database.Close()
	base := time.Now().UTC().Add(time.Hour)
	other := enqueueLyricsDiscoveryJob(t, s, model.LyricsDiscoveryJobFetchRevision,
		model.LyricsDiscoveryJobTarget{MusicID: 61, PageID: 67, RevisionID: 71}, 3)
	discover := enqueueLyricsDiscoveryJob(t, s, model.LyricsDiscoveryJobDiscover, discoveryTarget(73), 3)
	if _, err := database.Exec(`UPDATE lyrics_discovery_jobs SET next_attempt_at=?`, base.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimLyricsDiscoveryJob(context.Background(), LyricsDiscoveryJobLease{
		Owner: "discover-worker", Duration: time.Minute, Kind: model.LyricsDiscoveryJobDiscover, Now: base.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ID != discover.ID || claimed.Kind != model.LyricsDiscoveryJobDiscover || !claimed.UpdatedAt.Equal(canonicalLyricsDiscoveryTime(base.Add(time.Second))) || !claimed.LeaseExpiresAt.Equal(canonicalLyricsDiscoveryTime(base.Add(time.Minute+time.Second))) {
		t.Fatalf("kind-filtered claim=%+v discover=%+v", claimed, discover)
	}
	untouched, err := s.GetLyricsDiscoveryJob(context.Background(), other.ID)
	if err != nil || untouched.State != model.LyricsDiscoveryJobQueued || untouched.Attempts != 0 || untouched.Version != other.Version {
		t.Fatalf("unsupported kind changed=%+v err=%v", untouched, err)
	}
}

func TestLyricsDiscoveryJobPersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restart.db")
	s, database := openLyricsDiscoveryJobStore(t, path)
	queued := enqueueLyricsDiscoveryJob(t, s, model.LyricsDiscoveryJobRevalidatePinned,
		model.LyricsDiscoveryJobTarget{MusicID: 37, PageID: 41, ArtifactID: 43}, 4)
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	reopenedStore, reopenedDB := openLyricsDiscoveryJobStore(t, path)
	defer reopenedDB.Close()
	loaded, err := reopenedStore.GetLyricsDiscoveryJob(context.Background(), queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.IdempotencyKey != queued.IdempotencyKey || loaded.Kind != queued.Kind || loaded.Target != queued.Target || loaded.State != model.LyricsDiscoveryJobQueued {
		t.Fatalf("restarted job queued=%+v loaded=%+v", queued, loaded)
	}
	claimed := claimLyricsDiscoveryJob(t, reopenedStore, "after_restart", time.Minute)
	if claimed.ID != queued.ID || claimed.Attempts != 1 {
		t.Fatalf("claim after restart=%+v", claimed)
	}
}

func TestLyricsDiscoveryJobValidationAndCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "validation.db")
	s, database := openLyricsDiscoveryJobStore(t, path)
	defer database.Close()
	for _, params := range []EnqueueLyricsDiscoveryJobParams{
		{Kind: "unknown", Target: model.LyricsDiscoveryJobTarget{MusicID: 1}},
		{Kind: model.LyricsDiscoveryJobDiscover, Target: model.LyricsDiscoveryJobTarget{MusicID: 0}},
		{Kind: model.LyricsDiscoveryJobDiscover, Target: model.LyricsDiscoveryJobTarget{MusicID: 1, PageID: 2, CatalogFingerprint: "fingerprint", PolicyVersion: "shadow-v1"}},
		{Kind: model.LyricsDiscoveryJobFetchRevision, Target: model.LyricsDiscoveryJobTarget{MusicID: 1, PageID: 2}},
		{Kind: model.LyricsDiscoveryJobRevalidatePinned, Target: model.LyricsDiscoveryJobTarget{MusicID: 1, PageID: 2}},
		{Kind: model.LyricsDiscoveryJobRevalidateHead, Target: model.LyricsDiscoveryJobTarget{MusicID: 1, PageID: 2, RevisionID: 3}},
		{Kind: model.LyricsDiscoveryJobDiscover, Target: discoveryTarget(1), MaxAttempts: 101},
	} {
		if _, _, err := s.EnqueueLyricsDiscoveryJob(context.Background(), params); err == nil {
			t.Fatalf("invalid enqueue succeeded: %+v", params)
		}
	}
	if _, _, err := s.EnqueueLyricsDiscoveryJob(nil, EnqueueLyricsDiscoveryJobParams{
		Kind: model.LyricsDiscoveryJobDiscover, Target: discoveryTarget(1),
	}); err == nil {
		t.Fatal("nil context enqueue succeeded")
	}
	cancelledContext, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := s.EnqueueLyricsDiscoveryJob(cancelledContext, EnqueueLyricsDiscoveryJobParams{
		Kind: model.LyricsDiscoveryJobDiscover, Target: discoveryTarget(2),
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled enqueue error=%v", err)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM lyrics_discovery_jobs`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("cancelled enqueue rows=%d err=%v", count, err)
	}
	queued := enqueueLyricsDiscoveryJob(t, s, model.LyricsDiscoveryJobDiscover, discoveryTarget(47), 2)
	cancelled, err := s.CancelLyricsDiscoveryJob(context.Background(), queued.ID, queued.Version)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.State != model.LyricsDiscoveryJobCancelled || cancelled.CompletedAt.IsZero() {
		t.Fatalf("cancelled job=%+v", cancelled)
	}
	if _, err := s.ClaimLyricsDiscoveryJob(context.Background(), LyricsDiscoveryJobLease{Owner: "worker", Duration: time.Minute}); !errors.Is(err, ErrLyricsDiscoveryJobNotFound) {
		t.Fatalf("cancelled job was claimable: %v", err)
	}
	if got := SanitizeLyricsDiscoveryErrorCode("  HTTP 503 / Upstream.Timeout  "); got != "http_503_upstream_timeout" {
		t.Fatalf("sanitized error code=%q", got)
	}
}
