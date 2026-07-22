package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
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
	rows, err := s.db.Query(`SELECT category, field, jp_key, locale, text, source, updated_at, updated_by, revision
		FROM entry_localizations ORDER BY category, field, jp_key, locale`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []EntryLocalizationRecord{}
	for rows.Next() {
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
	result := EventContentExport{Segments: []EventSegmentRecord{}, Localizations: []EventLocalizationRecord{}, LocaleMeta: []EventLocaleMetaRecord{}}
	rows, err := s.db.Query(`SELECT segment_id, event_id, episode_no, scenario_id, kind, position, jp_key, source_text, source_hash
		FROM event_story_segments ORDER BY event_id, episode_no, kind, position`)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var record EventSegmentRecord
		if err := rows.Scan(&record.SegmentID, &record.EventID, &record.EpisodeNo, &record.ScenarioID,
			&record.Kind, &record.Position, &record.JPKey, &record.SourceText, &record.SourceHash); err != nil {
			rows.Close()
			return result, err
		}
		result.Segments = append(result.Segments, record)
	}
	if err := rows.Close(); err != nil {
		return result, err
	}
	rows, err = s.db.Query(`SELECT segment_id, locale, text, source, updated_at, updated_by, revision
		FROM event_story_segment_localizations ORDER BY segment_id, locale`)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var record EventLocalizationRecord
		if err := rows.Scan(&record.SegmentID, &record.Locale, &record.Text, &record.Source,
			&record.UpdatedAt, &record.UpdatedBy, &record.Revision); err != nil {
			rows.Close()
			return result, err
		}
		result.Localizations = append(result.Localizations, record)
	}
	if err := rows.Close(); err != nil {
		return result, err
	}
	rows, err = s.db.Query(`SELECT event_id, locale, last_updated FROM event_story_locale_meta ORDER BY event_id, locale`)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var record EventLocaleMetaRecord
		if err := rows.Scan(&record.EventID, &record.Locale, &record.LastUpdated); err != nil {
			return result, err
		}
		result.LocaleMeta = append(result.LocaleMeta, record)
	}
	return result, rows.Err()
}

func (s *Store) ExportLyricsContent() (LyricsContentExport, error) {
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
	for _, item := range queries {
		rows, err := s.db.Query(item.query)
		if err != nil {
			return result, err
		}
		for rows.Next() {
			if err := item.scan(rows); err != nil {
				rows.Close()
				return result, err
			}
		}
		if err := rows.Close(); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (s *Store) ImportTranslationContent(entries []EntryLocalizationRecord, events EventContentExport, lyrics LyricsContentExport) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := importTranslationContentTx(tx, entries, events, lyrics); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.NotifyChange()
	return nil
}

func importTranslationContentTx(tx *sql.Tx, entries []EntryLocalizationRecord, events EventContentExport, lyrics LyricsContentExport) error {
	// These side tables intentionally do not reference legacy event parents so
	// previous binaries can keep doing replace-imports without cascading away
	// new locale data. Restore still validates parent identity explicitly.
	for _, record := range events.Segments {
		var parentCount int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM event_story_episodes WHERE event_id=? AND episode_no=?`,
			record.EventID, record.EpisodeNo).Scan(&parentCount); err != nil {
			return err
		}
		if parentCount != 1 {
			return fmt.Errorf("event segment %s references a missing episode", record.SegmentID)
		}
	}
	for _, record := range events.LocaleMeta {
		var parentCount int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM event_stories WHERE event_id=?`, record.EventID).Scan(&parentCount); err != nil {
			return err
		}
		if parentCount != 1 {
			return fmt.Errorf("event locale metadata references missing event %d", record.EventID)
		}
	}
	for _, statement := range []string{
		`DELETE FROM entry_localizations`,
		`DELETE FROM event_story_locale_meta`,
		`DELETE FROM event_story_segment_localizations`,
		`DELETE FROM event_story_segments`,
		`DELETE FROM song_lyrics_publications`,
		`DELETE FROM song_lyric_segments`,
		`DELETE FROM song_lyric_lines`,
		`DELETE FROM song_lyrics`,
		`DELETE FROM catalog_performers`,
		`DELETE FROM catalog_music`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	for _, record := range entries {
		if _, err := tx.Exec(`INSERT INTO entry_localizations(category, field, jp_key, locale, text, source, updated_at, updated_by, revision)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, record.Category, record.Field, record.JPKey, record.Locale,
			record.Text, record.Source, record.UpdatedAt, record.UpdatedBy, record.Revision); err != nil {
			return err
		}
	}
	for _, record := range events.Segments {
		if _, err := tx.Exec(`INSERT INTO event_story_segments(segment_id, event_id, episode_no, scenario_id, kind, position, jp_key, source_text, source_hash)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, record.SegmentID, record.EventID, record.EpisodeNo, record.ScenarioID,
			record.Kind, record.Position, record.JPKey, record.SourceText, record.SourceHash); err != nil {
			return fmt.Errorf("event segment %s: %w", record.SegmentID, err)
		}
	}
	for _, record := range events.Localizations {
		if _, err := tx.Exec(`INSERT INTO event_story_segment_localizations(segment_id, locale, text, source, updated_at, updated_by, revision)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, record.SegmentID, record.Locale, record.Text, record.Source,
			record.UpdatedAt, record.UpdatedBy, record.Revision); err != nil {
			return err
		}
	}
	for _, record := range events.LocaleMeta {
		if _, err := tx.Exec(`INSERT INTO event_story_locale_meta(event_id, locale, last_updated) VALUES (?, ?, ?)`,
			record.EventID, record.Locale, record.LastUpdated); err != nil {
			return err
		}
	}
	for _, record := range lyrics.Music {
		if _, err := tx.Exec(`INSERT INTO catalog_music(music_id, title_ja, title_zh, title_en, jacket_url, newly_written, updated_at, producer_metadata)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, record.MusicID, record.TitleJA, record.TitleZH, record.TitleEN,
			record.JacketURL, record.NewlyWritten, record.UpdatedAt, record.ProducerMetadata); err != nil {
			return err
		}
	}
	for _, record := range lyrics.Performers {
		if _, err := tx.Exec(`INSERT INTO catalog_performers(performer_id, name_ja, name_zh, name_en, updated_at)
			VALUES (?, ?, ?, ?, ?)`, record.PerformerID, record.NameJA, record.NameZH, record.NameEN, record.UpdatedAt); err != nil {
			return err
		}
	}
	lyricsAttribution := map[int]string{}
	for _, record := range lyrics.Documents {
		lyricsAttribution[record.MusicID] = record.Attribution
		if _, err := tx.Exec(`INSERT INTO song_lyrics(music_id, revision, updated_at, updated_by, attribution, source_note, source_url, license_note, source_hash,
			source_page_id, source_revision_id, source_sha1, source_fetched_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, record.MusicID, record.Revision, record.UpdatedAt, record.UpdatedBy,
			record.Attribution, record.SourceNote, record.SourceURL, record.LicenseNote, record.SourceHash,
			record.SourcePageID, record.SourceRevisionID, record.SourceSHA1, record.SourceFetchedAt); err != nil {
			return err
		}
	}
	for _, record := range lyrics.Lines {
		if _, err := tx.Exec(`INSERT INTO song_lyric_lines(music_id, line_id, position, japanese, zh_cn, en_us, stanza_break_before)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, record.MusicID, record.LineID, record.Position, record.Japanese,
			record.Chinese, record.English, record.StanzaBreakBefore); err != nil {
			return err
		}
	}
	for _, record := range lyrics.Segments {
		if _, err := tx.Exec(`INSERT INTO song_lyric_segments(music_id, line_id, position, text, performer_ids_json)
			VALUES (?, ?, ?, ?, ?)`, record.MusicID, record.LineID, record.Position, record.Text, record.PerformerIDsJSON); err != nil {
			return err
		}
	}
	for _, record := range lyrics.Publications {
		attribution := strings.TrimSpace(lyricsAttribution[record.MusicID])
		if attribution == "" {
			continue
		}
		var public model.PublicSongLyrics
		if err := json.Unmarshal([]byte(record.PayloadJSON), &public); err != nil {
			return fmt.Errorf("lyrics publication %d: %w", record.MusicID, err)
		}
		if public.Attribution != lyricsAttribution[record.MusicID] {
			return fmt.Errorf("lyrics publication %d attribution does not match its draft", record.MusicID)
		}
		if _, err := tx.Exec(`INSERT INTO song_lyrics_publications(music_id, revision, updated_at, payload_json)
			VALUES (?, ?, ?, ?)`, record.MusicID, record.Revision, record.UpdatedAt, record.PayloadJSON); err != nil {
			return err
		}
	}
	return nil
}

// RestoreBackup commits the legacy public projection and optional additive
// content in one transaction. A missing additive manifest is an old backup: it
// deliberately clears multilingual and lyrics-only state instead of retaining
// unrelated data from the database being replaced.
func (s *Store) RestoreBackup(categories map[string]model.Category, events []LegacyEventRestore,
	entries []EntryLocalizationRecord, eventContent EventContentExport, lyrics LyricsContentExport,
	additivePresent bool, actor string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, category := range model.SupportedCategories {
		content, ok := categories[category]
		if !ok {
			continue
		}
		if err := importCategoryTx(tx, category, content); err != nil {
			return fmt.Errorf("import %s: %w", category, err)
		}
	}
	for _, event := range events {
		if err := importOrderedTx(tx, event.EventID, event.Meta, event.Episodes, false); err != nil {
			return fmt.Errorf("import event %d: %w", event.EventID, err)
		}
	}
	if additivePresent {
		if err := importTranslationContentTx(tx, entries, eventContent, lyrics); err != nil {
			return err
		}
	} else {
		for _, statement := range []string{
			`DELETE FROM entry_localizations`,
			`DELETE FROM event_story_locale_meta`,
			`DELETE FROM event_story_segment_localizations WHERE locale<>'zh-CN'`,
			`DELETE FROM song_lyrics_publications`,
			`DELETE FROM song_lyric_segments`,
			`DELETE FROM song_lyric_lines`,
			`DELETE FROM song_lyrics`,
			`DELETE FROM catalog_performers`,
			`DELETE FROM catalog_music`,
		} {
			if _, err := tx.Exec(statement); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(`INSERT INTO audit_log(ts, user, action, detail) VALUES (?, ?, 'backup.restore', ?)`,
		time.Now().Unix(), actor, fmt.Sprintf("additive=%t categories=%d events=%d", additivePresent, len(categories), len(events))); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.NotifyChange()
	return nil
}

func importCategoryTx(tx *sql.Tx, category string, content model.Category) error {
	if _, err := tx.Exec(`DELETE FROM entries WHERE category=?`, category); err != nil {
		return err
	}
	now := time.Now().Unix()
	for field, entries := range content {
		for jpKey, entry := range entries {
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
			if _, err := tx.Exec(`INSERT INTO entries
				(category, field, jp_key, cn_text, source, ids_json, updated_at, updated_by)
				VALUES (?, ?, ?, ?, ?, ?, ?, 'restore')`, category, field, jpKey, entry.Text, source, idsJSON, now); err != nil {
				return err
			}
		}
	}
	return nil
}
