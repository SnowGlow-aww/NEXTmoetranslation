package store

import (
	"errors"
	"fmt"

	"reflect"
	"strings"
	"unicode/utf8"

	"moesekai/server/internal/model"
)

func buildPublicLyricsV2(lyrics model.SongLyrics, bundle *publicLyricsSourceBundle,
	catalogPerformers catalogPerformerAliases,
) (PublicLyricsDetailDocument, error) {
	if bundle == nil {
		return PublicLyricsDetailDocument{}, errors.New("source-backed public lyrics require a source document")
	}
	document := bundle.document
	fullIdentity, ok := publicLyricsFixedIdentity(document, document.Provenance.FullText.RenditionKey)
	if !ok || lyrics.SourcePageID != fullIdentity.PageID || lyrics.SourceRevisionID != fullIdentity.RevisionID ||
		lyrics.SourceSHA1 != fullIdentity.SHA1 || lyrics.SourceURL != fullIdentity.CanonicalURL ||
		lyrics.SourceFetchedAt != fullIdentity.FetchedAt {
		return PublicLyricsDetailDocument{}, errors.New("editable lyrics source identity does not match the authoritative Full document")
	}
	if len(lyrics.Lines) != len(document.Full.Lines) {
		return PublicLyricsDetailDocument{}, errors.New("editable lyrics lines do not match the authoritative Full document")
	}
	sourcePerformers, err := newPublicLyricsSourcePerformerCatalog(document, catalogPerformers)
	if err != nil {
		return PublicLyricsDetailDocument{}, err
	}
	public := PublicLyricsDetailDocument{
		Version: 2, MusicID: lyrics.MusicID, Revision: lyrics.Revision, UpdatedAt: lyrics.UpdatedAt,
		State: PublicLyricsStateComplete, Attributions: publicLyricsAttributions(bundle),
		TranslationCredits: publicLyricsTranslationCredits(lyrics),
		AvailableVersions:  publicLyricsAvailableVersions(document),
		Lines:              make([]PublicLyricsLine, len(document.Full.Lines)),
	}
	if document.GameProjection != nil {
		public.GameProjection = &PublicLyricsGameProjection{
			ReasonCode: document.ReasonCode,
			LineIDs:    append([]string{}, document.GameProjection.LineIDs...),
		}
	}
	for lineIndex, sourceLine := range document.Full.Lines {
		draftLine := lyrics.Lines[lineIndex]
		if draftLine.ID != sourceLine.ID || draftLine.Order != lineIndex || draftLine.Japanese != sourceLine.Text ||
			draftLine.StanzaBreakBefore != sourceLine.StanzaBreakBefore || len(draftLine.Segments) != len(sourceLine.Segments) {
			return PublicLyricsDetailDocument{}, fmt.Errorf("editable lyrics line %d does not match the authoritative Full document", lineIndex+1)
		}
		trailingPerformerIDs, err := publicLyricsSourceSegmentPerformerIDs(sourceLine.TrailingPerformerIDs, sourcePerformers)
		if err != nil {
			return PublicLyricsDetailDocument{}, fmt.Errorf("authoritative Full line %d trailing performers: %w", lineIndex+1, err)
		}
		if document.Provenance.PerformerSegmentation == nil && len(trailingPerformerIDs) != 0 {
			return PublicLyricsDetailDocument{}, fmt.Errorf("authoritative Full line %d has unproven trailing performer attribution", lineIndex+1)
		}
		publicLine := PublicLyricsLine{
			ID: sourceLine.ID, Order: lineIndex, Japanese: sourceLine.Text,
			Chinese: draftLine.Chinese, English: draftLine.English,
			StanzaBreakBefore:    sourceLine.StanzaBreakBefore,
			Segments:             make([]model.LyricSegment, len(sourceLine.Segments)),
			TrailingPerformerIDs: append([]int{}, trailingPerformerIDs...),
		}
		for segmentIndex, sourceSegment := range sourceLine.Segments {
			draftSegment := draftLine.Segments[segmentIndex]
			if draftSegment.Text != sourceSegment.Text || !publicLyricsRubyMatches(draftSegment.Ruby, sourceSegment.Ruby) {
				return PublicLyricsDetailDocument{}, fmt.Errorf("editable lyrics line %d segment %d does not match the authoritative Full document", lineIndex+1, segmentIndex+1)
			}
			performerIDs, err := publicLyricsSourceSegmentPerformerIDs(sourceSegment.PerformerIDs, sourcePerformers)
			if err != nil {
				return PublicLyricsDetailDocument{}, fmt.Errorf("authoritative Full line %d segment %d: %w", lineIndex+1, segmentIndex+1, err)
			}
			if document.Provenance.PerformerSegmentation == nil && len(performerIDs) != 0 {
				return PublicLyricsDetailDocument{}, fmt.Errorf("authoritative Full line %d segment %d has unproven performer segmentation", lineIndex+1, segmentIndex+1)
			}
			expectedDraftPerformerIDs := performerIDs
			if len(expectedDraftPerformerIDs) == 0 {
				expectedDraftPerformerIDs = trailingPerformerIDs
			}
			if !reflect.DeepEqual(draftSegment.PerformerIDs, expectedDraftPerformerIDs) {
				return PublicLyricsDetailDocument{}, fmt.Errorf("editable lyrics line %d segment %d performer assignment is stale: it does not exactly match authoritative source IDs", lineIndex+1, segmentIndex+1)
			}
			ruby := make([]model.LyricRubySpan, len(sourceSegment.Ruby))
			for rubyIndex, span := range sourceSegment.Ruby {
				ruby[rubyIndex] = model.LyricRubySpan{Text: span.Text, Reading: span.Reading}
			}
			publicLine.Segments[segmentIndex] = model.LyricSegment{
				Text: sourceSegment.Text, PerformerIDs: performerIDs, Ruby: ruby,
			}
		}
		public.Lines[lineIndex] = publicLine
	}
	if err := validatePublicLyricsV2Detail(public, bundle, catalogPerformers); err != nil {
		return PublicLyricsDetailDocument{}, err
	}
	return public, nil
}

func publicLyricsFixedIdentity(document model.LyricsSourceDocument, renditionKey string) (model.LyricsSourceFixedIdentity, bool) {
	for _, identity := range document.FixedIdentities {
		if identity.RenditionKey == renditionKey {
			return identity, true
		}
	}
	return model.LyricsSourceFixedIdentity{}, false
}

func publicLyricsTranslationCredits(lyrics model.SongLyrics) *PublicLyricsTranslationCredits {
	translation := strings.TrimSpace(lyrics.TranslationCredit)
	if translation == "" {
		translation = strings.TrimSpace(lyrics.Attribution)
	}
	proofreading := strings.TrimSpace(lyrics.ProofreadingCredit)
	if translation == "" && proofreading == "" {
		return nil
	}
	return &PublicLyricsTranslationCredits{Translation: translation, Proofreading: proofreading}
}

func validPublicLyricsTranslationCredits(credits *PublicLyricsTranslationCredits) bool {
	if credits == nil {
		return true
	}
	if credits.Translation != strings.TrimSpace(credits.Translation) ||
		credits.Proofreading != strings.TrimSpace(credits.Proofreading) ||
		len(credits.Translation) > maxLyricsMetadataBytes || len(credits.Proofreading) > maxLyricsMetadataBytes ||
		!utf8.ValidString(credits.Translation) || !utf8.ValidString(credits.Proofreading) {
		return false
	}
	return credits.Translation != "" || credits.Proofreading != ""
}

func publicLyricsAttributions(bundle *publicLyricsSourceBundle) []PublicLyricsAttribution {
	if bundle == nil {
		return nil
	}
	return publicLyricsAttributionsFrom(bundle.document.FixedIdentities, bundle.contributions)
}

func publicLyricsAttributionsFrom(
	identities []model.LyricsSourceFixedIdentity,
	contributions map[string]string,
) []PublicLyricsAttribution {
	used := make(map[string]bool, len(contributions))
	for _, renditionKey := range contributions {
		used[renditionKey] = true
	}
	result := make([]PublicLyricsAttribution, 0, len(used))
	seen := map[string]bool{}
	for _, identity := range identities {
		if !used[identity.RenditionKey] {
			continue
		}
		licenseName, licenseURL := publicLyricsProviderLicense(identity.Provider)
		attribution := PublicLyricsAttribution{
			Provider: identity.Provider, Title: identity.Title, RevisionID: identity.RevisionID,
			RevisionURL: identity.CanonicalURL, LicenseName: licenseName, LicenseURL: licenseURL,
		}
		key := fmt.Sprintf("%s\x00%s\x00%d\x00%s\x00%s\x00%s", attribution.Provider, attribution.Title,
			attribution.RevisionID, attribution.RevisionURL, attribution.LicenseName, attribution.LicenseURL)
		if !seen[key] {
			seen[key] = true
			result = append(result, attribution)
		}
	}
	return result
}

func publicLyricsProviderLicense(provider model.LyricsSourceProvider) (string, string) {
	switch provider {
	case model.LyricsSourceProviderVocaloidFandom:
		return "CC BY-SA 3.0", "https://creativecommons.org/licenses/by-sa/3.0/"
	case model.LyricsSourceProviderMoegirl, model.LyricsSourceProviderMoegirlPublicExact:
		return "CC BY-NC-SA 3.0", "https://creativecommons.org/licenses/by-nc-sa/3.0/"
	case model.LyricsSourceProviderSekaipedia:
		return "CC BY-SA 4.0", "https://creativecommons.org/licenses/by-sa/4.0/"
	default:
		return "", ""
	}
}

func publicLyricsAvailableVersions(document model.LyricsSourceDocument) []string {
	switch document.ReasonCode {
	case model.LyricsSourceVersionReasonTaggedFullAndGame, model.LyricsSourceVersionReasonUntaggedUncutIdentity:
		return []string{publicLyricsFullVersion, publicLyricsGameVersion}
	default:
		return []string{publicLyricsFullVersion}
	}
}

func publicLyricsRubyMatches(editable []model.LyricRubySpan, source []model.LyricsSourceRubySpan) bool {
	if len(editable) != len(source) {
		return false
	}
	for index := range editable {
		if editable[index].Text != source[index].Text || editable[index].Reading != source[index].Reading {
			return false
		}
	}
	return true
}

func validatePublicLyricsV2Detail(public PublicLyricsDetailDocument, bundle *publicLyricsSourceBundle,
	catalogPerformers catalogPerformerAliases,
) error {
	if bundle == nil || public.Version != 2 || public.MusicID <= 0 || public.Revision <= 0 || public.UpdatedAt == "" ||
		(public.State != "" && public.State != PublicLyricsStateComplete) || public.NoLyricsReason != "" ||
		public.Attribution != "" || !validPublicLyricsTranslationCredits(public.TranslationCredits) ||
		len(public.Attributions) == 0 || len(public.Attributions) > 16 ||
		!reflect.DeepEqual(public.Attributions, publicLyricsAttributions(bundle)) ||
		!samePublicLyricsVersions(public.AvailableVersions, publicLyricsAvailableVersions(bundle.document)) {
		return errors.New("public lyrics v2 header or attributions do not match the source document")
	}
	document := bundle.document
	sourcePerformers, err := newPublicLyricsSourcePerformerCatalog(document, catalogPerformers)
	if err != nil {
		return err
	}
	for _, attribution := range public.Attributions {
		licenseName, licenseURL := publicLyricsProviderLicense(attribution.Provider)
		if attribution.Title == "" || attribution.RevisionID <= 0 || attribution.RevisionURL == "" ||
			licenseName == "" || attribution.LicenseName != licenseName || attribution.LicenseURL != licenseURL {
			return errors.New("public lyrics v2 attribution is not backed by a fixed provider license policy")
		}
	}
	if document.Provenance.PerformerSegmentation != nil && document.Full.Version.Kind != "sekai" &&
		(document.PrivateReview == nil || document.PrivateReview.PerformerSegmentationEvidence !=
			model.LyricsSourcePerformerSegmentationEvidenceAuthoritativeCompleteStructured) {
		return errors.New("public lyrics v2 non-SEKAI Full lacks authoritative structured performer evidence")
	}
	if len(public.Lines) == 0 || len(public.Lines) > maxLyricsLines || len(public.Lines) != len(document.Full.Lines) {
		return errors.New("public lyrics v2 lines do not match authoritative Full")
	}
	for lineIndex, line := range public.Lines {
		sourceLine := document.Full.Lines[lineIndex]
		if line.ID != sourceLine.ID || line.Order != lineIndex || line.Japanese != sourceLine.Text ||
			line.StanzaBreakBefore != sourceLine.StanzaBreakBefore || len(line.ID) > 128 ||
			len(line.Japanese) > maxLyricsLineTextBytes || len(line.Chinese) > maxLyricsLineTextBytes ||
			len(line.English) > maxLyricsLineTextBytes || !utf8.ValidString(line.Chinese) || !utf8.ValidString(line.English) ||
			len(line.Segments) == 0 || len(line.Segments) > maxLyricsSegmentsPerLine ||
			len(line.Segments) != len(sourceLine.Segments) || len(line.TrailingPerformerIDs) > maxLyricsPerformers {
			return fmt.Errorf("public lyrics v2 line %d is invalid or stale", lineIndex+1)
		}
		expectedTrailingPerformerIDs, err := publicLyricsSourceSegmentPerformerIDs(sourceLine.TrailingPerformerIDs, sourcePerformers)
		if err != nil {
			return fmt.Errorf("public lyrics v2 line %d trailing performers: %w", lineIndex+1, err)
		}
		if document.Provenance.PerformerSegmentation == nil && len(expectedTrailingPerformerIDs) != 0 {
			return fmt.Errorf("public lyrics v2 line %d has unproven trailing performer attribution", lineIndex+1)
		}
		if !sameLyricsPerformerIDs(line.TrailingPerformerIDs, expectedTrailingPerformerIDs) {
			return fmt.Errorf("public lyrics v2 line %d trailing performer assignment is stale", lineIndex+1)
		}
		var lineText strings.Builder
		for segmentIndex, segment := range line.Segments {
			sourceSegment := sourceLine.Segments[segmentIndex]
			if segment.Text != sourceSegment.Text || segment.Text == "" || len(segment.Text) > maxLyricsLineTextBytes ||
				segment.PerformerIDs == nil || len(segment.PerformerIDs) > maxLyricsPerformers ||
				segment.Ruby == nil || len(segment.Ruby) == 0 || len(segment.Ruby) > maxLyricsRubyPerSegment ||
				!publicLyricsRubyMatches(segment.Ruby, sourceSegment.Ruby) {
				return fmt.Errorf("public lyrics v2 line %d segment %d is invalid or stale", lineIndex+1, segmentIndex+1)
			}
			expectedPerformerIDs, err := publicLyricsSourceSegmentPerformerIDs(sourceSegment.PerformerIDs, sourcePerformers)
			if err != nil {
				return fmt.Errorf("public lyrics v2 line %d segment %d: %w", lineIndex+1, segmentIndex+1, err)
			}
			if document.Provenance.PerformerSegmentation == nil && len(expectedPerformerIDs) != 0 {
				return fmt.Errorf("public lyrics v2 line %d segment %d has unproven performer segmentation", lineIndex+1, segmentIndex+1)
			}
			if !reflect.DeepEqual(segment.PerformerIDs, expectedPerformerIDs) {
				return fmt.Errorf("public lyrics v2 line %d segment %d performer assignment is stale: it does not exactly match authoritative source IDs", lineIndex+1, segmentIndex+1)
			}
			var rubyText strings.Builder
			for rubyIndex, span := range segment.Ruby {
				if span.Text == "" || len(span.Text) > maxLyricsLineTextBytes || !utf8.ValidString(span.Text) ||
					len(span.Reading) > 1024 || !publicLyricsKanaReading(span.Reading) {
					return fmt.Errorf("public lyrics v2 line %d segment %d ruby span %d is invalid", lineIndex+1, segmentIndex+1, rubyIndex+1)
				}
				rubyText.WriteString(span.Text)
			}
			if rubyText.String() != segment.Text {
				return fmt.Errorf("public lyrics v2 line %d segment %d ruby spans do not concatenate exactly", lineIndex+1, segmentIndex+1)
			}
			lineText.WriteString(segment.Text)
		}
		if lineText.String() != line.Japanese {
			return fmt.Errorf("public lyrics v2 line %d segments do not concatenate exactly", lineIndex+1)
		}
	}
	expectedProjection := document.GameProjection
	if expectedProjection == nil {
		if public.GameProjection != nil {
			return errors.New("public lyrics v2 exposes a Game projection that is not allowed")
		}
	} else if public.GameProjection == nil || public.GameProjection.ReasonCode != document.ReasonCode ||
		!reflect.DeepEqual(public.GameProjection.LineIDs, expectedProjection.LineIDs) {
		return errors.New("public lyrics v2 Game projection does not match the source document")
	}
	return nil
}

func publicLyricsKanaReading(value string) bool {
	for _, current := range value {
		if current >= '\u3041' && current <= '\u3096' || current >= '\u30a1' && current <= '\u30fa' ||
			current == '\u30fc' || current == '\u30fb' || current == '\u3099' || current == '\u309a' {
			continue
		}
		return false
	}
	return true
}

func samePublicLyricsVersions(left, right []string) bool {
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
