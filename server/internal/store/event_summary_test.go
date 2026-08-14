package store

import (
	"path/filepath"
	"testing"

	"moesekai/server/internal/db"
	"moesekai/server/internal/model"
)

func eventSummaryEpisode(t *testing.T, scenarioID, title, body string, bodySource string) OrderedEpisode {
	t.Helper()
	canonical, digest, err := CanonicalizeEventScenario(map[string]any{
		"ScenarioId":        scenarioID,
		"Snippets":          []any{},
		"TalkData":          []any{map[string]any{"WindowDisplayName": "角色", "Body": body, "Voices": []any{}}},
		"SpecialEffectData": []any{},
		"AppearCharacters":  []any{},
	}, scenarioID)
	if err != nil {
		t.Fatal(err)
	}
	return OrderedEpisode{
		EpisodeNo: "1", ScenarioID: scenarioID, ScenarioCanonicalJSON: canonical, ScenarioSHA256: digest,
		Title: title, TitleSource: model.SourceCN,
		Lines: []OrderedLine{
			{JPKey: body, Text: body + "译文", Source: bodySource, ScenarioPosition: 0, Field: "body"},
			{JPKey: "角色", Text: "角色", Source: model.SourceCN, ScenarioPosition: 1, Field: "speaker"},
		},
	}
}

func TestEventStorySummariesJoinNamesByStableEventIDAndHideOnlyFullyOfficialStories(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "event-summary.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	translations := New(database)
	if _, err := translations.ImportCategory("events", model.Category{
		"name": {
			"活动甲": {Text: "Activity A", Source: model.SourceCN, Ids: []string{"101"}},
			"活动乙": {Text: "Activity B", Source: model.SourceCN, Ids: []string{"102"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	events := NewEventStore(database)
	if err := events.ImportOrdered(101, model.EventStoryMeta{Source: model.SourceCN}, []OrderedEpisode{
		eventSummaryEpisode(t, "scenario-a", "官方标题", "官方台词", model.SourceCN),
	}); err != nil {
		t.Fatal(err)
	}
	if err := events.ImportOrdered(102, model.EventStoryMeta{Source: model.SourceCN}, []OrderedEpisode{
		eventSummaryEpisode(t, "scenario-b", "混合标题", "人工台词", model.SourceHuman),
	}); err != nil {
		t.Fatal(err)
	}
	stories, err := events.ListLocale(model.LocaleEnglish)
	if err != nil {
		t.Fatal(err)
	}
	if len(stories) != 2 {
		t.Fatalf("stories=%+v", stories)
	}
	byID := map[int]model.EventStorySummary{}
	for _, story := range stories {
		byID[story.EventID] = story
	}
	if byID[101].EventName != "Activity A" || byID[101].EventNameJapanese != "活动甲" {
		t.Fatalf("stable event name join=%+v", byID[101])
	}
	if !byID[101].AllOfficialTagged {
		t.Fatalf("fully official story was not marked hidden=%+v", byID[101])
	}
	if byID[102].AllOfficialTagged {
		t.Fatalf("mixed-source story was incorrectly marked hidden=%+v", byID[102])
	}
}

func TestEventStorySummariesFailOpenWhenCanonicalSegmentsAreMissing(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "event-summary-missing.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	events := NewEventStore(database)
	if err := events.ImportOrdered(103, model.EventStoryMeta{Source: model.SourceCN}, []OrderedEpisode{
		eventSummaryEpisode(t, "scenario-c", "标题", "台词", model.SourceCN),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`DELETE FROM event_story_segments WHERE event_id=?`, 103); err != nil {
		t.Fatal(err)
	}
	stories, err := events.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(stories) != 1 || stories[0].AllOfficialTagged {
		t.Fatalf("missing canonical segments should fail open: %+v", stories)
	}
}
