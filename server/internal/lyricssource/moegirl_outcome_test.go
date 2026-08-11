package lyricssource

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"moesekai/server/internal/lyricsprovideroutcome"
)

func TestMoegirlOutcomeEvaluatesEveryTargetAndFailsClosedOnCandidateConflict(t *testing.T) {
	indexBody := strings.Join([]string{
		"* [[A冲突页#冲突锚点|合成試験曲]]",
		"* [[B有效页#有效锚点|合成試験曲]]",
		"",
	}, "\n")
	provider, requests := newMoegirlSearchTestProvider(t, indexBody, map[string]moegirlSearchTestPage{
		"A冲突页": {pageID: 2, revisionID: 22, body: moegirlMatchingTestSection("冲突锚点", `<--Tag-Start:Full Ver.-->
秘密歌词
<--Tag-End-->`)},
		"B有效页": {pageID: 3, revisionID: 33, body: moegirlMatchingTestSection("有效锚点", "有効な歌")},
	})
	fandom := &outcomeStubProvider{id: ProviderVocaloidFandom, candidates: []Candidate{{
		Provider: ProviderVocaloidFandom, PageID: 9,
	}}}
	registry, err := newRegistryWithProviders(fandom, provider)
	if err != nil {
		t.Fatal(err)
	}
	outcomes, err := registry.SearchOutcomes(context.Background(), moegirlOutcomeIdentity())
	if err != nil || len(outcomes) != 1 {
		t.Fatalf("outcomes=%+v err=%v", outcomes, err)
	}
	moegirl := outcomes[0]
	if moegirl.Status != lyricsprovideroutcome.StatusAmbiguous ||
		moegirl.Diagnostic.ReasonCode != lyricsprovideroutcome.ReasonCandidateConflict ||
		moegirl.Diagnostic.Counts.Targets != 2 || moegirl.Diagnostic.Counts.Evaluated != 2 ||
		moegirl.Diagnostic.Counts.Candidates != 1 || moegirl.Diagnostic.Counts.Unsupported != 1 ||
		len(moegirl.Candidates) != 0 {
		t.Fatalf("candidate-conflict outcome = %+v", moegirl)
	}
	if requests["A冲突页"].Load() != 1 || requests["B有效页"].Load() != 1 || fandom.searchCalls != 0 {
		t.Fatalf("deterministic target calls conflict=%d valid=%d fandom=%d",
			requests["A冲突页"].Load(), requests["B有效页"].Load(), fandom.searchCalls)
	}
	body, err := json.Marshal(moegirl.Diagnostic)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"秘密歌词", "冲突页", "有效锚点"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("Moegirl diagnostic leaked %q: %s", forbidden, body)
		}
	}
}

func TestMoegirlMissingAndUnsupportedTargetsRemainProviderLocalAndAllowFandom(t *testing.T) {
	indexBody := strings.Join([]string{
		"* [[A缺失页#缺失锚点|合成試験曲]]",
		"* [[B不支持页#不支持锚点|合成試験曲]]",
		"",
	}, "\n")
	missing := strings.Replace(
		moegirlMatchingTestSection("缺失锚点", "不会被读取"),
		"=== 歌词 ===", "=== 解说 ===", 1,
	)
	unsupported := moegirlMatchingTestSection("不支持锚点", `<--Tag-Start:Full Ver.-->
不支持歌词
<--Tag-End-->`)
	provider, requests := newMoegirlSearchTestProvider(t, indexBody, map[string]moegirlSearchTestPage{
		"A缺失页":  {pageID: 2, revisionID: 22, body: missing},
		"B不支持页": {pageID: 3, revisionID: 33, body: unsupported},
	})
	fandom := &outcomeStubProvider{id: ProviderVocaloidFandom, candidates: []Candidate{{
		Provider: ProviderVocaloidFandom, PageID: 9,
	}}}
	registry, err := newRegistryWithProviders(provider, fandom)
	if err != nil {
		t.Fatal(err)
	}
	outcomes, err := registry.SearchOutcomes(context.Background(), moegirlOutcomeIdentity())
	if err != nil || len(outcomes) != 2 {
		t.Fatalf("outcomes=%+v err=%v", outcomes, err)
	}
	moegirl := outcomes[0]
	if moegirl.Status != lyricsprovideroutcome.StatusUnsupported ||
		moegirl.Diagnostic.ReasonCode != lyricsprovideroutcome.ReasonUnsupportedFormat ||
		moegirl.Diagnostic.Counts.Targets != 2 || moegirl.Diagnostic.Counts.Evaluated != 2 ||
		moegirl.Diagnostic.Counts.NoMatch != 1 || moegirl.Diagnostic.Counts.Unsupported != 1 ||
		moegirl.Diagnostic.Counts.Candidates != 0 {
		t.Fatalf("missing/unsupported outcome = %+v", moegirl)
	}
	if outcomes[1].Status != lyricsprovideroutcome.StatusCandidate || fandom.searchCalls != 1 ||
		requests["A缺失页"].Load() != 1 || requests["B不支持页"].Load() != 1 {
		t.Fatalf("fallback outcome=%+v calls missing=%d unsupported=%d fandom=%d",
			outcomes[1], requests["A缺失页"].Load(), requests["B不支持页"].Load(), fandom.searchCalls)
	}
}

func TestMoegirlNoIndexCandidateHasExplicitFallbackReason(t *testing.T) {
	provider, requests := newMoegirlSearchTestProvider(t,
		"* [[无关页#无关锚点|其他歌曲]]\n",
		map[string]moegirlSearchTestPage{
			"无关页": {pageID: 2, revisionID: 22, body: moegirlMatchingTestSection("无关锚点", "歌う")},
		},
	)
	fandom := &outcomeStubProvider{id: ProviderVocaloidFandom, candidates: []Candidate{{
		Provider: ProviderVocaloidFandom, PageID: 9,
	}}}
	registry, err := newRegistryWithProviders(provider, fandom)
	if err != nil {
		t.Fatal(err)
	}
	outcomes, err := registry.SearchOutcomes(context.Background(), moegirlOutcomeIdentity())
	if err != nil || len(outcomes) != 2 {
		t.Fatalf("outcomes=%+v err=%v", outcomes, err)
	}
	if outcomes[0].Status != lyricsprovideroutcome.StatusNoMatch ||
		outcomes[0].Diagnostic.ReasonCode != lyricsprovideroutcome.ReasonNoSearchHits ||
		outcomes[0].Diagnostic.Phase != lyricsprovideroutcome.PhaseResolveTargets ||
		outcomes[0].Diagnostic.Counts.Targets != 0 || outcomes[0].Diagnostic.Counts.NoMatch != 1 ||
		outcomes[1].Status != lyricsprovideroutcome.StatusCandidate || fandom.searchCalls != 1 ||
		requests["固定索引"].Load() != 1 || requests["无关页"].Load() != 0 {
		t.Fatalf("explicit no-target fallback=%+v fandom=%d index=%d target=%d",
			outcomes, fandom.searchCalls, requests["固定索引"].Load(), requests["无关页"].Load())
	}
}

func TestMoegirlTransportAndCancellationAreRetryableProviderLocalOutcomes(t *testing.T) {
	t.Run("transport", func(t *testing.T) {
		const indexBody = "* [[目标页#目标锚点|合成試験曲]]\n"
		const indexPageID, indexRevisionID = 1, 11
		indexSHA := fmt.Sprintf("%x", sha1.Sum([]byte(indexBody)))
		var requests atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			if r.URL.Query().Get("revids") == "11" {
				writePageResponseWithCategories(w, indexPageID, indexRevisionID, indexSHA, "固定索引", indexBody, []string{"索引"})
				return
			}
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		}))
		defer server.Close()
		provider := newMoegirlProvider(ProviderConfig{
			Provider: ProviderMoegirl, Origin: OriginMoegirl,
			Indexes: []FixedIndex{{
				PageID: indexPageID, RevisionID: indexRevisionID, SHA1: indexSHA, Title: "固定索引",
			}},
		}, newMediaWikiClient(server.URL, 0, time.Hour, server.Client()))
		fandom := &outcomeStubProvider{id: ProviderVocaloidFandom, candidates: []Candidate{{
			Provider: ProviderVocaloidFandom, PageID: 9,
		}}}
		registry, err := newRegistryWithProviders(provider, fandom)
		if err != nil {
			t.Fatal(err)
		}
		outcomes, err := registry.SearchOutcomes(context.Background(), moegirlOutcomeIdentity())
		if err != nil || len(outcomes) != 1 {
			t.Fatalf("outcomes=%+v err=%v", outcomes, err)
		}
		if outcomes[0].Status != lyricsprovideroutcome.StatusTransportError ||
			outcomes[0].Diagnostic.ReasonCode != lyricsprovideroutcome.ReasonTransport ||
			!outcomes[0].Retryable() || outcomes[0].Diagnostic.Counts.TransportErrors != 1 ||
			len(outcomes[0].Diagnostic.AcquisitionRefs) != 1 {
			t.Fatalf("Moegirl transport outcome = %+v", outcomes[0])
		}
		if fandom.searchCalls != 0 || requests.Load() != 2 {
			t.Fatalf("transport fallback calls fandom=%d requests=%d", fandom.searchCalls, requests.Load())
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		provider, requests := newMoegirlSearchTestProvider(t,
			"* [[取消页#取消锚点|合成試験曲]]\n",
			map[string]moegirlSearchTestPage{
				"取消页": {pageID: 2, revisionID: 22, body: moegirlMatchingTestSection("取消锚点", "歌う")},
			},
		)
		fandom := &outcomeStubProvider{id: ProviderVocaloidFandom, candidates: []Candidate{{
			Provider: ProviderVocaloidFandom, PageID: 9,
		}}}
		registry, err := newRegistryWithProviders(provider, fandom)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		outcomes, err := registry.SearchOutcomes(ctx, moegirlOutcomeIdentity())
		if err != nil || len(outcomes) != 1 {
			t.Fatalf("outcomes=%+v err=%v", outcomes, err)
		}
		if outcomes[0].Status != lyricsprovideroutcome.StatusTransportError ||
			outcomes[0].Diagnostic.ReasonCode != lyricsprovideroutcome.ReasonCanceled || !outcomes[0].Retryable() ||
			fandom.searchCalls != 0 || requests["固定索引"].Load() != 0 || requests["取消页"].Load() != 0 {
			t.Fatalf("cancellation outcomes=%+v calls fandom=%d index=%d target=%d",
				outcomes, fandom.searchCalls, requests["固定索引"].Load(), requests["取消页"].Load())
		}
	})

	t.Run("deadline", func(t *testing.T) {
		provider := &outcomeStubProvider{id: ProviderMoegirl, candidates: []Candidate{{
			Provider: ProviderMoegirl, PageID: 3,
		}}}
		fandom := &outcomeStubProvider{id: ProviderVocaloidFandom, candidates: []Candidate{{
			Provider: ProviderVocaloidFandom, PageID: 9,
		}}}
		registry, err := newRegistryWithProviders(provider, fandom)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		outcomes, err := registry.SearchOutcomes(ctx, moegirlOutcomeIdentity())
		if err != nil || len(outcomes) != 1 ||
			outcomes[0].Status != lyricsprovideroutcome.StatusTransportError ||
			outcomes[0].Diagnostic.ReasonCode != lyricsprovideroutcome.ReasonDeadlineExceeded ||
			!outcomes[0].Retryable() || provider.searchCalls != 0 || fandom.searchCalls != 0 {
			t.Fatalf("deadline outcomes=%+v calls moegirl=%d fandom=%d err=%v",
				outcomes, provider.searchCalls, fandom.searchCalls, err)
		}
	})
}

func moegirlOutcomeIdentity() MusicIdentity {
	return MusicIdentity{
		MusicID: 10, JapaneseTitle: "合成試験曲", ProducerMetadata: "制作者",
		Lyricist: "制作者", Composer: "制作者", Arranger: "制作者",
		PerformerSegmentationPolicy: PerformerSegmentationSekaiEligible,
	}
}
