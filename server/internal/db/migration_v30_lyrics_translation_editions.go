package db

const migrationV30LyricsTranslationEditionsSQL = `
-- Translation editions are document-level stable identities. Existing v27/v29
-- rows remain the implicit main edition until the first metadata mutation
-- materializes this additive schema.
CREATE TABLE song_lyrics_translation_editions (
	document_id INTEGER NOT NULL,
	edition_key TEXT NOT NULL,
	label       TEXT NOT NULL,
	created_at  INTEGER NOT NULL,
	created_by  TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (document_id,edition_key),
	CHECK (length(edition_key) BETWEEN 1 AND 128 AND edition_key=lower(edition_key) AND
	       substr(edition_key,1,1) GLOB '[a-z0-9]' AND edition_key NOT GLOB '*[^a-z0-9._-]*'),
	CHECK (length(CAST(label AS BLOB)) BETWEEN 1 AND 256 AND label=trim(label)),
	CHECK (typeof(created_at)='integer' AND created_at>0),
	CHECK (length(created_by)<=128),
	FOREIGN KEY (document_id) REFERENCES song_lyrics_source_documents(document_id) ON DELETE CASCADE
);
CREATE INDEX idx_song_lyrics_translation_editions_document
	ON song_lyrics_translation_editions(document_id,edition_key);

CREATE TABLE song_lyrics_translation_edition_state (
	document_id        INTEGER NOT NULL PRIMARY KEY,
	default_edition_key TEXT NOT NULL,
	revision           INTEGER NOT NULL,
	updated_at         INTEGER NOT NULL,
	updated_by         TEXT NOT NULL DEFAULT '',
	CHECK (length(default_edition_key) BETWEEN 1 AND 128 AND default_edition_key=lower(default_edition_key) AND
	       substr(default_edition_key,1,1) GLOB '[a-z0-9]' AND default_edition_key NOT GLOB '*[^a-z0-9._-]*'),
	CHECK (typeof(revision)='integer' AND revision>0),
	CHECK (typeof(updated_at)='integer' AND updated_at>0),
	CHECK (length(updated_by)<=128),
	FOREIGN KEY (document_id,default_edition_key)
		REFERENCES song_lyrics_translation_editions(document_id,edition_key) ON DELETE RESTRICT
);
CREATE INDEX idx_song_lyrics_translation_edition_state_default
	ON song_lyrics_translation_edition_state(document_id,default_edition_key);

CREATE TABLE song_lyrics_translation_edition_localizations (
	document_id        INTEGER NOT NULL,
	edition_key        TEXT NOT NULL,
	rendition_key      TEXT NOT NULL,
	locale             TEXT NOT NULL,
	translation_credit TEXT NOT NULL DEFAULT '',
	proofreading_credit TEXT NOT NULL DEFAULT '',
	updated_at         INTEGER NOT NULL,
	updated_by         TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (document_id,edition_key,rendition_key,locale),
	CHECK (length(rendition_key) BETWEEN 1 AND 256 AND rendition_key=trim(rendition_key)),
	CHECK (locale='zh-CN'),
	CHECK (length(CAST(translation_credit AS BLOB))<=2048 AND length(CAST(proofreading_credit AS BLOB))<=2048),
	CHECK (typeof(updated_at)='integer' AND updated_at>0),
	CHECK (length(updated_by)<=128),
	FOREIGN KEY (document_id,edition_key)
		REFERENCES song_lyrics_translation_editions(document_id,edition_key) ON DELETE CASCADE
);
CREATE INDEX idx_song_lyrics_translation_edition_localizations_lookup
	ON song_lyrics_translation_edition_localizations(document_id,edition_key,locale,rendition_key);

CREATE TABLE song_lyrics_translation_edition_lines (
	document_id   INTEGER NOT NULL,
	edition_key   TEXT NOT NULL,
	rendition_key TEXT NOT NULL,
	side          TEXT NOT NULL,
	locale        TEXT NOT NULL,
	position      INTEGER NOT NULL,
	text          TEXT NOT NULL,
	PRIMARY KEY (document_id,edition_key,rendition_key,side,locale,position),
	CHECK (side IN ('full','game')),
	CHECK (locale='zh-CN'),
	CHECK (typeof(position)='integer' AND position>=0),
	CHECK (length(CAST(text AS BLOB))<=16384),
	FOREIGN KEY (document_id,edition_key,rendition_key,locale)
		REFERENCES song_lyrics_translation_edition_localizations(document_id,edition_key,rendition_key,locale) ON DELETE CASCADE
);
CREATE INDEX idx_song_lyrics_translation_edition_lines_lookup
	ON song_lyrics_translation_edition_lines(document_id,edition_key,rendition_key,side,locale,position);
`
