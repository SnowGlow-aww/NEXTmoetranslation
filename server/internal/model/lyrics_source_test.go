package model

import "testing"

func catalogGroupingRecord(id int, title, lyricist, composer, arranger, version string, versionPresent bool) CatalogLyricsGroupingRecord {
	evidence := CatalogLyricsEvidence{Title: title, Lyricist: lyricist, Composer: composer, Arranger: arranger,
		LyricsVersion: version, Vocals: []CatalogVocalSignal{}, Presence: CatalogEvidencePresence{
			Lyricist: lyricist != "", Composer: composer != "", Arranger: arranger != "", LyricsVersion: versionPresent,
		}}
	fingerprint, _ := CatalogLyricsEvidenceFingerprint(evidence)
	return CatalogLyricsGroupingRecord{MusicID: id, Fingerprint: fingerprint, Evidence: evidence}
}

func TestClassifyCatalogLyricsTargetsAutomaticAnchors(t *testing.T) {
	for name, test := range map[string]struct {
		records     []CatalogLyricsGroupingRecord
		anchorID    int
		association []int
	}{
		"unique game-size-only": {
			records:     []CatalogLyricsGroupingRecord{catalogGroupingRecord(17, "Song", "L", "C", "A", "game_size", true)},
			anchorID:    17,
			association: []int{},
		},
		"multiple game-size deterministic anchor": {
			records: []CatalogLyricsGroupingRecord{
				catalogGroupingRecord(42, "Song", "L", "C", "A", "game_size", true),
				catalogGroupingRecord(7, "Song", "L", "C", "A", "game_size", true),
				catalogGroupingRecord(19, "Song", "L", "C", "A", "game_size", true),
			},
			anchorID:    7,
			association: []int{19, 42},
		},
		"existing full and game behavior": {
			records: []CatalogLyricsGroupingRecord{
				catalogGroupingRecord(1, "Song", "L", "C", "A", "full", true),
				catalogGroupingRecord(2, "Song", "L", "C", "A", "game_size", true),
			},
			anchorID:    1,
			association: []int{2},
		},
	} {
		t.Run(name, func(t *testing.T) {
			result := ClassifyCatalogLyricsTargets(test.records)
			if len(result) != len(test.records) {
				t.Fatalf("result=%+v", result)
			}
			anchors := 0
			for _, item := range result {
				if item.TargetMusicID != test.anchorID {
					t.Fatalf("target music ID=%d want=%d result=%+v", item.TargetMusicID, test.anchorID, result)
				}
				if !equalMusicIDs(item.AssociationMusicIDs, test.association) {
					t.Fatalf("associations=%v want=%v result=%+v", item.AssociationMusicIDs, test.association, result)
				}
				if item.MusicID == test.anchorID {
					anchors++
					if item.Disposition != LyricsCatalogTargetFullTarget {
						t.Fatalf("anchor disposition=%q result=%+v", item.Disposition, result)
					}
				} else if item.Disposition != LyricsCatalogTargetGameSizeEvidence {
					t.Fatalf("sibling disposition=%q result=%+v", item.Disposition, result)
				}
			}
			if anchors != 1 {
				t.Fatalf("anchors=%d result=%+v", anchors, result)
			}
		})
	}
}

func TestClassifyCatalogLyricsTargetsAllowsMissingArranger(t *testing.T) {
	result := ClassifyCatalogLyricsTargets([]CatalogLyricsGroupingRecord{
		catalogGroupingRecord(1, "Song", "L", "C", "", "full", true),
		catalogGroupingRecord(2, "Song", "L", "C", "A", "game_size", true),
	})
	if len(result) != 2 || result[0].Disposition != LyricsCatalogTargetFullTarget ||
		result[1].Disposition != LyricsCatalogTargetGameSizeEvidence || result[0].TargetMusicID != 1 ||
		result[1].TargetMusicID != 1 {
		t.Fatalf("missing arranger alone blocked automatic grouping: %+v", result)
	}
}

func TestClassifyCatalogLyricsTargetsExplainsNoLyricsCatalogExceptions(t *testing.T) {
	instrumental := catalogGroupingRecord(162, "Instrumental Challenge", "", "Composer", "Composer", "game_size", true)
	instrumental.Evidence.Presence.Vocals = true
	instrumental.Evidence.Vocals = []CatalogVocalSignal{{VocalID: 1, VocalType: "instrumental", Caption: "Inst.ver."}}

	medley := catalogGroupingRecord(674, "周年記念 楽曲メドレー", "", "", "", "game_size", true)
	medley.Evidence.Presence.Vocals = true
	medley.Evidence.Vocals = []CatalogVocalSignal{{VocalID: 2, VocalType: "original_song"}}

	for name, test := range map[string]struct {
		record CatalogLyricsGroupingRecord
		reason string
	}{
		"instrumental vocal evidence": {record: instrumental, reason: "instrumental_no_vocals"},
		"medley title evidence":       {record: medley, reason: "medley_composite_source"},
	} {
		t.Run(name, func(t *testing.T) {
			result := ClassifyCatalogLyricsTargets([]CatalogLyricsGroupingRecord{test.record})
			if len(result) != 1 || result[0].Disposition != LyricsCatalogTargetReview || result[0].ReasonCode != test.reason {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

func TestClassifyCatalogLyricsTargetsKeepsLyricsBearingMedleyTitleOnLyricsPath(t *testing.T) {
	record := catalogGroupingRecord(380, "スターダストメドレー", "きさら", "きさら", "きさら", "game_size", true)
	record.Evidence.Presence.Vocals = true
	record.Evidence.Vocals = []CatalogVocalSignal{{
		VocalID: 918, VocalType: "virtual_singer", Caption: "バーチャル・シンガーver.",
	}}

	result := ClassifyCatalogLyricsTargets([]CatalogLyricsGroupingRecord{record})
	if len(result) != 1 || result[0].Disposition != LyricsCatalogTargetFullTarget ||
		result[0].TargetMusicID != record.MusicID || result[0].ReasonCode != "" {
		t.Fatalf("lyrics-bearing medley title was blocked: %+v", result)
	}
}

func TestClassifyCatalogLyricsTargetsDoesNotInferInstrumentalFromMissingCredits(t *testing.T) {
	record := catalogGroupingRecord(1, "Ordinary Song", "", "Composer", "", "game_size", true)
	record.Evidence.Presence.Vocals = true
	record.Evidence.Vocals = []CatalogVocalSignal{
		{VocalID: 1, VocalType: "instrumental"},
		{VocalID: 2, VocalType: "original_song"},
	}
	result := ClassifyCatalogLyricsTargets([]CatalogLyricsGroupingRecord{record})
	if len(result) != 1 || result[0].ReasonCode != "missing_role_bound_credits" {
		t.Fatalf("mixed vocal evidence was misclassified: %+v", result)
	}
}

func TestClassifyCatalogLyricsTargetsKeepsInstrumentalAssetWithLyricistOnLyricsPath(t *testing.T) {
	record := catalogGroupingRecord(707, "Lyrics-bearing Instrumental Asset", "LindaAI-CUE(BNSI)", "Composer", "", "game_size", true)
	record.Evidence.Presence.Vocals = true
	record.Evidence.Vocals = []CatalogVocalSignal{{VocalID: 1264, VocalType: "instrumental", Caption: "Inst.ver."}}

	result := ClassifyCatalogLyricsTargets([]CatalogLyricsGroupingRecord{record})
	if len(result) != 1 || result[0].ReasonCode == "instrumental_no_vocals" {
		t.Fatalf("role-bound lyricist was discarded by instrumental classification: %+v", result)
	}
}

func TestCatalogVocalSignalsAreInstrumentalRequiresExactNonemptyUniformEvidence(t *testing.T) {
	for name, test := range map[string]struct {
		vocals []CatalogVocalSignal
		want   bool
	}{
		"one exact signal":       {vocals: []CatalogVocalSignal{{VocalID: 1, VocalType: "instrumental"}}, want: true},
		"multiple exact signals": {vocals: []CatalogVocalSignal{{VocalID: 1, VocalType: "instrumental"}, {VocalID: 2, VocalType: "instrumental"}}, want: true},
		"empty":                  {vocals: []CatalogVocalSignal{}, want: false},
		"nil":                    {vocals: nil, want: false},
		"mixed":                  {vocals: []CatalogVocalSignal{{VocalType: "instrumental"}, {VocalType: "original_song"}}, want: false},
		"case drift":             {vocals: []CatalogVocalSignal{{VocalType: "Instrumental"}}, want: false},
		"whitespace drift":       {vocals: []CatalogVocalSignal{{VocalType: "instrumental "}}, want: false},
		"missing type":           {vocals: []CatalogVocalSignal{{VocalID: 1}}, want: false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := CatalogVocalSignalsAreInstrumental(test.vocals); got != test.want {
				t.Fatalf("CatalogVocalSignalsAreInstrumental()=%t want %t", got, test.want)
			}
		})
	}
}

func TestCatalogLyricsAreInstrumentalRequiresAbsentLyricist(t *testing.T) {
	vocals := []CatalogVocalSignal{{VocalID: 1264, VocalType: "instrumental", Caption: "Inst.ver."}}
	if !CatalogLyricsAreInstrumental(vocals, "") || !CatalogLyricsAreInstrumental(vocals, " \t") {
		t.Fatal("exact instrumental signals without a lyricist were rejected")
	}
	if CatalogLyricsAreInstrumental(vocals, "LindaAI-CUE(BNSI)") {
		t.Fatal("instrumental game asset with a role-bound lyricist was classified as no-lyrics")
	}
}

func TestClassifyCatalogLyricsTargetsFailsClosed(t *testing.T) {
	for name, test := range map[string]struct {
		records []CatalogLyricsGroupingRecord
		reason  string
	}{
		"multiple full": {
			records: []CatalogLyricsGroupingRecord{
				catalogGroupingRecord(1, "Song", "L", "C", "A", "full", true),
				catalogGroupingRecord(2, "Song", "L", "C", "A", "full", true),
			},
			reason: "multiple_explicit_full_targets",
		},
		"missing version":  {records: []CatalogLyricsGroupingRecord{catalogGroupingRecord(1, "Song", "L", "C", "A", "", false)}, reason: "missing_explicit_version"},
		"explicit short":   {records: []CatalogLyricsGroupingRecord{catalogGroupingRecord(1, "Song", "L", "C", "A", "short", true)}, reason: "unsupported_or_unknown_version"},
		"explicit preview": {records: []CatalogLyricsGroupingRecord{catalogGroupingRecord(1, "Song", "L", "C", "A", "preview", true)}, reason: "unsupported_or_unknown_version"},
		"explicit partial": {records: []CatalogLyricsGroupingRecord{catalogGroupingRecord(1, "Song", "L", "C", "A", "partial", true)}, reason: "unsupported_or_unknown_version"},
		"explicit cover":   {records: []CatalogLyricsGroupingRecord{catalogGroupingRecord(1, "Song", "L", "C", "A", "cover", true)}, reason: "unsupported_or_unknown_version"},
		"explicit medley":  {records: []CatalogLyricsGroupingRecord{catalogGroupingRecord(1, "Song", "L", "C", "A", "medley", true)}, reason: "unsupported_or_unknown_version"},
		"missing credit":   {records: []CatalogLyricsGroupingRecord{catalogGroupingRecord(1, "Song", "", "C", "A", "game_size", true)}, reason: "missing_role_bound_credits"},
		"same title required credit conflict": {
			records: []CatalogLyricsGroupingRecord{
				catalogGroupingRecord(1, "Song", "L1", "C", "A", "game_size", true),
				catalogGroupingRecord(2, "Song", "L2", "C", "A", "game_size", true),
			},
			reason: "role_bound_credits_conflict",
		},
		"same title provided arranger conflict": {
			records: []CatalogLyricsGroupingRecord{
				catalogGroupingRecord(1, "Song", "L", "C", "A1", "game_size", true),
				catalogGroupingRecord(2, "Song", "L", "C", "A2", "game_size", true),
			},
			reason: "role_bound_credits_conflict",
		},
	} {
		t.Run(name, func(t *testing.T) {
			result := ClassifyCatalogLyricsTargets(test.records)
			if len(result) != len(test.records) {
				t.Fatalf("result=%+v", result)
			}
			for _, item := range result {
				if item.Disposition != LyricsCatalogTargetReview || item.ReasonCode != test.reason ||
					item.TargetMusicID != 0 || len(item.AssociationMusicIDs) != 0 {
					t.Fatalf("unsafe grouping=%+v", result)
				}
			}
		})
	}
}

func equalMusicIDs(left, right []int) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func TestCatalogLyricsEvidenceFingerprintNormalizesNFKCAndOrder(t *testing.T) {
	left := CatalogLyricsEvidence{Title: "Ｓｏｎｇ", Lyricist: " L ", Composer: "C", Arranger: "A", LyricsVersion: "FULL",
		Presence: CatalogEvidencePresence{Lyricist: true, Composer: true, Arranger: true, LyricsVersion: true},
		Vocals:   []CatalogVocalSignal{{VocalID: 2}, {VocalID: 1}}}
	right := CatalogLyricsEvidence{Title: "Song", Lyricist: "L", Composer: "C", Arranger: "A", LyricsVersion: "full",
		Presence: left.Presence, Vocals: []CatalogVocalSignal{{VocalID: 1}, {VocalID: 2}}}
	leftHash, err := CatalogLyricsEvidenceFingerprint(left)
	if err != nil {
		t.Fatal(err)
	}
	rightHash, err := CatalogLyricsEvidenceFingerprint(right)
	if err != nil {
		t.Fatal(err)
	}
	if leftHash != rightHash {
		t.Fatalf("normalized fingerprints differ %s != %s", leftHash, rightHash)
	}
}
