package model

import (
	"errors"
	"fmt"
	"sort"
)

// LyricsSourceRenditionKind is the closed source-declared rendition family.
// Alternate is reserved for source labels that explicitly declare Alternate
// Vocal or Another Vocal semantics.
type LyricsSourceRenditionKind string

const (
	LyricsSourceRenditionOriginal  LyricsSourceRenditionKind = "original"
	LyricsSourceRenditionSekai     LyricsSourceRenditionKind = "sekai"
	LyricsSourceRenditionVocaloid  LyricsSourceRenditionKind = "vocaloid"
	LyricsSourceRenditionAlternate LyricsSourceRenditionKind = "alternate"
)

// LyricsSourceTabPath preserves one exact source tab path as ordered labels.
// A nested path such as Game Version / SEKAI is encoded as two labels rather
// than a slash-joined string, so source labels remain unambiguous.
type LyricsSourceTabPath []string

type LyricsSourceRenditionRelationKind string

const (
	LyricsSourceRenditionRelationNone            LyricsSourceRenditionRelationKind = "none"
	LyricsSourceRenditionRelationExactProjection LyricsSourceRenditionRelationKind = "exact_projection"
)

// LyricsSourceReadingEvidenceKind is the closed per-span reading provenance
// union used only by source/recovery v3. Legacy v2 coarse ruby provenance is
// retained explicitly during lossless up-conversion instead of being guessed.
type LyricsSourceReadingEvidenceKind string

const (
	LyricsSourceReadingEvidenceExplicitSourceKana      LyricsSourceReadingEvidenceKind = "explicit_source_kana"
	LyricsSourceReadingEvidenceSourceTransliteration   LyricsSourceReadingEvidenceKind = "source_transliteration"
	LyricsSourceReadingEvidenceDeterministicDictionary LyricsSourceReadingEvidenceKind = "deterministic_dictionary"
	LyricsSourceReadingEvidenceFixedReviewedToken      LyricsSourceReadingEvidenceKind = "fixed_reviewed_token"
	LyricsSourceReadingEvidenceLegacyV2Component       LyricsSourceReadingEvidenceKind = "legacy_v2_component"
)

type LyricsSourceRenditionSide string

const (
	LyricsSourceRenditionSideFull LyricsSourceRenditionSide = "full"
	LyricsSourceRenditionSideGame LyricsSourceRenditionSide = "game"
)

// LyricsSourceReadingEvidence binds one emitted reading either to an exact
// fixed source location or to one closed deterministic generator. Generated
// evidence never carries a source locator.
type LyricsSourceReadingEvidence struct {
	Kind                 LyricsSourceReadingEvidenceKind `json:"kind"`
	FixedIdentityKey     string                          `json:"fixedIdentityKey,omitempty"`
	RenditionKey         string                          `json:"renditionKey,omitempty"`
	Side                 LyricsSourceRenditionSide       `json:"side,omitempty"`
	SourceRowOrdinal     int                             `json:"sourceRowOrdinal,omitempty"`
	SourceSegmentOrdinal int                             `json:"sourceSegmentOrdinal,omitempty"`
	GeneratorVersion     string                          `json:"generatorVersion,omitempty"`
}

// LyricsSourcePerformerEvidenceState is independent for Full and Game. None,
// partial structured evidence, and complete structured evidence are never
// inferred from another side or rendition.
type LyricsSourcePerformerEvidenceState string

const (
	LyricsSourcePerformerEvidenceNone           LyricsSourcePerformerEvidenceState = "none"
	LyricsSourcePerformerEvidenceSourcePartial  LyricsSourcePerformerEvidenceState = "source_partial_structured"
	LyricsSourcePerformerEvidenceSourceComplete LyricsSourcePerformerEvidenceState = "source_complete_structured"
)

// LyricsSourceRenditionRelation is the closed Game-to-Full relation union.
// FullRenditionKey is explicit so a cross-rendition projection can be rejected
// rather than silently interpreted as local.
type LyricsSourceRenditionRelation struct {
	Kind             LyricsSourceRenditionRelationKind `json:"kind"`
	FullRenditionKey string                            `json:"fullRenditionKey,omitempty"`
	LineIDs          []string                          `json:"lineIds,omitempty"`
}

// LyricsSourceRenditionProvenance binds every independently persisted
// component to exactly one immutable fixed identity. Full and Game own
// independent segmentation and ruby references; relation and version evidence
// are required even when the relation kind is none.
type LyricsSourceRenditionProvenance struct {
	FullText                  *LyricsSourceComponentRef `json:"fullText,omitempty"`
	FullPerformerSegmentation *LyricsSourceComponentRef `json:"fullPerformerSegmentation,omitempty"`
	FullRuby                  *LyricsSourceComponentRef `json:"fullRuby,omitempty"`
	GameText                  *LyricsSourceComponentRef `json:"gameText,omitempty"`
	GamePerformerSegmentation *LyricsSourceComponentRef `json:"gamePerformerSegmentation,omitempty"`
	GameRuby                  *LyricsSourceComponentRef `json:"gameRuby,omitempty"`
	RelationEvidence          LyricsSourceComponentRef  `json:"relationEvidence"`
	VersionEvidence           LyricsSourceComponentRef  `json:"versionEvidence"`
}

// LyricsSourceRendition is one peer source rendition. Full and Game are
// independent optional text components. Exact projection never crosses the
// rendition boundary and may coexist with an independently retained Game.
type LyricsSourceRendition struct {
	RenditionKey          string                             `json:"renditionKey"`
	SourceKind            LyricsSourceRenditionKind          `json:"sourceKind"`
	SourceTabPaths        []LyricsSourceTabPath              `json:"sourceTabPaths"`
	ReasonCode            LyricsSourceVersionReasonCode      `json:"reasonCode"`
	SourcePerformerIDs    []string                           `json:"sourcePerformerIds,omitempty"`
	FullPerformerEvidence LyricsSourcePerformerEvidenceState `json:"fullPerformerEvidence"`
	GamePerformerEvidence LyricsSourcePerformerEvidenceState `json:"gamePerformerEvidence"`
	Full                  *LyricsSourceFull                  `json:"full,omitempty"`
	Game                  *LyricsSourceFull                  `json:"game,omitempty"`
	Relation              LyricsSourceRenditionRelation      `json:"relation"`
	Provenance            LyricsSourceRenditionProvenance    `json:"provenance"`
	PrivateReview         *LyricsSourcePrivateReview         `json:"privateReview,omitempty"`
}

type LyricsSourceRenditionComponentKind string

const (
	LyricsSourceRenditionComponentFullText                  LyricsSourceRenditionComponentKind = "full_text"
	LyricsSourceRenditionComponentFullPerformerSegmentation LyricsSourceRenditionComponentKind = "full_performer_segmentation"
	LyricsSourceRenditionComponentFullRuby                  LyricsSourceRenditionComponentKind = "full_ruby"
	LyricsSourceRenditionComponentGameText                  LyricsSourceRenditionComponentKind = "game_text"
	LyricsSourceRenditionComponentGamePerformerSegmentation LyricsSourceRenditionComponentKind = "game_performer_segmentation"
	LyricsSourceRenditionComponentGameRuby                  LyricsSourceRenditionComponentKind = "game_ruby"
	LyricsSourceRenditionComponentRelation                  LyricsSourceRenditionComponentKind = "relation"
	LyricsSourceRenditionComponentVersion                   LyricsSourceRenditionComponentKind = "version"
)

// LyricsSourceRenditionComponentBinding is the canonical reusable contribution
// record for staging/store/public attribution. ComponentKey is globally stable
// within one document and FixedIdentityKey resolves through FixedIdentities.
type LyricsSourceRenditionComponentBinding struct {
	RenditionKey     string
	Component        LyricsSourceRenditionComponentKind
	ComponentKey     string
	FixedIdentityKey string
}

func LyricsSourceRenditionComponentKey(
	renditionKey string,
	component LyricsSourceRenditionComponentKind,
) string {
	return "renditions/" + renditionKey + "/" + string(component)
}

func LyricsSourceRenditionComponentRank(component LyricsSourceRenditionComponentKind) int {
	switch component {
	case LyricsSourceRenditionComponentFullText:
		return 0
	case LyricsSourceRenditionComponentFullPerformerSegmentation:
		return 1
	case LyricsSourceRenditionComponentFullRuby:
		return 2
	case LyricsSourceRenditionComponentGameText:
		return 3
	case LyricsSourceRenditionComponentGamePerformerSegmentation:
		return 4
	case LyricsSourceRenditionComponentGameRuby:
		return 5
	case LyricsSourceRenditionComponentRelation:
		return 6
	case LyricsSourceRenditionComponentVersion:
		return 7
	default:
		return -1
	}
}

// EnumerateLyricsSourceRenditionComponents is the single canonical component
// enumerator for the plural contract. It validates payload/ref shape, emits one
// binding for each contributing component, and sorts by rendition key followed
// by the closed component order.
func EnumerateLyricsSourceRenditionComponents(
	renditions []LyricsSourceRendition,
) ([]LyricsSourceRenditionComponentBinding, error) {
	if err := ValidateLyricsSourceRenditionSetPayload(renditions); err != nil {
		return nil, err
	}
	bindings := make([]LyricsSourceRenditionComponentBinding, 0, len(renditions)*8)
	appendRef := func(
		rendition LyricsSourceRendition,
		component LyricsSourceRenditionComponentKind,
		reference *LyricsSourceComponentRef,
		required bool,
	) error {
		if required && (reference == nil || reference.RenditionKey == "") {
			return fmt.Errorf("lyrics source rendition %q component %q requires one fixed identity reference", rendition.RenditionKey, component)
		}
		if !required && reference != nil {
			return fmt.Errorf("lyrics source rendition %q component %q has a reference without component data", rendition.RenditionKey, component)
		}
		if reference == nil {
			return nil
		}
		bindings = append(bindings, LyricsSourceRenditionComponentBinding{
			RenditionKey: rendition.RenditionKey, Component: component,
			ComponentKey:     LyricsSourceRenditionComponentKey(rendition.RenditionKey, component),
			FixedIdentityKey: reference.RenditionKey,
		})
		return nil
	}
	for _, rendition := range renditions {
		fullSegmentation := rendition.Full != nil && lyricsSourceFullHasPerformerSegmentation(*rendition.Full)
		fullRuby := rendition.Full != nil && lyricsSourceFullHasSourceReadingEvidence(*rendition.Full)
		gameSegmentation := rendition.Game != nil && lyricsSourceFullHasPerformerSegmentation(*rendition.Game)
		gameRuby := rendition.Game != nil && lyricsSourceFullHasSourceReadingEvidence(*rendition.Game)
		for _, item := range []struct {
			component LyricsSourceRenditionComponentKind
			reference *LyricsSourceComponentRef
			required  bool
		}{
			{LyricsSourceRenditionComponentFullText, rendition.Provenance.FullText, rendition.Full != nil},
			{LyricsSourceRenditionComponentFullPerformerSegmentation, rendition.Provenance.FullPerformerSegmentation, fullSegmentation},
			{LyricsSourceRenditionComponentFullRuby, rendition.Provenance.FullRuby, fullRuby},
			{LyricsSourceRenditionComponentGameText, rendition.Provenance.GameText, rendition.Game != nil},
			{LyricsSourceRenditionComponentGamePerformerSegmentation, rendition.Provenance.GamePerformerSegmentation, gameSegmentation},
			{LyricsSourceRenditionComponentGameRuby, rendition.Provenance.GameRuby, gameRuby},
			{LyricsSourceRenditionComponentRelation, sourceComponentRefPtr(rendition.Provenance.RelationEvidence), true},
			{LyricsSourceRenditionComponentVersion, sourceComponentRefPtr(rendition.Provenance.VersionEvidence), true},
		} {
			if err := appendRef(rendition, item.component, item.reference, item.required); err != nil {
				return nil, err
			}
		}
	}
	sort.Slice(bindings, func(left, right int) bool {
		if bindings[left].RenditionKey != bindings[right].RenditionKey {
			return bindings[left].RenditionKey < bindings[right].RenditionKey
		}
		return LyricsSourceRenditionComponentRank(bindings[left].Component) <
			LyricsSourceRenditionComponentRank(bindings[right].Component)
	})
	seen := make(map[string]struct{}, len(bindings))
	for _, binding := range bindings {
		if binding.FixedIdentityKey == "" {
			return nil, errors.New("lyrics source rendition component has an empty fixed identity key")
		}
		if _, duplicate := seen[binding.ComponentKey]; duplicate {
			return nil, fmt.Errorf("lyrics source rendition component %q is duplicated", binding.ComponentKey)
		}
		seen[binding.ComponentKey] = struct{}{}
	}
	return bindings, nil
}

func CloneLyricsSourceRenditions(input []LyricsSourceRendition) []LyricsSourceRendition {
	if input == nil {
		return nil
	}
	result := make([]LyricsSourceRendition, len(input))
	for index, rendition := range input {
		result[index] = rendition
		result[index].SourceTabPaths = cloneLyricsSourceTabPaths(rendition.SourceTabPaths)
		result[index].SourcePerformerIDs = cloneStringsPreservingNil(rendition.SourcePerformerIDs)
		result[index].Full = CloneLyricsSourceFull(rendition.Full)
		result[index].Game = CloneLyricsSourceFull(rendition.Game)
		result[index].Relation.LineIDs = cloneStringsPreservingNil(rendition.Relation.LineIDs)
		result[index].Provenance = cloneLyricsSourceRenditionProvenance(rendition.Provenance)
		if rendition.PrivateReview != nil {
			privateReview := *rendition.PrivateReview
			result[index].PrivateReview = &privateReview
		}
	}
	return result
}

func cloneLyricsSourceTabPaths(input []LyricsSourceTabPath) []LyricsSourceTabPath {
	if input == nil {
		return nil
	}
	result := make([]LyricsSourceTabPath, len(input))
	for index, path := range input {
		if path != nil {
			result[index] = append(LyricsSourceTabPath{}, path...)
		}
	}
	return result
}

func cloneLyricsSourceRenditionProvenance(input LyricsSourceRenditionProvenance) LyricsSourceRenditionProvenance {
	result := input
	for target, reference := range map[**LyricsSourceComponentRef]*LyricsSourceComponentRef{
		&result.FullText:                  input.FullText,
		&result.FullPerformerSegmentation: input.FullPerformerSegmentation,
		&result.FullRuby:                  input.FullRuby,
		&result.GameText:                  input.GameText,
		&result.GamePerformerSegmentation: input.GamePerformerSegmentation,
		&result.GameRuby:                  input.GameRuby,
	} {
		if reference != nil {
			copy := *reference
			*target = &copy
		}
	}
	return result
}
