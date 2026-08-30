package store

import (
	"testing"

	"moesekai/server/internal/db"
)

func TestSong682V4Projection(t *testing.T) {
	s := setupLyricsStore(t)
	if err := s.UpsertMusicCatalog([]MusicCatalogRecord{
		{MusicID: 682, JapaneseTitle: "あなたしか見えないの", ChineseTitle: "眼中仅有你一人", EnglishTitle: "Anata Shika Mienai no"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(db.MigrationV32Song682TranslationEditionsSQL); err != nil {
		t.Fatalf("apply migration 32 failed: %v", err)
	}
	if _, err := s.db.Exec(db.MigrationV33Song682TranslationQEDCorrectionSQL); err != nil {
		t.Fatalf("apply migration 33 failed: %v", err)
	}
	if _, err := s.db.Exec(db.MigrationV34Song682TranslationMirrorSyncSQL); err != nil {
		t.Fatalf("apply migration 34 failed: %v", err)
	}

	index, details, v4Details, err := s.PublishedLyricsLocalizationProjection()
	if err != nil {
		t.Fatalf("projection err: %v", err)
	}

	var foundSong682 bool
	for _, song := range index {
		if song.MusicID == 682 {
			foundSong682 = true
			if song.Revision != 10 {
				t.Fatalf("song 682 index revision=%d want 10", song.Revision)
			}
			if song.State != PublicLyricsStateComplete {
				t.Fatalf("song 682 index state=%s want complete", song.State)
			}
		}
	}
	if !foundSong682 {
		t.Fatalf("song 682 not found in projected index: len=%d", len(index))
	}

	v3Detail, okV3 := details[682]
	if !okV3 {
		t.Fatalf("song 682 not found in v3 details")
	}
	if v3Detail.MusicID != 682 || v3Detail.Revision != 10 {
		t.Fatalf("song 682 v3Detail=%+v", v3Detail)
	}

	v4Detail, okV4 := v4Details[682]
	if !okV4 {
		t.Fatalf("song 682 not found in v4Details map (len=%d)", len(v4Details))
	}
	if v4Detail.Version != 4 {
		t.Fatalf("v4Detail.Version=%d want 4", v4Detail.Version)
	}
	if v4Detail.MusicID != 682 {
		t.Fatalf("v4Detail.MusicID=%d want 682", v4Detail.MusicID)
	}
	if v4Detail.DefaultTranslationEditionKey != "main" {
		t.Fatalf("v4Detail.DefaultTranslationEditionKey=%s want main", v4Detail.DefaultTranslationEditionKey)
	}
	if len(v4Detail.TranslationEditions) != 2 {
		t.Fatalf("v4Detail.TranslationEditions len=%d want 2", len(v4Detail.TranslationEditions))
	}

	editionMap := make(map[string]PublicLyricsV4TranslationEdition)
	for _, ed := range v4Detail.TranslationEditions {
		editionMap[ed.Key] = ed
	}

	// Verify Edition: main (雪莹ちゃん)
	edMain, okMain := editionMap["main"]
	if !okMain || edMain.Label != "雪莹ちゃん" {
		t.Fatalf("edMain mismatch: %+v", edMain)
	}
	if edMain.Renditions[0].TranslationCredits == nil || edMain.Renditions[0].TranslationCredits.Translation != "@雪莹ちゃん" {
		t.Fatalf("edMain credits mismatch: %+v", edMain.Renditions[0].TranslationCredits)
	}
	if edMain.Renditions[0].Full == nil || len(edMain.Renditions[0].Full.Translations) != 33 {
		t.Fatalf("edMain translations len mismatch: %+v", edMain.Renditions[0].Full)
	}
	if edMain.Renditions[0].Full.Translations[0] != "一定是命中注定的天之骄子" {
		t.Fatalf("edMain line 0 got %q", edMain.Renditions[0].Full.Translations[0])
	}
	if edMain.Renditions[0].Full.Translations[9] != "故证毕" {
		t.Fatalf("edMain line 9 got %q want 故证毕", edMain.Renditions[0].Full.Translations[9])
	}

	// Verify Edition: aishitenryu (爱死天流)
	edAishitenryu, okAishitenryu := editionMap["aishitenryu"]
	if !okAishitenryu || edAishitenryu.Label != "爱死天流" {
		t.Fatalf("edAishitenryu mismatch: %+v", edAishitenryu)
	}
	if edAishitenryu.Renditions[0].TranslationCredits == nil || edAishitenryu.Renditions[0].TranslationCredits.Translation != "@爱死天流" {
		t.Fatalf("edAishitenryu credits mismatch: %+v", edAishitenryu.Renditions[0].TranslationCredits)
	}
	if edAishitenryu.Renditions[0].Full == nil || len(edAishitenryu.Renditions[0].Full.Translations) != 33 {
		t.Fatalf("edAishitenryu translations len mismatch: %+v", edAishitenryu.Renditions[0].Full)
	}
	if edAishitenryu.Renditions[0].Full.Translations[0] != "定是命运的宠儿" {
		t.Fatalf("edAishitenryu line 0 got %q", edAishitenryu.Renditions[0].Full.Translations[0])
	}
}
