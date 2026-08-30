package store

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"moesekai/server/internal/db"
	"moesekai/server/internal/model"
)

func TestLegacyOfficialCNSourcePrecedenceGolden(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "legacy-precedence.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s := New(database)
	if _, err := s.ImportCategory("cards", model.Category{
		"prefix": {
			"pinned":         {Text: "锁定人工", Source: model.SourcePinned, Ids: []string{"old"}},
			"human":          {Text: "人工旧值", Source: model.SourceHuman, Ids: []string{"old"}},
			"cn":             {Text: "官方旧值", Source: model.SourceCN},
			"llm":            {Text: "机器旧值", Source: model.SourceLLM},
			"unknown":        {Text: "", Source: model.SourceUnknown},
			"blank-existing": {Text: "机器保留", Source: model.SourceLLM},
		},
	}); err != nil {
		t.Fatal(err)
	}
	hooks := 0
	s.OnChange(func() { hooks++ })
	fields := map[string]CNApplyField{
		"prefix": {
			Pairs: map[string]string{
				"pinned": "官方锁定", "human": "官方人工", "cn": "官方新值",
				"llm": "官方机器", "unknown": "官方未知", "blank-existing": "", "new": "官方新增",
			},
			Trace: map[string][]string{
				"pinned": {"new"}, "human": {"new"}, "new": {"7"},
			},
		},
	}
	updated, err := s.ApplyCNCategory("cards", fields)
	if err != nil {
		t.Fatal(err)
	}
	if updated != 6 {
		t.Fatalf("updated rows = %d, want 6", updated)
	}
	if hooks != 1 {
		t.Fatalf("change hooks = %d, want 1", hooks)
	}

	got, err := s.CategoryData("cards")
	if err != nil {
		t.Fatal(err)
	}
	gotJSON, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "legacy", "source-precedence.json"))
	if err != nil {
		t.Fatal(err)
	}
	want = bytes.TrimSuffix(want, []byte("\n"))
	if !bytes.Equal(gotJSON, want) {
		t.Fatalf("source precedence mismatch\ngot:\n%s\nwant:\n%s", gotJSON, want)
	}

	updated, err = s.ApplyCNCategory("cards", fields)
	if err != nil {
		t.Fatal(err)
	}
	if updated != 0 || hooks != 1 {
		t.Fatalf("identical reapply updated=%d hooks=%d", updated, hooks)
	}
}

func TestLegacyMysekaiTagPropagationOverwritesFlavorSource(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "legacy-mysekai.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s := New(database)
	if _, err := s.ImportCategory("mysekai", model.Category{
		"tag":        {"同じキー": {Text: "", Source: model.SourceUnknown}},
		"flavorText": {"同じキー": {Text: "人工风味文本", Source: model.SourceHuman}},
	}); err != nil {
		t.Fatal(err)
	}
	updated, err := s.ApplyCNCategory("mysekai", map[string]CNApplyField{
		"tag": {Pairs: map[string]string{"同じキー": "官方标签"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated != 2 {
		t.Fatalf("updated rows = %d, want tag plus flavorText", updated)
	}
	data, err := s.CategoryData("mysekai")
	if err != nil {
		t.Fatal(err)
	}
	flavor := data["flavorText"]["同じキー"]
	if flavor.Text != "官方标签" || flavor.Source != model.SourceCN {
		t.Fatalf("legacy flavor propagation = %+v", flavor)
	}
}
