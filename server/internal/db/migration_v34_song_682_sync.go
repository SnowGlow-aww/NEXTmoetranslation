package db

const migrationV34Song682TranslationMirrorSyncSQL = `
-- Ensure catalog entry exists and has valid metadata for song 682
INSERT OR IGNORE INTO catalog_music
(music_id, title_ja, title_zh, title_en, jacket_url, newly_written, updated_at, producer_metadata, lyricist, composer, arranger, lyrics_evidence_presence_json, vocal_signals_json, lyrics_catalog_fingerprint, lyrics_catalog_policy_version)
VALUES
(682, 'あなたしか見えないの', '眼中仅有你一人', 'Anata Shika Mienai no', '', 0, 1724544000, 'Guiano', 'Guiano', 'Guiano', '', '{"title":true,"lyricist":true,"composer":true,"arranger":false,"lyricsVersion":false,"vocals":true}', '[{"kind":"sekai","performers":["花里みのり","桐谷遥","桃井愛莉","日野森雫","巡音ルカ"]}]', 'fingerprint-682', 1);

UPDATE catalog_music
SET producer_metadata = COALESCE(NULLIF(producer_metadata, ''), 'Guiano'),
    lyricist = COALESCE(NULLIF(lyricist, ''), 'Guiano'),
    composer = COALESCE(NULLIF(composer, ''), 'Guiano'),
    lyrics_evidence_presence_json = COALESCE(NULLIF(lyrics_evidence_presence_json, ''), '{"title":true,"lyricist":true,"composer":true,"arranger":false,"lyricsVersion":false,"vocals":true}'),
    vocal_signals_json = COALESCE(NULLIF(vocal_signals_json, ''), '[{"kind":"sekai","performers":["花里みのり","桐谷遥","桃井愛莉","日野森雫","巡音ルカ"]}]')
WHERE music_id = 682;

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
