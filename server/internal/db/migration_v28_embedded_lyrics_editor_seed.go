package db

const migrationV28EmbeddedLyricsEditorSeedSQL = `
CREATE TABLE embedded_lyrics_editor_seed_batches (
	seed_sha256                 TEXT PRIMARY KEY,
	archive_sha256              TEXT NOT NULL UNIQUE,
	release_id                  TEXT NOT NULL UNIQUE,
	schema_version              INTEGER NOT NULL,
	source_batch_sha256         TEXT NOT NULL,
	root_sha256                 TEXT NOT NULL,
	catalog_policy_version      TEXT NOT NULL,
	catalog_count               INTEGER NOT NULL,
	music_ids_sha256            TEXT NOT NULL,
	catalog_fingerprints_sha256 TEXT NOT NULL,
	created_at                  INTEGER NOT NULL,
	CHECK (schema_version=1),
	CHECK (length(seed_sha256)=64 AND seed_sha256=lower(seed_sha256) AND seed_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(archive_sha256)=64 AND archive_sha256=lower(archive_sha256) AND archive_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(release_id) BETWEEN 1 AND 256 AND release_id=trim(release_id)),
	CHECK (length(source_batch_sha256)=64 AND source_batch_sha256=lower(source_batch_sha256) AND source_batch_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(root_sha256)=64 AND root_sha256=lower(root_sha256) AND root_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(catalog_policy_version) BETWEEN 1 AND 128 AND catalog_policy_version=trim(catalog_policy_version)),
	CHECK (typeof(catalog_count)='integer' AND catalog_count BETWEEN 1 AND 100000),
	CHECK (length(music_ids_sha256)=64 AND music_ids_sha256=lower(music_ids_sha256) AND music_ids_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(catalog_fingerprints_sha256)=64 AND catalog_fingerprints_sha256=lower(catalog_fingerprints_sha256) AND catalog_fingerprints_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (typeof(created_at)='integer' AND created_at>0)
);
CREATE TRIGGER embedded_lyrics_editor_seed_batches_immutable_update
BEFORE UPDATE ON embedded_lyrics_editor_seed_batches
BEGIN SELECT RAISE(ABORT, 'embedded lyrics editor seed batches are immutable'); END;
CREATE TRIGGER embedded_lyrics_editor_seed_batches_immutable_delete
BEFORE DELETE ON embedded_lyrics_editor_seed_batches
WHEN EXISTS (SELECT 1 FROM embedded_lyrics_editor_seed_items WHERE seed_sha256=OLD.seed_sha256)
BEGIN SELECT RAISE(ABORT, 'embedded lyrics editor seed batches are immutable while items exist'); END;

CREATE TABLE embedded_lyrics_editor_seed_items (
	seed_sha256                 TEXT NOT NULL,
	music_id                    INTEGER NOT NULL,
	japanese_title              TEXT NOT NULL,
	catalog_fingerprint         TEXT NOT NULL,
	state                       TEXT NOT NULL,
	seed_kind                   TEXT NOT NULL,
	apply_status                TEXT NOT NULL,
	result_sha256               TEXT NOT NULL,
	source_document_sha256      TEXT NOT NULL,
	availability_schema_version INTEGER NOT NULL,
	reason_code                 TEXT NOT NULL,
	no_lyrics_reason            TEXT NOT NULL,
	availability_document_json  TEXT NOT NULL,
	availability_document_sha256 TEXT NOT NULL,
	created_at                  INTEGER NOT NULL,
	PRIMARY KEY (seed_sha256,music_id),
	CHECK (typeof(music_id)='integer' AND music_id>0),
	CHECK (length(japanese_title) BETWEEN 1 AND 2048 AND japanese_title=trim(japanese_title)),
	CHECK (length(catalog_fingerprint)=64 AND catalog_fingerprint=lower(catalog_fingerprint) AND catalog_fingerprint NOT GLOB '*[^0-9a-f]*'),
	CHECK (state IN ('complete','game_only','satisfied_no_lyrics','ambiguous','missing','incomplete','failed')),
	CHECK (seed_kind IN ('source_v3','legacy','availability')),
	CHECK (apply_status IN ('inserted','preserved_existing')),
	CHECK (length(result_sha256)=64 AND result_sha256=lower(result_sha256) AND result_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK ((seed_kind IN ('source_v3','legacy') AND length(source_document_sha256)=64 AND
	        source_document_sha256=lower(source_document_sha256) AND source_document_sha256 NOT GLOB '*[^0-9a-f]*' AND
	        availability_schema_version=0 AND reason_code='' AND no_lyrics_reason='' AND
	        availability_document_json='' AND availability_document_sha256='') OR
	       (seed_kind='availability' AND source_document_sha256='' AND availability_schema_version=1 AND
	        length(availability_document_json) BETWEEN 2 AND 16777216 AND json_valid(availability_document_json) AND
	        json_type(availability_document_json)='object' AND
	        json_type(availability_document_json,'$.schemaVersion')='integer' AND
	        json_extract(availability_document_json,'$.schemaVersion')=availability_schema_version AND
	        json_type(availability_document_json,'$.state')='text' AND
	        json_extract(availability_document_json,'$.state')=state AND
	        COALESCE(json_extract(availability_document_json,'$.reasonCode'),'')=reason_code AND
	        COALESCE(json_extract(availability_document_json,'$.noLyricsReason'),'')=no_lyrics_reason AND
	        length(availability_document_sha256)=64 AND availability_document_sha256=lower(availability_document_sha256) AND
	        availability_document_sha256 NOT GLOB '*[^0-9a-f]*')),
	CHECK ((seed_kind='source_v3' AND state IN ('complete','game_only')) OR
	       (seed_kind='legacy' AND state='complete') OR
	       (seed_kind='availability' AND state IN ('satisfied_no_lyrics','ambiguous','missing','incomplete','failed'))),
	CHECK (typeof(created_at)='integer' AND created_at>0),
	FOREIGN KEY (seed_sha256) REFERENCES embedded_lyrics_editor_seed_batches(seed_sha256) ON DELETE CASCADE,
	FOREIGN KEY (music_id) REFERENCES catalog_music(music_id) ON DELETE RESTRICT
);
CREATE INDEX idx_embedded_lyrics_editor_seed_items_music
	ON embedded_lyrics_editor_seed_items(music_id,created_at,seed_sha256);
CREATE INDEX idx_embedded_lyrics_editor_seed_items_availability
	ON embedded_lyrics_editor_seed_items(seed_kind,apply_status,state,music_id);
CREATE TRIGGER embedded_lyrics_editor_seed_items_immutable_update
BEFORE UPDATE ON embedded_lyrics_editor_seed_items
BEGIN SELECT RAISE(ABORT, 'embedded lyrics editor seed items are immutable'); END;
CREATE TRIGGER embedded_lyrics_editor_seed_items_immutable_delete
BEFORE DELETE ON embedded_lyrics_editor_seed_items
WHEN EXISTS (SELECT 1 FROM embedded_lyrics_editor_seed_batches WHERE seed_sha256=OLD.seed_sha256)
BEGIN SELECT RAISE(ABORT, 'embedded lyrics editor seed items are immutable'); END;
`
