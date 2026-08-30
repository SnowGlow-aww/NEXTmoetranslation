package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"moesekai/server/internal/db"
	"moesekai/server/internal/model"
)

func testScenario(t *testing.T, scenarioID string) (string, string) {
	t.Helper()
	value := map[string]any{
		"TalkData": []any{
			map[string]any{
				"WindowDisplayName": "初音ミク_制服", "Body": "一行目", "WhenFinishCloseWindow": float64(1),
				"Voices": []any{
					map[string]any{"VoiceId": "voice-1", "Volume": float64(80.75), "Character2dId": float64(39)},
					map[string]any{"VoiceId": "voice-2", "Volume": float64(-2.75), "Character2dId": float64(40)},
				},
			},
			map[string]any{"WindowDisplayName": "旁白", "Body": "二行目", "Voices": []any{}},
		},
		"ScenarioId": scenarioID,
		"SpecialEffectData": []any{
			map[string]any{"EffectType": float64(8), "StringVal": "地点"},
			map[string]any{"EffectType": float64(18), "StringVal": "回想"},
			map[string]any{"EffectType": float64(23), "StringVal": "选择"},
		},
		"AppearCharacters": []any{map[string]any{"Character2dId": float64(39), "CostumeType": "v2_01miku_casual"}},
		"Snippets": []any{
			map[string]any{"Action": float64(1), "ReferenceIndex": float64(0)},
			map[string]any{"Action": float64(6), "ReferenceIndex": float64(0)},
			map[string]any{"Action": float64(1), "ReferenceIndex": float64(1)},
			map[string]any{"Action": float64(6), "ReferenceIndex": float64(1)},
			map[string]any{"Action": float64(6), "ReferenceIndex": float64(2)},
		},
	}
	canonical, digest, err := CanonicalizeEventScenario(value, scenarioID)
	if err != nil {
		t.Fatal(err)
	}
	return canonical, digest
}

func TestCanonicalScenarioAndSekaiTextSourceTalkOrder(t *testing.T) {
	canonical, digest := testScenario(t, "scenario-1")
	canonicalAgain, digestAgain, err := CanonicalizeEventScenario(map[string]any{
		"ScenarioId": "scenario-1", "Snippets": []any{}, "TalkData": []any{},
		"SpecialEffectData": []any{}, "AppearCharacters": []any{},
	}, "scenario-1")
	if err != nil || canonicalAgain == canonical || digestAgain == digest {
		t.Fatalf("canonical identity result canonical=%q digest=%q err=%v", canonicalAgain, digestAgain, err)
	}
	if len(digest) != 64 {
		t.Fatalf("scenario digest = %q", digest)
	}
	talks, err := ParseEventSourceTalks(canonical)
	if err != nil {
		t.Fatal(err)
	}
	if len(talks) != 8 {
		t.Fatalf("source talk count=%d talks=%+v", len(talks), talks)
	}
	if talks[0].Speaker != "初音ミク" || talks[0].Text != "一行目" || talks[0].TalkDataIndex == nil || *talks[0].TalkDataIndex != 0 ||
		!reflect.DeepEqual(talks[0].Voices, []string{"voice-1", "voice-2"}) || !reflect.DeepEqual(talks[0].Volume, []int{80, -2}) ||
		talks[0].Chara2D != 39 || talks[0].CharIndex != 20 {
		t.Fatalf("first source talk = %+v", talks[0])
	}
	if talks[1].Speaker != "" || talks[1].Text != "" || talks[2].Speaker != "场景" || talks[2].Text != "地点" ||
		talks[4].TalkDataIndex == nil || *talks[4].TalkDataIndex != 1 || talks[5].Speaker != "左上场景" || talks[7].Speaker != "选项" {
		t.Fatalf("SekaiText source order = %+v", talks)
	}
	speakerless, _, err := CanonicalizeEventScenario(map[string]any{
		"ScenarioId": "speakerless", "Snippets": []any{map[string]any{"Action": float64(1), "ReferenceIndex": float64(0)}},
		"TalkData":          []any{map[string]any{"WindowDisplayName": "", "Body": "speakerless final line", "Voices": []any{}}},
		"SpecialEffectData": []any{}, "AppearCharacters": []any{},
	}, "speakerless")
	if err != nil {
		t.Fatal(err)
	}
	speakerlessTalks, err := ParseEventSourceTalks(speakerless)
	if err != nil || len(speakerlessTalks) != 1 || speakerlessTalks[0].Text != "speakerless final line" {
		t.Fatalf("speakerless trailing talk=%+v err=%v", speakerlessTalks, err)
	}
}

func TestValidateScenarioRejectsNonCanonicalNumberLexemes(t *testing.T) {
	for _, number := range []string{"1.0", "1e0"} {
		t.Run(number, func(t *testing.T) {
			canonical := `{"ScenarioId":"scenario","Snippets":[{"Action":` + number + `,"ReferenceIndex":0}],"TalkData":[],"SpecialEffectData":[],"AppearCharacters":[]}`
			sum := sha256.Sum256([]byte(canonical))
			err := ValidateEventScenarioRecord(EventScenarioRecord{
				EventID: 1, EpisodeNo: "1", ScenarioID: "scenario", CanonicalJSON: canonical, SHA256: hex.EncodeToString(sum[:]),
			})
			if !errors.Is(err, ErrEventScenarioInvalid) {
				t.Fatalf("number lexeme %s error=%v", number, err)
			}
		})
	}
}

func TestImportOrderedScenarioSnapshotRevisionAndFailClosedReads(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "scenario-snapshot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	events := NewEventStore(database)
	canonical, digest := testScenario(t, "scenario-1")
	episode := OrderedEpisode{
		EpisodeNo: "1", ScenarioID: "scenario-1", ScenarioCanonicalJSON: canonical, ScenarioSHA256: digest,
		Title: "标题", TitleSource: model.SourceCN, SourceTitle: "原題",
		TalkKeys: []string{"一行目", "初音ミク"}, TalkData: map[string]string{"一行目": "第一行", "初音ミク": "未来"},
		Lines: []OrderedLine{
			{JPKey: "一行目", Text: "第一行", Source: model.SourceCN, ScenarioPosition: 0, Field: "body"},
			{JPKey: "初音ミク", Text: "未来", Source: model.SourceCN, ScenarioPosition: 1, Field: "speaker"},
		},
	}
	if err := events.ImportOrdered(81, model.EventStoryMeta{Source: "official_cn"}, []OrderedEpisode{episode}); err != nil {
		t.Fatal(err)
	}
	var storedJSON, storedSHA string
	if err := database.QueryRow(`SELECT canonical_json, sha256 FROM event_story_scenarios WHERE event_id=81 AND episode_no='1'`).
		Scan(&storedJSON, &storedSHA); err != nil || storedJSON != canonical || storedSHA != digest {
		t.Fatalf("stored scenario json=%q sha=%q err=%v", storedJSON, storedSHA, err)
	}
	snapshot, err := events.EpisodeSnapshot(81, "1", model.LocaleEnglish)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Revision == "" || snapshot.Scenario.RawJSON != canonical || snapshot.Scenario.FileName != "scenario-1.json" ||
		snapshot.Scenario.ParserVersion != EventScenarioParserVersion || len(snapshot.Segments) != 5 || len(snapshot.Scenario.SourceTalks) != 8 {
		t.Fatalf("episode snapshot = %+v", snapshot)
	}
	var target model.EventStorySegment
	for _, segment := range snapshot.Segments {
		if segment.Position == 0 {
			target = segment
		}
	}
	revision := target.Revision
	if err := events.UpdateLineLocaleRevision(81, "1", "一行目", target.ID, target.SourceHash, "English", model.SourceHuman,
		"talk", model.LocaleEnglish, "editor", &revision); err != nil {
		t.Fatal(err)
	}
	updated, err := events.EpisodeSnapshot(81, "1", model.LocaleEnglish)
	if err != nil || updated.Revision == snapshot.Revision {
		t.Fatalf("updated snapshot revision=%q old=%q err=%v", updated.Revision, snapshot.Revision, err)
	}

	if _, err := database.Exec(`UPDATE event_story_episodes SET scenario_id='other' WHERE event_id=81 AND episode_no='1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := events.EpisodeSnapshot(81, "1", model.LocaleEnglish); !errors.Is(err, ErrEventScenarioConflict) {
		t.Fatalf("parent mismatch error = %v", err)
	}
	filtered, err := New(database).ExportEventContent()
	if err != nil || len(filtered.Scenarios) != 0 || len(filtered.Segments) != 0 || len(filtered.Localizations) != 0 {
		t.Fatalf("backup export did not filter mismatched parent content: content=%+v err=%v", filtered, err)
	}
	if _, err := database.Exec(`UPDATE event_story_episodes SET scenario_id='scenario-1' WHERE event_id=81 AND episode_no='1'`); err != nil {
		t.Fatal(err)
	}
	bad := `{}`
	badSum := sha256.Sum256([]byte(bad))
	if _, err := database.Exec(`UPDATE event_story_scenarios SET canonical_json=?, sha256=? WHERE event_id=81 AND episode_no='1'`,
		bad, hex.EncodeToString(badSum[:])); err != nil {
		t.Fatal(err)
	}
	if _, err := events.EpisodeSnapshot(81, "1", model.LocaleEnglish); !errors.Is(err, ErrEventScenarioConflict) && !errors.Is(err, ErrEventScenarioInvalid) {
		t.Fatalf("bad scenario error = %v", err)
	}
	if _, err := New(database).ExportEventContent(); err == nil {
		t.Fatal("backup export accepted corrupt scenario data")
	}
}

func TestScenarioImportAndBackfillAreAtomicAndDoNotChangeTranslations(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "scenario-atomic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	events := NewEventStore(database)
	legacyEpisodes := []OrderedEpisode{
		{EpisodeNo: "1", ScenarioID: "one", Title: "人工标题", TitleSource: model.SourceHuman,
			TalkKeys: []string{"一"}, TalkData: map[string]string{"一": "人工一"}, TalkSources: map[string]string{"一": model.SourceHuman}},
		{EpisodeNo: "2", ScenarioID: "two", TalkKeys: []string{"二"}, TalkData: map[string]string{"二": "人工二"}, TalkSources: map[string]string{"二": model.SourcePinned}},
	}
	if err := events.ImportOrdered(82, model.EventStoryMeta{Source: model.SourceHuman}, legacyEpisodes); err != nil {
		t.Fatal(err)
	}
	before, err := events.Detail(82)
	if err != nil {
		t.Fatal(err)
	}
	oneJSON, oneSHA := testScenario(t, "one")
	twoJSON, twoSHA := testScenario(t, "two")
	invalid := append([]OrderedEpisode(nil), legacyEpisodes...)
	invalid[0].ScenarioCanonicalJSON, invalid[0].ScenarioSHA256 = oneJSON, oneSHA
	invalid[1].ScenarioCanonicalJSON, invalid[1].ScenarioSHA256 = twoJSON, "wrong"
	invalid[0].TalkData = map[string]string{"一": "不应覆盖"}
	if err := events.ImportOrdered(82, model.EventStoryMeta{Source: model.SourceCN}, invalid); err == nil {
		t.Fatal("invalid scenario set unexpectedly replaced event")
	}
	afterFailure, err := events.Detail(82)
	if err != nil || !reflect.DeepEqual(afterFailure, before) {
		t.Fatalf("failed scenario import changed translations: after=%+v before=%+v err=%v", afterFailure, before, err)
	}

	if _, err := database.Exec(`CREATE TRIGGER fail_second_scenario BEFORE INSERT ON event_story_scenarios
		WHEN NEW.episode_no='2' BEGIN SELECT RAISE(ABORT, 'scenario insert failed'); END`); err != nil {
		t.Fatal(err)
	}
	backfill := []OrderedEpisode{
		{EpisodeNo: "1", ScenarioID: "one", ScenarioCanonicalJSON: oneJSON, ScenarioSHA256: oneSHA},
		{EpisodeNo: "2", ScenarioID: "two", ScenarioCanonicalJSON: twoJSON, ScenarioSHA256: twoSHA},
	}
	if err := events.BackfillScenarios(82, backfill); err == nil {
		t.Fatal("injected scenario backfill failure unexpectedly committed")
	}
	var scenarios int
	if err := database.QueryRow(`SELECT COUNT(*) FROM event_story_scenarios WHERE event_id=82`).Scan(&scenarios); err != nil || scenarios != 0 {
		t.Fatalf("partial scenario backfill count=%d err=%v", scenarios, err)
	}
	if _, err := database.Exec(`DROP TRIGGER fail_second_scenario`); err != nil {
		t.Fatal(err)
	}
	if err := events.BackfillScenarios(82, backfill); err != nil {
		t.Fatal(err)
	}
	afterBackfill, err := events.Detail(82)
	if err != nil || !reflect.DeepEqual(afterBackfill, before) {
		t.Fatalf("scenario backfill changed translations: after=%+v before=%+v err=%v", afterBackfill, before, err)
	}
	missing, err := events.MissingScenarioEpisodes(82)
	if err != nil || len(missing) != 0 {
		t.Fatalf("missing scenarios=%+v err=%v", missing, err)
	}
}

func TestScenarioIDsCanSwapBetweenEpisodesDuringImportAndBackfill(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "scenario-swap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	events := NewEventStore(database)
	oneJSON, oneSHA := testScenario(t, "one")
	twoJSON, twoSHA := testScenario(t, "two")
	first := []OrderedEpisode{
		{EpisodeNo: "1", ScenarioID: "one", ScenarioCanonicalJSON: oneJSON, ScenarioSHA256: oneSHA},
		{EpisodeNo: "2", ScenarioID: "two", ScenarioCanonicalJSON: twoJSON, ScenarioSHA256: twoSHA},
	}
	if err := events.ImportOrdered(83, model.EventStoryMeta{Source: model.SourceCN}, first); err != nil {
		t.Fatal(err)
	}
	swapped := []OrderedEpisode{
		{EpisodeNo: "1", ScenarioID: "two", ScenarioCanonicalJSON: twoJSON, ScenarioSHA256: twoSHA},
		{EpisodeNo: "2", ScenarioID: "one", ScenarioCanonicalJSON: oneJSON, ScenarioSHA256: oneSHA},
	}
	if err := events.ImportOrdered(83, model.EventStoryMeta{Source: model.SourceCN}, swapped); err != nil {
		t.Fatalf("swap import: %v", err)
	}
	for episodeNo, want := range map[string]string{"1": "two", "2": "one"} {
		var got string
		if err := database.QueryRow(`SELECT scenario_id FROM event_story_scenarios WHERE event_id=83 AND episode_no=?`, episodeNo).Scan(&got); err != nil || got != want {
			t.Fatalf("imported episode %s scenario=%q want=%q err=%v", episodeNo, got, want, err)
		}
	}

	ghostJSON, ghostSHA := testScenario(t, "ghost")
	if _, err := database.Exec(`DELETE FROM event_story_scenarios WHERE event_id=83`); err != nil {
		t.Fatal(err)
	}
	for _, record := range []EventScenarioRecord{
		{EventID: 83, EpisodeNo: "1", ScenarioID: "one", CanonicalJSON: oneJSON, SHA256: oneSHA},
		{EventID: 83, EpisodeNo: "2", ScenarioID: "two", CanonicalJSON: twoJSON, SHA256: twoSHA},
		{EventID: 83, EpisodeNo: "ghost", ScenarioID: "ghost", CanonicalJSON: ghostJSON, SHA256: ghostSHA},
	} {
		if _, err := database.Exec(`INSERT INTO event_story_scenarios(event_id, episode_no, scenario_id, canonical_json, sha256)
			VALUES (?, ?, ?, ?, ?)`, record.EventID, record.EpisodeNo, record.ScenarioID, record.CanonicalJSON, record.SHA256); err != nil {
			t.Fatal(err)
		}
	}
	if err := events.BackfillScenarios(83, swapped); err != nil {
		t.Fatalf("swap backfill: %v", err)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM event_story_scenarios WHERE event_id=83`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("backfill retained orphan rows: count=%d err=%v", count, err)
	}
}

func TestCorruptScenarioRecordsAreReportedMissing(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "scenario-corrupt.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	events := NewEventStore(database)
	canonical, digest := testScenario(t, "scenario")
	if err := events.ImportOrdered(84, model.EventStoryMeta{Source: model.SourceCN}, []OrderedEpisode{{
		EpisodeNo: "1", ScenarioID: "scenario", ScenarioCanonicalJSON: canonical, ScenarioSHA256: digest,
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE event_story_scenarios SET canonical_json='{}' WHERE event_id=84`); err != nil {
		t.Fatal(err)
	}
	missing, err := events.MissingScenarioEpisodes(84)
	if err != nil || len(missing) != 1 || missing[0].EpisodeNo != "1" {
		t.Fatalf("missing=%+v err=%v", missing, err)
	}
	states, _, err := events.EventSyncStates()
	if err != nil || !states[84].MissingScenarios {
		t.Fatalf("sync state=%+v err=%v", states[84], err)
	}
}

func TestReopenPreservesRollingStaleSideRowsAndFiltersThemFromRestoreExport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scenario-reopen.db")
	database, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	events := NewEventStore(database)
	orphanJSON, orphanSHA := testScenario(t, "old-scenario")
	validJSON, validSHA := testScenario(t, "valid-scenario")
	if err := events.ImportOrdered(85, model.EventStoryMeta{Source: model.SourceCN}, []OrderedEpisode{{
		EpisodeNo: "1", ScenarioID: "old-scenario", ScenarioCanonicalJSON: orphanJSON, ScenarioSHA256: orphanSHA,
	}}); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := events.ImportOrdered(86, model.EventStoryMeta{Source: model.SourceCN}, []OrderedEpisode{{
		EpisodeNo: "1", ScenarioID: "valid-scenario", ScenarioCanonicalJSON: validJSON, ScenarioSHA256: validSHA,
	}}); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := events.BackfillScenarios(86, []OrderedEpisode{{
		EpisodeNo: "1", ScenarioID: "valid-scenario", ScenarioCanonicalJSON: validJSON, ScenarioSHA256: validSHA,
	}}); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO event_story_segment_localizations
		(segment_id, locale, text, source, updated_at, updated_by, revision)
		SELECT segment_id, ?, 'Keep me', 'human', 123, 'editor', 4 FROM event_story_segments
		WHERE event_id=86 AND kind='talk' AND source_text='一行目' AND segment_id LIKE '%:body'`,
		model.LocaleEnglish); err != nil {
		database.Close()
		t.Fatal(err)
	}
	var validSegment EventSegmentRecord
	if err := database.QueryRow(`SELECT segment_id, event_id, episode_no, scenario_id, kind, position, jp_key, source_text, source_hash
		FROM event_story_segments WHERE event_id=86 AND kind='talk' AND source_text='一行目' AND segment_id LIKE '%:body'`).Scan(&validSegment.SegmentID, &validSegment.EventID, &validSegment.EpisodeNo,
		&validSegment.ScenarioID, &validSegment.Kind, &validSegment.Position, &validSegment.JPKey,
		&validSegment.SourceText, &validSegment.SourceHash); err != nil {
		database.Close()
		t.Fatal(err)
	}
	validEnglish := EventLocalizationRecord{
		SegmentID: validSegment.SegmentID, Locale: model.LocaleEnglish, Text: "Keep me", Source: model.SourceHuman,
		UpdatedAt: 123, UpdatedBy: "editor", Revision: 4,
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	legacy, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`DELETE FROM event_stories WHERE event_id=85`); err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`INSERT INTO event_stories(event_id, source, version, last_updated)
		VALUES (85, 'official_cn', '1.0', 1)`); err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`INSERT INTO event_story_episodes(event_id, episode_no, scenario_id, position)
		VALUES (85, '1', 'replacement-scenario', 0)`); err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var orphanCount, validCount, orphanSegments, validSegments, orphanLocalizations int
	if err := reopened.QueryRow(`SELECT COUNT(*) FROM event_story_scenarios WHERE event_id=85`).Scan(&orphanCount); err != nil {
		t.Fatal(err)
	}
	if err := reopened.QueryRow(`SELECT COUNT(*) FROM event_story_scenarios WHERE event_id=86`).Scan(&validCount); err != nil {
		t.Fatal(err)
	}
	if err := reopened.QueryRow(`SELECT COUNT(*) FROM event_story_segments WHERE event_id=85`).Scan(&orphanSegments); err != nil {
		t.Fatal(err)
	}
	if err := reopened.QueryRow(`SELECT COUNT(*) FROM event_story_segments WHERE event_id=86`).Scan(&validSegments); err != nil {
		t.Fatal(err)
	}
	if err := reopened.QueryRow(`SELECT COUNT(*) FROM event_story_segment_localizations WHERE segment_id LIKE 'event:85:%'`).Scan(&orphanLocalizations); err != nil {
		t.Fatal(err)
	}
	if orphanCount != 1 || validCount != 1 || orphanSegments == 0 || validSegments == 0 || orphanLocalizations == 0 {
		t.Fatalf("preserved side tables scenarios=%d/%d segments=%d/%d orphanLocalizations=%d",
			orphanCount, validCount, orphanSegments, validSegments, orphanLocalizations)
	}
	var preservedSegment EventSegmentRecord
	if err := reopened.QueryRow(`SELECT segment_id, event_id, episode_no, scenario_id, kind, position, jp_key, source_text, source_hash
		FROM event_story_segments WHERE event_id=86`).Scan(&preservedSegment.SegmentID, &preservedSegment.EventID,
		&preservedSegment.EpisodeNo, &preservedSegment.ScenarioID, &preservedSegment.Kind, &preservedSegment.Position,
		&preservedSegment.JPKey, &preservedSegment.SourceText, &preservedSegment.SourceHash); err != nil {
		t.Fatal(err)
	}
	var preservedEnglish EventLocalizationRecord
	if err := reopened.QueryRow(`SELECT segment_id, locale, text, source, updated_at, updated_by, revision
		FROM event_story_segment_localizations WHERE segment_id=? AND locale=?`, validSegment.SegmentID, model.LocaleEnglish).
		Scan(&preservedEnglish.SegmentID, &preservedEnglish.Locale, &preservedEnglish.Text, &preservedEnglish.Source,
			&preservedEnglish.UpdatedAt, &preservedEnglish.UpdatedBy, &preservedEnglish.Revision); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(preservedSegment, validSegment) || !reflect.DeepEqual(preservedEnglish, validEnglish) {
		t.Fatalf("valid rolling rows changed: segment=%+v localization=%+v", preservedSegment, preservedEnglish)
	}
	contentStore := New(reopened)
	if _, err := reopened.Exec(`INSERT INTO event_story_locale_meta(event_id, locale, last_updated) VALUES (999, 'en-US', 1)`); err != nil {
		t.Fatal(err)
	}
	exported, err := contentStore.ExportEventContent()
	if err != nil {
		t.Fatalf("export after cleanup: %v", err)
	}
	if len(exported.Scenarios) != 1 || exported.Scenarios[0].EventID != 86 || len(exported.Segments) == 0 {
		t.Fatalf("exported event content=%+v", exported)
	}
	for _, segment := range exported.Segments {
		if segment.EventID != 86 {
			t.Fatalf("export included stale segment=%+v", segment)
		}
	}
	for _, metadata := range exported.LocaleMeta {
		if metadata.EventID == 999 {
			t.Fatalf("export included orphan locale metadata=%+v", metadata)
		}
	}
	destinationDB, err := db.Open(filepath.Join(t.TempDir(), "scenario-restore.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer destinationDB.Close()
	destinationEvents := NewEventStore(destinationDB)
	if err := destinationEvents.ImportOrdered(86, model.EventStoryMeta{Source: model.SourceCN}, []OrderedEpisode{{
		EpisodeNo: "1", ScenarioID: "valid-scenario",
	}}); err != nil {
		t.Fatal(err)
	}
	destinationStore := New(destinationDB)
	if err := destinationStore.ImportTranslationContent(nil, exported, LyricsContentExport{}); err != nil {
		t.Fatalf("restore filtered export: %v", err)
	}
	restored, err := destinationStore.ExportEventContent()
	if err != nil || !reflect.DeepEqual(restored, exported) {
		t.Fatalf("restored event content=%+v want=%+v err=%v", restored, exported, err)
	}
	var staleAfterExport int
	if err := reopened.QueryRow(`SELECT COUNT(*) FROM event_story_segment_localizations
		WHERE segment_id LIKE 'event:85:%'`).Scan(&staleAfterExport); err != nil || staleAfterExport != orphanLocalizations {
		t.Fatalf("source stale localizations after export=%d want=%d err=%v", staleAfterExport, orphanLocalizations, err)
	}
}

func TestCanonicalCoverageIncludesEmptyScenarioFieldsAndRejectsIncompleteRestore(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "scenario-empty-fields.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	canonical, digest, err := CanonicalizeEventScenario(map[string]any{
		"ScenarioId": "empty-fields", "Snippets": []any{},
		"TalkData":          []any{map[string]any{"WindowDisplayName": "", "Body": "", "Voices": []any{}}},
		"SpecialEffectData": []any{}, "AppearCharacters": []any{},
	}, "empty-fields")
	if err != nil {
		t.Fatal(err)
	}
	events := NewEventStore(database)
	episode := OrderedEpisode{
		EpisodeNo: "1", ScenarioID: "empty-fields", ScenarioCanonicalJSON: canonical, ScenarioSHA256: digest,
		Title: "", TitleSource: model.SourceUnknown, SourceTitle: "",
	}
	if err := events.ImportOrdered(90, model.EventStoryMeta{Source: model.SourceUnknown}, []OrderedEpisode{episode}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := events.EpisodeSnapshot(90, "1", model.LocaleEnglish)
	if err != nil || len(snapshot.Segments) != 3 || snapshot.Segments[1].Japanese != "" || snapshot.Segments[2].Japanese != "" {
		t.Fatalf("empty-field snapshot=%+v err=%v", snapshot, err)
	}
	contentStore := New(database)
	exported, err := contentStore.ExportEventContent()
	if err != nil {
		t.Fatal(err)
	}
	incomplete := exported
	incomplete.Segments = append([]EventSegmentRecord(nil), exported.Segments[:len(exported.Segments)-1]...)
	destinationDB, err := db.Open(filepath.Join(t.TempDir(), "scenario-empty-restore.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer destinationDB.Close()
	destinationEvents := NewEventStore(destinationDB)
	if err := destinationEvents.ImportOrdered(90, model.EventStoryMeta{Source: model.SourceUnknown}, []OrderedEpisode{{
		EpisodeNo: "1", ScenarioID: "empty-fields",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := New(destinationDB).ImportTranslationContent(nil, incomplete, LyricsContentExport{}); err == nil {
		t.Fatal("incomplete canonical event content unexpectedly restored")
	}
	var remaining int
	if err := destinationDB.QueryRow(`SELECT COUNT(*) FROM event_story_segments WHERE event_id=90`).Scan(&remaining); err != nil || remaining != 1 {
		t.Fatalf("failed restore changed destination segments=%d err=%v", remaining, err)
	}
	if _, err := database.Exec(`DELETE FROM event_story_segments
		WHERE event_id=90 AND kind='talk' AND position=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := events.EpisodeSnapshot(90, "1", model.LocaleEnglish); !errors.Is(err, ErrEventScenarioConflict) {
		t.Fatalf("incomplete snapshot error=%v", err)
	}
	if _, err := contentStore.ExportEventContent(); err == nil {
		t.Fatal("incomplete canonical event content unexpectedly exported")
	}
}

func TestScenarioIdentityCycleArchivesConflictingDeterministicRows(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "scenario-cycle.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	scenario := func(id, body string) (string, string) {
		canonical, digest, scenarioErr := CanonicalizeEventScenario(map[string]any{
			"ScenarioId": id, "Snippets": []any{},
			"TalkData":          []any{map[string]any{"WindowDisplayName": "", "Body": body, "Voices": []any{}}},
			"SpecialEffectData": []any{}, "AppearCharacters": []any{},
		}, id)
		if scenarioErr != nil {
			t.Fatal(scenarioErr)
		}
		return canonical, digest
	}
	oldA, oldASHA := scenario("scenario-a", "old body")
	events := NewEventStore(database)
	if err := events.ImportOrdered(91, model.EventStoryMeta{Source: "jp_pending"}, []OrderedEpisode{{
		EpisodeNo: "1", ScenarioID: "scenario-a", ScenarioCanonicalJSON: oldA, ScenarioSHA256: oldASHA,
		Title: "old title", TitleSource: "jp_pending", SourceTitle: "old title",
		TalkKeys: []string{"old body"}, TalkData: map[string]string{"old body": ""},
		Lines: []OrderedLine{{JPKey: "old body", Source: "jp_pending", ScenarioPosition: 0, Field: "body"}},
	}}); err != nil {
		t.Fatal(err)
	}
	detail, err := events.DetailLocale(91, model.LocaleEnglish)
	if err != nil {
		t.Fatal(err)
	}
	oldBody := detail.Episodes["1"].Segments[1]
	if err := events.UpdateLineLocale(91, "1", "old body", oldBody.ID, oldBody.SourceHash,
		"Old English", model.SourceHuman, "talk", model.LocaleEnglish, "editor"); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DELETE FROM event_stories WHERE event_id=91`,
		`INSERT INTO event_stories(event_id, source, version, last_updated) VALUES (91, 'jp_pending', '2', 2)`,
		`INSERT INTO event_story_episodes(event_id, episode_no, scenario_id, title, title_source, position)
		 VALUES (91, '1', 'scenario-b', 'middle title', 'jp_pending', 0)`,
		`INSERT INTO event_story_lines(event_id, episode_no, jp_key, cn_text, source, position)
		 VALUES (91, '1', 'middle body', '', 'jp_pending', 0)`,
	} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	middle, middleSHA := scenario("scenario-b", "middle body")
	if err := events.BackfillScenarios(91, []OrderedEpisode{{
		EpisodeNo: "1", ScenarioID: "scenario-b", ScenarioCanonicalJSON: middle, ScenarioSHA256: middleSHA,
		SourceTitle: "middle title",
	}}); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DELETE FROM event_stories WHERE event_id=91`,
		`INSERT INTO event_stories(event_id, source, version, last_updated) VALUES (91, 'jp_pending', '3', 3)`,
		`INSERT INTO event_story_episodes(event_id, episode_no, scenario_id, title, title_source, position)
		 VALUES (91, '1', 'scenario-a', 'new title', 'jp_pending', 0)`,
		`INSERT INTO event_story_lines(event_id, episode_no, jp_key, cn_text, source, position)
		 VALUES (91, '1', 'new body', '', 'jp_pending', 0)`,
	} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	newA, newASHA := scenario("scenario-a", "new body")
	if err := events.BackfillScenarios(91, []OrderedEpisode{{
		EpisodeNo: "1", ScenarioID: "scenario-a", ScenarioCanonicalJSON: newA, ScenarioSHA256: newASHA,
		SourceTitle: "new title",
	}}); err != nil {
		t.Fatalf("scenario identity cycle: %v", err)
	}
	canonicalID := eventSegmentID(91, "scenario-a", "1", "talk", 0, "body")
	var sourceText string
	if err := database.QueryRow(`SELECT source_text FROM event_story_segments WHERE segment_id=?`, canonicalID).Scan(&sourceText); err != nil || sourceText != "new body" {
		t.Fatalf("cycled canonical source=%q err=%v", sourceText, err)
	}
	var recoveryLocalizations int
	if err := database.QueryRow(`SELECT COUNT(*) FROM event_story_segment_localizations localization
		JOIN event_story_segments segment ON segment.segment_id=localization.segment_id
		WHERE segment.segment_id LIKE ? AND segment.source_text='old body' AND localization.text='Old English'`,
		canonicalID+":recovery%").Scan(&recoveryLocalizations); err != nil || recoveryLocalizations != 1 {
		t.Fatalf("cycled recovery localizations=%d err=%v", recoveryLocalizations, err)
	}
	targets, err := events.UntranslatedTargets(91)
	if err != nil || len(targets) != 2 {
		t.Fatalf("cycled targets=%+v err=%v", targets, err)
	}
	for _, target := range targets {
		for _, segmentID := range target.SegmentIDs {
			if strings.Contains(segmentID, ":recovery") {
				t.Fatalf("AI target included recovery segment=%s", segmentID)
			}
		}
	}
	if err := events.ImportOrdered(91, model.EventStoryMeta{Source: "jp_pending"}, []OrderedEpisode{{
		EpisodeNo: "1", ScenarioID: "scenario-a", ScenarioCanonicalJSON: newA, ScenarioSHA256: newASHA,
		Title: "new title", TitleSource: "jp_pending", SourceTitle: "new title",
		TalkKeys: []string{"new body"}, TalkData: map[string]string{"new body": ""},
		Lines: []OrderedLine{{JPKey: "new body", Source: "jp_pending", ScenarioPosition: 0, Field: "body"}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM event_story_segment_localizations localization
		JOIN event_story_segments segment ON segment.segment_id=localization.segment_id
		WHERE segment.segment_id LIKE ? AND segment.source_text='old body' AND localization.text='Old English'`,
		canonicalID+":recovery%").Scan(&recoveryLocalizations); err != nil || recoveryLocalizations != 1 {
		t.Fatalf("reimported recovery localizations=%d err=%v", recoveryLocalizations, err)
	}
	detail, err = events.DetailLocale(91, model.LocaleEnglish)
	if err != nil {
		t.Fatal(err)
	}
	seenIDs := map[string]bool{}
	newBodies := 0
	for _, segment := range detail.Episodes["1"].Segments {
		if seenIDs[segment.ID] || strings.Contains(segment.ID, ":recovery") || segment.Japanese == "old body" {
			t.Fatalf("active read exposed duplicate or recovery segment=%+v", segment)
		}
		seenIDs[segment.ID] = true
		if segment.Japanese == "new body" {
			newBodies++
		}
	}
	if newBodies != 1 {
		t.Fatalf("active read new body count=%d segments=%+v", newBodies, detail.Episodes["1"].Segments)
	}
	exported, err := New(database).ExportEventContent()
	if err != nil {
		t.Fatal(err)
	}
	exportedIDs := map[string]bool{}
	for _, segment := range exported.Segments {
		if exportedIDs[segment.SegmentID] || strings.Contains(segment.SegmentID, ":recovery") {
			t.Fatalf("export exposed duplicate or recovery segment=%+v", segment)
		}
		exportedIDs[segment.SegmentID] = true
	}
	for _, localization := range exported.Localizations {
		if localization.Text == "Old English" || strings.Contains(localization.SegmentID, ":recovery") {
			t.Fatalf("export exposed recovery localization=%+v", localization)
		}
	}
}
