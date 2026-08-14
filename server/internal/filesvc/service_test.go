package filesvc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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

func TestSetAssetsDefensivelyCopiesBodies(t *testing.T) {
	svc := &Service{assets: map[string]asset{}}
	body := []byte(`{"version":1}`)
	svc.SetAsset("data/search-index.json", body, "application/json; charset=utf-8")
	body[2] = 'X'

	req := httptest.NewRequest(http.MethodGet, "/files/data/search-index.json", nil)
	resp := httptest.NewRecorder()
	svc.Handler().ServeHTTP(resp, req)
	if resp.Code != http.StatusOK || resp.Body.String() != `{"version":1}` {
		t.Fatalf("SetAsset retained a mutable caller buffer: status=%d body=%s", resp.Code, resp.Body.String())
	}
}

func TestEmbeddedPublicLyricsOverlaySurvivesDatabaseRebuild(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "embedded-public-lyrics.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s := store.New(database)
	es := store.NewEventStore(database)
	svc := New(s, es, files.NewGenerator(s, es, ""))
	svc.Rebuild()

	read := func(path string) (int, []byte) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		resp := httptest.NewRecorder()
		svc.Handler().ServeHTTP(resp, req)
		return resp.Code, resp.Body.Bytes()
	}
	status, index := read("/files/translation/lyrics/index.json")
	if status != http.StatusOK {
		t.Fatalf("embedded lyrics index status=%d body=%s", status, index)
	}
	var document struct {
		Version int           `json:"version"`
		Songs   []interface{} `json:"songs"`
	}
	if err := json.Unmarshal(index, &document); err != nil {
		t.Fatal(err)
	}
	if document.Version != 3 || len(document.Songs) != 700 {
		t.Fatalf("embedded lyrics index version=%d songs=%d", document.Version, len(document.Songs))
	}
	if status, _ := read("/files/translation/lyrics/music_307.json"); status != http.StatusOK {
		t.Fatalf("embedded music_307 status=%d", status)
	}
	if status, _ := read("/files/translation/lyrics/music_789.json"); status != http.StatusNotFound {
		t.Fatalf("embedded incomplete music_789 status=%d", status)
	}
	for _, locale := range model.SupportedLocales {
		root := "/files/v2/" + locale + "/translation/lyrics/"
		if status, localizedIndex := read(root + "index.json"); status != http.StatusOK || !bytes.Equal(localizedIndex, index) {
			t.Fatalf("embedded locale index %s status=%d differs=%v", locale, status, !bytes.Equal(localizedIndex, index))
		}
		if status, _ := read(root + "music_307.json"); status != http.StatusOK {
			t.Fatalf("embedded locale music_307 %s status=%d", locale, status)
		}
		if status, _ := read(root + "music_789.json"); status != http.StatusNotFound {
			t.Fatalf("embedded locale incomplete music_789 %s status=%d", locale, status)
		}
	}

	const malformedMusicID = 99001
	const malformedPerformerID = 99002
	if err := s.UpsertMusicCatalog([]store.MusicCatalogRecord{{MusicID: malformedMusicID, JapaneseTitle: "壊れたDB投影"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertPerformerCatalog([]store.PerformerCatalogRecord{{PerformerID: malformedPerformerID, JapaneseName: "試験歌唱者"}}); err != nil {
		t.Fatal(err)
	}
	saved, changed, err := s.SaveImportedLyricsMutation(model.SongLyrics{
		MusicID: malformedMusicID, Attribution: "Synthetic private database publication",
		SourceURL: "https://source.invalid/wiki/99001", SourcePageID: 99001, SourceRevisionID: 1,
		SourceSHA1: "0123456789abcdef0123456789abcdef01234567", SourceFetchedAt: "2026-08-11T00:00:00Z",
		Lines: []model.LyricLine{{
			ID: "malformed-db-line", Order: 0, Japanese: "壊れたDB投影",
			Segments: []model.LyricSegment{{Text: "壊れたDB投影", PerformerIDs: []int{malformedPerformerID}}},
		}},
	}, "fixture")
	if err != nil || !changed {
		t.Fatalf("save malformed DB fixture changed=%t err=%v", changed, err)
	}
	if _, err := s.PublishLyrics(saved.MusicID, saved.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE song_lyrics_publications SET payload_json='{' WHERE music_id=?`, malformedMusicID); err != nil {
		t.Fatal(err)
	}
	svc.Rebuild()
	status, malformedDBIndex := read("/files/translation/lyrics/index.json")
	if status != http.StatusOK || !bytes.Equal(malformedDBIndex, index) || svc.Status().LastError != "" {
		t.Fatalf("malformed DB publication blocked immutable bundle: status=%d projection=%+v", status, svc.Status())
	}

	svc.SetAsset("translation/lyrics/index.json", []byte(`{"version":1,"songs":[]}`), "application/json; charset=utf-8")
	svc.SetAsset("v2/zh-CN/translation/lyrics/index.json", []byte(`{"version":1,"songs":[]}`), "application/json; charset=utf-8")
	for _, path := range []string{
		"/files/translation/lyrics/index.json",
		"/files/v2/zh-CN/translation/lyrics/index.json",
	} {
		status, protectedIndex := read(path)
		if status != http.StatusOK || !bytes.Equal(protectedIndex, index) {
			t.Fatalf("external asset setter replaced immutable public lyrics %s: status=%d", path, status)
		}
	}
	svc.Rebuild()
	status, rebuiltIndex := read("/files/translation/lyrics/index.json")
	if status != http.StatusOK || !bytes.Equal(rebuiltIndex, index) {
		t.Fatalf("database rebuild did not retain immutable public lyrics overlay: status=%d", status)
	}
}

func TestPublishedLyricsFilesAndAtomicRebuild(t *testing.T) {
	publicLocales := []string{"ja-JP", "zh-CN", "en-US"}
	firstMusicID := 41001
	laterMusicID := firstMusicID + 17
	performerID := 601

	database, err := db.Open(filepath.Join(t.TempDir(), "lyrics-files.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s := store.New(database)
	es := store.NewEventStore(database)
	catalog := []store.MusicCatalogRecord{
		{MusicID: firstMusicID, JapaneseTitle: "試験曲甲", ChineseTitle: "测试歌曲甲", EnglishTitle: "Test Song Alpha", IsNewlyWrittenMusic: true},
		{MusicID: laterMusicID, JapaneseTitle: "試験曲乙", ChineseTitle: "测试歌曲乙", EnglishTitle: "Test Song Beta"},
	}
	if err := s.UpsertMusicCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertPerformerCatalog([]store.PerformerCatalogRecord{{PerformerID: performerID, JapaneseName: "試験歌唱者"}}); err != nil {
		t.Fatal(err)
	}
	inputs := []model.SongLyrics{
		{
			MusicID: firstMusicID, Revision: 0, Attribution: "Synthetic translation team alpha",
			SourceNote: "private alpha note", SourceURL: fmt.Sprintf("https://source.invalid/wiki/%d", firstMusicID), LicenseNote: "private alpha license",
			SourcePageID: 123, SourceRevisionID: 456, SourceSHA1: "0123456789abcdef0123456789abcdef01234567",
			SourceFetchedAt: "2026-07-23T00:00:00Z",
			Lines: []model.LyricLine{{
				ID: "source-alpha-1", Order: 0, Japanese: "甲を歌う", Chinese: "歌唱甲", English: "Sings alpha",
				Segments: []model.LyricSegment{{Text: "甲を歌う", PerformerIDs: []int{performerID}}},
			}},
		},
		{
			MusicID: laterMusicID, Revision: 0, Attribution: "Synthetic translation team beta",
			SourceNote: "private beta note", SourceURL: fmt.Sprintf("https://source.invalid/wiki/%d", laterMusicID), LicenseNote: "private beta license",
			SourcePageID: 789, SourceRevisionID: 987, SourceSHA1: "89abcdef0123456789abcdef0123456789abcdef",
			SourceFetchedAt: "2026-07-24T00:00:00Z",
			Lines: []model.LyricLine{{
				ID: "source-beta-1", Order: 0, Japanese: "乙を歌う", Chinese: "歌唱乙", English: "Sings beta",
				Segments: []model.LyricSegment{{Text: "乙を歌う", PerformerIDs: []int{performerID}}},
			}},
		},
	}
	if len(inputs) < 2 || inputs[0].MusicID >= inputs[1].MusicID {
		t.Fatal("failed rebuild fixture requires at least two publications in query order")
	}
	savedLyrics := make([]model.SongLyrics, 0, len(inputs))
	for _, input := range inputs {
		saved, changed, err := s.SaveImportedLyricsMutation(input, "editor")
		if err != nil || !changed {
			t.Fatalf("save lyrics musicId=%d changed=%t err=%v", input.MusicID, changed, err)
		}
		if _, err := s.PublishLyrics(saved.MusicID, saved.Revision); err != nil {
			t.Fatalf("publish lyrics musicId=%d: %v", saved.MusicID, err)
		}
		savedLyrics = append(savedLyrics, saved)
	}

	svc := New(s, es, files.NewGenerator(s, es, ""))
	svc.publicLyrics = nil
	svc.Rebuild()

	readAsset := func(path string) ([]byte, string, int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rec, req)
		return rec.Body.Bytes(), rec.Header().Get("ETag"), rec.Code
	}
	type assetSnapshot struct {
		body []byte
		etag string
	}
	snapshotAllAssets := func() map[string]assetSnapshot {
		svc.mu.RLock()
		defer svc.mu.RUnlock()
		snapshot := make(map[string]assetSnapshot, len(svc.assets))
		for key, projected := range svc.assets {
			snapshot[key] = assetSnapshot{body: append([]byte(nil), projected.body...), etag: projected.etag}
		}
		return snapshot
	}
	canonicalRoot := "/files/translation/lyrics"
	localizedRoot := func(locale string) string { return "/files/v2/" + locale + "/translation/lyrics" }
	indexPath := func(root string) string { return root + "/index.json" }
	detailPath := func(root string, musicID int) string { return fmt.Sprintf("%s/music_%d.json", root, musicID) }
	canonicalIndexPath := indexPath(canonicalRoot)

	index, indexETag, status := readAsset(canonicalIndexPath)
	var indexDocument model.PublicLyricsIndex
	if status != http.StatusOK {
		t.Fatalf("lyrics index status=%d body=%s", status, index)
	}
	if err := json.Unmarshal(index, &indexDocument); err != nil {
		t.Fatalf("lyrics index JSON: %v\nbody=%s", err, index)
	}
	if indexDocument.Version != 1 || len(indexDocument.Songs) != len(savedLyrics) {
		t.Fatalf("lyrics index document=%+v body=%s", indexDocument, index)
	}
	for position, saved := range savedLyrics {
		if indexDocument.Songs[position].MusicID != saved.MusicID || indexDocument.Songs[position].Revision != saved.Revision {
			t.Fatalf("lyrics index song[%d]=%+v saved=%+v", position, indexDocument.Songs[position], saved)
		}
	}

	published := map[string]assetSnapshot{
		canonicalIndexPath: {body: append([]byte(nil), index...), etag: indexETag},
	}
	canonicalDetails := make(map[int]assetSnapshot, len(savedLyrics))
	for _, saved := range savedLyrics {
		path := detailPath(canonicalRoot, saved.MusicID)
		body, etag, detailStatus := readAsset(path)
		if detailStatus != http.StatusOK || !bytes.Contains(body, []byte(`"version": 1`)) {
			t.Fatalf("lyrics detail %s status=%d body=%s", path, detailStatus, body)
		}
		canonicalDetails[saved.MusicID] = assetSnapshot{body: append([]byte(nil), body...), etag: etag}
		published[path] = canonicalDetails[saved.MusicID]
	}
	firstSaved := savedLyrics[0]
	firstPrivateLineID := []byte(firstSaved.Lines[0].ID)
	for _, saved := range savedLyrics {
		detail := canonicalDetails[saved.MusicID].body
		attributionJSON, err := json.Marshal(saved.Attribution)
		if err != nil {
			t.Fatal(err)
		}
		attributionJSON = append([]byte(`"attribution": `), attributionJSON...)
		privateLineID := []byte(saved.Lines[0].ID)
		if !bytes.Contains(detail, attributionJSON) {
			t.Fatalf("public lyrics musicId=%d omitted attribution: %s", saved.MusicID, detail)
		}
		if !bytes.Contains(detail, []byte(`"id": "line-1"`)) || bytes.Contains(detail, privateLineID) {
			t.Fatalf("public lyrics musicId=%d did not replace private line identity: %s", saved.MusicID, detail)
		}
		for _, privateField := range []string{
			"status", "updatedBy", "sourceNote", "sourceUrl", "licenseNote", "sourcePageId", "sourceRevisionId", "sourceSha1", "sourceFetchedAt",
			saved.SourceNote, saved.SourceURL, saved.LicenseNote, saved.SourceSHA1,
		} {
			if bytes.Contains(detail, []byte(privateField)) {
				t.Fatalf("public lyrics musicId=%d leaked %q: %s", saved.MusicID, privateField, detail)
			}
		}
	}
	for _, locale := range publicLocales {
		root := localizedRoot(locale)
		localizedIndexPath := indexPath(root)
		localizedIndex, localizedIndexETag, localizedIndexStatus := readAsset(localizedIndexPath)
		if localizedIndexStatus != http.StatusOK || !bytes.Equal(localizedIndex, index) || localizedIndexETag != indexETag {
			t.Fatalf("localized lyrics index %s status=%d etag=%q body=%s", locale, localizedIndexStatus, localizedIndexETag, localizedIndex)
		}
		published[localizedIndexPath] = assetSnapshot{body: append([]byte(nil), localizedIndex...), etag: localizedIndexETag}
		for _, saved := range savedLyrics {
			localizedDetailPath := detailPath(root, saved.MusicID)
			localized, localizedETag, localizedStatus := readAsset(localizedDetailPath)
			canonical := canonicalDetails[saved.MusicID]
			if localizedStatus != http.StatusOK || !bytes.Equal(localized, canonical.body) || localizedETag != canonical.etag {
				t.Fatalf("localized lyrics %s musicId=%d status=%d etag=%q body=%s", locale, saved.MusicID, localizedStatus, localizedETag, localized)
			}
			published[localizedDetailPath] = assetSnapshot{body: append([]byte(nil), localized...), etag: localizedETag}
		}
	}
	initialAssets := snapshotAllAssets()
	actualLyricsKeys := make(map[string]bool, len(published))
	for key := range initialAssets {
		if strings.HasPrefix(key, "translation/lyrics/") || (strings.HasPrefix(key, "v2/") && strings.Contains(key, "/translation/lyrics/")) {
			actualLyricsKeys["/files/"+key] = true
		}
	}
	if len(actualLyricsKeys) != len(published) {
		t.Fatalf("public locale contract projected %d lyrics paths, want %d: %v", len(actualLyricsKeys), len(published), actualLyricsKeys)
	}
	for path := range published {
		if !actualLyricsKeys[path] {
			t.Fatalf("public locale contract omitted %s: %v", path, actualLyricsKeys)
		}
	}

	var storedPayload string
	if err := database.QueryRow(`SELECT payload_json FROM song_lyrics_publications WHERE music_id=?`, firstSaved.MusicID).Scan(&storedPayload); err != nil {
		t.Fatal(err)
	}
	privateLineIDJSON, err := json.Marshal(firstSaved.Lines[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	legacyPayload := strings.Replace(storedPayload, `"id":"line-1"`, `"id":`+string(privateLineIDJSON), 1)
	if legacyPayload == storedPayload {
		t.Fatalf("could not construct legacy publication payload: %s", storedPayload)
	}
	if _, err := database.Exec(`UPDATE song_lyrics_publications SET payload_json=? WHERE music_id=?`, legacyPayload, firstSaved.MusicID); err != nil {
		t.Fatal(err)
	}
	svc.Rebuild()
	for path, previous := range published {
		current, currentETag, currentStatus := readAsset(path)
		if currentStatus != http.StatusOK || !bytes.Equal(current, previous.body) || currentETag != previous.etag {
			t.Fatalf("legacy stored identity changed %s: status=%d etag=%q body=%s", path, currentStatus, currentETag, current)
		}
		if strings.Contains(path, "/music_") && bytes.Contains(current, firstPrivateLineID) {
			t.Fatalf("legacy source line identity leaked through %s: %s", path, current)
		}
	}
	lastSuccessfulProjection := svc.Status()
	if lastSuccessfulProjection.Generation == 0 || lastSuccessfulProjection.Pending || lastSuccessfulProjection.LastSuccessAt == "" || lastSuccessfulProjection.LastError != "" {
		t.Fatalf("successful projection status = %+v", lastSuccessfulProjection)
	}
	lastSuccessfulAssets := snapshotAllAssets()

	var validCandidate model.PublicSongLyrics
	if err := json.Unmarshal([]byte(legacyPayload), &validCandidate); err != nil {
		t.Fatal(err)
	}
	candidateAttribution := fmt.Sprintf("candidate-attribution-%d", firstSaved.MusicID)
	candidateTitle := fmt.Sprintf("candidate-title-%d", firstSaved.MusicID)
	validCandidate.Attribution = candidateAttribution
	validCandidatePayload, err := json.Marshal(validCandidate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE song_lyrics_publications SET payload_json=? WHERE music_id=?`, string(validCandidatePayload), firstSaved.MusicID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE catalog_music SET title_en=? WHERE music_id=?`, candidateTitle, firstSaved.MusicID); err != nil {
		t.Fatal(err)
	}
	laterSaved := savedLyrics[len(savedLyrics)-1]
	var invalidCandidate model.PublicSongLyrics
	if err := database.QueryRow(`SELECT payload_json FROM song_lyrics_publications WHERE music_id=?`, laterSaved.MusicID).Scan(&storedPayload); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(storedPayload), &invalidCandidate); err != nil {
		t.Fatal(err)
	}
	invalidCandidate.Revision++
	wellFormedInvalidPayload, err := json.Marshal(invalidCandidate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE song_lyrics_publications SET payload_json=? WHERE music_id=?`, string(wellFormedInvalidPayload), laterSaved.MusicID); err != nil {
		t.Fatal(err)
	}
	svc.Rebuild()

	afterFailedAssets := snapshotAllAssets()
	if len(afterFailedAssets) != len(lastSuccessfulAssets) {
		t.Fatalf("failed rebuild changed asset count: before=%d after=%d", len(lastSuccessfulAssets), len(afterFailedAssets))
	}
	for key, previous := range lastSuccessfulAssets {
		current, ok := afterFailedAssets[key]
		if !ok || !bytes.Equal(current.body, previous.body) || current.etag != previous.etag {
			t.Fatalf("failed candidate generation changed asset %s: present=%t etag=%q body=%s", key, ok, current.etag, current.body)
		}
		if bytes.Contains(current.body, []byte(candidateAttribution)) || bytes.Contains(current.body, []byte(candidateTitle)) {
			t.Fatalf("failed candidate generation leaked candidate data through %s: %s", key, current.body)
		}
	}
	for path, previous := range published {
		afterFailure, afterFailureETag, afterFailureStatus := readAsset(path)
		if afterFailureStatus != http.StatusOK || !bytes.Equal(afterFailure, previous.body) || afterFailureETag != previous.etag {
			t.Fatalf("failed candidate generation changed served %s: status=%d etag=%q body=%s", path, afterFailureStatus, afterFailureETag, afterFailure)
		}
	}
	failedProjection := svc.Status()
	if failedProjection.Generation != lastSuccessfulProjection.Generation || !failedProjection.Pending || failedProjection.LastError != "projection_generation_failed" || failedProjection.LastSuccessAt != lastSuccessfulProjection.LastSuccessAt {
		t.Fatalf("failed projection status = %+v, previous = %+v", failedProjection, lastSuccessfulProjection)
	}

	for _, saved := range savedLyrics {
		if _, err := s.UnpublishLyrics(saved.MusicID, saved.Revision, "admin"); err != nil {
			t.Fatalf("unpublish musicId=%d: %v", saved.MusicID, err)
		}
	}
	for path, previous := range published {
		beforeRebuild, beforeRebuildETag, beforeRebuildStatus := readAsset(path)
		if beforeRebuildStatus != http.StatusOK || !bytes.Equal(beforeRebuild, previous.body) || beforeRebuildETag != previous.etag {
			t.Fatalf("unpublish changed served projection before rebuild %s: status=%d etag=%q body=%s", path, beforeRebuildStatus, beforeRebuildETag, beforeRebuild)
		}
	}
	svc.Rebuild()
	recoveredProjection := svc.Status()
	if recoveredProjection.Generation != lastSuccessfulProjection.Generation+1 || recoveredProjection.Pending || recoveredProjection.LastError != "" || recoveredProjection.LastSuccessAt == "" {
		t.Fatalf("recovered projection status = %+v", recoveredProjection)
	}

	detailPaths := make([]string, 0, len(savedLyrics)*(len(publicLocales)+1))
	indexPaths := []string{canonicalIndexPath}
	for _, saved := range savedLyrics {
		detailPaths = append(detailPaths, detailPath(canonicalRoot, saved.MusicID))
	}
	for _, locale := range publicLocales {
		root := localizedRoot(locale)
		indexPaths = append(indexPaths, indexPath(root))
		for _, saved := range savedLyrics {
			detailPaths = append(detailPaths, detailPath(root, saved.MusicID))
		}
	}
	expectedUnpublishedKeys := make(map[string]bool, len(indexPaths))
	for _, path := range indexPaths {
		expectedUnpublishedKeys[strings.TrimPrefix(path, "/files/")] = true
	}
	unpublishedSnapshot := make(map[string]asset, len(indexPaths))
	unexpectedLyricsKeys := make([]string, 0)
	svc.mu.RLock()
	for key, projected := range svc.assets {
		if strings.HasPrefix(key, "translation/lyrics/") || (strings.HasPrefix(key, "v2/") && strings.Contains(key, "/translation/lyrics/")) {
			if expectedUnpublishedKeys[key] {
				unpublishedSnapshot["/files/"+key] = projected
			} else {
				unexpectedLyricsKeys = append(unexpectedLyricsKeys, key)
			}
		}
	}
	svc.mu.RUnlock()
	if len(unexpectedLyricsKeys) != 0 || len(unpublishedSnapshot) != len(indexPaths) {
		t.Fatalf("unpublish was not one complete asset swap: unexpected=%v indexes=%d/%d", unexpectedLyricsKeys, len(unpublishedSnapshot), len(indexPaths))
	}
	for _, path := range detailPaths {
		if _, _, detailStatus := readAsset(path); detailStatus != http.StatusNotFound {
			t.Fatalf("unpublished detail %s status=%d", path, detailStatus)
		}
	}
	var emptyIndex []byte
	var emptyIndexETag string
	for _, path := range indexPaths {
		projected := unpublishedSnapshot[path]
		var document model.PublicLyricsIndex
		if err := json.Unmarshal(projected.body, &document); err != nil {
			t.Fatalf("unpublished index %s is invalid JSON: %v\nbody=%s", path, err, projected.body)
		}
		if document.Version != 1 || len(document.Songs) != 0 {
			t.Fatalf("unpublished index %s = %+v", path, document)
		}
		body, bodyETag, indexStatus := readAsset(path)
		if indexStatus != http.StatusOK || !bytes.Equal(body, projected.body) || bodyETag != projected.etag {
			t.Fatalf("unpublished index %s status=%d etag=%q body=%s", path, indexStatus, bodyETag, body)
		}
		if path == canonicalIndexPath {
			emptyIndex = append([]byte(nil), body...)
			emptyIndexETag = bodyETag
			if bodyETag == indexETag {
				t.Fatalf("unpublished index retained published ETag %q", bodyETag)
			}
			continue
		}
		if !bytes.Equal(body, emptyIndex) || bodyETag != emptyIndexETag {
			t.Fatalf("unpublished locale index %s differs from canonical: etag=%q body=%s", path, bodyETag, body)
		}
	}
}

func TestRebuildWaitsForContentBoundary(t *testing.T) {
	svc := setupLegacyFileService(t)
	release := svc.store.LockContentExclusive()
	done := make(chan struct{})
	go func() {
		svc.Rebuild()
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("rebuild crossed an active exclusive operation")
	case <-time.After(30 * time.Millisecond):
	}
	release()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("rebuild did not resume after content operation")
	}
}

func TestIncrementalEventRebuild(t *testing.T) {
	svc := setupLegacyFileService(t)

	// Update event 42
	if err := svc.events.UpdateLine(42, "1", "zebra", "新斑马", model.SourceHuman, "talk"); err != nil {
		t.Fatal(err)
	}

	if err := svc.RebuildEvent(42); err != nil {
		t.Fatalf("RebuildEvent failed: %v", err)
	}

	read := func(path string) ([]byte, int) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		svc.Handler().ServeHTTP(rec, req)
		return rec.Body.Bytes(), rec.Code
	}

	canonical, status := read("/files/translation/eventStory/event_42.json")
	if status != http.StatusOK {
		t.Fatalf("Get event_42.json status=%d", status)
	}
	if !strings.Contains(string(canonical), "新斑马") {
		t.Fatalf("expected '新斑马' in event_42.json, got: %s", string(canonical))
	}

	localized, status := read("/files/v2/zh-CN/translation/eventStory/event_42.json")
	if status != http.StatusOK {
		t.Fatalf("Get v2 zh-CN event_42.json status=%d", status)
	}
	if !strings.Contains(string(localized), "新斑马") {
		t.Fatalf("expected '新斑马' in localized event_42.json, got: %s", string(localized))
	}
}

func TestIncrementalCategoryRebuild(t *testing.T) {
	svc := setupLegacyFileService(t)

	// Update category cards
	if _, err := svc.store.UpdateEntry("cards", "prefix", "こんにちは", "您好呀", model.SourceHuman, "test-user"); err != nil {
		t.Fatal(err)
	}

	if err := svc.RebuildCategory("cards"); err != nil {
		t.Fatalf("RebuildCategory failed: %v", err)
	}

	read := func(path string) ([]byte, int) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		svc.Handler().ServeHTTP(rec, req)
		return rec.Body.Bytes(), rec.Code
	}

	flat, status := read("/files/translation/cards.json")
	if status != http.StatusOK {
		t.Fatalf("Get cards.json status=%d", status)
	}
	if !strings.Contains(string(flat), "您好呀") {
		t.Fatalf("expected '您好呀' in cards.json, got: %s", string(flat))
	}

	full, status := read("/files/translation/cards.full.json")
	if status != http.StatusOK {
		t.Fatalf("Get cards.full.json status=%d", status)
	}
	if !strings.Contains(string(full), "您好呀") {
		t.Fatalf("expected '您好呀' in cards.full.json, got: %s", string(full))
	}
}

func TestPublishNowBypassesDebounce(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "publish-now.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s := store.New(database)
	es := store.NewEventStore(database)
	svc := New(s, es, files.NewGenerator(s, es, ""))
	svc.SetDebounce(time.Hour) // very long debounce
	svc.Start()
	defer func() {
		svc.Stop()
		svc.Wait()
	}()

	// Wait for initial generation 1
	deadline := time.Now().Add(time.Second)
	for svc.Status().Generation == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	// Trigger PublishNow immediately
	svc.PublishNow()

	deadline = time.Now().Add(time.Second)
	for svc.Status().Generation < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	if gen := svc.Status().Generation; gen < 2 {
		t.Fatalf("expected generation >= 2 after PublishNow with 1h debounce, got %d", gen)
	}
}
