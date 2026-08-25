package db

const migrationV34Song682TranslationMirrorSyncSQL = `
-- Ensure catalog entry exists for song 682
INSERT OR IGNORE INTO catalog_music
(music_id, title_ja, title_zh, title_en, jacket_url, newly_written, updated_at)
VALUES
(682, 'あなたしか見えないの', '眼中仅有你一人', 'Anata Shika Mienai no', '', 0, 1724544000);

-- Synchronize legacy rendition localization mirror revision with authoritative edition state
UPDATE song_lyrics_rendition_localizations
SET revision = (
  SELECT revision FROM song_lyrics_translation_edition_state
  WHERE song_lyrics_translation_edition_state.document_id = song_lyrics_rendition_localizations.document_id
)
WHERE document_id IN (
  SELECT document_id FROM song_lyrics_source_documents WHERE music_id = 682
);
`

const MigrationV34Song682TranslationMirrorSyncSQL = migrationV34Song682TranslationMirrorSyncSQL
