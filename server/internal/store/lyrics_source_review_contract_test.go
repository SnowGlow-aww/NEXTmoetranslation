package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"moesekai/server/internal/model"
)

func seedCandidateLyricsSourceReviews(t *testing.T, s *Store, count int) []int64 {
	t.Helper()
	identity := seedFullLyricsSourceCatalog(t, s, 10)
	ids := make([]int64, 0, count)
	for index := 0; index < count; index++ {
		tx, err := s.db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		item, created, err := createLyricsSourceReviewTx(context.Background(), tx, createLyricsSourceReviewParams{
			Kind: LyricsSourceReviewKindCandidate, MusicID: 10, CatalogFingerprint: identity.CatalogFingerprint,
			ReasonCode: fmt.Sprintf("candidate_%03d", index), EvidenceJSON: []byte(`{"candidates":[]}`),
			Priority: index % 3, CreatedAt: time.Now().UTC().Add(time.Duration(index) * time.Millisecond),
		})
		if err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if !created {
			tx.Rollback()
			t.Fatal("candidate review was not created")
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, item.ReviewID)
	}
	return ids
}

func TestLyricsSourceReviewListLimitPresenceAndBounds(t *testing.T) {
	s, _ := openLyricsSourcePipelineStore(t)
	seedCandidateLyricsSourceReviews(t, s, 2)

	page, err := s.ListLyricsSourceReviews(context.Background(), LyricsSourceReviewFilter{})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("default page=%+v err=%v", page, err)
	}
	for _, limit := range []int{0, -1, 101} {
		if _, err := s.ListLyricsSourceReviews(context.Background(), LyricsSourceReviewFilter{Limit: limit, LimitSet: true}); !errors.Is(err, ErrLyricsSourceInvalidRequest) {
			t.Fatalf("limit %d err=%v", limit, err)
		}
	}
	for _, limit := range []int{1, 100} {
		if _, err := s.ListLyricsSourceReviews(context.Background(), LyricsSourceReviewFilter{Limit: limit, LimitSet: true}); err != nil {
			t.Fatalf("limit %d err=%v", limit, err)
		}
		if _, err := s.ListLyricsSourceReviews(context.Background(), LyricsSourceReviewFilter{Limit: limit}); err != nil {
			t.Fatalf("internal limit %d err=%v", limit, err)
		}
	}
}

func TestLyricsSourceReviewPaginationHasNoDuplicatesOrTruncation(t *testing.T) {
	s, _ := openLyricsSourcePipelineStore(t)
	seedCandidateLyricsSourceReviews(t, s, 7)

	seen := make(map[int64]struct{})
	cursor := ""
	for pageNumber := 0; ; pageNumber++ {
		page, err := s.ListLyricsSourceReviews(context.Background(), LyricsSourceReviewFilter{
			Limit: 2, LimitSet: true, Cursor: cursor,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) == 0 {
			t.Fatalf("page %d was empty before cursor exhaustion", pageNumber)
		}
		for _, item := range page.Items {
			if _, duplicate := seen[item.ReviewID]; duplicate {
				t.Fatalf("duplicate review %d on page %d", item.ReviewID, pageNumber)
			}
			seen[item.ReviewID] = struct{}{}
		}
		if page.NextCursor == "" {
			break
		}
		if page.NextCursor == cursor {
			t.Fatalf("cursor did not advance: %q", cursor)
		}
		cursor = page.NextCursor
		if pageNumber > 10 {
			t.Fatal("pagination did not terminate")
		}
	}
	if len(seen) != 7 {
		t.Fatalf("paginated reviews=%d, want 7", len(seen))
	}
}

func seedArtifactLyricsSourceReviews(t *testing.T, s *Store, count int) []model.LyricsSourceReviewItem {
	t.Helper()
	identity := seedFullLyricsSourceCatalog(t, s, 10)
	items := make([]model.LyricsSourceReviewItem, 0, count)
	for index := 0; index < count; index++ {
		reason := fmt.Sprintf("artifact_%03d", index)
		domainKey := fmt.Sprintf("%064x", index+1)
		analysisKey := fmt.Sprintf("%064x", index+101)
		artifactSHA := fmt.Sprintf("%064x", index+201)
		now := time.Now().UTC().Add(-time.Duration(count-index) * time.Second).UnixMilli()
		artifact, err := s.db.Exec(`INSERT INTO lyrics_source_artifacts
			(source_type,source_origin,page_id,revision_id,page_title,canonical_revision_url,mediawiki_sha1,categories_json,
			 raw_wikitext,raw_byte_count,raw_wikitext_sha256,artifact_sha256,first_fetched_at,first_creating_job_id,created_at,
			 provider,provenance_status)
			VALUES ('mediawiki','https://vocaloid.fandom.com',?,?,?,? ,?,'[]',?,1,?,?,?, ?,?,'vocaloid_fandom','rebuild_required')`,
			index+1, index+101, "合成試験曲", fmt.Sprintf("https://vocaloid.fandom.com/wiki/Song?oldid=%d", index+101),
			strings.Repeat("a", 40), []byte("x"), strings.Repeat("b", 64), artifactSHA, now, index+1, now)
		if err != nil {
			t.Fatal(err)
		}
		artifactID, err := artifact.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		lines := []model.LyricsSourceExtractedLine{{
			Japanese: "x",
			Segments: []model.LyricsSourceSegment{{
				Text: "x", PerformerIDs: []string{}, Ruby: []model.LyricsSourceRubySpan{{Text: "x"}},
			}},
			TrailingPerformerIDs: []string{},
		}}
		linesJSON, err := json.Marshal(lines)
		if err != nil {
			t.Fatal(err)
		}
		analysis, err := s.db.Exec(`INSERT INTO lyrics_source_analyses
			(analysis_key,artifact_id,music_id,catalog_fingerprint,matching_policy_version,restriction_policy_version,
			 extractor_version,match_outcome,restriction_outcome,extraction_outcome,matching_evidence_json,
			 restriction_rule_ids_json,selected_version_json,performers_json,ruby_generator_version,
			 extracted_lines_json,extracted_line_count,extracted_lines_sha256,analysis_sha256,
			 creating_job_id,created_at,provider)
			VALUES (?,?,10,?,'matching-v2','restriction-v1','extractor-v1','matched','clear','extracted','[]','[]',
			 '{"kind":"vocaloid","label":"Vocaloid Version"}','[]','kagome-ipadic-v1',?,1,?,?,?,?,'vocaloid_fandom')`,
			analysisKey, artifactID, identity.CatalogFingerprint, string(linesJSON),
			model.LyricsSourceExtractedLinesSHA256(lines), strings.Repeat("d", 64), index+1, now)
		if err != nil {
			t.Fatal(err)
		}
		analysisID, err := analysis.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		result, err := s.db.Exec(`INSERT INTO lyrics_source_review_items
			(domain_key,kind,analysis_id,music_id,catalog_fingerprint,review_policy_version,reason_code,evidence_json,state,
			 identity_gate,source_use_gate,parse_gate,version,priority,created_at,updated_at,provider)
			VALUES (?,'artifact_review',?,10,?,?,?,'{}','pending','pending','pending','pending',1,0,?,?,'vocaloid_fandom')`,
			domainKey, analysisID, identity.CatalogFingerprint, model.LyricsReviewPolicyVersion, reason, now, now)
		if err != nil {
			t.Fatal(err)
		}
		reviewID, err := result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		item, err := loadLyricsSourceReviewItemContext(context.Background(), s.db, `WHERE review_id=?`, reviewID)
		if err != nil {
			t.Fatal(err)
		}
		items = append(items, item)
	}
	return items
}

func TestLyricsSourceReviewBatchDecisionIsAtomicReplaySafeAndSideEffectFree(t *testing.T) {
	s, database := openLyricsSourcePipelineStore(t)
	reviews := seedArtifactLyricsSourceReviews(t, s, 3)
	params := LyricsSourceReviewBatchDecisionParams{
		Gate: LyricsSourceReviewGateOverall, Decision: LyricsSourceReviewDecisionApproved,
		Items: []LyricsSourceReviewBatchItem{
			{ReviewID: reviews[2].ReviewID, ExpectedVersion: 1},
			{ReviewID: reviews[0].ReviewID, ExpectedVersion: 1},
		},
		Actor: "admin", IdempotencyKey: "batch-idempotency-0001", Note: "",
	}
	result, err := s.DecideLyricsSourceReviewBatch(context.Background(), params)
	if err != nil || result.Replayed || len(result.Items) != 2 {
		t.Fatalf("batch result=%+v err=%v", result, err)
	}
	for index, item := range result.Items {
		wantReviewID := []int64{reviews[0].ReviewID, reviews[2].ReviewID}[index]
		if item.ReviewID != wantReviewID || item.State != LyricsSourceReviewStateApproved || item.Version != 2 ||
			item.IdentityGate != "approved" || item.SourceUseGate != "approved" || item.ParseGate != "approved" {
			t.Fatalf("batch item %d=%+v", index, item)
		}
	}
	params.Items[0], params.Items[1] = params.Items[1], params.Items[0]
	replay, err := s.DecideLyricsSourceReviewBatch(context.Background(), params)
	if err != nil || !replay.Replayed || len(replay.Items) != 2 || replay.Items[0].Version != 2 || replay.Items[1].Version != 2 {
		t.Fatalf("batch replay=%+v err=%v", replay, err)
	}
	params.Note = "different"
	if _, err := s.DecideLyricsSourceReviewBatch(context.Background(), params); !errors.Is(err, ErrLyricsSourceInvalidRequest) {
		t.Fatalf("batch nonempty note error=%v", err)
	}
	var ledger, decisions, audits, lyrics, publications, outputs int
	for table, target := range map[string]*int{
		"lyrics_source_review_batch_idempotency": &ledger,
		"lyrics_source_review_decisions":         &decisions,
		"audit_log":                              &audits,
		"song_lyrics":                            &lyrics,
		"song_lyrics_publications":               &publications,
		"lyrics_discovery_job_outputs":           &outputs,
	} {
		if err := database.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if ledger != 1 || decisions != 2 || audits != 2 || lyrics != 0 || publications != 0 || outputs != 0 {
		t.Fatalf("counts ledger=%d decisions=%d audits=%d lyrics=%d publications=%d outputs=%d",
			ledger, decisions, audits, lyrics, publications, outputs)
	}
	rows, err := database.Query(`SELECT idempotency_key FROM lyrics_source_review_decisions ORDER BY review_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	keys := []string{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key)
		if len(key) != 64 {
			t.Fatalf("child key=%q", key)
		}
	}
	if len(keys) != 2 || keys[0] == keys[1] {
		t.Fatalf("child keys=%v", keys)
	}
}

func TestLyricsSourceReviewBatchConflictRollsBackEverything(t *testing.T) {
	s, database := openLyricsSourcePipelineStore(t)
	reviews := seedArtifactLyricsSourceReviews(t, s, 2)
	if _, _, err := s.DecideLyricsSourceReview(context.Background(), LyricsSourceReviewDecisionParams{
		ReviewID: reviews[1].ReviewID, Gate: LyricsSourceReviewGateOverall, Decision: LyricsSourceReviewDecisionRejected,
		ExpectedVersion: 1, Actor: "other", IdempotencyKey: "single-idempotency-0001", Note: "",
	}); err != nil {
		t.Fatal(err)
	}
	var beforeDecisions, beforeAudits int
	if err := database.QueryRow(`SELECT COUNT(*) FROM lyrics_source_review_decisions`).Scan(&beforeDecisions); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM audit_log`).Scan(&beforeAudits); err != nil {
		t.Fatal(err)
	}
	result, err := s.DecideLyricsSourceReviewBatch(context.Background(), LyricsSourceReviewBatchDecisionParams{
		Gate: LyricsSourceReviewGateOverall, Decision: LyricsSourceReviewDecisionApproved,
		Items: []LyricsSourceReviewBatchItem{{ReviewID: reviews[0].ReviewID, ExpectedVersion: 1}, {ReviewID: reviews[1].ReviewID, ExpectedVersion: 1}},
		Actor: "admin", IdempotencyKey: "batch-idempotency-0002", Note: "",
	})
	if !errors.Is(err, ErrLyricsSourceReviewConflict) || len(result.Conflicts) != 1 ||
		result.Conflicts[0].ReviewID != reviews[1].ReviewID || result.Conflicts[0].Reason != "not_pending" {
		t.Fatalf("batch conflict=%+v err=%v", result, err)
	}
	first, err := loadLyricsSourceReviewItemContext(context.Background(), database, `WHERE review_id=?`, reviews[0].ReviewID)
	if err != nil || first.State != LyricsSourceReviewStatePending || first.Version != 1 || first.IdentityGate != "pending" {
		t.Fatalf("first after rollback=%+v err=%v", first, err)
	}
	var ledger, afterDecisions, afterAudits int
	if err := database.QueryRow(`SELECT COUNT(*) FROM lyrics_source_review_batch_idempotency`).Scan(&ledger); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM lyrics_source_review_decisions`).Scan(&afterDecisions); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM audit_log`).Scan(&afterAudits); err != nil {
		t.Fatal(err)
	}
	if ledger != 0 || afterDecisions != beforeDecisions || afterAudits != beforeAudits {
		t.Fatalf("rollback counts ledger=%d decisions=%d/%d audits=%d/%d", ledger, afterDecisions, beforeDecisions, afterAudits, beforeAudits)
	}
}

func TestLyricsSourceReviewSingleReplayRequiresSameRequestKind(t *testing.T) {
	s, database := openLyricsSourcePipelineStore(t)
	review := seedArtifactLyricsSourceReviews(t, s, 1)[0]
	const key = "single-kind-collision-01"
	payload := struct {
		ReviewID        int64  `json:"reviewId"`
		Gate            string `json:"gate"`
		Decision        string `json:"decision"`
		ExpectedVersion int64  `json:"expectedVersion"`
		Note            string `json:"note"`
	}{review.ReviewID, LyricsSourceReviewGateOverall, LyricsSourceReviewDecisionApproved, 1, ""}
	requestSHA, err := validateLyricsReviewMutation(payload, review.ReviewID, 1, "admin", key, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO lyrics_source_review_decisions
		(review_id,gate,decision,selected_candidate_json,actor,note,idempotency_key,request_sha256,
		 expected_version,result_version,decided_at,provider)
		VALUES (?,'candidate','selected','{}','admin','',?, ?,1,2,1,'vocaloid_fandom')`, review.ReviewID, key, requestSHA); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.DecideLyricsSourceReview(context.Background(), LyricsSourceReviewDecisionParams{
		ReviewID: review.ReviewID, Gate: LyricsSourceReviewGateOverall, Decision: LyricsSourceReviewDecisionApproved,
		ExpectedVersion: 1, Actor: "admin", IdempotencyKey: key, Note: "",
	}); !errors.Is(err, ErrLyricsSourceIdempotency) {
		t.Fatalf("cross-kind replay err=%v", err)
	}
	item, err := loadLyricsSourceReviewItemContext(context.Background(), database, `WHERE review_id=?`, review.ReviewID)
	if err != nil || item.State != LyricsSourceReviewStatePending || item.Version != 1 {
		t.Fatalf("cross-kind replay changed item=%+v err=%v", item, err)
	}
}

func TestLyricsSourceReviewSharedRouteKeyConflictsAcrossSingleAndBatchLedgers(t *testing.T) {
	t.Run("single key blocks batch", func(t *testing.T) {
		s, database := openLyricsSourcePipelineStore(t)
		reviews := seedArtifactLyricsSourceReviews(t, s, 2)
		const sharedKey = "shared-route-key-0001"
		if _, _, err := s.DecideLyricsSourceReview(context.Background(), LyricsSourceReviewDecisionParams{
			ReviewID: reviews[0].ReviewID, Gate: LyricsSourceReviewGateOverall, Decision: LyricsSourceReviewDecisionApproved,
			ExpectedVersion: 1, Actor: "admin", IdempotencyKey: sharedKey, Note: "",
		}); err != nil {
			t.Fatal(err)
		}
		_, err := s.DecideLyricsSourceReviewBatch(context.Background(), LyricsSourceReviewBatchDecisionParams{
			Gate: LyricsSourceReviewGateOverall, Decision: LyricsSourceReviewDecisionRejected,
			Items: []LyricsSourceReviewBatchItem{{ReviewID: reviews[1].ReviewID, ExpectedVersion: 1}},
			Actor: "admin", IdempotencyKey: sharedKey, Note: "",
		})
		if !errors.Is(err, ErrLyricsSourceIdempotency) {
			t.Fatalf("batch after single err=%v", err)
		}
		item, loadErr := loadLyricsSourceReviewItemContext(context.Background(), database, `WHERE review_id=?`, reviews[1].ReviewID)
		if loadErr != nil || item.State != LyricsSourceReviewStatePending || item.Version != 1 {
			t.Fatalf("batch conflict changed review=%+v err=%v", item, loadErr)
		}
		var batchLedgers int
		if err := database.QueryRow(`SELECT COUNT(*) FROM lyrics_source_review_batch_idempotency`).Scan(&batchLedgers); err != nil || batchLedgers != 0 {
			t.Fatalf("batch ledgers=%d err=%v", batchLedgers, err)
		}
	})

	t.Run("batch key blocks single and candidate", func(t *testing.T) {
		s, database := openLyricsSourcePipelineStore(t)
		artifactReviews := seedArtifactLyricsSourceReviews(t, s, 2)
		const sharedKey = "shared-route-key-0002"
		if _, err := s.DecideLyricsSourceReviewBatch(context.Background(), LyricsSourceReviewBatchDecisionParams{
			Gate: LyricsSourceReviewGateOverall, Decision: LyricsSourceReviewDecisionApproved,
			Items: []LyricsSourceReviewBatchItem{{ReviewID: artifactReviews[0].ReviewID, ExpectedVersion: 1}},
			Actor: "admin", IdempotencyKey: sharedKey, Note: "",
		}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.DecideLyricsSourceReview(context.Background(), LyricsSourceReviewDecisionParams{
			ReviewID: artifactReviews[1].ReviewID, Gate: LyricsSourceReviewGateOverall, Decision: LyricsSourceReviewDecisionRejected,
			ExpectedVersion: 1, Actor: "admin", IdempotencyKey: sharedKey, Note: "",
		}); !errors.Is(err, ErrLyricsSourceIdempotency) {
			t.Fatalf("single after batch err=%v", err)
		}

		identity := seedFullLyricsSourceCatalog(t, s, 10)
		tx, err := s.db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		candidate, _, err := createLyricsSourceReviewTx(context.Background(), tx, createLyricsSourceReviewParams{
			Kind: LyricsSourceReviewKindCandidate, MusicID: 10, CatalogFingerprint: identity.CatalogFingerprint,
			ReasonCode: "shared_route_candidate", EvidenceJSON: []byte(`{"candidates":[]}`), CreatedAt: time.Now().UTC(),
		})
		if err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.SelectLyricsSourceCandidate(context.Background(), LyricsSourceCandidateSelectionParams{
			ReviewID: candidate.ReviewID, Exclude: true, ExpectedVersion: 1,
			Actor: "admin", IdempotencyKey: sharedKey, Note: "",
		}); !errors.Is(err, ErrLyricsSourceIdempotency) {
			t.Fatalf("candidate after batch err=%v", err)
		}
		for _, reviewID := range []int64{artifactReviews[1].ReviewID, candidate.ReviewID} {
			item, loadErr := loadLyricsSourceReviewItemContext(context.Background(), database, `WHERE review_id=?`, reviewID)
			if loadErr != nil || item.State != LyricsSourceReviewStatePending || item.Version != 1 {
				t.Fatalf("shared-key conflict changed review %d=%+v err=%v", reviewID, item, loadErr)
			}
		}
	})
}

func TestLyricsSourceReviewBatchDerivedChildCollisionIsIdempotencyConflictAndRollsBack(t *testing.T) {
	s, database := openLyricsSourcePipelineStore(t)
	reviews := seedArtifactLyricsSourceReviews(t, s, 2)
	params := LyricsSourceReviewBatchDecisionParams{
		Gate: LyricsSourceReviewGateOverall, Decision: LyricsSourceReviewDecisionApproved,
		Items: []LyricsSourceReviewBatchItem{{ReviewID: reviews[0].ReviewID, ExpectedVersion: 1}, {ReviewID: reviews[1].ReviewID, ExpectedVersion: 1}},
		Actor: "admin", IdempotencyKey: "batch-child-collision-01", Note: "",
	}
	items, _, requestSHA, actor, _, err := canonicalLyricsSourceReviewBatch(params)
	if err != nil {
		t.Fatal(err)
	}
	conflictingKey := lyricsSourceReviewBatchChildIdempotencyKey(params.IdempotencyKey, requestSHA, items[1])
	if _, err := database.Exec(`INSERT INTO lyrics_source_review_decisions
		(review_id,gate,decision,selected_candidate_json,actor,note,idempotency_key,request_sha256,
		 expected_version,result_version,decided_at,provider)
		VALUES (?,'overall','approved',NULL,?,'',?, ?,1,2,1,'vocaloid_fandom')`, reviews[1].ReviewID, actor,
		conflictingKey, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DecideLyricsSourceReviewBatch(context.Background(), params); !errors.Is(err, ErrLyricsSourceIdempotency) {
		t.Fatalf("derived child collision err=%v", err)
	}
	for _, review := range reviews {
		item, err := loadLyricsSourceReviewItemContext(context.Background(), database, `WHERE review_id=?`, review.ReviewID)
		if err != nil || item.State != LyricsSourceReviewStatePending || item.Version != 1 || item.IdentityGate != "pending" {
			t.Fatalf("review %d after collision rollback=%+v err=%v", review.ReviewID, item, err)
		}
	}
	var ledger, decisions, audits int
	if err := database.QueryRow(`SELECT COUNT(*) FROM lyrics_source_review_batch_idempotency`).Scan(&ledger); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM lyrics_source_review_decisions`).Scan(&decisions); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM audit_log`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if ledger != 0 || decisions != 1 || audits != 0 {
		t.Fatalf("child collision rollback counts ledger=%d decisions=%d audits=%d", ledger, decisions, audits)
	}
}

func TestLyricsSourceReviewBatchIdempotencyIsActorScoped(t *testing.T) {
	s, database := openLyricsSourcePipelineStore(t)
	review := seedArtifactLyricsSourceReviews(t, s, 1)[0]
	base := LyricsSourceReviewBatchDecisionParams{
		Gate: LyricsSourceReviewGateOverall, Decision: LyricsSourceReviewDecisionApproved,
		Items: []LyricsSourceReviewBatchItem{{ReviewID: review.ReviewID, ExpectedVersion: 1}},
		Actor: "admin", IdempotencyKey: "batch-shared-key-0001", Note: "",
	}
	if _, err := s.DecideLyricsSourceReviewBatch(context.Background(), base); err != nil {
		t.Fatal(err)
	}
	base.Actor = "other-admin"
	result, err := s.DecideLyricsSourceReviewBatch(context.Background(), base)
	if !errors.Is(err, ErrLyricsSourceReviewConflict) || len(result.Conflicts) != 1 || result.Conflicts[0].Reason != "not_pending" {
		t.Fatalf("actor-scoped result=%+v err=%v", result, err)
	}
	var ledger int
	if err := database.QueryRow(`SELECT COUNT(*) FROM lyrics_source_review_batch_idempotency`).Scan(&ledger); err != nil || ledger != 1 {
		t.Fatalf("actor-scoped ledger=%d err=%v", ledger, err)
	}
}

func TestLyricsSourceReviewBatchConflictReasonsCoverCandidateStaleAndMissing(t *testing.T) {
	s, _ := openLyricsSourcePipelineStore(t)
	artifact := seedArtifactLyricsSourceReviews(t, s, 1)[0]
	identity := seedFullLyricsSourceCatalog(t, s, 10)
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	candidate, _, err := createLyricsSourceReviewTx(context.Background(), tx, createLyricsSourceReviewParams{
		Kind: LyricsSourceReviewKindCandidate, MusicID: 10, CatalogFingerprint: identity.CatalogFingerprint,
		ReasonCode: "batch_candidate", EvidenceJSON: []byte(`{"candidates":[]}`), CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	candidateID := candidate.ReviewID
	result, err := s.DecideLyricsSourceReviewBatch(context.Background(), LyricsSourceReviewBatchDecisionParams{
		Gate: LyricsSourceReviewGateOverall, Decision: LyricsSourceReviewDecisionRejected,
		Items: []LyricsSourceReviewBatchItem{
			{ReviewID: artifact.ReviewID, ExpectedVersion: 2},
			{ReviewID: candidateID, ExpectedVersion: 1},
			{ReviewID: candidateID + 1000000, ExpectedVersion: 1},
		},
		Actor: "admin", IdempotencyKey: "batch-conflict-reasons-01", Note: "",
	})
	if !errors.Is(err, ErrLyricsSourceReviewConflict) || len(result.Conflicts) != 3 {
		t.Fatalf("conflict reasons result=%+v err=%v", result, err)
	}
	want := map[int64]string{
		artifact.ReviewID: "stale_version", candidateID: "not_artifact_review", candidateID + 1000000: "not_found",
	}
	for _, conflict := range result.Conflicts {
		if want[conflict.ReviewID] != conflict.Reason {
			t.Fatalf("conflict=%+v want=%q", conflict, want[conflict.ReviewID])
		}
	}
}

func TestLyricsSourceReviewBatchRejectsInvalidShapeBeforeWriting(t *testing.T) {
	s, database := openLyricsSourcePipelineStore(t)
	review := seedArtifactLyricsSourceReviews(t, s, 1)[0]
	for name, params := range map[string]LyricsSourceReviewBatchDecisionParams{
		"empty": {Gate: LyricsSourceReviewGateOverall, Decision: LyricsSourceReviewDecisionApproved, Actor: "admin", IdempotencyKey: "batch-invalid-empty", Note: ""},
		"duplicate": {Gate: LyricsSourceReviewGateOverall, Decision: LyricsSourceReviewDecisionApproved,
			Items: []LyricsSourceReviewBatchItem{{ReviewID: review.ReviewID, ExpectedVersion: 1}, {ReviewID: review.ReviewID, ExpectedVersion: 1}},
			Actor: "admin", IdempotencyKey: "batch-invalid-duplicate", Note: ""},
		"wrong gate": {Gate: LyricsSourceReviewGateIdentity, Decision: LyricsSourceReviewDecisionApproved,
			Items: []LyricsSourceReviewBatchItem{{ReviewID: review.ReviewID, ExpectedVersion: 1}}, Actor: "admin", IdempotencyKey: "batch-invalid-gate-01", Note: ""},
		"nonempty note": {Gate: LyricsSourceReviewGateOverall, Decision: LyricsSourceReviewDecisionApproved,
			Items: []LyricsSourceReviewBatchItem{{ReviewID: review.ReviewID, ExpectedVersion: 1}}, Actor: "admin", IdempotencyKey: "batch-invalid-note-01", Note: "forbidden"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := s.DecideLyricsSourceReviewBatch(context.Background(), params); !errors.Is(err, ErrLyricsSourceInvalidRequest) {
				t.Fatalf("invalid batch err=%v", err)
			}
		})
	}
	var ledger, decisions int
	if err := database.QueryRow(`SELECT COUNT(*) FROM lyrics_source_review_batch_idempotency`).Scan(&ledger); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM lyrics_source_review_decisions`).Scan(&decisions); err != nil {
		t.Fatal(err)
	}
	if ledger != 0 || decisions != 0 {
		t.Fatalf("invalid batch wrote ledger=%d decisions=%d", ledger, decisions)
	}
}

func TestLyricsSourceReviewMutationsRejectNonemptyNotesBeforeWrites(t *testing.T) {
	t.Run("single overall", func(t *testing.T) {
		s, database := openLyricsSourcePipelineStore(t)
		review := seedArtifactLyricsSourceReviews(t, s, 1)[0]
		assertLyricsSourceReviewMutationCounts(t, database, 0, 0, 0)

		_, _, err := s.DecideLyricsSourceReview(context.Background(), LyricsSourceReviewDecisionParams{
			ReviewID: review.ReviewID, Gate: LyricsSourceReviewGateOverall, Decision: LyricsSourceReviewDecisionApproved,
			ExpectedVersion: 1, Actor: "admin", IdempotencyKey: "single-note-rejected-01", Note: "forbidden",
		})
		if !errors.Is(err, ErrLyricsSourceInvalidRequest) {
			t.Fatalf("single nonempty note error=%v", err)
		}
		assertLyricsSourceReviewUnchanged(t, database, review.ReviewID)
		assertLyricsSourceReviewMutationCounts(t, database, 0, 0, 0)
	})

	t.Run("batch overall", func(t *testing.T) {
		s, database := openLyricsSourcePipelineStore(t)
		reviews := seedArtifactLyricsSourceReviews(t, s, 2)
		assertLyricsSourceReviewMutationCounts(t, database, 0, 0, 0)

		_, err := s.DecideLyricsSourceReviewBatch(context.Background(), LyricsSourceReviewBatchDecisionParams{
			Gate: LyricsSourceReviewGateOverall, Decision: LyricsSourceReviewDecisionRejected,
			Items: []LyricsSourceReviewBatchItem{
				{ReviewID: reviews[0].ReviewID, ExpectedVersion: 1},
				{ReviewID: reviews[1].ReviewID, ExpectedVersion: 1},
			},
			Actor: "admin", IdempotencyKey: "batch-note-rejected-001", Note: "forbidden",
		})
		if !errors.Is(err, ErrLyricsSourceInvalidRequest) {
			t.Fatalf("batch nonempty note error=%v", err)
		}
		for _, review := range reviews {
			assertLyricsSourceReviewUnchanged(t, database, review.ReviewID)
		}
		assertLyricsSourceReviewMutationCounts(t, database, 0, 0, 0)
	})

	t.Run("candidate decision", func(t *testing.T) {
		s, database := openLyricsSourcePipelineStore(t)
		reviewID := seedCandidateLyricsSourceReviews(t, s, 1)[0]
		assertLyricsSourceReviewMutationCounts(t, database, 0, 0, 0)

		_, _, err := s.SelectLyricsSourceCandidate(context.Background(), LyricsSourceCandidateSelectionParams{
			ReviewID: reviewID, Exclude: true, ExpectedVersion: 1,
			Actor: "admin", IdempotencyKey: "candidate-note-reject-1", Note: "forbidden",
		})
		if !errors.Is(err, ErrLyricsSourceInvalidRequest) {
			t.Fatalf("candidate nonempty note error=%v", err)
		}
		assertLyricsSourceReviewUnchanged(t, database, reviewID)
		assertLyricsSourceReviewMutationCounts(t, database, 0, 0, 0)
	})
}

func TestLyricsSourceReviewDetailPreservesHistoricalNotes(t *testing.T) {
	s, database := openLyricsSourcePipelineStore(t)
	review := seedArtifactLyricsSourceReviews(t, s, 1)[0]
	if _, err := database.Exec(`INSERT INTO lyrics_source_review_decisions
		(review_id,gate,decision,selected_candidate_json,actor,note,idempotency_key,request_sha256,
		 expected_version,result_version,decided_at,provider)
		VALUES (?,'overall','approved',NULL,'legacy-admin','historical note','historical-note-key-01',?,1,2,?,'vocaloid_fandom')`,
		review.ReviewID, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", time.Now().UTC().UnixMilli()); err != nil {
		t.Fatal(err)
	}

	detail, err := s.GetLyricsSourceReviewDetail(context.Background(), review.ReviewID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Decisions) != 1 || detail.Decisions[0].Note != "historical note" {
		t.Fatalf("historical decisions=%+v", detail.Decisions)
	}
}

func assertLyricsSourceReviewUnchanged(t *testing.T, database lyricsSourceReviewQuery, reviewID int64) {
	t.Helper()
	item, err := loadLyricsSourceReviewItemContext(context.Background(), database, `WHERE review_id=?`, reviewID)
	if err != nil || item.State != LyricsSourceReviewStatePending || item.Version != 1 {
		t.Fatalf("review %d changed item=%+v err=%v", reviewID, item, err)
	}
}

func assertLyricsSourceReviewMutationCounts(t *testing.T, database lyricsSourceReviewQuery, decisions, batchLedgers, audits int) {
	t.Helper()
	for table, want := range map[string]int{
		"lyrics_source_review_decisions":         decisions,
		"lyrics_source_review_batch_idempotency": batchLedgers,
		"audit_log":                              audits,
	} {
		var got int
		if err := database.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM `+table).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s count=%d want=%d", table, got, want)
		}
	}
}

func TestInvalidCandidateIdentityIsInvalidRequest(t *testing.T) {
	s, _ := openLyricsSourcePipelineStore(t)
	invalid := model.LyricsSourceCandidateIdentity{
		PageID: 12, RevisionID: 34, SHA1: strings.Repeat("a", 40), Title: "合成試験曲",
		CanonicalURL: "https://evil.example/wiki/Song?oldid=34", Categories: []string{},
	}
	_, _, err := s.SelectLyricsSourceCandidate(context.Background(), LyricsSourceCandidateSelectionParams{
		ReviewID: 1, CandidateIdentity: &invalid, ExpectedVersion: 1, Actor: "admin",
		IdempotencyKey: "idempotency-key-0001",
	})
	if !errors.Is(err, ErrLyricsSourceInvalidRequest) {
		t.Fatalf("invalid candidate err=%v", err)
	}
}
