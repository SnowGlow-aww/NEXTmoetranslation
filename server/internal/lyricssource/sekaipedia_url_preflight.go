package lyricssource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const MaxSekaipediaURLPreflightBatch = 50

// SekaipediaPageURLTarget is one exact page title obtained from the immutable
// Sekaipedia List and its canonical mutable wiki URL. The URL is derived from
// the provider title; catalog Japanese titles are never transliterated.
type SekaipediaPageURLTarget struct {
	PageTitle    string `json:"pageTitle"`
	CanonicalURL string `json:"canonicalUrl"`
}

// SekaipediaPageURLStatus records one successful existence check, including
// the resolved title and URL when MediaWiki normalization or redirects apply.
type SekaipediaPageURLStatus struct {
	PageTitle            string `json:"pageTitle"`
	CanonicalURL         string `json:"canonicalUrl"`
	ResolvedPageTitle    string `json:"resolvedPageTitle"`
	ResolvedCanonicalURL string `json:"resolvedCanonicalUrl"`
	Redirected           bool   `json:"redirected"`
	PageID               int    `json:"pageId"`
}

// SekaipediaURLPreflightBatch records the exact response identity for one
// bounded API existence query. Raw bytes are returned separately so callers can
// retain them under their own private evidence boundary.
type SekaipediaURLPreflightBatch struct {
	FirstTargetIndex int      `json:"firstTargetIndex"`
	TargetCount      int      `json:"targetCount"`
	PageTitles       []string `json:"pageTitles"`
	RequestURL       string   `json:"requestUrl"`
	FetchedAt        string   `json:"fetchedAt"`
	RawSHA256        string   `json:"rawSha256"`
	Raw              []byte   `json:"-"`
}

// SekaipediaPageURLTargetForTitle validates one official MediaWiki page title
// and derives its canonical mutable wiki URL without transliterating or guessing.
func SekaipediaPageURLTargetForTitle(title string) (SekaipediaPageURLTarget, error) {
	if !validSekaipediaTargetTitle(title) {
		return SekaipediaPageURLTarget{}, ErrMalformedResponse
	}
	canonical := canonicalRevisionURL(ProviderSekaipedia, title, 0)
	if canonical == "" {
		return SekaipediaPageURLTarget{}, ErrMalformedResponse
	}
	return SekaipediaPageURLTarget{
		PageTitle:    title,
		CanonicalURL: canonical,
	}, nil
}

// SekaipediaListPageURLTargets extracts only exact linked page titles from one
// MediaWiki List revision response. It does not inspect or romanize catalog
// names, and it never performs network I/O.
func SekaipediaListPageURLTargets(raw []byte) ([]SekaipediaPageURLTarget, error) {
	page, err := parsePageResponse(raw)
	if err != nil {
		return nil, err
	}
	targets, err := parseSekaipediaListAuthority(page.content)
	if err != nil {
		return nil, err
	}
	result := make([]SekaipediaPageURLTarget, len(targets))
	for index, target := range targets {
		canonical := canonicalRevisionURL(ProviderSekaipedia, target.pageTitle, 0)
		if canonical == "" {
			return nil, ErrMalformedResponse
		}
		result[index] = SekaipediaPageURLTarget{
			PageTitle: target.pageTitle, CanonicalURL: canonical,
		}
	}
	return result, nil
}

// PreflightSekaipediaPageURLs verifies every exact List page title through the
// MediaWiki API before any lyrics-page acquisition begins. Calls are sequential
// and batches are capped at 50 titles. The shared Client enforces maxlag=5,
// provider crawl delay, full Retry-After cooldown, and one actual request in
// flight.
func PreflightSekaipediaPageURLs(
	ctx context.Context,
	targets []SekaipediaPageURLTarget,
	httpClient *http.Client,
	crawlDelay time.Duration,
	cacheTTL time.Duration,
	maxAttempts int,
	retryDelay time.Duration,
) ([]SekaipediaPageURLStatus, []SekaipediaURLPreflightBatch, error) {
	if ctx == nil || httpClient == nil || len(targets) == 0 || len(targets) > 10_000 ||
		crawlDelay < defaultProviderCrawlDelay || cacheTTL < defaultProviderCacheTTL ||
		maxAttempts < 1 || maxAttempts > 5 || retryDelay < 0 {
		return nil, nil, errors.New("Sekaipedia URL preflight configuration is invalid")
	}
	seen := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		if target.PageTitle == "" || target.CanonicalURL != canonicalRevisionURL(ProviderSekaipedia, target.PageTitle, 0) {
			return nil, nil, errors.New("Sekaipedia URL preflight target is not canonical")
		}
		key := strings.ToLower(strings.ReplaceAll(target.PageTitle, "_", " "))
		if _, duplicate := seen[key]; duplicate {
			return nil, nil, errors.New("Sekaipedia URL preflight target is duplicated")
		}
		seen[key] = struct{}{}
	}

	client := newMediaWikiClient(sekaipediaAPI, crawlDelay, cacheTTL, httpClient)
	statuses := make([]SekaipediaPageURLStatus, 0, len(targets))
	batches := make([]SekaipediaURLPreflightBatch, 0, (len(targets)+MaxSekaipediaURLPreflightBatch-1)/MaxSekaipediaURLPreflightBatch)
	for start := 0; start < len(targets); start += MaxSekaipediaURLPreflightBatch {
		end := start + MaxSekaipediaURLPreflightBatch
		if end > len(targets) {
			end = len(targets)
		}
		batchStatuses, batch, err := preflightSekaipediaPageURLBatch(
			ctx, client, targets[start:end], start, maxAttempts, retryDelay,
		)
		if len(batch.Raw) > 0 {
			batches = append(batches, batch)
		}
		if err != nil {
			return statuses, batches, fmt.Errorf("Sekaipedia URL preflight targets %d-%d: %w", start+1, end, err)
		}
		statuses = append(statuses, batchStatuses...)
	}
	if len(statuses) != len(targets) {
		return nil, nil, ErrMalformedResponse
	}
	return statuses, batches, nil
}

func preflightSekaipediaPageURLBatch(
	ctx context.Context,
	client *Client,
	targets []SekaipediaPageURLTarget,
	firstTargetIndex int,
	maxAttempts int,
	retryDelay time.Duration,
) ([]SekaipediaPageURLStatus, SekaipediaURLPreflightBatch, error) {
	titles := make([]string, len(targets))
	for index, target := range targets {
		titles[index] = target.PageTitle
	}
	params := url.Values{
		"action":        {"query"},
		"format":        {"json"},
		"formatversion": {"2"},
		"prop":          {"info"},
		"redirects":     {"1"},
		"titles":        {strings.Join(titles, "|")},
	}
	requestParams := mediaWikiActionRequestParams(params)
	requestURL, err := canonicalMediaWikiRequestURL(sekaipediaAPI, requestParams)
	if err != nil {
		return nil, SekaipediaURLPreflightBatch{}, err
	}

	var raw []byte
	var fetchedAt time.Time
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, SekaipediaURLPreflightBatch{}, err
		}
		raw, err = client.request(ctx, "page", params, false)
		if err == nil {
			fetchedAt = time.Now().UTC()
			break
		}
		if attempt == maxAttempts || !retryableSekaipediaURLPreflightError(err) {
			return nil, SekaipediaURLPreflightBatch{}, err
		}
		if err := waitSekaipediaURLPreflightRetry(ctx, retryDelay); err != nil {
			return nil, SekaipediaURLPreflightBatch{}, err
		}
	}
	digest := sha256.Sum256(raw)
	batch := SekaipediaURLPreflightBatch{
		FirstTargetIndex: firstTargetIndex,
		TargetCount:      len(targets),
		PageTitles:       append([]string(nil), titles...),
		RequestURL:       requestURL,
		FetchedAt:        canonicalFetchedAt(fetchedAt),
		RawSHA256:        hex.EncodeToString(digest[:]),
		Raw:              append([]byte(nil), raw...),
	}
	statuses, err := parseSekaipediaURLExistenceResponse(raw, targets)
	if err != nil {
		return nil, batch, err
	}
	return statuses, batch, nil
}

func retryableSekaipediaURLPreflightError(err error) bool {
	var httpError *HTTPError
	return errors.As(err, &httpError) &&
		(httpError.StatusCode == http.StatusTooManyRequests || httpError.StatusCode == http.StatusServiceUnavailable)
}

func waitSekaipediaURLPreflightRetry(ctx context.Context, delay time.Duration) error {
	if delay == 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type sekaipediaTitleRewrite struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type sekaipediaURLExistenceResponse struct {
	BatchComplete bool `json:"batchcomplete"`
	Query         struct {
		Normalized []sekaipediaTitleRewrite `json:"normalized"`
		Redirects  []sekaipediaTitleRewrite `json:"redirects"`
		Pages      []struct {
			PageID    int    `json:"pageid"`
			Namespace int    `json:"ns"`
			Title     string `json:"title"`
			Missing   bool   `json:"missing"`
		} `json:"pages"`
	} `json:"query"`
}

func parseSekaipediaURLExistenceResponse(
	raw []byte,
	targets []SekaipediaPageURLTarget,
) ([]SekaipediaPageURLStatus, error) {
	if len(raw) == 0 || len(raw) > maxResponseBytes {
		return nil, ErrMalformedResponse
	}
	var decoded sekaipediaURLExistenceResponse
	if err := json.Unmarshal(raw, &decoded); err != nil || !decoded.BatchComplete ||
		len(decoded.Query.Pages) == 0 || len(decoded.Query.Pages) > len(targets) {
		return nil, ErrMalformedResponse
	}
	normalized, err := sekaipediaTitleRewriteMap(decoded.Query.Normalized)
	if err != nil {
		return nil, err
	}
	redirects, err := sekaipediaTitleRewriteMap(decoded.Query.Redirects)
	if err != nil {
		return nil, err
	}
	type pageStatus struct {
		pageID  int
		missing bool
	}
	byTitle := make(map[string]pageStatus, len(decoded.Query.Pages))
	for _, page := range decoded.Query.Pages {
		if page.Namespace != 0 || page.Title == "" {
			return nil, ErrMalformedResponse
		}
		if _, duplicate := byTitle[page.Title]; duplicate {
			return nil, ErrAmbiguous
		}
		byTitle[page.Title] = pageStatus{pageID: page.PageID, missing: page.Missing}
	}
	statuses := make([]SekaipediaPageURLStatus, len(targets))
	seenPageIDs := make(map[int]string, len(targets))
	for index, target := range targets {
		resolved := target.PageTitle
		if replacement := normalized[resolved]; replacement != "" {
			resolved = replacement
		}
		redirected := false
		seenTitles := map[string]struct{}{resolved: {}}
		for hop := 0; hop < 8; hop++ {
			replacement := redirects[resolved]
			if replacement == "" {
				break
			}
			if _, cycle := seenTitles[replacement]; cycle {
				return nil, fmt.Errorf("%w: redirect cycle for page %q", ErrAmbiguous, target.PageTitle)
			}
			seenTitles[replacement] = struct{}{}
			resolved = replacement
			redirected = true
		}
		if redirects[resolved] != "" {
			return nil, fmt.Errorf("%w: redirect chain for page %q exceeds the bound", ErrAmbiguous, target.PageTitle)
		}
		page, found := byTitle[resolved]
		if !found || page.missing || page.pageID <= 0 {
			return nil, fmt.Errorf("%w: page %q is missing", ErrRevisionChanged, target.PageTitle)
		}
		if previous := seenPageIDs[page.pageID]; previous != "" {
			return nil, fmt.Errorf("%w: pages %q and %q resolve to the same page ID", ErrAmbiguous, previous, target.PageTitle)
		}
		seenPageIDs[page.pageID] = target.PageTitle
		statuses[index] = SekaipediaPageURLStatus{
			PageTitle: target.PageTitle, CanonicalURL: target.CanonicalURL,
			ResolvedPageTitle: resolved, ResolvedCanonicalURL: canonicalRevisionURL(ProviderSekaipedia, resolved, 0),
			Redirected: redirected, PageID: page.pageID,
		}
	}
	return statuses, nil
}

func sekaipediaTitleRewriteMap(values []sekaipediaTitleRewrite) (map[string]string, error) {
	result := make(map[string]string, len(values))
	for _, value := range values {
		if value.From == "" || value.To == "" || value.From == value.To {
			return nil, ErrMalformedResponse
		}
		if _, duplicate := result[value.From]; duplicate {
			return nil, ErrAmbiguous
		}
		result[value.From] = value.To
	}
	return result, nil
}
