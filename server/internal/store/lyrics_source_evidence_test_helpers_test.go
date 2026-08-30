package store

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"moesekai/server/internal/lyricsdiscovery"
	"moesekai/server/internal/lyricssource"
	"moesekai/server/internal/model"
)

const testLyricsEvidenceFetchedAt = "2026-07-30T12:34:57Z"

func testFandomSearchCandidate(
	identity model.LyricsSourceCandidateIdentity,
	section string,
	renditionKey string,
	reason model.LyricsSourceVersionReasonCode,
) lyricssource.Candidate {
	categories := append([]string{}, identity.Categories...)
	sort.Strings(categories)
	categoryRows := make([]map[string]string, len(categories))
	for index, category := range categories {
		categoryRows[index] = map[string]string{"title": "Category:" + category}
	}
	raw, err := json.Marshal(map[string]any{
		"query": map[string]any{"pages": map[string]any{
			strconv.Itoa(identity.PageID): map[string]any{
				"pageid":     identity.PageID,
				"title":      identity.Title,
				"categories": categoryRows,
				"revisions": []any{map[string]any{
					"revid": identity.RevisionID,
					"sha1":  identity.SHA1,
					"slots": map[string]any{"main": map[string]any{"content": "test search evidence"}},
				}},
			},
		}},
	})
	if err != nil {
		panic(err)
	}
	params := url.Values{
		"action":       {"query"},
		"cllimit":      {"max"},
		"format":       {"json"},
		"generator":    {"search"},
		"gsrlimit":     {"32"},
		"gsrnamespace": {"0"},
		"gsrsearch":    {identity.Title},
		"maxlag":       {"5"},
		"prop":         {"revisions|categories"},
		"rvprop":       {"ids|sha1|content"},
		"rvslots":      {"main"},
	}
	requestURL := model.LyricsSourceOriginVocaloidFandom + "/api.php?" + params.Encode()
	canonicalRequestURL, err := lyricssource.CanonicalFandomSearchRequestURL(identity.Title)
	if err != nil || requestURL != canonicalRequestURL {
		panic(fmt.Sprintf("test Fandom search request URL drifted: got=%q want=%q err=%v", requestURL, canonicalRequestURL, err))
	}
	digest := sha256.Sum256(raw)
	evidenceIdentity := strings.Join([]string{
		"lyrics-source-index-evidence-v1",
		string(lyricssource.IndexEvidenceKindMediaWikiSearchResponse),
		string(model.LyricsSourceProviderVocaloidFandom),
		model.LyricsSourceOriginVocaloidFandom,
		requestURL,
		testLyricsEvidenceFetchedAt,
		hex.EncodeToString(digest[:]),
	}, "\x00")
	evidenceIDDigest := sha256.Sum256([]byte(evidenceIdentity))
	evidenceID := fmt.Sprintf("search:vocaloid-fandom:%x", evidenceIDDigest)
	canonicalEvidenceID := lyricssource.MediaWikiSearchResponseAcquisitionEvidenceID(
		requestURL, testLyricsEvidenceFetchedAt, hex.EncodeToString(digest[:]),
	)
	if evidenceID != canonicalEvidenceID {
		panic(fmt.Sprintf("test Fandom search evidence ID drifted: got=%q want=%q", evidenceID, canonicalEvidenceID))
	}
	evidence := lyricssource.IndexEvidence{
		EvidenceID:          evidenceID,
		SHA256:              hex.EncodeToString(digest[:]),
		Kind:                lyricssource.IndexEvidenceKindMediaWikiSearchResponse,
		Provider:            model.LyricsSourceProviderVocaloidFandom,
		Origin:              model.LyricsSourceOriginVocaloidFandom,
		Categories:          []string{},
		CanonicalRequestURL: requestURL,
		FetchedAt:           testLyricsEvidenceFetchedAt,
		Raw:                 append([]byte{}, raw...),
		RawSHA256:           hex.EncodeToString(digest[:]),
	}
	return lyricssource.Candidate{
		Provider:          model.LyricsSourceProviderVocaloidFandom,
		Origin:            model.LyricsSourceOriginVocaloidFandom,
		PageID:            identity.PageID,
		Title:             identity.Title,
		CanonicalURL:      identity.CanonicalURL,
		RevisionID:        identity.RevisionID,
		SHA1:              identity.SHA1,
		Categories:        categories,
		Section:           section,
		RenditionKey:      renditionKey,
		VersionReason:     reason,
		IndexEvidenceRefs: []model.LyricsSourceIndexEvidenceRef{{EvidenceID: evidenceID, SHA256: evidence.SHA256}},
		IndexEvidence:     []lyricssource.IndexEvidence{evidence},
	}
}

func testRevisionCandidate(
	provider model.LyricsSourceProvider,
	pageID int,
	revisionID int,
	title string,
	categories []string,
	section string,
	renditionKey string,
	reason model.LyricsSourceVersionReasonCode,
	raw []byte,
) lyricssource.Candidate {
	categories = append([]string{}, categories...)
	sort.Strings(categories)
	raw = append([]byte{}, raw...)
	rawSHA1 := sha1.Sum(raw)
	rawSHA256 := sha256.Sum256(raw)
	origin := model.LyricsSourceOriginVocaloidFandom
	canonical := url.URL{Scheme: "https", Host: "vocaloid.fandom.com", Path: "/wiki/" + strings.ReplaceAll(title, " ", "_")}
	query := canonical.Query()
	query.Set("oldid", strconv.Itoa(revisionID))
	canonical.RawQuery = query.Encode()
	canonicalURL := canonical.String()
	if provider == model.LyricsSourceProviderMoegirl {
		origin = model.LyricsSourceOriginMoegirl
		params := url.Values{"oldid": {strconv.Itoa(revisionID)}, "title": {title}}
		canonicalURL = origin + "/index.php?" + params.Encode()
	}
	evidenceBaseID := fmt.Sprintf("fetch:vocaloid-fandom:%d", pageID)
	if provider == model.LyricsSourceProviderMoegirl {
		evidenceBaseID = fmt.Sprintf("search:moegirl:%d", pageID)
	}
	rawSHA256Hex := hex.EncodeToString(rawSHA256[:])
	evidenceID := lyricssource.MediaWikiRevisionAcquisitionEvidenceID(
		provider, evidenceBaseID, testLyricsEvidenceFetchedAt, rawSHA256Hex,
	)
	evidence := lyricssource.IndexEvidence{
		EvidenceID:    evidenceID,
		SHA256:        rawSHA256Hex,
		Kind:          lyricssource.IndexEvidenceKindMediaWikiRevision,
		Provider:      provider,
		Origin:        origin,
		PageID:        pageID,
		RevisionID:    revisionID,
		MediaWikiSHA1: hex.EncodeToString(rawSHA1[:]),
		Title:         title,
		CanonicalURL:  canonicalURL,
		Categories:    categories,
		FetchedAt:     testLyricsEvidenceFetchedAt,
		Raw:           raw,
		RawSHA256:     hex.EncodeToString(rawSHA256[:]),
	}
	return lyricssource.Candidate{
		Provider:          provider,
		Origin:            origin,
		PageID:            pageID,
		Title:             title,
		CanonicalURL:      canonicalURL,
		RevisionID:        revisionID,
		SHA1:              evidence.MediaWikiSHA1,
		Categories:        categories,
		Section:           section,
		RenditionKey:      renditionKey,
		VersionReason:     reason,
		IndexEvidenceRefs: []model.LyricsSourceIndexEvidenceRef{{EvidenceID: evidenceID, SHA256: evidence.SHA256}},
		IndexEvidence:     []lyricssource.IndexEvidence{evidence},
	}
}

func attachTestCandidateToFixed(fixed *lyricssource.FixedRevision, candidate lyricssource.Candidate) {
	fixed.Provider = candidate.Provider
	fixed.Origin = candidate.Origin
	fixed.PageID = candidate.PageID
	fixed.RevisionID = candidate.RevisionID
	fixed.SHA1 = candidate.SHA1
	fixed.PageTitle = candidate.Title
	fixed.CanonicalURL = candidate.CanonicalURL
	fixed.Categories = append([]string{}, candidate.Categories...)
	fixed.Section = candidate.Section
	fixed.RenditionKey = candidate.RenditionKey
	fixed.VersionReason = candidate.VersionReason
	fixed.IndexEvidenceRefs = append([]model.LyricsSourceIndexEvidenceRef{}, candidate.IndexEvidenceRefs...)
	fixed.IndexEvidence = make([]lyricssource.IndexEvidence, len(candidate.IndexEvidence))
	for index, evidence := range candidate.IndexEvidence {
		fixed.IndexEvidence[index] = evidence
		fixed.IndexEvidence[index].Categories = append([]string{}, evidence.Categories...)
		fixed.IndexEvidence[index].Raw = append([]byte{}, evidence.Raw...)
	}
	if fixed.FetchedAt.IsZero() {
		fixed.FetchedAt, _ = time.Parse(time.RFC3339Nano, testLyricsEvidenceFetchedAt)
	}
}

func mustTestCandidateArtifact(t *testing.T, candidates []lyricssource.Candidate) []byte {
	t.Helper()
	artifact, err := lyricsdiscovery.MarshalCandidateArtifact(candidates)
	if err != nil {
		t.Fatal(err)
	}
	return artifact
}

func mustEmptyTestCandidateArtifact(t *testing.T) []byte {
	t.Helper()
	return mustTestCandidateArtifact(t, []lyricssource.Candidate{})
}
