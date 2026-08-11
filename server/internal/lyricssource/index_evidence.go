package lyricssource

import (
	"bytes"

	"crypto/sha256"

	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"moesekai/server/internal/model"
)

func newFandomSearchIndexEvidence(
	requestURL string,
	fetchedAt time.Time,
	raw []byte,
) (IndexEvidence, error) {
	if len(raw) == 0 || len(raw) > MaxIndexEvidenceRawBytes {
		return IndexEvidence{}, ErrMalformedResponse
	}
	digest := sha256.Sum256(raw)
	rawSHA256 := fmt.Sprintf("%x", digest)
	canonicalTimestamp := canonicalFetchedAt(fetchedAt)
	evidence := IndexEvidence{
		EvidenceID:          fandomSearchIndexEvidenceID(requestURL, canonicalTimestamp, rawSHA256),
		SHA256:              rawSHA256,
		Kind:                IndexEvidenceKindMediaWikiSearchResponse,
		Provider:            ProviderVocaloidFandom,
		Origin:              OriginVocaloidFandom,
		CanonicalRequestURL: requestURL,
		FetchedAt:           canonicalTimestamp,
		Raw:                 append([]byte(nil), raw...),
		RawSHA256:           rawSHA256,
	}
	if err := validateIndexEvidence(evidence); err != nil {
		return IndexEvidence{}, err
	}
	return evidence, nil
}

func fandomSearchIndexEvidenceID(requestURL, fetchedAt, rawSHA256 string) string {
	identity := strings.Join([]string{
		"lyrics-source-index-evidence-v1",
		string(IndexEvidenceKindMediaWikiSearchResponse),
		string(ProviderVocaloidFandom),
		OriginVocaloidFandom,
		requestURL,
		fetchedAt,
		rawSHA256,
	}, "\x00")
	digest := sha256.Sum256([]byte(identity))
	return fmt.Sprintf("search:vocaloid-fandom:%x", digest)
}

// CanonicalFandomSearchRequestURL returns the exact provider request URL used
// for one Fandom title-search acquisition.
func CanonicalFandomSearchRequestURL(query string) (string, error) {
	return canonicalMediaWikiRequestURL(vocaloidWikiAPI, searchQueryRequestParams(query))
}

// MediaWikiSearchResponseAcquisitionEvidenceID returns the canonical immutable
// acquisition ID for one exact Fandom search response envelope.
func MediaWikiSearchResponseAcquisitionEvidenceID(requestURL, fetchedAt, rawSHA256 string) string {
	return fandomSearchIndexEvidenceID(requestURL, fetchedAt, rawSHA256)
}

func newMediaWikiRevisionIndexEvidence(
	provider model.LyricsSourceProvider,
	baseID string,
	page wikiPage,
	raw []byte,
) (IndexEvidence, error) {
	if page.fetchedAt.IsZero() || len(raw) == 0 || len(raw) > MaxIndexEvidenceRawBytes {
		return IndexEvidence{}, ErrMalformedResponse
	}
	if provider == ProviderSekaipedia {
		parsed, err := parsePageResponse(raw)
		authorityBaseID := sekaipediaAuthorityEvidenceID(FixedIndex{
			PageID: page.pageID, RevisionID: page.revisionID, Title: page.title,
		})
		if err != nil || !sameWikiPageRevisionIdentity(parsed, page) || parsed.revisionTimestamp.IsZero() ||
			(baseID != sekaipediaSongEvidenceID(page.pageID, page.revisionID) && baseID != authorityBaseID) {
			return IndexEvidence{}, ErrMalformedResponse
		}
	} else if !bytes.Equal(raw, []byte(page.content)) {
		return IndexEvidence{}, ErrMalformedResponse
	}
	origin := OriginVocaloidFandom
	switch provider {
	case ProviderMoegirl:
		origin = OriginMoegirl
	case ProviderSekaipedia:
		origin = OriginSekaipedia
	}
	digest := sha256.Sum256(raw)
	rawSHA256 := fmt.Sprintf("%x", digest)
	fetchedAt := canonicalFetchedAt(page.fetchedAt)
	evidenceID := mediaWikiRevisionAcquisitionEvidenceID(provider, baseID, fetchedAt, rawSHA256)
	if evidenceID == "" {
		return IndexEvidence{}, ErrMalformedResponse
	}
	evidence := IndexEvidence{
		EvidenceID:    evidenceID,
		SHA256:        rawSHA256,
		Kind:          IndexEvidenceKindMediaWikiRevision,
		Provider:      provider,
		Origin:        origin,
		PageID:        page.pageID,
		RevisionID:    page.revisionID,
		MediaWikiSHA1: page.sha1,
		Title:         page.title,
		CanonicalURL:  canonicalRevisionURL(provider, page.title, page.revisionID),
		Categories:    append([]string{}, page.categories...),
		FetchedAt:     fetchedAt,
		Raw:           append([]byte(nil), raw...),
		RawSHA256:     rawSHA256,
	}
	if provider == ProviderSekaipedia && !page.revisionTimestamp.IsZero() {
		evidence.RevisionTimestamp = canonicalFetchedAt(page.revisionTimestamp)
	}
	if err := validateIndexEvidence(evidence); err != nil {
		return IndexEvidence{}, err
	}
	return evidence, nil
}

func sameWikiPageRevisionIdentity(left, right wikiPage) bool {
	return left.pageID == right.pageID && left.revisionID == right.revisionID && left.sha1 == right.sha1 &&
		left.title == right.title && left.revisionTimestamp.Equal(right.revisionTimestamp) &&
		equalCandidateCategories(left.categories, right.categories) && left.content == right.content
}

func sekaipediaSongEvidenceID(pageID, revisionID int) string {
	return fmt.Sprintf("revision:sekaipedia:%d:%d", pageID, revisionID)
}

func ExactPublicPageEvidenceID(
	pageID int,
	revisionID int,
	pageURL string,
	fetchedAt string,
	rawSHA256 string,
) string {
	if pageID <= 0 || revisionID <= 0 || !canonicalIndexEvidenceSHA256.MatchString(rawSHA256) {
		return ""
	}
	if _, err := MoegirlPageURLTargetForURL(pageURL); err != nil {
		return ""
	}
	if parsed, err := time.Parse(time.RFC3339Nano, fetchedAt); err != nil ||
		parsed.Location() != time.UTC || parsed.UTC().Format(time.RFC3339Nano) != fetchedAt {
		return ""
	}
	identity := strings.Join([]string{
		"lyrics-source-exact-public-evidence-v1",
		string(ProviderMoegirlPublicExact),
		OriginMoegirlPublicExact,
		strconv.Itoa(pageID), strconv.Itoa(revisionID), pageURL, fetchedAt, rawSHA256,
	}, "\x00")
	digest := sha256.Sum256([]byte(identity))
	return fmt.Sprintf("public:moegirl-public-exact:%x", digest)
}

func mediaWikiRevisionAcquisitionEvidenceID(
	provider model.LyricsSourceProvider,
	baseID, fetchedAt, rawSHA256 string,
) string {
	origin := OriginVocaloidFandom
	switch provider {
	case ProviderVocaloidFandom:
	case ProviderMoegirl:
		origin = OriginMoegirl
	case ProviderSekaipedia:
		origin = OriginSekaipedia
	default:
		return ""
	}
	identity := strings.Join([]string{
		"lyrics-source-index-evidence-v1",
		string(IndexEvidenceKindMediaWikiRevision),
		string(provider),
		origin,
		baseID,
		fetchedAt,
		rawSHA256,
	}, "\x00")
	digest := sha256.Sum256([]byte(identity))
	return fmt.Sprintf("%s:%x", baseID, digest)
}

// MediaWikiRevisionAcquisitionEvidenceID returns the canonical immutable
// acquisition ID for a provider-specific revision base ID. It returns an empty
// string for an unknown provider.
func MediaWikiRevisionAcquisitionEvidenceID(
	provider model.LyricsSourceProvider,
	baseID, fetchedAt, rawSHA256 string,
) string {
	return mediaWikiRevisionAcquisitionEvidenceID(provider, baseID, fetchedAt, rawSHA256)
}

func sekaipediaRevisionAcquisitionEvidenceID(baseID, fetchedAt, rawSHA256 string) string {
	return mediaWikiRevisionAcquisitionEvidenceID(ProviderSekaipedia, baseID, fetchedAt, rawSHA256)
}

func mediaWikiRevisionEvidenceBaseID(evidence IndexEvidence) string {
	switch evidence.Provider {
	case ProviderVocaloidFandom:
		return fmt.Sprintf("fetch:vocaloid-fandom:%d", evidence.PageID)
	case ProviderMoegirl:
		return fmt.Sprintf("search:moegirl:%d", evidence.PageID)
	case ProviderSekaipedia:
		authorityBaseID := sekaipediaAuthorityEvidenceID(FixedIndex{
			PageID: evidence.PageID, RevisionID: evidence.RevisionID, Title: evidence.Title,
		})
		if authorityBaseID != "" && strings.HasPrefix(evidence.EvidenceID, authorityBaseID+":") {
			return authorityBaseID
		}
		return sekaipediaSongEvidenceID(evidence.PageID, evidence.RevisionID)
	default:
		return ""
	}
}

func canonicalMediaWikiRequestURL(endpoint string, params url.Values) (string, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed == nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.Fragment != "" || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.ForceQuery ||
		(parsed.Path != "/api.php" && parsed.Path != "/w/api.php") {
		return "", ErrMalformedResponse
	}
	parsed.RawQuery = params.Encode()
	if parsed.RawQuery == "" {
		return "", ErrMalformedResponse
	}
	return parsed.String(), nil
}
