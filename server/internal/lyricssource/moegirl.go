package lyricssource

import (
	"context"
	"crypto/sha1"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"moesekai/server/internal/lyricsprovideroutcome"
	"moesekai/server/internal/model"
)

var (
	moegirlIndexLinkPattern   = regexp.MustCompile(`\[\[([^\]\n]+)\]\]`)
	moegirlCommentPattern     = regexp.MustCompile(`(?s)<!--.*?-->`)
	moegirlTopHeadingPattern  = regexp.MustCompile(`(?m)^==([^=].*?)==[ \t]*$`)
	moegirlEvidenceIDPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,255}$`)
	moegirlCanonicalSHA256Pat = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type moegirlProvider struct {
	config           ProviderConfig
	client           *Client
	fixedAuthorities fixedAuthorityCache
}

type moegirlIndexTarget struct {
	pageTitle     string
	anchor        string
	evidenceRef   model.LyricsSourceIndexEvidenceRef
	indexEvidence IndexEvidence
}

// MatchingSectionParseError reports that a provider target matched the catalog
// identity but could not be parsed into a safe, unambiguous version. Its public
// diagnostic surface is content-free: no source text, section names, titles,
// URLs, or parser error strings are retained or included in Error().
type MatchingSectionParseError struct {
	Provider     model.LyricsSourceProvider
	PageID       int
	RevisionID   int
	ReasonCode   model.LyricsSourceVersionReasonCode
	parseFailure error
}

func (failure *MatchingSectionParseError) Error() string {
	if failure == nil {
		return "matching lyrics source section failed closed"
	}
	return fmt.Sprintf(
		"matching lyrics source section failed closed (provider=%q page_id=%d revision_id=%d reason=%q)",
		failure.Provider,
		failure.PageID,
		failure.RevisionID,
		failure.ReasonCode,
	)
}

func (failure *MatchingSectionParseError) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.parseFailure
}

func newMatchingSectionParseError(
	page wikiPage,
	parsed MoegirlSectionExtraction,
	parseFailure error,
) *MatchingSectionParseError {
	reason := parsed.ReasonCode
	if reason != "" && !model.IsValidLyricsSourceVersionReasonCode(reason) {
		reason = ""
	}
	return &MatchingSectionParseError{
		Provider: ProviderMoegirl, PageID: page.pageID, RevisionID: page.revisionID,
		ReasonCode: reason, parseFailure: parseFailure,
	}
}

func newMoegirlProvider(config ProviderConfig, client *Client) *moegirlProvider {
	config.Indexes = append([]FixedIndex{}, config.Indexes...)
	return &moegirlProvider{config: config, client: client}
}

func (provider *moegirlProvider) ProviderID() model.LyricsSourceProvider {
	return ProviderMoegirl
}

type moegirlSearchEvaluation struct {
	status     lyricsprovideroutcome.Status
	reason     lyricsprovideroutcome.ReasonCode
	phase      lyricsprovideroutcome.Phase
	counts     lyricsprovideroutcome.Counts
	refs       []model.LyricsSourceIndexEvidenceRef
	candidates []Candidate
	legacyErr  error
}

func (evaluation moegirlSearchEvaluation) outcome() (lyricsprovideroutcome.Outcome[Candidate], error) {
	return newProviderSearchOutcome(
		ProviderMoegirl, evaluation.status, evaluation.reason, evaluation.phase,
		evaluation.candidates, evaluation.counts, evaluation.refs,
	)
}

func (provider *moegirlProvider) SearchOutcome(
	ctx context.Context,
	identity MusicIdentity,
) (lyricsprovideroutcome.Outcome[Candidate], error) {
	result := provider.evaluateProviderSearch(ctx, identity)
	return result.outcome, result.outcomeErr
}

func (provider *moegirlProvider) evaluateProviderSearch(
	ctx context.Context,
	identity MusicIdentity,
) providerSearchResult {
	evaluation := provider.evaluateSearch(ctx, identity)
	outcome, outcomeErr := evaluation.outcome()
	return providerSearchResult{
		outcome: outcome, legacyErr: evaluation.legacyErr, outcomeErr: outcomeErr,
	}
}

func (provider *moegirlProvider) Search(ctx context.Context, identity MusicIdentity) ([]Candidate, error) {
	evaluation := provider.evaluateSearch(ctx, identity)
	if evaluation.status == lyricsprovideroutcome.StatusCandidate {
		return cloneProviderCandidates(evaluation.candidates), nil
	}
	if evaluation.status == lyricsprovideroutcome.StatusNoMatch &&
		evaluation.reason != lyricsprovideroutcome.ReasonMissingLyrics {
		return []Candidate{}, nil
	}
	if evaluation.legacyErr != nil {
		return nil, evaluation.legacyErr
	}
	switch evaluation.status {
	case lyricsprovideroutcome.StatusAmbiguous:
		return nil, ErrAmbiguous
	case lyricsprovideroutcome.StatusStale:
		return nil, ErrRevisionChanged
	case lyricsprovideroutcome.StatusNoMatch:
		return nil, ErrMissingLyrics
	default:
		return nil, ErrMalformedResponse
	}
}

func (provider *moegirlProvider) evaluateSearch(ctx context.Context, identity MusicIdentity) moegirlSearchEvaluation {
	if ctx == nil || identity.MusicID <= 0 || strings.TrimSpace(identity.JapaneseTitle) == "" ||
		strings.TrimSpace(identity.ProducerMetadata) == "" {
		return moegirlFailureEvaluation(nil, ErrMalformedResponse, lyricsprovideroutcome.Counts{}, nil)
	}
	targets := []moegirlIndexTarget{}
	refs := []model.LyricsSourceIndexEvidenceRef{}
	counts := lyricsprovideroutcome.Counts{}
	for _, fixedIndex := range provider.config.Indexes {
		counts.Acquisitions++
		acquisition, err := provider.acquireFixedIndex(ctx, fixedIndex)
		if err != nil {
			return moegirlFailureEvaluation(nil, err, counts, refs)
		}
		page, indexEvidence := acquisition.page, acquisition.evidence
		evidenceRef := model.LyricsSourceIndexEvidenceRef{
			EvidenceID: indexEvidence.EvidenceID, SHA256: indexEvidence.SHA256,
		}
		refs = append(refs, evidenceRef)
		matches, err := moegirlIndexTargets(page.content, identity.JapaneseTitle, evidenceRef, indexEvidence)
		if err != nil {
			return moegirlFailureEvaluation(nil, err, counts, refs)
		}
		targets = append(targets, matches...)
	}
	if err := ctx.Err(); err != nil {
		return moegirlFailureEvaluation(nil, err, counts, refs)
	}

	uniqueTargets := map[string]moegirlIndexTarget{}
	for _, target := range targets {
		key := target.pageTitle + "\x00" + target.anchor
		if existing, found := uniqueTargets[key]; found {
			if existing.evidenceRef != target.evidenceRef || !indexEvidenceEqual(existing.indexEvidence, target.indexEvidence) {
				return moegirlFailureEvaluation(nil, ErrAmbiguous, counts, refs)
			}
			continue
		}
		uniqueTargets[key] = target
	}
	keys := make([]string, 0, len(uniqueTargets))
	for key := range uniqueTargets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	counts.Targets = len(keys)

	candidates := make([]Candidate, 0, len(keys))
	structuralFailures := 0
	var firstStructuralErr error
	var firstUnsupportedReason lyricsprovideroutcome.ReasonCode
	for _, key := range keys {
		target := uniqueTargets[key]
		counts.Acquisitions++
		page, err := provider.fetchPageByTitle(ctx, target.pageTitle, true)
		if err != nil {
			counts.Candidates = len(candidates)
			return moegirlFailureEvaluation(candidates, err, counts, refs)
		}
		counts.Evaluated++
		candidateRefs := []model.LyricsSourceIndexEvidenceRef{target.evidenceRef}
		candidateEvidence := []IndexEvidence{target.indexEvidence}
		if provider.config.RecoveryExactCapture {
			songEvidence, evidenceErr := newMediaWikiRevisionIndexEvidence(
				ProviderMoegirl, fmt.Sprintf("search:moegirl:%d", page.pageID), page, []byte(page.content),
			)
			if evidenceErr != nil {
				counts.Candidates = len(candidates)
				return moegirlFailureEvaluation(candidates, evidenceErr, counts, refs)
			}
			songEvidenceRef := model.LyricsSourceIndexEvidenceRef{
				EvidenceID: songEvidence.EvidenceID, SHA256: songEvidence.SHA256,
			}
			refs = append(refs, songEvidenceRef)
			candidateRefs = append(candidateRefs, songEvidenceRef)
			candidateEvidence = append(candidateEvidence, songEvidence)
		}
		section, sectionPath, err := moegirlTargetSection(page.content, target.anchor)
		if err != nil {
			structuralFailures++
			if firstStructuralErr == nil {
				firstStructuralErr = err
			}
			status, _, reason := classifyProviderSearchFailure(err)
			switch status {
			case lyricsprovideroutcome.StatusNoMatch:
				counts.NoMatch++
			case lyricsprovideroutcome.StatusAmbiguous:
				counts.Ambiguous++
			default:
				counts.Unsupported++
				if firstUnsupportedReason == "" {
					firstUnsupportedReason = reason
				}
			}
			continue
		}
		if !moegirlCatalogIdentityMatches(section, identity) {
			counts.NoMatch++
			continue
		}
		parsed, err := parseMoegirlSongSection(section, identity.PerformerSegmentationPolicy)
		if err != nil {
			failure := newMatchingSectionParseError(page, parsed, err)
			structuralFailures++
			if firstStructuralErr == nil {
				firstStructuralErr = failure
			}
			status, _, reason := classifyProviderSearchFailure(failure)
			switch status {
			case lyricsprovideroutcome.StatusNoMatch:
				counts.NoMatch++
			case lyricsprovideroutcome.StatusAmbiguous:
				counts.Ambiguous++
			default:
				counts.Unsupported++
				if firstUnsupportedReason == "" {
					firstUnsupportedReason = reason
				}
			}
			continue
		}
		renditionKey := moegirlRenditionKey(parsed)
		if renditionKey == "" {
			failure := newMatchingSectionParseError(page, parsed, ErrCatalogRenditionConflict)
			structuralFailures++
			counts.Unsupported++
			if firstStructuralErr == nil {
				firstStructuralErr = failure
			}
			if firstUnsupportedReason == "" {
				firstUnsupportedReason = lyricsprovideroutcome.ReasonCatalogRenditionConflict
			}
			continue
		}
		candidate := Candidate{
			Provider: ProviderMoegirl, Origin: OriginMoegirl,
			PageID: page.pageID, Title: page.title,
			CanonicalURL: canonicalRevisionURL(ProviderMoegirl, page.title, page.revisionID),
			RevisionID:   page.revisionID, SHA1: page.sha1, Categories: cloneIdentityCategories(page.categories),
			Section: sectionPath, RenditionKey: renditionKey, VersionReason: parsed.ReasonCode,
			IndexEvidenceRefs: candidateRefs,
			IndexEvidence:     candidateEvidence,
		}
		if err := ValidateCandidateIndexEvidence(candidate); err != nil {
			structuralFailures++
			counts.Unsupported++
			if firstStructuralErr == nil {
				firstStructuralErr = err
			}
			if firstUnsupportedReason == "" {
				firstUnsupportedReason = lyricsprovideroutcome.ReasonMalformedResponse
			}
			continue
		}
		candidates = append(candidates, candidate)
	}
	counts.Candidates = len(candidates)
	switch {
	case len(candidates) > 1:
		counts.Ambiguous++
		return moegirlSearchEvaluation{
			status: lyricsprovideroutcome.StatusAmbiguous, reason: lyricsprovideroutcome.ReasonMultipleCandidates,
			phase: lyricsprovideroutcome.PhaseFinalize, counts: counts, refs: refs, legacyErr: ErrAmbiguous,
		}
	case len(candidates) == 1 && structuralFailures > 0:
		counts.Ambiguous++
		return moegirlSearchEvaluation{
			status: lyricsprovideroutcome.StatusAmbiguous, reason: lyricsprovideroutcome.ReasonCandidateConflict,
			phase: lyricsprovideroutcome.PhaseFinalize, counts: counts, refs: refs, legacyErr: firstStructuralErr,
		}
	case len(candidates) == 1:
		return moegirlSearchEvaluation{
			status: lyricsprovideroutcome.StatusCandidate, reason: lyricsprovideroutcome.ReasonCandidate,
			phase: lyricsprovideroutcome.PhaseFinalize, counts: counts, refs: refs, candidates: candidates,
		}
	case counts.Ambiguous > 0:
		return moegirlSearchEvaluation{
			status: lyricsprovideroutcome.StatusAmbiguous, reason: lyricsprovideroutcome.ReasonAmbiguousMatch,
			phase: lyricsprovideroutcome.PhaseResolveTargets, counts: counts, refs: refs, legacyErr: firstStructuralErr,
		}
	case counts.Unsupported > 0:
		if firstUnsupportedReason == "" {
			firstUnsupportedReason = lyricsprovideroutcome.ReasonUnsupportedFormat
		}
		return moegirlSearchEvaluation{
			status: lyricsprovideroutcome.StatusUnsupported, reason: firstUnsupportedReason,
			phase: lyricsprovideroutcome.PhaseParseLyrics, counts: counts, refs: refs, legacyErr: firstStructuralErr,
		}
	case structuralFailures > 0:
		return moegirlSearchEvaluation{
			status: lyricsprovideroutcome.StatusNoMatch, reason: lyricsprovideroutcome.ReasonMissingLyrics,
			phase: lyricsprovideroutcome.PhaseParseLyrics, counts: counts, refs: refs, legacyErr: firstStructuralErr,
		}
	default:
		reason := lyricsprovideroutcome.ReasonIdentityMismatch
		phase := lyricsprovideroutcome.PhaseMatchIdentity
		if counts.Targets == 0 {
			reason = lyricsprovideroutcome.ReasonNoSearchHits
			phase = lyricsprovideroutcome.PhaseResolveTargets
			counts.NoMatch++
		}
		return moegirlSearchEvaluation{
			status: lyricsprovideroutcome.StatusNoMatch, reason: reason,
			phase: phase, counts: counts, refs: refs,
		}
	}
}

func moegirlFailureEvaluation(
	candidates []Candidate,
	err error,
	counts lyricsprovideroutcome.Counts,
	refs []model.LyricsSourceIndexEvidenceRef,
) moegirlSearchEvaluation {
	status, phase, reason := classifyProviderSearchFailure(err)
	incrementOutcomeFailureCount(&counts, status)
	counts.Candidates = len(candidates)
	return moegirlSearchEvaluation{
		status: status, reason: reason, phase: phase, counts: counts, refs: refs, legacyErr: err,
	}
}

func (provider *moegirlProvider) FetchFixedCandidateRevision(ctx context.Context, identity MusicIdentity, candidate Candidate) (FixedRevision, error) {
	if !validMoegirlCandidate(candidate) {
		return FixedRevision{}, ErrMalformedResponse
	}
	page, err := provider.client.fetchPage(ctx, candidate.PageID, candidate.RevisionID, false)
	if err != nil {
		return FixedRevision{}, err
	}
	if page.pageID != candidate.PageID || page.revisionID != candidate.RevisionID || page.title != candidate.Title ||
		page.sha1 != candidate.SHA1 || canonicalRevisionURL(ProviderMoegirl, page.title, page.revisionID) != candidate.CanonicalURL ||
		!equalCandidateCategories(page.categories, candidate.Categories) {
		return FixedRevision{}, ErrRevisionChanged
	}
	content := []byte(page.content)
	contentSHA1 := fmt.Sprintf("%x", sha1.Sum(content))
	if contentSHA1 != candidate.SHA1 {
		return FixedRevision{}, ErrRevisionChanged
	}
	anchor, ok := moegirlSectionAnchor(candidate.Section)
	if !ok {
		return FixedRevision{}, ErrMalformedResponse
	}
	section, sectionPath, err := moegirlTargetSection(page.content, anchor)
	if err != nil || sectionPath != candidate.Section {
		return FixedRevision{}, ErrRevisionChanged
	}
	if hasLyricsTextRestriction(section, page.categories) {
		return FixedRevision{}, ErrRestrictedReprint
	}
	if !moegirlCatalogIdentityMatches(section, identity) {
		return FixedRevision{}, ErrAmbiguous
	}
	parsed, err := parseMoegirlSongSection(section, identity.PerformerSegmentationPolicy)
	if err != nil {
		return FixedRevision{}, err
	}
	if parsed.ReasonCode != candidate.VersionReason || moegirlRenditionKey(parsed) != candidate.RenditionKey {
		return FixedRevision{}, ErrRevisionChanged
	}
	if parsed.ReasonCode == model.LyricsSourceVersionReasonVersionConflict {
		return FixedRevision{}, ErrMissingLyrics
	}

	fetchedAt := time.Now().UTC()
	var (
		extraction Extraction
		identities []model.LyricsSourceFixedIdentity
		document   *model.LyricsSourceDocument
	)
	switch candidate.RenditionKey {
	case "full-sekai", "full-vocaloid":
		if len(parsed.Full.Lines) == 0 {
			return FixedRevision{}, ErrMissingLyrics
		}
		extraction = parsed.Full
		identities, document, err = buildMoegirlDocument(candidate, parsed, fetchedAt)
	case "game-sekai":
		if len(parsed.Game.Lines) == 0 {
			return FixedRevision{}, ErrMissingLyrics
		}
		extraction = parsed.Game
		identities, document, err = buildMoegirlDocument(candidate, parsed, fetchedAt)
	default:
		return FixedRevision{}, ErrMissingLyrics
	}
	if err != nil {
		return FixedRevision{}, err
	}
	fixed := FixedRevision{
		Provider: ProviderMoegirl, Origin: OriginMoegirl,
		CanonicalURL: candidate.CanonicalURL, PageID: page.pageID, PageTitle: page.title,
		RevisionID: page.revisionID, SHA1: page.sha1, Categories: cloneIdentityCategories(page.categories),
		FetchedAt: fetchedAt, Wikitext: content, Lines: legacyExtractedLines(extraction.Lines), Extraction: extraction,
		Section: candidate.Section, RenditionKey: candidate.RenditionKey, VersionReason: parsed.ReasonCode,
		IndexEvidenceRefs: cloneIndexEvidenceRefs(candidate.IndexEvidenceRefs),
		IndexEvidence:     cloneStrictIndexEvidence(candidate.IndexEvidence), FixedIdentities: identities, Document: document,
	}
	return fixed, nil
}

func (provider *moegirlProvider) acquireFixedIndex(
	ctx context.Context,
	fixed FixedIndex,
) (fixedAuthorityAcquisition, error) {
	if provider == nil || !hasConfiguredFixedIndex(provider.config, fixed) {
		return fixedAuthorityAcquisition{}, ErrMalformedResponse
	}
	return provider.fixedAuthorities.resolve(ctx, fixed, func(workCtx context.Context) (fixedAuthorityAcquisition, error) {
		page, err := provider.fetchFixedIndex(workCtx, fixed)
		if err != nil {
			return fixedAuthorityAcquisition{}, err
		}
		evidence, err := newMediaWikiRevisionIndexEvidence(
			ProviderMoegirl,
			fmt.Sprintf("search:moegirl:%d", page.pageID),
			page,
			[]byte(page.content),
		)
		if err != nil {
			return fixedAuthorityAcquisition{}, err
		}
		if err := workCtx.Err(); err != nil {
			return fixedAuthorityAcquisition{}, err
		}
		return fixedAuthorityAcquisition{page: page, evidence: evidence}, nil
	})
}

func (provider *moegirlProvider) fetchFixedIndex(ctx context.Context, fixed FixedIndex) (wikiPage, error) {
	page, err := provider.client.fetchPage(ctx, fixed.PageID, fixed.RevisionID, false)
	if err != nil {
		return wikiPage{}, err
	}
	if page.pageID != fixed.PageID || page.revisionID != fixed.RevisionID || page.title != fixed.Title || page.sha1 != fixed.SHA1 ||
		fmt.Sprintf("%x", sha1.Sum([]byte(page.content))) != fixed.SHA1 {
		return wikiPage{}, ErrRevisionChanged
	}
	return page, nil
}

func (provider *moegirlProvider) fetchPageByTitle(ctx context.Context, title string, cacheable bool) (wikiPage, error) {
	if ctx == nil || strings.TrimSpace(title) == "" || strings.TrimSpace(title) != title {
		return wikiPage{}, ErrMalformedResponse
	}
	params := url.Values{
		"action": {"query"}, "format": {"json"}, "prop": {"revisions|categories"}, "titles": {title},
		"rvprop": {"ids|sha1|content"}, "rvslots": {"main"}, "rvlimit": {"1"}, "cllimit": {"max"},
		"maxlag": {mediaWikiMaxLag},
	}
	data, fetchedAt, err := provider.client.requestWithFetchedAt(ctx, "page", params, cacheable)
	if err != nil {
		return wikiPage{}, err
	}
	page, err := parsePageResponse(data)
	if err != nil {
		return wikiPage{}, err
	}
	if page.title != title {
		return wikiPage{}, ErrRevisionChanged
	}
	page.fetchedAt = fetchedAt
	return page, nil
}

func moegirlIndexTargets(
	content, japaneseTitle string,
	evidenceRef model.LyricsSourceIndexEvidenceRef,
	indexEvidence IndexEvidence,
) ([]moegirlIndexTarget, error) {
	if !utf8ValidBounded(content, maxResponseBytes) || strings.TrimSpace(japaneseTitle) == "" {
		return nil, ErrMalformedResponse
	}
	content = moegirlCommentPattern.ReplaceAllString(content, "")
	matches := moegirlIndexLinkPattern.FindAllStringSubmatch(content, -1)
	result := []moegirlIndexTarget{}
	for _, match := range matches {
		fields, ok := splitTopLevelStructuredFields(match[1], "|")
		if !ok || len(fields) < 1 || len(fields) > 2 {
			continue
		}
		targetValue := strings.TrimSpace(fields[0])
		pageTitle, anchor, found := strings.Cut(targetValue, "#")
		pageTitle, anchor = strings.TrimSpace(pageTitle), strings.TrimSpace(anchor)
		if !found || pageTitle == "" || anchor == "" || strings.ContainsAny(pageTitle, "[]{}<>|") {
			continue
		}
		display := anchor
		if len(fields) == 2 {
			display = strings.TrimSpace(fields[1])
		}
		display = sanitizeMoegirlIndexDisplay(display)
		if display == "" || !titleFormMatches(display, japaneseTitle) {
			continue
		}
		result = append(result, moegirlIndexTarget{
			pageTitle: pageTitle, anchor: anchor, evidenceRef: evidenceRef, indexEvidence: indexEvidence,
		})
	}
	return result, nil
}

func sanitizeMoegirlIndexDisplay(value string) string {
	for range 4 {
		start, end, inner, ok := findBalancedNamedTemplate(value, "lj")
		if !ok {
			break
		}
		parts, valid := splitTopLevelStructuredFields(inner, "|")
		if !valid || len(parts) != 2 {
			return ""
		}
		value = value[:start] + parts[1] + value[end:]
	}
	value = strings.ReplaceAll(value, "'''", "")
	value = strings.ReplaceAll(value, "''", "")
	value = strings.TrimSpace(htmlUnescape(value))
	if strings.ContainsAny(value, "{}[]<>|") {
		return ""
	}
	return value
}

func moegirlTargetSection(content, wantedAnchor string) (string, string, error) {
	content = strings.ReplaceAll(content, "\r", "")
	matches := moegirlTopHeadingPattern.FindAllStringSubmatchIndex(content, -1)
	matched := -1
	for index, location := range matches {
		heading := strings.TrimSpace(content[location[2]:location[3]])
		if normalizeMoegirlAnchor(heading) != normalizeMoegirlAnchor(wantedAnchor) {
			continue
		}
		if matched >= 0 {
			return "", "", ErrAmbiguous
		}
		matched = index
	}
	if matched < 0 {
		return "", "", ErrMissingLyrics
	}
	start := matches[matched][1]
	end := len(content)
	if matched+1 < len(matches) {
		end = matches[matched+1][0]
	}
	heading := strings.TrimSpace(content[matches[matched][2]:matches[matched][3]])
	section := content[start:end]
	if !moegirlLyricsHeadingPattern.MatchString(section) {
		return "", "", ErrMissingLyrics
	}
	return section, heading + "/歌词", nil
}

func normalizeMoegirlAnchor(value string) string {
	value = identityDisplayText(strings.ReplaceAll(value, "_", " "))
	return normalizeTitle(value)
}

func moegirlSectionAnchor(section string) (string, bool) {
	anchor, suffix, found := strings.Cut(section, "/")
	return anchor, found && strings.TrimSpace(anchor) == anchor && anchor != "" && suffix == "歌词"
}

func moegirlCatalogIdentityMatches(section string, identity MusicIdentity) bool {
	_, _, inner, ok := findBalancedNamedTemplate(section, "ProjectsekaiSongGai")
	if !ok {
		return false
	}
	params, ok := parseMoegirlMetadataParameters(inner)
	if !ok {
		return false
	}
	titleMatched := false
	for _, key := range []string{"曲名", "名字"} {
		if value := identityDisplayText(params[key]); value != "" && titleFormMatches(value, identity.JapaneseTitle) {
			titleMatched = true
		}
	}
	if !titleMatched {
		return false
	}
	expectedRoles := 0
	matchedRoles := 0
	for _, expectation := range []struct {
		wanted string
		field  string
	}{
		{wanted: identity.Lyricist, field: "作词"},
		{wanted: identity.Composer, field: "作曲"},
		{wanted: identity.Arranger, field: "编曲"},
	} {
		if strings.TrimSpace(expectation.wanted) == "" {
			continue
		}
		expectedRoles++
		actualValue := identityDisplayText(params[expectation.field])
		if actualValue == "" {
			continue
		}
		wanted, wantedOK := contributorSet(expectation.wanted)
		actual, actualOK := contributorSet(actualValue)
		if !wantedOK || !actualOK || !contributorSetsEqual(wanted, actual) {
			// Missing source roles may be corroborated by the remaining exact
			// metadata, but an explicit contributor conflict always rejects.
			return false
		}
		matchedRoles++
	}
	return expectedRoles == 0 || matchedRoles > 0
}

func parseMoegirlMetadataParameters(inner string) (map[string]string, bool) {
	fields, ok := splitTopLevelStructuredFields(inner, "|")
	if !ok || len(fields) < 2 || !strings.EqualFold(strings.TrimSpace(fields[0]), "ProjectsekaiSongGai") {
		return nil, false
	}
	params := map[string]string{}
	for _, field := range fields[1:] {
		separator := strings.IndexByte(field, '=')
		if separator < 0 {
			continue
		}
		name := strings.TrimSpace(field[:separator])
		value := strings.TrimSpace(field[separator+1:])
		if name == "" || value == "" {
			continue
		}
		canonicalName := name
		switch name {
		case "作詞":
			canonicalName = "作词"
		case "作曲":
			canonicalName = "作曲"
		case "編曲":
			canonicalName = "编曲"
		}
		if _, duplicate := params[canonicalName]; duplicate {
			return nil, false
		}
		params[canonicalName] = value
	}
	return params, true
}

func validMoegirlCandidate(candidate Candidate) bool {
	if candidate.Provider != ProviderMoegirl || candidate.Origin != OriginMoegirl || candidate.PageID <= 0 ||
		candidate.RevisionID <= 0 || !HasCanonicalSHA1(candidate.SHA1) || candidate.Title == "" ||
		strings.TrimSpace(candidate.Title) != candidate.Title || candidate.CanonicalURL != canonicalRevisionURL(ProviderMoegirl, candidate.Title, candidate.RevisionID) ||
		candidate.Section == "" || !model.IsValidLyricsSourceCandidateVersionReasonCode(candidate.VersionReason) ||
		!validMoegirlRenditionKey(candidate.VersionReason, candidate.RenditionKey) || candidate.Categories == nil || len(candidate.Categories) > 256 ||
		len(candidate.IndexEvidenceRefs) == 0 || len(candidate.IndexEvidenceRefs) > 64 {
		return false
	}
	if _, ok := moegirlSectionAnchor(candidate.Section); !ok {
		return false
	}
	for index, category := range candidate.Categories {
		if category == "" || strings.TrimSpace(category) != category || (index > 0 && candidate.Categories[index-1] >= category) {
			return false
		}
	}
	seen := map[string]struct{}{}
	for _, reference := range candidate.IndexEvidenceRefs {
		if !moegirlEvidenceIDPattern.MatchString(reference.EvidenceID) || !moegirlCanonicalSHA256Pat.MatchString(reference.SHA256) {
			return false
		}
		if _, duplicate := seen[reference.EvidenceID]; duplicate {
			return false
		}
		seen[reference.EvidenceID] = struct{}{}
	}
	return ValidateCandidateIndexEvidence(candidate) == nil
}

func moegirlRenditionKey(parsed MoegirlSectionExtraction) string {
	kind := parsed.Full.Version.Kind
	if kind == "" {
		kind = parsed.Game.Version.Kind
	}
	switch parsed.ReasonCode {
	case model.LyricsSourceVersionReasonTaggedFullAndGame, model.LyricsSourceVersionReasonUntaggedFullOnly,
		model.LyricsSourceVersionReasonUntaggedUncutIdentity:
		return fullRenditionKey(kind)
	case model.LyricsSourceVersionReasonTaggedGameOnlyFullFromVocaloid, model.LyricsSourceVersionReasonUntaggedGameSubset:
		if kind == "sekai" {
			return "game-sekai"
		}
	}
	return ""
}

func validMoegirlRenditionKey(reason model.LyricsSourceVersionReasonCode, renditionKey string) bool {
	switch reason {
	case model.LyricsSourceVersionReasonUntaggedFullOnly:
		return renditionKey == "full-sekai" || renditionKey == "full-vocaloid"
	case model.LyricsSourceVersionReasonTaggedFullAndGame, model.LyricsSourceVersionReasonUntaggedUncutIdentity:
		return renditionKey == "full-sekai"
	case model.LyricsSourceVersionReasonTaggedGameOnlyFullFromVocaloid, model.LyricsSourceVersionReasonUntaggedGameSubset:
		return renditionKey == "game-sekai"
	default:
		return false
	}
}

func utf8ValidBounded(value string, maxBytes int) bool {
	return len(value) <= maxBytes && utf8.ValidString(value)
}

func htmlUnescape(value string) string {
	return html.UnescapeString(value)
}
