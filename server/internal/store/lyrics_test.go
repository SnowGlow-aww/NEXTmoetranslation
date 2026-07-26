package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

const validSourceSHA1 = "0123456789abcdef0123456789abcdef01234567"

func validLyrics() model.SongLyrics {
	return model.SongLyrics{
		MusicID: 10, Revision: 0, Status: "draft",
		Attribution: "Lyrics transcription and translation by the MoeSeka team",
		SourceNote:  "manual transcription", SourceURL: "https://example.invalid/source", LicenseNote: "internal",
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
	if public.Version != 1 || public.Revision != 1 || public.Attribution != saved.Attribution || len(public.Lines) != 1 {
		t.Fatalf("public detail = %+v", public)
	}

	edited := saved
	edited.Lines[0].English = "Hatsune Miku sings"
	edited, err = s.SaveLyrics(edited, "editor")
	if err != nil {
		t.Fatal(err)
	}
	if edited.Revision != 2 || edited.Status != "draft-published" || edited.PublishedRevision != 1 {
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

func TestSavedLyricsAllowEquivalentResegmentation(t *testing.T) {
	s := setupLyricsStore(t)
	saved, err := s.SaveLyrics(validLyrics(), "editor")
	if err != nil {
		t.Fatal(err)
	}
	saved.Lines[0].Segments = []model.LyricSegment{
		{Text: "初", PerformerIDs: []int{1}},
		{Text: "音歌", PerformerIDs: []int{2}},
		{Text: "う", PerformerIDs: []int{1, 2}},
	}
	updated, err := s.SaveLyrics(saved, "editor")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != saved.Revision+1 || len(updated.Lines[0].Segments) != 3 || updated.Lines[0].Japanese != "初音歌う" {
		t.Fatalf("equivalent resegmentation = %+v", updated)
	}
}

func TestSavedLyricsRejectNumericOrderDrift(t *testing.T) {
	s := setupLyricsStore(t)
	saved, err := s.SaveLyrics(validLyrics(), "editor")
	if err != nil {
		t.Fatal(err)
	}
	saved.Lines[0].Order = 10
	_, err = s.SaveLyrics(saved, "editor")
	var contractErr *LyricsContractError
	if !errors.As(err, &contractErr) || contractErr.Code != "source_drift" {
		t.Fatalf("numeric order drift error = %#v", err)
	}
}

func TestImportedLyricsPathRejectsNonzeroRevisionForNewDocument(t *testing.T) {
	s := setupLyricsStore(t)
	input := validLyrics()
	input.Revision = 1
	_, _, err := s.SaveImportedLyricsMutation(input, "editor")
	var contractErr *LyricsContractError
	if !errors.As(err, &contractErr) || contractErr.Code != "revision_conflict" {
		t.Fatalf("new imported save with nonzero revision error = %#v", err)
	}
}

func TestImportedLyricsPathRejectsExistingDocument(t *testing.T) {
	s := setupLyricsStore(t)
	saved, err := s.SaveLyrics(validLyrics(), "editor")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.SaveImportedLyricsMutation(saved, "editor")
	var contractErr *LyricsContractError
	if !errors.As(err, &contractErr) || contractErr.Code != "source_drift" {
		t.Fatalf("existing imported save error = %#v", err)
	}
}

func TestImportedLyricsEligibilityAndDatabaseSaveShareMusicStripe(t *testing.T) {
	s := setupLyricsStore(t)
	input := validLyrics()
	input.SourcePageID = 123
	input.SourceRevisionID = 456
	input.SourceSHA1 = validSourceSHA1
	input.SourceFetchedAt = "2026-07-22T12:34:56Z"

	unlock := s.lockLyrics(input.MusicID)
	started := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		close(started)
		_, _, err := s.SaveImportedLyricsMutation(input, "editor")
		finished <- err
	}()
	<-started
	select {
	case err := <-finished:
		unlock()
		t.Fatalf("imported eligibility/save escaped the music stripe: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := s.GetLyrics(input.MusicID); err != ErrLyricsNotFound {
		unlock()
		t.Fatalf("imported lyrics became visible before stripe release: %v", err)
	}
	unlock()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("imported save failed after stripe release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("imported save remained blocked after stripe release")
	}
	loaded, err := s.GetLyrics(input.MusicID)
	if err != nil || loaded.Revision != 1 || loaded.SourceSHA1 != validSourceSHA1 {
		t.Fatalf("imported save after stripe release=%+v err=%v", loaded, err)
	}
}

func TestLyricsFirstSaveEligibilityAndSaveShareMusicStripe(t *testing.T) {
	s := setupLyricsStore(t)
	callbackStarted := make(chan struct{})
	allowCallback := make(chan struct{})
	eligibilityDone := make(chan error, 1)
	go func() {
		eligibilityDone <- s.WithLyricsFirstSaveEligibility(10, func() error {
			close(callbackStarted)
			<-allowCallback
			return nil
		})
	}()
	<-callbackStarted

	saveDone := make(chan error, 1)
	go func() {
		_, err := s.SaveLyrics(validLyrics(), "editor")
		saveDone <- err
	}()
	select {
	case err := <-saveDone:
		close(allowCallback)
		t.Fatalf("first save crossed eligibility callback: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(allowCallback)
	if err := <-eligibilityDone; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-saveDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first save did not continue after eligibility callback")
	}
	if err := s.WithLyricsFirstSaveEligibility(10, func() error {
		t.Fatal("eligibility callback ran for an existing document")
		return nil
	}); !errors.Is(err, ErrLyricsAlreadySaved) {
		t.Fatalf("existing document eligibility error = %v", err)
	}
}

func TestLyricsMutexStripesHandleNonpositiveIDs(t *testing.T) {
	s := setupLyricsStore(t)
	if len(s.lyricsMutexes) != 256 {
		t.Fatalf("lyrics mutex stripe count = %d", len(s.lyricsMutexes))
	}
	for _, musicID := range []int{-1, 0, 1, -256, 256} {
		stripe := lyricsMutexStripe(musicID)
		if stripe < 0 || stripe >= lyricsMutexStripeCount {
			t.Fatalf("musicID=%d stripe=%d", musicID, stripe)
		}
		unlock := s.lockLyrics(musicID)
		unlock()
	}
	if lyricsMutexStripe(-1) != 255 || lyricsMutexStripe(-256) != 0 || lyricsMutexStripe(0) != 0 {
		t.Fatalf("nonpositive stripes: -1=%d -256=%d 0=%d", lyricsMutexStripe(-1), lyricsMutexStripe(-256), lyricsMutexStripe(0))
	}
}

func TestSameSongSavePublishUnpublishAreSerialized(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*Store, model.SongLyrics) error
		mutate  func(*Store, model.SongLyrics) error
	}{
		{name: "publish", mutate: func(s *Store, saved model.SongLyrics) error {
			_, err := s.PublishLyrics(saved.MusicID, saved.Revision)
			return err
		}},
		{name: "unpublish", prepare: func(s *Store, saved model.SongLyrics) error {
			_, err := s.PublishLyrics(saved.MusicID, saved.Revision)
			return err
		}, mutate: func(s *Store, saved model.SongLyrics) error {
			_, err := s.UnpublishLyrics(saved.MusicID, saved.Revision)
			return err
		}},
		{name: "save", mutate: func(s *Store, saved model.SongLyrics) error {
			candidate := saved
			candidate.Lines = append([]model.LyricLine(nil), saved.Lines...)
			candidate.Lines[0].Segments = append([]model.LyricSegment(nil), saved.Lines[0].Segments...)
			candidate.Lines[0].English = "Serialized edit"
			_, err := s.SaveLyrics(candidate, "editor")
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := setupLyricsStore(t)
			saved, err := s.SaveLyrics(validLyrics(), "editor")
			if err != nil {
				t.Fatal(err)
			}
			if test.prepare != nil {
				if err := test.prepare(s, saved); err != nil {
					t.Fatal(err)
				}
			}

			unlock := s.lockLyrics(saved.MusicID)
			started := make(chan struct{})
			finished := make(chan error, 1)
			go func() {
				close(started)
				finished <- test.mutate(s, saved)
			}()
			<-started
			select {
			case err := <-finished:
				unlock()
				t.Fatalf("same-song %s completed while stripe was locked: %v", test.name, err)
			case <-time.After(100 * time.Millisecond):
			}
			unlock()
			select {
			case err := <-finished:
				if err != nil {
					t.Fatalf("same-song %s failed after stripe unlock: %v", test.name, err)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("same-song %s remained blocked after stripe unlock", test.name)
			}
		})
	}
}

func TestDifferentSongMutationsCanAcquireLocksConcurrently(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Store, model.SongLyrics) error
	}{
		{name: "save", mutate: func(s *Store, saved model.SongLyrics) error {
			candidate := saved
			candidate.Lines = append([]model.LyricLine(nil), saved.Lines...)
			candidate.Lines[0].Segments = append([]model.LyricSegment(nil), saved.Lines[0].Segments...)
			candidate.Lines[0].English = "Different-song edit"
			_, err := s.SaveLyrics(candidate, "editor")
			return err
		}},
		{name: "publish", mutate: func(s *Store, saved model.SongLyrics) error {
			_, err := s.PublishLyrics(saved.MusicID, saved.Revision)
			return err
		}},
		{name: "unpublish", mutate: func(s *Store, saved model.SongLyrics) error {
			if _, err := s.PublishLyrics(saved.MusicID, saved.Revision); err != nil {
				return err
			}
			_, err := s.UnpublishLyrics(saved.MusicID, saved.Revision)
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := setupLyricsStore(t)
			first := validLyrics()
			if _, err := s.SaveLyrics(first, "editor"); err != nil {
				t.Fatal(err)
			}
			second := validLyrics()
			second.MusicID = 20
			second.Lines[0].ID = "line-20"
			secondSaved, err := s.SaveLyrics(second, "editor")
			if err != nil {
				t.Fatal(err)
			}
			if lyricsMutexStripe(first.MusicID) == lyricsMutexStripe(second.MusicID) {
				t.Fatal("test music IDs unexpectedly share a mutex stripe")
			}

			unlock := s.lockLyrics(first.MusicID)
			finished := make(chan error, 1)
			go func() { finished <- test.mutate(s, secondSaved) }()
			select {
			case err := <-finished:
				unlock()
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				unlock()
				t.Fatalf("different-song %s blocked on unrelated stripe", test.name)
			}
		})
	}
}

func TestGetLyricsRejectsCorruptPerformerJSON(t *testing.T) {
	s := setupLyricsStore(t)
	if _, err := s.SaveLyrics(validLyrics(), "editor"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE song_lyric_segments SET performer_ids_json='not-json' WHERE music_id=10`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetLyrics(10); err == nil || !strings.Contains(err.Error(), "lyrics segment performers") {
		t.Fatalf("corrupt performer JSON error=%v", err)
	}
}

func TestGetLyricsUsesOneSQLiteSnapshot(t *testing.T) {
	s := setupLyricsStore(t)
	saved, err := s.SaveLyrics(validLyrics(), "editor")
	if err != nil {
		t.Fatal(err)
	}

	readStarted := make(chan struct{})
	allowRead := make(chan struct{})
	readResult := make(chan model.SongLyrics, 1)
	readErr := make(chan error, 1)
	go func() {
		loaded, err := s.getLyricsSnapshot(saved.MusicID, func() {
			close(readStarted)
			<-allowRead
		})
		if err != nil {
			readErr <- err
			return
		}
		readResult <- loaded
	}()
	<-readStarted

	updated := saved
	updated.Lines = append([]model.LyricLine(nil), saved.Lines...)
	updated.Lines[0].Segments = append([]model.LyricSegment(nil), saved.Lines[0].Segments...)
	updated.Lines[0].English = "Committed after snapshot header"
	updated.Lines[0].Segments = []model.LyricSegment{{Text: updated.Lines[0].Japanese, PerformerIDs: []int{2}}}
	updated, err = s.SaveLyrics(updated, "editor")
	if err != nil {
		t.Fatal(err)
	}
	close(allowRead)

	var snapshot model.SongLyrics
	select {
	case err := <-readErr:
		t.Fatal(err)
	case snapshot = <-readResult:
	case <-time.After(2 * time.Second):
		t.Fatal("snapshot read did not complete")
	}
	if snapshot.Revision != saved.Revision || snapshot.Lines[0].English != saved.Lines[0].English ||
		len(snapshot.Lines[0].Segments) != 2 || snapshot.Lines[0].Segments[0].PerformerIDs[0] != 1 {
		t.Fatalf("snapshot mixed revisions: %+v", snapshot)
	}
	latest, err := s.GetLyrics(saved.MusicID)
	if err != nil || latest.Revision != updated.Revision || latest.Lines[0].English != updated.Lines[0].English ||
		len(latest.Lines[0].Segments) != 1 || latest.Lines[0].Segments[0].PerformerIDs[0] != 2 {
		t.Fatalf("latest lyrics=%+v err=%v", latest, err)
	}
}

func TestConcurrentLyricsSaveFromSameRevisionHasOneWinner(t *testing.T) {
	s := setupLyricsStore(t)
	saved, err := s.SaveLyrics(validLyrics(), "editor")
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, english := range []string{"First concurrent edit", "Second concurrent edit"} {
		wg.Add(1)
		go func(english string) {
			defer wg.Done()
			candidate := saved
			candidate.Lines = append([]model.LyricLine(nil), saved.Lines...)
			candidate.Lines[0].Segments = append([]model.LyricSegment(nil), saved.Lines[0].Segments...)
			candidate.Lines[0].English = english
			<-start
			_, err := s.SaveLyrics(candidate, "editor")
			results <- err
		}(english)
	}
	close(start)
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
			continue
		}
		var contractErr *LyricsContractError
		if errors.As(err, &contractErr) && contractErr.Code == "revision_conflict" {
			conflicts++
			continue
		}
		t.Fatalf("unexpected concurrent save error = %v", err)
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent save results successes=%d conflicts=%d", successes, conflicts)
	}
	loaded, err := s.GetLyrics(saved.MusicID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != saved.Revision+1 {
		t.Fatalf("concurrent save revision = %d", loaded.Revision)
	}
}

func TestLyricsValidationCodes(t *testing.T) {
	s := setupLyricsStore(t)

	emptySource := validLyrics()
	emptySource.Lines[0].Japanese = ""
	emptySource.Lines[0].Segments = []model.LyricSegment{{Text: "", PerformerIDs: []int{1}}}
	_, err := s.SaveLyrics(emptySource, "editor")
	var contractErr *LyricsContractError
	if !errors.As(err, &contractErr) || contractErr.Code != "segment_mismatch" || len(contractErr.Details) == 0 ||
		!strings.Contains(contractErr.Details[0], ".japanese must not be empty") {
		t.Fatalf("empty Japanese source error = %#v", err)
	}

	mismatch := validLyrics()
	mismatch.Lines[0].Segments[1].Text = "不一致"
	_, err = s.SaveLyrics(mismatch, "editor")
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

	missingAttribution := validLyrics()
	missingAttribution.MusicID = 20
	missingAttribution.Attribution = ""
	saved, err = s.SaveLyrics(missingAttribution, "editor")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.PublishLyrics(missingAttribution.MusicID, saved.Revision)
	if !errors.As(err, &contractErr) || contractErr.Code != "incomplete_publication" {
		t.Fatalf("missing attribution publication error = %#v", err)
	}
}

func TestLyricsValidationRejectsOversizedFields(t *testing.T) {
	s := setupLyricsStore(t)
	oversized := validLyrics()
	oversized.Lines[0].English = strings.Repeat("x", maxLyricsLineTextBytes+1)
	_, err := s.SaveLyrics(oversized, "editor")
	var contractErr *LyricsContractError
	if !errors.As(err, &contractErr) || contractErr.Code != "segment_mismatch" {
		t.Fatalf("oversized line error = %#v", err)
	}

	oversized = validLyrics()
	oversized.SourceURL = "https://example.invalid/" + strings.Repeat("x", maxLyricsURLBytes)
	_, err = s.SaveLyrics(oversized, "editor")
	if !errors.As(err, &contractErr) || contractErr.Code != "segment_mismatch" {
		t.Fatalf("oversized URL error = %#v", err)
	}
}

func TestLyricsPublicationRequiresPerformerAndFreezesProvenance(t *testing.T) {
	s := setupLyricsStore(t)
	input := validLyrics()
	input.SourcePageID = 10
	input.SourceRevisionID = 20
	input.SourceSHA1 = validSourceSHA1
	input.SourceFetchedAt = "2026-07-22T12:00:00Z"
	input.Lines[0].Segments[0].PerformerIDs = nil
	saved, _, err := s.SaveImportedLyricsMutation(input, "editor")
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
	drift.SourceSHA1 = "1123456789abcdef0123456789abcdef01234567"
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

func TestLyricsSourceURLRejectsUnsafeSchemesAndCredentials(t *testing.T) {
	s := setupLyricsStore(t)
	for _, sourceURL := range []string{"javascript:alert(1)", "data:text/html,unsafe", "https://user:secret@example.invalid/source"} {
		input := validLyrics()
		input.SourceURL = sourceURL
		_, err := s.SaveLyrics(input, "editor")
		var contractErr *LyricsContractError
		if !errors.As(err, &contractErr) || contractErr.Code != "source_drift" {
			t.Fatalf("source URL %q error = %#v", sourceURL, err)
		}
	}
}

func TestLyricsSourceProvenanceRejectsMalformedSHA1(t *testing.T) {
	tests := []struct {
		name       string
		sourceSHA1 string
	}{
		{name: "short", sourceSHA1: "0123456789abcdef0123456789abcdef0123456"},
		{name: "uppercase", sourceSHA1: "0123456789abcdef0123456789abcdef0123456A"},
		{name: "nonhex", sourceSHA1: "0123456789abcdef0123456789abcdef0123456g"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := setupLyricsStore(t)
			input := validLyrics()
			input.SourcePageID = 123
			input.SourceRevisionID = 456
			input.SourceSHA1 = test.sourceSHA1
			input.SourceFetchedAt = "2026-07-22T12:34:56Z"
			_, _, err := s.SaveImportedLyricsMutation(input, "editor")
			var contractErr *LyricsContractError
			if !errors.As(err, &contractErr) || contractErr.Code != "source_drift" || len(contractErr.Details) == 0 ||
				!strings.Contains(contractErr.Details[0], "40 lowercase hexadecimal") {
				t.Fatalf("sourceSha1=%q error = %#v", test.sourceSHA1, err)
			}
		})
	}
}

func TestRestoreRejectsMalformedLyricsSourceSHA1(t *testing.T) {
	s := setupLyricsStore(t)
	input := validLyrics()
	input.SourcePageID = 123
	input.SourceRevisionID = 456
	input.SourceSHA1 = validSourceSHA1
	input.SourceFetchedAt = "2026-07-22T12:34:56Z"
	if _, _, err := s.SaveImportedLyricsMutation(input, "editor"); err != nil {
		t.Fatal(err)
	}
	exported, err := s.ExportLyricsContent()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		sourceSHA1 string
	}{
		{name: "short", sourceSHA1: "0123456789abcdef0123456789abcdef0123456"},
		{name: "uppercase", sourceSHA1: "0123456789abcdef0123456789abcdef0123456A"},
		{name: "nonhex", sourceSHA1: "0123456789abcdef0123456789abcdef0123456g"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			malformed := exported
			malformed.Documents = append([]LyricsDocumentBackupRecord(nil), exported.Documents...)
			malformed.Documents[0].SourceSHA1 = test.sourceSHA1
			restored := setupLyricsStore(t)
			err := restored.ImportTranslationContent(nil, EventContentExport{}, malformed)
			var contractErr *LyricsContractError
			if !errors.As(err, &contractErr) || contractErr.Code != "source_drift" || len(contractErr.Details) == 0 ||
				!strings.Contains(contractErr.Details[0], "40 lowercase hexadecimal") {
				t.Fatalf("malformed restored sourceSha1 error = %v", err)
			}
		})
	}
}

func TestLyricsSourceProvenanceRoundTrip(t *testing.T) {
	s := setupLyricsStore(t)
	input := validLyrics()
	input.SourcePageID = 123
	input.SourceRevisionID = 456
	input.SourceSHA1 = validSourceSHA1
	input.SourceFetchedAt = "2026-07-22T12:34:56Z"
	saved, _, err := s.SaveImportedLyricsMutation(input, "editor")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := s.GetLyrics(saved.MusicID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SourcePageID != 123 || loaded.SourceRevisionID != 456 || loaded.SourceSHA1 != validSourceSHA1 || loaded.SourceFetchedAt != input.SourceFetchedAt {
		t.Fatalf("source provenance = %+v", loaded)
	}

	exported, err := s.ExportLyricsContent()
	if err != nil {
		t.Fatal(err)
	}
	restored := setupLyricsStore(t)
	if err := restored.ImportTranslationContent(nil, EventContentExport{}, exported); err != nil {
		t.Fatalf("restore rejected canonical source SHA1: %v", err)
	}
	restoredLyrics, err := restored.GetLyrics(saved.MusicID)
	if err != nil {
		t.Fatal(err)
	}
	if restoredLyrics.SourceSHA1 != validSourceSHA1 || restoredLyrics.SourceFetchedAt != input.SourceFetchedAt {
		t.Fatalf("restored provenance = %+v", restoredLyrics)
	}

	invalid := validLyrics()
	invalid.SourcePageID = 123
	_, err = s.SaveLyrics(invalid, "editor")
	var contractErr *LyricsContractError
	if !errors.As(err, &contractErr) || contractErr.Code != "source_drift" {
		t.Fatalf("partial source provenance error = %#v", err)
	}
}

func TestExportLyricsContentUsesOneSQLiteSnapshot(t *testing.T) {
	s := setupLyricsStore(t)
	saved, err := s.SaveLyrics(validLyrics(), "editor")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishLyrics(saved.MusicID, saved.Revision); err != nil {
		t.Fatal(err)
	}

	exportStarted := make(chan struct{})
	allowExport := make(chan struct{})
	exportResult := make(chan LyricsContentExport, 1)
	exportErr := make(chan error, 1)
	go func() {
		exported, err := s.exportLyricsContentSnapshot(context.Background(), func() {
			close(exportStarted)
			<-allowExport
		})
		if err != nil {
			exportErr <- err
			return
		}
		exportResult <- exported
	}()
	<-exportStarted

	updated := saved
	updated.Lines = append([]model.LyricLine(nil), saved.Lines...)
	updated.Lines[0].Segments = []model.LyricSegment{{Text: updated.Lines[0].Japanese, PerformerIDs: []int{2}}}
	updated.Lines[0].English = "Published after export snapshot"
	updated, err = s.SaveLyrics(updated, "editor")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishLyrics(updated.MusicID, updated.Revision); err != nil {
		t.Fatal(err)
	}
	close(allowExport)

	var snapshot LyricsContentExport
	select {
	case err := <-exportErr:
		t.Fatal(err)
	case snapshot = <-exportResult:
	case <-time.After(2 * time.Second):
		t.Fatal("lyrics export snapshot did not complete")
	}
	if len(snapshot.Documents) != 1 || snapshot.Documents[0].Revision != saved.Revision ||
		len(snapshot.Lines) != 1 || snapshot.Lines[0].English != saved.Lines[0].English ||
		len(snapshot.Segments) != 2 || snapshot.Segments[0].PerformerIDsJSON != "[1]" ||
		len(snapshot.Publications) != 1 || snapshot.Publications[0].Revision != saved.Revision ||
		!strings.Contains(snapshot.Publications[0].PayloadJSON, `"en-US":"Miku sings"`) {
		t.Fatalf("export mixed revisions: %+v", snapshot)
	}
	fresh, err := s.ExportLyricsContent()
	if err != nil || fresh.Documents[0].Revision != updated.Revision || fresh.Lines[0].English != updated.Lines[0].English ||
		len(fresh.Segments) != 1 || fresh.Segments[0].PerformerIDsJSON != "[2]" || fresh.Publications[0].Revision != updated.Revision {
		t.Fatalf("fresh export=%+v err=%v", fresh, err)
	}
	restored := setupLyricsStore(t)
	if err := restored.ImportTranslationContent(nil, EventContentExport{}, snapshot); err != nil {
		t.Fatalf("snapshot export did not restore coherently: %v", err)
	}
	loaded, err := restored.GetLyrics(saved.MusicID)
	if err != nil || loaded.Revision != saved.Revision || loaded.Lines[0].English != saved.Lines[0].English {
		t.Fatalf("restored snapshot=%+v err=%v", loaded, err)
	}
}

func TestLegacyPublicationLineIDsAreCanonicalizedForPublicReadAndRestore(t *testing.T) {
	s := setupLyricsStore(t)
	input := validLyrics()
	input.Lines[0].ID = "wiki-123-456-1"
	saved, err := s.SaveLyrics(input, "editor")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishLyrics(saved.MusicID, saved.Revision); err != nil {
		t.Fatal(err)
	}
	var payload string
	if err := s.db.QueryRow(`SELECT payload_json FROM song_lyrics_publications WHERE music_id=?`, saved.MusicID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	legacyPayload := strings.Replace(payload, `"id":"line-1"`, `"id":"wiki-123-456-1"`, 1)
	if legacyPayload == payload {
		t.Fatalf("could not construct legacy publication payload: %s", payload)
	}
	if _, err := s.db.Exec(`UPDATE song_lyrics_publications SET payload_json=? WHERE music_id=?`, legacyPayload, saved.MusicID); err != nil {
		t.Fatal(err)
	}

	_, details, err := s.PublishedLyrics()
	if err != nil {
		t.Fatal(err)
	}
	if got := details[saved.MusicID].Lines[0].ID; got != "line-1" {
		t.Fatalf("legacy public line ID = %q", got)
	}
	exported, err := s.ExportLyricsContent()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(exported.Publications[0].PayloadJSON, `wiki-123-456-1`) {
		t.Fatalf("test export lost the simulated legacy ID: %s", exported.Publications[0].PayloadJSON)
	}
	restored := setupLyricsStore(t)
	if err := restored.ImportTranslationContent(nil, EventContentExport{}, exported); err != nil {
		t.Fatal(err)
	}
	var restoredPayload string
	if err := restored.db.QueryRow(`SELECT payload_json FROM song_lyrics_publications WHERE music_id=?`, saved.MusicID).Scan(&restoredPayload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(restoredPayload, `wiki-123-456-1`) || !strings.Contains(restoredPayload, `"id":"line-1"`) {
		t.Fatalf("restored publication retained private line identity: %s", restoredPayload)
	}
}

func TestLyricsSourceProvenanceRejectsNonpositiveFetchedAt(t *testing.T) {
	for _, fetchedAt := range []string{"1970-01-01T00:00:00Z", "1969-12-31T23:59:59Z"} {
		t.Run(fetchedAt, func(t *testing.T) {
			s := setupLyricsStore(t)
			input := validLyrics()
			input.SourcePageID = 123
			input.SourceRevisionID = 456
			input.SourceSHA1 = validSourceSHA1
			input.SourceFetchedAt = fetchedAt
			_, _, err := s.SaveImportedLyricsMutation(input, "editor")
			var contractErr *LyricsContractError
			if !errors.As(err, &contractErr) || contractErr.Code != "source_drift" {
				t.Fatalf("sourceFetchedAt=%q error = %#v", fetchedAt, err)
			}
		})
	}
}

func TestLyricsSourceFetchedAtIsCanonicalBeforeDriftComparison(t *testing.T) {
	s := setupLyricsStore(t)
	input := validLyrics()
	input.SourcePageID = 123
	input.SourceRevisionID = 456
	input.SourceSHA1 = validSourceSHA1
	input.SourceFetchedAt = "2026-07-22T20:34:56.900+08:00"
	saved, _, err := s.SaveImportedLyricsMutation(input, "editor")
	if err != nil {
		t.Fatal(err)
	}
	if saved.SourceFetchedAt != "2026-07-22T12:34:56Z" {
		t.Fatalf("sourceFetchedAt = %q", saved.SourceFetchedAt)
	}
	saved.Lines[0].English = "Canonical retry"
	if _, err := s.SaveLyrics(saved, "editor"); err != nil {
		t.Fatalf("canonical retry reported drift: %v", err)
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

func TestRestoreRejectsInvalidLyricsDraftWithoutChangingStoredContent(t *testing.T) {
	s := setupLyricsStore(t)
	saved, err := s.SaveLyrics(validLyrics(), "editor")
	if err != nil {
		t.Fatal(err)
	}
	invalid := LyricsContentExport{
		Music:      []CatalogMusicBackupRecord{{MusicID: 10, TitleJA: "新曲", NewlyWritten: 1}},
		Performers: []CatalogPerformerBackupRecord{{PerformerID: 1, NameJA: "初音ミク"}},
		Documents:  []LyricsDocumentBackupRecord{{MusicID: 10, Revision: 1, UpdatedAt: 1, SourceHash: saved.Lines[0].ID}},
		Lines:      []LyricsLineBackupRecord{{MusicID: 10, LineID: "line-1", Japanese: "", Chinese: "歌唱", English: "Sings"}},
		Segments:   []LyricsSegmentBackupRecord{{MusicID: 10, LineID: "line-1", Text: "", PerformerIDsJSON: "[1]"}},
	}
	if err := s.ImportTranslationContent(nil, EventContentExport{}, invalid); err == nil {
		t.Fatal("invalid lyrics draft unexpectedly restored")
	}
	loaded, err := s.GetLyrics(10)
	if err != nil || loaded.Revision != saved.Revision || loaded.Lines[0].Japanese != saved.Lines[0].Japanese {
		t.Fatalf("failed restore changed stored lyrics: %+v err=%v", loaded, err)
	}
}

func TestRestoreRejectsInvalidLyricsPublication(t *testing.T) {
	s := setupLyricsStore(t)
	lyrics := LyricsContentExport{
		Music:      []CatalogMusicBackupRecord{{MusicID: 10, TitleJA: "新曲", NewlyWritten: 1}},
		Performers: []CatalogPerformerBackupRecord{{PerformerID: 1, NameJA: "初音ミク"}},
		Documents:  []LyricsDocumentBackupRecord{{MusicID: 10, Revision: 1, UpdatedAt: 1, SourceHash: "hash"}},
		Lines:      []LyricsLineBackupRecord{{MusicID: 10, LineID: "line-1", Japanese: "歌う", Chinese: "歌唱", English: "Sings"}},
		Segments:   []LyricsSegmentBackupRecord{{MusicID: 10, LineID: "line-1", Text: "歌う", PerformerIDsJSON: "[1]"}},
		Publications: []LyricsPublicationBackupRecord{{
			MusicID: 10, Revision: 1, UpdatedAt: 1,
			PayloadJSON: `{"version":1,"musicId":10,"revision":1,"updatedAt":"1970-01-01T00:00:01Z","lines":[]}`,
		}},
	}
	if err := s.ImportTranslationContent(nil, EventContentExport{}, lyrics); err == nil {
		t.Fatal("invalid public lyrics snapshot unexpectedly restored")
	}
	if _, err := s.GetLyrics(10); err != ErrLyricsNotFound {
		t.Fatalf("failed restore changed lyrics: %v", err)
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
