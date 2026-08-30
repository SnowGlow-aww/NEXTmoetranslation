package db

const migrationV27LyricsRenditionsSQL = `
-- migration:foreign_keys_off
-- v27 is additive for source/recovery v3. Existing v26 credit columns and
-- every pre-existing document byte are copied without rewriting.
CREATE TEMP TABLE lyrics_v27_sequences (
	name TEXT PRIMARY KEY,
	seq  INTEGER NOT NULL
);
INSERT INTO lyrics_v27_sequences(name,seq)
SELECT name,seq FROM sqlite_sequence
WHERE name IN ('song_lyrics_availability_documents','song_lyrics_source_documents');
PRAGMA legacy_alter_table=ON;

-- v21-v23 migration checksums are pinned. Replace only the live v27
-- persistence surfaces so the final schema matches the seven-value model
-- reason-code closure without rewriting durable migration history.
DROP TRIGGER lyrics_discovery_fixed_candidate_validate_insert;
DROP TRIGGER lyrics_discovery_fixed_candidate_validate_update;
DROP TRIGGER lyrics_source_renditions_identity_validate_insert;
DROP TRIGGER song_lyrics_source_artifacts_identity_validate_insert;
DROP VIEW lyrics_source_fixed_identity_violations;
DROP VIEW lyrics_source_fixed_identity_rows;
DROP VIEW lyrics_discovery_job_identity_violations;

-- v24 recovery tables are rebuilt here because their v24 checksum is pinned.
DROP INDEX IF EXISTS idx_lyrics_recovery_import_artifacts_provider;
ALTER TABLE lyrics_recovery_import_artifacts RENAME TO lyrics_recovery_import_artifacts_v26;
CREATE TABLE lyrics_recovery_import_artifacts (
	batch_sha256             TEXT NOT NULL,
	music_id                 INTEGER NOT NULL,
	provider                 TEXT NOT NULL,
	rendition_key            TEXT NOT NULL,
	origin                   TEXT NOT NULL,
	page_id                  INTEGER NOT NULL,
	revision_id              INTEGER NOT NULL,
	revision_timestamp       TEXT NOT NULL,
	mediawiki_sha1           TEXT NOT NULL,
	page_title               TEXT NOT NULL,
	canonical_revision_url   TEXT NOT NULL,
	fetched_at               TEXT NOT NULL,
	categories_json          TEXT NOT NULL,
	section                  TEXT NOT NULL,
	composition_rendition_key TEXT NOT NULL,
	version_reason           TEXT NOT NULL,
	index_evidence_refs_json TEXT NOT NULL,
	fixed_identity_json      TEXT NOT NULL,
	fixed_identity_sha256    TEXT NOT NULL,
	raw_byte_count           INTEGER NOT NULL,
	raw_wikitext_sha256      TEXT NOT NULL,
	artifact_sha256          TEXT NOT NULL,
	created_at               INTEGER NOT NULL,
	PRIMARY KEY (batch_sha256,music_id,rendition_key),
	UNIQUE (batch_sha256,music_id,fixed_identity_sha256),
	CHECK (provider IN ('vocaloid_fandom','moegirl','moegirl_public_exact','sekaipedia')),
	CHECK ((provider='vocaloid_fandom' AND origin='https://vocaloid.fandom.com') OR
	       (provider='moegirl' AND origin='https://moegirl.icu') OR
	       (provider='moegirl_public_exact' AND origin='https://zh.moegirl.org.cn') OR
	       (provider='sekaipedia' AND origin='https://www.sekaipedia.org')),
	CHECK (length(rendition_key) BETWEEN 1 AND 128 AND rendition_key=lower(rendition_key) AND
	       substr(rendition_key,1,1) GLOB '[a-z0-9]' AND rendition_key NOT GLOB '*[^a-z0-9._-]*'),
	CHECK (typeof(page_id)='integer' AND page_id>0 AND typeof(revision_id)='integer' AND revision_id>0),
	CHECK (length(mediawiki_sha1)=40 AND mediawiki_sha1=lower(mediawiki_sha1) AND mediawiki_sha1 NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(page_title) BETWEEN 1 AND 2048 AND page_title=trim(page_title)),
	CHECK (length(canonical_revision_url) BETWEEN 1 AND 4096 AND canonical_revision_url=trim(canonical_revision_url) AND
	       substr(canonical_revision_url,1,length(origin))=origin),
	CHECK ((provider='sekaipedia' AND length(revision_timestamp) BETWEEN 20 AND 30 AND
	        revision_timestamp=trim(revision_timestamp) AND substr(revision_timestamp,-1)='Z') OR
	       (provider<>'sekaipedia' AND revision_timestamp='')),
	CHECK (length(fetched_at) BETWEEN 20 AND 35 AND fetched_at=trim(fetched_at) AND substr(fetched_at,-1)='Z'),
	CHECK (json_valid(categories_json) AND json_type(categories_json)='array'),
	CHECK (length(section) BETWEEN 1 AND 512 AND section=trim(section)),
	CHECK (composition_rendition_key='' OR
	       (length(composition_rendition_key) BETWEEN 1 AND 128 AND composition_rendition_key=lower(composition_rendition_key) AND
	        substr(composition_rendition_key,1,1) GLOB '[a-z0-9]' AND composition_rendition_key NOT GLOB '*[^a-z0-9._-]*')),
	CHECK (version_reason='' OR version_reason IN
	       ('tagged_full_and_game','tagged_game_only','tagged_game_only_full_from_vocaloid','untagged_uncut_identity',
	        'untagged_game_subset','untagged_full_only','version_conflict')),
	CHECK (json_valid(index_evidence_refs_json) AND json_type(index_evidence_refs_json)='array' AND
	       json_array_length(index_evidence_refs_json) BETWEEN 1 AND 64),
	CHECK (length(fixed_identity_json) BETWEEN 2 AND 1048576 AND json_valid(fixed_identity_json) AND json_type(fixed_identity_json)='object'),
	CHECK (length(fixed_identity_sha256)=64 AND fixed_identity_sha256=lower(fixed_identity_sha256) AND fixed_identity_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (typeof(raw_byte_count)='integer' AND raw_byte_count BETWEEN 1 AND 2097152),
	CHECK (length(raw_wikitext_sha256)=64 AND raw_wikitext_sha256=lower(raw_wikitext_sha256) AND raw_wikitext_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(artifact_sha256)=64 AND artifact_sha256=lower(artifact_sha256) AND artifact_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (typeof(created_at)='integer' AND created_at>0),
	FOREIGN KEY (batch_sha256,music_id) REFERENCES lyrics_recovery_import_items(batch_sha256,music_id) ON DELETE CASCADE
);

INSERT INTO lyrics_recovery_import_artifacts
	(batch_sha256,music_id,provider,rendition_key,origin,page_id,revision_id,revision_timestamp,mediawiki_sha1,
	 page_title,canonical_revision_url,fetched_at,categories_json,section,composition_rendition_key,version_reason,
	 index_evidence_refs_json,fixed_identity_json,fixed_identity_sha256,raw_byte_count,raw_wikitext_sha256,
	 artifact_sha256,created_at)
SELECT batch_sha256,music_id,provider,rendition_key,origin,page_id,revision_id,revision_timestamp,mediawiki_sha1,
	 page_title,canonical_revision_url,fetched_at,categories_json,section,composition_rendition_key,version_reason,
	 index_evidence_refs_json,fixed_identity_json,fixed_identity_sha256,raw_byte_count,raw_wikitext_sha256,
	 artifact_sha256,created_at
FROM lyrics_recovery_import_artifacts_v26 ORDER BY batch_sha256,music_id,rendition_key;
DROP TABLE lyrics_recovery_import_artifacts_v26;
CREATE INDEX idx_lyrics_recovery_import_artifacts_provider
	ON lyrics_recovery_import_artifacts(provider,page_id,revision_id,rendition_key);
CREATE TRIGGER lyrics_recovery_import_artifacts_state_insert
BEFORE INSERT ON lyrics_recovery_import_artifacts
WHEN (SELECT state FROM lyrics_recovery_import_items
      WHERE batch_sha256=NEW.batch_sha256 AND music_id=NEW.music_id) NOT IN ('complete','game_only')
BEGIN SELECT RAISE(ABORT, 'text-free lyrics recovery items cannot own artifacts'); END;
CREATE TRIGGER lyrics_recovery_import_artifacts_immutable_update
BEFORE UPDATE ON lyrics_recovery_import_artifacts
BEGIN SELECT RAISE(ABORT, 'lyrics recovery import artifacts are immutable'); END;
CREATE TRIGGER lyrics_recovery_import_artifacts_immutable_delete
BEFORE DELETE ON lyrics_recovery_import_artifacts
WHEN EXISTS (SELECT 1 FROM lyrics_recovery_import_items
	WHERE batch_sha256=OLD.batch_sha256 AND music_id=OLD.music_id)
BEGIN SELECT RAISE(ABORT, 'lyrics recovery import artifacts are immutable'); END;

-- Availability documents carry the same closed Game-only reason set.
DROP INDEX IF EXISTS idx_song_lyrics_availability_documents_music;
ALTER TABLE song_lyrics_availability_documents RENAME TO song_lyrics_availability_documents_v26;
CREATE TABLE song_lyrics_availability_documents (
	availability_document_id INTEGER PRIMARY KEY AUTOINCREMENT,
	batch_sha256              TEXT NOT NULL,
	music_id                  INTEGER NOT NULL,
	schema_version            INTEGER NOT NULL,
	state                     TEXT NOT NULL,
	reason_code               TEXT NOT NULL,
	no_lyrics_reason          TEXT NOT NULL,
	document_json             TEXT NOT NULL,
	document_sha256           TEXT NOT NULL,
	result_sha256             TEXT NOT NULL,
	created_at                INTEGER NOT NULL,
	UNIQUE (batch_sha256,music_id),
	CHECK (schema_version=1),
	CHECK (state IN ('game_only','satisfied_no_lyrics','ambiguous','missing','incomplete','failed')),
	CHECK ((state='game_only' AND reason_code IN ('tagged_game_only','tagged_game_only_full_from_vocaloid') AND no_lyrics_reason='') OR
	       (state='satisfied_no_lyrics' AND reason_code='' AND no_lyrics_reason='catalog_instrumental') OR
	       (state IN ('ambiguous','missing','incomplete','failed') AND reason_code='version_conflict' AND no_lyrics_reason='')),
	CHECK (length(document_json) BETWEEN 2 AND 16777216 AND json_valid(document_json) AND json_type(document_json)='object'),
	CHECK (json_type(document_json,'$.schemaVersion')='integer' AND json_extract(document_json,'$.schemaVersion')=schema_version),
	CHECK (json_type(document_json,'$.state')='text' AND json_extract(document_json,'$.state')=state),
	CHECK (COALESCE(json_extract(document_json,'$.reasonCode'),'')=reason_code),
	CHECK (COALESCE(json_extract(document_json,'$.noLyricsReason'),'')=no_lyrics_reason),
	CHECK (length(document_sha256)=64 AND document_sha256=lower(document_sha256) AND document_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(result_sha256)=64 AND result_sha256=lower(result_sha256) AND result_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (typeof(created_at)='integer' AND created_at>0),
	FOREIGN KEY (batch_sha256,music_id) REFERENCES lyrics_recovery_import_items(batch_sha256,music_id) ON DELETE CASCADE
);

INSERT INTO song_lyrics_availability_documents
	(availability_document_id,batch_sha256,music_id,schema_version,state,reason_code,no_lyrics_reason,
	 document_json,document_sha256,result_sha256,created_at)
SELECT availability_document_id,batch_sha256,music_id,schema_version,state,reason_code,no_lyrics_reason,
	 document_json,document_sha256,result_sha256,created_at
FROM song_lyrics_availability_documents_v26 ORDER BY availability_document_id;
DROP TABLE song_lyrics_availability_documents_v26;
CREATE INDEX idx_song_lyrics_availability_documents_music
	ON song_lyrics_availability_documents(music_id,batch_sha256);
CREATE TRIGGER song_lyrics_availability_documents_item_insert
BEFORE INSERT ON song_lyrics_availability_documents
WHEN NOT EXISTS (
	SELECT 1 FROM lyrics_recovery_import_items AS item
	WHERE item.batch_sha256=NEW.batch_sha256 AND item.music_id=NEW.music_id AND item.state=NEW.state AND
	      item.result_sha256=NEW.result_sha256 AND item.availability_document_sha256=NEW.document_sha256
)
BEGIN SELECT RAISE(ABORT, 'lyrics availability document does not match its recovery item'); END;
CREATE TRIGGER song_lyrics_availability_documents_immutable_update
BEFORE UPDATE ON song_lyrics_availability_documents
BEGIN SELECT RAISE(ABORT, 'song lyrics availability documents are immutable'); END;
CREATE TRIGGER song_lyrics_availability_documents_immutable_delete
BEFORE DELETE ON song_lyrics_availability_documents
WHEN EXISTS (
	SELECT 1 FROM lyrics_recovery_import_items
	WHERE batch_sha256=OLD.batch_sha256 AND music_id=OLD.music_id
)
BEGIN SELECT RAISE(ABORT, 'song lyrics availability documents are immutable'); END;


DROP INDEX idx_song_lyrics_source_artifacts_provider;
ALTER TABLE song_lyrics_source_artifacts RENAME TO song_lyrics_source_artifacts_v26;
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
	       ('tagged_full_and_game','tagged_game_only','tagged_game_only_full_from_vocaloid','untagged_uncut_identity',
	        'untagged_game_subset','untagged_full_only','version_conflict')),
	CHECK (json_valid(index_evidence_refs_json) AND json_type(index_evidence_refs_json)='array' AND json_array_length(index_evidence_refs_json) BETWEEN 1 AND 64),
	CHECK (length(fixed_identity_json) BETWEEN 2 AND 1048576 AND json_valid(fixed_identity_json) AND json_type(fixed_identity_json)='object'),
	CHECK (length(fixed_identity_sha256)=64 AND fixed_identity_sha256=lower(fixed_identity_sha256) AND fixed_identity_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (typeof(raw_byte_count)='integer' AND raw_byte_count BETWEEN 1 AND 2097152),
	CHECK (length(raw_wikitext_sha256)=64 AND raw_wikitext_sha256=lower(raw_wikitext_sha256) AND raw_wikitext_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(artifact_sha256)=64 AND artifact_sha256=lower(artifact_sha256) AND artifact_sha256 NOT GLOB '*[^0-9a-f]*'),
	FOREIGN KEY (document_id) REFERENCES song_lyrics_source_documents(document_id) ON DELETE CASCADE
);
INSERT INTO song_lyrics_source_artifacts
	(document_id,provider,rendition_key,origin,page_id,revision_id,revision_timestamp,mediawiki_sha1,page_title,
	 canonical_revision_url,fetched_at,categories_json,section,composition_rendition_key,version_reason,
	 index_evidence_refs_json,fixed_identity_json,fixed_identity_sha256,raw_byte_count,raw_wikitext_sha256,artifact_sha256)
SELECT document_id,provider,rendition_key,origin,page_id,revision_id,revision_timestamp,mediawiki_sha1,page_title,
	canonical_revision_url,fetched_at,categories_json,section,composition_rendition_key,version_reason,
	index_evidence_refs_json,fixed_identity_json,fixed_identity_sha256,raw_byte_count,raw_wikitext_sha256,artifact_sha256
FROM song_lyrics_source_artifacts_v26 ORDER BY document_id,rendition_key;
DROP TABLE song_lyrics_source_artifacts_v26;
CREATE INDEX idx_song_lyrics_source_artifacts_provider
	ON song_lyrics_source_artifacts(provider,page_id,revision_id,rendition_key);
CREATE TRIGGER song_lyrics_source_artifacts_immutable_update BEFORE UPDATE ON song_lyrics_source_artifacts
BEGIN SELECT RAISE(ABORT, 'song lyrics source artifacts are immutable'); END;
CREATE TRIGGER song_lyrics_source_artifacts_immutable_delete BEFORE DELETE ON song_lyrics_source_artifacts
WHEN EXISTS (SELECT 1 FROM song_lyrics_source_documents WHERE document_id=OLD.document_id)
BEGIN SELECT RAISE(ABORT, 'song lyrics source artifacts are immutable'); END;

DROP INDEX IF EXISTS idx_lyrics_recovery_import_items_music;
DROP INDEX IF EXISTS idx_lyrics_recovery_import_items_state;
ALTER TABLE lyrics_recovery_import_items RENAME TO lyrics_recovery_import_items_v26;
CREATE TABLE lyrics_recovery_import_items (
	batch_sha256                 TEXT NOT NULL,
	music_id                     INTEGER NOT NULL,
	japanese_title               TEXT NOT NULL,
	catalog_fingerprint          TEXT NOT NULL,
	target_music_id              INTEGER NOT NULL,
	association_music_ids_json   TEXT NOT NULL,
	state                        TEXT NOT NULL,
	result_sha256                TEXT NOT NULL,
	draft_sha256                 TEXT NOT NULL,
	document_sha256              TEXT NOT NULL,
	availability_document_sha256 TEXT NOT NULL,
	created_at                   INTEGER NOT NULL,
	PRIMARY KEY (batch_sha256,music_id),
	CHECK (typeof(music_id)='integer' AND music_id>0),
	CHECK (typeof(target_music_id)='integer' AND target_music_id=music_id),
	CHECK (length(japanese_title) BETWEEN 1 AND 2048 AND japanese_title=trim(japanese_title)),
	CHECK (length(catalog_fingerprint)=64 AND catalog_fingerprint=lower(catalog_fingerprint) AND catalog_fingerprint NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(association_music_ids_json) BETWEEN 2 AND 1048576 AND json_valid(association_music_ids_json) AND json_type(association_music_ids_json)='array'),
	CHECK (state IN ('complete','game_only','satisfied_no_lyrics','ambiguous','missing','incomplete','failed')),
	CHECK (length(result_sha256)=64 AND result_sha256=lower(result_sha256) AND result_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (((state IN ('complete','game_only') AND length(draft_sha256)=64 AND draft_sha256=lower(draft_sha256) AND draft_sha256 NOT GLOB '*[^0-9a-f]*' AND length(document_sha256)=64 AND document_sha256=lower(document_sha256) AND document_sha256 NOT GLOB '*[^0-9a-f]*' AND availability_document_sha256='') OR
	       (state='game_only' AND draft_sha256='' AND document_sha256='' AND length(availability_document_sha256)=64 AND availability_document_sha256=lower(availability_document_sha256) AND availability_document_sha256 NOT GLOB '*[^0-9a-f]*') OR
	       (state NOT IN ('complete','game_only') AND draft_sha256='' AND document_sha256='' AND length(availability_document_sha256)=64 AND availability_document_sha256=lower(availability_document_sha256) AND availability_document_sha256 NOT GLOB '*[^0-9a-f]*'))),
	CHECK (typeof(created_at)='integer' AND created_at>0),
	FOREIGN KEY (batch_sha256) REFERENCES lyrics_recovery_import_batches(batch_sha256) ON DELETE CASCADE
);
INSERT INTO lyrics_recovery_import_items
	(batch_sha256,music_id,japanese_title,catalog_fingerprint,target_music_id,association_music_ids_json,
	 state,result_sha256,draft_sha256,document_sha256,availability_document_sha256,created_at)
SELECT batch_sha256,music_id,japanese_title,catalog_fingerprint,target_music_id,association_music_ids_json,
	 state,result_sha256,draft_sha256,document_sha256,availability_document_sha256,created_at
FROM lyrics_recovery_import_items_v26;
DROP TABLE lyrics_recovery_import_items_v26;
CREATE INDEX idx_lyrics_recovery_import_items_music ON lyrics_recovery_import_items(music_id,batch_sha256);
CREATE INDEX idx_lyrics_recovery_import_items_state ON lyrics_recovery_import_items(batch_sha256,state,music_id);
CREATE TRIGGER lyrics_recovery_import_items_immutable_update BEFORE UPDATE ON lyrics_recovery_import_items
BEGIN SELECT RAISE(ABORT, 'lyrics recovery import items are immutable'); END;
CREATE TRIGGER lyrics_recovery_import_items_immutable_delete BEFORE DELETE ON lyrics_recovery_import_items
WHEN EXISTS (SELECT 1 FROM lyrics_recovery_import_batches WHERE batch_sha256=OLD.batch_sha256)
BEGIN SELECT RAISE(ABORT, 'lyrics recovery import items are immutable'); END;

ALTER TABLE song_lyrics_component_contributions RENAME TO song_lyrics_component_contributions_v26;
CREATE TABLE song_lyrics_component_contributions (
	document_id         INTEGER NOT NULL,
	component           TEXT NOT NULL,
	rendition_key       TEXT NOT NULL,
	contribution_sha256 TEXT NOT NULL,
	PRIMARY KEY (document_id,component),
	CHECK (length(component) BETWEEN 1 AND 512),
	CHECK (length(rendition_key) BETWEEN 1 AND 256),
	CHECK (length(contribution_sha256)=64 AND contribution_sha256=lower(contribution_sha256) AND contribution_sha256 NOT GLOB '*[^0-9a-f]*'),
	FOREIGN KEY (document_id) REFERENCES song_lyrics_source_documents(document_id) ON DELETE CASCADE,
	FOREIGN KEY (document_id,rendition_key)
		REFERENCES song_lyrics_source_artifacts(document_id,rendition_key) ON DELETE CASCADE
);
INSERT INTO song_lyrics_component_contributions
	(document_id,component,rendition_key,contribution_sha256)
SELECT document_id,component,rendition_key,contribution_sha256
FROM song_lyrics_component_contributions_v26;
DROP TABLE song_lyrics_component_contributions_v26;
CREATE TRIGGER song_lyrics_component_contributions_immutable_update
BEFORE UPDATE ON song_lyrics_component_contributions
BEGIN SELECT RAISE(ABORT, 'song lyrics component contributions are immutable'); END;
CREATE TRIGGER song_lyrics_component_contributions_immutable_delete
BEFORE DELETE ON song_lyrics_component_contributions
WHEN EXISTS (SELECT 1 FROM song_lyrics_source_documents WHERE document_id=OLD.document_id)
BEGIN SELECT RAISE(ABORT, 'song lyrics component contributions are immutable'); END;

ALTER TABLE lyrics_recovery_import_component_contributions RENAME TO lyrics_recovery_import_component_contributions_v26;
CREATE TABLE lyrics_recovery_import_component_contributions (
	batch_sha256       TEXT NOT NULL,
	music_id           INTEGER NOT NULL,
	component          TEXT NOT NULL,
	rendition_key      TEXT NOT NULL,
	contribution_sha256 TEXT NOT NULL,
	PRIMARY KEY (batch_sha256,music_id,component),
	CHECK (length(component) BETWEEN 1 AND 512),
	CHECK (length(rendition_key) BETWEEN 1 AND 256),
	CHECK (length(contribution_sha256)=64 AND contribution_sha256=lower(contribution_sha256) AND contribution_sha256 NOT GLOB '*[^0-9a-f]*'),
	FOREIGN KEY (batch_sha256,music_id,rendition_key)
		REFERENCES lyrics_recovery_import_artifacts(batch_sha256,music_id,rendition_key) ON DELETE CASCADE
);
INSERT INTO lyrics_recovery_import_component_contributions
	(batch_sha256,music_id,component,rendition_key,contribution_sha256)
SELECT batch_sha256,music_id,component,rendition_key,contribution_sha256
FROM lyrics_recovery_import_component_contributions_v26;
DROP TABLE lyrics_recovery_import_component_contributions_v26;
CREATE TRIGGER lyrics_recovery_import_component_contributions_immutable_update
BEFORE UPDATE ON lyrics_recovery_import_component_contributions
BEGIN SELECT RAISE(ABORT, 'lyrics recovery component contributions are immutable'); END;
CREATE TRIGGER lyrics_recovery_import_component_contributions_immutable_delete
BEFORE DELETE ON lyrics_recovery_import_component_contributions
WHEN EXISTS (SELECT 1 FROM lyrics_recovery_import_items WHERE batch_sha256=OLD.batch_sha256 AND music_id=OLD.music_id)
BEGIN SELECT RAISE(ABORT, 'lyrics recovery component contributions are immutable'); END;

ALTER TABLE song_lyrics_source_documents RENAME TO song_lyrics_source_documents_v26;
CREATE TABLE song_lyrics_source_documents (
	document_id          INTEGER PRIMARY KEY AUTOINCREMENT,
	music_id             INTEGER NOT NULL UNIQUE,
	schema_version       INTEGER NOT NULL,
	reason_code          TEXT NOT NULL,
	document_json        TEXT NOT NULL,
	document_sha256      TEXT NOT NULL UNIQUE,
	manifest_batch_sha256 TEXT NOT NULL,
	created_at           INTEGER NOT NULL,
	CHECK (schema_version IN (1,2,3)),
	CHECK ((schema_version=3 AND reason_code='') OR (schema_version IN (1,2) AND reason_code IN ('tagged_full_and_game','tagged_game_only','tagged_game_only_full_from_vocaloid','untagged_uncut_identity','untagged_game_subset','untagged_full_only','version_conflict'))),
	CHECK (reason_code<>'version_conflict'),
	CHECK (length(document_json) BETWEEN 2 AND 16777216 AND json_valid(document_json) AND json_type(document_json)='object'),
	CHECK (length(document_sha256)=64 AND document_sha256=lower(document_sha256) AND document_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(manifest_batch_sha256)=64 AND manifest_batch_sha256=lower(manifest_batch_sha256) AND manifest_batch_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (typeof(created_at)='integer' AND created_at>0),
	FOREIGN KEY (music_id) REFERENCES catalog_music(music_id) ON DELETE CASCADE
);
INSERT INTO song_lyrics_source_documents(document_id,music_id,schema_version,reason_code,document_json,document_sha256,manifest_batch_sha256,created_at)
SELECT document_id,music_id,schema_version,reason_code,document_json,document_sha256,manifest_batch_sha256,created_at
FROM song_lyrics_source_documents_v26;
DROP TABLE song_lyrics_source_documents_v26;
CREATE TRIGGER song_lyrics_source_documents_immutable_update BEFORE UPDATE ON song_lyrics_source_documents
BEGIN SELECT RAISE(ABORT, 'song lyrics source documents are immutable'); END;

CREATE TABLE song_lyrics_rendition_localizations (
	document_id       INTEGER NOT NULL,
	rendition_key     TEXT NOT NULL,
	locale            TEXT NOT NULL,
	translation_credit TEXT NOT NULL DEFAULT '',
	proofreading_credit TEXT NOT NULL DEFAULT '',
	updated_at        INTEGER NOT NULL DEFAULT 0,
	updated_by        TEXT NOT NULL DEFAULT '',
	revision           INTEGER NOT NULL DEFAULT 1,
	PRIMARY KEY (document_id,rendition_key,locale),
	CHECK (length(rendition_key) BETWEEN 1 AND 256 AND rendition_key=trim(rendition_key)),
	CHECK (length(locale) BETWEEN 2 AND 32 AND locale=trim(locale)),
	CHECK (length(translation_credit) <= 2048 AND length(proofreading_credit) <= 2048),
	CHECK (typeof(updated_at)='integer' AND updated_at>=0),
	CHECK (typeof(revision)='integer' AND revision>0),
	FOREIGN KEY (document_id) REFERENCES song_lyrics_source_documents(document_id) ON DELETE CASCADE
);
CREATE TABLE song_lyrics_rendition_translation_lines (
	document_id   INTEGER NOT NULL,
	rendition_key TEXT NOT NULL,
	locale        TEXT NOT NULL,
	position      INTEGER NOT NULL,
	text          TEXT NOT NULL,
	PRIMARY KEY (document_id,rendition_key,locale,position),
	CHECK (typeof(position)='integer' AND position>=0),
	CHECK (length(text) <= 16384),
	FOREIGN KEY (document_id,rendition_key,locale)
		REFERENCES song_lyrics_rendition_localizations(document_id,rendition_key,locale) ON DELETE CASCADE
);
CREATE INDEX idx_song_lyrics_rendition_localizations_lookup
	ON song_lyrics_rendition_localizations(rendition_key,locale,document_id);
CREATE INDEX idx_song_lyrics_rendition_translation_lines_lookup
	ON song_lyrics_rendition_translation_lines(document_id,rendition_key,locale,position);
CREATE TRIGGER song_lyrics_source_documents_immutable_delete BEFORE DELETE ON song_lyrics_source_documents
WHEN EXISTS (SELECT 1 FROM song_lyrics WHERE music_id=OLD.music_id) OR
     EXISTS (SELECT 1 FROM song_lyrics_rendition_localizations WHERE document_id=OLD.document_id)
BEGIN SELECT RAISE(ABORT, 'song lyrics source documents are immutable'); END;
-- Recreate the final identity guards with tagged_game_only included in the
-- same closed model-defined reason set.
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
	       ('tagged_full_and_game','tagged_game_only','tagged_game_only_full_from_vocaloid','untagged_uncut_identity',
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
	    ('tagged_full_and_game','tagged_game_only','tagged_game_only_full_from_vocaloid','untagged_uncut_identity',
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

UPDATE sqlite_sequence
SET seq=MAX(seq,(SELECT saved.seq FROM lyrics_v27_sequences AS saved WHERE saved.name=sqlite_sequence.name))
WHERE name IN (SELECT name FROM lyrics_v27_sequences);
INSERT INTO sqlite_sequence(name,seq)
SELECT saved.name,saved.seq FROM lyrics_v27_sequences AS saved
WHERE NOT EXISTS (SELECT 1 FROM sqlite_sequence AS current WHERE current.name=saved.name);
DROP TABLE temp.lyrics_v27_sequences;
PRAGMA legacy_alter_table=OFF;

`
