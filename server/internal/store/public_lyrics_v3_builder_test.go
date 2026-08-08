package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"moesekai/server/internal/model"
)

type publicV3TestTranslation struct {
	texts        []string
	translation  string
	proofreading string
}

func TestPublicLyricsV3BuilderCoversClosedRenditionShapes(t *testing.T) {
	tests := []struct {
		name          string
		rendition     model.LyricsSourceRendition
		state         PublicLyricsAvailabilityState
		legacy        bool
		wantFull      bool
		wantGame      bool
		wantKind      model.LyricsSourceRenditionKind
		wantPerformer string
	}{
		{
			name:      "one-rendition legacy-compatible",
			rendition: publicV3TestRendition("original", "original-source", model.LyricsSourceRenditionOriginal, true, false, false, true, ""),
			state:     PublicLyricsStateComplete, legacy: true, wantFull: true,
			wantKind: model.LyricsSourceRenditionOriginal,
		},
		{
			name:      "Full-only with performer evidence",
			rendition: publicV3TestRendition("sekai", "sekai-source", model.LyricsSourceRenditionSekai, true, false, false, false, "performer-01"),
			state:     PublicLyricsStateComplete, wantFull: true,
			wantKind: model.LyricsSourceRenditionSekai, wantPerformer: "performer-01",
		},
		{
			name:      "exact Full and Game projection",
			rendition: publicV3TestRendition("sekai", "sekai-source", model.LyricsSourceRenditionSekai, true, true, true, false, ""),
			state:     PublicLyricsStateComplete, wantFull: true, wantGame: true,
			wantKind: model.LyricsSourceRenditionSekai,
		},
		{
			name:      "independent Game-only",
			rendition: publicV3TestRendition("vocaloid", "vocaloid-source", model.LyricsSourceRenditionVocaloid, false, true, false, false, ""),
			state:     PublicLyricsStateGameOnly, wantGame: true,
			wantKind: model.LyricsSourceRenditionVocaloid,
		},
		{
			name:      "explicit Alternate Vocal",
			rendition: publicV3TestRendition("alternate.test", "alternate-source", model.LyricsSourceRenditionAlternate, true, false, false, false, ""),
			state:     PublicLyricsStateComplete, wantFull: true,
			wantKind: model.LyricsSourceRenditionAlternate,
		},
	}

	for testIndex, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := publicV3TestDocument(t, test.rendition)
			if test.legacy {
				if err := model.LyricsSourceDocumentV3ToV2Compatibility(document); err != nil {
					t.Fatalf("legacy-compatible rendition was not lossless for v2: %v", err)
				}
			} else if err := model.LyricsSourceDocumentV3ToV2Compatibility(document); err == nil {
				t.Fatal("native v3 rendition was unexpectedly accepted as legacy-v2-compatible")
			}
			translations := map[string]publicV3TestTranslation{
				test.rendition.RenditionKey: {
					texts: []string{"訳"}, translation: "Translator", proofreading: "Proofreader",
				},
			}
			detail := publicV3TestBuildDetail(t, 100+testIndex, test.state, document, translations)
			if len(detail.Renditions) != 1 {
				t.Fatalf("rendition count=%d", len(detail.Renditions))
			}
			got := detail.Renditions[0]
			if got.Kind != test.wantKind || (got.Full != nil) != test.wantFull || (got.Game != nil) != test.wantGame {
				t.Fatalf("closed rendition shape=%+v", got)
			}
			if got.TranslationCredits == nil || got.TranslationCredits.Translation != "Translator" ||
				got.TranslationCredits.Proofreading != "Proofreader" {
				t.Fatalf("independent translation credits=%+v", got.TranslationCredits)
			}
			if test.wantPerformer != "" {
				if len(got.Performers) != 1 || got.Performers[0].PerformerID != test.wantPerformer ||
					len(got.Full.Lines[0].Segments[0].PerformerIDs) != 1 ||
					got.Full.Lines[0].Segments[0].PerformerIDs[0] != test.wantPerformer {
					t.Fatalf("per-rendition performer metadata was not preserved: %+v", got)
				}
			}
			if test.wantKind == model.LyricsSourceRenditionAlternate &&
				!reflect.DeepEqual(got.SourceTabPaths, test.rendition.SourceTabPaths) {
				t.Fatalf("explicit alternate source tabs=%v", got.SourceTabPaths)
			}
			if test.wantFull && test.wantGame {
				if got.Relation.Kind != model.LyricsSourceRenditionRelationExactProjection ||
					!reflect.DeepEqual(got.Relation.LineIDs, []string{"full-000001"}) ||
					got.Full.Lines[0].Chinese != got.Game.Lines[0].Chinese {
					t.Fatalf("exact projection was not preserved: %+v", got.Relation)
				}
			}
			body, err := EncodePublicLyricsV3Detail(detail)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodePublicLyricsV3Detail(body)
			if err != nil || !reflect.DeepEqual(decoded, detail) {
				t.Fatalf("strict v3 round trip err=%v decoded=%+v", err, decoded)
			}
		})
	}
}

func TestPublicLyricsV3BuilderPreservesSourceExactEdgeWhitespace(t *testing.T) {
	rendition := publicV3TestRendition(
		"original", "original-source", model.LyricsSourceRenditionOriginal,
		true, false, false, false, "",
	)
	line := &rendition.Full.Lines[0]
	line.Text = "　歌う "
	line.Segments[0].Text = line.Text
	line.Segments[0].Ruby = []model.LyricsSourceRubySpan{
		{Text: "　"},
		{Text: "歌", Reading: "うた", ReadingEvidence: &model.LyricsSourceReadingEvidence{
			Kind:             model.LyricsSourceReadingEvidenceDeterministicDictionary,
			GeneratorVersion: "public-v3-test",
		}},
		{Text: "う "},
	}
	document := publicV3TestDocument(t, rendition)
	detail := publicV3TestBuildDetail(t, 8, PublicLyricsStateComplete, document, map[string]publicV3TestTranslation{
		"original": {texts: []string{"訳"}, translation: "Translator"},
	})
	got := detail.Renditions[0].Full.Lines[0]
	if got.Japanese != line.Text || got.Segments[0].Text != line.Text ||
		got.Segments[0].Ruby[0].Text != "　" || got.Segments[0].Ruby[2].Text != "う " {
		t.Fatalf("source-exact edge whitespace changed: %+v", got)
	}
	body, err := EncodePublicLyricsV3Detail(detail)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodePublicLyricsV3Detail(body)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Renditions[0].Full.Lines[0].Japanese != line.Text {
		t.Fatalf("strict round trip trimmed source text: %q", decoded.Renditions[0].Full.Lines[0].Japanese)
	}
}

func TestPublicLyricsV3BuilderUsesLocalizationRevisionForPartialAndEdgeText(t *testing.T) {
	rendition := publicV3TestRendition(
		"original", "original-source", model.LyricsSourceRenditionOriginal,
		true, false, false, false, "",
	)
	secondLine := rendition.Full.Lines[0]
	secondLine.ID = "full-000002"
	rendition.Full.Lines = append(rendition.Full.Lines, secondLine)
	document := publicV3TestDocument(t, rendition)
	detail := publicV3TestBuildDetail(t, 9, PublicLyricsStateComplete, document, map[string]publicV3TestTranslation{
		"original": {texts: []string{" 译文\n续 ", ""}, translation: "Translator"},
	})
	if detail.Revision != 2 || detail.UpdatedAt != formatTimestamp(1785456060) {
		t.Fatalf("localization metadata revision=%d updatedAt=%q", detail.Revision, detail.UpdatedAt)
	}
	lines := detail.Renditions[0].Full.Lines
	if len(lines) != 2 || lines[0].Chinese != " 译文\n续 " || lines[1].Chinese != "" {
		t.Fatalf("partial localization lines=%+v", lines)
	}
	body, err := EncodePublicLyricsV3Detail(detail)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodePublicLyricsV3Detail(body)
	if err != nil || decoded.Revision != detail.Revision || decoded.UpdatedAt != detail.UpdatedAt ||
		decoded.Renditions[0].Full.Lines[0].Chinese != lines[0].Chinese || decoded.Renditions[0].Full.Lines[1].Chinese != "" {
		t.Fatalf("partial localization round trip err=%v decoded=%+v", err, decoded)
	}
}

func TestPublicLyricsV3BuilderFallsBackToSourceMetadataWithoutLocalization(t *testing.T) {
	document := publicV3TestDocument(t, publicV3TestRendition(
		"original", "original-source", model.LyricsSourceRenditionOriginal,
		true, false, false, false, "",
	))
	detail := publicV3TestBuildDetail(t, 10, PublicLyricsStateComplete, document, nil)
	if detail.Revision != 1 || detail.UpdatedAt != formatTimestamp(1785456000) {
		t.Fatalf("source metadata fallback revision=%d updatedAt=%q", detail.Revision, detail.UpdatedAt)
	}
}

func TestPublicLyricsV3BuilderMaterializesImplicitExactProjection(t *testing.T) {
	rendition := publicV3TestRendition(
		"vocaloid", "vocaloid-source", model.LyricsSourceRenditionVocaloid,
		true, false, false, true, "",
	)
	rendition.ReasonCode = model.LyricsSourceVersionReasonUntaggedUncutIdentity
	rendition.Relation = model.LyricsSourceRenditionRelation{
		Kind:             model.LyricsSourceRenditionRelationExactProjection,
		FullRenditionKey: rendition.RenditionKey,
		LineIDs:          []string{"full-000001"},
	}
	document := publicV3TestDocument(t, rendition)
	if document.Renditions[0].Game != nil {
		t.Fatal("source fixture unexpectedly owns a duplicated Game side")
	}
	detail := publicV3TestBuildDetail(t, 176, PublicLyricsStateComplete, document, map[string]publicV3TestTranslation{
		"vocaloid": {texts: []string{"訳"}, translation: "Translator"},
	})
	got := detail.Renditions[0]
	if got.Full == nil || got.Game == nil ||
		!reflect.DeepEqual(got.AvailableVersions, []string{"full", "game"}) ||
		got.Relation.Kind != model.LyricsSourceRenditionRelationExactProjection ||
		got.Game.Lines[0].ID != "game-000001" || got.Game.Lines[0].Order != 0 ||
		got.Game.Lines[0].Japanese != got.Full.Lines[0].Japanese ||
		got.Game.Lines[0].Chinese != got.Full.Lines[0].Chinese ||
		!reflect.DeepEqual(got.Game.Lines[0].Segments, got.Full.Lines[0].Segments) {
		t.Fatalf("implicit exact projection was not materialized losslessly: %+v", got)
	}
	for _, attribution := range got.Provenance {
		if strings.HasSuffix(attribution.Component, "/game_text") {
			t.Fatal("derived Game projection invented independent Game text provenance")
		}
	}
	body, err := EncodePublicLyricsV3Detail(detail)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodePublicLyricsV3Detail(body)
	if err != nil || !reflect.DeepEqual(decoded, detail) {
		t.Fatalf("materialized exact projection round trip err=%v decoded=%+v", err, decoded)
	}
}

func TestRecoveryPublicLyricsV3RejectsNativeSourceV3WithLegacyEditableLyrics(t *testing.T) {
	document := publicV3TestDocument(t, publicV3TestRendition(
		"original", "original-source", model.LyricsSourceRenditionOriginal,
		true, false, false, false, "",
	))
	content, batchSHA := publicV3RecoverySourceFixture(t, 796, document)
	content.Documents = []LyricsDocumentBackupRecord{{
		MusicID: 796, Revision: 1, UpdatedAt: 1786173174, UpdatedBy: "legacy-editor",
	}}
	if _, err := buildRecoveryPublicLyricsV3Candidate(content, batchSHA); err == nil ||
		!strings.Contains(err.Error(), "mixed source-v3 and legacy editable ownership") {
		t.Fatalf("native source-v3 mixed storage was accepted: %v", err)
	}
}

func TestRecoveryPublicLyricsV3UpconvertsLegacyV2SourceWithEditableLocalization(t *testing.T) {
	const batchSHA = "abababababababababababababababababababababababababababababababab"
	const rootSHA = "cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd"

	rendition := publicV3TestRendition(
		"original", "original-source", model.LyricsSourceRenditionOriginal,
		true, false, false, true, "",
	)
	v3Document := publicV3TestDocument(t, rendition)
	v3Rendition := v3Document.Renditions[0]
	legacyDocument := model.LyricsSourceDocument{
		SchemaVersion:   model.LyricsSourceDocumentSchemaVersionV2,
		ReasonCode:      v3Rendition.ReasonCode,
		FixedIdentities: append([]model.LyricsSourceFixedIdentity(nil), v3Document.FixedIdentities...),
		Full:            *model.CloneLyricsSourceFull(v3Rendition.Full),
		Provenance: model.LyricsSourceComponentProvenance{
			FullText:        *v3Rendition.Provenance.FullText,
			Ruby:            v3Rendition.Provenance.FullRuby,
			VersionEvidence: v3Rendition.Provenance.VersionEvidence,
		},
	}
	for lineIndex := range legacyDocument.Full.Lines {
		for segmentIndex := range legacyDocument.Full.Lines[lineIndex].Segments {
			for spanIndex := range legacyDocument.Full.Lines[lineIndex].Segments[segmentIndex].Ruby {
				legacyDocument.Full.Lines[lineIndex].Segments[segmentIndex].Ruby[spanIndex].ReadingEvidence = nil
			}
		}
	}
	if err := model.ValidateLyricsSourceDocument(legacyDocument); err != nil {
		t.Fatal(err)
	}
	documentBody, documentSHA := publicV3TestDocumentBytes(t, legacyDocument)
	identityBody, err := json.Marshal(legacyDocument.FixedIdentities[0])
	if err != nil {
		t.Fatal(err)
	}
	contributions := []LyricsRecoveryContributionBackupRecord{}
	for component, identityKey := range publicLyricsSourceComponentRefs(legacyDocument) {
		contributions = append(contributions, LyricsRecoveryContributionBackupRecord{
			BatchSHA256: batchSHA, MusicID: 795, Component: component, RenditionKey: identityKey,
		})
	}
	content := LyricsContentExport{
		Music: []CatalogMusicBackupRecord{{MusicID: 795, TitleJA: "Legacy v2"}},
		Documents: []LyricsDocumentBackupRecord{{
			MusicID: 795, Revision: 1, UpdatedAt: 1786173174, UpdatedBy: "test",
			TranslationCredit: "Translator", ProofreadingCredit: "Proofreader",
		}},
		Lines: []LyricsLineBackupRecord{{
			MusicID: 795, LineID: legacyDocument.Full.Lines[0].ID, Position: 0,
			Japanese: legacyDocument.Full.Lines[0].Text, Chinese: "译文", English: "Translation",
		}},
		SourceDocuments: []LyricsSourceDocumentBackupRecord{{
			DocumentID: 41, MusicID: 795, SchemaVersion: model.LyricsSourceDocumentSchemaVersionV2,
			ReasonCode: string(legacyDocument.ReasonCode), DocumentJSON: string(documentBody),
			DocumentSHA256: documentSHA, ManifestBatchSHA256: batchSHA, CreatedAt: 1786173174,
		}},
		RecoveryBatches: []LyricsRecoveryBatchBackupRecord{{
			BatchSHA256: batchSHA, RootSHA256: rootSHA, CatalogCount: 1,
		}},
		RecoveryItems: []LyricsRecoveryItemBackupRecord{{
			BatchSHA256: batchSHA, MusicID: 795, State: string(PublicLyricsStateComplete),
			DocumentSHA256: documentSHA,
		}},
		RecoveryArtifacts: []LyricsRecoveryArtifactBackupRecord{{
			BatchSHA256: batchSHA, MusicID: 795,
			RenditionKey:      legacyDocument.FixedIdentities[0].RenditionKey,
			FixedIdentityJSON: string(identityBody),
		}},
		RecoveryContributions: contributions,
	}
	candidate, err := buildRecoveryPublicLyricsV3Candidate(content, batchSHA)
	if err != nil {
		t.Fatal(err)
	}
	detail := candidate.Details[795]
	if len(detail.Renditions) != 1 || detail.Renditions[0].Full == nil || detail.Renditions[0].Game != nil {
		t.Fatalf("legacy v2 Public v3 rendition shape=%+v", detail.Renditions)
	}
	line := detail.Renditions[0].Full.Lines[0]
	credits := detail.Renditions[0].TranslationCredits
	if line.Japanese != legacyDocument.Full.Lines[0].Text || line.Chinese != "译文" ||
		line.English != "Translation" || credits == nil || credits.Translation != "Translator" ||
		credits.Proofreading != "Proofreader" {
		t.Fatalf("legacy v2 localization was not preserved: line=%+v credits=%+v", line, credits)
	}
	compatibility, err := buildRecoveryPublicLyricsV2CompatibilityCandidate(content, candidate)
	if err != nil {
		t.Fatal(err)
	}
	legacyPublic := compatibility.Details[795]
	if len(legacyPublic.Lines) != 1 || legacyPublic.Lines[0].Chinese != "译文" ||
		legacyPublic.Lines[0].English != "Translation" || legacyPublic.TranslationCredits == nil ||
		legacyPublic.TranslationCredits.Translation != "Translator" ||
		legacyPublic.TranslationCredits.Proofreading != "Proofreader" {
		t.Fatalf("legacy v2 compatibility localization=%+v", legacyPublic)
	}
}

func TestPublicLyricsV3BuilderPreservesREMStylePeerFamiliesWithoutMergingEqualText(t *testing.T) {
	vocaloid := publicV3TestRendition("vocaloid", "vocaloid-source", model.LyricsSourceRenditionVocaloid, false, true, false, false, "")
	sekai := publicV3TestRendition("sekai", "sekai-source", model.LyricsSourceRenditionSekai, true, true, true, false, "")
	document := publicV3TestDocument(t, vocaloid, sekai)
	detail := publicV3TestBuildDetail(t, 765, PublicLyricsStateComplete, document, map[string]publicV3TestTranslation{
		"sekai":    {texts: []string{"訳甲"}, translation: "SEKAI Translator", proofreading: "SEKAI Proofreader"},
		"vocaloid": {texts: []string{"訳乙"}, translation: "Vocaloid Translator", proofreading: "Vocaloid Proofreader"},
	})
	if len(detail.Renditions) != 2 || detail.Renditions[0].Key != "sekai" || detail.Renditions[1].Key != "vocaloid" {
		t.Fatalf("peer rendition ordering or count changed: %+v", detail.Renditions)
	}
	sekaiPublic, vocaloidPublic := detail.Renditions[0], detail.Renditions[1]
	if sekaiPublic.Full == nil || sekaiPublic.Game == nil || vocaloidPublic.Full != nil || vocaloidPublic.Game == nil ||
		sekaiPublic.Full.Lines[0].Japanese != vocaloidPublic.Game.Lines[0].Japanese {
		t.Fatalf("REM-style peer shape changed: sekai=%+v vocaloid=%+v", sekaiPublic, vocaloidPublic)
	}
	if sekaiPublic.Full.Lines[0].Chinese == vocaloidPublic.Game.Lines[0].Chinese ||
		sekaiPublic.TranslationCredits.Translation == vocaloidPublic.TranslationCredits.Translation {
		t.Fatal("equal source text merged independent peer translations or credits")
	}
	if reflect.DeepEqual(sekaiPublic.SourceTabPaths, vocaloidPublic.SourceTabPaths) ||
		sekaiPublic.Provenance[0].RevisionURL == vocaloidPublic.Provenance[0].RevisionURL {
		t.Fatal("peer source tabs or component revision provenance were merged")
	}
	if got := publicV3AvailableVersions(detail); !reflect.DeepEqual(got, []string{"full", "game"}) {
		t.Fatalf("data-driven available versions=%v", got)
	}
}

func TestPublicLyricsV2CompatibilityIsSeparateLosslessAndOneRenditionOnly(t *testing.T) {
	one := publicV3TestRendition("original", "original-source", model.LyricsSourceRenditionOriginal, true, false, false, true, "")
	oneDocument := publicV3TestDocument(t, one)
	oneDetail := publicV3TestBuildDetail(t, 101, PublicLyricsStateComplete, oneDocument, map[string]publicV3TestTranslation{
		"original": {texts: []string{"訳"}, translation: "Translator", proofreading: "Proofreader"},
	})
	oneContent, oneCandidate := publicV3CompatibilityFixture(t, 101, oneDocument, oneDetail)
	compatibility, err := buildRecoveryPublicLyricsV2CompatibilityCandidate(oneContent, oneCandidate)
	if err != nil {
		t.Fatal(err)
	}
	converted := compatibility.Details[101]
	if compatibility.Index.Version != 2 || converted.Version != 2 || len(converted.Lines) != 1 ||
		converted.Lines[0].Japanese != oneDetail.Renditions[0].Full.Lines[0].Japanese ||
		converted.TranslationCredits == nil || converted.TranslationCredits.Translation != "Translator" ||
		len(converted.Attributions) != 1 || converted.Attributions[0].RevisionURL != oneDetail.Renditions[0].Provenance[0].RevisionURL {
		t.Fatalf("lossless one-rendition compatibility=%+v", compatibility)
	}

	vocaloid := publicV3TestRendition("vocaloid", "vocaloid-source", model.LyricsSourceRenditionVocaloid, false, true, false, true, "")
	sekai := publicV3TestRendition("sekai", "sekai-source", model.LyricsSourceRenditionSekai, true, true, true, true, "")
	peerDocument := publicV3TestDocument(t, vocaloid, sekai)
	peerDetail := publicV3TestBuildDetail(t, 765, PublicLyricsStateComplete, peerDocument, map[string]publicV3TestTranslation{
		"sekai":    {texts: []string{"訳甲"}, translation: "SEKAI Translator"},
		"vocaloid": {texts: []string{"訳乙"}, translation: "Vocaloid Translator"},
	})
	peerContent, peerCandidate := publicV3CompatibilityFixture(t, 765, peerDocument, peerDetail)
	peerCompatibility, err := buildRecoveryPublicLyricsV2CompatibilityCandidate(peerContent, peerCandidate)
	if err != nil {
		t.Fatal(err)
	}
	if len(peerCompatibility.Details) != 0 || peerCompatibility.Index.Songs[0].State != PublicLyricsStateIncomplete ||
		peerCompatibility.Index.Songs[0].AvailableVersions != nil {
		t.Fatalf("peer renditions were not omitted fail-closed: %+v", peerCompatibility)
	}

	alternate := publicV3TestRendition("alternate.test", "alternate-source", model.LyricsSourceRenditionAlternate, true, false, false, true, "")
	alternateDocument := publicV3TestDocument(t, alternate)
	alternateDetail := publicV3TestBuildDetail(t, 102, PublicLyricsStateComplete, alternateDocument, map[string]publicV3TestTranslation{
		"alternate.test": {texts: []string{"訳"}, translation: "Translator"},
	})
	alternateContent, alternateCandidate := publicV3CompatibilityFixture(t, 102, alternateDocument, alternateDetail)
	alternateCompatibility, err := buildRecoveryPublicLyricsV2CompatibilityCandidate(alternateContent, alternateCandidate)
	if err != nil {
		t.Fatal(err)
	}
	if len(alternateCompatibility.Details) != 0 || alternateCompatibility.Index.Songs[0].State != PublicLyricsStateIncomplete ||
		alternateCompatibility.Index.Songs[0].AvailableVersions != nil {
		t.Fatalf("alternate semantics were flattened into v2: %+v", alternateCompatibility)
	}
}

func TestRecoveryPublicLyricsV3KeepsIncompleteIndexEntryWithoutDetail(t *testing.T) {
	const batchSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const rootSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	document := publicV3TestDocument(t,
		publicV3TestRendition("sekai", "sekai-source", model.LyricsSourceRenditionSekai, true, false, false, false, ""),
	)
	documentBody, documentSHA := publicV3TestDocumentBytes(t, document)
	bindings, err := model.EnumerateLyricsSourceRenditionComponents(document.Renditions)
	if err != nil {
		t.Fatal(err)
	}
	contributions := make([]LyricsRecoveryContributionBackupRecord, len(bindings))
	for index, binding := range bindings {
		contributions[index] = LyricsRecoveryContributionBackupRecord{
			BatchSHA256: batchSHA, MusicID: 765, Component: binding.ComponentKey, RenditionKey: binding.FixedIdentityKey,
		}
	}
	artifacts := make([]LyricsRecoveryArtifactBackupRecord, len(document.FixedIdentities))
	for index, identity := range document.FixedIdentities {
		identityBody, err := json.Marshal(identity)
		if err != nil {
			t.Fatal(err)
		}
		artifacts[index] = LyricsRecoveryArtifactBackupRecord{
			BatchSHA256: batchSHA, MusicID: 765, RenditionKey: identity.RenditionKey,
			FixedIdentityJSON: string(identityBody),
		}
	}
	availability := model.LyricsAvailabilityDocument{
		SchemaVersion:   model.LyricsAvailabilityDocumentSchemaVersion,
		State:           model.LyricsAvailabilityStateIncomplete,
		ReasonCode:      model.LyricsSourceVersionReasonVersionConflict,
		FixedIdentities: []model.LyricsSourceFixedIdentity{},
	}
	availabilityBody, err := json.Marshal(availability)
	if err != nil {
		t.Fatal(err)
	}
	content := LyricsContentExport{
		Music: []CatalogMusicBackupRecord{
			{MusicID: 765, TitleJA: "Complete"},
			{MusicID: 789, TitleJA: "Incomplete"},
		},
		SourceDocuments: []LyricsSourceDocumentBackupRecord{{
			DocumentID: 41, MusicID: 765, SchemaVersion: model.LyricsSourceDocumentSchemaVersionV3,
			DocumentJSON: string(documentBody), DocumentSHA256: documentSHA,
			ManifestBatchSHA256: batchSHA, CreatedAt: 1785456000,
		}},
		RecoveryBatches: []LyricsRecoveryBatchBackupRecord{{
			BatchSHA256: batchSHA, RootSHA256: rootSHA, CatalogCount: 2,
		}},
		RecoveryItems: []LyricsRecoveryItemBackupRecord{
			{BatchSHA256: batchSHA, MusicID: 765, State: string(PublicLyricsStateComplete), DocumentSHA256: documentSHA},
			{BatchSHA256: batchSHA, MusicID: 789, State: string(PublicLyricsStateIncomplete)},
		},
		RecoveryArtifacts:     artifacts,
		RecoveryContributions: contributions,
		AvailabilityDocuments: []LyricsAvailabilityDocumentBackupRecord{{
			BatchSHA256: batchSHA, MusicID: 789, State: string(model.LyricsAvailabilityStateIncomplete),
			DocumentJSON: string(availabilityBody), CreatedAt: 1785456060,
		}},
	}
	candidate, err := buildRecoveryPublicLyricsV3Candidate(content, batchSHA)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidate.Index.Songs) != 2 || len(candidate.Details) != 1 {
		t.Fatalf("data-driven index/detail counts=%d/%d", len(candidate.Index.Songs), len(candidate.Details))
	}
	if _, exists := candidate.Details[789]; exists {
		t.Fatal("incomplete music unexpectedly owns a Public detail")
	}
	incomplete := candidate.Index.Songs[1]
	if incomplete.MusicID != 789 || incomplete.State != PublicLyricsStateIncomplete ||
		incomplete.AvailableVersions != nil || incomplete.NoLyricsReason != "" {
		t.Fatalf("incomplete index semantics=%+v", incomplete)
	}
	if _, err := EncodePublicLyricsV3Index(candidate.Index); err != nil {
		t.Fatal(err)
	}

	duplicatedAvailability := content
	duplicatedAvailability.AvailabilityDocuments = append(
		append([]LyricsAvailabilityDocumentBackupRecord(nil), content.AvailabilityDocuments...),
		content.AvailabilityDocuments[0],
	)
	if _, err := buildRecoveryPublicLyricsV3Candidate(duplicatedAvailability, batchSHA); err == nil {
		t.Fatal("duplicate availability document was accepted")
	}
	extraCatalog := content
	extraCatalog.Music = append(append([]CatalogMusicBackupRecord(nil), content.Music...), CatalogMusicBackupRecord{MusicID: 790, TitleJA: "Extra"})
	if _, err := buildRecoveryPublicLyricsV3Candidate(extraCatalog, batchSHA); err == nil {
		t.Fatal("catalog count drift was accepted")
	}
}

func TestRecoveryPublicLyricsV3UsesUnifiedLocalizationMetadataAndFailsClosed(t *testing.T) {
	document := publicV3TestDocument(t,
		publicV3TestRendition("original", "original-source", model.LyricsSourceRenditionOriginal, true, false, false, false, ""),
		publicV3TestRendition("sekai", "sekai-source", model.LyricsSourceRenditionSekai, true, false, false, false, ""),
	)
	content, batchSHA := publicV3RecoverySourceFixture(t, 766, document)
	const localizedAt = int64(1785456120)
	for index, rendition := range document.Renditions {
		localization := LyricsRenditionLocalizationBackupRecord{
			DocumentID: 41, RenditionKey: rendition.RenditionKey, Locale: "zh-CN",
			UpdatedAt: localizedAt, UpdatedBy: "editor", Revision: 2,
		}
		text := " 译文\n续 "
		if index == 0 {
			localization.TranslationCredit = "仅署名"
			text = ""
		}
		content.RenditionLocalizations = append(content.RenditionLocalizations, localization)
		content.RenditionTranslationLines = append(content.RenditionTranslationLines,
			LyricsRenditionTranslationLineBackupRecord{
				DocumentID: 41, RenditionKey: rendition.RenditionKey, Locale: "zh-CN", Position: 0, Text: text,
			})
	}

	candidate, err := buildRecoveryPublicLyricsV3Candidate(content, batchSHA)
	if err != nil {
		t.Fatal(err)
	}
	detail := candidate.Details[766]
	index := candidate.Index.Songs[0]
	if detail.Revision != 2 || detail.UpdatedAt != formatTimestamp(localizedAt) ||
		index.Revision != detail.Revision || index.UpdatedAt != detail.UpdatedAt {
		t.Fatalf("localized candidate metadata detail=%+v index=%+v", detail, index)
	}
	if detail.Renditions[0].TranslationCredits == nil ||
		detail.Renditions[0].TranslationCredits.Translation != "仅署名" ||
		detail.Renditions[0].Full.Lines[0].Chinese != "" ||
		detail.Renditions[1].Full.Lines[0].Chinese != " 译文\n续 " {
		t.Fatalf("credit-only and edge localization contract=%+v", detail.Renditions)
	}

	for name, mutate := range map[string]func(*LyricsContentExport){
		"missing coverage": func(input *LyricsContentExport) {
			input.RenditionLocalizations = input.RenditionLocalizations[:1]
		},
		"inconsistent revision": func(input *LyricsContentExport) {
			input.RenditionLocalizations[1].Revision++
		},
		"inconsistent updatedAt": func(input *LyricsContentExport) {
			input.RenditionLocalizations[1].UpdatedAt++
		},
	} {
		t.Run(name, func(t *testing.T) {
			invalid := cloneLyricsContentExport(t, content)
			mutate(&invalid)
			if _, err := buildRecoveryPublicLyricsV3Candidate(invalid, batchSHA); err == nil {
				t.Fatal("inconsistent rendition localization metadata was accepted")
			}
		})
	}
}

func TestPublicLyricsV3StrictDecodersRejectNestedTampering(t *testing.T) {
	root := "../../../contracts/public-lyrics/v3"
	validDetail := readPublicLyricsV3Contract(t, root+"/detail-legacy-one-rendition.fixture.json")
	if _, err := DecodePublicLyricsV3Detail(validDetail); err != nil {
		t.Fatal(err)
	}
	validIndex := readPublicLyricsV3Contract(t, root+"/index.fixture.json")
	if _, err := DecodePublicLyricsV3Index(validIndex); err != nil {
		t.Fatal(err)
	}

	mutateDetail := func(mutate func(map[string]any)) []byte {
		t.Helper()
		var document map[string]any
		if err := json.Unmarshal(validDetail, &document); err != nil {
			t.Fatal(err)
		}
		mutate(document)
		body, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	firstRendition := func(document map[string]any) map[string]any {
		return document["renditions"].([]any)[0].(map[string]any)
	}
	firstSegment := func(document map[string]any) map[string]any {
		full := firstRendition(document)["full"].(map[string]any)
		line := full["lines"].([]any)[0].(map[string]any)
		return line["segments"].([]any)[0].(map[string]any)
	}

	tampered := map[string][]byte{
		"unknown nested field": mutateDetail(func(document map[string]any) {
			firstSegment(document)["private"] = true
		}),
		"Han without reading": mutateDetail(func(document map[string]any) {
			ruby := firstSegment(document)["ruby"].([]any)
			delete(ruby[0].(map[string]any), "reading")
		}),
		"reading on kana": mutateDetail(func(document map[string]any) {
			ruby := firstSegment(document)["ruby"].([]any)
			ruby[1].(map[string]any)["reading"] = "う"
		}),
		"reading without kana": mutateDetail(func(document map[string]any) {
			ruby := firstSegment(document)["ruby"].([]any)
			ruby[0].(map[string]any)["reading"] = "ー"
		}),
		"mixed Han and kana base": mutateDetail(func(document map[string]any) {
			segment := firstSegment(document)
			ruby := segment["ruby"].([]any)
			ruby[0].(map[string]any)["text"] = "歌う"
			segment["ruby"] = ruby[:1]
		}),
		"wrong revision host": mutateDetail(func(document map[string]any) {
			provenance := firstRendition(document)["provenance"].([]any)
			provenance[0].(map[string]any)["revisionUrl"] = "https://example.invalid/wiki/Test?oldid=1201"
		}),
		"wrong provider license": mutateDetail(func(document map[string]any) {
			provenance := firstRendition(document)["provenance"].([]any)
			provenance[0].(map[string]any)["licenseName"] = "CC0"
		}),
		"missing Full text provenance": mutateDetail(func(document map[string]any) {
			rendition := firstRendition(document)
			provenance := rendition["provenance"].([]any)
			rendition["provenance"] = provenance[1:]
		}),
		"missing relation provenance": mutateDetail(func(document map[string]any) {
			rendition := firstRendition(document)
			provenance := rendition["provenance"].([]any)
			rendition["provenance"] = append(provenance[:1], provenance[2:]...)
		}),
		"mutable Moegirl URL": mutateDetail(func(document map[string]any) {
			provenance := firstRendition(document)["provenance"].([]any)
			attribution := provenance[0].(map[string]any)
			attribution["provider"] = "moegirl"
			attribution["revisionUrl"] = "https://moegirl.icu/wiki/Public_Test"
			attribution["licenseName"] = "CC BY-NC-SA 3.0"
			attribution["licenseUrl"] = "https://creativecommons.org/licenses/by-nc-sa/3.0/"
		}),
		"null optional object": mutateDetail(func(document map[string]any) {
			firstRendition(document)["translationCredits"] = nil
		}),
		"trailing JSON":        append(append([]byte(nil), validDetail...), []byte(` {}`)...),
		"duplicate root field": bytes.Replace(validDetail, []byte(`"version": 3`), []byte(`"version": 3, "version": 3`), 1),
	}
	for name, body := range tampered {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodePublicLyricsV3Detail(body); err == nil {
				t.Fatal("tampered detail was accepted")
			}
		})
	}

	projectionFixture := readPublicLyricsV3Contract(t, root+"/detail-full-game-projection.fixture.json")
	mutateProjection := func(mutate func(map[string]any)) []byte {
		t.Helper()
		var document map[string]any
		if err := json.Unmarshal(projectionFixture, &document); err != nil {
			t.Fatal(err)
		}
		mutate(document)
		body, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	for name, body := range map[string][]byte{
		"projection translation drift": mutateProjection(func(document map[string]any) {
			rendition := document["renditions"].([]any)[0].(map[string]any)
			fullLine := rendition["full"].(map[string]any)["lines"].([]any)[0].(map[string]any)
			gameLine := rendition["game"].(map[string]any)["lines"].([]any)[0].(map[string]any)
			fullLine["zh-CN"] = "一致"
			gameLine["zh-CN"] = "漂移"
		}),
		"projection relation order drift": mutateProjection(func(document map[string]any) {
			rendition := document["renditions"].([]any)[0].(map[string]any)
			rendition["relation"].(map[string]any)["lineIds"] = []any{"full-000003", "full-000001"}
			gameLines := rendition["game"].(map[string]any)["lines"].([]any)
			gameLines[0].(map[string]any)["japanese"] = "進もう"
			gameLines[1].(map[string]any)["japanese"] = "歌う"
		}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodePublicLyricsV3Detail(body); err == nil {
				t.Fatal("tampered exact projection was accepted")
			}
		})
	}

	var emptyIndex PublicLyricsV3IndexDocument
	emptyIndex.Version = 3
	emptyIndex.Songs = []PublicLyricsIndexSong{}
	if _, err := EncodePublicLyricsV3Index(emptyIndex); err == nil {
		t.Fatal("empty Public v3 index was accepted")
	}
	var index map[string]any
	if err := json.Unmarshal(validIndex, &index); err != nil {
		t.Fatal(err)
	}
	index["songs"].([]any)[0].(map[string]any)["private"] = true
	unknownIndex, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodePublicLyricsV3Index(unknownIndex); err == nil {
		t.Fatal("unknown index field was accepted")
	}
	if err := json.Unmarshal(validIndex, &index); err != nil {
		t.Fatal(err)
	}
	index["songs"].([]any)[0].(map[string]any)["title"].(map[string]any)["zh-CN"] = nil
	nullIndex, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodePublicLyricsV3Index(nullIndex); err == nil {
		t.Fatal("null optional index field was accepted")
	}
}

func TestPublicLyricsV3TreatsIdeographicZeroAsPlainNumericText(t *testing.T) {
	root := "../../../contracts/public-lyrics/v3"
	var document map[string]any
	if err := json.Unmarshal(readPublicLyricsV3Contract(t, root+"/detail-legacy-one-rendition.fixture.json"), &document); err != nil {
		t.Fatal(err)
	}
	rendition := document["renditions"].([]any)[0].(map[string]any)
	line := rendition["full"].(map[string]any)["lines"].([]any)[0].(map[string]any)
	segment := line["segments"].([]any)[0].(map[string]any)
	line["japanese"] = "〇"
	segment["text"] = "〇"
	segment["ruby"] = []any{map[string]any{"text": "〇"}}

	plain, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodePublicLyricsV3Detail(plain); err != nil {
		t.Fatalf("plain ideographic zero was rejected: %v", err)
	}

	segment["ruby"].([]any)[0].(map[string]any)["reading"] = "れい"
	annotated, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodePublicLyricsV3Detail(annotated); err == nil {
		t.Fatal("numeric ideographic zero received a ruby reading")
	}
}

func publicV3RecoverySourceFixture(t *testing.T, musicID int, document model.LyricsSourceDocument) (LyricsContentExport, string) {
	t.Helper()
	const batchSHA = "f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0"
	const rootSHA = "e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0"
	body, digest := publicV3TestDocumentBytes(t, document)
	bindings, err := model.EnumerateLyricsSourceRenditionComponents(document.Renditions)
	if err != nil {
		t.Fatal(err)
	}
	contributions := make([]LyricsRecoveryContributionBackupRecord, len(bindings))
	for index, binding := range bindings {
		contributions[index] = LyricsRecoveryContributionBackupRecord{
			BatchSHA256: batchSHA, MusicID: musicID,
			Component: binding.ComponentKey, RenditionKey: binding.FixedIdentityKey,
		}
	}
	artifacts := make([]LyricsRecoveryArtifactBackupRecord, len(document.FixedIdentities))
	for index, identity := range document.FixedIdentities {
		identityBody, err := json.Marshal(identity)
		if err != nil {
			t.Fatal(err)
		}
		artifacts[index] = LyricsRecoveryArtifactBackupRecord{
			BatchSHA256: batchSHA, MusicID: musicID, RenditionKey: identity.RenditionKey,
			FixedIdentityJSON: string(identityBody),
		}
	}
	return LyricsContentExport{
		Music: []CatalogMusicBackupRecord{{MusicID: musicID, TitleJA: "Localized metadata fixture"}},
		SourceDocuments: []LyricsSourceDocumentBackupRecord{{
			DocumentID: 41, MusicID: musicID, SchemaVersion: model.LyricsSourceDocumentSchemaVersionV3,
			DocumentJSON: string(body), DocumentSHA256: digest, ManifestBatchSHA256: batchSHA,
			CreatedAt: 1785456000,
		}},
		RecoveryBatches: []LyricsRecoveryBatchBackupRecord{{
			BatchSHA256: batchSHA, RootSHA256: rootSHA, CatalogCount: 1,
		}},
		RecoveryItems: []LyricsRecoveryItemBackupRecord{{
			BatchSHA256: batchSHA, MusicID: musicID, State: string(PublicLyricsStateComplete), DocumentSHA256: digest,
		}},
		RecoveryArtifacts: artifacts, RecoveryContributions: contributions,
	}, batchSHA
}

func publicV3CompatibilityFixture(t *testing.T, musicID int, document model.LyricsSourceDocument, detail PublicLyricsV3DetailDocument) (LyricsContentExport, RecoveryPublicLyricsV3Candidate) {
	t.Helper()
	const batchSHA = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	const rootSHA = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	body, digest := publicV3TestDocumentBytes(t, document)
	content := LyricsContentExport{SourceDocuments: []LyricsSourceDocumentBackupRecord{{
		DocumentID: 41, MusicID: musicID, SchemaVersion: model.LyricsSourceDocumentSchemaVersionV3,
		DocumentJSON: string(body), DocumentSHA256: digest, ManifestBatchSHA256: batchSHA, CreatedAt: 1785456000,
	}}}
	candidate := RecoveryPublicLyricsV3Candidate{
		BatchSHA256: batchSHA, RootSHA256: rootSHA,
		Index: PublicLyricsV3IndexDocument{Version: 3, Songs: []PublicLyricsIndexSong{{
			MusicID: musicID, Revision: detail.Revision, UpdatedAt: detail.UpdatedAt, State: detail.State,
			Title: model.LocalizedTitle{Japanese: "Compatibility"}, AvailableVersions: publicV3AvailableVersions(detail),
		}}},
		Details: map[int]PublicLyricsV3DetailDocument{musicID: detail},
	}
	return content, candidate
}

func publicV3TestRendition(
	key, identityKey string,
	kind model.LyricsSourceRenditionKind,
	hasFull, hasGame, exactProjection, legacyReading bool,
	performerID string,
) model.LyricsSourceRendition {
	label := map[model.LyricsSourceRenditionKind]string{
		model.LyricsSourceRenditionOriginal:  "Original Version",
		model.LyricsSourceRenditionSekai:     "SEKAI Version",
		model.LyricsSourceRenditionVocaloid:  "VIRTUAL SINGER",
		model.LyricsSourceRenditionAlternate: "Alternate Vocal Test",
	}[kind]
	ref := model.LyricsSourceComponentRef{RenditionKey: identityKey}
	rendition := model.LyricsSourceRendition{
		RenditionKey:          key,
		SourceKind:            kind,
		ReasonCode:            model.LyricsSourceVersionReasonUntaggedFullOnly,
		SourceTabPaths:        []model.LyricsSourceTabPath{{label}},
		FullPerformerEvidence: model.LyricsSourcePerformerEvidenceNone,
		GamePerformerEvidence: model.LyricsSourcePerformerEvidenceNone,
		Relation:              model.LyricsSourceRenditionRelation{Kind: model.LyricsSourceRenditionRelationNone},
		Provenance: model.LyricsSourceRenditionProvenance{
			RelationEvidence: ref,
			VersionEvidence:  ref,
		},
	}
	if kind == model.LyricsSourceRenditionAlternate {
		rendition.SourceTabPaths = []model.LyricsSourceTabPath{{"Alternate Vocal", label}}
	}
	if hasFull && hasGame {
		rendition.SourceTabPaths = []model.LyricsSourceTabPath{{"Full Version", label}, {"Game Version", label}}
	} else if hasGame {
		rendition.SourceTabPaths = []model.LyricsSourceTabPath{{"Game Version", label}}
	}
	if hasFull {
		rendition.Full = publicV3TestSide(key, identityKey, kind, label, model.LyricsSourceRenditionSideFull, legacyReading, performerID)
		rendition.Provenance.FullText = publicV3TestRef(identityKey)
	}
	if hasGame {
		rendition.Game = publicV3TestSide(key, identityKey, kind, label, model.LyricsSourceRenditionSideGame, legacyReading, performerID)
		rendition.Provenance.GameText = publicV3TestRef(identityKey)
	}
	if legacyReading {
		if hasFull {
			rendition.Provenance.FullRuby = publicV3TestRef(identityKey)
		}
		if hasGame {
			rendition.Provenance.GameRuby = publicV3TestRef(identityKey)
		}
	}
	if performerID != "" {
		rendition.SourcePerformerIDs = []string{performerID}
		rendition.PrivateReview = &model.LyricsSourcePrivateReview{
			PerformerSegmentationEvidence: model.LyricsSourcePerformerSegmentationEvidenceAuthoritativeCompleteStructured,
		}
		if hasFull {
			rendition.FullPerformerEvidence = model.LyricsSourcePerformerEvidenceSourceComplete
			rendition.Provenance.FullPerformerSegmentation = publicV3TestRef(identityKey)
		}
		if hasGame {
			rendition.GamePerformerEvidence = model.LyricsSourcePerformerEvidenceSourceComplete
			rendition.Provenance.GamePerformerSegmentation = publicV3TestRef(identityKey)
		}
	}
	switch {
	case hasFull && hasGame && exactProjection:
		rendition.ReasonCode = model.LyricsSourceVersionReasonTaggedFullAndGame
		rendition.Relation = model.LyricsSourceRenditionRelation{
			Kind:             model.LyricsSourceRenditionRelationExactProjection,
			FullRenditionKey: key,
			LineIDs:          []string{"full-000001"},
		}
	case !hasFull && hasGame:
		rendition.ReasonCode = model.LyricsSourceVersionReasonTaggedGameOnly
	case hasFull:
		rendition.ReasonCode = model.LyricsSourceVersionReasonUntaggedFullOnly
	}
	return rendition
}

func publicV3TestSide(
	renditionKey, identityKey string,
	kind model.LyricsSourceRenditionKind,
	label string,
	side model.LyricsSourceRenditionSide,
	legacyReading bool,
	performerID string,
) *model.LyricsSourceFull {
	evidence := &model.LyricsSourceReadingEvidence{
		Kind:             model.LyricsSourceReadingEvidenceDeterministicDictionary,
		GeneratorVersion: "public-v3-test",
	}
	if legacyReading {
		evidence = &model.LyricsSourceReadingEvidence{
			Kind:                 model.LyricsSourceReadingEvidenceLegacyV2Component,
			FixedIdentityKey:     identityKey,
			RenditionKey:         renditionKey,
			Side:                 side,
			SourceRowOrdinal:     1,
			SourceSegmentOrdinal: 1,
		}
	}
	performers := []model.LyricsSourcePerformer{}
	performerIDs := []string{}
	if performerID != "" {
		performers = append(performers, model.LyricsSourcePerformer{
			PerformerID: performerID,
			Name:        "Test Performer",
			Color:       "#39C5BB",
		})
		performerIDs = append(performerIDs, performerID)
	}
	return &model.LyricsSourceFull{
		Version:              model.LyricsSourceVersion{Kind: string(kind), Label: label},
		Performers:           performers,
		RubyGeneratorVersion: "public-v3-test",
		Lines: []model.LyricsSourceFullLine{{
			ID:   string(side) + "-000001",
			Text: "歌う",
			Segments: []model.LyricsSourceSegment{{
				Text:         "歌う",
				PerformerIDs: performerIDs,
				Ruby: []model.LyricsSourceRubySpan{
					{Text: "歌", Reading: "うた", ReadingEvidence: evidence},
					{Text: "う"},
				},
			}},
			TrailingPerformerIDs: []string{},
		}},
	}
}

func publicV3TestRef(identityKey string) *model.LyricsSourceComponentRef {
	return &model.LyricsSourceComponentRef{RenditionKey: identityKey}
}

func publicV3TestDocument(t *testing.T, renditions ...model.LyricsSourceRendition) model.LyricsSourceDocument {
	t.Helper()
	identities := make([]model.LyricsSourceFixedIdentity, 0, len(renditions))
	seen := map[string]bool{}
	for index, rendition := range renditions {
		bindings, err := model.EnumerateLyricsSourceRenditionComponents([]model.LyricsSourceRendition{rendition})
		if err != nil {
			t.Fatalf("rendition fixture %q: %v", rendition.RenditionKey, err)
		}
		for _, binding := range bindings {
			if seen[binding.FixedIdentityKey] {
				continue
			}
			seen[binding.FixedIdentityKey] = true
			identities = append(identities, publicV3TestIdentity(binding.FixedIdentityKey, 1200+index))
		}
	}
	document := model.LyricsSourceDocument{
		SchemaVersion:   model.LyricsSourceDocumentSchemaVersionV3,
		FixedIdentities: identities,
		Renditions:      model.CloneLyricsSourceRenditions(renditions),
	}
	if err := model.ValidateLyricsSourceDocument(document); err != nil {
		t.Fatalf("source v3 document fixture: %v", err)
	}
	return document
}

func publicV3TestIdentity(key string, revisionID int) model.LyricsSourceFixedIdentity {
	return model.LyricsSourceFixedIdentity{
		Provider:          model.LyricsSourceProviderSekaipedia,
		Origin:            model.LyricsSourceOriginSekaipedia,
		PageID:            revisionID + 100,
		RevisionID:        revisionID,
		SHA1:              strings.Repeat("a", 40),
		Title:             "Public v3 test " + key,
		CanonicalURL:      "https://www.sekaipedia.org/wiki/Public_v3_test_" + key + "?oldid=" + jsonNumber(revisionID),
		RevisionTimestamp: "2026-07-31T00:00:00Z",
		FetchedAt:         "2026-07-31T00:01:00Z",
		Categories:        []string{"Test"},
		Section:           "Lyrics",
		RenditionKey:      key,
		IndexEvidenceRefs: []model.LyricsSourceIndexEvidenceRef{{
			EvidenceID: "test-" + key,
			SHA256:     strings.Repeat("b", 64),
		}},
	}
}

func publicV3TestBuildDetail(
	t *testing.T,
	musicID int,
	state PublicLyricsAvailabilityState,
	document model.LyricsSourceDocument,
	translations map[string]publicV3TestTranslation,
) PublicLyricsV3DetailDocument {
	t.Helper()
	const batchSHA = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	body, digest := publicV3TestDocumentBytes(t, document)
	bindings, err := model.EnumerateLyricsSourceRenditionComponents(document.Renditions)
	if err != nil {
		t.Fatal(err)
	}
	contributions := make(map[string]string, len(bindings))
	for _, binding := range bindings {
		contributions[binding.ComponentKey] = binding.FixedIdentityKey
	}
	artifacts := make(map[string]LyricsRecoveryArtifactBackupRecord, len(document.FixedIdentities))
	for _, identity := range document.FixedIdentities {
		identityBody, err := json.Marshal(identity)
		if err != nil {
			t.Fatal(err)
		}
		artifacts[identity.RenditionKey] = LyricsRecoveryArtifactBackupRecord{
			BatchSHA256: batchSHA, MusicID: musicID, RenditionKey: identity.RenditionKey,
			FixedIdentityJSON: string(identityBody),
		}
	}
	localizations := make([]LyricsRenditionLocalizationBackupRecord, 0, len(translations))
	lines := []LyricsRenditionTranslationLineBackupRecord{}
	for key, translation := range translations {
		localizations = append(localizations, LyricsRenditionLocalizationBackupRecord{
			DocumentID: 41, RenditionKey: key, Locale: "zh-CN",
			TranslationCredit: translation.translation, ProofreadingCredit: translation.proofreading,
			UpdatedAt: 1785456060, UpdatedBy: "public-v3-test", Revision: 2,
		})
		for position, text := range translation.texts {
			lines = append(lines, LyricsRenditionTranslationLineBackupRecord{
				DocumentID: 41, RenditionKey: key, Locale: "zh-CN", Position: position, Text: text,
			})
		}
	}
	sort.Slice(localizations, func(left, right int) bool {
		return localizations[left].RenditionKey < localizations[right].RenditionKey
	})
	sort.Slice(lines, func(left, right int) bool {
		if lines[left].RenditionKey != lines[right].RenditionKey {
			return lines[left].RenditionKey < lines[right].RenditionKey
		}
		return lines[left].Position < lines[right].Position
	})
	detail, err := buildPublicLyricsV3Detail(
		LyricsRecoveryItemBackupRecord{
			BatchSHA256: batchSHA, MusicID: musicID, State: string(state), DocumentSHA256: digest,
		},
		LyricsSourceDocumentBackupRecord{
			DocumentID: 41, MusicID: musicID, SchemaVersion: model.LyricsSourceDocumentSchemaVersionV3,
			DocumentJSON: string(body), DocumentSHA256: digest, ManifestBatchSHA256: batchSHA,
			CreatedAt: 1785456000,
		},
		document, contributions, artifacts, localizations, lines,
	)
	if err != nil {
		t.Fatal(err)
	}
	return detail
}

func publicV3TestDocumentBytes(t *testing.T, document model.LyricsSourceDocument) ([]byte, string) {
	t.Helper()
	body, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	return body, hex.EncodeToString(digest[:])
}

func jsonNumber(value int) string {
	body, _ := json.Marshal(value)
	return string(body)
}
