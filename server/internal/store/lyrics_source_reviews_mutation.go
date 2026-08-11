package store

import (
	"context"
	"crypto/sha256"
	"database/sql"

	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"net/url"

	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"moesekai/server/internal/lyricssource"
	"moesekai/server/internal/model"
)

func (s *Store) SelectLyricsSourceCandidate(ctx context.Context, params LyricsSourceCandidateSelectionParams) (model.LyricsSourceReviewItem, bool, error) {
	if params.Exclude == (params.CandidateIdentity != nil) {
		return model.LyricsSourceReviewItem{}, false, invalidLyricsReviewRequest("provide exactly one candidate identity or exclude")
	}
	if params.Note != "" {
		return model.LyricsSourceReviewItem{}, false, invalidLyricsReviewRequest("new review decisions cannot include a note")
	}
	decision := "excluded"
	if params.CandidateIdentity != nil {
		decision = "selected"
		if err := validateLyricsSourceReviewCandidateIdentity(*params.CandidateIdentity); err != nil {
			return model.LyricsSourceReviewItem{}, false, invalidLyricsReviewRequest("invalid candidate identity")
		}
	}
	requestPayload := struct {
		ReviewID          int64                                `json:"reviewId"`
		CandidateIdentity *model.LyricsSourceCandidateIdentity `json:"candidateIdentity,omitempty"`
		Exclude           bool                                 `json:"exclude"`
		ExpectedVersion   int64                                `json:"expectedVersion"`
		Note              string                               `json:"note"`
	}{params.ReviewID, params.CandidateIdentity, params.Exclude, params.ExpectedVersion, params.Note}
	requestSHA, err := validateLyricsReviewMutation(requestPayload, params.ReviewID, params.ExpectedVersion, params.Actor, params.IdempotencyKey, params.Note)
	if err != nil {
		return model.LyricsSourceReviewItem{}, false, err
	}
	return s.applyLyricsSourceReviewDecision(ctx, params.ReviewID, "candidate", decision, params.CandidateIdentity,
		params.ExpectedVersion, params.Actor, params.IdempotencyKey, params.Note, requestSHA, params.DecidedAt)
}

func validateLyricsReviewMutation(payload any, reviewID, expectedVersion int64, actor, idempotencyKey, note string) (string, error) {
	actor = strings.TrimSpace(actor)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if reviewID <= 0 || expectedVersion <= 0 || actor == "" || len(actor) > maxLyricsReviewActorBytes || !utf8.ValidString(actor) ||
		len(idempotencyKey) < 16 || len(idempotencyKey) > maxLyricsReviewIdempotencyKey || !utf8.ValidString(idempotencyKey) ||
		len(note) > maxLyricsReviewNoteBytes || !utf8.ValidString(note) {
		return "", invalidLyricsReviewRequest("invalid mutation")
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func (s *Store) applyLyricsSourceReviewDecision(ctx context.Context, reviewID int64, gate, decision string,
	selected *model.LyricsSourceCandidateIdentity, expectedVersion int64, actor, idempotencyKey, note, requestSHA string,
	decidedAt time.Time) (model.LyricsSourceReviewItem, bool, error) {
	if ctx == nil {
		return model.LyricsSourceReviewItem{}, false, errors.New("lyrics source review mutation requires context")
	}
	actor, idempotencyKey = strings.TrimSpace(actor), strings.TrimSpace(idempotencyKey)
	if decidedAt.IsZero() {
		decidedAt = time.Now().UTC()
	}
	decidedAt = canonicalLyricsDiscoveryTime(decidedAt)
	if decidedAt.After(time.Now().UTC().Add(maxLyricsDiscoveryClockSkew)) {
		return model.LyricsSourceReviewItem{}, false, errors.New("invalid lyrics source review decision time")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.LyricsSourceReviewItem{}, false, err
	}
	defer tx.Rollback()
	var existingGate, existingDecision, existingSHA string
	var existingReviewID, resultVersion int64
	err = tx.QueryRowContext(ctx, `SELECT review_id, gate, decision, request_sha256, result_version FROM lyrics_source_review_decisions
		WHERE actor=? AND idempotency_key=?`, actor, idempotencyKey).Scan(
		&existingReviewID, &existingGate, &existingDecision, &existingSHA, &resultVersion)
	if err == nil {
		if existingReviewID != reviewID || existingGate != gate || existingDecision != decision || existingSHA != requestSHA {
			return model.LyricsSourceReviewItem{}, false, ErrLyricsSourceIdempotency
		}
		item, loadErr := loadLyricsSourceReviewItemAtVersion(ctx, tx, reviewID, resultVersion)
		if loadErr != nil {
			return model.LyricsSourceReviewItem{}, false, loadErr
		}
		return item, true, tx.Commit()
	}
	if err != sql.ErrNoRows {
		return model.LyricsSourceReviewItem{}, false, err
	}
	if collision, collisionErr := lyricsSourceReviewBatchKeyExists(ctx, tx, actor, idempotencyKey); collisionErr != nil {
		return model.LyricsSourceReviewItem{}, false, collisionErr
	} else if collision {
		return model.LyricsSourceReviewItem{}, false, ErrLyricsSourceIdempotency
	}
	item, err := loadLyricsSourceReviewItemContext(ctx, tx, `WHERE review_id=?`, reviewID)
	if err != nil {
		return model.LyricsSourceReviewItem{}, false, err
	}
	if item.State != LyricsSourceReviewStatePending || item.Version != expectedVersion {
		return item, false, ErrLyricsSourceReviewConflict
	}
	if gate == "candidate" {
		if item.Kind != LyricsSourceReviewKindCandidate {
			return item, false, ErrLyricsSourceReviewConflict
		}
		if selected != nil {
			candidate, matched, err := candidateIdentityInReview(item.EvidenceJSON, *selected)
			if err != nil {
				return item, false, err
			}
			if !matched || candidate == nil {
				return item, false, ErrLyricsSourceReviewConflict
			}
			provider := candidate.Provider
			if provider == "" {
				provider = model.LyricsSourceProviderVocaloidFandom
			}
			if err := verifyLyricsSourceReviewCandidateEvidence(ctx, tx, item.ReviewID, *candidate); err != nil {
				return item, false, err
			}
			fixedCandidate := legacyLyricsDiscoveryCandidateIdentity(candidate)
			if _, _, err := enqueueLyricsDiscoveryJobTx(ctx, tx, EnqueueLyricsDiscoveryJobParams{
				Provider: provider, Kind: model.LyricsDiscoveryJobFetchRevision,
				Target: model.LyricsDiscoveryJobTarget{MusicID: item.MusicID, PageID: fixedCandidate.PageID,
					RevisionID: fixedCandidate.RevisionID, ExpectedSHA1: fixedCandidate.SHA1, CatalogFingerprint: item.CatalogFingerprint,
					PolicyVersion: model.LyricsMatchingPolicyVersion, FixedCandidate: fixedCandidate},
				FixedCandidate: candidate,
				MaxAttempts:    DefaultLyricsDiscoveryJobMaxAttempts,
			}, decidedAt); err != nil {
				return item, false, err
			}
		}
	} else {
		if item.Kind != LyricsSourceReviewKindArtifact {
			return item, false, ErrLyricsSourceReviewConflict
		}
		if gate != LyricsSourceReviewGateOverall && reviewGateValue(item, gate) != "pending" {
			return item, false, ErrLyricsSourceReviewConflict
		}
	}

	next := item
	next.Version++
	next.UpdatedAt = decidedAt
	if gate == "candidate" {
		if decision == "selected" {
			next.State = LyricsSourceReviewStateApproved
		} else {
			next.State = LyricsSourceReviewStateRejected
		}
		next.CompletedAt = decidedAt
	} else {
		if gate == LyricsSourceReviewGateOverall {
			next.IdentityGate, next.SourceUseGate, next.ParseGate = decision, decision, decision
		} else {
			setReviewGate(&next, gate, decision)
		}
		if decision == LyricsSourceReviewDecisionRejected {
			next.State, next.CompletedAt = LyricsSourceReviewStateRejected, decidedAt
		} else if next.IdentityGate == "approved" && next.SourceUseGate == "approved" && next.ParseGate == "approved" {
			next.State, next.CompletedAt = LyricsSourceReviewStateApproved, decidedAt
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE lyrics_source_review_items SET state=?, identity_gate=?, source_use_gate=?, parse_gate=?,
		version=?, updated_at=?, completed_at=? WHERE review_id=? AND state='pending' AND version=?`, next.State, next.IdentityGate,
		next.SourceUseGate, next.ParseGate, next.Version, decidedAt.UnixMilli(), nullableTimeMillis(next.CompletedAt), reviewID, expectedVersion)
	if err != nil {
		return item, false, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return item, false, ErrLyricsSourceReviewConflict
	}
	var selectedJSON any
	if selected != nil {
		encoded, _ := json.Marshal(selected)
		selectedJSON = string(encoded)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO lyrics_source_review_decisions
		(review_id, gate, decision, selected_candidate_json, actor, note, idempotency_key, request_sha256,
		 expected_version, result_version, decided_at, provider) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
		 (SELECT provider FROM lyrics_source_review_items WHERE review_id=?))`, reviewID, gate,
		decision, selectedJSON, actor, note, idempotencyKey, requestSHA, expectedVersion, next.Version, decidedAt.UnixMilli(), reviewID); err != nil {
		return item, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_log(ts, user, action, detail) VALUES (?, ?, ?, ?)`,
		decidedAt.Unix(), actor, "lyrics_source.review."+decision,
		fmt.Sprintf("reviewId=%d gate=%s version=%d", reviewID, gate, next.Version)); err != nil {
		return item, false, err
	}
	if err := tx.Commit(); err != nil {
		return item, false, err
	}
	return next, false, nil
}

func candidateIdentityInReview(evidence []byte, selected model.LyricsSourceCandidateIdentity) (*lyricssource.Candidate, bool, error) {
	var stored struct {
		Candidates []lyricssource.Candidate `json:"candidates"`
	}
	if err := json.Unmarshal(evidence, &stored); err != nil {
		return nil, false, err
	}
	var matched *lyricssource.Candidate
	for index := range stored.Candidates {
		candidate := cloneLyricsDiscoveryCandidate(stored.Candidates[index])
		provider := candidate.Provider
		if provider == "" {
			provider = model.LyricsSourceProviderVocaloidFandom
		}
		if validateProviderAwareLyricsDiscoveryCandidate(provider, candidate) != nil ||
			!sameLyricsSourceCandidateIdentity(selected, *legacyLyricsDiscoveryCandidateIdentity(&candidate)) {
			continue
		}
		if matched != nil {
			// The legacy review mutation shape cannot disambiguate two provider
			// candidates that collapse to the same page/revision identity.
			return nil, false, ErrLyricsSourceReviewConflict
		}
		matched = &candidate
	}
	return matched, matched != nil, nil
}

func validateLyricsSourceReviewCandidateIdentity(candidate model.LyricsSourceCandidateIdentity) error {
	if candidate.PageID <= 0 || candidate.RevisionID <= 0 || !lyricssource.HasCanonicalSHA1(candidate.SHA1) ||
		strings.TrimSpace(candidate.Title) == "" || len(candidate.Title) > 2048 || len(candidate.CanonicalURL) > 4096 ||
		candidate.Categories == nil || len(candidate.Categories) > maxLyricsSourceCategories {
		return errors.New("invalid lyrics source candidate identity")
	}
	categories, err := canonicalLyricsSourceStringSet(candidate.Categories, maxLyricsSourceCategories, maxLyricsSourceCategoryBytes)
	if err != nil || !sameStringSlice(categories, candidate.Categories) {
		return errors.New("invalid lyrics source candidate categories")
	}
	if validateCanonicalLyricsSourceURL(candidate.CanonicalURL, candidate.Title, candidate.RevisionID) == nil ||
		validateProviderCanonicalLyricsSourceURL(model.LyricsSourceProviderMoegirl, model.LyricsSourceOriginMoegirl,
			candidate.CanonicalURL, candidate.Title, candidate.RevisionID) == nil {
		return nil
	}
	return errors.New("invalid lyrics source candidate URL")
}

func validateLyricsSourceCandidateIdentity(candidate model.LyricsSourceCandidateIdentity) error {
	if candidate.PageID <= 0 || candidate.RevisionID <= 0 || !lyricssource.HasCanonicalSHA1(candidate.SHA1) ||
		strings.TrimSpace(candidate.Title) == "" || len(candidate.Title) > 2048 || len(candidate.CanonicalURL) > 4096 ||
		candidate.Categories == nil || len(candidate.Categories) > maxLyricsSourceCategories {
		return errors.New("invalid lyrics source candidate identity")
	}
	if err := validateCanonicalLyricsSourceURL(candidate.CanonicalURL, candidate.Title, candidate.RevisionID); err != nil {
		return errors.New("invalid lyrics source candidate URL")
	}
	categories, err := canonicalLyricsSourceStringSet(candidate.Categories, maxLyricsSourceCategories, maxLyricsSourceCategoryBytes)
	if err != nil || len(categories) != len(candidate.Categories) {
		return errors.New("invalid lyrics source candidate categories")
	}
	for index := range categories {
		if categories[index] != candidate.Categories[index] {
			return errors.New("lyrics source candidate categories are not canonical")
		}
	}
	return nil
}

func validateCanonicalLyricsSourceURL(rawURL, title string, revisionID int) error {
	if rawURL == "" || rawURL != strings.TrimSpace(rawURL) || len(rawURL) > 4096 ||
		title == "" || title != strings.TrimSpace(title) || revisionID <= 0 {
		return errors.New("invalid canonical lyrics source URL")
	}
	expected := url.URL{
		Scheme: "https",
		Host:   "vocaloid.fandom.com",
		Path:   "/wiki/" + strings.ReplaceAll(title, " ", "_"),
	}
	expected.RawQuery = "oldid=" + strconv.Itoa(revisionID)
	if rawURL != expected.String() {
		return errors.New("noncanonical lyrics source URL")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "vocaloid.fandom.com" || parsed.User != nil ||
		parsed.Fragment != "" || parsed.RawQuery != expected.RawQuery || parsed.Path != expected.Path ||
		parsed.EscapedPath() != expected.EscapedPath() || parsed.Opaque != "" || parsed.ForceQuery {
		return errors.New("invalid canonical lyrics source URL")
	}
	return nil
}

func validateProviderCanonicalLyricsSourceURL(provider model.LyricsSourceProvider, origin, rawURL, title string, revisionID int) error {
	if provider == model.LyricsSourceProviderVocaloidFandom {
		if origin != model.LyricsSourceOriginVocaloidFandom {
			return errors.New("invalid Vocaloid Fandom origin")
		}
		return validateCanonicalLyricsSourceURL(rawURL, title, revisionID)
	}
	if provider != model.LyricsSourceProviderMoegirl || origin != model.LyricsSourceOriginMoegirl ||
		rawURL == "" || rawURL != strings.TrimSpace(rawURL) || len(rawURL) > 4096 ||
		title == "" || title != strings.TrimSpace(title) || revisionID <= 0 {
		return errors.New("invalid Moegirl canonical lyrics source URL")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Scheme+"://"+parsed.Host != origin || parsed.User != nil ||
		parsed.Fragment != "" || parsed.Opaque != "" || parsed.ForceQuery || parsed.Path == "" {
		return errors.New("invalid Moegirl canonical lyrics source URL")
	}
	query := parsed.Query()
	if len(query["oldid"]) != 1 || query.Get("oldid") != strconv.Itoa(revisionID) || parsed.RawQuery != query.Encode() {
		return errors.New("noncanonical Moegirl revision query")
	}
	switch {
	case parsed.EscapedPath() == "/index.php":
		if len(query) != 2 || len(query["title"]) != 1 || query.Get("title") != title {
			return errors.New("noncanonical Moegirl index revision URL")
		}
	case strings.HasPrefix(parsed.EscapedPath(), "/wiki/") && parsed.EscapedPath() != "/wiki/":
		if len(query) != 1 {
			return errors.New("noncanonical Moegirl wiki revision URL")
		}
		expected := url.URL{Scheme: "https", Host: "moegirl.icu", Path: "/wiki/" + strings.ReplaceAll(title, " ", "_")}
		expected.RawQuery = "oldid=" + strconv.Itoa(revisionID)
		if rawURL != expected.String() {
			return errors.New("noncanonical Moegirl wiki revision URL")
		}
	default:
		return errors.New("invalid Moegirl revision path")
	}
	return nil
}

func reviewGateValue(item model.LyricsSourceReviewItem, gate string) string {
	switch gate {
	case LyricsSourceReviewGateIdentity:
		return item.IdentityGate
	case LyricsSourceReviewGateSourceUse:
		return item.SourceUseGate
	default:
		return item.ParseGate
	}
}

func setReviewGate(item *model.LyricsSourceReviewItem, gate, decision string) {
	switch gate {
	case LyricsSourceReviewGateIdentity:
		item.IdentityGate = decision
	case LyricsSourceReviewGateSourceUse:
		item.SourceUseGate = decision
	case LyricsSourceReviewGateParse:
		item.ParseGate = decision
	}
}

func nullableTimeMillis(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UnixMilli()
}
