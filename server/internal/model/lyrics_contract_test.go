package model_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"moesekai/server/internal/db"
	"moesekai/server/internal/files"
	"moesekai/server/internal/model"
	"moesekai/server/internal/store"
)

func TestPublicLyricsFixturesAndGeneratedAssetsMatchSchemasAndBytes(t *testing.T) {
	root := filepath.Join("..", "..", "..", "contracts", "public-lyrics", "v1")
	indexFixture := readContractFile(t, filepath.Join(root, "index.fixture.json"))
	detailFixture := readContractFile(t, filepath.Join(root, "detail.fixture.json"))
	indexSchema := compileJSONSchema(t, "index.schema.json", readContractFile(t, filepath.Join(root, "index.schema.json")))
	detailSchema := compileJSONSchema(t, "detail.schema.json", readContractFile(t, filepath.Join(root, "detail.schema.json")))
	validateJSONDocument(t, indexSchema, indexFixture)
	validateJSONDocument(t, detailSchema, detailFixture)

	var detail model.PublicSongLyrics
	if err := json.Unmarshal(detailFixture, &detail); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(filepath.Join(t.TempDir(), "public-lyrics-contract.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s := store.New(database)
	es := store.NewEventStore(database)
	if err := s.UpsertMusicCatalog([]store.MusicCatalogRecord{{
		MusicID: detail.MusicID, JapaneseTitle: "新曲", ChineseTitle: "新歌", EnglishTitle: "New Song",
	}}); err != nil {
		t.Fatal(err)
	}
	updatedAt, err := time.Parse(time.RFC3339, detail.UpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO song_lyrics
		(music_id, revision, updated_at, attribution, source_hash) VALUES (?, ?, ?, ?, 'fixture')`,
		detail.MusicID, detail.Revision, updatedAt.Unix(), detail.Attribution); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO song_lyrics_publications
		(music_id, revision, updated_at, payload_json) VALUES (?, ?, ?, ?)`,
		detail.MusicID, detail.Revision, updatedAt.Unix(), string(payload)); err != nil {
		t.Fatal(err)
	}
	assets, err := files.NewGenerator(s, es, "").PublishedLyricsJSON()
	if err != nil {
		t.Fatal(err)
	}
	indexGenerated := assets["translation/lyrics/index.json"]
	detailGenerated := assets["translation/lyrics/music_10.json"]
	if !bytes.Equal(indexGenerated, indexFixture) {
		t.Fatalf("generated index differs from canonical fixture\nwant: %q\ngot:  %q", indexFixture, indexGenerated)
	}
	if !bytes.Equal(detailGenerated, detailFixture) {
		t.Fatalf("generated detail differs from canonical fixture\nwant: %q\ngot:  %q", detailFixture, detailGenerated)
	}
	validateJSONDocument(t, indexSchema, indexGenerated)
	validateJSONDocument(t, detailSchema, detailGenerated)
}

func compileJSONSchema(t *testing.T, name string, document []byte) *jsonschema.Schema {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()
	var resource any
	if err := json.Unmarshal(document, &resource); err != nil {
		t.Fatal(err)
	}
	if err := compiler.AddResource(name, resource); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile(name)
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func validateJSONDocument(t *testing.T, schema *jsonschema.Schema, body []byte) {
	t.Helper()
	var document any
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(document); err != nil {
		t.Fatal(err)
	}
}

func readContractFile(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
