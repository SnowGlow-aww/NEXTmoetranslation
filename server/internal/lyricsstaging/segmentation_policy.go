package lyricsstaging

import (
	"errors"
	"fmt"

	"moesekai/server/internal/lyricssource"
	"moesekai/server/internal/model"
)

func ValidateFixedPerformerSegmentationPolicy(identity CatalogIdentity, fixed lyricssource.FixedRevision) error {
	policy := lyricssource.PerformerSegmentationPolicyFromCatalogVocals(identity.Vocals)
	if policy != lyricssource.PerformerSegmentationDisabled && policy != lyricssource.PerformerSegmentationSekaiEligible {
		return errors.New("catalog performer segmentation policy is invalid")
	}

	authoritativePerformerSegmentation := false
	if fixed.Document != nil {
		if err := model.ValidateLyricsSourceDocument(*fixed.Document); err != nil {
			return err
		}
		if fixed.Document.Full.Version.Kind != fixed.Extraction.Version.Kind {
			return errors.New("fixed source document and extraction version kinds differ")
		}
		authoritativePerformerSegmentation = acceptsAuthoritativePerformerSegmentation(*fixed.Document)
		if err := validateDocumentPerformerSegmentationPolicy(policy, *fixed.Document, authoritativePerformerSegmentation); err != nil {
			return err
		}
	}
	if len(fixed.Extraction.Lines) == 0 {
		return errors.New("fixed extraction is required for catalog performer policy validation")
	}
	return validateExtractionPerformerSegmentationPolicy(policy, fixed.Extraction, authoritativePerformerSegmentation)
}

func validateDocumentPerformerSegmentationPolicy(
	policy lyricssource.PerformerSegmentationPolicy,
	document model.LyricsSourceDocument,
	authoritativePerformerSegmentation bool,
) error {
	kind := document.Full.Version.Kind
	if kind != "original" && kind != "sekai" && kind != "vocaloid" {
		return errors.New("fixed source document has an invalid version kind")
	}
	if policy == lyricssource.PerformerSegmentationDisabled && kind == "sekai" {
		return errors.New("catalog-disabled fixed source cannot select a SEKAI Full rendition")
	}
	if document.Provenance.PerformerSegmentation == nil {
		if !fullIsCompleteAndPerformerFree(document.Full) {
			return errors.New("unsegmented Full must preserve one complete performer-free segment per line")
		}
		return nil
	}
	if kind != "sekai" && !authoritativePerformerSegmentation {
		return errors.New("non-SEKAI performer segmentation requires authoritative structured source evidence")
	}
	return nil
}

func validateExtractionPerformerSegmentationPolicy(
	policy lyricssource.PerformerSegmentationPolicy,
	extraction lyricssource.Extraction,
	authoritativePerformerSegmentation bool,
) error {
	kind := extraction.Version.Kind
	if kind != "original" && kind != "sekai" && kind != "vocaloid" {
		return errors.New("fixed extraction has an invalid version kind")
	}
	if policy == lyricssource.PerformerSegmentationDisabled && kind == "sekai" {
		return errors.New("catalog-disabled extraction cannot select a SEKAI Full rendition")
	}
	if structuredExtractionIsUnassigned(extraction) {
		return nil
	}
	if kind != "sekai" && !authoritativePerformerSegmentation {
		return errors.New("non-SEKAI extraction segmentation requires authoritative structured source evidence")
	}
	return nil
}

// acceptsAuthoritativePerformerSegmentation recognizes the exact private marker
// used for source-proven structured singer groups. The marker is rendition-
// agnostic: VIRTUAL SINGER and Original renditions are no longer flattened.
func acceptsAuthoritativePerformerSegmentation(document model.LyricsSourceDocument) bool {
	return document.PrivateReview != nil &&
		document.PrivateReview.PerformerSegmentationEvidence ==
			model.LyricsSourcePerformerSegmentationEvidenceAuthoritativeCompleteStructured &&
		model.ValidateLyricsSourceDocument(document) == nil
}

func structuredExtractionIsUnassigned(extraction lyricssource.Extraction) bool {
	if len(extraction.Performers) != 0 {
		return false
	}
	for _, line := range extraction.Lines {
		if len(line.Segments) != 1 || line.Segments[0].Text != line.Japanese ||
			len(line.Segments[0].PerformerIDs) != 0 || len(line.TrailingPerformerIDs) != 0 {
			return false
		}
	}
	return true
}

func fullIsCompleteAndPerformerFree(full model.LyricsSourceFull) bool {
	if len(full.Performers) != 0 {
		return false
	}
	for _, line := range full.Lines {
		if len(line.Segments) != 1 || line.Segments[0].Text != line.Text ||
			len(line.Segments[0].PerformerIDs) != 0 || len(line.TrailingPerformerIDs) != 0 {
			return false
		}
	}
	return true
}

func catalogPerformerPolicyError(musicID int, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("music %d catalog performer segmentation policy: %w", musicID, err)
}
