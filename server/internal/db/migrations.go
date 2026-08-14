package db

import (
	"context"
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
	before  func(*sql.Tx) error
	after   func(*sql.Tx) error
}

func (m migration) checksum() string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s\x00%s", m.version, m.name, m.sql)))
	return hex.EncodeToString(sum[:])
}

func (m migration) validateDefinition() error {
	if m.version >= 13 && (m.before != nil || m.after != nil) {
		return fmt.Errorf("migration %d cannot use an unchecksummed migration callback", m.version)
	}
	return nil
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
}, {
	version: 7,
	name:    "public_lyrics_attribution_and_token_generation",
	sql: `
ALTER TABLE song_lyrics ADD COLUMN attribution TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN token_version INTEGER NOT NULL DEFAULT 1;
DELETE FROM song_lyrics_publications;
`,
}, {
	version: 8,
	name:    "event_story_scenario_snapshots",
	sql: `
CREATE TABLE event_story_scenarios (
	event_id       INTEGER NOT NULL,
	episode_no     TEXT NOT NULL,
	scenario_id    TEXT NOT NULL,
	canonical_json TEXT NOT NULL,
	sha256         TEXT NOT NULL,
	PRIMARY KEY (event_id, episode_no),
	UNIQUE (event_id, scenario_id),
	CHECK (episode_no <> ''),
	CHECK (scenario_id <> ''),
	CHECK (canonical_json <> ''),
	CHECK (length(sha256) = 64)
);
CREATE INDEX idx_event_story_scenarios_identity ON event_story_scenarios(event_id, scenario_id);
`,
}, {
	version: 9,
	name:    "retain_rolling_event_segment_recovery_rows",
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
	source_hash TEXT NOT NULL DEFAULT ''
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
`,
}, {
	version: 10,
	name:    "durable_lyrics_discovery_jobs",
	sql: `
CREATE TABLE lyrics_discovery_jobs (
	job_id           INTEGER PRIMARY KEY AUTOINCREMENT,
	idempotency_key  TEXT NOT NULL UNIQUE,
	kind             TEXT NOT NULL,
	state            TEXT NOT NULL,
	music_id         INTEGER NOT NULL,
	page_id          INTEGER,
	revision_id      INTEGER,
	artifact_id      INTEGER,
	attempts         INTEGER NOT NULL DEFAULT 0,
	max_attempts     INTEGER NOT NULL,
	next_attempt_at  INTEGER NOT NULL,
	lease_owner      TEXT,
	lease_expires_at INTEGER,
	last_error_code  TEXT,
	created_at       INTEGER NOT NULL,
	updated_at       INTEGER NOT NULL,
	completed_at     INTEGER,
	version          INTEGER NOT NULL DEFAULT 1,
	CHECK (length(idempotency_key) = 64 AND idempotency_key = lower(idempotency_key) AND idempotency_key NOT GLOB '*[^0-9a-f]*'),
	CHECK (kind IN ('discover', 'fetch_revision', 'revalidate_pinned', 'revalidate_head')),
	CHECK (length(kind) <= 32),
	CHECK (state IN ('queued', 'leased', 'retry_wait', 'succeeded', 'dead_letter', 'cancelled')),
	CHECK (length(state) <= 16),
	CHECK (music_id > 0),
	CHECK (page_id IS NULL OR page_id > 0),
	CHECK (revision_id IS NULL OR revision_id > 0),
	CHECK (artifact_id IS NULL OR artifact_id > 0),
	CHECK (attempts >= 0 AND attempts <= max_attempts),
	CHECK (state <> 'leased' OR attempts > 0),
	CHECK (state <> 'dead_letter' OR attempts = max_attempts),
	CHECK (max_attempts BETWEEN 1 AND 100),
	CHECK (next_attempt_at >= 0),
	CHECK ((state = 'leased' AND lease_owner IS NOT NULL AND lease_expires_at IS NOT NULL) OR
		(state <> 'leased' AND lease_owner IS NULL AND lease_expires_at IS NULL)),
	CHECK (lease_owner IS NULL OR (length(lease_owner) BETWEEN 1 AND 128 AND lease_owner = trim(lease_owner))),
	CHECK (lease_expires_at IS NULL OR lease_expires_at > 0),
	CHECK (last_error_code IS NULL OR (length(last_error_code) BETWEEN 1 AND 64 AND last_error_code = lower(last_error_code) AND last_error_code NOT GLOB '*[^a-z0-9_]*')),
	CHECK (created_at >= 0 AND updated_at >= created_at),
	CHECK ((state IN ('succeeded', 'dead_letter', 'cancelled')) = (completed_at IS NOT NULL)),
	CHECK (completed_at IS NULL OR completed_at >= created_at),
	CHECK (version > 0),
	CHECK (
		(kind = 'discover' AND page_id IS NULL AND revision_id IS NULL AND artifact_id IS NULL) OR
		(kind = 'fetch_revision' AND page_id IS NOT NULL AND revision_id IS NOT NULL) OR
		(kind = 'revalidate_pinned' AND page_id IS NOT NULL AND artifact_id IS NOT NULL) OR
		(kind = 'revalidate_head' AND page_id IS NOT NULL AND revision_id IS NULL)
	)
);
CREATE INDEX idx_lyrics_discovery_jobs_claim
	ON lyrics_discovery_jobs(state, next_attempt_at, job_id);
CREATE INDEX idx_lyrics_discovery_jobs_lease_expiry
	ON lyrics_discovery_jobs(lease_expires_at, job_id) WHERE state = 'leased';
CREATE INDEX idx_lyrics_discovery_jobs_music
	ON lyrics_discovery_jobs(music_id, job_id);
`,
}, {
	version: 11,
	name:    "lyrics_discovery_shadow_results",
	sql: `
ALTER TABLE lyrics_discovery_jobs ADD COLUMN catalog_fingerprint TEXT NOT NULL DEFAULT '';
ALTER TABLE lyrics_discovery_jobs ADD COLUMN policy_version TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX idx_lyrics_discovery_jobs_shadow_identity
	ON lyrics_discovery_jobs(job_id, music_id, catalog_fingerprint, policy_version);

CREATE TABLE lyrics_discovery_shadow_results (
	result_id            INTEGER PRIMARY KEY AUTOINCREMENT,
	job_id               INTEGER NOT NULL UNIQUE,
	music_id             INTEGER NOT NULL,
	catalog_fingerprint  TEXT NOT NULL,
	policy_version       TEXT NOT NULL,
	outcome              TEXT NOT NULL,
	candidate_count      INTEGER NOT NULL,
	result_json          TEXT NOT NULL,
	created_at           INTEGER NOT NULL,
	CHECK (music_id > 0),
	CHECK (length(catalog_fingerprint) = 64 AND catalog_fingerprint = lower(catalog_fingerprint) AND catalog_fingerprint NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(policy_version) BETWEEN 1 AND 64 AND policy_version = trim(policy_version)),
	CHECK (outcome IN ('candidates_found', 'no_candidates', 'ambiguous')),
	CHECK (candidate_count >= 0),
	CHECK ((outcome = 'candidates_found' AND candidate_count = 1) OR
		(outcome = 'no_candidates' AND candidate_count = 0) OR
		(outcome = 'ambiguous' AND candidate_count > 1)),
	CHECK (length(result_json) BETWEEN 2 AND 1048576 AND json_valid(result_json) AND json_type(result_json) = 'object'),
	CHECK (created_at >= 0),
	FOREIGN KEY (job_id, music_id, catalog_fingerprint, policy_version)
		REFERENCES lyrics_discovery_jobs(job_id, music_id, catalog_fingerprint, policy_version) ON DELETE CASCADE
);
CREATE INDEX idx_lyrics_discovery_shadow_results_music
	ON lyrics_discovery_shadow_results(music_id, result_id);
`,
}, {
	version: 12,
	name:    "enforce_lyrics_discovery_integer_types",
	sql: `
CREATE TRIGGER lyrics_discovery_jobs_integer_types_insert
BEFORE INSERT ON lyrics_discovery_jobs
WHEN typeof(NEW.job_id) <> 'integer'
	OR typeof(NEW.music_id) <> 'integer'
	OR typeof(NEW.page_id) NOT IN ('null', 'integer')
	OR typeof(NEW.revision_id) NOT IN ('null', 'integer')
	OR typeof(NEW.artifact_id) NOT IN ('null', 'integer')
	OR typeof(NEW.attempts) <> 'integer'
	OR typeof(NEW.max_attempts) <> 'integer'
	OR typeof(NEW.next_attempt_at) <> 'integer'
	OR typeof(NEW.lease_expires_at) NOT IN ('null', 'integer')
	OR typeof(NEW.created_at) <> 'integer'
	OR typeof(NEW.updated_at) <> 'integer'
	OR typeof(NEW.completed_at) NOT IN ('null', 'integer')
	OR typeof(NEW.version) <> 'integer'
BEGIN
	SELECT RAISE(ABORT, 'lyrics discovery job integer fields must be integers');
END;

CREATE TRIGGER lyrics_discovery_jobs_integer_types_update
BEFORE UPDATE ON lyrics_discovery_jobs
WHEN typeof(NEW.job_id) <> 'integer'
	OR typeof(NEW.music_id) <> 'integer'
	OR typeof(NEW.page_id) NOT IN ('null', 'integer')
	OR typeof(NEW.revision_id) NOT IN ('null', 'integer')
	OR typeof(NEW.artifact_id) NOT IN ('null', 'integer')
	OR typeof(NEW.attempts) <> 'integer'
	OR typeof(NEW.max_attempts) <> 'integer'
	OR typeof(NEW.next_attempt_at) <> 'integer'
	OR typeof(NEW.lease_expires_at) NOT IN ('null', 'integer')
	OR typeof(NEW.created_at) <> 'integer'
	OR typeof(NEW.updated_at) <> 'integer'
	OR typeof(NEW.completed_at) NOT IN ('null', 'integer')
	OR typeof(NEW.version) <> 'integer'
BEGIN
	SELECT RAISE(ABORT, 'lyrics discovery job integer fields must be integers');
END;

CREATE TRIGGER lyrics_discovery_shadow_results_integer_types_insert
BEFORE INSERT ON lyrics_discovery_shadow_results
WHEN typeof(NEW.result_id) <> 'integer'
	OR typeof(NEW.job_id) <> 'integer'
	OR typeof(NEW.music_id) <> 'integer'
	OR typeof(NEW.candidate_count) <> 'integer'
	OR typeof(NEW.created_at) <> 'integer'
BEGIN
	SELECT RAISE(ABORT, 'lyrics discovery shadow result integer fields must be integers');
END;

CREATE TRIGGER lyrics_discovery_shadow_results_integer_types_update
BEFORE UPDATE ON lyrics_discovery_shadow_results
WHEN typeof(NEW.result_id) <> 'integer'
	OR typeof(NEW.job_id) <> 'integer'
	OR typeof(NEW.music_id) <> 'integer'
	OR typeof(NEW.candidate_count) <> 'integer'
	OR typeof(NEW.created_at) <> 'integer'
BEGIN
	SELECT RAISE(ABORT, 'lyrics discovery shadow result integer fields must be integers');
END;
`,
	after: validateLyricsDiscoveryIntegerTypes,
}, {
	version: 13,
	name:    "private_lyrics_source_artifacts",
	sql: `
ALTER TABLE catalog_music ADD COLUMN lyricist TEXT NOT NULL DEFAULT '';
ALTER TABLE catalog_music ADD COLUMN composer TEXT NOT NULL DEFAULT '';
ALTER TABLE catalog_music ADD COLUMN arranger TEXT NOT NULL DEFAULT '';
ALTER TABLE catalog_music ADD COLUMN assetbundle_name TEXT NOT NULL DEFAULT '';
ALTER TABLE catalog_music ADD COLUMN version_hint TEXT NOT NULL DEFAULT '';
ALTER TABLE catalog_music ADD COLUMN lyrics_version TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE catalog_music ADD COLUMN lyrics_evidence_presence_json TEXT NOT NULL DEFAULT '{"lyricist":false,"composer":false,"arranger":false,"assetbundle":false,"versionHint":false,"lyricsVersion":false,"vocals":false}';
ALTER TABLE catalog_music ADD COLUMN vocal_signals_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE catalog_music ADD COLUMN lyrics_catalog_fingerprint TEXT NOT NULL DEFAULT '';
ALTER TABLE catalog_music ADD COLUMN lyrics_catalog_policy_version TEXT NOT NULL DEFAULT 'catalog-identity-v2';
ALTER TABLE lyrics_discovery_jobs ADD COLUMN expected_sha1 TEXT NOT NULL DEFAULT '';
CREATE TEMP TABLE lyrics_discovery_v13_expected_sha1 (
	job_id         INTEGER PRIMARY KEY,
	expected_sha1  TEXT NOT NULL
);
CREATE TEMP TRIGGER lyrics_discovery_v13_expected_sha1_conflict
BEFORE UPDATE ON lyrics_discovery_v13_expected_sha1
WHEN OLD.expected_sha1<>NEW.expected_sha1
BEGIN SELECT RAISE(ABORT, 'conflicting expected lyrics source sha1'); END;
INSERT INTO lyrics_discovery_v13_expected_sha1(job_id, expected_sha1)
SELECT j.job_id,
	json_extract(candidate.value, '$.sha1')
FROM lyrics_discovery_jobs j
JOIN lyrics_discovery_shadow_results r ON r.job_id=j.job_id
JOIN json_each(json_extract(r.result_json, '$.candidates')) candidate
WHERE j.kind='fetch_revision'
	AND typeof(json_extract(candidate.value, '$.pageId'))='integer'
	AND json_extract(candidate.value, '$.pageId')=j.page_id
	AND typeof(json_extract(candidate.value, '$.revisionId'))='integer'
	AND json_extract(candidate.value, '$.revisionId')=j.revision_id
	AND typeof(json_extract(candidate.value, '$.sha1'))='text'
	AND length(json_extract(candidate.value, '$.sha1'))=40
	AND json_extract(candidate.value, '$.sha1')=lower(json_extract(candidate.value, '$.sha1'))
	AND json_extract(candidate.value, '$.sha1') NOT GLOB '*[^0-9a-f]*'
ON CONFLICT(job_id) DO UPDATE SET expected_sha1=excluded.expected_sha1;
DROP TRIGGER temp.lyrics_discovery_v13_expected_sha1_conflict;
UPDATE lyrics_discovery_jobs
SET expected_sha1 = COALESCE((SELECT e.expected_sha1 FROM lyrics_discovery_v13_expected_sha1 e WHERE e.job_id=lyrics_discovery_jobs.job_id), '')
WHERE kind='fetch_revision';
CREATE TEMP TRIGGER lyrics_discovery_v13_expected_sha1_preflight
BEFORE DELETE ON main.lyrics_discovery_jobs
WHEN OLD.kind='fetch_revision' AND OLD.expected_sha1=''
BEGIN SELECT RAISE(ABORT, 'unreconcilable v12 fetch_revision expected_sha1'); END;
DELETE FROM main.lyrics_discovery_jobs WHERE kind='fetch_revision' AND expected_sha1='';
DROP TRIGGER temp.lyrics_discovery_v13_expected_sha1_preflight;
DROP TABLE temp.lyrics_discovery_v13_expected_sha1;

CREATE TRIGGER lyrics_discovery_fetch_expected_sha1_insert
BEFORE INSERT ON lyrics_discovery_jobs
WHEN (NEW.kind = 'fetch_revision' AND (length(NEW.expected_sha1) <> 40 OR NEW.expected_sha1 <> lower(NEW.expected_sha1) OR NEW.expected_sha1 GLOB '*[^0-9a-f]*'))
	OR (NEW.kind <> 'fetch_revision' AND NEW.expected_sha1 <> '')
BEGIN SELECT RAISE(ABORT, 'invalid expected lyrics source sha1'); END;
CREATE TRIGGER lyrics_discovery_fetch_expected_sha1_update
BEFORE UPDATE ON lyrics_discovery_jobs
WHEN (NEW.kind = 'fetch_revision' AND (length(NEW.expected_sha1) <> 40 OR NEW.expected_sha1 <> lower(NEW.expected_sha1) OR NEW.expected_sha1 GLOB '*[^0-9a-f]*'))
	OR (NEW.kind <> 'fetch_revision' AND NEW.expected_sha1 <> '')
BEGIN SELECT RAISE(ABORT, 'invalid expected lyrics source sha1'); END;

CREATE TABLE lyrics_source_artifacts (
	artifact_id             INTEGER PRIMARY KEY AUTOINCREMENT,
	source_type             TEXT NOT NULL,
	source_origin           TEXT NOT NULL,
	page_id                 INTEGER NOT NULL,
	revision_id             INTEGER NOT NULL,
	page_title              TEXT NOT NULL,
	canonical_revision_url  TEXT NOT NULL,
	mediawiki_sha1          TEXT NOT NULL,
	categories_json         TEXT NOT NULL,
	raw_wikitext            BLOB NOT NULL,
	raw_byte_count          INTEGER NOT NULL,
	raw_wikitext_sha256     TEXT NOT NULL,
	artifact_sha256         TEXT NOT NULL,
	first_fetched_at        INTEGER NOT NULL,
	first_creating_job_id   INTEGER NOT NULL,
	created_at              INTEGER NOT NULL,
	UNIQUE (source_type, source_origin, page_id, revision_id),
	CHECK (source_type = 'mediawiki'),
	CHECK (source_origin = 'https://vocaloid.fandom.com'),
	CHECK (typeof(page_id) = 'integer' AND page_id > 0),
	CHECK (typeof(revision_id) = 'integer' AND revision_id > 0),
	CHECK (length(page_title) BETWEEN 1 AND 2048 AND page_title = trim(page_title)),
	CHECK (length(canonical_revision_url) BETWEEN 1 AND 4096 AND canonical_revision_url = trim(canonical_revision_url)),
	CHECK (length(mediawiki_sha1) = 40 AND mediawiki_sha1 = lower(mediawiki_sha1) AND mediawiki_sha1 NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(categories_json) BETWEEN 2 AND 262144 AND json_valid(categories_json) AND json_type(categories_json) = 'array'),
	CHECK (typeof(raw_wikitext) = 'blob'),
	CHECK (typeof(raw_byte_count) = 'integer' AND raw_byte_count BETWEEN 1 AND 2097152 AND raw_byte_count = length(raw_wikitext)),
	CHECK (length(raw_wikitext_sha256) = 64 AND raw_wikitext_sha256 = lower(raw_wikitext_sha256) AND raw_wikitext_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(artifact_sha256) = 64 AND artifact_sha256 = lower(artifact_sha256) AND artifact_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (typeof(first_fetched_at) = 'integer' AND first_fetched_at > 0),
	CHECK (typeof(first_creating_job_id) = 'integer' AND first_creating_job_id > 0),
	CHECK (typeof(created_at) = 'integer' AND created_at > 0)
);
CREATE INDEX idx_lyrics_source_artifacts_revision ON lyrics_source_artifacts(source_origin, revision_id);
CREATE TRIGGER lyrics_source_artifacts_immutable_update
BEFORE UPDATE ON lyrics_source_artifacts BEGIN
	SELECT RAISE(ABORT, 'lyrics source artifacts are immutable');
END;
CREATE TRIGGER lyrics_source_artifacts_immutable_delete
BEFORE DELETE ON lyrics_source_artifacts BEGIN
	SELECT RAISE(ABORT, 'lyrics source artifacts are immutable');
END;
`,
}, {
	version: 14,
	name:    "versioned_lyrics_source_analysis_outputs",
	sql: `
CREATE TABLE lyrics_source_analyses (
	analysis_id                INTEGER PRIMARY KEY AUTOINCREMENT,
	analysis_key               TEXT NOT NULL UNIQUE,
	artifact_id                INTEGER NOT NULL,
	music_id                   INTEGER NOT NULL,
	catalog_fingerprint        TEXT NOT NULL,
	matching_policy_version    TEXT NOT NULL,
	restriction_policy_version TEXT NOT NULL,
	extractor_version          TEXT NOT NULL,
	match_outcome              TEXT NOT NULL,
	restriction_outcome        TEXT NOT NULL,
	extraction_outcome         TEXT NOT NULL,
	matching_evidence_json     TEXT NOT NULL,
	restriction_rule_ids_json  TEXT NOT NULL,
	extracted_lines_json       TEXT NOT NULL,
	extracted_line_count       INTEGER NOT NULL,
	extracted_lines_sha256     TEXT NOT NULL,
	analysis_sha256            TEXT NOT NULL,
	creating_job_id            INTEGER NOT NULL,
	created_at                 INTEGER NOT NULL,
	UNIQUE (artifact_id, music_id, catalog_fingerprint, matching_policy_version, restriction_policy_version, extractor_version),
	CHECK (length(analysis_key) = 64 AND analysis_key = lower(analysis_key) AND analysis_key NOT GLOB '*[^0-9a-f]*'),
	CHECK (typeof(artifact_id) = 'integer' AND artifact_id > 0),
	CHECK (typeof(music_id) = 'integer' AND music_id > 0),
	CHECK (length(catalog_fingerprint) = 64 AND catalog_fingerprint = lower(catalog_fingerprint) AND catalog_fingerprint NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(matching_policy_version) BETWEEN 1 AND 128 AND matching_policy_version = trim(matching_policy_version)),
	CHECK (length(restriction_policy_version) BETWEEN 1 AND 128 AND restriction_policy_version = trim(restriction_policy_version)),
	CHECK (length(extractor_version) BETWEEN 1 AND 128 AND extractor_version = trim(extractor_version)),
	CHECK (match_outcome IN ('matched', 'no_match', 'ambiguous')),
	CHECK (restriction_outcome IN ('clear', 'restricted', 'unknown')),
	CHECK (extraction_outcome IN ('extracted', 'not_run', 'unsupported', 'invalid')),
	CHECK (length(matching_evidence_json) BETWEEN 2 AND 1048576 AND json_valid(matching_evidence_json) AND json_type(matching_evidence_json) = 'array'),
	CHECK (length(restriction_rule_ids_json) BETWEEN 2 AND 262144 AND json_valid(restriction_rule_ids_json) AND json_type(restriction_rule_ids_json) = 'array'),
	CHECK (length(extracted_lines_json) BETWEEN 2 AND 4194304 AND json_valid(extracted_lines_json) AND json_type(extracted_lines_json) = 'array'),
	CHECK (typeof(extracted_line_count) = 'integer' AND extracted_line_count BETWEEN 0 AND 5000),
	CHECK ((extraction_outcome = 'extracted' AND match_outcome = 'matched' AND restriction_outcome = 'clear' AND extracted_line_count > 0 AND json_array_length(extracted_lines_json) = extracted_line_count AND length(extracted_lines_sha256) = 64 AND extracted_lines_sha256 = lower(extracted_lines_sha256) AND extracted_lines_sha256 NOT GLOB '*[^0-9a-f]*') OR
		(extraction_outcome <> 'extracted' AND extracted_line_count = 0 AND extracted_lines_json = '[]' AND extracted_lines_sha256 = '')),
	CHECK (length(analysis_sha256) = 64 AND analysis_sha256 = lower(analysis_sha256) AND analysis_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (typeof(creating_job_id) = 'integer' AND creating_job_id > 0),
	CHECK (typeof(created_at) = 'integer' AND created_at > 0),
	FOREIGN KEY (artifact_id) REFERENCES lyrics_source_artifacts(artifact_id) ON DELETE RESTRICT
);
CREATE INDEX idx_lyrics_source_analyses_music ON lyrics_source_analyses(music_id, analysis_id);

CREATE TABLE lyrics_source_associations (
	analysis_id          INTEGER NOT NULL,
	music_id             INTEGER NOT NULL,
	catalog_fingerprint  TEXT NOT NULL,
	kind                 TEXT NOT NULL,
	created_at           INTEGER NOT NULL,
	PRIMARY KEY (analysis_id, music_id, kind),
	CHECK (typeof(analysis_id) = 'integer' AND analysis_id > 0),
	CHECK (typeof(music_id) = 'integer' AND music_id > 0),
	CHECK (length(catalog_fingerprint) = 64 AND catalog_fingerprint = lower(catalog_fingerprint) AND catalog_fingerprint NOT GLOB '*[^0-9a-f]*'),
	CHECK (kind IN ('full_target', 'game_size_evidence')),
	CHECK (typeof(created_at) = 'integer' AND created_at > 0),
	FOREIGN KEY (analysis_id) REFERENCES lyrics_source_analyses(analysis_id) ON DELETE RESTRICT
);
CREATE INDEX idx_lyrics_source_associations_music ON lyrics_source_associations(music_id, kind, analysis_id);

CREATE TABLE lyrics_discovery_job_outputs (
	job_id       INTEGER PRIMARY KEY,
	artifact_id  INTEGER NOT NULL,
	analysis_id  INTEGER NOT NULL,
	review_id    INTEGER,
	created_at   INTEGER NOT NULL,
	CHECK (typeof(job_id) = 'integer' AND job_id > 0),
	CHECK (typeof(artifact_id) = 'integer' AND artifact_id > 0),
	CHECK (typeof(analysis_id) = 'integer' AND analysis_id > 0),
	CHECK (review_id IS NULL OR (typeof(review_id) = 'integer' AND review_id > 0)),
	CHECK (typeof(created_at) = 'integer' AND created_at > 0),
	FOREIGN KEY (job_id) REFERENCES lyrics_discovery_jobs(job_id) ON DELETE RESTRICT,
	FOREIGN KEY (artifact_id) REFERENCES lyrics_source_artifacts(artifact_id) ON DELETE RESTRICT,
	FOREIGN KEY (analysis_id) REFERENCES lyrics_source_analyses(analysis_id) ON DELETE RESTRICT
);

CREATE TRIGGER lyrics_source_analyses_immutable_update BEFORE UPDATE ON lyrics_source_analyses BEGIN SELECT RAISE(ABORT, 'lyrics source analyses are immutable'); END;
CREATE TRIGGER lyrics_source_analyses_immutable_delete BEFORE DELETE ON lyrics_source_analyses BEGIN SELECT RAISE(ABORT, 'lyrics source analyses are immutable'); END;
CREATE TRIGGER lyrics_source_associations_immutable_update BEFORE UPDATE ON lyrics_source_associations BEGIN SELECT RAISE(ABORT, 'lyrics source associations are immutable'); END;
CREATE TRIGGER lyrics_source_associations_immutable_delete BEFORE DELETE ON lyrics_source_associations BEGIN SELECT RAISE(ABORT, 'lyrics source associations are immutable'); END;
CREATE TRIGGER lyrics_discovery_job_outputs_immutable_update BEFORE UPDATE ON lyrics_discovery_job_outputs BEGIN SELECT RAISE(ABORT, 'lyrics discovery job outputs are immutable'); END;
CREATE TRIGGER lyrics_discovery_job_outputs_immutable_delete BEFORE DELETE ON lyrics_discovery_job_outputs BEGIN SELECT RAISE(ABORT, 'lyrics discovery job outputs are immutable'); END;
`,
}, {
	version: 15,
	name:    "private_lyrics_source_review_queue",
	sql: `
CREATE TABLE lyrics_source_review_items (
	review_id            INTEGER PRIMARY KEY AUTOINCREMENT,
	domain_key           TEXT NOT NULL UNIQUE,
	kind                 TEXT NOT NULL,
	analysis_id          INTEGER,
	music_id             INTEGER NOT NULL,
	catalog_fingerprint  TEXT NOT NULL,
	review_policy_version TEXT NOT NULL,
	reason_code          TEXT NOT NULL,
	evidence_json        TEXT NOT NULL,
	state                TEXT NOT NULL,
	identity_gate        TEXT NOT NULL,
	source_use_gate      TEXT NOT NULL,
	parse_gate           TEXT NOT NULL,
	version              INTEGER NOT NULL,
	priority             INTEGER NOT NULL,
	created_at           INTEGER NOT NULL,
	updated_at           INTEGER NOT NULL,
	completed_at         INTEGER,
	CHECK (length(domain_key) = 64 AND domain_key = lower(domain_key) AND domain_key NOT GLOB '*[^0-9a-f]*'),
	CHECK (kind IN ('candidate_selection', 'artifact_review')),
	CHECK ((kind = 'candidate_selection' AND analysis_id IS NULL) OR (kind = 'artifact_review' AND typeof(analysis_id) = 'integer' AND analysis_id > 0)),
	CHECK (typeof(music_id) = 'integer' AND music_id > 0),
	CHECK (length(catalog_fingerprint) = 64 AND catalog_fingerprint = lower(catalog_fingerprint) AND catalog_fingerprint NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(review_policy_version) BETWEEN 1 AND 128 AND review_policy_version = trim(review_policy_version)),
	CHECK (length(reason_code) BETWEEN 1 AND 128 AND reason_code = lower(reason_code) AND reason_code NOT GLOB '*[^a-z0-9_]*'),
	CHECK (length(evidence_json) BETWEEN 2 AND 1048576 AND json_valid(evidence_json) AND json_type(evidence_json) = 'object'),
	CHECK (state IN ('pending', 'approved', 'rejected', 'superseded', 'cancelled')),
	CHECK (identity_gate IN ('not_applicable', 'pending', 'approved', 'rejected')),
	CHECK (source_use_gate IN ('not_applicable', 'pending', 'approved', 'rejected')),
	CHECK (parse_gate IN ('not_applicable', 'pending', 'approved', 'rejected')),
	CHECK ((kind = 'candidate_selection' AND identity_gate = 'not_applicable' AND source_use_gate = 'not_applicable' AND parse_gate = 'not_applicable') OR
		(kind = 'artifact_review' AND identity_gate <> 'not_applicable' AND source_use_gate <> 'not_applicable' AND parse_gate <> 'not_applicable')),
	CHECK (typeof(version) = 'integer' AND version > 0),
	CHECK (typeof(priority) = 'integer' AND priority BETWEEN -1000 AND 1000),
	CHECK (typeof(created_at) = 'integer' AND created_at > 0),
	CHECK (typeof(updated_at) = 'integer' AND updated_at >= created_at),
	CHECK ((state IN ('approved', 'rejected', 'superseded', 'cancelled')) = (completed_at IS NOT NULL)),
	CHECK (completed_at IS NULL OR (typeof(completed_at) = 'integer' AND completed_at >= created_at)),
	FOREIGN KEY (analysis_id) REFERENCES lyrics_source_analyses(analysis_id) ON DELETE RESTRICT
);
CREATE INDEX idx_lyrics_source_review_items_queue ON lyrics_source_review_items(state, priority DESC, review_id);
CREATE INDEX idx_lyrics_source_review_items_music ON lyrics_source_review_items(music_id, review_id);

CREATE TABLE lyrics_source_review_decisions (
	decision_id             INTEGER PRIMARY KEY AUTOINCREMENT,
	review_id               INTEGER NOT NULL,
	gate                    TEXT NOT NULL,
	decision                TEXT NOT NULL,
	selected_candidate_json TEXT,
	actor                   TEXT NOT NULL,
	note                    TEXT NOT NULL,
	idempotency_key         TEXT NOT NULL,
	request_sha256          TEXT NOT NULL,
	expected_version        INTEGER NOT NULL,
	result_version          INTEGER NOT NULL,
	decided_at              INTEGER NOT NULL,
	UNIQUE (actor, idempotency_key),
	CHECK (typeof(review_id) = 'integer' AND review_id > 0),
	CHECK (gate IN ('identity', 'source_use', 'parse', 'candidate')),
	CHECK (decision IN ('approved', 'rejected', 'selected', 'excluded')),
	CHECK ((gate = 'candidate' AND decision IN ('selected', 'excluded')) OR (gate <> 'candidate' AND decision IN ('approved', 'rejected'))),
	CHECK ((decision = 'selected' AND selected_candidate_json IS NOT NULL AND length(selected_candidate_json) BETWEEN 2 AND 262144 AND json_valid(selected_candidate_json) AND json_type(selected_candidate_json) = 'object') OR
		(decision <> 'selected' AND selected_candidate_json IS NULL)),
	CHECK (length(actor) BETWEEN 1 AND 128 AND actor = trim(actor)),
	CHECK (length(note) <= 2000),
	CHECK (length(idempotency_key) BETWEEN 16 AND 128 AND idempotency_key = trim(idempotency_key)),
	CHECK (length(request_sha256) = 64 AND request_sha256 = lower(request_sha256) AND request_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (typeof(expected_version) = 'integer' AND expected_version > 0),
	CHECK (typeof(result_version) = 'integer' AND result_version = expected_version + 1),
	CHECK (typeof(decided_at) = 'integer' AND decided_at > 0),
	FOREIGN KEY (review_id) REFERENCES lyrics_source_review_items(review_id) ON DELETE RESTRICT
);
CREATE INDEX idx_lyrics_source_review_decisions_review ON lyrics_source_review_decisions(review_id, decision_id);
CREATE TRIGGER lyrics_source_review_decisions_immutable_update BEFORE UPDATE ON lyrics_source_review_decisions BEGIN SELECT RAISE(ABORT, 'lyrics source review decisions are immutable'); END;
CREATE TRIGGER lyrics_source_review_decisions_immutable_delete BEFORE DELETE ON lyrics_source_review_decisions BEGIN SELECT RAISE(ABORT, 'lyrics source review decisions are immutable'); END;
`,
}, {
	version: 16,
	name:    "overall_lyrics_source_artifact_review_decisions",
	sql: `
ALTER TABLE lyrics_source_review_decisions RENAME TO lyrics_source_review_decisions_v15;

CREATE TABLE lyrics_source_review_decisions (
	decision_id             INTEGER PRIMARY KEY AUTOINCREMENT,
	review_id               INTEGER NOT NULL,
	gate                    TEXT NOT NULL,
	decision                TEXT NOT NULL,
	selected_candidate_json TEXT,
	actor                   TEXT NOT NULL,
	note                    TEXT NOT NULL,
	idempotency_key         TEXT NOT NULL,
	request_sha256          TEXT NOT NULL,
	expected_version        INTEGER NOT NULL,
	result_version          INTEGER NOT NULL,
	decided_at              INTEGER NOT NULL,
	UNIQUE (actor, idempotency_key),
	CHECK (typeof(review_id) = 'integer' AND review_id > 0),
	CHECK (gate IN ('identity', 'source_use', 'parse', 'overall', 'candidate')),
	CHECK (decision IN ('approved', 'rejected', 'selected', 'excluded')),
	CHECK ((gate = 'candidate' AND decision IN ('selected', 'excluded')) OR (gate <> 'candidate' AND decision IN ('approved', 'rejected'))),
	CHECK ((decision = 'selected' AND selected_candidate_json IS NOT NULL AND length(selected_candidate_json) BETWEEN 2 AND 262144 AND json_valid(selected_candidate_json) AND json_type(selected_candidate_json) = 'object') OR
		(decision <> 'selected' AND selected_candidate_json IS NULL)),
	CHECK (length(actor) BETWEEN 1 AND 128 AND actor = trim(actor)),
	CHECK (length(note) <= 2000),
	CHECK (length(idempotency_key) BETWEEN 16 AND 128 AND idempotency_key = trim(idempotency_key)),
	CHECK (length(request_sha256) = 64 AND request_sha256 = lower(request_sha256) AND request_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (typeof(expected_version) = 'integer' AND expected_version > 0),
	CHECK (typeof(result_version) = 'integer' AND result_version = expected_version + 1),
	CHECK (typeof(decided_at) = 'integer' AND decided_at > 0),
	FOREIGN KEY (review_id) REFERENCES lyrics_source_review_items(review_id) ON DELETE RESTRICT
);
INSERT INTO lyrics_source_review_decisions
	(decision_id, review_id, gate, decision, selected_candidate_json, actor, note, idempotency_key,
	 request_sha256, expected_version, result_version, decided_at)
SELECT decision_id, review_id, gate, decision, selected_candidate_json, actor, note, idempotency_key,
	request_sha256, expected_version, result_version, decided_at
FROM lyrics_source_review_decisions_v15;
DROP TABLE lyrics_source_review_decisions_v15;

CREATE INDEX idx_lyrics_source_review_decisions_review ON lyrics_source_review_decisions(review_id, decision_id);
CREATE TRIGGER lyrics_source_review_decisions_immutable_update BEFORE UPDATE ON lyrics_source_review_decisions BEGIN SELECT RAISE(ABORT, 'lyrics source review decisions are immutable'); END;
CREATE TRIGGER lyrics_source_review_decisions_immutable_delete BEFORE DELETE ON lyrics_source_review_decisions BEGIN SELECT RAISE(ABORT, 'lyrics source review decisions are immutable'); END;
`,
}, {
	version: 17,
	name:    "lyrics_source_review_batch_idempotency",
	sql: `
CREATE TABLE lyrics_source_review_batch_idempotency (
	batch_id        INTEGER PRIMARY KEY AUTOINCREMENT,
	actor           TEXT NOT NULL,
	idempotency_key TEXT NOT NULL,
	request_sha256  TEXT NOT NULL,
	gate            TEXT NOT NULL,
	decision        TEXT NOT NULL,
	items_json      TEXT NOT NULL,
	item_count      INTEGER NOT NULL,
	note            TEXT NOT NULL,
	decided_at      INTEGER NOT NULL,
	UNIQUE (actor, idempotency_key),
	CHECK (typeof(batch_id) = 'integer' AND batch_id > 0),
	CHECK (length(actor) BETWEEN 1 AND 128 AND actor = trim(actor)),
	CHECK (length(idempotency_key) BETWEEN 16 AND 128 AND idempotency_key = trim(idempotency_key)),
	CHECK (length(request_sha256) = 64 AND request_sha256 = lower(request_sha256) AND request_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (gate = 'overall'),
	CHECK (decision IN ('approved', 'rejected')),
	CHECK (length(items_json) BETWEEN 2 AND 32768 AND json_valid(items_json) AND json_type(items_json) = 'array'),
	CHECK (typeof(item_count) = 'integer' AND item_count BETWEEN 1 AND 100 AND json_array_length(items_json) = item_count),
	CHECK (length(note) <= 2000),
	CHECK (typeof(decided_at) = 'integer' AND decided_at > 0)
);
CREATE TRIGGER lyrics_source_review_batch_idempotency_validate_insert
BEFORE INSERT ON lyrics_source_review_batch_idempotency
WHEN EXISTS (
	SELECT 1 FROM json_each(NEW.items_json) AS item
	WHERE json_type(item.value) <> 'object'
		OR (SELECT COUNT(*) FROM json_each(item.value)) <> 2
		OR COALESCE(json_type(item.value, '$.reviewId'), '') <> 'integer'
		OR json_extract(item.value, '$.reviewId') <= 0
		OR COALESCE(json_type(item.value, '$.expectedVersion'), '') <> 'integer'
		OR json_extract(item.value, '$.expectedVersion') <= 0
) OR EXISTS (
	SELECT 1
	FROM json_each(NEW.items_json) AS previous
	JOIN json_each(NEW.items_json) AS following
		ON CAST(following.key AS INTEGER) = CAST(previous.key AS INTEGER) + 1
	WHERE json_extract(previous.value, '$.reviewId') >= json_extract(following.value, '$.reviewId')
)
BEGIN
	SELECT RAISE(ABORT, 'invalid lyrics source review batch items');
END;
CREATE TRIGGER lyrics_source_review_batch_idempotency_immutable_update
BEFORE UPDATE ON lyrics_source_review_batch_idempotency BEGIN
	SELECT RAISE(ABORT, 'lyrics source review batch idempotency is immutable');
END;
CREATE TRIGGER lyrics_source_review_batch_idempotency_immutable_delete
BEFORE DELETE ON lyrics_source_review_batch_idempotency BEGIN
	SELECT RAISE(ABORT, 'lyrics source review batch idempotency is immutable');
END;
`,
}, {
	version: 18,
	name:    "structured_lyrics_source_analysis_evidence",
	sql: `
ALTER TABLE lyrics_source_analyses ADD COLUMN selected_version_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE lyrics_source_analyses ADD COLUMN performers_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE lyrics_source_analyses ADD COLUMN ruby_generator_version TEXT NOT NULL DEFAULT '';

CREATE TRIGGER lyrics_source_analyses_structured_v2_insert
BEFORE INSERT ON lyrics_source_analyses
WHEN NEW.extractor_version = 'wiki-lyrics-v2-sekai-ruby-colors' AND (
	length(NEW.selected_version_json) < 2 OR json_valid(NEW.selected_version_json) = 0 OR json_type(NEW.selected_version_json) <> 'object' OR
	COALESCE(json_extract(NEW.selected_version_json, '$.kind'), '') NOT IN ('sekai', 'vocaloid', 'original') OR
	COALESCE(json_type(NEW.selected_version_json, '$.label'), '') <> 'text' OR trim(json_extract(NEW.selected_version_json, '$.label')) = '' OR
	length(NEW.performers_json) < 2 OR json_valid(NEW.performers_json) = 0 OR json_type(NEW.performers_json) <> 'array' OR
	trim(NEW.ruby_generator_version) = ''
)
BEGIN SELECT RAISE(ABORT, 'invalid structured lyrics source analysis evidence'); END;
`,
}, {
	version: 19,
	name:    "editable_song_lyrics_ruby",
	sql: `
ALTER TABLE song_lyric_segments ADD COLUMN ruby_json TEXT NOT NULL DEFAULT '[]';

-- A legacy row with only empty segments cannot satisfy the editable ruby
-- invariant. Preserve one deterministic row when the parent line has Japanese
-- text, then discard every remaining empty segment. Empty segments mixed with
-- real text are no-op artifacts and are removed without changing concatenation.
UPDATE song_lyric_segments AS segment
SET text = (
	SELECT line.japanese FROM song_lyric_lines AS line
	WHERE line.music_id=segment.music_id AND line.line_id=segment.line_id
)
WHERE segment.text=''
  AND segment.position=(
	SELECT MIN(candidate.position) FROM song_lyric_segments AS candidate
	WHERE candidate.music_id=segment.music_id AND candidate.line_id=segment.line_id
  )
  AND COALESCE((
	SELECT line.japanese FROM song_lyric_lines AS line
	WHERE line.music_id=segment.music_id AND line.line_id=segment.line_id
  ), '')<>''
  AND NOT EXISTS (
	SELECT 1 FROM song_lyric_segments AS populated
	WHERE populated.music_id=segment.music_id AND populated.line_id=segment.line_id AND populated.text<>''
  );
DELETE FROM song_lyric_segments WHERE text='';
UPDATE song_lyric_segments
SET ruby_json = json_array(json_object('text', text));

CREATE TRIGGER song_lyric_segments_ruby_insert
BEFORE INSERT ON song_lyric_segments
WHEN NEW.text='' OR (NEW.ruby_json<>'[]' AND CASE
	WHEN json_valid(NEW.ruby_json)=0 THEN 1
	WHEN json_type(NEW.ruby_json)<>'array' OR json_array_length(NEW.ruby_json)=0 THEN 1
	WHEN EXISTS (
		SELECT 1 FROM json_each(NEW.ruby_json) AS span
		WHERE json_type(span.value)<>'object'
		   OR COALESCE(json_type(span.value, '$.text'), '')<>'text'
		   OR json_extract(span.value, '$.text')=''
		   OR (json_type(span.value, '$.reading') IS NOT NULL AND json_type(span.value, '$.reading')<>'text')
	) THEN 1
	WHEN COALESCE((
		SELECT group_concat(ordered.text_value, '') FROM (
			SELECT json_extract(span.value, '$.text') AS text_value
			FROM json_each(NEW.ruby_json) AS span ORDER BY CAST(span.key AS INTEGER)
		) AS ordered
	), '')<>NEW.text THEN 1
	ELSE 0
END)
BEGIN SELECT RAISE(ABORT, 'invalid song lyric ruby'); END;

-- The v18 writer does not know ruby_json and therefore receives its declared
-- [] default. Normalize that one legacy insert shape before the statement
-- completes; all explicit nonempty arrays still pass the exact-span guard.
CREATE TRIGGER song_lyric_segments_ruby_legacy_insert
AFTER INSERT ON song_lyric_segments
WHEN NEW.text<>'' AND NEW.ruby_json='[]'
BEGIN
	UPDATE song_lyric_segments
	SET ruby_json=json_array(json_object('text', NEW.text))
	WHERE music_id=NEW.music_id AND line_id=NEW.line_id AND position=NEW.position;
END;

CREATE TRIGGER song_lyric_segments_ruby_update
BEFORE UPDATE OF text, ruby_json ON song_lyric_segments
WHEN NEW.text='' OR CASE
	WHEN json_valid(NEW.ruby_json)=0 THEN 1
	WHEN json_type(NEW.ruby_json)<>'array' OR json_array_length(NEW.ruby_json)=0 THEN 1
	WHEN EXISTS (
		SELECT 1 FROM json_each(NEW.ruby_json) AS span
		WHERE json_type(span.value)<>'object'
		   OR COALESCE(json_type(span.value, '$.text'), '')<>'text'
		   OR json_extract(span.value, '$.text')=''
		   OR (json_type(span.value, '$.reading') IS NOT NULL AND json_type(span.value, '$.reading')<>'text')
	) THEN 1
	WHEN COALESCE((
		SELECT group_concat(ordered.text_value, '') FROM (
			SELECT json_extract(span.value, '$.text') AS text_value
			FROM json_each(NEW.ruby_json) AS span ORDER BY CAST(span.key AS INTEGER)
		) AS ordered
	), '')<>NEW.text THEN 1
	ELSE 0
END
BEGIN SELECT RAISE(ABORT, 'invalid song lyric ruby'); END;
`,
}, {
	version: 20,
	name:    "versioned_lyrics_discovery_fixed_candidate_identity",
	sql: `
ALTER TABLE lyrics_discovery_jobs ADD COLUMN fixed_candidate_json TEXT NOT NULL DEFAULT '';

-- Reconcile every legacy fetch job from immutable artifact output, an admin
-- selection decision, or discovery evidence. Any conflicting metadata for the
-- same fixed page/revision/SHA identity aborts the migration.
CREATE TEMP TABLE lyrics_discovery_v20_fixed_candidates (
	job_id         INTEGER PRIMARY KEY,
	candidate_json TEXT NOT NULL
);
CREATE TEMP TRIGGER lyrics_discovery_v20_fixed_candidate_conflict
BEFORE UPDATE ON lyrics_discovery_v20_fixed_candidates
WHEN OLD.candidate_json <> NEW.candidate_json
BEGIN SELECT RAISE(ABORT, 'conflicting legacy fixed candidate identity'); END;

INSERT INTO lyrics_discovery_v20_fixed_candidates(job_id, candidate_json)
SELECT j.job_id, json_object(
	'pageId', json_extract(candidate.value, '$.pageId'),
	'revisionId', json_extract(candidate.value, '$.revisionId'),
	'sha1', json_extract(candidate.value, '$.sha1'),
	'title', json_extract(candidate.value, '$.title'),
	'canonicalUrl', json_extract(candidate.value, '$.canonicalUrl'),
	'categories', json(json_extract(candidate.value, '$.categories'))
)
FROM lyrics_discovery_jobs AS j
JOIN lyrics_discovery_shadow_results AS result
  ON result.music_id=j.music_id AND result.catalog_fingerprint=j.catalog_fingerprint
JOIN json_each(json_extract(result.result_json, '$.candidates')) AS candidate
WHERE j.kind='fetch_revision'
  AND json_type(candidate.value)='object'
  AND (SELECT COUNT(*) FROM json_each(candidate.value))=6
  AND NOT EXISTS (SELECT 1 FROM json_each(candidate.value) AS field
                  WHERE field.key NOT IN ('pageId','revisionId','sha1','title','canonicalUrl','categories'))
  AND json_type(candidate.value, '$.pageId')='integer'
  AND json_extract(candidate.value, '$.pageId')=j.page_id
  AND json_type(candidate.value, '$.revisionId')='integer'
  AND json_extract(candidate.value, '$.revisionId')=j.revision_id
  AND json_type(candidate.value, '$.sha1')='text'
  AND json_extract(candidate.value, '$.sha1')=j.expected_sha1
  AND json_type(candidate.value, '$.title')='text'
  AND length(json_extract(candidate.value, '$.title')) BETWEEN 1 AND 2048
  AND json_extract(candidate.value, '$.title')=trim(json_extract(candidate.value, '$.title'))
  AND json_type(candidate.value, '$.canonicalUrl')='text'
  AND length(json_extract(candidate.value, '$.canonicalUrl')) BETWEEN 1 AND 4096
  AND json_extract(candidate.value, '$.canonicalUrl')=trim(json_extract(candidate.value, '$.canonicalUrl'))
  AND json_extract(candidate.value, '$.canonicalUrl') LIKE 'https://vocaloid.fandom.com/wiki/%'
  AND json_extract(candidate.value, '$.canonicalUrl') LIKE '%?oldid=' || j.revision_id
  AND instr(json_extract(candidate.value, '$.canonicalUrl'), '#')=0
  AND json_type(candidate.value, '$.categories')='array'
  AND json_array_length(json_extract(candidate.value, '$.categories')) BETWEEN 0 AND 256
  AND NOT EXISTS (SELECT 1 FROM json_each(json_extract(candidate.value, '$.categories')) AS category
                  WHERE category.type<>'text' OR length(category.value) NOT BETWEEN 1 AND 512 OR category.value<>trim(category.value))
  AND NOT EXISTS (SELECT 1 FROM json_each(json_extract(candidate.value, '$.categories')) AS category
                  JOIN json_each(json_extract(candidate.value, '$.categories')) AS following
                    ON CAST(following.key AS INTEGER)=CAST(category.key AS INTEGER)+1
                  WHERE category.value>=following.value)
ON CONFLICT(job_id) DO UPDATE SET candidate_json=excluded.candidate_json;

INSERT INTO lyrics_discovery_v20_fixed_candidates(job_id, candidate_json)
SELECT j.job_id, json_object(
	'pageId', json_extract(decision.selected_candidate_json, '$.pageId'),
	'revisionId', json_extract(decision.selected_candidate_json, '$.revisionId'),
	'sha1', json_extract(decision.selected_candidate_json, '$.sha1'),
	'title', json_extract(decision.selected_candidate_json, '$.title'),
	'canonicalUrl', json_extract(decision.selected_candidate_json, '$.canonicalUrl'),
	'categories', json(json_extract(decision.selected_candidate_json, '$.categories'))
)
FROM lyrics_discovery_jobs AS j
JOIN lyrics_source_review_items AS review
  ON review.music_id=j.music_id AND review.catalog_fingerprint=j.catalog_fingerprint
JOIN lyrics_source_review_decisions AS decision
  ON decision.review_id=review.review_id AND decision.gate='candidate' AND decision.decision='selected'
WHERE j.kind='fetch_revision'
  AND json_type(decision.selected_candidate_json)='object'
  AND (SELECT COUNT(*) FROM json_each(decision.selected_candidate_json))=6
  AND NOT EXISTS (SELECT 1 FROM json_each(decision.selected_candidate_json) AS field
                  WHERE field.key NOT IN ('pageId','revisionId','sha1','title','canonicalUrl','categories'))
  AND json_type(decision.selected_candidate_json, '$.pageId')='integer'
  AND json_extract(decision.selected_candidate_json, '$.pageId')=j.page_id
  AND json_type(decision.selected_candidate_json, '$.revisionId')='integer'
  AND json_extract(decision.selected_candidate_json, '$.revisionId')=j.revision_id
  AND json_type(decision.selected_candidate_json, '$.sha1')='text'
  AND json_extract(decision.selected_candidate_json, '$.sha1')=j.expected_sha1
  AND json_type(decision.selected_candidate_json, '$.title')='text'
  AND length(json_extract(decision.selected_candidate_json, '$.title')) BETWEEN 1 AND 2048
  AND json_extract(decision.selected_candidate_json, '$.title')=trim(json_extract(decision.selected_candidate_json, '$.title'))
  AND json_type(decision.selected_candidate_json, '$.canonicalUrl')='text'
  AND length(json_extract(decision.selected_candidate_json, '$.canonicalUrl')) BETWEEN 1 AND 4096
  AND json_extract(decision.selected_candidate_json, '$.canonicalUrl')=trim(json_extract(decision.selected_candidate_json, '$.canonicalUrl'))
  AND json_extract(decision.selected_candidate_json, '$.canonicalUrl') LIKE 'https://vocaloid.fandom.com/wiki/%'
  AND json_extract(decision.selected_candidate_json, '$.canonicalUrl') LIKE '%?oldid=' || j.revision_id
  AND instr(json_extract(decision.selected_candidate_json, '$.canonicalUrl'), '#')=0
  AND json_type(decision.selected_candidate_json, '$.categories')='array'
  AND json_array_length(json_extract(decision.selected_candidate_json, '$.categories')) BETWEEN 0 AND 256
  AND NOT EXISTS (SELECT 1 FROM json_each(json_extract(decision.selected_candidate_json, '$.categories')) AS category
                  WHERE category.type<>'text' OR length(category.value) NOT BETWEEN 1 AND 512 OR category.value<>trim(category.value))
  AND NOT EXISTS (SELECT 1 FROM json_each(json_extract(decision.selected_candidate_json, '$.categories')) AS category
                  JOIN json_each(json_extract(decision.selected_candidate_json, '$.categories')) AS following
                    ON CAST(following.key AS INTEGER)=CAST(category.key AS INTEGER)+1
                  WHERE category.value>=following.value)
ON CONFLICT(job_id) DO UPDATE SET candidate_json=excluded.candidate_json;

INSERT INTO lyrics_discovery_v20_fixed_candidates(job_id, candidate_json)
SELECT j.job_id, json_object(
	'pageId', artifact.page_id,
	'revisionId', artifact.revision_id,
	'sha1', artifact.mediawiki_sha1,
	'title', artifact.page_title,
	'canonicalUrl', artifact.canonical_revision_url,
	'categories', json(artifact.categories_json)
)
FROM lyrics_discovery_jobs AS j
JOIN lyrics_discovery_job_outputs AS output ON output.job_id=j.job_id
JOIN lyrics_source_artifacts AS artifact ON artifact.artifact_id=output.artifact_id
WHERE j.kind='fetch_revision'
  AND artifact.page_id=j.page_id AND artifact.revision_id=j.revision_id AND artifact.mediawiki_sha1=j.expected_sha1
  AND length(artifact.page_title) BETWEEN 1 AND 2048 AND artifact.page_title=trim(artifact.page_title)
  AND length(artifact.canonical_revision_url) BETWEEN 1 AND 4096
  AND artifact.canonical_revision_url=trim(artifact.canonical_revision_url)
  AND artifact.canonical_revision_url LIKE 'https://vocaloid.fandom.com/wiki/%'
  AND artifact.canonical_revision_url LIKE '%?oldid=' || j.revision_id
  AND instr(artifact.canonical_revision_url, '#')=0
  AND json_type(artifact.categories_json)='array' AND json_array_length(artifact.categories_json) BETWEEN 0 AND 256
  AND NOT EXISTS (SELECT 1 FROM json_each(artifact.categories_json) AS category
                  WHERE category.type<>'text' OR length(category.value) NOT BETWEEN 1 AND 512 OR category.value<>trim(category.value))
  AND NOT EXISTS (SELECT 1 FROM json_each(artifact.categories_json) AS category
                  JOIN json_each(artifact.categories_json) AS following
                    ON CAST(following.key AS INTEGER)=CAST(category.key AS INTEGER)+1
                  WHERE category.value>=following.value)
ON CONFLICT(job_id) DO UPDATE SET candidate_json=excluded.candidate_json;

UPDATE lyrics_discovery_jobs
SET fixed_candidate_json=json_object(
	'schemaVersion', 1,
	'candidate', json((SELECT candidate_json FROM lyrics_discovery_v20_fixed_candidates AS candidate
	                   WHERE candidate.job_id=lyrics_discovery_jobs.job_id))
)
WHERE kind='fetch_revision'
  AND EXISTS (SELECT 1 FROM lyrics_discovery_v20_fixed_candidates AS candidate WHERE candidate.job_id=lyrics_discovery_jobs.job_id);

CREATE TEMP TRIGGER lyrics_discovery_v20_fixed_candidate_preflight
BEFORE DELETE ON main.lyrics_discovery_jobs
WHEN OLD.kind='fetch_revision' AND OLD.fixed_candidate_json=''
BEGIN SELECT RAISE(ABORT, 'unreconcilable legacy fixed candidate identity'); END;
DELETE FROM main.lyrics_discovery_jobs WHERE kind='fetch_revision' AND fixed_candidate_json='';
DROP TRIGGER temp.lyrics_discovery_v20_fixed_candidate_preflight;
DROP TRIGGER temp.lyrics_discovery_v20_fixed_candidate_conflict;
DROP TABLE temp.lyrics_discovery_v20_fixed_candidates;

CREATE TRIGGER lyrics_discovery_fixed_candidate_validate_insert
BEFORE INSERT ON lyrics_discovery_jobs
WHEN CASE
	WHEN NEW.kind<>'fetch_revision' THEN NEW.fixed_candidate_json<>''
	WHEN NEW.fixed_candidate_json='' OR json_valid(NEW.fixed_candidate_json)=0 THEN 1
	ELSE (
	json_type(NEW.fixed_candidate_json)<>'object' OR
	(SELECT COUNT(*) FROM json_each(NEW.fixed_candidate_json))<>2 OR
	EXISTS (SELECT 1 FROM json_each(NEW.fixed_candidate_json) AS field WHERE field.key NOT IN ('schemaVersion','candidate')) OR
	json_type(NEW.fixed_candidate_json, '$.schemaVersion')<>'integer' OR json_extract(NEW.fixed_candidate_json, '$.schemaVersion')<>1 OR
	json_type(NEW.fixed_candidate_json, '$.candidate')<>'object' OR
	(SELECT COUNT(*) FROM json_each(json_extract(NEW.fixed_candidate_json, '$.candidate')))<>6 OR
	EXISTS (SELECT 1 FROM json_each(json_extract(NEW.fixed_candidate_json, '$.candidate')) AS field
	        WHERE field.key NOT IN ('pageId','revisionId','sha1','title','canonicalUrl','categories')) OR
	json_type(NEW.fixed_candidate_json, '$.candidate.pageId')<>'integer' OR json_extract(NEW.fixed_candidate_json, '$.candidate.pageId')<>NEW.page_id OR
	json_type(NEW.fixed_candidate_json, '$.candidate.revisionId')<>'integer' OR json_extract(NEW.fixed_candidate_json, '$.candidate.revisionId')<>NEW.revision_id OR
	json_type(NEW.fixed_candidate_json, '$.candidate.sha1')<>'text' OR json_extract(NEW.fixed_candidate_json, '$.candidate.sha1')<>NEW.expected_sha1 OR
	json_type(NEW.fixed_candidate_json, '$.candidate.title')<>'text' OR length(json_extract(NEW.fixed_candidate_json, '$.candidate.title')) NOT BETWEEN 1 AND 2048 OR
	json_extract(NEW.fixed_candidate_json, '$.candidate.title')<>trim(json_extract(NEW.fixed_candidate_json, '$.candidate.title')) OR
	json_type(NEW.fixed_candidate_json, '$.candidate.canonicalUrl')<>'text' OR length(json_extract(NEW.fixed_candidate_json, '$.candidate.canonicalUrl')) NOT BETWEEN 1 AND 4096 OR
	json_extract(NEW.fixed_candidate_json, '$.candidate.canonicalUrl')<>trim(json_extract(NEW.fixed_candidate_json, '$.candidate.canonicalUrl')) OR
	json_extract(NEW.fixed_candidate_json, '$.candidate.canonicalUrl') NOT LIKE 'https://vocaloid.fandom.com/wiki/%' OR
	json_extract(NEW.fixed_candidate_json, '$.candidate.canonicalUrl') NOT LIKE '%?oldid=' || NEW.revision_id OR
	instr(json_extract(NEW.fixed_candidate_json, '$.candidate.canonicalUrl'), '#')<>0 OR
	json_type(NEW.fixed_candidate_json, '$.candidate.categories')<>'array' OR
	json_array_length(json_extract(NEW.fixed_candidate_json, '$.candidate.categories')) NOT BETWEEN 0 AND 256 OR
	EXISTS (SELECT 1 FROM json_each(json_extract(NEW.fixed_candidate_json, '$.candidate.categories')) AS category
	        WHERE category.type<>'text' OR length(category.value) NOT BETWEEN 1 AND 512 OR category.value<>trim(category.value)) OR
	EXISTS (SELECT 1 FROM json_each(json_extract(NEW.fixed_candidate_json, '$.candidate.categories')) AS category
	        JOIN json_each(json_extract(NEW.fixed_candidate_json, '$.candidate.categories')) AS following
	          ON CAST(following.key AS INTEGER)=CAST(category.key AS INTEGER)+1
	        WHERE category.value>=following.value)
	)
END
BEGIN SELECT RAISE(ABORT, 'invalid fixed candidate identity'); END;

CREATE TRIGGER lyrics_discovery_fixed_candidate_validate_update
BEFORE UPDATE ON lyrics_discovery_jobs
WHEN CASE
	WHEN NEW.kind<>'fetch_revision' THEN NEW.fixed_candidate_json<>''
	WHEN NEW.fixed_candidate_json='' OR json_valid(NEW.fixed_candidate_json)=0 THEN 1
	ELSE (
	json_type(NEW.fixed_candidate_json)<>'object' OR
	(SELECT COUNT(*) FROM json_each(NEW.fixed_candidate_json))<>2 OR
	EXISTS (SELECT 1 FROM json_each(NEW.fixed_candidate_json) AS field WHERE field.key NOT IN ('schemaVersion','candidate')) OR
	json_type(NEW.fixed_candidate_json, '$.schemaVersion')<>'integer' OR json_extract(NEW.fixed_candidate_json, '$.schemaVersion')<>1 OR
	json_type(NEW.fixed_candidate_json, '$.candidate')<>'object' OR
	(SELECT COUNT(*) FROM json_each(json_extract(NEW.fixed_candidate_json, '$.candidate')))<>6 OR
	EXISTS (SELECT 1 FROM json_each(json_extract(NEW.fixed_candidate_json, '$.candidate')) AS field
	        WHERE field.key NOT IN ('pageId','revisionId','sha1','title','canonicalUrl','categories')) OR
	json_type(NEW.fixed_candidate_json, '$.candidate.pageId')<>'integer' OR json_extract(NEW.fixed_candidate_json, '$.candidate.pageId')<>NEW.page_id OR
	json_type(NEW.fixed_candidate_json, '$.candidate.revisionId')<>'integer' OR json_extract(NEW.fixed_candidate_json, '$.candidate.revisionId')<>NEW.revision_id OR
	json_type(NEW.fixed_candidate_json, '$.candidate.sha1')<>'text' OR json_extract(NEW.fixed_candidate_json, '$.candidate.sha1')<>NEW.expected_sha1 OR
	json_type(NEW.fixed_candidate_json, '$.candidate.title')<>'text' OR length(json_extract(NEW.fixed_candidate_json, '$.candidate.title')) NOT BETWEEN 1 AND 2048 OR
	json_extract(NEW.fixed_candidate_json, '$.candidate.title')<>trim(json_extract(NEW.fixed_candidate_json, '$.candidate.title')) OR
	json_type(NEW.fixed_candidate_json, '$.candidate.canonicalUrl')<>'text' OR length(json_extract(NEW.fixed_candidate_json, '$.candidate.canonicalUrl')) NOT BETWEEN 1 AND 4096 OR
	json_extract(NEW.fixed_candidate_json, '$.candidate.canonicalUrl')<>trim(json_extract(NEW.fixed_candidate_json, '$.candidate.canonicalUrl')) OR
	json_extract(NEW.fixed_candidate_json, '$.candidate.canonicalUrl') NOT LIKE 'https://vocaloid.fandom.com/wiki/%' OR
	json_extract(NEW.fixed_candidate_json, '$.candidate.canonicalUrl') NOT LIKE '%?oldid=' || NEW.revision_id OR
	instr(json_extract(NEW.fixed_candidate_json, '$.candidate.canonicalUrl'), '#')<>0 OR
	json_type(NEW.fixed_candidate_json, '$.candidate.categories')<>'array' OR
	json_array_length(json_extract(NEW.fixed_candidate_json, '$.candidate.categories')) NOT BETWEEN 0 AND 256 OR
	EXISTS (SELECT 1 FROM json_each(json_extract(NEW.fixed_candidate_json, '$.candidate.categories')) AS category
	        WHERE category.type<>'text' OR length(category.value) NOT BETWEEN 1 AND 512 OR category.value<>trim(category.value)) OR
	EXISTS (SELECT 1 FROM json_each(json_extract(NEW.fixed_candidate_json, '$.candidate.categories')) AS category
	        JOIN json_each(json_extract(NEW.fixed_candidate_json, '$.candidate.categories')) AS following
	          ON CAST(following.key AS INTEGER)=CAST(category.key AS INTEGER)+1
	        WHERE category.value>=following.value)
	)
END
BEGIN SELECT RAISE(ABORT, 'invalid fixed candidate identity'); END;

CREATE TRIGGER lyrics_discovery_fixed_target_immutable_update
BEFORE UPDATE OF kind, music_id, page_id, revision_id, artifact_id, catalog_fingerprint, policy_version, expected_sha1, fixed_candidate_json
ON lyrics_discovery_jobs
WHEN OLD.kind IS NOT NEW.kind OR OLD.music_id IS NOT NEW.music_id OR OLD.page_id IS NOT NEW.page_id OR
	OLD.revision_id IS NOT NEW.revision_id OR OLD.artifact_id IS NOT NEW.artifact_id OR
	OLD.catalog_fingerprint IS NOT NEW.catalog_fingerprint OR OLD.policy_version IS NOT NEW.policy_version OR
	OLD.expected_sha1 IS NOT NEW.expected_sha1 OR OLD.fixed_candidate_json IS NOT NEW.fixed_candidate_json
BEGIN SELECT RAISE(ABORT, 'lyrics discovery job target is immutable'); END;
`,
}, {
	version: 21,
	name:    "provider_scoped_lyrics_source_provenance",
	sql: `
-- migration:foreign_keys_off
-- Every pre-v21 source row was produced by the sole legacy Vocaloid Fandom
-- adapter. Fail closed if the database contains bytes whose origin cannot be
-- proven to belong to that provider; no heuristic origin rewrite is allowed.
CREATE TEMP TABLE lyrics_source_v21_legacy_guard (
	invalid_count INTEGER NOT NULL CHECK (invalid_count = 0)
);
INSERT INTO lyrics_source_v21_legacy_guard(invalid_count)
SELECT COUNT(*) FROM lyrics_source_artifacts
WHERE source_type <> 'mediawiki' OR source_origin <> 'https://vocaloid.fandom.com'
   OR canonical_revision_url NOT LIKE 'https://vocaloid.fandom.com/wiki/%'
   OR canonical_revision_url NOT LIKE '%?oldid=' || revision_id
   OR instr(canonical_revision_url, '#') <> 0;
DROP TABLE temp.lyrics_source_v21_legacy_guard;

ALTER TABLE lyrics_discovery_jobs ADD COLUMN provider TEXT NOT NULL DEFAULT 'vocaloid_fandom'
	CHECK (provider IN ('vocaloid_fandom','moegirl'));
ALTER TABLE lyrics_discovery_jobs ADD COLUMN fixed_identity_json TEXT NOT NULL DEFAULT '';
ALTER TABLE lyrics_discovery_jobs ADD COLUMN provenance_status TEXT NOT NULL DEFAULT 'not_applicable'
	CHECK (provenance_status IN ('not_applicable','candidate_complete','complete','rebuild_required'));
UPDATE lyrics_discovery_jobs SET provenance_status='rebuild_required' WHERE kind='fetch_revision';

DROP TRIGGER lyrics_discovery_fixed_candidate_validate_insert;
DROP TRIGGER lyrics_discovery_fixed_candidate_validate_update;
CREATE TRIGGER lyrics_discovery_fixed_candidate_validate_insert
BEFORE INSERT ON lyrics_discovery_jobs
WHEN CASE
	WHEN NEW.kind<>'fetch_revision' THEN NEW.fixed_candidate_json<>'' OR NEW.fixed_identity_json<>'' OR NEW.provenance_status<>'not_applicable'
	WHEN NEW.fixed_candidate_json='' OR json_valid(NEW.fixed_candidate_json)=0 THEN 1
	WHEN json_type(NEW.fixed_candidate_json)<>'object' OR
	     (SELECT COUNT(*) FROM json_each(NEW.fixed_candidate_json))<>2 OR
	     EXISTS (SELECT 1 FROM json_each(NEW.fixed_candidate_json) AS field WHERE field.key NOT IN ('schemaVersion','candidate')) OR
	     json_type(NEW.fixed_candidate_json,'$.schemaVersion')<>'integer' OR json_extract(NEW.fixed_candidate_json,'$.schemaVersion')<>1 OR
	     json_type(NEW.fixed_candidate_json,'$.candidate')<>'object' OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.pageId')<>'integer' OR json_extract(NEW.fixed_candidate_json,'$.candidate.pageId')<>NEW.page_id OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.revisionId')<>'integer' OR json_extract(NEW.fixed_candidate_json,'$.candidate.revisionId')<>NEW.revision_id OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.sha1')<>'text' OR json_extract(NEW.fixed_candidate_json,'$.candidate.sha1')<>NEW.expected_sha1 OR
	     length(json_extract(NEW.fixed_candidate_json,'$.candidate.sha1'))<>40 OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.sha1')<>lower(json_extract(NEW.fixed_candidate_json,'$.candidate.sha1')) OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.sha1') GLOB '*[^0-9a-f]*' OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.title')<>'text' OR
	     length(json_extract(NEW.fixed_candidate_json,'$.candidate.title')) NOT BETWEEN 1 AND 2048 OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.title')<>trim(json_extract(NEW.fixed_candidate_json,'$.candidate.title')) OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.canonicalUrl')<>'text' OR
	     length(json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl')) NOT BETWEEN 1 AND 4096 OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl')<>trim(json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl')) OR
	     instr(json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl'),'#')<>0 OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.categories')<>'array' OR
	     json_array_length(json_extract(NEW.fixed_candidate_json,'$.candidate.categories')) NOT BETWEEN 0 AND 256 OR
	     EXISTS (SELECT 1 FROM json_each(json_extract(NEW.fixed_candidate_json,'$.candidate.categories')) AS category
	             WHERE category.type<>'text' OR length(category.value) NOT BETWEEN 1 AND 512 OR category.value<>trim(category.value)) OR
	     EXISTS (SELECT 1 FROM json_each(json_extract(NEW.fixed_candidate_json,'$.candidate.categories')) AS category
	             JOIN json_each(json_extract(NEW.fixed_candidate_json,'$.candidate.categories')) AS following
	               ON CAST(following.key AS INTEGER)=CAST(category.key AS INTEGER)+1
	             WHERE category.value>=following.value) THEN 1
	WHEN NEW.provenance_status='rebuild_required' THEN
	     NEW.provider<>'vocaloid_fandom' OR NEW.fixed_identity_json<>'' OR
	     (SELECT COUNT(*) FROM json_each(json_extract(NEW.fixed_candidate_json,'$.candidate')))<>6 OR
	     EXISTS (SELECT 1 FROM json_each(json_extract(NEW.fixed_candidate_json,'$.candidate')) AS field
	             WHERE field.key NOT IN ('pageId','revisionId','sha1','title','canonicalUrl','categories')) OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl') NOT LIKE 'https://vocaloid.fandom.com/wiki/%' OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl') NOT LIKE '%?oldid=' || NEW.revision_id OR
	     instr(json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl'),'&')<>0
	WHEN NEW.provenance_status IN ('candidate_complete','complete') THEN
	     (SELECT COUNT(*) FROM json_each(json_extract(NEW.fixed_candidate_json,'$.candidate')))<>12 OR
	     EXISTS (SELECT 1 FROM json_each(json_extract(NEW.fixed_candidate_json,'$.candidate')) AS field
	             WHERE field.key NOT IN ('provider','origin','pageId','revisionId','sha1','title','canonicalUrl','categories',
	                                     'section','renditionKey','versionReason','indexEvidenceRefs')) OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.provider')<>'text' OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.provider')<>NEW.provider OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.origin')<>'text' OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.section')<>'text' OR
	     length(json_extract(NEW.fixed_candidate_json,'$.candidate.section')) NOT BETWEEN 1 AND 512 OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.section')<>trim(json_extract(NEW.fixed_candidate_json,'$.candidate.section')) OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.renditionKey')<>'text' OR
	     length(json_extract(NEW.fixed_candidate_json,'$.candidate.renditionKey')) NOT BETWEEN 1 AND 128 OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.renditionKey')<>lower(json_extract(NEW.fixed_candidate_json,'$.candidate.renditionKey')) OR
	     substr(json_extract(NEW.fixed_candidate_json,'$.candidate.renditionKey'),1,1) NOT GLOB '[a-z0-9]' OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.renditionKey') GLOB '*[^a-z0-9._-]*' OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.versionReason')<>'text' OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.versionReason') NOT IN
	       ('tagged_full_and_game','tagged_game_only_full_from_vocaloid','untagged_uncut_identity',
	        'untagged_game_subset','untagged_full_only','version_conflict') OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.indexEvidenceRefs')<>'array' OR
	     json_array_length(json_extract(NEW.fixed_candidate_json,'$.candidate.indexEvidenceRefs')) NOT BETWEEN 1 AND 64 OR
	     EXISTS (SELECT 1 FROM json_each(json_extract(NEW.fixed_candidate_json,'$.candidate.indexEvidenceRefs')) AS reference
	             WHERE reference.type<>'object' OR
	                   (SELECT COUNT(*) FROM json_each(reference.value))<>2 OR
	                   EXISTS (SELECT 1 FROM json_each(reference.value) AS field WHERE field.key NOT IN ('evidenceId','sha256')) OR
	                   json_type(reference.value,'$.evidenceId')<>'text' OR
	                   length(json_extract(reference.value,'$.evidenceId')) NOT BETWEEN 1 AND 256 OR
	                   substr(json_extract(reference.value,'$.evidenceId'),1,1) NOT GLOB '[A-Za-z0-9]' OR
	                   substr(json_extract(reference.value,'$.evidenceId'),2) GLOB '*[^A-Za-z0-9._:/-]*' OR
	                   json_type(reference.value,'$.sha256')<>'text' OR
	                   length(json_extract(reference.value,'$.sha256'))<>64 OR
	                   json_extract(reference.value,'$.sha256')<>lower(json_extract(reference.value,'$.sha256')) OR
	                   json_extract(reference.value,'$.sha256') GLOB '*[^0-9a-f]*') OR
	     EXISTS (SELECT 1 FROM json_each(json_extract(NEW.fixed_candidate_json,'$.candidate.indexEvidenceRefs')) AS reference
	             JOIN json_each(json_extract(NEW.fixed_candidate_json,'$.candidate.indexEvidenceRefs')) AS duplicate
	               ON CAST(reference.key AS INTEGER)<CAST(duplicate.key AS INTEGER)
	              AND json_extract(reference.value,'$.evidenceId')=json_extract(duplicate.value,'$.evidenceId')) OR
	     (NEW.provider='vocaloid_fandom' AND
	       (json_extract(NEW.fixed_candidate_json,'$.candidate.origin')<>'https://vocaloid.fandom.com' OR
	        json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl') NOT LIKE 'https://vocaloid.fandom.com/wiki/%' OR
	        json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl') NOT LIKE '%?oldid=' || NEW.revision_id OR
	        instr(json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl'),'&')<>0)) OR
	     (NEW.provider='moegirl' AND
	       (json_extract(NEW.fixed_candidate_json,'$.candidate.origin')<>'https://moegirl.icu' OR
	        json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl') NOT LIKE 'https://moegirl.icu/index.php?oldid=' || NEW.revision_id || '&title=%' OR
	        instr(substr(json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl'),
	                     instr(json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl'),'&title=')+7),'&')<>0)) OR
	     (NEW.provenance_status='candidate_complete' AND NEW.fixed_identity_json<>'') OR
	     (NEW.provenance_status='complete' AND CASE
	       WHEN NEW.fixed_identity_json='' OR json_valid(NEW.fixed_identity_json)=0 THEN 1
	       ELSE json_type(NEW.fixed_identity_json)<>'object' OR
	         (SELECT COUNT(*) FROM json_each(NEW.fixed_identity_json))<>12 OR
	         EXISTS (SELECT 1 FROM json_each(NEW.fixed_identity_json) AS field
	                 WHERE field.key NOT IN ('provider','origin','pageId','revisionId','sha1','title','canonicalUrl','fetchedAt',
	                                         'categories','section','renditionKey','indexEvidenceRefs')) OR
	         json_type(NEW.fixed_identity_json,'$.provider')<>'text' OR json_extract(NEW.fixed_identity_json,'$.provider')<>NEW.provider OR
	         json_type(NEW.fixed_identity_json,'$.origin')<>'text' OR
	         json_extract(NEW.fixed_identity_json,'$.origin')<>json_extract(NEW.fixed_candidate_json,'$.candidate.origin') OR
	         json_type(NEW.fixed_identity_json,'$.pageId')<>'integer' OR json_extract(NEW.fixed_identity_json,'$.pageId')<>NEW.page_id OR
	         json_type(NEW.fixed_identity_json,'$.revisionId')<>'integer' OR json_extract(NEW.fixed_identity_json,'$.revisionId')<>NEW.revision_id OR
	         json_type(NEW.fixed_identity_json,'$.sha1')<>'text' OR json_extract(NEW.fixed_identity_json,'$.sha1')<>NEW.expected_sha1 OR
	         json_type(NEW.fixed_identity_json,'$.title')<>'text' OR
	         json_extract(NEW.fixed_identity_json,'$.title')<>json_extract(NEW.fixed_candidate_json,'$.candidate.title') OR
	         json_type(NEW.fixed_identity_json,'$.canonicalUrl')<>'text' OR
	         json_extract(NEW.fixed_identity_json,'$.canonicalUrl')<>json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl') OR
	         json_type(NEW.fixed_identity_json,'$.fetchedAt')<>'text' OR
	         length(json_extract(NEW.fixed_identity_json,'$.fetchedAt')) NOT BETWEEN 20 AND 35 OR
	         json_extract(NEW.fixed_identity_json,'$.fetchedAt')<>trim(json_extract(NEW.fixed_identity_json,'$.fetchedAt')) OR
	         substr(json_extract(NEW.fixed_identity_json,'$.fetchedAt'),-1)<>'Z' OR
	         strftime('%s',json_extract(NEW.fixed_identity_json,'$.fetchedAt')) IS NULL OR
	         CAST(strftime('%s',json_extract(NEW.fixed_identity_json,'$.fetchedAt')) AS INTEGER)<=0 OR
	         json_type(NEW.fixed_identity_json,'$.categories')<>'array' OR
	         json(json_extract(NEW.fixed_identity_json,'$.categories'))<>json(json_extract(NEW.fixed_candidate_json,'$.candidate.categories')) OR
	         json_type(NEW.fixed_identity_json,'$.section')<>'text' OR
	         json_extract(NEW.fixed_identity_json,'$.section')<>json_extract(NEW.fixed_candidate_json,'$.candidate.section') OR
	         json_type(NEW.fixed_identity_json,'$.renditionKey')<>'text' OR
	         json_extract(NEW.fixed_identity_json,'$.renditionKey')<>json_extract(NEW.fixed_candidate_json,'$.candidate.renditionKey') OR
	         json_type(NEW.fixed_identity_json,'$.indexEvidenceRefs')<>'array' OR
	         json(json_extract(NEW.fixed_identity_json,'$.indexEvidenceRefs'))<>
	           json(json_extract(NEW.fixed_candidate_json,'$.candidate.indexEvidenceRefs'))
	       END)
	ELSE 1
END
BEGIN SELECT RAISE(ABORT, 'invalid provider-scoped fixed candidate identity'); END;

CREATE TRIGGER lyrics_discovery_fixed_candidate_validate_update
BEFORE UPDATE ON lyrics_discovery_jobs
WHEN CASE
	WHEN NEW.kind<>'fetch_revision' THEN NEW.fixed_candidate_json<>'' OR NEW.fixed_identity_json<>'' OR NEW.provenance_status<>'not_applicable'
	WHEN NEW.fixed_candidate_json='' OR json_valid(NEW.fixed_candidate_json)=0 THEN 1
	WHEN json_type(NEW.fixed_candidate_json)<>'object' OR
	     (SELECT COUNT(*) FROM json_each(NEW.fixed_candidate_json))<>2 OR
	     EXISTS (SELECT 1 FROM json_each(NEW.fixed_candidate_json) AS field WHERE field.key NOT IN ('schemaVersion','candidate')) OR
	     json_type(NEW.fixed_candidate_json,'$.schemaVersion')<>'integer' OR json_extract(NEW.fixed_candidate_json,'$.schemaVersion')<>1 OR
	     json_type(NEW.fixed_candidate_json,'$.candidate')<>'object' OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.pageId')<>'integer' OR json_extract(NEW.fixed_candidate_json,'$.candidate.pageId')<>NEW.page_id OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.revisionId')<>'integer' OR json_extract(NEW.fixed_candidate_json,'$.candidate.revisionId')<>NEW.revision_id OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.sha1')<>'text' OR json_extract(NEW.fixed_candidate_json,'$.candidate.sha1')<>NEW.expected_sha1 OR
	     length(json_extract(NEW.fixed_candidate_json,'$.candidate.sha1'))<>40 OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.sha1')<>lower(json_extract(NEW.fixed_candidate_json,'$.candidate.sha1')) OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.sha1') GLOB '*[^0-9a-f]*' OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.title')<>'text' OR
	     length(json_extract(NEW.fixed_candidate_json,'$.candidate.title')) NOT BETWEEN 1 AND 2048 OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.title')<>trim(json_extract(NEW.fixed_candidate_json,'$.candidate.title')) OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.canonicalUrl')<>'text' OR
	     length(json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl')) NOT BETWEEN 1 AND 4096 OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl')<>trim(json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl')) OR
	     instr(json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl'),'#')<>0 OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.categories')<>'array' OR
	     json_array_length(json_extract(NEW.fixed_candidate_json,'$.candidate.categories')) NOT BETWEEN 0 AND 256 OR
	     EXISTS (SELECT 1 FROM json_each(json_extract(NEW.fixed_candidate_json,'$.candidate.categories')) AS category
	             WHERE category.type<>'text' OR length(category.value) NOT BETWEEN 1 AND 512 OR category.value<>trim(category.value)) OR
	     EXISTS (SELECT 1 FROM json_each(json_extract(NEW.fixed_candidate_json,'$.candidate.categories')) AS category
	             JOIN json_each(json_extract(NEW.fixed_candidate_json,'$.candidate.categories')) AS following
	               ON CAST(following.key AS INTEGER)=CAST(category.key AS INTEGER)+1
	             WHERE category.value>=following.value) THEN 1
	WHEN NEW.provenance_status='rebuild_required' THEN
	     NEW.provider<>'vocaloid_fandom' OR NEW.fixed_identity_json<>'' OR
	     (SELECT COUNT(*) FROM json_each(json_extract(NEW.fixed_candidate_json,'$.candidate')))<>6 OR
	     EXISTS (SELECT 1 FROM json_each(json_extract(NEW.fixed_candidate_json,'$.candidate')) AS field
	             WHERE field.key NOT IN ('pageId','revisionId','sha1','title','canonicalUrl','categories')) OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl') NOT LIKE 'https://vocaloid.fandom.com/wiki/%' OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl') NOT LIKE '%?oldid=' || NEW.revision_id OR
	     instr(json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl'),'&')<>0
	WHEN NEW.provenance_status IN ('candidate_complete','complete') THEN
	     (SELECT COUNT(*) FROM json_each(json_extract(NEW.fixed_candidate_json,'$.candidate')))<>12 OR
	     EXISTS (SELECT 1 FROM json_each(json_extract(NEW.fixed_candidate_json,'$.candidate')) AS field
	             WHERE field.key NOT IN ('provider','origin','pageId','revisionId','sha1','title','canonicalUrl','categories',
	                                     'section','renditionKey','versionReason','indexEvidenceRefs')) OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.provider')<>'text' OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.provider')<>NEW.provider OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.origin')<>'text' OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.section')<>'text' OR
	     length(json_extract(NEW.fixed_candidate_json,'$.candidate.section')) NOT BETWEEN 1 AND 512 OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.section')<>trim(json_extract(NEW.fixed_candidate_json,'$.candidate.section')) OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.renditionKey')<>'text' OR
	     length(json_extract(NEW.fixed_candidate_json,'$.candidate.renditionKey')) NOT BETWEEN 1 AND 128 OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.renditionKey')<>lower(json_extract(NEW.fixed_candidate_json,'$.candidate.renditionKey')) OR
	     substr(json_extract(NEW.fixed_candidate_json,'$.candidate.renditionKey'),1,1) NOT GLOB '[a-z0-9]' OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.renditionKey') GLOB '*[^a-z0-9._-]*' OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.versionReason')<>'text' OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.versionReason') NOT IN
	       ('tagged_full_and_game','tagged_game_only_full_from_vocaloid','untagged_uncut_identity',
	        'untagged_game_subset','untagged_full_only','version_conflict') OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.indexEvidenceRefs')<>'array' OR
	     json_array_length(json_extract(NEW.fixed_candidate_json,'$.candidate.indexEvidenceRefs')) NOT BETWEEN 1 AND 64 OR
	     EXISTS (SELECT 1 FROM json_each(json_extract(NEW.fixed_candidate_json,'$.candidate.indexEvidenceRefs')) AS reference
	             WHERE reference.type<>'object' OR
	                   (SELECT COUNT(*) FROM json_each(reference.value))<>2 OR
	                   EXISTS (SELECT 1 FROM json_each(reference.value) AS field WHERE field.key NOT IN ('evidenceId','sha256')) OR
	                   json_type(reference.value,'$.evidenceId')<>'text' OR
	                   length(json_extract(reference.value,'$.evidenceId')) NOT BETWEEN 1 AND 256 OR
	                   substr(json_extract(reference.value,'$.evidenceId'),1,1) NOT GLOB '[A-Za-z0-9]' OR
	                   substr(json_extract(reference.value,'$.evidenceId'),2) GLOB '*[^A-Za-z0-9._:/-]*' OR
	                   json_type(reference.value,'$.sha256')<>'text' OR
	                   length(json_extract(reference.value,'$.sha256'))<>64 OR
	                   json_extract(reference.value,'$.sha256')<>lower(json_extract(reference.value,'$.sha256')) OR
	                   json_extract(reference.value,'$.sha256') GLOB '*[^0-9a-f]*') OR
	     EXISTS (SELECT 1 FROM json_each(json_extract(NEW.fixed_candidate_json,'$.candidate.indexEvidenceRefs')) AS reference
	             JOIN json_each(json_extract(NEW.fixed_candidate_json,'$.candidate.indexEvidenceRefs')) AS duplicate
	               ON CAST(reference.key AS INTEGER)<CAST(duplicate.key AS INTEGER)
	              AND json_extract(reference.value,'$.evidenceId')=json_extract(duplicate.value,'$.evidenceId')) OR
	     (NEW.provider='vocaloid_fandom' AND
	       (json_extract(NEW.fixed_candidate_json,'$.candidate.origin')<>'https://vocaloid.fandom.com' OR
	        json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl') NOT LIKE 'https://vocaloid.fandom.com/wiki/%' OR
	        json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl') NOT LIKE '%?oldid=' || NEW.revision_id OR
	        instr(json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl'),'&')<>0)) OR
	     (NEW.provider='moegirl' AND
	       (json_extract(NEW.fixed_candidate_json,'$.candidate.origin')<>'https://moegirl.icu' OR
	        json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl') NOT LIKE 'https://moegirl.icu/index.php?oldid=' || NEW.revision_id || '&title=%' OR
	        instr(substr(json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl'),
	                     instr(json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl'),'&title=')+7),'&')<>0)) OR
	     (NEW.provenance_status='candidate_complete' AND NEW.fixed_identity_json<>'') OR
	     (NEW.provenance_status='complete' AND CASE
	       WHEN NEW.fixed_identity_json='' OR json_valid(NEW.fixed_identity_json)=0 THEN 1
	       ELSE json_type(NEW.fixed_identity_json)<>'object' OR
	         (SELECT COUNT(*) FROM json_each(NEW.fixed_identity_json))<>12 OR
	         EXISTS (SELECT 1 FROM json_each(NEW.fixed_identity_json) AS field
	                 WHERE field.key NOT IN ('provider','origin','pageId','revisionId','sha1','title','canonicalUrl','fetchedAt',
	                                         'categories','section','renditionKey','indexEvidenceRefs')) OR
	         json_type(NEW.fixed_identity_json,'$.provider')<>'text' OR json_extract(NEW.fixed_identity_json,'$.provider')<>NEW.provider OR
	         json_type(NEW.fixed_identity_json,'$.origin')<>'text' OR
	         json_extract(NEW.fixed_identity_json,'$.origin')<>json_extract(NEW.fixed_candidate_json,'$.candidate.origin') OR
	         json_type(NEW.fixed_identity_json,'$.pageId')<>'integer' OR json_extract(NEW.fixed_identity_json,'$.pageId')<>NEW.page_id OR
	         json_type(NEW.fixed_identity_json,'$.revisionId')<>'integer' OR json_extract(NEW.fixed_identity_json,'$.revisionId')<>NEW.revision_id OR
	         json_type(NEW.fixed_identity_json,'$.sha1')<>'text' OR json_extract(NEW.fixed_identity_json,'$.sha1')<>NEW.expected_sha1 OR
	         json_type(NEW.fixed_identity_json,'$.title')<>'text' OR
	         json_extract(NEW.fixed_identity_json,'$.title')<>json_extract(NEW.fixed_candidate_json,'$.candidate.title') OR
	         json_type(NEW.fixed_identity_json,'$.canonicalUrl')<>'text' OR
	         json_extract(NEW.fixed_identity_json,'$.canonicalUrl')<>json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl') OR
	         json_type(NEW.fixed_identity_json,'$.fetchedAt')<>'text' OR
	         length(json_extract(NEW.fixed_identity_json,'$.fetchedAt')) NOT BETWEEN 20 AND 35 OR
	         json_extract(NEW.fixed_identity_json,'$.fetchedAt')<>trim(json_extract(NEW.fixed_identity_json,'$.fetchedAt')) OR
	         substr(json_extract(NEW.fixed_identity_json,'$.fetchedAt'),-1)<>'Z' OR
	         strftime('%s',json_extract(NEW.fixed_identity_json,'$.fetchedAt')) IS NULL OR
	         CAST(strftime('%s',json_extract(NEW.fixed_identity_json,'$.fetchedAt')) AS INTEGER)<=0 OR
	         json_type(NEW.fixed_identity_json,'$.categories')<>'array' OR
	         json(json_extract(NEW.fixed_identity_json,'$.categories'))<>json(json_extract(NEW.fixed_candidate_json,'$.candidate.categories')) OR
	         json_type(NEW.fixed_identity_json,'$.section')<>'text' OR
	         json_extract(NEW.fixed_identity_json,'$.section')<>json_extract(NEW.fixed_candidate_json,'$.candidate.section') OR
	         json_type(NEW.fixed_identity_json,'$.renditionKey')<>'text' OR
	         json_extract(NEW.fixed_identity_json,'$.renditionKey')<>json_extract(NEW.fixed_candidate_json,'$.candidate.renditionKey') OR
	         json_type(NEW.fixed_identity_json,'$.indexEvidenceRefs')<>'array' OR
	         json(json_extract(NEW.fixed_identity_json,'$.indexEvidenceRefs'))<>
	           json(json_extract(NEW.fixed_candidate_json,'$.candidate.indexEvidenceRefs'))
	       END)
	ELSE 1
END
BEGIN SELECT RAISE(ABORT, 'invalid provider-scoped fixed candidate identity'); END;

ALTER TABLE lyrics_discovery_shadow_results ADD COLUMN provider TEXT NOT NULL DEFAULT 'vocaloid_fandom'
	CHECK (provider IN ('vocaloid_fandom','moegirl'));
ALTER TABLE lyrics_source_artifacts ADD COLUMN provider TEXT NOT NULL DEFAULT 'vocaloid_fandom'
	CHECK (provider IN ('vocaloid_fandom','moegirl'));
ALTER TABLE lyrics_source_artifacts ADD COLUMN provenance_status TEXT NOT NULL DEFAULT 'rebuild_required'
	CHECK (provenance_status IN ('complete','rebuild_required'));

-- v13 intentionally admitted only the sole legacy Fandom origin. Rebuild the
-- private artifact table after the strict legacy guard so fresh provider-owned
-- artifacts can coexist without rewriting any old bytes. legacy_alter_table
-- keeps child foreign keys pointed at the canonical table name during rebuild.
PRAGMA legacy_alter_table=ON;
ALTER TABLE lyrics_source_artifacts RENAME TO lyrics_source_artifacts_v20;
CREATE TABLE lyrics_source_artifacts (
	artifact_id             INTEGER PRIMARY KEY AUTOINCREMENT,
	source_type             TEXT NOT NULL,
	source_origin           TEXT NOT NULL,
	page_id                 INTEGER NOT NULL,
	revision_id             INTEGER NOT NULL,
	page_title              TEXT NOT NULL,
	canonical_revision_url  TEXT NOT NULL,
	mediawiki_sha1          TEXT NOT NULL,
	categories_json         TEXT NOT NULL,
	raw_wikitext            BLOB NOT NULL,
	raw_byte_count          INTEGER NOT NULL,
	raw_wikitext_sha256     TEXT NOT NULL,
	artifact_sha256         TEXT NOT NULL,
	first_fetched_at        INTEGER NOT NULL,
	first_creating_job_id   INTEGER NOT NULL,
	created_at              INTEGER NOT NULL,
	provider                TEXT NOT NULL,
	provenance_status       TEXT NOT NULL,
	UNIQUE (source_type, source_origin, page_id, revision_id),
	CHECK (source_type='mediawiki'),
	CHECK ((provider='vocaloid_fandom' AND source_origin='https://vocaloid.fandom.com') OR
	       (provider='moegirl' AND source_origin='https://moegirl.icu')),
	CHECK (provenance_status IN ('complete','rebuild_required')),
	CHECK (typeof(page_id)='integer' AND page_id>0),
	CHECK (typeof(revision_id)='integer' AND revision_id>0),
	CHECK (length(page_title) BETWEEN 1 AND 2048 AND page_title=trim(page_title)),
	CHECK (length(canonical_revision_url) BETWEEN 1 AND 4096 AND canonical_revision_url=trim(canonical_revision_url)),
	CHECK (length(mediawiki_sha1)=40 AND mediawiki_sha1=lower(mediawiki_sha1) AND mediawiki_sha1 NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(categories_json) BETWEEN 2 AND 262144 AND json_valid(categories_json) AND json_type(categories_json)='array'),
	CHECK (typeof(raw_wikitext)='blob'),
	CHECK (typeof(raw_byte_count)='integer' AND raw_byte_count BETWEEN 1 AND 2097152 AND raw_byte_count=length(raw_wikitext)),
	CHECK (length(raw_wikitext_sha256)=64 AND raw_wikitext_sha256=lower(raw_wikitext_sha256) AND raw_wikitext_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(artifact_sha256)=64 AND artifact_sha256=lower(artifact_sha256) AND artifact_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (typeof(first_fetched_at)='integer' AND first_fetched_at>0),
	CHECK (typeof(first_creating_job_id)='integer' AND first_creating_job_id>0),
	CHECK (typeof(created_at)='integer' AND created_at>0)
);
INSERT INTO lyrics_source_artifacts
	(artifact_id,source_type,source_origin,page_id,revision_id,page_title,canonical_revision_url,mediawiki_sha1,
	 categories_json,raw_wikitext,raw_byte_count,raw_wikitext_sha256,artifact_sha256,first_fetched_at,
	 first_creating_job_id,created_at,provider,provenance_status)
SELECT artifact_id,source_type,source_origin,page_id,revision_id,page_title,canonical_revision_url,mediawiki_sha1,
	categories_json,raw_wikitext,raw_byte_count,raw_wikitext_sha256,artifact_sha256,first_fetched_at,
	first_creating_job_id,created_at,provider,provenance_status
FROM lyrics_source_artifacts_v20 ORDER BY artifact_id;
DROP TABLE lyrics_source_artifacts_v20;
CREATE INDEX idx_lyrics_source_artifacts_revision ON lyrics_source_artifacts(source_origin,revision_id);
CREATE TRIGGER lyrics_source_artifacts_immutable_update BEFORE UPDATE ON lyrics_source_artifacts
BEGIN SELECT RAISE(ABORT, 'lyrics source artifacts are immutable'); END;
CREATE TRIGGER lyrics_source_artifacts_immutable_delete BEFORE DELETE ON lyrics_source_artifacts
BEGIN SELECT RAISE(ABORT, 'lyrics source artifacts are immutable'); END;
PRAGMA legacy_alter_table=OFF;

ALTER TABLE lyrics_source_analyses ADD COLUMN provider TEXT NOT NULL DEFAULT 'vocaloid_fandom'
	CHECK (provider IN ('vocaloid_fandom','moegirl'));
ALTER TABLE lyrics_source_review_items ADD COLUMN provider TEXT NOT NULL DEFAULT 'vocaloid_fandom'
	CHECK (provider IN ('vocaloid_fandom','moegirl'));
ALTER TABLE lyrics_source_review_decisions ADD COLUMN provider TEXT NOT NULL DEFAULT 'vocaloid_fandom'
	CHECK (provider IN ('vocaloid_fandom','moegirl'));
ALTER TABLE lyrics_discovery_job_outputs ADD COLUMN provider TEXT NOT NULL DEFAULT 'vocaloid_fandom'
	CHECK (provider IN ('vocaloid_fandom','moegirl'));

CREATE INDEX idx_lyrics_discovery_jobs_provider_queue
	ON lyrics_discovery_jobs(provider,state,kind,next_attempt_at,job_id);
CREATE INDEX idx_lyrics_source_artifacts_provider_identity
	ON lyrics_source_artifacts(provider,page_id,revision_id);
CREATE INDEX idx_lyrics_source_reviews_provider_queue
	ON lyrics_source_review_items(provider,state,priority DESC,review_id);

-- Exact, bounded provider evidence is stored once and referenced by every
-- discovery result, fetch job, review, and final rendition that depends on it.
-- A compact {evidenceId,sha256} reference is never accepted without this row.
CREATE TABLE lyrics_source_index_evidence (
	provider                 TEXT NOT NULL,
	evidence_id              TEXT NOT NULL,
	sha256                   TEXT NOT NULL,
	kind                     TEXT NOT NULL,
	origin                   TEXT NOT NULL,
	page_id                  INTEGER,
	revision_id              INTEGER,
	mediawiki_sha1           TEXT NOT NULL,
	page_title               TEXT NOT NULL,
	canonical_revision_url   TEXT NOT NULL,
	categories_json          TEXT NOT NULL,
	canonical_request_url    TEXT NOT NULL,
	fetched_at               TEXT NOT NULL,
	raw_bytes                BLOB NOT NULL,
	raw_byte_count           INTEGER NOT NULL,
	raw_sha256               TEXT NOT NULL,
	created_at               INTEGER NOT NULL,
	PRIMARY KEY (provider,evidence_id),
	UNIQUE (provider,evidence_id,sha256),
	CHECK (provider IN ('vocaloid_fandom','moegirl')),
	CHECK ((provider='vocaloid_fandom' AND origin='https://vocaloid.fandom.com') OR
	       (provider='moegirl' AND origin='https://moegirl.icu')),
	CHECK (length(evidence_id) BETWEEN 1 AND 256 AND substr(evidence_id,1,1) GLOB '[A-Za-z0-9]' AND
	       substr(evidence_id,2) NOT GLOB '*[^A-Za-z0-9._:/-]*'),
	CHECK (length(sha256)=64 AND sha256=lower(sha256) AND sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (kind IN ('mediawiki_revision','mediawiki_search_response')),
	CHECK (json_valid(categories_json) AND json_type(categories_json)='array'),
	CHECK (length(fetched_at) BETWEEN 20 AND 35 AND fetched_at=trim(fetched_at) AND substr(fetched_at,-1)='Z'),
	CHECK (typeof(raw_bytes)='blob' AND typeof(raw_byte_count)='integer' AND
	       raw_byte_count BETWEEN 1 AND 2097152 AND raw_byte_count=length(raw_bytes)),
	CHECK (length(raw_sha256)=64 AND raw_sha256=sha256 AND raw_sha256=lower(raw_sha256) AND raw_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (typeof(created_at)='integer' AND created_at>0),
	CHECK ((kind='mediawiki_revision' AND typeof(page_id)='integer' AND page_id>0 AND
	        typeof(revision_id)='integer' AND revision_id>0 AND length(mediawiki_sha1)=40 AND
	        mediawiki_sha1=lower(mediawiki_sha1) AND mediawiki_sha1 NOT GLOB '*[^0-9a-f]*' AND
	        length(page_title) BETWEEN 1 AND 2048 AND page_title=trim(page_title) AND
	        length(canonical_revision_url) BETWEEN 1 AND 4096 AND canonical_revision_url=trim(canonical_revision_url) AND
	        canonical_request_url='') OR
	       (kind='mediawiki_search_response' AND provider='vocaloid_fandom' AND page_id IS NULL AND
	        revision_id IS NULL AND mediawiki_sha1='' AND page_title='' AND canonical_revision_url='' AND
	        categories_json='[]' AND length(canonical_request_url) BETWEEN 1 AND 8192 AND
	        canonical_request_url LIKE 'https://vocaloid.fandom.com/api.php?%'))
);
CREATE INDEX idx_lyrics_source_index_evidence_digest
	ON lyrics_source_index_evidence(provider,sha256,evidence_id);
CREATE TRIGGER lyrics_source_index_evidence_immutable_update BEFORE UPDATE ON lyrics_source_index_evidence
BEGIN SELECT RAISE(ABORT, 'lyrics source index evidence is immutable'); END;
CREATE TRIGGER lyrics_source_index_evidence_immutable_delete BEFORE DELETE ON lyrics_source_index_evidence
BEGIN SELECT RAISE(ABORT, 'lyrics source index evidence is immutable'); END;

CREATE TABLE lyrics_discovery_result_index_evidence (
	result_id    INTEGER NOT NULL,
	position     INTEGER NOT NULL,
	provider     TEXT NOT NULL,
	evidence_id  TEXT NOT NULL,
	sha256       TEXT NOT NULL,
	PRIMARY KEY (result_id,position),
	UNIQUE (result_id,provider,evidence_id),
	CHECK (typeof(position)='integer' AND position BETWEEN 0 AND 63),
	FOREIGN KEY (result_id) REFERENCES lyrics_discovery_shadow_results(result_id) ON DELETE CASCADE,
	FOREIGN KEY (provider,evidence_id,sha256) REFERENCES lyrics_source_index_evidence(provider,evidence_id,sha256) ON DELETE RESTRICT
);
CREATE TRIGGER lyrics_discovery_result_index_evidence_immutable_update BEFORE UPDATE ON lyrics_discovery_result_index_evidence
BEGIN SELECT RAISE(ABORT, 'discovery result index evidence is immutable'); END;
CREATE TRIGGER lyrics_discovery_result_index_evidence_immutable_delete BEFORE DELETE ON lyrics_discovery_result_index_evidence
WHEN EXISTS (SELECT 1 FROM lyrics_discovery_shadow_results WHERE result_id=OLD.result_id)
BEGIN SELECT RAISE(ABORT, 'discovery result index evidence is immutable'); END;

CREATE TABLE lyrics_discovery_job_index_evidence (
	job_id       INTEGER NOT NULL,
	position     INTEGER NOT NULL,
	provider     TEXT NOT NULL,
	evidence_id  TEXT NOT NULL,
	sha256       TEXT NOT NULL,
	created_at   INTEGER NOT NULL,
	PRIMARY KEY (job_id,position),
	UNIQUE (job_id,provider,evidence_id),
	CHECK (typeof(position)='integer' AND position BETWEEN 0 AND 63),
	CHECK (typeof(created_at)='integer' AND created_at>0),
	FOREIGN KEY (job_id) REFERENCES lyrics_discovery_jobs(job_id) ON DELETE CASCADE,
	FOREIGN KEY (provider,evidence_id,sha256) REFERENCES lyrics_source_index_evidence(provider,evidence_id,sha256) ON DELETE RESTRICT
);
CREATE TRIGGER lyrics_discovery_job_index_evidence_provider_insert
BEFORE INSERT ON lyrics_discovery_job_index_evidence
WHEN NEW.provider<>(SELECT provider FROM lyrics_discovery_jobs WHERE job_id=NEW.job_id)
BEGIN SELECT RAISE(ABORT, 'discovery job evidence provider mismatch'); END;
CREATE TRIGGER lyrics_discovery_job_index_evidence_immutable_update BEFORE UPDATE ON lyrics_discovery_job_index_evidence
BEGIN SELECT RAISE(ABORT, 'discovery job index evidence is immutable'); END;
CREATE TRIGGER lyrics_discovery_job_index_evidence_immutable_delete BEFORE DELETE ON lyrics_discovery_job_index_evidence
WHEN EXISTS (SELECT 1 FROM lyrics_discovery_jobs WHERE job_id=OLD.job_id)
BEGIN SELECT RAISE(ABORT, 'discovery job index evidence is immutable'); END;
CREATE TRIGGER lyrics_discovery_fetch_evidence_resolution_before_lease
BEFORE UPDATE OF state ON lyrics_discovery_jobs
WHEN NEW.kind='fetch_revision' AND NEW.state='leased' AND (
	NEW.provenance_status='rebuild_required' OR
	NEW.provenance_status NOT IN ('candidate_complete','complete') OR
	(SELECT COUNT(*) FROM lyrics_discovery_job_index_evidence AS link WHERE link.job_id=NEW.job_id)<>
	  json_array_length(json_extract(NEW.fixed_candidate_json,'$.candidate.indexEvidenceRefs')) OR
	EXISTS (
		SELECT 1 FROM json_each(json_extract(NEW.fixed_candidate_json,'$.candidate.indexEvidenceRefs')) AS reference
		LEFT JOIN lyrics_discovery_job_index_evidence AS link
		  ON link.job_id=NEW.job_id AND link.position=CAST(reference.key AS INTEGER)
		 AND link.provider=NEW.provider
		 AND link.evidence_id=json_extract(reference.value,'$.evidenceId')
		 AND link.sha256=json_extract(reference.value,'$.sha256')
		WHERE link.job_id IS NULL
	)
)
BEGIN SELECT RAISE(ABORT, 'fetch job index evidence is unresolved'); END;

CREATE TABLE lyrics_source_review_index_evidence (
	review_id    INTEGER NOT NULL,
	position     INTEGER NOT NULL,
	provider     TEXT NOT NULL,
	evidence_id  TEXT NOT NULL,
	sha256       TEXT NOT NULL,
	PRIMARY KEY (review_id,position),
	UNIQUE (review_id,provider,evidence_id),
	CHECK (typeof(position)='integer' AND position BETWEEN 0 AND 63),
	FOREIGN KEY (review_id) REFERENCES lyrics_source_review_items(review_id) ON DELETE CASCADE,
	FOREIGN KEY (provider,evidence_id,sha256) REFERENCES lyrics_source_index_evidence(provider,evidence_id,sha256) ON DELETE RESTRICT
);
CREATE TRIGGER lyrics_source_review_index_evidence_immutable_update BEFORE UPDATE ON lyrics_source_review_index_evidence
BEGIN SELECT RAISE(ABORT, 'lyrics source review index evidence is immutable'); END;
CREATE TRIGGER lyrics_source_review_index_evidence_immutable_delete BEFORE DELETE ON lyrics_source_review_index_evidence
WHEN EXISTS (SELECT 1 FROM lyrics_source_review_items WHERE review_id=OLD.review_id)
BEGIN SELECT RAISE(ABORT, 'lyrics source review index evidence is immutable'); END;

CREATE TRIGGER lyrics_discovery_provider_identity_immutable
BEFORE UPDATE OF provider,fixed_identity_json,provenance_status ON lyrics_discovery_jobs
WHEN OLD.provider IS NOT NEW.provider OR OLD.fixed_identity_json IS NOT NEW.fixed_identity_json OR
	OLD.provenance_status IS NOT NEW.provenance_status
BEGIN SELECT RAISE(ABORT, 'lyrics discovery provider identity is immutable'); END;
CREATE TRIGGER lyrics_discovery_shadow_provider_immutable
BEFORE UPDATE OF provider ON lyrics_discovery_shadow_results
WHEN OLD.provider IS NOT NEW.provider
BEGIN SELECT RAISE(ABORT, 'lyrics discovery shadow provider is immutable'); END;
CREATE TRIGGER lyrics_source_review_provider_immutable
BEFORE UPDATE OF provider ON lyrics_source_review_items
WHEN OLD.provider IS NOT NEW.provider
BEGIN SELECT RAISE(ABORT, 'lyrics source review provider is immutable'); END;

CREATE TABLE lyrics_source_renditions (
	rendition_id             INTEGER PRIMARY KEY AUTOINCREMENT,
	provider                 TEXT NOT NULL,
	artifact_id              INTEGER NOT NULL,
	origin                   TEXT NOT NULL,
	page_id                  INTEGER NOT NULL,
	revision_id              INTEGER NOT NULL,
	mediawiki_sha1           TEXT NOT NULL,
	page_title               TEXT NOT NULL,
	canonical_revision_url   TEXT NOT NULL,
	fetched_at               TEXT NOT NULL,
	categories_json          TEXT NOT NULL,
	section                  TEXT NOT NULL,
	rendition_key            TEXT NOT NULL,
	index_evidence_refs_json TEXT NOT NULL,
	fixed_identity_json      TEXT NOT NULL,
	fixed_identity_sha256    TEXT NOT NULL,
	created_at               INTEGER NOT NULL,
	UNIQUE (provider, origin, page_id, revision_id, section, rendition_key),
	UNIQUE (fixed_identity_sha256),
	CHECK (provider IN ('vocaloid_fandom','moegirl')),
	CHECK ((provider='vocaloid_fandom' AND origin='https://vocaloid.fandom.com') OR
	       (provider='moegirl' AND origin='https://moegirl.icu')),
	CHECK (typeof(artifact_id)='integer' AND artifact_id>0),
	CHECK (typeof(page_id)='integer' AND page_id>0 AND typeof(revision_id)='integer' AND revision_id>0),
	CHECK (length(mediawiki_sha1)=40 AND mediawiki_sha1=lower(mediawiki_sha1) AND mediawiki_sha1 NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(page_title) BETWEEN 1 AND 2048 AND page_title=trim(page_title)),
	CHECK (length(canonical_revision_url) BETWEEN 1 AND 4096 AND canonical_revision_url=trim(canonical_revision_url)),
	CHECK (length(fetched_at) BETWEEN 20 AND 35 AND fetched_at=trim(fetched_at) AND substr(fetched_at,-1)='Z'),
	CHECK (json_valid(categories_json) AND json_type(categories_json)='array'),
	CHECK (length(section) BETWEEN 1 AND 512 AND section=trim(section)),
	CHECK (length(rendition_key) BETWEEN 1 AND 128 AND rendition_key=lower(rendition_key) AND rendition_key NOT GLOB '*[^a-z0-9._-]*'),
	CHECK (json_valid(index_evidence_refs_json) AND json_type(index_evidence_refs_json)='array' AND json_array_length(index_evidence_refs_json) BETWEEN 1 AND 64),
	CHECK (length(fixed_identity_json) BETWEEN 2 AND 1048576 AND json_valid(fixed_identity_json) AND json_type(fixed_identity_json)='object'),
	CHECK (length(fixed_identity_sha256)=64 AND fixed_identity_sha256=lower(fixed_identity_sha256) AND fixed_identity_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (typeof(created_at)='integer' AND created_at>0),
	FOREIGN KEY (artifact_id) REFERENCES lyrics_source_artifacts(artifact_id) ON DELETE RESTRICT
);
CREATE INDEX idx_lyrics_source_renditions_artifact ON lyrics_source_renditions(artifact_id,rendition_key);
CREATE TRIGGER lyrics_source_renditions_immutable_update BEFORE UPDATE ON lyrics_source_renditions
BEGIN SELECT RAISE(ABORT, 'lyrics source renditions are immutable'); END;
CREATE TRIGGER lyrics_source_renditions_immutable_delete BEFORE DELETE ON lyrics_source_renditions
BEGIN SELECT RAISE(ABORT, 'lyrics source renditions are immutable'); END;

CREATE TABLE lyrics_source_rendition_index_evidence (
	rendition_id INTEGER NOT NULL,
	position     INTEGER NOT NULL,
	provider     TEXT NOT NULL,
	evidence_id  TEXT NOT NULL,
	sha256       TEXT NOT NULL,
	PRIMARY KEY (rendition_id,position),
	UNIQUE (rendition_id,provider,evidence_id),
	CHECK (typeof(position)='integer' AND position BETWEEN 0 AND 63),
	FOREIGN KEY (rendition_id) REFERENCES lyrics_source_renditions(rendition_id) ON DELETE RESTRICT,
	FOREIGN KEY (provider,evidence_id,sha256) REFERENCES lyrics_source_index_evidence(provider,evidence_id,sha256) ON DELETE RESTRICT
);
CREATE TRIGGER lyrics_source_rendition_index_evidence_provider_insert
BEFORE INSERT ON lyrics_source_rendition_index_evidence
WHEN NEW.provider<>(SELECT provider FROM lyrics_source_renditions WHERE rendition_id=NEW.rendition_id)
BEGIN SELECT RAISE(ABORT, 'lyrics source rendition evidence provider mismatch'); END;
CREATE TRIGGER lyrics_source_rendition_index_evidence_immutable_update BEFORE UPDATE ON lyrics_source_rendition_index_evidence
BEGIN SELECT RAISE(ABORT, 'lyrics source rendition index evidence is immutable'); END;
CREATE TRIGGER lyrics_source_rendition_index_evidence_immutable_delete BEFORE DELETE ON lyrics_source_rendition_index_evidence
BEGIN SELECT RAISE(ABORT, 'lyrics source rendition index evidence is immutable'); END;

CREATE TABLE lyrics_source_component_contributions (
	contribution_id    INTEGER PRIMARY KEY AUTOINCREMENT,
	analysis_id        INTEGER NOT NULL,
	component          TEXT NOT NULL,
	rendition_id       INTEGER NOT NULL,
	contribution_sha256 TEXT NOT NULL,
	created_at         INTEGER NOT NULL,
	UNIQUE (analysis_id, component),
	CHECK (component IN ('full_text','performer_segmentation','game_projection','ruby','version_evidence')),
	CHECK (length(contribution_sha256)=64 AND contribution_sha256=lower(contribution_sha256) AND contribution_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (typeof(created_at)='integer' AND created_at>0),
	FOREIGN KEY (analysis_id) REFERENCES lyrics_source_analyses(analysis_id) ON DELETE RESTRICT,
	FOREIGN KEY (rendition_id) REFERENCES lyrics_source_renditions(rendition_id) ON DELETE RESTRICT
);
CREATE TRIGGER lyrics_source_component_contributions_immutable_update BEFORE UPDATE ON lyrics_source_component_contributions
BEGIN SELECT RAISE(ABORT, 'lyrics source component contributions are immutable'); END;
CREATE TRIGGER lyrics_source_component_contributions_immutable_delete BEFORE DELETE ON lyrics_source_component_contributions
BEGIN SELECT RAISE(ABORT, 'lyrics source component contributions are immutable'); END;
`,
}, {
	version: 22,
	name:    "lyrics_source_game_projection_and_ruby_contract",
	sql: `
ALTER TABLE song_lyrics ADD COLUMN source_fetched_at_rfc3339 TEXT NOT NULL DEFAULT '';
UPDATE song_lyrics
SET source_fetched_at_rfc3339=strftime('%Y-%m-%dT%H:%M:%SZ',source_fetched_at,'unixepoch')
WHERE source_fetched_at>0;
CREATE TRIGGER song_lyrics_source_fetched_at_rfc3339_insert
BEFORE INSERT ON song_lyrics
WHEN NEW.source_fetched_at_rfc3339<>'' AND (
	length(NEW.source_fetched_at_rfc3339) NOT BETWEEN 20 AND 35 OR
	NEW.source_fetched_at_rfc3339<>trim(NEW.source_fetched_at_rfc3339) OR substr(NEW.source_fetched_at_rfc3339,-1)<>'Z'
)
BEGIN SELECT RAISE(ABORT, 'invalid exact lyrics source fetched timestamp'); END;
CREATE TRIGGER song_lyrics_source_fetched_at_rfc3339_update
BEFORE UPDATE OF source_fetched_at_rfc3339 ON song_lyrics
WHEN NEW.source_fetched_at_rfc3339<>'' AND (
	length(NEW.source_fetched_at_rfc3339) NOT BETWEEN 20 AND 35 OR
	NEW.source_fetched_at_rfc3339<>trim(NEW.source_fetched_at_rfc3339) OR substr(NEW.source_fetched_at_rfc3339,-1)<>'Z'
)
BEGIN SELECT RAISE(ABORT, 'invalid exact lyrics source fetched timestamp'); END;

CREATE TABLE song_lyrics_source_documents (
	document_id          INTEGER PRIMARY KEY AUTOINCREMENT,
	music_id             INTEGER NOT NULL UNIQUE,
	schema_version       INTEGER NOT NULL,
	reason_code          TEXT NOT NULL,
	document_json        TEXT NOT NULL,
	document_sha256      TEXT NOT NULL UNIQUE,
	manifest_batch_sha256 TEXT NOT NULL,
	created_at           INTEGER NOT NULL,
	CHECK (schema_version=1),
	CHECK (reason_code IN ('tagged_full_and_game','tagged_game_only_full_from_vocaloid','untagged_uncut_identity','untagged_game_subset','untagged_full_only','version_conflict')),
	CHECK (reason_code<>'version_conflict'),
	CHECK (length(document_json) BETWEEN 2 AND 16777216 AND json_valid(document_json) AND json_type(document_json)='object'),
	CHECK (length(document_sha256)=64 AND document_sha256=lower(document_sha256) AND document_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(manifest_batch_sha256)=64 AND manifest_batch_sha256=lower(manifest_batch_sha256) AND manifest_batch_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (typeof(created_at)='integer' AND created_at>0),
	FOREIGN KEY (music_id) REFERENCES song_lyrics(music_id) ON DELETE CASCADE
);
CREATE TRIGGER song_lyrics_source_documents_immutable_update BEFORE UPDATE ON song_lyrics_source_documents
BEGIN SELECT RAISE(ABORT, 'song lyrics source documents are immutable'); END;
CREATE TRIGGER song_lyrics_source_documents_immutable_delete BEFORE DELETE ON song_lyrics_source_documents
WHEN EXISTS (SELECT 1 FROM song_lyrics WHERE music_id=OLD.music_id)
BEGIN SELECT RAISE(ABORT, 'song lyrics source documents are immutable'); END;

CREATE TABLE song_lyrics_source_artifacts (
	document_id             INTEGER NOT NULL,
	provider                TEXT NOT NULL,
	rendition_key           TEXT NOT NULL,
	origin                  TEXT NOT NULL,
	page_id                 INTEGER NOT NULL,
	revision_id             INTEGER NOT NULL,
	mediawiki_sha1          TEXT NOT NULL,
	page_title              TEXT NOT NULL,
	canonical_revision_url  TEXT NOT NULL,
	fetched_at              TEXT NOT NULL,
	categories_json         TEXT NOT NULL,
	section                 TEXT NOT NULL,
	index_evidence_refs_json TEXT NOT NULL,
	fixed_identity_json     TEXT NOT NULL,
	fixed_identity_sha256   TEXT NOT NULL,
	raw_byte_count          INTEGER NOT NULL,
	raw_wikitext_sha256     TEXT NOT NULL,
	artifact_sha256         TEXT NOT NULL,
	PRIMARY KEY (document_id,rendition_key),
	UNIQUE (document_id,fixed_identity_sha256),
	CHECK (provider IN ('vocaloid_fandom','moegirl')),
	CHECK ((provider='vocaloid_fandom' AND origin='https://vocaloid.fandom.com') OR
	       (provider='moegirl' AND origin='https://moegirl.icu')),
	CHECK (length(rendition_key) BETWEEN 1 AND 128 AND rendition_key=lower(rendition_key) AND rendition_key NOT GLOB '*[^a-z0-9._-]*'),
	CHECK (typeof(page_id)='integer' AND page_id>0 AND typeof(revision_id)='integer' AND revision_id>0),
	CHECK (length(mediawiki_sha1)=40 AND mediawiki_sha1=lower(mediawiki_sha1) AND mediawiki_sha1 NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(page_title) BETWEEN 1 AND 2048 AND page_title=trim(page_title)),
	CHECK (length(canonical_revision_url) BETWEEN 1 AND 4096 AND canonical_revision_url=trim(canonical_revision_url)),
	CHECK (length(fetched_at) BETWEEN 20 AND 35 AND fetched_at=trim(fetched_at) AND substr(fetched_at,-1)='Z'),
	CHECK (json_valid(categories_json) AND json_type(categories_json)='array'),
	CHECK (length(section) BETWEEN 1 AND 512 AND section=trim(section)),
	CHECK (json_valid(index_evidence_refs_json) AND json_type(index_evidence_refs_json)='array' AND json_array_length(index_evidence_refs_json) BETWEEN 1 AND 64),
	CHECK (length(fixed_identity_json) BETWEEN 2 AND 1048576 AND json_valid(fixed_identity_json) AND json_type(fixed_identity_json)='object'),
	CHECK (length(fixed_identity_sha256)=64 AND fixed_identity_sha256=lower(fixed_identity_sha256) AND fixed_identity_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (typeof(raw_byte_count)='integer' AND raw_byte_count BETWEEN 1 AND 2097152),
	CHECK (length(raw_wikitext_sha256)=64 AND raw_wikitext_sha256=lower(raw_wikitext_sha256) AND raw_wikitext_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(artifact_sha256)=64 AND artifact_sha256=lower(artifact_sha256) AND artifact_sha256 NOT GLOB '*[^0-9a-f]*'),
	FOREIGN KEY (document_id) REFERENCES song_lyrics_source_documents(document_id) ON DELETE CASCADE
);
CREATE INDEX idx_song_lyrics_source_artifacts_provider
	ON song_lyrics_source_artifacts(provider,page_id,revision_id,rendition_key);
CREATE TRIGGER song_lyrics_source_artifacts_immutable_update BEFORE UPDATE ON song_lyrics_source_artifacts
BEGIN SELECT RAISE(ABORT, 'song lyrics source artifacts are immutable'); END;
CREATE TRIGGER song_lyrics_source_artifacts_immutable_delete BEFORE DELETE ON song_lyrics_source_artifacts
WHEN EXISTS (SELECT 1 FROM song_lyrics_source_documents WHERE document_id=OLD.document_id)
BEGIN SELECT RAISE(ABORT, 'song lyrics source artifacts are immutable'); END;

CREATE TABLE song_lyrics_source_artifact_index_evidence (
	document_id   INTEGER NOT NULL,
	rendition_key TEXT NOT NULL,
	position      INTEGER NOT NULL,
	provider      TEXT NOT NULL,
	evidence_id   TEXT NOT NULL,
	sha256        TEXT NOT NULL,
	PRIMARY KEY (document_id,rendition_key,position),
	UNIQUE (document_id,rendition_key,provider,evidence_id),
	CHECK (typeof(position)='integer' AND position BETWEEN 0 AND 63),
	FOREIGN KEY (document_id,rendition_key) REFERENCES song_lyrics_source_artifacts(document_id,rendition_key) ON DELETE CASCADE,
	FOREIGN KEY (provider,evidence_id,sha256) REFERENCES lyrics_source_index_evidence(provider,evidence_id,sha256) ON DELETE RESTRICT
);
CREATE TRIGGER song_lyrics_source_artifact_index_evidence_provider_insert
BEFORE INSERT ON song_lyrics_source_artifact_index_evidence
WHEN NEW.provider<>(SELECT provider FROM song_lyrics_source_artifacts
                     WHERE document_id=NEW.document_id AND rendition_key=NEW.rendition_key)
BEGIN SELECT RAISE(ABORT, 'song lyrics source artifact evidence provider mismatch'); END;
CREATE TRIGGER song_lyrics_source_artifact_index_evidence_immutable_update BEFORE UPDATE ON song_lyrics_source_artifact_index_evidence
BEGIN SELECT RAISE(ABORT, 'song lyrics source artifact index evidence is immutable'); END;
CREATE TRIGGER song_lyrics_source_artifact_index_evidence_immutable_delete BEFORE DELETE ON song_lyrics_source_artifact_index_evidence
WHEN EXISTS (SELECT 1 FROM song_lyrics_source_artifacts
             WHERE document_id=OLD.document_id AND rendition_key=OLD.rendition_key)
BEGIN SELECT RAISE(ABORT, 'song lyrics source artifact index evidence is immutable'); END;

CREATE TABLE song_lyrics_component_contributions (
	document_id        INTEGER NOT NULL,
	component          TEXT NOT NULL,
	rendition_key      TEXT NOT NULL,
	contribution_sha256 TEXT NOT NULL,
	PRIMARY KEY (document_id,component),
	CHECK (component IN ('full_text','performer_segmentation','game_projection','ruby','version_evidence')),
	CHECK (length(contribution_sha256)=64 AND contribution_sha256=lower(contribution_sha256) AND contribution_sha256 NOT GLOB '*[^0-9a-f]*'),
	FOREIGN KEY (document_id,rendition_key) REFERENCES song_lyrics_source_artifacts(document_id,rendition_key) ON DELETE CASCADE
);
CREATE TRIGGER song_lyrics_component_contributions_immutable_update BEFORE UPDATE ON song_lyrics_component_contributions
BEGIN SELECT RAISE(ABORT, 'song lyrics component contributions are immutable'); END;
CREATE TRIGGER song_lyrics_component_contributions_immutable_delete BEFORE DELETE ON song_lyrics_component_contributions
WHEN EXISTS (SELECT 1 FROM song_lyrics_source_documents WHERE document_id=OLD.document_id)
BEGIN SELECT RAISE(ABORT, 'song lyrics component contributions are immutable'); END;
`,
}, {
	version: 23,
	name:    "sekaipedia_lyrics_source_provenance",
	sql: `
-- migration:foreign_keys_off
-- SQLite cannot relax the closed v21/v22 provider CHECK constraints in place.
-- Rename every provider-bearing durable table with legacy alter semantics so
-- unaffected child foreign keys continue to target the canonical names, then
-- rebuild and copy every row without transforming its stored bytes.
CREATE TEMP TABLE lyrics_source_v23_sequences (
	name TEXT PRIMARY KEY,
	seq  INTEGER NOT NULL
);
INSERT INTO lyrics_source_v23_sequences(name,seq)
SELECT name,seq FROM sqlite_sequence
WHERE name IN ('lyrics_discovery_jobs','lyrics_discovery_shadow_results','lyrics_source_artifacts',
	'lyrics_source_analyses','lyrics_source_review_items','lyrics_source_review_decisions','lyrics_source_renditions');
PRAGMA legacy_alter_table=ON;
ALTER TABLE lyrics_discovery_jobs RENAME TO lyrics_discovery_jobs_v22;
ALTER TABLE lyrics_discovery_shadow_results RENAME TO lyrics_discovery_shadow_results_v22;
ALTER TABLE lyrics_source_artifacts RENAME TO lyrics_source_artifacts_v22;
ALTER TABLE lyrics_source_analyses RENAME TO lyrics_source_analyses_v22;
ALTER TABLE lyrics_source_review_items RENAME TO lyrics_source_review_items_v22;
ALTER TABLE lyrics_source_review_decisions RENAME TO lyrics_source_review_decisions_v22;
ALTER TABLE lyrics_discovery_job_outputs RENAME TO lyrics_discovery_job_outputs_v22;
ALTER TABLE lyrics_source_index_evidence RENAME TO lyrics_source_index_evidence_v22;
ALTER TABLE lyrics_source_renditions RENAME TO lyrics_source_renditions_v22;
ALTER TABLE song_lyrics_source_artifacts RENAME TO song_lyrics_source_artifacts_v22;

CREATE TABLE lyrics_discovery_jobs (
	job_id           INTEGER PRIMARY KEY AUTOINCREMENT,
	idempotency_key  TEXT NOT NULL UNIQUE,
	kind             TEXT NOT NULL,
	state            TEXT NOT NULL,
	music_id         INTEGER NOT NULL,
	page_id          INTEGER,
	revision_id      INTEGER,
	artifact_id      INTEGER,
	attempts         INTEGER NOT NULL DEFAULT 0,
	max_attempts     INTEGER NOT NULL,
	next_attempt_at  INTEGER NOT NULL,
	lease_owner      TEXT,
	lease_expires_at INTEGER,
	last_error_code  TEXT,
	created_at       INTEGER NOT NULL,
	updated_at       INTEGER NOT NULL,
	completed_at     INTEGER,
	version          INTEGER NOT NULL DEFAULT 1,
	catalog_fingerprint TEXT NOT NULL DEFAULT '',
	policy_version      TEXT NOT NULL DEFAULT '',
	expected_sha1       TEXT NOT NULL DEFAULT '',
	fixed_candidate_json TEXT NOT NULL DEFAULT '',
	provider             TEXT NOT NULL DEFAULT 'vocaloid_fandom',
	fixed_identity_json  TEXT NOT NULL DEFAULT '',
	provenance_status    TEXT NOT NULL DEFAULT 'not_applicable',
	CHECK (length(idempotency_key)=64 AND idempotency_key=lower(idempotency_key) AND idempotency_key NOT GLOB '*[^0-9a-f]*'),
	CHECK (kind IN ('discover','fetch_revision','revalidate_pinned','revalidate_head')),
	CHECK (length(kind)<=32),
	CHECK (state IN ('queued','leased','retry_wait','succeeded','dead_letter','cancelled')),
	CHECK (length(state)<=16),
	CHECK (music_id>0),
	CHECK (page_id IS NULL OR page_id>0),
	CHECK (revision_id IS NULL OR revision_id>0),
	CHECK (artifact_id IS NULL OR artifact_id>0),
	CHECK (attempts>=0 AND attempts<=max_attempts),
	CHECK (state<>'leased' OR attempts>0),
	CHECK (state<>'dead_letter' OR attempts=max_attempts),
	CHECK (max_attempts BETWEEN 1 AND 100),
	CHECK (next_attempt_at>=0),
	CHECK ((state='leased' AND lease_owner IS NOT NULL AND lease_expires_at IS NOT NULL) OR
	       (state<>'leased' AND lease_owner IS NULL AND lease_expires_at IS NULL)),
	CHECK (lease_owner IS NULL OR (length(lease_owner) BETWEEN 1 AND 128 AND lease_owner=trim(lease_owner))),
	CHECK (lease_expires_at IS NULL OR lease_expires_at>0),
	CHECK (last_error_code IS NULL OR (length(last_error_code) BETWEEN 1 AND 64 AND
	       last_error_code=lower(last_error_code) AND last_error_code NOT GLOB '*[^a-z0-9_]*')),
	CHECK (created_at>=0 AND updated_at>=created_at),
	CHECK ((state IN ('succeeded','dead_letter','cancelled'))=(completed_at IS NOT NULL)),
	CHECK (completed_at IS NULL OR completed_at>=created_at),
	CHECK (version>0),
	CHECK ((kind='discover' AND page_id IS NULL AND revision_id IS NULL AND artifact_id IS NULL) OR
	       (kind='fetch_revision' AND page_id IS NOT NULL AND revision_id IS NOT NULL) OR
	       (kind='revalidate_pinned' AND page_id IS NOT NULL AND artifact_id IS NOT NULL) OR
	       (kind='revalidate_head' AND page_id IS NOT NULL AND revision_id IS NULL)),
	CHECK (provider IN ('vocaloid_fandom','moegirl','sekaipedia')),
	CHECK (provenance_status IN ('not_applicable','candidate_complete','complete','rebuild_required'))
);

CREATE TABLE lyrics_discovery_shadow_results (
	result_id            INTEGER PRIMARY KEY AUTOINCREMENT,
	job_id               INTEGER NOT NULL UNIQUE,
	music_id             INTEGER NOT NULL,
	catalog_fingerprint  TEXT NOT NULL,
	policy_version       TEXT NOT NULL,
	outcome              TEXT NOT NULL,
	candidate_count      INTEGER NOT NULL,
	result_json          TEXT NOT NULL,
	created_at           INTEGER NOT NULL,
	provider             TEXT NOT NULL DEFAULT 'vocaloid_fandom',
	CHECK (music_id>0),
	CHECK (length(catalog_fingerprint)=64 AND catalog_fingerprint=lower(catalog_fingerprint) AND catalog_fingerprint NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(policy_version) BETWEEN 1 AND 64 AND policy_version=trim(policy_version)),
	CHECK (outcome IN ('candidates_found','no_candidates','ambiguous')),
	CHECK (candidate_count>=0),
	CHECK ((outcome='candidates_found' AND candidate_count=1) OR
	       (outcome='no_candidates' AND candidate_count=0) OR
	       (outcome='ambiguous' AND candidate_count>1)),
	CHECK (length(result_json) BETWEEN 2 AND 1048576 AND json_valid(result_json) AND json_type(result_json)='object'),
	CHECK (created_at>=0),
	CHECK (provider IN ('vocaloid_fandom','moegirl','sekaipedia')),
	FOREIGN KEY (job_id,music_id,catalog_fingerprint,policy_version)
		REFERENCES lyrics_discovery_jobs(job_id,music_id,catalog_fingerprint,policy_version) ON DELETE CASCADE
);

CREATE TABLE lyrics_source_artifacts (
	artifact_id             INTEGER PRIMARY KEY AUTOINCREMENT,
	source_type             TEXT NOT NULL,
	source_origin           TEXT NOT NULL,
	page_id                 INTEGER NOT NULL,
	revision_id             INTEGER NOT NULL,
	page_title              TEXT NOT NULL,
	canonical_revision_url  TEXT NOT NULL,
	mediawiki_sha1          TEXT NOT NULL,
	categories_json         TEXT NOT NULL,
	raw_wikitext            BLOB NOT NULL,
	raw_byte_count          INTEGER NOT NULL,
	raw_wikitext_sha256     TEXT NOT NULL,
	artifact_sha256         TEXT NOT NULL,
	first_fetched_at        INTEGER NOT NULL,
	first_creating_job_id   INTEGER NOT NULL,
	created_at              INTEGER NOT NULL,
	provider                TEXT NOT NULL,
	provenance_status       TEXT NOT NULL,
	UNIQUE (source_type,source_origin,page_id,revision_id),
	CHECK (source_type='mediawiki'),
	CHECK (provider IN ('vocaloid_fandom','moegirl','sekaipedia')),
	CHECK ((provider='vocaloid_fandom' AND source_origin='https://vocaloid.fandom.com') OR
	       (provider='moegirl' AND source_origin='https://moegirl.icu') OR
	       (provider='sekaipedia' AND source_origin='https://www.sekaipedia.org')),
	CHECK (provenance_status IN ('complete','rebuild_required')),
	CHECK (typeof(page_id)='integer' AND page_id>0),
	CHECK (typeof(revision_id)='integer' AND revision_id>0),
	CHECK (length(page_title) BETWEEN 1 AND 2048 AND page_title=trim(page_title)),
	CHECK (length(canonical_revision_url) BETWEEN 1 AND 4096 AND canonical_revision_url=trim(canonical_revision_url)),
	CHECK (provider<>'sekaipedia' OR
	       (instr(canonical_revision_url,'#')=0 AND
	        substr(canonical_revision_url,1,length(source_origin||'/wiki/'))=source_origin||'/wiki/' AND
	        instr(substr(canonical_revision_url,length(source_origin||'/wiki/')+1),'?')>1 AND
	        substr(canonical_revision_url,instr(canonical_revision_url,'?'))='?oldid='||revision_id)),
	CHECK (length(mediawiki_sha1)=40 AND mediawiki_sha1=lower(mediawiki_sha1) AND mediawiki_sha1 NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(categories_json) BETWEEN 2 AND 262144 AND json_valid(categories_json) AND json_type(categories_json)='array'),
	CHECK (typeof(raw_wikitext)='blob'),
	CHECK (typeof(raw_byte_count)='integer' AND raw_byte_count BETWEEN 1 AND 2097152 AND raw_byte_count=length(raw_wikitext)),
	CHECK (length(raw_wikitext_sha256)=64 AND raw_wikitext_sha256=lower(raw_wikitext_sha256) AND raw_wikitext_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(artifact_sha256)=64 AND artifact_sha256=lower(artifact_sha256) AND artifact_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (typeof(first_fetched_at)='integer' AND first_fetched_at>0),
	CHECK (typeof(first_creating_job_id)='integer' AND first_creating_job_id>0),
	CHECK (typeof(created_at)='integer' AND created_at>0)
);

CREATE TABLE lyrics_source_analyses (
	analysis_id                INTEGER PRIMARY KEY AUTOINCREMENT,
	analysis_key               TEXT NOT NULL UNIQUE,
	artifact_id                INTEGER NOT NULL,
	music_id                   INTEGER NOT NULL,
	catalog_fingerprint        TEXT NOT NULL,
	matching_policy_version    TEXT NOT NULL,
	restriction_policy_version TEXT NOT NULL,
	extractor_version          TEXT NOT NULL,
	match_outcome              TEXT NOT NULL,
	restriction_outcome        TEXT NOT NULL,
	extraction_outcome         TEXT NOT NULL,
	matching_evidence_json     TEXT NOT NULL,
	restriction_rule_ids_json  TEXT NOT NULL,
	extracted_lines_json       TEXT NOT NULL,
	extracted_line_count       INTEGER NOT NULL,
	extracted_lines_sha256     TEXT NOT NULL,
	analysis_sha256            TEXT NOT NULL,
	creating_job_id            INTEGER NOT NULL,
	created_at                 INTEGER NOT NULL,
	selected_version_json      TEXT NOT NULL DEFAULT '{}',
	performers_json            TEXT NOT NULL DEFAULT '[]',
	ruby_generator_version     TEXT NOT NULL DEFAULT '',
	provider                   TEXT NOT NULL DEFAULT 'vocaloid_fandom',
	UNIQUE (artifact_id,music_id,catalog_fingerprint,matching_policy_version,restriction_policy_version,extractor_version),
	CHECK (length(analysis_key)=64 AND analysis_key=lower(analysis_key) AND analysis_key NOT GLOB '*[^0-9a-f]*'),
	CHECK (typeof(artifact_id)='integer' AND artifact_id>0),
	CHECK (typeof(music_id)='integer' AND music_id>0),
	CHECK (length(catalog_fingerprint)=64 AND catalog_fingerprint=lower(catalog_fingerprint) AND catalog_fingerprint NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(matching_policy_version) BETWEEN 1 AND 128 AND matching_policy_version=trim(matching_policy_version)),
	CHECK (length(restriction_policy_version) BETWEEN 1 AND 128 AND restriction_policy_version=trim(restriction_policy_version)),
	CHECK (length(extractor_version) BETWEEN 1 AND 128 AND extractor_version=trim(extractor_version)),
	CHECK (match_outcome IN ('matched','no_match','ambiguous')),
	CHECK (restriction_outcome IN ('clear','restricted','unknown')),
	CHECK (extraction_outcome IN ('extracted','not_run','unsupported','invalid')),
	CHECK (length(matching_evidence_json) BETWEEN 2 AND 1048576 AND json_valid(matching_evidence_json) AND json_type(matching_evidence_json)='array'),
	CHECK (length(restriction_rule_ids_json) BETWEEN 2 AND 262144 AND json_valid(restriction_rule_ids_json) AND json_type(restriction_rule_ids_json)='array'),
	CHECK (length(extracted_lines_json) BETWEEN 2 AND 4194304 AND json_valid(extracted_lines_json) AND json_type(extracted_lines_json)='array'),
	CHECK (typeof(extracted_line_count)='integer' AND extracted_line_count BETWEEN 0 AND 5000),
	CHECK ((extraction_outcome='extracted' AND match_outcome='matched' AND restriction_outcome='clear' AND
	        extracted_line_count>0 AND json_array_length(extracted_lines_json)=extracted_line_count AND
	        length(extracted_lines_sha256)=64 AND extracted_lines_sha256=lower(extracted_lines_sha256) AND
	        extracted_lines_sha256 NOT GLOB '*[^0-9a-f]*') OR
	       (extraction_outcome<>'extracted' AND extracted_line_count=0 AND extracted_lines_json='[]' AND extracted_lines_sha256='')),
	CHECK (length(analysis_sha256)=64 AND analysis_sha256=lower(analysis_sha256) AND analysis_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (typeof(creating_job_id)='integer' AND creating_job_id>0),
	CHECK (typeof(created_at)='integer' AND created_at>0),
	CHECK (provider IN ('vocaloid_fandom','moegirl','sekaipedia')),
	FOREIGN KEY (artifact_id) REFERENCES lyrics_source_artifacts(artifact_id) ON DELETE RESTRICT
);

CREATE TABLE lyrics_source_review_items (
	review_id             INTEGER PRIMARY KEY AUTOINCREMENT,
	domain_key            TEXT NOT NULL UNIQUE,
	kind                  TEXT NOT NULL,
	analysis_id           INTEGER,
	music_id              INTEGER NOT NULL,
	catalog_fingerprint   TEXT NOT NULL,
	review_policy_version TEXT NOT NULL,
	reason_code           TEXT NOT NULL,
	evidence_json         TEXT NOT NULL,
	state                 TEXT NOT NULL,
	identity_gate         TEXT NOT NULL,
	source_use_gate       TEXT NOT NULL,
	parse_gate            TEXT NOT NULL,
	version               INTEGER NOT NULL,
	priority              INTEGER NOT NULL,
	created_at            INTEGER NOT NULL,
	updated_at            INTEGER NOT NULL,
	completed_at          INTEGER,
	provider              TEXT NOT NULL DEFAULT 'vocaloid_fandom',
	CHECK (length(domain_key)=64 AND domain_key=lower(domain_key) AND domain_key NOT GLOB '*[^0-9a-f]*'),
	CHECK (kind IN ('candidate_selection','artifact_review')),
	CHECK ((kind='candidate_selection' AND analysis_id IS NULL) OR
	       (kind='artifact_review' AND typeof(analysis_id)='integer' AND analysis_id>0)),
	CHECK (typeof(music_id)='integer' AND music_id>0),
	CHECK (length(catalog_fingerprint)=64 AND catalog_fingerprint=lower(catalog_fingerprint) AND catalog_fingerprint NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(review_policy_version) BETWEEN 1 AND 128 AND review_policy_version=trim(review_policy_version)),
	CHECK (length(reason_code) BETWEEN 1 AND 128 AND reason_code=lower(reason_code) AND reason_code NOT GLOB '*[^a-z0-9_]*'),
	CHECK (length(evidence_json) BETWEEN 2 AND 1048576 AND json_valid(evidence_json) AND json_type(evidence_json)='object'),
	CHECK (state IN ('pending','approved','rejected','superseded','cancelled')),
	CHECK (identity_gate IN ('not_applicable','pending','approved','rejected')),
	CHECK (source_use_gate IN ('not_applicable','pending','approved','rejected')),
	CHECK (parse_gate IN ('not_applicable','pending','approved','rejected')),
	CHECK ((kind='candidate_selection' AND identity_gate='not_applicable' AND source_use_gate='not_applicable' AND parse_gate='not_applicable') OR
	       (kind='artifact_review' AND identity_gate<>'not_applicable' AND source_use_gate<>'not_applicable' AND parse_gate<>'not_applicable')),
	CHECK (typeof(version)='integer' AND version>0),
	CHECK (typeof(priority)='integer' AND priority BETWEEN -1000 AND 1000),
	CHECK (typeof(created_at)='integer' AND created_at>0),
	CHECK (typeof(updated_at)='integer' AND updated_at>=created_at),
	CHECK ((state IN ('approved','rejected','superseded','cancelled'))=(completed_at IS NOT NULL)),
	CHECK (completed_at IS NULL OR (typeof(completed_at)='integer' AND completed_at>=created_at)),
	CHECK (provider IN ('vocaloid_fandom','moegirl','sekaipedia')),
	FOREIGN KEY (analysis_id) REFERENCES lyrics_source_analyses(analysis_id) ON DELETE RESTRICT
);

CREATE TABLE lyrics_source_review_decisions (
	decision_id             INTEGER PRIMARY KEY AUTOINCREMENT,
	review_id               INTEGER NOT NULL,
	gate                    TEXT NOT NULL,
	decision                TEXT NOT NULL,
	selected_candidate_json TEXT,
	actor                   TEXT NOT NULL,
	note                    TEXT NOT NULL,
	idempotency_key         TEXT NOT NULL,
	request_sha256          TEXT NOT NULL,
	expected_version        INTEGER NOT NULL,
	result_version          INTEGER NOT NULL,
	decided_at              INTEGER NOT NULL,
	provider                TEXT NOT NULL DEFAULT 'vocaloid_fandom',
	UNIQUE (actor,idempotency_key),
	CHECK (typeof(review_id)='integer' AND review_id>0),
	CHECK (gate IN ('identity','source_use','parse','overall','candidate')),
	CHECK (decision IN ('approved','rejected','selected','excluded')),
	CHECK ((gate='candidate' AND decision IN ('selected','excluded')) OR
	       (gate<>'candidate' AND decision IN ('approved','rejected'))),
	CHECK ((decision='selected' AND selected_candidate_json IS NOT NULL AND
	        length(selected_candidate_json) BETWEEN 2 AND 262144 AND json_valid(selected_candidate_json) AND
	        json_type(selected_candidate_json)='object') OR
	       (decision<>'selected' AND selected_candidate_json IS NULL)),
	CHECK (length(actor) BETWEEN 1 AND 128 AND actor=trim(actor)),
	CHECK (length(note)<=2000),
	CHECK (length(idempotency_key) BETWEEN 16 AND 128 AND idempotency_key=trim(idempotency_key)),
	CHECK (length(request_sha256)=64 AND request_sha256=lower(request_sha256) AND request_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (typeof(expected_version)='integer' AND expected_version>0),
	CHECK (typeof(result_version)='integer' AND result_version=expected_version+1),
	CHECK (typeof(decided_at)='integer' AND decided_at>0),
	CHECK (provider IN ('vocaloid_fandom','moegirl','sekaipedia')),
	FOREIGN KEY (review_id) REFERENCES lyrics_source_review_items(review_id) ON DELETE RESTRICT
);

CREATE TABLE lyrics_discovery_job_outputs (
	job_id       INTEGER PRIMARY KEY,
	artifact_id  INTEGER NOT NULL,
	analysis_id  INTEGER NOT NULL,
	review_id    INTEGER,
	created_at   INTEGER NOT NULL,
	provider     TEXT NOT NULL DEFAULT 'vocaloid_fandom',
	CHECK (typeof(job_id)='integer' AND job_id>0),
	CHECK (typeof(artifact_id)='integer' AND artifact_id>0),
	CHECK (typeof(analysis_id)='integer' AND analysis_id>0),
	CHECK (review_id IS NULL OR (typeof(review_id)='integer' AND review_id>0)),
	CHECK (typeof(created_at)='integer' AND created_at>0),
	CHECK (provider IN ('vocaloid_fandom','moegirl','sekaipedia')),
	FOREIGN KEY (job_id) REFERENCES lyrics_discovery_jobs(job_id) ON DELETE RESTRICT,
	FOREIGN KEY (artifact_id) REFERENCES lyrics_source_artifacts(artifact_id) ON DELETE RESTRICT,
	FOREIGN KEY (analysis_id) REFERENCES lyrics_source_analyses(analysis_id) ON DELETE RESTRICT
);

CREATE TABLE lyrics_source_index_evidence (
	provider                 TEXT NOT NULL,
	evidence_id              TEXT NOT NULL,
	sha256                   TEXT NOT NULL,
	kind                     TEXT NOT NULL,
	origin                   TEXT NOT NULL,
	page_id                  INTEGER,
	revision_id              INTEGER,
	mediawiki_sha1           TEXT NOT NULL,
	page_title               TEXT NOT NULL,
	canonical_revision_url   TEXT NOT NULL,
	categories_json          TEXT NOT NULL,
	canonical_request_url    TEXT NOT NULL,
	fetched_at               TEXT NOT NULL,
	raw_bytes                BLOB NOT NULL,
	raw_byte_count           INTEGER NOT NULL,
	raw_sha256               TEXT NOT NULL,
	created_at               INTEGER NOT NULL,
	revision_timestamp       TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (provider,evidence_id),
	UNIQUE (provider,evidence_id,sha256),
	CHECK (provider IN ('vocaloid_fandom','moegirl','sekaipedia')),
	CHECK ((provider='vocaloid_fandom' AND origin='https://vocaloid.fandom.com') OR
	       (provider='moegirl' AND origin='https://moegirl.icu') OR
	       (provider='sekaipedia' AND origin='https://www.sekaipedia.org')),
	CHECK (length(evidence_id) BETWEEN 1 AND 256 AND substr(evidence_id,1,1) GLOB '[A-Za-z0-9]' AND
	       substr(evidence_id,2) NOT GLOB '*[^A-Za-z0-9._:/-]*'),
	CHECK (length(sha256)=64 AND sha256=lower(sha256) AND sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (kind IN ('mediawiki_revision','mediawiki_search_response')),
	CHECK (json_valid(categories_json) AND json_type(categories_json)='array'),
	CHECK ((provider<>'sekaipedia' AND length(fetched_at) BETWEEN 20 AND 35 AND
	        fetched_at=trim(fetched_at) AND substr(fetched_at,-1)='Z') OR
	       (provider='sekaipedia' AND length(fetched_at) BETWEEN 20 AND 30 AND
	        fetched_at=trim(fetched_at) AND substr(fetched_at,-1)='Z' AND strftime('%s',fetched_at) IS NOT NULL AND
	        (length(fetched_at)=20 OR
	         (length(fetched_at) BETWEEN 22 AND 30 AND substr(fetched_at,20,1)='.' AND
	          substr(fetched_at,21,length(fetched_at)-21) NOT GLOB '*[^0-9]*' AND substr(fetched_at,-2,1)<>'0')))),
	CHECK (typeof(raw_bytes)='blob' AND typeof(raw_byte_count)='integer' AND
	       raw_byte_count BETWEEN 1 AND 2097152 AND raw_byte_count=length(raw_bytes)),
	CHECK (length(raw_sha256)=64 AND raw_sha256=sha256 AND raw_sha256=lower(raw_sha256) AND raw_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (typeof(created_at)='integer' AND created_at>0),
	CHECK ((kind='mediawiki_revision' AND typeof(page_id)='integer' AND page_id>0 AND
	        typeof(revision_id)='integer' AND revision_id>0 AND length(mediawiki_sha1)=40 AND
	        mediawiki_sha1=lower(mediawiki_sha1) AND mediawiki_sha1 NOT GLOB '*[^0-9a-f]*' AND
	        length(page_title) BETWEEN 1 AND 2048 AND page_title=trim(page_title) AND
	        length(canonical_revision_url) BETWEEN 1 AND 4096 AND canonical_revision_url=trim(canonical_revision_url) AND
	        canonical_request_url='' AND
	        (provider<>'sekaipedia' OR
	         (instr(canonical_revision_url,'#')=0 AND
	          substr(canonical_revision_url,1,length(origin||'/wiki/'))=origin||'/wiki/' AND
	          instr(substr(canonical_revision_url,length(origin||'/wiki/')+1),'?')>1 AND
	          substr(canonical_revision_url,instr(canonical_revision_url,'?'))='?oldid='||revision_id)) AND
	        ((provider='sekaipedia' AND length(revision_timestamp) BETWEEN 20 AND 30 AND
	          revision_timestamp=trim(revision_timestamp) AND substr(revision_timestamp,-1)='Z' AND
	          strftime('%s',revision_timestamp) IS NOT NULL AND julianday(revision_timestamp)<=julianday(fetched_at) AND
	          (length(revision_timestamp)=20 OR
	           (length(revision_timestamp) BETWEEN 22 AND 30 AND substr(revision_timestamp,20,1)='.' AND
	            substr(revision_timestamp,21,length(revision_timestamp)-21) NOT GLOB '*[^0-9]*' AND
	            substr(revision_timestamp,-2,1)<>'0'))) OR
	         (provider<>'sekaipedia' AND revision_timestamp=''))) OR
	       (kind='mediawiki_search_response' AND provider='vocaloid_fandom' AND page_id IS NULL AND
	        revision_id IS NULL AND revision_timestamp='' AND mediawiki_sha1='' AND page_title='' AND
	        canonical_revision_url='' AND categories_json='[]' AND length(canonical_request_url) BETWEEN 1 AND 8192 AND
	        canonical_request_url LIKE 'https://vocaloid.fandom.com/api.php?%'))
);

CREATE TABLE lyrics_source_renditions (
	rendition_id             INTEGER PRIMARY KEY AUTOINCREMENT,
	provider                 TEXT NOT NULL,
	artifact_id              INTEGER NOT NULL,
	origin                   TEXT NOT NULL,
	page_id                  INTEGER NOT NULL,
	revision_id              INTEGER NOT NULL,
	mediawiki_sha1           TEXT NOT NULL,
	page_title               TEXT NOT NULL,
	canonical_revision_url   TEXT NOT NULL,
	fetched_at               TEXT NOT NULL,
	categories_json          TEXT NOT NULL,
	section                  TEXT NOT NULL,
	rendition_key            TEXT NOT NULL,
	index_evidence_refs_json TEXT NOT NULL,
	fixed_identity_json      TEXT NOT NULL,
	fixed_identity_sha256    TEXT NOT NULL,
	created_at               INTEGER NOT NULL,
	UNIQUE (provider,origin,page_id,revision_id,section,rendition_key),
	UNIQUE (fixed_identity_sha256),
	CHECK (provider IN ('vocaloid_fandom','moegirl','sekaipedia')),
	CHECK ((provider='vocaloid_fandom' AND origin='https://vocaloid.fandom.com') OR
	       (provider='moegirl' AND origin='https://moegirl.icu') OR
	       (provider='sekaipedia' AND origin='https://www.sekaipedia.org')),
	CHECK (typeof(artifact_id)='integer' AND artifact_id>0),
	CHECK (typeof(page_id)='integer' AND page_id>0 AND typeof(revision_id)='integer' AND revision_id>0),
	CHECK (length(mediawiki_sha1)=40 AND mediawiki_sha1=lower(mediawiki_sha1) AND mediawiki_sha1 NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(page_title) BETWEEN 1 AND 2048 AND page_title=trim(page_title)),
	CHECK (length(canonical_revision_url) BETWEEN 1 AND 4096 AND canonical_revision_url=trim(canonical_revision_url)),
	CHECK (provider<>'sekaipedia' OR
	       (instr(canonical_revision_url,'#')=0 AND
	        substr(canonical_revision_url,1,length(origin||'/wiki/'))=origin||'/wiki/' AND
	        instr(substr(canonical_revision_url,length(origin||'/wiki/')+1),'?')>1 AND
	        substr(canonical_revision_url,instr(canonical_revision_url,'?'))='?oldid='||revision_id)),
	CHECK (length(fetched_at) BETWEEN 20 AND 35 AND fetched_at=trim(fetched_at) AND substr(fetched_at,-1)='Z'),
	CHECK (json_valid(categories_json) AND json_type(categories_json)='array'),
	CHECK (length(section) BETWEEN 1 AND 512 AND section=trim(section)),
	CHECK (length(rendition_key) BETWEEN 1 AND 128 AND rendition_key=lower(rendition_key) AND rendition_key NOT GLOB '*[^a-z0-9._-]*'),
	CHECK (json_valid(index_evidence_refs_json) AND json_type(index_evidence_refs_json)='array' AND json_array_length(index_evidence_refs_json) BETWEEN 1 AND 64),
	CHECK (length(fixed_identity_json) BETWEEN 2 AND 1048576 AND json_valid(fixed_identity_json) AND json_type(fixed_identity_json)='object'),
	CHECK (length(fixed_identity_sha256)=64 AND fixed_identity_sha256=lower(fixed_identity_sha256) AND fixed_identity_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (typeof(created_at)='integer' AND created_at>0),
	FOREIGN KEY (artifact_id) REFERENCES lyrics_source_artifacts(artifact_id) ON DELETE RESTRICT
);

CREATE TABLE song_lyrics_source_artifacts (
	document_id              INTEGER NOT NULL,
	provider                 TEXT NOT NULL,
	rendition_key            TEXT NOT NULL,
	origin                   TEXT NOT NULL,
	page_id                  INTEGER NOT NULL,
	revision_id              INTEGER NOT NULL,
	revision_timestamp       TEXT NOT NULL DEFAULT '',
	mediawiki_sha1           TEXT NOT NULL,
	page_title               TEXT NOT NULL,
	canonical_revision_url   TEXT NOT NULL,
	fetched_at               TEXT NOT NULL,
	categories_json          TEXT NOT NULL,
	section                  TEXT NOT NULL,
	composition_rendition_key TEXT NOT NULL DEFAULT '',
	version_reason           TEXT NOT NULL DEFAULT '',
	index_evidence_refs_json TEXT NOT NULL,
	fixed_identity_json      TEXT NOT NULL,
	fixed_identity_sha256    TEXT NOT NULL,
	raw_byte_count           INTEGER NOT NULL,
	raw_wikitext_sha256      TEXT NOT NULL,
	artifact_sha256          TEXT NOT NULL,
	PRIMARY KEY (document_id,rendition_key),
	UNIQUE (document_id,fixed_identity_sha256),
	CHECK (provider IN ('vocaloid_fandom','moegirl','sekaipedia')),
	CHECK ((provider='vocaloid_fandom' AND origin='https://vocaloid.fandom.com') OR
	       (provider='moegirl' AND origin='https://moegirl.icu') OR
	       (provider='sekaipedia' AND origin='https://www.sekaipedia.org')),
	CHECK (length(rendition_key) BETWEEN 1 AND 128 AND rendition_key=lower(rendition_key) AND rendition_key NOT GLOB '*[^a-z0-9._-]*'),
	CHECK (typeof(page_id)='integer' AND page_id>0 AND typeof(revision_id)='integer' AND revision_id>0),
	CHECK (length(mediawiki_sha1)=40 AND mediawiki_sha1=lower(mediawiki_sha1) AND mediawiki_sha1 NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(page_title) BETWEEN 1 AND 2048 AND page_title=trim(page_title)),
	CHECK (length(canonical_revision_url) BETWEEN 1 AND 4096 AND canonical_revision_url=trim(canonical_revision_url)),
	CHECK (provider<>'sekaipedia' OR
	       (instr(canonical_revision_url,'#')=0 AND
	        substr(canonical_revision_url,1,length(origin||'/wiki/'))=origin||'/wiki/' AND
	        instr(substr(canonical_revision_url,length(origin||'/wiki/')+1),'?')>1 AND
	        substr(canonical_revision_url,instr(canonical_revision_url,'?'))='?oldid='||revision_id)),
	CHECK (length(fetched_at) BETWEEN 20 AND 35 AND fetched_at=trim(fetched_at) AND substr(fetched_at,-1)='Z'),
	CHECK (json_valid(categories_json) AND json_type(categories_json)='array'),
	CHECK (length(section) BETWEEN 1 AND 512 AND section=trim(section)),
	CHECK ((provider='sekaipedia' AND length(revision_timestamp) BETWEEN 20 AND 30 AND
	        revision_timestamp=trim(revision_timestamp) AND substr(revision_timestamp,-1)='Z' AND
	        strftime('%s',revision_timestamp) IS NOT NULL AND julianday(revision_timestamp)<=julianday(fetched_at) AND
	        (length(revision_timestamp)=20 OR
	         (length(revision_timestamp) BETWEEN 22 AND 30 AND substr(revision_timestamp,20,1)='.' AND
	          substr(revision_timestamp,21,length(revision_timestamp)-21) NOT GLOB '*[^0-9]*' AND
	          substr(revision_timestamp,-2,1)<>'0'))) OR
	       (provider<>'sekaipedia' AND revision_timestamp='')),
	CHECK (composition_rendition_key='' OR
	       (length(composition_rendition_key) BETWEEN 1 AND 128 AND
	        substr(composition_rendition_key,1,1) GLOB '[a-z0-9]' AND
	        composition_rendition_key=lower(composition_rendition_key) AND
	        composition_rendition_key NOT GLOB '*[^a-z0-9._-]*')),
	CHECK (version_reason='' OR version_reason IN
	       ('tagged_full_and_game','tagged_game_only_full_from_vocaloid','untagged_uncut_identity',
	        'untagged_game_subset','untagged_full_only','version_conflict')),
	CHECK (json_valid(index_evidence_refs_json) AND json_type(index_evidence_refs_json)='array' AND json_array_length(index_evidence_refs_json) BETWEEN 1 AND 64),
	CHECK (length(fixed_identity_json) BETWEEN 2 AND 1048576 AND json_valid(fixed_identity_json) AND json_type(fixed_identity_json)='object'),
	CHECK (length(fixed_identity_sha256)=64 AND fixed_identity_sha256=lower(fixed_identity_sha256) AND fixed_identity_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (typeof(raw_byte_count)='integer' AND raw_byte_count BETWEEN 1 AND 2097152),
	CHECK (length(raw_wikitext_sha256)=64 AND raw_wikitext_sha256=lower(raw_wikitext_sha256) AND raw_wikitext_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(artifact_sha256)=64 AND artifact_sha256=lower(artifact_sha256) AND artifact_sha256 NOT GLOB '*[^0-9a-f]*'),
	FOREIGN KEY (document_id) REFERENCES song_lyrics_source_documents(document_id) ON DELETE CASCADE
);

-- Copy parents before children. The new evidence timestamp column is empty for
-- every v22 Fandom/Moegirl row; no pre-v23 byte is rewritten or synthesized.
INSERT INTO lyrics_discovery_jobs
	(job_id,idempotency_key,kind,state,music_id,page_id,revision_id,artifact_id,attempts,max_attempts,next_attempt_at,
	 lease_owner,lease_expires_at,last_error_code,created_at,updated_at,completed_at,version,catalog_fingerprint,
	 policy_version,expected_sha1,fixed_candidate_json,provider,fixed_identity_json,provenance_status)
SELECT job_id,idempotency_key,kind,state,music_id,page_id,revision_id,artifact_id,attempts,max_attempts,next_attempt_at,
	lease_owner,lease_expires_at,last_error_code,created_at,updated_at,completed_at,version,catalog_fingerprint,
	policy_version,expected_sha1,fixed_candidate_json,provider,fixed_identity_json,provenance_status
FROM lyrics_discovery_jobs_v22 ORDER BY job_id;

INSERT INTO lyrics_discovery_shadow_results
	(result_id,job_id,music_id,catalog_fingerprint,policy_version,outcome,candidate_count,result_json,created_at,provider)
SELECT result_id,job_id,music_id,catalog_fingerprint,policy_version,outcome,candidate_count,result_json,created_at,provider
FROM lyrics_discovery_shadow_results_v22 ORDER BY result_id;

INSERT INTO lyrics_source_artifacts
	(artifact_id,source_type,source_origin,page_id,revision_id,page_title,canonical_revision_url,mediawiki_sha1,
	 categories_json,raw_wikitext,raw_byte_count,raw_wikitext_sha256,artifact_sha256,first_fetched_at,
	 first_creating_job_id,created_at,provider,provenance_status)
SELECT artifact_id,source_type,source_origin,page_id,revision_id,page_title,canonical_revision_url,mediawiki_sha1,
	categories_json,raw_wikitext,raw_byte_count,raw_wikitext_sha256,artifact_sha256,first_fetched_at,
	first_creating_job_id,created_at,provider,provenance_status
FROM lyrics_source_artifacts_v22 ORDER BY artifact_id;

INSERT INTO lyrics_source_analyses
	(analysis_id,analysis_key,artifact_id,music_id,catalog_fingerprint,matching_policy_version,
	 restriction_policy_version,extractor_version,match_outcome,restriction_outcome,extraction_outcome,
	 matching_evidence_json,restriction_rule_ids_json,extracted_lines_json,extracted_line_count,
	 extracted_lines_sha256,analysis_sha256,creating_job_id,created_at,selected_version_json,performers_json,
	 ruby_generator_version,provider)
SELECT analysis_id,analysis_key,artifact_id,music_id,catalog_fingerprint,matching_policy_version,
	restriction_policy_version,extractor_version,match_outcome,restriction_outcome,extraction_outcome,
	matching_evidence_json,restriction_rule_ids_json,extracted_lines_json,extracted_line_count,
	extracted_lines_sha256,analysis_sha256,creating_job_id,created_at,selected_version_json,performers_json,
	ruby_generator_version,provider
FROM lyrics_source_analyses_v22 ORDER BY analysis_id;

INSERT INTO lyrics_source_review_items
	(review_id,domain_key,kind,analysis_id,music_id,catalog_fingerprint,review_policy_version,reason_code,
	 evidence_json,state,identity_gate,source_use_gate,parse_gate,version,priority,created_at,updated_at,completed_at,provider)
SELECT review_id,domain_key,kind,analysis_id,music_id,catalog_fingerprint,review_policy_version,reason_code,
	evidence_json,state,identity_gate,source_use_gate,parse_gate,version,priority,created_at,updated_at,completed_at,provider
FROM lyrics_source_review_items_v22 ORDER BY review_id;

INSERT INTO lyrics_source_review_decisions
	(decision_id,review_id,gate,decision,selected_candidate_json,actor,note,idempotency_key,request_sha256,
	 expected_version,result_version,decided_at,provider)
SELECT decision_id,review_id,gate,decision,selected_candidate_json,actor,note,idempotency_key,request_sha256,
	expected_version,result_version,decided_at,provider
FROM lyrics_source_review_decisions_v22 ORDER BY decision_id;

INSERT INTO lyrics_discovery_job_outputs
	(job_id,artifact_id,analysis_id,review_id,created_at,provider)
SELECT job_id,artifact_id,analysis_id,review_id,created_at,provider
FROM lyrics_discovery_job_outputs_v22 ORDER BY job_id;

INSERT INTO lyrics_source_index_evidence
	(provider,evidence_id,sha256,kind,origin,page_id,revision_id,mediawiki_sha1,page_title,
	 canonical_revision_url,categories_json,canonical_request_url,fetched_at,raw_bytes,raw_byte_count,
	 raw_sha256,created_at,revision_timestamp)
SELECT provider,evidence_id,sha256,kind,origin,page_id,revision_id,mediawiki_sha1,page_title,
	canonical_revision_url,categories_json,canonical_request_url,fetched_at,raw_bytes,raw_byte_count,
	raw_sha256,created_at,''
FROM lyrics_source_index_evidence_v22 ORDER BY provider,evidence_id;

INSERT INTO lyrics_source_renditions
	(rendition_id,provider,artifact_id,origin,page_id,revision_id,mediawiki_sha1,page_title,canonical_revision_url,
	 fetched_at,categories_json,section,rendition_key,index_evidence_refs_json,fixed_identity_json,
	 fixed_identity_sha256,created_at)
SELECT rendition_id,provider,artifact_id,origin,page_id,revision_id,mediawiki_sha1,page_title,canonical_revision_url,
	fetched_at,categories_json,section,rendition_key,index_evidence_refs_json,fixed_identity_json,
	fixed_identity_sha256,created_at
FROM lyrics_source_renditions_v22 ORDER BY rendition_id;

INSERT INTO song_lyrics_source_artifacts
	(document_id,provider,rendition_key,origin,page_id,revision_id,revision_timestamp,mediawiki_sha1,page_title,
	 canonical_revision_url,fetched_at,categories_json,section,composition_rendition_key,version_reason,
	 index_evidence_refs_json,fixed_identity_json,fixed_identity_sha256,raw_byte_count,raw_wikitext_sha256,artifact_sha256)
SELECT document_id,provider,rendition_key,origin,page_id,revision_id,
	COALESCE(json_extract(fixed_identity_json,'$.revisionTimestamp'),''),mediawiki_sha1,page_title,
	canonical_revision_url,fetched_at,categories_json,section,
	COALESCE(json_extract(fixed_identity_json,'$.compositionRenditionKey'),''),
	COALESCE(json_extract(fixed_identity_json,'$.versionReason'),''),
	index_evidence_refs_json,fixed_identity_json,fixed_identity_sha256,raw_byte_count,raw_wikitext_sha256,artifact_sha256
FROM song_lyrics_source_artifacts_v22 ORDER BY document_id,rendition_key;

DROP TABLE song_lyrics_source_artifacts_v22;
DROP TABLE lyrics_source_renditions_v22;
DROP TABLE lyrics_source_index_evidence_v22;
DROP TABLE lyrics_discovery_job_outputs_v22;
DROP TABLE lyrics_source_review_decisions_v22;
DROP TABLE lyrics_source_review_items_v22;
DROP TABLE lyrics_source_analyses_v22;
DROP TABLE lyrics_source_artifacts_v22;
DROP TABLE lyrics_discovery_shadow_results_v22;
DROP TABLE lyrics_discovery_jobs_v22;

UPDATE sqlite_sequence
SET seq=MAX(seq,(SELECT saved.seq FROM lyrics_source_v23_sequences AS saved WHERE saved.name=sqlite_sequence.name))
WHERE name IN (SELECT name FROM lyrics_source_v23_sequences);
INSERT INTO sqlite_sequence(name,seq)
SELECT saved.name,saved.seq FROM lyrics_source_v23_sequences AS saved
WHERE NOT EXISTS (SELECT 1 FROM sqlite_sequence AS current WHERE current.name=saved.name);
DROP TABLE temp.lyrics_source_v23_sequences;
PRAGMA legacy_alter_table=OFF;

CREATE INDEX idx_lyrics_discovery_jobs_claim ON lyrics_discovery_jobs(state,next_attempt_at,job_id);
CREATE INDEX idx_lyrics_discovery_jobs_lease_expiry ON lyrics_discovery_jobs(lease_expires_at,job_id) WHERE state='leased';
CREATE INDEX idx_lyrics_discovery_jobs_music ON lyrics_discovery_jobs(music_id,job_id);
CREATE UNIQUE INDEX idx_lyrics_discovery_jobs_shadow_identity
	ON lyrics_discovery_jobs(job_id,music_id,catalog_fingerprint,policy_version);
CREATE INDEX idx_lyrics_discovery_jobs_provider_queue
	ON lyrics_discovery_jobs(provider,state,kind,next_attempt_at,job_id);
CREATE INDEX idx_lyrics_discovery_shadow_results_music
	ON lyrics_discovery_shadow_results(music_id,result_id);
CREATE INDEX idx_lyrics_source_artifacts_revision
	ON lyrics_source_artifacts(source_origin,revision_id);
CREATE INDEX idx_lyrics_source_artifacts_provider_identity
	ON lyrics_source_artifacts(provider,page_id,revision_id);
CREATE INDEX idx_lyrics_source_analyses_music
	ON lyrics_source_analyses(music_id,analysis_id);
CREATE INDEX idx_lyrics_source_review_items_queue
	ON lyrics_source_review_items(state,priority DESC,review_id);
CREATE INDEX idx_lyrics_source_review_items_music
	ON lyrics_source_review_items(music_id,review_id);
CREATE INDEX idx_lyrics_source_reviews_provider_queue
	ON lyrics_source_review_items(provider,state,priority DESC,review_id);
CREATE INDEX idx_lyrics_source_review_decisions_review
	ON lyrics_source_review_decisions(review_id,decision_id);
CREATE INDEX idx_lyrics_source_index_evidence_digest
	ON lyrics_source_index_evidence(provider,sha256,evidence_id);
CREATE INDEX idx_lyrics_source_renditions_artifact
	ON lyrics_source_renditions(artifact_id,rendition_key);
CREATE INDEX idx_song_lyrics_source_artifacts_provider
	ON song_lyrics_source_artifacts(provider,page_id,revision_id,rendition_key);

CREATE TRIGGER lyrics_discovery_jobs_integer_types_insert
BEFORE INSERT ON lyrics_discovery_jobs
WHEN typeof(NEW.job_id)<>'integer' OR typeof(NEW.music_id)<>'integer' OR
	typeof(NEW.page_id) NOT IN ('null','integer') OR typeof(NEW.revision_id) NOT IN ('null','integer') OR
	typeof(NEW.artifact_id) NOT IN ('null','integer') OR typeof(NEW.attempts)<>'integer' OR
	typeof(NEW.max_attempts)<>'integer' OR typeof(NEW.next_attempt_at)<>'integer' OR
	typeof(NEW.lease_expires_at) NOT IN ('null','integer') OR typeof(NEW.created_at)<>'integer' OR
	typeof(NEW.updated_at)<>'integer' OR typeof(NEW.completed_at) NOT IN ('null','integer') OR
	typeof(NEW.version)<>'integer'
BEGIN SELECT RAISE(ABORT, 'lyrics discovery job integer fields must be integers'); END;
CREATE TRIGGER lyrics_discovery_jobs_integer_types_update
BEFORE UPDATE ON lyrics_discovery_jobs
WHEN typeof(NEW.job_id)<>'integer' OR typeof(NEW.music_id)<>'integer' OR
	typeof(NEW.page_id) NOT IN ('null','integer') OR typeof(NEW.revision_id) NOT IN ('null','integer') OR
	typeof(NEW.artifact_id) NOT IN ('null','integer') OR typeof(NEW.attempts)<>'integer' OR
	typeof(NEW.max_attempts)<>'integer' OR typeof(NEW.next_attempt_at)<>'integer' OR
	typeof(NEW.lease_expires_at) NOT IN ('null','integer') OR typeof(NEW.created_at)<>'integer' OR
	typeof(NEW.updated_at)<>'integer' OR typeof(NEW.completed_at) NOT IN ('null','integer') OR
	typeof(NEW.version)<>'integer'
BEGIN SELECT RAISE(ABORT, 'lyrics discovery job integer fields must be integers'); END;
CREATE TRIGGER lyrics_discovery_shadow_results_integer_types_insert
BEFORE INSERT ON lyrics_discovery_shadow_results
WHEN typeof(NEW.result_id)<>'integer' OR typeof(NEW.job_id)<>'integer' OR typeof(NEW.music_id)<>'integer' OR
	typeof(NEW.candidate_count)<>'integer' OR typeof(NEW.created_at)<>'integer'
BEGIN SELECT RAISE(ABORT, 'lyrics discovery shadow result integer fields must be integers'); END;
CREATE TRIGGER lyrics_discovery_shadow_results_integer_types_update
BEFORE UPDATE ON lyrics_discovery_shadow_results
WHEN typeof(NEW.result_id)<>'integer' OR typeof(NEW.job_id)<>'integer' OR typeof(NEW.music_id)<>'integer' OR
	typeof(NEW.candidate_count)<>'integer' OR typeof(NEW.created_at)<>'integer'
BEGIN SELECT RAISE(ABORT, 'lyrics discovery shadow result integer fields must be integers'); END;

CREATE TRIGGER lyrics_discovery_fetch_expected_sha1_insert
BEFORE INSERT ON lyrics_discovery_jobs
WHEN (NEW.kind='fetch_revision' AND (length(NEW.expected_sha1)<>40 OR NEW.expected_sha1<>lower(NEW.expected_sha1) OR NEW.expected_sha1 GLOB '*[^0-9a-f]*')) OR
	(NEW.kind<>'fetch_revision' AND NEW.expected_sha1<>'')
BEGIN SELECT RAISE(ABORT, 'invalid expected lyrics source sha1'); END;
CREATE TRIGGER lyrics_discovery_fetch_expected_sha1_update
BEFORE UPDATE ON lyrics_discovery_jobs
WHEN (NEW.kind='fetch_revision' AND (length(NEW.expected_sha1)<>40 OR NEW.expected_sha1<>lower(NEW.expected_sha1) OR NEW.expected_sha1 GLOB '*[^0-9a-f]*')) OR
	(NEW.kind<>'fetch_revision' AND NEW.expected_sha1<>'')
BEGIN SELECT RAISE(ABORT, 'invalid expected lyrics source sha1'); END;

CREATE TRIGGER lyrics_source_artifacts_immutable_update BEFORE UPDATE ON lyrics_source_artifacts
BEGIN SELECT RAISE(ABORT, 'lyrics source artifacts are immutable'); END;
CREATE TRIGGER lyrics_source_artifacts_immutable_delete BEFORE DELETE ON lyrics_source_artifacts
BEGIN SELECT RAISE(ABORT, 'lyrics source artifacts are immutable'); END;
CREATE TRIGGER lyrics_source_analyses_structured_v2_insert
BEFORE INSERT ON lyrics_source_analyses
WHEN NEW.extractor_version='wiki-lyrics-v2-sekai-ruby-colors' AND (
	length(NEW.selected_version_json)<2 OR json_valid(NEW.selected_version_json)=0 OR json_type(NEW.selected_version_json)<>'object' OR
	COALESCE(json_extract(NEW.selected_version_json,'$.kind'),'') NOT IN ('sekai','vocaloid','original') OR
	COALESCE(json_type(NEW.selected_version_json,'$.label'),'')<>'text' OR trim(json_extract(NEW.selected_version_json,'$.label'))='' OR
	length(NEW.performers_json)<2 OR json_valid(NEW.performers_json)=0 OR json_type(NEW.performers_json)<>'array' OR
	trim(NEW.ruby_generator_version)='')
BEGIN SELECT RAISE(ABORT, 'invalid structured lyrics source analysis evidence'); END;
CREATE TRIGGER lyrics_source_analyses_immutable_update BEFORE UPDATE ON lyrics_source_analyses
BEGIN SELECT RAISE(ABORT, 'lyrics source analyses are immutable'); END;
CREATE TRIGGER lyrics_source_analyses_immutable_delete BEFORE DELETE ON lyrics_source_analyses
BEGIN SELECT RAISE(ABORT, 'lyrics source analyses are immutable'); END;
CREATE TRIGGER lyrics_source_review_decisions_immutable_update BEFORE UPDATE ON lyrics_source_review_decisions
BEGIN SELECT RAISE(ABORT, 'lyrics source review decisions are immutable'); END;
CREATE TRIGGER lyrics_source_review_decisions_immutable_delete BEFORE DELETE ON lyrics_source_review_decisions
BEGIN SELECT RAISE(ABORT, 'lyrics source review decisions are immutable'); END;
CREATE TRIGGER lyrics_discovery_job_outputs_immutable_update BEFORE UPDATE ON lyrics_discovery_job_outputs
BEGIN SELECT RAISE(ABORT, 'lyrics discovery job outputs are immutable'); END;
CREATE TRIGGER lyrics_discovery_job_outputs_immutable_delete BEFORE DELETE ON lyrics_discovery_job_outputs
BEGIN SELECT RAISE(ABORT, 'lyrics discovery job outputs are immutable'); END;
CREATE TRIGGER lyrics_source_index_evidence_immutable_update BEFORE UPDATE ON lyrics_source_index_evidence
BEGIN SELECT RAISE(ABORT, 'lyrics source index evidence is immutable'); END;
CREATE TRIGGER lyrics_source_index_evidence_immutable_delete BEFORE DELETE ON lyrics_source_index_evidence
BEGIN SELECT RAISE(ABORT, 'lyrics source index evidence is immutable'); END;
CREATE TRIGGER lyrics_source_renditions_immutable_update BEFORE UPDATE ON lyrics_source_renditions
BEGIN SELECT RAISE(ABORT, 'lyrics source renditions are immutable'); END;
CREATE TRIGGER lyrics_source_renditions_immutable_delete BEFORE DELETE ON lyrics_source_renditions
BEGIN SELECT RAISE(ABORT, 'lyrics source renditions are immutable'); END;
CREATE TRIGGER song_lyrics_source_artifacts_immutable_update BEFORE UPDATE ON song_lyrics_source_artifacts
BEGIN SELECT RAISE(ABORT, 'song lyrics source artifacts are immutable'); END;
CREATE TRIGGER song_lyrics_source_artifacts_immutable_delete BEFORE DELETE ON song_lyrics_source_artifacts
WHEN EXISTS (SELECT 1 FROM song_lyrics_source_documents WHERE document_id=OLD.document_id)
BEGIN SELECT RAISE(ABORT, 'song lyrics source artifacts are immutable'); END;

-- Provider columns are immutable identities and must match every durable parent.
CREATE TRIGGER lyrics_discovery_shadow_provider_parent_insert
BEFORE INSERT ON lyrics_discovery_shadow_results
WHEN NEW.provider<>(SELECT provider FROM lyrics_discovery_jobs WHERE job_id=NEW.job_id)
BEGIN SELECT RAISE(ABORT, 'lyrics discovery shadow provider mismatch'); END;
CREATE TRIGGER lyrics_discovery_shadow_provider_parent_update
BEFORE UPDATE OF job_id,provider ON lyrics_discovery_shadow_results
WHEN NEW.provider<>(SELECT provider FROM lyrics_discovery_jobs WHERE job_id=NEW.job_id)
BEGIN SELECT RAISE(ABORT, 'lyrics discovery shadow provider mismatch'); END;
CREATE TRIGGER lyrics_source_analysis_provider_insert
BEFORE INSERT ON lyrics_source_analyses
WHEN NEW.provider<>(SELECT provider FROM lyrics_source_artifacts WHERE artifact_id=NEW.artifact_id)
BEGIN SELECT RAISE(ABORT, 'lyrics source analysis provider mismatch'); END;
CREATE TRIGGER lyrics_source_review_provider_parent_insert
BEFORE INSERT ON lyrics_source_review_items
WHEN NEW.kind='artifact_review' AND NEW.provider<>(SELECT provider FROM lyrics_source_analyses WHERE analysis_id=NEW.analysis_id)
BEGIN SELECT RAISE(ABORT, 'lyrics source review provider mismatch'); END;
CREATE TRIGGER lyrics_source_review_provider_parent_update
BEFORE UPDATE OF kind,analysis_id,provider ON lyrics_source_review_items
WHEN NEW.kind='artifact_review' AND NEW.provider<>(SELECT provider FROM lyrics_source_analyses WHERE analysis_id=NEW.analysis_id)
BEGIN SELECT RAISE(ABORT, 'lyrics source review provider mismatch'); END;
CREATE TRIGGER lyrics_source_review_decision_provider_insert
BEFORE INSERT ON lyrics_source_review_decisions
WHEN NEW.provider<>(SELECT provider FROM lyrics_source_review_items WHERE review_id=NEW.review_id)
BEGIN SELECT RAISE(ABORT, 'lyrics source review decision provider mismatch'); END;
CREATE TRIGGER lyrics_discovery_job_output_provider_insert
BEFORE INSERT ON lyrics_discovery_job_outputs
WHEN NEW.provider<>(SELECT provider FROM lyrics_discovery_jobs WHERE job_id=NEW.job_id) OR
	NEW.provider<>(SELECT provider FROM lyrics_source_artifacts WHERE artifact_id=NEW.artifact_id) OR
	NEW.provider<>(SELECT provider FROM lyrics_source_analyses WHERE analysis_id=NEW.analysis_id) OR
	(NEW.review_id IS NOT NULL AND NEW.provider<>(SELECT provider FROM lyrics_source_review_items WHERE review_id=NEW.review_id))
BEGIN SELECT RAISE(ABORT, 'lyrics discovery job output provider mismatch'); END;
CREATE TRIGGER lyrics_source_rendition_provider_insert
BEFORE INSERT ON lyrics_source_renditions
WHEN NEW.provider<>(SELECT provider FROM lyrics_source_artifacts WHERE artifact_id=NEW.artifact_id)
BEGIN SELECT RAISE(ABORT, 'lyrics source rendition provider mismatch'); END;
-- Discovery results and candidate-selection reviews can intentionally aggregate
-- evidence from multiple providers. Artifact reviews remain provider-exact.
CREATE TRIGGER lyrics_source_review_index_evidence_provider_insert
BEFORE INSERT ON lyrics_source_review_index_evidence
WHEN (SELECT kind FROM lyrics_source_review_items WHERE review_id=NEW.review_id)='artifact_review' AND
	NEW.provider<>(SELECT provider FROM lyrics_source_review_items WHERE review_id=NEW.review_id)
BEGIN SELECT RAISE(ABORT, 'lyrics source review evidence provider mismatch'); END;

CREATE TRIGGER lyrics_discovery_provider_identity_immutable
BEFORE UPDATE OF provider,fixed_identity_json,provenance_status ON lyrics_discovery_jobs
WHEN OLD.provider IS NOT NEW.provider OR OLD.fixed_identity_json IS NOT NEW.fixed_identity_json OR
	OLD.provenance_status IS NOT NEW.provenance_status
BEGIN SELECT RAISE(ABORT, 'lyrics discovery provider identity is immutable'); END;
CREATE TRIGGER lyrics_discovery_shadow_provider_immutable
BEFORE UPDATE OF provider ON lyrics_discovery_shadow_results
WHEN OLD.provider IS NOT NEW.provider
BEGIN SELECT RAISE(ABORT, 'lyrics discovery shadow provider is immutable'); END;
CREATE TRIGGER lyrics_source_review_provider_immutable
BEFORE UPDATE OF provider ON lyrics_source_review_items
WHEN OLD.provider IS NOT NEW.provider
BEGIN SELECT RAISE(ABORT, 'lyrics source review provider is immutable'); END;
CREATE TRIGGER lyrics_discovery_fixed_target_immutable_update
BEFORE UPDATE OF kind,music_id,page_id,revision_id,artifact_id,catalog_fingerprint,policy_version,expected_sha1,fixed_candidate_json
ON lyrics_discovery_jobs
WHEN OLD.kind IS NOT NEW.kind OR OLD.music_id IS NOT NEW.music_id OR OLD.page_id IS NOT NEW.page_id OR
	OLD.revision_id IS NOT NEW.revision_id OR OLD.artifact_id IS NOT NEW.artifact_id OR
	OLD.catalog_fingerprint IS NOT NEW.catalog_fingerprint OR OLD.policy_version IS NOT NEW.policy_version OR
	OLD.expected_sha1 IS NOT NEW.expected_sha1 OR OLD.fixed_candidate_json IS NOT NEW.fixed_candidate_json
BEGIN SELECT RAISE(ABORT, 'lyrics discovery job target is immutable'); END;
CREATE TRIGGER lyrics_discovery_fetch_evidence_resolution_before_lease
BEFORE UPDATE OF state ON lyrics_discovery_jobs
WHEN NEW.kind='fetch_revision' AND NEW.state='leased' AND (
	NEW.provenance_status='rebuild_required' OR NEW.provenance_status NOT IN ('candidate_complete','complete') OR
	(SELECT COUNT(*) FROM lyrics_discovery_job_index_evidence AS link WHERE link.job_id=NEW.job_id)<>
	  json_array_length(json_extract(NEW.fixed_candidate_json,'$.candidate.indexEvidenceRefs')) OR
	EXISTS (
		SELECT 1 FROM json_each(json_extract(NEW.fixed_candidate_json,'$.candidate.indexEvidenceRefs')) AS reference
		LEFT JOIN lyrics_discovery_job_index_evidence AS link
		  ON link.job_id=NEW.job_id AND link.position=CAST(reference.key AS INTEGER) AND link.provider=NEW.provider
		 AND link.evidence_id=json_extract(reference.value,'$.evidenceId')
		 AND link.sha256=json_extract(reference.value,'$.sha256')
		WHERE link.job_id IS NULL))
BEGIN SELECT RAISE(ABORT, 'fetch job index evidence is unresolved'); END;

-- One shared violation view keeps insert and update validation identical. It
-- admits the exact v21 legacy Fandom envelope and the provider-aware envelope,
-- with Sekaipedia requiring one canonical revisionTimestamp throughout.
CREATE VIEW lyrics_discovery_job_identity_violations AS
SELECT job_id FROM lyrics_discovery_jobs AS job
WHERE CASE
	WHEN kind<>'fetch_revision' THEN fixed_candidate_json<>'' OR fixed_identity_json<>'' OR provenance_status<>'not_applicable'
	WHEN fixed_candidate_json='' OR json_valid(fixed_candidate_json)=0 THEN 1
	WHEN json_type(fixed_candidate_json)<>'object' OR
	     (SELECT COUNT(*) FROM json_each(fixed_candidate_json))<>2 OR
	     EXISTS (SELECT 1 FROM json_each(fixed_candidate_json) AS field WHERE field.key NOT IN ('schemaVersion','candidate')) OR
	     json_type(fixed_candidate_json,'$.schemaVersion')<>'integer' OR json_extract(fixed_candidate_json,'$.schemaVersion')<>1 OR
	     json_type(fixed_candidate_json,'$.candidate')<>'object' OR
	     json_type(fixed_candidate_json,'$.candidate.pageId')<>'integer' OR json_extract(fixed_candidate_json,'$.candidate.pageId')<>page_id OR
	     json_type(fixed_candidate_json,'$.candidate.revisionId')<>'integer' OR json_extract(fixed_candidate_json,'$.candidate.revisionId')<>revision_id OR
	     json_type(fixed_candidate_json,'$.candidate.sha1')<>'text' OR json_extract(fixed_candidate_json,'$.candidate.sha1')<>expected_sha1 OR
	     length(json_extract(fixed_candidate_json,'$.candidate.sha1'))<>40 OR
	     json_extract(fixed_candidate_json,'$.candidate.sha1')<>lower(json_extract(fixed_candidate_json,'$.candidate.sha1')) OR
	     json_extract(fixed_candidate_json,'$.candidate.sha1') GLOB '*[^0-9a-f]*' OR
	     json_type(fixed_candidate_json,'$.candidate.title')<>'text' OR
	     length(json_extract(fixed_candidate_json,'$.candidate.title')) NOT BETWEEN 1 AND 2048 OR
	     json_extract(fixed_candidate_json,'$.candidate.title')<>trim(json_extract(fixed_candidate_json,'$.candidate.title')) OR
	     json_type(fixed_candidate_json,'$.candidate.canonicalUrl')<>'text' OR
	     length(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl')) NOT BETWEEN 1 AND 4096 OR
	     json_extract(fixed_candidate_json,'$.candidate.canonicalUrl')<>trim(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl')) OR
	     instr(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl'),'#')<>0 OR
	     json_type(fixed_candidate_json,'$.candidate.categories')<>'array' OR
	     json_array_length(json_extract(fixed_candidate_json,'$.candidate.categories')) NOT BETWEEN 0 AND 256 OR
	     EXISTS (SELECT 1 FROM json_each(json_extract(fixed_candidate_json,'$.candidate.categories')) AS category
	             WHERE category.type<>'text' OR length(category.value) NOT BETWEEN 1 AND 512 OR category.value<>trim(category.value)) OR
	     EXISTS (SELECT 1 FROM json_each(json_extract(fixed_candidate_json,'$.candidate.categories')) AS category
	             JOIN json_each(json_extract(fixed_candidate_json,'$.candidate.categories')) AS following
	               ON CAST(following.key AS INTEGER)=CAST(category.key AS INTEGER)+1
	             WHERE category.value>=following.value) THEN 1
	WHEN provenance_status='rebuild_required' THEN
	     provider<>'vocaloid_fandom' OR fixed_identity_json<>'' OR
	     (SELECT COUNT(*) FROM json_each(json_extract(fixed_candidate_json,'$.candidate')))<>6 OR
	     EXISTS (SELECT 1 FROM json_each(json_extract(fixed_candidate_json,'$.candidate')) AS field
	             WHERE field.key NOT IN ('pageId','revisionId','sha1','title','canonicalUrl','categories')) OR
	     substr(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl'),1,
	            length('https://vocaloid.fandom.com/wiki/'))<>'https://vocaloid.fandom.com/wiki/' OR
	     instr(substr(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl'),
	                  length('https://vocaloid.fandom.com/wiki/')+1),'?')<=1 OR
	     substr(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl'),
	            instr(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl'),'?'))<>'?oldid='||revision_id
	WHEN provenance_status IN ('candidate_complete','complete') THEN
	     (SELECT COUNT(*) FROM json_each(json_extract(fixed_candidate_json,'$.candidate')))<>
	       CASE WHEN provider='sekaipedia' THEN 13 ELSE 12 END OR
	     EXISTS (SELECT 1 FROM json_each(json_extract(fixed_candidate_json,'$.candidate')) AS field
	             WHERE field.key NOT IN ('provider','origin','pageId','revisionId','revisionTimestamp','sha1','title',
	                                     'canonicalUrl','categories','section','renditionKey','versionReason','indexEvidenceRefs')) OR
	     json_type(fixed_candidate_json,'$.candidate.provider')<>'text' OR
	     json_extract(fixed_candidate_json,'$.candidate.provider')<>provider OR
	     json_type(fixed_candidate_json,'$.candidate.origin')<>'text' OR
	     json_type(fixed_candidate_json,'$.candidate.section')<>'text' OR
	     length(json_extract(fixed_candidate_json,'$.candidate.section')) NOT BETWEEN 1 AND 512 OR
	     json_extract(fixed_candidate_json,'$.candidate.section')<>trim(json_extract(fixed_candidate_json,'$.candidate.section')) OR
	     json_type(fixed_candidate_json,'$.candidate.renditionKey')<>'text' OR
	     length(json_extract(fixed_candidate_json,'$.candidate.renditionKey')) NOT BETWEEN 1 AND 128 OR
	     json_extract(fixed_candidate_json,'$.candidate.renditionKey')<>lower(json_extract(fixed_candidate_json,'$.candidate.renditionKey')) OR
	     substr(json_extract(fixed_candidate_json,'$.candidate.renditionKey'),1,1) NOT GLOB '[a-z0-9]' OR
	     json_extract(fixed_candidate_json,'$.candidate.renditionKey') GLOB '*[^a-z0-9._-]*' OR
	     json_type(fixed_candidate_json,'$.candidate.versionReason')<>'text' OR
	     json_extract(fixed_candidate_json,'$.candidate.versionReason') NOT IN
	       ('tagged_full_and_game','tagged_game_only_full_from_vocaloid','untagged_uncut_identity',
	        'untagged_game_subset','untagged_full_only','version_conflict') OR
	     json_type(fixed_candidate_json,'$.candidate.indexEvidenceRefs')<>'array' OR
	     json_array_length(json_extract(fixed_candidate_json,'$.candidate.indexEvidenceRefs')) NOT BETWEEN 1 AND 64 OR
	     EXISTS (SELECT 1 FROM json_each(json_extract(fixed_candidate_json,'$.candidate.indexEvidenceRefs')) AS reference
	             WHERE reference.type<>'object' OR (SELECT COUNT(*) FROM json_each(reference.value))<>2 OR
	                   EXISTS (SELECT 1 FROM json_each(reference.value) AS field WHERE field.key NOT IN ('evidenceId','sha256')) OR
	                   json_type(reference.value,'$.evidenceId')<>'text' OR
	                   length(json_extract(reference.value,'$.evidenceId')) NOT BETWEEN 1 AND 256 OR
	                   substr(json_extract(reference.value,'$.evidenceId'),1,1) NOT GLOB '[A-Za-z0-9]' OR
	                   substr(json_extract(reference.value,'$.evidenceId'),2) GLOB '*[^A-Za-z0-9._:/-]*' OR
	                   json_type(reference.value,'$.sha256')<>'text' OR
	                   length(json_extract(reference.value,'$.sha256'))<>64 OR
	                   json_extract(reference.value,'$.sha256')<>lower(json_extract(reference.value,'$.sha256')) OR
	                   json_extract(reference.value,'$.sha256') GLOB '*[^0-9a-f]*') OR
	     EXISTS (SELECT 1 FROM json_each(json_extract(fixed_candidate_json,'$.candidate.indexEvidenceRefs')) AS reference
	             JOIN json_each(json_extract(fixed_candidate_json,'$.candidate.indexEvidenceRefs')) AS duplicate
	               ON CAST(reference.key AS INTEGER)<CAST(duplicate.key AS INTEGER) AND
	                  json_extract(reference.value,'$.evidenceId')=json_extract(duplicate.value,'$.evidenceId')) OR
	     (provider='vocaloid_fandom' AND
	       (json_extract(fixed_candidate_json,'$.candidate.origin')<>'https://vocaloid.fandom.com' OR
	        substr(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl'),1,
	               length('https://vocaloid.fandom.com/wiki/'))<>'https://vocaloid.fandom.com/wiki/' OR
	        instr(substr(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl'),
	                     length('https://vocaloid.fandom.com/wiki/')+1),'?')<=1 OR
	        substr(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl'),
	               instr(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl'),'?'))<>'?oldid='||revision_id OR
	        json_type(fixed_candidate_json,'$.candidate.revisionTimestamp') IS NOT NULL)) OR
	     (provider='sekaipedia' AND
	       (json_extract(fixed_candidate_json,'$.candidate.origin')<>'https://www.sekaipedia.org' OR
	        substr(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl'),1,
	               length('https://www.sekaipedia.org/wiki/'))<>'https://www.sekaipedia.org/wiki/' OR
	        instr(substr(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl'),
	                     length('https://www.sekaipedia.org/wiki/')+1),'?')<=1 OR
	        substr(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl'),
	               instr(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl'),'?'))<>'?oldid='||revision_id OR
	        json_type(fixed_candidate_json,'$.candidate.revisionTimestamp')<>'text' OR
	        length(json_extract(fixed_candidate_json,'$.candidate.revisionTimestamp')) NOT BETWEEN 20 AND 30 OR
	        substr(json_extract(fixed_candidate_json,'$.candidate.revisionTimestamp'),-1)<>'Z' OR
	        strftime('%s',json_extract(fixed_candidate_json,'$.candidate.revisionTimestamp')) IS NULL OR
	        NOT (length(json_extract(fixed_candidate_json,'$.candidate.revisionTimestamp'))=20 OR
	             (length(json_extract(fixed_candidate_json,'$.candidate.revisionTimestamp')) BETWEEN 22 AND 30 AND
	              substr(json_extract(fixed_candidate_json,'$.candidate.revisionTimestamp'),20,1)='.' AND
	              substr(json_extract(fixed_candidate_json,'$.candidate.revisionTimestamp'),21,
	                     length(json_extract(fixed_candidate_json,'$.candidate.revisionTimestamp'))-21) NOT GLOB '*[^0-9]*' AND
	              substr(json_extract(fixed_candidate_json,'$.candidate.revisionTimestamp'),-2,1)<>'0')))) OR
	     (provider='moegirl' AND
	       (json_extract(fixed_candidate_json,'$.candidate.origin')<>'https://moegirl.icu' OR
	        json_type(fixed_candidate_json,'$.candidate.revisionTimestamp') IS NOT NULL OR
	        NOT (
	          (substr(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl'),1,length('https://moegirl.icu/wiki/'))=
	             'https://moegirl.icu/wiki/' AND
	           instr(substr(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl'),length('https://moegirl.icu/wiki/')+1),'?')>1 AND
	           substr(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl'),
	                  instr(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl'),'?'))='?oldid='||revision_id) OR
	          (substr(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl'),1,
	                  length('https://moegirl.icu/index.php?oldid='||revision_id||'&title='))=
	             'https://moegirl.icu/index.php?oldid='||revision_id||'&title=' AND
	           length(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl'))>
	             length('https://moegirl.icu/index.php?oldid='||revision_id||'&title=') AND
	           instr(substr(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl'),
	                        length('https://moegirl.icu/index.php?oldid='||revision_id||'&title=')+1),'&')=0)))) OR
	     (provenance_status='candidate_complete' AND fixed_identity_json<>'') OR
	     (provenance_status='complete' AND CASE
	       WHEN fixed_identity_json='' OR json_valid(fixed_identity_json)=0 THEN 1
	       ELSE json_type(fixed_identity_json)<>'object' OR
	         (SELECT COUNT(*) FROM json_each(fixed_identity_json)) NOT BETWEEN 12 AND 15 OR
	         EXISTS (SELECT 1 FROM json_each(fixed_identity_json) AS field
	                 WHERE field.key NOT IN ('provider','origin','pageId','revisionId','sha1','title','canonicalUrl',
	                                         'revisionTimestamp','fetchedAt','categories','section','renditionKey',
	                                         'compositionRenditionKey','versionReason','indexEvidenceRefs')) OR
	         json_type(fixed_identity_json,'$.provider')<>'text' OR json_extract(fixed_identity_json,'$.provider')<>provider OR
	         json_type(fixed_identity_json,'$.origin')<>'text' OR
	         json_extract(fixed_identity_json,'$.origin')<>json_extract(fixed_candidate_json,'$.candidate.origin') OR
	         json_type(fixed_identity_json,'$.pageId')<>'integer' OR json_extract(fixed_identity_json,'$.pageId')<>page_id OR
	         json_type(fixed_identity_json,'$.revisionId')<>'integer' OR json_extract(fixed_identity_json,'$.revisionId')<>revision_id OR
	         json_type(fixed_identity_json,'$.sha1')<>'text' OR json_extract(fixed_identity_json,'$.sha1')<>expected_sha1 OR
	         json_type(fixed_identity_json,'$.title')<>'text' OR
	         json_extract(fixed_identity_json,'$.title')<>json_extract(fixed_candidate_json,'$.candidate.title') OR
	         json_type(fixed_identity_json,'$.canonicalUrl')<>'text' OR
	         json_extract(fixed_identity_json,'$.canonicalUrl')<>json_extract(fixed_candidate_json,'$.candidate.canonicalUrl') OR
	         json_type(fixed_identity_json,'$.fetchedAt')<>'text' OR
	         length(json_extract(fixed_identity_json,'$.fetchedAt')) NOT BETWEEN 20 AND 30 OR
	         substr(json_extract(fixed_identity_json,'$.fetchedAt'),-1)<>'Z' OR
	         strftime('%s',json_extract(fixed_identity_json,'$.fetchedAt')) IS NULL OR
	         NOT (length(json_extract(fixed_identity_json,'$.fetchedAt'))=20 OR
	              (length(json_extract(fixed_identity_json,'$.fetchedAt')) BETWEEN 22 AND 30 AND
	               substr(json_extract(fixed_identity_json,'$.fetchedAt'),20,1)='.' AND
	               substr(json_extract(fixed_identity_json,'$.fetchedAt'),21,
	                      length(json_extract(fixed_identity_json,'$.fetchedAt'))-21) NOT GLOB '*[^0-9]*' AND
	               substr(json_extract(fixed_identity_json,'$.fetchedAt'),-2,1)<>'0')) OR
	         json_type(fixed_identity_json,'$.categories')<>'array' OR
	         json(json_extract(fixed_identity_json,'$.categories'))<>json(json_extract(fixed_candidate_json,'$.candidate.categories')) OR
	         json_type(fixed_identity_json,'$.section')<>'text' OR
	         json_extract(fixed_identity_json,'$.section')<>json_extract(fixed_candidate_json,'$.candidate.section') OR
	         json_type(fixed_identity_json,'$.renditionKey')<>'text' OR
	         json_extract(fixed_identity_json,'$.renditionKey')<>json_extract(fixed_candidate_json,'$.candidate.renditionKey') OR
	         json_type(fixed_identity_json,'$.indexEvidenceRefs')<>'array' OR
	         json(json_extract(fixed_identity_json,'$.indexEvidenceRefs'))<>
	           json(json_extract(fixed_candidate_json,'$.candidate.indexEvidenceRefs')) OR
	         (json_type(fixed_identity_json,'$.compositionRenditionKey') IS NOT NULL AND
	          (json_type(fixed_identity_json,'$.compositionRenditionKey')<>'text' OR
	           length(json_extract(fixed_identity_json,'$.compositionRenditionKey')) NOT BETWEEN 1 AND 128 OR
	           json_extract(fixed_identity_json,'$.compositionRenditionKey')<>lower(json_extract(fixed_identity_json,'$.compositionRenditionKey')) OR
	           json_extract(fixed_identity_json,'$.compositionRenditionKey') GLOB '*[^a-z0-9._-]*')) OR
	         (json_type(fixed_identity_json,'$.versionReason') IS NOT NULL AND
	          (json_type(fixed_identity_json,'$.versionReason')<>'text' OR
	           json_extract(fixed_identity_json,'$.versionReason')<>json_extract(fixed_candidate_json,'$.candidate.versionReason'))) OR
	         (provider='sekaipedia' AND
	          (json_type(fixed_identity_json,'$.revisionTimestamp')<>'text' OR
	           json_extract(fixed_identity_json,'$.revisionTimestamp')<>
	             json_extract(fixed_candidate_json,'$.candidate.revisionTimestamp') OR
	           julianday(json_extract(fixed_identity_json,'$.revisionTimestamp'))>
	             julianday(json_extract(fixed_identity_json,'$.fetchedAt')))) OR
	         (provider<>'sekaipedia' AND json_type(fixed_identity_json,'$.revisionTimestamp') IS NOT NULL)
	       END)
	ELSE 1
END;

CREATE TRIGGER lyrics_discovery_fixed_candidate_validate_insert
AFTER INSERT ON lyrics_discovery_jobs
WHEN EXISTS (SELECT 1 FROM lyrics_discovery_job_identity_violations WHERE job_id=NEW.job_id)
BEGIN SELECT RAISE(ABORT, 'invalid provider-scoped fixed candidate identity'); END;
CREATE TRIGGER lyrics_discovery_fixed_candidate_validate_update
AFTER UPDATE ON lyrics_discovery_jobs
WHEN EXISTS (SELECT 1 FROM lyrics_discovery_job_identity_violations WHERE job_id=NEW.job_id)
BEGIN SELECT RAISE(ABORT, 'invalid provider-scoped fixed candidate identity'); END;

-- Renditions and final song-source artifacts keep revisionTimestamp in exactly
-- one canonical fixed-identity object. Every scalar duplicate is compared back
-- to that object, and final artifacts must resolve to the same object in their
-- immutable parent document graph.
CREATE VIEW lyrics_source_fixed_identity_rows AS
SELECT 'rendition' AS scope,rendition_id AS owner_id,rendition_key,provider,origin,page_id,revision_id,
	COALESCE(json_extract(fixed_identity_json,'$.revisionTimestamp'),'') AS revision_timestamp,
	mediawiki_sha1,page_title,canonical_revision_url,fetched_at,categories_json,section,
	COALESCE(json_extract(fixed_identity_json,'$.compositionRenditionKey'),'') AS composition_rendition_key,
	COALESCE(json_extract(fixed_identity_json,'$.versionReason'),'') AS version_reason,
	index_evidence_refs_json,fixed_identity_json,NULL AS parent_document_json
FROM lyrics_source_renditions
UNION ALL
SELECT 'song' AS scope,artifact.document_id AS owner_id,artifact.rendition_key,artifact.provider,artifact.origin,
	artifact.page_id,artifact.revision_id,artifact.revision_timestamp,artifact.mediawiki_sha1,artifact.page_title,
	artifact.canonical_revision_url,artifact.fetched_at,artifact.categories_json,artifact.section,
	artifact.composition_rendition_key,artifact.version_reason,artifact.index_evidence_refs_json,
	artifact.fixed_identity_json,document.document_json AS parent_document_json
FROM song_lyrics_source_artifacts AS artifact
JOIN song_lyrics_source_documents AS document ON document.document_id=artifact.document_id;

CREATE VIEW lyrics_source_fixed_identity_violations AS
SELECT scope,owner_id,rendition_key FROM lyrics_source_fixed_identity_rows AS row
WHERE json_valid(fixed_identity_json)=0 OR json_type(fixed_identity_json)<>'object' OR
	(SELECT COUNT(*) FROM json_each(fixed_identity_json)) NOT BETWEEN 12 AND 15 OR
	EXISTS (SELECT 1 FROM json_each(fixed_identity_json) AS field
	        WHERE field.key NOT IN ('provider','origin','pageId','revisionId','sha1','title','canonicalUrl',
	                                'revisionTimestamp','fetchedAt','categories','section','renditionKey',
	                                'compositionRenditionKey','versionReason','indexEvidenceRefs')) OR
	json_type(fixed_identity_json,'$.provider')<>'text' OR json_extract(fixed_identity_json,'$.provider')<>provider OR
	json_type(fixed_identity_json,'$.origin')<>'text' OR json_extract(fixed_identity_json,'$.origin')<>origin OR
	json_type(fixed_identity_json,'$.pageId')<>'integer' OR json_extract(fixed_identity_json,'$.pageId')<>page_id OR
	json_type(fixed_identity_json,'$.revisionId')<>'integer' OR json_extract(fixed_identity_json,'$.revisionId')<>revision_id OR
	COALESCE(json_extract(fixed_identity_json,'$.revisionTimestamp'),'')<>revision_timestamp OR
	json_type(fixed_identity_json,'$.sha1')<>'text' OR json_extract(fixed_identity_json,'$.sha1')<>mediawiki_sha1 OR
	json_type(fixed_identity_json,'$.title')<>'text' OR json_extract(fixed_identity_json,'$.title')<>page_title OR
	json_type(fixed_identity_json,'$.canonicalUrl')<>'text' OR
	json_extract(fixed_identity_json,'$.canonicalUrl')<>canonical_revision_url OR
	json_type(fixed_identity_json,'$.fetchedAt')<>'text' OR json_extract(fixed_identity_json,'$.fetchedAt')<>fetched_at OR
	length(json_extract(fixed_identity_json,'$.fetchedAt')) NOT BETWEEN 20 AND 30 OR
	substr(json_extract(fixed_identity_json,'$.fetchedAt'),-1)<>'Z' OR
	strftime('%s',json_extract(fixed_identity_json,'$.fetchedAt')) IS NULL OR
	NOT (length(json_extract(fixed_identity_json,'$.fetchedAt'))=20 OR
	     (length(json_extract(fixed_identity_json,'$.fetchedAt')) BETWEEN 22 AND 30 AND
	      substr(json_extract(fixed_identity_json,'$.fetchedAt'),20,1)='.' AND
	      substr(json_extract(fixed_identity_json,'$.fetchedAt'),21,
	             length(json_extract(fixed_identity_json,'$.fetchedAt'))-21) NOT GLOB '*[^0-9]*' AND
	      substr(json_extract(fixed_identity_json,'$.fetchedAt'),-2,1)<>'0')) OR
	json_type(fixed_identity_json,'$.categories')<>'array' OR
	json(json_extract(fixed_identity_json,'$.categories'))<>json(categories_json) OR
	json_type(fixed_identity_json,'$.section')<>'text' OR json_extract(fixed_identity_json,'$.section')<>section OR
	json_type(fixed_identity_json,'$.renditionKey')<>'text' OR
	json_extract(fixed_identity_json,'$.renditionKey')<>rendition_key OR
	COALESCE(json_extract(fixed_identity_json,'$.compositionRenditionKey'),'')<>composition_rendition_key OR
	COALESCE(json_extract(fixed_identity_json,'$.versionReason'),'')<>version_reason OR
	json_type(fixed_identity_json,'$.indexEvidenceRefs')<>'array' OR
	json(json_extract(fixed_identity_json,'$.indexEvidenceRefs'))<>json(index_evidence_refs_json) OR
	EXISTS (SELECT 1 FROM json_each(json_extract(fixed_identity_json,'$.indexEvidenceRefs')) AS reference
	        WHERE reference.type<>'object' OR (SELECT COUNT(*) FROM json_each(reference.value))<>2 OR
	              EXISTS (SELECT 1 FROM json_each(reference.value) AS field WHERE field.key NOT IN ('evidenceId','sha256')) OR
	              json_type(reference.value,'$.evidenceId')<>'text' OR
	              length(json_extract(reference.value,'$.evidenceId')) NOT BETWEEN 1 AND 256 OR
	              substr(json_extract(reference.value,'$.evidenceId'),1,1) NOT GLOB '[A-Za-z0-9]' OR
	              substr(json_extract(reference.value,'$.evidenceId'),2) GLOB '*[^A-Za-z0-9._:/-]*' OR
	              json_type(reference.value,'$.sha256')<>'text' OR
	              length(json_extract(reference.value,'$.sha256'))<>64 OR
	              json_extract(reference.value,'$.sha256')<>lower(json_extract(reference.value,'$.sha256')) OR
	              json_extract(reference.value,'$.sha256') GLOB '*[^0-9a-f]*') OR
	EXISTS (SELECT 1 FROM json_each(json_extract(fixed_identity_json,'$.indexEvidenceRefs')) AS reference
	        JOIN json_each(json_extract(fixed_identity_json,'$.indexEvidenceRefs')) AS duplicate
	          ON CAST(reference.key AS INTEGER)<CAST(duplicate.key AS INTEGER) AND
	             json_extract(reference.value,'$.evidenceId')=json_extract(duplicate.value,'$.evidenceId')) OR
	(json_type(fixed_identity_json,'$.compositionRenditionKey') IS NOT NULL AND
	 (json_type(fixed_identity_json,'$.compositionRenditionKey')<>'text' OR
	  length(json_extract(fixed_identity_json,'$.compositionRenditionKey')) NOT BETWEEN 1 AND 128 OR
	  json_extract(fixed_identity_json,'$.compositionRenditionKey')<>lower(json_extract(fixed_identity_json,'$.compositionRenditionKey')) OR
	  substr(json_extract(fixed_identity_json,'$.compositionRenditionKey'),1,1) NOT GLOB '[a-z0-9]' OR
	  json_extract(fixed_identity_json,'$.compositionRenditionKey') GLOB '*[^a-z0-9._-]*')) OR
	(json_type(fixed_identity_json,'$.versionReason') IS NOT NULL AND
	 (json_type(fixed_identity_json,'$.versionReason')<>'text' OR
	  json_extract(fixed_identity_json,'$.versionReason') NOT IN
	    ('tagged_full_and_game','tagged_game_only_full_from_vocaloid','untagged_uncut_identity',
	     'untagged_game_subset','untagged_full_only','version_conflict'))) OR
	(provider='sekaipedia' AND
	 (json_type(fixed_identity_json,'$.revisionTimestamp')<>'text' OR
	  length(json_extract(fixed_identity_json,'$.revisionTimestamp')) NOT BETWEEN 20 AND 30 OR
	  substr(json_extract(fixed_identity_json,'$.revisionTimestamp'),-1)<>'Z' OR
	  strftime('%s',json_extract(fixed_identity_json,'$.revisionTimestamp')) IS NULL OR
	  NOT (length(json_extract(fixed_identity_json,'$.revisionTimestamp'))=20 OR
	       (length(json_extract(fixed_identity_json,'$.revisionTimestamp')) BETWEEN 22 AND 30 AND
	        substr(json_extract(fixed_identity_json,'$.revisionTimestamp'),20,1)='.' AND
	        substr(json_extract(fixed_identity_json,'$.revisionTimestamp'),21,
	               length(json_extract(fixed_identity_json,'$.revisionTimestamp'))-21) NOT GLOB '*[^0-9]*' AND
	        substr(json_extract(fixed_identity_json,'$.revisionTimestamp'),-2,1)<>'0')) OR
	  julianday(json_extract(fixed_identity_json,'$.revisionTimestamp'))>
	    julianday(json_extract(fixed_identity_json,'$.fetchedAt')))) OR
	(provider<>'sekaipedia' AND json_type(fixed_identity_json,'$.revisionTimestamp') IS NOT NULL) OR
	(scope='song' AND
	 (json_type(parent_document_json,'$.fixedIdentities')<>'array' OR
	  (SELECT COUNT(*) FROM json_each(json_extract(parent_document_json,'$.fixedIdentities')) AS identity
	   WHERE identity.type='object' AND json_extract(identity.value,'$.renditionKey')=rendition_key)<>1 OR
	  (SELECT COUNT(*) FROM json_each(json_extract(parent_document_json,'$.fixedIdentities')) AS identity
	   WHERE identity.type='object' AND json_extract(identity.value,'$.renditionKey')=rendition_key AND
	         json(identity.value)=json(fixed_identity_json))<>1));

CREATE TRIGGER lyrics_source_renditions_identity_validate_insert
AFTER INSERT ON lyrics_source_renditions
WHEN EXISTS (SELECT 1 FROM lyrics_source_fixed_identity_violations
             WHERE scope='rendition' AND owner_id=NEW.rendition_id AND rendition_key=NEW.rendition_key)
BEGIN SELECT RAISE(ABORT, 'invalid lyrics source rendition fixed identity'); END;
CREATE TRIGGER song_lyrics_source_artifacts_identity_validate_insert
AFTER INSERT ON song_lyrics_source_artifacts
WHEN EXISTS (SELECT 1 FROM lyrics_source_fixed_identity_violations
             WHERE scope='song' AND owner_id=NEW.document_id AND rendition_key=NEW.rendition_key)
BEGIN SELECT RAISE(ABORT, 'invalid song lyrics source artifact fixed identity'); END;

CREATE TEMP TABLE lyrics_source_v23_validation_guard (
	invalid_count INTEGER NOT NULL CHECK (invalid_count=0)
);
INSERT INTO lyrics_source_v23_validation_guard(invalid_count)
SELECT (SELECT COUNT(*) FROM lyrics_discovery_job_identity_violations) +
       (SELECT COUNT(*) FROM lyrics_source_fixed_identity_violations) +
       (SELECT COUNT(*) FROM lyrics_discovery_shadow_results AS result
        JOIN lyrics_discovery_jobs AS job ON job.job_id=result.job_id
        WHERE result.provider<>job.provider) +
       (SELECT COUNT(*) FROM lyrics_source_analyses AS analysis
        JOIN lyrics_source_artifacts AS artifact ON artifact.artifact_id=analysis.artifact_id
        WHERE analysis.provider<>artifact.provider) +
       (SELECT COUNT(*) FROM lyrics_source_review_items AS review
        JOIN lyrics_source_analyses AS analysis ON analysis.analysis_id=review.analysis_id
        WHERE review.kind='artifact_review' AND review.provider<>analysis.provider) +
       (SELECT COUNT(*) FROM lyrics_source_review_decisions AS decision
        JOIN lyrics_source_review_items AS review ON review.review_id=decision.review_id
        WHERE decision.provider<>review.provider) +
       (SELECT COUNT(*) FROM lyrics_discovery_job_outputs AS output
        JOIN lyrics_discovery_jobs AS job ON job.job_id=output.job_id
        JOIN lyrics_source_artifacts AS artifact ON artifact.artifact_id=output.artifact_id
        JOIN lyrics_source_analyses AS analysis ON analysis.analysis_id=output.analysis_id
        LEFT JOIN lyrics_source_review_items AS review ON review.review_id=output.review_id
        WHERE output.provider<>job.provider OR output.provider<>artifact.provider OR
              output.provider<>analysis.provider OR (output.review_id IS NOT NULL AND output.provider<>review.provider)) +
       (SELECT COUNT(*) FROM lyrics_source_renditions AS rendition
        JOIN lyrics_source_artifacts AS artifact ON artifact.artifact_id=rendition.artifact_id
        WHERE rendition.provider<>artifact.provider) +
       (SELECT COUNT(*) FROM lyrics_discovery_job_index_evidence AS link
        JOIN lyrics_discovery_jobs AS job ON job.job_id=link.job_id
        WHERE link.provider<>job.provider) +
       (SELECT COUNT(*) FROM lyrics_source_review_index_evidence AS link
        JOIN lyrics_source_review_items AS review ON review.review_id=link.review_id
        WHERE review.kind='artifact_review' AND link.provider<>review.provider) +
       (SELECT COUNT(*) FROM lyrics_source_rendition_index_evidence AS link
        JOIN lyrics_source_renditions AS rendition ON rendition.rendition_id=link.rendition_id
        WHERE link.provider<>rendition.provider) +
       (SELECT COUNT(*) FROM song_lyrics_source_artifact_index_evidence AS link
        JOIN song_lyrics_source_artifacts AS artifact
          ON artifact.document_id=link.document_id AND artifact.rendition_key=link.rendition_key
        WHERE link.provider<>artifact.provider);
DROP TABLE temp.lyrics_source_v23_validation_guard;
`,
}, {
	version: 24,
	name:    "additive_lyrics_recovery_import_storage",
	sql:     migrationV24LyricsRecoveryImportSQL,
}, {
	version: 25,
	name:    "lyrics_source_document_schema_v2",
	sql:     migrationV25LyricsSourceSchemaSQL,
}, {
	version: 26,
	name:    "lyrics_translation_and_proofreading_credits",
	sql: `
ALTER TABLE song_lyrics ADD COLUMN translation_credit TEXT NOT NULL DEFAULT '';
ALTER TABLE song_lyrics ADD COLUMN proofreading_credit TEXT NOT NULL DEFAULT '';
`,
}, {
	version: 27,
	name:    "lyrics_peer_renditions_and_localizations",
	sql:     migrationV27LyricsRenditionsSQL,
}, {
	version: 28,
	name:    "embedded_lyrics_editor_seed_ledger",
	sql:     migrationV28EmbeddedLyricsEditorSeedSQL,
}, {
	version: 29,
	name:    "lyrics_yjs_collaboration",
	sql: `
CREATE TABLE lyrics_collab_documents (
	music_id          INTEGER PRIMARY KEY,
	schema_version    INTEGER NOT NULL CHECK (schema_version=1),
	epoch             INTEGER NOT NULL CHECK (epoch>0),
	update_v1         BLOB NOT NULL CHECK (length(update_v1)>0),
	base_revision     INTEGER NOT NULL CHECK (base_revision>=0),
	authority_sha256  TEXT NOT NULL CHECK (
		length(authority_sha256)=64 AND authority_sha256=lower(authority_sha256) AND
		authority_sha256 NOT GLOB '*[^0-9a-f]*'
	),
	updated_at        INTEGER NOT NULL CHECK (updated_at>0),
	checkpointed_at   INTEGER NOT NULL DEFAULT 0 CHECK (checkpointed_at>=0),
	checkpointed_by   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_lyrics_collab_documents_updated_at
ON lyrics_collab_documents(updated_at);

CREATE TABLE lyrics_collab_updates (
	music_id       INTEGER NOT NULL,
	epoch          INTEGER NOT NULL CHECK (epoch>0),
	seq            INTEGER NOT NULL CHECK (seq>0),
	update_v1      BLOB NOT NULL CHECK (length(update_v1)>0),
	update_sha256  TEXT NOT NULL CHECK (
		length(update_sha256)=64 AND update_sha256=lower(update_sha256) AND
		update_sha256 NOT GLOB '*[^0-9a-f]*'
	),
	update_size    INTEGER NOT NULL CHECK (update_size=length(update_v1)),
	created_at     INTEGER NOT NULL CHECK (created_at>0),
	PRIMARY KEY (music_id,epoch,seq)
);
CREATE INDEX idx_lyrics_collab_updates_created_at
ON lyrics_collab_updates(created_at);

CREATE TABLE lyrics_collab_checkpoints (
	checkpoint_id          INTEGER PRIMARY KEY AUTOINCREMENT,
	music_id               INTEGER NOT NULL,
	epoch                  INTEGER NOT NULL CHECK (epoch>0),
	base_revision          INTEGER NOT NULL CHECK (base_revision>=0),
	new_revision           INTEGER NOT NULL CHECK (new_revision>=base_revision),
	base_authority_sha256  TEXT NOT NULL CHECK (length(base_authority_sha256)=64),
	new_authority_sha256   TEXT NOT NULL CHECK (length(new_authority_sha256)=64),
	actor                  TEXT NOT NULL,
	changed                INTEGER NOT NULL CHECK (changed IN (0,1)),
	created_at             INTEGER NOT NULL CHECK (created_at>0)
);
CREATE INDEX idx_lyrics_collab_checkpoints_music
ON lyrics_collab_checkpoints(music_id,checkpoint_id);
`,
}}

func validateLyricsDiscoveryIntegerTypes(tx *sql.Tx) error {
	checks := []struct {
		table     string
		predicate string
	}{
		{
			table: "lyrics_discovery_jobs",
			predicate: `typeof(job_id) <> 'integer'
				OR typeof(music_id) <> 'integer'
				OR typeof(page_id) NOT IN ('null', 'integer')
				OR typeof(revision_id) NOT IN ('null', 'integer')
				OR typeof(artifact_id) NOT IN ('null', 'integer')
				OR typeof(attempts) <> 'integer'
				OR typeof(max_attempts) <> 'integer'
				OR typeof(next_attempt_at) <> 'integer'
				OR typeof(lease_expires_at) NOT IN ('null', 'integer')
				OR typeof(created_at) <> 'integer'
				OR typeof(updated_at) <> 'integer'
				OR typeof(completed_at) NOT IN ('null', 'integer')
				OR typeof(version) <> 'integer'`,
		},
		{
			table: "lyrics_discovery_shadow_results",
			predicate: `typeof(result_id) <> 'integer'
				OR typeof(job_id) <> 'integer'
				OR typeof(music_id) <> 'integer'
				OR typeof(candidate_count) <> 'integer'
				OR typeof(created_at) <> 'integer'`,
		},
	}
	for _, check := range checks {
		var invalid int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM ` + check.table + ` WHERE ` + check.predicate).Scan(&invalid); err != nil {
			return err
		}
		if invalid != 0 {
			return fmt.Errorf("%s contains %d rows with non-integer numeric fields", check.table, invalid)
		}
	}
	return nil
}

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
	expectedVersion := 1
	for rows.Next() {
		var version int
		var name, checksum string
		if err := rows.Scan(&version, &name, &checksum); err != nil {
			return nil, err
		}
		if version < 1 || version > len(migrations) {
			return nil, fmt.Errorf("database migration version %d is newer than this binary", version)
		}
		if version != expectedVersion {
			return nil, fmt.Errorf("database migration history is not a contiguous prefix: expected version %d, found %d", expectedVersion, version)
		}
		want := migrations[version-1]
		if name != want.name || checksum != want.checksum() {
			return nil, fmt.Errorf("migration %d checksum mismatch", version)
		}
		applied[version] = checksum
		expectedVersion++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var pending []migration
	for _, m := range migrations {
		if err := m.validateDefinition(); err != nil {
			return nil, err
		}
		if _, ok := applied[m.version]; !ok {
			pending = append(pending, m)
		}
	}
	return pending, nil
}

func (d *DB) applyMigrations(pending []migration) error {
	for _, m := range pending {
		if err := m.validateDefinition(); err != nil {
			return err
		}
		if m.version == 13 {
			if err := d.validateV13LyricsSourceJobs(); err != nil {
				return fmt.Errorf("migration 13 preflight: %w", err)
			}
		}
		if err := d.applyMigration(m); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) applyMigration(m migration) error {
	foreignKeysOff := strings.Contains(m.sql, "-- migration:foreign_keys_off")
	var (
		conn *sql.Conn
		tx   *sql.Tx
		err  error
	)
	if foreignKeysOff {
		conn, err = d.Conn(context.Background())
		if err != nil {
			return err
		}
		defer conn.Close()
		if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys=OFF`); err != nil {
			return fmt.Errorf("migration %d disable foreign keys: %w", m.version, err)
		}
		defer conn.ExecContext(context.Background(), `PRAGMA foreign_keys=ON`)
		defer conn.ExecContext(context.Background(), `PRAGMA legacy_alter_table=OFF`)
		tx, err = conn.BeginTx(context.Background(), nil)
	} else {
		tx, err = d.Begin()
	}
	if err != nil {
		return err
	}
	rollback := func(err error) error {
		_ = tx.Rollback()
		return err
	}
	if m.before != nil {
		if err := m.before(tx); err != nil {
			return rollback(fmt.Errorf("migration %d preflight: %w", m.version, err))
		}
	}
	if _, err := tx.Exec(m.sql); err != nil {
		return rollback(fmt.Errorf("migration %d %s: %w", m.version, m.name, err))
	}
	if foreignKeysOff {
		rows, err := tx.Query(`PRAGMA foreign_key_check`)
		if err != nil {
			return rollback(fmt.Errorf("migration %d foreign key check: %w", m.version, err))
		}
		hasViolation := rows.Next()
		closeErr := rows.Close()
		if hasViolation || closeErr != nil {
			return rollback(fmt.Errorf("migration %d produced invalid foreign keys", m.version))
		}
	}
	if m.after != nil {
		if err := m.after(tx); err != nil {
			return rollback(fmt.Errorf("migration %d post-step: %w", m.version, err))
		}
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES (?, ?, ?, ?)`,
		m.version, m.name, m.checksum(), time.Now().Unix()); err != nil {
		return rollback(err)
	}
	if migrationBeforeCommitHook != nil {
		if err := migrationBeforeCommitHook(m.version); err != nil {
			return rollback(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if foreignKeysOff {
		if _, err := conn.ExecContext(context.Background(), `PRAGMA legacy_alter_table=OFF`); err != nil {
			return fmt.Errorf("migration %d restore alter-table semantics: %w", m.version, err)
		}
		if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys=ON`); err != nil {
			return fmt.Errorf("migration %d restore foreign keys: %w", m.version, err)
		}
		var enabled int
		if err := conn.QueryRowContext(context.Background(), `PRAGMA foreign_keys`).Scan(&enabled); err != nil || enabled != 1 {
			return fmt.Errorf("migration %d did not restore foreign key enforcement", m.version)
		}
	}
	return nil
}

func (d *DB) validateV13LyricsSourceJobs() error {
	rows, err := d.Query(`SELECT j.job_id, COUNT(DISTINCT json_extract(candidate.value, '$.sha1'))
		FROM lyrics_discovery_jobs j
		LEFT JOIN lyrics_discovery_shadow_results r ON r.job_id=j.job_id
		LEFT JOIN json_each(json_extract(r.result_json, '$.candidates')) candidate ON
			typeof(json_extract(candidate.value, '$.pageId'))='integer'
			AND json_extract(candidate.value, '$.pageId')=j.page_id
			AND typeof(json_extract(candidate.value, '$.revisionId'))='integer'
			AND json_extract(candidate.value, '$.revisionId')=j.revision_id
			AND typeof(json_extract(candidate.value, '$.sha1'))='text'
			AND length(json_extract(candidate.value, '$.sha1'))=40
			AND json_extract(candidate.value, '$.sha1')=lower(json_extract(candidate.value, '$.sha1'))
			AND json_extract(candidate.value, '$.sha1') NOT GLOB '*[^0-9a-f]*'
		WHERE j.kind='fetch_revision'
		GROUP BY j.job_id ORDER BY j.job_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var jobID int64
		var sha1Count int
		if err := rows.Scan(&jobID, &sha1Count); err != nil {
			return err
		}
		switch {
		case sha1Count == 0:
			return fmt.Errorf("lyrics discovery job %d has unreconcilable v12 fetch_revision expected_sha1", jobID)
		case sha1Count > 1:
			return fmt.Errorf("lyrics discovery job %d has conflicting v12 fetch_revision expected_sha1 values", jobID)
		}
	}
	return rows.Err()
}

func (d *DB) createPreMigrationBackup(version int) (string, error) {
	if strings.TrimSpace(d.path) == "" || strings.Contains(d.path, ":memory:") {
		return "", nil
	}
	backupPath := fmt.Sprintf("%s.pre-migration-v%d.bak", d.path, version)
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
	if err := os.Chmod(tempPath, 0o600); err != nil {
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
