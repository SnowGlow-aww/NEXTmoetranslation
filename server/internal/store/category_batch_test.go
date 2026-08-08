package store

import (
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"moesekai/server/internal/db"
	"moesekai/server/internal/model"
)

func setupCategoryBatchStore(t *testing.T) (*Store, *db.DB) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "category-batch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	s := New(database)
	if _, err := s.ImportCategory("cards", model.Category{
		"prefix": {
			"first":  {Text: "第一", Source: model.SourceCN, Ids: []string{"1"}},
			"second": {Text: "第二", Source: model.SourceHuman, Ids: []string{"2"}},
		},
		"name": {"third": {Text: "第三", Source: model.SourcePinned}},
	}); err != nil {
		t.Fatal(err)
	}
	return s, database
}

func TestCategorySnapshotAndBatchAreCompleteAtomicAndAudited(t *testing.T) {
	s, database := setupCategoryBatchStore(t)
	snapshot, err := s.CategorySnapshotLocale("cards", model.LocaleEnglish)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Revision == "" || len(snapshot.Fields) != 2 || len(snapshot.Fields["prefix"]) != 2 || len(snapshot.Fields["name"]) != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if snapshot.Fields["prefix"][0].Text != "" || snapshot.Fields["prefix"][0].Source != model.SourceUnknown {
		t.Fatalf("initial localized row = %+v", snapshot.Fields["prefix"][0])
	}

	var notifications atomic.Int32
	s.OnChange(func() { notifications.Add(1) })
	result, err := s.UpdateCategoryLocale("cards", model.LocaleEnglish, snapshot.Revision, "editor", []model.CategoryEntryUpdate{
		{Field: "prefix", Key: "first", Text: "First", Source: model.SourceHuman},
		{Field: "name", Key: "third", Text: "Third", Source: model.SourceHuman},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.Revision == snapshot.Revision || len(result.Changed) != 2 || notifications.Load() != 1 {
		t.Fatalf("result=%+v notifications=%d", result, notifications.Load())
	}
	var audits int
	if err := database.QueryRow(`SELECT COUNT(*) FROM audit_log
		WHERE action='entry.locale.update' AND user='editor' AND detail LIKE '%batch=true'`).Scan(&audits); err != nil || audits != 2 {
		t.Fatalf("audits=%d err=%v", audits, err)
	}

	if _, err := s.UpdateCategoryLocale("cards", model.LocaleEnglish, snapshot.Revision, "stale", []model.CategoryEntryUpdate{
		{Field: "prefix", Key: "second", Text: "stale", Source: model.SourceHuman},
	}); !errors.Is(err, ErrCategoryRevisionConflict) {
		t.Fatalf("stale error = %v", err)
	}
	if _, err := s.UpdateCategoryLocale("cards", model.LocaleEnglish, result.Snapshot.Revision, "editor", []model.CategoryEntryUpdate{
		{Field: "prefix", Key: "second", Text: "must roll back", Source: model.SourceHuman},
		{Field: "missing", Key: "unknown", Text: "invalid", Source: model.SourceHuman},
	}); !errors.Is(err, ErrEntryIdentityConflict) {
		t.Fatalf("identity error = %v", err)
	}
	text, err := s.EntryTextLocale("cards", "prefix", "second", model.LocaleEnglish)
	if err != nil || text != "" || notifications.Load() != 1 {
		t.Fatalf("failed batch persisted text=%q notifications=%d err=%v", text, notifications.Load(), err)
	}
}

func TestCategoryBatchReportsOnlyActualChangesForCompleteAndPartialNoOps(t *testing.T) {
	s, database := setupCategoryBatchStore(t)
	snapshot, err := s.CategorySnapshotLocale("cards", model.LocaleEnglish)
	if err != nil {
		t.Fatal(err)
	}
	var notifications atomic.Int32
	s.OnChange(func() { notifications.Add(1) })

	noOp, err := s.UpdateCategoryLocale("cards", model.LocaleEnglish, snapshot.Revision, "editor", []model.CategoryEntryUpdate{
		{Field: "prefix", Key: "first", Text: "", Source: model.SourceUnknown},
	})
	if err != nil {
		t.Fatal(err)
	}
	if noOp.Snapshot.Revision != snapshot.Revision || len(noOp.Changed) != 0 || notifications.Load() != 0 {
		t.Fatalf("complete no-op result=%+v notifications=%d", noOp, notifications.Load())
	}
	var audits int
	if err := database.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE detail LIKE '%batch=true'`).Scan(&audits); err != nil || audits != 0 {
		t.Fatalf("complete no-op audits=%d err=%v", audits, err)
	}

	partial, err := s.UpdateCategoryLocale("cards", model.LocaleEnglish, snapshot.Revision, "editor", []model.CategoryEntryUpdate{
		{Field: "prefix", Key: "first", Text: "First", Source: model.SourceHuman},
		{Field: "prefix", Key: "second", Text: "", Source: model.SourceUnknown},
	})
	if err != nil {
		t.Fatal(err)
	}
	if partial.Snapshot.Revision == snapshot.Revision || len(partial.Changed) != 1 ||
		partial.Changed[0].Key != "first" || notifications.Load() != 1 {
		t.Fatalf("partial no-op result=%+v notifications=%d", partial, notifications.Load())
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE detail LIKE '%batch=true'`).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("partial no-op audits=%d err=%v", audits, err)
	}
}

func TestCategoryBatchAuditFailureRollsBackEveryEntry(t *testing.T) {
	s, database := setupCategoryBatchStore(t)
	snapshot, err := s.CategorySnapshotLocale("cards", model.LocaleChinese)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TRIGGER fail_category_batch_audit BEFORE INSERT ON audit_log
		WHEN NEW.detail LIKE '%batch=true' BEGIN SELECT RAISE(ABORT, 'audit failed'); END`); err != nil {
		t.Fatal(err)
	}
	var notifications atomic.Int32
	s.OnChange(func() { notifications.Add(1) })
	_, err = s.UpdateCategoryLocale("cards", model.LocaleChinese, snapshot.Revision, "editor", []model.CategoryEntryUpdate{
		{Field: "prefix", Key: "first", Text: "changed one", Source: model.SourceHuman},
		{Field: "prefix", Key: "second", Text: "changed two", Source: model.SourceHuman},
	})
	if err == nil {
		t.Fatal("audit failure unexpectedly committed")
	}
	category, err := s.CategoryData("cards")
	if err != nil {
		t.Fatal(err)
	}
	if category["prefix"]["first"].Text != "第一" || category["prefix"]["second"].Text != "第二" || notifications.Load() != 0 {
		t.Fatalf("partial batch survived: %+v notifications=%d", category["prefix"], notifications.Load())
	}
}

func TestConcurrentCategoryBatchesAcceptExactlyOneBaseRevision(t *testing.T) {
	s, _ := setupCategoryBatchStore(t)
	snapshot, err := s.CategorySnapshotLocale("cards", model.LocaleChinese)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errorsCh := make(chan error, 2)
	var wg sync.WaitGroup
	for _, text := range []string{"winner-a", "winner-b"} {
		text := text
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := s.UpdateCategoryLocale("cards", model.LocaleChinese, snapshot.Revision, text, []model.CategoryEntryUpdate{{
				Field: "prefix", Key: "first", Text: text, Source: model.SourceHuman,
			}})
			errorsCh <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errorsCh)
	succeeded, conflicted := 0, 0
	for err := range errorsCh {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrCategoryRevisionConflict):
			conflicted++
		default:
			t.Fatalf("unexpected race error: %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("succeeded=%d conflicted=%d", succeeded, conflicted)
	}
}
