package lyricssource

import (
	"bytes"
	"context"

	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"io"
	"log"
	"net/http"
	"net/url"

	"sort"
	"strings"
	"sync"
	"time"

	"moesekai/server/internal/model"
)

type wikiPage struct {
	pageID            int
	title             string
	revisionID        int
	revisionTimestamp time.Time
	sha1              string
	rawSHA256         string
	categories        []string
	content           string
	fetchedAt         time.Time
	rawResponse       []byte
	rawResponseSHA256 string
	indexEvidenceRefs []model.LyricsSourceIndexEvidenceRef
	indexEvidence     []IndexEvidence
}

func (c *Client) fetchPage(ctx context.Context, pageID, revisionID int, cacheable bool) (wikiPage, error) {
	if err := ctx.Err(); err != nil {
		return wikiPage{}, err
	}
	params := url.Values{
		"action": {"query"}, "format": {"json"}, "prop": {"revisions|categories"},
		"rvprop": {"ids|sha1|content"}, "rvslots": {"main"}, "cllimit": {"max"},
		"maxlag": {mediaWikiMaxLag},
	}
	if revisionID > 0 {
		params.Set("revids", fmt.Sprintf("%d", revisionID))
	} else {
		params.Set("pageids", fmt.Sprintf("%d", pageID))
		params.Set("rvlimit", "1")
	}
	data, fetchedAt, err := c.requestWithFetchedAt(ctx, "page", params, cacheable)
	if err != nil {
		return wikiPage{}, err
	}
	if err := ctx.Err(); err != nil {
		return wikiPage{}, err
	}
	page, err := parseAcquiredPageResponse(data, fetchedAt)
	if contextErr := ctx.Err(); contextErr != nil {
		return wikiPage{}, contextErr
	}
	if err != nil {
		return wikiPage{}, err
	}
	return page, nil
}

func parseAcquiredPageResponse(data []byte, fetchedAt time.Time) (wikiPage, error) {
	page, err := parsePageResponse(data)
	if err != nil || fetchedAt.IsZero() {
		return wikiPage{}, ErrMalformedResponse
	}
	responseDigest := sha256.Sum256(data)
	page.fetchedAt = fetchedAt.UTC()
	page.rawResponse = append([]byte(nil), data...)
	page.rawResponseSHA256 = fmt.Sprintf("%x", responseDigest)
	return page, nil
}

type pageResponse struct {
	Error json.RawMessage `json:"error"`
	Query *struct {
		Pages pageResponsePages `json:"pages"`
	} `json:"query"`
}

type pageResponsePages []pageResponseItem

func (pages *pageResponsePages) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return errors.New("missing MediaWiki pages")
	}
	switch trimmed[0] {
	case '[':
		var values []pageResponseItem
		if err := json.Unmarshal(trimmed, &values); err != nil {
			return err
		}
		*pages = values
		return nil
	case '{':
		var values map[string]pageResponseItem
		if err := json.Unmarshal(trimmed, &values); err != nil {
			return err
		}
		result := make([]pageResponseItem, 0, len(values))
		for _, value := range values {
			result = append(result, value)
		}
		sort.Slice(result, func(left, right int) bool {
			if result[left].PageID != result[right].PageID {
				return result[left].PageID < result[right].PageID
			}
			return result[left].Title < result[right].Title
		})
		*pages = result
		return nil
	default:
		return errors.New("invalid MediaWiki pages")
	}
}

type pageResponseItem struct {
	PageID     int    `json:"pageid"`
	Title      string `json:"title"`
	Missing    bool   `json:"missing"`
	Invalid    bool   `json:"invalid"`
	Categories []struct {
		Title string `json:"title"`
	} `json:"categories"`
	Revisions []struct {
		RevisionID int    `json:"revid"`
		Timestamp  string `json:"timestamp"`
		SHA1       string `json:"sha1"`
		Slots      struct {
			Main struct {
				LegacyContent string `json:"*"`
				Content       string `json:"content"`
			} `json:"main"`
		} `json:"slots"`
		LegacyContent string `json:"*"`
	} `json:"revisions"`
}

func parseSearchResponse(data []byte) ([]wikiPage, error) {
	return parseSearchResponseWithLimit(data, maxSearchPages)
}

func parseSearchResponseWithLimit(data []byte, maxPages int) ([]wikiPage, error) {
	if maxPages <= 0 || maxPages > maxSearchPages {
		return nil, ErrMalformedResponse
	}
	var response pageResponse
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &response); err != nil || json.Unmarshal(data, &root) != nil || root == nil || len(response.Error) > 0 {
		return nil, ErrMalformedResponse
	}
	// MediaWiki omits the entire query object for a valid zero-result generator
	// search and returns only batchcomplete/limits metadata. Do not confuse that
	// canonical shape with an arbitrary empty object or an explicit null query.
	rawQuery, hasQuery := root["query"]
	if !hasQuery {
		if raw, ok := root["batchcomplete"]; !ok || !validMediaWikiBatchComplete(raw) {
			return nil, ErrMalformedResponse
		}
		return []wikiPage{}, nil
	}
	if bytes.Equal(bytes.TrimSpace(rawQuery), []byte("null")) || response.Query == nil || response.Query.Pages == nil || len(response.Query.Pages) > maxPages {
		return nil, ErrMalformedResponse
	}
	pages := make([]wikiPage, 0, len(response.Query.Pages))
	for _, item := range response.Query.Pages {
		page, err := parsePageResponseItem(item)
		if err != nil {
			return nil, ErrMalformedResponse
		}
		pages = append(pages, page)
	}
	sort.Slice(pages, func(left, right int) bool {
		if pages[left].pageID != pages[right].pageID {
			return pages[left].pageID < pages[right].pageID
		}
		return pages[left].revisionID < pages[right].revisionID
	})
	return pages, nil
}

func validMediaWikiBatchComplete(raw json.RawMessage) bool {
	var asString string
	if json.Unmarshal(raw, &asString) == nil {
		return true
	}
	var asBool bool
	return json.Unmarshal(raw, &asBool) == nil && asBool
}

func parsePageResponse(data []byte) (wikiPage, error) {
	response, err := decodePageResponse(data)
	if err != nil || len(response.Query.Pages) != 1 {
		return wikiPage{}, ErrMalformedResponse
	}
	return parsePageResponseItem(response.Query.Pages[0])
}

func decodePageResponse(data []byte) (pageResponse, error) {
	var response pageResponse
	if err := json.Unmarshal(data, &response); err != nil || len(response.Error) > 0 || response.Query == nil || response.Query.Pages == nil {
		return pageResponse{}, ErrMalformedResponse
	}
	return response, nil
}

func parsePageResponseItem(item pageResponseItem) (wikiPage, error) {
	if item.PageID <= 0 || strings.TrimSpace(item.Title) == "" || item.Missing || item.Invalid || len(item.Revisions) != 1 {
		return wikiPage{}, ErrMalformedResponse
	}
	revision := item.Revisions[0]
	if revision.RevisionID <= 0 || !mediaWikiSHA1Pattern.MatchString(revision.SHA1) {
		return wikiPage{}, ErrMalformedResponse
	}
	var revisionTimestamp time.Time
	if revision.Timestamp != "" {
		parsed, err := time.Parse(time.RFC3339Nano, revision.Timestamp)
		if err != nil || parsed.UTC().Format(time.RFC3339Nano) != revision.Timestamp || !strings.HasSuffix(revision.Timestamp, "Z") {
			return wikiPage{}, ErrMalformedResponse
		}
		revisionTimestamp = parsed.UTC()
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
		if !strings.HasPrefix(category.Title, "Category:") || len(category.Title) == len("Category:") {
			return wikiPage{}, ErrMalformedResponse
		}
		categories = append(categories, strings.TrimPrefix(category.Title, "Category:"))
	}
	sort.Strings(categories)
	for index := 1; index < len(categories); index++ {
		if categories[index-1] == categories[index] {
			return wikiPage{}, ErrMalformedResponse
		}
	}
	rawDigest := sha256.Sum256([]byte(content))
	return wikiPage{
		pageID: item.PageID, title: item.Title, revisionID: revision.RevisionID, revisionTimestamp: revisionTimestamp,
		sha1: revision.SHA1, rawSHA256: fmt.Sprintf("%x", rawDigest), categories: categories, content: content,
	}, nil
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return retryAfterFallback
	}

	allDigits := true
	saturated := false
	seconds := uint64(0)
	maxSeconds := uint64(maximumRetryAfterDelay / time.Second)
	for index := 0; index < len(value); index++ {
		digit := value[index]
		if digit < '0' || digit > '9' {
			allDigits = false
			break
		}
		if saturated {
			continue
		}
		next := seconds*10 + uint64(digit-'0')
		if next > maxSeconds {
			saturated = true
			continue
		}
		seconds = next
	}
	if allDigits {
		if saturated {
			return maximumRetryAfterDelay
		}
		return time.Duration(seconds) * time.Second
	}

	retryAt, err := http.ParseTime(value)
	if err != nil {
		return retryAfterFallback
	}
	if !retryAt.After(now) {
		return 0
	}
	return retryAt.Sub(now)
}

func (c *Client) extendCooldown(retryAfter string, now time.Time) {
	delay := parseRetryAfter(retryAfter, now)
	if delay <= 0 {
		return
	}
	until := now.Add(delay)
	if c.recoverySafety != nil {
		c.recoverySafety.rateMu.Lock()
		if until.After(c.recoverySafety.cooldownUntil) {
			c.recoverySafety.cooldownUntil = until
		}
		c.recoverySafety.rateMu.Unlock()
		return
	}
	c.rateMu.Lock()
	if until.After(c.cooldownUntil) {
		c.cooldownUntil = until
	}
	c.rateMu.Unlock()
}

func mediaWikiActionRequestParams(params url.Values) url.Values {
	cloned := make(url.Values, len(params)+1)
	for key, values := range params {
		cloned[key] = append([]string(nil), values...)
	}
	if strings.TrimSpace(cloned.Get("action")) != "" {
		cloned.Set("maxlag", mediaWikiMaxLag)
	}
	return cloned
}

func (c *Client) request(ctx context.Context, action string, params url.Values, cacheable bool) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	params = mediaWikiActionRequestParams(params)
	if !cacheable {
		body, _, err := c.requestUncached(ctx, action, params.Encode())
		return body, err
	}
	return c.requestCoalesced(ctx, action, params, true)
}

func (c *Client) requestWithFetchedAt(
	ctx context.Context,
	action string,
	params url.Values,
	cacheable bool,
) ([]byte, time.Time, error) {
	if err := ctx.Err(); err != nil {
		return nil, time.Time{}, err
	}
	params = mediaWikiActionRequestParams(params)
	if !cacheable {
		return c.requestUncached(ctx, action, params.Encode())
	}
	return c.requestCoalescedWithFetchedAt(ctx, action, params, true)
}

// requestFresh bypasses completed response-cache entries while still joining an
// identical in-flight network revalidation. Successful responses replace the
// completed cache entry for ordinary Search callers, but later fresh callers
// revalidate again.
func (c *Client) requestFresh(ctx context.Context, action string, params url.Values) ([]byte, error) {
	body, _, err := c.requestCoalescedWithFetchedAt(ctx, action, mediaWikiActionRequestParams(params), false)
	return body, err
}

func (c *Client) requestCoalesced(ctx context.Context, action string, params url.Values, allowCacheHit bool) ([]byte, error) {
	body, _, err := c.requestCoalescedWithFetchedAt(ctx, action, params, allowCacheHit)
	return body, err
}

func (c *Client) requestCoalescedWithFetchedAt(
	ctx context.Context,
	action string,
	params url.Values,
	allowCacheHit bool,
) ([]byte, time.Time, error) {
	if err := ctx.Err(); err != nil {
		return nil, time.Time{}, err
	}
	key := params.Encode()
	request, cached, cacheHit := c.beginCachedRequest(key, allowCacheHit)
	if cacheHit {
		return cached.body, cached.createdAt.UTC(), nil
	}
	if request.owner {
		go func() {
			body, fetchedAt, err := c.requestUncached(request.inflight.ctx, action, key)
			if err == nil && (action == "search" || action == "creator-alias" || action == "page") {
				err = validateActionResponse(action, body)
			}
			c.finishCachedRequest(key, request.inflight, body, fetchedAt, err)
		}()
	}
	select {
	case <-ctx.Done():
		c.leaveCachedRequest(request.inflight, request.waiter)
		return nil, time.Time{}, ctx.Err()
	case <-request.inflight.done:
		return c.cachedParticipantResult(request.inflight, request.waiter)
	}
}

type cachedRequest struct {
	inflight *inflightRequest
	owner    bool
	waiter   bool
}

func (c *Client) beginCachedRequest(key string, allowCacheHit bool) (*cachedRequest, cacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cached, ok := c.cache[key]; ok {
		if allowCacheHit && time.Since(cached.createdAt) < c.cacheTTL {
			cached.body = append([]byte(nil), cached.body...)
			return nil, cached, true
		}
		if time.Since(cached.createdAt) >= c.cacheTTL {
			delete(c.cache, key)
		}
	}
	if inflight := c.inflight[key]; inflight != nil {
		inflight.waiters++
		inflight.participants++
		return &cachedRequest{inflight: inflight, waiter: true}, cacheEntry{}, false
	}
	workCtx, cancel := context.WithCancel(context.Background())
	inflight := &inflightRequest{done: make(chan struct{}), participants: 1, ctx: workCtx, cancel: cancel}
	c.inflight[key] = inflight
	return &cachedRequest{inflight: inflight, owner: true}, cacheEntry{}, false
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

func (c *Client) cachedParticipantResult(inflight *inflightRequest, waiter bool) ([]byte, time.Time, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if waiter {
		if inflight.waiters <= 0 {
			return nil, time.Time{}, ErrMalformedResponse
		}
		inflight.waiters--
	}
	if inflight.participants <= 0 {
		return nil, time.Time{}, ErrMalformedResponse
	}
	inflight.participants--
	return append([]byte(nil), inflight.body...), inflight.fetchedAt.UTC(), inflight.err
}

func validateActionResponse(action string, body []byte) error {
	switch action {
	case "search":
		_, err := parseSearchResponse(body)
		return err
	case "creator-alias":
		_, err := parseSearchResponseWithLimit(body, maxCreatorAliasPages)
		return err
	case "page":
		_, err := parsePageResponse(body)
		return err
	default:
		return nil
	}
}

func (c *Client) finishCachedRequest(
	key string,
	inflight *inflightRequest,
	body []byte,
	fetchedAt time.Time,
	err error,
) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if current := c.inflight[key]; current != inflight {
		return
	}
	if err == nil {
		fetchedAt = fetchedAt.UTC()
		for cacheKey, cached := range c.cache {
			if fetchedAt.Sub(cached.createdAt) >= c.cacheTTL {
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
		c.cache[key] = cacheEntry{body: append([]byte(nil), body...), createdAt: fetchedAt}
	}
	inflight.body = append([]byte(nil), body...)
	inflight.fetchedAt = fetchedAt
	inflight.err = err
	delete(c.inflight, key)
	close(inflight.done)
	inflight.cancel()
}

func mediaWikiRetryableAPIStatus(body []byte) (int, bool) {
	var response struct {
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &response); err != nil || response.Error == nil {
		return 0, false
	}
	switch strings.ToLower(strings.TrimSpace(response.Error.Code)) {
	case "maxlag":
		return http.StatusTooManyRequests, true
	default:
		return 0, false
	}
}

func (c *Client) requestUncached(ctx context.Context, action, key string) ([]byte, time.Time, error) {
	if err := c.acquireRequestSlot(ctx); err != nil {
		return nil, time.Time{}, err
	}
	defer c.releaseRequestSlot()

	separator := "?"
	if strings.Contains(c.endpoint, "?") {
		separator = "&"
	}
	requestURL := c.endpoint + separator + key
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, time.Time{}, err
	}
	if query := req.URL.Query(); strings.TrimSpace(query.Get("action")) != "" {
		query.Set("maxlag", mediaWikiMaxLag)
		req.URL.RawQuery = query.Encode()
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "moesekai-lyrics-source/1")
	offlineRequest := false
	if transport, ok := c.httpClient.Transport.(recoveryOfflineRequestTransport); ok {
		offlineRequest = transport.RecoveryRequestOffline(req)
	}
	if !offlineRequest {
		if err := c.acquireActualHTTPRequest(ctx); err != nil {
			return nil, time.Time{}, err
		}
		defer c.releaseActualHTTPRequest()
		if err := c.waitRateLimit(ctx); err != nil {
			return nil, time.Time{}, err
		}
		log.Printf("[lyrics-source] request action=%s", action)
	}
	client := *c.httpClient
	baseRedirectPolicy := c.httpClient.CheckRedirect
	originalScheme := req.URL.Scheme
	originalHost := req.URL.Host
	originalPath := req.URL.EscapedPath()
	strictEndpointPath := originalPath == "/w/api.php"
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if next.URL.Scheme != originalScheme || !strings.EqualFold(next.URL.Host, originalHost) {
			return fmt.Errorf("lyrics source redirect changed origin")
		}
		// Sekaipedia acquisition is pinned to its exact /w/api.php endpoint;
		// other providers retain the existing same-origin redirect contract.
		if strictEndpointPath && next.URL.EscapedPath() != originalPath {
			return fmt.Errorf("lyrics source redirect changed endpoint path")
		}
		if query := next.URL.Query(); strings.TrimSpace(query.Get("action")) != "" {
			query.Set("maxlag", mediaWikiMaxLag)
			next.URL.RawQuery = query.Encode()
		}
		if len(via) > maxSourceRedirects {
			return fmt.Errorf("lyrics source redirect limit exceeded")
		}
		if baseRedirectPolicy != nil {
			return baseRedirectPolicy(next, via)
		}
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, time.Time{}, err
	}
	if len(body) > maxResponseBytes {
		return nil, time.Time{}, fmt.Errorf("lyrics source response too large")
	}

	fetchedAt := time.Now().UTC()
	var recoveryResponse RecoveryHTTPResponse
	if transport, ok := c.httpClient.Transport.(RecoveryHTTPTransport); ok {
		fetchedAt, err = transport.RecoveryFetchedAt(req, resp)
		if err != nil {
			return nil, time.Time{}, err
		}
		if fetchedAt.IsZero() || fetchedAt.Location() != time.UTC {
			return nil, time.Time{}, errors.New("recovery response fetchedAt must be a non-zero UTC timestamp")
		}
		canonicalRequestURL := req.URL.String()
		if resp.Request != nil && resp.Request.URL != nil {
			canonicalRequestURL = resp.Request.URL.String()
		}
		recoveryResponse = RecoveryHTTPResponse{
			Action: action, CanonicalRequestURL: canonicalRequestURL, FetchedAt: fetchedAt,
			StatusCode: resp.StatusCode, Status: resp.Status, Header: resp.Header.Clone(),
			Raw: append([]byte{}, body...),
		}
		if err := transport.RecoveryRetainResponse(ctx, req, resp, recoveryResponse); err != nil {
			return nil, time.Time{}, err
		}
	}

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
			c.extendCooldown(resp.Header.Get("Retry-After"), time.Now())
		}
		return nil, time.Time{}, &HTTPError{StatusCode: resp.StatusCode}
	}
	if status, retryable := mediaWikiRetryableAPIStatus(body); retryable {
		c.extendCooldown(resp.Header.Get("Retry-After"), time.Now())
		return nil, time.Time{}, &HTTPError{StatusCode: status}
	}
	if transport, ok := c.httpClient.Transport.(RecoveryHTTPTransport); ok {
		if err := transport.RecoveryCommitResponse(ctx, recoveryResponse); err != nil {
			return nil, time.Time{}, err
		}
	}
	return body, fetchedAt, nil
}

func (c *Client) actualHTTPRequestSemaphore() chan struct{} {
	if c.recoverySafety != nil {
		c.recoverySafety.actualHTTPOnce.Do(func() {
			c.recoverySafety.actualHTTPToken = make(chan struct{}, 1)
			c.recoverySafety.actualHTTPToken <- struct{}{}
		})
		return c.recoverySafety.actualHTTPToken
	}
	c.actualHTTPOnce.Do(func() {
		c.actualHTTPToken = make(chan struct{}, 1)
		c.actualHTTPToken <- struct{}{}
	})
	return c.actualHTTPToken
}

func (c *Client) acquireActualHTTPRequest(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.actualHTTPRequestSemaphore():
		return nil
	}
}

func (c *Client) releaseActualHTTPRequest() {
	c.actualHTTPRequestSemaphore() <- struct{}{}
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
	if c.recoverySafety != nil {
		return waitRateLimitState(
			ctx, c.minInterval, &c.recoverySafety.rateMu,
			&c.recoverySafety.lastRequest, &c.recoverySafety.cooldownUntil,
			c.recoverySafety.rateToken,
		)
	}
	return waitRateLimitState(ctx, c.minInterval, &c.rateMu, &c.lastRequest, &c.cooldownUntil, c.rateToken)
}

func waitRateLimitState(
	ctx context.Context,
	minimumInterval time.Duration,
	mu *sync.Mutex,
	lastRequest *time.Time,
	cooldownUntil *time.Time,
	token chan struct{},
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-token:
	}
	if err := ctx.Err(); err != nil {
		token <- struct{}{}
		return err
	}
	defer func() { token <- struct{}{} }()

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		mu.Lock()
		now := time.Now()
		nextRequest := lastRequest.Add(minimumInterval)
		if cooldownUntil.After(nextRequest) {
			nextRequest = *cooldownUntil
		}
		wait := nextRequest.Sub(now)
		if wait <= 0 {
			if err := ctx.Err(); err != nil {
				mu.Unlock()
				return err
			}
			*lastRequest = now
			mu.Unlock()
			return nil
		}
		mu.Unlock()
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
