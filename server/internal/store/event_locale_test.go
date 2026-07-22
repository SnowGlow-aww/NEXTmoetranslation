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
	chinese, err := events.Detail(9)
	if err != nil {
		t.Fatal(err)
	}
	if got := chinese.Episodes["1"].TalkData["原文"]; got != "新中文" {
		t.Fatalf("legacy Chinese reimport = %q", got)
	}
}
