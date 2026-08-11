package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"moesekai/server/internal/db"
	"moesekai/server/internal/model"
)

func TestCanceledEventImportLeavesCommittedStoryUnchanged(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "canceled-event-import.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	events := NewEventStore(database)
	meta := model.EventStoryMeta{Source: "official_cn", Version: "1", LastUpdated: 1}
	original := []OrderedEpisode{{
		EpisodeNo: "1", ScenarioID: "scenario-1", Title: "original", TitleSource: model.SourceCN,
		TalkKeys: []string{"jp"}, TalkData: map[string]string{"jp": "original line"},
	}}
	if err := events.ImportOrdered(1, meta, original); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	replacement := []OrderedEpisode{{
		EpisodeNo: "1", ScenarioID: "scenario-1", Title: "replacement", TitleSource: model.SourceCN,
		TalkKeys: []string{"jp"}, TalkData: map[string]string{"jp": "replacement line"},
	}}
	if _, err := events.ImportOrderedForSyncContext(ctx, 1, meta, replacement); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled event import error = %v", err)
	}
	detail, err := events.OrderedDetail(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Episodes) != 1 || detail.Episodes[0].Title != "original" || detail.Episodes[0].TalkData["jp"] != "original line" {
		t.Fatalf("canceled import changed story = %+v", detail)
	}
}
