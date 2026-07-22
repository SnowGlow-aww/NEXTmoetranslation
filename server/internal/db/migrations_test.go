package db

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func legacyFixtureCopy(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := copyFixture(filepath.Join("testdata", "legacy-v2.db"), path); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMigrationIsTransactionalIdempotentAndBackedUp(t *testing.T) {
	path := legacyFixtureCopy(t, "production.db")
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var version, migrationCount int
	var checksum string
	if err := database.QueryRow(`SELECT version, checksum FROM schema_migrations ORDER BY version DESC LIMIT 1`).Scan(&version, &checksum); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&migrationCount); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if version != 6 || migrationCount != 6 || checksum != migrations[5].checksum() {
		database.Close()
		t.Fatalf("migration record version=%d count=%d checksum=%q", version, migrationCount, checksum)
	}
	var segments, localized int
	if err := database.QueryRow(`SELECT COUNT(*) FROM event_story_segments`).Scan(&segments); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM event_story_segment_localizations WHERE locale='zh-CN'`).Scan(&localized); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if segments != 2 || localized != 2 {
		database.Close()
		t.Fatalf("backfill segments=%d localized=%d", segments, localized)
	}
	var talkHash, titleSource string
	if err := database.QueryRow(`SELECT source_hash FROM event_story_segments WHERE kind='talk'`).Scan(&talkHash); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT source_text FROM event_story_segments WHERE kind='title'`).Scan(&titleSource); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if talkHash == "" || titleSource != "" {
		database.Close()
		t.Fatalf("source backfill hash=%q titleSource=%q", talkHash, titleSource)
	}
	var titleSegmentID string
	if err := database.QueryRow(`SELECT segment_id FROM event_story_segments WHERE kind='title'`).Scan(&titleSegmentID); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if !strings.HasSuffix(titleSegmentID, ":title:-1") {
		database.Close()
		t.Fatalf("migrated title segment ID = %q", titleSegmentID)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	backupPath := path + ".pre-migration-v1.bak"
	before, err := os.Stat(backupPath)
	if err != nil {
		t.Fatalf("pre-migration backup missing: %v", err)
	}
	if err := verifySQLiteBackup(backupPath); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	after, err := os.Stat(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Fatal("idempotent reopen rewrote the pre-migration backup")
	}
}

func TestMigrationFailureRollsBackAndLeavesRecoverableBackup(t *testing.T) {
	path := legacyFixtureCopy(t, "failure.db")
	migrationBeforeCommitHook = func(version int) error {
		return errors.New("injected migration failure")
	}
	t.Cleanup(func() { migrationBeforeCommitHook = nil })
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "injected migration failure") {
		t.Fatalf("Open error = %v", err)
	}
	migrationBeforeCommitHook = nil

	raw, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var tableCount int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'`).Scan(&tableCount); err != nil {
		t.Fatal(err)
	}
	if tableCount != 0 {
		t.Fatal("failed migration left schema_migrations behind")
	}
	var text string
	if err := raw.QueryRow(`SELECT cn_text FROM entries WHERE jp_key='旧キー'`).Scan(&text); err != nil {
		t.Fatal(err)
	}
	if text != "旧翻译" {
		t.Fatalf("legacy data changed to %q", text)
	}
	if err := verifySQLiteBackup(path + ".pre-migration-v1.bak"); err != nil {
		t.Fatal(err)
	}
}

func TestInterruptedOrCorruptMigrationBackupRecovers(t *testing.T) {
	path := legacyFixtureCopy(t, "backup-recovery.db")
	backupPath := path + ".pre-migration-v1.bak"
	if err := os.WriteFile(backupPath, []byte("interrupted backup"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backupPath+".tmp", []byte("stale temporary backup"), 0o644); err != nil {
		t.Fatal(err)
	}
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	database.Close()
	if err := verifySQLiteBackup(backupPath); err != nil {
		t.Fatalf("replacement backup is not recoverable: %v", err)
	}
	if _, err := os.Stat(backupPath + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary backup remains after recovery: %v", err)
	}
}

func TestMigrationBackupRenameFailureKeepsExistingFinal(t *testing.T) {
	path := legacyFixtureCopy(t, "backup-rename-failure.db")
	backupPath := path + ".pre-migration-v1.bak"
	corrupt := []byte("existing interrupted final")
	if err := os.WriteFile(backupPath, corrupt, 0o644); err != nil {
		t.Fatal(err)
	}
	migrationBackupBeforeRenameHook = func(string) error { return errors.New("injected rename interruption") }
	t.Cleanup(func() { migrationBackupBeforeRenameHook = nil })
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "injected rename interruption") {
		t.Fatalf("Open error = %v", err)
	}
	migrationBackupBeforeRenameHook = nil
	got, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(corrupt) {
		t.Fatalf("failed atomic replacement changed final backup to %q", got)
	}
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	database.Close()
	if err := verifySQLiteBackup(backupPath); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationChecksumMismatchRefusesStartup(t *testing.T) {
	path := legacyFixtureCopy(t, "checksum.db")
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE schema_migrations SET checksum='tampered' WHERE version=1`); err != nil {
		t.Fatal(err)
	}
	database.Close()
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("Open error = %v", err)
	}
}

func TestPreviousBinaryQueriesRemainCompatibleAfterMigration(t *testing.T) {
	path := legacyFixtureCopy(t, "rolling.db")
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	database.Close()

	legacy, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	if _, err := legacy.Exec(`UPDATE entries SET cn_text='rolling-old-binary', source='human' WHERE jp_key='旧キー'`); err != nil {
		t.Fatalf("legacy writer failed after additive migration: %v", err)
	}
	var got string
	if err := legacy.QueryRow(`SELECT cn_text FROM entries WHERE category='cards' AND field='prefix' AND jp_key='旧キー'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "rolling-old-binary" {
		t.Fatalf("legacy query got %q", got)
	}
	if _, err := legacy.Exec(`INSERT INTO event_story_segment_localizations
		(segment_id, locale, text, source, updated_at, updated_by, revision)
		SELECT segment_id, 'en-US', 'manual English', 'human', 1, 'editor', 1
		FROM event_story_segments WHERE event_id=7 AND kind='talk'`); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`DELETE FROM event_stories WHERE event_id=7`); err != nil {
		t.Fatalf("legacy event replace delete failed: %v", err)
	}
	var localized int
	if err := legacy.QueryRow(`SELECT COUNT(*) FROM event_story_segment_localizations WHERE locale='en-US' AND text='manual English'`).Scan(&localized); err != nil {
		t.Fatal(err)
	}
	if localized != 1 {
		t.Fatal("legacy event replace cascaded into additive locale content")
	}
}

func TestTitleIdentityMigrationMovesExistingLocalizations(t *testing.T) {
	path := legacyFixtureCopy(t, "title-identity.db")
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(ON)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	database := &DB{DB: raw, path: path}
	if err := database.applyMigrations(migrations[:4]); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	var oldID string
	if err := raw.QueryRow(`SELECT segment_id FROM event_story_segments WHERE kind='title'`).Scan(&oldID); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO event_story_segment_localizations
		(segment_id, locale, text, source, updated_at, updated_by, revision)
		VALUES (?, 'en-US', 'Existing English title', 'human', 1, 'editor', 2)`, oldID); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	var newID, text string
	if err := migrated.QueryRow(`SELECT seg.segment_id, loc.text
		FROM event_story_segments seg JOIN event_story_segment_localizations loc ON loc.segment_id=seg.segment_id
		WHERE seg.kind='title' AND loc.locale='en-US'`).Scan(&newID, &text); err != nil {
		t.Fatal(err)
	}
	if newID != oldID+":-1" || text != "Existing English title" {
		t.Fatalf("migrated title id=%q text=%q old=%q", newID, text, oldID)
	}
}

func TestTalkIdentityMigrationMovesMatchingLocalizationsAndHash(t *testing.T) {
	path := legacyFixtureCopy(t, "talk-identity.db")
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(ON)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	database := &DB{DB: raw, path: path}
	if err := database.applyMigrations(migrations[:5]); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	var oldID, oldHash string
	if err := raw.QueryRow(`SELECT segment_id, source_hash FROM event_story_segments WHERE kind='talk'`).Scan(&oldID, &oldHash); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO event_story_segment_localizations
		(segment_id, locale, text, source, updated_at, updated_by, revision)
		VALUES (?, 'en-US', 'Existing English talk', 'human', 1, 'editor', 2)`, oldID); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	var newID, newHash, text string
	if err := migrated.QueryRow(`SELECT seg.segment_id, seg.source_hash, loc.text
		FROM event_story_segments seg JOIN event_story_segment_localizations loc ON loc.segment_id=seg.segment_id
		WHERE seg.kind='talk' AND loc.locale='en-US'`).Scan(&newID, &newHash, &text); err != nil {
		t.Fatal(err)
	}
	if newID != oldID+":body" || newHash != oldHash || text != "Existing English talk" {
		t.Fatalf("migrated talk id=%q hash=%q text=%q oldID=%q oldHash=%q", newID, newHash, text, oldID, oldHash)
	}
}
