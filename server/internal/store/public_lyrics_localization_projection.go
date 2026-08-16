package store

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"strings"

	"moesekai/server/internal/model"
)

// PublishedLyricsLocalizationProjection returns the public index entries and
// validated v3 detail documents for source-v3 songs whose rendition
// localizations were edited after the recovery batch (revision > 1) and carry
// a translation credit. Songs with a legacy publication row are excluded:
// the legacy publication snapshot owns them. Individual songs that cannot be
// materialized or validated are skipped and logged; the caller keeps serving
// the reviewed bundle for those songs. A systemic query failure is returned so
// the caller can fail closed to the bundle.
func (s *Store) PublishedLyricsLocalizationProjection() ([]PublicLyricsIndexSong, map[int]PublicLyricsV3DetailDocument, error) {
	tx, err := s.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT d.music_id, m.title_ja,
		COALESCE(NULLIF(zh.cn_text,''), m.title_zh), COALESCE(NULLIF(en.text,''), m.title_en)
		FROM song_lyrics_source_documents d
		JOIN catalog_music m ON m.music_id=d.music_id
		LEFT JOIN entries zh ON zh.category='music' AND zh.field='title' AND zh.jp_key=m.title_ja
		LEFT JOIN entry_localizations en ON en.category='music' AND en.field='title'
		 AND en.jp_key=m.title_ja AND en.locale='en-US'
		LEFT JOIN song_lyrics_publications p ON p.music_id=d.music_id
		WHERE p.music_id IS NULL
		ORDER BY d.music_id`)
	if err != nil {
		return nil, nil, err
	}
	index := []PublicLyricsIndexSong{}
	details := make(map[int]PublicLyricsV3DetailDocument)
	for rows.Next() {
		var musicID int
		var title model.LocalizedTitle
		if err := rows.Scan(&musicID, &title.Japanese, &title.Chinese, &title.English); err != nil {
			rows.Close()
			return nil, nil, err
		}
		document, err := s.GetLyricsRenditionDocument(musicID)
		if err != nil {
			if !errors.Is(err, ErrLyricsNotFound) {
				log.Printf("[projection] lyrics localization %d unavailable; skipping: %v", musicID, err)
			}
			continue
		}
		if document.Revision <= 1 {
			// The recovery batch materializes every source-v3 document at
			// revision 1; only later editor saves are localization updates.
			continue
		}
		if !lyricsRenditionHasLocalizationCredit(document) {
			continue
		}
		state := PublicLyricsStateGameOnly
		for _, rendition := range document.Renditions {
			if rendition.Full != nil {
				state = PublicLyricsStateComplete
				break
			}
		}
		detail := PublicLyricsV3DetailDocument{
			Version: 3, MusicID: musicID, Revision: document.Revision,
			UpdatedAt: document.UpdatedAt, State: state,
			Renditions: document.Renditions,
		}
		if err := validatePublicLyricsV3Detail(detail); err != nil {
			log.Printf("[projection] lyrics localization %d invalid; skipping: %v", musicID, err)
			continue
		}
		entry := PublicLyricsIndexSong{
			MusicID: musicID, Revision: document.Revision, UpdatedAt: document.UpdatedAt,
			State: state, Title: title, AvailableVersions: publicV3AvailableVersions(detail),
		}
		if !validLocalizationIndexEntry(entry) {
			log.Printf("[projection] lyrics localization %d index entry invalid; skipping", musicID)
			continue
		}
		index = append(index, entry)
		details[musicID] = detail
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return index, details, nil
}

// lyricsRenditionHasLocalizationCredit reports whether any rendition of an
// edited source-v3 document declares a translation or proofreading credit.
func lyricsRenditionHasLocalizationCredit(document LyricsRenditionDocument) bool {
	for _, rendition := range document.Renditions {
		if rendition.TranslationCredits != nil &&
			(rendition.TranslationCredits.Translation != "" || rendition.TranslationCredits.Proofreading != "") {
			return true
		}
	}
	return false
}

// validLocalizationIndexEntry applies the index-level contract checks for a
// projected localization entry without requiring a whole index document.
func validLocalizationIndexEntry(song PublicLyricsIndexSong) bool {
	return song.MusicID > 0 && song.Revision > 0 && canonicalPublicV3Timestamp(song.UpdatedAt) &&
		song.Title.Japanese != "" && song.Title.Japanese == strings.TrimSpace(song.Title.Japanese) &&
		(song.State == PublicLyricsStateComplete || song.State == PublicLyricsStateGameOnly) &&
		len(song.AvailableVersions) > 0 && len(song.AvailableVersions) <= 2
}
