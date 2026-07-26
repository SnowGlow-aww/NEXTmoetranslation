package store

import (
	"errors"
	"path/filepath"
	"testing"

	"moesekai/server/internal/db"
	"moesekai/server/internal/model"
)

func TestAITranslationUpdatesChineseAdditiveProjection(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "event-ai-locale.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	events := NewEventStore(database)
	if err := events.ImportOrdered(7, model.EventStoryMeta{Source: "jp_pending"}, []OrderedEpisode{{
		EpisodeNo: "1", ScenarioID: "scenario", Title: "題名", TitleSource: "jp_pending",
		TalkKeys: []string{"原文"}, TalkData: map[string]string{"原文": ""},
	}}); err != nil {
		t.Fatal(err)
	}
	targets, err := events.UntranslatedTargets(7)
	if err != nil || len(targets) != 2 {
		t.Fatalf("targets=%+v err=%v", targets, err)
	}
	if _, err := events.ApplyEventTranslations(7, targets, []string{"标题", "译文"}, model.SourceLLM); err != nil {
		t.Fatal(err)
	}
	detail, err := events.DetailLocale(7, model.LocaleChinese)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Episodes["1"].Title != "标题" || detail.Episodes["1"].TalkData["原文"] != "译文" {
		t.Fatalf("stale Chinese additive projection: %+v", detail.Episodes["1"])
	}
}

func TestAIApplySkipsCollaboratorDriftAndDisappearedCandidates(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "event-ai-stale.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	events := NewEventStore(database)
	if err := events.ImportOrdered(18, model.EventStoryMeta{Source: "jp_pending"}, []OrderedEpisode{{
		EpisodeNo: "1", ScenarioID: "scenario", Title: "題名", TitleSource: "jp_pending",
		TalkKeys: []string{"原文"}, TalkData: map[string]string{"原文": ""},
	}}); err != nil {
		t.Fatal(err)
	}
	targets, err := events.UntranslatedTargets(18)
	if err != nil || len(targets) != 2 {
		t.Fatalf("targets=%+v err=%v", targets, err)
	}
	if _, err := database.Exec(`UPDATE event_story_episodes SET title='协作标题', title_source='unknown'
		WHERE event_id=18 AND episode_no='1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE event_story_lines SET cn_text='协作译文', source='unknown'
		WHERE event_id=18 AND episode_no='1' AND jp_key='原文'`); err != nil {
		t.Fatal(err)
	}
	changed, err := events.ApplyEventTranslations(18, targets, []string{"AI 标题", "AI 译文"}, model.SourceLLM)
	if err != nil || changed != 0 {
		t.Fatalf("stale AI apply changed=%d err=%v", changed, err)
	}
	detail, err := events.Detail(18)
	if err != nil || detail.Episodes["1"].Title != "协作标题" || detail.Episodes["1"].TalkData["原文"] != "协作译文" ||
		detail.Episodes["1"].TitleSource != model.SourceUnknown || detail.Episodes["1"].TalkSources["原文"] != model.SourceUnknown {
		t.Fatalf("collaborator values overwritten: detail=%+v err=%v", detail, err)
	}

	if err := events.ImportOrdered(19, model.EventStoryMeta{Source: "jp_pending"}, []OrderedEpisode{{
		EpisodeNo: "1", ScenarioID: "scenario", TalkKeys: []string{"消失原文"}, TalkData: map[string]string{"消失原文": ""},
	}}); err != nil {
		t.Fatal(err)
	}
	staleTargets, err := events.UntranslatedTargets(19)
	if err != nil || len(staleTargets) != 1 {
		t.Fatalf("disappearing targets=%+v err=%v", staleTargets, err)
	}
	if _, err := database.Exec(`DELETE FROM event_story_lines WHERE event_id=19 AND episode_no='1' AND jp_key='消失原文'`); err != nil {
		t.Fatal(err)
	}
	changed, err = events.ApplyEventTranslations(19, staleTargets, []string{"不应重建"}, model.SourceLLM)
	if err != nil || changed != 0 {
		t.Fatalf("disappeared AI apply changed=%d err=%v", changed, err)
	}
	var localizedText string
	if err := database.QueryRow(`SELECT text FROM event_story_segment_localizations
		WHERE segment_id=? AND locale=?`, staleTargets[0].SegmentIDs[0], model.LocaleChinese).Scan(&localizedText); err != nil || localizedText != "" {
		t.Fatalf("disappeared candidate localization=%q err=%v", localizedText, err)
	}
}

func TestPromoteHumanUpdatesLegacyAndChineseSegmentProvenance(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "event-promote-human.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	events := NewEventStore(database)
	if err := events.ImportOrdered(8, model.EventStoryMeta{Source: model.SourceCN}, []OrderedEpisode{{
		EpisodeNo: "1", ScenarioID: "scenario", Title: "标题", TitleSource: model.SourceCN,
		TalkKeys: []string{"原文"}, TalkData: map[string]string{"原文": "译文"},
		TalkSources: map[string]string{"原文": model.SourceLLM},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := events.PromoteHuman(8); err != nil {
		t.Fatal(err)
	}
	legacy, err := events.Detail(8)
	if err != nil {
		t.Fatal(err)
	}
	localized, err := events.DetailLocale(8, model.LocaleChinese)
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Episodes["1"].TitleSource != model.SourceHuman || legacy.Episodes["1"].TalkSources["原文"] != model.SourceHuman ||
		localized.Episodes["1"].TitleSource != model.SourceHuman || localized.Episodes["1"].TalkSources["原文"] != model.SourceHuman {
		t.Fatalf("promoted provenance diverged: legacy=%+v localized=%+v", legacy.Episodes["1"], localized.Episodes["1"])
	}
}

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

func TestUpdateLineIsAtomicAndTitleIgnoresJPKey(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "event-update-line.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	events := NewEventStore(database)
	if err := events.ImportOrdered(16, model.EventStoryMeta{Source: model.SourceCN, LastUpdated: 100}, []OrderedEpisode{{
		EpisodeNo: "1", ScenarioID: "scenario", Title: "旧标题", TitleSource: model.SourceCN,
		TalkKeys: []string{"原文"}, TalkData: map[string]string{"原文": "旧译文"}, TalkSources: map[string]string{"原文": model.SourceCN},
	}}); err != nil {
		t.Fatal(err)
	}
	assertUnchanged := func() {
		t.Helper()
		var text, source string
		var updated int64
		if err := database.QueryRow(`SELECT cn_text, source FROM event_story_lines WHERE event_id=16 AND episode_no='1' AND jp_key='原文'`).Scan(&text, &source); err != nil {
			t.Fatal(err)
		}
		if err := database.QueryRow(`SELECT last_updated FROM event_stories WHERE event_id=16`).Scan(&updated); err != nil {
			t.Fatal(err)
		}
		if text != "旧译文" || source != model.SourceCN || updated != 100 {
			t.Fatalf("partial update text=%q source=%q updated=%d", text, source, updated)
		}
	}
	if _, err := database.Exec(`CREATE TRIGGER fail_event_timestamp BEFORE UPDATE OF last_updated ON event_stories
		BEGIN SELECT RAISE(ABORT, 'timestamp failed'); END`); err != nil {
		t.Fatal(err)
	}
	if err := events.UpdateLine(16, "1", "原文", "不应保存", model.SourceHuman, "talk"); err == nil {
		t.Fatal("last_updated failure was ignored")
	}
	assertUnchanged()
	if _, err := database.Exec(`DROP TRIGGER fail_event_timestamp`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TRIGGER fail_event_localization BEFORE UPDATE ON event_story_segment_localizations
		BEGIN SELECT RAISE(ABORT, 'localization failed'); END`); err != nil {
		t.Fatal(err)
	}
	if err := events.UpdateLine(16, "1", "原文", "仍不应保存", model.SourceHuman, "talk"); err == nil {
		t.Fatal("localization failure was ignored")
	}
	assertUnchanged()
	if _, err := database.Exec(`DROP TRIGGER fail_event_localization`); err != nil {
		t.Fatal(err)
	}
	if err := events.UpdateLine(16, "1", "must-be-ignored", "新标题", model.SourceHuman, "title"); err != nil {
		t.Fatal(err)
	}
	detail, err := events.DetailLocale(16, model.LocaleChinese)
	if err != nil || detail.Episodes["1"].Title != "新标题" || detail.Episodes["1"].TitleSource != model.SourceHuman {
		t.Fatalf("localized title=%+v err=%v", detail.Episodes["1"], err)
	}
}

func TestRollingLegacyWriterStaleSegmentsFallBackByEpisode(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "event-rolling-stale.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	events := NewEventStore(database)
	if err := events.ImportOrdered(17, model.EventStoryMeta{Source: model.SourceCN}, []OrderedEpisode{{
		EpisodeNo: "1", ScenarioID: "old", Title: "旧标题", TitleSource: model.SourceCN,
		TalkKeys: []string{"旧原文"}, TalkData: map[string]string{"旧原文": "旧译文"},
	}}); err != nil {
		t.Fatal(err)
	}
	oldDetail, err := events.DetailLocale(17, model.LocaleEnglish)
	if err != nil {
		t.Fatal(err)
	}
	stale := oldDetail.Episodes["1"].Segments[1]
	if _, err := database.Exec(`DELETE FROM event_stories WHERE event_id=17`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO event_stories(event_id, source, version, last_updated) VALUES (17, 'official_cn', '2', 200)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO event_story_episodes(event_id, episode_no, scenario_id, title, title_source, position)
		VALUES (17, '1', 'new', '新标题', 'cn', 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO event_story_lines(event_id, episode_no, jp_key, cn_text, source, position)
		VALUES (17, '1', '新原文', '新译文', 'cn', 0)`); err != nil {
		t.Fatal(err)
	}
	detail, err := events.DetailLocale(17, model.LocaleChinese)
	if err != nil {
		t.Fatal(err)
	}
	episode := detail.Episodes["1"]
	if len(episode.Segments) != 0 || episode.Title != "新标题" || episode.TalkData["新原文"] != "新译文" {
		t.Fatalf("rolling fallback episode=%+v", episode)
	}
	if err := events.UpdateLineLocaleRevision(17, "1", "旧原文", stale.ID, stale.SourceHash, "stale", model.SourceHuman,
		"talk", model.LocaleEnglish, "editor", nil); !errors.Is(err, ErrEventSourceConflict) {
		t.Fatalf("stale segment update error=%v", err)
	}
}
