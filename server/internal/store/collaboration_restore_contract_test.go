package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"moesekai/server/internal/model"
)

func seedCollaborationLedger(t *testing.T, s *Store, musicID int, epoch int64) {
	t.Helper()
	if _, err := s.db.Exec(`INSERT INTO lyrics_collab_documents
		(music_id,schema_version,epoch,update_v1,base_revision,authority_sha256,updated_at)
		VALUES (?,1,?,?,0,?,?)`, musicID, epoch, []byte{1}, strings.Repeat("a", 64), time.Now().UTC().Unix()); err != nil {
		t.Fatal(err)
	}
}

func collaborationRestoreFixture(t *testing.T, s *Store) (map[string]model.Category, []EntryLocalizationRecord, EventContentExport, LyricsContentExport) {
	t.Helper()
	categories := make(map[string]model.Category, len(model.SupportedCategories))
	for _, category := range model.SupportedCategories {
		categories[category] = model.Category{}
	}
	entries, err := s.ExportEntryLocalizations()
	if err != nil {
		t.Fatal(err)
	}
	events, err := s.ExportEventContent()
	if err != nil {
		t.Fatal(err)
	}
	lyrics, err := s.ExportLyricsContent()
	if err != nil {
		t.Fatal(err)
	}
	return categories, entries, events, lyrics
}

func collaborationEpoch(t *testing.T, s *Store, musicID int) int64 {
	t.Helper()
	var epoch int64
	if err := s.db.QueryRow(`SELECT epoch FROM lyrics_collab_documents WHERE music_id=?`, musicID).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	return epoch
}

func TestBackupRestoreFencesCollaborationEpochInsideTheContentTransaction(t *testing.T) {
	s := setupLyricsStore(t)
	seedCollaborationLedger(t, s, 10, 7)
	categories, entries, events, lyrics := collaborationRestoreFixture(t, s)

	if err := s.RestoreBackupContext(context.Background(), categories, nil, entries, events, lyrics, true, "operator"); err != nil {
		t.Fatal(err)
	}
	if epoch := collaborationEpoch(t, s, 10); epoch != 8 {
		t.Fatalf("collaboration epoch after restore = %d, want 8", epoch)
	}
	var catalogRows int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM catalog_music WHERE music_id=10`).Scan(&catalogRows); err != nil || catalogRows != 1 {
		t.Fatalf("catalog replacement lost music 10: rows=%d err=%v", catalogRows, err)
	}
}

func TestFailedBackupRestoreRollsBackCollaborationEpoch(t *testing.T) {
	s := setupLyricsStore(t)
	seedCollaborationLedger(t, s, 10, 11)
	categories, entries, events, lyrics := collaborationRestoreFixture(t, s)
	lyrics.Music[0].LyricsCatalogFingerprint = strings.Repeat("0", 64)

	if err := s.RestoreBackupContext(context.Background(), categories, nil, entries, events, lyrics, true, "operator"); err == nil {
		t.Fatal("malformed restore unexpectedly succeeded")
	}
	if epoch := collaborationEpoch(t, s, 10); epoch != 11 {
		t.Fatalf("failed restore advanced collaboration epoch to %d", epoch)
	}
}
