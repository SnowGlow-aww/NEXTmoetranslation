package db

import (
	"database/sql"
	"fmt"
)

var migrationsV11ToV20 = []migration{
	{
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
	},
}

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
