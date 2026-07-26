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

type Client struct {
	endpoint    string
	httpClient  *http.Client
	minInterval time.Duration
	cacheTTL    time.Duration

	mu          sync.Mutex
	lastRequest time.Time
	cache       map[string]cacheEntry
}

func New() *Client {
	return &Client{
		endpoint:    vocaloidWikiAPI,
		httpClient:  &http.Client{Timeout: 12 * time.Second},
		minInterval: 300 * time.Millisecond,
		cacheTTL:    2 * time.Minute,
		cache:       map[string]cacheEntry{},
	}
}

func (c *Client) Search(ctx context.Context, identity MusicIdentity) ([]Candidate, error) {
	params := url.Values{
		"action": {"query"}, "format": {"json"}, "list": {"search"},
		"srnamespace": {"0"}, "srlimit": {"8"}, "srsearch": {identity.JapaneseTitle},
	}
	data, err := c.request(ctx, "search", params, true)
	if err != nil {
		return nil, err
	}
	var response struct {
		Error json.RawMessage `json:"error"`
		Query *struct {
			Search []struct {
				PageID int    `json:"pageid"`
				Title  string `json:"title"`
			} `json:"search"`
		} `json:"query"`
	}
	if err := json.Unmarshal(data, &response); err != nil || len(response.Error) > 0 || response.Query == nil {
		return nil, ErrMalformedResponse
	}
	result := []Candidate{}
	fetchFailures := 0
	var lastFetchErr error
	for _, search := range response.Query.Search {
		page, err := c.fetchPage(ctx, search.PageID, 0, true)
		if err != nil {
			fetchFailures++
			lastFetchErr = err
			continue
		}
		if page.pageID != search.PageID || hasReprintRestriction(page.content, page.categories) {
			continue
		}
		if verifyCandidate(identity, page.title, page.content, page.categories) {
			result = append(result, Candidate{
				PageID: search.PageID, Title: page.title, CanonicalURL: canonicalURL(page.title),
				RevisionID: page.revisionID, SHA1: page.sha1, Categories: page.categories,
			})
		}
	}
	if fetchFailures > 0 {
		if lastFetchErr != nil {
			return nil, fmt.Errorf("fetch %d of %d source candidates: %w", fetchFailures, len(response.Query.Search), lastFetchErr)
		}
		return nil, ErrMalformedResponse
	}
	sort.Slice(result, func(i, j int) bool { return result[i].PageID < result[j].PageID })
	return result, nil
}

func (c *Client) Preview(ctx context.Context, identity MusicIdentity, pageID, revisionID int) (Preview, error) {
	page, err := c.fetchPage(ctx, pageID, revisionID, revisionID == 0)
	if err != nil {
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
		CanonicalURL: canonicalURL(page.title), PageID: pageID, RevisionID: page.revisionID,
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
	key := params.Encode()
	if cacheable {
		c.mu.Lock()
		if cached, ok := c.cache[key]; ok && time.Since(cached.createdAt) < c.cacheTTL {
			body := append([]byte(nil), cached.body...)
			c.mu.Unlock()
			return body, nil
		}
		c.mu.Unlock()
	}
	if err := c.waitRateLimit(ctx); err != nil {
		return nil, err
	}
	requestURL := c.endpoint + "?" + key
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "moesekai-lyrics-source/1")
	log.Printf("[lyrics-source] request action=%s", action)
	client := *c.httpClient
	originalHost := req.URL.Host
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("lyrics source redirect limit exceeded")
		}
		if next.URL.Scheme != req.URL.Scheme || !strings.EqualFold(next.URL.Host, originalHost) {
			return fmt.Errorf("lyrics source redirect changed origin")
		}
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lyrics source http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxResponseBytes {
		return nil, fmt.Errorf("lyrics source response too large")
	}
	if cacheable {
		c.mu.Lock()
		if len(c.cache) >= maxCacheEntries {
			var oldestKey string
			var oldest time.Time
			for cacheKey, cached := range c.cache {
				if oldest.IsZero() || cached.createdAt.Before(oldest) {
					oldestKey, oldest = cacheKey, cached.createdAt
				}
			}
			delete(c.cache, oldestKey)
		}
		c.cache[key] = cacheEntry{body: append([]byte(nil), body...), createdAt: time.Now()}
		c.mu.Unlock()
	}
	return body, nil
}

func (c *Client) waitRateLimit(ctx context.Context) error {
	c.mu.Lock()
	wait := c.minInterval - time.Since(c.lastRequest)
	if wait < 0 {
		wait = 0
	}
	c.lastRequest = time.Now().Add(wait)
	c.mu.Unlock()
	if wait == 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func canonicalURL(title string) string {
	canonical := url.URL{
		Scheme: "https",
		Host:   "vocaloid.fandom.com",
		Path:   "/wiki/" + strings.ReplaceAll(title, " ", "_"),
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
var markupPattern = regexp.MustCompile(`(?s)<!--.*?-->|<ref[^>]*>.*?</ref>|<[^>]+>`)
var linkPattern = regexp.MustCompile(`\[\[(?:[^]|]+\|)?([^]]+)\]\]`)
var sharedPlainLinePattern = regexp.MustCompile(`(?i)^\{\{\s*shared\s*\}\}\s*(?:\|\s*)?(.*)$`)
var sharedTableCellPattern = regexp.MustCompile(`(?i)^\{\{\s*shared\s*\}\}\s*(?:\|\s*)?`)
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
	combined := strings.ToLower(content + "\n" + strings.Join(categories, "\n"))
	for _, restriction := range []string{
		"no unauthorized reprints", "unauthorized reprint", "転載禁止", "無断転載禁止", "do not repost",
		"{{no reprint", "{{no-reprint", "{{noreprint", "reprint prohibited", "reprints prohibited",
	} {
		if strings.Contains(combined, strings.ToLower(restriction)) {
			return true
		}
	}
	return false
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
