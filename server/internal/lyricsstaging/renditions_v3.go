package lyricsstaging

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"moesekai/server/internal/lyricscompose"
	"moesekai/server/internal/lyricssource"
	"moesekai/server/internal/model"
)

// BuildRecoveryPeerDraft preserves the complete source-v3 rendition array in
// the staging handoff. It deliberately leaves the legacy singular extraction
// projection empty; store import may create that projection only after the
// explicit v3-to-v2 lossless compatibility predicate succeeds.
func BuildRecoveryPeerDraft(
	musicID int,
	japaneseTitle string,
	catalogFingerprint string,
	targetMusicID int,
	associationMusicIDs []int,
	document model.LyricsSourceDocument,
	artifacts []Artifact,
	translations []RenditionTranslation,
) (Draft, error) {
	if musicID <= 0 || targetMusicID != musicID || strings.TrimSpace(japaneseTitle) == "" ||
		!canonicalSHA256.MatchString(catalogFingerprint) {
		return Draft{}, errors.New("recovery peer draft catalog identity is invalid")
	}
	associations, err := sortedUniqueInts(associationMusicIDs)
	if err != nil {
		return Draft{}, fmt.Errorf("music %d associations: %w", musicID, err)
	}
	if document.SchemaVersion != model.LyricsSourceDocumentSchemaVersionV3 {
		return Draft{}, errors.New("recovery peer draft requires a source v3 document")
	}
	if err := validateCanonicalV3RenditionOrder(document.Renditions); err != nil {
		return Draft{}, err
	}
	canonicalArtifacts := append([]Artifact(nil), artifacts...)
	sort.Slice(canonicalArtifacts, func(left, right int) bool {
		return canonicalArtifacts[left].Identity.RenditionKey < canonicalArtifacts[right].Identity.RenditionKey
	})
	if len(canonicalArtifacts) == 0 {
		return Draft{}, errors.New("recovery peer draft requires source artifacts")
	}
	document, err = canonicalizeStagedDocument(document)
	if err != nil {
		return Draft{}, err
	}
	if err := model.ValidateLyricsSourceDocument(document); err != nil {
		return Draft{}, fmt.Errorf("music %d recovery peer source document: %w", musicID, err)
	}
	for _, artifact := range canonicalArtifacts {
		if err := ValidateRecoveryArtifact(artifact); err != nil {
			return Draft{}, err
		}
	}
	translations = cloneRenditionTranslations(translations)
	if err := validateRenditionTranslations(musicID, document.Renditions, translations); err != nil {
		return Draft{}, err
	}
	documentSHA, err := lyricsSourceDocumentDigest(document)
	if err != nil {
		return Draft{}, err
	}
	if err := validateV3ArtifactsForDocument(document, canonicalArtifacts); err != nil {
		return Draft{}, err
	}
	source := fixedSourceFromArtifact(canonicalArtifacts[0])
	draft := Draft{
		MusicID: musicID, JapaneseTitle: japaneseTitle, CatalogFingerprint: catalogFingerprint,
		TargetMusicID: targetMusicID, AssociationMusicIDs: associations, Source: source,
		Artifacts: canonicalArtifacts, Document: document, DocumentSHA256: documentSHA,
		RenditionTranslations: translations,
	}
	if translations == nil {
		draft.RenditionTranslations = nil
	}
	digest, err := draftDigest(draft)
	if err != nil {
		return Draft{}, err
	}
	draft.DraftSHA256 = digest
	if err := ValidateDraft(draft); err != nil {
		return Draft{}, err
	}
	return draft, nil
}

func fixedSourceFromArtifact(artifact Artifact) FixedSource {
	return FixedSource{
		PageID: artifact.Identity.PageID, RevisionID: artifact.Identity.RevisionID,
		SHA1: artifact.Identity.SHA1, PageTitle: artifact.Identity.Title,
		CanonicalURL:         artifact.Identity.CanonicalURL,
		Categories:           append([]string{}, artifact.Identity.Categories...),
		FetchedAt:            artifact.Identity.FetchedAt,
		RawWikitextByteCount: artifact.RawWikitextByteCount,
		RawWikitextSHA256:    artifact.RawWikitextSHA256,
	}
}

func validateV3DraftPublicFields(draft Draft) error {
	if draft.SelectedVersion != (model.LyricsSourceVersion{}) || draft.Performers != nil || draft.Lines != nil ||
		draft.Translations != nil || draft.ExtractedLinesSHA256 != "" {
		return fmt.Errorf("staged music %d v3 draft leaked legacy singular extraction fields", draft.MusicID)
	}
	if err := validateV3FullMetadata(draft.Document); err != nil {
		return fmt.Errorf("staged music %d source document metadata: %w", draft.MusicID, err)
	}
	return nil
}

func cloneRenditionTranslations(input []RenditionTranslation) []RenditionTranslation {
	if input == nil {
		return nil
	}
	result := make([]RenditionTranslation, len(input))
	for index, item := range input {
		result[index] = item
		result[index].Translations = append([]string(nil), item.Translations...)
		if item.Translations == nil {
			result[index].Translations = nil
		}
	}
	return result
}

func renditionTranslationMap(input []RenditionTranslation) map[string]RenditionTranslation {
	result := make(map[string]RenditionTranslation, len(input))
	for _, item := range input {
		result[item.RenditionKey] = item
	}
	return result
}

func validateCanonicalV3RenditionOrder(renditions []model.LyricsSourceRendition) error {
	lastKey := ""
	for _, rendition := range renditions {
		if lastKey != "" && rendition.RenditionKey <= lastKey {
			return errors.New("source v3 renditions are not strictly ordered")
		}
		lastKey = rendition.RenditionKey
	}
	return nil
}

func renditionLineCount(rendition model.LyricsSourceRendition) int {
	if rendition.Full != nil {
		return len(rendition.Full.Lines)
	}
	if rendition.Game != nil {
		return len(rendition.Game.Lines)
	}
	return 0
}

func validateRenditionTranslations(musicID int, renditions []model.LyricsSourceRendition, translations []RenditionTranslation) error {
	if translations == nil {
		return nil
	}
	if len(translations) == 0 || len(translations) != len(renditions) {
		return fmt.Errorf("staged music %d rendition translations must cover the complete rendition array", musicID)
	}
	renditionByKey := make(map[string]model.LyricsSourceRendition, len(renditions))
	for _, rendition := range renditions {
		renditionByKey[rendition.RenditionKey] = rendition
	}
	lastKey := ""
	seen := make(map[string]struct{}, len(translations))
	for _, item := range translations {
		if item.RenditionKey == "" || item.RenditionKey <= lastKey {
			return fmt.Errorf("staged music %d rendition translations are not canonically ordered", musicID)
		}
		lastKey = item.RenditionKey
		if _, duplicate := seen[item.RenditionKey]; duplicate {
			return fmt.Errorf("staged music %d repeats rendition translation %q", musicID, item.RenditionKey)
		}
		rendition, found := renditionByKey[item.RenditionKey]
		if !found {
			return fmt.Errorf("staged music %d rendition translation %q has no source rendition", musicID, item.RenditionKey)
		}
		seen[item.RenditionKey] = struct{}{}
		if item.TranslationCredit != strings.TrimSpace(item.TranslationCredit) ||
			item.ProofreadingCredit != strings.TrimSpace(item.ProofreadingCredit) ||
			len(item.TranslationCredit) > 2048 || len(item.ProofreadingCredit) > 2048 ||
			!utf8.ValidString(item.TranslationCredit) || !utf8.ValidString(item.ProofreadingCredit) {
			return fmt.Errorf("staged music %d rendition %q credits are invalid", musicID, item.RenditionKey)
		}
		lineCount := renditionLineCount(rendition)
		if item.Translations != nil {
			if len(item.Translations) == 0 || len(item.Translations) != lineCount {
				return fmt.Errorf("staged music %d rendition %q translations do not align with Japanese lines", musicID, item.RenditionKey)
			}
			total := 0
			for lineIndex, text := range item.Translations {
				if text == "" || text != strings.TrimSpace(text) || !utf8.ValidString(text) ||
					strings.ContainsAny(text, "\r\n\x00") || len(text) > 16<<10 {
					return fmt.Errorf("staged music %d rendition %q translation %d is invalid", musicID, item.RenditionKey, lineIndex+1)
				}
				total += len(text)
				if total > 2<<20 {
					return fmt.Errorf("staged music %d rendition %q translations exceed the text boundary", musicID, item.RenditionKey)
				}
			}
		} else if item.TranslationCredit != "" || item.ProofreadingCredit != "" {
			return fmt.Errorf("staged music %d rendition %q credits exist without translations", musicID, item.RenditionKey)
		}
	}
	for _, rendition := range renditions {
		if _, found := seen[rendition.RenditionKey]; !found {
			return fmt.Errorf("staged music %d rendition %q has no translation record", musicID, rendition.RenditionKey)
		}
	}
	return nil
}

func validateV3ArtifactsForDocument(document model.LyricsSourceDocument, artifacts []Artifact) error {
	bindings, err := model.EnumerateLyricsSourceRenditionComponents(document.Renditions)
	if err != nil {
		return err
	}
	if len(artifacts) != len(document.FixedIdentities) {
		return errors.New("source v3 artifacts do not match fixed identities")
	}
	byKey := make(map[string]Artifact, len(artifacts))
	lastKey := ""
	for index, artifact := range artifacts {
		if err := ValidateRecoveryArtifact(artifact); err != nil {
			return fmt.Errorf("source v3 artifact %d: %w", index+1, err)
		}
		key := artifact.Identity.RenditionKey
		if key <= lastKey {
			return errors.New("source v3 artifacts are not strictly ordered")
		}
		lastKey = key
		if _, duplicate := byKey[key]; duplicate {
			return errors.New("source v3 repeats an artifact identity")
		}
		if !equalFixedIdentity(artifact.Identity, document.FixedIdentities[index]) {
			return fmt.Errorf("source v3 fixed identity %q has no exact ordered artifact", document.FixedIdentities[index].RenditionKey)
		}
		byKey[key] = artifact
	}
	contributing := make(map[string]bool, len(bindings))
	for _, binding := range bindings {
		artifact, found := byKey[binding.FixedIdentityKey]
		if !found {
			return fmt.Errorf("source v3 component %q has no artifact", binding.ComponentKey)
		}
		if model.LyricsSourceCompositionRenditionKey(artifact.Identity) != binding.RenditionKey {
			return fmt.Errorf("source v3 component %q crosses logical rendition families", binding.ComponentKey)
		}
		contributing[binding.FixedIdentityKey] = true
	}
	for key := range byKey {
		if !contributing[key] {
			return fmt.Errorf("source v3 artifact %q has no component contribution", key)
		}
	}
	return nil
}

func validateV3DraftProvenance(draft Draft) error {
	if err := validateCanonicalV3RenditionOrder(draft.Document.Renditions); err != nil {
		return fmt.Errorf("staged music %d: %w", draft.MusicID, err)
	}
	if err := validateV3ArtifactsForDocument(draft.Document, draft.Artifacts); err != nil {
		return fmt.Errorf("staged music %d: %w", draft.MusicID, err)
	}
	documentDigest, err := lyricsSourceDocumentDigest(draft.Document)
	if err != nil || !canonicalSHA256.MatchString(draft.DocumentSHA256) || documentDigest != draft.DocumentSHA256 {
		return fmt.Errorf("staged music %d source-document digest does not match", draft.MusicID)
	}
	first := draft.Artifacts[0]
	identity := first.Identity
	if draft.Source.PageID != identity.PageID || draft.Source.RevisionID != identity.RevisionID ||
		draft.Source.SHA1 != identity.SHA1 || draft.Source.PageTitle != identity.Title ||
		draft.Source.CanonicalURL != identity.CanonicalURL || draft.Source.FetchedAt != identity.FetchedAt ||
		!equalStrings(draft.Source.Categories, identity.Categories) ||
		draft.Source.RawWikitextByteCount != first.RawWikitextByteCount ||
		draft.Source.RawWikitextSHA256 != first.RawWikitextSHA256 {
		return fmt.Errorf("staged music %d source projection differs from the first canonical artifact", draft.MusicID)
	}
	if err := validateRenditionTranslations(draft.MusicID, draft.Document.Renditions, draft.RenditionTranslations); err != nil {
		return err
	}
	return nil
}

func v3DocumentComponentRefs(document model.LyricsSourceDocument) (map[string]string, error) {
	bindings, err := model.EnumerateLyricsSourceRenditionComponents(document.Renditions)
	if err != nil {
		return nil, err
	}
	refs := make(map[string]string, len(bindings))
	for _, binding := range bindings {
		refs[binding.ComponentKey] = binding.FixedIdentityKey
	}
	return refs, nil
}

func validateV3FullMetadata(document model.LyricsSourceDocument) error {
	for _, rendition := range document.Renditions {
		for _, full := range []*model.LyricsSourceFull{rendition.Full, rendition.Game} {
			if full == nil {
				continue
			}
			if err := lyricscompose.ValidatePersistedPerformerMetadata(*full); err != nil {
				return err
			}
			canonical, err := lyricssource.RecoveryPersistedRubyGeneratorVersion(full.RubyGeneratorVersion)
			if err != nil || canonical != full.RubyGeneratorVersion {
				return errors.New("unsafe persisted lyrics ruby generator metadata")
			}
		}
	}
	return nil
}
