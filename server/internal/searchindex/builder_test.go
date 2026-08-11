package searchindex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"moesekai/server/internal/httpx"
	"moesekai/server/internal/model"
	"moesekai/server/internal/store"
)

func TestMain(m *testing.M) {
	_ = os.Setenv("MOESEKAI_PRODUCTION", "false")
	_ = os.Setenv(httpx.UpstreamAllowInsecureLocalEnv, "true")
	os.Exit(m.Run())
}

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
	allowSingleRecordFixture(builder)
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
	allowSingleRecordFixture(builder)
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
	status := builder.Status()
	if !status.Ready || !status.Degraded || status.LastError != "search_index_build_failed" {
		t.Fatalf("partial build status = %+v", status)
	}
}

func TestInitialFailureRetriesQuicklyAndPublishes(t *testing.T) {
	documents := completeMasterdataDocuments()
	var healthy atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !healthy.Load() {
			http.Error(w, "transient", http.StatusBadGateway)
			return
		}
		_, _ = io.WriteString(w, documents[r.URL.Path])
	}))
	defer upstream.Close()
	database, err := db.Open(filepath.Join(t.TempDir(), "retry-search.db"))
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
	allowSingleRecordFixture(builder)
	builder.SetRetryBounds(10*time.Millisecond, 20*time.Millisecond)
	builder.Start()
	t.Cleanup(func() { builder.Stop(); builder.Wait() })

	waitForSearchStatus(t, builder, func(status Status) bool {
		return !status.Ready && status.Degraded && status.LastError == "search_index_build_failed"
	})
	healthy.Store(true)
	status := waitForSearchStatus(t, builder, func(status Status) bool { return status.Ready && !status.Degraded })
	if status.Source != "live" || status.Generation == 0 {
		t.Fatalf("recovered search status = %+v", status)
	}
	recorder := httptest.NewRecorder()
	fileService.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/files/data/search-index.json", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("retried search asset status = %d", recorder.Code)
	}
}

func TestValidatedLastKnownGoodIndexSurvivesRestart(t *testing.T) {
	documents := completeMasterdataDocuments()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, documents[r.URL.Path])
	}))
	directory := t.TempDir()
	database, err := db.Open(filepath.Join(directory, "cache-search.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	dataStore := store.New(database)
	events := store.NewEventStore(database)
	cfg, _ := config.New(database, "search-master-key")
	_ = cfg.Set(config.KeyUpstreamJPMasterdataURL, upstream.URL)
	_ = cfg.Set(config.KeyUpstreamJPMasterdataFallbackURL, upstream.URL)
	cachePath := filepath.Join(directory, "search-index-cache.json")
	firstFiles := filesvc.New(dataStore, events, files.NewGenerator(dataStore, events, ""))
	first := New(dataStore, firstFiles, cfg, time.Hour, time.Hour)
	allowSingleRecordFixture(first)
	first.SetCachePath(cachePath)
	first.build("prime-cache")
	if status := first.Status(); !status.Ready || status.Degraded {
		t.Fatalf("primed status = %+v", status)
	}
	upstream.Close()

	restartedFiles := filesvc.New(dataStore, events, files.NewGenerator(dataStore, events, ""))
	restarted := New(dataStore, restartedFiles, cfg, time.Hour, time.Hour)
	allowSingleRecordFixture(restarted)
	restarted.SetCachePath(cachePath)
	restarted.SetRetryBounds(time.Hour, time.Hour)
	restarted.Start()
	t.Cleanup(func() { restarted.Stop(); restarted.Wait() })
	status := restarted.Status()
	if !status.Ready || !status.Degraded || status.Source != "cache" {
		t.Fatalf("restart cache status = %+v", status)
	}
	recorder := httptest.NewRecorder()
	restartedFiles.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/files/data/search-index.json", nil))
	if recorder.Code != http.StatusOK || !bytes.Contains(recorder.Body.Bytes(), []byte(`"g":"events"`)) {
		t.Fatalf("cached search asset status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestCachedIndexRejectsIncompleteAndOversizedAssetSets(t *testing.T) {
	directory := t.TempDir()
	database, err := db.Open(filepath.Join(directory, "invalid-cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	dataStore := store.New(database)
	events := store.NewEventStore(database)
	fileService := filesvc.New(dataStore, events, files.NewGenerator(dataStore, events, ""))
	builder := New(dataStore, fileService, nil, time.Hour, time.Hour)
	allowSingleRecordFixture(builder)
	cachePath := filepath.Join(directory, "search-index-cache.json")
	builder.SetCachePath(cachePath)

	legacy, _ := json.Marshal([]Entry{{ID: 1, N: "event", G: "events"}})
	multilingual, _ := json.Marshal([]MultilingualEntry{{ID: 1, N: "event", G: "events"}})
	body, _ := json.Marshal(cacheEnvelope{Version: searchCacheVersion, Legacy: legacy, Multilingual: multilingual})
	if err := os.WriteFile(cachePath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := builder.loadCache(t.Context()); err == nil || !strings.Contains(err.Error(), "missing group") {
		t.Fatalf("incomplete cache error = %v", err)
	}
	recorder := httptest.NewRecorder()
	fileService.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/files/data/search-index.json", nil))
	if recorder.Code != http.StatusNotFound || builder.Status().Ready {
		t.Fatalf("incomplete cache published: status=%d search=%+v", recorder.Code, builder.Status())
	}

	if err := os.Truncate(cachePath, maxSearchCacheBytes+1); err != nil {
		t.Fatal(err)
	}
	if err := builder.loadCache(t.Context()); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized cache error = %v", err)
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
	allowSingleRecordFixture(builder)
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

func TestStopCancelsBlockedStartupBuildAndWaits(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(started) })
		<-r.Context().Done()
	}))
	defer upstream.Close()
	database, err := db.Open(filepath.Join(t.TempDir(), "cancel-search.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s := store.New(database)
	es := store.NewEventStore(database)
	cfg, _ := config.New(database, "search-master-key")
	_ = cfg.Set(config.KeyUpstreamJPMasterdataURL, upstream.URL)
	_ = cfg.Set(config.KeyUpstreamJPMasterdataFallbackURL, upstream.URL)
	builder := New(s, filesvc.New(s, es, files.NewGenerator(s, es, "")), cfg, time.Hour, time.Hour)
	allowSingleRecordFixture(builder)
	builder.Start()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("startup search build did not begin")
	}
	builder.Stop()
	builder.Stop()
	done := make(chan struct{})
	go func() {
		builder.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("search builder did not stop after request cancellation")
	}
}

func TestOneRecordHTTP200MasterdataIsRejected(t *testing.T) {
	documents := completeMasterdataDocuments()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, documents[r.URL.Path])
	}))
	defer upstream.Close()
	database, err := db.Open(filepath.Join(t.TempDir(), "truncated-search.db"))
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
	err = builder.buildContext(t.Context(), "truncated-200")
	if err == nil || !strings.Contains(err.Error(), "require at least 2") {
		t.Fatalf("one-record masterdata error = %v", err)
	}
	recorder := httptest.NewRecorder()
	fileService.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/files/data/search-index.json", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("truncated masterdata published asset: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestProductionCoverageFloorsRejectTinyColdStartSources(t *testing.T) {
	builder := &Builder{minimumSourceRecords: 2}
	builder.UseProductionCoverageFloors()
	coverage := cloneCoverage(productionMinimumSourceRecords)
	for source, minimum := range productionMinimumSourceRecords {
		if minimum <= 2 {
			t.Fatalf("production floor for %s = %d", source, minimum)
		}
		coverage[source] = minimum - 1
		if err := builder.validateCoverage(coverage); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("%s has %d records; require at least %d", source, minimum-1, minimum)) {
			t.Fatalf("production floor %s error = %v", source, err)
		}
		coverage[source] = minimum
	}
	if err := builder.validateCoverage(coverage); err != nil {
		t.Fatalf("minimum production coverage rejected: %v", err)
	}
}

func TestSuspiciousCoverageReductionDoesNotReplaceLKG(t *testing.T) {
	documents := countedMasterdataDocuments(3, "complete")
	var reduced atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		document := documents[r.URL.Path]
		if reduced.Load() && r.URL.Path == "/events.json" {
			document = countedMasterdataDocuments(2, "reduced")[r.URL.Path]
		}
		_, _ = io.WriteString(w, document)
	}))
	defer upstream.Close()
	directory := t.TempDir()
	database, err := db.Open(filepath.Join(directory, "lkg-search.db"))
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
	cachePath := filepath.Join(directory, "search-index-cache.json")
	builder.SetCachePath(cachePath)
	if err := builder.buildContext(t.Context(), "complete"); err != nil {
		t.Fatal(err)
	}
	readAsset := func() []byte {
		recorder := httptest.NewRecorder()
		fileService.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/files/data/search-index.json", nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("search asset status = %d", recorder.Code)
		}
		return append([]byte(nil), recorder.Body.Bytes()...)
	}
	completeAsset := readAsset()
	completeCache, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	reduced.Store(true)
	err = builder.buildContext(t.Context(), "reduced")
	if err == nil || !strings.Contains(err.Error(), "suspiciously reduced from 3 to 2") {
		t.Fatalf("reduced coverage error = %v", err)
	}
	if got := readAsset(); !bytes.Equal(got, completeAsset) {
		t.Fatal("reduced source replaced the in-memory LKG")
	}
	if got, readErr := os.ReadFile(cachePath); readErr != nil || !bytes.Equal(got, completeCache) {
		t.Fatalf("reduced source replaced persisted LKG: err=%v", readErr)
	}
}

func TestContentChangeSupersedesShortSearchSnapshotWithoutBlockingRestore(t *testing.T) {
	documents := countedMasterdataDocuments(2, "source")
	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	var once sync.Once
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() {
			close(requestStarted)
			<-releaseRequest
		})
		_, _ = io.WriteString(w, documents[r.URL.Path])
	}))
	defer upstream.Close()
	database, err := db.Open(filepath.Join(t.TempDir(), "restore-search.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	dataStore := store.New(database)
	if _, err := dataStore.ImportCategory("events", model.Category{"name": {
		"source-events-1": {Text: "old translation", Source: model.SourceHuman},
	}}); err != nil {
		t.Fatal(err)
	}
	events := store.NewEventStore(database)
	cfg, _ := config.New(database, "search-master-key")
	_ = cfg.Set(config.KeyUpstreamJPMasterdataURL, upstream.URL)
	_ = cfg.Set(config.KeyUpstreamJPMasterdataFallbackURL, upstream.URL)
	fileService := filesvc.New(dataStore, events, files.NewGenerator(dataStore, events, ""))
	builder := New(dataStore, fileService, cfg, time.Hour, time.Hour)
	buildDone := make(chan error, 1)
	go func() { buildDone <- builder.buildContext(t.Context(), "before-restore") }()
	<-requestStarted
	restoreEntered := make(chan struct{})
	restoreDone := make(chan error, 1)
	go func() {
		release, lockErr := dataStore.LockContentExclusiveContext(t.Context())
		if lockErr != nil {
			restoreDone <- lockErr
			return
		}
		close(restoreEntered)
		_, importErr := dataStore.ImportCategory("events", model.Category{"name": {
			"source-events-1": {Text: "restored translation", Source: model.SourceHuman},
		}})
		release()
		builder.Trigger()
		restoreDone <- importErr
	}()
	select {
	case <-restoreEntered:
	case <-time.After(time.Second):
		t.Fatal("remote search fetch kept the restore fence locked")
	}
	if err := <-restoreDone; err != nil {
		t.Fatal(err)
	}
	close(releaseRequest)
	if err := <-buildDone; !errors.Is(err, errBuildSuperseded) {
		t.Fatalf("pre-change search build error = %v", err)
	}
	recorder := httptest.NewRecorder()
	fileService.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/files/data/search-index.json", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("superseded search snapshot was published: %s", recorder.Body.String())
	}
	if err := builder.buildContext(t.Context(), "after-restore"); err != nil {
		t.Fatal(err)
	}
	recorder = httptest.NewRecorder()
	fileService.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/files/data/search-index.json", nil))
	if !bytes.Contains(recorder.Body.Bytes(), []byte("restored translation")) || bytes.Contains(recorder.Body.Bytes(), []byte("old translation")) {
		t.Fatalf("search rebuild did not use restored snapshot: %s", recorder.Body.String())
	}
}

func TestSupersededSearchCandidateCannotReplacePersistedLKG(t *testing.T) {
	directory := t.TempDir()
	cachePath := filepath.Join(directory, "search-index-cache.json")
	original := []byte(`{"version":1,"legacy":[],"multilingual":[]}`)
	if err := os.WriteFile(cachePath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	builder := &Builder{cachePath: cachePath, generation: 2}
	err := builder.persistCache(context.Background(), 1, []byte(`[{"id":1}]`), []byte(`[{"id":1}]`), map[string]int{"events.json": 1})
	if err != errBuildSuperseded {
		t.Fatalf("superseded persist error = %v", err)
	}
	got, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("superseded candidate replaced LKG: %s", got)
	}
}

func completeMasterdataDocuments() map[string]string {
	return map[string]string{
		"/events.json":          `[{"id":1,"name":"event"}]`,
		"/musics.json":          `[{"id":2,"title":"music"}]`,
		"/cards.json":           `[{"id":3,"prefix":"card"}]`,
		"/gachas.json":          `[{"id":4,"name":"gacha"}]`,
		"/mysekaiFixtures.json": `[{"id":5,"name":"fixture"}]`,
		"/virtualLives.json":    `[{"id":6,"name":"live"}]`,
		"/snowy_costumes.json":  `{"costumes":[{"id":7,"name":"costume"}]}`,
	}
}

func countedMasterdataDocuments(count int, prefix string) map[string]string {
	array := func(field, group string) string {
		items := make([]string, 0, count)
		for index := 1; index <= count; index++ {
			items = append(items, fmt.Sprintf(`{"id":%d,%q:%q}`, index, field, fmt.Sprintf("%s-%s-%d", prefix, group, index)))
		}
		return "[" + strings.Join(items, ",") + "]"
	}
	costumes := make([]string, 0, count)
	for index := 1; index <= count; index++ {
		costumes = append(costumes, fmt.Sprintf(`{"id":%d,"name":%q}`, index, fmt.Sprintf("%s-costumes-%d", prefix, index)))
	}
	return map[string]string{
		"/events.json": array("name", "events"), "/musics.json": array("title", "music"),
		"/cards.json": array("prefix", "cards"), "/gachas.json": array("name", "gacha"),
		"/mysekaiFixtures.json": array("name", "mysekai"), "/virtualLives.json": array("name", "live"),
		"/snowy_costumes.json": `{"costumes":[` + strings.Join(costumes, ",") + `]}`,
	}
}

func allowSingleRecordFixture(builder *Builder) {
	builder.minimumSourceRecords = 1
}

func waitForSearchStatus(t *testing.T, builder *Builder, predicate func(Status) bool) Status {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status := builder.Status()
		if predicate(status) {
			return status
		}
		time.Sleep(time.Millisecond)
	}
	status := builder.Status()
	t.Fatalf("search status did not converge: %+v", status)
	return status
}
