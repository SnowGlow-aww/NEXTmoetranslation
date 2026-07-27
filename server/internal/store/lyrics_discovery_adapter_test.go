package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"moesekai/server/internal/db"
	"moesekai/server/internal/lyricsdiscovery"
	"moesekai/server/internal/model"
)

func openLyricsDiscoveryAdapter(t *testing.T) (*LyricsDiscoveryAdapter, *Store, *db.DB) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "adapter.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	store := New(database)
	adapter, err := NewLyricsDiscoveryAdapter(store, LyricsDiscoveryShadowPolicyVersion, 3)
	if err != nil {
		t.Fatal(err)
	}
	return adapter, store, database
}

func seedLyricsDiscoveryCatalog(t *testing.T, store *Store, records ...MusicCatalogRecord) {
	t.Helper()
	if err := store.UpsertMusicCatalog(records); err != nil {
		t.Fatal(err)
	}
}

func scanAndClaimLyricsDiscovery(t *testing.T, adapter *LyricsDiscoveryAdapter, workerID string) lyricsdiscovery.Job {
	t.Helper()
	result, err := adapter.Scan(context.Background(), lyricsdiscovery.ScanRequest{WorkerID: workerID, Now: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if result.Scheduled == 0 {
		t.Fatal("scan scheduled no discovery jobs")
	}
	job, ok, err := adapter.Claim(context.Background(), lyricsdiscovery.ClaimRequest{
		WorkerID: workerID, Now: time.Now().UTC(), LeaseDuration: time.Minute,
	})
	if err != nil || !ok {
		t.Fatalf("claim ok=%t job=%+v err=%v", ok, job, err)
	}
	return job
}

func TestLyricsDiscoveryAdapterScansFullCatalogAndDeduplicatesGeneration(t *testing.T) {
	adapter, store, database := openLyricsDiscoveryAdapter(t)
	seedLyricsDiscoveryCatalog(t, store,
		MusicCatalogRecord{MusicID: 41, JapaneseTitle: "合成試験曲甲", ProducerMetadata: "制作者甲"},
		MusicCatalogRecord{MusicID: 7, JapaneseTitle: "合成試験曲乙", ProducerMetadata: "制作者乙"},
	)
	first, err := adapter.Scan(context.Background(), lyricsdiscovery.ScanRequest{WorkerID: "worker", Now: time.Now().UTC()})
	if err != nil || first.Scheduled != 2 {
		t.Fatalf("first scan=%+v err=%v", first, err)
	}
	second, err := adapter.Scan(context.Background(), lyricsdiscovery.ScanRequest{WorkerID: "worker", Now: time.Now().UTC()})
	if err != nil || second.Scheduled != 0 {
		t.Fatalf("deduplicated scan=%+v err=%v", second, err)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM lyrics_discovery_jobs`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("queue count=%d err=%v", count, err)
	}
	rows, err := database.Query(`SELECT music_id, catalog_fingerprint, policy_version FROM lyrics_discovery_jobs ORDER BY job_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[int]bool{}
	for rows.Next() {
		var musicID int
		var fingerprint, policy string
		if err := rows.Scan(&musicID, &fingerprint, &policy); err != nil {
			t.Fatal(err)
		}
		if len(fingerprint) != 64 || policy != LyricsDiscoveryShadowPolicyVersion {
			t.Fatalf("generation musicId=%d fingerprint=%q policy=%q", musicID, fingerprint, policy)
		}
		seen[musicID] = true
	}
	if err := rows.Err(); err != nil || !seen[7] || !seen[41] {
		t.Fatalf("scanned catalog=%v err=%v", seen, err)
	}
}

func TestLyricsDiscoveryAdapterClaimMapsCatalogAndFence(t *testing.T) {
	adapter, store, _ := openLyricsDiscoveryAdapter(t)
	seedLyricsDiscoveryCatalog(t, store, MusicCatalogRecord{
		MusicID: 10, JapaneseTitle: "合成試験曲", ProducerMetadata: "制作者",
	})
	job := scanAndClaimLyricsDiscovery(t, adapter, "worker-a")
	if job.ID == "" || job.LeaseToken == "" || job.Attempt != 1 || job.MusicID != 10 ||
		job.JapaneseTitle != "合成試験曲" || job.ProducerMetadata != "制作者" {
		t.Fatalf("mapped job=%+v", job)
	}
	storedID, version, err := parseLyricsDiscoveryLease(job.ID, job.LeaseToken, "worker-a")
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetLyricsDiscoveryJob(context.Background(), storedID)
	if err != nil || stored.Version != version || stored.LeaseOwner != "worker-a" || stored.Attempts != job.Attempt {
		t.Fatalf("stored job=%+v version=%d err=%v", stored, version, err)
	}
}

func TestLyricsDiscoveryAdapterCompleteIsAtomicAndFenced(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		adapter, store, database := openLyricsDiscoveryAdapter(t)
		seedLyricsDiscoveryCatalog(t, store, MusicCatalogRecord{MusicID: 10, JapaneseTitle: "合成試験曲", ProducerMetadata: "制作者"})
		job := scanAndClaimLyricsDiscovery(t, adapter, "worker-a")
		completion := lyricsdiscovery.Completion{
			JobID: job.ID, LeaseToken: job.LeaseToken, WorkerID: "worker-a", CompletedAt: time.Now().UTC(),
			Result: lyricsdiscovery.Result{
				Outcome: lyricsdiscovery.OutcomeCandidatesFound, CandidateCount: 1,
				Artifact: []byte(`{"candidates":[{"pageId":12,"revisionId":34}]}`),
			},
		}
		if err := adapter.Complete(context.Background(), completion); err != nil {
			t.Fatal(err)
		}
		var state model.LyricsDiscoveryJobState
		var outcome, resultJSON string
		var candidateCount int
		if err := database.QueryRow(`SELECT j.state, r.outcome, r.candidate_count, r.result_json
			FROM lyrics_discovery_jobs j JOIN lyrics_discovery_shadow_results r ON r.job_id=j.job_id`).
			Scan(&state, &outcome, &candidateCount, &resultJSON); err != nil {
			t.Fatal(err)
		}
		if state != model.LyricsDiscoveryJobSucceeded || outcome != string(lyricsdiscovery.OutcomeCandidatesFound) ||
			candidateCount != 1 || resultJSON != string(completion.Result.Artifact) {
			t.Fatalf("completion state=%q outcome=%q count=%d json=%s", state, outcome, candidateCount, resultJSON)
		}
		if err := adapter.Complete(context.Background(), completion); !errors.Is(err, ErrLyricsDiscoveryJobTerminal) {
			t.Fatalf("stale completion error=%v", err)
		}
	})

	t.Run("rollback", func(t *testing.T) {
		adapter, store, database := openLyricsDiscoveryAdapter(t)
		seedLyricsDiscoveryCatalog(t, store, MusicCatalogRecord{MusicID: 10, JapaneseTitle: "合成試験曲", ProducerMetadata: "制作者"})
		job := scanAndClaimLyricsDiscovery(t, adapter, "worker-a")
		if _, err := database.Exec(`CREATE TRIGGER fail_shadow_success BEFORE UPDATE OF state ON lyrics_discovery_jobs
			WHEN NEW.state='succeeded' BEGIN SELECT RAISE(ABORT, 'injected completion failure'); END`); err != nil {
			t.Fatal(err)
		}
		err := adapter.Complete(context.Background(), lyricsdiscovery.Completion{
			JobID: job.ID, LeaseToken: job.LeaseToken, WorkerID: "worker-a", CompletedAt: time.Now().UTC(),
			Result: lyricsdiscovery.Result{Outcome: lyricsdiscovery.OutcomeNoCandidates, Artifact: []byte(`{"candidates":[]}`)},
		})
		if err == nil || !strings.Contains(err.Error(), "injected completion failure") {
			t.Fatalf("completion rollback error=%v", err)
		}
		var results int
		var state model.LyricsDiscoveryJobState
		if err := database.QueryRow(`SELECT COUNT(*) FROM lyrics_discovery_shadow_results`).Scan(&results); err != nil {
			t.Fatal(err)
		}
		if err := database.QueryRow(`SELECT state FROM lyrics_discovery_jobs`).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if results != 0 || state != model.LyricsDiscoveryJobLeased {
			t.Fatalf("rolled back results=%d state=%q", results, state)
		}
	})
}

func TestLyricsDiscoveryAdapterRejectsInvalidShadowResultWithoutTransition(t *testing.T) {
	for name, result := range map[string]lyricsdiscovery.Result{
		"duplicate JSON key":  {Outcome: lyricsdiscovery.OutcomeNoCandidates, Artifact: []byte(`{"candidates":[],"candidates":[]}`)},
		"array artifact":      {Outcome: lyricsdiscovery.OutcomeNoCandidates, Artifact: []byte(`[]`)},
		"found count zero":    {Outcome: lyricsdiscovery.OutcomeCandidatesFound, Artifact: []byte(`{"candidates":[]}`)},
		"ambiguous count one": {Outcome: lyricsdiscovery.OutcomeAmbiguous, CandidateCount: 1, Artifact: []byte(`{"candidates":[{}]}`)},
	} {
		t.Run(name, func(t *testing.T) {
			adapter, store, database := openLyricsDiscoveryAdapter(t)
			seedLyricsDiscoveryCatalog(t, store, MusicCatalogRecord{MusicID: 10, JapaneseTitle: "合成試験曲", ProducerMetadata: "制作者"})
			job := scanAndClaimLyricsDiscovery(t, adapter, "worker-a")
			err := adapter.Complete(context.Background(), lyricsdiscovery.Completion{
				JobID: job.ID, LeaseToken: job.LeaseToken, WorkerID: "worker-a", CompletedAt: time.Now().UTC(), Result: result,
			})
			var coded *lyricsdiscovery.Error
			if !errors.As(err, &coded) || coded.Code != lyricsdiscovery.CodeInvalidResult {
				t.Fatalf("invalid result error=%v", err)
			}
			var results int
			var state model.LyricsDiscoveryJobState
			if err := database.QueryRow(`SELECT COUNT(*) FROM lyrics_discovery_shadow_results`).Scan(&results); err != nil {
				t.Fatal(err)
			}
			if err := database.QueryRow(`SELECT state FROM lyrics_discovery_jobs`).Scan(&state); err != nil {
				t.Fatal(err)
			}
			if results != 0 || state != model.LyricsDiscoveryJobLeased {
				t.Fatalf("invalid result changed results=%d state=%q", results, state)
			}
		})
	}
}

func TestLyricsDiscoveryAdapterRetryAndTerminalFailureSemantics(t *testing.T) {
	t.Run("retry", func(t *testing.T) {
		adapter, store, _ := openLyricsDiscoveryAdapter(t)
		seedLyricsDiscoveryCatalog(t, store, MusicCatalogRecord{MusicID: 10, JapaneseTitle: "合成試験曲", ProducerMetadata: "制作者"})
		job := scanAndClaimLyricsDiscovery(t, adapter, "worker-a")
		next := time.Now().UTC().Add(time.Minute)
		if err := adapter.Retry(context.Background(), lyricsdiscovery.Retry{
			JobID: job.ID, LeaseToken: job.LeaseToken, WorkerID: "worker-a", Attempt: job.Attempt,
			FailedAt: time.Now().UTC(), NextAttemptAt: next,
			Failure: lyricsdiscovery.ClassifiedError{Code: lyricsdiscovery.CodeRateLimited, Retryable: true},
		}); err != nil {
			t.Fatal(err)
		}
		jobID, _, _ := parseLyricsDiscoveryLease(job.ID, job.LeaseToken, "worker-a")
		stored, err := store.GetLyricsDiscoveryJob(context.Background(), jobID)
		if err != nil || stored.State != model.LyricsDiscoveryJobRetryWait || stored.LastErrorCode != string(lyricsdiscovery.CodeRateLimited) {
			t.Fatalf("retry job=%+v err=%v", stored, err)
		}
	})

	t.Run("terminal does not requeue", func(t *testing.T) {
		adapter, store, _ := openLyricsDiscoveryAdapter(t)
		seedLyricsDiscoveryCatalog(t, store, MusicCatalogRecord{MusicID: 10, JapaneseTitle: "合成試验曲", ProducerMetadata: "制作者"})
		job := scanAndClaimLyricsDiscovery(t, adapter, "worker-a")
		if err := adapter.Fail(context.Background(), lyricsdiscovery.TerminalFailure{
			JobID: job.ID, LeaseToken: job.LeaseToken, WorkerID: "worker-a", Attempt: job.Attempt, FailedAt: time.Now().UTC(),
			Failure: lyricsdiscovery.ClassifiedError{Code: lyricsdiscovery.CodeRestricted},
		}); err != nil {
			t.Fatal(err)
		}
		jobID, _, _ := parseLyricsDiscoveryLease(job.ID, job.LeaseToken, "worker-a")
		stored, err := store.GetLyricsDiscoveryJob(context.Background(), jobID)
		if err != nil || stored.State != model.LyricsDiscoveryJobDeadLetter || stored.LastErrorCode != string(lyricsdiscovery.CodeRestricted) ||
			stored.Attempts != stored.MaxAttempts || stored.CompletedAt.IsZero() {
			t.Fatalf("terminal job=%+v err=%v", stored, err)
		}
		if _, ok, err := adapter.Claim(context.Background(), lyricsdiscovery.ClaimRequest{WorkerID: "worker-b", LeaseDuration: time.Minute}); err != nil || ok {
			t.Fatalf("terminal job claim ok=%t err=%v", ok, err)
		}
	})
}

func TestLyricsDiscoveryAdapterStaleLeaseRejectsEveryTerminalMutation(t *testing.T) {
	for name, mutate := range map[string]func(*LyricsDiscoveryAdapter, lyricsdiscovery.Job) error{
		"complete": func(adapter *LyricsDiscoveryAdapter, job lyricsdiscovery.Job) error {
			return adapter.Complete(context.Background(), lyricsdiscovery.Completion{
				JobID: job.ID, LeaseToken: job.LeaseToken, WorkerID: "wrong-worker", CompletedAt: time.Now().UTC(),
				Result: lyricsdiscovery.Result{Outcome: lyricsdiscovery.OutcomeNoCandidates, Artifact: []byte(`{"candidates":[]}`)},
			})
		},
		"retry": func(adapter *LyricsDiscoveryAdapter, job lyricsdiscovery.Job) error {
			return adapter.Retry(context.Background(), lyricsdiscovery.Retry{
				JobID: job.ID, LeaseToken: job.LeaseToken, WorkerID: "wrong-worker", Attempt: job.Attempt,
				NextAttemptAt: time.Now().UTC().Add(time.Minute), Failure: lyricsdiscovery.ClassifiedError{Code: lyricsdiscovery.CodeTemporary},
			})
		},
		"fail": func(adapter *LyricsDiscoveryAdapter, job lyricsdiscovery.Job) error {
			return adapter.Fail(context.Background(), lyricsdiscovery.TerminalFailure{
				JobID: job.ID, LeaseToken: job.LeaseToken, WorkerID: "wrong-worker", Attempt: job.Attempt,
				FailedAt: time.Now().UTC(), Failure: lyricsdiscovery.ClassifiedError{Code: lyricsdiscovery.CodeRestricted},
			})
		},
	} {
		t.Run(name, func(t *testing.T) {
			adapter, store, database := openLyricsDiscoveryAdapter(t)
			seedLyricsDiscoveryCatalog(t, store, MusicCatalogRecord{MusicID: 10, JapaneseTitle: "合成試験曲", ProducerMetadata: "制作者"})
			job := scanAndClaimLyricsDiscovery(t, adapter, "worker-a")
			if err := mutate(adapter, job); !errors.Is(err, ErrLyricsDiscoveryLeaseNotOwned) {
				t.Fatalf("stale %s error=%v", name, err)
			}
			var state model.LyricsDiscoveryJobState
			var results int
			if err := database.QueryRow(`SELECT state FROM lyrics_discovery_jobs`).Scan(&state); err != nil {
				t.Fatal(err)
			}
			if err := database.QueryRow(`SELECT COUNT(*) FROM lyrics_discovery_shadow_results`).Scan(&results); err != nil {
				t.Fatal(err)
			}
			if state != model.LyricsDiscoveryJobLeased || results != 0 {
				t.Fatalf("stale %s changed state=%q results=%d", name, state, results)
			}
		})
	}
}

func TestLyricsDiscoveryAdapterFailsClosedOnCatalogDrift(t *testing.T) {
	adapter, store, database := openLyricsDiscoveryAdapter(t)
	seedLyricsDiscoveryCatalog(t, store, MusicCatalogRecord{MusicID: 10, JapaneseTitle: "合成試験曲", ProducerMetadata: "制作者"})
	if _, err := adapter.Scan(context.Background(), lyricsdiscovery.ScanRequest{WorkerID: "worker-a", Now: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	seedLyricsDiscoveryCatalog(t, store, MusicCatalogRecord{MusicID: 10, JapaneseTitle: "変更後の合成試験曲", ProducerMetadata: "制作者"})
	if _, ok, err := adapter.Claim(context.Background(), lyricsdiscovery.ClaimRequest{WorkerID: "worker-a", LeaseDuration: time.Minute}); ok {
		t.Fatal("catalog-drifted job was returned to discovery")
	} else {
		var coded *lyricsdiscovery.Error
		if !errors.As(err, &coded) || coded.Code != lyricsdiscovery.CodeSourceDrift {
			t.Fatalf("catalog drift error=%v", err)
		}
	}
	var state model.LyricsDiscoveryJobState
	var attempts, maxAttempts int
	if err := database.QueryRow(`SELECT state, attempts, max_attempts FROM lyrics_discovery_jobs`).Scan(&state, &attempts, &maxAttempts); err != nil {
		t.Fatal(err)
	}
	if state != model.LyricsDiscoveryJobDeadLetter || attempts != maxAttempts {
		t.Fatalf("drifted job state=%q attempts=%d max=%d", state, attempts, maxAttempts)
	}
}
