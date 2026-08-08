package model

import (
	"errors"
	"fmt"
)

const LyricsAvailabilityDocumentSchemaVersion = 1

type LyricsAvailabilityState string

const (
	LyricsAvailabilityStateGameOnly          LyricsAvailabilityState = "game_only"
	LyricsAvailabilityStateSatisfiedNoLyrics LyricsAvailabilityState = "satisfied_no_lyrics"
	LyricsAvailabilityStateAmbiguous         LyricsAvailabilityState = "ambiguous"
	LyricsAvailabilityStateMissing           LyricsAvailabilityState = "missing"
	LyricsAvailabilityStateIncomplete        LyricsAvailabilityState = "incomplete"
	LyricsAvailabilityStateFailed            LyricsAvailabilityState = "failed"

	LyricsAvailabilityNoLyricsCatalogInstrumental = "catalog_instrumental"
)

// LyricsAvailabilityComponentProvenance is the source-component map for a
// rendition that cannot be represented by the Full-owning source document v1.
// GameText owns authoritative Game text; the other refs retain the same exact
// fixed-identity semantics as LyricsSourceComponentProvenance.
type LyricsAvailabilityComponentProvenance struct {
	GameText              *LyricsSourceComponentRef `json:"gameText,omitempty"`
	PerformerSegmentation *LyricsSourceComponentRef `json:"performerSegmentation,omitempty"`
	Ruby                  *LyricsSourceComponentRef `json:"ruby,omitempty"`
	VersionEvidence       *LyricsSourceComponentRef `json:"versionEvidence,omitempty"`
}

// LyricsAvailabilityDocument is an additive, non-Full source contract.
//
// Game-only owns an authoritative Game rendition and never synthesizes Full.
// Satisfied-no-lyrics owns only the reviewed catalog reason. Fail-closed
// unresolved states intentionally own neither source text nor fixed identities.
// Complete songs continue to use LyricsSourceDocument schema v1.
type LyricsAvailabilityDocument struct {
	SchemaVersion   int                                   `json:"schemaVersion"`
	State           LyricsAvailabilityState               `json:"state"`
	ReasonCode      LyricsSourceVersionReasonCode         `json:"reasonCode"`
	NoLyricsReason  string                                `json:"noLyricsReason,omitempty"`
	FixedIdentities []LyricsSourceFixedIdentity           `json:"fixedIdentities"`
	Provenance      LyricsAvailabilityComponentProvenance `json:"provenance"`
	Game            *LyricsSourceFull                     `json:"game,omitempty"`
	AlternateVocals []LyricsSourceAlternateVocal          `json:"alternateVocals,omitempty"`
	PrivateReview   *LyricsSourcePrivateReview            `json:"privateReview,omitempty"`
}

func DecodeLyricsAvailabilityDocument(body []byte) (LyricsAvailabilityDocument, error) {
	var document LyricsAvailabilityDocument
	if err := decodeClosedLyricsSourceJSON(body, &document); err != nil {
		return LyricsAvailabilityDocument{}, fmt.Errorf("decode lyrics availability document: %w", err)
	}
	if err := ValidateLyricsAvailabilityDocument(document); err != nil {
		return LyricsAvailabilityDocument{}, err
	}
	return document, nil
}

func ValidateLyricsAvailabilityDocument(document LyricsAvailabilityDocument) error {
	if document.SchemaVersion != LyricsAvailabilityDocumentSchemaVersion {
		return fmt.Errorf("unsupported lyrics availability document schema version %d", document.SchemaVersion)
	}
	switch document.State {
	case LyricsAvailabilityStateGameOnly:
		return validateLyricsGameOnlyAvailability(document)
	case LyricsAvailabilityStateSatisfiedNoLyrics:
		if document.NoLyricsReason != LyricsAvailabilityNoLyricsCatalogInstrumental || document.ReasonCode != "" {
			return errors.New("satisfied no-lyrics availability requires the reviewed catalog instrumental reason")
		}
		return validateLyricsTextFreeAvailability(document)
	case LyricsAvailabilityStateAmbiguous, LyricsAvailabilityStateMissing,
		LyricsAvailabilityStateIncomplete, LyricsAvailabilityStateFailed:
		if document.NoLyricsReason != "" || document.ReasonCode != LyricsSourceVersionReasonVersionConflict {
			return errors.New("unresolved lyrics availability must remain fail closed")
		}
		return validateLyricsTextFreeAvailability(document)
	default:
		return fmt.Errorf("unsupported lyrics availability state %q", document.State)
	}
}

func validateLyricsGameOnlyAvailability(document LyricsAvailabilityDocument) error {
	if (document.ReasonCode != LyricsSourceVersionReasonTaggedGameOnly &&
		document.ReasonCode != LyricsSourceVersionReasonTaggedGameOnlyFullFromVocaloid) ||
		document.NoLyricsReason != "" || document.Game == nil {
		return errors.New("Game-only availability must own only an authoritative Game rendition")
	}
	if document.FixedIdentities == nil || len(document.FixedIdentities) == 0 ||
		len(document.FixedIdentities) > maxLyricsSourceFixedIdentities {
		return errors.New("Game-only availability requires bounded fixed identities")
	}
	identities := make(map[string]LyricsSourceFixedIdentity, len(document.FixedIdentities))
	for index, identity := range document.FixedIdentities {
		if err := ValidateLyricsSourceFixedIdentity(identity); err != nil {
			return fmt.Errorf("lyrics availability fixed identity %d: %w", index+1, err)
		}
		if _, duplicate := identities[identity.RenditionKey]; duplicate {
			return fmt.Errorf("lyrics availability repeats rendition key %q", identity.RenditionKey)
		}
		identities[identity.RenditionKey] = identity
	}

	hasAuthoritativeSegmentation := document.PrivateReview != nil
	if hasAuthoritativeSegmentation && document.PrivateReview.PerformerSegmentationEvidence !=
		LyricsSourcePerformerSegmentationEvidenceAuthoritativeCompleteStructured {
		return errors.New("lyrics availability privateReview has an invalid performer segmentation marker")
	}
	authoritativeVocaloidSegmentation := hasAuthoritativeSegmentation && document.Game.Version.Kind == "vocaloid"
	if authoritativeVocaloidSegmentation {
		if err := ValidateLyricsSourceFullWithAuthoritativeVocaloidSegmentation(*document.Game); err != nil {
			return err
		}
	} else if err := ValidateLyricsSourceFull(*document.Game); err != nil {
		return err
	}
	if document.Provenance.GameText == nil || document.Provenance.VersionEvidence == nil {
		return errors.New("Game-only availability requires Game text and version provenance")
	}
	if err := validateLyricsSourceComponentRef("gameText", *document.Provenance.GameText, identities); err != nil {
		return err
	}
	if err := validateLyricsSourceComponentRef("versionEvidence", *document.Provenance.VersionEvidence, identities); err != nil {
		return err
	}

	hasSegmentation, hasRuby := lyricsSourceFullComponentPresence(*document.Game)
	if hasAuthoritativeSegmentation && !hasSegmentation {
		return errors.New("lyrics availability performer evidence is present without component data")
	}
	if err := validateConditionalLyricsSourceComponentRef(
		"performerSegmentation", document.Provenance.PerformerSegmentation, hasSegmentation, identities,
	); err != nil {
		return err
	}
	if err := validateConditionalLyricsSourceComponentRef("ruby", document.Provenance.Ruby, hasRuby, identities); err != nil {
		return err
	}
	if authoritativeVocaloidSegmentation {
		gameIdentity := identities[document.Provenance.GameText.RenditionKey]
		segmentationIdentity := identities[document.Provenance.PerformerSegmentation.RenditionKey]
		if LyricsSourceCompositionRenditionKey(gameIdentity) != LyricsSourceCompositionRenditionKey(segmentationIdentity) {
			return errors.New("authoritative vocaloid Game segmentation must resolve to the Game logical rendition")
		}
	}
	if len(document.AlternateVocals) > maxLyricsSourceFixedIdentities {
		return errors.New("lyrics availability has too many alternate vocal entries")
	}
	for index, alternate := range document.AlternateVocals {
		if err := validateLyricsSourceAlternateVocal(index, alternate, identities); err != nil {
			return err
		}
	}
	return nil
}

func validateLyricsTextFreeAvailability(document LyricsAvailabilityDocument) error {
	if document.FixedIdentities == nil || len(document.FixedIdentities) != 0 || document.Game != nil ||
		len(document.AlternateVocals) != 0 || document.PrivateReview != nil ||
		document.Provenance.GameText != nil || document.Provenance.PerformerSegmentation != nil ||
		document.Provenance.Ruby != nil || document.Provenance.VersionEvidence != nil {
		return errors.New("text-free lyrics availability must not retain provisional source components")
	}
	return nil
}

func lyricsSourceFullComponentPresence(full LyricsSourceFull) (bool, bool) {
	hasSegmentation := len(full.Performers) > 0
	hasRuby := false
	for _, line := range full.Lines {
		hasSegmentation = hasSegmentation || len(line.TrailingPerformerIDs) > 0 || len(line.Segments) != 1 ||
			(len(line.Segments) == 1 && line.Segments[0].Text != line.Text)
		for _, segment := range line.Segments {
			hasSegmentation = hasSegmentation || len(segment.PerformerIDs) > 0
			for _, span := range segment.Ruby {
				hasRuby = hasRuby || span.Reading != ""
			}
		}
	}
	return hasSegmentation, hasRuby
}
