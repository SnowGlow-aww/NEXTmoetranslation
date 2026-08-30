package lyricssource

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"moesekai/server/internal/lyricsprovideroutcome"
	"moesekai/server/internal/model"
)

func TestRecoveryRubyGeneratorCompatibilityAliasCannotEscape(t *testing.T) {
	for name, input := range map[string]string{
		"canonical":  "sekaipedia-ruby-kana-v1",
		"historical": "sekaipedia-romaji-kana-v1",
	} {
		t.Run(name, func(t *testing.T) {
			got, err := RecoveryPersistedRubyGeneratorVersion(input)
			if err != nil || got != "sekaipedia-ruby-kana-v1" {
				t.Fatalf("persisted ruby generator=%q err=%v", got, err)
			}
			if strings.Contains(got, "romaji") {
				t.Fatalf("retired compatibility vocabulary escaped: %q", got)
			}
		})
	}
}

func TestRecoveryRedirectedTitleRequestBindsObservedCanonicalIdentity(t *testing.T) {
	evidence := IndexEvidence{
		Kind: IndexEvidenceKindMediaWikiRevision, Provider: ProviderSekaipedia,
		PageID: 42, RevisionID: 330574, Title: "Roki",
	}
	params := sekaipediaPageRequestParams()
	params.Set("titles", "ロキ")
	params.Set("redirects", "1")
	params.Set("rvlimit", "1")
	requestURL, err := canonicalMediaWikiRequestURL(sekaipediaAPI, params)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateRecoveryRevisionRequestIdentity(ProviderSekaipedia, requestURL, evidence); err != nil {
		t.Fatalf("redirected exact title request was rejected: %v", err)
	}
	params.Del("redirects")
	withoutRedirect, err := canonicalMediaWikiRequestURL(sekaipediaAPI, params)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateRecoveryRevisionRequestIdentity(ProviderSekaipedia, withoutRedirect, evidence); err == nil {
		t.Fatal("title drift without an explicit redirect selector was accepted")
	}
}

func TestRecoveryFutureSekaipediaAuthorityIsExplicitlyBound(t *testing.T) {
	const (
		pageID     = 901
		revisionID = 1901
		title      = "Reviewed future song index"
	)
	historicalPage, err := parsePageResponse(readSekaipediaFixture(t, "testdata/sekaipedia-list-335193.json"))
	if err != nil {
		t.Fatal(err)
	}
	content := historicalPage.content
	revisionTimestamp := time.Date(2026, time.August, 1, 1, 2, 3, 0, time.UTC)
	fetchedAt := revisionTimestamp.Add(time.Hour)
	contentSHA1 := sha1.Sum([]byte(content))
	contentSHA256 := sha256.Sum256([]byte(content))
	raw, err := json.Marshal(map[string]any{
		"batchcomplete": true,
		"query": map[string]any{"pages": []any{map[string]any{
			"pageid": pageID, "ns": 0, "title": title, "categories": []any{},
			"revisions": []any{map[string]any{
				"revid": revisionID, "timestamp": revisionTimestamp.Format(time.RFC3339Nano),
				"sha1": hex.EncodeToString(contentSHA1[:]),
				"slots": map[string]any{"main": map[string]any{
					"contentmodel": "wikitext", "contentformat": "text/x-wiki", "content": content,
				}},
			}},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rawSHA := sha256.Sum256(raw)
	authority := FixedIndex{
		PageID: pageID, RevisionID: revisionID, RevisionTimestamp: revisionTimestamp.Format(time.RFC3339Nano),
		SHA1: hex.EncodeToString(contentSHA1[:]), ContentSHA256: hex.EncodeToString(contentSHA256[:]),
		RawSHA256: hex.EncodeToString(rawSHA[:]), Title: title,
	}
	params := sekaipediaPageRequestParams()
	params.Set("revids", strconv.Itoa(revisionID))
	requestURL, err := canonicalMediaWikiRequestURL(sekaipediaAPI, params)
	if err != nil {
		t.Fatal(err)
	}
	capture, err := CaptureRecoveryHTTPResponse(ProviderSekaipedia, []FixedIndex{authority}, RecoveryHTTPResponse{
		Action: "page", CanonicalRequestURL: requestURL, FetchedAt: fetchedAt, Raw: raw,
	})
	if err != nil {
		t.Fatal(err)
	}
	if capture.RequestKind != RecoveryRequestFixedIndex || !isFixedSekaipediaAuthorityEvidence(capture.Evidence, authority) {
		t.Fatalf("future reviewed authority was not bound exactly: kind=%s evidence=%s", capture.RequestKind, capture.Evidence.EvidenceID)
	}
	if err := ValidateIndexEvidenceEnvelope(capture.Evidence); err != nil {
		t.Fatal(err)
	}

	arbitrary := authority
	arbitrary.RevisionID++
	unbound, err := CaptureRecoveryHTTPResponse(ProviderSekaipedia, []FixedIndex{arbitrary}, RecoveryHTTPResponse{
		Action: "page", CanonicalRequestURL: requestURL, FetchedAt: fetchedAt, Raw: raw,
	})
	if err != nil {
		t.Fatal(err)
	}
	if unbound.RequestKind == RecoveryRequestFixedIndex || isFixedSekaipediaAuthorityEvidence(unbound.Evidence, arbitrary) {
		t.Fatal("arbitrary authority acquired fixed-authority status")
	}
}

func TestSekaipediaRevisionContentAuthorityIgnoresEnvelopePageInfoAndRejectsSemanticDrift(t *testing.T) {
	const content = "reviewed revision content\n"
	timestamp := time.Date(2026, time.August, 1, 1, 2, 3, 0, time.UTC).Format(time.RFC3339Nano)
	contentSHA1 := sha1.Sum([]byte(content))
	contentSHA256 := sha256.Sum256([]byte(content))
	authority := FixedIndex{
		PageID: 901, RevisionID: 1901, RevisionTimestamp: timestamp,
		SHA1: hex.EncodeToString(contentSHA1[:]), ContentSHA256: hex.EncodeToString(contentSHA256[:]),
		Title: "Reviewed index",
	}
	base := semanticSekaipediaRevisionResponse(t, authority, content, map[string]any{
		"touched": "2026-08-01T01:02:04Z", "length": 28,
	})
	baseDigest := sha256.Sum256(base)
	authority.RawSHA256 = hex.EncodeToString(baseDigest[:])
	variant := semanticSekaipediaRevisionResponse(t, authority, content, map[string]any{
		"touched": "2026-08-02T15:58:47Z", "length": 1, "pageInfoOnly": true,
	})
	variantDigest := sha256.Sum256(variant)
	if bytes.Equal(base, variant) || hex.EncodeToString(variantDigest[:]) == authority.RawSHA256 {
		t.Fatal("page-info variant did not produce distinct raw envelope evidence")
	}
	if err := VerifySekaipediaRevisionContent(variant, authority); err != nil {
		t.Fatalf("identical revision content with page-info drift was rejected: %v", err)
	}

	changedContent := semanticSekaipediaRevisionResponse(t, authority, content+"changed\n", nil)
	if err := VerifySekaipediaRevisionContent(changedContent, authority); !errors.Is(err, ErrRevisionChanged) {
		t.Fatalf("changed revision content error=%v", err)
	}
	for name, mutate := range map[string]func(*FixedIndex){
		"page ID":   func(value *FixedIndex) { value.PageID++ },
		"revision":  func(value *FixedIndex) { value.RevisionID++ },
		"timestamp": func(value *FixedIndex) { value.RevisionTimestamp = "2026-08-01T01:02:04Z" },
		"SHA-1":     func(value *FixedIndex) { value.SHA1 = strings.Repeat("0", 40) },
		"content":   func(value *FixedIndex) { value.ContentSHA256 = strings.Repeat("0", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			drifted := authority
			mutate(&drifted)
			if err := VerifySekaipediaRevisionContent(variant, drifted); !errors.Is(err, ErrRevisionChanged) {
				t.Fatalf("wrong revision identity error=%v", err)
			}
		})
	}
}

func semanticSekaipediaRevisionResponse(
	t *testing.T,
	authority FixedIndex,
	content string,
	pageInfo map[string]any,
) []byte {
	t.Helper()
	page := map[string]any{
		"pageid": authority.PageID, "ns": 0, "title": authority.Title, "categories": []any{},
		"revisions": []any{map[string]any{
			"revid": authority.RevisionID, "timestamp": authority.RevisionTimestamp, "sha1": authority.SHA1,
			"slots": map[string]any{"main": map[string]any{
				"contentmodel": "wikitext", "contentformat": "text/x-wiki", "content": content,
			}},
		}},
	}
	for key, value := range pageInfo {
		page[key] = value
	}
	body, err := json.Marshal(map[string]any{
		"batchcomplete": true, "query": map[string]any{"pages": []any{page}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestRecoveryRegistryDefensivelyCopiesAuthoritiesAndSongAliases(t *testing.T) {
	authorities := []FixedIndex{{
		PageID: 268, RevisionID: 335193, RevisionTimestamp: "2026-07-27T16:29:13Z",
		SHA1:          "b216a827f88c59f5e954a120027832fe9cd74413",
		ContentSHA256: "aaddff2922548aab7e522124ff2bad86427501930d549c9d94c9b4e473c35f92",
		RawSHA256:     "c21e31c36f8e7d7534af1617d5b737a1662decd40c34c9e7d4aab71b103ef8dd",
		Title:         "List of songs",
	}}
	aliases := []ProviderContributorAlias{{
		MusicID: 2, CatalogContributor: "catalog-contributor", ProviderContributor: "provider-contributor",
	}}
	config, err := RecoveryProviderConfig(
		ProviderSekaipedia, defaultProviderCrawlDelay, defaultProviderCacheTTL, authorities, aliases,
	)
	if err != nil {
		t.Fatal(err)
	}
	authorities[0].RevisionID++
	aliases[0].ProviderContributor = "mutated-before-registry"
	if config.Indexes[0].RevisionID != 335193 || config.ContributorAliases[0].ProviderContributor != "provider-contributor" {
		t.Fatal("caller mutation changed plan-derived provider configuration")
	}
	transport := &recoveryHookTransport{
		body: []byte(`{"batchcomplete":true}`), fetchedAt: time.Date(2026, time.August, 2, 1, 0, 0, 0, time.UTC),
	}
	registry, err := NewRecoveryRegistry(
		[]ProviderConfig{config},
		map[model.LyricsSourceProvider]RecoveryHTTPTransport{ProviderSekaipedia: transport},
	)
	if err != nil {
		t.Fatal(err)
	}
	config.Indexes[0].RevisionID++
	config.ContributorAliases[0].ProviderContributor = "mutated-after-registry"
	provider, ok := registry.providers[ProviderSekaipedia].(*sekaipediaProvider)
	if !ok || provider.config.Indexes[0].RevisionID != 335193 ||
		provider.config.ContributorAliases[0].ProviderContributor != "provider-contributor" {
		t.Fatal("caller mutation changed recovery registry authority or alias behavior")
	}
}

func TestRecoveryCommitAdmissionPrecedesProviderParsing(t *testing.T) {
	commitFailure := errors.New("fixture durable commit rejected")
	transport := &recoveryHookTransport{
		body: []byte(`{"batchcomplete":true}`), fetchedAt: time.Date(2026, time.August, 2, 1, 0, 0, 0, time.UTC),
		commitErr: commitFailure,
	}
	config, err := RecoveryProviderConfig(
		ProviderVocaloidFandom, defaultProviderCrawlDelay, defaultProviderCacheTTL,
		[]FixedIndex{}, []ProviderContributorAlias{},
	)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRecoveryRegistry(
		[]ProviderConfig{config},
		map[model.LyricsSourceProvider]RecoveryHTTPTransport{ProviderVocaloidFandom: transport},
	)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := registry.SearchProviderOutcome(t.Context(), ProviderVocaloidFandom, MusicIdentity{
		MusicID: 2, JapaneseTitle: "Test Song", ProducerMetadata: "Test Producer",
	})
	if err != nil {
		t.Fatal(err)
	}
	if transport.retainCount() != 1 || transport.commitCount() != 1 ||
		outcome.Status != lyricsprovideroutcome.StatusTransportError ||
		outcome.Diagnostic.ReasonCode != lyricsprovideroutcome.ReasonTransport {
		t.Fatalf("retain-and-commit-before-parser admission was not enforced: retained=%d commits=%d outcome=%+v",
			transport.retainCount(), transport.commitCount(), outcome)
	}
}

func TestRecoveryCompletedMalformedAndNonSuccessResponsesCrossRetentionBeforeClosedOutcome(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       []byte
		header     http.Header
		wantStatus lyricsprovideroutcome.Status
		wantReason lyricsprovideroutcome.ReasonCode
		wantCommit int
	}{
		{
			name: "malformed JSON", statusCode: http.StatusOK, body: []byte{0x7b},
			header:     http.Header{"Content-Type": {"application/json"}},
			wantStatus: lyricsprovideroutcome.StatusUnsupported,
			wantReason: lyricsprovideroutcome.ReasonMalformedResponse, wantCommit: 1,
		},
		{
			name: "non success", statusCode: http.StatusServiceUnavailable,
			body:       []byte{0x7b, 0x22, 0x65, 0x72, 0x72, 0x6f, 0x72, 0x22, 0x3a, 0x7b, 0x7d, 0x7d},
			header:     http.Header{"Content-Type": {"application/json"}, "Retry-After": {"23"}, "X-Exact": {"one", "two"}},
			wantStatus: lyricsprovideroutcome.StatusTransportError,
			wantReason: lyricsprovideroutcome.ReasonTransport, wantCommit: 0,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fetchedAt := time.Date(2026, time.August, 3, 8, 36, 12, 456_789_000, time.UTC)
			transport := &recoveryHookTransport{
				body: append([]byte(nil), test.body...), fetchedAt: fetchedAt,
				statusCode: test.statusCode, header: test.header.Clone(), offline: true,
			}
			config, err := RecoveryProviderConfig(
				ProviderVocaloidFandom, defaultProviderCrawlDelay, defaultProviderCacheTTL,
				[]FixedIndex{}, []ProviderContributorAlias{},
			)
			if err != nil {
				t.Fatal(err)
			}
			registry, err := NewRecoveryRegistry(
				[]ProviderConfig{config},
				map[model.LyricsSourceProvider]RecoveryHTTPTransport{ProviderVocaloidFandom: transport},
			)
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := registry.SearchProviderOutcome(t.Context(), ProviderVocaloidFandom, MusicIdentity{
				MusicID: 2, JapaneseTitle: "Test Song", ProducerMetadata: "Test Producer",
			})
			if err != nil {
				t.Fatal(err)
			}
			retained := transport.retainedSnapshot()
			var boundaryErr error
			if len(retained) == 1 {
				boundaryErr = ValidateRecoveryHTTPRequestBoundary(
					ProviderVocaloidFandom, retained[0].Action, retained[0].CanonicalRequestURL,
				)
			}
			if transport.requestCount() != 1 || len(retained) != 1 || transport.commitCount() != test.wantCommit ||
				boundaryErr != nil ||
				outcome.Status != test.wantStatus || outcome.Diagnostic.ReasonCode != test.wantReason ||
				retained[0].FetchedAt != fetchedAt || retained[0].StatusCode != test.statusCode ||
				retained[0].Status != http.StatusText(test.statusCode) ||
				!reflect.DeepEqual(retained[0].Header, test.header) || !bytes.Equal(retained[0].Raw, test.body) {
				t.Fatalf("completed response retention boundary mismatch: requests=%d retained=%d commits=%d outcome=%+v",
					transport.requestCount(), len(retained), transport.commitCount(), outcome)
			}
		})
	}
}

func TestRecoveryProviderSafetyCarriesFullRetryAfterAcrossFreshRegistries(t *testing.T) {
	config, err := RecoveryProviderConfig(
		ProviderVocaloidFandom, defaultProviderCrawlDelay, defaultProviderCacheTTL,
		[]FixedIndex{}, []ProviderContributorAlias{},
	)
	if err != nil {
		t.Fatal(err)
	}
	safety := map[model.LyricsSourceProvider]*RecoveryProviderSafety{
		ProviderVocaloidFandom: NewRecoveryProviderSafety(),
	}
	limited := &recoveryHookTransport{
		body: []byte(`{"error":"loaded"}`), statusCode: http.StatusServiceUnavailable,
		header: http.Header{"Retry-After": []string{"7200"}},
	}
	first, err := NewRecoveryRegistryWithProviderSafety(
		[]ProviderConfig{config},
		map[model.LyricsSourceProvider]RecoveryHTTPTransport{ProviderVocaloidFandom: limited}, safety,
	)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := first.SearchProviderOutcome(t.Context(), ProviderVocaloidFandom, MusicIdentity{
		MusicID: 2, JapaneseTitle: "First Song", ProducerMetadata: "Test Producer",
	})
	if err != nil || outcome.Status != lyricsprovideroutcome.StatusTransportError || limited.requestCount() != 1 {
		t.Fatalf("first rate-limited outcome=%+v requests=%d err=%v", outcome, limited.requestCount(), err)
	}

	secondTransport := &recoveryHookTransport{
		body:      []byte(`{"batchcomplete":true}`),
		fetchedAt: time.Date(2026, time.August, 2, 1, 0, 0, 0, time.UTC),
	}
	second, err := NewRecoveryRegistryWithProviderSafety(
		[]ProviderConfig{config},
		map[model.LyricsSourceProvider]RecoveryHTTPTransport{ProviderVocaloidFandom: secondTransport}, safety,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	outcome, err = second.SearchProviderOutcome(ctx, ProviderVocaloidFandom, MusicIdentity{
		MusicID: 235, JapaneseTitle: "Second Song", ProducerMetadata: "Test Producer",
	})
	if err != nil || outcome.Status != lyricsprovideroutcome.StatusTransportError || secondTransport.requestCount() != 0 {
		t.Fatalf("shared Retry-After was evaded: outcome=%+v requests=%d err=%v", outcome, secondTransport.requestCount(), err)
	}
}

func TestRecoveryOfflineRegistryBypassesCrawlSleepWithoutDefaultNetwork(t *testing.T) {
	transport := &recoveryHookTransport{
		body: []byte(`{"batchcomplete":true}`), fetchedAt: time.Date(2026, time.August, 2, 1, 0, 0, 0, time.UTC),
		offline: true,
	}
	config, err := RecoveryProviderConfig(
		ProviderVocaloidFandom, defaultProviderCrawlDelay, defaultProviderCacheTTL,
		[]FixedIndex{}, []ProviderContributorAlias{},
	)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRecoveryRegistry(
		[]ProviderConfig{config},
		map[model.LyricsSourceProvider]RecoveryHTTPTransport{ProviderVocaloidFandom: transport},
	)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	for musicID, title := range map[int]string{2: "First Song", 235: "Second Song"} {
		outcome, err := registry.SearchProviderOutcome(context.Background(), ProviderVocaloidFandom, MusicIdentity{
			MusicID: musicID, JapaneseTitle: title, ProducerMetadata: "Test Producer",
		})
		if err != nil || outcome.Status != lyricsprovideroutcome.StatusNoMatch {
			t.Fatalf("offline provider outcome=%+v err=%v", outcome, err)
		}
	}
	if elapsed := time.Since(started); elapsed >= time.Second || transport.requestCount() < 2 ||
		transport.retainCount() != transport.requestCount() || transport.commitCount() != transport.requestCount() {
		t.Fatalf("offline registry slept or skipped exact local transport: elapsed=%s requests=%d retained=%d commits=%d",
			elapsed, transport.requestCount(), transport.retainCount(), transport.commitCount())
	}
}

type recoveryHookTransport struct {
	body              []byte
	fetchedAt         time.Time
	retainErr         error
	commitErr         error
	offline           bool
	statusCode        int
	header            http.Header
	mu                sync.Mutex
	requests          int
	retained          int
	retainedResponses []RecoveryHTTPResponse
	commits           int
	lastAction        string
}

func (transport *recoveryHookTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.mu.Lock()
	transport.requests++
	transport.mu.Unlock()
	statusCode := transport.statusCode
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	header := transport.header.Clone()
	if header == nil {
		header = make(http.Header)
	}
	return &http.Response{
		StatusCode: statusCode, Status: http.StatusText(statusCode), Header: header,
		Body: io.NopCloser(bytes.NewReader(transport.body)), ContentLength: int64(len(transport.body)), Request: request,
	}, nil
}

func (transport *recoveryHookTransport) RecoveryFetchedAt(*http.Request, *http.Response) (time.Time, error) {
	if transport.fetchedAt.IsZero() {
		return time.Unix(1, 0).UTC(), nil
	}
	return transport.fetchedAt, nil
}

func (transport *recoveryHookTransport) RecoveryRetainResponse(
	_ context.Context,
	_ *http.Request,
	_ *http.Response,
	response RecoveryHTTPResponse,
) error {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.retained++
	response.Header = response.Header.Clone()
	response.Raw = append([]byte(nil), response.Raw...)
	transport.retainedResponses = append(transport.retainedResponses, response)
	transport.lastAction = response.Action
	return transport.retainErr
}

func (transport *recoveryHookTransport) RecoveryCommitResponse(_ context.Context, response RecoveryHTTPResponse) error {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.commits++
	transport.lastAction = response.Action
	return transport.commitErr
}

func (transport *recoveryHookTransport) RecoveryOffline() bool { return transport.offline }

func (transport *recoveryHookTransport) requestCount() int {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.requests
}

func (transport *recoveryHookTransport) retainCount() int {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.retained
}

func (transport *recoveryHookTransport) retainedSnapshot() []RecoveryHTTPResponse {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	result := make([]RecoveryHTTPResponse, len(transport.retainedResponses))
	for index, response := range transport.retainedResponses {
		response.Header = response.Header.Clone()
		response.Raw = append([]byte(nil), response.Raw...)
		result[index] = response
	}
	return result
}

func (transport *recoveryHookTransport) commitCount() int {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.commits
}
