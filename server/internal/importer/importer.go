// Package importer loads a translations directory (legacy layout: category JSON
// files plus an eventStory/ subdir) into the SQLite-backed stores. It is shared
// by backup restore; the migration command has its own copy with verification.
package importer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"moesekai/server/internal/legacy"
	"moesekai/server/internal/model"
	"moesekai/server/internal/store"
)

// Result summarizes an import.
type Result struct {
	Categories   int
	Entries      int
	EventStories int
	Warnings     []string
}

type Payload struct {
	Categories map[string]model.Category
	Events     []store.LegacyEventRestore
}

// ReadDir parses a complete restore candidate without mutating the database.
// Callers that need all-or-nothing restore can validate additive content and
// commit this payload with it in one transaction.
func ReadDir(src string) (Payload, Result, error) {
	payload := Payload{Categories: map[string]model.Category{}}
	var res Result
	for _, cat := range model.SupportedCategories {
		category, warnings, err := loadCompleteCategory(src, cat)
		if err != nil {
			return payload, res, err
		}
		payload.Categories[cat] = category
		res.Warnings = append(res.Warnings, warnings...)
		res.Categories++
		for _, entries := range category {
			res.Entries += len(entries)
		}
	}
	eventDir := filepath.Join(src, "eventStory")
	info, err := os.Stat(eventDir)
	if err != nil || !info.IsDir() {
		return payload, res, fmt.Errorf("missing eventStory directory")
	}
	storyFiles, err := filepath.Glob(filepath.Join(eventDir, "event_*.json"))
	if err != nil {
		return payload, res, err
	}
	for _, file := range storyFiles {
		eventID := eventIDFromPath(file)
		if eventID <= 0 {
			return payload, res, fmt.Errorf("invalid event story filename %s", filepath.Base(file))
		}
		story, err := legacy.LoadEventStory(file)
		if err != nil {
			return payload, res, fmt.Errorf("event %d: %w", eventID, err)
		}
		episodes := make([]store.OrderedEpisode, 0, len(story.EpisodeKeys))
		for _, no := range story.EpisodeKeys {
			episode := story.Episodes[no]
			episodes = append(episodes, store.OrderedEpisode{
				EpisodeNo: no, ScenarioID: episode.ScenarioID, Title: episode.Title,
				TitleSource: episode.TitleSource, TalkKeys: episode.TalkKeys,
				TalkData: episode.TalkData, TalkSources: episode.TalkSources,
				SpeakerNames: episode.SpeakerNames,
			})
		}
		payload.Events = append(payload.Events, store.LegacyEventRestore{EventID: eventID, Meta: story.Meta, Episodes: episodes})
		res.EventStories++
	}
	return payload, res, nil
}

func loadCompleteCategory(src, category string) (model.Category, []string, error) {
	flatPath := filepath.Join(src, category+".json")
	fullPath := filepath.Join(src, category+".full.json")
	flatBody, err := os.ReadFile(flatPath)
	if err != nil {
		return nil, nil, fmt.Errorf("missing %s.json: %w", category, err)
	}
	if _, err := os.Stat(fullPath); err != nil {
		return nil, nil, fmt.Errorf("missing %s.full.json: %w", category, err)
	}
	var flat map[string]map[string]string
	if err := json.Unmarshal(flatBody, &flat); err != nil {
		return nil, nil, fmt.Errorf("%s.json: %w", category, err)
	}
	loaded, warnings, err := legacy.LoadCategory(src, category)
	if err != nil {
		return nil, warnings, err
	}
	projected := make(map[string]map[string]string, len(loaded))
	for field, entries := range loaded {
		projected[field] = make(map[string]string, len(entries))
		for key, entry := range entries {
			projected[field][key] = entry.Text
		}
	}
	if !reflect.DeepEqual(projected, flat) {
		return nil, warnings, fmt.Errorf("%s flat/full projections do not match", category)
	}
	return loaded, warnings, nil
}

// ImportDir loads every category and event story under src into the stores,
// then fires a single change notification so public files regenerate. src must
// directly contain X.json/X.full.json and an eventStory/ subdir.
func ImportDir(src string, s *store.Store, es *store.EventStore) (Result, error) {
	_ = es
	payload, res, err := ReadDir(src)
	if err != nil {
		return res, err
	}
	if err := s.RestoreBackup(payload.Categories, payload.Events, nil, store.EventContentExport{}, store.LyricsContentExport{}, false, "import"); err != nil {
		return res, err
	}
	return res, nil
}

func eventIDFromPath(path string) int {
	base := strings.TrimSuffix(filepath.Base(path), ".json")
	base = strings.TrimPrefix(base, "event_")
	n, err := strconv.Atoi(base)
	if err != nil {
		return -1
	}
	return n
}

// ValidateDir parses the complete legacy projection before a destructive
// restore. Every generated category pair and the event directory are required.
func ValidateDir(src string) error {
	_, _, err := ReadDir(src)
	return err
}
