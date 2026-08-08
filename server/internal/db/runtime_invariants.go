package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ValidateLyricsStorageOwnership rejects historical databases that already
// contain both the legacy editable lyrics row and a native source-v3 document
// for the same music. Runtime triggers prevent new mixed writes, but existing
// mixed rows must also fail closed at every read/publish boundary.
func (d *DB) ValidateLyricsStorageOwnership(ctx context.Context) error {
	var musicID int
	err := d.QueryRowContext(ctx, `SELECT legacy.music_id
		FROM song_lyrics AS legacy
		JOIN song_lyrics_source_documents AS source
			ON source.music_id=legacy.music_id
		WHERE source.schema_version=?
		ORDER BY legacy.music_id
		LIMIT 1`, 3).Scan(&musicID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("scan lyrics storage ownership: %w", err)
	}
	return fmt.Errorf("mixed lyrics storage ownership for music %d: source-v3 and legacy editable rows coexist", musicID)
}

// ensureRuntimeInvariants installs additive guards that protect newer
// cross-table ownership rules without changing the checksum of an already
// published migration. These guards are safe to re-run on every normal Open.
func (d *DB) ensureRuntimeInvariants(ctx context.Context) error {
	if err := d.ValidateLyricsStorageOwnership(ctx); err != nil {
		return err
	}
	statements := []string{
		`CREATE TRIGGER IF NOT EXISTS song_lyrics_source_v3_reject_legacy_insert
			BEFORE INSERT ON song_lyrics_source_documents
			WHEN NEW.schema_version=3 AND EXISTS (SELECT 1 FROM song_lyrics WHERE music_id=NEW.music_id)
			BEGIN SELECT RAISE(ABORT, 'source v3 cannot coexist with legacy editable lyrics'); END`,
		`CREATE TRIGGER IF NOT EXISTS song_lyrics_reject_source_v3_insert
			BEFORE INSERT ON song_lyrics
			WHEN EXISTS (SELECT 1 FROM song_lyrics_source_documents WHERE music_id=NEW.music_id AND schema_version=3)
			BEGIN SELECT RAISE(ABORT, 'legacy editable lyrics cannot coexist with source v3'); END`,
		`CREATE TRIGGER IF NOT EXISTS song_lyrics_reject_source_v3_update
			BEFORE UPDATE ON song_lyrics
			WHEN EXISTS (SELECT 1 FROM song_lyrics_source_documents WHERE music_id=NEW.music_id AND schema_version=3)
			BEGIN SELECT RAISE(ABORT, 'legacy editable lyrics cannot coexist with source v3'); END`,
		`CREATE TRIGGER IF NOT EXISTS song_lyrics_source_v3_reject_delete
			BEFORE DELETE ON song_lyrics_source_documents
			WHEN OLD.schema_version=3
			BEGIN SELECT RAISE(ABORT, 'source v3 documents are immutable'); END`,
	}
	for _, statement := range statements {
		if _, err := d.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
