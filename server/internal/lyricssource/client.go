package lyricssource

import (
	"context"

	"errors"
	"fmt"
	"html"

	"net/http"
	"net/url"
	"regexp"

	"strings"
	"sync"
	"time"

	"moesekai/server/internal/model"
)

const (
	vocaloidWikiAPI          = "https://vocaloid.fandom.com/api.php"
	maxResponseBytes         = 2 << 20
	maxSearchPages           = 32
	maxCreatorAliasLookups   = 8
	maxCreatorAliasPages     = 3
	maxTitleSearchQueries    = 6
	maxCacheEntries          = 128
	maxInflightRequests      = 16
	maxSourceRedirects       = 5
	maxExtractedLines        = 1000
	maxExtractedLineBytes    = 8 << 10
	maxExtractedTextBytes    = 1 << 20
	MaxIndexEvidenceRawBytes = maxResponseBytes
	defaultRequestInterval   = time.Second
	mediaWikiMaxLag          = "5"
	retryAfterFallback       = time.Minute
	maximumRetryAfterDelay   = time.Duration(1<<63 - 1)
)

var (
	canonicalIndexEvidenceID     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,255}$`)
	canonicalIndexEvidenceSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)

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
	MusicID                     int
	JapaneseTitle               string
	ProducerMetadata            string
	Lyricist                    string
	Composer                    string
	Arranger                    string
	PerformerSegmentationPolicy PerformerSegmentationPolicy
	Instrumental                bool
}

type Candidate struct {
	Provider          model.LyricsSourceProvider           `json:"provider,omitempty"`
	Origin            string                               `json:"origin,omitempty"`
	PageID            int                                  `json:"pageId"`
	Title             string                               `json:"title"`
	CanonicalURL      string                               `json:"canonicalUrl"`
	RevisionID        int                                  `json:"revisionId"`
	RevisionTimestamp string                               `json:"revisionTimestamp,omitempty"`
	SHA1              string                               `json:"sha1"`
	RawSHA256         string                               `json:"rawSha256,omitempty"`
	Categories        []string                             `json:"categories"`
	FetchedAt         string                               `json:"fetchedAt,omitempty"`
	Section           string                               `json:"section,omitempty"`
	RenditionKey      string                               `json:"renditionKey,omitempty"`
	VersionReason     model.LyricsSourceVersionReasonCode  `json:"versionReason,omitempty"`
	IndexEvidenceRefs []model.LyricsSourceIndexEvidenceRef `json:"indexEvidenceRefs,omitempty"`
	IndexEvidence     []IndexEvidence                      `json:"indexEvidence,omitempty"`
}

type IndexEvidenceKind string

const (
	IndexEvidenceKindMediaWikiRevision       IndexEvidenceKind = "mediawiki_revision"
	IndexEvidenceKindMediaWikiSearchResponse IndexEvidenceKind = "mediawiki_search_response"
	IndexEvidenceKindExactPublicHTML         IndexEvidenceKind = "exact_public_html"
)

// IndexEvidence is the private discovery/fetch transport that resolves one
// compact public-document evidence reference to immutable bytes and their
// provider-specific acquisition identity. Raw bytes remain internal and never
// enter model.LyricsSourceDocument.
type IndexEvidence struct {
	EvidenceID          string                     `json:"evidenceId"`
	SHA256              string                     `json:"sha256"`
	Kind                IndexEvidenceKind          `json:"kind"`
	Provider            model.LyricsSourceProvider `json:"provider"`
	Origin              string                     `json:"origin"`
	PageID              int                        `json:"pageId,omitempty"`
	RevisionID          int                        `json:"revisionId,omitempty"`
	RevisionTimestamp   string                     `json:"revisionTimestamp,omitempty"`
	MediaWikiSHA1       string                     `json:"mediawikiSha1,omitempty"`
	Title               string                     `json:"title,omitempty"`
	CanonicalURL        string                     `json:"canonicalRevisionUrl,omitempty"`
	Categories          []string                   `json:"categories"`
	CanonicalRequestURL string                     `json:"canonicalRequestUrl,omitempty"`
	FetchedAt           string                     `json:"fetchedAt"`
	Raw                 []byte                     `json:"raw"`
	RawSHA256           string                     `json:"rawSha256"`
}

// SearchDiagnostics contains only bounded aggregate counts for explaining why
// a successful MediaWiki search produced no verified candidate. It never
// carries page text, lyrics, URLs, titles, or creator names.
type SearchDiagnostics struct {
	SearchHits             int `json:"searchHits"`
	Restricted             int `json:"restricted"`
	RestrictedTitleMatch   int `json:"restrictedTitleMatch"`
	TitleMismatch          int `json:"titleMismatch"`
	CreditMismatch         int `json:"creditMismatch"`
	LyricistCreditMissing  int `json:"lyricistCreditMissing"`
	LyricistCreditMismatch int `json:"lyricistCreditMismatch"`
	ComposerCreditMissing  int `json:"composerCreditMissing"`
	ComposerCreditMismatch int `json:"composerCreditMismatch"`
	ArrangerCreditMissing  int `json:"arrangerCreditMissing"`
	ArrangerCreditMismatch int `json:"arrangerCreditMismatch"`
	SignalMismatch         int `json:"signalMismatch"`
	Verified               int `json:"verified"`
}

// ZeroCandidateReason is the deepest mandatory gate reached by any page in a
// successful search that produced no verified candidates. It is derived only
// from bounded aggregate counters and never exposes page or catalog text.
type ZeroCandidateReason string

const (
	ZeroCandidateNoSearchHits      ZeroCandidateReason = "no_search_hits"
	ZeroCandidateTitleMismatch     ZeroCandidateReason = "title_mismatch"
	ZeroCandidateCreditMismatch    ZeroCandidateReason = "credit_mismatch"
	ZeroCandidateRestricted        ZeroCandidateReason = "restricted"
	ZeroCandidateMissingSongSignal ZeroCandidateReason = "missing_song_signal"
)

// ZeroCandidateReason validates the aggregate envelope before assigning one
// stable reason. A deeper non-restricted gate takes precedence over shallower
// rejection buckets; restricted wins over unrelated title mismatches only when
// at least one restricted page matched the requested title.
func (d SearchDiagnostics) ZeroCandidateReason() (ZeroCandidateReason, bool) {
	counts := []int{
		d.SearchHits, d.Restricted, d.RestrictedTitleMatch, d.TitleMismatch, d.CreditMismatch,
		d.LyricistCreditMissing, d.LyricistCreditMismatch, d.ComposerCreditMissing, d.ComposerCreditMismatch,
		d.ArrangerCreditMissing, d.ArrangerCreditMismatch, d.SignalMismatch, d.Verified,
	}
	for _, count := range counts {
		if count < 0 {
			return "", false
		}
	}
	if d.RestrictedTitleMatch > d.Restricted || d.LyricistCreditMissing+d.LyricistCreditMismatch > d.CreditMismatch ||
		d.ComposerCreditMissing+d.ComposerCreditMismatch > d.CreditMismatch ||
		d.ArrangerCreditMissing+d.ArrangerCreditMismatch > d.CreditMismatch ||
		d.SearchHits != d.Restricted+d.TitleMismatch+d.CreditMismatch+d.SignalMismatch+d.Verified || d.Verified != 0 {
		return "", false
	}
	switch {
	case d.SearchHits == 0:
		return ZeroCandidateNoSearchHits, true
	case d.SignalMismatch > 0:
		return ZeroCandidateMissingSongSignal, true
	case d.CreditMismatch > 0:
		return ZeroCandidateCreditMismatch, true
	case d.RestrictedTitleMatch > 0:
		return ZeroCandidateRestricted, true
	case d.TitleMismatch > 0 || d.Restricted > d.RestrictedTitleMatch:
		return ZeroCandidateTitleMismatch, true
	default:
		return "", false
	}
}

type ExtractedLine struct {
	Japanese          string `json:"japanese"`
	StanzaBreakBefore bool   `json:"stanzaBreakBefore,omitempty"`
}

// LyricsVersion identifies the exact lyrics rendition selected from a source
// page. Multi-version pages prefer one unambiguous Project SEKAI/SEKAI
// character rendition; a sole complete VOCALOID rendition remains eligible.
type LyricsVersion struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
}

// Performer is a source-local singer identity. IDs are stable normalized keys
// used by colored fragments and ordered line-end performer squares.
type Performer struct {
	PerformerID string `json:"performerId"`
	Name        string `json:"name"`
	Color       string `json:"color,omitempty"`
}

// RubySpan is a machine-generated, reviewable furigana suggestion. Reading is
// omitted for text that should render without a ruby annotation.
type RubySpan struct {
	Text                 string                                `json:"text"`
	Reading              string                                `json:"reading,omitempty"`
	ReadingEvidenceKind  model.LyricsSourceReadingEvidenceKind `json:"-"`
	GeneratorVersion     string                                `json:"-"`
	SourceRowOrdinal     int                                   `json:"-"`
	SourceSegmentOrdinal int                                   `json:"-"`
}

// LyricsSegment preserves the Wiki's inline singer-color assignment while its
// Ruby spans preserve the exact same text in display-ready order.
type LyricsSegment struct {
	Text         string     `json:"text"`
	PerformerIDs []string   `json:"performerIds"`
	Ruby         []RubySpan `json:"ruby"`
}

// StructuredLine is private Phase 2 extraction evidence. TrailingPerformerIDs
// preserves the exact order of the small colored squares shown after a Wiki
// lyrics line; the squares are not inferred later from inline text colors.
type StructuredLine struct {
	Japanese             string          `json:"japanese"`
	StanzaBreakBefore    bool            `json:"stanzaBreakBefore,omitempty"`
	Segments             []LyricsSegment `json:"segments"`
	TrailingPerformerIDs []string        `json:"trailingPerformerIds"`
}

// Extraction contains the selected rendition and all source-local display
// evidence. It remains private and does not create or update song_lyrics.
type Extraction struct {
	Version              LyricsVersion    `json:"version"`
	Performers           []Performer      `json:"performers"`
	RubyGeneratorVersion string           `json:"rubyGeneratorVersion"`
	Lines                []StructuredLine `json:"lines"`
}

type Preview struct {
	CanonicalURL         string           `json:"canonicalUrl"`
	PageID               int              `json:"pageId"`
	RevisionID           int              `json:"revisionId"`
	SHA1                 string           `json:"sha1"`
	Categories           []string         `json:"categories"`
	FetchedAt            string           `json:"fetchedAt"`
	Lines                []ExtractedLine  `json:"lines"`
	StructuredLines      []StructuredLine `json:"structuredLines,omitempty"`
	RubyGeneratorVersion string           `json:"rubyGeneratorVersion,omitempty"`
	ImportToken          string           `json:"importToken,omitempty"`
}

// FixedRevision is the private fixed source artifact used by the Phase 2
// worker. Fandom and Moegirl retain exact revision wikitext; Sekaipedia retains
// only the canonical selected-Japanese column while exact immutable API bytes
// stay in IndexEvidence. Wikitext must never be returned by admin APIs, list
// views, logs, or shared content backups.
type FixedRevision struct {
	Provider          model.LyricsSourceProvider
	Origin            string
	CanonicalURL      string
	PageID            int
	PageTitle         string
	RevisionID        int
	RevisionTimestamp time.Time
	SHA1              string
	RawSHA256         string
	Categories        []string
	FetchedAt         time.Time
	Wikitext          []byte
	Lines             []ExtractedLine
	Translations      []string
	Extraction        Extraction
	Section           string
	RenditionKey      string
	VersionReason     model.LyricsSourceVersionReasonCode
	IndexEvidenceRefs []model.LyricsSourceIndexEvidenceRef
	IndexEvidence     []IndexEvidence
	FixedIdentities   []model.LyricsSourceFixedIdentity
	Document          *model.LyricsSourceDocument
}

// EnsureStructuredExtraction keeps internal synthetic sources and tests on the
// same v2 persistence contract without weakening production FetchFixedRevision,
// which always returns a parser-generated structured extraction.
func EnsureStructuredExtraction(fixed FixedRevision) (FixedRevision, error) {
	if len(fixed.Extraction.Lines) > 0 {
		return fixed, nil
	}
	lines, err := structuredLinesFromLegacy(fixed.Lines)
	if err != nil {
		return FixedRevision{}, err
	}
	fixed.Extraction = Extraction{
		Version: LyricsVersion{Kind: "original", Label: "Original Version"}, Performers: []Performer{},
		RubyGeneratorVersion: rubyGeneratorVersion, Lines: lines,
	}
	return fixed, nil
}

type cacheEntry struct {
	body      []byte
	createdAt time.Time
}

type inflightRequest struct {
	done         chan struct{}
	body         []byte
	fetchedAt    time.Time
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

	rateMu        sync.Mutex
	lastRequest   time.Time
	cooldownUntil time.Time
	rateToken     chan struct{}

	actualHTTPOnce  sync.Once
	actualHTTPToken chan struct{}

	recoverySafety *RecoveryProviderSafety
}

func New() *Client {
	return newMediaWikiClient(vocaloidWikiAPI, defaultRequestInterval, 2*time.Minute, nil)
}

func searchRequestParams(title string) url.Values {
	return searchQueryRequestParams(title)
}

func searchQueryRequestParams(query string) url.Values {
	return url.Values{
		"action":       {"query"},
		"format":       {"json"},
		"generator":    {"search"},
		"gsrnamespace": {"0"},
		"gsrlimit":     {fmt.Sprintf("%d", maxSearchPages)},
		"gsrsearch":    {query},
		"prop":         {"revisions|categories"},
		"rvprop":       {"ids|sha1|content"},
		"rvslots":      {"main"},
		"cllimit":      {"max"},
		"maxlag":       {mediaWikiMaxLag},
	}
}

func creatorSearchRequestParams(query string) url.Values {
	params := searchQueryRequestParams(query)
	params.Set("gsrlimit", fmt.Sprintf("%d", maxCreatorAliasPages))
	return params
}

func exactTitleSearchQuery(title string) string {
	title = strings.TrimSpace(strings.ReplaceAll(title, `"`, ""))
	if title == "" {
		return ""
	}
	return `"` + title + `"`
}

func titleSearchQueries(title string) []string {
	title = strings.TrimSpace(html.UnescapeString(title))
	if title == "" {
		return nil
	}
	variants := []string{title}
	if canonical := canonicalCatalogTitle(title, false); canonical != "" && canonical != title {
		variants = append(variants, canonical)
	}
	if smart := alternateTitleTypographyQuery(canonicalCatalogTitle(title, false)); smart != "" {
		variants = append(variants, smart)
	}

	queries := make([]string, 0, maxTitleSearchQueries)
	seen := map[string]struct{}{}
	appendQuery := func(query string) {
		if query == "" || len(queries) >= maxTitleSearchQueries {
			return
		}
		if _, duplicate := seen[query]; duplicate {
			return
		}
		seen[query] = struct{}{}
		queries = append(queries, query)
	}
	for _, variant := range variants {
		appendQuery(variant)
		appendQuery(exactTitleSearchQuery(variant))
	}
	return queries
}

func alternateTitleTypographyQuery(value string) string {
	if value == "" {
		return ""
	}
	var result strings.Builder
	changed := false
	openDoubleQuote := true
	for _, current := range value {
		switch current {
		case '\'':
			result.WriteRune('’')
			changed = true
		case '"':
			if openDoubleQuote {
				result.WriteRune('“')
			} else {
				result.WriteRune('”')
			}
			openDoubleQuote = !openDoubleQuote
			changed = true
		case '-':
			result.WriteRune('‐')
			changed = true
		case '~':
			result.WriteRune('〜')
			changed = true
		default:
			result.WriteRune(current)
		}
	}
	if !changed {
		return ""
	}
	return result.String()
}
