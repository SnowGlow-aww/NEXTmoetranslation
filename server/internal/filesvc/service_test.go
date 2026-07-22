package filesvc

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"moesekai/server/internal/db"
	"moesekai/server/internal/files"
	"moesekai/server/internal/model"
	"moesekai/server/internal/store"
)

func setupLegacyFileService(t *testing.T) *Service {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "legacy-filesvc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	s := store.New(database)
	es := store.NewEventStore(database)
	if _, err := s.ImportCategory("cards", model.Category{
		"prefix": {
			"こんにちは":     {Text: "你好", Source: model.SourceCN, Ids: []string{"1", "2"}},
			"A & B < C": {Text: "甲 & 乙 < 丙", Source: model.SourceHuman},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := es.ImportOrdered(42, model.EventStoryMeta{
		Source: "official_cn", Version: "1.0", LastUpdated: 1700000000,
	}, []store.OrderedEpisode{{
		EpisodeNo: "1", ScenarioID: "scenario-1", Title: "标题 & <", TitleSource: model.SourceHuman,
		TalkKeys: []string{"zebra", "apple", "mango & lime"},
		TalkData: map[string]string{"zebra": "斑马", "apple": "苹果", "mango & lime": "芒果 & 青柠 <"},
	}}); err != nil {
		t.Fatal(err)
	}
	svc := New(s, es, files.NewGenerator(s, es, ""))
	svc.Rebuild()
	return svc
}

func TestLegacyPublicFileHTTPContract(t *testing.T) {
	svc := setupLegacyFileService(t)
	ts := httptest.NewServer(svc.Handler())
	defer ts.Close()

	tests := []struct {
		path    string
		fixture string
	}{
		{"/files/translation/cards.json", "cards.json"},
		{"/files/translation/cards.full.json", "cards.full.json"},
		{"/files/translation/eventStory/event_42.json", "event_42.json"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			resp, err := http.Get(ts.URL + tt.path)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d", resp.StatusCode)
			}
			got, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(filepath.Join("..", "files", "testdata", "legacy", tt.fixture))
			if err != nil {
				t.Fatal(err)
			}
			want = bytes.TrimSuffix(want, []byte("\n"))
			if !bytes.Equal(got, want) {
				t.Fatalf("body mismatch\ngot:\n%s\nwant:\n%s", got, want)
			}
			if got := resp.Header.Get("Content-Type"); got != "application/json; charset=utf-8" {
				t.Fatalf("Content-Type = %q", got)
			}
			if got := resp.Header.Get("Cache-Control"); got != "public, max-age=300, stale-while-revalidate=3600" {
				t.Fatalf("Cache-Control = %q", got)
			}
			if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
				t.Fatalf("CORS = %q", got)
			}
			etag := resp.Header.Get("ETag")
			if len(etag) != 34 || etag[0] != '"' || etag[len(etag)-1] != '"' {
				t.Fatalf("strong ETag = %q", etag)
			}

			req, _ := http.NewRequest(http.MethodGet, ts.URL+tt.path, nil)
			req.Header.Set("If-None-Match", etag)
			notModified, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			notModified.Body.Close()
			if notModified.StatusCode != http.StatusNotModified {
				t.Fatalf("conditional status = %d", notModified.StatusCode)
			}

			headReq, _ := http.NewRequest(http.MethodHead, ts.URL+tt.path, nil)
			headResp, err := http.DefaultClient.Do(headReq)
			if err != nil {
				t.Fatal(err)
			}
			headBody, _ := io.ReadAll(headResp.Body)
			headResp.Body.Close()
			if headResp.StatusCode != http.StatusOK || len(headBody) != 0 || headResp.Header.Get("ETag") != etag {
				t.Fatalf("HEAD status=%d body=%d etag=%q", headResp.StatusCode, len(headBody), headResp.Header.Get("ETag"))
			}
		})
	}
}

func TestPublishedLyricsFilesAndAtomicRebuild(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "lyrics-files.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s := store.New(database)
	es := store.NewEventStore(database)
	if err := s.UpsertMusicCatalog([]store.MusicCatalogRecord{{
		MusicID: 10, JapaneseTitle: "新曲", ChineseTitle: "新歌", EnglishTitle: "New Song", IsNewlyWrittenMusic: true,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertPerformerCatalog([]store.PerformerCatalogRecord{{PerformerID: 1, JapaneseName: "初音ミク"}}); err != nil {
		t.Fatal(err)
	}
	saved, err := s.SaveLyrics(model.SongLyrics{
		MusicID: 10, Revision: 0, Attribution: "MoeSeka translation team",
		SourceNote: "must stay private", SourceURL: "https://private.invalid/wiki", LicenseNote: "private",
		SourcePageID: 123, SourceRevisionID: 456, SourceSHA1: "private-sha",
		SourceFetchedAt: "2026-07-23T00:00:00Z",
		Lines: []model.LyricLine{{
			ID: "line-1", Order: 0, Japanese: "歌う", Chinese: "歌唱", English: "Sings",
			Segments: []model.LyricSegment{{Text: "歌う", PerformerIDs: []int{1}}},
		}},
	}, "editor")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishLyrics(10, saved.Revision); err != nil {
		t.Fatal(err)
	}
	svc := New(s, es, files.NewGenerator(s, es, ""))
	svc.Rebuild()

	readAsset := func(path string) ([]byte, string, int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rec, req)
		return rec.Body.Bytes(), rec.Header().Get("ETag"), rec.Code
	}
	index, _, status := readAsset("/files/translation/lyrics/index.json")
	if status != http.StatusOK || !bytes.Contains(index, []byte(`"musicId": 10`)) || !bytes.Contains(index, []byte(`"title"`)) {
		t.Fatalf("lyrics index status=%d body=%s", status, index)
	}
	detail, etag, status := readAsset("/files/translation/lyrics/music_10.json")
	if status != http.StatusOK || !bytes.Contains(detail, []byte(`"version": 1`)) {
		t.Fatalf("lyrics detail status=%d body=%s", status, detail)
	}
	if !bytes.Contains(detail, []byte(`"attribution": "MoeSeka translation team"`)) {
		t.Fatalf("public lyrics omitted attribution: %s", detail)
	}
	for _, locale := range model.SupportedLocales {
		localized, localizedETag, localizedStatus := readAsset("/files/v2/" + locale + "/translation/lyrics/music_10.json")
		if localizedStatus != http.StatusOK || !bytes.Equal(localized, detail) || localizedETag != etag {
			t.Fatalf("localized lyrics %s status=%d etag=%q body=%s", locale, localizedStatus, localizedETag, localized)
		}
	}
	for _, privateField := range []string{"status", "updatedBy", "sourceNote", "sourceUrl", "licenseNote", "sourcePageId", "sourceRevisionId", "sourceSha1", "sourceFetchedAt", "must stay private", "private.invalid", "private-sha"} {
		if bytes.Contains(detail, []byte(privateField)) {
			t.Fatalf("public lyrics leaked %q: %s", privateField, detail)
		}
	}

	if _, err := database.Exec(`UPDATE song_lyrics_publications SET payload_json='not-json' WHERE music_id=10`); err != nil {
		t.Fatal(err)
	}
	svc.Rebuild()
	afterFailure, afterETag, status := readAsset("/files/translation/lyrics/music_10.json")
	if status != http.StatusOK || !bytes.Equal(afterFailure, detail) || afterETag != etag {
		t.Fatalf("failed all-or-nothing rebuild changed published asset: status=%d etag=%q body=%s", status, afterETag, afterFailure)
	}
}
