package db

var migrationsV31ToV34 = []migration{
	{
		version: 31,
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
	}, {
		version: 32,
		name:    "song_682_translation_editions",
		sql:     migrationV32Song682TranslationEditionsSQL,
	}, {
		version: 33,
		name:    "song_682_translation_qed_correction",
		sql:     migrationV33Song682TranslationQEDCorrectionSQL,
	}, {
		version: 34,
		name:    "song_682_translation_mirror_sync",
		sql:     migrationV34Song682TranslationMirrorSyncSQL,
	},
}
