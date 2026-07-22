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
	var version int
	var checksum string
	if err := database.QueryRow(`SELECT version, checksum FROM schema_migrations`).Scan(&version, &checksum); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if version != 1 || checksum != migrations[0].checksum() {
		database.Close()
		t.Fatalf("migration record version=%d checksum=%q", version, checksum)
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
}
