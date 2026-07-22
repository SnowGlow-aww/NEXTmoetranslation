package searchindex

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"moesekai/server/internal/config"
	"moesekai/server/internal/db"
	"moesekai/server/internal/files"
	"moesekai/server/internal/filesvc"
	"moesekai/server/internal/model"
	"moesekai/server/internal/store"
)

func TestLegacySearchIndexGolden(t *testing.T) {
	documents := map[string]string{
		"/events.json":          `[{"id":10,"name":"Event JP"}]`,
		"/musics.json":          `[{"id":20,"title":"Song JP"}]`,
		"/cards.json":           `[{"id":30,"prefix":"Card JP","characterId":7}]`,
		"/gachas.json":          `[{"id":40,"name":"Gacha JP"}]`,
		"/mysekaiFixtures.json": `[{"id":50,"name":"Fixture JP"}]`,
		"/virtualLives.json":    `[{"id":60,"name":"Live JP"}]`,
		"/snowy_costumes.json":  `{"costumes":[{"id":70,"name":"Costume JP"},{"id":71,"name":"-"}]}`,
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		doc, ok := documents[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, doc)
	}))
	defer upstream.Close()

	database, err := db.Open(filepath.Join(t.TempDir(), "legacy-search.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s := store.New(database)
	es := store.NewEventStore(database)
	translations := map[string]model.Category{
		"events":      {"name": {"Event JP": {Text: "事件", Source: model.SourceCN}}},
		"music":       {"title": {"Song JP": {Text: "Song JP", Source: model.SourceCN}}},
		"cards":       {"prefix": {"Card JP": {Text: "卡片", Source: model.SourceHuman}}},
		"mysekai":     {"fixtureName": {"Fixture JP": {Text: "家具", Source: model.SourceCN}}},
		"virtualLive": {"name": {"Live JP": {Text: "演唱会", Source: model.SourceCN}}},
		"costumes":    {"name": {"Costume JP": {Text: "服装", Source: model.SourcePinned}}},
	}
	for category, data := range translations {
		if _, err := s.ImportCategory(category, data); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.New(database, "legacy-search-key")
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Set(config.KeyUpstreamJPMasterdataURL, upstream.URL); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Set(config.KeyUpstreamJPMasterdataFallbackURL, upstream.URL); err != nil {
		t.Fatal(err)
	}
	fileService := filesvc.New(s, es, files.NewGenerator(s, es, ""))
	builder := New(s, fileService, cfg, time.Hour, time.Hour)
	builder.build("legacy-contract")

	req := httptest.NewRequest(http.MethodGet, "/files/data/search-index.json", nil)
	rec := httptest.NewRecorder()
	fileService.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("search-index status = %d: %s", rec.Code, rec.Body.String())
	}
	want, err := os.ReadFile(filepath.Join("testdata", "legacy", "search-index.json"))
	if err != nil {
		t.Fatal(err)
	}
	want = bytes.TrimSuffix(want, []byte("\n"))
	if !bytes.Equal(rec.Body.Bytes(), want) {
		t.Fatalf("search-index mismatch\ngot:\n%s\nwant:\n%s", rec.Body.Bytes(), want)
	}
	if rec.Header().Get("Cache-Control") != "public, max-age=300, stale-while-revalidate=3600" {
		t.Fatalf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
}
