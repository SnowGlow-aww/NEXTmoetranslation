package store

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestPublicLyricsV4ContractFixturesValidateStrictSchemas(t *testing.T) {
	root := filepath.Join("..", "..", "..", "contracts", "public-lyrics", "v4")
	indexSchema := compilePublicLyricsV3Schema(t, "index.schema.json", root)
	detailSchema := compilePublicLyricsV3Schema(t, "detail.schema.json", root)
	validatePublicLyricsV3JSON(t, indexSchema, readPublicLyricsV3Contract(t, filepath.Join(root, "index.fixture.json")))
	for _, name := range []string{
		"detail-default-only.fixture.json",
		"detail-multi-edition.fixture.json",
		"detail-exact-projection.fixture.json",
	} {
		validatePublicLyricsV3JSON(t, detailSchema, readPublicLyricsV3Contract(t, filepath.Join(root, name)))
	}
}

func TestPublicLyricsV4SchemaRejectsLocalizationInsideSourceFacts(t *testing.T) {
	root := filepath.Join("..", "..", "..", "contracts", "public-lyrics", "v4")
	detailSchema := compilePublicLyricsV3Schema(t, "detail.schema.json", root)
	var document map[string]any
	if err := json.Unmarshal(readPublicLyricsV3Contract(t, filepath.Join(root, "detail-default-only.fixture.json")), &document); err != nil {
		t.Fatal(err)
	}
	line := document["renditions"].([]any)[0].(map[string]any)["full"].(map[string]any)["lines"].([]any)[0].(map[string]any)
	line["zh-CN"] = "禁止"
	if err := detailSchema.Validate(document); err == nil {
		t.Fatal("Public v4 schema accepted zh-CN inside source line")
	}
}
