package store

import (
	"database/sql"
	"strconv"
	"strings"
	"time"

	"moesekai/server/internal/model"
)

type MusicCatalogRecord struct {
	MusicID             int
	JapaneseTitle       string
	ChineseTitle        string
	EnglishTitle        string
	JacketURL           string
	IsNewlyWrittenMusic bool
	ProducerMetadata    string
}

type PerformerCatalogRecord struct {
	PerformerID  int
	JapaneseName string
	ChineseName  string
	EnglishName  string
}

func (s *Store) UpsertMusicCatalog(records []MusicCatalogRecord) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	for _, record := range records {
		if record.MusicID <= 0 || strings.TrimSpace(record.JapaneseTitle) == "" {
			continue
		}
		newlyWritten := 0
		if record.IsNewlyWrittenMusic {
			newlyWritten = 1
		}
		if _, err := tx.Exec(`INSERT INTO catalog_music
			(music_id, title_ja, title_zh, title_en, jacket_url, newly_written, updated_at, producer_metadata)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(music_id) DO UPDATE SET title_ja=excluded.title_ja,
			title_zh=CASE WHEN excluded.title_zh<>'' THEN excluded.title_zh ELSE catalog_music.title_zh END,
			title_en=CASE WHEN excluded.title_en<>'' THEN excluded.title_en ELSE catalog_music.title_en END,
			jacket_url=CASE WHEN excluded.jacket_url<>'' THEN excluded.jacket_url ELSE catalog_music.jacket_url END,
			newly_written=excluded.newly_written, updated_at=excluded.updated_at,
			producer_metadata=CASE WHEN excluded.producer_metadata<>'' THEN excluded.producer_metadata ELSE catalog_music.producer_metadata END`,
			record.MusicID, record.JapaneseTitle, record.ChineseTitle, record.EnglishTitle,
			record.JacketURL, newlyWritten, now, record.ProducerMetadata); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) UpsertPerformerCatalog(records []PerformerCatalogRecord) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	for _, record := range records {
		if record.PerformerID <= 0 || strings.TrimSpace(record.JapaneseName) == "" {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO catalog_performers
			(performer_id, name_ja, name_zh, name_en, updated_at) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(performer_id) DO UPDATE SET name_ja=excluded.name_ja,
			name_zh=CASE WHEN excluded.name_zh<>'' THEN excluded.name_zh ELSE catalog_performers.name_zh END,
			name_en=CASE WHEN excluded.name_en<>'' THEN excluded.name_en ELSE catalog_performers.name_en END,
			updated_at=excluded.updated_at`,
			record.PerformerID, record.JapaneseName, record.ChineseName, record.EnglishName, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) CatalogMusic(query string, newlyWrittenOnly bool, limit, cursor int) (model.CatalogMusicResponse, error) {
	if limit <= 0 {
		limit = 50
	}
	sqlQuery := `SELECT m.music_id, m.title_ja,
		COALESCE(NULLIF(zh.cn_text, ''), m.title_zh), COALESCE(NULLIF(en.text, ''), m.title_en), m.jacket_url,
		m.newly_written, l.revision, p.revision
		FROM catalog_music m
		LEFT JOIN entries zh ON zh.category='music' AND zh.field='title' AND zh.jp_key=m.title_ja
		LEFT JOIN entry_localizations en ON en.category='music' AND en.field='title'
		 AND en.jp_key=m.title_ja AND en.locale='en-US'
		LEFT JOIN song_lyrics l ON l.music_id=m.music_id
		LEFT JOIN song_lyrics_publications p ON p.music_id=m.music_id
		WHERE m.music_id>?`
	args := []any{cursor}
	if newlyWrittenOnly {
		sqlQuery += ` AND m.newly_written=1`
	}
	query = strings.TrimSpace(query)
	if query != "" {
		sqlQuery += ` AND (m.title_ja LIKE ? OR m.title_zh LIKE ? OR m.title_en LIKE ? OR CAST(m.music_id AS TEXT)=?)`
		like := "%" + query + "%"
		args = append(args, like, like, like, query)
	}
	sqlQuery += ` ORDER BY m.music_id LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.Query(sqlQuery, args...)
	if err != nil {
		return model.CatalogMusicResponse{}, err
	}
	defer rows.Close()
	response := model.CatalogMusicResponse{Items: []model.CatalogMusicItem{}}
	for rows.Next() {
		var item model.CatalogMusicItem
		var newlyWritten int
		var revision, publishedRevision sql.NullInt64
		if err := rows.Scan(&item.MusicID, &item.Title.Japanese, &item.Title.Chinese, &item.Title.English,
			&item.JacketURL, &newlyWritten, &revision, &publishedRevision); err != nil {
			return model.CatalogMusicResponse{}, err
		}
		item.IsNewlyWrittenMusic = newlyWritten == 1
		if revision.Valid {
			item.LyricsStatus = "draft"
			if publishedRevision.Valid && publishedRevision.Int64 == revision.Int64 {
				item.LyricsStatus = "published"
			}
		}
		response.Items = append(response.Items, item)
	}
	if err := rows.Err(); err != nil {
		return model.CatalogMusicResponse{}, err
	}
	if len(response.Items) > limit {
		response.NextCursor = itoa(response.Items[limit-1].MusicID)
		response.Items = response.Items[:limit]
	}
	return response, nil
}

func (s *Store) CatalogPerformers() (model.CatalogPerformerResponse, error) {
	rows, err := s.db.Query(`SELECT performer_id, name_ja, name_zh, name_en FROM catalog_performers ORDER BY performer_id`)
	if err != nil {
		return model.CatalogPerformerResponse{}, err
	}
	defer rows.Close()
	response := model.CatalogPerformerResponse{Items: []model.CatalogPerformerItem{}}
	for rows.Next() {
		var item model.CatalogPerformerItem
		if err := rows.Scan(&item.PerformerID, &item.Name.Japanese, &item.Name.Chinese, &item.Name.English); err != nil {
			return model.CatalogPerformerResponse{}, err
		}
		response.Items = append(response.Items, item)
	}
	return response, rows.Err()
}

type CatalogMusicIdentity struct {
	MusicID          int
	JapaneseTitle    string
	ProducerMetadata string
}

func (s *Store) CatalogMusicIdentity(musicID int) (CatalogMusicIdentity, error) {
	var identity CatalogMusicIdentity
	err := s.db.QueryRow(`SELECT music_id, title_ja, producer_metadata FROM catalog_music WHERE music_id=?`, musicID).
		Scan(&identity.MusicID, &identity.JapaneseTitle, &identity.ProducerMetadata)
	return identity, err
}

func (s *Store) catalogMusicExists(q queryRower, musicID int) (bool, error) {
	var count int
	err := q.QueryRow(`SELECT COUNT(*) FROM catalog_music WHERE music_id=?`, musicID).Scan(&count)
	return count == 1, err
}

func (s *Store) performerIDs(q queryRower) (map[int]bool, error) {
	rows, err := q.Query(`SELECT performer_id FROM catalog_performers`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[int]bool{}
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result[id] = true
	}
	return result, rows.Err()
}

type queryRower interface {
	QueryRow(string, ...any) *sql.Row
	Query(string, ...any) (*sql.Rows, error)
}

func itoa(value int) string {
	return strconv.Itoa(value)
}
