package store

import (
	"strings"
	"testing"
)

func TestLyricsTranslationEditionReadsFailClosedWhenDefaultMirrorDrifts(t *testing.T) {
	s, _ := setupRenditionV3PersistenceStore(t)
	current, err := s.GetLyricsRenditionDocument(10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.MutateLyricsTranslationEdition(LyricsTranslationEditionMutation{
		MusicID: 10, Revision: current.Revision, Operation: "create", EditionKey: "alternate", Label: "另一译本",
	}, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE song_lyrics_rendition_translation_lines SET text='tampered' WHERE position=0`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetLyricsRenditionDocumentEdition(10, "alternate", true); err == nil || !strings.Contains(err.Error(), "mirror is stale") {
		t.Fatalf("stale default mirror read error=%v", err)
	}
}
