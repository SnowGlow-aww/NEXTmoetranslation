package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"moesekai/server/internal/model"
)

type EntryLocalizationRecord struct {
	Category  string `json:"category"`
	Field     string `json:"field"`
	JPKey     string `json:"jpKey"`
	Locale    string `json:"locale"`
	Text      string `json:"text"`
	Source    string `json:"source"`
	UpdatedAt int64  `json:"updatedAt"`
	UpdatedBy string `json:"updatedBy"`
	Revision  int    `json:"revision"`
}

type EventSegmentRecord struct {
	SegmentID  string `json:"segmentId"`
	EventID    int    `json:"eventId"`
	EpisodeNo  string `json:"episodeNo"`
	ScenarioID string `json:"scenarioId"`
	Kind       string `json:"kind"`
	Position   int    `json:"position"`
	JPKey      string `json:"jpKey"`
	SourceText string `json:"sourceText"`
	SourceHash string `json:"sourceHash"`
}

type EventLocalizationRecord struct {
	SegmentID string `json:"segmentId"`
	Locale    string `json:"locale"`
	Text      string `json:"text"`
	Source    string `json:"source"`
	UpdatedAt int64  `json:"updatedAt"`
	UpdatedBy string `json:"updatedBy"`
	Revision  int    `json:"revision"`
}

type EventLocaleMetaRecord struct {
	EventID     int    `json:"eventId"`
	Locale      string `json:"locale"`
	LastUpdated int64  `json:"lastUpdated"`
}

type EventContentExport struct {
	Segments      []EventSegmentRecord      `json:"segments"`
	Localizations []EventLocalizationRecord `json:"localizations"`
	LocaleMeta    []EventLocaleMetaRecord   `json:"localeMeta"`
	Scenarios     []EventScenarioRecord     `json:"scenarios,omitempty"`
}

type CatalogMusicBackupRecord struct {
	MusicID          int    `json:"musicId"`
	TitleJA          string `json:"titleJa"`
	TitleZH          string `json:"titleZh"`
	TitleEN          string `json:"titleEn"`
	JacketURL        string `json:"jacketUrl"`
	NewlyWritten     int    `json:"newlyWritten"`
	UpdatedAt        int64  `json:"updatedAt"`
	ProducerMetadata string `json:"producerMetadata"`
}

type CatalogPerformerBackupRecord struct {
	PerformerID int    `json:"performerId"`
	NameJA      string `json:"nameJa"`
	NameZH      string `json:"nameZh"`
	NameEN      string `json:"nameEn"`
	UpdatedAt   int64  `json:"updatedAt"`
}

type LyricsDocumentBackupRecord struct {
	MusicID          int    `json:"musicId"`
	Revision         int    `json:"revision"`
	UpdatedAt        int64  `json:"updatedAt"`
	UpdatedBy        string `json:"updatedBy"`
	Attribution      string `json:"attribution"`
	SourceNote       string `json:"sourceNote"`
	SourceURL        string `json:"sourceUrl"`
	LicenseNote      string `json:"licenseNote"`
	SourceHash       string `json:"sourceHash"`
	SourcePageID     int    `json:"sourcePageId"`
	SourceRevisionID int    `json:"sourceRevisionId"`
	SourceSHA1       string `json:"sourceSha1"`
	SourceFetchedAt  int64  `json:"sourceFetchedAt"`
}

type LyricsLineBackupRecord struct {
	MusicID           int    `json:"musicId"`
	LineID            string `json:"lineId"`
	Position          int    `json:"position"`
	Japanese          string `json:"japanese"`
	Chinese           string `json:"zh-CN"`
	English           string `json:"en-US"`
	StanzaBreakBefore int    `json:"stanzaBreakBefore"`
}

type LyricsSegmentBackupRecord struct {
	MusicID          int    `json:"musicId"`
	LineID           string `json:"lineId"`
	Position         int    `json:"position"`
	Text             string `json:"text"`
	PerformerIDsJSON string `json:"performerIdsJson"`
}

type LyricsPublicationBackupRecord struct {
	MusicID     int    `json:"musicId"`
	Revision    int    `json:"revision"`
	UpdatedAt   int64  `json:"updatedAt"`
	PayloadJSON string `json:"payloadJson"`
}

type LyricsContentExport struct {
	Music        []CatalogMusicBackupRecord      `json:"music"`
	Performers   []CatalogPerformerBackupRecord  `json:"performers"`
	Documents    []LyricsDocumentBackupRecord    `json:"documents"`
	Lines        []LyricsLineBackupRecord        `json:"lines"`
	Segments     []LyricsSegmentBackupRecord     `json:"segments"`
	Publications []LyricsPublicationBackupRecord `json:"publications"`
}

type LegacyEventRestore struct {
	EventID  int
	Meta     model.EventStoryMeta
	Episodes []OrderedEpisode
}

func (s *Store) ExportEntryLocalizations() ([]EntryLocalizationRecord, error) {
	return s.ExportEntryLocalizationsContext(context.Background())
}

func (s *Store) ExportEntryLocalizationsContext(ctx context.Context) ([]EntryLocalizationRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT category, field, jp_key, locale, text, source, updated_at, updated_by, revision
		FROM entry_localizations ORDER BY category, field, jp_key, locale`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []EntryLocalizationRecord{}
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var record EntryLocalizationRecord
		if err := rows.Scan(&record.Category, &record.Field, &record.JPKey, &record.Locale, &record.Text,
			&record.Source, &record.UpdatedAt, &record.UpdatedBy, &record.Revision); err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, rows.Err()
}

func (s *Store) ExportEventContent() (EventContentExport, error) {
	return s.ExportEventContentContext(context.Background())
}

func (s *Store) ExportEventContentContext(ctx context.Context) (EventContentExport, error) {
	result := EventContentExport{
		Segments: []EventSegmentRecord{}, Localizations: []EventLocalizationRecord{},
		LocaleMeta: []EventLocaleMetaRecord{}, Scenarios: []EventScenarioRecord{},
	}
	type episodeIdentity struct {
		eventID   int
		episodeNo string
	}
	canonicalSegmentIDs := map[episodeIdentity]map[string]bool{}
	rows, err := s.db.QueryContext(ctx, `SELECT scenario.event_id, scenario.episode_no, scenario.scenario_id,
		scenario.canonical_json, scenario.sha256
		FROM event_story_scenarios scenario
		JOIN event_story_episodes episode
		ON episode.event_id=scenario.event_id AND episode.episode_no=scenario.episode_no
		AND episode.scenario_id=scenario.scenario_id
		ORDER BY scenario.event_id, scenario.episode_no`)
	if err != nil {
		return result, err
	}
	var scenarios []EventScenarioRecord
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			rows.Close()
			return result, err
		}
		var record EventScenarioRecord
		if err := rows.Scan(&record.EventID, &record.EpisodeNo, &record.ScenarioID, &record.CanonicalJSON,
			&record.SHA256); err != nil {
			rows.Close()
			return result, err
		}
		if err := ValidateEventScenarioRecord(record); err != nil {
			rows.Close()
			return result, fmt.Errorf("event scenario %d/%s: %w", record.EventID, record.EpisodeNo, err)
		}
		scenarios = append(scenarios, record)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return result, err
	}
	if err := rows.Close(); err != nil {
		return result, err
	}
	for _, record := range scenarios {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		expected, covered, err := eventEpisodeCanonicalSegments(s.db, record)
		if err != nil {
			return result, err
		}
		if !covered {
			return result, fmt.Errorf("event scenario %d/%s has incomplete segment coverage", record.EventID, record.EpisodeNo)
		}
		ids := make(map[string]bool, len(expected))
		for segmentID := range expected {
			ids[segmentID] = true
		}
		canonicalSegmentIDs[episodeIdentity{eventID: record.EventID, episodeNo: record.EpisodeNo}] = ids
		result.Scenarios = append(result.Scenarios, record)
	}
	rows, err = s.db.QueryContext(ctx, `SELECT segment.segment_id, segment.event_id, segment.episode_no, segment.scenario_id,
		segment.kind, segment.position, segment.jp_key, segment.source_text, segment.source_hash
		FROM event_story_segments segment
		JOIN event_story_episodes episode
		ON episode.event_id=segment.event_id AND episode.episode_no=segment.episode_no
		AND episode.scenario_id=segment.scenario_id
		ORDER BY segment.event_id, segment.episode_no, segment.kind, segment.position`)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			rows.Close()
			return result, err
		}
		var record EventSegmentRecord
		if err := rows.Scan(&record.SegmentID, &record.EventID, &record.EpisodeNo, &record.ScenarioID,
			&record.Kind, &record.Position, &record.JPKey, &record.SourceText, &record.SourceHash); err != nil {
			rows.Close()
			return result, err
		}
		if canonicalIDs := canonicalSegmentIDs[episodeIdentity{eventID: record.EventID, episodeNo: record.EpisodeNo}]; canonicalIDs != nil && !canonicalIDs[record.SegmentID] {
			continue
		}
		result.Segments = append(result.Segments, record)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return result, err
	}
	if err := rows.Close(); err != nil {
		return result, err
	}
	exportedSegmentIDs := make(map[string]bool, len(result.Segments))
	for _, segment := range result.Segments {
		exportedSegmentIDs[segment.SegmentID] = true
	}
	rows, err = s.db.QueryContext(ctx, `SELECT localization.segment_id, localization.locale, localization.text,
		localization.source, localization.updated_at, localization.updated_by, localization.revision
		FROM event_story_segment_localizations localization
		JOIN event_story_segments segment ON segment.segment_id=localization.segment_id
		JOIN event_story_episodes episode
		ON episode.event_id=segment.event_id AND episode.episode_no=segment.episode_no
		AND episode.scenario_id=segment.scenario_id
		ORDER BY localization.segment_id, localization.locale`)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			rows.Close()
			return result, err
		}
		var record EventLocalizationRecord
		if err := rows.Scan(&record.SegmentID, &record.Locale, &record.Text, &record.Source,
			&record.UpdatedAt, &record.UpdatedBy, &record.Revision); err != nil {
			rows.Close()
			return result, err
		}
		if !exportedSegmentIDs[record.SegmentID] {
			continue
		}
		result.Localizations = append(result.Localizations, record)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return result, err
	}
	if err := rows.Close(); err != nil {
		return result, err
	}
	rows, err = s.db.QueryContext(ctx, `SELECT meta.event_id, meta.locale, meta.last_updated
		FROM event_story_locale_meta meta
		JOIN event_stories story ON story.event_id=meta.event_id
		ORDER BY meta.event_id, meta.locale`)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		var record EventLocaleMetaRecord
		if err := rows.Scan(&record.EventID, &record.Locale, &record.LastUpdated); err != nil {
			return result, err
		}
		result.LocaleMeta = append(result.LocaleMeta, record)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return result, err
	}
	if err := rows.Close(); err != nil {
		return result, err
	}
	return result, nil
}

func (s *Store) ExportLyricsContent() (LyricsContentExport, error) {
	return s.ExportLyricsContentContext(context.Background())
}

func (s *Store) ExportLyricsContentContext(ctx context.Context) (LyricsContentExport, error) {
	return s.exportLyricsContentSnapshot(ctx, nil)
}

func (s *Store) exportLyricsContentSnapshot(ctx context.Context, afterDocuments func()) (LyricsContentExport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return LyricsContentExport{}, err
	}
	defer tx.Rollback()
	result := LyricsContentExport{
		Music: []CatalogMusicBackupRecord{}, Performers: []CatalogPerformerBackupRecord{},
		Documents: []LyricsDocumentBackupRecord{}, Lines: []LyricsLineBackupRecord{},
		Segments: []LyricsSegmentBackupRecord{}, Publications: []LyricsPublicationBackupRecord{},
	}
	queries := []struct {
		query string
		scan  func(*sql.Rows) error
	}{
		{`SELECT music_id, title_ja, title_zh, title_en, jacket_url, newly_written, updated_at, producer_metadata FROM catalog_music ORDER BY music_id`, func(rows *sql.Rows) error {
			var record CatalogMusicBackupRecord
			if err := rows.Scan(&record.MusicID, &record.TitleJA, &record.TitleZH, &record.TitleEN, &record.JacketURL, &record.NewlyWritten, &record.UpdatedAt, &record.ProducerMetadata); err != nil {
				return err
			}
			result.Music = append(result.Music, record)
			return nil
		}},
		{`SELECT performer_id, name_ja, name_zh, name_en, updated_at FROM catalog_performers ORDER BY performer_id`, func(rows *sql.Rows) error {
			var record CatalogPerformerBackupRecord
			if err := rows.Scan(&record.PerformerID, &record.NameJA, &record.NameZH, &record.NameEN, &record.UpdatedAt); err != nil {
				return err
			}
			result.Performers = append(result.Performers, record)
			return nil
		}},
		{`SELECT music_id, revision, updated_at, updated_by, attribution, source_note, source_url, license_note, source_hash,
			source_page_id, source_revision_id, source_sha1, source_fetched_at FROM song_lyrics ORDER BY music_id`, func(rows *sql.Rows) error {
			var record LyricsDocumentBackupRecord
			if err := rows.Scan(&record.MusicID, &record.Revision, &record.UpdatedAt, &record.UpdatedBy, &record.Attribution, &record.SourceNote, &record.SourceURL, &record.LicenseNote, &record.SourceHash,
				&record.SourcePageID, &record.SourceRevisionID, &record.SourceSHA1, &record.SourceFetchedAt); err != nil {
				return err
			}
			result.Documents = append(result.Documents, record)
			return nil
		}},
		{`SELECT music_id, line_id, position, japanese, zh_cn, en_us, stanza_break_before FROM song_lyric_lines ORDER BY music_id, position`, func(rows *sql.Rows) error {
			var record LyricsLineBackupRecord
			if err := rows.Scan(&record.MusicID, &record.LineID, &record.Position, &record.Japanese, &record.Chinese, &record.English, &record.StanzaBreakBefore); err != nil {
				return err
			}
			result.Lines = append(result.Lines, record)
			return nil
		}},
		{`SELECT music_id, line_id, position, text, performer_ids_json FROM song_lyric_segments ORDER BY music_id, line_id, position`, func(rows *sql.Rows) error {
			var record LyricsSegmentBackupRecord
			if err := rows.Scan(&record.MusicID, &record.LineID, &record.Position, &record.Text, &record.PerformerIDsJSON); err != nil {
				return err
			}
			result.Segments = append(result.Segments, record)
			return nil
		}},
		{`SELECT music_id, revision, updated_at, payload_json FROM song_lyrics_publications ORDER BY music_id`, func(rows *sql.Rows) error {
			var record LyricsPublicationBackupRecord
			if err := rows.Scan(&record.MusicID, &record.Revision, &record.UpdatedAt, &record.PayloadJSON); err != nil {
				return err
			}
			result.Publications = append(result.Publications, record)
			return nil
		}},
	}
	for queryIndex, item := range queries {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		rows, err := tx.QueryContext(ctx, item.query)
		if err != nil {
			return result, err
		}
		for rows.Next() {
			if err := ctx.Err(); err != nil {
				rows.Close()
				return result, err
			}
			if err := item.scan(rows); err != nil {
				rows.Close()
				return result, err
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return result, err
		}
		if err := rows.Close(); err != nil {
			return result, err
		}
		if queryIndex == 2 && afterDocuments != nil {
			afterDocuments()
		}
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}

func (s *Store) ImportTranslationContent(entries []EntryLocalizationRecord, events EventContentExport, lyrics LyricsContentExport) error {
	return s.ImportTranslationContentContext(context.Background(), entries, events, lyrics)
}

func (s *Store) ImportTranslationContentContext(ctx context.Context, entries []EntryLocalizationRecord, events EventContentExport, lyrics LyricsContentExport) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := importTranslationContentTx(ctx, tx, entries, events, lyrics); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.NotifyChange()
	return nil
}

func importTranslationContentTx(ctx context.Context, tx *sql.Tx, entries []EntryLocalizationRecord, events EventContentExport, lyrics LyricsContentExport) error {
	performerIDs := make(map[int]bool, len(lyrics.Performers))
	for _, performer := range lyrics.Performers {
		if err := ctx.Err(); err != nil {
			return err
		}
		if performer.PerformerID <= 0 || performerIDs[performer.PerformerID] {
			return fmt.Errorf("lyrics performer %d is invalid or duplicated", performer.PerformerID)
		}
		performerIDs[performer.PerformerID] = true
	}
	musicIDs := make(map[int]bool, len(lyrics.Music))
	for _, music := range lyrics.Music {
		if music.MusicID <= 0 || musicIDs[music.MusicID] {
			return fmt.Errorf("lyrics catalog music %d is invalid or duplicated", music.MusicID)
		}
		musicIDs[music.MusicID] = true
	}
	documentIDs := make(map[int]bool, len(lyrics.Documents))
	documentRevisions := make(map[int]int, len(lyrics.Documents))
	for _, document := range lyrics.Documents {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !musicIDs[document.MusicID] || document.Revision <= 0 || document.UpdatedAt < 0 || documentIDs[document.MusicID] {
			return fmt.Errorf("lyrics document %d has invalid identity or metadata", document.MusicID)
		}
		documentIDs[document.MusicID] = true
		documentRevisions[document.MusicID] = document.Revision
	}
	if err := validateRestoredLyricsDocuments(lyrics, performerIDs, documentIDs); err != nil {
		return err
	}
	lyrics.Publications = append([]LyricsPublicationBackupRecord(nil), lyrics.Publications...)
	for publicationIndex := range lyrics.Publications {
		if err := ctx.Err(); err != nil {
			return err
		}
		publication := &lyrics.Publications[publicationIndex]
		if !documentIDs[publication.MusicID] {
			return fmt.Errorf("lyrics publication %d has no draft document", publication.MusicID)
		}
		if publication.Revision > documentRevisions[publication.MusicID] {
			return fmt.Errorf("lyrics publication %d revision exceeds its draft document", publication.MusicID)
		}
		if err := canonicalizeRestoredPublication(publication, performerIDs); err != nil {
			return err
		}
	}
	// These side tables intentionally do not reference legacy event parents so
	// previous binaries can keep doing replace-imports without cascading away
	// new locale data. Restore still validates parent identity explicitly.
	for _, record := range events.Segments {
		if err := ctx.Err(); err != nil {
			return err
		}
		var parentScenarioID string
		if err := tx.QueryRowContext(ctx, `SELECT scenario_id FROM event_story_episodes WHERE event_id=? AND episode_no=?`,
			record.EventID, record.EpisodeNo).Scan(&parentScenarioID); err == sql.ErrNoRows {
			return fmt.Errorf("event segment %s references a missing episode", record.SegmentID)
		} else if err != nil {
			return err
		}
		if parentScenarioID != record.ScenarioID {
			return fmt.Errorf("event segment %s scenario identity does not match its parent", record.SegmentID)
		}
	}
	for _, record := range events.LocaleMeta {
		if err := ctx.Err(); err != nil {
			return err
		}
		var parentCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_stories WHERE event_id=?`, record.EventID).Scan(&parentCount); err != nil {
			return err
		}
		if parentCount != 1 {
			return fmt.Errorf("event locale metadata references missing event %d", record.EventID)
		}
	}
	for _, record := range events.Scenarios {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := ValidateEventScenarioRecord(record); err != nil {
			return fmt.Errorf("event scenario %d/%s: %w", record.EventID, record.EpisodeNo, err)
		}
		var parentScenarioID string
		if err := tx.QueryRowContext(ctx, `SELECT scenario_id FROM event_story_episodes WHERE event_id=? AND episode_no=?`,
			record.EventID, record.EpisodeNo).Scan(&parentScenarioID); err == sql.ErrNoRows {
			return fmt.Errorf("event scenario %d/%s references a missing episode", record.EventID, record.EpisodeNo)
		} else if err != nil {
			return err
		}
		if parentScenarioID != record.ScenarioID {
			return fmt.Errorf("event scenario %d/%s identity does not match its parent", record.EventID, record.EpisodeNo)
		}
	}
	if err := validateEventContentCanonicalCoverage(events); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, statement := range []string{
		`DELETE FROM entry_localizations`,
		`DELETE FROM event_story_locale_meta`,
		`DELETE FROM event_story_segment_localizations`,
		`DELETE FROM event_story_segments`,
		`DELETE FROM event_story_scenarios`,
		`DELETE FROM song_lyrics_publications`,
		`DELETE FROM song_lyric_segments`,
		`DELETE FROM song_lyric_lines`,
		`DELETE FROM song_lyrics`,
		`DELETE FROM catalog_performers`,
		`DELETE FROM catalog_music`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	for _, record := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO entry_localizations(category, field, jp_key, locale, text, source, updated_at, updated_by, revision)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, record.Category, record.Field, record.JPKey, record.Locale,
			record.Text, record.Source, record.UpdatedAt, record.UpdatedBy, record.Revision); err != nil {
			return err
		}
	}
	for _, record := range events.Segments {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO event_story_segments(segment_id, event_id, episode_no, scenario_id, kind, position, jp_key, source_text, source_hash)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, record.SegmentID, record.EventID, record.EpisodeNo, record.ScenarioID,
			record.Kind, record.Position, record.JPKey, record.SourceText, record.SourceHash); err != nil {
			return fmt.Errorf("event segment %s: %w", record.SegmentID, err)
		}
	}
	for _, record := range events.Localizations {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO event_story_segment_localizations(segment_id, locale, text, source, updated_at, updated_by, revision)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, record.SegmentID, record.Locale, record.Text, record.Source,
			record.UpdatedAt, record.UpdatedBy, record.Revision); err != nil {
			return err
		}
	}
	for _, record := range events.LocaleMeta {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO event_story_locale_meta(event_id, locale, last_updated) VALUES (?, ?, ?)`,
			record.EventID, record.Locale, record.LastUpdated); err != nil {
			return err
		}
	}
	for _, record := range events.Scenarios {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO event_story_scenarios(event_id, episode_no, scenario_id, canonical_json, sha256)
			VALUES (?, ?, ?, ?, ?)`, record.EventID, record.EpisodeNo, record.ScenarioID, record.CanonicalJSON, record.SHA256); err != nil {
			return err
		}
	}
	for _, record := range lyrics.Music {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO catalog_music(music_id, title_ja, title_zh, title_en, jacket_url, newly_written, updated_at, producer_metadata)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, record.MusicID, record.TitleJA, record.TitleZH, record.TitleEN,
			record.JacketURL, record.NewlyWritten, record.UpdatedAt, record.ProducerMetadata); err != nil {
			return err
		}
	}
	for _, record := range lyrics.Performers {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO catalog_performers(performer_id, name_ja, name_zh, name_en, updated_at)
			VALUES (?, ?, ?, ?, ?)`, record.PerformerID, record.NameJA, record.NameZH, record.NameEN, record.UpdatedAt); err != nil {
			return err
		}
	}
	for _, record := range lyrics.Documents {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO song_lyrics(music_id, revision, updated_at, updated_by, attribution, source_note, source_url, license_note, source_hash,
			source_page_id, source_revision_id, source_sha1, source_fetched_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, record.MusicID, record.Revision, record.UpdatedAt, record.UpdatedBy,
			record.Attribution, record.SourceNote, record.SourceURL, record.LicenseNote, record.SourceHash,
			record.SourcePageID, record.SourceRevisionID, record.SourceSHA1, record.SourceFetchedAt); err != nil {
			return err
		}
	}
	for _, record := range lyrics.Lines {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO song_lyric_lines(music_id, line_id, position, japanese, zh_cn, en_us, stanza_break_before)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, record.MusicID, record.LineID, record.Position, record.Japanese,
			record.Chinese, record.English, record.StanzaBreakBefore); err != nil {
			return err
		}
	}
	for _, record := range lyrics.Segments {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO song_lyric_segments(music_id, line_id, position, text, performer_ids_json)
			VALUES (?, ?, ?, ?, ?)`, record.MusicID, record.LineID, record.Position, record.Text, record.PerformerIDsJSON); err != nil {
			return err
		}
	}
	for _, record := range lyrics.Publications {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO song_lyrics_publications(music_id, revision, updated_at, payload_json)
			VALUES (?, ?, ?, ?)`, record.MusicID, record.Revision, record.UpdatedAt, record.PayloadJSON); err != nil {
			return err
		}
	}
	return nil
}

func validateEventContentCanonicalCoverage(events EventContentExport) error {
	type scenarioIdentity struct {
		eventID    int
		episodeNo  string
		scenarioID string
	}
	segmentsByID := make(map[string]EventSegmentRecord, len(events.Segments))
	for _, segment := range events.Segments {
		if _, exists := segmentsByID[segment.SegmentID]; exists {
			return fmt.Errorf("event segment %s appears more than once", segment.SegmentID)
		}
		segmentsByID[segment.SegmentID] = segment
	}
	expectedByScenario := make(map[scenarioIdentity]map[string]bool, len(events.Scenarios))
	for _, scenario := range events.Scenarios {
		identity := scenarioIdentity{eventID: scenario.EventID, episodeNo: scenario.EpisodeNo, scenarioID: scenario.ScenarioID}
		if _, exists := expectedByScenario[identity]; exists {
			return fmt.Errorf("event scenario %d/%s appears more than once", scenario.EventID, scenario.EpisodeNo)
		}
		definitions, err := eventScenarioSegmentDefinitions(scenario, "")
		if err != nil {
			return err
		}
		expected := make(map[string]bool, len(definitions))
		for _, definition := range definitions {
			segment, exists := segmentsByID[definition.SegmentID]
			if !exists {
				return fmt.Errorf("event scenario %d/%s is missing canonical %s position %d",
					scenario.EventID, scenario.EpisodeNo, definition.Kind, definition.Position)
			}
			if definition.Kind == "title" {
				definition.SourceText = segment.SourceText
				definition.SourceHash = hashText(segment.SourceText)
			}
			if !eventSegmentMatchesDefinition(segment, definition) {
				return fmt.Errorf("event segment %s does not match its canonical definition", segment.SegmentID)
			}
			expected[definition.SegmentID] = true
		}
		expectedByScenario[identity] = expected
	}
	for _, segment := range events.Segments {
		identity := scenarioIdentity{eventID: segment.EventID, episodeNo: segment.EpisodeNo, scenarioID: segment.ScenarioID}
		if expected := expectedByScenario[identity]; expected != nil && !expected[segment.SegmentID] {
			return fmt.Errorf("event segment %s is non-canonical recovery data", segment.SegmentID)
		}
	}
	for _, localization := range events.Localizations {
		if _, exists := segmentsByID[localization.SegmentID]; !exists {
			return fmt.Errorf("event localization references missing segment %s", localization.SegmentID)
		}
	}
	return nil
}

func validateRestoredLyricsDocuments(lyrics LyricsContentExport, performerIDs, documentIDs map[int]bool) error {
	type lineIdentity struct {
		musicID int
		lineID  string
	}
	type positionedSegment struct {
		position int
		segment  model.LyricSegment
	}
	type restoredLine struct {
		line     model.LyricLine
		segments []positionedSegment
	}

	lines := make(map[lineIdentity]*restoredLine, len(lyrics.Lines))
	lineOrder := make(map[int][]lineIdentity, len(documentIDs))
	for _, record := range lyrics.Lines {
		identity := lineIdentity{musicID: record.MusicID, lineID: record.LineID}
		if !documentIDs[record.MusicID] || (record.StanzaBreakBefore != 0 && record.StanzaBreakBefore != 1) {
			return fmt.Errorf("lyrics line %d/%s references an invalid document or stanza flag", record.MusicID, record.LineID)
		}
		if strings.TrimSpace(record.LineID) == "" || lines[identity] != nil {
			return fmt.Errorf("lyrics line %d/%s is empty or duplicated", record.MusicID, record.LineID)
		}
		lines[identity] = &restoredLine{line: model.LyricLine{
			ID: record.LineID, Order: record.Position, Japanese: record.Japanese, Chinese: record.Chinese, English: record.English,
			StanzaBreakBefore: record.StanzaBreakBefore == 1, Segments: []model.LyricSegment{},
		}}
		lineOrder[record.MusicID] = append(lineOrder[record.MusicID], identity)
	}
	segmentPositions := make(map[lineIdentity]map[int]bool, len(lines))
	for _, record := range lyrics.Segments {
		identity := lineIdentity{musicID: record.MusicID, lineID: record.LineID}
		line := lines[identity]
		if line == nil || record.Position < 0 {
			return fmt.Errorf("lyrics segment %d/%s/%d references an invalid line or position", record.MusicID, record.LineID, record.Position)
		}
		if segmentPositions[identity] == nil {
			segmentPositions[identity] = map[int]bool{}
		}
		if segmentPositions[identity][record.Position] {
			return fmt.Errorf("lyrics segment %d/%s/%d is duplicated", record.MusicID, record.LineID, record.Position)
		}
		segmentPositions[identity][record.Position] = true
		var performerIDsJSON []int
		decoder := json.NewDecoder(bytes.NewBufferString(record.PerformerIDsJSON))
		if err := decoder.Decode(&performerIDsJSON); err != nil {
			return fmt.Errorf("lyrics segment %d/%s/%d performerIds: %w", record.MusicID, record.LineID, record.Position, err)
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			return fmt.Errorf("lyrics segment %d/%s/%d performerIds has trailing JSON", record.MusicID, record.LineID, record.Position)
		}
		line.segments = append(line.segments, positionedSegment{
			position: record.Position,
			segment:  model.LyricSegment{Text: record.Text, PerformerIDs: performerIDsJSON},
		})
	}
	for _, document := range lyrics.Documents {
		documentLines := make([]model.LyricLine, 0, len(lineOrder[document.MusicID]))
		for _, identity := range lineOrder[document.MusicID] {
			line := lines[identity]
			sort.Slice(line.segments, func(left, right int) bool { return line.segments[left].position < line.segments[right].position })
			for _, segment := range line.segments {
				line.line.Segments = append(line.line.Segments, segment.segment)
			}
			documentLines = append(documentLines, line.line)
		}
		candidate := model.SongLyrics{
			MusicID: document.MusicID, Revision: document.Revision, UpdatedAt: formatTimestamp(document.UpdatedAt),
			Attribution: document.Attribution, SourceNote: document.SourceNote, SourceURL: document.SourceURL,
			LicenseNote: document.LicenseNote, SourcePageID: document.SourcePageID, SourceRevisionID: document.SourceRevisionID,
			SourceSHA1: document.SourceSHA1, Lines: documentLines,
		}
		if document.SourceFetchedAt > 0 {
			candidate.SourceFetchedAt = formatTimestamp(document.SourceFetchedAt)
		}
		if _, err := validateLyricsProvenance(candidate); err != nil {
			return fmt.Errorf("lyrics document %d provenance: %w", document.MusicID, err)
		}
		code, details, sourceHash := validateLyrics(candidate, performerIDs, false)
		if code != "" {
			return fmt.Errorf("lyrics document %d violates %s: %s", document.MusicID, code, strings.Join(details, "; "))
		}
		if sourceHash != document.SourceHash {
			return fmt.Errorf("lyrics document %d source hash does not match its lines", document.MusicID)
		}
	}
	return nil
}

func canonicalizeRestoredPublication(record *LyricsPublicationBackupRecord, performerIDs map[int]bool) error {
	decoder := json.NewDecoder(bytes.NewBufferString(record.PayloadJSON))
	decoder.DisallowUnknownFields()
	var public model.PublicSongLyrics
	if err := decoder.Decode(&public); err != nil {
		return fmt.Errorf("lyrics publication %d: %w", record.MusicID, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("lyrics publication %d has trailing JSON", record.MusicID)
	}
	updatedAt, err := parseTimestamp(public.UpdatedAt)
	if err != nil {
		return fmt.Errorf("lyrics publication %d has invalid updatedAt", record.MusicID)
	}
	if public.Version != 1 || public.MusicID != record.MusicID || public.Revision != record.Revision || updatedAt != record.UpdatedAt {
		return fmt.Errorf("lyrics publication %d identity does not match its manifest record", record.MusicID)
	}
	lyrics := model.SongLyrics{MusicID: public.MusicID, Revision: public.Revision, UpdatedAt: public.UpdatedAt,
		Attribution: public.Attribution, Lines: public.Lines}
	if code, details, _ := validateLyrics(lyrics, performerIDs, true); code != "" {
		return fmt.Errorf("lyrics publication %d violates %s: %s", record.MusicID, code, strings.Join(details, "; "))
	}
	public.Lines = publicLyricsLines(public.Lines)
	payload, err := json.Marshal(public)
	if err != nil {
		return fmt.Errorf("lyrics publication %d: %w", record.MusicID, err)
	}
	record.PayloadJSON = string(payload)
	return nil
}

// RestoreBackup commits the legacy public projection and optional additive
// content in one transaction. A missing additive manifest is an old backup: it
// deliberately clears multilingual and lyrics-only state instead of retaining
// unrelated data from the database being replaced.
func (s *Store) RestoreBackup(categories map[string]model.Category, events []LegacyEventRestore,
	entries []EntryLocalizationRecord, eventContent EventContentExport, lyrics LyricsContentExport,
	additivePresent bool, actor string) error {
	return s.RestoreBackupContext(context.Background(), categories, events, entries, eventContent, lyrics, additivePresent, actor)
}

func (s *Store) RestoreBackupContext(ctx context.Context, categories map[string]model.Category, events []LegacyEventRestore,
	entries []EntryLocalizationRecord, eventContent EventContentExport, lyrics LyricsContentExport,
	additivePresent bool, actor string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, category := range model.SupportedCategories {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, ok := categories[category]; !ok {
			return fmt.Errorf("restore is missing category %s", category)
		}
	}
	for _, statement := range []string{
		`DELETE FROM event_story_locale_meta`,
		`DELETE FROM event_story_scenarios`,
		`DELETE FROM event_story_segments`,
		`DELETE FROM event_stories`,
		`DELETE FROM entries`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	for _, category := range model.SupportedCategories {
		if err := ctx.Err(); err != nil {
			return err
		}
		content := categories[category]
		if err := importCategoryTx(ctx, tx, category, content); err != nil {
			return fmt.Errorf("import %s: %w", category, err)
		}
	}
	for _, event := range events {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := importOrderedTx(ctx, tx, event.EventID, event.Meta, event.Episodes, false); err != nil {
			return fmt.Errorf("import event %d: %w", event.EventID, err)
		}
	}
	if additivePresent {
		if err := importTranslationContentTx(ctx, tx, entries, eventContent, lyrics); err != nil {
			return err
		}
	} else {
		for _, statement := range []string{
			`DELETE FROM entry_localizations`,
			`DELETE FROM event_story_segment_localizations WHERE locale<>'zh-CN'`,
			`DELETE FROM song_lyrics_publications`,
			`DELETE FROM song_lyric_segments`,
			`DELETE FROM song_lyric_lines`,
			`DELETE FROM song_lyrics`,
			`DELETE FROM catalog_performers`,
			`DELETE FROM catalog_music`,
		} {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_log(ts, user, action, detail) VALUES (?, ?, 'backup.restore', ?)`,
		time.Now().Unix(), actor, fmt.Sprintf("additive=%t categories=%d events=%d", additivePresent, len(categories), len(events))); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.NotifyChange()
	return nil
}

func importCategoryTx(ctx context.Context, tx *sql.Tx, category string, content model.Category) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM entries WHERE category=?`, category); err != nil {
		return err
	}
	now := time.Now().Unix()
	for field, entries := range content {
		for jpKey, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			idsJSON := ""
			if len(entry.Ids) > 0 {
				encoded, err := json.Marshal(entry.Ids)
				if err != nil {
					return err
				}
				idsJSON = string(encoded)
			}
			source := entry.Source
			if source == "" {
				source = model.SourceUnknown
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO entries
				(category, field, jp_key, cn_text, source, ids_json, updated_at, updated_by)
				VALUES (?, ?, ?, ?, ?, ?, ?, 'restore')`, category, field, jpKey, entry.Text, source, idsJSON, now); err != nil {
				return err
			}
		}
	}
	return nil
}
