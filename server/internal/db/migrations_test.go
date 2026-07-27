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
	if version != 10 || migrationCount != 10 || checksum != migrations[9].checksum() {
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
	var scenarioTable int
	if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='event_story_scenarios'`).Scan(&scenarioTable); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if scenarioTable != 1 {
		database.Close()
		t.Fatal("v8 scenario side table was not created")
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
	if got := before.Mode().Perm(); got != 0o600 {
		t.Fatalf("pre-migration backup mode=%#o want %#o", got, os.FileMode(0o600))
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

func TestMigrationRetryRefreshesBackupAfterLegacyWrite(t *testing.T) {
	path := legacyFixtureCopy(t, "retry-refresh.db")
	migrationBeforeCommitHook = func(version int) error {
		return errors.New("injected first migration failure")
	}
	t.Cleanup(func() { migrationBeforeCommitHook = nil })
	if _, err := Open(path); err == nil {
		t.Fatal("first migration unexpectedly succeeded")
	}

	legacy, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`UPDATE entries SET cn_text='written-after-failure' WHERE jp_key='旧キー'`); err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	migrationBeforeCommitHook = nil
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	backup, err := sql.Open("sqlite", "file:"+path+".pre-migration-v1.bak?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	var text string
	if err := backup.QueryRow(`SELECT cn_text FROM entries WHERE jp_key='旧キー'`).Scan(&text); err != nil {
		t.Fatal(err)
	}
	if text != "written-after-failure" {
		t.Fatalf("retry backup retained stale value %q", text)
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

func TestMigrationHistoryGapRefusesStartup(t *testing.T) {
	path := legacyFixtureCopy(t, "migration-gap.db")
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`DELETE FROM schema_migrations WHERE version=3`); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "not a contiguous prefix") {
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
	if _, err := legacy.Exec(`INSERT INTO event_story_scenarios(event_id, episode_no, scenario_id, canonical_json, sha256)
		VALUES (7, '1', 'legacy-scenario', '{"ScenarioId":"legacy-scenario","Snippets":[],"TalkData":[],"SpecialEffectData":[],"AppearCharacters":[]}',
		'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')`); err != nil {
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
	var scenarios int
	if err := legacy.QueryRow(`SELECT COUNT(*) FROM event_story_scenarios WHERE event_id=7`).Scan(&scenarios); err != nil {
		t.Fatal(err)
	}
	if scenarios != 1 {
		t.Fatal("legacy event replace cascaded into scenario snapshots")
	}
}

func TestV9MigrationRetainsExistingSegmentsAndAllowsRecoveryIdentityAtSamePosition(t *testing.T) {
	path := legacyFixtureCopy(t, "v9-segment-recovery.db")
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(ON)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	database := &DB{DB: raw, path: path}
	if err := database.applyMigrations(migrations[:8]); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	var originalID, episodeNo, scenarioID, kind, jpKey, sourceText, sourceHash string
	var eventID, position int
	if err := raw.QueryRow(`SELECT segment_id, event_id, episode_no, scenario_id, kind, position, jp_key, source_text, source_hash
		FROM event_story_segments WHERE kind='talk'`).Scan(&originalID, &eventID, &episodeNo, &scenarioID,
		&kind, &position, &jpKey, &sourceText, &sourceHash); err != nil {
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
	recoveryID := originalID + ":recovery"
	if _, err := migrated.Exec(`INSERT INTO event_story_segments
		(segment_id, event_id, episode_no, scenario_id, kind, position, jp_key, source_text, source_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, recoveryID, eventID, episodeNo, "replacement-scenario", kind,
		position, jpKey, sourceText, sourceHash); err != nil {
		t.Fatalf("same-position recovery segment after v9: %v", err)
	}
	var count int
	if err := migrated.QueryRow(`SELECT COUNT(*) FROM event_story_segments WHERE segment_id IN (?, ?)`,
		originalID, recoveryID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("v9 segment preservation count=%d err=%v", count, err)
	}
}

func TestV10MigrationCreatesDurableLyricsDiscoveryQueue(t *testing.T) {
	path := legacyFixtureCopy(t, "v10-lyrics-discovery-queue.db")
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(ON)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	database := &DB{DB: raw, path: path}
	if err := database.applyMigrations(migrations[:9]); err != nil {
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
	var version int
	if err := migrated.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != 10 {
		t.Fatalf("migration version=%d err=%v", version, err)
	}
	if _, err := migrated.Exec(`INSERT INTO lyrics_discovery_jobs
		(idempotency_key, kind, state, music_id, attempts, max_attempts, next_attempt_at, created_at, updated_at, version)
		VALUES (?, 'discover', 'queued', 1, 0, 3, 1, 1, 1, 1)`, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := migrated.Exec(`INSERT INTO lyrics_discovery_jobs
		(idempotency_key, kind, state, music_id, attempts, max_attempts, next_attempt_at, created_at, updated_at, version)
		VALUES (?, 'unsupported', 'queued', 2, 0, 3, 1, 1, 1, 1)`, strings.Repeat("b", 64)); err == nil {
		t.Fatal("v10 queue accepted unsupported job kind")
	}
	if _, err := migrated.Exec(`INSERT INTO lyrics_discovery_jobs
		(idempotency_key, kind, state, music_id, attempts, max_attempts, next_attempt_at, created_at, updated_at, version)
		VALUES (?, 'fetch_revision', 'queued', 2, 0, 3, 1, 1, 1, 1)`, strings.Repeat("c", 64)); err == nil {
		t.Fatal("v10 queue accepted incomplete fetch_revision target")
	}
	if _, err := migrated.Exec(`INSERT INTO lyrics_discovery_jobs
		(idempotency_key, kind, state, music_id, attempts, max_attempts, next_attempt_at, lease_owner, created_at, updated_at, version)
		VALUES (?, 'discover', 'queued', 3, 0, 3, 1, 'orphan_owner', 1, 1, 1)`, strings.Repeat("d", 64)); err == nil {
		t.Fatal("v10 queue accepted lease owner outside leased state")
	}
}

func TestV8MigrationCreatesDedicatedPreMigrationBackup(t *testing.T) {
	path := legacyFixtureCopy(t, "v8-backup.db")
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(ON)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	database := &DB{DB: raw, path: path}
	if err := database.applyMigrations(migrations[:7]); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	reopened.Close()
	backupPath := path + ".pre-migration-v8.bak"
	if err := verifySQLiteBackup(backupPath); err != nil {
		t.Fatalf("v8 pre-migration backup: %v", err)
	}
	backup, err := sql.Open("sqlite", "file:"+backupPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	var version int
	if err := backup.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != 7 {
		t.Fatalf("v8 backup version=%d err=%v", version, err)
	}
	var scenarioTable int
	if err := backup.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='event_story_scenarios'`).Scan(&scenarioTable); err != nil || scenarioTable != 0 {
		t.Fatalf("v8 backup scenario table=%d err=%v", scenarioTable, err)
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

func TestTalkIdentityMigrationKeepsFilteredFieldOpaqueAndPreservesLocalization(t *testing.T) {
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
	if newID != oldID+":legacy" || newHash != oldHash || text != "Existing English talk" {
		t.Fatalf("migrated talk id=%q hash=%q text=%q oldID=%q oldHash=%q", newID, newHash, text, oldID, oldHash)
	}
}

func TestAttributionAndTokenGenerationMigrationRequiresRepublish(t *testing.T) {
	path := legacyFixtureCopy(t, "attribution-token-version.db")
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(ON)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	database := &DB{DB: raw, path: path}
	if err := database.applyMigrations(migrations[:6]); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO catalog_music(music_id, title_ja, producer_metadata) VALUES (10, 'song', 'producer')`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO song_lyrics(music_id, revision, updated_at, source_hash) VALUES (10, 1, 1, 'hash')`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO song_lyrics_publications(music_id, revision, updated_at, payload_json)
		VALUES (10, 1, 1, '{"version":1,"musicId":10,"revision":1,"updatedAt":"1970-01-01T00:00:01Z","lines":[]}')`); err != nil {
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
	var attribution string
	if err := migrated.QueryRow(`SELECT attribution FROM song_lyrics WHERE music_id=10`).Scan(&attribution); err != nil {
		t.Fatal(err)
	}
	var tokenVersion, publications int
	if err := migrated.QueryRow(`SELECT token_version FROM users LIMIT 1`).Scan(&tokenVersion); err != nil {
		t.Fatal(err)
	}
	if err := migrated.QueryRow(`SELECT COUNT(*) FROM song_lyrics_publications`).Scan(&publications); err != nil {
		t.Fatal(err)
	}
	if attribution != "" || tokenVersion != 1 || publications != 0 {
		t.Fatalf("migration attribution=%q tokenVersion=%d publications=%d", attribution, tokenVersion, publications)
	}
}
