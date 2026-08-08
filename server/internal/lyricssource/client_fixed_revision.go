package lyricssource

import (
	"context"
	"crypto/sha1"

	"fmt"

	"strings"

	"time"

	"moesekai/server/internal/model"
)

func (c *Client) FetchFixedRevision(ctx context.Context, identity MusicIdentity, pageID, revisionID int, expectedSHA1 string) (FixedRevision, error) {
	return c.fetchFixedRevision(ctx, identity, pageID, revisionID, expectedSHA1, nil)
}

// FetchFixedCandidateRevision fetches the reviewed exact revision and rejects
// any drift in the complete candidate identity before applying current source
// restrictions or parsing. MediaWiki title and categories are page metadata and
// can change independently of the old revision's immutable content.
func (c *Client) FetchFixedCandidateRevision(ctx context.Context, identity MusicIdentity, candidate Candidate) (FixedRevision, error) {
	if candidateProvider(candidate) != ProviderVocaloidFandom || !validFixedCandidate(candidate) {
		return FixedRevision{}, ErrMalformedResponse
	}
	return c.fetchFixedRevision(ctx, identity, candidate.PageID, candidate.RevisionID, candidate.SHA1, &candidate)
}

func (c *Client) fetchFixedRevision(ctx context.Context, identity MusicIdentity, pageID, revisionID int, expectedSHA1 string, reviewed *Candidate) (FixedRevision, error) {
	if ctx == nil || pageID <= 0 || revisionID <= 0 || !HasCanonicalSHA1(expectedSHA1) {
		return FixedRevision{}, ErrMalformedResponse
	}
	page, err := c.fetchPage(ctx, pageID, revisionID, false)
	if err != nil {
		return FixedRevision{}, err
	}
	if page.pageID != pageID || page.revisionID != revisionID {
		return FixedRevision{}, ErrRevisionChanged
	}
	content := []byte(page.content)
	contentSHA1 := fmt.Sprintf("%x", sha1.Sum(content))
	if page.sha1 != expectedSHA1 || contentSHA1 != page.sha1 || contentSHA1 != expectedSHA1 {
		return FixedRevision{}, ErrRevisionChanged
	}
	if reviewed != nil && (page.title != reviewed.Title || canonicalURL(page.title, page.revisionID) != reviewed.CanonicalURL ||
		!equalCandidateCategories(page.categories, reviewed.Categories) ||
		(reviewed.Provider != "" && reviewed.Provider != ProviderVocaloidFandom) ||
		(reviewed.Origin != "" && reviewed.Origin != OriginVocaloidFandom)) {
		return FixedRevision{}, ErrRevisionChanged
	}
	if hasLyricsTextRestriction(page.content, page.categories) {
		return FixedRevision{}, ErrRestrictedReprint
	}
	if hasWrongEntityEvidence(page.categories) {
		return FixedRevision{}, ErrAmbiguous
	}
	verified, err := c.verifyCandidateWithCreatorAliases(ctx, identity, page)
	if err != nil {
		return FixedRevision{}, err
	}
	if !verified {
		return FixedRevision{}, ErrAmbiguous
	}
	if hasExcludedVersionSignal(page.title, page.content, page.categories) {
		return FixedRevision{}, ErrAmbiguous
	}
	extraction, err := extractCategoryAwareLyrics(page.content, page.categories)
	if contextErr := ctx.Err(); contextErr != nil {
		return FixedRevision{}, contextErr
	}
	if err != nil {
		return FixedRevision{}, err
	}
	section, _, reason := fandomRenditionIdentityFromExtraction(extraction)
	extraction, err = applyPerformerSegmentationPolicy(identity, extraction)
	if err != nil {
		return FixedRevision{}, err
	}
	_, renditionKey, _ := fandomRenditionIdentityFromExtraction(extraction)
	if reviewed != nil && reviewed.Section != "" &&
		(reviewed.Section != section || reviewed.RenditionKey != renditionKey ||
			(reviewed.VersionReason != "" && reviewed.VersionReason != reason)) {
		return FixedRevision{}, ErrRevisionChanged
	}
	fetchedAt := time.Now().UTC()
	references := []model.LyricsSourceIndexEvidenceRef(nil)
	indexEvidence := []IndexEvidence(nil)
	if reviewed != nil {
		references = cloneIndexEvidenceRefs(reviewed.IndexEvidenceRefs)
		indexEvidence = cloneIndexEvidence(reviewed.IndexEvidence)
	}
	if len(references) == 0 {
		evidence, evidenceErr := newMediaWikiRevisionIndexEvidence(
			ProviderVocaloidFandom,
			fmt.Sprintf("fetch:vocaloid-fandom:%d", page.pageID),
			page,
			content,
		)
		if evidenceErr != nil {
			return FixedRevision{}, evidenceErr
		}
		references = []model.LyricsSourceIndexEvidenceRef{{EvidenceID: evidence.EvidenceID, SHA256: evidence.SHA256}}
		indexEvidence = []IndexEvidence{evidence}
	}
	fixedIdentities, document, err := buildFandomDocument(page, extraction, section, renditionKey, references, fetchedAt)
	if err != nil {
		return FixedRevision{}, err
	}
	lines := legacyExtractedLines(extraction.Lines)
	fixed := FixedRevision{
		Provider: ProviderVocaloidFandom, Origin: OriginVocaloidFandom,
		CanonicalURL: canonicalURL(page.title, page.revisionID), PageID: page.pageID, PageTitle: page.title,
		RevisionID: page.revisionID, SHA1: page.sha1, Categories: append([]string(nil), page.categories...),
		FetchedAt: fetchedAt, Wikitext: content, Lines: lines, Extraction: extraction,
		Section: section, RenditionKey: renditionKey, VersionReason: reason,
		IndexEvidenceRefs: references, IndexEvidence: indexEvidence,
		FixedIdentities: fixedIdentities, Document: document,
	}
	if err := ctx.Err(); err != nil {
		return FixedRevision{}, err
	}
	return fixed, nil
}

func validFixedCandidate(candidate Candidate) bool {
	if candidateProvider(candidate) == ProviderMoegirl {
		return validMoegirlCandidate(candidate)
	}
	if candidate.PageID <= 0 || candidate.RevisionID <= 0 || !HasCanonicalSHA1(candidate.SHA1) ||
		candidate.Title == "" || strings.TrimSpace(candidate.Title) != candidate.Title || candidate.Categories == nil ||
		candidate.CanonicalURL != canonicalURL(candidate.Title, candidate.RevisionID) {
		return false
	}
	if candidate.Provider != "" && (candidate.Provider != ProviderVocaloidFandom || candidate.Origin != OriginVocaloidFandom ||
		candidate.Section == "" || candidate.RenditionKey == "" || len(candidate.IndexEvidenceRefs) == 0) {
		return false
	}
	for index, category := range candidate.Categories {
		if category == "" || strings.TrimSpace(category) != category || (index > 0 && candidate.Categories[index-1] >= category) {
			return false
		}
	}
	if candidate.Provider != "" && ValidateCandidateIndexEvidence(candidate) != nil {
		return false
	}
	return true
}

func equalCandidateCategories(left, right []string) bool {
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

func (c *Client) Preview(ctx context.Context, identity MusicIdentity, pageID, revisionID int) (Preview, error) {
	if err := ctx.Err(); err != nil {
		return Preview{}, err
	}
	page, err := c.fetchPage(ctx, pageID, revisionID, revisionID == 0)
	if err != nil {
		return Preview{}, err
	}
	if err := ctx.Err(); err != nil {
		return Preview{}, err
	}
	if revisionID > 0 && page.revisionID != revisionID {
		return Preview{}, ErrRevisionChanged
	}
	if page.pageID != pageID {
		return Preview{}, ErrRevisionChanged
	}
	if hasLyricsTextRestriction(page.content, page.categories) {
		return Preview{}, ErrRestrictedReprint
	}
	verified, err := c.verifyCandidateWithCreatorAliases(ctx, identity, page)
	if err != nil {
		return Preview{}, err
	}
	if !verified || hasExcludedVersionSignal(page.title, page.content, page.categories) {
		return Preview{}, ErrAmbiguous
	}
	extraction, err := extractCategoryAwareLyrics(page.content, page.categories)
	if contextErr := ctx.Err(); contextErr != nil {
		return Preview{}, contextErr
	}
	if err != nil {
		return Preview{}, err
	}
	lines := legacyExtractedLines(extraction.Lines)
	if err := ctx.Err(); err != nil {
		return Preview{}, err
	}
	return Preview{
		CanonicalURL: canonicalURL(page.title, page.revisionID), PageID: pageID, RevisionID: page.revisionID,
		SHA1: page.sha1, Categories: page.categories, FetchedAt: time.Now().UTC().Format(time.RFC3339),
		Lines: lines, StructuredLines: extraction.Lines,
		RubyGeneratorVersion: extraction.RubyGeneratorVersion,
	}, nil
}
