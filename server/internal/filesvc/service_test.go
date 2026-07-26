package filesvc

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
	initialProjection := svc.Status()
	if initialProjection.Generation != 1 || initialProjection.Pending || initialProjection.LastError != "" {
		t.Fatalf("initial projection status = %+v", initialProjection)
	}
	return svc
}

func TestInitialProjectionRetriesAfterFailOnce(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "projection-retry.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s := store.New(database)
	es := store.NewEventStore(database)
	svc := New(s, es, files.NewGenerator(s, es, ""))
	svc.retryMin = 5 * time.Millisecond
	svc.retryMax = 10 * time.Millisecond
	rebuild := svc.rebuildAssetsFn
	var calls atomic.Int32
	svc.rebuildAssetsFn = func() error {
		if calls.Add(1) == 1 {
			return errors.New("internal projection detail")
		}
		return rebuild()
	}
	svc.Start()
	defer func() {
		svc.Stop()
		svc.Wait()
	}()
	deadline := time.Now().Add(time.Second)
	for svc.Status().Generation == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if status := svc.Status(); status.Generation != 1 || status.Pending || status.LastError != "" || calls.Load() != 2 {
		t.Fatalf("recovered status=%+v calls=%d", status, calls.Load())
	}
}

func TestStartReturnsWhileInitialProjectionIsBlockedAndStopWaits(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "projection-blocked.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s := store.New(database)
	es := store.NewEventStore(database)
	svc := New(s, es, files.NewGenerator(s, es, ""))
	entered := make(chan struct{})
	release := make(chan struct{})
	svc.rebuildAssetsFn = func() error {
		close(entered)
		<-release
		return nil
	}

	startReturned := make(chan struct{})
	go func() {
		svc.Start()
		close(startReturned)
	}()
	select {
	case <-startReturned:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Start blocked on initial projection")
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("initial projection worker did not start")
	}
	if status := svc.Status(); !status.Pending || status.Generation != 0 {
		t.Fatalf("blocked initial status = %+v", status)
	}

	svc.Stop()
	waitReturned := make(chan struct{})
	go func() {
		svc.Wait()
		close(waitReturned)
	}()
	select {
	case <-waitReturned:
		t.Fatal("Wait returned while the tracked projection was still running")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	select {
	case <-waitReturned:
	case <-time.After(time.Second):
		t.Fatal("Wait did not return after initial projection completed")
	}
}

func TestProjectionRetriesFailOnceAfterSuccessUntilConverged(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "projection-post-start-retry.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s := store.New(database)
	es := store.NewEventStore(database)
	svc := New(s, es, files.NewGenerator(s, es, ""))
	svc.debounce = time.Millisecond
	svc.retryMin = 5 * time.Millisecond
	svc.retryMax = 10 * time.Millisecond
	rebuild := svc.rebuildAssetsFn
	var calls atomic.Int32
	svc.rebuildAssetsFn = func() error {
		call := calls.Add(1)
		if call == 2 {
			return errors.New("post-start failure")
		}
		return rebuild()
	}
	svc.Start()
	defer func() {
		svc.Stop()
		svc.Wait()
	}()
	waitForProjection(t, svc, func(status ProjectionStatus) bool { return status.Generation == 1 && !status.Pending })
	svc.Trigger()
	waitForProjection(t, svc, func(status ProjectionStatus) bool { return status.Generation == 2 && !status.Pending })
	if calls.Load() != 3 || svc.Status().LastError != "" {
		t.Fatalf("post-start convergence status=%+v calls=%d", svc.Status(), calls.Load())
	}
}

func TestPersistentPostStartProjectionFailureStopsRetries(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "projection-stop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s := store.New(database)
	es := store.NewEventStore(database)
	svc := New(s, es, files.NewGenerator(s, es, ""))
	svc.retryMin = 5 * time.Millisecond
	svc.retryMax = 10 * time.Millisecond
	svc.debounce = time.Millisecond
	rebuild := svc.rebuildAssetsFn
	var calls atomic.Int32
	svc.rebuildAssetsFn = func() error {
		if calls.Add(1) == 1 {
			return rebuild()
		}
		return errors.New("sensitive sqlite failure")
	}
	svc.Start()
	waitForProjection(t, svc, func(status ProjectionStatus) bool { return status.Generation == 1 && !status.Pending })
	svc.Trigger()
	deadline := time.Now().Add(time.Second)
	for calls.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if calls.Load() < 3 {
		t.Fatalf("projection did not retry: calls=%d", calls.Load())
	}
	svc.Stop()
	svc.Stop()
	svc.Wait()
	stoppedCalls := calls.Load()
	time.Sleep(4 * svc.retryMax)
	if calls.Load() != stoppedCalls {
		t.Fatalf("projection ran after stop: before=%d after=%d", stoppedCalls, calls.Load())
	}
	status := svc.Status()
	if status.Generation != 1 || !status.Pending || status.LastError != "projection_generation_failed" || status.LastError == "sensitive sqlite failure" {
		t.Fatalf("persistent failure status = %+v", status)
	}
}

func waitForProjection(t *testing.T, svc *Service, ready func(ProjectionStatus) bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if status := svc.Status(); ready(status) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("projection did not reach expected state: %+v", svc.Status())
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
	localized, err := http.Get(ts.URL + "/files/v2/zh-CN/translation/eventStory/event_42.json")
	if err != nil {
		t.Fatal(err)
	}
	defer localized.Body.Close()
	localizedBody, err := io.ReadAll(localized.Body)
	if err != nil || localized.StatusCode != http.StatusOK {
		t.Fatalf("localized event status=%d err=%v", localized.StatusCode, err)
	}
	if bytes.Contains(localizedBody, []byte(`"revision"`)) {
		t.Fatalf("authenticated event revision leaked into public bytes: %s", localizedBody)
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
	saved, _, err := s.SaveImportedLyricsMutation(model.SongLyrics{
		MusicID: 10, Revision: 0, Attribution: "MoeSeka translation team",
		SourceNote: "must stay private", SourceURL: "https://private.invalid/wiki", LicenseNote: "private",
		SourcePageID: 123, SourceRevisionID: 456, SourceSHA1: "0123456789abcdef0123456789abcdef01234567",
		SourceFetchedAt: "2026-07-23T00:00:00Z",
		Lines: []model.LyricLine{{
			ID: "wiki-123-456-1", Order: 0, Japanese: "歌う", Chinese: "歌唱", English: "Sings",
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
	if !bytes.Contains(detail, []byte(`"id": "line-1"`)) || bytes.Contains(detail, []byte(`wiki-123-456-1`)) {
		t.Fatalf("public lyrics did not replace private line identity: %s", detail)
	}
	for _, locale := range model.SupportedLocales {
		localized, localizedETag, localizedStatus := readAsset("/files/v2/" + locale + "/translation/lyrics/music_10.json")
		if localizedStatus != http.StatusOK || !bytes.Equal(localized, detail) || localizedETag != etag {
			t.Fatalf("localized lyrics %s status=%d etag=%q body=%s", locale, localizedStatus, localizedETag, localized)
		}
	}
	for _, privateField := range []string{"status", "updatedBy", "sourceNote", "sourceUrl", "licenseNote", "sourcePageId", "sourceRevisionId", "sourceSha1", "sourceFetchedAt", "must stay private", "private.invalid", "0123456789abcdef0123456789abcdef01234567"} {
		if bytes.Contains(detail, []byte(privateField)) {
			t.Fatalf("public lyrics leaked %q: %s", privateField, detail)
		}
	}

	var storedPayload string
	if err := database.QueryRow(`SELECT payload_json FROM song_lyrics_publications WHERE music_id=10`).Scan(&storedPayload); err != nil {
		t.Fatal(err)
	}
	legacyPayload := strings.Replace(storedPayload, `"id":"line-1"`, `"id":"wiki-123-456-1"`, 1)
	if legacyPayload == storedPayload {
		t.Fatalf("could not construct legacy publication payload: %s", storedPayload)
	}
	if _, err := database.Exec(`UPDATE song_lyrics_publications SET payload_json=? WHERE music_id=10`, legacyPayload); err != nil {
		t.Fatal(err)
	}
	svc.Rebuild()
	legacyDetail, legacyETag, status := readAsset("/files/translation/lyrics/music_10.json")
	if status != http.StatusOK || !bytes.Equal(legacyDetail, detail) || legacyETag != etag || bytes.Contains(legacyDetail, []byte(`wiki-123-456-1`)) {
		t.Fatalf("legacy stored identity changed public bytes: status=%d etag=%q body=%s", status, legacyETag, legacyDetail)
	}
	for _, locale := range model.SupportedLocales {
		localized, localizedETag, localizedStatus := readAsset("/files/v2/" + locale + "/translation/lyrics/music_10.json")
		if localizedStatus != http.StatusOK || !bytes.Equal(localized, detail) || localizedETag != etag {
			t.Fatalf("legacy localized lyrics %s status=%d etag=%q body=%s", locale, localizedStatus, localizedETag, localized)
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
	failedProjection := svc.Status()
	if failedProjection.Generation != 2 || !failedProjection.Pending || failedProjection.LastError != "projection_generation_failed" {
		t.Fatalf("failed projection status = %+v", failedProjection)
	}
	if _, err := s.UnpublishLyrics(10, saved.Revision, "admin"); err != nil {
		t.Fatal(err)
	}
	svc.Rebuild()
	recoveredProjection := svc.Status()
	if recoveredProjection.Generation != 3 || recoveredProjection.Pending || recoveredProjection.LastError != "" || recoveredProjection.LastSuccessAt == "" {
		t.Fatalf("recovered projection status = %+v", recoveredProjection)
	}
	for _, path := range []string{
		"/files/translation/lyrics/music_10.json",
		"/files/v2/zh-CN/translation/lyrics/music_10.json",
		"/files/v2/en-US/translation/lyrics/music_10.json",
	} {
		if _, _, status := readAsset(path); status != http.StatusNotFound {
			t.Fatalf("unpublished mirror %s status=%d", path, status)
		}
	}
}

func TestRebuildWaitsForContentBoundary(t *testing.T) {
	svc := setupLegacyFileService(t)
	release := svc.store.LockContentShared()
	done := make(chan struct{})
	go func() {
		svc.Rebuild()
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("rebuild crossed an active content operation")
	case <-time.After(30 * time.Millisecond):
	}
	release()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("rebuild did not resume after content operation")
	}
}
