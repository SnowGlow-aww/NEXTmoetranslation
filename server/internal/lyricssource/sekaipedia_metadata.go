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
	"strconv"
	"strings"
	"time"
)

const (
	SekaipediaMetadataMapped         = "mapped"
	SekaipediaMetadataMissingInfobox = "missing_infobox"
	SekaipediaMetadataInvalidSongID  = "invalid_song_id"
)

// SekaipediaSongPageMetadata contains only the lead-section identity needed to
// bind a provider page to a catalog music ID. Romaji fields are deliberately
// neither represented nor returned.
type SekaipediaSongPageMetadata struct {
	PageTitle            string `json:"pageTitle"`
	CanonicalURL         string `json:"canonicalUrl"`
	ResolvedPageTitle    string `json:"resolvedPageTitle"`
	ResolvedCanonicalURL string `json:"resolvedCanonicalUrl"`
	PageID               int    `json:"pageId"`
	RevisionID           int    `json:"revisionId"`
	RevisionTimestamp    string `json:"revisionTimestamp"`
	SHA1                 string `json:"sha1"`
	MusicID              int    `json:"musicId,omitempty"`
	JapaneseTitle        string `json:"japaneseTitle,omitempty"`
	Status               string `json:"status"`
}

type SekaipediaMetadataBatch struct {
	FirstTargetIndex int      `json:"firstTargetIndex"`
	TargetCount      int      `json:"targetCount"`
	PageTitles       []string `json:"pageTitles"`
	RequestURL       string   `json:"requestUrl"`
	FetchedAt        string   `json:"fetchedAt"`
	RawSHA256        string   `json:"rawSha256"`
	Raw              []byte   `json:"-"`
}

// FetchSekaipediaSongPageMetadata reads only lead sections (rvsection=0) in
// bounded title batches. One Client owns all calls, so maxlag, Retry-After,
// request spacing, and the one-in-flight rule remain provider-wide.
func FetchSekaipediaSongPageMetadata(
	ctx context.Context,
	targets []SekaipediaPageURLStatus,
	httpClient *http.Client,
	crawlDelay time.Duration,
	cacheTTL time.Duration,
	maxAttempts int,
	retryDelay time.Duration,
) ([]SekaipediaSongPageMetadata, []SekaipediaMetadataBatch, error) {
	if ctx == nil || httpClient == nil || len(targets) == 0 || len(targets) > 10_000 ||
		crawlDelay < defaultProviderCrawlDelay || cacheTTL < defaultProviderCacheTTL ||
		maxAttempts < 1 || maxAttempts > 5 || retryDelay < 0 {
		return nil, nil, errors.New("Sekaipedia metadata configuration is invalid")
	}
	seenPageIDs := make(map[int]struct{}, len(targets))
	seenTitles := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		if target.PageID <= 0 || !validSekaipediaTargetTitle(target.PageTitle) ||
			!validSekaipediaTargetTitle(target.ResolvedPageTitle) ||
			target.CanonicalURL != canonicalRevisionURL(ProviderSekaipedia, target.PageTitle, 0) ||
			target.ResolvedCanonicalURL != canonicalRevisionURL(ProviderSekaipedia, target.ResolvedPageTitle, 0) {
			return nil, nil, errors.New("Sekaipedia metadata target is invalid")
		}
		if _, duplicate := seenPageIDs[target.PageID]; duplicate {
			return nil, nil, errors.New("Sekaipedia metadata page ID is duplicated")
		}
		key := strings.ToLower(strings.ReplaceAll(target.ResolvedPageTitle, "_", " "))
		if _, duplicate := seenTitles[key]; duplicate {
			return nil, nil, errors.New("Sekaipedia metadata resolved title is duplicated")
		}
		seenPageIDs[target.PageID] = struct{}{}
		seenTitles[key] = struct{}{}
	}

	client := newMediaWikiClient(sekaipediaAPI, crawlDelay, cacheTTL, httpClient)
	metadata := make([]SekaipediaSongPageMetadata, 0, len(targets))
	batches := make([]SekaipediaMetadataBatch, 0, (len(targets)+MaxSekaipediaURLPreflightBatch-1)/MaxSekaipediaURLPreflightBatch)
	for start := 0; start < len(targets); start += MaxSekaipediaURLPreflightBatch {
		end := start + MaxSekaipediaURLPreflightBatch
		if end > len(targets) {
			end = len(targets)
		}
		batchMetadata, batch, err := fetchSekaipediaMetadataBatch(
			ctx, client, targets[start:end], start, maxAttempts, retryDelay,
		)
		if len(batch.Raw) > 0 {
			batches = append(batches, batch)
		}
		if err != nil {
			return metadata, batches, fmt.Errorf("Sekaipedia metadata targets %d-%d: %w", start+1, end, err)
		}
		metadata = append(metadata, batchMetadata...)
	}
	if len(metadata) != len(targets) {
		return metadata, batches, ErrMalformedResponse
	}
	return metadata, batches, nil
}

func fetchSekaipediaMetadataBatch(
	ctx context.Context,
	client *Client,
	targets []SekaipediaPageURLStatus,
	firstTargetIndex int,
	maxAttempts int,
	retryDelay time.Duration,
) ([]SekaipediaSongPageMetadata, SekaipediaMetadataBatch, error) {
	titles := make([]string, len(targets))
	for index, target := range targets {
		titles[index] = target.ResolvedPageTitle
	}
	params := url.Values{
		"action":        {"query"},
		"format":        {"json"},
		"formatversion": {"2"},
		"prop":          {"revisions"},
		"rvprop":        {"ids|timestamp|sha1|content"},
		"rvsection":     {"0"},
		"rvslots":       {"main"},
		"titles":        {strings.Join(titles, "|")},
	}
	requestParams := mediaWikiActionRequestParams(params)
	requestURL, err := canonicalMediaWikiRequestURL(sekaipediaAPI, requestParams)
	if err != nil {
		return nil, SekaipediaMetadataBatch{}, err
	}
	var raw []byte
	var fetchedAt time.Time
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		raw, err = client.request(ctx, "page", params, false)
		if err == nil {
			fetchedAt = time.Now().UTC()
			break
		}
		if attempt == maxAttempts || !retryableSekaipediaURLPreflightError(err) {
			return nil, SekaipediaMetadataBatch{}, err
		}
		if err := waitSekaipediaURLPreflightRetry(ctx, retryDelay); err != nil {
			return nil, SekaipediaMetadataBatch{}, err
		}
	}
	digest := sha256.Sum256(raw)
	batch := SekaipediaMetadataBatch{
		FirstTargetIndex: firstTargetIndex, TargetCount: len(targets),
		PageTitles: append([]string(nil), titles...), RequestURL: requestURL,
		FetchedAt: canonicalFetchedAt(fetchedAt), RawSHA256: hex.EncodeToString(digest[:]), Raw: append([]byte(nil), raw...),
	}
	parsed, err := parseSekaipediaMetadataResponse(raw, targets)
	if err != nil {
		return nil, batch, err
	}
	return parsed, batch, nil
}

type sekaipediaMetadataResponse struct {
	BatchComplete bool `json:"batchcomplete"`
	Error         *struct {
		Code string `json:"code"`
		Info string `json:"info"`
	} `json:"error"`
	Query struct {
		Pages []struct {
			PageID    int    `json:"pageid"`
			Namespace int    `json:"ns"`
			Title     string `json:"title"`
			Missing   bool   `json:"missing"`
			Revisions []struct {
				RevisionID int    `json:"revid"`
				Timestamp  string `json:"timestamp"`
				SHA1       string `json:"sha1"`
				Slots      struct {
					Main struct {
						Content string `json:"content"`
					} `json:"main"`
				} `json:"slots"`
			} `json:"revisions"`
		} `json:"pages"`
	} `json:"query"`
}

func parseSekaipediaMetadataResponse(
	raw []byte,
	targets []SekaipediaPageURLStatus,
) ([]SekaipediaSongPageMetadata, error) {
	if len(raw) == 0 || len(raw) > maxResponseBytes {
		return nil, ErrMalformedResponse
	}
	var decoded sekaipediaMetadataResponse
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded.Error != nil || !decoded.BatchComplete ||
		len(decoded.Query.Pages) != len(targets) {
		if decoded.Error != nil {
			return nil, fmt.Errorf("MediaWiki metadata API error %s: %s", decoded.Error.Code, decoded.Error.Info)
		}
		return nil, ErrMalformedResponse
	}
	byPageID := make(map[int]SekaipediaSongPageMetadata, len(decoded.Query.Pages))
	for _, page := range decoded.Query.Pages {
		if page.Missing || page.PageID <= 0 || page.Namespace != 0 || page.Title == "" || len(page.Revisions) != 1 {
			return nil, ErrMalformedResponse
		}
		revision := page.Revisions[0]
		if revision.RevisionID <= 0 || revision.Timestamp == "" || !HasCanonicalSHA1(revision.SHA1) || revision.Slots.Main.Content == "" {
			return nil, ErrMalformedResponse
		}
		entry := SekaipediaSongPageMetadata{
			ResolvedPageTitle: page.Title, ResolvedCanonicalURL: canonicalRevisionURL(ProviderSekaipedia, page.Title, 0),
			PageID: page.PageID, RevisionID: revision.RevisionID,
			RevisionTimestamp: revision.Timestamp, SHA1: revision.SHA1,
		}
		params, err := parseSekaipediaInfoboxSongParams(revision.Slots.Main.Content)
		if err != nil {
			entry.Status = SekaipediaMetadataMissingInfobox
		} else {
			musicID, musicIDErr := strconv.Atoi(strings.TrimSpace(params["song id"]))
			if musicIDErr != nil || musicID <= 0 {
				entry.Status = SekaipediaMetadataInvalidSongID
			} else {
				entry.Status = SekaipediaMetadataMapped
				entry.MusicID = musicID
				entry.JapaneseTitle = identityDisplayText(params["japanese"])
				if entry.JapaneseTitle == "" {
					entry.JapaneseTitle = identityDisplayText(params["song name"])
				}
			}
		}
		if _, duplicate := byPageID[page.PageID]; duplicate {
			return nil, ErrAmbiguous
		}
		byPageID[page.PageID] = entry
	}
	result := make([]SekaipediaSongPageMetadata, len(targets))
	for index, target := range targets {
		entry, found := byPageID[target.PageID]
		if !found || entry.ResolvedPageTitle != target.ResolvedPageTitle {
			return nil, ErrRevisionChanged
		}
		entry.PageTitle = target.PageTitle
		entry.CanonicalURL = target.CanonicalURL
		result[index] = entry
	}
	return result, nil
}
