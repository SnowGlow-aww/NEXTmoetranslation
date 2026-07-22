package store

import (
	"errors"
	"path/filepath"
	"testing"

	"moesekai/server/internal/db"
	"moesekai/server/internal/model"
)

func setupLyricsStore(t *testing.T) *Store {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "lyrics.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	s := New(database)
	if err := s.UpsertMusicCatalog([]MusicCatalogRecord{
		{MusicID: 10, JapaneseTitle: "新曲", ChineseTitle: "新歌", EnglishTitle: "New Song", IsNewlyWrittenMusic: true},
		{MusicID: 20, JapaneseTitle: "旧曲", IsNewlyWrittenMusic: false},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertPerformerCatalog([]PerformerCatalogRecord{
		{PerformerID: 1, JapaneseName: "初音ミク", ChineseName: "初音未来", EnglishName: "Hatsune Miku"},
		{PerformerID: 2, JapaneseName: "鏡音リン"},
	}); err != nil {
		t.Fatal(err)
	}
	return s
}

func validLyrics() model.SongLyrics {
	return model.SongLyrics{
		MusicID: 10, Revision: 0, Status: "draft",
		SourceNote: "manual transcription", SourceURL: "https://example.invalid/source", LicenseNote: "internal",
		Lines: []model.LyricLine{{
			ID: "line-1", Order: 0, Japanese: "初音歌う", Chinese: "初音歌唱", English: "Miku sings",
			Segments: []model.LyricSegment{
				{Text: "初音", PerformerIDs: []int{1}},
				{Text: "歌う", PerformerIDs: []int{1, 2}},
			},
		}},
	}
}

func TestLyricsCRUDRevisionDriftAndPublication(t *testing.T) {
	s := setupLyricsStore(t)
	saved, err := s.SaveLyrics(validLyrics(), "editor")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Revision != 1 || saved.Status != "draft" || saved.UpdatedAt == "" {
		t.Fatalf("saved = %+v", saved)
	}

	stale := validLyrics()
	stale.Revision = 0
	_, err = s.SaveLyrics(stale, "editor")
	var contractErr *LyricsContractError
	if !errors.As(err, &contractErr) || contractErr.Code != "revision_conflict" || contractErr.Current == nil || contractErr.Current.Revision != 1 {
		t.Fatalf("stale save error = %#v", err)
	}

	drift := saved
	drift.Lines[0].Japanese = "初音が歌う"
	drift.Lines[0].Segments[1].Text = "が歌う"
	_, err = s.SaveLyrics(drift, "editor")
	if !errors.As(err, &contractErr) || contractErr.Code != "source_drift" {
		t.Fatalf("source drift error = %#v", err)
	}
	saved, err = s.GetLyrics(10)
	if err != nil {
		t.Fatal(err)
	}

	published, err := s.PublishLyrics(10, 1)
	if err != nil {
		t.Fatal(err)
	}
	if published.Status != "published" {
		t.Fatalf("published status = %q", published.Status)
	}
	again, err := s.PublishLyrics(10, 1)
	if err != nil || again.Status != "published" || again.Revision != 1 {
		t.Fatalf("idempotent publish = %+v err=%v", again, err)
	}

	index, details, err := s.PublishedLyrics()
	if err != nil {
		t.Fatal(err)
	}
	if len(index.Songs) != 1 || index.Songs[0].Title.English != "New Song" {
		t.Fatalf("public index = %+v", index)
	}
	public := details[10]
	if public.Version != 1 || public.Revision != 1 || len(public.Lines) != 1 {
		t.Fatalf("public detail = %+v", public)
	}

	edited := saved
	edited.Lines[0].English = "Hatsune Miku sings"
	edited, err = s.SaveLyrics(edited, "editor")
	if err != nil {
		t.Fatal(err)
	}
	if edited.Revision != 2 || edited.Status != "draft" {
		t.Fatalf("edited draft = %+v", edited)
	}
	_, details, err = s.PublishedLyrics()
	if err != nil {
		t.Fatal(err)
	}
	if details[10].Revision != 1 || details[10].Lines[0].English != "Miku sings" {
		t.Fatalf("draft edit changed published snapshot: %+v", details[10])
	}

	unpublished, err := s.UnpublishLyrics(10, 2)
	if err != nil || unpublished.Status != "draft" {
		t.Fatalf("unpublish = %+v err=%v", unpublished, err)
	}
	unpublished, err = s.UnpublishLyrics(10, 2)
	if err != nil || unpublished.Status != "draft" {
		t.Fatalf("idempotent unpublish = %+v err=%v", unpublished, err)
	}
}

func TestLyricsValidationCodes(t *testing.T) {
	s := setupLyricsStore(t)

	mismatch := validLyrics()
	mismatch.Lines[0].Segments[1].Text = "不一致"
	_, err := s.SaveLyrics(mismatch, "editor")
	var contractErr *LyricsContractError
	if !errors.As(err, &contractErr) || contractErr.Code != "segment_mismatch" || len(contractErr.Details) == 0 {
		t.Fatalf("segment mismatch error = %#v", err)
	}

	invalidPerformer := validLyrics()
	invalidPerformer.Lines[0].Segments[0].PerformerIDs = []int{999}
	_, err = s.SaveLyrics(invalidPerformer, "editor")
	if !errors.As(err, &contractErr) || contractErr.Code != "invalid_performer" {
		t.Fatalf("performer error = %#v", err)
	}

	duplicatePerformer := validLyrics()
	duplicatePerformer.Lines[0].Segments[0].PerformerIDs = []int{1, 1}
	_, err = s.SaveLyrics(duplicatePerformer, "editor")
	if !errors.As(err, &contractErr) || contractErr.Code != "invalid_performer" {
		t.Fatalf("duplicate performer error = %#v", err)
	}

	incomplete := validLyrics()
	incomplete.Lines[0].English = ""
	saved, err := s.SaveLyrics(incomplete, "editor")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.PublishLyrics(10, saved.Revision)
	if !errors.As(err, &contractErr) || contractErr.Code != "incomplete_publication" {
		t.Fatalf("publication error = %#v", err)
	}
}

func TestLyricsPublicationRequiresPerformerAndFreezesProvenance(t *testing.T) {
	s := setupLyricsStore(t)
	input := validLyrics()
	input.SourcePageID = 10
	input.SourceRevisionID = 20
	input.SourceSHA1 = "sha"
	input.SourceFetchedAt = "2026-07-22T12:00:00Z"
	input.Lines[0].Segments[0].PerformerIDs = nil
	saved, err := s.SaveLyrics(input, "editor")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.PublishLyrics(saved.MusicID, saved.Revision)
	var contractErr *LyricsContractError
	if !errors.As(err, &contractErr) || contractErr.Code != "incomplete_publication" {
		t.Fatalf("missing performer publication error = %#v", err)
	}

	drift := saved
	drift.SourceRevisionID++
	drift.SourceSHA1 = "changed"
	_, err = s.SaveLyrics(drift, "editor")
	if !errors.As(err, &contractErr) || contractErr.Code != "source_drift" {
		t.Fatalf("provenance drift error = %#v", err)
	}
}

func TestCatalogFiltersStableMasterdataIDs(t *testing.T) {
	s := setupLyricsStore(t)
	defaultResult, err := s.CatalogMusic("", true, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(defaultResult.Items) != 1 || defaultResult.Items[0].MusicID != 10 || !defaultResult.Items[0].IsNewlyWrittenMusic {
		t.Fatalf("default catalog = %+v", defaultResult)
	}
	all, err := s.CatalogMusic("", false, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Items) != 1 || all.NextCursor != "10" {
		t.Fatalf("paged catalog = %+v", all)
	}
	performers, err := s.CatalogPerformers()
	if err != nil {
		t.Fatal(err)
	}
	if len(performers.Items) != 2 || performers.Items[0].PerformerID != 1 {
		t.Fatalf("performer catalog = %+v", performers)
	}
}

func TestLyricsSourceProvenanceRoundTrip(t *testing.T) {
	s := setupLyricsStore(t)
	input := validLyrics()
	input.SourcePageID = 123
	input.SourceRevisionID = 456
	input.SourceSHA1 = "source-sha1"
	input.SourceFetchedAt = "2026-07-22T12:34:56Z"
	saved, err := s.SaveLyrics(input, "editor")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := s.GetLyrics(saved.MusicID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SourcePageID != 123 || loaded.SourceRevisionID != 456 || loaded.SourceSHA1 != "source-sha1" || loaded.SourceFetchedAt != input.SourceFetchedAt {
		t.Fatalf("source provenance = %+v", loaded)
	}

	invalid := validLyrics()
	invalid.SourcePageID = 123
	_, err = s.SaveLyrics(invalid, "editor")
	var contractErr *LyricsContractError
	if !errors.As(err, &contractErr) || contractErr.Code != "source_drift" {
		t.Fatalf("partial source provenance error = %#v", err)
	}
}

func TestLyricsMutationsWriteAuditRows(t *testing.T) {
	s := setupLyricsStore(t)
	saved, err := s.SaveLyrics(validLyrics(), "editor")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishLyrics(saved.MusicID, saved.Revision, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UnpublishLyrics(saved.MusicID, saved.Revision, "admin"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action IN ('lyrics.save', 'lyrics.publish', 'lyrics.unpublish')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("lyrics audit count = %d", count)
	}
}

func TestCatalogTitlesFollowLocaleEdits(t *testing.T) {
	s := setupLyricsStore(t)
	if _, err := s.ImportCategory("music", model.Category{"title": {
		"新曲": {Text: "中文目录名", Source: model.SourceHuman},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateEntryLocale("music", "title", "新曲", "English Catalog Title", model.SourceHuman, "editor", model.LocaleEnglish); err != nil {
		t.Fatal(err)
	}
	result, err := s.CatalogMusic("", true, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].Title.Chinese != "中文目录名" || result.Items[0].Title.English != "English Catalog Title" {
		t.Fatalf("localized catalog = %+v", result)
	}
}
