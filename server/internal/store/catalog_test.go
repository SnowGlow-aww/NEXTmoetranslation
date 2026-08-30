package store

import (
	"path/filepath"
	"sync/atomic"
	"testing"

	"moesekai/server/internal/db"
)

func openCatalogStore(t *testing.T) (*Store, *db.DB) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return New(database), database
}

func TestApplyCNCategoryWithCatalogNotifiesForCatalogOnlyChanges(t *testing.T) {
	s, _ := openCatalogStore(t)
	var notifications atomic.Int32
	s.OnChange(func() { notifications.Add(1) })

	music := []MusicCatalogRecord{{MusicID: 41, JapaneseTitle: "合成試験曲", IsNewlyWrittenMusic: true}}
	updated, err := s.ApplyCNCategoryWithCatalog("music", map[string]CNApplyField{}, music, nil)
	if err != nil {
		t.Fatal(err)
	}
	if updated != 0 || notifications.Load() != 1 {
		t.Fatalf("catalog-only apply updated=%d notifications=%d", updated, notifications.Load())
	}
	if _, err := s.ApplyCNCategoryWithCatalog("music", map[string]CNApplyField{}, music, nil); err != nil {
		t.Fatal(err)
	}
	if notifications.Load() != 1 {
		t.Fatalf("identical catalog reapply notifications=%d", notifications.Load())
	}

	performers := []PerformerCatalogRecord{{PerformerID: 7, JapaneseName: "合成歌唱者"}}
	if _, err := s.ApplyCNCategoryWithCatalog("characters", map[string]CNApplyField{}, nil, performers); err != nil {
		t.Fatal(err)
	}
	if notifications.Load() != 2 {
		t.Fatalf("performer-only apply notifications=%d", notifications.Load())
	}
}

func TestApplyCNCategoryWithCatalogRollsBackPerformerFailure(t *testing.T) {
	s, database := openCatalogStore(t)
	if _, err := database.Exec(`CREATE TRIGGER fail_performer_catalog_insert BEFORE INSERT ON catalog_performers
		WHEN NEW.performer_id=8 BEGIN SELECT RAISE(ABORT, 'performer insert failed'); END`); err != nil {
		t.Fatal(err)
	}
	fields := map[string]CNApplyField{
		"hobby": {Pairs: map[string]string{"合成趣味": "合成爱好"}},
	}
	performers := []PerformerCatalogRecord{
		{PerformerID: 7, JapaneseName: "合成歌唱者一"},
		{PerformerID: 8, JapaneseName: "合成歌唱者二"},
	}
	var notifications atomic.Int32
	s.OnChange(func() { notifications.Add(1) })

	if _, err := s.ApplyCNCategoryWithCatalog("characters", fields, nil, performers); err == nil {
		t.Fatal("performer catalog failure unexpectedly committed")
	}
	var entries, catalog int
	if err := database.QueryRow(`SELECT COUNT(*) FROM entries WHERE category='characters'`).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM catalog_performers WHERE performer_id IN (7, 8)`).Scan(&catalog); err != nil {
		t.Fatal(err)
	}
	if entries != 0 || catalog != 0 || notifications.Load() != 0 {
		t.Fatalf("failed performer apply entries=%d catalog=%d notifications=%d", entries, catalog, notifications.Load())
	}
}
