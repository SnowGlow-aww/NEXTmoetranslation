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
	vocaloidWikiAPI  = "https://vocaloid.fandom.com/api.php"
	maxResponseBytes = 2 << 20
	maxCacheEntries  = 128
)

var (
	ErrAmbiguous         = errors.New("ambiguous source match")
	ErrRevisionChanged   = errors.New("source revision changed")
	ErrMissingLyrics     = errors.New("missing Lyrics section")
	ErrRestrictedReprint = errors.New("source prohibits reprints")
	ErrUnsupportedTable  = errors.New("unsupported lyrics table")
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
		Query struct {
			Search []struct {
				PageID int    `json:"pageid"`
				Title  string `json:"title"`
			} `json:"search"`
		} `json:"query"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, ErrMalformedResponse
	}
	result := []Candidate{}
	for _, search := range response.Query.Search {
		page, err := c.fetchPage(ctx, search.PageID, 0, true)
		if err != nil {
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
	return "https://vocaloid.fandom.com/wiki/" + url.PathEscape(strings.ReplaceAll(title, " ", "_"))
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
	if wantedTitle == "" || (!strings.Contains(normalizeTitle(title), wantedTitle) && !strings.Contains(normalizeTitle(content), wantedTitle)) {
		return false
	}
	producerMatch := false
	for _, producer := range strings.FieldsFunc(identity.ProducerMetadata, func(r rune) bool {
		return r == '|' || r == '/' || r == ',' || r == ';'
	}) {
		producer = strings.TrimSpace(strings.ToLower(producer))
		if producer != "" && producer != "-" && strings.Contains(combined, producer) {
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
	return containsJapanese(content)
}

var headingPattern = regexp.MustCompile(`(?im)^==+\s*Lyrics\s*==+\s*$`)
var nextHeadingPattern = regexp.MustCompile(`(?m)^==+[^=].*==+\s*$`)
var markupPattern = regexp.MustCompile(`(?s)<!--.*?-->|<ref[^>]*>.*?</ref>|<[^>]+>`)
var linkPattern = regexp.MustCompile(`\[\[(?:[^]|]+\|)?([^]]+)\]\]`)
var templatePattern = regexp.MustCompile(`\{\{[^{}]*\}\}`)

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
	section = markupPattern.ReplaceAllString(section, "")
	section = linkPattern.ReplaceAllString(section, "$1")
	section = templatePattern.ReplaceAllString(section, "")
	if strings.Contains(section, "{{") || strings.Contains(section, "{|") {
		return nil, ErrUnsupportedTable
	}
	return lyricLines(strings.Split(strings.ReplaceAll(section, "\r", ""), "\n"))
}

func extractLyricsTable(section string) ([]ExtractedLine, error) {
	if strings.Contains(strings.ToLower(section), "rowspan") || strings.Contains(strings.ToLower(section), "colspan") {
		return nil, ErrUnsupportedTable
	}
	var cells []string
	for _, row := range strings.Split(strings.ReplaceAll(section, "\r", ""), "\n") {
		row = strings.TrimSpace(row)
		if row == "|-" {
			cells = append(cells, "")
			continue
		}
		if !strings.HasPrefix(row, "|") || strings.HasPrefix(row, "|-") || strings.HasPrefix(row, "|}") || strings.HasPrefix(row, "{|") {
			continue
		}
		for _, cell := range strings.Split(strings.TrimPrefix(row, "|"), "||") {
			cell = strings.TrimSpace(cell)
			if containsJapanese(cell) {
				cells = append(cells, cell)
				break
			}
		}
	}
	return lyricLines(cells)
}

func lyricLines(raw []string) ([]ExtractedLine, error) {
	result := []ExtractedLine{}
	stanza := false
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
		if !containsJapanese(line) {
			continue
		}
		result = append(result, ExtractedLine{Japanese: line, StanzaBreakBefore: stanza})
		stanza = false
	}
	if len(result) == 0 {
		return nil, ErrMissingLyrics
	}
	return result, nil
}

func containsJapanese(value string) bool {
	for _, r := range value {
		if (r >= 0x3040 && r <= 0x30ff) || (r >= 0x4e00 && r <= 0x9fff) {
			return true
		}
	}
	return false
}
