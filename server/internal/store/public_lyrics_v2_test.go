package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"moesekai/server/internal/model"
)

func TestPublicLyricsV2PublishesAuthoritativeFullProjectionAndAttributions(t *testing.T) {
	s := setupLyricsStore(t)
	input, document := publicLyricsV2SekaiFixture(10)
	saved := savePublicLyricsV2Fixture(t, s, input, document)

	index, details, err := s.PublishedLyrics()
	if err != nil {
		t.Fatal(err)
	}
	if index.Version != 2 || len(index.Songs) != 0 || len(details) != 0 {
		t.Fatalf("source-backed empty projection index=%+v details=%+v", index, details)
	}

	published, err := s.PublishLyrics(saved.MusicID, saved.Revision)
	if err != nil || published.Status != "published" {
		t.Fatalf("publish v2=%+v err=%v", published, err)
	}
	var payload string
	if err := s.db.QueryRow(`SELECT payload_json FROM song_lyrics_publications WHERE music_id=?`, saved.MusicID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{
		`"attribution"`, `"sourceUrl"`, `"sourcePageId"`, `"sourceRevisionId"`, `"sourceSha1"`,
		`"sourceFetchedAt"`, `"fixedIdentities"`, `"provenance"`, `"indexEvidenceRefs"`,
		`"privateReview"`, `"revisionTimestamp"`, `"compositionRenditionKey"`, `"versionReason"`,
		`"full_text"`, `"performer_segmentation"`, `"game_projection"`, `"version_evidence"`,
		`"rawBytes"`, "private source note", "private license note", "romaji", "romanization",
	} {
		if strings.Contains(payload, private) {
			t.Fatalf("public v2 payload leaked %q: %s", private, payload)
		}
	}
	var stored PublicLyricsDetailDocument
	if err := json.Unmarshal([]byte(payload), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Version != 2 || stored.MusicID != saved.MusicID ||
		!reflect.DeepEqual(stored.AvailableVersions, []string{"full", "game"}) || stored.GameProjection == nil ||
		stored.GameProjection.ReasonCode != model.LyricsSourceVersionReasonTaggedFullAndGame ||
		!reflect.DeepEqual(stored.GameProjection.LineIDs, []string{"full-000001"}) {
		t.Fatalf("stored v2 header/projection=%+v", stored)
	}
	if len(stored.Attributions) != 2 || stored.Attributions[0].Provider != model.LyricsSourceProviderVocaloidFandom ||
		stored.Attributions[1].Provider != model.LyricsSourceProviderMoegirl {
		t.Fatalf("component-derived attributions=%+v", stored.Attributions)
	}
	if stored.TranslationCredits == nil || stored.TranslationCredits.Translation != "Legacy Translator" ||
		stored.TranslationCredits.Proofreading != "" {
		t.Fatalf("legacy-compatible translation credits=%+v", stored.TranslationCredits)
	}
	for _, attribution := range stored.Attributions {
		if attribution.Title == "Inspected but unused" {
			t.Fatalf("unused source was attributed: %+v", stored.Attributions)
		}
	}
	if len(stored.Lines) != len(document.Full.Lines) || stored.Lines[0].ID != document.Full.Lines[0].ID ||
		stored.Lines[0].Japanese != document.Full.Lines[0].Text ||
		stored.Lines[0].Segments[0].Ruby[0].Reading != document.Full.Lines[0].Segments[0].Ruby[0].Reading {
		t.Fatalf("authoritative Full projection=%+v", stored.Lines)
	}

	index, details, err = s.PublishedLyrics()
	if err != nil {
		t.Fatal(err)
	}
	if index.Version != 2 || len(index.Songs) != 1 ||
		!reflect.DeepEqual(index.Songs[0].AvailableVersions, stored.AvailableVersions) ||
		!reflect.DeepEqual(details[saved.MusicID].AvailableVersions, stored.AvailableVersions) {
		t.Fatalf("v2 index/detail agreement index=%+v detail=%+v", index, details[saved.MusicID])
	}
}

func TestPublicLyricsV2TranslationCreditsStrictDecodeAndOmission(t *testing.T) {
	s := setupLyricsStore(t)
	input, document := publicLyricsV2VirtualSingerFixture(10)
	saved := savePublicLyricsV2Fixture(t, s, input, document)
	if _, err := s.PublishLyrics(saved.MusicID, saved.Revision); err != nil {
		t.Fatal(err)
	}
	var payload string
	if err := s.db.QueryRow(`SELECT payload_json FROM song_lyrics_publications WHERE music_id=?`, saved.MusicID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	bundle, err := s.loadPublicLyricsSourceBundle(s.db, saved.MusicID)
	if err != nil {
		t.Fatal(err)
	}
	catalogPerformers, err := loadCatalogPerformerAliases(s.db)
	if err != nil {
		t.Fatal(err)
	}
	updatedAt, err := parseTimestamp(saved.UpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodePublicLyricsV2Detail(payload, saved.MusicID, saved.Revision, updatedAt, bundle, catalogPerformers); err != nil {
		t.Fatalf("valid translationCredits payload: %v", err)
	}

	for name, credits := range map[string]any{
		"empty object":         map[string]any{},
		"non-object":           "Translator",
		"whitespace only":      map[string]any{"translation": "   "},
		"leading whitespace":   map[string]any{"translation": " Translator"},
		"wrong field type":     map[string]any{"translation": 26},
		"translation oversize": map[string]any{"translation": strings.Repeat("x", maxLyricsMetadataBytes+1)},
		"proofreading oversize": map[string]any{
			"translation": "Translator", "proofreading": strings.Repeat("x", maxLyricsMetadataBytes+1),
		},
		"unknown key": map[string]any{"translation": "Translator", "editor": "Private"},
	} {
		t.Run(name, func(t *testing.T) {
			var document map[string]any
			if err := json.Unmarshal([]byte(payload), &document); err != nil {
				t.Fatal(err)
			}
			document["translationCredits"] = credits
			mutated, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodePublicLyricsV2Detail(string(mutated), saved.MusicID, saved.Revision, updatedAt, bundle, catalogPerformers); err == nil {
				t.Fatalf("accepted invalid translationCredits: %s", mutated)
			}
		})
	}

	withoutCredits := saved
	withoutCredits.Attribution = ""
	withoutCredits.TranslationCredit = ""
	withoutCredits.ProofreadingCredit = ""
	public, err := buildPublicLyricsV2(withoutCredits, bundle, catalogPerformers)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"translationCredits"`) {
		t.Fatalf("empty translation credits were not omitted: %s", encoded)
	}
}

func TestPublicLyricsV2UntaggedIdentityProjectionIsExact(t *testing.T) {
	s := setupLyricsStore(t)
	input, document := publicLyricsV2SekaiFixture(10)
	document.ReasonCode = model.LyricsSourceVersionReasonUntaggedUncutIdentity
	document.GameProjection.LineIDs = []string{"full-000001", "full-000002"}
	saved := savePublicLyricsV2Fixture(t, s, input, document)
	if _, err := s.PublishLyrics(saved.MusicID, saved.Revision); err != nil {
		t.Fatal(err)
	}
	index, details, err := s.PublishedLyrics()
	if err != nil {
		t.Fatal(err)
	}
	detail := details[saved.MusicID]
	if !reflect.DeepEqual(detail.AvailableVersions, []string{"full", "game"}) ||
		detail.GameProjection == nil ||
		detail.GameProjection.ReasonCode != model.LyricsSourceVersionReasonUntaggedUncutIdentity ||
		!reflect.DeepEqual(detail.GameProjection.LineIDs, []string{"full-000001", "full-000002"}) ||
		!reflect.DeepEqual(index.Songs[0].AvailableVersions, detail.AvailableVersions) {
		t.Fatalf("untagged identity projection index=%+v detail=%+v", index, detail)
	}
}

func TestPublicLyricsV2VocaloidOnlyAllowsEmptyPublicFields(t *testing.T) {
	s := setupLyricsStore(t)
	input, document := publicLyricsV2VocaloidFixture(10, 1)
	saved := savePublicLyricsV2Fixture(t, s, input, document)
	if saved.Attribution != "" || saved.Lines[0].Chinese != "" || saved.Lines[0].English != "" ||
		len(saved.Lines[0].Segments[0].PerformerIDs) != 0 {
		t.Fatalf("private Vocaloid fixture=%+v", saved)
	}
	if _, err := s.PublishLyrics(saved.MusicID, saved.Revision); err != nil {
		t.Fatal(err)
	}
	index, details, err := s.PublishedLyrics()
	if err != nil {
		t.Fatal(err)
	}
	detail := details[saved.MusicID]
	if index.Version != 2 || !reflect.DeepEqual(index.Songs[0].AvailableVersions, []string{"full"}) ||
		!reflect.DeepEqual(detail.AvailableVersions, []string{"full"}) || detail.GameProjection != nil ||
		detail.TranslationCredits == nil || detail.TranslationCredits.Translation != "Vocaloid Translator" ||
		detail.TranslationCredits.Proofreading != "" || len(detail.Lines) != 1 || len(detail.Lines[0].Segments) != 1 ||
		detail.Lines[0].Segments[0].Text != detail.Lines[0].Japanese ||
		detail.Lines[0].Segments[0].PerformerIDs == nil || len(detail.Lines[0].Segments[0].PerformerIDs) != 0 ||
		detail.Lines[0].Chinese != "" || detail.Lines[0].English != "" {
		t.Fatalf("Vocaloid-only public detail=%+v index=%+v", detail, index)
	}
	var payload string
	if err := s.db.QueryRow(`SELECT payload_json FROM song_lyrics_publications WHERE music_id=?`, saved.MusicID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, `"performerIds":[]`) || !strings.Contains(payload, `"zh-CN":""`) ||
		!strings.Contains(payload, `"en-US":""`) {
		t.Fatalf("Vocaloid-only empty arrays/strings were not explicit: %s", payload)
	}
}

func TestPublicLyricsV2AcceptsGenuinelyUnattributedSekaiLines(t *testing.T) {
	s := setupLyricsStore(t)
	input, document := publicLyricsV2SekaiFixture(10)
	document.Full.Performers = []model.LyricsSourcePerformer{}
	document.Provenance.PerformerSegmentation = nil
	for lineIndex := range document.Full.Lines {
		sourceLine := &document.Full.Lines[lineIndex]
		ruby := []model.LyricsSourceRubySpan{}
		for _, segment := range sourceLine.Segments {
			ruby = append(ruby, segment.Ruby...)
		}
		sourceLine.Segments = []model.LyricsSourceSegment{{
			Text: sourceLine.Text, PerformerIDs: []string{}, Ruby: ruby,
		}}
		sourceLine.TrailingPerformerIDs = []string{}
		input.Lines[lineIndex].Segments = []model.LyricSegment{{
			Text: sourceLine.Text, PerformerIDs: []int{}, Ruby: func() []model.LyricRubySpan {
				result := make([]model.LyricRubySpan, len(ruby))
				for index, span := range ruby {
					result[index] = model.LyricRubySpan{Text: span.Text, Reading: span.Reading}
				}
				return result
			}(),
		}}
	}
	saved := savePublicLyricsV2Fixture(t, s, input, document)
	if _, err := s.PublishLyrics(saved.MusicID, saved.Revision); err != nil {
		t.Fatalf("publish genuinely unattributed SEKAI lines: %v", err)
	}
	_, details, err := s.PublishedLyrics()
	if err != nil {
		t.Fatal(err)
	}
	for lineIndex, line := range details[saved.MusicID].Lines {
		if len(line.Segments) != 1 || line.Segments[0].PerformerIDs == nil || len(line.Segments[0].PerformerIDs) != 0 {
			t.Fatalf("public unattributed SEKAI line %d=%+v", lineIndex+1, line)
		}
	}
}

func TestPublicLyricsV2RejectsEditedPerformerAssignment(t *testing.T) {
	s := setupLyricsStore(t)
	input, document := publicLyricsV2SekaiFixture(10)
	input.Lines[0].Segments[0].PerformerIDs = []int{}
	saved := savePublicLyricsV2Fixture(t, s, input, document)
	_, err := s.PublishLyrics(saved.MusicID, saved.Revision)
	var contractErr *LyricsContractError
	if !errors.As(err, &contractErr) || contractErr.Code != "incomplete_publication" ||
		!strings.Contains(strings.Join(contractErr.Details, "; "), "performer assignment is stale") {
		t.Fatalf("edited performer assignment publication error=%#v", err)
	}
}

func TestPublicLyricsV2RejectsSameLengthPerformerSubstitutionAndOrderChanges(t *testing.T) {
	t.Run("same-length substitution", func(t *testing.T) {
		s := setupLyricsStore(t)
		input, document := publicLyricsV2SekaiFixture(10)
		input.Lines[0].Segments[0].PerformerIDs = []int{2}
		saved := savePublicLyricsV2Fixture(t, s, input, document)
		_, err := s.PublishLyrics(saved.MusicID, saved.Revision)
		var contractErr *LyricsContractError
		if !errors.As(err, &contractErr) || contractErr.Code != "incomplete_publication" ||
			!strings.Contains(strings.Join(contractErr.Details, "; "), "does not exactly match authoritative source IDs") {
			t.Fatalf("same-length performer substitution error=%#v", err)
		}
	})

	t.Run("order change", func(t *testing.T) {
		s := setupLyricsStore(t)
		input, document := publicLyricsV2SekaiFixture(10)
		document.Full.Performers = append(document.Full.Performers, model.LyricsSourcePerformer{
			PerformerID: "歌唱者-22", Name: "鏡音リン", Color: "#FFCC11",
		})
		document.Full.Lines[0].Segments[0].PerformerIDs = []string{"歌唱者-21", "歌唱者-22"}
		input.Lines[0].Segments[0].PerformerIDs = []int{2, 1}
		saved := savePublicLyricsV2Fixture(t, s, input, document)
		_, err := s.PublishLyrics(saved.MusicID, saved.Revision)
		var contractErr *LyricsContractError
		if !errors.As(err, &contractErr) || contractErr.Code != "incomplete_publication" ||
			!strings.Contains(strings.Join(contractErr.Details, "; "), "does not exactly match authoritative source IDs") {
			t.Fatalf("performer order change error=%#v", err)
		}
	})
}

func TestPublicLyricsV2PreservesAuthoritativePerformerOrder(t *testing.T) {
	s := setupLyricsStore(t)
	input, document := publicLyricsV2SekaiFixture(10)
	document.Full.Performers = append(document.Full.Performers, model.LyricsSourcePerformer{
		PerformerID: "歌唱者-22", Name: "鏡音リン", Color: "#FFCC11",
	})
	document.Full.Lines[0].Segments[0].PerformerIDs = []string{"歌唱者-22", "歌唱者-21"}
	document.Full.Lines[0].TrailingPerformerIDs = []string{"歌唱者-22", "歌唱者-21"}
	input.Lines[0].Segments[0].PerformerIDs = []int{2, 1}

	saved := savePublicLyricsV2Fixture(t, s, input, document)
	if _, err := s.PublishLyrics(saved.MusicID, saved.Revision); err != nil {
		t.Fatal(err)
	}
	_, details, err := s.PublishedLyrics()
	if err != nil {
		t.Fatal(err)
	}
	if got := details[saved.MusicID].Lines[0].Segments[0].PerformerIDs; !reflect.DeepEqual(got, []int{2, 1}) {
		t.Fatalf("authoritative source performer order changed: got %v, want [2 1]", got)
	}
}

func TestPublicLyricsV2RejectsSourceLocalPerformerMetadataWithoutEcho(t *testing.T) {
	for _, test := range []struct {
		name          string
		sourceID      string
		performerName string
	}{
		{name: "romanized alias", sourceID: "provider_miku", performerName: "Hatsune Miku"},
		{name: "unknown performer", sourceID: "mikito-p", performerName: "Mikito-P"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := setupLyricsStore(t)
			input, document := publicLyricsV2SekaiFixture(10)
			document.Full.Performers[0].PerformerID = test.sourceID
			document.Full.Performers[0].Name = test.performerName
			for lineIndex := range document.Full.Lines {
				for segmentIndex := range document.Full.Lines[lineIndex].Segments {
					document.Full.Lines[lineIndex].Segments[segmentIndex].PerformerIDs = []string{test.sourceID}
				}
				document.Full.Lines[lineIndex].TrailingPerformerIDs = []string{test.sourceID}
			}
			saved := savePublicLyricsV2Fixture(t, s, input, document)
			_, err := s.PublishLyrics(saved.MusicID, saved.Revision)
			var contractErr *LyricsContractError
			if !errors.As(err, &contractErr) || contractErr.Code != "incomplete_publication" {
				t.Fatalf("unsafe public performer error=%#v", err)
			}
			lowerError := strings.ToLower(err.Error())
			for _, prohibited := range []string{strings.ToLower(test.sourceID), strings.ToLower(test.performerName)} {
				if strings.Contains(lowerError, prohibited) {
					t.Fatal("public error echoed unsafe performer metadata")
				}
			}
		})
	}
}

func TestPublicLyricsV2RejectsUnmappedProviderLocalPerformerLabel(t *testing.T) {
	s := setupLyricsStore(t)
	input, document := publicLyricsV2SekaiFixture(10)
	const sourceID = "external_singer"
	document.Full.Performers[0].PerformerID = sourceID
	document.Full.Performers[0].Name = "External Singer"
	for lineIndex := range document.Full.Lines {
		for segmentIndex := range document.Full.Lines[lineIndex].Segments {
			document.Full.Lines[lineIndex].Segments[segmentIndex].PerformerIDs = []string{sourceID}
		}
		document.Full.Lines[lineIndex].TrailingPerformerIDs = []string{sourceID}
	}
	saved := savePublicLyricsV2Fixture(t, s, input, document)
	_, err := s.PublishLyrics(saved.MusicID, saved.Revision)
	var contractErr *LyricsContractError
	if !errors.As(err, &contractErr) || contractErr.Code != "incomplete_publication" {
		t.Fatalf("unmapped provider-local performer error=%#v", err)
	}
	lowerError := strings.ToLower(err.Error())
	for _, prohibited := range []string{sourceID, "external singer"} {
		if strings.Contains(lowerError, prohibited) {
			t.Fatal("public error echoed unmapped performer metadata")
		}
	}
}

func TestPublicLyricsV2PublishesSekaipediaVirtualSingerSegmentationWithoutPrivateLeakage(t *testing.T) {
	s := setupLyricsStore(t)
	input, document := publicLyricsV2VirtualSingerFixture(10)
	saved := savePublicLyricsV2Fixture(t, s, input, document)
	if _, err := s.PublishLyrics(saved.MusicID, saved.Revision); err != nil {
		t.Fatal(err)
	}
	_, details, err := s.PublishedLyrics()
	if err != nil {
		t.Fatal(err)
	}
	detail := details[saved.MusicID]
	if len(detail.Attributions) != 2 || detail.Attributions[0].Provider != model.LyricsSourceProviderSekaipedia ||
		detail.Attributions[0].LicenseName != "CC BY-SA 4.0" ||
		detail.Attributions[0].LicenseURL != "https://creativecommons.org/licenses/by-sa/4.0/" ||
		detail.Attributions[1].Provider != model.LyricsSourceProviderVocaloidFandom ||
		detail.TranslationCredits == nil || detail.TranslationCredits.Translation != "Same Person" ||
		detail.TranslationCredits.Proofreading != "Same Person" ||
		!reflect.DeepEqual(detail.Lines[0].Segments[0].PerformerIDs, []int{1}) ||
		!reflect.DeepEqual(detail.Lines[0].Segments[1].PerformerIDs, []int{2}) {
		t.Fatalf("Sekaipedia VIRTUAL SINGER detail=%+v", detail)
	}
	body, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"privateReview", "revisionTimestamp", "compositionRenditionKey", "versionReason",
		"full_text", "performer_segmentation", "rawBytes", "romaji", "romanization",
	} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("VIRTUAL SINGER public detail leaked %q: %s", forbidden, body)
		}
	}
}

func TestPublicLyricsV2PublishesAuditedExternalPerformerWithReservedLyricsOnlyID(t *testing.T) {
	s := setupLyricsStore(t)
	input, document := publicLyricsV2VirtualSingerFixture(10)
	document.Full.Performers[0] = model.LyricsSourcePerformer{
		PerformerID: "外部歌唱者-01", Name: "GUMI", Color: "#70B85A",
	}
	document.Full.Lines[0].Segments[0].PerformerIDs = []string{"外部歌唱者-01"}
	document.Full.Lines[0].TrailingPerformerIDs = []string{"外部歌唱者-01", "歌唱者-22"}
	input.Lines[0].Segments[0].PerformerIDs = []int{1001}

	saved := savePublicLyricsV2Fixture(t, s, input, document)
	if _, err := s.PublishLyrics(saved.MusicID, saved.Revision); err != nil {
		t.Fatal(err)
	}
	_, details, err := s.PublishedLyrics()
	if err != nil {
		t.Fatal(err)
	}
	got := details[saved.MusicID].Lines[0].Segments
	if len(got) != 2 || !reflect.DeepEqual(got[0].PerformerIDs, []int{1001}) ||
		!reflect.DeepEqual(got[1].PerformerIDs, []int{2}) {
		t.Fatalf("audited external public performer projection=%+v", got)
	}
}

func TestPublicLyricsV2SourceBundleRejectsStaleScalarAndJSONOnlyIdentityFields(t *testing.T) {
	t.Run("stale scalar field", func(t *testing.T) {
		s := setupLyricsStore(t)
		input, document := publicLyricsV2VirtualSingerFixture(10)
		saved, changed, err := s.SaveImportedLyricsMutation(input, "fixture")
		if err != nil || !changed {
			t.Fatalf("save source-backed fixture changed=%t saved=%+v err=%v", changed, saved, err)
		}
		insertPublicLyricsV2SourceBundleWithArtifactMutation(t, s, input.MusicID, document,
			func(index int, stored, _ *model.LyricsSourceFixedIdentity) {
				if index == 0 {
					stored.Title = "stale title"
				}
			})
		_, err = s.PublishLyrics(saved.MusicID, saved.Revision)
		var contractErr *LyricsContractError
		if !errors.As(err, &contractErr) || contractErr.Code != "incomplete_publication" ||
			!strings.Contains(strings.Join(contractErr.Details, "; "), "does not match its fixed identity") {
			t.Fatalf("stale scalar identity error=%#v", err)
		}
	})

	for _, test := range []struct {
		name   string
		mutate func(*model.LyricsSourceFixedIdentity)
	}{
		{name: "revisionTimestamp", mutate: func(identity *model.LyricsSourceFixedIdentity) {
			identity.RevisionTimestamp = "2026-07-30T23:59:58Z"
		}},
		{name: "compositionRenditionKey", mutate: func(identity *model.LyricsSourceFixedIdentity) {
			identity.CompositionRenditionKey = "other-vocaloid"
		}},
		{name: "versionReason", mutate: func(identity *model.LyricsSourceFixedIdentity) {
			identity.VersionReason = model.LyricsSourceVersionReasonUntaggedGameSubset
		}},
	} {
		t.Run("stale JSON-only "+test.name, func(t *testing.T) {
			s := setupLyricsStore(t)
			input, document := publicLyricsV2VirtualSingerFixture(10)
			saved, changed, err := s.SaveImportedLyricsMutation(input, "fixture")
			if err != nil || !changed {
				t.Fatalf("save source-backed fixture changed=%t saved=%+v err=%v", changed, saved, err)
			}
			insertPublicLyricsV2SourceBundleWithArtifactMutation(t, s, input.MusicID, document,
				func(index int, stored, identityJSON *model.LyricsSourceFixedIdentity) {
					if index == 0 {
						test.mutate(stored)
						test.mutate(identityJSON)
					}
				})
			_, err = s.PublishLyrics(saved.MusicID, saved.Revision)
			var contractErr *LyricsContractError
			if !errors.As(err, &contractErr) || contractErr.Code != "incomplete_publication" ||
				!strings.Contains(strings.Join(contractErr.Details, "; "), "does not match its fixed identity") {
				t.Fatalf("stale %s identity error=%#v", test.name, err)
			}
		})
	}
}

func TestPublicLyricsV2FailsClosedOnStaleSourceAndMixedRollout(t *testing.T) {
	t.Run("stale source document", func(t *testing.T) {
		s := setupLyricsStore(t)
		input, document := publicLyricsV2SekaiFixture(10)
		saved, _, err := s.SaveImportedLyricsMutation(input, "fixture")
		if err != nil {
			t.Fatal(err)
		}
		document.Full.Lines[0].Text = "別歌"
		document.Full.Lines[0].Segments = []model.LyricsSourceSegment{{
			Text: "別歌", PerformerIDs: []string{"歌唱者-21"},
			Ruby: []model.LyricsSourceRubySpan{{Text: "別", Reading: "べつ"}, {Text: "歌", Reading: "うた"}},
		}}
		insertPublicLyricsV2SourceBundle(t, s, input.MusicID, document)
		_, err = s.PublishLyrics(saved.MusicID, saved.Revision)
		var contractErr *LyricsContractError
		if !errors.As(err, &contractErr) || contractErr.Code != "incomplete_publication" ||
			!strings.Contains(strings.Join(contractErr.Details, "; "), "authoritative Full") {
			t.Fatalf("stale source publication error=%#v", err)
		}
		var publications int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM song_lyrics_publications`).Scan(&publications); err != nil || publications != 0 {
			t.Fatalf("stale source wrote publications=%d err=%v", publications, err)
		}
	})

	t.Run("mixed v1 and v2 publications", func(t *testing.T) {
		s := setupLyricsStore(t)
		input, document := publicLyricsV2SekaiFixture(10)
		v2 := savePublicLyricsV2Fixture(t, s, input, document)
		if _, err := s.PublishLyrics(v2.MusicID, v2.Revision); err != nil {
			t.Fatal(err)
		}
		legacy := validLyrics()
		legacy.MusicID = 20
		legacy.Lines[0].ID = "legacy-source-line"
		v1, err := s.SaveLyrics(legacy, "fixture")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.PublishLyrics(v1.MusicID, v1.Revision); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.PublishedLyrics(); err == nil || !strings.Contains(err.Error(), "mixed v1/v2 rollout") {
			t.Fatalf("mixed rollout error=%v", err)
		}
	})
}

func TestPublicLyricsV2RejectsEncodedArtifactOverLimit(t *testing.T) {
	s := setupLyricsStore(t)
	input, document := publicLyricsV2VocaloidFixture(10, 64)
	quoted := strings.Repeat("\\", maxLyricsLineTextBytes)
	for lineIndex := range input.Lines {
		input.Lines[lineIndex].Chinese = quoted
		input.Lines[lineIndex].English = quoted
	}
	saved := savePublicLyricsV2Fixture(t, s, input, document)
	_, err := s.PublishLyrics(saved.MusicID, saved.Revision)
	var contractErr *LyricsContractError
	if !errors.As(err, &contractErr) || contractErr.Code != "incomplete_publication" ||
		!strings.Contains(strings.Join(contractErr.Details, "; "), "public artifact size limit") {
		t.Fatalf("oversized v2 publication error=%#v", err)
	}
	var publications int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM song_lyrics_publications`).Scan(&publications); err != nil || publications != 0 {
		t.Fatalf("oversized v2 wrote publications=%d err=%v", publications, err)
	}
}

func TestPublicLyricsV1PublicationBytesRemainReadableWithoutSourceDocument(t *testing.T) {
	s := setupLyricsStore(t)
	saved, err := s.SaveLyrics(validLyrics(), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishLyrics(saved.MusicID, saved.Revision); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := s.db.QueryRow(`SELECT payload_json FROM song_lyrics_publications WHERE music_id=?`, saved.MusicID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	expectedStored, err := json.Marshal(publicLyricsV1(saved))
	if err != nil {
		t.Fatal(err)
	}
	if stored != string(expectedStored) {
		t.Fatalf("v1 stored publication bytes changed\nwant=%s\ngot=%s", expectedStored, stored)
	}
	index, details, err := s.PublishedLyrics()
	if err != nil {
		t.Fatal(err)
	}
	if index.Version != 1 || details[saved.MusicID].Version != 1 ||
		details[saved.MusicID].Attribution != saved.Attribution || len(details[saved.MusicID].Attributions) != 0 {
		t.Fatalf("v1 publication readability index=%+v detail=%+v", index, details[saved.MusicID])
	}
	expectedPretty, _ := json.MarshalIndent(publicLyricsV1(saved), "", "  ")
	actualPretty, _ := json.MarshalIndent(details[saved.MusicID], "", "  ")
	if !bytes.Equal(actualPretty, expectedPretty) {
		t.Fatalf("v1 public read bytes changed\nwant=%s\ngot=%s", expectedPretty, actualPretty)
	}
}

func publicLyricsV2SekaiFixture(musicID int) (model.SongLyrics, model.LyricsSourceDocument) {
	full := publicLyricsV2TestIdentity(model.LyricsSourceProviderVocaloidFandom, "full-sekai", musicID*10+1, musicID*10+2, "Authoritative Full")
	game := publicLyricsV2TestIdentity(model.LyricsSourceProviderMoegirl, "game-sekai", musicID*10+3, musicID*10+4, "Game projection")
	unused := publicLyricsV2TestIdentity(model.LyricsSourceProviderVocaloidFandom, "unused-review", musicID*10+5, musicID*10+6, "Inspected but unused")
	fullRef := model.LyricsSourceComponentRef{RenditionKey: full.RenditionKey}
	gameRef := model.LyricsSourceComponentRef{RenditionKey: game.RenditionKey}
	lines := []model.LyricsSourceFullLine{
		{
			ID: "full-000001", Text: "初音歌う",
			Segments: []model.LyricsSourceSegment{
				{Text: "初音", PerformerIDs: []string{"歌唱者-21"}, Ruby: []model.LyricsSourceRubySpan{{Text: "初音", Reading: "はつね"}}},
				{Text: "歌う", PerformerIDs: []string{"歌唱者-21"}, Ruby: []model.LyricsSourceRubySpan{{Text: "歌", Reading: "うた"}, {Text: "う"}}},
			},
			TrailingPerformerIDs: []string{"歌唱者-21"},
		},
		{
			ID: "full-000002", Text: "未来へ", StanzaBreakBefore: true,
			Segments: []model.LyricsSourceSegment{{
				Text: "未来へ", PerformerIDs: []string{"歌唱者-21"},
				Ruby: []model.LyricsSourceRubySpan{{Text: "未来", Reading: "みらい"}, {Text: "へ"}},
			}},
			TrailingPerformerIDs: []string{"歌唱者-21"},
		},
	}
	document := model.LyricsSourceDocument{
		SchemaVersion: model.LyricsSourceDocumentSchemaVersion,
		ReasonCode:    model.LyricsSourceVersionReasonTaggedFullAndGame,
		FixedIdentities: []model.LyricsSourceFixedIdentity{
			full, game, unused,
		},
		Provenance: model.LyricsSourceComponentProvenance{
			FullText: fullRef, PerformerSegmentation: &fullRef, GameProjection: &gameRef,
			Ruby: &fullRef, VersionEvidence: fullRef,
		},
		Full: model.LyricsSourceFull{
			Version:              model.LyricsSourceVersion{Kind: "sekai", Label: "Project SEKAI Version"},
			Performers:           []model.LyricsSourcePerformer{{PerformerID: "歌唱者-21", Name: "初音ミク", Color: "#33CCBB"}},
			RubyGeneratorVersion: "kagome-ipadic-v1", Lines: lines,
		},
		GameProjection: &model.LyricsSourceGameProjection{LineIDs: []string{"full-000001"}},
	}
	input := model.SongLyrics{
		MusicID: musicID, Attribution: "Legacy Translator", SourceNote: "private source note",
		LicenseNote: "private license note", SourceURL: full.CanonicalURL, SourcePageID: full.PageID,
		SourceRevisionID: full.RevisionID, SourceSHA1: full.SHA1, SourceFetchedAt: full.FetchedAt,
		Lines: []model.LyricLine{
			{
				ID: lines[0].ID, Order: 0, Japanese: lines[0].Text, Chinese: "初音在歌唱", English: "Miku sings",
				Segments: []model.LyricSegment{
					{Text: "初音", PerformerIDs: []int{1}, Ruby: []model.LyricRubySpan{{Text: "初音", Reading: "はつね"}}},
					{Text: "歌う", PerformerIDs: []int{1}, Ruby: []model.LyricRubySpan{{Text: "歌", Reading: "うた"}, {Text: "う"}}},
				},
			},
			{
				ID: lines[1].ID, Order: 1, Japanese: lines[1].Text, Chinese: "", English: "",
				StanzaBreakBefore: true,
				Segments: []model.LyricSegment{{
					Text: "未来へ", PerformerIDs: []int{1}, Ruby: []model.LyricRubySpan{{Text: "未来", Reading: "みらい"}, {Text: "へ"}},
				}},
			},
		},
	}
	return input, document
}

func publicLyricsV2VirtualSingerFixture(musicID int) (model.SongLyrics, model.LyricsSourceDocument) {
	full := publicLyricsV2TestIdentity(model.LyricsSourceProviderSekaipedia, "full-vs-sekaipedia", musicID*100+1, musicID*100+2, "VIRTUAL SINGER Full")
	full.CompositionRenditionKey = "full-vocaloid"
	full.VersionReason = model.LyricsSourceVersionReasonUntaggedFullOnly
	segments := publicLyricsV2TestIdentity(model.LyricsSourceProviderVocaloidFandom, "segments-vs-fandom", musicID*100+3, musicID*100+4, "VIRTUAL SINGER segmentation")
	segments.CompositionRenditionKey = "full-vocaloid"
	segments.VersionReason = model.LyricsSourceVersionReasonUntaggedFullOnly
	fullRef := model.LyricsSourceComponentRef{RenditionKey: full.RenditionKey}
	segmentsRef := model.LyricsSourceComponentRef{RenditionKey: segments.RenditionKey}
	document := model.LyricsSourceDocument{
		SchemaVersion:   model.LyricsSourceDocumentSchemaVersion,
		ReasonCode:      model.LyricsSourceVersionReasonUntaggedFullOnly,
		FixedIdentities: []model.LyricsSourceFixedIdentity{full, segments},
		Provenance: model.LyricsSourceComponentProvenance{
			FullText: fullRef, PerformerSegmentation: &segmentsRef, Ruby: &fullRef, VersionEvidence: fullRef,
		},
		Full: model.LyricsSourceFull{
			Version:              model.LyricsSourceVersion{Kind: "vocaloid", Label: "VIRTUAL SINGER Version"},
			RubyGeneratorVersion: "kagome-ipadic-v1",
			Performers: []model.LyricsSourcePerformer{
				{PerformerID: "歌唱者-21", Name: "初音ミク", Color: "#33CCBB"},
				{PerformerID: "歌唱者-22", Name: "鏡音リン", Color: "#FFCC11"},
			},
			Lines: []model.LyricsSourceFullLine{{
				ID: "full-000001", Text: "初音鏡音",
				Segments: []model.LyricsSourceSegment{
					{Text: "初音", PerformerIDs: []string{"歌唱者-21"}, Ruby: []model.LyricsSourceRubySpan{{Text: "初音", Reading: "はつね"}}},
					{Text: "鏡音", PerformerIDs: []string{"歌唱者-22"}, Ruby: []model.LyricsSourceRubySpan{{Text: "鏡音", Reading: "かがみね"}}},
				},
				TrailingPerformerIDs: []string{"歌唱者-21", "歌唱者-22"},
			}},
		},
		PrivateReview: &model.LyricsSourcePrivateReview{
			PerformerSegmentationEvidence: model.LyricsSourcePerformerSegmentationEvidenceAuthoritativeCompleteStructured,
		},
	}
	input := model.SongLyrics{
		MusicID: musicID, Attribution: "private VIRTUAL SINGER review",
		TranslationCredit: "Same Person", ProofreadingCredit: "Same Person", SourceNote: "private source note",
		LicenseNote: "private license note", SourceURL: full.CanonicalURL, SourcePageID: full.PageID,
		SourceRevisionID: full.RevisionID, SourceSHA1: full.SHA1, SourceFetchedAt: full.FetchedAt,
		Lines: []model.LyricLine{{
			ID: "full-000001", Order: 0, Japanese: "初音鏡音", Chinese: "初音镜音", English: "Miku and Rin",
			Segments: []model.LyricSegment{
				{Text: "初音", PerformerIDs: []int{1}, Ruby: []model.LyricRubySpan{{Text: "初音", Reading: "はつね"}}},
				{Text: "鏡音", PerformerIDs: []int{2}, Ruby: []model.LyricRubySpan{{Text: "鏡音", Reading: "かがみね"}}},
			},
		}},
	}
	return input, document
}

func publicLyricsV2VocaloidFixture(musicID, lineCount int) (model.SongLyrics, model.LyricsSourceDocument) {
	full := publicLyricsV2TestIdentity(model.LyricsSourceProviderVocaloidFandom, "full-vocaloid", musicID*100+1, musicID*100+2, "Vocaloid Full")
	fullRef := model.LyricsSourceComponentRef{RenditionKey: full.RenditionKey}
	input := model.SongLyrics{
		MusicID: musicID, TranslationCredit: "Vocaloid Translator",
		SourceURL: full.CanonicalURL, SourcePageID: full.PageID,
		SourceRevisionID: full.RevisionID, SourceSHA1: full.SHA1, SourceFetchedAt: full.FetchedAt,
		Lines: make([]model.LyricLine, lineCount),
	}
	fullLines := make([]model.LyricsSourceFullLine, lineCount)
	for lineIndex := 0; lineIndex < lineCount; lineIndex++ {
		lineID := fmt.Sprintf("full-%06d", lineIndex+1)
		text := "歌"
		input.Lines[lineIndex] = model.LyricLine{
			ID: lineID, Order: lineIndex, Japanese: text, Chinese: "", English: "",
			Segments: []model.LyricSegment{{Text: text, PerformerIDs: []int{}, Ruby: []model.LyricRubySpan{{Text: text}}}},
		}
		fullLines[lineIndex] = model.LyricsSourceFullLine{
			ID: lineID, Text: text,
			Segments:             []model.LyricsSourceSegment{{Text: text, PerformerIDs: []string{}, Ruby: []model.LyricsSourceRubySpan{{Text: text}}}},
			TrailingPerformerIDs: []string{},
		}
	}
	document := model.LyricsSourceDocument{
		SchemaVersion: model.LyricsSourceDocumentSchemaVersion,
		ReasonCode:    model.LyricsSourceVersionReasonUntaggedFullOnly,
		FixedIdentities: []model.LyricsSourceFixedIdentity{
			full,
		},
		Provenance: model.LyricsSourceComponentProvenance{FullText: fullRef, VersionEvidence: fullRef},
		Full: model.LyricsSourceFull{
			Version:    model.LyricsSourceVersion{Kind: "vocaloid", Label: "Vocaloid Version"},
			Performers: []model.LyricsSourcePerformer{}, Lines: fullLines,
		},
	}
	return input, document
}

func publicLyricsV2TestIdentity(provider model.LyricsSourceProvider, renditionKey string, pageID, revisionID int, title string) model.LyricsSourceFixedIdentity {
	origin := model.LyricsSourceOriginVocaloidFandom
	canonicalURL := fmt.Sprintf("https://vocaloid.fandom.com/wiki/Public_Test_%d?oldid=%d", pageID, revisionID)
	revisionTimestamp := ""
	switch provider {
	case model.LyricsSourceProviderMoegirl:
		origin = model.LyricsSourceOriginMoegirl
		canonicalURL = fmt.Sprintf("https://moegirl.icu/index.php?oldid=%d&title=Public_Test_%d", revisionID, pageID)
	case model.LyricsSourceProviderSekaipedia:
		origin = model.LyricsSourceOriginSekaipedia
		canonicalURL = fmt.Sprintf("https://www.sekaipedia.org/wiki/Public_Test_%d?oldid=%d", pageID, revisionID)
		revisionTimestamp = "2026-07-30T23:59:59Z"
	}
	return model.LyricsSourceFixedIdentity{
		Provider: provider, Origin: origin, PageID: pageID, RevisionID: revisionID,
		SHA1: strings.Repeat(fmt.Sprintf("%x", pageID%15+1), 40), Title: title, CanonicalURL: canonicalURL,
		RevisionTimestamp: revisionTimestamp, FetchedAt: "2026-07-31T00:00:00Z",
		Categories: []string{"Lyrics"}, Section: "Lyrics",
		RenditionKey: renditionKey,
		IndexEvidenceRefs: []model.LyricsSourceIndexEvidenceRef{{
			EvidenceID: fmt.Sprintf("public-test:%s:%d", provider, pageID), SHA256: strings.Repeat("a", 64),
		}},
	}
}

func savePublicLyricsV2Fixture(t *testing.T, s *Store, input model.SongLyrics, document model.LyricsSourceDocument) model.SongLyrics {
	t.Helper()
	saved, changed, err := s.SaveImportedLyricsMutation(input, "fixture")
	if err != nil || !changed {
		t.Fatalf("save source-backed fixture changed=%t saved=%+v err=%v", changed, saved, err)
	}
	insertPublicLyricsV2SourceBundle(t, s, input.MusicID, document)
	return saved
}

type publicLyricsV2ArtifactMutation func(int, *model.LyricsSourceFixedIdentity, *model.LyricsSourceFixedIdentity)

func insertPublicLyricsV2SourceBundle(t *testing.T, s *Store, musicID int, document model.LyricsSourceDocument) {
	t.Helper()
	insertPublicLyricsV2SourceBundleWithArtifactMutation(t, s, musicID, document, nil)
}

func insertPublicLyricsV2SourceBundleWithArtifactMutation(t *testing.T, s *Store, musicID int,
	document model.LyricsSourceDocument, mutate publicLyricsV2ArtifactMutation,
) {
	t.Helper()
	if err := model.ValidateLyricsSourceDocument(document); err != nil {
		t.Fatalf("invalid source fixture: %v", err)
	}
	documentJSON, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	documentDigest := sha256.Sum256(documentJSON)
	documentSHA := hex.EncodeToString(documentDigest[:])
	result, err := s.db.Exec(`INSERT INTO song_lyrics_source_documents
		(music_id,schema_version,reason_code,document_json,document_sha256,manifest_batch_sha256,created_at)
		VALUES (?,?,?,?,?,?,?)`, musicID, document.SchemaVersion, document.ReasonCode, string(documentJSON),
		documentSHA, strings.Repeat("b", 64), 1)
	if err != nil {
		t.Fatal(err)
	}
	documentID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		if _, err := s.db.Exec(`DROP TRIGGER song_lyrics_source_artifacts_identity_validate_insert`); err != nil {
			t.Fatal(err)
		}
	}
	for identityIndex, identity := range document.FixedIdentities {
		storedIdentity := identity
		JSONIdentity := identity
		if mutate != nil {
			mutate(identityIndex, &storedIdentity, &JSONIdentity)
		}
		identityJSON, err := json.Marshal(JSONIdentity)
		if err != nil {
			t.Fatal(err)
		}
		identityDigest := sha256.Sum256(identityJSON)
		categoriesJSON, _ := json.Marshal(storedIdentity.Categories)
		evidenceJSON, _ := json.Marshal(storedIdentity.IndexEvidenceRefs)
		artifactCharacter := fmt.Sprintf("%x", identityIndex%15+1)
		if _, err := s.db.Exec(`INSERT INTO song_lyrics_source_artifacts
			(document_id,provider,rendition_key,origin,page_id,revision_id,revision_timestamp,mediawiki_sha1,page_title,
			 canonical_revision_url,fetched_at,categories_json,section,composition_rendition_key,version_reason,
			 index_evidence_refs_json,fixed_identity_json,fixed_identity_sha256,raw_byte_count,raw_wikitext_sha256,artifact_sha256)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, documentID, storedIdentity.Provider, storedIdentity.RenditionKey,
			storedIdentity.Origin, storedIdentity.PageID, storedIdentity.RevisionID, storedIdentity.RevisionTimestamp,
			storedIdentity.SHA1, storedIdentity.Title, storedIdentity.CanonicalURL, storedIdentity.FetchedAt,
			string(categoriesJSON), storedIdentity.Section, storedIdentity.CompositionRenditionKey, storedIdentity.VersionReason,
			string(evidenceJSON), string(identityJSON), hex.EncodeToString(identityDigest[:]),
			1, strings.Repeat("c", 64), strings.Repeat(artifactCharacter, 64)); err != nil {
			t.Fatal(err)
		}
	}
	for component, renditionKey := range publicLyricsSourceComponentRefs(document) {
		contributionDigest := sha256.Sum256([]byte(documentSHA + "\x00" + component + "\x00" + renditionKey))
		if _, err := s.db.Exec(`INSERT INTO song_lyrics_component_contributions
			(document_id,component,rendition_key,contribution_sha256) VALUES (?,?,?,?)`, documentID, component,
			renditionKey, hex.EncodeToString(contributionDigest[:])); err != nil {
			t.Fatal(err)
		}
	}
}
