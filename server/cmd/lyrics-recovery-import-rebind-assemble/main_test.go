package main

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"moesekai/server/internal/lyricsrootmanifest"
	"moesekai/server/internal/model"
)

func TestOpenRuntimeCatalogValidatesFullEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.sqlite")
	createRuntimeCatalogFixture(t, path, false)
	root := lyricsrootmanifest.Manifest{
		Catalog: lyricsrootmanifest.CatalogBinding{RecordCount: 1},
		Songs:   []lyricsrootmanifest.SongResultRef{{MusicID: 42}},
	}
	catalog, err := openRuntimeCatalog(t.Context(), path, root)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	item := catalog.items[42]
	if item.MusicID != 42 || item.JapaneseTitle != "合成試験曲" || !sha256Pattern.MatchString(item.CatalogFingerprint) {
		t.Fatalf("runtime item=%+v", item)
	}
	if catalog.recordCount != 1 || catalog.schemaVersion != 27 || !sha256Pattern.MatchString(catalog.identitySHA256) ||
		!sha256Pattern.MatchString(catalog.musicIDsSHA256) || !sha256Pattern.MatchString(catalog.sha256) {
		t.Fatalf("runtime binding=%+v", catalog)
	}
}

func TestOpenRuntimeCatalogRejectsStoredFingerprintDrift(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.sqlite")
	createRuntimeCatalogFixture(t, path, true)
	root := lyricsrootmanifest.Manifest{
		Catalog: lyricsrootmanifest.CatalogBinding{RecordCount: 1},
		Songs:   []lyricsrootmanifest.SongResultRef{{MusicID: 42}},
	}
	if catalog, err := openRuntimeCatalog(t.Context(), path, root); err == nil {
		catalog.Close()
		t.Fatal("runtime catalog accepted a fingerprint that did not match its full evidence")
	}
}

func createRuntimeCatalogFixture(t *testing.T, path string, drift bool) {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	for version := 1; version <= 27; version++ {
		if _, err := database.Exec(`INSERT INTO schema_migrations(version) VALUES (?)`, version); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Exec(`CREATE TABLE catalog_music(
		music_id INTEGER PRIMARY KEY,
		title_ja TEXT NOT NULL,
		lyricist TEXT NOT NULL,
		composer TEXT NOT NULL,
		arranger TEXT NOT NULL,
		assetbundle_name TEXT NOT NULL,
		version_hint TEXT NOT NULL,
		lyrics_version TEXT NOT NULL,
		lyrics_evidence_presence_json TEXT NOT NULL,
		vocal_signals_json TEXT NOT NULL,
		lyrics_catalog_fingerprint TEXT NOT NULL,
		lyrics_catalog_policy_version TEXT NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	presence := model.CatalogEvidencePresence{
		Lyricist: true, Composer: true, Arranger: true, Assetbundle: true,
		VersionHint: true, LyricsVersion: true, Vocals: true,
	}
	vocals := []model.CatalogVocalSignal{{VocalID: 1, VocalType: "sekai", CharacterType: "game_character", CharacterID: 21}}
	evidence := model.CatalogLyricsEvidence{
		Title: "合成試験曲", Lyricist: "作詞者", Composer: "作曲者", Arranger: "編曲者",
		Assetbundle: "jacket_s_42", VersionHint: "jacket_s_42", LyricsVersion: "game_size",
		Presence: presence, Vocals: vocals,
	}
	fingerprint, err := model.CatalogLyricsEvidenceFingerprint(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if drift {
		fingerprint = strings.Repeat("f", 64)
	}
	presenceJSON, err := json.Marshal(presence)
	if err != nil {
		t.Fatal(err)
	}
	vocalsJSON, err := json.Marshal(vocals)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO catalog_music VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, 42, "合成試験曲",
		"作詞者", "作曲者", "編曲者", "jacket_s_42", "jacket_s_42", "game_size", string(presenceJSON),
		string(vocalsJSON), fingerprint, model.LyricsCatalogIdentityPolicyVersion); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}
