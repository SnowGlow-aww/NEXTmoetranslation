package model

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPublicLyricsContractFixturesMatchProducerModels(t *testing.T) {
	root := filepath.Join("..", "..", "..", "contracts", "public-lyrics", "v1")
	indexBody := readContractFile(t, filepath.Join(root, "index.fixture.json"))
	var index PublicLyricsIndex
	if err := json.Unmarshal(indexBody, &index); err != nil {
		t.Fatal(err)
	}
	if index.Version != 1 || len(index.Songs) != 1 || index.Songs[0].MusicID != 10 {
		t.Fatalf("index fixture = %+v", index)
	}

	detailBody := readContractFile(t, filepath.Join(root, "detail.fixture.json"))
	var detail PublicSongLyrics
	if err := json.Unmarshal(detailBody, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Version != 1 || detail.MusicID != 10 || detail.Attribution == "" || len(detail.Lines) != 1 {
		t.Fatalf("detail fixture = %+v", detail)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(detailBody, &fields); err != nil {
		t.Fatal(err)
	}
	for _, privateField := range []string{
		"sourceNote", "licenseNote", "sourceUrl", "sourcePageId", "sourceRevisionId",
		"sourceSha1", "sourceFetchedAt", "updatedBy", "status",
	} {
		if _, ok := fields[privateField]; ok {
			t.Fatalf("detail fixture exposes private field %q", privateField)
		}
	}

	for _, schema := range []string{"index.schema.json", "detail.schema.json"} {
		var document map[string]any
		if err := json.Unmarshal(readContractFile(t, filepath.Join(root, schema)), &document); err != nil {
			t.Fatalf("%s: %v", schema, err)
		}
		if document["$schema"] == nil || document["properties"] == nil {
			t.Fatalf("%s is missing schema metadata", schema)
		}
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
