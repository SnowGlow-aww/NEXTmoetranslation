package model

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func validGameOnlyAvailabilityDocument() LyricsAvailabilityDocument {
	gameRef := LyricsSourceComponentRef{RenditionKey: "game-sekai"}
	game := validLyricsSourceFull()
	for index := range game.Lines {
		game.Lines[index].ID = strings.Replace(game.Lines[index].ID, "full-", "game-", 1)
	}
	return LyricsAvailabilityDocument{
		SchemaVersion:   LyricsAvailabilityDocumentSchemaVersion,
		State:           LyricsAvailabilityStateGameOnly,
		ReasonCode:      LyricsSourceVersionReasonTaggedGameOnlyFullFromVocaloid,
		FixedIdentities: []LyricsSourceFixedIdentity{validProviderFixedIdentity(LyricsSourceProviderSekaipedia, gameRef.RenditionKey)},
		Provenance: LyricsAvailabilityComponentProvenance{
			GameText: &gameRef, PerformerSegmentation: &gameRef, Ruby: &gameRef, VersionEvidence: &gameRef,
		},
		Game: &game,
	}
}

func TestLyricsAvailabilityDocumentUnion(t *testing.T) {
	t.Run("Game-only never owns Full or a projection", func(t *testing.T) {
		document := validGameOnlyAvailabilityDocument()
		if err := ValidateLyricsAvailabilityDocument(document); err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := DecodeLyricsAvailabilityDocument(body)
		if err != nil || !reflect.DeepEqual(decoded, document) {
			t.Fatalf("round trip err=%v decoded=%+v", err, decoded)
		}
		if strings.Contains(string(body), `"full"`) || strings.Contains(string(body), `"gameProjection"`) {
			t.Fatalf("Game-only document synthesized a Full surface: %s", body)
		}
	})

	t.Run("catalog instrumental is explicitly text free", func(t *testing.T) {
		document := LyricsAvailabilityDocument{
			SchemaVersion:   LyricsAvailabilityDocumentSchemaVersion,
			State:           LyricsAvailabilityStateSatisfiedNoLyrics,
			NoLyricsReason:  LyricsAvailabilityNoLyricsCatalogInstrumental,
			FixedIdentities: []LyricsSourceFixedIdentity{},
		}
		if err := ValidateLyricsAvailabilityDocument(document); err != nil {
			t.Fatal(err)
		}
		withText := document
		game := validLyricsSourceFull()
		withText.Game = &game
		if err := ValidateLyricsAvailabilityDocument(withText); err == nil {
			t.Fatal("catalog instrumental accepted provisional lyric text")
		}
	})

	t.Run("unresolved states remain fail closed", func(t *testing.T) {
		for _, state := range []LyricsAvailabilityState{
			LyricsAvailabilityStateAmbiguous,
			LyricsAvailabilityStateMissing,
			LyricsAvailabilityStateIncomplete,
			LyricsAvailabilityStateFailed,
		} {
			document := LyricsAvailabilityDocument{
				SchemaVersion:   LyricsAvailabilityDocumentSchemaVersion,
				State:           state,
				ReasonCode:      LyricsSourceVersionReasonVersionConflict,
				FixedIdentities: []LyricsSourceFixedIdentity{},
			}
			if err := ValidateLyricsAvailabilityDocument(document); err != nil {
				t.Fatalf("state %s: %v", state, err)
			}
		}
	})
}

func TestLyricsAvailabilityDocumentRejectsUnsafeGameOnlyVariants(t *testing.T) {
	tests := map[string]func(*LyricsAvailabilityDocument){
		"wrong reason": func(document *LyricsAvailabilityDocument) {
			document.ReasonCode = LyricsSourceVersionReasonUntaggedFullOnly
		},
		"missing Game text provenance": func(document *LyricsAvailabilityDocument) {
			document.Provenance.GameText = nil
		},
		"missing version provenance": func(document *LyricsAvailabilityDocument) {
			document.Provenance.VersionEvidence = nil
		},
		"unknown rendition": func(document *LyricsAvailabilityDocument) {
			document.Provenance.GameText = &LyricsSourceComponentRef{RenditionKey: "unknown"}
		},
		"performer evidence removed": func(document *LyricsAvailabilityDocument) {
			document.Provenance.PerformerSegmentation = nil
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			document := validGameOnlyAvailabilityDocument()
			mutate(&document)
			if err := ValidateLyricsAvailabilityDocument(document); err == nil {
				t.Fatal("unsafe Game-only document was accepted")
			}
		})
	}
}

func TestLyricsAvailabilityDecoderRejectsRomajiFields(t *testing.T) {
	document := LyricsAvailabilityDocument{
		SchemaVersion:   LyricsAvailabilityDocumentSchemaVersion,
		State:           LyricsAvailabilityStateMissing,
		ReasonCode:      LyricsSourceVersionReasonVersionConflict,
		FixedIdentities: []LyricsSourceFixedIdentity{},
	}
	body, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	body = append(body[:len(body)-1], []byte(`,"romaji":"forbidden"}`)...)
	if _, err := DecodeLyricsAvailabilityDocument(body); err == nil {
		t.Fatal("availability decoder accepted a romaji field")
	}
}
