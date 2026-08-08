package lyricssource

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/url"

	"strings"
	"time"
	"unicode/utf8"

	"moesekai/server/internal/model"
)

// ValidateIndexEvidenceEnvelope independently validates one exact immutable
// evidence acquisition, including its bounded raw bytes, digest, provider
// identity, and provider-specific acquisition ID.
func ValidateIndexEvidenceEnvelope(evidence IndexEvidence) error {
	return validateIndexEvidence(evidence)
}

// CanonicalizeLegacyPersistedMediaWikiRevisionEvidence validates one historical
// Fandom or Moegirl revision envelope whose persisted evidence ID predates the
// acquisition suffix. It returns an in-memory copy with the current canonical
// acquisition ID so backup validation can reuse the strict candidate validator.
//
// This compatibility path is intentionally unsuitable for new acquisition,
// staging, provider outcomes, or evidence packs: callers must preserve the
// historical row unchanged and may use the returned copy only for validation.
func CanonicalizeLegacyPersistedMediaWikiRevisionEvidence(evidence IndexEvidence) (IndexEvidence, error) {
	if evidence.Kind != IndexEvidenceKindMediaWikiRevision ||
		(evidence.Provider != ProviderVocaloidFandom && evidence.Provider != ProviderMoegirl) {
		return IndexEvidence{}, errors.New("legacy persisted evidence must be a Fandom or Moegirl MediaWiki revision")
	}
	baseID := mediaWikiRevisionEvidenceBaseID(evidence)
	if baseID == "" || evidence.EvidenceID != baseID {
		return IndexEvidence{}, errors.New("legacy persisted MediaWiki revision evidence ID is invalid")
	}
	canonical := evidence
	canonical.Categories = append([]string{}, evidence.Categories...)
	canonical.Raw = append([]byte(nil), evidence.Raw...)
	canonical.EvidenceID = mediaWikiRevisionAcquisitionEvidenceID(
		canonical.Provider,
		baseID,
		canonical.FetchedAt,
		canonical.RawSHA256,
	)
	if canonical.EvidenceID == "" {
		return IndexEvidence{}, errors.New("legacy persisted MediaWiki revision evidence cannot derive an acquisition ID")
	}
	if err := validateIndexEvidence(canonical); err != nil {
		return IndexEvidence{}, fmt.Errorf("validate legacy persisted MediaWiki revision evidence: %w", err)
	}
	return canonical, nil
}

type validatedIndexEvidenceBinding struct {
	searchIdentities    []candidateIndexEvidenceIdentity
	sekaipediaRawSHA256 string
	sekaipediaAuthority *FixedIndex
}

func validateIndexEvidence(evidence IndexEvidence) error {
	_, err := validateIndexEvidenceWithBinding(evidence)
	return err
}

func validateIndexEvidenceWithBinding(evidence IndexEvidence) (validatedIndexEvidenceBinding, error) {
	var binding validatedIndexEvidenceBinding
	if len(evidence.Raw) == 0 || len(evidence.Raw) > MaxIndexEvidenceRawBytes || !utf8.Valid(evidence.Raw) ||
		!canonicalIndexEvidenceID.MatchString(evidence.EvidenceID) ||
		!canonicalIndexEvidenceSHA256.MatchString(evidence.SHA256) ||
		!canonicalIndexEvidenceSHA256.MatchString(evidence.RawSHA256) || evidence.SHA256 != evidence.RawSHA256 {
		return binding, errors.New("index evidence bytes or digest are invalid")
	}
	digest := sha256.Sum256(evidence.Raw)
	if evidence.RawSHA256 != fmt.Sprintf("%x", digest) {
		return binding, errors.New("index evidence digest does not bind exact raw bytes")
	}
	fetchedAt, err := time.Parse(time.RFC3339Nano, evidence.FetchedAt)
	if err != nil || evidence.FetchedAt == "" || !strings.HasSuffix(evidence.FetchedAt, "Z") ||
		fetchedAt.Unix() <= 0 || fetchedAt.UTC().Format(time.RFC3339Nano) != evidence.FetchedAt {
		return binding, errors.New("index evidence fetchedAt is not canonical")
	}
	if !model.IsValidLyricsSourceProvider(evidence.Provider) {
		return binding, errors.New("index evidence provider is invalid")
	}
	wantOrigin := OriginVocaloidFandom
	switch evidence.Provider {
	case ProviderMoegirl:
		wantOrigin = OriginMoegirl
	case ProviderMoegirlPublicExact:
		wantOrigin = OriginMoegirlPublicExact
	case ProviderSekaipedia:
		wantOrigin = OriginSekaipedia
	}
	if evidence.Origin != wantOrigin {
		return binding, errors.New("index evidence origin is invalid")
	}

	switch evidence.Kind {
	case IndexEvidenceKindMediaWikiRevision:
		if evidence.PageID <= 0 || evidence.RevisionID <= 0 || !HasCanonicalSHA1(evidence.MediaWikiSHA1) ||
			evidence.Title == "" || strings.TrimSpace(evidence.Title) != evidence.Title ||
			evidence.CanonicalURL != canonicalRevisionURL(evidence.Provider, evidence.Title, evidence.RevisionID) ||
			evidence.CanonicalRequestURL != "" || evidence.Categories == nil {
			return binding, errors.New("MediaWiki revision evidence identity is invalid")
		}
		for index, category := range evidence.Categories {
			if category == "" || strings.TrimSpace(category) != category ||
				(index > 0 && evidence.Categories[index-1] >= category) {
				return binding, errors.New("MediaWiki revision evidence categories are not canonical")
			}
		}
		expectedEvidenceID := mediaWikiRevisionAcquisitionEvidenceID(
			evidence.Provider,
			mediaWikiRevisionEvidenceBaseID(evidence),
			evidence.FetchedAt,
			evidence.RawSHA256,
		)
		if expectedEvidenceID == "" || evidence.EvidenceID != expectedEvidenceID {
			return binding, errors.New("MediaWiki revision evidence acquisition identity is invalid")
		}
		if evidence.Provider == ProviderSekaipedia {
			revisionTimestamp, err := time.Parse(time.RFC3339Nano, evidence.RevisionTimestamp)
			if err != nil || evidence.RevisionTimestamp == "" || !strings.HasSuffix(evidence.RevisionTimestamp, "Z") ||
				revisionTimestamp.UTC().Format(time.RFC3339Nano) != evidence.RevisionTimestamp || revisionTimestamp.After(fetchedAt) {
				return binding, errors.New("Sekaipedia revision evidence timestamp is invalid")
			}
			page, err := parsePageResponse(evidence.Raw)
			if err != nil || page.pageID != evidence.PageID || page.revisionID != evidence.RevisionID ||
				page.sha1 != evidence.MediaWikiSHA1 || page.title != evidence.Title ||
				canonicalFetchedAt(page.revisionTimestamp) != evidence.RevisionTimestamp ||
				!equalCandidateCategories(page.categories, evidence.Categories) {
				return binding, errors.New("Sekaipedia revision response does not bind its identity")
			}
			rawSHA1 := sha1.Sum([]byte(page.content))
			if evidence.MediaWikiSHA1 != fmt.Sprintf("%x", rawSHA1) {
				return binding, errors.New("Sekaipedia MediaWiki SHA1 does not bind exact revision content")
			}
			binding.sekaipediaRawSHA256 = page.rawSHA256
			authorityBaseID := sekaipediaAuthorityEvidenceID(FixedIndex{
				PageID: evidence.PageID, RevisionID: evidence.RevisionID, Title: evidence.Title,
			})
			if authorityBaseID != "" && strings.HasPrefix(evidence.EvidenceID, authorityBaseID+":") {
				authority := FixedIndex{
					PageID: evidence.PageID, RevisionID: evidence.RevisionID,
					RevisionTimestamp: evidence.RevisionTimestamp, SHA1: evidence.MediaWikiSHA1,
					ContentSHA256: page.rawSHA256, RawSHA256: evidence.RawSHA256, Title: evidence.Title,
				}
				if !validSekaipediaAuthorityBinding(authority) ||
					VerifySekaipediaRevisionContent(evidence.Raw, authority) != nil {
					return binding, errors.New("Sekaipedia authority evidence does not bind exact revision content")
				}
				if _, err := parseSekaipediaListAuthority(page.content); err != nil {
					return binding, errors.New("Sekaipedia authority evidence is not a valid song index")
				}
				binding.sekaipediaAuthority = &authority
			}
		} else {
			if evidence.RevisionTimestamp != "" {
				return binding, errors.New("legacy MediaWiki revision evidence has an unexpected revision timestamp")
			}
			rawSHA1 := sha1.Sum(evidence.Raw)
			if evidence.MediaWikiSHA1 != fmt.Sprintf("%x", rawSHA1) {
				return binding, errors.New("MediaWiki revision SHA1 does not bind exact raw bytes")
			}
		}
	case IndexEvidenceKindMediaWikiSearchResponse:
		if evidence.Provider != ProviderVocaloidFandom || evidence.PageID != 0 || evidence.RevisionID != 0 ||
			evidence.RevisionTimestamp != "" || evidence.MediaWikiSHA1 != "" || evidence.Title != "" || evidence.CanonicalURL != "" ||
			len(evidence.Categories) != 0 || validateCanonicalFandomSearchRequestURL(evidence.CanonicalRequestURL) != nil ||
			evidence.EvidenceID != fandomSearchIndexEvidenceID(
				evidence.CanonicalRequestURL, evidence.FetchedAt, evidence.RawSHA256,
			) {
			return binding, errors.New("MediaWiki search response evidence identity is invalid")
		}
		pages, err := parseSearchResponse(evidence.Raw)
		if err != nil {
			return binding, errors.New("MediaWiki search response evidence bytes are malformed")
		}
		binding.searchIdentities = make([]candidateIndexEvidenceIdentity, len(pages))
		for index, page := range pages {
			binding.searchIdentities[index] = candidateIndexEvidenceIdentity{
				pageID: page.pageID, revisionID: page.revisionID, sha1: page.sha1,
				title: page.title, categories: append([]string(nil), page.categories...),
			}
		}
	case IndexEvidenceKindExactPublicHTML:
		urlTarget, urlErr := MoegirlPageURLTargetForURL(evidence.CanonicalURL)
		extracted, parseErr := ParseMoegirlPublicPageHTML(evidence.Raw, evidence.CanonicalURL)
		if evidence.Provider != ProviderMoegirlPublicExact || evidence.PageID <= 0 || evidence.RevisionID <= 0 ||
			evidence.RevisionTimestamp != "" || evidence.MediaWikiSHA1 != "" || evidence.Title == "" ||
			urlErr != nil || urlTarget.PageTitle != evidence.Title || evidence.CanonicalRequestURL != evidence.CanonicalURL ||
			evidence.Categories == nil || len(evidence.Categories) != 0 || parseErr != nil ||
			extracted.PageID != evidence.PageID || extracted.RevisionID != evidence.RevisionID ||
			extracted.PageTitle != evidence.Title || evidence.EvidenceID != ExactPublicPageEvidenceID(
			evidence.PageID, evidence.RevisionID, evidence.CanonicalURL, evidence.FetchedAt, evidence.RawSHA256,
		) {
			return binding, errors.New("exact public HTML evidence identity is invalid")
		}
	default:
		return binding, errors.New("index evidence kind is invalid")
	}
	return binding, nil
}

func validateCanonicalFandomSearchRequestURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed == nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Fragment != "" ||
		parsed.ForceQuery || parsed.Scheme+"://"+parsed.Host != OriginVocaloidFandom || parsed.Path != "/api.php" {
		return ErrMalformedResponse
	}
	query := parsed.Query()
	search := query.Get("gsrsearch")
	canonical, canonicalErr := canonicalMediaWikiRequestURL(vocaloidWikiAPI, searchQueryRequestParams(search))
	if search == "" || parsed.RawQuery != query.Encode() || canonicalErr != nil || value != canonical {
		return ErrMalformedResponse
	}
	return nil
}

func searchEvidenceContainsCandidate(pages []wikiPage, candidate Candidate) bool {
	matches := 0
	for _, page := range pages {
		if page.pageID == candidate.PageID && page.revisionID == candidate.RevisionID && page.sha1 == candidate.SHA1 &&
			page.title == candidate.Title && equalCandidateCategories(page.categories, candidate.Categories) {
			matches++
		}
	}
	return matches == 1
}

func searchIdentityContainsCandidate(identities []candidateIndexEvidenceIdentity, candidate Candidate) bool {
	matches := 0
	for _, identity := range identities {
		if identity.pageID == candidate.PageID && identity.revisionID == candidate.RevisionID &&
			identity.sha1 == candidate.SHA1 && identity.title == candidate.Title &&
			equalCandidateCategories(identity.categories, candidate.Categories) {
			matches++
		}
	}
	return matches == 1
}

func cloneIndexEvidence(input []IndexEvidence) []IndexEvidence {
	if input == nil {
		return nil
	}
	result := make([]IndexEvidence, len(input))
	for index, evidence := range input {
		result[index] = evidence
		result[index].Categories = append([]string(nil), evidence.Categories...)
		result[index].Raw = append([]byte(nil), evidence.Raw...)
	}
	return result
}

func conflictingIndexEvidence(left, right []IndexEvidence) bool {
	byID := make(map[string]IndexEvidence, len(left)+len(right))
	for _, collection := range [][]IndexEvidence{left, right} {
		for _, evidence := range collection {
			if existing, found := byID[evidence.EvidenceID]; found && !indexEvidenceEqual(existing, evidence) {
				return true
			}
			byID[evidence.EvidenceID] = evidence
		}
	}
	return false
}

func indexEvidenceEqual(left, right IndexEvidence) bool {
	return left.EvidenceID == right.EvidenceID && left.SHA256 == right.SHA256 && left.Kind == right.Kind &&
		left.Provider == right.Provider && left.Origin == right.Origin && left.PageID == right.PageID &&
		left.RevisionID == right.RevisionID && left.RevisionTimestamp == right.RevisionTimestamp &&
		left.MediaWikiSHA1 == right.MediaWikiSHA1 && left.Title == right.Title &&
		left.CanonicalURL == right.CanonicalURL && equalCandidateCategories(left.Categories, right.Categories) &&
		left.CanonicalRequestURL == right.CanonicalRequestURL && left.FetchedAt == right.FetchedAt &&
		left.RawSHA256 == right.RawSHA256 && bytes.Equal(left.Raw, right.Raw)
}
