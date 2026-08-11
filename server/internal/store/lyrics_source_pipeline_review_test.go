package store

import (
	"context"

	"errors"
	"fmt"

	"strings"
	"testing"
	"time"

	"moesekai/server/internal/lyricssource"
	"moesekai/server/internal/model"
)

func TestCatalogFingerprintChangeSupersedesPendingReview(t *testing.T) {
	s, _ := openLyricsSourcePipelineStore(t)
	identity := seedFullLyricsSourceCatalog(t, s, 10)
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	review, _, err := createLyricsSourceReviewTx(context.Background(), tx, createLyricsSourceReviewParams{Kind: LyricsSourceReviewKindCandidate,
		MusicID: 10, CatalogFingerprint: identity.CatalogFingerprint, ReasonCode: "ambiguous_candidates", CreatedAt: time.Now().UTC(),
		EvidenceJSON: []byte(`{"candidates":[]}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertMusicCatalog([]MusicCatalogRecord{{MusicID: 10, JapaneseTitle: "変更後の合成試験曲",
		ProducerMetadata: "制作者", Lyricist: "制作者", Composer: "制作者", Arranger: "制作者",
		LyricsVersion: "full", LyricsVersionKnown: true}}); err != nil {
		t.Fatal(err)
	}
	updated, err := loadLyricsSourceReviewItemContext(context.Background(), s.db, `WHERE review_id=?`, review.ReviewID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.State != LyricsSourceReviewStateSuperseded || updated.Version != review.Version+1 || updated.CompletedAt.IsZero() {
		t.Fatalf("superseded review=%+v", updated)
	}
}

func TestSharedRestorePreservesPrivateRowsAndSupersedesStaleReview(t *testing.T) {
	s, database := openLyricsSourcePipelineStore(t)
	identity := seedFullLyricsSourceCatalog(t, s, 10)
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	review, _, err := createLyricsSourceReviewTx(context.Background(), tx, createLyricsSourceReviewParams{Kind: LyricsSourceReviewKindCandidate,
		MusicID: 10, CatalogFingerprint: identity.CatalogFingerprint, ReasonCode: "ambiguous_candidates", CreatedAt: time.Now().UTC(),
		EvidenceJSON: []byte(`{"candidates":[]}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO lyrics_source_artifacts
		(source_type, source_origin, page_id, revision_id, page_title, canonical_revision_url, mediawiki_sha1,
		 categories_json, raw_wikitext, raw_byte_count, raw_wikitext_sha256, artifact_sha256,
		 first_fetched_at, first_creating_job_id, created_at, provider, provenance_status)
		VALUES ('mediawiki','https://vocaloid.fandom.com',12,34,'合成試験曲','https://vocaloid.fandom.com/wiki/%E5%90%88%E6%88%90%E8%A9%A6%E9%A8%93%E6%9B%B2?oldid=34',?,
		 '[]',CAST(? AS BLOB),?, ?, ?, ?, ?, ?,'vocaloid_fandom','rebuild_required')`, strings.Repeat("a", 40), "PRIVATE-RESTORE-SENTINEL",
		len("PRIVATE-RESTORE-SENTINEL"), strings.Repeat("b", 64), strings.Repeat("c", 64), time.Now().UnixMilli(), 1, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	backup, err := s.ExportLyricsContent()
	if err != nil {
		t.Fatal(err)
	}
	backup.Music[0].TitleJA = "恢复后的曲名"
	backup.Music[0].LyricsCatalogFingerprint = ""
	if err := s.ImportTranslationContent(nil, EventContentExport{}, backup); err != nil {
		t.Fatal(err)
	}
	var artifactCount int
	var provider, origin, provenanceStatus string
	var raw []byte
	if err := database.QueryRow(`SELECT COUNT(*),provider,source_origin,provenance_status,raw_wikitext
		FROM lyrics_source_artifacts`).Scan(&artifactCount, &provider, &origin, &provenanceStatus, &raw); err != nil ||
		artifactCount != 1 || provider != string(model.LyricsSourceProviderVocaloidFandom) ||
		origin != model.LyricsSourceOriginVocaloidFandom || provenanceStatus != "rebuild_required" ||
		string(raw) != "PRIVATE-RESTORE-SENTINEL" {
		t.Fatalf("preserved artifact count=%d provider=%q origin=%q status=%q raw=%q err=%v",
			artifactCount, provider, origin, provenanceStatus, raw, err)
	}
	updated, err := loadLyricsSourceReviewItemContext(context.Background(), s.db, `WHERE review_id=?`, review.ReviewID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.State != LyricsSourceReviewStateSuperseded || updated.Version != review.Version+1 {
		t.Fatalf("restore review=%+v", updated)
	}
}

func TestLyricsSourceReviewDecisionCASAndIdempotency(t *testing.T) {
	s, database := openLyricsSourcePipelineStore(t)
	identity := seedFullLyricsSourceCatalog(t, s, 10)
	// Create a candidate review directly through the same transactional helper used by discovery.
	firstCandidate := pipelineProviderCandidate()
	secondCandidate := testRevisionCandidate(
		model.LyricsSourceProviderVocaloidFandom,
		13,
		35,
		"合成試験曲",
		[]string{"Songs"},
		"Lyrics",
		"full-vocaloid",
		model.LyricsSourceVersionReasonUntaggedFullOnly,
		[]byte("candidate review second exact index evidence"),
	)
	artifact := mustTestCandidateArtifact(t, []lyricssource.Candidate{firstCandidate, secondCandidate})
	reviewCandidates, indexEvidence, err := decodeLyricsDiscoveryArtifact(artifact, 2)
	if err != nil {
		t.Fatal(err)
	}
	reviewEvidence, err := canonicalCandidateReviewEvidence(reviewCandidates)
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Now().UTC()
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := insertOrVerifyLyricsIndexEvidenceCollectionTx(context.Background(), tx, indexEvidence, createdAt); err != nil {
		t.Fatal(err)
	}
	review, _, err := createLyricsSourceReviewTx(context.Background(), tx, createLyricsSourceReviewParams{
		Provider: model.LyricsSourceProviderVocaloidFandom, Kind: LyricsSourceReviewKindCandidate,
		MusicID: 10, CatalogFingerprint: identity.CatalogFingerprint, ReasonCode: "ambiguous_candidates",
		CreatedAt: createdAt, EvidenceJSON: reviewEvidence,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := linkLyricsSourceReviewEvidenceTx(context.Background(), tx, review.ReviewID, indexEvidence); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	candidate := legacyLyricsDiscoveryCandidateIdentity(&firstCandidate)
	params := LyricsSourceCandidateSelectionParams{ReviewID: review.ReviewID, CandidateIdentity: candidate, ExpectedVersion: 1,
		Actor: "admin", IdempotencyKey: "idempotency-key-0001", Note: ""}
	selected, replayed, err := s.SelectLyricsSourceCandidate(context.Background(), params)
	if err != nil || replayed || selected.State != LyricsSourceReviewStateApproved || selected.Version != 2 {
		t.Fatalf("selected=%+v replayed=%t err=%v", selected, replayed, err)
	}
	var fixedCandidateJSON string
	if err := database.QueryRow(`SELECT fixed_candidate_json FROM lyrics_discovery_jobs WHERE kind='fetch_revision'`).Scan(&fixedCandidateJSON); err != nil {
		t.Fatal(err)
	}
	persisted, err := decodeLyricsDiscoveryFixedCandidate(fixedCandidateJSON)
	wantPersisted := stripLyricsCandidateIndexEvidence(firstCandidate)
	if err != nil || persisted == nil || fmt.Sprint(*persisted) != fmt.Sprint(wantPersisted) {
		t.Fatalf("selected fixed candidate=%+v want=%+v json=%q err=%v", persisted, wantPersisted, fixedCandidateJSON, err)
	}
	var evidenceParents, reviewLinks, jobLinks int
	if err := database.QueryRow(`SELECT
		(SELECT COUNT(*) FROM lyrics_source_index_evidence),
		(SELECT COUNT(*) FROM lyrics_source_review_index_evidence WHERE review_id=?),
		(SELECT COUNT(*) FROM lyrics_discovery_job_index_evidence)`, review.ReviewID).Scan(
		&evidenceParents, &reviewLinks, &jobLinks,
	); err != nil || evidenceParents != 2 || reviewLinks != 2 || jobLinks != 1 {
		t.Fatalf("selection evidence parents=%d reviewLinks=%d jobLinks=%d err=%v",
			evidenceParents, reviewLinks, jobLinks, err)
	}
	replayedItem, replayed, err := s.SelectLyricsSourceCandidate(context.Background(), params)
	if err != nil || !replayed || replayedItem.Version != 2 {
		t.Fatalf("replay=%+v replayed=%t err=%v", replayedItem, replayed, err)
	}
	params.Note = "different body"
	if _, _, err := s.SelectLyricsSourceCandidate(context.Background(), params); !errors.Is(err, ErrLyricsSourceInvalidRequest) {
		t.Fatalf("nonempty note error=%v", err)
	}
	params.Actor, params.IdempotencyKey, params.Note = "other-admin", "idempotency-key-0002", ""
	if _, _, err := s.SelectLyricsSourceCandidate(context.Background(), params); !errors.Is(err, ErrLyricsSourceReviewConflict) {
		t.Fatalf("stale CAS err=%v", err)
	}
}

func TestOverallLyricsSourceReviewDecisionIsAtomicAndCompatibleWithPartialLegacyReview(t *testing.T) {
	s, database := openLyricsSourcePipelineStore(t)
	identity := seedFullLyricsSourceCatalog(t, s, 10)
	candidate := pipelineProviderCandidate()
	job := enqueuePipelineFetchJob(t, s, 10, identity.CatalogFingerprint, candidate)
	leased, err := s.ClaimLyricsDiscoveryJob(context.Background(), LyricsDiscoveryJobLease{Owner: "fetch-worker", Duration: time.Minute,
		Kind: model.LyricsDiscoveryJobFetchRevision, Now: time.Now().UTC()})
	if err != nil || leased.ID != job.ID {
		t.Fatalf("claim=%+v err=%v", leased, err)
	}
	fixed := pipelineFixedRevision(candidate, time.Now().UTC(), []byte("== Lyrics ==\n合成歌詞"),
		[]lyricssource.ExtractedLine{{Japanese: "合成歌詞"}})
	review, err := s.CompleteLyricsFetch(context.Background(), CompleteLyricsFetchParams{JobID: leased.ID, LeaseOwner: leased.LeaseOwner,
		ExpectedVersion: leased.Version, CompletedAt: time.Now().UTC(), Fixed: fixed,
		Evidence: []model.LyricsSourceEvidence{{RuleID: "fixed", Gate: "identity", Outcome: "passed", Summary: "exact revision"}}})
	if err != nil {
		t.Fatal(err)
	}

	legacy, _, err := s.DecideLyricsSourceReview(context.Background(), LyricsSourceReviewDecisionParams{
		ReviewID: review.ReviewID, Gate: LyricsSourceReviewGateIdentity, Decision: LyricsSourceReviewDecisionApproved,
		ExpectedVersion: 1, Actor: "legacy-admin", IdempotencyKey: "idempotency-legacy-gate-1",
	})
	if err != nil || legacy.Version != 2 || legacy.IdentityGate != "approved" || legacy.SourceUseGate != "pending" || legacy.ParseGate != "pending" {
		t.Fatalf("legacy partial decision=%+v err=%v", legacy, err)
	}
	overallParams := LyricsSourceReviewDecisionParams{ReviewID: review.ReviewID, Gate: LyricsSourceReviewGateOverall,
		Decision: LyricsSourceReviewDecisionApproved, ExpectedVersion: 2, Actor: "admin", IdempotencyKey: "idempotency-overall-0001"}
	approved, replayed, err := s.DecideLyricsSourceReview(context.Background(), overallParams)
	if err != nil || replayed || approved.Version != 3 || approved.State != LyricsSourceReviewStateApproved ||
		approved.IdentityGate != "approved" || approved.SourceUseGate != "approved" || approved.ParseGate != "approved" || approved.CompletedAt.IsZero() {
		t.Fatalf("overall decision=%+v replayed=%t err=%v", approved, replayed, err)
	}
	replayedItem, replayed, err := s.DecideLyricsSourceReview(context.Background(), overallParams)
	if err != nil || !replayed || replayedItem.Version != 3 || replayedItem.State != LyricsSourceReviewStateApproved ||
		replayedItem.IdentityGate != "approved" || replayedItem.SourceUseGate != "approved" || replayedItem.ParseGate != "approved" {
		t.Fatalf("overall replay=%+v replayed=%t err=%v", replayedItem, replayed, err)
	}
	var gate, decision string
	var expectedVersion, resultVersion int64
	if err := database.QueryRow(`SELECT gate, decision, expected_version, result_version FROM lyrics_source_review_decisions
		WHERE review_id=? ORDER BY decision_id DESC LIMIT 1`, review.ReviewID).Scan(&gate, &decision, &expectedVersion, &resultVersion); err != nil {
		t.Fatal(err)
	}
	if gate != LyricsSourceReviewGateOverall || decision != LyricsSourceReviewDecisionApproved || expectedVersion != 2 || resultVersion != 3 {
		t.Fatalf("stored overall decision=%q/%q v%d->v%d", gate, decision, expectedVersion, resultVersion)
	}
	var authoritative int
	if err := database.QueryRow(`SELECT COUNT(*) FROM song_lyrics`).Scan(&authoritative); err != nil || authoritative != 0 {
		t.Fatalf("overall review created authoritative lyrics count=%d err=%v", authoritative, err)
	}
}

func TestOverallLyricsSourceReviewRejectionSetsAllCompatibilityGates(t *testing.T) {
	s, database := openLyricsSourcePipelineStore(t)
	identity := seedFullLyricsSourceCatalog(t, s, 10)
	candidate := pipelineProviderCandidate()
	job := enqueuePipelineFetchJob(t, s, 10, identity.CatalogFingerprint, candidate)
	leased, err := s.ClaimLyricsDiscoveryJob(context.Background(), LyricsDiscoveryJobLease{Owner: "fetch-worker", Duration: time.Minute,
		Kind: model.LyricsDiscoveryJobFetchRevision, Now: time.Now().UTC()})
	if err != nil || leased.ID != job.ID {
		t.Fatalf("claim=%+v err=%v", leased, err)
	}
	fixed := pipelineFixedRevision(candidate, time.Now().UTC(), []byte("== Lyrics ==\n合成歌詞"),
		[]lyricssource.ExtractedLine{{Japanese: "合成歌詞"}})
	review, err := s.CompleteLyricsFetch(context.Background(), CompleteLyricsFetchParams{JobID: leased.ID, LeaseOwner: leased.LeaseOwner,
		ExpectedVersion: leased.Version, CompletedAt: time.Now().UTC(), Fixed: fixed,
		Evidence: []model.LyricsSourceEvidence{{RuleID: "fixed", Gate: "identity", Outcome: "passed", Summary: "exact revision"}}})
	if err != nil {
		t.Fatal(err)
	}
	rejected, replayed, err := s.DecideLyricsSourceReview(context.Background(), LyricsSourceReviewDecisionParams{
		ReviewID: review.ReviewID, Gate: LyricsSourceReviewGateOverall, Decision: LyricsSourceReviewDecisionRejected,
		ExpectedVersion: 1, Actor: "admin", IdempotencyKey: "idempotency-overall-0002", Note: "",
	})
	if err != nil || replayed || rejected.Version != 2 || rejected.State != LyricsSourceReviewStateRejected ||
		rejected.IdentityGate != "rejected" || rejected.SourceUseGate != "rejected" || rejected.ParseGate != "rejected" || rejected.CompletedAt.IsZero() {
		t.Fatalf("overall rejection=%+v replayed=%t err=%v", rejected, replayed, err)
	}
	var authoritative int
	if err := database.QueryRow(`SELECT COUNT(*) FROM song_lyrics`).Scan(&authoritative); err != nil || authoritative != 0 {
		t.Fatalf("overall rejection created authoritative lyrics count=%d err=%v", authoritative, err)
	}
}

func TestLyricsSourceReviewIdempotentReplayReturnsOriginalVersion(t *testing.T) {
	s, _ := openLyricsSourcePipelineStore(t)
	identity := seedFullLyricsSourceCatalog(t, s, 10)
	candidate := pipelineProviderCandidate()
	job := enqueuePipelineFetchJob(t, s, 10, identity.CatalogFingerprint, candidate)
	leased, err := s.ClaimLyricsDiscoveryJob(context.Background(), LyricsDiscoveryJobLease{Owner: "fetch-worker", Duration: time.Minute,
		Kind: model.LyricsDiscoveryJobFetchRevision, Now: time.Now().UTC()})
	if err != nil || leased.ID != job.ID {
		t.Fatalf("claim=%+v err=%v", leased, err)
	}
	fixed := pipelineFixedRevision(candidate, time.Now().UTC(), []byte("== Lyrics ==\n合成歌詞"),
		[]lyricssource.ExtractedLine{{Japanese: "合成歌詞"}})
	review, err := s.CompleteLyricsFetch(context.Background(), CompleteLyricsFetchParams{JobID: leased.ID, LeaseOwner: leased.LeaseOwner,
		ExpectedVersion: leased.Version, CompletedAt: time.Now().UTC(), Fixed: fixed,
		Evidence: []model.LyricsSourceEvidence{{RuleID: "fixed", Gate: "identity", Outcome: "passed", Summary: "exact revision"}}})
	if err != nil {
		t.Fatal(err)
	}
	first := LyricsSourceReviewDecisionParams{ReviewID: review.ReviewID, Gate: LyricsSourceReviewGateIdentity,
		Decision: LyricsSourceReviewDecisionApproved, ExpectedVersion: 1, Actor: "admin", IdempotencyKey: "idempotency-key-gate-1"}
	firstResult, replayed, err := s.DecideLyricsSourceReview(context.Background(), first)
	if err != nil || replayed || firstResult.Version != 2 || firstResult.IdentityGate != "approved" {
		t.Fatalf("first decision=%+v replayed=%t err=%v", firstResult, replayed, err)
	}
	second := LyricsSourceReviewDecisionParams{ReviewID: review.ReviewID, Gate: LyricsSourceReviewGateSourceUse,
		Decision: LyricsSourceReviewDecisionApproved, ExpectedVersion: 2, Actor: "admin", IdempotencyKey: "idempotency-key-gate-2"}
	if result, _, err := s.DecideLyricsSourceReview(context.Background(), second); err != nil || result.Version != 3 {
		t.Fatalf("second decision=%+v err=%v", result, err)
	}
	replayedResult, replayed, err := s.DecideLyricsSourceReview(context.Background(), first)
	if err != nil || !replayed || replayedResult.Version != 2 || replayedResult.IdentityGate != "approved" ||
		replayedResult.SourceUseGate != "pending" || replayedResult.ParseGate != "pending" {
		t.Fatalf("historical replay=%+v replayed=%t err=%v", replayedResult, replayed, err)
	}
}
