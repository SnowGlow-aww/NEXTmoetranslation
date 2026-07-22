package backup

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"moesekai/server/internal/config"
	"moesekai/server/internal/db"
	"moesekai/server/internal/files"
	"moesekai/server/internal/importer"
	"moesekai/server/internal/legacy"
	"moesekai/server/internal/model"
	"moesekai/server/internal/store"
)

type legacyBackupHarness struct {
	manager *Manager
	store   *store.Store
	events  *store.EventStore
	cfg     *config.Config
	gen     *files.Generator
}

func setupLegacyBackup(t *testing.T) *legacyBackupHarness {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "legacy-backup.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	s := store.New(database)
	es := store.NewEventStore(database)
	cfg, err := config.New(database, "legacy-backup-master-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportCategory("cards", model.Category{
		"prefix": {
			"こんにちは": {Text: "你好", Source: model.SourceHuman, Ids: []string{"legacy-1"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := es.ImportOrdered(42, model.EventStoryMeta{
		Source: "official_cn", Version: "1.0", LastUpdated: 1700000000,
	}, []store.OrderedEpisode{{
		EpisodeNo: "1", ScenarioID: "scenario-1", Title: "人工标题", TitleSource: model.SourceHuman,
		TalkKeys:     []string{"一", "二"},
		TalkData:     map[string]string{"一": "第一句", "二": "第二句"},
		TalkSources:  map[string]string{"一": model.SourcePinned, "二": model.SourceHuman},
		SpeakerNames: map[string]string{"一": "角色"},
	}}); err != nil {
		t.Fatal(err)
	}
	gen := files.NewGenerator(s, es, "")
	manager := NewManager(cfg, gen, s, es, filepath.Join(t.TempDir(), "work"))
	return &legacyBackupHarness{manager: manager, store: s, events: es, cfg: cfg, gen: gen}
}

func TestLegacyBackupRestoreRoundTrip(t *testing.T) {
	source := setupLegacyBackup(t)
	backupRoot := t.TempDir()
	translations, err := source.manager.materializeTranslations(backupRoot)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(translations) != "translations" {
		t.Fatalf("backup directory = %q", translations)
	}
	for _, path := range []string{
		"cards.json", "cards.full.json", filepath.Join("eventStory", "event_42.json"),
	} {
		if _, err := os.Stat(filepath.Join(translations, path)); err != nil {
			t.Fatalf("missing backup payload %s: %v", path, err)
		}
	}

	destDB, err := db.Open(filepath.Join(t.TempDir(), "restored.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer destDB.Close()
	destStore := store.New(destDB)
	destEvents := store.NewEventStore(destDB)
	hooks := 0
	destStore.OnChange(func() { hooks++ })
	result, err := importer.ImportDir(translations, destStore, destEvents)
	if err != nil {
		t.Fatal(err)
	}
	if result.Categories != len(model.SupportedCategories) || result.Entries != 1 || result.EventStories != 1 {
		t.Fatalf("restore result = %+v", result)
	}
	if hooks != 1 {
		t.Fatalf("restore change hooks = %d, want 1", hooks)
	}

	destGen := files.NewGenerator(destStore, destEvents, "")
	compareGenerated := func(name string, sourceFn, destFn func() ([]byte, error)) {
		t.Helper()
		want, err := sourceFn()
		if err != nil {
			t.Fatal(err)
		}
		got, err := destFn()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%s changed across backup/restore\ngot:\n%s\nwant:\n%s", name, got, want)
		}
	}
	compareGenerated("cards flat", func() ([]byte, error) { return source.gen.CategoryFlatJSON("cards") }, func() ([]byte, error) { return destGen.CategoryFlatJSON("cards") })
	compareGenerated("cards full", func() ([]byte, error) { return source.gen.CategoryFullJSON("cards") }, func() ([]byte, error) { return destGen.CategoryFullJSON("cards") })
	compareGenerated("event public", func() ([]byte, error) { return source.gen.EventStoryJSON(42) }, func() ([]byte, error) { return destGen.EventStoryJSON(42) })

	// The legacy backup is a public projection: these private fields are lost on restore.
	detail, err := destEvents.Detail(42)
	if err != nil {
		t.Fatal(err)
	}
	ep := detail.Episodes["1"]
	if ep.TitleSource != "" || ep.TalkSources["一"] != "official_cn" || ep.TalkSources["二"] != "official_cn" || len(ep.SpeakerNames) != 0 {
		t.Fatalf("restored legacy provenance contract changed: %+v", ep)
	}
}

func TestLegacyS3BackupTriggerAndRestoreSemantics(t *testing.T) {
	h := setupLegacyBackup(t)
	type requestRecord struct {
		method, path, auth, contentType string
		body                            []byte
	}
	var mu sync.Mutex
	var requests []requestRecord
	var latest []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests = append(requests, requestRecord{
			method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization"),
			contentType: r.Header.Get("Content-Type"), body: append([]byte(nil), body...),
		})
		if r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/latest.tar.gz") {
			latest = append([]byte(nil), body...)
		}
		response := append([]byte(nil), latest...)
		mu.Unlock()
		if r.Method == http.MethodGet {
			_, _ = w.Write(response)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	settings := map[string]string{
		config.KeyBackupS3Endpoint:  server.URL,
		config.KeyBackupS3Region:    "legacy-region-1",
		config.KeyBackupS3Bucket:    "legacy-bucket",
		config.KeyBackupS3Prefix:    "/snapshots/",
		config.KeyBackupS3AccessKey: "legacy-access",
		config.KeyBackupS3SecretKey: "legacy-secret",
	}
	for key, value := range settings {
		if err := h.cfg.Set(key, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.manager.backupS3(); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	puts := append([]requestRecord(nil), requests...)
	mu.Unlock()
	if len(puts) != 2 {
		t.Fatalf("S3 backup requests = %d, want timestamped plus latest", len(puts))
	}
	timestamped := regexp.MustCompile(`^/legacy-bucket/snapshots/translations-[0-9]{8}-[0-9]{6}\.tar\.gz$`)
	if puts[0].method != http.MethodPut || !timestamped.MatchString(puts[0].path) {
		t.Fatalf("timestamped PUT = %s %s", puts[0].method, puts[0].path)
	}
	if puts[1].method != http.MethodPut || puts[1].path != "/legacy-bucket/snapshots/latest.tar.gz" {
		t.Fatalf("latest PUT = %s %s", puts[1].method, puts[1].path)
	}
	for _, put := range puts {
		if !strings.HasPrefix(put.auth, "AWS4-HMAC-SHA256 Credential=legacy-access/") {
			t.Fatalf("missing SigV4 Authorization: %q", put.auth)
		}
		if put.contentType != "application/gzip" {
			t.Fatalf("PUT Content-Type = %q", put.contentType)
		}
	}
	if !bytes.Equal(puts[0].body, puts[1].body) || len(puts[0].body) == 0 {
		t.Fatal("timestamped and latest objects must contain the same non-empty tarball")
	}

	if _, err := h.store.UpdateEntry("cards", "prefix", "こんにちは", "被覆盖", model.SourceLLM, "test"); err != nil {
		t.Fatal(err)
	}
	result, err := h.manager.restoreS3()
	if err != nil {
		t.Fatal(err)
	}
	if result.Entries != 1 || result.EventStories != 1 {
		t.Fatalf("S3 restore result = %+v", result)
	}
	category, err := h.store.CategoryData("cards")
	if err != nil {
		t.Fatal(err)
	}
	if got := category["prefix"]["こんにちは"]; got.Text != "你好" || got.Source != model.SourceHuman {
		t.Fatalf("S3 restore entry = %+v", got)
	}
}

func TestLegacyGitBackupCommitsOnlyChangedProjection(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git executable unavailable")
	}
	h := setupLegacyBackup(t)
	remote := filepath.Join(t.TempDir(), "legacy-remote.git")
	cmd := exec.Command("git", "init", "--bare", "--initial-branch=legacy-backup", remote)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v: %s", err, out)
	}
	if err := h.cfg.Set(config.KeyBackupGitRepoURL, "file://"+remote); err != nil {
		t.Fatal(err)
	}
	if err := h.cfg.Set(config.KeyBackupGitBranch, "legacy-backup"); err != nil {
		t.Fatal(err)
	}
	if err := h.manager.backupGit(); err != nil {
		t.Fatal(err)
	}
	first := gitOutput(t, remote, "rev-parse", "refs/heads/legacy-backup")
	full := gitOutput(t, remote, "show", "refs/heads/legacy-backup:translations/cards.full.json")
	if !strings.Contains(full, `"source": "human"`) || !strings.Contains(full, `"legacy-1"`) {
		t.Fatalf("Git backup projection missing full entry metadata: %s", full)
	}
	if err := h.manager.backupGit(); err != nil {
		t.Fatal(err)
	}
	second := gitOutput(t, remote, "rev-parse", "refs/heads/legacy-backup")
	if first != second {
		t.Fatalf("unchanged backup created another commit: %s -> %s", first, second)
	}
}

func TestTranslationContentManifestRoundTripAndAtomicFailure(t *testing.T) {
	source := setupLegacyBackup(t)
	if _, err := source.store.UpdateEntryLocale("cards", "prefix", "こんにちは", "Hello", model.SourceHuman, "editor", model.LocaleEnglish); err != nil {
		t.Fatal(err)
	}
	if err := source.store.UpsertMusicCatalog([]store.MusicCatalogRecord{{
		MusicID: 10, JapaneseTitle: "新曲", EnglishTitle: "New Song", IsNewlyWrittenMusic: true,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := source.store.UpsertPerformerCatalog([]store.PerformerCatalogRecord{{PerformerID: 1, JapaneseName: "初音ミク"}}); err != nil {
		t.Fatal(err)
	}
	saved, err := source.store.SaveLyrics(model.SongLyrics{
		MusicID: 10, Revision: 0, Attribution: "MoeSeka translation team",
		Lines: []model.LyricLine{{
			ID: "line-1", Order: 0, Japanese: "歌う", Chinese: "歌唱", English: "Sings",
			Segments: []model.LyricSegment{{Text: "歌う", PerformerIDs: []int{1}}},
		}},
	}, "editor")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.store.PublishLyrics(10, saved.Revision); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	translations, err := source.manager.materializeTranslations(root)
	if err != nil {
		t.Fatal(err)
	}
	contentDir, err := source.manager.materializeTranslationContent(root)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(contentDir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"password_hash", "legacy-admin", "llm.openai.key"} {
		if bytes.Contains(manifest, []byte(forbidden)) {
			t.Fatalf("manifest contains forbidden user/secret field %q", forbidden)
		}
	}
	content, present, err := readTranslationContent(contentDir)
	if err != nil || !present {
		t.Fatalf("read content present=%v err=%v", present, err)
	}

	destDB, err := db.Open(filepath.Join(t.TempDir(), "content-restore.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer destDB.Close()
	destStore := store.New(destDB)
	destEvents := store.NewEventStore(destDB)
	if _, err := importer.ImportDir(translations, destStore, destEvents); err != nil {
		t.Fatal(err)
	}
	if err := destStore.ImportTranslationContent(content.Entries, content.Events, content.Lyrics); err != nil {
		t.Fatal(err)
	}
	english, err := destStore.GetEntriesLocale("cards", "prefix", "", model.LocaleEnglish)
	if err != nil {
		t.Fatal(err)
	}
	if len(english) != 1 || english[0].Text != "Hello" {
		t.Fatalf("restored English entries = %+v", english)
	}
	restoredLyrics, err := destStore.GetLyrics(10)
	if err != nil || restoredLyrics.Status != "published" || restoredLyrics.Attribution != "MoeSeka translation team" {
		t.Fatalf("restored lyrics = %+v err=%v", restoredLyrics, err)
	}

	entriesPath := filepath.Join(contentDir, "entries.json")
	entriesBody, err := os.ReadFile(entriesPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entriesPath, append(entriesBody, ' '), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readTranslationContent(contentDir); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("tampered manifest error = %v", err)
	}

	invalidContent := content
	invalidContent.Events.Segments = append(invalidContent.Events.Segments, store.EventSegmentRecord{
		SegmentID: "missing-parent", EventID: 999, EpisodeNo: "1", Kind: "talk", Position: 0,
	})
	if err := destStore.ImportTranslationContent(invalidContent.Entries, invalidContent.Events, invalidContent.Lyrics); err == nil {
		t.Fatal("expected foreign-key failure for invalid content")
	}
	english, err = destStore.GetEntriesLocale("cards", "prefix", "", model.LocaleEnglish)
	if err != nil || len(english) != 1 || english[0].Text != "Hello" {
		t.Fatalf("failed import was not atomic: entries=%+v err=%v", english, err)
	}
}

func TestBackupPayloadUsesSingleSQLiteSnapshot(t *testing.T) {
	h := setupLegacyBackup(t)
	if _, err := h.store.UpdateEntryLocale("cards", "prefix", "こんにちは", "Old English", model.SourceHuman, "editor", model.LocaleEnglish); err != nil {
		t.Fatal(err)
	}
	backupSnapshotCreatedHook = func() error {
		if _, err := h.store.UpdateEntry("cards", "prefix", "こんにちは", "新中文", model.SourceHuman, "editor"); err != nil {
			return err
		}
		_, err := h.store.UpdateEntryLocale("cards", "prefix", "こんにちは", "New English", model.SourceHuman, "editor", model.LocaleEnglish)
		return err
	}
	t.Cleanup(func() { backupSnapshotCreatedHook = nil })
	translations, contentDir, err := h.manager.materializeBackupPayload(t.TempDir())
	backupSnapshotCreatedHook = nil
	if err != nil {
		t.Fatal(err)
	}
	category, _, err := legacy.LoadCategory(translations, "cards")
	if err != nil {
		t.Fatal(err)
	}
	if got := category["prefix"]["こんにちは"].Text; got != "你好" {
		t.Fatalf("legacy snapshot text = %q", got)
	}
	content, present, err := readTranslationContent(contentDir)
	if err != nil || !present {
		t.Fatalf("content present=%v err=%v", present, err)
	}
	if len(content.Entries) != 1 || content.Entries[0].Text != "Old English" {
		t.Fatalf("additive snapshot entries = %+v", content.Entries)
	}
}

func TestRestoreIsAtomicAndOldBackupClearsAdditiveState(t *testing.T) {
	source := setupLegacyBackup(t)
	translations, contentDir, err := source.manager.materializeBackupPayload(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	payload, _, err := importer.ReadDir(translations)
	if err != nil {
		t.Fatal(err)
	}
	content, present, err := readTranslationContent(contentDir)
	if err != nil || !present {
		t.Fatalf("content present=%v err=%v", present, err)
	}

	database, err := db.Open(filepath.Join(t.TempDir(), "atomic-restore.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	destination := store.New(database)
	if _, err := destination.ImportCategory("cards", model.Category{"prefix": {
		"こんにちは": {Text: "Before Chinese", Source: model.SourceHuman},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := destination.UpdateEntryLocale("cards", "prefix", "こんにちは", "Before English", model.SourceHuman, "editor", model.LocaleEnglish); err != nil {
		t.Fatal(err)
	}
	if err := destination.UpsertMusicCatalog([]store.MusicCatalogRecord{{MusicID: 10, JapaneseTitle: "歌", IsNewlyWrittenMusic: true}}); err != nil {
		t.Fatal(err)
	}
	if err := destination.UpsertPerformerCatalog([]store.PerformerCatalogRecord{{PerformerID: 1, JapaneseName: "歌手"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := destination.SaveLyrics(model.SongLyrics{MusicID: 10, Lines: []model.LyricLine{{
		ID: "line", Order: 0, Japanese: "歌", Chinese: "歌", English: "Song",
		Segments: []model.LyricSegment{{Text: "歌", PerformerIDs: []int{1}}},
	}}}, "editor"); err != nil {
		t.Fatal(err)
	}

	invalid := content
	invalid.Events.Segments = append(invalid.Events.Segments, store.EventSegmentRecord{
		SegmentID: "missing-parent", EventID: 999, EpisodeNo: "1", Kind: "talk", Position: 0,
	})
	if err := destination.RestoreBackup(payload.Categories, payload.Events, invalid.Entries, invalid.Events, invalid.Lyrics, true, "admin"); err == nil {
		t.Fatal("invalid additive restore unexpectedly succeeded")
	}
	legacyBefore, err := destination.GetEntries("cards", "prefix", "")
	if err != nil || len(legacyBefore) != 1 || legacyBefore[0].Text != "Before Chinese" {
		t.Fatalf("failed restore changed legacy content: %+v err=%v", legacyBefore, err)
	}
	englishBefore, err := destination.GetEntriesLocale("cards", "prefix", "", model.LocaleEnglish)
	if err != nil || len(englishBefore) != 1 || englishBefore[0].Text != "Before English" {
		t.Fatalf("failed restore changed additive content: %+v err=%v", englishBefore, err)
	}

	if err := destination.RestoreBackup(payload.Categories, payload.Events, nil, store.EventContentExport{}, store.LyricsContentExport{}, false, "admin"); err != nil {
		t.Fatal(err)
	}
	englishAfter, err := destination.GetEntriesLocale("cards", "prefix", "", model.LocaleEnglish)
	if err != nil || len(englishAfter) != 1 || englishAfter[0].Text != "" {
		t.Fatalf("old backup retained English content: %+v err=%v", englishAfter, err)
	}
	if _, err := destination.GetLyrics(10); err != store.ErrLyricsNotFound {
		t.Fatalf("old backup retained lyrics: %v", err)
	}
	var audits int
	if err := database.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='backup.restore' AND user='admin'`).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("restore audit count=%d err=%v", audits, err)
	}
}

func gitOutput(t *testing.T, gitDir string, args ...string) string {
	t.Helper()
	allArgs := append([]string{"--git-dir", gitDir}, args...)
	cmd := exec.Command("git", allArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}
