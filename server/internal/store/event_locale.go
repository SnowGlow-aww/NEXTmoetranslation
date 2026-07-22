package store

import (
	"database/sql"
	"fmt"
	"time"

	"moesekai/server/internal/model"
)

func (s *EventStore) DetailLocale(eventID int, locale string) (model.EventStoryDetail, error) {
	if locale == model.LocaleChinese {
		return s.Detail(eventID)
	}
	ordered, err := s.OrderedDetail(eventID)
	if err != nil {
		return model.EventStoryDetail{}, err
	}
	detail := model.EventStoryDetail{Meta: ordered.Meta, Episodes: map[string]model.EventStoryEpisode{}}
	baseByEpisode := map[string]OrderedEpisode{}
	for _, episode := range ordered.Episodes {
		baseByEpisode[episode.EpisodeNo] = episode
		detail.Episodes[episode.EpisodeNo] = model.EventStoryEpisode{
			ScenarioID: episode.ScenarioID, TalkData: map[string]string{},
			TalkSources: map[string]string{}, SpeakerNames: episode.SpeakerNames,
		}
	}
	rows, err := s.db.Query(`SELECT seg.segment_id, seg.episode_no, seg.kind, seg.position,
		seg.jp_key, seg.source_text, loc.text, loc.source
		FROM event_story_segments seg
		LEFT JOIN event_story_segment_localizations loc ON loc.segment_id=seg.segment_id AND loc.locale=?
		WHERE seg.event_id=? ORDER BY seg.episode_no, seg.position`, locale, eventID)
	if err != nil {
		return model.EventStoryDetail{}, err
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var id, episodeNo, kind, jpKey, sourceText string
		var position int
		var localizedText, localizedSource sql.NullString
		if err := rows.Scan(&id, &episodeNo, &kind, &position, &jpKey, &sourceText, &localizedText, &localizedSource); err != nil {
			return model.EventStoryDetail{}, err
		}
		episode, ok := detail.Episodes[episodeNo]
		if !ok {
			continue
		}
		text, source := "", model.SourceUnknown
		if locale == model.LocaleJapanese {
			text = sourceText
		} else if localizedText.Valid {
			text = localizedText.String
			source = localizedSource.String
		}
		segment := model.EventStorySegment{
			ID: id, Kind: kind, Position: position, Japanese: sourceText, Text: text, Source: source,
		}
		episode.Segments = append(episode.Segments, segment)
		if kind == "title" {
			episode.Title = text
			episode.TitleSource = source
		} else {
			episode.TalkData[jpKey] = text
			episode.TalkSources[jpKey] = source
			episode.TalkOrder = append(episode.TalkOrder, jpKey)
		}
		detail.Episodes[episodeNo] = episode
		seen++
	}
	if err := rows.Err(); err != nil {
		return model.EventStoryDetail{}, err
	}
	if seen == 0 {
		// A previous binary can create stories while the new tables already exist.
		// Serve a non-fabricated projection until the current binary next imports it.
		for episodeNo, base := range baseByEpisode {
			episode := detail.Episodes[episodeNo]
			if locale == model.LocaleJapanese {
				for _, key := range base.TalkKeys {
					episode.TalkData[key] = key
					episode.TalkSources[key] = model.SourceUnknown
					episode.TalkOrder = append(episode.TalkOrder, key)
				}
			} else {
				for _, key := range base.TalkKeys {
					episode.TalkData[key] = ""
					episode.TalkSources[key] = model.SourceUnknown
					episode.TalkOrder = append(episode.TalkOrder, key)
				}
			}
			detail.Episodes[episodeNo] = episode
		}
	}
	return detail, nil
}

func (s *EventStore) ListLocale(locale string) ([]model.EventStorySummary, error) {
	if locale == model.LocaleChinese {
		return s.List()
	}
	rows, err := s.db.Query(`SELECT stories.event_id, stories.source,
		(SELECT COUNT(*) FROM event_story_episodes e WHERE e.event_id=stories.event_id),
		CASE WHEN ?='ja-JP' THEN
			(SELECT COUNT(*) FROM event_story_segments seg WHERE seg.event_id=stories.event_id AND seg.source_text='')
		ELSE
			(SELECT COUNT(*) FROM event_story_segments seg
			 LEFT JOIN event_story_segment_localizations loc ON loc.segment_id=seg.segment_id AND loc.locale=?
			 WHERE seg.event_id=stories.event_id AND (loc.segment_id IS NULL OR loc.text=''))
		END,
		COALESCE((SELECT last_updated FROM event_story_locale_meta lm WHERE lm.event_id=stories.event_id AND lm.locale=?), stories.last_updated)
		FROM event_stories stories ORDER BY stories.event_id`, locale, locale, locale)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []model.EventStorySummary
	for rows.Next() {
		var summary model.EventStorySummary
		if err := rows.Scan(&summary.EventID, &summary.Source, &summary.EpisodeCount, &summary.UntranslatedCount, &summary.LastUpdated); err != nil {
			return nil, err
		}
		result = append(result, summary)
	}
	return result, rows.Err()
}

func (s *EventStore) UpdateLineLocale(eventID int, episodeNo, jpKey, segmentID, text, source, entryType, locale, user string) error {
	if locale == model.LocaleChinese {
		return s.UpdateLine(eventID, episodeNo, jpKey, text, source, entryType)
	}
	if locale == model.LocaleJapanese {
		return ErrReadOnlyLocale
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	kind := "talk"
	if entryType == "title" {
		kind = "title"
	}
	if segmentID == "" {
		query := `SELECT segment_id FROM event_story_segments WHERE event_id=? AND episode_no=? AND kind=?`
		args := []any{eventID, episodeNo, kind}
		if kind == "talk" {
			query += ` AND jp_key=?`
			args = append(args, jpKey)
		}
		query += ` ORDER BY position LIMIT 1`
		if err := tx.QueryRow(query, args...).Scan(&segmentID); err != nil {
			return err
		}
	} else {
		var found int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM event_story_segments WHERE segment_id=? AND event_id=?`, segmentID, eventID).Scan(&found); err != nil {
			return err
		}
		if found == 0 {
			return sql.ErrNoRows
		}
	}
	now := time.Now().Unix()
	if _, err := tx.Exec(`INSERT INTO event_story_segment_localizations
		(segment_id, locale, text, source, updated_at, updated_by, revision)
		VALUES (?, ?, ?, ?, ?, ?, 1)
		ON CONFLICT(segment_id, locale) DO UPDATE SET text=excluded.text, source=excluded.source,
		updated_at=excluded.updated_at, updated_by=excluded.updated_by,
		revision=event_story_segment_localizations.revision+1`,
		segmentID, locale, text, source, now, user); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO event_story_locale_meta(event_id, locale, last_updated) VALUES (?, ?, ?)
		ON CONFLICT(event_id, locale) DO UPDATE SET last_updated=excluded.last_updated`, eventID, locale, now); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO audit_log(ts, user, action, detail) VALUES (?, ?, 'event.locale.update', ?)`,
		now, user, fmt.Sprintf("locale=%s eventId=%d entryType=%s", locale, eventID, entryType)); err != nil {
		return err
	}
	return tx.Commit()
}
