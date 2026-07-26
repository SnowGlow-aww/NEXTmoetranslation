package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"moesekai/server/internal/model"
)

var (
	ErrLyricsNotFound     = errors.New("lyrics not found")
	ErrLyricsAlreadySaved = errors.New("lyrics already saved")
)

const (
	maxLyricsLines           = 5000
	maxLyricsSegmentsPerLine = 100
	maxLyricsLineTextBytes   = 16 << 10
	maxLyricsMetadataBytes   = 16 << 10
	maxLyricsURLBytes        = 2 << 10
	maxLyricsDocumentBytes   = 4 << 20
)

type LyricsContractError struct {
	Code    string
	Details []string
	Current *model.SongLyrics
}

func (e *LyricsContractError) Error() string { return e.Code }

type storedLyrics struct {
	lyrics     model.SongLyrics
	sourceHash string
}

func (s *Store) GetLyrics(musicID int) (model.SongLyrics, error) {
	return s.getLyricsSnapshot(musicID, nil)
}

// getLyricsSnapshot assembles every part of a lyrics document from one
// read-only SQLite transaction. The optional hook exists for deterministic
// concurrency tests and runs after the document header establishes the
// snapshot but before line and segment reads.
func (s *Store) getLyricsSnapshot(musicID int, afterHeader func()) (model.SongLyrics, error) {
	tx, err := s.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return model.SongLyrics{}, err
	}
	defer tx.Rollback()
	stored, err := s.loadLyricsWithHook(tx, musicID, afterHeader)
	if err != nil {
		return model.SongLyrics{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.SongLyrics{}, err
	}
	return stored.lyrics, nil
}

// WithLyricsFirstSaveEligibility serializes a fast callback with every
// save/publish/unpublish mutation for the same music. It is used to issue a
// first-save import capability without a check-to-issue race.
func (s *Store) WithLyricsFirstSaveEligibility(musicID int, issue func() error) error {
	unlock := s.lockLyrics(musicID)
	defer unlock()
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM song_lyrics WHERE music_id=?`, musicID).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return ErrLyricsAlreadySaved
	}
	return issue()
}

func (s *Store) ListLyrics(limit, cursor int) (model.LyricsListResponse, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT l.music_id, l.revision, l.updated_at, p.revision
		FROM song_lyrics l LEFT JOIN song_lyrics_publications p ON p.music_id=l.music_id
		WHERE l.music_id>? ORDER BY l.music_id LIMIT ?`, cursor, limit+1)
	if err != nil {
		return model.LyricsListResponse{}, err
	}
	defer rows.Close()
	response := model.LyricsListResponse{Items: []model.LyricsListItem{}}
	for rows.Next() {
		var item model.LyricsListItem
		var updatedAt int64
		var published sql.NullInt64
		if err := rows.Scan(&item.MusicID, &item.Revision, &updatedAt, &published); err != nil {
			return model.LyricsListResponse{}, err
		}
		item.Status = "draft"
		if published.Valid {
			item.PublishedRevision = int(published.Int64)
			item.Status = "draft-published"
			if published.Int64 == int64(item.Revision) {
				item.Status = "published"
			}
		}
		item.UpdatedAt = formatTimestamp(updatedAt)
		response.Items = append(response.Items, item)
	}
	if err := rows.Err(); err != nil {
		return model.LyricsListResponse{}, err
	}
	if len(response.Items) > limit {
		response.NextCursor = itoa(response.Items[limit-1].MusicID)
		response.Items = response.Items[:limit]
	}
	return response, nil
}

func (s *Store) SaveLyrics(input model.SongLyrics, user string) (model.SongLyrics, error) {
	lyrics, _, err := s.SaveLyricsMutation(input, user)
	return lyrics, err
}

// SaveLyricsMutation reports whether the successful call committed a change.
func (s *Store) SaveLyricsMutation(input model.SongLyrics, user string) (model.SongLyrics, bool, error) {
	return s.saveLyricsMutation(input, user, false, nil)
}

// SaveImportedLyricsMutation permits source provenance on the first save for
// trusted internal callers and compatibility tests. All provenance-bearing saves
// require the canonical lowercase 40-hex MediaWiki SHA1 representation.
func (s *Store) SaveImportedLyricsMutation(input model.SongLyrics, user string) (model.SongLyrics, bool, error) {
	return s.saveLyricsMutation(input, user, true, nil)
}

// SaveImportedLyricsMutationWithCommit runs afterCommit immediately after the
// first-save transaction commits and before synchronous change notifications.
// The callback must be fast and must not call back into lyrics mutations.
func (s *Store) SaveImportedLyricsMutationWithCommit(input model.SongLyrics, user string, afterCommit func()) (model.SongLyrics, bool, error) {
	return s.saveLyricsMutation(input, user, true, afterCommit)
}

func (s *Store) saveLyricsMutation(input model.SongLyrics, user string, allowNewProvenance bool, afterCommit func()) (model.SongLyrics, bool, error) {
	unlock := s.lockLyrics(input.MusicID)
	defer unlock()
	normalized := input
	sort.SliceStable(normalized.Lines, func(i, j int) bool { return normalized.Lines[i].Order < normalized.Lines[j].Order })
	sourceFetchedAt, err := validateLyricsProvenance(normalized)
	if err != nil {
		return model.SongLyrics{}, false, err
	}
	if sourceFetchedAt > 0 {
		normalized.SourceFetchedAt = formatTimestamp(sourceFetchedAt)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return model.SongLyrics{}, false, err
	}
	defer tx.Rollback()
	exists, err := s.catalogMusicExists(tx, normalized.MusicID)
	if err != nil {
		return model.SongLyrics{}, false, err
	}
	if !exists {
		return model.SongLyrics{}, false, &LyricsContractError{Code: "source_drift", Details: []string{"musicId is not present in the server catalog"}}
	}
	validPerformers, err := s.performerIDs(tx)
	if err != nil {
		return model.SongLyrics{}, false, err
	}
	code, details, sourceHash := validateLyrics(normalized, validPerformers, false)
	if code != "" {
		return model.SongLyrics{}, false, &LyricsContractError{Code: code, Details: details}
	}

	current, loadErr := s.loadLyrics(tx, normalized.MusicID)
	if loadErr != nil && loadErr != ErrLyricsNotFound {
		return model.SongLyrics{}, false, loadErr
	}
	if loadErr == ErrLyricsNotFound {
		if normalized.Revision != 0 {
			return model.SongLyrics{}, false, &LyricsContractError{Code: "revision_conflict"}
		}
		if sourceFetchedAt > 0 && !allowNewProvenance {
			return model.SongLyrics{}, false, &LyricsContractError{
				Code: "source_drift", Details: []string{"new source provenance requires a verified server preview"},
			}
		}
	} else {
		if allowNewProvenance {
			return model.SongLyrics{}, false, &LyricsContractError{
				Code: "source_drift", Details: []string{"verified source previews may only be used for the first save of a new lyrics document"},
			}
		}
		if normalized.Revision != current.lyrics.Revision {
			copy := current.lyrics
			return model.SongLyrics{}, false, &LyricsContractError{Code: "revision_conflict", Current: &copy}
		}
		if lyricsProvenanceChanged(normalized, current.lyrics) {
			return model.SongLyrics{}, false, &LyricsContractError{
				Code: "source_drift", Details: []string{"source page, revision, SHA1, fetched timestamp, and URL are immutable after first save"},
			}
		}
		if lyricsSourceStructureChanged(normalized.Lines, current.lyrics.Lines) || sourceHash != current.sourceHash {
			return model.SongLyrics{}, false, &LyricsContractError{
				Code: "source_drift", Details: []string{"ordered line IDs, numeric order values, or Japanese source text changed"},
			}
		}
		if sameLyricsContent(normalized, current.lyrics) {
			return current.lyrics, false, nil
		}
	}

	nextRevision := 1
	if loadErr == nil {
		nextRevision = current.lyrics.Revision + 1
	}
	now := time.Now().Unix()
	if _, err := tx.Exec(`INSERT INTO song_lyrics
		(music_id, revision, updated_at, updated_by, attribution, source_note, source_url, license_note, source_hash,
		 source_page_id, source_revision_id, source_sha1, source_fetched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(music_id) DO UPDATE SET revision=excluded.revision, updated_at=excluded.updated_at,
		updated_by=excluded.updated_by, attribution=excluded.attribution, source_note=excluded.source_note, source_url=excluded.source_url,
		license_note=excluded.license_note, source_hash=excluded.source_hash,
		 source_page_id=excluded.source_page_id, source_revision_id=excluded.source_revision_id,
		 source_sha1=excluded.source_sha1, source_fetched_at=excluded.source_fetched_at`,
		normalized.MusicID, nextRevision, now, user, normalized.Attribution, normalized.SourceNote, normalized.SourceURL,
		normalized.LicenseNote, sourceHash, normalized.SourcePageID, normalized.SourceRevisionID,
		normalized.SourceSHA1, sourceFetchedAt); err != nil {
		return model.SongLyrics{}, false, err
	}
	if _, err := tx.Exec(`DELETE FROM song_lyric_lines WHERE music_id=?`, normalized.MusicID); err != nil {
		return model.SongLyrics{}, false, err
	}
	for _, line := range normalized.Lines {
		stanzaBreak := 0
		if line.StanzaBreakBefore {
			stanzaBreak = 1
		}
		if _, err := tx.Exec(`INSERT INTO song_lyric_lines
			(music_id, line_id, position, japanese, zh_cn, en_us, stanza_break_before) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			normalized.MusicID, line.ID, line.Order, line.Japanese, line.Chinese, line.English, stanzaBreak); err != nil {
			return model.SongLyrics{}, false, err
		}
		for position, segment := range line.Segments {
			performersJSON, _ := json.Marshal(segment.PerformerIDs)
			if _, err := tx.Exec(`INSERT INTO song_lyric_segments
				(music_id, line_id, position, text, performer_ids_json) VALUES (?, ?, ?, ?, ?)`,
				normalized.MusicID, line.ID, position, segment.Text, string(performersJSON)); err != nil {
				return model.SongLyrics{}, false, err
			}
		}
	}
	if _, err := tx.Exec(`INSERT INTO audit_log(ts, user, action, detail) VALUES (?, ?, 'lyrics.save', ?)`,
		now, user, fmt.Sprintf("musicId=%d revision=%d", normalized.MusicID, nextRevision)); err != nil {
		return model.SongLyrics{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return model.SongLyrics{}, false, err
	}
	normalized.Status = "draft"
	if loadErr == nil && current.lyrics.PublishedRevision > 0 {
		normalized.Status = "draft-published"
		normalized.PublishedRevision = current.lyrics.PublishedRevision
	}
	normalized.Revision = nextRevision
	normalized.UpdatedAt = formatTimestamp(now)
	if afterCommit != nil {
		afterCommit()
	}
	s.NotifyChange()
	return normalized, true, nil
}

func (s *Store) PublishLyrics(musicID, revision int, users ...string) (model.SongLyrics, error) {
	lyrics, _, err := s.PublishLyricsMutation(musicID, revision, users...)
	return lyrics, err
}

// PublishLyricsMutation reports whether the successful call committed a change.
func (s *Store) PublishLyricsMutation(musicID, revision int, users ...string) (model.SongLyrics, bool, error) {
	unlock := s.lockLyrics(musicID)
	defer unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return model.SongLyrics{}, false, err
	}
	defer tx.Rollback()
	current, err := s.loadLyrics(tx, musicID)
	if err != nil {
		return model.SongLyrics{}, false, err
	}
	if revision != current.lyrics.Revision {
		copy := current.lyrics
		return model.SongLyrics{}, false, &LyricsContractError{Code: "revision_conflict", Current: &copy}
	}
	validPerformers, err := s.performerIDs(tx)
	if err != nil {
		return model.SongLyrics{}, false, err
	}
	code, details, _ := validateLyrics(current.lyrics, validPerformers, true)
	if code != "" {
		if code != "invalid_performer" {
			code = "incomplete_publication"
		}
		return model.SongLyrics{}, false, &LyricsContractError{Code: code, Details: details}
	}
	var existingRevision int
	err = tx.QueryRow(`SELECT revision FROM song_lyrics_publications WHERE music_id=?`, musicID).Scan(&existingRevision)
	if err == nil && existingRevision == revision {
		current.lyrics.Status = "published"
		current.lyrics.PublishedRevision = revision
		return current.lyrics, false, nil
	}
	if err != nil && err != sql.ErrNoRows {
		return model.SongLyrics{}, false, err
	}
	public := model.PublicSongLyrics{
		Version: 1, MusicID: musicID, Revision: revision,
		UpdatedAt: current.lyrics.UpdatedAt, Attribution: current.lyrics.Attribution, Lines: publicLyricsLines(current.lyrics.Lines),
	}
	payload, err := json.Marshal(public)
	if err != nil {
		return model.SongLyrics{}, false, err
	}
	updatedAt, err := parseTimestamp(current.lyrics.UpdatedAt)
	if err != nil {
		return model.SongLyrics{}, false, err
	}
	if _, err := tx.Exec(`INSERT INTO song_lyrics_publications(music_id, revision, updated_at, payload_json)
		VALUES (?, ?, ?, ?) ON CONFLICT(music_id) DO UPDATE SET revision=excluded.revision,
		updated_at=excluded.updated_at, payload_json=excluded.payload_json`,
		musicID, revision, updatedAt, string(payload)); err != nil {
		return model.SongLyrics{}, false, err
	}
	if _, err := tx.Exec(`INSERT INTO audit_log(ts, user, action, detail) VALUES (?, ?, 'lyrics.publish', ?)`,
		time.Now().Unix(), optionalActor(users), fmt.Sprintf("musicId=%d revision=%d", musicID, revision)); err != nil {
		return model.SongLyrics{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return model.SongLyrics{}, false, err
	}
	current.lyrics.Status = "published"
	current.lyrics.PublishedRevision = revision
	s.NotifyChange()
	return current.lyrics, true, nil
}

func (s *Store) UnpublishLyrics(musicID, revision int, users ...string) (model.SongLyrics, error) {
	lyrics, _, err := s.UnpublishLyricsMutation(musicID, revision, users...)
	return lyrics, err
}

// UnpublishLyricsMutation reports whether the successful call committed a change.
func (s *Store) UnpublishLyricsMutation(musicID, revision int, users ...string) (model.SongLyrics, bool, error) {
	unlock := s.lockLyrics(musicID)
	defer unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return model.SongLyrics{}, false, err
	}
	defer tx.Rollback()
	current, err := s.loadLyrics(tx, musicID)
	if err != nil {
		return model.SongLyrics{}, false, err
	}
	if revision != current.lyrics.Revision {
		copy := current.lyrics
		return model.SongLyrics{}, false, &LyricsContractError{Code: "revision_conflict", Current: &copy}
	}
	var publishedRevision int
	if err := tx.QueryRow(`SELECT revision FROM song_lyrics_publications WHERE music_id=?`, musicID).Scan(&publishedRevision); err == sql.ErrNoRows {
		current.lyrics.Status = "draft"
		current.lyrics.PublishedRevision = 0
		return current.lyrics, false, nil
	} else if err != nil {
		return model.SongLyrics{}, false, err
	}
	if _, err := tx.Exec(`DELETE FROM song_lyrics_publications WHERE music_id=?`, musicID); err != nil {
		return model.SongLyrics{}, false, err
	}
	if _, err := tx.Exec(`INSERT INTO audit_log(ts, user, action, detail) VALUES (?, ?, 'lyrics.unpublish', ?)`,
		time.Now().Unix(), optionalActor(users), fmt.Sprintf("musicId=%d revision=%d", musicID, revision)); err != nil {
		return model.SongLyrics{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return model.SongLyrics{}, false, err
	}
	current.lyrics.Status = "draft"
	current.lyrics.PublishedRevision = 0
	s.NotifyChange()
	return current.lyrics, true, nil
}

func publicLyricsLines(lines []model.LyricLine) []model.LyricLine {
	public := make([]model.LyricLine, len(lines))
	for index, line := range lines {
		public[index] = line
		public[index].ID = fmt.Sprintf("line-%d", line.Order+1)
		public[index].Segments = append([]model.LyricSegment(nil), line.Segments...)
		for segmentIndex := range public[index].Segments {
			public[index].Segments[segmentIndex].PerformerIDs = append([]int(nil), line.Segments[segmentIndex].PerformerIDs...)
		}
	}
	return public
}

func (s *Store) PublishedLyrics() (model.PublicLyricsIndex, map[int]model.PublicSongLyrics, error) {
	rows, err := s.db.Query(`SELECT p.music_id, p.revision, p.updated_at, p.payload_json,
		m.title_ja, m.title_zh, m.title_en
		FROM song_lyrics_publications p JOIN catalog_music m ON m.music_id=p.music_id
		ORDER BY p.music_id`)
	if err != nil {
		return model.PublicLyricsIndex{}, nil, err
	}
	defer rows.Close()
	index := model.PublicLyricsIndex{Version: 1, Songs: []model.PublicLyricsIndexItem{}}
	details := map[int]model.PublicSongLyrics{}
	for rows.Next() {
		var item model.PublicLyricsIndexItem
		var updatedAt int64
		var payload string
		if err := rows.Scan(&item.MusicID, &item.Revision, &updatedAt, &payload,
			&item.Title.Japanese, &item.Title.Chinese, &item.Title.English); err != nil {
			return model.PublicLyricsIndex{}, nil, err
		}
		item.UpdatedAt = formatTimestamp(updatedAt)
		var detail model.PublicSongLyrics
		if err := json.Unmarshal([]byte(payload), &detail); err != nil {
			return model.PublicLyricsIndex{}, nil, err
		}
		// Canonicalize legacy stored snapshots on read so a pre-upgrade payload
		// can never expose private draft/source line identities publicly.
		detail.Lines = publicLyricsLines(detail.Lines)
		index.Songs = append(index.Songs, item)
		details[item.MusicID] = detail
	}
	return index, details, rows.Err()
}

func (s *Store) loadLyrics(q queryRower, musicID int) (storedLyrics, error) {
	return s.loadLyricsWithHook(q, musicID, nil)
}

func (s *Store) loadLyricsWithHook(q queryRower, musicID int, afterHeader func()) (storedLyrics, error) {
	var result storedLyrics
	var updatedAt int64
	var publishedRevision sql.NullInt64
	var sourceFetchedAt int64
	err := q.QueryRow(`SELECT l.music_id, l.revision, l.updated_at, l.attribution, l.source_note, l.source_url,
		l.license_note, l.source_hash, l.source_page_id, l.source_revision_id, l.source_sha1,
		l.source_fetched_at, p.revision
		FROM song_lyrics l LEFT JOIN song_lyrics_publications p ON p.music_id=l.music_id
		WHERE l.music_id=?`, musicID).Scan(
		&result.lyrics.MusicID, &result.lyrics.Revision, &updatedAt, &result.lyrics.Attribution, &result.lyrics.SourceNote,
		&result.lyrics.SourceURL, &result.lyrics.LicenseNote, &result.sourceHash,
		&result.lyrics.SourcePageID, &result.lyrics.SourceRevisionID, &result.lyrics.SourceSHA1,
		&sourceFetchedAt, &publishedRevision)
	if err == sql.ErrNoRows {
		return result, ErrLyricsNotFound
	}
	if err != nil {
		return result, err
	}
	result.lyrics.Status = "draft"
	if publishedRevision.Valid {
		result.lyrics.PublishedRevision = int(publishedRevision.Int64)
		result.lyrics.Status = "draft-published"
		if publishedRevision.Int64 == int64(result.lyrics.Revision) {
			result.lyrics.Status = "published"
		}
	}
	result.lyrics.UpdatedAt = formatTimestamp(updatedAt)
	if sourceFetchedAt > 0 {
		result.lyrics.SourceFetchedAt = formatTimestamp(sourceFetchedAt)
	}
	if afterHeader != nil {
		afterHeader()
	}
	result.lyrics.Lines = []model.LyricLine{}
	lineRows, err := q.Query(`SELECT line_id, position, japanese, zh_cn, en_us, stanza_break_before
		FROM song_lyric_lines WHERE music_id=? ORDER BY position`, musicID)
	if err != nil {
		return storedLyrics{}, err
	}
	for lineRows.Next() {
		var line model.LyricLine
		var stanzaBreak int
		if err := lineRows.Scan(&line.ID, &line.Order, &line.Japanese, &line.Chinese, &line.English, &stanzaBreak); err != nil {
			lineRows.Close()
			return storedLyrics{}, err
		}
		line.StanzaBreakBefore = stanzaBreak == 1
		line.Segments = []model.LyricSegment{}
		result.lyrics.Lines = append(result.lyrics.Lines, line)
	}
	if err := lineRows.Close(); err != nil {
		return storedLyrics{}, err
	}
	lineIndex := map[string]int{}
	for index, line := range result.lyrics.Lines {
		lineIndex[line.ID] = index
	}
	segmentRows, err := q.Query(`SELECT line_id, text, performer_ids_json FROM song_lyric_segments
		WHERE music_id=? ORDER BY line_id, position`, musicID)
	if err != nil {
		return storedLyrics{}, err
	}
	defer segmentRows.Close()
	for segmentRows.Next() {
		var lineID, performerJSON string
		var segment model.LyricSegment
		if err := segmentRows.Scan(&lineID, &segment.Text, &performerJSON); err != nil {
			return storedLyrics{}, err
		}
		if err := json.Unmarshal([]byte(performerJSON), &segment.PerformerIDs); err != nil {
			return storedLyrics{}, fmt.Errorf("lyrics segment performers for musicId=%d lineId=%q: %w", musicID, lineID, err)
		}
		if segment.PerformerIDs == nil {
			segment.PerformerIDs = []int{}
		}
		if index, ok := lineIndex[lineID]; ok {
			result.lyrics.Lines[index].Segments = append(result.lyrics.Lines[index].Segments, segment)
		}
	}
	return result, segmentRows.Err()
}

func validateLyrics(lyrics model.SongLyrics, performers map[int]bool, publishing bool) (string, []string, string) {
	if lyrics.MusicID <= 0 {
		return "source_drift", []string{"musicId must be positive"}, ""
	}
	if len(lyrics.Lines) == 0 || len(lyrics.Lines) > maxLyricsLines {
		return "segment_mismatch", []string{"lines must contain between 1 and 5000 items"}, ""
	}
	if len(lyrics.Attribution) > maxLyricsMetadataBytes || len(lyrics.SourceNote) > maxLyricsMetadataBytes ||
		len(lyrics.LicenseNote) > maxLyricsMetadataBytes || len(lyrics.SourceURL) > maxLyricsURLBytes ||
		len(lyrics.SourceSHA1) > 256 {
		return "segment_mismatch", []string{"lyrics metadata exceeds safe size limits"}, ""
	}
	totalBytes := len(lyrics.Attribution) + len(lyrics.SourceNote) + len(lyrics.LicenseNote) + len(lyrics.SourceURL) + len(lyrics.SourceSHA1)
	var segmentDetails, performerDetails, publicationDetails []string
	if publishing && strings.TrimSpace(lyrics.Attribution) == "" {
		publicationDetails = append(publicationDetails, "attribution is required for publication")
	}
	lineIDs := map[string]bool{}
	orders := map[int]bool{}
	for lineIndex, line := range lyrics.Lines {
		path := fmt.Sprintf("lines[%d]", lineIndex)
		if strings.TrimSpace(line.ID) == "" || len(line.ID) > 128 || lineIDs[line.ID] {
			segmentDetails = append(segmentDetails, path+".id must be unique and 1-128 characters")
		}
		lineIDs[line.ID] = true
		if line.Order < 0 || orders[line.Order] {
			segmentDetails = append(segmentDetails, path+".order must be unique and non-negative")
		}
		orders[line.Order] = true
		if len(line.Segments) == 0 || len(line.Segments) > maxLyricsSegmentsPerLine {
			segmentDetails = append(segmentDetails, path+".segments must contain between 1 and 100 items")
		}
		if strings.TrimSpace(line.Japanese) == "" {
			segmentDetails = append(segmentDetails, path+".japanese must not be empty")
		}
		if len(line.Japanese) > maxLyricsLineTextBytes || len(line.Chinese) > maxLyricsLineTextBytes || len(line.English) > maxLyricsLineTextBytes {
			segmentDetails = append(segmentDetails, path+" text exceeds the safe per-line size limit")
		}
		totalBytes += len(line.ID) + len(line.Japanese) + len(line.Chinese) + len(line.English)
		var concatenated strings.Builder
		for segmentIndex, segment := range line.Segments {
			if len(segment.Text) > maxLyricsLineTextBytes {
				segmentDetails = append(segmentDetails, fmt.Sprintf("%s.segments[%d].text exceeds the safe size limit", path, segmentIndex))
			}
			totalBytes += len(segment.Text) + len(segment.PerformerIDs)*8
			concatenated.WriteString(segment.Text)
			seenPerformers := map[int]bool{}
			if publishing && len(segment.PerformerIDs) == 0 {
				publicationDetails = append(publicationDetails,
					fmt.Sprintf("%s.segments[%d] requires at least one performerId", path, segmentIndex))
			}
			for _, performerID := range segment.PerformerIDs {
				if seenPerformers[performerID] {
					performerDetails = append(performerDetails,
						fmt.Sprintf("%s.segments[%d] has duplicate performerId %d", path, segmentIndex, performerID))
					continue
				}
				seenPerformers[performerID] = true
				if !performers[performerID] {
					performerDetails = append(performerDetails,
						fmt.Sprintf("%s.segments[%d] has invalid performerId %d", path, segmentIndex, performerID))
				}
			}
		}
		if concatenated.String() != line.Japanese {
			segmentDetails = append(segmentDetails, path+".japanese must equal concatenated segment text")
		}
		if publishing && (strings.TrimSpace(line.Japanese) == "" || strings.TrimSpace(line.Chinese) == "" || strings.TrimSpace(line.English) == "") {
			publicationDetails = append(publicationDetails, path+" requires japanese, zh-CN, and en-US")
		}
	}
	if totalBytes > maxLyricsDocumentBytes {
		segmentDetails = append(segmentDetails, "lyrics document exceeds the safe total size limit")
	}
	if len(segmentDetails) > 0 {
		return "segment_mismatch", segmentDetails, ""
	}
	if len(performerDetails) > 0 {
		return "invalid_performer", performerDetails, ""
	}
	if len(publicationDetails) > 0 {
		return "incomplete_publication", publicationDetails, ""
	}
	return "", nil, lyricsSourceHash(lyrics.Lines)
}

func lyricsProvenanceChanged(left, right model.SongLyrics) bool {
	return left.SourcePageID != right.SourcePageID || left.SourceRevisionID != right.SourceRevisionID ||
		left.SourceSHA1 != right.SourceSHA1 || left.SourceFetchedAt != right.SourceFetchedAt ||
		left.SourceURL != right.SourceURL
}

func lyricsSourceStructureChanged(left, right []model.LyricLine) bool {
	if len(left) != len(right) {
		return true
	}
	for index := range left {
		if left[index].ID != right[index].ID || left[index].Order != right[index].Order || left[index].Japanese != right[index].Japanese {
			return true
		}
	}
	return false
}

func validateLyricsProvenance(lyrics model.SongLyrics) (int64, error) {
	provenanceSet := lyrics.SourcePageID != 0 || lyrics.SourceRevisionID != 0 ||
		strings.TrimSpace(lyrics.SourceSHA1) != "" || strings.TrimSpace(lyrics.SourceFetchedAt) != ""
	if !provenanceSet {
		if sourceURL := strings.TrimSpace(lyrics.SourceURL); sourceURL != "" {
			if _, err := parseLyricsSourceURL(sourceURL); err != nil {
				return 0, err
			}
		}
		return 0, nil
	}
	if lyrics.SourcePageID <= 0 || lyrics.SourceRevisionID <= 0 ||
		strings.TrimSpace(lyrics.SourceSHA1) == "" || strings.TrimSpace(lyrics.SourceFetchedAt) == "" ||
		strings.TrimSpace(lyrics.SourceURL) == "" {
		return 0, &LyricsContractError{Code: "source_drift", Details: []string{
			"sourcePageId, sourceRevisionId, sourceSha1, sourceFetchedAt, and sourceUrl must be supplied together",
		}}
	}
	if !hasCanonicalLyricsSourceSHA1(lyrics.SourceSHA1) {
		return 0, &LyricsContractError{Code: "source_drift", Details: []string{
			"sourceSha1 must be exactly 40 lowercase hexadecimal characters",
		}}
	}
	if _, err := parseLyricsSourceURL(lyrics.SourceURL); err != nil {
		return 0, err
	}
	fetchedAt, err := parseTimestamp(lyrics.SourceFetchedAt)
	if err != nil {
		return 0, &LyricsContractError{Code: "source_drift", Details: []string{"sourceFetchedAt must be an RFC3339 timestamp"}}
	}
	if fetchedAt <= 0 {
		return 0, &LyricsContractError{Code: "source_drift", Details: []string{"sourceFetchedAt must be after 1970-01-01T00:00:00Z"}}
	}
	return fetchedAt, nil
}

func hasCanonicalLyricsSourceSHA1(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func parseLyricsSourceURL(value string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil {
		return nil, &LyricsContractError{Code: "source_drift", Details: []string{
			"sourceUrl must be an absolute http(s) URL without credentials",
		}}
	}
	return parsed, nil
}

func lyricsSourceHash(lines []model.LyricLine) string {
	ordered := append([]model.LyricLine(nil), lines...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Order < ordered[j].Order })
	hash := sha256.New()
	for _, line := range ordered {
		hash.Write([]byte(line.ID))
		hash.Write([]byte{0})
		hash.Write([]byte(line.Japanese))
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func lyricsMutexStripe(musicID int) int {
	return int(uint(musicID) & uint(lyricsMutexStripeCount-1))
}

func (s *Store) lockLyrics(musicID int) func() {
	mutex := &s.lyricsMutexes[lyricsMutexStripe(musicID)]
	mutex.Lock()
	return mutex.Unlock
}

func sameLyricsContent(left, right model.SongLyrics) bool {
	canonicalize := func(lyrics model.SongLyrics) model.SongLyrics {
		lyrics.Lines = append([]model.LyricLine(nil), lyrics.Lines...)
		for lineIndex := range lyrics.Lines {
			lyrics.Lines[lineIndex].Segments = append([]model.LyricSegment(nil), lyrics.Lines[lineIndex].Segments...)
			for segmentIndex := range lyrics.Lines[lineIndex].Segments {
				if len(lyrics.Lines[lineIndex].Segments[segmentIndex].PerformerIDs) == 0 {
					lyrics.Lines[lineIndex].Segments[segmentIndex].PerformerIDs = nil
				}
			}
		}
		return lyrics
	}
	left = canonicalize(left)
	right = canonicalize(right)
	left.Status, left.Revision, left.PublishedRevision, left.UpdatedAt = "", 0, 0, ""
	right.Status, right.Revision, right.PublishedRevision, right.UpdatedAt = "", 0, 0, ""
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return string(leftJSON) == string(rightJSON)
}

func formatTimestamp(unix int64) string {
	return time.Unix(unix, 0).UTC().Format(time.RFC3339)
}

func parseTimestamp(value string) (int64, error) {
	timestamp, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return 0, err
	}
	return timestamp.Unix(), nil
}
