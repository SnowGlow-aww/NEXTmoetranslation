package store

import (
	"context"
	"crypto/sha256"
	"database/sql"

	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"

	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"moesekai/server/internal/model"
)

func (s *Store) DecideLyricsSourceReview(ctx context.Context, params LyricsSourceReviewDecisionParams) (model.LyricsSourceReviewItem, bool, error) {
	if params.Gate != LyricsSourceReviewGateIdentity && params.Gate != LyricsSourceReviewGateSourceUse &&
		params.Gate != LyricsSourceReviewGateParse && params.Gate != LyricsSourceReviewGateOverall {
		return model.LyricsSourceReviewItem{}, false, invalidLyricsReviewRequest("invalid gate")
	}
	if params.Decision != LyricsSourceReviewDecisionApproved && params.Decision != LyricsSourceReviewDecisionRejected {
		return model.LyricsSourceReviewItem{}, false, invalidLyricsReviewRequest("invalid decision")
	}
	if params.Note != "" {
		return model.LyricsSourceReviewItem{}, false, invalidLyricsReviewRequest("new review decisions cannot include a note")
	}
	requestPayload := struct {
		ReviewID        int64  `json:"reviewId"`
		Gate            string `json:"gate"`
		Decision        string `json:"decision"`
		ExpectedVersion int64  `json:"expectedVersion"`
		Note            string `json:"note"`
	}{params.ReviewID, params.Gate, params.Decision, params.ExpectedVersion, params.Note}
	requestSHA, err := validateLyricsReviewMutation(requestPayload, params.ReviewID, params.ExpectedVersion, params.Actor, params.IdempotencyKey, params.Note)
	if err != nil {
		return model.LyricsSourceReviewItem{}, false, err
	}
	return s.applyLyricsSourceReviewDecision(ctx, params.ReviewID, params.Gate, params.Decision, nil, params.ExpectedVersion,
		params.Actor, params.IdempotencyKey, params.Note, requestSHA, params.DecidedAt)
}

func (s *Store) DecideLyricsSourceReviewBatch(ctx context.Context, params LyricsSourceReviewBatchDecisionParams) (LyricsSourceReviewBatchResult, error) {
	if params.Gate != LyricsSourceReviewGateOverall {
		return LyricsSourceReviewBatchResult{}, invalidLyricsReviewRequest("batch decisions require the overall gate")
	}
	if params.Decision != LyricsSourceReviewDecisionApproved && params.Decision != LyricsSourceReviewDecisionRejected {
		return LyricsSourceReviewBatchResult{}, invalidLyricsReviewRequest("invalid decision")
	}
	if params.Note != "" {
		return LyricsSourceReviewBatchResult{}, invalidLyricsReviewRequest("new review decisions cannot include a note")
	}
	items, itemsJSON, requestSHA, actor, idempotencyKey, err := canonicalLyricsSourceReviewBatch(params)
	if err != nil {
		return LyricsSourceReviewBatchResult{}, err
	}
	if ctx == nil {
		return LyricsSourceReviewBatchResult{}, errors.New("lyrics source review batch mutation requires context")
	}
	decidedAt := params.DecidedAt
	if decidedAt.IsZero() {
		decidedAt = time.Now().UTC()
	}
	decidedAt = canonicalLyricsDiscoveryTime(decidedAt)
	if decidedAt.After(time.Now().UTC().Add(maxLyricsDiscoveryClockSkew)) {
		return LyricsSourceReviewBatchResult{}, errors.New("invalid lyrics source review batch decision time")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return LyricsSourceReviewBatchResult{}, err
	}
	defer tx.Rollback()

	var existingSHA, existingGate, existingDecision, existingItemsJSON, existingNote string
	err = tx.QueryRowContext(ctx, `SELECT request_sha256, gate, decision, items_json, note
		FROM lyrics_source_review_batch_idempotency WHERE actor=? AND idempotency_key=?`, actor, idempotencyKey).Scan(
		&existingSHA, &existingGate, &existingDecision, &existingItemsJSON, &existingNote)
	if err == nil {
		if existingSHA != requestSHA || existingGate != params.Gate || existingDecision != params.Decision ||
			existingItemsJSON != itemsJSON || existingNote != params.Note {
			return LyricsSourceReviewBatchResult{}, ErrLyricsSourceIdempotency
		}
		replayed := make([]model.LyricsSourceReviewItem, 0, len(items))
		for _, batchItem := range items {
			item, loadErr := loadLyricsSourceReviewItemAtVersion(ctx, tx, batchItem.ReviewID, batchItem.ExpectedVersion+1)
			if loadErr != nil {
				return LyricsSourceReviewBatchResult{}, loadErr
			}
			replayed = append(replayed, item)
		}
		if err := tx.Commit(); err != nil {
			return LyricsSourceReviewBatchResult{}, err
		}
		return LyricsSourceReviewBatchResult{Items: replayed, Replayed: true}, nil
	}
	if err != sql.ErrNoRows {
		return LyricsSourceReviewBatchResult{}, err
	}
	if collision, collisionErr := lyricsSourceReviewDecisionKeyExists(ctx, tx, actor, idempotencyKey); collisionErr != nil {
		return LyricsSourceReviewBatchResult{}, collisionErr
	} else if collision {
		return LyricsSourceReviewBatchResult{}, ErrLyricsSourceIdempotency
	}

	childKeys := make([]string, len(items))
	reservedKeys := map[string]struct{}{idempotencyKey: {}}
	for index, batchItem := range items {
		childKeys[index] = lyricsSourceReviewBatchChildIdempotencyKey(idempotencyKey, requestSHA, batchItem)
		if _, collision := reservedKeys[childKeys[index]]; collision {
			return LyricsSourceReviewBatchResult{}, ErrLyricsSourceIdempotency
		}
		reservedKeys[childKeys[index]] = struct{}{}
		if collision, collisionErr := lyricsSourceReviewIdempotencyKeyExists(ctx, tx, actor, childKeys[index]); collisionErr != nil {
			return LyricsSourceReviewBatchResult{}, collisionErr
		} else if collision {
			return LyricsSourceReviewBatchResult{}, ErrLyricsSourceIdempotency
		}
	}

	current := make([]model.LyricsSourceReviewItem, 0, len(items))
	conflicts := make([]LyricsSourceReviewBatchConflict, 0)
	for _, batchItem := range items {
		item, loadErr := loadLyricsSourceReviewItemContext(ctx, tx, `WHERE review_id=?`, batchItem.ReviewID)
		if errors.Is(loadErr, ErrLyricsSourceReviewNotFound) {
			conflicts = append(conflicts, LyricsSourceReviewBatchConflict{ReviewID: batchItem.ReviewID, Reason: "not_found"})
			continue
		}
		if loadErr != nil {
			return LyricsSourceReviewBatchResult{}, loadErr
		}
		switch {
		case item.Kind != LyricsSourceReviewKindArtifact:
			conflicts = append(conflicts, LyricsSourceReviewBatchConflict{ReviewID: batchItem.ReviewID, Reason: "not_artifact_review"})
		case item.State != LyricsSourceReviewStatePending:
			conflicts = append(conflicts, LyricsSourceReviewBatchConflict{ReviewID: batchItem.ReviewID, Reason: "not_pending"})
		case item.Version != batchItem.ExpectedVersion:
			conflicts = append(conflicts, LyricsSourceReviewBatchConflict{ReviewID: batchItem.ReviewID, Reason: "stale_version"})
		default:
			current = append(current, item)
		}
	}
	if len(conflicts) != 0 {
		return LyricsSourceReviewBatchResult{Conflicts: conflicts}, ErrLyricsSourceReviewConflict
	}

	resultItems := make([]model.LyricsSourceReviewItem, 0, len(items))
	for index, batchItem := range items {
		item := current[index]
		next := item
		next.State = params.Decision
		next.IdentityGate, next.SourceUseGate, next.ParseGate = params.Decision, params.Decision, params.Decision
		next.Version = batchItem.ExpectedVersion + 1
		next.UpdatedAt, next.CompletedAt = decidedAt, decidedAt
		result, updateErr := tx.ExecContext(ctx, `UPDATE lyrics_source_review_items
			SET state=?, identity_gate=?, source_use_gate=?, parse_gate=?, version=?, updated_at=?, completed_at=?
			WHERE review_id=? AND kind='artifact_review' AND state='pending' AND version=?`, next.State,
			next.IdentityGate, next.SourceUseGate, next.ParseGate, next.Version, decidedAt.UnixMilli(), decidedAt.UnixMilli(),
			batchItem.ReviewID, batchItem.ExpectedVersion)
		if updateErr != nil {
			return LyricsSourceReviewBatchResult{}, updateErr
		}
		affected, updateErr := result.RowsAffected()
		if updateErr != nil {
			return LyricsSourceReviewBatchResult{}, updateErr
		}
		if affected != 1 {
			return LyricsSourceReviewBatchResult{Conflicts: []LyricsSourceReviewBatchConflict{{
				ReviewID: batchItem.ReviewID, Reason: "stale_version",
			}}}, ErrLyricsSourceReviewConflict
		}
		childKey := childKeys[index]
		childPayload := struct {
			ReviewID        int64  `json:"reviewId"`
			Gate            string `json:"gate"`
			Decision        string `json:"decision"`
			ExpectedVersion int64  `json:"expectedVersion"`
			Note            string `json:"note"`
		}{batchItem.ReviewID, params.Gate, params.Decision, batchItem.ExpectedVersion, params.Note}
		encodedChild, marshalErr := json.Marshal(childPayload)
		if marshalErr != nil {
			return LyricsSourceReviewBatchResult{}, marshalErr
		}
		childDigest := sha256.Sum256(encodedChild)
		if _, insertErr := tx.ExecContext(ctx, `INSERT INTO lyrics_source_review_decisions
			(review_id, gate, decision, selected_candidate_json, actor, note, idempotency_key, request_sha256,
			 expected_version, result_version, decided_at, provider) VALUES (?, 'overall', ?, NULL, ?, ?, ?, ?, ?, ?, ?,
			 (SELECT provider FROM lyrics_source_review_items WHERE review_id=?))`,
			batchItem.ReviewID, params.Decision, actor, params.Note, childKey, hex.EncodeToString(childDigest[:]),
			batchItem.ExpectedVersion, next.Version, decidedAt.UnixMilli(), batchItem.ReviewID); insertErr != nil {
			return LyricsSourceReviewBatchResult{}, insertErr
		}
		if _, auditErr := tx.ExecContext(ctx, `INSERT INTO audit_log(ts, user, action, detail) VALUES (?, ?, ?, ?)`,
			decidedAt.Unix(), actor, "lyrics_source.review."+params.Decision,
			fmt.Sprintf("reviewId=%d gate=%s version=%d", batchItem.ReviewID, params.Gate, next.Version)); auditErr != nil {
			return LyricsSourceReviewBatchResult{}, auditErr
		}
		resultItems = append(resultItems, next)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO lyrics_source_review_batch_idempotency
		(actor, idempotency_key, request_sha256, gate, decision, items_json, item_count, note, decided_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, actor, idempotencyKey, requestSHA, params.Gate, params.Decision,
		itemsJSON, len(items), params.Note, decidedAt.UnixMilli()); err != nil {
		return LyricsSourceReviewBatchResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return LyricsSourceReviewBatchResult{}, err
	}
	return LyricsSourceReviewBatchResult{Items: resultItems}, nil
}

func canonicalLyricsSourceReviewBatch(params LyricsSourceReviewBatchDecisionParams) ([]LyricsSourceReviewBatchItem, string, string, string, string, error) {
	actor, idempotencyKey := strings.TrimSpace(params.Actor), strings.TrimSpace(params.IdempotencyKey)
	if len(params.Items) < 1 || len(params.Items) > 100 || actor == "" || len(actor) > maxLyricsReviewActorBytes ||
		!utf8.ValidString(actor) || len(idempotencyKey) < 16 || len(idempotencyKey) > maxLyricsReviewIdempotencyKey ||
		!utf8.ValidString(idempotencyKey) || len(params.Note) > maxLyricsReviewNoteBytes || !utf8.ValidString(params.Note) {
		return nil, "", "", "", "", invalidLyricsReviewRequest("invalid batch mutation")
	}
	items := append([]LyricsSourceReviewBatchItem(nil), params.Items...)
	sort.Slice(items, func(left, right int) bool { return items[left].ReviewID < items[right].ReviewID })
	for index, item := range items {
		if item.ReviewID <= 0 || item.ExpectedVersion <= 0 || index > 0 && items[index-1].ReviewID == item.ReviewID {
			return nil, "", "", "", "", invalidLyricsReviewRequest("invalid batch items")
		}
	}
	encodedItems, err := json.Marshal(items)
	if err != nil {
		return nil, "", "", "", "", err
	}
	digest := sha256.New()
	writeLyricsSourceReviewBatchDigestPart(digest, params.Gate)
	writeLyricsSourceReviewBatchDigestPart(digest, params.Decision)
	for _, item := range items {
		writeLyricsSourceReviewBatchDigestPart(digest, strconv.FormatInt(item.ReviewID, 10))
		writeLyricsSourceReviewBatchDigestPart(digest, strconv.FormatInt(item.ExpectedVersion, 10))
	}
	writeLyricsSourceReviewBatchDigestPart(digest, params.Note)
	return items, string(encodedItems), hex.EncodeToString(digest.Sum(nil)), actor, idempotencyKey, nil
}

func writeLyricsSourceReviewBatchDigestPart(digest hash.Hash, value string) {
	_, _ = digest.Write([]byte(strconv.Itoa(len(value))))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write([]byte(value))
	_, _ = digest.Write([]byte{0})
}

func lyricsSourceReviewBatchChildIdempotencyKey(parentKey, requestSHA string, item LyricsSourceReviewBatchItem) string {
	payload := fmt.Sprintf("lyrics-source-review-batch-child-v1\x00%s\x00%s\x00%d\x00%d", parentKey, requestSHA,
		item.ReviewID, item.ExpectedVersion)
	digest := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(digest[:])
}

func lyricsSourceReviewDecisionKeyExists(ctx context.Context, query lyricsSourceReviewQuery, actor, idempotencyKey string) (bool, error) {
	var exists int
	err := query.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM lyrics_source_review_decisions WHERE actor=? AND idempotency_key=?)`, actor, idempotencyKey).Scan(&exists)
	return exists != 0, err
}

func lyricsSourceReviewBatchKeyExists(ctx context.Context, query lyricsSourceReviewQuery, actor, idempotencyKey string) (bool, error) {
	var exists int
	err := query.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM lyrics_source_review_batch_idempotency WHERE actor=? AND idempotency_key=?)`, actor, idempotencyKey).Scan(&exists)
	return exists != 0, err
}

func lyricsSourceReviewIdempotencyKeyExists(ctx context.Context, query lyricsSourceReviewQuery, actor, idempotencyKey string) (bool, error) {
	decisionExists, err := lyricsSourceReviewDecisionKeyExists(ctx, query, actor, idempotencyKey)
	if err != nil || decisionExists {
		return decisionExists, err
	}
	return lyricsSourceReviewBatchKeyExists(ctx, query, actor, idempotencyKey)
}
