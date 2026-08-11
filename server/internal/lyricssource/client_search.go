package lyricssource

import (
	"context"

	"html"

	"sort"
	"strings"

	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"moesekai/server/internal/model"
)

func (c *Client) Search(ctx context.Context, identity MusicIdentity) ([]Candidate, error) {
	result, _, err := c.SearchWithDiagnostics(ctx, identity)
	return result, err
}

// SearchWithDiagnostics applies the exact production candidate gates and the
// same bounded title-query and authoritative creator-alias recovery as Search,
// while returning only aggregate rejection counts for read-only coverage
// analysis.
func (c *Client) SearchWithDiagnostics(ctx context.Context, identity MusicIdentity) ([]Candidate, SearchDiagnostics, error) {
	queries := titleSearchQueries(identity.JapaneseTitle)
	if len(queries) == 0 {
		return nil, SearchDiagnostics{}, ErrMalformedResponse
	}
	var (
		pages       []wikiPage
		diagnostics SearchDiagnostics
	)
	for _, query := range queries {
		queriedPages, err := c.querySearchPages(ctx, query)
		if err != nil {
			return nil, SearchDiagnostics{}, err
		}
		// The first observation for an exact page revision is authoritative for
		// diagnostics. A later query variant cannot overwrite an earlier
		// restricted or mismatched outcome for the same PageID+RevisionID.
		pages, err = mergeSearchPages(pages, queriedPages)
		if err != nil {
			return nil, SearchDiagnostics{}, err
		}
		if err := ctx.Err(); err != nil {
			return nil, SearchDiagnostics{}, err
		}
		result, evaluated, err := evaluateSearchPages(identity, pages, nil)
		if err != nil {
			return nil, evaluated, err
		}
		diagnostics = evaluated
		if err := ctx.Err(); err != nil {
			return nil, SearchDiagnostics{}, err
		}
		if len(result) > 0 {
			return result, diagnostics, nil
		}
	}

	// Alias recovery is a single bounded phase over the merged page set. Each
	// page must resolve all of its own aliases; failure on a noisy page does not
	// prevent an independent page from being recovered.
	return c.searchPagesWithCreatorAliases(ctx, identity, pages)
}

type searchPageKey struct {
	pageID     int
	revisionID int
}

func pageSearchKey(page wikiPage) searchPageKey {
	return searchPageKey{pageID: page.pageID, revisionID: page.revisionID}
}

func mergeSearchPages(existing, additional []wikiPage) ([]wikiPage, error) {
	merged := make(map[searchPageKey]wikiPage, len(existing)+len(additional))
	for _, page := range existing {
		merged[pageSearchKey(page)] = page
	}
	for _, page := range additional {
		key := pageSearchKey(page)
		if current, found := merged[key]; found {
			// Keep the first observation for diagnostics when the same revision is
			// returned by a different canonical query. Only a shared evidence ID
			// claims to be the same acquisition and therefore must resolve to the
			// exact same immutable envelope.
			if conflictingIndexEvidence(current.indexEvidence, page.indexEvidence) {
				return nil, ErrAmbiguous
			}
			continue
		}
		merged[key] = page
	}
	pages := make([]wikiPage, 0, len(merged))
	for _, page := range merged {
		pages = append(pages, page)
	}
	sort.Slice(pages, func(i, j int) bool {
		if pages[i].pageID != pages[j].pageID {
			return pages[i].pageID < pages[j].pageID
		}
		return pages[i].revisionID < pages[j].revisionID
	})
	return pages, nil
}

func (c *Client) querySearchPages(ctx context.Context, query string) ([]wikiPage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	params := searchQueryRequestParams(query)
	data, fetchedAt, err := c.requestWithFetchedAt(ctx, "search", params, true)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pages, err := parseSearchResponse(data)
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, contextErr
	}
	if err != nil {
		return nil, err
	}
	requestURL, err := canonicalMediaWikiRequestURL(vocaloidWikiAPI, params)
	if err != nil {
		return nil, err
	}
	if len(pages) == 0 {
		return pages, nil
	}
	evidence, err := newFandomSearchIndexEvidence(requestURL, fetchedAt, data)
	if err != nil {
		return nil, err
	}
	for index := range pages {
		pages[index].fetchedAt = fetchedAt
		pages[index].indexEvidenceRefs = []model.LyricsSourceIndexEvidenceRef{{
			EvidenceID: evidence.EvidenceID, SHA256: evidence.SHA256,
		}}
		pages[index].indexEvidence = []IndexEvidence{evidence}
	}
	return pages, nil
}

func evaluateSearchPages(identity MusicIdentity, pages []wikiPage, aliasRecovered map[searchPageKey]bool) ([]Candidate, SearchDiagnostics, error) {
	diagnostics := SearchDiagnostics{SearchHits: len(pages)}
	result := make([]Candidate, 0, len(pages))
	for _, page := range pages {
		titleMatched := candidateTitleMatches(page.title, identity.JapaneseTitle)
		if hasLyricsTextRestriction(page.content, page.categories) {
			diagnostics.Restricted++
			if titleMatched {
				diagnostics.RestrictedTitleMatch++
			}
			continue
		}
		outcome := candidateVerification(identity, page.title, page.content, page.categories)
		if outcome == candidateCreditMismatch && aliasRecovered[pageSearchKey(page)] {
			outcome = candidateVerified
		}
		switch outcome {
		case candidateVerified:
			diagnostics.Verified++
			section, renditionKey, reason, err := fandomRenditionIdentity(identity, page.content, page.categories)
			if err != nil {
				return nil, diagnostics, err
			}
			candidate := Candidate{
				Provider: ProviderVocaloidFandom, Origin: OriginVocaloidFandom,
				PageID: page.pageID, Title: page.title, CanonicalURL: canonicalURL(page.title, page.revisionID),
				RevisionID: page.revisionID, SHA1: page.sha1, Categories: append([]string{}, page.categories...),
				Section: section, RenditionKey: renditionKey, VersionReason: reason,
				IndexEvidenceRefs: cloneIndexEvidenceRefs(page.indexEvidenceRefs),
				IndexEvidence:     cloneIndexEvidence(page.indexEvidence),
			}
			if err := ValidateCandidateIndexEvidence(candidate); err != nil {
				return nil, diagnostics, err
			}
			result = append(result, candidate)
		case candidateTitleMismatch:
			diagnostics.TitleMismatch++
		case candidateCreditMismatch:
			diagnostics.CreditMismatch++
			addCreditDiagnostics(&diagnostics, identity, page.content)
		case candidateSignalMismatch:
			diagnostics.SignalMismatch++
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].PageID != result[j].PageID {
			return result[i].PageID < result[j].PageID
		}
		return result[i].RevisionID < result[j].RevisionID
	})
	return result, diagnostics, nil
}

type creatorAliasResolution struct {
	canonical string
	found     bool
}

func (c *Client) searchPagesWithCreatorAliases(ctx context.Context, identity MusicIdentity, pages []wikiPage) ([]Candidate, SearchDiagnostics, error) {
	aliasCache := map[string]creatorAliasResolution{}
	aliasLookups := 0
	recovered := map[searchPageKey]bool{}
	for _, page := range pages {
		if err := ctx.Err(); err != nil {
			return nil, SearchDiagnostics{}, err
		}
		if hasLyricsTextRestriction(page.content, page.categories) ||
			candidateVerification(identity, page.title, page.content, page.categories) != candidateCreditMismatch {
			continue
		}
		aliases, ok, err := c.resolveCreatorAliasesForPage(ctx, identity, page, aliasCache, &aliasLookups, false)
		if err != nil {
			return nil, SearchDiagnostics{}, err
		}
		if !ok || !roleBoundCreditsMatchWithAliases(identity, page.content, aliases) ||
			!hasSongSignal(identity.JapaneseTitle, page.content, page.categories) {
			continue
		}
		recovered[pageSearchKey(page)] = true
	}
	result, diagnostics, err := evaluateSearchPages(identity, pages, recovered)
	if err != nil {
		return nil, diagnostics, err
	}
	if err := ctx.Err(); err != nil {
		return nil, SearchDiagnostics{}, err
	}
	return result, diagnostics, nil
}

func (c *Client) resolveCreatorAliases(ctx context.Context, identity MusicIdentity, pages []wikiPage, fresh bool) (map[string]string, bool, error) {
	aliasCache := map[string]creatorAliasResolution{}
	aliasLookups := 0
	combined := map[string]string{}
	for _, page := range pages {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		aliases, ok, err := c.resolveCreatorAliasesForPage(ctx, identity, page, aliasCache, &aliasLookups, fresh)
		if err != nil || !ok {
			return nil, false, err
		}
		for key, canonical := range aliases {
			if previous, found := combined[key]; found && normalizeTitle(previous) != normalizeTitle(canonical) {
				return nil, false, nil
			}
			combined[key] = canonical
		}
	}
	return combined, len(combined) > 0, nil
}

func (c *Client) resolveCreatorAliasesForPage(ctx context.Context, identity MusicIdentity, page wikiPage, cache map[string]creatorAliasResolution, aliasLookups *int, fresh bool) (map[string]string, bool, error) {
	if hasLyricsTextRestriction(page.content, page.categories) || hasWrongEntityEvidence(page.categories) ||
		!candidateTitleMatches(page.title, identity.JapaneseTitle) {
		return nil, false, nil
	}
	unresolved, resolvable := unresolvedRoleCredits(identity, page.content)
	if !resolvable || len(unresolved) == 0 {
		return nil, false, nil
	}
	unresolvedByKey := make(map[string]roleBoundCreditExpectation, len(unresolved))
	creatorsByKey := map[string]string{}
	for _, expected := range unresolved {
		roleKey := creditAliasKey(expected.role, expected.credit)
		unresolvedByKey[roleKey] = expected
		creatorsByKey[normalizeTitle(expected.credit)] = expected.credit
	}
	if len(creatorsByKey) == 0 || len(creatorsByKey) > maxCreatorAliasLookups {
		return nil, false, nil
	}
	creatorKeys := make([]string, 0, len(creatorsByKey))
	for key := range creatorsByKey {
		creatorKeys = append(creatorKeys, key)
	}
	sort.Strings(creatorKeys)
	for _, key := range creatorKeys {
		if resolution, cached := cache[key]; cached {
			if !resolution.found {
				return nil, false, nil
			}
			continue
		}
		if aliasLookups == nil || *aliasLookups >= maxCreatorAliasLookups {
			return nil, false, nil
		}
		*aliasLookups++
		canonical, found, err := c.resolveCreatorAlias(ctx, creatorsByKey[key], fresh)
		if err != nil {
			return nil, false, err
		}
		cache[key] = creatorAliasResolution{canonical: canonical, found: found}
		if !found {
			return nil, false, nil
		}
	}
	aliases := make(map[string]string, len(unresolvedByKey))
	for key, expected := range unresolvedByKey {
		resolution, found := cache[normalizeTitle(expected.credit)]
		if !found || !resolution.found {
			return nil, false, nil
		}
		aliases[key] = resolution.canonical
	}
	return aliases, true, nil
}

func (c *Client) resolveCreatorAlias(ctx context.Context, creator string, fresh bool) (string, bool, error) {
	contributors, ok := splitTopLevelContributors(creator)
	if !ok || len(contributors) != 1 {
		return "", false, nil
	}
	creator = contributors[0]
	var data []byte
	var err error
	if fresh {
		data, err = c.requestFresh(ctx, "creator-alias", creatorSearchRequestParams(creator))
	} else {
		data, err = c.request(ctx, "creator-alias", creatorSearchRequestParams(creator), true)
	}
	if err != nil {
		return "", false, err
	}
	pages, err := parseSearchResponseWithLimit(data, maxCreatorAliasPages)
	if contextErr := ctx.Err(); contextErr != nil {
		return "", false, contextErr
	}
	if err != nil {
		return "", false, err
	}
	matches := []wikiPage{}
	for _, page := range pages {
		if producerAliasPageMatches(page, creator) {
			matches = append(matches, page)
		}
	}
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if len(matches) != 1 {
		return "", false, nil
	}
	canonical, ok := producerCanonicalIdentity(matches[0].title)
	if !ok {
		return "", false, nil
	}
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	return canonical, true, nil
}

func producerAliasPageMatches(page wikiPage, creator string) bool {
	canonical, ok := producerCanonicalIdentity(page.title)
	if !ok || !isJapaneseLatinAliasPair(creator, canonical) ||
		!hasExactCreatorAliasEvidence(producerIdentityMetadata(page.content), creator, canonical) {
		return false
	}
	for _, category := range page.categories {
		category = strings.ToLower(strings.TrimSpace(category))
		if category == "producer" || strings.HasSuffix(category, " producer") || strings.HasSuffix(category, " producers") ||
			strings.HasPrefix(category, "producer on ") {
			return true
		}
	}
	return false
}

func producerCanonicalIdentity(value string) (string, bool) {
	value = strings.TrimSpace(norm.NFKC.String(html.UnescapeString(value)))
	if base, disambiguator, ok := splitTrailingParenthetical(value); ok {
		switch normalizeTitle(disambiguator) {
		case "producer", "musicproducer", "vocaloidproducer":
			value = base
		}
	}
	return singleContributorIdentity(value)
}

func singleContributorIdentity(value string) (string, bool) {
	contributors, ok := splitTopLevelContributors(value)
	if !ok || len(contributors) != 1 {
		return "", false
	}
	return contributors[0], true
}

func producerIdentityMetadata(content string) string {
	content = inactiveRestrictionMarkupPattern.ReplaceAllString(content, "")
	content = strings.ReplaceAll(content, "\r", "")
	for _, heading := range topLevelHeadingPattern.FindAllStringIndex(content, -1) {
		label := normalizeTitle(strings.Trim(content[heading[0]:heading[1]], "= \t\n"))
		switch label {
		case "affiliation", "affiliations", "labels", "externallinks":
			continue
		default:
			return content[:heading[0]]
		}
	}
	return content
}

func hasExactCreatorAliasEvidence(content, creator, canonical string) bool {
	for _, rawLine := range strings.Split(strings.ReplaceAll(content, "\r", ""), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "|"))
		if producerIntroductionAliasMatches(line, creator, canonical) {
			return true
		}
		separator := strings.IndexAny(line, "=:：")
		if separator < 0 {
			if containsExactIdentity(line, creator) && normalizeTitle(line) == normalizeTitle(creator) {
				return true
			}
			continue
		}
		label := normalizeTitle(line[:separator])
		switch label {
		case "alias", "aliases", "alsoknownas", "japanese", "japanesename", "native", "nativename",
			"romaji", "romanization", "romanized", "romanizedname", "english", "englishname",
			"別名", "日本語", "日本語名", "ローマ字", "英語", "英語名", "英名", "本名":
			if containsExactIdentity(line[separator+1:], creator) {
				return true
			}
		}
	}
	return false
}

func producerIntroductionAliasMatches(line, creator, canonical string) bool {
	line = foldIdentityPhrase(identityDisplayText(line))
	canonical = foldIdentityPhrase(canonical)
	if line == "" || canonical == "" || !strings.HasPrefix(line, canonical) ||
		!identityPhraseBoundary(line, 0, len(canonical)) {
		return false
	}
	evidence := foldIdentityPhrase(creator)
	evidenceIndex, ok := exactIdentityIndex(line, evidence)
	if !ok {
		base, annotation, split := splitTrailingParenthetical(creator)
		switch {
		case split && normalizeTitle(base) == normalizeTitle(canonical):
			evidence = foldIdentityPhrase(annotation)
		case split && normalizeTitle(annotation) == normalizeTitle(canonical):
			evidence = foldIdentityPhrase(base)
		default:
			return false
		}
		evidenceIndex, ok = exactIdentityIndex(line, evidence)
	}
	if !ok || evidenceIndex <= len(canonical) {
		return false
	}
	aliasContext := line[len(canonical):evidenceIndex]
	if strings.TrimSpace(aliasContext) != "(" && !strings.Contains(aliasContext, "also known") &&
		!strings.Contains(aliasContext, "alias") {
		return false
	}
	afterEvidence := line[evidenceIndex+len(evidence):]
	return strings.Contains(afterEvidence, " is a ") || strings.Contains(afterEvidence, " is an ") ||
		strings.Contains(afterEvidence, " is the ")
}

func containsExactIdentity(value, wanted string) bool {
	value = foldIdentityPhrase(identityDisplayText(value))
	wanted = foldIdentityPhrase(wanted)
	_, ok := exactIdentityIndex(value, wanted)
	return ok
}

func exactIdentityIndex(value, wanted string) (int, bool) {
	if value == "" || wanted == "" || normalizeTitle(wanted) == "" || len(wanted) > len(value) {
		return 0, false
	}
	for offset := 0; offset <= len(value)-len(wanted); {
		index := strings.Index(value[offset:], wanted)
		if index < 0 {
			return 0, false
		}
		index += offset
		end := index + len(wanted)
		if identityPhraseBoundary(value, index, end) {
			return index, true
		}
		_, size := utf8.DecodeRuneInString(value[index:])
		offset = index + size
	}
	return 0, false
}

func foldIdentityPhrase(value string) string {
	value = strings.ToLower(norm.NFKC.String(html.UnescapeString(value)))
	return strings.Join(strings.Fields(value), " ")
}

func identityPhraseBoundary(value string, start, end int) bool {
	if start > 0 {
		previous, _ := utf8.DecodeLastRuneInString(value[:start])
		if unicode.IsLetter(previous) || unicode.IsNumber(previous) {
			return false
		}
	}
	if end < len(value) {
		next, _ := utf8.DecodeRuneInString(value[end:])
		if unicode.IsLetter(next) || unicode.IsNumber(next) {
			return false
		}
	}
	return true
}

type creatorScript uint8

const (
	creatorScriptUnknown creatorScript = iota
	creatorScriptJapanese
	creatorScriptLatin
)

func isJapaneseLatinAliasPair(left, right string) bool {
	leftScript := creatorIdentityScript(left)
	rightScript := creatorIdentityScript(right)
	return (leftScript == creatorScriptJapanese && rightScript == creatorScriptLatin) ||
		(leftScript == creatorScriptLatin && rightScript == creatorScriptJapanese)
}

func creatorIdentityScript(value string) creatorScript {
	hasJapanese := false
	hasLatin := false
	for _, r := range norm.NFKC.String(html.UnescapeString(value)) {
		if !unicode.IsLetter(r) {
			continue
		}
		switch {
		case unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana) || strings.ContainsRune("々〆ヶヵー", r):
			hasJapanese = true
		case unicode.Is(unicode.Latin, r):
			hasLatin = true
		default:
			return creatorScriptUnknown
		}
	}
	if hasJapanese {
		return creatorScriptJapanese
	}
	if hasLatin {
		return creatorScriptLatin
	}
	return creatorScriptUnknown
}

func hasSongSignal(wantedTitle, content string, categories []string) bool {
	if hasRecognizedSongBox(wantedTitle, content) || hasActiveTopLevelLyricsSection(content) {
		return true
	}
	for _, category := range categories {
		if isSongEntityCategory(category) {
			return true
		}
	}
	return false
}

func hasRecognizedSongBox(wantedTitle, content string) bool {
	for _, block := range wikiSongBoxMetadataBlocks(primaryPageMetadata(content)) {
		title, hasTitle := wikiSongBoxTitle(block)
		if !hasTitle || titleFormMatches(title, wantedTitle) {
			return true
		}
	}
	return false
}

func hasActiveTopLevelLyricsSection(content string) bool {
	content = inactiveRestrictionMarkupPattern.ReplaceAllString(content, "")
	return topLevelLyricsHeadingPattern.MatchString(strings.ReplaceAll(content, "\r", ""))
}

func normalizedCategoryName(category string) string {
	category = strings.TrimSpace(norm.NFKC.String(html.UnescapeString(category)))
	if len(category) >= len("Category:") && strings.EqualFold(category[:len("Category:")], "Category:") {
		category = strings.TrimSpace(category[len("Category:"):])
	}
	return strings.ToLower(strings.Join(strings.Fields(category), " "))
}

func isSongEntityCategory(category string) bool {
	category = normalizedCategoryName(category)
	return category == "song" || category == "songs" || strings.HasPrefix(category, "songs ") ||
		strings.HasSuffix(category, " song") || strings.HasSuffix(category, " songs")
}

func hasWrongEntityEvidence(categories []string) bool {
	for _, category := range categories {
		switch normalizedCategoryName(category) {
		case "album", "albums":
			return true
		}
	}
	return false
}

func (c *Client) verifyCandidateWithCreatorAliases(ctx context.Context, identity MusicIdentity, page wikiPage) (bool, error) {
	outcome := candidateVerification(identity, page.title, page.content, page.categories)
	if outcome == candidateVerified {
		return true, nil
	}
	if outcome != candidateCreditMismatch {
		return false, nil
	}
	aliases, ok, err := c.resolveCreatorAliases(ctx, identity, []wikiPage{page}, true)
	if err != nil || !ok {
		return false, err
	}
	return !hasWrongEntityEvidence(page.categories) &&
		candidateTitleMatches(page.title, identity.JapaneseTitle) &&
		roleBoundCreditsMatchWithAliases(identity, page.content, aliases) &&
		hasSongSignal(identity.JapaneseTitle, page.content, page.categories), nil
}
