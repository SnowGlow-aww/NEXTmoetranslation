package store

import (
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
	if err := events.UpdateLineLocale(9, "1", "", titleSegmentID, "Manual title", model.SourceHuman, "title", model.LocaleEnglish, "editor"); err != nil {
		t.Fatal(err)
	}
	if err := events.UpdateLineLocale(9, "1", "原文", segmentID, "Manual English", model.SourceHuman, "talk", model.LocaleEnglish, "editor"); err != nil {
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
	if err := events.UpdateLineLocale(10, "1", "", detail.Episodes["1"].Segments[0].ID, "Stale title", model.SourceHuman, "title", model.LocaleEnglish, "editor"); err != nil {
		t.Fatal(err)
	}
	if err := events.UpdateLineLocale(10, "1", "古い原文", segmentID, "Stale English", model.SourceHuman, "talk", model.LocaleEnglish, "editor"); err != nil {
		t.Fatal(err)
	}
	second := []OrderedEpisode{{
		EpisodeNo: "1", ScenarioID: "stable", Title: "标题", TitleSource: model.SourceCN, SourceTitle: "新しい題名",
		Lines: []OrderedLine{{JPKey: "新しい原文", Text: "新中文", Source: model.SourceCN}},
	}}
	if err := events.ImportOrdered(10, model.EventStoryMeta{Source: "official_cn"}, second); err != nil {
		t.Fatal(err)
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
	if err := events.UpdateLineLocale(11, "1", "同じ", segments[1].ID, "First", model.SourceHuman, "talk", model.LocaleEnglish, "editor"); err != nil {
		t.Fatal(err)
	}
	if err := events.UpdateLineLocale(11, "1", "同じ", segments[2].ID, "Second", model.SourceHuman, "talk", model.LocaleEnglish, "editor"); err != nil {
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
