package lyricssource

import (
	"context"
	"crypto/sha1"

	"fmt"
	"net/http"
	"net/http/httptest"

	"strconv"

	"sync/atomic"
	"testing"
	"time"

	"moesekai/server/internal/model"
)

func testRevisionIndexEvidence(
	t *testing.T,
	provider model.LyricsSourceProvider,
	evidenceID string,
	pageID, revisionID int,
	title string,
	raw []byte,
	categories []string,
) (model.LyricsSourceIndexEvidenceRef, IndexEvidence) {
	t.Helper()
	page := wikiPage{
		pageID: pageID, revisionID: revisionID, title: title,
		sha1: fmt.Sprintf("%x", sha1.Sum(raw)), categories: append([]string{}, categories...), content: string(raw),
		fetchedAt: time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC),
	}
	evidence, err := newMediaWikiRevisionIndexEvidence(provider, evidenceID, page, raw)
	if err != nil {
		t.Fatal(err)
	}
	return model.LyricsSourceIndexEvidenceRef{EvidenceID: evidence.EvidenceID, SHA256: evidence.SHA256}, evidence
}

type moegirlSearchTestPage struct {
	pageID     int
	revisionID int
	body       string
}

func newMoegirlSearchTestProvider(
	t *testing.T,
	indexBody string,
	pages map[string]moegirlSearchTestPage,
) (*moegirlProvider, map[string]*atomic.Int32) {
	t.Helper()
	const indexPageID, indexRevisionID = 1, 11
	const indexTitle = "固定索引"
	indexSHA := fmt.Sprintf("%x", sha1.Sum([]byte(indexBody)))
	requests := make(map[string]*atomic.Int32, len(pages)+1)
	requests[indexTitle] = &atomic.Int32{}
	for title := range pages {
		requests[title] = &atomic.Int32{}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		requestedRevision := query.Get("revids")
		if requestedRevision == strconv.Itoa(indexRevisionID) {
			requests[indexTitle].Add(1)
			writePageResponseWithCategories(w, indexPageID, indexRevisionID, indexSHA, indexTitle, indexBody, []string{"索引"})
			return
		}
		if requestedRevision != "" {
			for title, page := range pages {
				if requestedRevision != strconv.Itoa(page.revisionID) {
					continue
				}
				requests[title].Add(1)
				sha := fmt.Sprintf("%x", sha1.Sum([]byte(page.body)))
				writePageResponseWithCategories(w, page.pageID, page.revisionID, sha, title, page.body, []string{"歌曲"})
				return
			}
		}
		title := query.Get("titles")
		page, ok := pages[title]
		if !ok {
			t.Errorf("unexpected MediaWiki query: %s", r.URL.RawQuery)
			http.Error(w, "unexpected query", http.StatusBadRequest)
			return
		}
		requests[title].Add(1)
		sha := fmt.Sprintf("%x", sha1.Sum([]byte(page.body)))
		writePageResponseWithCategories(w, page.pageID, page.revisionID, sha, title, page.body, []string{"歌曲"})
	}))
	t.Cleanup(server.Close)

	provider := newMoegirlProvider(ProviderConfig{
		Provider: ProviderMoegirl, Origin: OriginMoegirl,
		Indexes: []FixedIndex{{
			PageID: indexPageID, RevisionID: indexRevisionID, SHA1: indexSHA, Title: indexTitle,
		}},
	}, newMediaWikiClient(server.URL, 0, time.Hour, server.Client()))
	return provider, requests
}

func moegirlMatchingTestSection(anchor, original string) string {
	return fmt.Sprintf(`== %s ==
{{ProjectsekaiSongGai
|曲名=合成試験曲
|作词=制作者
|作曲=制作者
|编曲=制作者
}}
=== 歌词 ===
{{LyricsKai/ext
|type=colors,multiver
|colors=#39c
|charas=初音未来
|original=%s
}}
`, anchor, original)
}

type stubSourceProvider struct {
	id          model.LyricsSourceProvider
	candidates  []Candidate
	searchErr   error
	searchCalls int
	fetchCalls  int
}

func (provider *stubSourceProvider) ProviderID() model.LyricsSourceProvider {
	return provider.id
}

func (provider *stubSourceProvider) Search(context.Context, MusicIdentity) ([]Candidate, error) {
	provider.searchCalls++
	if provider.searchErr != nil {
		return nil, provider.searchErr
	}
	return append([]Candidate{}, provider.candidates...), nil
}

func (provider *stubSourceProvider) FetchFixedCandidateRevision(context.Context, MusicIdentity, Candidate) (FixedRevision, error) {
	provider.fetchCalls++
	return FixedRevision{Provider: provider.id}, nil
}

func waitForFixedAuthorityParticipants(
	t *testing.T,
	cache *fixedAuthorityCache,
	fixed FixedIndex,
	want int,
) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		cache.mu.Lock()
		participants := 0
		if pending := cache.inflight[fixed]; pending != nil {
			participants = pending.participants
		}
		cache.mu.Unlock()
		if participants == want {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("fixed-authority participants = %d, want %d", participants, want)
		case <-ticker.C:
		}
	}
}

func waitForFixedAuthorityIdle(t *testing.T, cache *fixedAuthorityCache, fixed FixedIndex) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		cache.mu.Lock()
		pending := cache.inflight[fixed]
		cache.mu.Unlock()
		if pending == nil {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("fixed-authority resolution did not become idle")
		case <-ticker.C:
		}
	}
}

func mediaWikiCategories(categories []string) []any {
	result := make([]any, len(categories))
	for index, category := range categories {
		result[index] = map[string]any{"title": "Category:" + category}
	}
	return result
}
