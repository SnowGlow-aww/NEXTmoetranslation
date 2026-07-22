package db

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type migration struct {
	version int
	name    string
	sql     string
	after   func(*sql.Tx) error
}

func (m migration) checksum() string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s\x00%s", m.version, m.name, m.sql)))
	return hex.EncodeToString(sum[:])
}

var migrationBeforeCommitHook func(version int) error
var migrationBackupBeforeRenameHook func(tempPath string) error

var migrations = []migration{{
	version: 1,
	name:    "multilingual_content_and_lyrics",
	sql: `
CREATE TABLE schema_migrations (
	version    INTEGER PRIMARY KEY,
	name       TEXT NOT NULL,
	checksum   TEXT NOT NULL,
	applied_at INTEGER NOT NULL
);

CREATE TABLE entry_localizations (
	category   TEXT NOT NULL,
	field      TEXT NOT NULL,
	jp_key     TEXT NOT NULL,
	locale     TEXT NOT NULL,
	text       TEXT NOT NULL DEFAULT '',
	source     TEXT NOT NULL DEFAULT 'unknown',
	updated_at INTEGER NOT NULL DEFAULT 0,
	updated_by TEXT NOT NULL DEFAULT '',
	revision   INTEGER NOT NULL DEFAULT 1,
	PRIMARY KEY (category, field, jp_key, locale)
);
CREATE INDEX idx_entry_localizations_locale ON entry_localizations(locale, category, field);

CREATE TABLE event_story_segments (
	segment_id  TEXT PRIMARY KEY,
	event_id    INTEGER NOT NULL,
	episode_no  TEXT NOT NULL,
	scenario_id TEXT NOT NULL DEFAULT '',
	kind        TEXT NOT NULL,
	position    INTEGER NOT NULL,
	jp_key      TEXT NOT NULL DEFAULT '',
	source_text TEXT NOT NULL DEFAULT '',
	source_hash TEXT NOT NULL DEFAULT '',
	UNIQUE (event_id, episode_no, kind, position),
	FOREIGN KEY (event_id, episode_no) REFERENCES event_story_episodes(event_id, episode_no) ON DELETE CASCADE
);
CREATE INDEX idx_event_story_segments_lookup ON event_story_segments(event_id, episode_no, kind, jp_key);

CREATE TABLE event_story_segment_localizations (
	segment_id  TEXT NOT NULL,
	locale      TEXT NOT NULL,
	text        TEXT NOT NULL DEFAULT '',
	source      TEXT NOT NULL DEFAULT 'unknown',
	updated_at  INTEGER NOT NULL DEFAULT 0,
	updated_by  TEXT NOT NULL DEFAULT '',
	revision    INTEGER NOT NULL DEFAULT 1,
	PRIMARY KEY (segment_id, locale),
	FOREIGN KEY (segment_id) REFERENCES event_story_segments(segment_id) ON DELETE CASCADE
);

CREATE TABLE event_story_locale_meta (
	event_id    INTEGER NOT NULL,
	locale      TEXT NOT NULL,
	last_updated INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (event_id, locale),
	FOREIGN KEY (event_id) REFERENCES event_stories(event_id) ON DELETE CASCADE
);

CREATE TABLE catalog_music (
	music_id       INTEGER PRIMARY KEY,
	title_ja       TEXT NOT NULL,
	title_zh       TEXT NOT NULL DEFAULT '',
	title_en       TEXT NOT NULL DEFAULT '',
	jacket_url     TEXT NOT NULL DEFAULT '',
	newly_written  INTEGER NOT NULL DEFAULT 0,
	updated_at     INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE catalog_performers (
	performer_id INTEGER PRIMARY KEY,
	name_ja      TEXT NOT NULL,
	name_zh      TEXT NOT NULL DEFAULT '',
	name_en      TEXT NOT NULL DEFAULT '',
	updated_at   INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE song_lyrics (
	music_id      INTEGER PRIMARY KEY,
	revision      INTEGER NOT NULL DEFAULT 0,
	updated_at    INTEGER NOT NULL DEFAULT 0,
	updated_by    TEXT NOT NULL DEFAULT '',
	source_note   TEXT NOT NULL DEFAULT '',
	source_url    TEXT NOT NULL DEFAULT '',
	license_note  TEXT NOT NULL DEFAULT '',
	source_hash   TEXT NOT NULL DEFAULT '',
	FOREIGN KEY (music_id) REFERENCES catalog_music(music_id) ON DELETE RESTRICT
);

CREATE TABLE song_lyric_lines (
	music_id  INTEGER NOT NULL,
	line_id   TEXT NOT NULL,
	position  INTEGER NOT NULL,
	japanese  TEXT NOT NULL,
	zh_cn     TEXT NOT NULL DEFAULT '',
	en_us     TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (music_id, line_id),
	UNIQUE (music_id, position),
	FOREIGN KEY (music_id) REFERENCES song_lyrics(music_id) ON DELETE CASCADE
);

CREATE TABLE song_lyric_segments (
	music_id          INTEGER NOT NULL,
	line_id           TEXT NOT NULL,
	position          INTEGER NOT NULL,
	text              TEXT NOT NULL,
	performer_ids_json TEXT NOT NULL DEFAULT '[]',
	PRIMARY KEY (music_id, line_id, position),
	FOREIGN KEY (music_id, line_id) REFERENCES song_lyric_lines(music_id, line_id) ON DELETE CASCADE
);

CREATE TABLE song_lyrics_publications (
	music_id     INTEGER PRIMARY KEY,
	revision     INTEGER NOT NULL,
	updated_at   INTEGER NOT NULL,
	payload_json TEXT NOT NULL,
	FOREIGN KEY (music_id) REFERENCES song_lyrics(music_id) ON DELETE CASCADE
);

INSERT INTO event_story_segments
	(segment_id, event_id, episode_no, scenario_id, kind, position, jp_key, source_text, source_hash)
SELECT printf('event:%d:%s:%s:title', e.event_id, e.scenario_id, e.episode_no),
	e.event_id, e.episode_no, e.scenario_id, 'title', -1, '',
	CASE WHEN s.source = 'jp_pending' OR e.title_source = 'jp_pending' THEN e.title ELSE '' END, ''
FROM event_story_episodes e
JOIN event_stories s ON s.event_id = e.event_id;

INSERT INTO event_story_segments
	(segment_id, event_id, episode_no, scenario_id, kind, position, jp_key, source_text, source_hash)
SELECT printf('event:%d:%s:%s:talk:%d', l.event_id, e.scenario_id, l.episode_no, l.position),
	l.event_id, l.episode_no, e.scenario_id, 'talk', l.position, l.jp_key, l.jp_key, ''
FROM event_story_lines l
JOIN event_story_episodes e ON e.event_id = l.event_id AND e.episode_no = l.episode_no;

INSERT INTO event_story_segment_localizations (segment_id, locale, text, source, updated_at, updated_by, revision)
SELECT seg.segment_id, 'zh-CN', e.title, e.title_source, s.last_updated, 'migration', 1
FROM event_story_segments seg
JOIN event_story_episodes e ON e.event_id = seg.event_id AND e.episode_no = seg.episode_no
JOIN event_stories s ON s.event_id = seg.event_id
WHERE seg.kind = 'title' AND s.source <> 'jp_pending' AND e.title_source <> 'jp_pending';

INSERT INTO event_story_segment_localizations (segment_id, locale, text, source, updated_at, updated_by, revision)
SELECT seg.segment_id, 'zh-CN', l.cn_text, l.source, s.last_updated, 'migration', 1
FROM event_story_segments seg
JOIN event_story_lines l ON l.event_id = seg.event_id AND l.episode_no = seg.episode_no AND l.position = seg.position
JOIN event_stories s ON s.event_id = seg.event_id
WHERE seg.kind = 'talk';
`,
	after: backfillSegmentHashes,
}, {
	version: 2,
	name:    "lyrics_source_provenance_and_stanzas",
	sql: `
ALTER TABLE song_lyrics ADD COLUMN source_page_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE song_lyrics ADD COLUMN source_revision_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE song_lyrics ADD COLUMN source_sha1 TEXT NOT NULL DEFAULT '';
ALTER TABLE song_lyrics ADD COLUMN source_fetched_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE song_lyric_lines ADD COLUMN stanza_break_before INTEGER NOT NULL DEFAULT 0;
`,
}, {
	version: 3,
	name:    "lyrics_catalog_source_identity",
	sql: `
ALTER TABLE catalog_music ADD COLUMN producer_metadata TEXT NOT NULL DEFAULT '';
`,
}, {
	version: 4,
	name:    "rolling_event_side_tables_no_legacy_cascade",
	sql: `
CREATE TABLE event_story_segments_next (
	segment_id  TEXT PRIMARY KEY,
	event_id    INTEGER NOT NULL,
	episode_no  TEXT NOT NULL,
	scenario_id TEXT NOT NULL DEFAULT '',
	kind        TEXT NOT NULL,
	position    INTEGER NOT NULL,
	jp_key      TEXT NOT NULL DEFAULT '',
	source_text TEXT NOT NULL DEFAULT '',
	source_hash TEXT NOT NULL DEFAULT '',
	UNIQUE (event_id, episode_no, kind, position)
);
INSERT INTO event_story_segments_next SELECT * FROM event_story_segments;

CREATE TABLE event_story_segment_localizations_next (
	segment_id  TEXT NOT NULL,
	locale      TEXT NOT NULL,
	text        TEXT NOT NULL DEFAULT '',
	source      TEXT NOT NULL DEFAULT 'unknown',
	updated_at  INTEGER NOT NULL DEFAULT 0,
	updated_by  TEXT NOT NULL DEFAULT '',
	revision    INTEGER NOT NULL DEFAULT 1,
	PRIMARY KEY (segment_id, locale),
	FOREIGN KEY (segment_id) REFERENCES event_story_segments_next(segment_id) ON DELETE CASCADE
);
INSERT INTO event_story_segment_localizations_next SELECT * FROM event_story_segment_localizations;

DROP TABLE event_story_segment_localizations;
DROP TABLE event_story_segments;
ALTER TABLE event_story_segments_next RENAME TO event_story_segments;
ALTER TABLE event_story_segment_localizations_next RENAME TO event_story_segment_localizations;
CREATE INDEX idx_event_story_segments_lookup ON event_story_segments(event_id, episode_no, kind, jp_key);

CREATE TABLE event_story_locale_meta_next (
	event_id     INTEGER NOT NULL,
	locale       TEXT NOT NULL,
	last_updated INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (event_id, locale)
);
INSERT INTO event_story_locale_meta_next SELECT * FROM event_story_locale_meta;
DROP TABLE event_story_locale_meta;
ALTER TABLE event_story_locale_meta_next RENAME TO event_story_locale_meta;
`,
}, {
	version: 5,
	name:    "stable_event_title_segment_identity",
	sql: `
CREATE TABLE event_story_segments_next (
	segment_id  TEXT PRIMARY KEY,
	event_id    INTEGER NOT NULL,
	episode_no  TEXT NOT NULL,
	scenario_id TEXT NOT NULL DEFAULT '',
	kind        TEXT NOT NULL,
	position    INTEGER NOT NULL,
	jp_key      TEXT NOT NULL DEFAULT '',
	source_text TEXT NOT NULL DEFAULT '',
	source_hash TEXT NOT NULL DEFAULT '',
	UNIQUE (event_id, episode_no, kind, position)
);
INSERT INTO event_story_segments_next
	(segment_id, event_id, episode_no, scenario_id, kind, position, jp_key, source_text, source_hash)
SELECT CASE WHEN kind='title' AND segment_id NOT LIKE '%:title:-1' THEN segment_id || ':-1' ELSE segment_id END,
	event_id, episode_no, scenario_id, kind, position, jp_key, source_text, source_hash
FROM event_story_segments;

CREATE TABLE event_story_segment_localizations_next (
	segment_id  TEXT NOT NULL,
	locale      TEXT NOT NULL,
	text        TEXT NOT NULL DEFAULT '',
	source      TEXT NOT NULL DEFAULT 'unknown',
	updated_at  INTEGER NOT NULL DEFAULT 0,
	updated_by  TEXT NOT NULL DEFAULT '',
	revision    INTEGER NOT NULL DEFAULT 1,
	PRIMARY KEY (segment_id, locale),
	FOREIGN KEY (segment_id) REFERENCES event_story_segments_next(segment_id) ON DELETE CASCADE
);
INSERT INTO event_story_segment_localizations_next
	(segment_id, locale, text, source, updated_at, updated_by, revision)
SELECT CASE WHEN seg.kind='title' AND loc.segment_id NOT LIKE '%:title:-1' THEN loc.segment_id || ':-1' ELSE loc.segment_id END,
	loc.locale, loc.text, loc.source, loc.updated_at, loc.updated_by, loc.revision
FROM event_story_segment_localizations loc
JOIN event_story_segments seg ON seg.segment_id=loc.segment_id;

DROP TABLE event_story_segment_localizations;
DROP TABLE event_story_segments;
ALTER TABLE event_story_segments_next RENAME TO event_story_segments;
ALTER TABLE event_story_segment_localizations_next RENAME TO event_story_segment_localizations;
CREATE INDEX idx_event_story_segments_lookup ON event_story_segments(event_id, episode_no, kind, jp_key);
`,
}, {
	version: 6,
	name:    "mark_legacy_event_talk_identity",
	sql: `
CREATE TABLE event_story_segments_next (
	segment_id  TEXT PRIMARY KEY,
	event_id    INTEGER NOT NULL,
	episode_no  TEXT NOT NULL,
	scenario_id TEXT NOT NULL DEFAULT '',
	kind        TEXT NOT NULL,
	position    INTEGER NOT NULL,
	jp_key      TEXT NOT NULL DEFAULT '',
	source_text TEXT NOT NULL DEFAULT '',
	source_hash TEXT NOT NULL DEFAULT '',
	UNIQUE (event_id, episode_no, kind, position)
);
INSERT INTO event_story_segments_next
	(segment_id, event_id, episode_no, scenario_id, kind, position, jp_key, source_text, source_hash)
SELECT CASE
	WHEN kind='talk' AND segment_id NOT LIKE '%:body' AND segment_id NOT LIKE '%:speaker' AND segment_id NOT LIKE '%:legacy'
	THEN segment_id || ':legacy'
	ELSE segment_id END,
	event_id, episode_no, scenario_id, kind, position, jp_key, source_text, source_hash
FROM event_story_segments;

CREATE TABLE event_story_segment_localizations_next (
	segment_id  TEXT NOT NULL,
	locale      TEXT NOT NULL,
	text        TEXT NOT NULL DEFAULT '',
	source      TEXT NOT NULL DEFAULT 'unknown',
	updated_at  INTEGER NOT NULL DEFAULT 0,
	updated_by  TEXT NOT NULL DEFAULT '',
	revision    INTEGER NOT NULL DEFAULT 1,
	PRIMARY KEY (segment_id, locale),
	FOREIGN KEY (segment_id) REFERENCES event_story_segments_next(segment_id) ON DELETE CASCADE
);
INSERT INTO event_story_segment_localizations_next
	(segment_id, locale, text, source, updated_at, updated_by, revision)
SELECT CASE
	WHEN seg.kind='talk' AND loc.segment_id NOT LIKE '%:body' AND loc.segment_id NOT LIKE '%:speaker' AND loc.segment_id NOT LIKE '%:legacy'
	THEN loc.segment_id || ':legacy'
	ELSE loc.segment_id END,
	loc.locale, loc.text, loc.source, loc.updated_at, loc.updated_by, loc.revision
FROM event_story_segment_localizations loc
JOIN event_story_segments seg ON seg.segment_id=loc.segment_id;

DROP TABLE event_story_segment_localizations;
DROP TABLE event_story_segments;
ALTER TABLE event_story_segments_next RENAME TO event_story_segments;
ALTER TABLE event_story_segment_localizations_next RENAME TO event_story_segment_localizations;
CREATE INDEX idx_event_story_segments_lookup ON event_story_segments(event_id, episode_no, kind, jp_key);
`,
}}

func (d *DB) pendingMigrations() ([]migration, error) {
	var exists int
	if err := d.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'`).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return append([]migration(nil), migrations...), nil
	}
	rows, err := d.Query(`SELECT version, name, checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	applied := map[int]string{}
	for rows.Next() {
		var version int
		var name, checksum string
		if err := rows.Scan(&version, &name, &checksum); err != nil {
			return nil, err
		}
		if version < 1 || version > len(migrations) {
			return nil, fmt.Errorf("database migration version %d is newer than this binary", version)
		}
		want := migrations[version-1]
		if name != want.name || checksum != want.checksum() {
			return nil, fmt.Errorf("migration %d checksum mismatch", version)
		}
		applied[version] = checksum
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var pending []migration
	for _, m := range migrations {
		if _, ok := applied[m.version]; !ok {
			pending = append(pending, m)
		}
	}
	return pending, nil
}

func (d *DB) applyMigrations(pending []migration) error {
	for _, m := range pending {
		tx, err := d.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(m.sql); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d %s: %w", m.version, m.name, err)
		}
		if m.after != nil {
			if err := m.after(tx); err != nil {
				tx.Rollback()
				return fmt.Errorf("migration %d backfill: %w", m.version, err)
			}
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES (?, ?, ?, ?)`,
			m.version, m.name, m.checksum(), time.Now().Unix()); err != nil {
			tx.Rollback()
			return err
		}
		if migrationBeforeCommitHook != nil {
			if err := migrationBeforeCommitHook(m.version); err != nil {
				tx.Rollback()
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) createPreMigrationBackup(version int) (string, error) {
	if strings.TrimSpace(d.path) == "" || strings.Contains(d.path, ":memory:") {
		return "", nil
	}
	backupPath := fmt.Sprintf("%s.pre-migration-v%d.bak", d.path, version)
	if _, err := os.Stat(backupPath); err == nil {
		if err := verifySQLiteBackup(backupPath); err == nil {
			return backupPath, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(backupPath), 0o755); err != nil {
		return "", err
	}
	tempPath := backupPath + ".tmp"
	if err := os.Remove(tempPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if _, err := d.Exec(`VACUUM INTO ?`, tempPath); err != nil {
		return "", err
	}
	defer os.Remove(tempPath)
	if err := verifySQLiteBackup(tempPath); err != nil {
		return "", err
	}
	backupFile, err := os.OpenFile(tempPath, os.O_RDWR, 0)
	if err != nil {
		return "", err
	}
	if err := backupFile.Sync(); err != nil {
		backupFile.Close()
		return "", err
	}
	if err := backupFile.Close(); err != nil {
		return "", err
	}
	if migrationBackupBeforeRenameHook != nil {
		if err := migrationBackupBeforeRenameHook(tempPath); err != nil {
			return "", err
		}
	}
	if err := os.Rename(tempPath, backupPath); err != nil {
		return "", err
	}
	dir, err := os.Open(filepath.Dir(backupPath))
	if err != nil {
		return "", err
	}
	if err := dir.Sync(); err != nil {
		dir.Close()
		return "", err
	}
	if err := dir.Close(); err != nil {
		return "", err
	}
	return backupPath, nil
}

func verifySQLiteBackup(path string) error {
	backup, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return err
	}
	defer backup.Close()
	var result string
	if err := backup.QueryRow(`PRAGMA integrity_check`).Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("backup integrity_check: %s", result)
	}
	return nil
}

func backfillSegmentHashes(tx *sql.Tx) error {
	rows, err := tx.Query(`SELECT segment_id, source_text FROM event_story_segments WHERE source_text <> ''`)
	if err != nil {
		return err
	}
	type update struct{ id, hash string }
	var updates []update
	for rows.Next() {
		var id, text string
		if err := rows.Scan(&id, &text); err != nil {
			rows.Close()
			return err
		}
		sum := sha256.Sum256([]byte(text))
		updates = append(updates, update{id: id, hash: hex.EncodeToString(sum[:])})
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, u := range updates {
		if _, err := tx.Exec(`UPDATE event_story_segments SET source_hash=? WHERE segment_id=?`, u.hash, u.id); err != nil {
			return err
		}
	}
	return nil
}
