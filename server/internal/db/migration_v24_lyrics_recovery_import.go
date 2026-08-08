package db

const migrationV24LyricsRecoveryImportSQL = `
-- Recovery imports are an additive all-root ledger. Existing Full source
-- document v1 tables remain byte- and schema-compatible; recovery-owned
-- artifacts and evidence live in the separate graph below so exact public-page
-- providers and non-Full availability states never weaken the legacy checks.
CREATE TABLE lyrics_recovery_import_batches (
	batch_sha256           TEXT PRIMARY KEY,
	schema_version         INTEGER NOT NULL,
	root_schema_version    INTEGER NOT NULL,
	root_id                TEXT NOT NULL,
	root_sha256            TEXT NOT NULL UNIQUE,
	catalog_count          INTEGER NOT NULL,
	music_ids_sha256       TEXT NOT NULL,
	coverage_json          TEXT NOT NULL,
	evidence_receipt_sha256 TEXT NOT NULL,
	pack_sha256            TEXT NOT NULL,
	selection_sha256       TEXT NOT NULL,
	evidence_count         INTEGER NOT NULL,
	shard_count            INTEGER NOT NULL,
	raw_byte_count         INTEGER NOT NULL,
	encoded_byte_count     INTEGER NOT NULL,
	actor                   TEXT NOT NULL,
	created_at              INTEGER NOT NULL,
	CHECK (schema_version=1),
	CHECK (root_schema_version=2),
	CHECK (length(batch_sha256)=64 AND batch_sha256=lower(batch_sha256) AND batch_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(root_id) BETWEEN 1 AND 256 AND root_id=trim(root_id) AND root_id NOT GLOB '*[^A-Za-z0-9._:/-]*'),
	CHECK (length(root_sha256)=64 AND root_sha256=lower(root_sha256) AND root_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (typeof(catalog_count)='integer' AND catalog_count BETWEEN 1 AND 100000),
	CHECK (length(music_ids_sha256)=64 AND music_ids_sha256=lower(music_ids_sha256) AND music_ids_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(coverage_json) BETWEEN 2 AND 1048576 AND json_valid(coverage_json) AND json_type(coverage_json)='object'),
	CHECK (json_type(coverage_json,'$.total')='integer' AND json_extract(coverage_json,'$.total')=catalog_count),
	CHECK (length(evidence_receipt_sha256)=64 AND evidence_receipt_sha256=lower(evidence_receipt_sha256) AND evidence_receipt_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(pack_sha256)=64 AND pack_sha256=lower(pack_sha256) AND pack_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(selection_sha256)=64 AND selection_sha256=lower(selection_sha256) AND selection_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (typeof(evidence_count)='integer' AND evidence_count BETWEEN 0 AND 65536),
	CHECK (typeof(shard_count)='integer' AND shard_count BETWEEN 0 AND 65536),
	CHECK (typeof(raw_byte_count)='integer' AND raw_byte_count BETWEEN 0 AND 536870912),
	CHECK (typeof(encoded_byte_count)='integer' AND encoded_byte_count BETWEEN 0 AND 1073741824),
	CHECK ((evidence_count=0 AND shard_count=0 AND raw_byte_count=0 AND encoded_byte_count=0) OR
	       (evidence_count>0 AND shard_count>0 AND raw_byte_count>0 AND encoded_byte_count>0)),
	CHECK (length(actor) BETWEEN 1 AND 128 AND actor=trim(actor)),
	CHECK (typeof(created_at)='integer' AND created_at>0)
);
CREATE TRIGGER lyrics_recovery_import_batches_immutable_update
BEFORE UPDATE ON lyrics_recovery_import_batches
BEGIN SELECT RAISE(ABORT, 'lyrics recovery import batches are immutable'); END;
CREATE TRIGGER lyrics_recovery_import_batches_guarded_delete
BEFORE DELETE ON lyrics_recovery_import_batches
WHEN EXISTS (
	SELECT 1 FROM lyrics_recovery_import_items AS item
	JOIN catalog_music AS music ON music.music_id=item.music_id
	WHERE item.batch_sha256=OLD.batch_sha256
)
BEGIN SELECT RAISE(ABORT, 'lyrics recovery import batches are immutable while their catalog exists'); END;

CREATE TABLE lyrics_recovery_import_items (
	batch_sha256                 TEXT NOT NULL,
	music_id                     INTEGER NOT NULL,
	japanese_title               TEXT NOT NULL,
	catalog_fingerprint          TEXT NOT NULL,
	target_music_id               INTEGER NOT NULL,
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
	CHECK ((state='complete' AND
	        length(draft_sha256)=64 AND draft_sha256=lower(draft_sha256) AND draft_sha256 NOT GLOB '*[^0-9a-f]*' AND
	        length(document_sha256)=64 AND document_sha256=lower(document_sha256) AND document_sha256 NOT GLOB '*[^0-9a-f]*' AND
	        availability_document_sha256='') OR
	       (state<>'complete' AND draft_sha256='' AND document_sha256='' AND
	        length(availability_document_sha256)=64 AND availability_document_sha256=lower(availability_document_sha256) AND
	        availability_document_sha256 NOT GLOB '*[^0-9a-f]*')),
	CHECK (typeof(created_at)='integer' AND created_at>0),
	FOREIGN KEY (batch_sha256) REFERENCES lyrics_recovery_import_batches(batch_sha256) ON DELETE CASCADE
);
CREATE INDEX idx_lyrics_recovery_import_items_music
	ON lyrics_recovery_import_items(music_id,batch_sha256);
CREATE INDEX idx_lyrics_recovery_import_items_state
	ON lyrics_recovery_import_items(batch_sha256,state,music_id);
CREATE TRIGGER lyrics_recovery_import_items_immutable_update
BEFORE UPDATE ON lyrics_recovery_import_items
BEGIN SELECT RAISE(ABORT, 'lyrics recovery import items are immutable'); END;
CREATE TRIGGER lyrics_recovery_import_items_immutable_delete
BEFORE DELETE ON lyrics_recovery_import_items
WHEN EXISTS (SELECT 1 FROM lyrics_recovery_import_batches WHERE batch_sha256=OLD.batch_sha256)
BEGIN SELECT RAISE(ABORT, 'lyrics recovery import items are immutable'); END;

CREATE TABLE lyrics_recovery_source_evidence (
	provider                 TEXT NOT NULL,
	evidence_id              TEXT NOT NULL,
	sha256                   TEXT NOT NULL,
	acquisition_id           TEXT NOT NULL,
	envelope_sha256          TEXT NOT NULL,
	kind                     TEXT NOT NULL,
	origin                   TEXT NOT NULL,
	page_id                  INTEGER,
	revision_id              INTEGER,
	revision_timestamp       TEXT NOT NULL,
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
	CHECK (provider IN ('vocaloid_fandom','moegirl','moegirl_public_exact','sekaipedia')),
	CHECK ((provider='vocaloid_fandom' AND origin='https://vocaloid.fandom.com') OR
	       (provider='moegirl' AND origin='https://moegirl.icu') OR
	       (provider='moegirl_public_exact' AND origin='https://zh.moegirl.org.cn') OR
	       (provider='sekaipedia' AND origin='https://www.sekaipedia.org')),
	CHECK (length(evidence_id) BETWEEN 1 AND 256 AND substr(evidence_id,1,1) GLOB '[A-Za-z0-9]' AND
	       substr(evidence_id,2) NOT GLOB '*[^A-Za-z0-9._:/-]*'),
	CHECK (length(sha256)=64 AND sha256=lower(sha256) AND sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(acquisition_id)=64 AND acquisition_id=lower(acquisition_id) AND acquisition_id NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(envelope_sha256)=64 AND envelope_sha256=lower(envelope_sha256) AND envelope_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (kind IN ('mediawiki_revision','mediawiki_search_response','exact_public_html')),
	CHECK (json_valid(categories_json) AND json_type(categories_json)='array'),
	CHECK (length(fetched_at) BETWEEN 20 AND 35 AND fetched_at=trim(fetched_at) AND substr(fetched_at,-1)='Z'),
	CHECK (typeof(raw_bytes)='blob' AND typeof(raw_byte_count)='integer' AND
	       raw_byte_count BETWEEN 1 AND 2097152 AND raw_byte_count=length(raw_bytes)),
	CHECK (length(raw_sha256)=64 AND raw_sha256=sha256 AND raw_sha256=lower(raw_sha256) AND raw_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (typeof(created_at)='integer' AND created_at>0),
	CHECK ((kind='mediawiki_revision' AND provider IN ('vocaloid_fandom','moegirl','sekaipedia') AND
	        typeof(page_id)='integer' AND page_id>0 AND typeof(revision_id)='integer' AND revision_id>0 AND
	        length(mediawiki_sha1)=40 AND mediawiki_sha1=lower(mediawiki_sha1) AND mediawiki_sha1 NOT GLOB '*[^0-9a-f]*' AND
	        length(page_title) BETWEEN 1 AND 2048 AND page_title=trim(page_title) AND
	        length(canonical_revision_url) BETWEEN 1 AND 4096 AND canonical_revision_url=trim(canonical_revision_url) AND
	        canonical_request_url='' AND
	        ((provider='sekaipedia' AND length(revision_timestamp) BETWEEN 20 AND 30 AND
	          revision_timestamp=trim(revision_timestamp) AND substr(revision_timestamp,-1)='Z') OR
	         (provider<>'sekaipedia' AND revision_timestamp=''))) OR
	       (kind='mediawiki_search_response' AND provider='vocaloid_fandom' AND page_id IS NULL AND revision_id IS NULL AND
	        revision_timestamp='' AND mediawiki_sha1='' AND page_title='' AND canonical_revision_url='' AND
	        categories_json='[]' AND length(canonical_request_url) BETWEEN 1 AND 8192) OR
	       (kind='exact_public_html' AND provider='moegirl_public_exact' AND
	        typeof(page_id)='integer' AND page_id>0 AND typeof(revision_id)='integer' AND revision_id>0 AND
	        revision_timestamp='' AND mediawiki_sha1='' AND length(page_title) BETWEEN 1 AND 2048 AND
	        page_title=trim(page_title) AND length(canonical_revision_url) BETWEEN 1 AND 4096 AND
	        canonical_revision_url=trim(canonical_revision_url) AND canonical_request_url=canonical_revision_url))
);
CREATE INDEX idx_lyrics_recovery_source_evidence_digest
	ON lyrics_recovery_source_evidence(provider,sha256,evidence_id);
CREATE INDEX idx_lyrics_recovery_source_evidence_acquisition
	ON lyrics_recovery_source_evidence(acquisition_id,evidence_id);
CREATE TRIGGER lyrics_recovery_source_evidence_immutable_update
BEFORE UPDATE ON lyrics_recovery_source_evidence
BEGIN SELECT RAISE(ABORT, 'lyrics recovery source evidence is immutable'); END;
CREATE TRIGGER lyrics_recovery_source_evidence_immutable_delete
BEFORE DELETE ON lyrics_recovery_source_evidence
WHEN EXISTS (
	SELECT 1 FROM lyrics_recovery_import_artifact_evidence AS link
	WHERE link.provider=OLD.provider AND link.evidence_id=OLD.evidence_id AND link.sha256=OLD.sha256
)
BEGIN SELECT RAISE(ABORT, 'linked lyrics recovery source evidence is immutable'); END;

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
	       ('tagged_full_and_game','tagged_game_only_full_from_vocaloid','untagged_uncut_identity',
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
WHEN EXISTS (
	SELECT 1 FROM lyrics_recovery_import_items
	WHERE batch_sha256=OLD.batch_sha256 AND music_id=OLD.music_id
)
BEGIN SELECT RAISE(ABORT, 'lyrics recovery import artifacts are immutable'); END;

CREATE TABLE lyrics_recovery_import_artifact_evidence (
	batch_sha256  TEXT NOT NULL,
	music_id      INTEGER NOT NULL,
	rendition_key TEXT NOT NULL,
	position      INTEGER NOT NULL,
	provider      TEXT NOT NULL,
	evidence_id   TEXT NOT NULL,
	sha256        TEXT NOT NULL,
	PRIMARY KEY (batch_sha256,music_id,rendition_key,position),
	UNIQUE (batch_sha256,music_id,rendition_key,provider,evidence_id),
	CHECK (typeof(position)='integer' AND position BETWEEN 0 AND 63),
	FOREIGN KEY (batch_sha256,music_id,rendition_key)
		REFERENCES lyrics_recovery_import_artifacts(batch_sha256,music_id,rendition_key) ON DELETE CASCADE,
	FOREIGN KEY (provider,evidence_id,sha256)
		REFERENCES lyrics_recovery_source_evidence(provider,evidence_id,sha256) ON DELETE RESTRICT
);
CREATE TRIGGER lyrics_recovery_import_artifact_evidence_provider_insert
BEFORE INSERT ON lyrics_recovery_import_artifact_evidence
WHEN NEW.provider<>(SELECT provider FROM lyrics_recovery_import_artifacts
                     WHERE batch_sha256=NEW.batch_sha256 AND music_id=NEW.music_id AND rendition_key=NEW.rendition_key)
BEGIN SELECT RAISE(ABORT, 'lyrics recovery artifact evidence provider mismatch'); END;
CREATE TRIGGER lyrics_recovery_import_artifact_evidence_immutable_update
BEFORE UPDATE ON lyrics_recovery_import_artifact_evidence
BEGIN SELECT RAISE(ABORT, 'lyrics recovery artifact evidence links are immutable'); END;
CREATE TRIGGER lyrics_recovery_import_artifact_evidence_immutable_delete
BEFORE DELETE ON lyrics_recovery_import_artifact_evidence
WHEN EXISTS (
	SELECT 1 FROM lyrics_recovery_import_artifacts
	WHERE batch_sha256=OLD.batch_sha256 AND music_id=OLD.music_id AND rendition_key=OLD.rendition_key
)
BEGIN SELECT RAISE(ABORT, 'lyrics recovery artifact evidence links are immutable'); END;

CREATE TABLE lyrics_recovery_import_component_contributions (
	batch_sha256       TEXT NOT NULL,
	music_id           INTEGER NOT NULL,
	component          TEXT NOT NULL,
	rendition_key      TEXT NOT NULL,
	contribution_sha256 TEXT NOT NULL,
	PRIMARY KEY (batch_sha256,music_id,component),
	CHECK (component IN ('full_text','game_text','performer_segmentation','game_projection','ruby','version_evidence')),
	CHECK (length(contribution_sha256)=64 AND contribution_sha256=lower(contribution_sha256) AND contribution_sha256 NOT GLOB '*[^0-9a-f]*'),
	FOREIGN KEY (batch_sha256,music_id,rendition_key)
		REFERENCES lyrics_recovery_import_artifacts(batch_sha256,music_id,rendition_key) ON DELETE CASCADE
);
CREATE TRIGGER lyrics_recovery_import_component_state_insert
BEFORE INSERT ON lyrics_recovery_import_component_contributions
WHEN ((SELECT state FROM lyrics_recovery_import_items
       WHERE batch_sha256=NEW.batch_sha256 AND music_id=NEW.music_id)='complete' AND NEW.component='game_text') OR
     ((SELECT state FROM lyrics_recovery_import_items
       WHERE batch_sha256=NEW.batch_sha256 AND music_id=NEW.music_id)='game_only' AND
      NEW.component NOT IN ('game_text','performer_segmentation','ruby','version_evidence')) OR
     ((SELECT state FROM lyrics_recovery_import_items
       WHERE batch_sha256=NEW.batch_sha256 AND music_id=NEW.music_id) NOT IN ('complete','game_only'))
BEGIN SELECT RAISE(ABORT, 'lyrics recovery component does not match its availability state'); END;
CREATE TRIGGER lyrics_recovery_import_component_contributions_immutable_update
BEFORE UPDATE ON lyrics_recovery_import_component_contributions
BEGIN SELECT RAISE(ABORT, 'lyrics recovery component contributions are immutable'); END;
CREATE TRIGGER lyrics_recovery_import_component_contributions_immutable_delete
BEFORE DELETE ON lyrics_recovery_import_component_contributions
WHEN EXISTS (
	SELECT 1 FROM lyrics_recovery_import_items
	WHERE batch_sha256=OLD.batch_sha256 AND music_id=OLD.music_id
)
BEGIN SELECT RAISE(ABORT, 'lyrics recovery component contributions are immutable'); END;

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
	CHECK ((state='game_only' AND reason_code='tagged_game_only_full_from_vocaloid' AND no_lyrics_reason='') OR
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
`
