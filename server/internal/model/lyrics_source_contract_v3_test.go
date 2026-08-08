package model

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func validLyricsSourceDocumentV3(t *testing.T) LyricsSourceDocument {
	t.Helper()
	sekaiV2 := validLyricsSourceDocument()
	sekaiGame := *CloneLyricsSourceFull(&sekaiV2.Full)
	sekaiGame.Lines = []LyricsSourceFullLine{sekaiGame.Lines[0], sekaiGame.Lines[2]}
	for index := range sekaiGame.Lines {
		sekaiGame.Lines[index].ID = fmt.Sprintf("game-%06d", index+1)
	}
	gameRef := LyricsSourceComponentRef{RenditionKey: "game-sekai"}
	sekaiV2.Game = &sekaiGame
	sekaiV2.Provenance.GameText = &gameRef
	sekai, err := UpconvertLyricsSourceDocumentV2(sekaiV2)
	if err != nil {
		t.Fatal(err)
	}
	sekai.Renditions[0].SourceTabPaths = []LyricsSourceTabPath{
		{"Full Version"}, {"Game Version", "SEKAI"},
	}

	vocaloidV2 := validAuthoritativeVirtualSingerLyricsSourceDocument()
	vocaloidGame := *CloneLyricsSourceFull(&vocaloidV2.Full)
	for index := range vocaloidGame.Lines {
		vocaloidGame.Lines[index].ID = fmt.Sprintf("game-%06d", index+1)
	}
	vocaloidRef := vocaloidV2.Provenance.FullText
	vocaloidV2.ReasonCode = LyricsSourceVersionReasonTaggedGameOnly
	vocaloidV2.Full = LyricsSourceFull{}
	vocaloidV2.Game = &vocaloidGame
	vocaloidV2.GameProjection = nil
	vocaloidV2.Provenance.FullText = LyricsSourceComponentRef{}
	vocaloidV2.Provenance.GameText = &vocaloidRef
	vocaloidV2.Provenance.GameProjection = nil
	vocaloid, err := UpconvertLyricsSourceDocumentV2(vocaloidV2)
	if err != nil {
		t.Fatal(err)
	}
	vocaloid.Renditions[0].SourceTabPaths = []LyricsSourceTabPath{
		{"Game Version", "VIRTUAL SINGER"},
	}

	document := LyricsSourceDocument{
		SchemaVersion: LyricsSourceDocumentSchemaVersionV3,
		FixedIdentities: append(
			append([]LyricsSourceFixedIdentity{}, sekai.FixedIdentities...),
			vocaloid.FixedIdentities...,
		),
		Renditions: append(CloneLyricsSourceRenditions(sekai.Renditions), vocaloid.Renditions...),
	}
	if err := ValidateLyricsSourceDocument(document); err != nil {
		t.Fatalf("valid source v3 fixture: %v", err)
	}
	return document
}

func TestLyricsSourceDocumentV3RoundTripCanonicalHashAndComponentEnumeration(t *testing.T) {
	document := validLyricsSourceDocumentV3(t)
	body, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte(`"alternateVocals"`)) || bytes.Contains(body, []byte(`"full":null`)) ||
		!bytes.HasPrefix(body, []byte(`{"schemaVersion":3,"fixedIdentities":`)) ||
		!bytes.Contains(body, []byte(`"renditions":[`)) {
		t.Fatalf("source v3 canonical wire shape drifted: %s", body)
	}
	decoded, err := DecodeLyricsSourceDocument(body)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip, err := json.Marshal(decoded)
	if err != nil || !bytes.Equal(body, roundTrip) {
		t.Fatalf("source v3 canonical round trip drifted: err=%v", err)
	}
	hostile := map[string][]byte{
		"duplicate":         bytes.Replace(body, []byte(`"schemaVersion":3`), []byte(`"schemaVersion":3,"schemaVersion":3`), 1),
		"unknown":           bytes.Replace(body, []byte(`{"schemaVersion"`), []byte(`{"unknown":1,"schemaVersion"`), 1),
		"legacy zero field": bytes.Replace(body, []byte(`"fixedIdentities"`), []byte(`"reasonCode":"","fixedIdentities"`), 1),
		"trailing":          append(append([]byte{}, body...), []byte("\n{}")...),
	}
	for name, candidate := range hostile {
		t.Run("decode "+name, func(t *testing.T) {
			if _, err := DecodeLyricsSourceDocument(candidate); err == nil {
				t.Fatal("hostile source v3 document was accepted")
			}
		})
	}
	bindings, err := EnumerateLyricsSourceRenditionComponents(decoded.Renditions)
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 13 {
		t.Fatalf("canonical component count=%d bindings=%+v", len(bindings), bindings)
	}
	for index := 1; index < len(bindings); index++ {
		left, right := bindings[index-1], bindings[index]
		if left.RenditionKey > right.RenditionKey ||
			left.RenditionKey == right.RenditionKey &&
				LyricsSourceRenditionComponentRank(left.Component) >= LyricsSourceRenditionComponentRank(right.Component) {
			t.Fatalf("component enumeration is not canonical: %+v", bindings)
		}
	}
	digest := sha256.Sum256(body)
	const expectedSHA256 = "f560b7046ea3c444105d11867f686072672c219095d998a97e41c649c7974135"
	if got := hex.EncodeToString(digest[:]); got != expectedSHA256 {
		t.Fatalf("source v3 canonical SHA-256=%s", got)
	}
}

func TestLyricsSourceDocumentV3RejectsInvalidPeerRenditionShapes(t *testing.T) {
	base := validLyricsSourceDocumentV3(t)
	tests := map[string]func(*LyricsSourceDocument){
		"invalid rendition key": func(document *LyricsSourceDocument) {
			document.Renditions[0].RenditionKey = "SEKAI"
		},
		"duplicate rendition key": func(document *LyricsSourceDocument) {
			document.Renditions[1].RenditionKey = document.Renditions[0].RenditionKey
		},
		"duplicate source tab path": func(document *LyricsSourceDocument) {
			document.Renditions[1].SourceTabPaths[0] = append(LyricsSourceTabPath{}, document.Renditions[0].SourceTabPaths[0]...)
		},
		"duplicate source tab path within rendition": func(document *LyricsSourceDocument) {
			document.Renditions[0].SourceTabPaths = append(document.Renditions[0].SourceTabPaths,
				append(LyricsSourceTabPath{}, document.Renditions[0].SourceTabPaths[0]...))
		},
		"missing source tab paths": func(document *LyricsSourceDocument) {
			document.Renditions[0].SourceTabPaths = nil
		},
		"empty source tab label": func(document *LyricsSourceDocument) {
			document.Renditions[0].SourceTabPaths[0][0] = ""
		},
		"unknown source kind": func(document *LyricsSourceDocument) {
			document.Renditions[0].SourceKind = "other"
		},
		"unknown version reason": func(document *LyricsSourceDocument) {
			document.Renditions[0].ReasonCode = "other"
		},
		"no Full or Game": func(document *LyricsSourceDocument) {
			document.Renditions[1].Game = nil
		},
		"Full source kind drift": func(document *LyricsSourceDocument) {
			document.Renditions[0].Full.Version.Kind = "original"
		},
		"Game source kind drift": func(document *LyricsSourceDocument) {
			document.Renditions[1].Game.Version.Kind = "original"
		},
		"cross rendition projection": func(document *LyricsSourceDocument) {
			document.Renditions[0].Relation.FullRenditionKey = document.Renditions[1].RenditionKey
		},
		"unknown relation": func(document *LyricsSourceDocument) {
			document.Renditions[0].Relation.Kind = "approximate"
		},
		"none relation carries projection": func(document *LyricsSourceDocument) {
			document.Renditions[1].Relation.LineIDs = []string{"full-000001"}
		},
		"none relation carries target": func(document *LyricsSourceDocument) {
			document.Renditions[1].Relation.FullRenditionKey = "vocaloid"
		},
		"exact projection without Full": func(document *LyricsSourceDocument) {
			document.Renditions[0].Full = nil
		},
		"duplicate exact projection line": func(document *LyricsSourceDocument) {
			document.Renditions[0].Relation.LineIDs[1] = document.Renditions[0].Relation.LineIDs[0]
		},
		"non exact projection": func(document *LyricsSourceDocument) {
			game := document.Renditions[0].Game
			gameID := game.Lines[0].ID
			game.Lines[0] = document.Renditions[0].Full.Lines[1]
			game.Lines[0].ID = gameID
		},
		"missing Full text ref": func(document *LyricsSourceDocument) {
			document.Renditions[0].Provenance.FullText = nil
		},
		"missing Full ruby ref": func(document *LyricsSourceDocument) {
			document.Renditions[0].Provenance.FullRuby = nil
		},
		"missing Game text ref": func(document *LyricsSourceDocument) {
			document.Renditions[1].Provenance.GameText = nil
		},
		"missing Game segmentation ref": func(document *LyricsSourceDocument) {
			document.Renditions[1].Provenance.GamePerformerSegmentation = nil
		},
		"missing relation ref": func(document *LyricsSourceDocument) {
			document.Renditions[0].Provenance.RelationEvidence = LyricsSourceComponentRef{}
		},
		"missing version ref": func(document *LyricsSourceDocument) {
			document.Renditions[0].Provenance.VersionEvidence = LyricsSourceComponentRef{}
		},
		"component ref without data": func(document *LyricsSourceDocument) {
			ref := document.Renditions[1].Provenance.GameText
			document.Renditions[1].Provenance.FullText = ref
		},
		"unknown component identity": func(document *LyricsSourceDocument) {
			document.Renditions[0].Provenance.VersionEvidence.RenditionKey = "missing-artifact"
		},
		"non contributing fixed identity": func(document *LyricsSourceDocument) {
			identity := validProviderFixedIdentity(LyricsSourceProviderMoegirl, "unused-v3-artifact")
			document.FixedIdentities = append(document.FixedIdentities, identity)
		},
		"duplicate fixed identity": func(document *LyricsSourceDocument) {
			document.FixedIdentities = append(document.FixedIdentities, document.FixedIdentities[0])
		},
		"performer registry drift": func(document *LyricsSourceDocument) {
			document.Renditions[0].Game.Performers[0].Color = "#112233"
		},
		"alternate without explicit label": func(document *LyricsSourceDocument) {
			rendering := &document.Renditions[1]
			rendering.SourceKind = LyricsSourceRenditionAlternate
			rendering.Game.Version.Kind = string(LyricsSourceRenditionAlternate)
		},
		"alternate label on ordinary rendition": func(document *LyricsSourceDocument) {
			document.Renditions[0].SourceTabPaths = append(document.Renditions[0].SourceTabPaths,
				LyricsSourceTabPath{"Another Vocal"})
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			document := LyricsSourceDocument{
				SchemaVersion:   LyricsSourceDocumentSchemaVersionV3,
				FixedIdentities: append([]LyricsSourceFixedIdentity{}, base.FixedIdentities...),
				Renditions:      CloneLyricsSourceRenditions(base.Renditions),
			}
			mutate(&document)
			if err := ValidateLyricsSourceDocument(document); err == nil {
				t.Fatal("invalid source v3 document was accepted")
			}
		})
	}
}

func TestLyricsSourceDocumentV3RejectsUnboundedRenditionAndTabPathShapes(t *testing.T) {
	base := validLyricsSourceDocumentV3(t).Renditions[0]
	tooManyRenditions := make([]LyricsSourceRendition, maxLyricsSourceRenditions+1)
	for index := range tooManyRenditions {
		tooManyRenditions[index] = CloneLyricsSourceRenditions([]LyricsSourceRendition{base})[0]
	}
	if err := ValidateLyricsSourceRenditionSetPayload(tooManyRenditions); err == nil {
		t.Fatal("unbounded rendition set was accepted")
	}

	tooManyPaths := CloneLyricsSourceRenditions([]LyricsSourceRendition{base})[0]
	tooManyPaths.SourceTabPaths = make([]LyricsSourceTabPath, maxLyricsSourceTabPaths+1)
	for index := range tooManyPaths.SourceTabPaths {
		tooManyPaths.SourceTabPaths[index] = LyricsSourceTabPath{fmt.Sprintf("Path %d", index+1)}
	}
	if err := ValidateLyricsSourceRenditionPayload(tooManyPaths); err == nil {
		t.Fatal("unbounded source tab path set was accepted")
	}

	tooDeep := CloneLyricsSourceRenditions([]LyricsSourceRendition{base})[0]
	tooDeep.SourceTabPaths = []LyricsSourceTabPath{make(LyricsSourceTabPath, maxLyricsSourceTabPathDepth+1)}
	for index := range tooDeep.SourceTabPaths[0] {
		tooDeep.SourceTabPaths[0][index] = fmt.Sprintf("Depth %d", index+1)
	}
	if err := ValidateLyricsSourceRenditionPayload(tooDeep); err == nil {
		t.Fatal("unbounded source tab depth was accepted")
	}

	tooLong := CloneLyricsSourceRenditions([]LyricsSourceRendition{base})[0]
	tooLong.SourceTabPaths = []LyricsSourceTabPath{{strings.Repeat("x", maxLyricsSourceTabPathLabelBytes+1)}}
	if err := ValidateLyricsSourceRenditionPayload(tooLong); err == nil {
		t.Fatal("unbounded source tab label was accepted")
	}
}

func rebindLyricsSourceV3ReadingEvidenceRendition(full *LyricsSourceFull, renditionKey string) {
	if full == nil {
		return
	}
	for lineIndex := range full.Lines {
		for segmentIndex := range full.Lines[lineIndex].Segments {
			for spanIndex := range full.Lines[lineIndex].Segments[segmentIndex].Ruby {
				evidence := full.Lines[lineIndex].Segments[segmentIndex].Ruby[spanIndex].ReadingEvidence
				if evidence != nil && evidence.RenditionKey != "" {
					evidence.RenditionKey = renditionKey
				}
			}
		}
	}
}

func clearLyricsSourceV3ReadingEvidence(full *LyricsSourceFull) {
	if full == nil {
		return
	}
	for lineIndex := range full.Lines {
		for segmentIndex := range full.Lines[lineIndex].Segments {
			for spanIndex := range full.Lines[lineIndex].Segments[segmentIndex].Ruby {
				full.Lines[lineIndex].Segments[segmentIndex].Ruby[spanIndex].ReadingEvidence = nil
			}
		}
	}
}

func TestLyricsSourceDocumentV3AllowsExplicitAlternateArchiveAndAprilFoolsSemantics(t *testing.T) {
	base := validLyricsSourceDocumentV3(t).Renditions[1]
	for _, label := range []string{
		"Alternate Vocal", "Alternate Vocals", "Another Vocal Version", "Another Vocals Version",
		"Archive Version", "April Fools' Version", "Alt. Group Covers", "Alt. Group Covers (Full)",
		"COLORFUL LIVE", "Full Version (Movie)", "Game Size",
	} {
		t.Run(label, func(t *testing.T) {
			rendering := CloneLyricsSourceRenditions([]LyricsSourceRendition{base})[0]
			rendering.RenditionKey = "alternate"
			rebindLyricsSourceV3ReadingEvidenceRendition(rendering.Full, rendering.RenditionKey)
			rebindLyricsSourceV3ReadingEvidenceRendition(rendering.Game, rendering.RenditionKey)
			rendering.SourceKind = LyricsSourceRenditionAlternate
			rendering.SourceTabPaths = []LyricsSourceTabPath{{"Game Version", label}}
			rendering.Game.Version.Kind = string(LyricsSourceRenditionAlternate)
			if err := ValidateLyricsSourceRenditionPayload(rendering); err != nil {
				t.Fatalf("explicit alternate label rejected: %v", err)
			}
		})
	}
}

func TestLyricsSourceDocumentV3ReadingEvidenceIsSourceOrGeneratorBounded(t *testing.T) {
	base := validLyricsSourceDocumentV3(t)

	t.Run("source evidence requires an exact locator", func(t *testing.T) {
		document := LyricsSourceDocument{
			SchemaVersion:   LyricsSourceDocumentSchemaVersionV3,
			FixedIdentities: append([]LyricsSourceFixedIdentity{}, base.FixedIdentities...),
			Renditions:      CloneLyricsSourceRenditions(base.Renditions),
		}
		evidence := document.Renditions[0].Full.Lines[0].Segments[0].Ruby[0].ReadingEvidence
		evidence.SourceRowOrdinal = 0
		if err := ValidateLyricsSourceDocument(document); err == nil {
			t.Fatal("source reading without a row locator was accepted")
		}
	})

	t.Run("generated evidence cannot carry source attribution", func(t *testing.T) {
		document := LyricsSourceDocument{
			SchemaVersion:   LyricsSourceDocumentSchemaVersionV3,
			FixedIdentities: append([]LyricsSourceFixedIdentity{}, base.FixedIdentities...),
			Renditions:      CloneLyricsSourceRenditions(base.Renditions),
		}
		evidence := document.Renditions[0].Full.Lines[0].Segments[0].Ruby[0].ReadingEvidence
		evidence.Kind = LyricsSourceReadingEvidenceDeterministicDictionary
		evidence.GeneratorVersion = "dictionary-v1"
		if err := ValidateLyricsSourceDocument(document); err == nil {
			t.Fatal("generated reading with source locator was accepted")
		}
	})

	t.Run("generated-only ruby has no coarse source reference", func(t *testing.T) {
		document := LyricsSourceDocument{
			SchemaVersion:   LyricsSourceDocumentSchemaVersionV3,
			FixedIdentities: append([]LyricsSourceFixedIdentity{}, base.FixedIdentities...),
			Renditions:      CloneLyricsSourceRenditions(base.Renditions),
		}
		for renditionIndex := range document.Renditions {
			rendition := &document.Renditions[renditionIndex]
			for _, full := range []*LyricsSourceFull{rendition.Full, rendition.Game} {
				if full == nil {
					continue
				}
				for lineIndex := range full.Lines {
					for segmentIndex := range full.Lines[lineIndex].Segments {
						for spanIndex := range full.Lines[lineIndex].Segments[segmentIndex].Ruby {
							span := &full.Lines[lineIndex].Segments[segmentIndex].Ruby[spanIndex]
							if span.Reading == "" {
								continue
							}
							span.ReadingEvidence = &LyricsSourceReadingEvidence{
								Kind:             LyricsSourceReadingEvidenceDeterministicDictionary,
								GeneratorVersion: "dictionary-v1",
							}
						}
					}
				}
			}
			rendition.Provenance.FullRuby = nil
			rendition.Provenance.GameRuby = nil
		}
		if err := ValidateLyricsSourceDocument(document); err != nil {
			t.Fatalf("generated-only ruby was rejected: %v", err)
		}
		ref := document.Renditions[0].Provenance.FullText
		document.Renditions[0].Provenance.FullRuby = ref
		if err := ValidateLyricsSourceDocument(document); err == nil {
			t.Fatal("generated-only ruby with a coarse source reference was accepted")
		}
	})
}

func TestLyricsSourceDocumentV3PerformerEvidenceIsSideBoundedAndExplicit(t *testing.T) {
	base := validLyricsSourceDocumentV3(t)
	partialExactAssignment := LyricsSourceDocument{
		SchemaVersion:   LyricsSourceDocumentSchemaVersionV3,
		FixedIdentities: append([]LyricsSourceFixedIdentity{}, base.FixedIdentities...),
		Renditions:      CloneLyricsSourceRenditions(base.Renditions),
	}
	partialExactAssignment.Renditions[1].GamePerformerEvidence = LyricsSourcePerformerEvidenceSourcePartial
	partialExactAssignment.Renditions[1].PrivateReview = nil
	if err := ValidateLyricsSourceDocument(partialExactAssignment); err != nil {
		t.Fatalf("source-partial exact retained assignment was rejected: %v", err)
	}

	for name, mutate := range map[string]func(*LyricsSourceDocument){
		"complete side cannot retain an anonymous segment": func(document *LyricsSourceDocument) {
			document.Renditions[1].Game.Lines[0].Segments[0].PerformerIDs = nil
		},
		"structured side cannot claim none": func(document *LyricsSourceDocument) {
			document.Renditions[1].GamePerformerEvidence = LyricsSourcePerformerEvidenceNone
		},
		"source roster cannot omit a retained performer": func(document *LyricsSourceDocument) {
			document.Renditions[1].SourcePerformerIDs = document.Renditions[1].SourcePerformerIDs[:1]
		},
	} {
		t.Run(name, func(t *testing.T) {
			document := LyricsSourceDocument{
				SchemaVersion:   LyricsSourceDocumentSchemaVersionV3,
				FixedIdentities: append([]LyricsSourceFixedIdentity{}, base.FixedIdentities...),
				Renditions:      CloneLyricsSourceRenditions(base.Renditions),
			}
			mutate(&document)
			if err := ValidateLyricsSourceDocument(document); err == nil {
				t.Fatal("invalid performer evidence was accepted")
			}
		})
	}
}

func TestLyricsSourceDocumentV3ToV2CompatibilityFailsClosed(t *testing.T) {
	representable, err := UpconvertLyricsSourceDocumentV2(validVocaloidOnlyLyricsSourceDocument())
	if err != nil {
		t.Fatal(err)
	}
	if err := LyricsSourceDocumentV3ToV2Compatibility(representable); err != nil {
		t.Fatalf("exact legacy v2 shape was rejected: %v", err)
	}

	if err := LyricsSourceDocumentV3ToV2Compatibility(validLyricsSourceDocumentV3(t)); err == nil {
		t.Fatal("peer renditions were accepted as v2-compatible")
	}

	partial, err := UpconvertLyricsSourceDocumentV2(validLyricsSourceDocument())
	if err != nil {
		t.Fatal(err)
	}
	if err := LyricsSourceDocumentV3ToV2Compatibility(partial); err == nil {
		t.Fatal("partial performer evidence was accepted as v2-compatible")
	}

	nativeReading := LyricsSourceDocument{
		SchemaVersion:   LyricsSourceDocumentSchemaVersionV3,
		FixedIdentities: append([]LyricsSourceFixedIdentity{}, representable.FixedIdentities...),
		Renditions:      CloneLyricsSourceRenditions(representable.Renditions),
	}
	evidence := nativeReading.Renditions[0].Full.Lines[0].Segments[0].Ruby[0].ReadingEvidence
	*evidence = LyricsSourceReadingEvidence{
		Kind: LyricsSourceReadingEvidenceDeterministicDictionary, GeneratorVersion: "dictionary-v1",
	}
	if err := ValidateLyricsSourceDocument(nativeReading); err != nil {
		t.Fatalf("native reading fixture: %v", err)
	}
	if err := LyricsSourceDocumentV3ToV2Compatibility(nativeReading); err == nil {
		t.Fatal("native v3 reading evidence was accepted as v2-compatible")
	}
}

func TestLyricsSourceDocumentV2BytesRemainStableAndUpgradeLosslessly(t *testing.T) {
	v2 := validLyricsSourceDocument()
	body, err := json.Marshal(v2)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte(`"renditions"`)) {
		t.Fatal("source v2 canonical bytes gained renditions")
	}
	digest := sha256.Sum256(body)
	const expectedV2SHA256 = "8c71ce69720d422391915f78d1f31f78971c0f31cc37646df0c951098ba6bb87"
	if got := hex.EncodeToString(digest[:]); got != expectedV2SHA256 {
		t.Fatalf("source v2 canonical SHA-256=%s", got)
	}
	decoded, err := DecodeLyricsSourceDocument(body)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip, err := json.Marshal(decoded)
	if err != nil || !bytes.Equal(body, roundTrip) {
		t.Fatalf("source v2 canonical bytes drifted: err=%v", err)
	}
	upgraded, err := UpconvertLyricsSourceDocumentV2(decoded)
	if err != nil {
		t.Fatal(err)
	}
	upgradedFullWithoutEvidence := CloneLyricsSourceFull(upgraded.Renditions[0].Full)
	clearLyricsSourceV3ReadingEvidence(upgradedFullWithoutEvidence)
	if upgraded.SchemaVersion != LyricsSourceDocumentSchemaVersionV3 || len(upgraded.Renditions) != 1 ||
		upgraded.Renditions[0].RenditionKey != "sekai" || upgraded.Renditions[0].Full == nil ||
		!reflect.DeepEqual(*upgradedFullWithoutEvidence, decoded.Full) || upgraded.Renditions[0].Game != nil ||
		upgraded.Renditions[0].Relation.Kind != LyricsSourceRenditionRelationExactProjection ||
		!reflect.DeepEqual(upgraded.Renditions[0].Relation.LineIDs, decoded.GameProjection.LineIDs) {
		t.Fatalf("lossless source v2 up-conversion=%+v", upgraded)
	}
	if got, err := json.Marshal(decoded); err != nil || !bytes.Equal(got, body) {
		t.Fatal("source v2 input was mutated during up-conversion")
	}

	crossKind := decoded
	game := *CloneLyricsSourceFull(&decoded.Full)
	game.Version.Kind = "original"
	for index := range game.Lines {
		game.Lines[index].ID = fmt.Sprintf("game-%06d", index+1)
	}
	gameRef := LyricsSourceComponentRef{RenditionKey: decoded.FixedIdentities[1].RenditionKey}
	crossKind.Game = &game
	crossKind.Provenance.GameText = &gameRef
	crossKind.GameProjection = nil
	crossKind.Provenance.GameProjection = nil
	if err := ValidateLyricsSourceDocument(crossKind); err != nil {
		t.Fatalf("legacy cross-kind fixture must remain v2-decodable: %v", err)
	}
	if _, err := UpconvertLyricsSourceDocumentV2(crossKind); err == nil ||
		!strings.Contains(err.Error(), "do not form one rendition") {
		t.Fatalf("cross-kind v2 up-conversion error=%v", err)
	}
}
