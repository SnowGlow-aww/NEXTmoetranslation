package store

import (
	"errors"
	"path/filepath"
	"testing"

	"moesekai/server/internal/db"
	"moesekai/server/internal/model"
)

func TestEnglishStoryEditsSurviveLegacyChineseReimport(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "event-locale.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	events := NewEventStore(database)
	first := []OrderedEpisode{{
		EpisodeNo: "1", ScenarioID: "stable-scenario", Title: "原标题", TitleSource: model.SourceCN,
		TalkKeys: []string{"原文"}, TalkData: map[string]string{"原文": "旧中文"},
	}}
	if err := events.ImportOrdered(9, model.EventStoryMeta{Source: "official_cn", Version: "1"}, first); err != nil {
		t.Fatal(err)
	}
	detail, err := events.DetailLocale(9, model.LocaleEnglish)
	if err != nil {
		t.Fatal(err)
	}
	segmentID := detail.Episodes["1"].Segments[1].ID
	titleSegmentID := detail.Episodes["1"].Segments[0].ID
	if err := events.UpdateLineLocale(9, "1", "", titleSegmentID, detail.Episodes["1"].Segments[0].SourceHash, "Manual title", model.SourceHuman, "title", model.LocaleEnglish, "editor"); err != nil {
		t.Fatal(err)
	}
	if err := events.UpdateLineLocale(9, "1", "原文", segmentID, detail.Episodes["1"].Segments[1].SourceHash, "Manual English", model.SourceHuman, "talk", model.LocaleEnglish, "editor"); err != nil {
		t.Fatal(err)
	}
	second := []OrderedEpisode{{
		EpisodeNo: "1", ScenarioID: "stable-scenario", Title: "新标题", TitleSource: model.SourceCN,
		TalkKeys: []string{"原文"}, TalkData: map[string]string{"原文": "新中文"},
	}}
	if err := events.ImportOrdered(9, model.EventStoryMeta{Source: "official_cn", Version: "2"}, second); err != nil {
		t.Fatal(err)
	}
	english, err := events.DetailLocale(9, model.LocaleEnglish)
	if err != nil {
		t.Fatal(err)
	}
	if got := english.Episodes["1"].TalkData["原文"]; got != "Manual English" {
		t.Fatalf("English edit after reimport = %q", got)
	}
	if got := english.Episodes["1"].Title; got != "Manual title" {
		t.Fatalf("English title after reimport = %q", got)
	}
	chinese, err := events.Detail(9)
	if err != nil {
		t.Fatal(err)
	}
	if got := chinese.Episodes["1"].TalkData["原文"]; got != "新中文" {
		t.Fatalf("legacy Chinese reimport = %q", got)
	}
}

func TestFirstStableReimportPreservesUniquelyMatchingLegacyTalkLocalization(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "event-legacy-position.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	events := NewEventStore(database)
	first := []OrderedEpisode{{
		EpisodeNo: "1", ScenarioID: "stable", Title: "标题", TitleSource: model.SourceCN,
		// This is the legacy filtered position for a translated speaker whose
		// missing/equal Chinese body never appeared in the old output.
		Lines: []OrderedLine{{JPKey: "初音ミク", Text: "初音未来", Source: model.SourceCN, ScenarioPosition: 0, Field: "legacy"}},
	}}
	if err := events.ImportOrdered(11, model.EventStoryMeta{Source: "official_cn"}, first); err != nil {
		t.Fatal(err)
	}
	detail, err := events.DetailLocale(11, model.LocaleEnglish)
	if err != nil {
		t.Fatal(err)
	}
	segment := detail.Episodes["1"].Segments[1]
	if err := events.UpdateLineLocale(11, "1", "初音ミク", segment.ID, segment.SourceHash,
		"Preserved English", model.SourceHuman, "talk", model.LocaleEnglish, "editor"); err != nil {
		t.Fatal(err)
	}
	second := []OrderedEpisode{{
		EpisodeNo: "1", ScenarioID: "stable", Title: "标题", TitleSource: model.SourceCN,
		Lines: []OrderedLine{
			{JPKey: "本文", Text: "", Source: model.SourceCN, ScenarioPosition: 0, Field: "body"},
			{JPKey: "初音ミク", Text: "初音未来", Source: model.SourceCN, ScenarioPosition: 1, Field: "speaker"},
		},
	}}
	if err := events.ImportOrdered(11, model.EventStoryMeta{Source: "official_cn"}, second); err != nil {
		t.Fatal(err)
	}
	localized, err := events.DetailLocale(11, model.LocaleEnglish)
	if err != nil {
		t.Fatal(err)
	}
	segments := localized.Episodes["1"].Segments
	if len(segments) != 3 || segments[1].Japanese != "本文" || segments[1].Text != "" ||
		segments[2].Japanese != "初音ミク" || segments[2].Text != "Preserved English" {
		t.Fatalf("first stable reimport segments = %+v", segments)
	}
}

func TestReimportFallsBackWhenGuessedExactIDHasDifferentSource(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "event-guessed-position.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	events := NewEventStore(database)
	first := []OrderedEpisode{{
		EpisodeNo: "1", ScenarioID: "stable", Title: "标题", TitleSource: model.SourceCN,
		Lines: []OrderedLine{{JPKey: "初音ミク", Text: "初音未来", Source: model.SourceCN, ScenarioPosition: 0, Field: "body"}},
	}}
	if err := events.ImportOrdered(13, model.EventStoryMeta{Source: "official_cn"}, first); err != nil {
		t.Fatal(err)
	}
	detail, err := events.DetailLocale(13, model.LocaleEnglish)
	if err != nil {
		t.Fatal(err)
	}
	guessed := detail.Episodes["1"].Segments[1]
	if err := events.UpdateLineLocale(13, "1", "初音ミク", guessed.ID, guessed.SourceHash,
		"Speaker English", model.SourceHuman, "talk", model.LocaleEnglish, "editor"); err != nil {
		t.Fatal(err)
	}
	second := []OrderedEpisode{{
		EpisodeNo: "1", ScenarioID: "stable", Title: "标题", TitleSource: model.SourceCN,
		Lines: []OrderedLine{
			{JPKey: "本文", Text: "正文", Source: model.SourceCN, ScenarioPosition: 0, Field: "body"},
			{JPKey: "初音ミク", Text: "初音未来", Source: model.SourceCN, ScenarioPosition: 1, Field: "speaker"},
		},
	}}
	if err := events.ImportOrdered(13, model.EventStoryMeta{Source: "official_cn"}, second); err != nil {
		t.Fatal(err)
	}
	localized, err := events.DetailLocale(13, model.LocaleEnglish)
	if err != nil {
		t.Fatal(err)
	}
	segments := localized.Episodes["1"].Segments
	if len(segments) != 3 || segments[1].Japanese != "本文" || segments[1].Text != "" ||
		segments[2].Japanese != "初音ミク" || segments[2].Text != "Speaker English" {
		t.Fatalf("hash fallback segments = %+v", segments)
	}
}

func TestDuplicateSourceContractionPreservesOnlyExactUnambiguousLocalization(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "event-source-contraction.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	events := NewEventStore(database)
	first := []OrderedEpisode{{
		EpisodeNo: "1", ScenarioID: "stable", Title: "标题", TitleSource: model.SourceCN,
		Lines: []OrderedLine{
			{JPKey: "同じ", Text: "正文", Source: model.SourceCN, ScenarioPosition: 0, Field: "body"},
			{JPKey: "同じ", Text: "说话人", Source: model.SourceCN, ScenarioPosition: 1, Field: "speaker"},
		},
	}}
	if err := events.ImportOrdered(14, model.EventStoryMeta{Source: "official_cn"}, first); err != nil {
		t.Fatal(err)
	}
	detail, err := events.DetailLocale(14, model.LocaleEnglish)
	if err != nil {
		t.Fatal(err)
	}
	segments := detail.Episodes["1"].Segments
	if err := events.UpdateLineLocale(14, "1", "同じ", segments[1].ID, segments[1].SourceHash,
		"Body English", model.SourceHuman, "talk", model.LocaleEnglish, "editor"); err != nil {
		t.Fatal(err)
	}
	if err := events.UpdateLineLocale(14, "1", "同じ", segments[2].ID, segments[2].SourceHash,
		"Speaker English", model.SourceHuman, "talk", model.LocaleEnglish, "editor"); err != nil {
		t.Fatal(err)
	}
	contracted := []OrderedEpisode{{
		EpisodeNo: "1", ScenarioID: "stable", Title: "标题", TitleSource: model.SourceCN,
		Lines: []OrderedLine{{JPKey: "同じ", Text: "正文", Source: model.SourceCN, ScenarioPosition: 0, Field: "body"}},
	}}
	if err := events.ImportOrdered(14, model.EventStoryMeta{Source: "official_cn"}, contracted); err != nil {
		t.Fatalf("duplicate contraction rolled back reimport: %v", err)
	}
	localized, err := events.DetailLocale(14, model.LocaleEnglish)
	if err != nil {
		t.Fatal(err)
	}
	segments = localized.Episodes["1"].Segments
	if len(segments) != 2 || segments[1].Text != "Body English" {
		t.Fatalf("contracted localization = %+v", segments)
	}
}

func TestDuplicateSourceContractionSkipsTwoAmbiguousFallbacks(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "event-ambiguous-contraction.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	events := NewEventStore(database)
	first := []OrderedEpisode{{
		EpisodeNo: "1", ScenarioID: "stable", Title: "标题", TitleSource: model.SourceCN,
		Lines: []OrderedLine{
			{JPKey: "同じ", Text: "第一处", Source: model.SourceCN, ScenarioPosition: 0, Field: "body"},
			{JPKey: "同じ", Text: "第二处", Source: model.SourceCN, ScenarioPosition: 1, Field: "speaker"},
		},
	}}
	if err := events.ImportOrdered(15, model.EventStoryMeta{Source: "official_cn"}, first); err != nil {
		t.Fatal(err)
	}
	detail, err := events.DetailLocale(15, model.LocaleEnglish)
	if err != nil {
		t.Fatal(err)
	}
	segments := detail.Episodes["1"].Segments
	for i, text := range []string{"First English", "Second English"} {
		segment := segments[i+1]
		if err := events.UpdateLineLocale(15, "1", "同じ", segment.ID, segment.SourceHash,
			text, model.SourceHuman, "talk", model.LocaleEnglish, "editor"); err != nil {
			t.Fatal(err)
		}
	}
	contracted := []OrderedEpisode{{
		EpisodeNo: "1", ScenarioID: "stable", Title: "标题", TitleSource: model.SourceCN,
		Lines: []OrderedLine{{JPKey: "同じ", Text: "唯一处", Source: model.SourceCN, ScenarioPosition: 2, Field: "body"}},
	}}
	if err := events.ImportOrdered(15, model.EventStoryMeta{Source: "official_cn"}, contracted); err != nil {
		t.Fatalf("ambiguous fallback rolled back reimport: %v", err)
	}
	localized, err := events.DetailLocale(15, model.LocaleEnglish)
	if err != nil {
		t.Fatal(err)
	}
	segments = localized.Episodes["1"].Segments
	if len(segments) != 2 || segments[1].Text != "" {
		t.Fatalf("ambiguous localization was attached: %+v", segments)
	}
}

func TestChangedSourceHashDoesNotKeepStaleLocalization(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "event-drift.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	events := NewEventStore(database)
	first := []OrderedEpisode{{
		EpisodeNo: "1", ScenarioID: "stable", Title: "标题", TitleSource: model.SourceCN, SourceTitle: "古い題名",
		Lines: []OrderedLine{{JPKey: "古い原文", Text: "旧中文", Source: model.SourceCN}},
	}}
	if err := events.ImportOrdered(10, model.EventStoryMeta{Source: "official_cn"}, first); err != nil {
		t.Fatal(err)
	}
	detail, err := events.DetailLocale(10, model.LocaleEnglish)
	if err != nil {
		t.Fatal(err)
	}
	segmentID := detail.Episodes["1"].Segments[1].ID
	if err := events.UpdateLineLocale(10, "1", "", detail.Episodes["1"].Segments[0].ID, detail.Episodes["1"].Segments[0].SourceHash, "Stale title", model.SourceHuman, "title", model.LocaleEnglish, "editor"); err != nil {
		t.Fatal(err)
	}
	if err := events.UpdateLineLocale(10, "1", "古い原文", segmentID, detail.Episodes["1"].Segments[1].SourceHash, "Stale English", model.SourceHuman, "talk", model.LocaleEnglish, "editor"); err != nil {
		t.Fatal(err)
	}
	second := []OrderedEpisode{{
		EpisodeNo: "1", ScenarioID: "stable", Title: "标题", TitleSource: model.SourceCN, SourceTitle: "新しい題名",
		Lines: []OrderedLine{{JPKey: "新しい原文", Text: "新中文", Source: model.SourceCN}},
	}}
	if err := events.ImportOrdered(10, model.EventStoryMeta{Source: "official_cn"}, second); err != nil {
		t.Fatal(err)
	}
	if err := events.UpdateLineLocale(10, "1", "古い原文", segmentID, detail.Episodes["1"].Segments[1].SourceHash,
		"Stale browser write", model.SourceHuman, "talk", model.LocaleEnglish, "editor"); !errors.Is(err, ErrEventSourceConflict) {
		t.Fatalf("stale event write error = %v", err)
	}
	localized, err := events.DetailLocale(10, model.LocaleEnglish)
	if err != nil {
		t.Fatal(err)
	}
	if got := localized.Episodes["1"].Segments[1].Text; got != "" {
		t.Fatalf("changed source retained stale translation %q", got)
	}
	if got := localized.Episodes["1"].Segments[0].Text; got != "" {
		t.Fatalf("changed title source retained stale translation %q", got)
	}
}

func TestDuplicateSourceLinesKeepDistinctStableSegments(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "event-duplicates.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	events := NewEventStore(database)
	episodes := []OrderedEpisode{{
		EpisodeNo: "1", ScenarioID: "duplicates", Title: "标题", TitleSource: model.SourceCN,
		Lines: []OrderedLine{
			{JPKey: "同じ", Text: "第一处", Source: model.SourceCN},
			{JPKey: "同じ", Text: "第二处", Source: model.SourceCN},
		},
	}}
	if err := events.ImportOrdered(11, model.EventStoryMeta{Source: "official_cn"}, episodes); err != nil {
		t.Fatal(err)
	}
	detail, err := events.DetailLocale(11, model.LocaleEnglish)
	if err != nil {
		t.Fatal(err)
	}
	segments := detail.Episodes["1"].Segments
	if len(segments) != 3 || segments[1].ID == segments[2].ID || segments[1].Japanese != "同じ" || segments[2].Japanese != "同じ" {
		t.Fatalf("duplicate segments = %+v", segments)
	}
	legacy, err := events.Detail(11)
	if err != nil {
		t.Fatal(err)
	}
	if got := legacy.Episodes["1"].TalkData["同じ"]; got != "第二处" {
		t.Fatalf("legacy duplicate projection = %q", got)
	}
	if err := events.UpdateLineLocale(11, "1", "同じ", segments[1].ID, segments[1].SourceHash, "First", model.SourceHuman, "talk", model.LocaleEnglish, "editor"); err != nil {
		t.Fatal(err)
	}
	if err := events.UpdateLineLocale(11, "1", "同じ", segments[2].ID, segments[2].SourceHash, "Second", model.SourceHuman, "talk", model.LocaleEnglish, "editor"); err != nil {
		t.Fatal(err)
	}
	detail, err = events.DetailLocale(11, model.LocaleEnglish)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Episodes["1"].Segments[1].Text != "First" || detail.Episodes["1"].Segments[2].Text != "Second" {
		t.Fatalf("localized duplicate segments = %+v", detail.Episodes["1"].Segments)
	}
}

func TestScenarioPositionAndFieldKeepIdentityAcrossChineseAvailability(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "event-invariant-position.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	events := NewEventStore(database)
	first := []OrderedEpisode{{
		EpisodeNo: "1", ScenarioID: "scenario", Title: "标题", TitleSource: model.SourceCN,
		Lines: []OrderedLine{
			{JPKey: "一行目", Text: "第一行", Source: model.SourceCN, ScenarioPosition: 0, Field: "body"},
			{JPKey: "初音ミク", Text: "", Source: model.SourceCN, ScenarioPosition: 1, Field: "speaker"},
			{JPKey: "二行目", Text: "", Source: model.SourceCN, ScenarioPosition: 2, Field: "body"},
		},
	}}
	if err := events.ImportOrdered(12, model.EventStoryMeta{Source: "official_cn"}, first); err != nil {
		t.Fatal(err)
	}
	detail, err := events.DetailLocale(12, model.LocaleEnglish)
	if err != nil {
		t.Fatal(err)
	}
	segments := detail.Episodes["1"].Segments
	if len(segments) != 4 || segments[2].Japanese != "初音ミク" || segments[3].Japanese != "二行目" {
		t.Fatalf("JP fields missing from locale segments: %+v", segments)
	}
	secondLineID := segments[3].ID
	if err := events.UpdateLineLocale(12, "1", "二行目", secondLineID, segments[3].SourceHash, "Second line", model.SourceHuman, "talk", model.LocaleEnglish, "editor"); err != nil {
		t.Fatal(err)
	}
	second := first
	second[0].Lines = append([]OrderedLine(nil), first[0].Lines...)
	second[0].Lines[1].Text = "初音未来"
	if err := events.ImportOrdered(12, model.EventStoryMeta{Source: "official_cn"}, second); err != nil {
		t.Fatal(err)
	}
	detail, err = events.DetailLocale(12, model.LocaleEnglish)
	if err != nil {
		t.Fatal(err)
	}
	segments = detail.Episodes["1"].Segments
	if segments[3].ID != secondLineID || segments[3].Text != "Second line" {
		t.Fatalf("CN availability shifted stable identity: %+v", segments)
	}
}
