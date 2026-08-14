package db

import (
	"database/sql"
	"testing"
)

func TestV29LyricsTranslationVersionsAddsPeerSideStorageWithoutRewritingPrimaryRows(t *testing.T) {
	path := t.TempDir() + "/lyrics-translation-versions-v29.db"
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(ON)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	database := &DB{DB: raw, path: path}
	if err := database.applySchema(); err != nil {
		t.Fatal(err)
	}
	if err := database.applyMigrations(migrations[:28]); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO catalog_music(music_id,title_ja) VALUES (1,'試験曲')`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO song_lyrics_source_documents
		(document_id,music_id,schema_version,reason_code,document_json,document_sha256,manifest_batch_sha256,created_at)
		VALUES (1,1,1,'untagged_full_only','{}',?, ?,1)`, hex64("a"), hex64("b")); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO song_lyrics_rendition_localizations
		(document_id,rendition_key,locale,updated_at,updated_by,revision)
		VALUES (1,'sekai','zh-CN',1,'tester',2)`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO song_lyrics_rendition_translation_lines
		(document_id,rendition_key,locale,position,text) VALUES (1,'sekai','zh-CN',0,'原有 Full 译文')`); err != nil {
		t.Fatal(err)
	}
	if err := database.applyMigrations(migrations[28:29]); err != nil {
		t.Fatal(err)
	}
	var primary string
	if err := raw.QueryRow(`SELECT text FROM song_lyrics_rendition_translation_lines
		WHERE document_id=1 AND rendition_key='sekai' AND locale='zh-CN' AND position=0`).Scan(&primary); err != nil {
		t.Fatal(err)
	}
	if primary != "原有 Full 译文" {
		t.Fatalf("primary translation=%q", primary)
	}
	if _, err := raw.Exec(`INSERT INTO song_lyrics_rendition_side_translation_lines
		(document_id,rendition_key,side,locale,position,text)
		VALUES (1,'sekai','game','zh-CN',0,'独立 Game 译文')`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO song_lyrics_rendition_side_translation_lines
		(document_id,rendition_key,side,locale,position,text)
		VALUES (1,'sekai','invalid','zh-CN',1,'bad')`); err == nil {
		t.Fatal("v29 accepted an invalid translation side")
	}
	assertNoForeignKeyViolations(t, raw)
}
