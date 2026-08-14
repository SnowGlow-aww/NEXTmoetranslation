package db

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func TestV30LyricsTranslationEditionsIsAdditiveAndEnforcesIdentity(t *testing.T) {
	path := t.TempDir() + "/lyrics-translation-editions-v30.db"
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(ON)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	database := &DB{DB: raw, path: path}
	if err := database.applySchema(); err != nil {
		t.Fatal(err)
	}
	if err := database.applyMigrations(migrations[:29]); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO catalog_music(music_id,title_ja) VALUES (1,'試験曲')`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO song_lyrics_source_documents
		(document_id,music_id,schema_version,reason_code,document_json,document_sha256,manifest_batch_sha256,created_at)
		VALUES (1,1,3,'','{}',?, ?,1)`, hex64("a"), hex64("b")); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO song_lyrics_rendition_localizations
		(document_id,rendition_key,locale,updated_at,updated_by,revision)
		VALUES (1,'sekai','zh-CN',10,'tester',2)`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO song_lyrics_rendition_translation_lines
		(document_id,rendition_key,locale,position,text) VALUES (1,'sekai','zh-CN',0,'旧默认译文')`); err != nil {
		t.Fatal(err)
	}
	if err := database.applyMigrations(migrations[29:30]); err != nil {
		t.Fatal(err)
	}
	var states, editions int
	if err := raw.QueryRow(`SELECT (SELECT COUNT(*) FROM song_lyrics_translation_edition_state),
		(SELECT COUNT(*) FROM song_lyrics_translation_editions)`).Scan(&states, &editions); err != nil {
		t.Fatal(err)
	}
	if states != 0 || editions != 0 {
		t.Fatalf("v30 eagerly materialized state=%d editions=%d", states, editions)
	}
	var primary string
	if err := raw.QueryRow(`SELECT text FROM song_lyrics_rendition_translation_lines WHERE document_id=1`).Scan(&primary); err != nil {
		t.Fatal(err)
	}
	if primary != "旧默认译文" {
		t.Fatalf("v30 rewrote legacy mirror=%q", primary)
	}
	if _, err := raw.Exec(`INSERT INTO song_lyrics_translation_editions
		(document_id,edition_key,label,created_at,created_by) VALUES (1,'main','默认译本',10,'tester')`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO song_lyrics_translation_edition_state
		(document_id,default_edition_key,revision,updated_at,updated_by) VALUES (1,'main',2,10,'tester')`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO song_lyrics_translation_edition_localizations
		(document_id,edition_key,rendition_key,locale,updated_at,updated_by)
		VALUES (1,'main','sekai','zh-CN',10,'tester')`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO song_lyrics_translation_edition_lines
		(document_id,edition_key,rendition_key,side,locale,position,text)
		VALUES (1,'main','sekai','full','zh-CN',0,'默认译文')`); err != nil {
		t.Fatal(err)
	}
	for name, statement := range map[string]string{
		"uppercase key":       `INSERT INTO song_lyrics_translation_editions(document_id,edition_key,label,created_at) VALUES (1,'Bad','标签',10)`,
		"trim-unstable label": `INSERT INTO song_lyrics_translation_editions(document_id,edition_key,label,created_at) VALUES (1,'bad',' 标签 ',10)`,
		"invalid locale":      `INSERT INTO song_lyrics_translation_edition_localizations(document_id,edition_key,rendition_key,locale,updated_at) VALUES (1,'main','other','en-US',10)`,
		"orphan default":      `INSERT INTO song_lyrics_translation_edition_state(document_id,default_edition_key,revision,updated_at) VALUES (2,'main',1,10)`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := raw.Exec(statement); err == nil {
				t.Fatal("invalid v30 row was accepted")
			}
		})
	}
	if err := ValidateLyricsTranslationEditionSchema(context.Background(), raw, true, "test"); err != nil {
		t.Fatal(err)
	}
	assertNoForeignKeyViolations(t, raw)
}

func TestValidateLyricsTranslationEditionSchemaRejectsAlteredLedgerAndIndex(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate string
		want   string
	}{
		{name: "ledger", mutate: `UPDATE schema_migrations SET checksum='tampered' WHERE version=30`, want: "ledger is invalid"},
		{name: "index", mutate: `DROP INDEX idx_song_lyrics_translation_edition_lines_lookup`, want: "index"},
	} {
		t.Run(test.name, func(t *testing.T) {
			database, err := Open(t.TempDir() + "/runtime-v30.db")
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			if _, err := database.Exec(test.mutate); err != nil {
				t.Fatal(err)
			}
			err = ValidateLyricsTranslationEditionSchema(context.Background(), database, true, "test")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("runtime v30 validation error=%v want %q", err, test.want)
			}
		})
	}
}
