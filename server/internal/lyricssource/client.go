package lyricssource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	vocaloidWikiAPI       = "https://vocaloid.fandom.com/api.php"
	maxResponseBytes      = 2 << 20
	maxCacheEntries       = 128
	maxInflightRequests   = 16
	maxSourceRedirects    = 5
	maxExtractedLines     = 1000
	maxExtractedLineBytes = 8 << 10
	maxExtractedTextBytes = 1 << 20
)

var (
	ErrAmbiguous         = errors.New("ambiguous source match")
	ErrRevisionChanged   = errors.New("source revision changed")
	ErrMissingLyrics     = errors.New("missing Lyrics section")
	ErrRestrictedReprint = errors.New("source prohibits reprints")
	ErrUnsupportedTable  = errors.New("unsupported lyrics table")
	ErrLyricsTooLarge    = errors.New("lyrics source exceeds safe limits")
	ErrMalformedResponse = errors.New("malformed source response")
)

// HTTPError preserves only the upstream status class needed by the shadow
// worker's stable retry policy. Response bodies and URLs are never retained.
type HTTPError struct {
	StatusCode int
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("lyrics source http %d", e.StatusCode)
}

type MusicIdentity struct {
	MusicID          int
	JapaneseTitle    string
	ProducerMetadata string
}

type Candidate struct {
	PageID       int      `json:"pageId"`
	Title        string   `json:"title"`
	CanonicalURL string   `json:"canonicalUrl"`
	RevisionID   int      `json:"revisionId"`
	SHA1         string   `json:"sha1"`
	Categories   []string `json:"categories"`
}

type ExtractedLine struct {
	Japanese          string `json:"japanese"`
	StanzaBreakBefore bool   `json:"stanzaBreakBefore,omitempty"`
}

type Preview struct {
	CanonicalURL string          `json:"canonicalUrl"`
	PageID       int             `json:"pageId"`
	RevisionID   int             `json:"revisionId"`
	SHA1         string          `json:"sha1"`
	Categories   []string        `json:"categories"`
	FetchedAt    string          `json:"fetchedAt"`
	Lines        []ExtractedLine `json:"lines"`
	ImportToken  string          `json:"importToken,omitempty"`
}

type cacheEntry struct {
	body      []byte
	createdAt time.Time
}

type inflightRequest struct {
	done         chan struct{}
	body         []byte
	err          error
	waiters      int
	participants int
	ctx          context.Context
	cancel       context.CancelFunc
}

type Client struct {
	endpoint    string
	httpClient  *http.Client
	minInterval time.Duration
	cacheTTL    time.Duration

	mu           sync.Mutex
	cache        map[string]cacheEntry
	inflight     map[string]*inflightRequest
	requestSlots chan struct{}

	rateMu      sync.Mutex
	lastRequest time.Time
	rateToken   chan struct{}
}

func New() *Client {
	client := &Client{
		endpoint:     vocaloidWikiAPI,
		httpClient:   &http.Client{Timeout: 12 * time.Second},
		minInterval:  300 * time.Millisecond,
		cacheTTL:     2 * time.Minute,
		cache:        map[string]cacheEntry{},
		inflight:     map[string]*inflightRequest{},
		requestSlots: make(chan struct{}, maxInflightRequests),
		rateToken:    make(chan struct{}, 1),
	}
	client.rateToken <- struct{}{}
	return client
}

type searchResult struct {
	PageID int    `json:"pageid"`
	Title  string `json:"title"`
}

func parseSearchResponse(data []byte) ([]searchResult, error) {
	var response struct {
		Error json.RawMessage `json:"error"`
		Query *struct {
			Search []searchResult `json:"search"`
		} `json:"query"`
	}
	if err := json.Unmarshal(data, &response); err != nil || len(response.Error) > 0 || response.Query == nil || response.Query.Search == nil {
		return nil, ErrMalformedResponse
	}
	return response.Query.Search, nil
}

func (c *Client) Search(ctx context.Context, identity MusicIdentity) ([]Candidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	params := url.Values{
		"action": {"query"}, "format": {"json"}, "list": {"search"},
		"srnamespace": {"0"}, "srlimit": {"8"}, "srsearch": {identity.JapaneseTitle},
	}
	data, err := c.request(ctx, "search", params, true)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	searchResults, err := parseSearchResponse(data)
	if err != nil {
		return nil, err
	}
	result := []Candidate{}
	fetchFailures := 0
	var lastFetchErr error
	for _, search := range searchResults {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, err := c.fetchPage(ctx, search.PageID, 0, true)
		if err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return nil, contextErr
			}
			fetchFailures++
			lastFetchErr = err
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if page.pageID != search.PageID {
			fetchFailures++
			lastFetchErr = ErrRevisionChanged
			continue
		}
		if hasReprintRestriction(page.content, page.categories) {
			continue
		}
		if verifyCandidate(identity, page.title, page.content, page.categories) {
			result = append(result, Candidate{
				PageID: search.PageID, Title: page.title, CanonicalURL: canonicalURL(page.title, page.revisionID),
				RevisionID: page.revisionID, SHA1: page.sha1, Categories: page.categories,
			})
		}
	}
	if fetchFailures > 0 {
		if lastFetchErr != nil {
			return nil, fmt.Errorf("fetch %d of %d source candidates: %w", fetchFailures, len(searchResults), lastFetchErr)
		}
		return nil, ErrMalformedResponse
	}
	sort.Slice(result, func(i, j int) bool { return result[i].PageID < result[j].PageID })
	return result, nil
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
	if hasReprintRestriction(page.content, page.categories) {
		return Preview{}, ErrRestrictedReprint
	}
	if !verifyCandidate(identity, page.title, page.content, page.categories) {
		return Preview{}, ErrAmbiguous
	}
	lines, err := extractLyrics(page.content)
	if err != nil {
		return Preview{}, err
	}
	return Preview{
		CanonicalURL: canonicalURL(page.title, page.revisionID), PageID: pageID, RevisionID: page.revisionID,
		SHA1: page.sha1, Categories: page.categories, FetchedAt: time.Now().UTC().Format(time.RFC3339), Lines: lines,
	}, nil
}

type wikiPage struct {
	pageID     int
	title      string
	revisionID int
	sha1       string
	categories []string
	content    string
}

func (c *Client) fetchPage(ctx context.Context, pageID, revisionID int, cacheable bool) (wikiPage, error) {
	if err := ctx.Err(); err != nil {
		return wikiPage{}, err
	}
	params := url.Values{
		"action": {"query"}, "format": {"json"}, "prop": {"revisions|categories"},
		"rvprop": {"ids|sha1|content"}, "rvslots": {"main"}, "cllimit": {"max"},
	}
	if revisionID > 0 {
		params.Set("revids", fmt.Sprintf("%d", revisionID))
	} else {
		params.Set("pageids", fmt.Sprintf("%d", pageID))
		params.Set("rvlimit", "1")
	}
	data, err := c.request(ctx, "page", params, cacheable)
	if err != nil {
		return wikiPage{}, err
	}
	if err := ctx.Err(); err != nil {
		return wikiPage{}, err
	}
	return parsePageResponse(data)
}

func parsePageResponse(data []byte) (wikiPage, error) {
	var response struct {
		Query struct {
			Pages map[string]struct {
				PageID     int    `json:"pageid"`
				Title      string `json:"title"`
				Categories []struct {
					Title string `json:"title"`
				} `json:"categories"`
				Revisions []struct {
					RevisionID int    `json:"revid"`
					SHA1       string `json:"sha1"`
					Slots      struct {
						Main struct {
							LegacyContent string `json:"*"`
							Content       string `json:"content"`
						} `json:"main"`
					} `json:"slots"`
					LegacyContent string `json:"*"`
				} `json:"revisions"`
			} `json:"pages"`
		} `json:"query"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return wikiPage{}, ErrMalformedResponse
	}
	for _, item := range response.Query.Pages {
		if item.PageID <= 0 || len(item.Revisions) != 1 {
			continue
		}
		revision := item.Revisions[0]
		if revision.RevisionID <= 0 || !mediaWikiSHA1Pattern.MatchString(revision.SHA1) {
			continue
		}
		content := revision.Slots.Main.Content
		if content == "" {
			content = revision.Slots.Main.LegacyContent
		}
		if content == "" {
			content = revision.LegacyContent
		}
		categories := make([]string, 0, len(item.Categories))
		for _, category := range item.Categories {
			categories = append(categories, strings.TrimPrefix(category.Title, "Category:"))
		}
		return wikiPage{pageID: item.PageID, title: item.Title, revisionID: revision.RevisionID, sha1: revision.SHA1, categories: categories, content: content}, nil
	}
	return wikiPage{}, ErrMalformedResponse
}

func (c *Client) request(ctx context.Context, action string, params url.Values, cacheable bool) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := params.Encode()
	if !cacheable {
		return c.requestUncached(ctx, action, key)
	}
	request, body := c.beginCachedRequest(key)
	if body != nil {
		return body, nil
	}
	if request.owner {
		go func() {
			body, err := c.requestUncached(request.inflight.ctx, action, key)
			if err == nil && (action == "search" || action == "page") {
				err = validateActionResponse(action, body)
			}
			c.finishCachedRequest(key, request.inflight, body, err)
		}()
	}
	select {
	case <-ctx.Done():
		c.leaveCachedRequest(request.inflight, request.waiter)
		return nil, ctx.Err()
	case <-request.inflight.done:
		return c.cachedParticipantResult(request.inflight, request.waiter)
	}
}

type cachedRequest struct {
	inflight *inflightRequest
	owner    bool
	waiter   bool
}

func (c *Client) beginCachedRequest(key string) (*cachedRequest, []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cached, ok := c.cache[key]; ok {
		if time.Since(cached.createdAt) < c.cacheTTL {
			return nil, append([]byte(nil), cached.body...)
		}
		delete(c.cache, key)
	}
	if inflight := c.inflight[key]; inflight != nil {
		inflight.waiters++
		inflight.participants++
		return &cachedRequest{inflight: inflight, waiter: true}, nil
	}
	workCtx, cancel := context.WithCancel(context.Background())
	inflight := &inflightRequest{done: make(chan struct{}), participants: 1, ctx: workCtx, cancel: cancel}
	c.inflight[key] = inflight
	return &cachedRequest{inflight: inflight, owner: true}, nil
}

func (c *Client) leaveCachedRequest(inflight *inflightRequest, waiter bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if waiter && inflight.waiters > 0 {
		inflight.waiters--
	}
	if inflight.participants > 0 {
		inflight.participants--
	}
	if inflight.participants == 0 {
		inflight.cancel()
	}
}

func (c *Client) cachedParticipantResult(inflight *inflightRequest, waiter bool) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if waiter {
		if inflight.waiters <= 0 {
			return nil, ErrMalformedResponse
		}
		inflight.waiters--
	}
	if inflight.participants <= 0 {
		return nil, ErrMalformedResponse
	}
	inflight.participants--
	return append([]byte(nil), inflight.body...), inflight.err
}

func validateActionResponse(action string, body []byte) error {
	switch action {
	case "search":
		_, err := parseSearchResponse(body)
		return err
	case "page":
		_, err := parsePageResponse(body)
		return err
	default:
		return nil
	}
}

func (c *Client) finishCachedRequest(key string, inflight *inflightRequest, body []byte, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if current := c.inflight[key]; current != inflight {
		return
	}
	if err == nil {
		now := time.Now()
		for cacheKey, cached := range c.cache {
			if now.Sub(cached.createdAt) >= c.cacheTTL {
				delete(c.cache, cacheKey)
			}
		}
		if _, replacing := c.cache[key]; !replacing && len(c.cache) >= maxCacheEntries {
			var oldestKey string
			var oldest time.Time
			for cacheKey, cached := range c.cache {
				if oldest.IsZero() || cached.createdAt.Before(oldest) {
					oldestKey, oldest = cacheKey, cached.createdAt
				}
			}
			delete(c.cache, oldestKey)
		}
		c.cache[key] = cacheEntry{body: append([]byte(nil), body...), createdAt: now}
	}
	inflight.body = append([]byte(nil), body...)
	inflight.err = err
	delete(c.inflight, key)
	close(inflight.done)
	inflight.cancel()
}

func (c *Client) requestUncached(ctx context.Context, action, key string) ([]byte, error) {
	if err := c.acquireRequestSlot(ctx); err != nil {
		return nil, err
	}
	defer c.releaseRequestSlot()

	separator := "?"
	if strings.Contains(c.endpoint, "?") {
		separator = "&"
	}
	requestURL := c.endpoint + separator + key
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "moesekai-lyrics-source/1")
	if err := c.waitRateLimit(ctx); err != nil {
		return nil, err
	}
	log.Printf("[lyrics-source] request action=%s", action)
	client := *c.httpClient
	originalScheme := req.URL.Scheme
	originalHost := req.URL.Host
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if next.URL.Scheme != originalScheme || !strings.EqualFold(next.URL.Host, originalHost) {
			return fmt.Errorf("lyrics source redirect changed origin")
		}
		if len(via) > maxSourceRedirects {
			return fmt.Errorf("lyrics source redirect limit exceeded")
		}
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &HTTPError{StatusCode: resp.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxResponseBytes {
		return nil, fmt.Errorf("lyrics source response too large")
	}
	return body, nil
}

func (c *Client) acquireRequestSlot(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case c.requestSlots <- struct{}{}:
		return nil
	}
}

func (c *Client) releaseRequestSlot() {
	<-c.requestSlots
}

func (c *Client) waitRateLimit(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.rateToken:
	}
	if err := ctx.Err(); err != nil {
		c.rateToken <- struct{}{}
		return err
	}
	defer func() { c.rateToken <- struct{}{} }()

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		c.rateMu.Lock()
		wait := c.minInterval - time.Since(c.lastRequest)
		c.rateMu.Unlock()
		if wait <= 0 {
			c.rateMu.Lock()
			if err := ctx.Err(); err != nil {
				c.rateMu.Unlock()
				return err
			}
			c.lastRequest = time.Now()
			c.rateMu.Unlock()
			return nil
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func canonicalURL(title string, revisionID int) string {
	canonical := url.URL{
		Scheme: "https",
		Host:   "vocaloid.fandom.com",
		Path:   "/wiki/" + strings.ReplaceAll(title, " ", "_"),
	}
	if revisionID > 0 {
		query := canonical.Query()
		query.Set("oldid", fmt.Sprintf("%d", revisionID))
		canonical.RawQuery = query.Encode()
	}
	return canonical.String()
}

func normalizeTitle(value string) string {
	var result strings.Builder
	for _, r := range strings.ToLower(html.UnescapeString(value)) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			result.WriteRune(r)
		}
	}
	return result.String()
}

func verifyCandidate(identity MusicIdentity, title, content string, categories []string) bool {
	wantedTitle := normalizeTitle(identity.JapaneseTitle)
	combined := strings.ToLower(title + "\n" + content + "\n" + strings.Join(categories, "\n"))
	if wantedTitle == "" || !candidateTitleMatches(title, wantedTitle) {
		return false
	}
	producerMatch := false
	for _, producer := range identityFields(identity.ProducerMetadata) {
		if producer != "" && producer != "-" && containsIdentityField(combined, producer, true) {
			producerMatch = true
			break
		}
	}
	if !producerMatch {
		return false
	}
	signals := []string{"lyrics", "vocaloid", "original song", "project sekai", "プロジェクトセカイ", "書き下ろし"}
	for _, signal := range signals {
		if strings.Contains(combined, signal) {
			return true
		}
	}
	return false
}

func candidateTitleMatches(title, wanted string) bool {
	parts := strings.SplitN(title, "/", 2)
	return normalizeTitle(parts[0]) == wanted
}

func identityFields(value string) []string {
	raw := strings.FieldsFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("|/／,，;；:=：[]{}()（）<>\"'", r)
	})
	fields := make([]string, 0, len(raw))
	for _, field := range raw {
		if normalized := normalizeTitle(field); normalized != "" {
			fields = append(fields, normalized)
		}
	}
	return fields
}

func containsIdentityField(value, wanted string, allowJapaneseSuffix bool) bool {
	for _, field := range identityFields(value) {
		if field == wanted {
			return true
		}
		if allowJapaneseSuffix && strings.HasPrefix(field, wanted) {
			suffix := strings.TrimPrefix(field, wanted)
			for _, allowed := range []string{"による", "の", "制作", "作詞", "作曲"} {
				if suffix == allowed || strings.HasPrefix(suffix, allowed) {
					return true
				}
			}
		}
	}
	return false
}

var headingPattern = regexp.MustCompile(`(?im)^==+\s*Lyrics\s*==+\s*$`)
var nextHeadingPattern = regexp.MustCompile(`(?m)^==+[^=].*==+\s*$`)
var inactiveRestrictionMarkupPattern = regexp.MustCompile(`(?is)<!--.*?-->|<nowiki\b[^>]*>.*?</nowiki\s*>`)
var markupPattern = regexp.MustCompile(`(?s)<!--.*?-->|<ref[^>]*>.*?</ref>|<[^>]+>`)
var linkPattern = regexp.MustCompile(`\[\[(?:[^]|]+\|)?([^]]+)\]\]`)
var sharedPlainLinePattern = regexp.MustCompile(`(?i)^\{\{\s*shared\s*\}\}\s*(?:\|\s*)?(.*)$`)
var sharedTableCellPattern = regexp.MustCompile(`(?i)^\{\{\s*shared\s*\}\}\s*(?:\|\s*)?`)
var noReprintTemplatePattern = regexp.MustCompile(`(?i)\{\{\s*(?:no[\s_-]*reprint|noreprint)\b`)
var directReprintRestrictionPattern = regexp.MustCompile(`(?i)(?:no\s+(?:unauthorized\s+)?reprints?\b|(?:unauthorized\s+)?reprint(?:s|ing)?\s+(?:(?:are|is)\s+)?(?:prohibited|forbidden|not\s+allowed)\b|reposts?\s+(?:(?:are|is)\s+)?(?:prohibited|forbidden|not\s+allowed)\b|do\s+not\s+(?:repost|reprint)\b)`)
var mediaWikiSHA1Pattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// HasCanonicalSHA1 reports whether value is the lowercase 40-hex revision
// identity required for every provenance-bearing preview, save, and restore.
func HasCanonicalSHA1(value string) bool {
	return mediaWikiSHA1Pattern.MatchString(value)
}

func extractLyrics(content string) ([]ExtractedLine, error) {
	if hasReprintRestriction(content, nil) {
		return nil, ErrRestrictedReprint
	}
	location := headingPattern.FindStringIndex(content)
	if location == nil {
		return nil, ErrMissingLyrics
	}
	section := content[location[1]:]
	if next := nextHeadingPattern.FindStringIndex(section); next != nil {
		section = section[:next[0]]
	}
	tableCount := strings.Count(section, "{|")
	if tableCount > 1 {
		return nil, ErrUnsupportedTable
	}
	if tableCount == 1 {
		return extractLyricsTable(section)
	}
	return extractPlainLyrics(section)
}

func hasReprintRestriction(content string, categories []string) bool {
	content = inactiveRestrictionMarkupPattern.ReplaceAllString(content, "")
	if noReprintTemplatePattern.MatchString(content) {
		return true
	}
	for _, category := range categories {
		category = strings.ToLower(strings.TrimPrefix(category, "category:"))
		if strings.Contains(category, "unauthorized reprint") || restrictionStatement(category) {
			return true
		}
	}
	for _, line := range strings.Split(strings.ReplaceAll(strings.ToLower(content), "\r", ""), "\n") {
		line = stripRestrictionMarkup(line)
		if !restrictionStatement(line) {
			continue
		}
		if historicalRemovalStatement(line) {
			line = stripHistoricalRemovalClauses(line)
		}
		if restrictionStatement(line) {
			return true
		}
	}
	return false
}

func historicalRemovalStatement(value string) bool {
	return strings.Contains(value, "unauthorized reprint") &&
		(strings.Contains(value, " removed") || strings.Contains(value, " deleted") || strings.Contains(value, " taken down"))
}

func stripHistoricalRemovalClauses(value string) string {
	clauses := strings.FieldsFunc(value, func(r rune) bool {
		return r == ';' || r == '.' || r == '!' || r == '?' || r == '\n' || r == '\r'
	})
	active := clauses[:0]
	for _, clause := range clauses {
		clause = strings.TrimSpace(clause)
		if clause == "" || historicalRemovalStatement(clause) {
			continue
		}
		active = append(active, clause)
	}
	return strings.Join(active, "; ")
}

func stripRestrictionMarkup(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimLeft(value, "*#:;!| ")
	value = strings.TrimSpace(value)
	value = strings.Trim(value, "'\"")
	value = linkPattern.ReplaceAllString(value, "$1")
	value = strings.Trim(value, "'\"")
	return strings.Join(strings.Fields(value), " ")
}

func restrictionStatement(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	return strings.Contains(value, "転載禁止") || strings.Contains(value, "無断転載禁止") || directReprintRestrictionPattern.MatchString(value)
}

func extractPlainLyrics(section string) ([]ExtractedLine, error) {
	raw := strings.Split(strings.ReplaceAll(section, "\r", ""), "\n")
	lines := make([]string, 0, len(raw))
	explicitShared := make([]bool, 0, len(raw))
	for _, line := range raw {
		shared := sharedPlainLinePattern.FindStringSubmatch(strings.TrimSpace(line))
		if shared != nil {
			line = shared[1]
		}
		sanitized, err := sanitizeLyricText(line)
		if err != nil {
			return nil, err
		}
		lines = append(lines, sanitized)
		explicitShared = append(explicitShared, shared != nil)
	}
	return plainLyricLines(lines, explicitShared)
}

func extractLyricsTable(section string) ([]ExtractedLine, error) {
	lower := strings.ToLower(section)
	if strings.Contains(lower, "rowspan") || strings.Contains(lower, "colspan") || strings.Count(section, "{|") != 1 {
		return nil, ErrUnsupportedTable
	}

	var cells []string
	var headers []string
	var rowCells []string
	inTable := false
	dataStarted := false
	sourceColumn := -1

	flushHeaders := func() error {
		if sourceColumn >= 0 {
			return nil
		}
		if len(headers) == 0 || !isSupportedSourceHeader(headers[0]) {
			return ErrUnsupportedTable
		}
		sourceColumn = 0
		for _, header := range headers[1:] {
			if isSupportedSourceHeader(header) {
				return ErrUnsupportedTable
			}
		}
		return nil
	}
	flushRow := func() error {
		if len(rowCells) == 0 {
			return nil
		}
		if err := flushHeaders(); err != nil {
			return err
		}
		if len(rowCells) > len(headers) || sourceColumn >= len(rowCells) {
			return ErrUnsupportedTable
		}
		sanitized := make([]string, len(rowCells))
		for index, raw := range rowCells {
			cell := strings.TrimSpace(raw)
			if index == sourceColumn {
				if looksLikeTableCellAttributes(cell) {
					return ErrUnsupportedTable
				}
				cell = sharedTableCellPattern.ReplaceAllString(cell, "")
			}
			var err error
			cell, err = sanitizeLyricText(cell)
			if err != nil {
				return err
			}
			sanitized[index] = strings.TrimSpace(html.UnescapeString(cell))
		}
		source := sanitized[sourceColumn]
		if source == "" {
			for index, cell := range sanitized {
				if index != sourceColumn && cell != "" {
					return ErrUnsupportedTable
				}
			}
		}
		cells = append(cells, source)
		rowCells = nil
		return nil
	}

	for _, rawLine := range strings.Split(strings.ReplaceAll(section, "\r", ""), "\n") {
		line := strings.TrimSpace(rawLine)
		if strings.HasPrefix(line, "{|") {
			if inTable {
				return nil, ErrUnsupportedTable
			}
			inTable = true
			continue
		}
		if !inTable {
			continue
		}
		if line == "|}" {
			if err := flushRow(); err != nil {
				return nil, err
			}
			if err := flushHeaders(); err != nil {
				return nil, err
			}
			inTable = false
			continue
		}
		if strings.HasPrefix(line, "|-") {
			if line != "|-" {
				return nil, ErrUnsupportedTable
			}
			if err := flushRow(); err != nil {
				return nil, err
			}
			continue
		}
		if strings.HasPrefix(line, "!") {
			if dataStarted || len(rowCells) > 0 {
				return nil, ErrUnsupportedTable
			}
			for _, rawHeader := range strings.Split(strings.TrimPrefix(line, "!"), "!!") {
				header := strings.TrimSpace(rawHeader)
				if separator := strings.LastIndex(header, "|"); separator >= 0 {
					header = strings.TrimSpace(header[separator+1:])
				}
				var err error
				header, err = sanitizeLyricText(header)
				if err != nil {
					return nil, err
				}
				headers = append(headers, strings.TrimSpace(html.UnescapeString(header)))
			}
			continue
		}
		if strings.HasPrefix(line, "|+") {
			return nil, ErrUnsupportedTable
		}
		if !strings.HasPrefix(line, "|") {
			if len(rowCells) > 0 && line != "" {
				return nil, ErrUnsupportedTable
			}
			continue
		}
		if err := flushHeaders(); err != nil {
			return nil, err
		}
		dataStarted = true
		for _, cell := range strings.Split(strings.TrimPrefix(line, "|"), "||") {
			rowCells = append(rowCells, strings.TrimSpace(cell))
		}
	}
	if inTable || sourceColumn < 0 {
		return nil, ErrUnsupportedTable
	}
	return lyricLines(cells, false)
}

func isSupportedSourceHeader(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "'", "")
	value = strings.Join(strings.Fields(value), " ")
	if value == "japanese" || value == "japanese lyrics" || value == "lyrics" || value == "original" || value == "original lyrics" ||
		value == "source" || value == "source lyrics" || value == "日本語" || value == "日本語歌詞" {
		return true
	}
	return strings.HasPrefix(value, "japanese (") && strings.HasSuffix(value, ")") && strings.Contains(value, "日本語歌詞")
}

func sanitizeLyricText(value string) (string, error) {
	value = markupPattern.ReplaceAllString(value, "")
	value = linkPattern.ReplaceAllString(value, "$1")
	if strings.Contains(value, "{{") || strings.Contains(value, "}}") || strings.Contains(value, "{|") || strings.Contains(value, "|}") ||
		strings.Contains(value, "[[") || strings.Contains(value, "]]") {
		return "", ErrUnsupportedTable
	}
	return value, nil
}

func looksLikeTableCellAttributes(value string) bool {
	separator := strings.Index(value, "|")
	if separator < 0 {
		return false
	}
	attributes := strings.ToLower(strings.TrimSpace(value[:separator]))
	return strings.Contains(attributes, "=") || strings.Contains(attributes, "style") ||
		strings.Contains(attributes, "class") || strings.Contains(attributes, "scope")
}

func plainLyricLines(raw []string, explicitShared []bool) ([]ExtractedLine, error) {
	if len(raw) != len(explicitShared) {
		return nil, ErrMalformedResponse
	}
	result := []ExtractedLine{}
	stanza := false
	totalBytes := 0
	for index, line := range raw {
		line = strings.TrimSpace(html.UnescapeString(line))
		line = strings.Trim(line, "'")
		if line == "" {
			if len(result) > 0 {
				stanza = true
			}
			continue
		}
		if strings.HasPrefix(line, "Category:") || strings.HasPrefix(line, "[[Category:") {
			continue
		}
		if !explicitShared[index] && !containsJapanese(line) {
			continue
		}
		if err := appendExtractedLine(&result, line, stanza, &totalBytes); err != nil {
			return nil, err
		}
		stanza = false
	}
	if len(result) == 0 {
		return nil, ErrMissingLyrics
	}
	return result, nil
}

func lyricLines(raw []string, requireJapanese bool) ([]ExtractedLine, error) {
	result := []ExtractedLine{}
	stanza := false
	totalBytes := 0
	for _, line := range raw {
		line = strings.TrimSpace(html.UnescapeString(line))
		line = strings.Trim(line, "'")
		if line == "" {
			if len(result) > 0 {
				stanza = true
			}
			continue
		}
		if strings.HasPrefix(line, "Category:") || strings.HasPrefix(line, "[[Category:") {
			continue
		}
		if requireJapanese && !containsJapanese(line) {
			continue
		}
		if err := appendExtractedLine(&result, line, stanza, &totalBytes); err != nil {
			return nil, err
		}
		stanza = false
	}
	if len(result) == 0 {
		return nil, ErrMissingLyrics
	}
	return result, nil
}

func appendExtractedLine(result *[]ExtractedLine, line string, stanza bool, totalBytes *int) error {
	if len(line) > maxExtractedLineBytes || len(*result) >= maxExtractedLines || *totalBytes > maxExtractedTextBytes-len(line) {
		return ErrLyricsTooLarge
	}
	*totalBytes += len(line)
	*result = append(*result, ExtractedLine{Japanese: line, StanzaBreakBefore: stanza})
	return nil
}

func containsJapanese(value string) bool {
	for _, r := range value {
		if (r >= 0x3040 && r <= 0x30ff) || (r >= 0x4e00 && r <= 0x9fff) {
			return true
		}
	}
	return false
}
