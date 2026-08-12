package db

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func TestV28EmbeddedLyricsEditorSeedLedgerSchemaAndImmutability(t *testing.T) {
	path := t.TempDir() + "/embedded-editor-seed-v28.db"
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	for _, object := range []struct {
		kind string
		name string
	}{
		{"table", "embedded_lyrics_editor_seed_batches"},
		{"table", "embedded_lyrics_editor_seed_items"},
		{"index", "idx_embedded_lyrics_editor_seed_items_music"},
		{"index", "idx_embedded_lyrics_editor_seed_items_availability"},
		{"trigger", "embedded_lyrics_editor_seed_batches_immutable_update"},
		{"trigger", "embedded_lyrics_editor_seed_batches_immutable_delete"},
		{"trigger", "embedded_lyrics_editor_seed_items_immutable_update"},
		{"trigger", "embedded_lyrics_editor_seed_items_immutable_delete"},
	} {
		var count int
		if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type=? AND name=?`, object.kind, object.name).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("v28 %s %s count=%d", object.kind, object.name, count)
		}
	}
	if _, err := database.Exec(`INSERT INTO catalog_music(music_id,title_ja,lyrics_catalog_fingerprint,lyrics_catalog_policy_version)
		VALUES (1,'試験曲',?,'catalog-identity-v2')`, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	seedSHA, archiveSHA := strings.Repeat("b", 64), strings.Repeat("c", 64)
	if _, err := database.Exec(`INSERT INTO embedded_lyrics_editor_seed_batches
		(seed_sha256,archive_sha256,release_id,schema_version,source_batch_sha256,root_sha256,catalog_policy_version,
		 catalog_count,music_ids_sha256,catalog_fingerprints_sha256,created_at)
		VALUES (?,?, 'release-v1',1,?,?,'catalog-identity-v2',1,?,?,1)`, seedSHA, archiveSHA,
		strings.Repeat("d", 64), strings.Repeat("e", 64), strings.Repeat("f", 64), strings.Repeat("1", 64)); err != nil {
		t.Fatal(err)
	}
	availabilityJSON := `{"schemaVersion":1,"state":"satisfied_no_lyrics","noLyricsReason":"catalog_instrumental","fixedIdentities":[]}`
	if _, err := database.Exec(`INSERT INTO embedded_lyrics_editor_seed_items
		(seed_sha256,music_id,japanese_title,catalog_fingerprint,state,seed_kind,apply_status,result_sha256,
		 source_document_sha256,availability_schema_version,reason_code,no_lyrics_reason,availability_document_json,
		 availability_document_sha256,created_at)
		VALUES (?,1,'試験曲',?,'satisfied_no_lyrics','availability','inserted',?,'',1,'','catalog_instrumental',?,?,1)`,
		seedSHA, strings.Repeat("a", 64), strings.Repeat("2", 64), availabilityJSON, strings.Repeat("3", 64)); err != nil {
		t.Fatal(err)
	}
	for name, statement := range map[string]string{
		"batch update": `UPDATE embedded_lyrics_editor_seed_batches SET release_id='changed'`,
		"batch delete": `DELETE FROM embedded_lyrics_editor_seed_batches`,
		"item update":  `UPDATE embedded_lyrics_editor_seed_items SET japanese_title='changed'`,
		"item delete":  `DELETE FROM embedded_lyrics_editor_seed_items`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := database.Exec(statement); err == nil || !strings.Contains(err.Error(), "immutable") {
				t.Fatalf("v28 %s error=%v", name, err)
			}
		})
	}
	if _, err := database.Exec(`DELETE FROM catalog_music WHERE music_id=1`); err == nil {
		t.Fatal("v28 availability ownership allowed catalog deletion")
	}
	rows, err := database.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("v28 embedded editor seed ledger has a foreign-key violation")
	}
}

func TestV28EmbeddedLyricsEditorSeedLedgerRejectsInvalidAvailabilityShape(t *testing.T) {
	path := t.TempDir() + "/embedded-editor-seed-v28-invalid.db"
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec(`INSERT INTO catalog_music(music_id,title_ja,lyrics_catalog_fingerprint,lyrics_catalog_policy_version)
		VALUES (1,'試験曲',?,'catalog-identity-v2')`, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	seedSHA := strings.Repeat("b", 64)
	if _, err := database.Exec(`INSERT INTO embedded_lyrics_editor_seed_batches
		(seed_sha256,archive_sha256,release_id,schema_version,source_batch_sha256,root_sha256,catalog_policy_version,
		 catalog_count,music_ids_sha256,catalog_fingerprints_sha256,created_at)
		VALUES (?,?, 'release-v1',1,?,?,'catalog-identity-v2',1,?,?,1)`, seedSHA, strings.Repeat("c", 64),
		strings.Repeat("d", 64), strings.Repeat("e", 64), strings.Repeat("f", 64), strings.Repeat("1", 64)); err != nil {
		t.Fatal(err)
	}
	for name, values := range map[string]struct {
		state, kind, sourceSHA, document string
	}{
		"availability with source document": {"satisfied_no_lyrics", "availability", strings.Repeat("4", 64), `{"schemaVersion":1,"state":"satisfied_no_lyrics"}`},
		"source-v3 with availability JSON":  {"complete", "source_v3", strings.Repeat("4", 64), `{}`},
		"availability state mismatch":       {"incomplete", "availability", "", `{"schemaVersion":1,"state":"failed"}`},
	} {
		t.Run(name, func(t *testing.T) {
			availabilityVersion, reason, noLyrics, documentSHA := 0, "", "", ""
			if values.kind == "availability" {
				availabilityVersion, noLyrics, documentSHA = 1, "catalog_instrumental", strings.Repeat("5", 64)
			}
			_, err := database.Exec(`INSERT INTO embedded_lyrics_editor_seed_items
				(seed_sha256,music_id,japanese_title,catalog_fingerprint,state,seed_kind,apply_status,result_sha256,
				 source_document_sha256,availability_schema_version,reason_code,no_lyrics_reason,availability_document_json,
				 availability_document_sha256,created_at)
				VALUES (?,1,'試験曲',?,?,?,'inserted',?,?,?,?,?,?,?,1)`, seedSHA, strings.Repeat("a", 64), values.state,
				values.kind, strings.Repeat("2", 64), values.sourceSHA, availabilityVersion, reason, noLyrics, values.document, documentSHA)
			if err == nil {
				t.Fatal("v28 accepted invalid embedded editor seed item")
			}
		})
	}
}

func TestV28EmbeddedLyricsEditorSeedMigrationFailureRollsBack(t *testing.T) {
	path := legacyFixtureCopy(t, "embedded-editor-seed-v28-rollback.db")
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(ON)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	database := &DB{DB: raw, path: path}
	if err := database.applyMigrations(migrations[:27]); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected v28 migration failure")
	migrationBeforeCommitHook = func(version int) error {
		if version == 28 {
			return injected
		}
		return nil
	}
	t.Cleanup(func() { migrationBeforeCommitHook = nil })
	if err := database.applyMigrations(migrations[27:28]); err == nil || !strings.Contains(err.Error(), injected.Error()) {
		t.Fatalf("v28 migration error=%v", err)
	}
	migrationBeforeCommitHook = nil
	var version, tables int
	if err := raw.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != 27 {
		t.Fatalf("v28 rollback version=%d err=%v", version, err)
	}
	if err := raw.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name LIKE 'embedded_lyrics_editor_seed_%'`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("v28 rollback tables=%d err=%v", tables, err)
	}
}
