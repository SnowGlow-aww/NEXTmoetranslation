package lyricssource

import (
	"context"
	"crypto/sha1"

	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"

	"os"

	"strings"

	"testing"
	"time"

	"moesekai/server/internal/model"
)

func TestMoegirlFixtureParsesOriginalOnlyPerformersVersionsAndRuby(t *testing.T) {
	content, err := os.ReadFile("testdata/moegirl-section-full-game.wiki")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseMoegirlSectionWithPolicy(string(content), PerformerSegmentationSekaiEligible)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.ReasonCode != model.LyricsSourceVersionReasonTaggedFullAndGame || !parsed.TaggedFull || !parsed.TaggedGame ||
		parsed.Full.Version.Kind != "sekai" || len(parsed.Full.Performers) != 2 || len(parsed.Full.Lines) != 5 {
		t.Fatalf("parsed section = %+v", parsed)
	}
	wantLines := []string{"共通の理由", "同じ歌", "完全版だけ", "二人で先へ　編み合わせて", "弱さ歌おう"}
	for index, want := range wantLines {
		if parsed.Full.Lines[index].Japanese != want {
			t.Fatalf("line %d = %q, want %q", index, parsed.Full.Lines[index].Japanese, want)
		}
	}
	if got := parsed.GameLineIndexes; fmt.Sprint(got) != "[0 1 3 4]" {
		t.Fatalf("game projection = %v", got)
	}
	if !parsed.Full.Lines[3].StanzaBreakBefore {
		t.Fatalf("full stanza markers = %+v", parsed.Full.Lines)
	}

	first := parsed.Full.Lines[0]
	if len(first.Segments) != 1 || fmt.Sprint(first.Segments[0].PerformerIDs) != "[初音未来]" {
		t.Fatalf("first performer segmentation = %+v", first)
	}
	var aligned bool
	for index := 0; index+1 < len(first.Segments[0].Ruby); index++ {
		left, right := first.Segments[0].Ruby[index], first.Segments[0].Ruby[index+1]
		if left == (RubySpan{Text: "理", Reading: "わ"}) && right == (RubySpan{Text: "由", Reading: "け"}) {
			aligned = true
		}
	}
	if !aligned {
		t.Fatalf("grapheme-aligned source ruby was not retained: %+v", first.Segments[0].Ruby)
	}
	groupLine := parsed.Full.Lines[3]
	if len(groupLine.Segments) != 1 || fmt.Sprint(groupLine.Segments[0].PerformerIDs) != "[初音未来 镜音铃]" {
		t.Fatalf("group performer segmentation = %+v", groupLine)
	}
	for _, line := range parsed.Full.Lines {
		if strings.Contains(line.Japanese, "翻译") || strings.Contains(strings.ToLower(line.Japanese), "romanization") ||
			strings.Contains(line.Japanese, "Amia") {
			t.Fatalf("non-original or romanized output leaked: %q", line.Japanese)
		}
		for _, segment := range line.Segments {
			var rubyText strings.Builder
			for _, span := range segment.Ruby {
				if span.Reading == "Amia" {
					t.Fatalf("Latin source ruby leaked: %+v", segment.Ruby)
				}
				rubyText.WriteString(span.Text)
			}
			if rubyText.String() != segment.Text {
				t.Fatalf("ruby text = %q segment = %q", rubyText.String(), segment.Text)
			}
		}
	}
	if got := parsed.Full.Performers; got[0].Color != "#33CCBB" || got[1].Color != "#FFCC11" {
		t.Fatalf("performer colors = %+v", got)
	}
}

func TestMoegirlFixtureClassifiesTaggedGameOnlyWithoutPromotingItToFull(t *testing.T) {
	content, err := os.ReadFile("testdata/moegirl-section-game-only.wiki")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseMoegirlSectionWithPolicy(string(content), PerformerSegmentationSekaiEligible)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.ReasonCode != model.LyricsSourceVersionReasonTaggedGameOnlyFullFromVocaloid ||
		len(parsed.Full.Lines) != 0 || len(parsed.Game.Lines) != 1 || parsed.Game.Lines[0].Japanese != "ゲームだけ" ||
		len(parsed.GameLineIndexes) != 0 {
		t.Fatalf("game-only parse = %+v", parsed)
	}
}

func TestMoegirlFixedFetchRetainsTaggedGameEvidenceWithoutPromotingFull(t *testing.T) {
	body, err := os.ReadFile("testdata/moegirl-section-game-only.wiki")
	if err != nil {
		t.Fatal(err)
	}
	const pageID, revisionID = 3, 33
	const title = "合成演唱歌曲/游戏歌曲"
	sha := fmt.Sprintf("%x", sha1.Sum(body))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("revids") != "33" || r.URL.Query().Get("maxlag") != mediaWikiMaxLag {
			t.Fatalf("unexpected MediaWiki query: %s", r.URL.RawQuery)
		}
		writePageResponseWithCategories(w, pageID, revisionID, sha, title, string(body), []string{"歌曲"})
	}))
	defer server.Close()

	provider := newMoegirlProvider(ProviderConfig{Provider: ProviderMoegirl, Origin: OriginMoegirl},
		newMediaWikiClient(server.URL, 0, time.Hour, server.Client()))
	evidenceRef, indexEvidence := testRevisionIndexEvidence(
		t, ProviderMoegirl, "search:moegirl:1", 1, 11, "固定索引", []byte("固定索引证据"), []string{"索引"},
	)
	candidate := Candidate{
		Provider: ProviderMoegirl, Origin: OriginMoegirl, PageID: pageID, RevisionID: revisionID,
		SHA1: sha, Title: title, CanonicalURL: canonicalRevisionURL(ProviderMoegirl, title, revisionID),
		Categories: []string{"歌曲"}, Section: "合成游戏版/歌词", RenditionKey: "game-sekai",
		VersionReason:     model.LyricsSourceVersionReasonTaggedGameOnlyFullFromVocaloid,
		IndexEvidenceRefs: []model.LyricsSourceIndexEvidenceRef{evidenceRef},
		IndexEvidence:     []IndexEvidence{indexEvidence},
	}
	identity := MusicIdentity{MusicID: 10, JapaneseTitle: "合成試験曲", ProducerMetadata: "制作者",
		Lyricist: "制作者", Composer: "制作者", Arranger: "制作者",
		PerformerSegmentationPolicy: PerformerSegmentationSekaiEligible}
	fixed, err := provider.FetchFixedCandidateRevision(context.Background(), identity, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if fixed.Document == nil || len(fixed.Extraction.Lines) != 1 || fixed.Extraction.Lines[0].Japanese != "ゲームだけ" ||
		len(fixed.FixedIdentities) != 1 || fixed.FixedIdentities[0].RenditionKey != "game-sekai" ||
		fixed.FixedIdentities[0].FetchedAt != canonicalFetchedAt(fixed.FetchedAt) ||
		len(fixed.Document.Full.Lines) != 0 || fixed.Document.Game == nil || len(fixed.Document.Game.Lines) != 1 ||
		fixed.Document.Game.Lines[0].Text != "ゲームだけ" || fixed.Document.Provenance.FullText.RenditionKey != "" ||
		fixed.Document.Provenance.GameText == nil || fixed.Document.Provenance.GameText.RenditionKey != "game-sekai" {
		t.Fatalf("game-only fixed evidence = %+v", fixed)
	}
	if err := model.ValidateLyricsSourceFixedIdentity(fixed.FixedIdentities[0]); err != nil {
		t.Fatalf("game-only fixed identity: %v", err)
	}
	if err := model.ValidateLyricsSourceDocument(*fixed.Document); err != nil {
		t.Fatalf("game-only source document: %v", err)
	}
}

func TestMoegirlParserIsOrderIndependentAndFailsClosed(t *testing.T) {
	valid := `{{LyricsKai/ext
|original=歌う
|Romanization=utau
|charas=初音未来
|translated=唱歌
|colors=#39c
|type=colors,multiver
}}`
	parsed, err := ParseMoegirlSectionWithPolicy(valid, PerformerSegmentationSekaiEligible)
	if err != nil || parsed.ReasonCode != model.LyricsSourceVersionReasonUntaggedFullOnly ||
		len(parsed.Full.Lines) != 1 || parsed.Full.Lines[0].Japanese != "歌う" {
		t.Fatalf("order-independent parse = %+v err=%v", parsed, err)
	}
	for name, mutate := range map[string]func(string) string{
		"unknown parameter": func(value string) string { return strings.Replace(value, "|translated=唱歌", "|unknown=payload", 1) },
		"unknown tag": func(value string) string {
			return strings.Replace(value, "歌う", "<--Tag-Start:Short Ver.-->\n歌う\n<--Tag-End-->", 1)
		},
		"unknown template": func(value string) string { return strings.Replace(value, "歌う", "{{unsafe|歌う}}", 1) },
		"bad performer":    func(value string) string { return strings.Replace(value, "歌う", "@2歌う", 1) },
		"bad color":        func(value string) string { return strings.Replace(value, "#39c", "red", 1) },
		"duplicate original": func(value string) string {
			return strings.Replace(value, "|Romanization=utau", "|original=踊る", 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseMoegirlSectionWithPolicy(mutate(valid), PerformerSegmentationSekaiEligible); err == nil {
				t.Fatal("malformed Moegirl section was accepted")
			}
		})
	}
}

func TestMoegirlGameProjectionFailsClosedOnRepeatedLineAmbiguity(t *testing.T) {
	full := []StructuredLine{{Japanese: "共通"}, {Japanese: "反復"}, {Japanese: "反復"}, {Japanese: "末尾"}}
	game := []StructuredLine{{Japanese: "共通"}, {Japanese: "反復"}, {Japanese: "末尾"}}
	if _, err := moegirlGameProjection(full, game); err == nil {
		t.Fatal("ambiguous repeated Full line was projected")
	}
	uniqueGame := []StructuredLine{{Japanese: "共通"}, {Japanese: "末尾"}}
	projection, err := moegirlGameProjection(full, uniqueGame)
	if err != nil || fmt.Sprint(projection) != "[0 3]" {
		t.Fatalf("unique projection=%v err=%v", projection, err)
	}
}

func TestMoegirlTaggedFullAndGameRetainBothArtifactsWhenProjectionIsAmbiguous(t *testing.T) {
	content := `{{LyricsKai/ext
|original=共通
<--Tag-Start:Game Ver.-->
反復
<--Tag-End-->
<--Tag-Start:Full Ver.-->
反復
反復
<--Tag-End-->
末尾
|charas=初音未来
|colors=#39c
|type=colors,multiver
}}`
	parsed, err := ParseMoegirlSectionWithPolicy(content, PerformerSegmentationSekaiEligible)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.ReasonCode != model.LyricsSourceVersionReasonTaggedFullAndGame || len(parsed.Full.Lines) != 4 ||
		len(parsed.Game.Lines) != 3 || parsed.GameLineIndexes != nil || parsed.Full.Lines[1].Japanese != "反復" ||
		parsed.Game.Lines[1].Japanese != "反復" {
		t.Fatalf("ambiguous tagged Full/Game extraction=%+v", parsed)
	}
}

func TestMoegirlGraphemeAlignedRubyAcceptsKanaOnly(t *testing.T) {
	for _, test := range []struct {
		base, reading string
		want          bool
	}{
		{base: "理由", reading: "わけ", want: true},
		{base: "暗闇", reading: "クロ", want: true},
		{base: "先", reading: "さき"},
		{base: "編み合", reading: "Amia"},
		{base: "仮死", reading: "歌詞"},
		{base: "ば", reading: "ば", want: true},
	} {
		spans, ok := alignedMoegirlSourceRuby(test.base, test.reading)
		if ok != test.want {
			t.Fatalf("alignedMoegirlSourceRuby(%q,%q) ok=%t spans=%+v want=%t", test.base, test.reading, ok, spans, test.want)
		}
		if ok {
			var text strings.Builder
			for _, span := range spans {
				text.WriteString(span.Text)
			}
			if text.String() != test.base {
				t.Fatalf("aligned ruby text = %q, want %q", text.String(), test.base)
			}
		}
	}
}

func TestProviderConfigKeepsFandomAndMoegirlIndependentAndSecure(t *testing.T) {
	configs := DefaultProviderConfigs()
	if len(configs) != 2 || configs[0].Provider != ProviderVocaloidFandom || configs[1].Provider != ProviderMoegirl ||
		configs[0].Origin != OriginVocaloidFandom || configs[1].Origin != OriginMoegirl ||
		configs[0].CrawlDelay != 10*time.Second || configs[1].CrawlDelay != 10*time.Second ||
		configs[0].CacheTTL != 10*time.Second || configs[1].CacheTTL != 10*time.Second {
		t.Fatalf("default provider configs = %+v", configs)
	}
	for name, mutate := range map[string]func(*ProviderConfig){
		"cross origin": func(config *ProviderConfig) { config.Origin = OriginVocaloidFandom },
		"http":         func(config *ProviderConfig) { config.APIEndpoint = "http://moegirl.icu/api.php" },
		"other host":   func(config *ProviderConfig) { config.APIEndpoint = "https://evil.example/api.php" },
		"short delay":  func(config *ProviderConfig) { config.CrawlDelay = time.Second },
		"short cache":  func(config *ProviderConfig) { config.CacheTTL = time.Second },
		"no index":     func(config *ProviderConfig) { config.Indexes = nil },
	} {
		t.Run(name, func(t *testing.T) {
			config := configs[1]
			mutate(&config)
			if _, err := NewRegistry(config); err == nil {
				t.Fatal("unsafe provider config was accepted")
			}
		})
	}
}

func TestProviderRegistryUsesIndependentClientsAndRoutesByProvider(t *testing.T) {
	registry, err := NewRegistry(DefaultProviderConfigs()...)
	if err != nil {
		t.Fatal(err)
	}
	fandom := registry.providers[ProviderVocaloidFandom].(*fandomProvider)
	moegirl := registry.providers[ProviderMoegirl].(*moegirlProvider)
	if fandom.client == moegirl.client || fandom.client.cache == nil || moegirl.client.cache == nil ||
		fandom.client.rateToken == moegirl.client.rateToken {
		t.Fatal("provider clients, caches, or crawl limiters share state")
	}

	fandomStub := &stubSourceProvider{id: ProviderVocaloidFandom, candidates: []Candidate{{Provider: ProviderVocaloidFandom, PageID: 9}}}
	moegirlStub := &stubSourceProvider{id: ProviderMoegirl, candidates: []Candidate{{Provider: ProviderMoegirl, PageID: 3}}}
	routed, err := newRegistryWithProviders(moegirlStub, fandomStub)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := routed.Search(context.Background(), MusicIdentity{MusicID: 1, JapaneseTitle: "曲", ProducerMetadata: "作者"})
	if err != nil || len(candidates) != 1 || candidates[0].Provider != ProviderMoegirl ||
		moegirlStub.searchCalls != 1 || fandomStub.searchCalls != 0 {
		t.Fatalf("registry candidates=%+v calls moegirl=%d fandom=%d err=%v",
			candidates, moegirlStub.searchCalls, fandomStub.searchCalls, err)
	}
	if _, err := routed.FetchFixedCandidateRevision(context.Background(), MusicIdentity{}, Candidate{Provider: ProviderMoegirl}); err != nil || moegirlStub.fetchCalls != 1 {
		t.Fatalf("Moegirl routing calls=%d err=%v", moegirlStub.fetchCalls, err)
	}
	if _, err := routed.FetchFixedCandidateRevision(context.Background(), MusicIdentity{}, Candidate{}); err != nil || fandomStub.fetchCalls != 1 {
		t.Fatalf("legacy Fandom routing calls=%d err=%v", fandomStub.fetchCalls, err)
	}
}

func TestRegistryFallsBackFromDeterministicSekaipediaFailuresOnly(t *testing.T) {
	fallbackErrors := []error{
		ErrAmbiguous,
		ErrRevisionChanged,
		ErrMissingLyrics,
		ErrUnsupportedTable,
		ErrLyricsTooLarge,
		ErrMalformedResponse,
		ErrCatalogRenditionConflict,
	}
	for _, authorityErr := range fallbackErrors {
		t.Run(authorityErr.Error(), func(t *testing.T) {
			sekaipedia := &stubSourceProvider{id: ProviderSekaipedia, searchErr: authorityErr}
			moegirl := &stubSourceProvider{id: ProviderMoegirl, candidates: []Candidate{{Provider: ProviderMoegirl, PageID: 3}}}
			fandom := &stubSourceProvider{id: ProviderVocaloidFandom, candidates: []Candidate{{Provider: ProviderVocaloidFandom, PageID: 9}}}
			registry, err := newRegistryWithProviders(fandom, sekaipedia, moegirl)
			if err != nil {
				t.Fatal(err)
			}
			candidates, err := registry.Search(context.Background(), MusicIdentity{MusicID: 1, JapaneseTitle: "曲", ProducerMetadata: "作者"})
			if err != nil || len(candidates) != 1 || candidates[0].Provider != ProviderMoegirl ||
				sekaipedia.searchCalls != 1 || moegirl.searchCalls != 1 || fandom.searchCalls != 0 {
				t.Fatalf("fallback candidates=%+v calls=%d/%d/%d err=%v", candidates,
					sekaipedia.searchCalls, moegirl.searchCalls, fandom.searchCalls, err)
			}
		})
	}

	transportErr := &HTTPError{StatusCode: http.StatusServiceUnavailable}
	sekaipedia := &stubSourceProvider{id: ProviderSekaipedia, searchErr: transportErr}
	moegirl := &stubSourceProvider{id: ProviderMoegirl, candidates: []Candidate{{Provider: ProviderMoegirl, PageID: 3}}}
	registry, err := newRegistryWithProviders(sekaipedia, moegirl)
	if err != nil {
		t.Fatal(err)
	}
	if candidates, err := registry.Search(context.Background(), MusicIdentity{MusicID: 1, JapaneseTitle: "曲", ProducerMetadata: "作者"}); !errors.Is(err, transportErr) || candidates != nil || moegirl.searchCalls != 0 {
		t.Fatalf("transport failure candidates=%+v fallbackCalls=%d err=%v", candidates, moegirl.searchCalls, err)
	}

	nonStructuralErr := &HTTPError{StatusCode: http.StatusNotFound}
	sekaipedia = &stubSourceProvider{id: ProviderSekaipedia, searchErr: nonStructuralErr}
	moegirl = &stubSourceProvider{id: ProviderMoegirl, candidates: []Candidate{{Provider: ProviderMoegirl, PageID: 3}}}
	registry, err = newRegistryWithProviders(sekaipedia, moegirl)
	if err != nil {
		t.Fatal(err)
	}
	if candidates, err := registry.Search(context.Background(), MusicIdentity{MusicID: 1, JapaneseTitle: "曲", ProducerMetadata: "作者"}); !errors.Is(err, nonStructuralErr) || candidates != nil || moegirl.searchCalls != 0 {
		t.Fatalf("non-structural acquisition failure candidates=%+v fallbackCalls=%d err=%v",
			candidates, moegirl.searchCalls, err)
	}
}
