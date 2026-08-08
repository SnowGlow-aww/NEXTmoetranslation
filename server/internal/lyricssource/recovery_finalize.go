package lyricssource

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"time"

	"moesekai/server/internal/model"
)

// historicalSekaipediaRubyGeneratorAlias is accepted only at the inbound
// compatibility boundary. Every returned or newly serialized value uses the
// canonical non-romaji vocabulary.
const historicalSekaipediaRubyGeneratorAlias = "sekaipedia-romaji-kana-v1"

// FinalizeRecoveryCandidate reparses one provider candidate exclusively from
// its already resolved exact evidence. It performs no HTTP request.
func (registry *Registry) FinalizeRecoveryCandidate(
	identity MusicIdentity,
	candidate Candidate,
) (FixedRevision, error) {
	if registry == nil || registry.providers[candidate.Provider] == nil {
		return FixedRevision{}, errors.New("recovery candidate provider is not configured")
	}
	switch provider := registry.providers[candidate.Provider].(type) {
	case *sekaipediaProvider:
		return provider.finalizeRecoveryCandidate(identity, candidate)
	case *moegirlProvider:
		return provider.finalizeRecoveryCandidate(identity, candidate)
	case *fandomProvider:
		return finalizeFandomRecoveryCandidate(identity, candidate)
	default:
		return FixedRevision{}, ErrMalformedResponse
	}
}

func (provider *sekaipediaProvider) finalizeRecoveryCandidate(
	identity MusicIdentity,
	candidate Candidate,
) (FixedRevision, error) {
	if len(provider.config.Indexes) != 1 ||
		!validSekaipediaCandidateForAuthority(candidate, provider.config.Indexes[0]) {
		return FixedRevision{}, ErrMalformedResponse
	}
	page, err := sekaipediaCandidateRevisionPage(candidate)
	if err != nil || !provider.catalogIdentityMatches(page.content, page.title, identity) {
		return FixedRevision{}, ErrAmbiguous
	}
	parsed, err := parseSekaipediaSong(page.content, identity.PerformerSegmentationPolicy)
	if err != nil {
		return FixedRevision{}, err
	}
	if parsed.Section != candidate.Section || parsed.RenditionKey != candidate.RenditionKey ||
		parsed.ReasonCode != candidate.VersionReason {
		return FixedRevision{}, ErrRevisionChanged
	}
	identities, document, err := buildSekaipediaDocument(candidate, parsed, page.fetchedAt)
	if err != nil {
		return FixedRevision{}, err
	}
	fixedWikitext := sekaipediaFixedJapaneseWikitext(parsed.Full.Lines)
	if len(fixedWikitext) == 0 {
		return FixedRevision{}, ErrMalformedResponse
	}
	return FixedRevision{
		Provider: ProviderSekaipedia, Origin: OriginSekaipedia,
		CanonicalURL: candidate.CanonicalURL, PageID: page.pageID, PageTitle: page.title,
		RevisionID: page.revisionID, RevisionTimestamp: page.revisionTimestamp, SHA1: page.sha1,
		RawSHA256: page.rawSHA256, Categories: cloneIdentityCategories(page.categories), FetchedAt: page.fetchedAt,
		Wikitext: fixedWikitext, Lines: legacyExtractedLines(parsed.Full.Lines), Extraction: parsed.Full,
		Section: parsed.Section, RenditionKey: parsed.RenditionKey, VersionReason: parsed.ReasonCode,
		IndexEvidenceRefs: cloneIndexEvidenceRefs(candidate.IndexEvidenceRefs),
		IndexEvidence:     cloneStrictIndexEvidence(candidate.IndexEvidence), FixedIdentities: identities, Document: document,
	}, nil
}

func (provider *moegirlProvider) finalizeRecoveryCandidate(
	identity MusicIdentity,
	candidate Candidate,
) (FixedRevision, error) {
	if !validMoegirlCandidate(candidate) {
		return FixedRevision{}, ErrMalformedResponse
	}
	page, fetchedAt, err := moegirlCandidateEvidencePage(candidate)
	if err != nil {
		return FixedRevision{}, err
	}
	anchor, ok := moegirlSectionAnchor(candidate.Section)
	if !ok {
		return FixedRevision{}, ErrMalformedResponse
	}
	section, sectionPath, err := moegirlTargetSection(page.content, anchor)
	if err != nil || sectionPath != candidate.Section || hasLyricsTextRestriction(section, page.categories) {
		return FixedRevision{}, ErrRevisionChanged
	}
	if !moegirlCatalogIdentityMatches(section, identity) {
		return FixedRevision{}, ErrAmbiguous
	}
	parsed, err := parseMoegirlSongSection(section, identity.PerformerSegmentationPolicy)
	if err != nil {
		return FixedRevision{}, err
	}
	if parsed.ReasonCode != candidate.VersionReason || moegirlRenditionKey(parsed) != candidate.RenditionKey ||
		parsed.ReasonCode == model.LyricsSourceVersionReasonVersionConflict {
		return FixedRevision{}, ErrRevisionChanged
	}
	var extraction Extraction
	var identities []model.LyricsSourceFixedIdentity
	var document *model.LyricsSourceDocument
	switch candidate.RenditionKey {
	case "full-sekai", "full-vocaloid":
		extraction = parsed.Full
		identities, document, err = buildMoegirlDocument(candidate, parsed, fetchedAt)
	case "game-sekai":
		extraction = parsed.Game
		identities, document, err = buildMoegirlDocument(candidate, parsed, fetchedAt)
	default:
		return FixedRevision{}, ErrMissingLyrics
	}
	if err != nil || len(extraction.Lines) == 0 {
		return FixedRevision{}, ErrMissingLyrics
	}
	return FixedRevision{
		Provider: ProviderMoegirl, Origin: OriginMoegirl,
		CanonicalURL: candidate.CanonicalURL, PageID: page.pageID, PageTitle: page.title,
		RevisionID: page.revisionID, SHA1: page.sha1, Categories: cloneIdentityCategories(page.categories),
		FetchedAt: fetchedAt, Wikitext: []byte(page.content), Lines: legacyExtractedLines(extraction.Lines),
		Extraction: extraction, Section: candidate.Section, RenditionKey: candidate.RenditionKey,
		VersionReason: parsed.ReasonCode, IndexEvidenceRefs: cloneIndexEvidenceRefs(candidate.IndexEvidenceRefs),
		IndexEvidence: cloneStrictIndexEvidence(candidate.IndexEvidence), FixedIdentities: identities, Document: document,
	}, nil
}

func moegirlCandidateEvidencePage(candidate Candidate) (wikiPage, time.Time, error) {
	matches := 0
	var result wikiPage
	var fetchedAt time.Time
	for _, evidence := range candidate.IndexEvidence {
		if evidence.Provider != ProviderMoegirl || evidence.Kind != IndexEvidenceKindMediaWikiRevision ||
			evidence.PageID != candidate.PageID || evidence.RevisionID != candidate.RevisionID ||
			evidence.Title != candidate.Title || evidence.MediaWikiSHA1 != candidate.SHA1 ||
			!equalCandidateCategories(evidence.Categories, candidate.Categories) {
			continue
		}
		parsedFetchedAt, err := time.Parse(time.RFC3339Nano, evidence.FetchedAt)
		if err != nil {
			return wikiPage{}, time.Time{}, ErrMalformedResponse
		}
		contentSHA1 := sha1.Sum(evidence.Raw)
		if hex.EncodeToString(contentSHA1[:]) != candidate.SHA1 {
			return wikiPage{}, time.Time{}, ErrRevisionChanged
		}
		result = wikiPage{
			pageID: candidate.PageID, title: candidate.Title, revisionID: candidate.RevisionID,
			sha1: candidate.SHA1, categories: cloneIdentityCategories(candidate.Categories),
			content: string(evidence.Raw), fetchedAt: parsedFetchedAt.UTC(),
		}
		fetchedAt = parsedFetchedAt.UTC()
		matches++
	}
	if matches != 1 {
		return wikiPage{}, time.Time{}, ErrMalformedResponse
	}
	return result, fetchedAt, nil
}

func finalizeFandomRecoveryCandidate(identity MusicIdentity, candidate Candidate) (FixedRevision, error) {
	if candidateProvider(candidate) != ProviderVocaloidFandom || !validFixedCandidate(candidate) {
		return FixedRevision{}, ErrMalformedResponse
	}
	page, fetchedAt, err := fandomCandidateEvidencePage(candidate)
	if err != nil {
		return FixedRevision{}, err
	}
	if hasLyricsTextRestriction(page.content, page.categories) || hasWrongEntityEvidence(page.categories) ||
		hasExcludedVersionSignal(page.title, page.content, page.categories) {
		return FixedRevision{}, ErrRestrictedReprint
	}
	extraction, err := extractCategoryAwareLyrics(page.content, page.categories)
	if err != nil {
		return FixedRevision{}, err
	}
	section, _, reason := fandomRenditionIdentityFromExtraction(extraction)
	extraction, err = applyPerformerSegmentationPolicy(identity, extraction)
	if err != nil {
		return FixedRevision{}, err
	}
	_, renditionKey, _ := fandomRenditionIdentityFromExtraction(extraction)
	if section != candidate.Section || renditionKey != candidate.RenditionKey || reason != candidate.VersionReason {
		return FixedRevision{}, ErrRevisionChanged
	}
	page.fetchedAt = fetchedAt
	fixedIdentities, document, err := buildFandomDocument(
		page, extraction, section, renditionKey, candidate.IndexEvidenceRefs, fetchedAt,
	)
	if err != nil {
		return FixedRevision{}, err
	}
	return FixedRevision{
		Provider: ProviderVocaloidFandom, Origin: OriginVocaloidFandom,
		CanonicalURL: candidate.CanonicalURL, PageID: page.pageID, PageTitle: page.title,
		RevisionID: page.revisionID, SHA1: page.sha1, Categories: cloneIdentityCategories(page.categories),
		FetchedAt: fetchedAt, Wikitext: []byte(page.content), Lines: legacyExtractedLines(extraction.Lines),
		Extraction: extraction, Section: section, RenditionKey: renditionKey, VersionReason: reason,
		IndexEvidenceRefs: cloneIndexEvidenceRefs(candidate.IndexEvidenceRefs),
		IndexEvidence:     cloneStrictIndexEvidence(candidate.IndexEvidence), FixedIdentities: fixedIdentities, Document: document,
	}, nil
}

// CanonicalPersistedRubyGeneratorVersion normalizes the sole immutable
// historical input alias. Unknown contemporary generator names are preserved
// for their owning provider boundary; only the retired alias is rewritten.
func CanonicalPersistedRubyGeneratorVersion(value string) string {
	if value == historicalSekaipediaRubyGeneratorAlias {
		return historicalSekaipediaRubyGeneratorVersion
	}
	return value
}

// RecoveryPersistedRubyGeneratorVersion maps parser-internal generator names
// to the closed persisted recovery vocabulary. No romanization-shaped token may
// cross the song-result boundary even when transient source alignment used it.
func RecoveryPersistedRubyGeneratorVersion(value string) (string, error) {
	value = CanonicalPersistedRubyGeneratorVersion(value)
	switch value {
	case "":
		return "", nil
	case historicalRubyGeneratorVersion:
		return historicalRubyGeneratorVersion, nil
	case rubyGeneratorVersion:
		return rubyGeneratorVersion, nil
	case historicalSekaipediaRubyGeneratorVersion:
		return historicalSekaipediaRubyGeneratorVersion, nil
	case sekaipediaRubyGeneratorVersion:
		return sekaipediaRubyGeneratorVersion, nil
	default:
		return "", errors.New("recovery ruby generator version is not registered")
	}
}

func fandomCandidateEvidencePage(candidate Candidate) (wikiPage, time.Time, error) {
	matches := 0
	var result wikiPage
	var fetchedAt time.Time
	for _, evidence := range candidate.IndexEvidence {
		if evidence.Provider != ProviderVocaloidFandom || evidence.Kind != IndexEvidenceKindMediaWikiSearchResponse {
			continue
		}
		pages, err := parseSearchResponse(evidence.Raw)
		parsedFetchedAt, fetchedErr := time.Parse(time.RFC3339Nano, evidence.FetchedAt)
		if err != nil || fetchedErr != nil {
			return wikiPage{}, time.Time{}, ErrMalformedResponse
		}
		for _, page := range pages {
			if page.pageID == candidate.PageID && page.revisionID == candidate.RevisionID &&
				page.sha1 == candidate.SHA1 && page.title == candidate.Title &&
				equalCandidateCategories(page.categories, candidate.Categories) {
				result = page
				fetchedAt = parsedFetchedAt.UTC()
				matches++
			}
		}
	}
	if matches != 1 {
		return wikiPage{}, time.Time{}, ErrMalformedResponse
	}
	contentSHA1 := sha1.Sum([]byte(result.content))
	if hex.EncodeToString(contentSHA1[:]) != candidate.SHA1 {
		return wikiPage{}, time.Time{}, ErrRevisionChanged
	}
	return result, fetchedAt, nil
}
