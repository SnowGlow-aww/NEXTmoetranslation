package lyricssource

import (
	"context"
	"crypto/sha1"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"moesekai/server/internal/lyricsprovideroutcome"
	"moesekai/server/internal/model"
)

const (
	sekaipediaAuthorityEvidencePrefix       = "authority:sekaipedia:"
	maxSekaipediaAuthorityKeyBytes          = 128
	maxSekaipediaFixedJapaneseWikitextBytes = maxExtractedTextBytes + 2*maxExtractedLines
)

type sekaipediaProvider struct {
	config           ProviderConfig
	client           *Client
	fixedAuthorities fixedAuthorityCache
}

func newSekaipediaProvider(config ProviderConfig, client *Client) *sekaipediaProvider {
	config = cloneRecoveryProviderConfig(config)
	return &sekaipediaProvider{config: config, client: client}
}

func (provider *sekaipediaProvider) ProviderID() model.LyricsSourceProvider {
	return ProviderSekaipedia
}

func sekaipediaAuthorityEvidenceID(index FixedIndex) string {
	if index.PageID <= 0 || index.RevisionID <= 0 {
		return ""
	}
	key, ok := sekaipediaAuthorityTitleKey(index.Title)
	if !ok {
		return ""
	}
	return sekaipediaAuthorityEvidencePrefix + key + ":" + strconv.Itoa(index.RevisionID)
}

func sekaipediaAuthorityTitleKey(title string) (string, bool) {
	if title == "" || len(title) > maxProviderContributorAliasBytes || !utf8.ValidString(title) || strings.TrimSpace(title) != title {
		return "", false
	}
	var result strings.Builder
	separatorPending := false
	for _, current := range title {
		switch {
		case current >= 'A' && current <= 'Z':
			if separatorPending && result.Len() > 0 {
				result.WriteByte('-')
			}
			separatorPending = false
			result.WriteByte(byte(current - 'A' + 'a'))
		case current >= 'a' && current <= 'z' || current >= '0' && current <= '9':
			if separatorPending && result.Len() > 0 {
				result.WriteByte('-')
			}
			separatorPending = false
			result.WriteRune(current)
		default:
			separatorPending = result.Len() > 0
		}
		if result.Len() > maxSekaipediaAuthorityKeyBytes {
			return "", false
		}
	}
	return result.String(), result.Len() > 0
}

type sekaipediaSearchEvaluation struct {
	status     lyricsprovideroutcome.Status
	reason     lyricsprovideroutcome.ReasonCode
	phase      lyricsprovideroutcome.Phase
	counts     lyricsprovideroutcome.Counts
	refs       []model.LyricsSourceIndexEvidenceRef
	candidates []Candidate
	legacyErr  error
}

func (evaluation sekaipediaSearchEvaluation) outcome() (lyricsprovideroutcome.Outcome[Candidate], error) {
	return newProviderSearchOutcome(
		ProviderSekaipedia, evaluation.status, evaluation.reason, evaluation.phase,
		evaluation.candidates, evaluation.counts, evaluation.refs,
	)
}

func (provider *sekaipediaProvider) SearchOutcome(
	ctx context.Context,
	identity MusicIdentity,
) (lyricsprovideroutcome.Outcome[Candidate], error) {
	result := provider.evaluateProviderSearch(ctx, identity)
	return result.outcome, result.outcomeErr
}

func (provider *sekaipediaProvider) evaluateProviderSearch(
	ctx context.Context,
	identity MusicIdentity,
) providerSearchResult {
	evaluation := provider.evaluateSearch(ctx, identity)
	outcome, outcomeErr := evaluation.outcome()
	return providerSearchResult{
		outcome: outcome, legacyErr: evaluation.legacyErr, outcomeErr: outcomeErr,
	}
}

func (provider *sekaipediaProvider) Search(ctx context.Context, identity MusicIdentity) ([]Candidate, error) {
	evaluation := provider.evaluateSearch(ctx, identity)
	if evaluation.status == lyricsprovideroutcome.StatusCandidate {
		return cloneProviderCandidates(evaluation.candidates), nil
	}
	if evaluation.legacyErr != nil {
		return nil, evaluation.legacyErr
	}
	return []Candidate{}, nil
}

func (provider *sekaipediaProvider) evaluateSearch(
	ctx context.Context,
	identity MusicIdentity,
) sekaipediaSearchEvaluation {
	counts := lyricsprovideroutcome.Counts{}
	refs := []model.LyricsSourceIndexEvidenceRef{}
	if ctx == nil || identity.MusicID <= 0 || strings.TrimSpace(identity.JapaneseTitle) == "" ||
		strings.TrimSpace(identity.ProducerMetadata) == "" ||
		(identity.PerformerSegmentationPolicy != PerformerSegmentationDisabled &&
			identity.PerformerSegmentationPolicy != PerformerSegmentationSekaiEligible) || len(provider.config.Indexes) != 1 {
		return sekaipediaFailureEvaluationAt(
			ErrMalformedResponse, lyricsprovideroutcome.PhaseFinalize, counts, refs,
		)
	}

	counts.Acquisitions++
	acquisition, err := provider.acquireFixedIndex(ctx, provider.config.Indexes[0])
	if err != nil {
		return sekaipediaFailureEvaluationAt(
			err, lyricsprovideroutcome.PhaseAcquireAuthority, counts, refs,
		)
	}
	indexPage, indexEvidence := acquisition.page, acquisition.evidence
	refs = append(refs, model.LyricsSourceIndexEvidenceRef{
		EvidenceID: indexEvidence.EvidenceID, SHA256: indexEvidence.SHA256,
	})
	targets, err := parseSekaipediaListAuthority(indexPage.content)
	if err != nil {
		return sekaipediaFailureEvaluationAt(
			err, lyricsprovideroutcome.PhaseResolveTargets, counts, refs,
		)
	}
	target, found, err := provider.resolveListTarget(targets, identity)
	if err != nil {
		return sekaipediaFailureEvaluationAt(
			err, lyricsprovideroutcome.PhaseResolveTargets, counts, refs,
		)
	}
	if !found {
		return sekaipediaNoMatchEvaluation(
			lyricsprovideroutcome.ReasonNoSearchHits,
			lyricsprovideroutcome.PhaseResolveTargets,
			counts,
			refs,
		)
	}

	counts.Targets = 1
	counts.Acquisitions++
	var page wikiPage
	if provider.config.RecoveryRevision != nil {
		pinned := *provider.config.RecoveryRevision
		page, err = provider.fetchExactRevision(ctx, pinned.RevisionID, true)
		if err == nil && (page.title != pinned.Title || VerifySekaipediaRevisionContent(page.rawResponse, pinned) != nil) {
			err = ErrRevisionChanged
		}
	} else {
		page, err = provider.fetchPageByTitle(ctx, target.pageTitle, true)
	}
	if err != nil {
		return sekaipediaFailureEvaluationAt(
			err, lyricsprovideroutcome.PhaseAcquireTarget, counts, refs,
		)
	}
	counts.Evaluated = 1
	songEvidence, err := newMediaWikiRevisionIndexEvidence(
		ProviderSekaipedia, sekaipediaSongEvidenceID(page.pageID, page.revisionID), page, page.rawResponse,
	)
	if err != nil {
		return sekaipediaFailureEvaluationAt(
			err, lyricsprovideroutcome.PhaseAcquireTarget, counts, refs,
		)
	}
	refs = append(refs, model.LyricsSourceIndexEvidenceRef{
		EvidenceID: songEvidence.EvidenceID, SHA256: songEvidence.SHA256,
	})
	expectedPageTitle := target.resolvedPageTitle
	if expectedPageTitle == "" {
		expectedPageTitle = target.pageTitle
	}
	if page.title != expectedPageTitle {
		return sekaipediaFailureEvaluationAt(
			ErrRevisionChanged, lyricsprovideroutcome.PhaseAcquireTarget, counts, refs,
		)
	}
	if !provider.catalogIdentityMatches(page.content, page.title, identity) {
		return sekaipediaNoMatchEvaluation(
			lyricsprovideroutcome.ReasonIdentityMismatch,
			lyricsprovideroutcome.PhaseMatchIdentity,
			counts,
			refs,
		)
	}
	parsed, err := parseSekaipediaSong(page.content, identity.PerformerSegmentationPolicy)
	if err != nil {
		return sekaipediaFailureEvaluationAt(
			err, lyricsprovideroutcome.PhaseParseLyrics, counts, refs,
		)
	}
	candidate := Candidate{
		Provider: ProviderSekaipedia, Origin: OriginSekaipedia,
		PageID: page.pageID, Title: page.title,
		CanonicalURL: canonicalRevisionURL(ProviderSekaipedia, page.title, page.revisionID),
		RevisionID:   page.revisionID, RevisionTimestamp: canonicalFetchedAt(page.revisionTimestamp),
		SHA1: page.sha1, RawSHA256: page.rawSHA256,
		Categories: cloneIdentityCategories(page.categories), FetchedAt: canonicalFetchedAt(page.fetchedAt),
		Section: parsed.Section, RenditionKey: parsed.RenditionKey, VersionReason: parsed.ReasonCode,
		IndexEvidenceRefs: cloneIndexEvidenceRefs(refs),
		IndexEvidence:     []IndexEvidence{indexEvidence, songEvidence},
	}
	if !validSekaipediaCandidateForAuthority(candidate, provider.config.Indexes[0]) {
		return sekaipediaFailureEvaluationAt(
			ErrMalformedResponse, lyricsprovideroutcome.PhaseFinalize, counts, refs,
		)
	}
	counts.Candidates = 1
	return sekaipediaSearchEvaluation{
		status: lyricsprovideroutcome.StatusCandidate, reason: lyricsprovideroutcome.ReasonCandidate,
		phase: lyricsprovideroutcome.PhaseFinalize, counts: counts, refs: refs, candidates: []Candidate{candidate},
	}
}

func (provider *sekaipediaProvider) resolveListTarget(
	targets []sekaipediaListTarget,
	identity MusicIdentity,
) (sekaipediaListTarget, bool, error) {
	if provider.config.RecoveryRevision != nil {
		target, found := exactSekaipediaListTarget(targets, provider.config.RecoveryRevision.Title)
		target.resolvedPageTitle = provider.config.RecoveryRevision.Title
		return target, found, nil
	}
	if len(provider.config.SekaipediaTargets) > 0 {
		for _, planned := range provider.config.SekaipediaTargets {
			if planned.MusicID == identity.MusicID {
				target, found := exactSekaipediaListTarget(targets, planned.PageTitle)
				if !found && provider.config.RecoveryExactCapture {
					// A reviewed recovery plan may bind a page introduced after the
					// immutable List revision. The plan title is exact authority here;
					// ordinary discovery continues to require List membership.
					target = sekaipediaListTarget{
						pageTitle: planned.PageTitle,
						display:   planned.PageTitle,
					}
					found = true
				}
				target.resolvedPageTitle = planned.ResolvedPageTitle
				if target.resolvedPageTitle == "" {
					target.resolvedPageTitle = planned.PageTitle
				}
				return target, found, nil
			}
			if planned.MusicID > identity.MusicID {
				break
			}
		}
		return sekaipediaListTarget{}, false, nil
	}
	if provider.config.RecoveryExactCapture {
		// Recovery never guesses a romanized MediaWiki title from a catalog song
		// name. New plans must carry a reviewed music-ID-to-page-title map.
		return sekaipediaListTarget{}, false, nil
	}
	target, found, err := selectSekaipediaListTarget(targets, identity.JapaneseTitle)
	target.resolvedPageTitle = target.pageTitle
	return target, found, err
}

func sekaipediaFailureEvaluationAt(
	err error,
	phase lyricsprovideroutcome.Phase,
	counts lyricsprovideroutcome.Counts,
	refs []model.LyricsSourceIndexEvidenceRef,
) sekaipediaSearchEvaluation {
	status, _, reason := classifyProviderSearchFailure(err)
	incrementOutcomeFailureCount(&counts, status)
	return sekaipediaSearchEvaluation{
		status: status, reason: reason, phase: phase, counts: counts, refs: refs, legacyErr: err,
	}
}

func sekaipediaNoMatchEvaluation(
	reason lyricsprovideroutcome.ReasonCode,
	phase lyricsprovideroutcome.Phase,
	counts lyricsprovideroutcome.Counts,
	refs []model.LyricsSourceIndexEvidenceRef,
) sekaipediaSearchEvaluation {
	counts.NoMatch++
	return sekaipediaSearchEvaluation{
		status: lyricsprovideroutcome.StatusNoMatch, reason: reason, phase: phase, counts: counts, refs: refs,
	}
}

func (provider *sekaipediaProvider) FetchFixedCandidateRevision(
	ctx context.Context,
	identity MusicIdentity,
	candidate Candidate,
) (FixedRevision, error) {
	if ctx == nil || len(provider.config.Indexes) != 1 ||
		!validSekaipediaCandidateForAuthority(candidate, provider.config.Indexes[0]) || identity.MusicID <= 0 ||
		(identity.PerformerSegmentationPolicy != PerformerSegmentationDisabled &&
			identity.PerformerSegmentationPolicy != PerformerSegmentationSekaiEligible) {
		return FixedRevision{}, fmt.Errorf("sekaipedia fixed revision guard failed: %w", ErrMalformedResponse)
	}
	candidatePage, err := sekaipediaCandidateRevisionPage(candidate)
	if err != nil {
		return FixedRevision{}, fmt.Errorf("sekaipedia candidate revision page parse: %w", ErrMalformedResponse)
	}
	page, err := provider.fetchExactRevision(ctx, candidate.RevisionID, false)
	if err != nil {
		return FixedRevision{}, err
	}
	if page.pageID != candidate.PageID || page.revisionID != candidate.RevisionID || page.title != candidate.Title ||
		page.sha1 != candidate.SHA1 || page.rawSHA256 != candidatePage.rawSHA256 ||
		canonicalFetchedAt(page.revisionTimestamp) != candidate.RevisionTimestamp ||
		canonicalRevisionURL(ProviderSekaipedia, page.title, page.revisionID) != candidate.CanonicalURL ||
		!equalCandidateCategories(page.categories, candidate.Categories) {
		return FixedRevision{}, ErrRevisionChanged
	}
	if !provider.catalogIdentityMatches(page.content, page.title, identity) {
		return FixedRevision{}, ErrAmbiguous
	}
	parsed, err := parseSekaipediaSong(page.content, identity.PerformerSegmentationPolicy)
	if err != nil {
		return FixedRevision{}, err
	}
	if parsed.Section != candidate.Section || parsed.RenditionKey != candidate.RenditionKey || parsed.ReasonCode != candidate.VersionReason {
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
		// Only the selected Japanese column crosses the FixedRevision boundary.
		// Exact API bytes remain private revision evidence; romanized columns are
		// transient parser input and are discarded here.
		Wikitext: fixedWikitext, Lines: legacyExtractedLines(parsed.Full.Lines), Extraction: parsed.Full,
		Section: parsed.Section, RenditionKey: parsed.RenditionKey, VersionReason: parsed.ReasonCode,
		IndexEvidenceRefs: cloneIndexEvidenceRefs(candidate.IndexEvidenceRefs),
		IndexEvidence:     cloneStrictIndexEvidence(candidate.IndexEvidence),
		FixedIdentities:   identities, Document: document,
	}, nil
}

func (provider *sekaipediaProvider) acquireFixedIndex(
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
			ProviderSekaipedia, sekaipediaAuthorityEvidenceID(fixed), page, page.rawResponse,
		)
		if err != nil {
			return fixedAuthorityAcquisition{}, err
		}
		if _, err := parseSekaipediaListAuthority(page.content); err != nil {
			return fixedAuthorityAcquisition{}, err
		}
		if err := workCtx.Err(); err != nil {
			return fixedAuthorityAcquisition{}, err
		}
		return fixedAuthorityAcquisition{page: page, evidence: evidence}, nil
	})
}

func (provider *sekaipediaProvider) fetchFixedIndex(ctx context.Context, fixed FixedIndex) (wikiPage, error) {
	page, err := provider.fetchExactRevision(ctx, fixed.RevisionID, false)
	if err != nil {
		return wikiPage{}, err
	}
	if page.pageID != fixed.PageID || page.revisionID != fixed.RevisionID || page.sha1 != fixed.SHA1 ||
		canonicalFetchedAt(page.revisionTimestamp) != fixed.RevisionTimestamp ||
		fmt.Sprintf("%x", sha1.Sum([]byte(page.content))) != fixed.SHA1 {
		return wikiPage{}, ErrRevisionChanged
	}
	if fixed.ContentSHA256 != "" && page.rawSHA256 != fixed.ContentSHA256 {
		return wikiPage{}, ErrRevisionChanged
	}
	return page, nil
}

func (provider *sekaipediaProvider) fetchExactRevision(ctx context.Context, revisionID int, cacheable bool) (wikiPage, error) {
	if ctx == nil || revisionID <= 0 {
		return wikiPage{}, ErrMalformedResponse
	}
	params := sekaipediaPageRequestParams()
	params.Set("revids", strconv.Itoa(revisionID))
	return provider.fetchPage(ctx, params, cacheable)
}

func (provider *sekaipediaProvider) fetchPageByTitle(ctx context.Context, title string, cacheable bool) (wikiPage, error) {
	if ctx == nil || strings.TrimSpace(title) == "" || strings.TrimSpace(title) != title {
		return wikiPage{}, ErrMalformedResponse
	}
	params := sekaipediaPageRequestParams()
	params.Set("titles", title)
	params.Set("redirects", "1")
	params.Set("rvlimit", "1")
	return provider.fetchPage(ctx, params, cacheable)
}

func (provider *sekaipediaProvider) fetchPage(ctx context.Context, params url.Values, cacheable bool) (wikiPage, error) {
	data, fetchedAt, err := provider.client.requestWithFetchedAt(ctx, "page", params, cacheable)
	if err != nil {
		return wikiPage{}, err
	}
	page, err := parseAcquiredPageResponse(data, fetchedAt)
	if err != nil || page.revisionTimestamp.IsZero() || page.revisionTimestamp.After(page.fetchedAt) {
		return wikiPage{}, ErrMalformedResponse
	}
	if fmt.Sprintf("%x", sha1.Sum([]byte(page.content))) != page.sha1 {
		return wikiPage{}, ErrRevisionChanged
	}
	return page, nil
}

func sekaipediaPageRequestParams() url.Values {
	return url.Values{
		"action": {"query"}, "format": {"json"}, "formatversion": {"2"},
		"prop": {"revisions|categories"}, "rvprop": {"ids|timestamp|sha1|content"},
		"rvslots": {"main"}, "cllimit": {"max"}, "maxlag": {mediaWikiMaxLag},
	}
}

// sekaipediaFixedJapaneseWikitext serializes only the selected Japanese lyric
// column. Stanza boundaries are represented by one additional newline; no
// source templates, translations, or transient romanization cross this private
// fixed-artifact boundary.
func sekaipediaFixedJapaneseWikitext(lines []StructuredLine) []byte {
	if len(lines) == 0 || len(lines) > maxExtractedLines {
		return nil
	}
	var result strings.Builder
	textBytes := 0
	for index, line := range lines {
		if line.Japanese == "" || strings.TrimSpace(line.Japanese) == "" ||
			len(line.Japanese) > maxExtractedLineBytes || !utf8.ValidString(line.Japanese) ||
			strings.ContainsAny(line.Japanese, "\r\n") || (index == 0 && line.StanzaBreakBefore) {
			return nil
		}
		if index > 0 {
			result.WriteByte('\n')
			if line.StanzaBreakBefore {
				result.WriteByte('\n')
			}
		}
		textBytes += len(line.Japanese)
		if textBytes > maxExtractedTextBytes {
			return nil
		}
		result.WriteString(line.Japanese)
		if result.Len() > maxSekaipediaFixedJapaneseWikitextBytes {
			return nil
		}
	}
	return []byte(result.String())
}

// SekaipediaFixedJapaneseWikitext exposes the single canonical minimized-byte
// representation used by preflight and staging. The returned bytes contain no
// translation or romanization columns.
func SekaipediaFixedJapaneseWikitext(lines []StructuredLine) []byte {
	return sekaipediaFixedJapaneseWikitext(lines)
}

// catalogIdentityMatches applies only the provider's reviewed, song-scoped
// contributor alias plan after the exact catalog identity fails. Titles and
// music IDs remain unchanged, and an empty plan adds no implicit exceptions.
func (provider *sekaipediaProvider) catalogIdentityMatches(
	content string,
	pageTitle string,
	identity MusicIdentity,
) bool {
	if sekaipediaCatalogIdentityMatches(content, pageTitle, identity) ||
		provider.recoveryExactCatalogIdentityMatches(content, pageTitle, identity) {
		return true
	}
	aliased := identity
	aliased.ProducerMetadata = sekaipediaProviderCreditAliases(
		identity.MusicID, identity.ProducerMetadata, provider.config.ContributorAliases,
	)
	aliased.Lyricist = sekaipediaProviderCreditAliases(
		identity.MusicID, identity.Lyricist, provider.config.ContributorAliases,
	)
	aliased.Composer = sekaipediaProviderCreditAliases(
		identity.MusicID, identity.Composer, provider.config.ContributorAliases,
	)
	aliased.Arranger = sekaipediaProviderCreditAliases(
		identity.MusicID, identity.Arranger, provider.config.ContributorAliases,
	)
	if aliased == identity {
		return false
	}
	return sekaipediaCatalogIdentityMatches(content, pageTitle, aliased)
}

func sekaipediaProviderCreditAliases(
	musicID int,
	value string,
	aliases []ProviderContributorAlias,
) string {
	contributors, ok := splitTopLevelContributors(value)
	if !ok || musicID <= 0 || len(aliases) == 0 {
		return value
	}
	changed := false
	for index, contributor := range contributors {
		key := normalizeTitle(contributor)
		for _, alias := range aliases {
			aliasKey, valid := providerContributorAliasKey(alias.CatalogContributor)
			providerKey, providerValid := providerContributorAliasKey(alias.ProviderContributor)
			if valid && providerValid && alias.MusicID == musicID && aliasKey == key && aliasKey != providerKey {
				contributors[index] = alias.ProviderContributor
				changed = true
				break
			}
		}
	}
	if !changed {
		return value
	}
	return strings.Join(contributors, " & ")
}

func validSekaipediaCandidate(candidate Candidate) bool {
	return validSekaipediaCandidateForAuthority(candidate, FixedIndex{})
}

func validSekaipediaCandidateForAuthority(candidate Candidate, authority FixedIndex) bool {
	if candidate.Provider != ProviderSekaipedia || candidate.Origin != OriginSekaipedia || candidate.PageID <= 0 ||
		candidate.RevisionID <= 0 || !HasCanonicalSHA1(candidate.SHA1) || candidate.Title == "" ||
		strings.TrimSpace(candidate.Title) != candidate.Title ||
		(candidate.RawSHA256 != "" && !canonicalIndexEvidenceSHA256.MatchString(candidate.RawSHA256)) ||
		candidate.CanonicalURL != canonicalRevisionURL(ProviderSekaipedia, candidate.Title, candidate.RevisionID) ||
		candidate.Categories == nil || candidate.Section == "" || strings.TrimSpace(candidate.Section) != candidate.Section ||
		!model.IsValidLyricsSourceCandidateVersionReasonCode(candidate.VersionReason) ||
		(candidate.RenditionKey != "full-original" && candidate.RenditionKey != "full-sekai" &&
			candidate.RenditionKey != "full-vocaloid" && candidate.RenditionKey != "game-sekai" &&
			candidate.RenditionKey != "game-vocaloid") {
		return false
	}
	revisionTimestamp, revisionErr := time.Parse(time.RFC3339Nano, candidate.RevisionTimestamp)
	if revisionErr != nil || candidate.RevisionTimestamp == "" ||
		canonicalFetchedAt(revisionTimestamp) != candidate.RevisionTimestamp {
		return false
	}
	if candidate.FetchedAt != "" {
		fetchedAt, fetchedErr := time.Parse(time.RFC3339Nano, candidate.FetchedAt)
		if fetchedErr != nil || canonicalFetchedAt(fetchedAt) != candidate.FetchedAt || revisionTimestamp.After(fetchedAt) {
			return false
		}
	}
	for index, category := range candidate.Categories {
		if category == "" || strings.TrimSpace(category) != category || (index > 0 && candidate.Categories[index-1] >= category) {
			return false
		}
	}
	if sekaipediaAuthorityEvidenceID(authority) == "" {
		return ValidateCandidateIndexEvidence(candidate) == nil
	}
	return ValidateCandidateIndexEvidenceAgainstSekaipediaAuthority(candidate, authority) == nil
}

func mergeSekaipediaProjectedGamePerformerSegmentation(
	full, game Extraction,
	mapping []int,
) (Extraction, error) {
	if len(mapping) == 0 || len(mapping) != len(game.Lines) {
		return Extraction{}, ErrUnsupportedTable
	}
	result := full
	result.Performers = append([]Performer{}, full.Performers...)
	result.Lines = make([]StructuredLine, len(full.Lines))
	for index, line := range full.Lines {
		result.Lines[index] = cloneStructuredLine(line)
	}

	usedGamePerformerIDs := map[string]struct{}{}
	mergedRuby := false
	for gameIndex, fullIndex := range mapping {
		if fullIndex < 0 || fullIndex >= len(result.Lines) ||
			game.Lines[gameIndex].Japanese != result.Lines[fullIndex].Japanese {
			return Extraction{}, ErrUnsupportedTable
		}
		if structuredLineHasPerformerEvidence(result.Lines[fullIndex]) ||
			!structuredLineHasPerformerEvidence(game.Lines[gameIndex]) {
			continue
		}
		stanzaBreakBefore := result.Lines[fullIndex].StanzaBreakBefore
		result.Lines[fullIndex] = cloneStructuredLine(game.Lines[gameIndex])
		result.Lines[fullIndex].StanzaBreakBefore = stanzaBreakBefore
		for _, performerID := range result.Lines[fullIndex].TrailingPerformerIDs {
			usedGamePerformerIDs[performerID] = struct{}{}
		}
		for _, segment := range result.Lines[fullIndex].Segments {
			for _, performerID := range segment.PerformerIDs {
				usedGamePerformerIDs[performerID] = struct{}{}
			}
			for _, span := range segment.Ruby {
				mergedRuby = mergedRuby || span.Reading != ""
			}
		}
	}
	if len(usedGamePerformerIDs) == 0 {
		return result, nil
	}
	if mergedRuby && game.RubyGeneratorVersion != "" {
		switch {
		case result.RubyGeneratorVersion == "":
			result.RubyGeneratorVersion = game.RubyGeneratorVersion
		case result.RubyGeneratorVersion != game.RubyGeneratorVersion:
			return Extraction{}, ErrUnsupportedTable
		}
	}

	performersByID := make(map[string]Performer, len(result.Performers)+len(game.Performers))
	for _, performer := range result.Performers {
		performersByID[performer.PerformerID] = performer
	}
	for _, performer := range game.Performers {
		if _, used := usedGamePerformerIDs[performer.PerformerID]; !used {
			continue
		}
		if existing, found := performersByID[performer.PerformerID]; found {
			if existing != performer {
				return Extraction{}, ErrUnsupportedTable
			}
			delete(usedGamePerformerIDs, performer.PerformerID)
			continue
		}
		result.Performers = append(result.Performers, performer)
		performersByID[performer.PerformerID] = performer
		delete(usedGamePerformerIDs, performer.PerformerID)
	}
	if len(usedGamePerformerIDs) != 0 {
		return Extraction{}, ErrUnsupportedTable
	}
	return result, nil
}

func structuredLineHasPerformerEvidence(line StructuredLine) bool {
	if len(line.TrailingPerformerIDs) != 0 {
		return true
	}
	for _, segment := range line.Segments {
		if len(segment.PerformerIDs) != 0 {
			return true
		}
	}
	return false
}

func buildSekaipediaDocumentV3(
	candidate Candidate,
	parsed sekaipediaSongExtraction,
	fetchedAt time.Time,
) ([]model.LyricsSourceFixedIdentity, *model.LyricsSourceDocument, error) {
	if fetchedAt.IsZero() || len(parsed.Renditions) == 0 {
		return nil, nil, ErrMissingLyrics
	}
	artifactKey := candidate.RenditionKey
	if artifactKey == "" {
		artifactKey = parsed.RenditionKey
	}
	artifactSection := candidate.Section
	if artifactSection == "" {
		artifactSection = parsed.Section
	}
	artifactReason := candidate.VersionReason
	if artifactReason == "" {
		artifactReason = parsed.ReasonCode
	}
	// One immutable page revision is one artifact even when it contributes to
	// several logical renditions. The contribution refs below establish the
	// many-to-many ownership without duplicating the raw acquisition identity.
	artifact := model.LyricsSourceFixedIdentity{
		Provider: ProviderSekaipedia, Origin: OriginSekaipedia,
		PageID: candidate.PageID, RevisionID: candidate.RevisionID, RevisionTimestamp: candidate.RevisionTimestamp,
		SHA1: candidate.SHA1, Title: candidate.Title, CanonicalURL: candidate.CanonicalURL,
		FetchedAt: canonicalFetchedAt(fetchedAt), Categories: cloneIdentityCategories(candidate.Categories),
		Section: artifactSection, RenditionKey: artifactKey, VersionReason: artifactReason,
		IndexEvidenceRefs: cloneIndexEvidenceRefs(candidate.IndexEvidenceRefs),
	}
	if err := model.ValidateLyricsSourceFixedIdentity(artifact); err != nil {
		return nil, nil, fmt.Errorf("validate sekaipedia v3 artifact: %w", err)
	}
	artifactRef := model.LyricsSourceComponentRef{RenditionKey: artifact.RenditionKey}

	peers := append([]sekaipediaPeerRenditionExtraction(nil), parsed.Renditions...)
	for _, alternate := range parsed.AlternateVocals {
		sourcePerformerIDs := append([]string(nil), alternate.SingerIDs...)
		sort.Strings(sourcePerformerIDs)
		peer := sekaipediaPeerRenditionExtraction{
			RenditionKey:           "alternate-" + alternate.Key,
			Kind:                   "alternate",
			SourcePerformerIDs:     sourcePerformerIDs,
			Full:                   alternate.Full,
			Game:                   alternate.Game,
			FullStructuredEvidence: alternate.FullStructuredEvidence,
			GameStructuredEvidence: alternate.GameStructuredEvidence,
			fullProjectionLines:    append([]sekaipediaColumnLine(nil), alternate.fullProjectionLines...),
			gameProjectionLines:    append([]sekaipediaColumnLine(nil), alternate.gameProjectionLines...),
		}
		if alternate.Full != nil {
			if len(alternate.FullTabPath) == 0 {
				return nil, nil, ErrUnsupportedTable
			}
			peer.SourceTabPaths = append(peer.SourceTabPaths, append(model.LyricsSourceTabPath{}, alternate.FullTabPath...))
			peer.FullSection = "Lyrics/" + strings.Join([]string(alternate.FullTabPath), "/")
		}
		if alternate.Game != nil {
			if len(alternate.GameTabPath) == 0 {
				return nil, nil, ErrUnsupportedTable
			}
			peer.SourceTabPaths = append(peer.SourceTabPaths, append(model.LyricsSourceTabPath{}, alternate.GameTabPath...))
			peer.GameSection = "Lyrics/" + strings.Join([]string(alternate.GameTabPath), "/")
		}
		switch {
		case peer.Full != nil && peer.Game != nil:
			peer.ReasonCode = model.LyricsSourceVersionReasonTaggedFullAndGame
			if mapping, err := sekaipediaResolveProjection(peer.fullProjectionLines, peer.gameProjectionLines); err == nil {
				fullSelection := sekaipediaRenditionExtraction{extraction: *peer.Full}
				gameSelection := sekaipediaRenditionExtraction{extraction: *peer.Game}
				if sekaipediaExactGameProjectionCompatible(fullSelection, gameSelection, mapping) {
					peer.GameLineIndexes = mapping
				}
			}
		case peer.Full != nil:
			peer.ReasonCode = model.LyricsSourceVersionReasonUntaggedFullOnly
		case peer.Game != nil:
			peer.ReasonCode = model.LyricsSourceVersionReasonTaggedGameOnly
		default:
			return nil, nil, ErrMissingLyrics
		}
		peers = append(peers, peer)
	}
	sort.Slice(peers, func(left, right int) bool { return peers[left].RenditionKey < peers[right].RenditionKey })

	renditions := make([]model.LyricsSourceRendition, 0, len(peers))
	seenKeys := map[string]struct{}{}
	for _, peer := range peers {
		if _, duplicate := seenKeys[peer.RenditionKey]; duplicate {
			return nil, nil, ErrAmbiguous
		}
		seenKeys[peer.RenditionKey] = struct{}{}
		rendition := model.LyricsSourceRendition{
			RenditionKey:       peer.RenditionKey,
			SourceKind:         model.LyricsSourceRenditionKind(peer.Kind),
			SourceTabPaths:     cloneSekaipediaTabPaths(peer.SourceTabPaths),
			ReasonCode:         peer.ReasonCode,
			SourcePerformerIDs: append([]string(nil), peer.SourcePerformerIDs...),
			FullPerformerEvidence: sekaipediaModelPerformerEvidenceStateForExtraction(
				peer.FullStructuredEvidence, peer.Full, peer.SourcePerformerIDs,
			),
			GamePerformerEvidence: sekaipediaModelPerformerEvidenceStateForExtraction(
				peer.GameStructuredEvidence, peer.Game, peer.SourcePerformerIDs,
			),
			Relation: model.LyricsSourceRenditionRelation{Kind: model.LyricsSourceRenditionRelationNone},
			Provenance: model.LyricsSourceRenditionProvenance{
				RelationEvidence: artifactRef, VersionEvidence: artifactRef,
			},
		}
		if peer.Full != nil {
			persisted, hasSegmentation := extractionForSourceDocument(*peer.Full, true)
			full := extractionToModelFullV3(
				persisted, artifact.RenditionKey, peer.RenditionKey, model.LyricsSourceRenditionSideFull,
			)
			rendition.Full = &full
			rendition.Provenance.FullText = cloneSekaipediaComponentRef(artifactRef)
			if hasSegmentation {
				rendition.Provenance.FullPerformerSegmentation = cloneSekaipediaComponentRef(artifactRef)
			}
			if hasExtractionSourceRubyReading(persisted) {
				rendition.Provenance.FullRuby = cloneSekaipediaComponentRef(artifactRef)
			}
		}
		if peer.Game != nil {
			persisted, hasSegmentation := extractionForSourceDocument(*peer.Game, true)
			game := extractionToModelFullV3(
				persisted, artifact.RenditionKey, peer.RenditionKey, model.LyricsSourceRenditionSideGame,
			)
			for index := range game.Lines {
				game.Lines[index].ID = fmt.Sprintf("game-%06d", index+1)
			}
			rendition.Game = &game
			rendition.Provenance.GameText = cloneSekaipediaComponentRef(artifactRef)
			if hasSegmentation {
				rendition.Provenance.GamePerformerSegmentation = cloneSekaipediaComponentRef(artifactRef)
			}
			if hasExtractionSourceRubyReading(persisted) {
				rendition.Provenance.GameRuby = cloneSekaipediaComponentRef(artifactRef)
			}
		}
		if len(peer.GameLineIndexes) != 0 {
			if rendition.Full == nil {
				return nil, nil, ErrUnsupportedTable
			}
			lineIDs := make([]string, len(peer.GameLineIndexes))
			for index, position := range peer.GameLineIndexes {
				if position < 0 || position >= len(rendition.Full.Lines) {
					return nil, nil, ErrUnsupportedTable
				}
				lineIDs[index] = rendition.Full.Lines[position].ID
			}
			rendition.Relation = model.LyricsSourceRenditionRelation{
				Kind:             model.LyricsSourceRenditionRelationExactProjection,
				FullRenditionKey: peer.RenditionKey,
				LineIDs:          lineIDs,
			}
		}
		if sekaipediaRenditionHasCompleteStructuredEvidence(rendition) {
			rendition.PrivateReview = &model.LyricsSourcePrivateReview{
				PerformerSegmentationEvidence: model.LyricsSourcePerformerSegmentationEvidenceAuthoritativeCompleteStructured,
			}
		}
		if err := model.ValidateLyricsSourceRenditionPayload(rendition); err != nil {
			return nil, nil, fmt.Errorf("validate sekaipedia v3 rendition %q: %w", peer.RenditionKey, err)
		}
		renditions = append(renditions, rendition)
	}
	document := &model.LyricsSourceDocument{
		SchemaVersion:   model.LyricsSourceDocumentSchemaVersionV3,
		FixedIdentities: []model.LyricsSourceFixedIdentity{artifact},
		Renditions:      renditions,
	}
	if err := model.ValidateLyricsSourceDocument(*document); err != nil {
		return nil, nil, fmt.Errorf("validate sekaipedia lyrics source v3 document: %w", err)
	}
	return []model.LyricsSourceFixedIdentity{artifact}, document, nil
}

func cloneSekaipediaTabPaths(input []model.LyricsSourceTabPath) []model.LyricsSourceTabPath {
	result := make([]model.LyricsSourceTabPath, len(input))
	for index, path := range input {
		result[index] = append(model.LyricsSourceTabPath{}, path...)
	}
	return result
}

func cloneSekaipediaComponentRef(reference model.LyricsSourceComponentRef) *model.LyricsSourceComponentRef {
	copy := reference
	return &copy
}

func sekaipediaModelPerformerEvidenceState(
	state sekaipediaPerformerEvidenceState,
) model.LyricsSourcePerformerEvidenceState {
	switch state {
	case sekaipediaPerformerEvidencePartial:
		return model.LyricsSourcePerformerEvidenceSourcePartial
	case sekaipediaPerformerEvidenceComplete:
		return model.LyricsSourcePerformerEvidenceSourceComplete
	default:
		return model.LyricsSourcePerformerEvidenceNone
	}
}

// sekaipediaModelPerformerEvidenceStateForExtraction keeps parser compatibility
// separate from the stricter v3 persisted contract. A parser can establish that
// every referenced singer was witnessed in the source while still producing a
// concrete Extraction whose performer registry is narrower than the declared
// source roster. Persisting that side as complete would be rejected by v3 and
// would overstate what the concrete artifact proves, so downgrade only that
// persisted side to source-partial.
func sekaipediaModelPerformerEvidenceStateForExtraction(
	state sekaipediaPerformerEvidenceState,
	extraction *Extraction,
	sourcePerformerIDs []string,
) model.LyricsSourcePerformerEvidenceState {
	mapped := sekaipediaModelPerformerEvidenceState(state)
	if mapped == model.LyricsSourcePerformerEvidenceSourceComplete &&
		!sekaipediaExtractionPerformerRosterMatchesIDs(extraction, sourcePerformerIDs) {
		return model.LyricsSourcePerformerEvidenceSourcePartial
	}
	return mapped
}

func sekaipediaRenditionHasCompleteStructuredEvidence(
	rendition model.LyricsSourceRendition,
) bool {
	hasEvidence := false
	if rendition.Full != nil {
		if rendition.FullPerformerEvidence != model.LyricsSourcePerformerEvidenceSourceComplete {
			return false
		}
		hasEvidence = true
	}
	if rendition.Game != nil {
		if rendition.GamePerformerEvidence != model.LyricsSourcePerformerEvidenceSourceComplete {
			return false
		}
		hasEvidence = true
	}
	return hasEvidence
}

func buildSekaipediaDocument(
	candidate Candidate,
	parsed sekaipediaSongExtraction,
	fetchedAt time.Time,
) ([]model.LyricsSourceFixedIdentity, *model.LyricsSourceDocument, error) {
	if len(parsed.Renditions) != 0 {
		return buildSekaipediaDocumentV3(candidate, parsed, fetchedAt)
	}
	gameOnly := strings.HasPrefix(parsed.RenditionKey, "game-")
	if fetchedAt.IsZero() ||
		(!gameOnly && (len(parsed.Full.Lines) == 0 || parsed.RenditionKey != fullRenditionKey(parsed.Full.Version.Kind))) ||
		(gameOnly && (parsed.Game == nil || len(parsed.Game.Lines) == 0)) {
		return nil, nil, ErrMissingLyrics
	}
	fullIdentity := model.LyricsSourceFixedIdentity{
		Provider: ProviderSekaipedia, Origin: OriginSekaipedia,
		PageID: candidate.PageID, RevisionID: candidate.RevisionID, RevisionTimestamp: candidate.RevisionTimestamp,
		SHA1: candidate.SHA1, Title: candidate.Title, CanonicalURL: candidate.CanonicalURL,
		FetchedAt: canonicalFetchedAt(fetchedAt), Categories: cloneIdentityCategories(candidate.Categories),
		Section: parsed.Section, RenditionKey: parsed.RenditionKey, VersionReason: parsed.ReasonCode,
		IndexEvidenceRefs: cloneIndexEvidenceRefs(candidate.IndexEvidenceRefs),
	}
	if err := model.ValidateLyricsSourceFixedIdentity(fullIdentity); err != nil {
		return nil, nil, fmt.Errorf("validate sekaipedia fixed identity: %w", err)
	}
	identities := []model.LyricsSourceFixedIdentity{fullIdentity}
	fullRef := model.LyricsSourceComponentRef{RenditionKey: fullIdentity.RenditionKey}
	provenance := model.LyricsSourceComponentProvenance{VersionEvidence: fullRef}
	var full model.LyricsSourceFull
	var game *model.LyricsSourceFull
	var gameKey string
	// Full and Game are independent source regions. In particular, Game-only
	// performer attribution must never be copied into an unsegmented Full line,
	// even when the text relation is an exact projection.
	var persistedExtraction Extraction
	var hasSegmentation bool
	if gameOnly {
		persistedExtraction, hasSegmentation = extractionForSourceDocument(
			*parsed.Game, parsed.AuthoritativeStructured || parsed.Game.Version.Kind != "vocaloid",
		)
		gameValue := extractionToModelFull(persistedExtraction)
		for index := range gameValue.Lines {
			gameValue.Lines[index].ID = fmt.Sprintf("game-%06d", index+1)
		}
		game = &gameValue
		provenance.GameText = &fullRef
		if hasSegmentation {
			ref := fullRef
			provenance.PerformerSegmentation = &ref
		}
		if hasExtractionRubyReading(persistedExtraction) {
			provenance.Ruby = &fullRef
		}
	} else {
		persistedExtraction, hasSegmentation = extractionForSourceDocument(
			parsed.Full, parsed.AuthoritativeStructured || parsed.Full.Version.Kind != "vocaloid",
		)
		fullValue := extractionToModelFull(persistedExtraction)
		full = fullValue
		provenance.FullText = fullRef
		if hasSegmentation {
			ref := fullRef
			provenance.PerformerSegmentation = &ref
		}
		if hasExtractionRubyReading(persistedExtraction) {
			ref := fullRef
			provenance.Ruby = &ref
		}
	}
	var gameProjection *model.LyricsSourceGameProjection
	if !gameOnly && parsed.Game != nil {
		gameKey = "game-" + parsed.Full.Version.Kind
		gameIdentity := fullIdentity
		gameIdentity.Section = parsed.GameSection
		gameIdentity.RenditionKey = gameKey
		if err := model.ValidateLyricsSourceFixedIdentity(gameIdentity); err != nil {
			return nil, nil, fmt.Errorf("validate sekaipedia Game fixed identity: %w", err)
		}
		identities = append(identities, gameIdentity)
		gameExtraction, _ := extractionForSourceDocument(
			*parsed.Game, parsed.AuthoritativeStructured || parsed.Game.Version.Kind != "vocaloid",
		)
		gameValue := extractionToModelFull(gameExtraction)
		for index := range gameValue.Lines {
			gameValue.Lines[index].ID = fmt.Sprintf("game-%06d", index+1)
		}
		game = &gameValue
		gameRef := model.LyricsSourceComponentRef{RenditionKey: gameKey}
		provenance.GameText = &gameRef
	}
	if len(parsed.GameLineIndexes) > 0 {
		if gameOnly || len(full.Lines) == 0 {
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
		projectionKey := gameKey
		if projectionKey == "" {
			// An untagged same-lyrics identity has no independent Game artifact;
			// its exact identity projection is a relation on the Full rendition.
			projectionKey = fullIdentity.RenditionKey
		}
		ref := model.LyricsSourceComponentRef{RenditionKey: projectionKey}
		provenance.GameProjection = &ref
	}
	alternateVocals := make([]model.LyricsSourceAlternateVocal, 0, len(parsed.AlternateVocals))
	for _, alternate := range parsed.AlternateVocals {
		if alternate.Key == "" || len(alternate.SingerIDs) == 0 || alternate.Full == nil && alternate.Game == nil {
			continue
		}
		logicalKey := "alternate-" + alternate.Key
		alternateDocument := model.LyricsSourceAlternateVocal{
			TabLabel: alternate.TabLabel, SingerLabel: alternate.SingerLabel,
			SingerIDs: append([]string{}, alternate.SingerIDs...),
		}
		if alternate.Full != nil {
			fullKey := "alternate-full-" + alternate.Key
			identity := fullIdentity
			identity.Section = "Lyrics/" + alternate.TabLabel + " (Full)/" + alternate.SingerLabel
			identity.RenditionKey = fullKey
			identity.CompositionRenditionKey = logicalKey
			identity.VersionReason = model.LyricsSourceVersionReasonUntaggedFullOnly
			if err := model.ValidateLyricsSourceFixedIdentity(identity); err != nil {
				return nil, nil, fmt.Errorf("validate sekaipedia alternate Full fixed identity: %w", err)
			}
			identities = append(identities, identity)
			converted := extractionToModelFull(*alternate.Full)
			alternateDocument.Full = &converted
			ref := model.LyricsSourceComponentRef{RenditionKey: fullKey}
			alternateDocument.Provenance.FullText = &ref
			alternateDocument.Provenance.VersionEvidence = ref
		}
		if alternate.Game != nil {
			gameKey := "alternate-game-" + alternate.Key
			identity := fullIdentity
			identity.Section = "Lyrics/" + alternate.TabLabel + "/" + alternate.SingerLabel
			identity.RenditionKey = gameKey
			identity.CompositionRenditionKey = logicalKey
			identity.VersionReason = model.LyricsSourceVersionReasonUntaggedFullOnly
			if err := model.ValidateLyricsSourceFixedIdentity(identity); err != nil {
				return nil, nil, fmt.Errorf("validate sekaipedia alternate Game fixed identity: %w", err)
			}
			identities = append(identities, identity)
			converted := extractionToModelFull(*alternate.Game)
			alternateDocument.Game = &converted
			ref := model.LyricsSourceComponentRef{RenditionKey: gameKey}
			alternateDocument.Provenance.GameText = &ref
			if alternateDocument.Provenance.VersionEvidence.RenditionKey == "" {
				alternateDocument.Provenance.VersionEvidence = ref
			}
		}
		if alternate.Full != nil && alternate.Game != nil &&
			alternateDocument.Provenance.FullText != nil && alternateDocument.Provenance.GameText != nil {
			mapping, projectionErr := sekaipediaResolveProjection(
				alternate.fullProjectionLines, alternate.gameProjectionLines,
			)
			if projectionErr == nil && len(mapping) == len(alternateDocument.Game.Lines) {
				lineIDs := make([]string, len(mapping))
				for index, position := range mapping {
					if position < 0 || position >= len(alternateDocument.Full.Lines) {
						return nil, nil, ErrUnsupportedTable
					}
					lineIDs[index] = alternateDocument.Full.Lines[position].ID
				}
				alternateDocument.GameProjection = &model.LyricsSourceGameProjection{LineIDs: lineIDs}
				ref := *alternateDocument.Provenance.GameText
				alternateDocument.Provenance.GameProjection = &ref
			}
		}
		alternateVocals = append(alternateVocals, alternateDocument)
	}
	document := &model.LyricsSourceDocument{
		SchemaVersion: model.LyricsSourceDocumentSchemaVersion, ReasonCode: parsed.ReasonCode,
		FixedIdentities: identities, Provenance: provenance, Full: full, Game: game,
		GameProjection: gameProjection, AlternateVocals: alternateVocals,
	}
	if parsed.AuthoritativeStructured {
		document.PrivateReview = &model.LyricsSourcePrivateReview{
			PerformerSegmentationEvidence: model.LyricsSourcePerformerSegmentationEvidenceAuthoritativeCompleteStructured,
		}
	}
	if err := model.ValidateLyricsSourceDocument(*document); err != nil {
		return nil, nil, fmt.Errorf("validate sekaipedia lyrics source document: %w", err)
	}
	return identities, document, nil
}
