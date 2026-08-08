package lyricssource

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"moesekai/server/internal/model"
)

func TestPerformerSegmentationPolicyUsesCatalogRenditionsWithoutMusicIDs(t *testing.T) {
	for name, test := range map[string]struct {
		vocals []model.CatalogVocalSignal
		want   PerformerSegmentationPolicy
	}{
		"vocaloid only": {
			vocals: []model.CatalogVocalSignal{
				{VocalID: 7001, VocalType: "original_song"},
				{VocalID: 7002, VocalType: "virtual_singer"},
			},
			want: PerformerSegmentationDisabled,
		},
		"cover or commissioned Sekai rendition": {
			vocals: []model.CatalogVocalSignal{
				{VocalID: 9001, VocalType: "virtual_singer"},
				{VocalID: 9002, VocalType: " SEKAI "},
			},
			want: PerformerSegmentationSekaiEligible,
		},
		"empty catalog evidence fails closed": {want: PerformerSegmentationDisabled},
	} {
		t.Run(name, func(t *testing.T) {
			if got := PerformerSegmentationPolicyFromCatalogVocals(test.vocals); got != test.want {
				t.Fatalf("policy=%q want=%q", got, test.want)
			}
		})
	}
}

func TestSekaipediaCatalogVocalSignalsSelectOnlyFixedSourceRenditions(t *testing.T) {
	virtualSingerBody := sekaipediaSyntheticTaggedLyricsBody("Miku", "歌う", "utau")
	sekaiBody := sekaipediaSyntheticTaggedLyricsBody("Kanade,Miku", "奏でる", "kanaderu")
	content := strings.Join([]string{
		"== Versions ==",
		"{{Song versions head}}",
		"{{Song versions line|version=VIRTUAL SINGER|singers=Hatsune Miku|audio=virtual|date=2026-01-01}}",
		"{{Song versions line|version=SEKAI|singers=Yoisaki Kanade, Hatsune Miku|audio=sekai|date=2026-01-02}}",
		"{{Song versions tail}}",
		"== Lyrics ==",
		"<tabber>",
		"VIRTUAL SINGER =",
		virtualSingerBody,
		"|-|",
		"SEKAI =",
		sekaiBody,
		"</tabber>",
	}, "\n")

	for _, test := range []struct {
		name             string
		vocals           []model.CatalogVocalSignal
		wantPolicy       PerformerSegmentationPolicy
		wantKind         string
		wantRenditionKey string
		wantJapanese     string
		wantPerformerIDs []string
	}{
		{
			name:       "original_song is not an Original-rendition override",
			vocals:     []model.CatalogVocalSignal{{VocalID: 1, VocalType: "original_song"}},
			wantPolicy: PerformerSegmentationDisabled, wantKind: "vocaloid",
			wantRenditionKey: "full-vocaloid", wantJapanese: "歌う", wantPerformerIDs: []string{"歌唱者-21"},
		},
		{
			name:       "explicit virtual_singer keeps VIRTUAL SINGER",
			vocals:     []model.CatalogVocalSignal{{VocalID: 2, VocalType: "virtual_singer"}},
			wantPolicy: PerformerSegmentationDisabled, wantKind: "vocaloid",
			wantRenditionKey: "full-vocaloid", wantJapanese: "歌う", wantPerformerIDs: []string{"歌唱者-21"},
		},
		{
			name: "original_song plus virtual_singer keeps VIRTUAL SINGER",
			vocals: []model.CatalogVocalSignal{
				{VocalID: 3, VocalType: "original_song"},
				{VocalID: 4, VocalType: "virtual_singer"},
			},
			wantPolicy: PerformerSegmentationDisabled, wantKind: "vocaloid",
			wantRenditionKey: "full-vocaloid", wantJapanese: "歌う", wantPerformerIDs: []string{"歌唱者-21"},
		},
		{
			name:       "explicit sekai selects SEKAI",
			vocals:     []model.CatalogVocalSignal{{VocalID: 5, VocalType: "sekai"}},
			wantPolicy: PerformerSegmentationSekaiEligible, wantKind: "sekai",
			wantRenditionKey: "full-sekai", wantJapanese: "奏でる", wantPerformerIDs: []string{"歌唱者-17", "歌唱者-21"},
		},
		{
			name: "original_song does not weaken explicit sekai",
			vocals: []model.CatalogVocalSignal{
				{VocalID: 6, VocalType: "original_song"},
				{VocalID: 7, VocalType: "sekai"},
			},
			wantPolicy: PerformerSegmentationSekaiEligible, wantKind: "sekai",
			wantRenditionKey: "full-sekai", wantJapanese: "奏でる", wantPerformerIDs: []string{"歌唱者-17", "歌唱者-21"},
		},
		{
			name: "mixed explicit rendition signals retain SEKAI authority",
			vocals: []model.CatalogVocalSignal{
				{VocalID: 8, VocalType: "virtual_singer"},
				{VocalID: 9, VocalType: " SEKAI "},
			},
			wantPolicy: PerformerSegmentationSekaiEligible, wantKind: "sekai",
			wantRenditionKey: "full-sekai", wantJapanese: "奏でる", wantPerformerIDs: []string{"歌唱者-17", "歌唱者-21"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy := PerformerSegmentationPolicyFromCatalogVocals(test.vocals)
			if policy != test.wantPolicy {
				t.Fatalf("policy=%q want=%q", policy, test.wantPolicy)
			}
			parsed, err := parseSekaipediaSong(content, policy)
			if err != nil {
				t.Fatal(err)
			}
			if parsed.Full.Version.Kind != test.wantKind || parsed.RenditionKey != test.wantRenditionKey ||
				len(parsed.Full.Lines) != 1 || parsed.Full.Lines[0].Japanese != test.wantJapanese ||
				len(parsed.Full.Lines[0].Segments) != 1 ||
				!equalStrings(parsed.Full.Lines[0].Segments[0].PerformerIDs, test.wantPerformerIDs) {
				t.Fatalf("source rendition=%+v", parsed)
			}
		})
	}
}

func TestSekaipediaCatalogPolicyNeverInventsMissingRendition(t *testing.T) {
	content := strings.Join([]string{
		"== Versions ==",
		"{{Song versions head}}",
		"{{Song versions line|version=SEKAI|singers=Yoisaki Kanade, Hatsune Miku|audio=sekai|date=2026-01-02}}",
		"{{Song versions tail}}",
		"== Lyrics ==",
		"<tabber>",
		"SEKAI =",
		sekaipediaSyntheticTaggedLyricsBody("Kanade,Miku", "奏でる", "kanaderu"),
		"</tabber>",
	}, "\n")
	policy := PerformerSegmentationPolicyFromCatalogVocals([]model.CatalogVocalSignal{{
		VocalID: 1, VocalType: "virtual_singer",
	}})
	if _, err := parseSekaipediaSong(content, policy); !errors.Is(err, ErrCatalogRenditionConflict) {
		t.Fatalf("missing VIRTUAL SINGER rendition error=%v", err)
	}
}

func TestPerformerSegmentationPolicySelectsCatalogRenditionWithoutFlatteningSourceEvidence(t *testing.T) {
	extraction := Extraction{
		Version:              LyricsVersion{Kind: "original", Label: "Original Version"},
		Performers:           []Performer{{PerformerID: "miku", Name: "Miku", Color: "#33CCBB"}},
		RubyGeneratorVersion: rubyGeneratorVersion,
		Lines: []StructuredLine{{
			Japanese: "初音歌う", StanzaBreakBefore: true,
			Segments: []LyricsSegment{
				{Text: "初音", PerformerIDs: []string{"miku"}, Ruby: []RubySpan{{Text: "初", Reading: "はつ"}, {Text: "音", Reading: "ね"}}},
				{Text: "歌う", PerformerIDs: []string{"miku"}, Ruby: []RubySpan{{Text: "歌", Reading: "うた"}, {Text: "う"}}},
			},
			TrailingPerformerIDs: []string{"miku"},
		}},
	}
	keptOriginal, err := applyPerformerSegmentationPolicy(
		MusicIdentity{PerformerSegmentationPolicy: PerformerSegmentationDisabled}, extraction,
	)
	if err != nil || keptOriginal.Version != extraction.Version || len(keptOriginal.Performers) != 1 ||
		len(keptOriginal.Lines) != 1 || len(keptOriginal.Lines[0].Segments) != 2 ||
		!equalStrings(keptOriginal.Lines[0].Segments[0].PerformerIDs, []string{"miku"}) ||
		!equalStrings(keptOriginal.Lines[0].TrailingPerformerIDs, []string{"miku"}) {
		t.Fatalf("catalog-disabled Original extraction=%+v err=%v", keptOriginal, err)
	}
	_, document, err := buildFandomDocument(wikiPage{
		pageID: 1, revisionID: 2, sha1: strings.Repeat("a", 40), title: "歌曲页", categories: []string{},
	}, keptOriginal, "Lyrics", "full-original", []model.LyricsSourceIndexEvidenceRef{{
		EvidenceID: "search:vocaloid-fandom:1", SHA256: strings.Repeat("b", 64),
	}}, time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC))
	if err != nil || document.Full.Version.Kind != "original" ||
		document.FixedIdentities[0].RenditionKey != "full-original" ||
		document.Provenance.FullText.RenditionKey != "full-original" ||
		document.Provenance.PerformerSegmentation != nil || len(document.Full.Performers) != 0 ||
		len(document.Full.Lines[0].Segments) != 1 || document.Full.Lines[0].Segments[0].Text != document.Full.Lines[0].Text ||
		len(document.Full.Lines[0].Segments[0].PerformerIDs) != 0 {
		t.Fatalf("source-proven Original document=%+v err=%v", document, err)
	}
	if err := model.ValidateLyricsSourceDocument(*document); err != nil {
		t.Fatalf("source-proven Original model validation: %v", err)
	}

	sekaiExtraction := extraction
	sekaiExtraction.Version = LyricsVersion{Kind: "sekai", Label: "Project SEKAI Version"}
	if _, err := applyPerformerSegmentationPolicy(
		MusicIdentity{PerformerSegmentationPolicy: PerformerSegmentationDisabled}, sekaiExtraction,
	); !errors.Is(err, ErrCatalogRenditionConflict) {
		t.Fatalf("catalog-disabled SEKAI evidence error=%v", err)
	}
	keptSekai, err := applyPerformerSegmentationPolicy(
		MusicIdentity{PerformerSegmentationPolicy: PerformerSegmentationSekaiEligible}, sekaiExtraction,
	)
	if err != nil || keptSekai.Version.Kind != "sekai" || len(keptSekai.Performers) != 1 || len(keptSekai.Lines[0].Segments) != 2 {
		t.Fatalf("eligible SEKAI extraction=%+v err=%v", keptSekai, err)
	}

	vocaloidExtraction := extraction
	vocaloidExtraction.Version = LyricsVersion{Kind: "vocaloid", Label: "VIRTUAL SINGER Version"}
	for _, policy := range []PerformerSegmentationPolicy{PerformerSegmentationDisabled, PerformerSegmentationSekaiEligible} {
		keptVocaloid, err := applyPerformerSegmentationPolicy(
			MusicIdentity{PerformerSegmentationPolicy: policy}, vocaloidExtraction,
		)
		if err != nil || keptVocaloid.Version.Kind != "vocaloid" || len(keptVocaloid.Performers) != 1 ||
			len(keptVocaloid.Lines[0].Segments) != 2 {
			t.Fatalf("Vocaloid policy=%q extraction=%+v err=%v", policy, keptVocaloid, err)
		}
	}
}

func TestFandomSearchIdentityHonorsCatalogRenditionPolicy(t *testing.T) {
	search := func(content string, policy PerformerSegmentationPolicy) ([]Candidate, error) {
		t.Helper()
		sha := sha1Hex(content)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writePageResponseWithCategories(w, 1, 2, sha, "新曲", content, []string{"Songs"})
		}))
		defer server.Close()
		return newTestClient(server.URL).Search(context.Background(), MusicIdentity{
			MusicID: 794, JapaneseTitle: "新曲", ProducerMetadata: "作者",
			PerformerSegmentationPolicy: policy,
		})
	}

	candidates, err := search("作者 original song Lyrics\n== Lyrics ==\n歌う", PerformerSegmentationDisabled)
	if err != nil || len(candidates) != 1 || candidates[0].RenditionKey != "full-original" ||
		candidates[0].VersionReason != model.LyricsSourceVersionReasonUntaggedFullOnly {
		t.Fatalf("catalog-disabled Fandom Original candidates=%+v err=%v", candidates, err)
	}

	sekaiContent := `作者 original song Lyrics
== Lyrics ==
<tabber>SEKAI Version =
{|
! Japanese
|-
|歌う
|}
</tabber>`
	candidates, err = search(sekaiContent, PerformerSegmentationDisabled)
	if candidates != nil || !errors.Is(err, ErrCatalogRenditionConflict) {
		t.Fatalf("catalog-disabled Fandom SEKAI candidates=%+v err=%v", candidates, err)
	}

	candidates, err = search(sekaiContent, PerformerSegmentationSekaiEligible)
	if err != nil || len(candidates) != 1 || candidates[0].RenditionKey != "full-sekai" {
		t.Fatalf("Sekai-eligible Fandom candidates=%+v err=%v", candidates, err)
	}
}

func TestFandomCatalogDisabledFixedFetchPreservesFullOriginal(t *testing.T) {
	const pageID, revisionID = 795, 1795
	const title = "新曲"
	const content = "作者 original song Lyrics\n== Lyrics ==\n歌う"
	sha := sha1Hex(content)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requestedRevision := r.URL.Query().Get("revids"); requestedRevision != "" && requestedRevision != "1795" {
			t.Fatalf("unexpected MediaWiki query: %s", r.URL.RawQuery)
		}
		writePageResponseWithCategories(w, pageID, revisionID, sha, title, content, []string{"Songs"})
	}))
	defer server.Close()

	identity := MusicIdentity{
		MusicID: pageID, JapaneseTitle: title, ProducerMetadata: "作者",
		PerformerSegmentationPolicy: PerformerSegmentationDisabled,
	}
	client := newTestClient(server.URL)
	candidates, err := client.Search(context.Background(), identity)
	if err != nil || len(candidates) != 1 || candidates[0].RenditionKey != "full-original" {
		t.Fatalf("catalog-disabled Fandom Original search candidates=%+v err=%v", candidates, err)
	}
	fixed, err := client.FetchFixedCandidateRevision(context.Background(), identity, candidates[0])
	if err != nil {
		t.Fatal(err)
	}
	if fixed.RenditionKey != "full-original" || fixed.Extraction.Version.Kind != "original" || fixed.Document == nil ||
		len(fixed.IndexEvidence) != 1 || !indexEvidenceEqual(fixed.IndexEvidence[0], candidates[0].IndexEvidence[0]) ||
		fixed.Document.Full.Version.Kind != "original" || fixed.Document.Full.Version.Label != "Original Version" ||
		len(fixed.FixedIdentities) != 1 || fixed.FixedIdentities[0].RenditionKey != "full-original" ||
		fixed.Document.Provenance.PerformerSegmentation != nil || fixed.Document.Full.Performers == nil ||
		len(fixed.Document.Full.Performers) != 0 {
		t.Fatalf("catalog-disabled Fandom Original fixed=%+v document=%+v", fixed, fixed.Document)
	}
	if err := model.ValidateLyricsSourceDocument(*fixed.Document); err != nil {
		t.Fatalf("catalog-disabled Fandom model validation: %v", err)
	}
}

func TestEligibleMoegirlDocumentRetainsPerformerSegmentationProvenance(t *testing.T) {
	body, err := os.ReadFile("testdata/moegirl-section-full-game.wiki")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseMoegirlSectionWithPolicy(string(body), PerformerSegmentationSekaiEligible)
	if err != nil {
		t.Fatal(err)
	}
	candidate := Candidate{
		Provider: ProviderMoegirl, Origin: OriginMoegirl, PageID: 1, RevisionID: 2,
		SHA1: strings.Repeat("a", 40), Title: "歌曲页",
		CanonicalURL: canonicalRevisionURL(ProviderMoegirl, "歌曲页", 2), Categories: []string{},
		Section: "合成试验曲/歌词", RenditionKey: "full-sekai", VersionReason: parsed.ReasonCode,
		IndexEvidenceRefs: []model.LyricsSourceIndexEvidenceRef{{EvidenceID: "search:moegirl:1", SHA256: strings.Repeat("b", 64)}},
	}
	_, document, err := buildMoegirlDocument(candidate, parsed, time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Full.Lines) != 5 || document.Full.Version.Kind != "sekai" ||
		len(document.Full.Performers) != 2 || document.Provenance.PerformerSegmentation == nil ||
		len(document.Full.Lines[4].Segments) != 3 ||
		!equalStrings(document.Full.Lines[4].Segments[0].PerformerIDs, []string{"初音未来"}) ||
		len(document.Full.Lines[4].Segments[1].PerformerIDs) != 0 ||
		!equalStrings(document.Full.Lines[4].Segments[2].PerformerIDs, []string{"镜音铃"}) ||
		document.Game == nil || len(document.Game.Lines) == 0 || document.GameProjection == nil {
		t.Fatalf("eligible document did not preserve authoritative partial performer segmentation: %+v", document)
	}
}

func TestMoegirlVocaloidOnlyPolicyIgnoresSourceSegmentationMarkup(t *testing.T) {
	body, err := os.ReadFile("testdata/moegirl-section-full-game.wiki")
	if err != nil {
		t.Fatal(err)
	}
	section := string(body)
	for _, tag := range []string{
		"<--Tag-Start:Full Ver.-->", "<--Tag-Start:Game Ver.-->", "<--Tag-End-->",
	} {
		section = strings.ReplaceAll(section, tag, "")
	}
	section = strings.Replace(section,
		"|charas= 初音未来(世界计划)；镜音铃(世界计划)；合唱(@nolink)；初音未来、镜音铃",
		"|charas= malformed source-only segmentation", 1)
	section = strings.Replace(section,
		"|colors= #33CCBB; #FFCC11; #000; lg(#33CCBB, #FFCC11)",
		"|colors= not-a-color", 1)
	section = strings.ReplaceAll(section, "@4", "@999")

	parsed, err := ParseMoegirlSectionWithPolicy(section, PerformerSegmentationDisabled)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Full.Version != (LyricsVersion{Kind: "vocaloid", Label: "Vocaloid Version"}) ||
		len(parsed.Full.Performers) != 0 || len(parsed.Full.Lines) == 0 {
		t.Fatalf("disabled parse=%+v", parsed)
	}
	for _, line := range parsed.Full.Lines {
		if len(line.Segments) != 1 || line.Segments[0].Text != line.Japanese ||
			line.Segments[0].PerformerIDs == nil || len(line.Segments[0].PerformerIDs) != 0 ||
			line.TrailingPerformerIDs == nil || len(line.TrailingPerformerIDs) != 0 {
			t.Fatalf("disabled line retained segmentation: %+v", line)
		}
	}
	if _, err := ParseMoegirlSectionWithPolicy(section, PerformerSegmentationSekaiEligible); err == nil {
		t.Fatal("eligible parse accepted malformed performer metadata")
	}
}

func TestMoegirlCatalogDisabledRejectsIndependentSekaiVersionEvidence(t *testing.T) {
	body, err := os.ReadFile("testdata/moegirl-section-full-game.wiki")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseMoegirlSectionWithPolicy(string(body), PerformerSegmentationDisabled)
	if !errors.Is(err, ErrCatalogRenditionConflict) ||
		parsed.ReasonCode != model.LyricsSourceVersionReasonVersionConflict || !parsed.TaggedFull || !parsed.TaggedGame {
		t.Fatalf("catalog-disabled tagged evidence parsed=%+v err=%v", parsed, err)
	}
}
