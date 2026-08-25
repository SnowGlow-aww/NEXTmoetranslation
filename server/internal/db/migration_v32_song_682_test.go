package db

import (
	"context"
	"database/sql"
	"testing"
)

func TestV32Song682TranslationEditionsMigration(t *testing.T) {
	path := t.TempDir() + "/song682-migration-v32.db"
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(ON)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	database := &DB{DB: raw, path: path}
	if err := database.applySchema(); err != nil {
		t.Fatal(err)
	}
	if err := database.applyMigrations(migrations[:31]); err != nil {
		t.Fatal(err)
	}
	// Seed catalog_music for song 682
	if _, err := raw.Exec(`INSERT INTO catalog_music(music_id,title_ja,title_zh,title_en) VALUES (682,'あなたしか見えないの','眼中仅有你一人','Anata Shika Mienai no')`); err != nil {
		t.Fatal(err)
	}
	// Seed legacy song_lyrics and publication for song 682
	if _, err := raw.Exec(`INSERT INTO song_lyrics(music_id,revision,updated_at,translation_credit)
		VALUES (682,8,1724544000,'@雪莹ちゃん')`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO song_lyrics_publications(music_id,revision,updated_at,payload_json)
		VALUES (682,8,1724544000,'{}')`); err != nil {
		t.Fatal(err)
	}

	// Apply migration 32
	if err := database.applyMigrations(migrations[31:32]); err != nil {
		t.Fatalf("apply migration 32 failed: %v", err)
	}

	// Check that legacy records are removed
	var legacyLyricsCount, publicationCount int
	if err := raw.QueryRow(`SELECT (SELECT COUNT(*) FROM song_lyrics WHERE music_id=682),
		(SELECT COUNT(*) FROM song_lyrics_publications WHERE music_id=682)`).Scan(&legacyLyricsCount, &publicationCount); err != nil {
		t.Fatal(err)
	}
	if legacyLyricsCount != 0 || publicationCount != 0 {
		t.Fatalf("legacy records not cleaned up: lyrics=%d pubs=%d", legacyLyricsCount, publicationCount)
	}

	// Check source document and editions
	var docCount, edCount, lineCount int
	if err := raw.QueryRow(`SELECT
		(SELECT COUNT(*) FROM song_lyrics_source_documents WHERE music_id=682),
		(SELECT COUNT(*) FROM song_lyrics_translation_editions WHERE document_id=(SELECT document_id FROM song_lyrics_source_documents WHERE music_id=682)),
		(SELECT COUNT(*) FROM song_lyrics_translation_edition_lines WHERE document_id=(SELECT document_id FROM song_lyrics_source_documents WHERE music_id=682))`,
	).Scan(&docCount, &edCount, &lineCount); err != nil {
		t.Fatal(err)
	}
	if docCount != 1 {
		t.Fatalf("source document count=%d want 1", docCount)
	}
	if edCount != 2 {
		t.Fatalf("translation edition count=%d want 2", edCount)
	}
	if lineCount != 66 {
		t.Fatalf("translation edition lines count=%d want 66 (33 x 2)", lineCount)
	}

	// Check runtime invariants
	if err := database.ensureRuntimeInvariants(context.Background()); err != nil {
		t.Fatalf("runtime invariants failed: %v", err)
	}
}
