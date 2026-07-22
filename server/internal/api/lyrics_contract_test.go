package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"moesekai/server/internal/auth"
	"moesekai/server/internal/lyricssource"
	"moesekai/server/internal/model"
	"moesekai/server/internal/store"
)

type fakeLyricsSource struct {
	candidates []lyricssource.Candidate
	preview    lyricssource.Preview
	err        error
}

func (f fakeLyricsSource) Search(context.Context, lyricssource.MusicIdentity) ([]lyricssource.Candidate, error) {
	return f.candidates, f.err
}

func (f fakeLyricsSource) Preview(context.Context, lyricssource.MusicIdentity, int, int) (lyricssource.Preview, error) {
	return f.preview, f.err
}

func seedLyricsCatalog(t *testing.T, h *legacyAPIHarness) {
	t.Helper()
	if err := h.store.UpsertMusicCatalog([]store.MusicCatalogRecord{
		{MusicID: 10, JapaneseTitle: "新曲", ChineseTitle: "新歌", EnglishTitle: "New Song", IsNewlyWrittenMusic: true},
		{MusicID: 20, JapaneseTitle: "旧曲", IsNewlyWrittenMusic: false},
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.UpsertPerformerCatalog([]store.PerformerCatalogRecord{
		{PerformerID: 1, JapaneseName: "初音ミク", ChineseName: "初音未来", EnglishName: "Hatsune Miku"},
	}); err != nil {
		t.Fatal(err)
	}
}

func apiLyrics() model.SongLyrics {
	return model.SongLyrics{
		MusicID: 10, Revision: 0, Status: "draft", Attribution: "MoeSeka translation team", SourceNote: "manual",
		Lines: []model.LyricLine{{
			ID: "line-1", Order: 0, Japanese: "初音歌う", Chinese: "初音歌唱", English: "Miku sings",
			Segments: []model.LyricSegment{{Text: "初音", PerformerIDs: []int{1}}, {Text: "歌う", PerformerIDs: []int{1}}},
		}},
	}
}

func TestLyricsAPIContractAndRBAC(t *testing.T) {
	h := setupLegacyAPI(t)
	seedLyricsCatalog(t, h)

	catalog := authorizedRequest(t, h, http.MethodGet, "/api/catalog/music", nil)
	defer catalog.Body.Close()
	var catalogResult model.CatalogMusicResponse
	if err := json.NewDecoder(catalog.Body).Decode(&catalogResult); err != nil {
		t.Fatal(err)
	}
	if len(catalogResult.Items) != 1 || catalogResult.Items[0].MusicID != 10 {
		t.Fatalf("default catalog = %+v", catalogResult)
	}
	characters := authorizedRequest(t, h, http.MethodGet, "/api/catalog/characters", nil)
	defer characters.Body.Close()
	var characterResult model.CatalogPerformerResponse
	if err := json.NewDecoder(characters.Body).Decode(&characterResult); err != nil {
		t.Fatal(err)
	}
	if len(characterResult.Items) != 1 || characterResult.Items[0].PerformerID != 1 {
		t.Fatalf("character catalog = %+v", characterResult)
	}

	createEditor := authorizedRequest(t, h, http.MethodPost, "/api/admin/users", map[string]string{
		"username": "editor", "password": "pw", "role": auth.RoleEditor,
	})
	createEditor.Body.Close()
	login := doJSON(t, http.MethodPost, h.server.URL+"/api/auth/login", "", map[string]string{"username": "editor", "password": "pw"})
	defer login.Body.Close()
	var editorLogin struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(login.Body).Decode(&editorLogin); err != nil {
		t.Fatal(err)
	}

	save := doJSON(t, http.MethodPut, h.server.URL+"/api/lyrics/save", editorLogin.Token, apiLyrics())
	defer save.Body.Close()
	if save.StatusCode != http.StatusOK {
		t.Fatalf("editor save status = %d", save.StatusCode)
	}
	var saved model.SongLyrics
	if err := json.NewDecoder(save.Body).Decode(&saved); err != nil {
		t.Fatal(err)
	}
	if saved.Revision != 1 || saved.Status != "draft" {
		t.Fatalf("saved lyrics = %+v", saved)
	}

	editorPublish := doJSON(t, http.MethodPost, h.server.URL+"/api/lyrics/publish", editorLogin.Token, map[string]int{
		"musicId": 10, "revision": 1,
	})
	defer editorPublish.Body.Close()
	if editorPublish.StatusCode != http.StatusForbidden {
		t.Fatalf("editor publish status = %d", editorPublish.StatusCode)
	}

	adminPublish := authorizedRequest(t, h, http.MethodPost, "/api/lyrics/publish", map[string]int{
		"musicId": 10, "revision": 1,
	})
	defer adminPublish.Body.Close()
	if adminPublish.StatusCode != http.StatusOK {
		t.Fatalf("admin publish status = %d", adminPublish.StatusCode)
	}
	if err := json.NewDecoder(adminPublish.Body).Decode(&saved); err != nil {
		t.Fatal(err)
	}
	if saved.Status != "published" {
		t.Fatalf("publish result = %+v", saved)
	}

	list := authorizedRequest(t, h, http.MethodGet, "/api/lyrics", nil)
	defer list.Body.Close()
	var listed model.LyricsListResponse
	if err := json.NewDecoder(list.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Items) != 1 || listed.Items[0].Status != "published" {
		t.Fatalf("lyrics list = %+v", listed)
	}
}

func TestLyricsAPIConflictAndValidationShapes(t *testing.T) {
	h := setupLegacyAPI(t)
	seedLyricsCatalog(t, h)
	first := authorizedRequest(t, h, http.MethodPut, "/api/lyrics/save", apiLyrics())
	first.Body.Close()

	stale := apiLyrics()
	stale.Lines[0].English = "changed"
	conflict := authorizedRequest(t, h, http.MethodPut, "/api/lyrics/save", stale)
	defer conflict.Body.Close()
	if conflict.StatusCode != http.StatusConflict {
		t.Fatalf("conflict status = %d", conflict.StatusCode)
	}
	var conflictBody struct {
		Error   string           `json:"error"`
		Current model.SongLyrics `json:"current"`
	}
	if err := json.NewDecoder(conflict.Body).Decode(&conflictBody); err != nil {
		t.Fatal(err)
	}
	if conflictBody.Error != "revision_conflict" || conflictBody.Current.Revision != 1 {
		t.Fatalf("conflict body = %+v", conflictBody)
	}

	mismatch := apiLyrics()
	mismatch.MusicID = 20
	mismatch.Lines[0].Segments[1].Text = "wrong"
	invalid := authorizedRequest(t, h, http.MethodPut, "/api/lyrics/save", mismatch)
	defer invalid.Body.Close()
	if invalid.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("validation status = %d", invalid.StatusCode)
	}
	var validation struct {
		Error   string   `json:"error"`
		Details []string `json:"details"`
	}
	if err := json.NewDecoder(invalid.Body).Decode(&validation); err != nil {
		t.Fatal(err)
	}
	if validation.Error != "segment_mismatch" || len(validation.Details) == 0 {
		t.Fatalf("validation body = %+v", validation)
	}
}

func TestLyricsSourcePreviewContract(t *testing.T) {
	h := setupLegacyAPI(t)
	if err := h.store.UpsertMusicCatalog([]store.MusicCatalogRecord{{
		MusicID: 10, JapaneseTitle: "新曲", IsNewlyWrittenMusic: true, ProducerMetadata: "producer",
	}}); err != nil {
		t.Fatal(err)
	}
	h.api.lyricsSrc = fakeLyricsSource{
		candidates: []lyricssource.Candidate{{PageID: 12, Title: "新曲", RevisionID: 34, SHA1: "sha"}},
		preview: lyricssource.Preview{
			CanonicalURL: "https://example.invalid/source", PageID: 12, RevisionID: 34, SHA1: "sha",
			FetchedAt: "2026-07-22T12:00:00Z", Lines: []lyricssource.ExtractedLine{{Japanese: "歌詞"}},
		},
	}
	search := authorizedRequest(t, h, http.MethodGet, "/api/lyrics/source/search?musicId=10", nil)
	defer search.Body.Close()
	if search.StatusCode != http.StatusOK {
		t.Fatalf("source search status = %d", search.StatusCode)
	}
	var candidates struct {
		Items []lyricssource.Candidate `json:"items"`
	}
	if err := json.NewDecoder(search.Body).Decode(&candidates); err != nil {
		t.Fatal(err)
	}
	if len(candidates.Items) != 1 || candidates.Items[0].PageID != 12 {
		t.Fatalf("source candidates = %+v", candidates)
	}
	preview := authorizedRequest(t, h, http.MethodPost, "/api/lyrics/source/preview", map[string]int{
		"musicId": 10, "pageId": 12, "revisionId": 34,
	})
	defer preview.Body.Close()
	if preview.StatusCode != http.StatusOK {
		t.Fatalf("source preview status = %d", preview.StatusCode)
	}
	var result lyricssource.Preview
	if err := json.NewDecoder(preview.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.RevisionID != 34 || len(result.Lines) != 1 || result.Lines[0].Japanese != "歌詞" {
		t.Fatalf("source preview = %+v", result)
	}
	var auditCount int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE user='alice' AND action IN ('lyrics.source.search', 'lyrics.source.preview')`).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 2 {
		t.Fatalf("source audit count = %d", auditCount)
	}
}

func TestLyricsSourceFailureIsSanitized(t *testing.T) {
	h := setupLegacyAPI(t)
	if err := h.store.UpsertMusicCatalog([]store.MusicCatalogRecord{{
		MusicID: 10, JapaneseTitle: "新曲", IsNewlyWrittenMusic: true, ProducerMetadata: "producer",
	}}); err != nil {
		t.Fatal(err)
	}
	h.api.lyricsSrc = fakeLyricsSource{err: errors.New("secret upstream response")}
	response := authorizedRequest(t, h, http.MethodGet, "/api/lyrics/source/search?musicId=10", nil)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusBadGateway || strings.Contains(string(body), "secret upstream response") ||
		!strings.Contains(string(body), "source_unavailable") {
		t.Fatalf("sanitized source failure status=%d body=%s", response.StatusCode, body)
	}
}

func TestConsoleJSONPayloadIsBounded(t *testing.T) {
	h := setupLegacyAPI(t)
	body := `{"category":"cards","field":"prefix","key":"large","text":"` +
		strings.Repeat("x", maxJSONBodyBytes) + `","source":"human"}`
	request, err := http.NewRequest(http.MethodPut, h.server.URL+"/api/entry", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+h.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("oversized payload status = %d", response.StatusCode)
	}
}
