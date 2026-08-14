package db

const migrationV29LyricsTranslationVersionsSQL = `
-- Keep the v27 primary-side representation byte-compatible and add only the
-- peer-side payload needed by Full + independent Game renditions. Historical
-- rows continue to mean Full when present, otherwise Game.
CREATE TABLE song_lyrics_rendition_side_translation_lines (
	document_id   INTEGER NOT NULL,
	rendition_key TEXT NOT NULL,
	side          TEXT NOT NULL,
	locale        TEXT NOT NULL,
	position      INTEGER NOT NULL,
	text          TEXT NOT NULL,
	PRIMARY KEY (document_id,rendition_key,side,locale,position),
	CHECK (side IN ('full','game')),
	CHECK (typeof(position)='integer' AND position>=0),
	CHECK (length(text) <= 16384),
	FOREIGN KEY (document_id,rendition_key,locale)
		REFERENCES song_lyrics_rendition_localizations(document_id,rendition_key,locale) ON DELETE CASCADE
);
CREATE INDEX idx_song_lyrics_rendition_side_translation_lines_lookup
	ON song_lyrics_rendition_side_translation_lines(document_id,rendition_key,side,locale,position);
`
