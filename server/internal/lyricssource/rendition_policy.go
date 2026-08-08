package lyricssource

import (
	"errors"
	"strings"

	"moesekai/server/internal/model"
)

// PerformerSegmentationPolicy is the historical name for a closed catalog
// rendition-selection decision. It must never erase source-proven singer tags:
// every selected vocal rendition (SEKAI, VIRTUAL SINGER, or Original) preserves
// its exact performer groups. The zero value remains valid only for legacy
// source-only callers and still fails closed on unknown rendition kinds.
type PerformerSegmentationPolicy string

const (
	PerformerSegmentationDisabled      PerformerSegmentationPolicy = "disabled"
	PerformerSegmentationSekaiEligible PerformerSegmentationPolicy = "sekai_eligible"
)

var ErrCatalogRenditionConflict = errors.New("catalog rendition policy conflicts with source version evidence")

// PerformerSegmentationPolicyFromCatalogVocals derives which source rendition
// the catalog authorizes. The legacy enum values are retained for persisted job
// compatibility; "disabled" means no SEKAI rendition is catalog-authorized,
// not that performer metadata should be flattened.
func PerformerSegmentationPolicyFromCatalogVocals(vocals []model.CatalogVocalSignal) PerformerSegmentationPolicy {
	for _, vocal := range vocals {
		if strings.EqualFold(strings.TrimSpace(vocal.VocalType), "sekai") {
			return PerformerSegmentationSekaiEligible
		}
	}
	return PerformerSegmentationDisabled
}

func performerSegmentationAllowed(policy PerformerSegmentationPolicy, renditionKind string) bool {
	switch policy {
	case PerformerSegmentationDisabled:
		return renditionKind == "original" || renditionKind == "vocaloid"
	case PerformerSegmentationSekaiEligible:
		return renditionKind == "original" || renditionKind == "sekai" || renditionKind == "vocaloid"
	case "":
		return renditionKind == "original" || renditionKind == "sekai" || renditionKind == "vocaloid"
	default:
		return false
	}
}

func fullRenditionKey(kind string) string {
	switch kind {
	case "original", "sekai", "vocaloid":
		return "full-" + kind
	default:
		return ""
	}
}

func applyPerformerSegmentationPolicy(identity MusicIdentity, extraction Extraction) (Extraction, error) {
	if performerSegmentationAllowed(identity.PerformerSegmentationPolicy, extraction.Version.Kind) {
		return extraction, nil
	}
	if identity.PerformerSegmentationPolicy == PerformerSegmentationDisabled && extraction.Version.Kind == "sekai" {
		return Extraction{}, ErrCatalogRenditionConflict
	}
	return Extraction{}, ErrMalformedResponse
}

func rubySpansText(spans []RubySpan) string {
	var result strings.Builder
	for _, span := range spans {
		result.WriteString(span.Text)
	}
	return result.String()
}
