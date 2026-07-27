package model_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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

	var index model.PublicLyricsIndex
	if err := json.Unmarshal(indexFixture, &index); err != nil {
		t.Fatal(err)
	}
	var detail model.PublicSongLyrics
	if err := json.Unmarshal(detailFixture, &detail); err != nil {
		t.Fatal(err)
	}
	if len(index.Songs) != 1 {
		t.Fatalf("canonical index song count = %d, want 1", len(index.Songs))
	}
	publicLocales := []string{"ja-JP", "zh-CN", "en-US"}
	assertExactJSONKeys(t, "canonical index title", indexFixture, "songs", 0, "title", publicLocales)
	assertExactJSONKeys(t, "canonical detail line", detailFixture, "lines", 0, []string{"id", "order", "japanese", "zh-CN", "en-US", "segments"})
	if detail.Version != 1 || index.Version != 1 {
		t.Fatalf("canonical versions index=%d detail=%d, want 1", index.Version, detail.Version)
	}
	if detail.MusicID <= 0 || detail.Revision < 2 || detail.Attribution == "" || len(detail.Lines) != 1 {
		t.Fatalf("canonical detail semantics = %+v", detail)
	}
	indexSong := index.Songs[0]
	if indexSong.MusicID != detail.MusicID || indexSong.Revision != detail.Revision || indexSong.UpdatedAt != detail.UpdatedAt {
		t.Fatalf("canonical fixtures disagree: index=%+v detail=%+v", indexSong, detail)
	}
	if indexSong.Title.Japanese != "新曲" || indexSong.Title.Chinese != "新歌" || indexSong.Title.English != "New Song" {
		t.Fatalf("canonical title locale semantics = %+v", indexSong.Title)
	}
	fixtureLine := detail.Lines[0]
	if fixtureLine.ID != "line-1" || fixtureLine.Order != 0 || fixtureLine.Japanese != "初音歌う" || fixtureLine.Chinese != "初音歌唱" || fixtureLine.English != "Miku sings" {
		t.Fatalf("canonical lyric locale semantics = %+v", fixtureLine)
	}
	if len(fixtureLine.Segments) != 1 || fixtureLine.Segments[0].Text != fixtureLine.Japanese || !reflect.DeepEqual(fixtureLine.Segments[0].PerformerIDs, []int{1}) {
		t.Fatalf("canonical segment semantics = %+v", fixtureLine.Segments)
	}
	fixtureUpdatedAt, err := time.Parse(time.RFC3339, detail.UpdatedAt)
	if err != nil || !fixtureUpdatedAt.Equal(time.Date(2026, time.July, 23, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("canonical updatedAt = %q parsed=%v err=%v", detail.UpdatedAt, fixtureUpdatedAt, err)
	}

	database, err := db.Open(filepath.Join(t.TempDir(), "public-lyrics-contract.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s := store.New(database)
	es := store.NewEventStore(database)
	syntheticMusicID := detail.MusicID + detail.Revision + 1000
	syntheticPerformerID := syntheticMusicID + 1000
	syntheticTitle := model.LocalizedTitle{Japanese: "合成試験曲", Chinese: "合成测试歌曲", English: "Synthetic Contract Song"}
	if err := s.UpsertMusicCatalog([]store.MusicCatalogRecord{
		{
			MusicID:       indexSong.MusicID,
			JapaneseTitle: indexSong.Title.Japanese,
			ChineseTitle:  indexSong.Title.Chinese,
			EnglishTitle:  indexSong.Title.English,
		},
		{
			MusicID:       syntheticMusicID,
			JapaneseTitle: syntheticTitle.Japanese,
			ChineseTitle:  syntheticTitle.Chinese,
			EnglishTitle:  syntheticTitle.English,
		},
	}); err != nil {
		t.Fatal(err)
	}
	seenPerformers := make(map[int]bool)
	performers := make([]store.PerformerCatalogRecord, 0)
	for _, line := range detail.Lines {
		for _, segment := range line.Segments {
			for _, performerID := range segment.PerformerIDs {
				if seenPerformers[performerID] {
					continue
				}
				seenPerformers[performerID] = true
				performers = append(performers, store.PerformerCatalogRecord{
					PerformerID:  performerID,
					JapaneseName: line.Japanese,
				})
			}
		}
	}
	if len(performers) == 0 {
		t.Fatal("canonical detail has no performer IDs")
	}
	performers = append(performers, store.PerformerCatalogRecord{PerformerID: syntheticPerformerID, JapaneseName: "合成歌唱者"})
	if err := s.UpsertPerformerCatalog(performers); err != nil {
		t.Fatal(err)
	}

	input := model.SongLyrics{
		MusicID:     detail.MusicID,
		Attribution: detail.Attribution,
		Lines:       make([]model.LyricLine, len(detail.Lines)),
	}
	for lineIndex, fixtureLine := range detail.Lines {
		input.Lines[lineIndex] = fixtureLine
		input.Lines[lineIndex].ID = fmt.Sprintf("fixture-source-%d", fixtureLine.Order)
		input.Lines[lineIndex].Chinese = ""
		input.Lines[lineIndex].English = ""
		input.Lines[lineIndex].Segments = make([]model.LyricSegment, len(fixtureLine.Segments))
		for segmentIndex, fixtureSegment := range fixtureLine.Segments {
			input.Lines[lineIndex].Segments[segmentIndex] = fixtureSegment
			input.Lines[lineIndex].Segments[segmentIndex].PerformerIDs = append([]int(nil), fixtureSegment.PerformerIDs...)
		}
	}
	var saved model.SongLyrics
	for revision := 1; revision <= detail.Revision; revision++ {
		input.Revision = revision - 1
		if revision < detail.Revision {
			input.SourceNote = fmt.Sprintf("fixture draft revision %d", revision)
		} else {
			input.SourceNote = ""
		}
		for lineIndex := range input.Lines {
			if revision*2 >= detail.Revision {
				input.Lines[lineIndex].Chinese = detail.Lines[lineIndex].Chinese
			}
			if revision == detail.Revision {
				input.Lines[lineIndex].English = detail.Lines[lineIndex].English
			}
		}
		var changed bool
		saved, changed, err = s.SaveLyricsMutation(input, "fixture")
		if err != nil || !changed || saved.Revision != revision {
			t.Fatalf("save fixture revision %d: changed=%t saved=%+v err=%v", revision, changed, saved, err)
		}
		input = saved
	}

	savedUpdatedAt, err := time.Parse(time.RFC3339, saved.UpdatedAt)
	if err != nil {
		t.Fatalf("ordinary save updatedAt %q: %v", saved.UpdatedAt, err)
	}
	if saved.UpdatedAt == detail.UpdatedAt || savedUpdatedAt.After(time.Now().UTC().Add(time.Second)) || time.Since(savedUpdatedAt) > time.Minute {
		t.Fatalf("ordinary save timestamp is not a recent runtime value: saved=%q fixture=%q now=%q", saved.UpdatedAt, detail.UpdatedAt, time.Now().UTC().Format(time.RFC3339))
	}

	updatedAt, err := time.Parse(time.RFC3339, detail.UpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	// Save timestamps are runtime-owned. Normalize only this fixture draft row
	// before publishing so the committed byte contract remains deterministic.
	result, err := database.Exec(
		`UPDATE song_lyrics SET updated_at=? WHERE music_id=? AND revision=?`,
		updatedAt.Unix(), detail.MusicID, detail.Revision,
	)
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		t.Fatalf("normalize fixture timestamp: rows=%d err=%v", rows, err)
	}
	published, err := s.PublishLyrics(detail.MusicID, detail.Revision, "fixture")
	if err != nil || published.PublishedRevision != detail.Revision || published.UpdatedAt != detail.UpdatedAt {
		t.Fatalf("publish fixture revision: published=%+v err=%v", published, err)
	}

	assets, err := files.NewGenerator(s, es, "").PublishedLyricsJSON()
	if err != nil {
		t.Fatal(err)
	}
	indexGenerated := assets["translation/lyrics/index.json"]
	detailGenerated := assets[fmt.Sprintf("translation/lyrics/music_%d.json", detail.MusicID)]
	if !bytes.Equal(indexGenerated, indexFixture) {
		t.Fatalf("generated index differs from canonical fixture\nwant: %q\ngot:  %q", indexFixture, indexGenerated)
	}
	if !bytes.Equal(detailGenerated, detailFixture) {
		t.Fatalf("generated detail differs from canonical fixture\nwant: %q\ngot:  %q", detailFixture, detailGenerated)
	}
	validateJSONDocument(t, indexSchema, indexGenerated)
	validateJSONDocument(t, detailSchema, detailGenerated)

	syntheticInput := model.SongLyrics{
		MusicID:     syntheticMusicID,
		Attribution: "Synthetic contract attribution",
		Lines: []model.LyricLine{{
			ID:       "synthetic-source-line",
			Order:    0,
			Japanese: "青空を歌う",
			Chinese:  "歌唱蓝天",
			English:  "Sing the blue sky",
			Segments: []model.LyricSegment{{Text: "青空", PerformerIDs: []int{syntheticPerformerID}}, {Text: "を歌う", PerformerIDs: []int{syntheticPerformerID}}},
		}},
	}
	syntheticSaved, changed, err := s.SaveLyricsMutation(syntheticInput, "synthetic-contract")
	if err != nil || !changed || syntheticSaved.Revision != 1 {
		t.Fatalf("save synthetic contract lyrics: changed=%t saved=%+v err=%v", changed, syntheticSaved, err)
	}
	syntheticTimestamp, err := time.Parse(time.RFC3339, syntheticSaved.UpdatedAt)
	if err != nil || syntheticTimestamp.After(time.Now().UTC().Add(time.Second)) || time.Since(syntheticTimestamp) > time.Minute {
		t.Fatalf("synthetic save timestamp = %q parsed=%v err=%v", syntheticSaved.UpdatedAt, syntheticTimestamp, err)
	}
	if _, err := s.PublishLyrics(syntheticSaved.MusicID, syntheticSaved.Revision, "synthetic-contract"); err != nil {
		t.Fatal(err)
	}
	syntheticAssets, err := files.NewGenerator(s, es, "").PublishedLyricsJSON()
	if err != nil {
		t.Fatal(err)
	}
	var syntheticIndex model.PublicLyricsIndex
	if err := json.Unmarshal(syntheticAssets["translation/lyrics/index.json"], &syntheticIndex); err != nil {
		t.Fatal(err)
	}
	var syntheticIndexSong *model.PublicLyricsIndexItem
	for songIndex := range syntheticIndex.Songs {
		if syntheticIndex.Songs[songIndex].MusicID == syntheticMusicID {
			syntheticIndexSong = &syntheticIndex.Songs[songIndex]
			break
		}
	}
	if syntheticIndexSong == nil || syntheticIndexSong.Revision != syntheticSaved.Revision || syntheticIndexSong.UpdatedAt != syntheticSaved.UpdatedAt || !reflect.DeepEqual(syntheticIndexSong.Title, syntheticTitle) {
		t.Fatalf("synthetic index semantics: song=%+v saved=%+v title=%+v", syntheticIndexSong, syntheticSaved, syntheticTitle)
	}
	var syntheticDetail model.PublicSongLyrics
	if err := json.Unmarshal(syntheticAssets[fmt.Sprintf("translation/lyrics/music_%d.json", syntheticMusicID)], &syntheticDetail); err != nil {
		t.Fatal(err)
	}
	if syntheticDetail.Version != 1 || syntheticDetail.MusicID != syntheticSaved.MusicID || syntheticDetail.Revision != syntheticSaved.Revision || syntheticDetail.UpdatedAt != syntheticSaved.UpdatedAt || syntheticDetail.Attribution != syntheticSaved.Attribution || len(syntheticDetail.Lines) != 1 {
		t.Fatalf("synthetic detail header semantics: generated=%+v saved=%+v", syntheticDetail, syntheticSaved)
	}
	syntheticLine := syntheticDetail.Lines[0]
	if syntheticLine.ID != "line-1" || syntheticLine.Order != syntheticSaved.Lines[0].Order || syntheticLine.Japanese != syntheticSaved.Lines[0].Japanese || syntheticLine.Chinese != syntheticSaved.Lines[0].Chinese || syntheticLine.English != syntheticSaved.Lines[0].English || !reflect.DeepEqual(syntheticLine.Segments, syntheticSaved.Lines[0].Segments) {
		t.Fatalf("synthetic detail lyric semantics: generated=%+v saved=%+v", syntheticLine, syntheticSaved.Lines[0])
	}
}

func assertExactJSONKeys(t *testing.T, name string, document []byte, path ...any) {
	t.Helper()
	if len(path) < 2 {
		t.Fatalf("%s key assertion requires a path and expected keys", name)
	}
	expected, ok := path[len(path)-1].([]string)
	if !ok {
		t.Fatalf("%s expected keys have type %T", name, path[len(path)-1])
	}
	var current any
	if err := json.Unmarshal(document, &current); err != nil {
		t.Fatal(err)
	}
	for _, component := range path[:len(path)-1] {
		switch component := component.(type) {
		case string:
			object, ok := current.(map[string]any)
			if !ok {
				t.Fatalf("%s path component %q found %T", name, component, current)
			}
			current, ok = object[component]
			if !ok {
				t.Fatalf("%s missing path component %q", name, component)
			}
		case int:
			array, ok := current.([]any)
			if !ok || component < 0 || component >= len(array) {
				t.Fatalf("%s array component %d found %T length=%d", name, component, current, len(array))
			}
			current = array[component]
		default:
			t.Fatalf("%s unsupported path component %T", name, component)
		}
	}
	object, ok := current.(map[string]any)
	if !ok {
		t.Fatalf("%s key target has type %T", name, current)
	}
	if len(object) != len(expected) {
		t.Fatalf("%s keys=%v, want exactly %v", name, sortedJSONKeys(object), expected)
	}
	for _, key := range expected {
		if _, ok := object[key]; !ok {
			t.Fatalf("%s keys=%v, want exactly %v", name, sortedJSONKeys(object), expected)
		}
	}
}

func sortedJSONKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
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
