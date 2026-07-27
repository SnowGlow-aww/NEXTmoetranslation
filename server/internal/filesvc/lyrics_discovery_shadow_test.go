package filesvc

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"moesekai/server/internal/db"
	"moesekai/server/internal/files"
	"moesekai/server/internal/lyricsdiscovery"
	"moesekai/server/internal/model"
	"moesekai/server/internal/store"
)

func TestLyricsDiscoveryShadowCompletionHasNoAuthoritativeOrPublicSideEffects(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "shadow-proof.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	translations := store.New(database)
	events := store.NewEventStore(database)
	if err := translations.UpsertMusicCatalog([]store.MusicCatalogRecord{{
		MusicID: 10, JapaneseTitle: "合成試験曲", ProducerMetadata: "制作者",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := translations.UpsertPerformerCatalog([]store.PerformerCatalogRecord{{PerformerID: 1, JapaneseName: "合成歌唱者"}}); err != nil {
		t.Fatal(err)
	}
	saved, err := translations.SaveLyrics(model.SongLyrics{
		MusicID: 10, Attribution: "Synthetic attribution", SourceURL: "https://example.invalid/manual-source",
		Lines: []model.LyricLine{{
			ID: "line-1", Order: 0, Japanese: "合成歌唱行", Chinese: "合成翻译行", English: "Synthetic translated line",
			Segments: []model.LyricSegment{{Text: "合成歌唱行", PerformerIDs: []int{1}}},
		}},
	}, "editor")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := translations.PublishLyrics(saved.MusicID, saved.Revision); err != nil {
		t.Fatal(err)
	}
	service := New(translations, events, files.NewGenerator(translations, events, ""))
	service.debounce = time.Millisecond
	service.Start()
	defer func() {
		service.Stop()
		service.Wait()
	}()
	waitForProjection(t, service, func(status ProjectionStatus) bool {
		return status.Generation == 1 && !status.Pending && status.LastError == ""
	})
	beforeAssets := snapshotPublicAssets(t, service)
	beforeGeneration := service.Status().Generation
	beforeTables := snapshotAuthoritativeLyricsTables(t, database)

	adapter, err := store.NewLyricsDiscoveryAdapter(translations, store.LyricsDiscoveryShadowPolicyVersion, 3)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := adapter.Scan(context.Background(), lyricsdiscovery.ScanRequest{WorkerID: "shadow-proof", Now: time.Now().UTC()}); err != nil || result.Scheduled != 1 {
		t.Fatalf("scan result=%+v err=%v", result, err)
	}
	job, ok, err := adapter.Claim(context.Background(), lyricsdiscovery.ClaimRequest{
		WorkerID: "shadow-proof", Now: time.Now().UTC(), LeaseDuration: time.Minute,
	})
	if err != nil || !ok {
		t.Fatalf("claim ok=%t job=%+v err=%v", ok, job, err)
	}
	if err := adapter.Complete(context.Background(), lyricsdiscovery.Completion{
		JobID: job.ID, LeaseToken: job.LeaseToken, WorkerID: "shadow-proof", CompletedAt: time.Now().UTC(),
		Result: lyricsdiscovery.Result{
			Outcome: lyricsdiscovery.OutcomeCandidatesFound, CandidateCount: 1,
			Artifact: []byte(`{"candidates":[{"pageId":12,"title":"合成試験曲","canonicalUrl":"https://vocaloid.fandom.com/wiki/Song?oldid=34","revisionId":34,"sha1":"0123456789abcdef0123456789abcdef01234567","categories":[]}]}`),
		},
	}); err != nil {
		t.Fatal(err)
	}
	// Give a wrongly-wired change hook enough time to publish another generation.
	time.Sleep(5 * service.debounce)

	afterTables := snapshotAuthoritativeLyricsTables(t, database)
	afterAssets := snapshotPublicAssets(t, service)
	if !reflect.DeepEqual(afterTables, beforeTables) {
		t.Fatalf("shadow completion changed authoritative tables\nbefore=%v\nafter=%v", beforeTables, afterTables)
	}
	if !reflect.DeepEqual(afterAssets, beforeAssets) {
		t.Fatalf("shadow completion changed public assets\nbefore=%v\nafter=%v", beforeAssets, afterAssets)
	}
	if service.Status().Generation != beforeGeneration {
		t.Fatalf("shadow completion changed projection generation from %d to %d", beforeGeneration, service.Status().Generation)
	}
	var shadowRows int
	if err := database.QueryRow(`SELECT COUNT(*) FROM lyrics_discovery_shadow_results`).Scan(&shadowRows); err != nil || shadowRows != 1 {
		t.Fatalf("shadow rows=%d err=%v", shadowRows, err)
	}
}

func snapshotAuthoritativeLyricsTables(t *testing.T, database *db.DB) map[string][]string {
	t.Helper()
	queries := map[string]string{
		"song_lyrics":              `SELECT printf('%d|%d|%d|%s|%s|%s|%s|%s|%s|%d|%d|%s|%d', music_id, revision, updated_at, updated_by, attribution, source_note, source_url, license_note, source_hash, source_page_id, source_revision_id, source_sha1, source_fetched_at) FROM song_lyrics ORDER BY music_id`,
		"song_lyric_lines":         `SELECT printf('%d|%s|%d|%s|%s|%s|%d', music_id, line_id, position, japanese, zh_cn, en_us, stanza_break_before) FROM song_lyric_lines ORDER BY music_id, position`,
		"song_lyric_segments":      `SELECT printf('%d|%s|%d|%s|%s', music_id, line_id, position, text, performer_ids_json) FROM song_lyric_segments ORDER BY music_id, line_id, position`,
		"song_lyrics_publications": `SELECT printf('%d|%d|%d|%s', music_id, revision, updated_at, payload_json) FROM song_lyrics_publications ORDER BY music_id`,
	}
	result := map[string][]string{}
	for table, query := range queries {
		rows, err := database.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var value string
			if err := rows.Scan(&value); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			result[table] = append(result[table], value)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
	}
	return result
}

func snapshotPublicAssets(t *testing.T, service *Service) map[string][]byte {
	t.Helper()
	service.mu.RLock()
	keys := make([]string, 0, len(service.assets))
	for key := range service.assets {
		keys = append(keys, key)
	}
	service.mu.RUnlock()
	sort.Strings(keys)
	result := make(map[string][]byte, len(keys))
	for _, key := range keys {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/files/"+key, nil)
		service.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("asset %s status=%d", key, response.Code)
		}
		body := append([]byte(nil), response.Body.Bytes()...)
		etag := []byte(response.Header().Get("ETag"))
		result[key] = bytes.Join([][]byte{etag, body}, []byte{0})
	}
	return result
}
