package lyricssource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const moegirlPublicHost = "zh.moegirl.org.cn"

// MoegirlPageURLTarget binds one exact user-authorized public page URL to the
// article title decoded from that URL. It never searches or guesses titles.
type MoegirlPageURLTarget struct {
	PageURL   string `json:"pageUrl"`
	PageTitle string `json:"pageTitle"`
}

// MoegirlPageURLStatus records the result of one direct request to the exact
// user-authorized public URL. Redirects are not followed implicitly.
type MoegirlPageURLStatus struct {
	PageURL        string `json:"pageUrl"`
	FinalURL       string `json:"finalUrl"`
	PageTitle      string `json:"pageTitle"`
	StatusCode     int    `json:"statusCode"`
	Status         string `json:"status"`
	ContentType    string `json:"contentType"`
	ContentBytes   int    `json:"contentBytes"`
	Redirected     bool   `json:"redirected"`
	RedirectTarget string `json:"redirectTarget,omitempty"`
}

// MoegirlURLPreflightBatch records the exact direct response identity for the
// single bounded public-page request.
type MoegirlURLPreflightBatch struct {
	RequestURL string `json:"requestUrl"`
	FetchedAt  string `json:"fetchedAt"`
	RawSHA256  string `json:"rawSha256"`
	Raw        []byte `json:"-"`
}

// MoegirlPageURLTargetForURL accepts only one canonical HTTPS public article
// URL on zh.moegirl.org.cn and derives its exact decoded article title.
func MoegirlPageURLTargetForURL(value string) (MoegirlPageURLTarget, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed == nil || parsed.Scheme != "https" || parsed.Host != moegirlPublicHost ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" ||
		!strings.HasPrefix(parsed.Path, "/") {
		return MoegirlPageURLTarget{}, ErrMalformedResponse
	}
	title := strings.TrimPrefix(parsed.Path, "/")
	if strings.Contains(title, "/") || !validSekaipediaTargetTitle(title) {
		return MoegirlPageURLTarget{}, ErrMalformedResponse
	}
	canonical := moegirlPublicPageURL(title)
	if canonical == "" || canonical != value {
		return MoegirlPageURLTarget{}, ErrMalformedResponse
	}
	return MoegirlPageURLTarget{PageURL: value, PageTitle: title}, nil
}

func moegirlPublicPageURL(title string) string {
	if !validSekaipediaTargetTitle(title) {
		return ""
	}
	return (&url.URL{Scheme: "https", Host: moegirlPublicHost, Path: "/" + title}).String()
}

// PreflightMoegirlPageURL directly requests one exact user-authorized public
// page URL before content extraction. The supplied client must disable redirect
// following so every actual request remains separately reviewed and admitted.
func PreflightMoegirlPageURL(
	ctx context.Context,
	target MoegirlPageURLTarget,
	httpClient *http.Client,
	maxAttempts int,
	retryDelay time.Duration,
) (MoegirlPageURLStatus, MoegirlURLPreflightBatch, error) {
	if ctx == nil || httpClient == nil || target.PageURL != moegirlPublicPageURL(target.PageTitle) ||
		maxAttempts < 1 || maxAttempts > 5 || retryDelay < 0 {
		return MoegirlPageURLStatus{}, MoegirlURLPreflightBatch{},
			errors.New("Moegirl direct URL preflight configuration is invalid")
	}
	var (
		response *http.Response
		raw      []byte
		err      error
	)
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		request, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, target.PageURL, nil)
		if requestErr != nil {
			return MoegirlPageURLStatus{}, MoegirlURLPreflightBatch{}, requestErr
		}
		request.Header.Set("Accept", "text/html,application/xhtml+xml")
		request.Header.Set("User-Agent", "moesekai-lyrics-source/1")
		response, err = httpClient.Do(request)
		if err == nil {
			raw, err = io.ReadAll(io.LimitReader(response.Body, int64(maxResponseBytes)+1))
			closeErr := response.Body.Close()
			err = errors.Join(err, closeErr)
			if len(raw) > maxResponseBytes {
				err = ErrMalformedResponse
			}
		}
		if err == nil && response != nil &&
			response.StatusCode != http.StatusTooManyRequests && response.StatusCode != http.StatusServiceUnavailable {
			break
		}
		if err == nil && response != nil {
			err = &HTTPError{StatusCode: response.StatusCode}
		}
		if attempt == maxAttempts || !retryableSekaipediaURLPreflightError(err) {
			return MoegirlPageURLStatus{}, MoegirlURLPreflightBatch{}, err
		}
		if err := waitSekaipediaURLPreflightRetry(ctx, retryDelay); err != nil {
			return MoegirlPageURLStatus{}, MoegirlURLPreflightBatch{}, err
		}
	}
	if response == nil || response.Request == nil || response.Request.URL == nil {
		return MoegirlPageURLStatus{}, MoegirlURLPreflightBatch{}, ErrMalformedResponse
	}
	fetchedAt := time.Now().UTC()
	digest := sha256.Sum256(raw)
	batch := MoegirlURLPreflightBatch{
		RequestURL: target.PageURL, FetchedAt: canonicalFetchedAt(fetchedAt),
		RawSHA256: hex.EncodeToString(digest[:]), Raw: append([]byte(nil), raw...),
	}
	contentType := strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0])
	finalURL := response.Request.URL.String()
	status := MoegirlPageURLStatus{
		PageURL: target.PageURL, FinalURL: finalURL, PageTitle: target.PageTitle,
		StatusCode: response.StatusCode, Status: response.Status,
		ContentType: contentType, ContentBytes: len(raw), Redirected: finalURL != target.PageURL,
		RedirectTarget: response.Header.Get("Location"),
	}
	if finalURL != target.PageURL {
		return status, batch, fmt.Errorf("%w: exact Moegirl public URL changed during request", ErrRevisionChanged)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return status, batch, &HTTPError{StatusCode: response.StatusCode}
	}
	if contentType != "text/html" || len(raw) == 0 {
		return status, batch, ErrMalformedResponse
	}
	return status, batch, nil
}
