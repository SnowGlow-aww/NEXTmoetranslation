package store

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"moesekai/server/internal/db"
	"moesekai/server/internal/model"
)

func TestConcurrentEventTranslationRevisionAcceptsExactlyOneEdit(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "event-revision.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	events := NewEventStore(database)
	if err := events.ImportOrdered(71, model.EventStoryMeta{Source: "official_cn", Version: "1"}, []OrderedEpisode{{
		EpisodeNo: "1", ScenarioID: "scenario", Title: "标题", TitleSource: model.SourceCN,
		TalkKeys: []string{"原文"}, TalkData: map[string]string{"原文": "中文"},
	}}); err != nil {
		t.Fatal(err)
	}
	detail, err := events.DetailLocale(71, model.LocaleEnglish)
	if err != nil {
		t.Fatal(err)
	}
	var target model.EventStorySegment
	for _, segment := range detail.Episodes["1"].Segments {
		if segment.Kind == "talk" {
			target = segment
		}
	}
	if target.ID == "" || target.Revision != 0 {
		t.Fatalf("target = %+v", target)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, text := range []string{"first edit", "second edit"} {
		text := text
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			revision := target.Revision
			results <- events.UpdateLineLocaleRevision(71, "1", "原文", target.ID, target.SourceHash,
				text, model.SourceHuman, "talk", model.LocaleEnglish, text, &revision)
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	succeeded, conflicted := 0, 0
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrEventRevisionConflict):
			conflicted++
		default:
			t.Fatalf("unexpected update error: %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("succeeded=%d conflicted=%d", succeeded, conflicted)
	}
	detail, err = events.DetailLocale(71, model.LocaleEnglish)
	if err != nil {
		t.Fatal(err)
	}
	for _, segment := range detail.Episodes["1"].Segments {
		if segment.ID == target.ID && (segment.Revision != 1 || (segment.Text != "first edit" && segment.Text != "second edit")) {
			t.Fatalf("winning segment = %+v", segment)
		}
	}
	var audits int
	if err := database.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='event.locale.update'`).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("audit count=%d err=%v", audits, err)
	}
}
