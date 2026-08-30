package lyricssource

import (
	"context"

	"errors"
	"fmt"

	"strconv"
	"strings"

	"testing"

	"moesekai/server/internal/model"
)

func TestMoegirlCatalogDisabledSearchAndFixedFetchUseVocaloidIdentity(t *testing.T) {
	for _, musicID := range []int{794, 795} {
		t.Run(strconv.Itoa(musicID), func(t *testing.T) {
			pageTitle := fmt.Sprintf("Vocaloid-only page %d", musicID)
			anchor := fmt.Sprintf("Vocaloid-only %d", musicID)
			indexBody := fmt.Sprintf("* [[%s#%s|合成試験曲]]\n", pageTitle, anchor)
			body := moegirlMatchingTestSection(anchor, "@999歌う")
			body = strings.Replace(body, "|colors=#39c", "|colors=not-a-color", 1)
			body = strings.Replace(body, "|charas=初音未来", "|charas=malformed source-only segmentation", 1)
			provider, _ := newMoegirlSearchTestProvider(t, indexBody, map[string]moegirlSearchTestPage{
				pageTitle: {pageID: musicID, revisionID: musicID + 1000, body: body},
			})
			identity := MusicIdentity{
				MusicID: musicID, JapaneseTitle: "合成試験曲", ProducerMetadata: "制作者",
				Lyricist: "制作者", Composer: "制作者", Arranger: "制作者",
				PerformerSegmentationPolicy: PerformerSegmentationDisabled,
			}

			candidates, err := provider.Search(context.Background(), identity)
			if err != nil || len(candidates) != 1 {
				t.Fatalf("catalog-disabled candidates=%+v err=%v", candidates, err)
			}
			candidate := candidates[0]
			if candidate.RenditionKey != "full-vocaloid" || candidate.VersionReason != model.LyricsSourceVersionReasonUntaggedFullOnly ||
				candidate.Section != anchor+"/歌词" {
				t.Fatalf("catalog-disabled candidate identity = %+v", candidate)
			}

			fixed, err := provider.FetchFixedCandidateRevision(context.Background(), identity, candidate)
			if err != nil {
				t.Fatal(err)
			}
			if fixed.RenditionKey != "full-vocaloid" || fixed.Extraction.Version.Kind != "vocaloid" || fixed.Document == nil ||
				fixed.Document.Full.Version.Kind != "vocaloid" || fixed.Document.Full.Version.Label != "Vocaloid Version" ||
				len(fixed.FixedIdentities) != 1 || fixed.FixedIdentities[0].RenditionKey != "full-vocaloid" ||
				fixed.Document.Provenance.FullText.RenditionKey != "full-vocaloid" ||
				fixed.Document.Provenance.VersionEvidence.RenditionKey != "full-vocaloid" ||
				fixed.Document.Provenance.PerformerSegmentation != nil || fixed.Document.Game != nil || fixed.Document.GameProjection != nil ||
				fixed.Document.Full.Performers == nil || len(fixed.Document.Full.Performers) != 0 {
				t.Fatalf("catalog-disabled fixed identity=%+v document=%+v", fixed, fixed.Document)
			}
			for _, line := range fixed.Document.Full.Lines {
				if len(line.Segments) != 1 || line.Segments[0].Text != line.Text ||
					line.Segments[0].PerformerIDs == nil || len(line.Segments[0].PerformerIDs) != 0 ||
					line.TrailingPerformerIDs == nil || len(line.TrailingPerformerIDs) != 0 || strings.Contains(line.Text, "@999") {
					t.Fatalf("catalog-disabled line retained source segmentation: %+v", line)
				}
			}
			if err := model.ValidateLyricsSourceDocument(*fixed.Document); err != nil {
				t.Fatalf("catalog-disabled model validation: %v", err)
			}
		})
	}
}

func TestMoegirlRecallRelaxationStillRequiresFixedIndexAndMatchingSectionMetadata(t *testing.T) {
	identity := MusicIdentity{
		MusicID: 10, JapaneseTitle: "光", ProducerMetadata: "制作者",
		Lyricist: "制作者", Composer: "制作者", Arranger: "制作者",
	}

	t.Run("positive typography alternate title and missing role", func(t *testing.T) {
		const pageTitle = "回収対象頁"
		indexBody := "* [[" + pageTitle + "#光|光　（光芒）]]\n"
		body := moegirlMatchingTestSection("光", "歌う")
		body = strings.Replace(body, "|曲名=合成試験曲", "|曲名=光（光之聲）", 1)
		body = strings.Replace(body, "|作曲=制作者\n", "", 1)
		provider, _ := newMoegirlSearchTestProvider(t, indexBody, map[string]moegirlSearchTestPage{
			pageTitle: {pageID: 2, revisionID: 22, body: body},
		})

		candidates, err := provider.Search(context.Background(), identity)
		if err != nil || len(candidates) != 1 {
			t.Fatalf("relaxed Moegirl candidates=%+v err=%v", candidates, err)
		}
		candidate := candidates[0]
		if len(candidate.IndexEvidenceRefs) != 1 || len(candidate.IndexEvidence) != 1 || ValidateCandidateIndexEvidence(candidate) != nil {
			t.Fatalf("relaxed Moegirl evidence refs=%+v evidence=%+v", candidate.IndexEvidenceRefs, candidate.IndexEvidence)
		}
		fixed, err := provider.FetchFixedCandidateRevision(context.Background(), identity, candidate)
		if err != nil || len(fixed.IndexEvidence) != 1 || !indexEvidenceEqual(fixed.IndexEvidence[0], candidate.IndexEvidence[0]) {
			t.Fatalf("relaxed fixed Moegirl identity=%+v err=%v", fixed, err)
		}
		mutated := candidate
		mutated.IndexEvidence = cloneIndexEvidence(candidate.IndexEvidence)
		mutated.IndexEvidence[0].Raw[0] ^= 0xff
		if ValidateCandidateIndexEvidence(mutated) == nil {
			t.Fatal("relaxed Moegirl match accepted mutated fixed-index raw evidence")
		}
	})

	for name, test := range map[string]struct {
		indexDisplay  string
		metadataTitle string
		composer      string
		wantRequests  int32
	}{
		"unrelated one-character fixed-index title":        {indexDisplay: "灯", metadataTitle: "光", composer: "制作者", wantRequests: 0},
		"matching index but unrelated metadata title":      {indexDisplay: "光（光芒）", metadataTitle: "灯", composer: "制作者", wantRequests: 1},
		"matching title but contradictory contributor set": {indexDisplay: "光（光芒）", metadataTitle: "光（光之聲）", composer: "別人", wantRequests: 1},
		"reserved Chinese derivative index title":          {indexDisplay: "光（遊戲版）", metadataTitle: "光", composer: "制作者", wantRequests: 0},
	} {
		t.Run(name, func(t *testing.T) {
			const pageTitle = "拒否対象頁"
			indexBody := "* [[" + pageTitle + "#光|" + test.indexDisplay + "]]\n"
			body := moegirlMatchingTestSection("光", "歌う")
			body = strings.Replace(body, "|曲名=合成試験曲", "|曲名="+test.metadataTitle, 1)
			body = strings.Replace(body, "|作曲=制作者", "|作曲="+test.composer, 1)
			provider, requests := newMoegirlSearchTestProvider(t, indexBody, map[string]moegirlSearchTestPage{
				pageTitle: {pageID: 2, revisionID: 22, body: body},
			})

			candidates, err := provider.Search(context.Background(), identity)
			if err != nil || len(candidates) != 0 {
				t.Fatalf("negative Moegirl candidates=%+v err=%v", candidates, err)
			}
			if requests[pageTitle].Load() != test.wantRequests {
				t.Fatalf("target requests=%d, want %d", requests[pageTitle].Load(), test.wantRequests)
			}
		})
	}
}

func TestMoegirlSearchFailsClosedForMatchingVersionConflict(t *testing.T) {
	const pageTitle = "冲突页"
	indexBody := "* [[" + pageTitle + "#合成试验曲|合成試験曲]]\n"
	conflictBody := moegirlMatchingTestSection("合成试验曲", `<--Tag-Start:Full Ver.-->
秘密歌词不得进入错误
<--Tag-End-->`)
	provider, _ := newMoegirlSearchTestProvider(t, indexBody, map[string]moegirlSearchTestPage{
		pageTitle: {pageID: 2, revisionID: 22, body: conflictBody},
	})
	identity := MusicIdentity{MusicID: 10, JapaneseTitle: "合成試験曲", ProducerMetadata: "制作者",
		Lyricist: "制作者", Composer: "制作者", Arranger: "制作者"}

	candidates, err := provider.Search(context.Background(), identity)
	if err == nil || candidates != nil {
		t.Fatalf("matching conflict candidates=%+v err=%v", candidates, err)
	}
	var parseFailure *MatchingSectionParseError
	if !errors.As(err, &parseFailure) || !errors.Is(err, ErrUnsupportedTable) {
		t.Fatalf("matching conflict error type=%T err=%v", err, err)
	}
	if parseFailure.Provider != ProviderMoegirl || parseFailure.PageID != 2 || parseFailure.RevisionID != 22 ||
		parseFailure.ReasonCode != model.LyricsSourceVersionReasonVersionConflict {
		t.Fatalf("matching conflict metadata = %+v", parseFailure)
	}
	for _, content := range []string{"秘密歌词", pageTitle, "合成试验曲", "歌词"} {
		if strings.Contains(err.Error(), content) {
			t.Fatalf("matching conflict diagnostic leaked source content %q: %v", content, err)
		}
	}
}

func TestRegistryFailsClosedForValidAndConflictingMatchingTargets(t *testing.T) {
	indexBody := strings.Join([]string{
		"* [[A有效页#有效锚点|合成試験曲]]",
		"* [[B冲突页#冲突锚点|合成試験曲]]",
		"",
	}, "\n")
	provider, requests := newMoegirlSearchTestProvider(t, indexBody, map[string]moegirlSearchTestPage{
		"A有效页": {pageID: 2, revisionID: 22, body: moegirlMatchingTestSection("有效锚点", "有効な歌")},
		"B冲突页": {pageID: 3, revisionID: 33, body: moegirlMatchingTestSection("冲突锚点", `<--Tag-Start:Full Ver.-->
冲突歌词
<--Tag-End-->`)},
	})
	fandom := &stubSourceProvider{id: ProviderVocaloidFandom, candidates: []Candidate{{
		Provider: ProviderVocaloidFandom, PageID: 9, RevisionID: 99,
	}}}
	registry, err := newRegistryWithProviders(fandom, provider)
	if err != nil {
		t.Fatal(err)
	}
	identity := MusicIdentity{MusicID: 10, JapaneseTitle: "合成試験曲", ProducerMetadata: "制作者",
		Lyricist: "制作者", Composer: "制作者", Arranger: "制作者"}

	candidates, err := registry.Search(context.Background(), identity)
	if err == nil || candidates != nil {
		t.Fatalf("mixed matching candidates=%+v err=%v", candidates, err)
	}
	var parseFailure *MatchingSectionParseError
	if !errors.As(err, &parseFailure) || parseFailure.PageID != 3 ||
		parseFailure.ReasonCode != model.LyricsSourceVersionReasonVersionConflict {
		t.Fatalf("mixed matching error = %T %+v", err, parseFailure)
	}
	if requests["A有效页"].Load() != 1 || requests["B冲突页"].Load() != 1 || fandom.searchCalls != 0 {
		t.Fatalf("matching target request counts: valid=%d conflicting=%d fandom=%d",
			requests["A有效页"].Load(), requests["B冲突页"].Load(), fandom.searchCalls)
	}
}

func TestMoegirlTargetSectionNormalizesCRLFBeforeSlicing(t *testing.T) {
	content := "== 第一首 ==\r\n=== 歌词 ===\r\n{{LyricsKai/ext|type=colors,multiver|colors=#39c|charas=初音未来|original=歌う}}\r\n== 第二首 ==\r\n"
	section, path, err := moegirlTargetSection(content, "第一首")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(section, "\r") || strings.Contains(section, "第二首") || path != "第一首/歌词" {
		t.Fatalf("section=%q path=%q", section, path)
	}
}

func TestMoegirlCandidateValidationRejectsNoncanonicalEvidenceAndReason(t *testing.T) {
	evidenceRef, indexEvidence := testRevisionIndexEvidence(
		t, ProviderMoegirl, "search:moegirl:9", 9, 10, "固定索引", []byte("固定索引证据"), []string{},
	)
	candidate := Candidate{
		Provider: ProviderMoegirl, Origin: OriginMoegirl, PageID: 1, RevisionID: 2,
		SHA1: strings.Repeat("a", 40), Title: "歌曲页",
		CanonicalURL: canonicalRevisionURL(ProviderMoegirl, "歌曲页", 2), Categories: []string{},
		Section: "歌曲/歌词", RenditionKey: "full-sekai",
		VersionReason:     model.LyricsSourceVersionReasonUntaggedFullOnly,
		IndexEvidenceRefs: []model.LyricsSourceIndexEvidenceRef{evidenceRef},
		IndexEvidence:     []IndexEvidence{indexEvidence},
	}
	if !validMoegirlCandidate(candidate) {
		t.Fatal("canonical Moegirl candidate was rejected")
	}
	vocaloidCandidate := candidate
	vocaloidCandidate.RenditionKey = "full-vocaloid"
	if !validMoegirlCandidate(vocaloidCandidate) {
		t.Fatal("canonical catalog-disabled Moegirl candidate was rejected")
	}
	taggedVocaloidCandidate := vocaloidCandidate
	taggedVocaloidCandidate.VersionReason = model.LyricsSourceVersionReasonTaggedFullAndGame
	if validMoegirlCandidate(taggedVocaloidCandidate) {
		t.Fatal("tagged SEKAI evidence was accepted as a Vocaloid candidate")
	}
	for name, mutate := range map[string]func(*Candidate){
		"unknown reason":      func(value *Candidate) { value.VersionReason = "other"; value.RenditionKey = "" },
		"uppercase sha256":    func(value *Candidate) { value.IndexEvidenceRefs[0].SHA256 = strings.Repeat("B", 64) },
		"invalid evidence id": func(value *Candidate) { value.IndexEvidenceRefs[0].EvidenceID = "search evidence" },
	} {
		t.Run(name, func(t *testing.T) {
			mutated := candidate
			mutated.IndexEvidenceRefs = cloneIndexEvidenceRefs(candidate.IndexEvidenceRefs)
			mutate(&mutated)
			if validMoegirlCandidate(mutated) {
				t.Fatal("noncanonical Moegirl candidate was accepted")
			}
		})
	}
}
