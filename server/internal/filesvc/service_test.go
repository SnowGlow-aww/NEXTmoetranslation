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
