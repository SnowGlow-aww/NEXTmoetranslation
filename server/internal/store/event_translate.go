package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"moesekai/server/internal/model"
)

// EventTranslateTarget is an untranslated title or talk line needing LLM work.
type EventTranslateTarget struct {
	EpisodeNo      string
	EntryType      string // "title" | "talk"
	JP             string // for talk: the jp key; for title: the jp title text
	SegmentIDs     []string
	SourceHash     string
	OriginalSource string
}

// UntranslatedTargets returns titles/talk lines of an event story whose cn text
// is empty (i.e. JP-pending lines awaiting translation), in stored order.
func (s *EventStore) UntranslatedTargets(eventID int) ([]EventTranslateTarget, error) {
	var targets []EventTranslateTarget
	canonicalIDs, err := currentEventCanonicalSegmentIDs(s.db, eventID)
	if err != nil {
		return nil, err
	}
	titleSegments := map[string]EventSegmentRecord{}
	talkSegments := map[string][]EventSegmentRecord{}
	segmentRows, err := s.db.Query(`SELECT segment.segment_id, segment.event_id, segment.episode_no,
		segment.scenario_id, segment.kind, segment.position, segment.jp_key, segment.source_text, segment.source_hash
		FROM event_story_segments segment
		JOIN event_story_episodes episode
		ON episode.event_id=segment.event_id AND episode.episode_no=segment.episode_no
		AND episode.scenario_id=segment.scenario_id
		WHERE segment.event_id=? ORDER BY episode.position, segment.position, segment.segment_id`, eventID)
	if err != nil {
		return nil, err
	}
	for segmentRows.Next() {
		var segment EventSegmentRecord
		if err := segmentRows.Scan(&segment.SegmentID, &segment.EventID, &segment.EpisodeNo, &segment.ScenarioID,
			&segment.Kind, &segment.Position, &segment.JPKey, &segment.SourceText, &segment.SourceHash); err != nil {
			segmentRows.Close()
			return nil, err
		}
		active := canonicalIDs[segment.EpisodeNo] != nil && canonicalIDs[segment.EpisodeNo][segment.SegmentID]
		if canonicalIDs[segment.EpisodeNo] == nil {
			active = isDeterministicEventSegment(segment)
		}
		if !active || (segment.Kind != "title" && segment.SourceText != segment.JPKey) ||
			segment.SourceHash != hashText(segment.SourceText) {
			continue
		}
		if segment.Kind == "title" {
			titleSegments[segment.EpisodeNo] = segment
		} else if segment.Kind == "talk" && segment.SourceText != "" {
			key := segment.EpisodeNo + "\x00" + segment.JPKey
			talkSegments[key] = append(talkSegments[key], segment)
		}
	}
	if err := segmentRows.Err(); err != nil {
		segmentRows.Close()
		return nil, err
	}
	if err := segmentRows.Close(); err != nil {
		return nil, err
	}

	// Titles with empty text are not translatable (we only have the cn title
	// slot); JP-pending titles store the JP text in `title` with empty source.
	// We translate a title when its source is unknown/empty and text non-empty.
	epRows, err := s.db.Query(
		`SELECT episode_no, title, title_source FROM event_story_episodes
		 WHERE event_id=? ORDER BY position`, eventID)
	if err != nil {
		return nil, err
	}
	defer epRows.Close()
	type titleRow struct{ no, title, src string }
	var titles []titleRow
	for epRows.Next() {
		var tr titleRow
		if err := epRows.Scan(&tr.no, &tr.title, &tr.src); err != nil {
			return nil, err
		}
		titles = append(titles, tr)
	}
	if err := epRows.Err(); err != nil {
		return nil, err
	}
	for _, tr := range titles {
		segment, ok := titleSegments[tr.no]
		if tr.title != "" && ok && segment.SourceText == tr.title && segment.SourceHash == hashText(tr.title) &&
			(tr.src == "" || tr.src == "unknown" || tr.src == "jp_pending") {
			targets = append(targets, EventTranslateTarget{
				EpisodeNo: tr.no, EntryType: "title", JP: tr.title, SegmentIDs: []string{segment.SegmentID},
				SourceHash: segment.SourceHash, OriginalSource: tr.src,
			})
		}
	}

	lineRows, err := s.db.Query(
		`SELECT episode_no, jp_key, source FROM event_story_lines
		 WHERE event_id=? AND cn_text='' AND source NOT IN ('human','pinned') ORDER BY episode_no, position`, eventID)
	if err != nil {
		return nil, err
	}
	defer lineRows.Close()
	for lineRows.Next() {
		var no, jp, source string
		if err := lineRows.Scan(&no, &jp, &source); err != nil {
			return nil, err
		}
		segments := talkSegments[no+"\x00"+jp]
		if len(segments) == 0 {
			continue
		}
		segmentIDs := make([]string, 0, len(segments))
		for _, segment := range segments {
			segmentIDs = append(segmentIDs, segment.SegmentID)
		}
		targets = append(targets, EventTranslateTarget{
			EpisodeNo: no, EntryType: "talk", JP: jp, SegmentIDs: segmentIDs, SourceHash: hashText(jp),
			OriginalSource: source,
		})
	}
	return targets, lineRows.Err()
}

func isDeterministicEventSegment(segment EventSegmentRecord) bool {
	if segment.Kind == "title" {
		return segment.SegmentID == eventSegmentID(segment.EventID, segment.ScenarioID, segment.EpisodeNo, "title", -1)
	}
	for _, field := range []string{"", "body", "speaker", "legacy"} {
		if segment.SegmentID == eventSegmentID(segment.EventID, segment.ScenarioID, segment.EpisodeNo, "talk", segment.Position, field) {
			return true
		}
	}
	return false
}

// ApplyEventTranslations writes LLM results for the given targets. For titles,
// the cn text replaces the title and title_source becomes the source; for talk
// lines, cn_text/source are set. Returns the number of rows changed.
func (s *EventStore) ApplyEventTranslations(eventID int, targets []EventTranslateTarget, cnByIndex []string, source string) (int, error) {
	changed, _, err := s.applyEventTranslations(eventID, targets, cnByIndex, source, false)
	return changed, err
}

// ApplyEventTranslationsForSync skips the batch if a human or pinned edit was
// made while the automatic translation request was in flight.
func (s *EventStore) ApplyEventTranslationsForSync(eventID int, targets []EventTranslateTarget, cnByIndex []string, source string) (int, bool, error) {
	return s.applyEventTranslations(eventID, targets, cnByIndex, source, true)
}

func (s *EventStore) applyEventTranslations(eventID int, targets []EventTranslateTarget, cnByIndex []string, source string, protectLocal bool) (int, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()
	if protectLocal {
		protected, err := eventHasProtectedTranslationsTx(context.Background(), tx, eventID)
		if err != nil {
			return 0, false, err
		}
		if protected {
			return 0, true, nil
		}
	}
	changed := 0
	now := time.Now().Unix()
	for i, tgt := range targets {
		if i >= len(cnByIndex) {
			break
		}
		cn := cnByIndex[i]
		if cn == "" {
			continue
		}
		if err := validateEventTranslationTargetTx(tx, eventID, tgt); err != nil {
			if err == sql.ErrNoRows || err == ErrEventSourceConflict {
				continue
			}
			return changed, false, err
		}
		var result sql.Result
		if tgt.EntryType == "title" {
			result, err = tx.Exec(
				`UPDATE event_story_episodes SET title=?, title_source=?
				 WHERE event_id=? AND episode_no=? AND title=? AND title_source=?`,
				cn, source, eventID, tgt.EpisodeNo, tgt.JP, tgt.OriginalSource)
			if err != nil {
				return changed, false, err
			}
		} else {
			result, err = tx.Exec(
				`UPDATE event_story_lines SET cn_text=?, source=?
				 WHERE event_id=? AND episode_no=? AND jp_key=? AND cn_text='' AND source=?`,
				cn, source, eventID, tgt.EpisodeNo, tgt.JP, tgt.OriginalSource)
			if err != nil {
				return changed, false, err
			}
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return changed, false, err
		}
		if affected != 1 {
			continue
		}
		if err := upsertEventTranslationLocale(tx, eventID, tgt, cn, source, now); err != nil {
			return changed, false, err
		}
		changed++
	}
	if changed == 0 {
		return 0, false, nil
	}
	if _, err := tx.Exec(`UPDATE event_stories SET last_updated=? WHERE event_id=?`, now, eventID); err != nil {
		return changed, false, err
	}
	if _, err := tx.Exec(`INSERT INTO event_story_locale_meta(event_id, locale, last_updated) VALUES (?, ?, ?)
		ON CONFLICT(event_id, locale) DO UPDATE SET last_updated=excluded.last_updated`, eventID, model.LocaleChinese, now); err != nil {
		return changed, false, err
	}
	if err := tx.Commit(); err != nil {
		return changed, false, err
	}
	return changed, false, nil
}

func upsertEventTranslationLocale(tx *sql.Tx, eventID int, target EventTranslateTarget, text, source string, now int64) error {
	for _, segmentID := range target.SegmentIDs {
		result, err := tx.Exec(`INSERT INTO event_story_segment_localizations
			(segment_id, locale, text, source, updated_at, updated_by, revision)
			SELECT segment.segment_id, ?, ?, ?, ?, 'ai', 1 FROM event_story_segments segment
			JOIN event_story_episodes episode
			ON episode.event_id=segment.event_id AND episode.episode_no=segment.episode_no
			AND episode.scenario_id=segment.scenario_id
			WHERE segment.segment_id=? AND segment.event_id=? AND segment.episode_no=?
			AND segment.source_text=? AND segment.source_hash=?
			ON CONFLICT(segment_id, locale) DO UPDATE SET text=excluded.text, source=excluded.source,
			updated_at=excluded.updated_at, updated_by=excluded.updated_by,
			revision=event_story_segment_localizations.revision+1`,
			model.LocaleChinese, text, source, now, segmentID, eventID, target.EpisodeNo, target.JP, target.SourceHash)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return fmt.Errorf("event %d %s target segment %s disappeared", eventID, target.EpisodeNo, segmentID)
		}
	}
	return nil
}

func validateEventTranslationTargetTx(tx *sql.Tx, eventID int, target EventTranslateTarget) error {
	if len(target.SegmentIDs) == 0 || target.SourceHash != hashText(target.JP) {
		return ErrEventSourceConflict
	}
	wantKind := "talk"
	if target.EntryType == "title" {
		wantKind = "title"
		if len(target.SegmentIDs) != 1 {
			return ErrEventSourceConflict
		}
	}
	for _, segmentID := range target.SegmentIDs {
		var segment EventSegmentRecord
		var parentScenario string
		if err := tx.QueryRow(`SELECT segment.segment_id, segment.event_id, segment.episode_no,
			segment.scenario_id, segment.kind, segment.position, segment.jp_key, segment.source_text,
			segment.source_hash, episode.scenario_id
			FROM event_story_segments segment
			JOIN event_story_episodes episode
			ON episode.event_id=segment.event_id AND episode.episode_no=segment.episode_no
			WHERE segment.segment_id=? AND segment.event_id=?`, segmentID, eventID).Scan(&segment.SegmentID,
			&segment.EventID, &segment.EpisodeNo, &segment.ScenarioID, &segment.Kind, &segment.Position,
			&segment.JPKey, &segment.SourceText, &segment.SourceHash, &parentScenario); err != nil {
			return err
		}
		if segment.EpisodeNo != target.EpisodeNo || segment.ScenarioID != parentScenario ||
			segment.Kind != wantKind || segment.SourceText != target.JP || segment.SourceHash != target.SourceHash ||
			!isDeterministicEventSegment(segment) || (wantKind == "talk" && segment.JPKey != target.JP) ||
			(wantKind == "title" && (segment.Position != -1 || segment.JPKey != "")) {
			return ErrEventSourceConflict
		}
	}
	return nil
}

// SetStorySource updates an event story's story-level source.
func (s *EventStore) SetStorySource(eventID int, source string) error {
	_, err := s.db.Exec(`UPDATE event_stories SET source=?, last_updated=? WHERE event_id=?`,
		source, time.Now().Unix(), eventID)
	return err
}

// SetStorySourceForSync rechecks protected edits in the same write transaction
// used to finalize an automatic sync translation.
func (s *EventStore) SetStorySourceForSync(eventID int, source string) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	protected, err := eventHasProtectedTranslationsTx(context.Background(), tx, eventID)
	if err != nil {
		return false, err
	}
	if protected {
		return false, nil
	}
	if _, err := tx.Exec(`UPDATE event_stories SET source=?, last_updated=? WHERE event_id=?`,
		source, time.Now().Unix(), eventID); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// ReorderEpisodeLines updates the stored positions of an episode's talk lines to
// match orderedKeys. Keys not present in orderedKeys keep their relative order
// after the listed ones. Existing translations are untouched.
func (s *EventStore) ReorderEpisodeLines(eventID int, episodeNo string, orderedKeys []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	pos := 0
	seen := map[string]bool{}
	for _, jp := range orderedKeys {
		res, err := tx.Exec(
			`UPDATE event_story_lines SET position=? WHERE event_id=? AND episode_no=? AND jp_key=?`,
			pos, eventID, episodeNo, jp)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			seen[jp] = true
			pos++
		}
	}
	// Append any lines not in orderedKeys, preserving their current order.
	rows, err := tx.Query(
		`SELECT jp_key FROM event_story_lines WHERE event_id=? AND episode_no=? ORDER BY position`,
		eventID, episodeNo)
	if err != nil {
		return err
	}
	var rest []string
	for rows.Next() {
		var jp string
		if err := rows.Scan(&jp); err != nil {
			rows.Close()
			return err
		}
		if !seen[jp] {
			rest = append(rest, jp)
		}
	}
	rows.Close()
	for _, jp := range rest {
		if _, err := tx.Exec(
			`UPDATE event_story_lines SET position=? WHERE event_id=? AND episode_no=? AND jp_key=?`,
			pos, eventID, episodeNo, jp); err != nil {
			return err
		}
		pos++
	}
	return tx.Commit()
}

// EpisodeTalkKeys returns an episode's talk jp keys (for reorder matching).
func (s *EventStore) EpisodeTalkKeys(eventID int, episodeNo string) (map[string]bool, error) {
	rows, err := s.db.Query(
		`SELECT jp_key FROM event_story_lines WHERE event_id=? AND episode_no=?`,
		eventID, episodeNo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var jp string
		if err := rows.Scan(&jp); err != nil {
			return nil, err
		}
		out[jp] = true
	}
	return out, rows.Err()
}

var _ = sql.ErrNoRows
