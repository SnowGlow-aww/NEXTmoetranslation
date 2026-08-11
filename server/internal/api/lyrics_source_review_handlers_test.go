package api

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"moesekai/server/internal/lyricssource"
	"moesekai/server/internal/model"
	"moesekai/server/internal/store"
)

func TestLyricsSourceReviewAPIsAreAdminJSONOnlyAndNoStore(t *testing.T) {
	h := setupLegacyAPI(t)
	list := authorizedRequest(t, h, http.MethodGet, "/api/admin/lyrics-source-reviews", nil)
	defer list.Body.Close()
	if list.StatusCode != http.StatusOK || list.Header.Get("Cache-Control") != "no-store" ||
		list.Header.Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("list status=%d headers=%v", list.StatusCode, list.Header)
	}
	var page struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.NewDecoder(list.Body).Decode(&page); err != nil || page.Items == nil {
		t.Fatalf("list body=%+v err=%v", page, err)
	}

	for _, path := range []string{"/api/admin/lyrics-source-reviews/decision", "/api/admin/lyrics-source-reviews/candidate-selection"} {
		request, err := http.NewRequest(http.MethodPut, h.server.URL+path, strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+h.token)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusUnsupportedMediaType || response.Header.Get("Cache-Control") != "no-store" ||
			!strings.Contains(string(body), "unsupported_media_type") {
			t.Fatalf("%s status=%d headers=%v body=%s", path, response.StatusCode, response.Header, body)
		}
	}
}

func TestLyricsSourceReviewAPIsRejectNonAdminAndURLCredentials(t *testing.T) {
	h := setupLegacyAPI(t)
	for _, path := range []string{
		"/api/admin/lyrics-source-reviews",
		"/api/admin/lyrics-source-reviews/detail?reviewId=1",
		"/api/admin/lyrics-source-reviews/import",
		"/api/admin/lyrics-source-reviews/decision",
		"/api/admin/lyrics-source-reviews/candidate-selection",
	} {
		method := http.MethodGet
		var body io.Reader
		if strings.HasSuffix(path, "/import") {
			method, body = http.MethodPost, strings.NewReader(`{}`)
		} else if strings.HasSuffix(path, "/decision") || strings.HasSuffix(path, "/candidate-selection") {
			method, body = http.MethodPut, strings.NewReader(`{}`)
		}
		request, err := http.NewRequest(method, h.server.URL+path, body)
		if err != nil {
			t.Fatal(err)
		}
		request.AddCookie(&http.Cookie{Name: "token", Value: h.token})
		query := request.URL.Query()
		query.Set("token", h.token)
		request.URL.RawQuery = query.Encode()
		if body != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized || response.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("URL/cookie credential %s status=%d headers=%v", path, response.StatusCode, response.Header)
		}
	}
}

func TestLyricsSourceReviewAPIRejectsUnknownFieldsAndInvalidQuery(t *testing.T) {
	h := setupLegacyAPI(t)
	response := authorizedRequest(t, h, http.MethodPut, "/api/admin/lyrics-source-reviews/decision", map[string]any{
		"reviewId": 1, "gate": "identity", "decision": "approved", "expectedVersion": 1,
		"idempotencyKey": "idempotency-key-0001", "note": "", "actor": "forged",
	})
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown actor status=%d", response.StatusCode)
	}
	for _, path := range []string{
		"/api/admin/lyrics-source-reviews/detail?reviewId=0",
		"/api/admin/lyrics-source-reviews/detail?reviewId=1&extra=1",
		"/api/admin/lyrics-source-reviews?state=pending&state=approved",
		"/api/admin/lyrics-source-reviews?extra=1",
		"/api/admin/lyrics-source-reviews?limit=",
		"/api/admin/lyrics-source-reviews?limit=0",
		"/api/admin/lyrics-source-reviews?limit=-1",
		"/api/admin/lyrics-source-reviews?limit=01",
		"/api/admin/lyrics-source-reviews?limit=101",
		"/api/admin/lyrics-source-reviews?limit=abc",
	} {
		invalid := authorizedRequest(t, h, http.MethodGet, path, nil)
		invalid.Body.Close()
		if invalid.StatusCode != http.StatusBadRequest {
			t.Fatalf("invalid query %q status=%d", path, invalid.StatusCode)
		}
	}
	for _, path := range []string{
		"/api/admin/lyrics-source-reviews",
		"/api/admin/lyrics-source-reviews?limit=1",
		"/api/admin/lyrics-source-reviews?limit=100",
	} {
		valid := authorizedRequest(t, h, http.MethodGet, path, nil)
		valid.Body.Close()
		if valid.StatusCode != http.StatusOK {
			t.Fatalf("valid query %q status=%d", path, valid.StatusCode)
		}
	}
}

func TestLyricsSourceReviewAPIDetailUsesExactSafeKeys(t *testing.T) {
	h := setupLegacyAPI(t)
	reviewID := seedArtifactReviewAPI(t, h)
	response := authorizedRequest(t, h, http.MethodGet, "/api/admin/lyrics-source-reviews/detail?reviewId="+strconv.FormatInt(reviewID, 10), nil)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("detail status=%d body=%s", response.StatusCode, body)
	}
	var detail map[string]json.RawMessage
	if err := json.NewDecoder(response.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	assertExactJSONKeys(t, detail, "analysis", "artifact", "associations", "candidates", "decisions", "review")
	var artifact map[string]json.RawMessage
	if err := json.Unmarshal(detail["artifact"], &artifact); err != nil {
		t.Fatal(err)
	}
	assertExactJSONKeys(t, artifact, "canonicalRevisionUrl", "categories", "firstFetchedAt", "mediaWikiSha1", "pageId", "pageTitle", "revisionId", "sourceOrigin", "sourceType")
	var analysis map[string]json.RawMessage
	if err := json.Unmarshal(detail["analysis"], &analysis); err != nil {
		t.Fatal(err)
	}
	assertExactJSONKeys(t, analysis, "extractedLines", "extractionOutcome", "extractorVersion", "matchOutcome", "matchingEvidence", "matchingPolicyVersion", "performers", "restrictionOutcome", "restrictionPolicyVersion", "restrictionRuleIds", "rubyGeneratorVersion", "selectedVersion")
	var selectedVersion map[string]json.RawMessage
	if err := json.Unmarshal(analysis["selectedVersion"], &selectedVersion); err != nil {
		t.Fatal(err)
	}
	assertExactJSONKeys(t, selectedVersion, "kind", "label")
	var performers []map[string]json.RawMessage
	if err := json.Unmarshal(analysis["performers"], &performers); err != nil || len(performers) != 0 {
		t.Fatalf("performers=%v err=%v", performers, err)
	}
	var extractedLines []map[string]json.RawMessage
	if err := json.Unmarshal(analysis["extractedLines"], &extractedLines); err != nil || len(extractedLines) != 1 {
		t.Fatalf("extracted lines=%v err=%v", extractedLines, err)
	}
	assertExactJSONKeys(t, extractedLines[0], "japanese", "segments", "trailingPerformerIds")
	var segments []map[string]json.RawMessage
	if err := json.Unmarshal(extractedLines[0]["segments"], &segments); err != nil || len(segments) != 1 {
		t.Fatalf("segments=%v err=%v", segments, err)
	}
	assertExactJSONKeys(t, segments[0], "performerIds", "ruby", "text")
	var ruby []map[string]json.RawMessage
	if err := json.Unmarshal(segments[0]["ruby"], &ruby); err != nil || len(ruby) == 0 {
		t.Fatalf("ruby=%v err=%v", ruby, err)
	}
	for _, span := range ruby {
		if _, hasReading := span["reading"]; hasReading {
			assertExactJSONKeys(t, span, "reading", "text")
		} else {
			assertExactJSONKeys(t, span, "text")
		}
	}
	if _, leaked := artifact["artifactId"]; leaked {
		t.Fatal("detail artifact leaked artifactId")
	}
	if _, leaked := analysis["analysisId"]; leaked {
		t.Fatal("detail analysis leaked analysisId")
	}
}

func TestLyricsSourceReviewAPIDetailCanonicalizesHistoricalMetadataAndNeverEchoesUnsafeValues(t *testing.T) {
	h := setupLegacyAPI(t)
	reviewID := seedArtifactReviewAPI(t, h)
	const englishLyric = "Jo-jo-jo-journey"
	const sourcePerformerID = "wiki-local-singer"
	const sourcePerformerName = "Romanized Singer"
	unsafePerformers, err := json.Marshal([]model.LyricsSourcePerformer{{
		PerformerID: sourcePerformerID, Name: sourcePerformerName, Color: "#33CCBB",
	}})
	if err != nil {
		t.Fatal(err)
	}
	unsafeLines, err := json.Marshal([]model.LyricsSourceExtractedLine{{
		Japanese: englishLyric,
		Segments: []model.LyricsSourceSegment{{
			Text: englishLyric, PerformerIDs: []string{sourcePerformerID},
			Ruby: []model.LyricsSourceRubySpan{{Text: englishLyric}},
		}},
		TrailingPerformerIDs: []string{sourcePerformerID},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`DROP TRIGGER lyrics_source_analyses_immutable_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`UPDATE lyrics_source_analyses
		SET performers_json=?,ruby_generator_version='sekaipedia-romaji-kana-v1',extracted_lines_json=?
		WHERE analysis_id=(SELECT analysis_id FROM lyrics_source_review_items WHERE review_id=?)`,
		string(unsafePerformers), string(unsafeLines), reviewID); err != nil {
		t.Fatal(err)
	}

	response := authorizedRequest(t, h, http.MethodGet,
		"/api/admin/lyrics-source-reviews/detail?reviewId="+strconv.FormatInt(reviewID, 10), nil)
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("canonical detail status=%d body=%s", response.StatusCode, body)
	}
	lowerBody := strings.ToLower(string(body))
	for _, prohibited := range []string{sourcePerformerID, strings.ToLower(sourcePerformerName), "sekaipedia-romaji"} {
		if strings.Contains(lowerBody, strings.ToLower(prohibited)) {
			t.Fatalf("canonical detail echoed prohibited value %q: %s", prohibited, body)
		}
	}
	var detail store.LyricsSourceReviewDetail
	if err := json.Unmarshal(body, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Analysis == nil || detail.Analysis.RubyGeneratorVersion != "sekaipedia-ruby-kana-v1" ||
		len(detail.Analysis.Performers) != 0 || len(detail.Analysis.ExtractedLines) != 1 ||
		detail.Analysis.ExtractedLines[0].Japanese != englishLyric ||
		len(detail.Analysis.ExtractedLines[0].Segments[0].PerformerIDs) != 0 ||
		len(detail.Analysis.ExtractedLines[0].TrailingPerformerIDs) != 0 {
		t.Fatalf("canonical detail=%+v", detail.Analysis)
	}

	const arbitraryGenerator = "provider-secret-romanizer-v9"
	if _, err := h.db.Exec(`UPDATE lyrics_source_analyses SET ruby_generator_version=?
		WHERE analysis_id=(SELECT analysis_id FROM lyrics_source_review_items WHERE review_id=?)`, arbitraryGenerator, reviewID); err != nil {
		t.Fatal(err)
	}
	errorResponse := authorizedRequest(t, h, http.MethodGet,
		"/api/admin/lyrics-source-reviews/detail?reviewId="+strconv.FormatInt(reviewID, 10), nil)
	errorBody, err := io.ReadAll(errorResponse.Body)
	errorResponse.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if errorResponse.StatusCode != http.StatusInternalServerError || strings.Contains(string(errorBody), arbitraryGenerator) ||
		!strings.Contains(string(errorBody), "internal_error") {
		t.Fatalf("unsafe generator status=%d body=%s", errorResponse.StatusCode, errorBody)
	}
}

func TestLyricsSourceReviewAPIOverallDecisionCompletesArtifactReviewOnce(t *testing.T) {
	h := setupLegacyAPI(t)
	reviewID := seedArtifactReviewAPI(t, h)
	response := authorizedRequest(t, h, http.MethodPut, "/api/admin/lyrics-source-reviews/decision", map[string]any{
		"reviewId": reviewID, "gate": "overall", "decision": "approved", "expectedVersion": 1,
		"idempotencyKey": "idempotency-overall-api-0001", "note": "",
	})
	defer response.Body.Close()
	var mutation lyricsSourceReviewMutationResponse
	if err := json.NewDecoder(response.Body).Decode(&mutation); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || mutation.State != "approved" || mutation.IdentityGate != "approved" ||
		mutation.SourceUseGate != "approved" || mutation.ParseGate != "approved" || mutation.Version != 2 || mutation.Replayed {
		t.Fatalf("overall mutation status=%d body=%+v", response.StatusCode, mutation)
	}
	detailResponse := authorizedRequest(t, h, http.MethodGet, "/api/admin/lyrics-source-reviews/detail?reviewId="+strconv.FormatInt(reviewID, 10), nil)
	defer detailResponse.Body.Close()
	var detail store.LyricsSourceReviewDetail
	if err := json.NewDecoder(detailResponse.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	if detailResponse.StatusCode != http.StatusOK || len(detail.Decisions) != 1 || detail.Decisions[0].Gate != "overall" ||
		detail.Decisions[0].Decision != "approved" || detail.Decisions[0].ExpectedVersion != 1 || detail.Decisions[0].ResultVersion != 2 {
		t.Fatalf("overall detail status=%d body=%+v", detailResponse.StatusCode, detail)
	}
	var authoritative int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM song_lyrics`).Scan(&authoritative); err != nil || authoritative != 0 {
		t.Fatalf("overall API decision created authoritative lyrics count=%d err=%v", authoritative, err)
	}
}

func TestLyricsSourceReviewAPIImportsOnlyApprovedArtifactAsOriginalOnlyDraft(t *testing.T) {
	h := setupLegacyAPI(t)
	reviewID := seedArtifactReviewAPI(t, h)
	pending := authorizedRequest(t, h, http.MethodPost, "/api/admin/lyrics-source-reviews/import", map[string]any{"reviewId": reviewID})
	defer pending.Body.Close()
	if pending.StatusCode != http.StatusUnprocessableEntity {
		body, _ := io.ReadAll(pending.Body)
		t.Fatalf("pending import status=%d body=%s", pending.StatusCode, body)
	}
	approve := authorizedRequest(t, h, http.MethodPut, "/api/admin/lyrics-source-reviews/decision", map[string]any{
		"reviewId": reviewID, "gate": "overall", "decision": "approved", "expectedVersion": 1,
		"idempotencyKey": "api-import-approved-0001", "note": "",
	})
	approve.Body.Close()
	if approve.StatusCode != http.StatusOK {
		t.Fatalf("approve status=%d", approve.StatusCode)
	}
	response := authorizedRequest(t, h, http.MethodPost, "/api/admin/lyrics-source-reviews/import", map[string]any{"reviewId": reviewID})
	defer response.Body.Close()
	var result lyricsSourceReviewImportResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("Cache-Control") != "no-store" || !result.Changed ||
		result.ReviewID != reviewID || result.Lyrics.Revision != 1 || result.Lyrics.Status != "draft" || len(result.Lyrics.Lines) != 1 ||
		result.Lyrics.Lines[0].Japanese != "合成歌詞" || result.Lyrics.Lines[0].Chinese != "" || result.Lyrics.Lines[0].English != "" {
		t.Fatalf("import status=%d headers=%v body=%+v", response.StatusCode, response.Header, result)
	}
	replay := authorizedRequest(t, h, http.MethodPost, "/api/admin/lyrics-source-reviews/import", map[string]any{"reviewId": reviewID})
	defer replay.Body.Close()
	var replayResult lyricsSourceReviewImportResponse
	if err := json.NewDecoder(replay.Body).Decode(&replayResult); err != nil {
		t.Fatal(err)
	}
	if replay.StatusCode != http.StatusOK || replayResult.Changed || replayResult.Lyrics.Revision != result.Lyrics.Revision {
		t.Fatalf("reimport status=%d body=%+v", replay.StatusCode, replayResult)
	}
}

func TestLyricsSourceReviewAPIBatchDecisionIsAtomicReplaySafeAndClosed(t *testing.T) {
	h := setupLegacyAPI(t)
	seed := seedArtifactReviewAPI(t, h)
	first := cloneArtifactReviewAPI(t, h, seed, 1)
	second := cloneArtifactReviewAPI(t, h, seed, 2)
	request := map[string]any{
		"gate": "overall", "decision": "approved",
		"items":          []map[string]any{{"reviewId": second, "expectedVersion": 1}, {"reviewId": first, "expectedVersion": 1}},
		"idempotencyKey": "batch-api-idempotency-0001", "note": "",
	}
	response := authorizedRequest(t, h, http.MethodPut, "/api/admin/lyrics-source-reviews/decision", request)
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	var responseObject map[string]json.RawMessage
	if err := json.Unmarshal(responseBody, &responseObject); err != nil {
		t.Fatal(err)
	}
	assertExactJSONKeys(t, responseObject, "items", "replayed")
	var rawItems []map[string]json.RawMessage
	if err := json.Unmarshal(responseObject["items"], &rawItems); err != nil {
		t.Fatal(err)
	}
	for _, item := range rawItems {
		assertExactJSONKeys(t, item, "reviewId", "state", "version")
	}
	var mutation lyricsSourceReviewBatchMutationResponse
	if err := json.Unmarshal(responseBody, &mutation); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("Cache-Control") != "no-store" || mutation.Replayed ||
		len(mutation.Items) != 2 || mutation.Items[0].ReviewID != first || mutation.Items[1].ReviewID != second {
		t.Fatalf("batch status=%d headers=%v body=%+v", response.StatusCode, response.Header, mutation)
	}
	for index, item := range mutation.Items {
		if item.State != "approved" || item.Version != 2 {
			t.Fatalf("batch item %d=%+v", index, item)
		}
	}
	request["items"] = []map[string]any{{"reviewId": first, "expectedVersion": 1}, {"reviewId": second, "expectedVersion": 1}}
	replay := authorizedRequest(t, h, http.MethodPut, "/api/admin/lyrics-source-reviews/decision", request)
	defer replay.Body.Close()
	var replayMutation lyricsSourceReviewBatchMutationResponse
	if err := json.NewDecoder(replay.Body).Decode(&replayMutation); err != nil {
		t.Fatal(err)
	}
	if replay.StatusCode != http.StatusOK || !replayMutation.Replayed || len(replayMutation.Items) != 2 ||
		replayMutation.Items[0].ReviewID != first || replayMutation.Items[1].ReviewID != second ||
		replayMutation.Items[0].State != "approved" || replayMutation.Items[1].State != "approved" ||
		replayMutation.Items[0].Version != 2 || replayMutation.Items[1].Version != 2 {
		t.Fatalf("batch replay status=%d body=%+v", replay.StatusCode, replayMutation)
	}
	request["note"] = "changed"
	conflict := authorizedRequest(t, h, http.MethodPut, "/api/admin/lyrics-source-reviews/decision", request)
	defer conflict.Body.Close()
	var conflictBody map[string]any
	if err := json.NewDecoder(conflict.Body).Decode(&conflictBody); err != nil {
		t.Fatal(err)
	}
	if conflict.StatusCode != http.StatusBadRequest || conflictBody["error"] != "invalid_request" {
		t.Fatalf("batch nonempty note status=%d body=%v", conflict.StatusCode, conflictBody)
	}
	mixed := authorizedRequest(t, h, http.MethodPut, "/api/admin/lyrics-source-reviews/decision", map[string]any{
		"reviewId": first, "expectedVersion": 1, "gate": "overall", "decision": "approved",
		"items":          []map[string]any{{"reviewId": second, "expectedVersion": 1}},
		"idempotencyKey": "batch-api-idempotency-0002", "note": "",
	})
	mixed.Body.Close()
	if mixed.StatusCode != http.StatusBadRequest {
		t.Fatalf("mixed single/batch status=%d", mixed.StatusCode)
	}
	unknown := authorizedRequest(t, h, http.MethodPut, "/api/admin/lyrics-source-reviews/decision", map[string]any{
		"gate": "overall", "decision": "approved",
		"items":          []map[string]any{{"reviewId": second, "expectedVersion": 1, "extra": true}},
		"idempotencyKey": "batch-api-idempotency-0003", "note": "",
	})
	unknown.Body.Close()
	if unknown.StatusCode != http.StatusBadRequest {
		t.Fatalf("batch nested unknown field status=%d", unknown.StatusCode)
	}
}

func TestLyricsSourceReviewAPISharedRouteIdempotencyKeyConflictsAcrossSingleAndBatch(t *testing.T) {
	t.Run("single key blocks batch", func(t *testing.T) {
		h := setupLegacyAPI(t)
		seed := seedArtifactReviewAPI(t, h)
		first := cloneArtifactReviewAPI(t, h, seed, 1)
		second := cloneArtifactReviewAPI(t, h, seed, 2)
		const sharedKey = "api-shared-route-key-0001"
		single := authorizedRequest(t, h, http.MethodPut, "/api/admin/lyrics-source-reviews/decision", map[string]any{
			"reviewId": first, "gate": "overall", "decision": "approved", "expectedVersion": 1,
			"idempotencyKey": sharedKey, "note": "",
		})
		single.Body.Close()
		if single.StatusCode != http.StatusOK {
			t.Fatalf("single status=%d", single.StatusCode)
		}
		batch := authorizedRequest(t, h, http.MethodPut, "/api/admin/lyrics-source-reviews/decision", map[string]any{
			"gate": "overall", "decision": "rejected",
			"items":          []map[string]any{{"reviewId": second, "expectedVersion": 1}},
			"idempotencyKey": sharedKey, "note": "",
		})
		defer batch.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(batch.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if batch.StatusCode != http.StatusConflict || body["error"] != "idempotency_conflict" {
			t.Fatalf("batch after single status=%d body=%v", batch.StatusCode, body)
		}
		var state string
		var version int64
		if err := h.db.QueryRow(`SELECT state,version FROM lyrics_source_review_items WHERE review_id=?`, second).Scan(&state, &version); err != nil || state != "pending" || version != 1 {
			t.Fatalf("second after conflict state=%q version=%d err=%v", state, version, err)
		}
	})

	t.Run("batch key blocks single", func(t *testing.T) {
		h := setupLegacyAPI(t)
		seed := seedArtifactReviewAPI(t, h)
		first := cloneArtifactReviewAPI(t, h, seed, 1)
		second := cloneArtifactReviewAPI(t, h, seed, 2)
		const sharedKey = "api-shared-route-key-0002"
		batch := authorizedRequest(t, h, http.MethodPut, "/api/admin/lyrics-source-reviews/decision", map[string]any{
			"gate": "overall", "decision": "approved",
			"items":          []map[string]any{{"reviewId": first, "expectedVersion": 1}},
			"idempotencyKey": sharedKey, "note": "",
		})
		batch.Body.Close()
		if batch.StatusCode != http.StatusOK {
			t.Fatalf("batch status=%d", batch.StatusCode)
		}
		single := authorizedRequest(t, h, http.MethodPut, "/api/admin/lyrics-source-reviews/decision", map[string]any{
			"reviewId": second, "gate": "overall", "decision": "rejected", "expectedVersion": 1,
			"idempotencyKey": sharedKey, "note": "",
		})
		defer single.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(single.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if single.StatusCode != http.StatusConflict || body["error"] != "idempotency_conflict" {
			t.Fatalf("single after batch status=%d body=%v", single.StatusCode, body)
		}
		var state string
		var version int64
		if err := h.db.QueryRow(`SELECT state,version FROM lyrics_source_review_items WHERE review_id=?`, second).Scan(&state, &version); err != nil || state != "pending" || version != 1 {
			t.Fatalf("second after conflict state=%q version=%d err=%v", state, version, err)
		}
	})
}

func TestLyricsSourceReviewAPIBatchValidatesBoundsAndRequiredFields(t *testing.T) {
	h := setupLegacyAPI(t)
	reviewID := seedArtifactReviewAPI(t, h)
	validItem := map[string]any{"reviewId": reviewID, "expectedVersion": 1}
	tooMany := make([]map[string]any, 101)
	for index := range tooMany {
		tooMany[index] = map[string]any{"reviewId": int64(index + 1), "expectedVersion": 1}
	}
	for name, body := range map[string]map[string]any{
		"empty":         {"gate": "overall", "decision": "approved", "items": []map[string]any{}, "idempotencyKey": "batch-api-bounds-0001", "note": ""},
		"too many":      {"gate": "overall", "decision": "approved", "items": tooMany, "idempotencyKey": "batch-api-bounds-0002", "note": ""},
		"duplicate":     {"gate": "overall", "decision": "approved", "items": []map[string]any{validItem, validItem}, "idempotencyKey": "batch-api-bounds-0003", "note": ""},
		"missing items": {"gate": "overall", "decision": "approved", "idempotencyKey": "batch-api-bounds-0004", "note": ""},
		"wrong gate":    {"gate": "identity", "decision": "approved", "items": []map[string]any{validItem}, "idempotencyKey": "batch-api-bounds-0005", "note": ""},
	} {
		t.Run(name, func(t *testing.T) {
			response := authorizedRequest(t, h, http.MethodPut, "/api/admin/lyrics-source-reviews/decision", body)
			defer response.Body.Close()
			var result map[string]any
			if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != http.StatusBadRequest || result["error"] != "invalid_request" {
				t.Fatalf("%s status=%d body=%v", name, response.StatusCode, result)
			}
		})
	}
}

func TestLyricsSourceReviewAPIBatchConflictIsBoundedAndRollsBack(t *testing.T) {
	h := setupLegacyAPI(t)
	seed := seedArtifactReviewAPI(t, h)
	first := cloneArtifactReviewAPI(t, h, seed, 1)
	second := cloneArtifactReviewAPI(t, h, seed, 2)
	finish := authorizedRequest(t, h, http.MethodPut, "/api/admin/lyrics-source-reviews/decision", map[string]any{
		"reviewId": second, "gate": "overall", "decision": "rejected", "expectedVersion": 1,
		"idempotencyKey": "single-api-idempotency-0001", "note": "",
	})
	finish.Body.Close()
	if finish.StatusCode != http.StatusOK {
		t.Fatalf("finish status=%d", finish.StatusCode)
	}
	response := authorizedRequest(t, h, http.MethodPut, "/api/admin/lyrics-source-reviews/decision", map[string]any{
		"gate": "overall", "decision": "approved",
		"items":          []map[string]any{{"reviewId": first, "expectedVersion": 1}, {"reviewId": second, "expectedVersion": 1}},
		"idempotencyKey": "batch-api-idempotency-0004", "note": "",
	})
	defer response.Body.Close()
	var body struct {
		Error     string                                  `json:"error"`
		Conflicts []store.LyricsSourceReviewBatchConflict `json:"conflicts"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusConflict || body.Error != "revision_conflict" || len(body.Conflicts) != 1 ||
		body.Conflicts[0].ReviewID != second || body.Conflicts[0].Reason != "not_pending" {
		t.Fatalf("batch conflict status=%d body=%+v", response.StatusCode, body)
	}
	var state string
	var version int64
	if err := h.db.QueryRow(`SELECT state,version FROM lyrics_source_review_items WHERE review_id=?`, first).Scan(&state, &version); err != nil || state != "pending" || version != 1 {
		t.Fatalf("first after conflict state=%q version=%d err=%v", state, version, err)
	}
	var ledger int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM lyrics_source_review_batch_idempotency`).Scan(&ledger); err != nil || ledger != 0 {
		t.Fatalf("ledger=%d err=%v", ledger, err)
	}
}

func TestLyricsSourceReviewAPINonemptyNotesAreRejectedBeforeWrites(t *testing.T) {
	for name, testCase := range map[string]struct {
		path string
		body func(t *testing.T, h *legacyAPIHarness) (map[string]any, []int64)
	}{
		"single overall": {
			path: "/api/admin/lyrics-source-reviews/decision",
			body: func(t *testing.T, h *legacyAPIHarness) (map[string]any, []int64) {
				reviewID := seedArtifactReviewAPI(t, h)
				return map[string]any{
					"reviewId": reviewID, "gate": "overall", "decision": "approved", "expectedVersion": 1,
					"idempotencyKey": "api-note-single-0001", "note": "forbidden",
				}, []int64{reviewID}
			},
		},
		"batch overall": {
			path: "/api/admin/lyrics-source-reviews/decision",
			body: func(t *testing.T, h *legacyAPIHarness) (map[string]any, []int64) {
				seed := seedArtifactReviewAPI(t, h)
				first := cloneArtifactReviewAPI(t, h, seed, 1)
				second := cloneArtifactReviewAPI(t, h, seed, 2)
				return map[string]any{
					"gate": "overall", "decision": "rejected",
					"items":          []map[string]any{{"reviewId": first, "expectedVersion": 1}, {"reviewId": second, "expectedVersion": 1}},
					"idempotencyKey": "api-note-batch-00001", "note": "forbidden",
				}, []int64{first, second}
			},
		},
		"candidate": {
			path: "/api/admin/lyrics-source-reviews/candidate-selection",
			body: func(t *testing.T, h *legacyAPIHarness) (map[string]any, []int64) {
				seedCandidateReviewAPIRows(t, h, 1)
				var reviewID int64
				if err := h.db.QueryRow(`SELECT review_id FROM lyrics_source_review_items WHERE kind='candidate_selection' ORDER BY review_id DESC LIMIT 1`).Scan(&reviewID); err != nil {
					t.Fatal(err)
				}
				return map[string]any{
					"reviewId": reviewID, "candidateIdentity": nil, "exclude": true, "expectedVersion": 1,
					"idempotencyKey": "api-note-candidate-1", "note": "forbidden",
				}, []int64{reviewID}
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := setupLegacyAPI(t)
			body, reviewIDs := testCase.body(t, h)
			before := lyricsSourceReviewAPIMutationCounts(t, h)
			response := authorizedRequest(t, h, http.MethodPut, testCase.path, body)
			defer response.Body.Close()
			var result map[string]any
			if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != http.StatusBadRequest || result["error"] != "invalid_request" {
				t.Fatalf("status=%d body=%v", response.StatusCode, result)
			}
			after := lyricsSourceReviewAPIMutationCounts(t, h)
			if after != before {
				t.Fatalf("mutation counts changed before=%+v after=%+v", before, after)
			}
			for _, reviewID := range reviewIDs {
				var state string
				var version int64
				if err := h.db.QueryRow(`SELECT state,version FROM lyrics_source_review_items WHERE review_id=?`, reviewID).Scan(&state, &version); err != nil || state != "pending" || version != 1 {
					t.Fatalf("review %d changed state=%q version=%d err=%v", reviewID, state, version, err)
				}
			}
		})
	}
}

type lyricsSourceReviewMutationCounts struct {
	decisions int
	batches   int
	audits    int
}

func lyricsSourceReviewAPIMutationCounts(t *testing.T, h *legacyAPIHarness) lyricsSourceReviewMutationCounts {
	t.Helper()
	var counts lyricsSourceReviewMutationCounts
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM lyrics_source_review_decisions`).Scan(&counts.decisions); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM lyrics_source_review_batch_idempotency`).Scan(&counts.batches); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM audit_log`).Scan(&counts.audits); err != nil {
		t.Fatal(err)
	}
	return counts
}

func TestLyricsSourceReviewAPIInvalidCandidateIsDeterministicBadRequest(t *testing.T) {
	h := setupLegacyAPI(t)
	response := authorizedRequest(t, h, http.MethodPut, "/api/admin/lyrics-source-reviews/candidate-selection", map[string]any{
		"reviewId": 1,
		"candidateIdentity": map[string]any{
			"pageId": 12, "revisionId": 34, "sha1": strings.Repeat("a", 40), "title": "合成試験曲",
			"canonicalUrl": "https://evil.example/wiki/Song?oldid=34", "categories": []string{},
		},
		"exclude": false, "expectedVersion": 1, "idempotencyKey": "idempotency-key-0001", "note": "",
	})
	defer response.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusBadRequest || body["error"] != "invalid_request" {
		t.Fatalf("invalid candidate status=%d body=%v", response.StatusCode, body)
	}
}

func TestLyricsSourceReviewAPIPaginatesWithoutDuplicates(t *testing.T) {
	h := setupLegacyAPI(t)
	seedCandidateReviewAPIRows(t, h, 5)
	seen := make(map[int64]struct{})
	cursor := ""
	for pageNumber := 0; ; pageNumber++ {
		path := "/api/admin/lyrics-source-reviews?limit=2"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		response := authorizedRequest(t, h, http.MethodGet, path, nil)
		if response.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			t.Fatalf("page %d status=%d body=%s", pageNumber, response.StatusCode, body)
		}
		var page struct {
			Items      []store.LyricsSourceReviewSummary `json:"items"`
			NextCursor string                            `json:"nextCursor"`
		}
		if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
			response.Body.Close()
			t.Fatal(err)
		}
		response.Body.Close()
		for _, item := range page.Items {
			if _, duplicate := seen[item.ReviewID]; duplicate {
				t.Fatalf("duplicate review %d", item.ReviewID)
			}
			seen[item.ReviewID] = struct{}{}
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(seen) != 5 {
		t.Fatalf("reviews=%d, want 5", len(seen))
	}
}

func seedArtifactReviewAPI(t *testing.T, h *legacyAPIHarness) int64 {
	t.Helper()
	identity := seedReviewCatalogAPI(t, h)
	const canonicalURL = "https://vocaloid.fandom.com/wiki/%E5%90%88%E6%88%90%E8%A9%A6%E9%A8%93%E6%9B%B2?oldid=34"
	wikitext := []byte("== Lyrics ==\n合成歌詞")
	mediaWikiSHA1 := fmt.Sprintf("%x", sha1.Sum(wikitext))
	rawSHA256 := fmt.Sprintf("%x", sha256.Sum256(wikitext))
	fetchedAt := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
	fetchedAtText := fetchedAt.Format(time.RFC3339Nano)
	evidenceID := lyricssource.MediaWikiRevisionAcquisitionEvidenceID(
		model.LyricsSourceProviderVocaloidFandom, "fetch:vocaloid-fandom:12", fetchedAtText, rawSHA256,
	)
	evidence := lyricssource.IndexEvidence{
		EvidenceID: evidenceID, SHA256: rawSHA256,
		Kind:     lyricssource.IndexEvidenceKindMediaWikiRevision,
		Provider: model.LyricsSourceProviderVocaloidFandom, Origin: model.LyricsSourceOriginVocaloidFandom,
		PageID: 12, RevisionID: 34, MediaWikiSHA1: mediaWikiSHA1, Title: "合成試験曲",
		CanonicalURL: canonicalURL, Categories: []string{"Songs"}, FetchedAt: fetchedAtText,
		Raw: append([]byte(nil), wikitext...), RawSHA256: rawSHA256,
	}
	candidate := lyricssource.Candidate{
		Provider: evidence.Provider, Origin: evidence.Origin, PageID: evidence.PageID, RevisionID: evidence.RevisionID,
		SHA1: evidence.MediaWikiSHA1, Title: evidence.Title, CanonicalURL: evidence.CanonicalURL,
		Categories: []string{"Songs"}, Section: "Lyrics", RenditionKey: "full-vocaloid",
		VersionReason:     model.LyricsSourceVersionReasonUntaggedFullOnly,
		IndexEvidenceRefs: []model.LyricsSourceIndexEvidenceRef{{EvidenceID: evidenceID, SHA256: rawSHA256}},
		IndexEvidence:     []lyricssource.IndexEvidence{evidence},
	}
	legacyCandidate := model.LyricsSourceCandidateIdentity{
		PageID: candidate.PageID, RevisionID: candidate.RevisionID, SHA1: candidate.SHA1,
		Title: candidate.Title, CanonicalURL: candidate.CanonicalURL, Categories: append([]string(nil), candidate.Categories...),
	}
	fixedIdentity := model.LyricsSourceFixedIdentity{
		Provider: candidate.Provider, Origin: candidate.Origin,
		PageID: candidate.PageID, RevisionID: candidate.RevisionID, SHA1: candidate.SHA1,
		Title: candidate.Title, CanonicalURL: candidate.CanonicalURL, FetchedAt: fetchedAtText,
		Categories: append([]string(nil), candidate.Categories...), Section: candidate.Section,
		RenditionKey: candidate.RenditionKey, VersionReason: candidate.VersionReason,
		IndexEvidenceRefs: append([]model.LyricsSourceIndexEvidenceRef(nil), candidate.IndexEvidenceRefs...),
	}
	job, _, err := h.store.EnqueueLyricsDiscoveryJob(context.Background(), store.EnqueueLyricsDiscoveryJobParams{
		Provider: model.LyricsSourceProviderVocaloidFandom, Kind: model.LyricsDiscoveryJobFetchRevision,
		Target: model.LyricsDiscoveryJobTarget{
			MusicID: 10, PageID: candidate.PageID, RevisionID: candidate.RevisionID, ExpectedSHA1: candidate.SHA1,
			CatalogFingerprint: identity.CatalogFingerprint, PolicyVersion: model.LyricsMatchingPolicyVersion,
			FixedCandidate: &legacyCandidate,
		},
		FixedCandidate: &candidate, MaxAttempts: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	leased, err := h.store.ClaimLyricsDiscoveryJob(context.Background(), store.LyricsDiscoveryJobLease{
		Owner: "api-fetch-worker", Duration: time.Minute, Kind: model.LyricsDiscoveryJobFetchRevision, Now: time.Now().UTC(),
	})
	if err != nil || leased.ID != job.ID {
		t.Fatalf("leased=%+v err=%v", leased, err)
	}
	review, err := h.store.CompleteLyricsFetch(context.Background(), store.CompleteLyricsFetchParams{
		JobID: leased.ID, LeaseOwner: leased.LeaseOwner, ExpectedVersion: leased.Version, CompletedAt: time.Now().UTC(),
		Fixed: lyricssource.FixedRevision{
			Provider: candidate.Provider, Origin: candidate.Origin,
			PageID: candidate.PageID, RevisionID: candidate.RevisionID, SHA1: candidate.SHA1, PageTitle: candidate.Title,
			CanonicalURL: candidate.CanonicalURL, Categories: append([]string(nil), candidate.Categories...),
			FetchedAt: fetchedAt, Wikitext: append([]byte(nil), wikitext...),
			Lines: []lyricssource.ExtractedLine{{Japanese: "合成歌詞"}},
			Extraction: lyricssource.Extraction{
				Version:    lyricssource.LyricsVersion{Kind: "vocaloid", Label: "Vocaloid Version"},
				Performers: []lyricssource.Performer{}, RubyGeneratorVersion: "kagome-ipadic-v1",
				Lines: []lyricssource.StructuredLine{{
					Japanese: "合成歌詞",
					Segments: []lyricssource.LyricsSegment{{
						Text: "合成歌詞", PerformerIDs: []string{}, Ruby: []lyricssource.RubySpan{{Text: "合成歌詞"}},
					}},
					TrailingPerformerIDs: []string{},
				}},
			},
			Section: candidate.Section, RenditionKey: candidate.RenditionKey, VersionReason: candidate.VersionReason,
			IndexEvidenceRefs: append([]model.LyricsSourceIndexEvidenceRef(nil), candidate.IndexEvidenceRefs...),
			IndexEvidence:     []lyricssource.IndexEvidence{evidence},
			FixedIdentities:   []model.LyricsSourceFixedIdentity{fixedIdentity},
		},
		Evidence: []model.LyricsSourceEvidence{{RuleID: "fixed", Gate: "identity", Outcome: "passed", Summary: "exact revision"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return review.ReviewID
}

func cloneArtifactReviewAPI(t *testing.T, h *legacyAPIHarness, sourceReviewID int64, ordinal int) int64 {
	t.Helper()
	var sourceAnalysisID int64
	if err := h.db.QueryRow(`SELECT analysis_id FROM lyrics_source_review_items WHERE review_id=?`, sourceReviewID).Scan(&sourceAnalysisID); err != nil {
		t.Fatal(err)
	}
	var sourceArtifactID int64
	if err := h.db.QueryRow(`SELECT artifact_id FROM lyrics_source_analyses WHERE analysis_id=?`, sourceAnalysisID).Scan(&sourceArtifactID); err != nil {
		t.Fatal(err)
	}
	var sourceType, sourceOrigin, pageTitle, canonicalURL, mediaWikiSHA1, categoriesJSON, provider string
	var rawWikitext []byte
	var rawByteCount, firstFetchedAt, firstCreatingJobID, createdAt int64
	if err := h.db.QueryRow(`SELECT source_type,source_origin,page_title,canonical_revision_url,mediawiki_sha1,categories_json,
		raw_wikitext,raw_byte_count,first_fetched_at,first_creating_job_id,created_at,provider
		FROM lyrics_source_artifacts WHERE artifact_id=?`, sourceArtifactID).Scan(
		&sourceType, &sourceOrigin, &pageTitle, &canonicalURL, &mediaWikiSHA1, &categoriesJSON, &rawWikitext,
		&rawByteCount, &firstFetchedAt, &firstCreatingJobID, &createdAt, &provider); err != nil {
		t.Fatal(err)
	}
	pageID, revisionID := 100+ordinal, 200+ordinal
	canonicalURL = fmt.Sprintf("https://vocaloid.fandom.com/wiki/%s?oldid=%d", url.PathEscape(strings.ReplaceAll(pageTitle, " ", "_")), revisionID)
	artifactResult, err := h.db.Exec(`INSERT INTO lyrics_source_artifacts
		(source_type,source_origin,page_id,revision_id,page_title,canonical_revision_url,mediawiki_sha1,categories_json,
		 raw_wikitext,raw_byte_count,raw_wikitext_sha256,artifact_sha256,first_fetched_at,first_creating_job_id,created_at,
		 provider,provenance_status)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, sourceType, sourceOrigin, pageID, revisionID, pageTitle, canonicalURL,
		mediaWikiSHA1, categoriesJSON, rawWikitext, rawByteCount, strings.Repeat(string("bcdef"[ordinal%5]), 64),
		strings.Repeat(string("cdefab"[ordinal%6]), 64), firstFetchedAt, firstCreatingJobID+int64(ordinal),
		createdAt+int64(ordinal), provider, "rebuild_required")
	if err != nil {
		t.Fatal(err)
	}
	artifactID, err := artifactResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	var musicID int
	var fingerprint, matchingPolicy, restrictionPolicy, extractorVersion, matchOutcome, restrictionOutcome, extractionOutcome string
	var matchingEvidence, restrictionRules, selectedVersion, performers, rubyGeneratorVersion, extractedLines string
	var extractedLineCount, creatingJobID int64
	if err := h.db.QueryRow(`SELECT music_id,catalog_fingerprint,matching_policy_version,restriction_policy_version,extractor_version,
		match_outcome,restriction_outcome,extraction_outcome,matching_evidence_json,restriction_rule_ids_json,selected_version_json,
		performers_json,ruby_generator_version,extracted_lines_json,extracted_line_count,creating_job_id
		FROM lyrics_source_analyses WHERE analysis_id=?`, sourceAnalysisID).Scan(
		&musicID, &fingerprint, &matchingPolicy, &restrictionPolicy, &extractorVersion, &matchOutcome, &restrictionOutcome,
		&extractionOutcome, &matchingEvidence, &restrictionRules, &selectedVersion, &performers, &rubyGeneratorVersion,
		&extractedLines, &extractedLineCount, &creatingJobID); err != nil {
		t.Fatal(err)
	}
	analysisResult, err := h.db.Exec(`INSERT INTO lyrics_source_analyses
		(analysis_key,artifact_id,music_id,catalog_fingerprint,matching_policy_version,restriction_policy_version,extractor_version,
		 match_outcome,restriction_outcome,extraction_outcome,matching_evidence_json,restriction_rule_ids_json,selected_version_json,
		 performers_json,ruby_generator_version,extracted_lines_json,extracted_line_count,extracted_lines_sha256,analysis_sha256,
		 creating_job_id,created_at,provider)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, strings.Repeat(string("defabc"[ordinal%6]), 64), artifactID, musicID,
		fingerprint, matchingPolicy, restrictionPolicy, extractorVersion, matchOutcome, restrictionOutcome, extractionOutcome,
		matchingEvidence, restrictionRules, selectedVersion, performers, rubyGeneratorVersion, extractedLines, extractedLineCount,
		strings.Repeat(string("efabcd"[ordinal%6]), 64), strings.Repeat(string("fabcde"[ordinal%6]), 64),
		creatingJobID+int64(ordinal), time.Now().UTC().Add(-time.Second).UnixMilli(), provider)
	if err != nil {
		t.Fatal(err)
	}
	analysisID, err := analysisResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	var reviewPolicy string
	if err := h.db.QueryRow(`SELECT review_policy_version FROM lyrics_source_review_items WHERE review_id=?`, sourceReviewID).Scan(&reviewPolicy); err != nil {
		t.Fatal(err)
	}
	reviewResult, err := h.db.Exec(`INSERT INTO lyrics_source_review_items
		(domain_key,kind,analysis_id,music_id,catalog_fingerprint,review_policy_version,reason_code,evidence_json,state,
		 identity_gate,source_use_gate,parse_gate,version,priority,created_at,updated_at,provider)
		VALUES (?,'artifact_review',?,?,?,?,?,'{}','pending','pending','pending','pending',1,0,?,?,?)`,
		strings.Repeat(string("abcdef"[ordinal%6]), 64), analysisID, musicID, fingerprint, reviewPolicy,
		fmt.Sprintf("artifact_clone_%d", ordinal), time.Now().UTC().Add(-time.Second).UnixMilli(),
		time.Now().UTC().Add(-time.Second).UnixMilli(), provider)
	if err != nil {
		t.Fatal(err)
	}
	reviewID, err := reviewResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return reviewID
}

func seedReviewCatalogAPI(t *testing.T, h *legacyAPIHarness) store.CatalogMusicIdentity {
	t.Helper()
	if err := h.store.UpsertMusicCatalog([]store.MusicCatalogRecord{{MusicID: 10, JapaneseTitle: "合成試験曲",
		ProducerMetadata: "制作者", Lyricist: "制作者", Composer: "制作者", Arranger: "制作者",
		LyricsVersion: "full", LyricsVersionKnown: true,
		Vocals: []model.CatalogVocalSignal{{VocalID: 1, VocalType: "original_song", CharacterType: "game_character", CharacterID: 1}}}}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.UpsertPerformerCatalog([]store.PerformerCatalogRecord{{PerformerID: 1, JapaneseName: "星乃 一歌"}}); err != nil {
		t.Fatal(err)
	}
	identity, err := h.store.CatalogMusicIdentity(10)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func seedCandidateReviewAPIRows(t *testing.T, h *legacyAPIHarness, count int) {
	t.Helper()
	identity := seedReviewCatalogAPI(t, h)
	now := time.Now().UTC().UnixMilli()
	for index := 0; index < count; index++ {
		domainKey := strings.Repeat(string("abcdef"[index%6]), 64)
		if _, err := h.db.Exec(`INSERT INTO lyrics_source_review_items
			(domain_key,kind,analysis_id,music_id,catalog_fingerprint,review_policy_version,reason_code,evidence_json,state,
			 identity_gate,source_use_gate,parse_gate,version,priority,created_at,updated_at)
			VALUES (?,'candidate_selection',NULL,?,?,? ,?,'{"candidates":[]}','pending','not_applicable','not_applicable','not_applicable',1,?,?,?)`,
			domainKey, 10, identity.CatalogFingerprint, model.LyricsReviewPolicyVersion, "candidate_"+strconv.Itoa(index), index%2, now+int64(index), now+int64(index)); err != nil {
			t.Fatal(err)
		}
	}
}

func assertExactJSONKeys(t *testing.T, object map[string]json.RawMessage, expected ...string) {
	t.Helper()
	actual := make([]string, 0, len(object))
	for key := range object {
		actual = append(actual, key)
	}
	sort.Strings(actual)
	sort.Strings(expected)
	if strings.Join(actual, ",") != strings.Join(expected, ",") {
		t.Fatalf("keys=%v, want %v", actual, expected)
	}
}
