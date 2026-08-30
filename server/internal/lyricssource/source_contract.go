package lyricssource

import (
	"fmt"
	"time"

	"moesekai/server/internal/model"
)

func fandomRenditionIdentity(
	identity MusicIdentity,
	content string,
	categories []string,
) (string, string, model.LyricsSourceVersionReasonCode, error) {
	extraction, err := extractCategoryAwareLyrics(content, categories)
	if err != nil {
		if identity.PerformerSegmentationPolicy == PerformerSegmentationDisabled {
			return "", "", "", err
		}
		extraction = Extraction{Version: LyricsVersion{Kind: "original", Label: "Original Version"}}
	}
	section, _, reason := fandomRenditionIdentityFromExtraction(extraction)
	policyExtraction, err := applyPerformerSegmentationPolicy(identity, extraction)
	if err != nil {
		return "", "", "", err
	}
	_, renditionKey, _ := fandomRenditionIdentityFromExtraction(policyExtraction)
	return section, renditionKey, reason, nil
}

func fandomRenditionIdentityFromExtraction(extraction Extraction) (string, string, model.LyricsSourceVersionReasonCode) {
	kind := extraction.Version.Kind
	if kind != "original" && kind != "sekai" && kind != "vocaloid" {
		kind = "original"
	}
	section := "Lyrics"
	if extraction.Version.Label != "" && extraction.Version.Label != "Original Version" {
		section += "/" + extraction.Version.Label
	}
	return section, "full-" + kind, model.LyricsSourceVersionReasonUntaggedFullOnly
}

func buildFandomDocument(
	page wikiPage,
	extraction Extraction,
	section, renditionKey string,
	references []model.LyricsSourceIndexEvidenceRef,
	fetchedAt time.Time,
) ([]model.LyricsSourceFixedIdentity, *model.LyricsSourceDocument, error) {
	identity := model.LyricsSourceFixedIdentity{
		Provider: ProviderVocaloidFandom, Origin: OriginVocaloidFandom,
		PageID: page.pageID, RevisionID: page.revisionID, SHA1: page.sha1, Title: page.title,
		CanonicalURL: canonicalRevisionURL(ProviderVocaloidFandom, page.title, page.revisionID),
		FetchedAt:    canonicalFetchedAt(fetchedAt), Categories: append([]string{}, page.categories...),
		Section: section, RenditionKey: renditionKey, IndexEvidenceRefs: cloneIndexEvidenceRefs(references),
	}
	ref := model.LyricsSourceComponentRef{RenditionKey: renditionKey}
	provenance := model.LyricsSourceComponentProvenance{FullText: ref, VersionEvidence: ref}
	persistedExtraction, hasSegmentation := extractionForSourceDocument(extraction, false)
	if hasSegmentation {
		copyRef := ref
		provenance.PerformerSegmentation = &copyRef
	}
	if hasExtractionRubyReading(persistedExtraction) {
		copyRef := ref
		provenance.Ruby = &copyRef
	}
	document := &model.LyricsSourceDocument{
		SchemaVersion:   model.LyricsSourceDocumentSchemaVersion,
		ReasonCode:      model.LyricsSourceVersionReasonUntaggedFullOnly,
		FixedIdentities: []model.LyricsSourceFixedIdentity{identity}, Provenance: provenance,
		Full: extractionToModelFull(persistedExtraction),
	}
	if err := model.ValidateLyricsSourceDocument(*document); err != nil {
		return nil, nil, fmt.Errorf("validate vocaloid fandom lyrics source document: %w", err)
	}
	return []model.LyricsSourceFixedIdentity{identity}, document, nil
}

func buildMoegirlFixedIdentity(candidate Candidate, renditionKey string, fetchedAt time.Time) (model.LyricsSourceFixedIdentity, error) {
	identity := model.LyricsSourceFixedIdentity{
		Provider: ProviderMoegirl, Origin: OriginMoegirl,
		PageID: candidate.PageID, RevisionID: candidate.RevisionID, SHA1: candidate.SHA1,
		Title: candidate.Title, CanonicalURL: candidate.CanonicalURL, FetchedAt: canonicalFetchedAt(fetchedAt),
		Categories: append([]string{}, candidate.Categories...), Section: candidate.Section, RenditionKey: renditionKey,
		IndexEvidenceRefs: cloneIndexEvidenceRefs(candidate.IndexEvidenceRefs),
	}
	if err := model.ValidateLyricsSourceFixedIdentity(identity); err != nil {
		return model.LyricsSourceFixedIdentity{}, fmt.Errorf("validate moegirl fixed identity: %w", err)
	}
	return identity, nil
}

func buildMoegirlDocument(candidate Candidate, parsed MoegirlSectionExtraction, fetchedAt time.Time) ([]model.LyricsSourceFixedIdentity, *model.LyricsSourceDocument, error) {
	gameOnly := len(parsed.Full.Lines) == 0 && len(parsed.Game.Lines) > 0
	if (!gameOnly && (len(parsed.Full.Lines) == 0 || candidate.RenditionKey != fullRenditionKey(parsed.Full.Version.Kind))) ||
		(gameOnly && candidate.RenditionKey != "game-sekai") {
		return nil, nil, ErrMissingLyrics
	}
	identity, err := buildMoegirlFixedIdentity(candidate, candidate.RenditionKey, fetchedAt)
	if err != nil {
		return nil, nil, err
	}
	identities := []model.LyricsSourceFixedIdentity{identity}
	ref := model.LyricsSourceComponentRef{RenditionKey: candidate.RenditionKey}
	provenance := model.LyricsSourceComponentProvenance{VersionEvidence: ref}
	var full model.LyricsSourceFull
	var game *model.LyricsSourceFull

	if gameOnly {
		persistedGame, hasSegmentation := extractionForSourceDocument(
			parsed.Game, parsed.Game.Version.Kind != "vocaloid",
		)
		gameValue := extractionToModelFull(persistedGame)
		for index := range gameValue.Lines {
			gameValue.Lines[index].ID = fmt.Sprintf("game-%06d", index+1)
		}
		game = &gameValue
		provenance.GameText = &ref
		if hasSegmentation {
			segmentationRef := ref
			provenance.PerformerSegmentation = &segmentationRef
		}
		if hasExtractionRubyReading(persistedGame) {
			rubyRef := ref
			provenance.Ruby = &rubyRef
		}
	} else {
		persistedFull, hasSegmentation := extractionForSourceDocument(
			parsed.Full, parsed.Full.Version.Kind != "vocaloid",
		)
		full = extractionToModelFull(persistedFull)
		provenance.FullText = ref
		if hasSegmentation {
			segmentationRef := ref
			provenance.PerformerSegmentation = &segmentationRef
		}
		if hasExtractionRubyReading(persistedFull) {
			rubyRef := ref
			provenance.Ruby = &rubyRef
		}
		if parsed.ReasonCode == model.LyricsSourceVersionReasonTaggedFullAndGame && len(parsed.Game.Lines) > 0 {
			gameKey := "game-sekai"
			gameIdentity := identity
			gameIdentity.RenditionKey = gameKey
			identities = append(identities, gameIdentity)
			persistedGame, _ := extractionForSourceDocument(
				parsed.Game, parsed.Game.Version.Kind != "vocaloid",
			)
			gameValue := extractionToModelFull(persistedGame)
			for index := range gameValue.Lines {
				gameValue.Lines[index].ID = fmt.Sprintf("game-%06d", index+1)
			}
			game = &gameValue
			gameRef := model.LyricsSourceComponentRef{RenditionKey: gameKey}
			provenance.GameText = &gameRef
		}
	}

	var gameProjection *model.LyricsSourceGameProjection
	if len(parsed.GameLineIndexes) > 0 {
		if gameOnly || parsed.Full.Version.Kind != "sekai" || len(full.Lines) == 0 {
			return nil, nil, ErrUnsupportedTable
		}
		lineIDs := make([]string, len(parsed.GameLineIndexes))
		for index, position := range parsed.GameLineIndexes {
			if position < 0 || position >= len(full.Lines) {
				return nil, nil, ErrUnsupportedTable
			}
			lineIDs[index] = full.Lines[position].ID
		}
		gameProjection = &model.LyricsSourceGameProjection{LineIDs: lineIDs}
		projectionKey := candidate.RenditionKey
		if game != nil {
			projectionKey = "game-sekai"
		}
		projectionRef := model.LyricsSourceComponentRef{RenditionKey: projectionKey}
		provenance.GameProjection = &projectionRef
	}
	document := &model.LyricsSourceDocument{
		SchemaVersion: model.LyricsSourceDocumentSchemaVersion, ReasonCode: parsed.ReasonCode,
		FixedIdentities: identities, Provenance: provenance, Full: full, Game: game,
		GameProjection: gameProjection,
	}
	if err := model.ValidateLyricsSourceDocument(*document); err != nil {
		return nil, nil, fmt.Errorf("validate moegirl lyrics source document: %w", err)
	}
	return identities, document, nil
}

func extractionToModelFull(extraction Extraction) model.LyricsSourceFull {
	performers := make([]model.LyricsSourcePerformer, len(extraction.Performers))
	for index, performer := range extraction.Performers {
		performers[index] = model.LyricsSourcePerformer{
			PerformerID: performer.PerformerID, Name: performer.Name, Color: performer.Color,
		}
	}
	lines := make([]model.LyricsSourceExtractedLine, len(extraction.Lines))
	for lineIndex, line := range extraction.Lines {
		segments := make([]model.LyricsSourceSegment, len(line.Segments))
		for segmentIndex, segment := range line.Segments {
			ruby := make([]model.LyricsSourceRubySpan, len(segment.Ruby))
			for rubyIndex, span := range segment.Ruby {
				ruby[rubyIndex] = model.LyricsSourceRubySpan{Text: span.Text, Reading: span.Reading}
			}
			segments[segmentIndex] = model.LyricsSourceSegment{
				Text: segment.Text, PerformerIDs: append([]string{}, segment.PerformerIDs...), Ruby: ruby,
			}
		}
		lines[lineIndex] = model.LyricsSourceExtractedLine{
			Japanese: line.Japanese, StanzaBreakBefore: line.StanzaBreakBefore,
			Segments: segments, TrailingPerformerIDs: append([]string{}, line.TrailingPerformerIDs...),
		}
	}
	return model.NewLyricsSourceFullFromLegacy(
		model.LyricsSourceVersion{Kind: extraction.Version.Kind, Label: extraction.Version.Label},
		performers, extraction.RubyGeneratorVersion, lines,
	)
}

func extractionToModelFullV3(
	extraction Extraction,
	fixedIdentityKey string,
	renditionKey string,
	side model.LyricsSourceRenditionSide,
) model.LyricsSourceFull {
	full := extractionToModelFull(extraction)
	for lineIndex, line := range extraction.Lines {
		for segmentIndex, segment := range line.Segments {
			for spanIndex, span := range segment.Ruby {
				if span.Reading == "" || span.ReadingEvidenceKind == "" {
					continue
				}
				evidence := model.LyricsSourceReadingEvidence{Kind: span.ReadingEvidenceKind}
				switch span.ReadingEvidenceKind {
				case model.LyricsSourceReadingEvidenceExplicitSourceKana,
					model.LyricsSourceReadingEvidenceSourceTransliteration:
					evidence.FixedIdentityKey = fixedIdentityKey
					evidence.RenditionKey = renditionKey
					evidence.Side = side
					evidence.SourceRowOrdinal = span.SourceRowOrdinal
					evidence.SourceSegmentOrdinal = span.SourceSegmentOrdinal
				case model.LyricsSourceReadingEvidenceDeterministicDictionary,
					model.LyricsSourceReadingEvidenceFixedReviewedToken:
					evidence.GeneratorVersion = span.GeneratorVersion
				}
				full.Lines[lineIndex].Segments[segmentIndex].Ruby[spanIndex].ReadingEvidence = &evidence
			}
		}
	}
	return full
}

func hasExtractionPerformerEvidence(extraction Extraction) bool {
	return extractionHasPerformerSegmentation(extraction)
}

// extractionForSourceDocument keeps Full-tab existence independent from the
// optional performer segmentation component. When the provider and rendition
// policy authorize partial source attribution, preserve exactly the tagged
// segments and leave only genuinely untagged spans anonymous. Callers that do
// not have that authority can still request the conservative flattened view.
func extractionForSourceDocument(extraction Extraction, preservePartial bool) (Extraction, bool) {
	if extractionHasCompletePerformerSegmentation(extraction) ||
		preservePartial && extractionHasPerformerSegmentation(extraction) {
		return extraction, true
	}
	result := extraction
	result.Performers = []Performer{}
	result.Lines = make([]StructuredLine, len(extraction.Lines))
	for index, line := range extraction.Lines {
		var ruby []RubySpan
		for _, segment := range line.Segments {
			ruby = append(ruby, segment.Ruby...)
		}
		result.Lines[index] = StructuredLine{
			Japanese:          line.Japanese,
			StanzaBreakBefore: line.StanzaBreakBefore,
			Segments: []LyricsSegment{{
				Text: line.Japanese, PerformerIDs: []string{}, Ruby: append([]RubySpan{}, ruby...),
			}},
			TrailingPerformerIDs: []string{},
		}
	}
	return result, false
}

func extractionHasPerformerSegmentation(extraction Extraction) bool {
	if len(extraction.Performers) == 0 || len(extraction.Lines) == 0 {
		return false
	}
	for _, line := range extraction.Lines {
		if len(line.TrailingPerformerIDs) != 0 {
			return true
		}
		for _, segment := range line.Segments {
			if len(segment.PerformerIDs) != 0 {
				return true
			}
		}
	}
	return false
}

func extractionHasCompletePerformerSegmentation(extraction Extraction) bool {
	if len(extraction.Performers) == 0 || len(extraction.Lines) == 0 {
		return false
	}
	for _, line := range extraction.Lines {
		if len(line.Segments) == 0 || len(line.TrailingPerformerIDs) != 0 {
			// A trailing attribution is source evidence, but without a complete
			// inline assignment it is not sufficient to claim complete segmentation.
			return false
		}
		for _, segment := range line.Segments {
			if len(segment.PerformerIDs) == 0 {
				return false
			}
		}
	}
	return true
}

func hasExtractionSourceRubyReading(extraction Extraction) bool {
	for _, line := range extraction.Lines {
		for _, segment := range line.Segments {
			for _, span := range segment.Ruby {
				switch span.ReadingEvidenceKind {
				case model.LyricsSourceReadingEvidenceExplicitSourceKana,
					model.LyricsSourceReadingEvidenceSourceTransliteration:
					return true
				}
			}
		}
	}
	return false
}

func hasExtractionRubyReading(extraction Extraction) bool {
	for _, line := range extraction.Lines {
		for _, segment := range line.Segments {
			for _, span := range segment.Ruby {
				if span.Reading != "" {
					return true
				}
			}
		}
	}
	return false
}

func cloneIndexEvidenceRefs(input []model.LyricsSourceIndexEvidenceRef) []model.LyricsSourceIndexEvidenceRef {
	if input == nil {
		return nil
	}
	return append([]model.LyricsSourceIndexEvidenceRef{}, input...)
}
