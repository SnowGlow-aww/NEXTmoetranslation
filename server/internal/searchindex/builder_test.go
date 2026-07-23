package searchindex

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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
	if _, err := s.UpdateEntryLocale("events", "name", "Event JP", "Event EN", model.SourceHuman, "editor", model.LocaleEnglish); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateEntryLocale("cards", "prefix", "Card JP", "Card EN", model.SourceHuman, "editor", model.LocaleEnglish); err != nil {
		t.Fatal(err)
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
	v2Req := httptest.NewRequest(http.MethodGet, "/files/v2/data/search-index.json", nil)
	v2Rec := httptest.NewRecorder()
	fileService.Handler().ServeHTTP(v2Rec, v2Req)
	if v2Rec.Code != http.StatusOK || !bytes.Contains(v2Rec.Body.Bytes(), []byte(`"n":"Event JP","g":"events","cn":"事件","en":"Event EN"`)) ||
		!bytes.Contains(v2Rec.Body.Bytes(), []byte(`"n":"Card JP","g":"cards","c":7,"cn":"卡片","en":"Card EN"`)) {
		t.Fatalf("multilingual search-index status=%d body=%s", v2Rec.Code, v2Rec.Body.String())
	}
}

func TestPartialSourceSuccessPreservesCompleteSearchIndex(t *testing.T) {
	documents := map[string]string{
		"/events.json": `[{"id":1,"name":"event"}]`, "/musics.json": `[{"id":2,"title":"music"}]`,
		"/cards.json": `[{"id":3,"prefix":"card"}]`, "/gachas.json": `[{"id":4,"name":"gacha"}]`,
		"/mysekaiFixtures.json": `[{"id":5,"name":"fixture"}]`, "/virtualLives.json": `[{"id":6,"name":"live"}]`,
		"/snowy_costumes.json": `{"costumes":[{"id":7,"name":"costume"}]}`,
	}
	var partial atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if partial.Load() && r.URL.Path != "/events.json" {
			http.Error(w, "failed", http.StatusBadGateway)
			return
		}
		_, _ = io.WriteString(w, documents[r.URL.Path])
	}))
	defer upstream.Close()
	database, err := db.Open(filepath.Join(t.TempDir(), "partial-search.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	dataStore := store.New(database)
	events := store.NewEventStore(database)
	cfg, _ := config.New(database, "search-master-key")
	_ = cfg.Set(config.KeyUpstreamJPMasterdataURL, upstream.URL)
	_ = cfg.Set(config.KeyUpstreamJPMasterdataFallbackURL, upstream.URL)
	fileService := filesvc.New(dataStore, events, files.NewGenerator(dataStore, events, ""))
	builder := New(dataStore, fileService, cfg, time.Hour, time.Hour)
	builder.build("complete")
	readIndex := func() []byte {
		recorder := httptest.NewRecorder()
		fileService.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/files/data/search-index.json", nil))
		return append([]byte(nil), recorder.Body.Bytes()...)
	}
	complete := readIndex()
	partial.Store(true)
	builder.build("partial")
	if got := readIndex(); !bytes.Equal(got, complete) {
		t.Fatalf("partial rebuild replaced complete index\ngot=%s\nwant=%s", got, complete)
	}
}

func TestNewerSearchBuildSupersedesBlockedOlderGeneration(t *testing.T) {
	var version atomic.Int32
	version.Store(1)
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := version.Load()
		once.Do(func() {
			close(started)
			<-release
		})
		name := fmt.Sprintf("version-%d", current)
		if r.URL.Path == "/snowy_costumes.json" {
			fmt.Fprintf(w, `{"costumes":[{"id":1,"name":%q}]}`, name)
			return
		}
		field := "name"
		if r.URL.Path == "/musics.json" {
			field = "title"
		} else if r.URL.Path == "/cards.json" {
			field = "prefix"
		}
		fmt.Fprintf(w, `[{"id":1,%q:%q}]`, field, name)
	}))
	defer upstream.Close()
	database, err := db.Open(filepath.Join(t.TempDir(), "generation-search.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	dataStore := store.New(database)
	events := store.NewEventStore(database)
	cfg, _ := config.New(database, "search-master-key")
	_ = cfg.Set(config.KeyUpstreamJPMasterdataURL, upstream.URL)
	_ = cfg.Set(config.KeyUpstreamJPMasterdataFallbackURL, upstream.URL)
	fileService := filesvc.New(dataStore, events, files.NewGenerator(dataStore, events, ""))
	builder := New(dataStore, fileService, cfg, time.Hour, time.Hour)
	oldDone := make(chan struct{})
	go func() { builder.build("old"); close(oldDone) }()
	<-started
	version.Store(2)
	newDone := make(chan struct{})
	go func() { builder.build("new"); close(newDone) }()
	close(release)
	<-oldDone
	<-newDone
	recorder := httptest.NewRecorder()
	fileService.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/files/data/search-index.json", nil))
	if recorder.Code != http.StatusOK || !bytes.Contains(recorder.Body.Bytes(), []byte("version-2")) || bytes.Contains(recorder.Body.Bytes(), []byte("version-1")) {
		t.Fatalf("published stale search generation: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	builder.mu.Lock()
	result := builder.lastResult
	builder.mu.Unlock()
	if !strings.Contains(result, "new") {
		t.Fatalf("last search result = %q", result)
	}
}
