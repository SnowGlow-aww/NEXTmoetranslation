package db

const migrationV25LyricsSourceSchemaSQL = `
-- migration:foreign_keys_off
-- Source documents now carry schema v2 and may contain an independent Game
-- artifact. Rebuild only the frozen parent table; every stored JSON byte and
-- document identity is copied unchanged, while child foreign keys continue to
-- target the canonical table name under legacy alter semantics.
CREATE TEMP TABLE lyrics_source_v25_sequences (
	name TEXT PRIMARY KEY,
	seq  INTEGER NOT NULL
);
INSERT INTO lyrics_source_v25_sequences(name,seq)
SELECT name,seq FROM sqlite_sequence
WHERE name='song_lyrics_source_documents';
PRAGMA legacy_alter_table=ON;
ALTER TABLE song_lyrics_source_documents RENAME TO song_lyrics_source_documents_v24;

CREATE TABLE song_lyrics_source_documents (
	document_id          INTEGER PRIMARY KEY AUTOINCREMENT,
	music_id             INTEGER NOT NULL UNIQUE,
	schema_version       INTEGER NOT NULL,
	reason_code          TEXT NOT NULL,
	document_json        TEXT NOT NULL,
	document_sha256      TEXT NOT NULL UNIQUE,
	manifest_batch_sha256 TEXT NOT NULL,
	created_at           INTEGER NOT NULL,
	CHECK (schema_version IN (1,2)),
	CHECK (reason_code IN ('tagged_full_and_game','tagged_game_only','tagged_game_only_full_from_vocaloid','untagged_uncut_identity','untagged_game_subset','untagged_full_only','version_conflict')),
	CHECK (reason_code<>'version_conflict'),
	CHECK (length(document_json) BETWEEN 2 AND 16777216 AND json_valid(document_json) AND json_type(document_json)='object'),
	CHECK (length(document_sha256)=64 AND document_sha256=lower(document_sha256) AND document_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(manifest_batch_sha256)=64 AND manifest_batch_sha256=lower(manifest_batch_sha256) AND manifest_batch_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (typeof(created_at)='integer' AND created_at>0),
	FOREIGN KEY (music_id) REFERENCES song_lyrics(music_id) ON DELETE CASCADE
);
INSERT INTO song_lyrics_source_documents(
	document_id,music_id,schema_version,reason_code,document_json,document_sha256,manifest_batch_sha256,created_at
)
SELECT document_id,music_id,schema_version,reason_code,document_json,document_sha256,manifest_batch_sha256,created_at
FROM song_lyrics_source_documents_v24;
DROP TABLE song_lyrics_source_documents_v24;

CREATE TRIGGER song_lyrics_source_documents_immutable_update BEFORE UPDATE ON song_lyrics_source_documents
BEGIN SELECT RAISE(ABORT, 'song lyrics source documents are immutable'); END;
CREATE TRIGGER song_lyrics_source_documents_immutable_delete BEFORE DELETE ON song_lyrics_source_documents
WHEN EXISTS (SELECT 1 FROM song_lyrics WHERE music_id=OLD.music_id)
BEGIN SELECT RAISE(ABORT, 'song lyrics source documents are immutable'); END;

UPDATE sqlite_sequence
SET seq=MAX(seq,(SELECT saved.seq FROM lyrics_source_v25_sequences AS saved WHERE saved.name=sqlite_sequence.name))
WHERE name IN (SELECT name FROM lyrics_source_v25_sequences);
INSERT INTO sqlite_sequence(name,seq)
SELECT saved.name,saved.seq FROM lyrics_source_v25_sequences AS saved
WHERE NOT EXISTS (SELECT 1 FROM sqlite_sequence AS current WHERE current.name=saved.name);
DROP TABLE temp.lyrics_source_v25_sequences;
PRAGMA legacy_alter_table=OFF;
`
