package lyricssource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

const entityRulesSHA1 = "0123456789abcdef0123456789abcdef01234567"

type entityRulesPage struct {
	pageID     int
	revisionID int
	sha1       string
	title      string
	categories []string
	content    string
}

type identityEntityRulesFixture struct {
	ProducerAliases []struct {
		Name       string   `json:"name"`
		PageTitle  string   `json:"pageTitle"`
		Creator    string   `json:"creator"`
		Categories []string `json:"categories"`
		Content    string   `json:"content"`
		Canonical  string   `json:"canonical"`
		Want       bool     `json:"want"`
	} `json:"producerAliases"`
	RoleCredits []struct {
		Name     string            `json:"name"`
		Title    string            `json:"title"`
		Lyricist string            `json:"lyricist"`
		Composer string            `json:"composer"`
		Arranger string            `json:"arranger"`
		Aliases  map[string]string `json:"aliases"`
		Content  string            `json:"content"`
		Want     bool              `json:"want"`
	} `json:"roleCredits"`
	TitleMatches []struct {
		Name      string `json:"name"`
		Wanted    string `json:"wanted"`
		PageTitle string `json:"pageTitle"`
		Want      bool   `json:"want"`
	} `json:"titleMatches"`
	SongSignals []struct {
		Name       string   `json:"name"`
		Wanted     string   `json:"wanted"`
		Content    string   `json:"content"`
		Categories []string `json:"categories"`
		Want       bool     `json:"want"`
	} `json:"songSignals"`
	Restrictions []struct {
		Name           string   `json:"name"`
		Content        string   `json:"content"`
		Categories     []string `json:"categories"`
		WantRestricted bool     `json:"wantRestricted"`
	} `json:"restrictions"`
}

func loadIdentityEntityRulesFixture(t *testing.T) identityEntityRulesFixture {
	t.Helper()
	data, err := os.ReadFile("testdata/identity-entity-rules.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture identityEntityRulesFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func TestIdentityEntityFixtures(t *testing.T) {
	fixture := loadIdentityEntityRulesFixture(t)
	for _, test := range fixture.ProducerAliases {
		t.Run("producer alias/"+test.Name, func(t *testing.T) {
			page := wikiPage{title: test.PageTitle, categories: test.Categories, content: test.Content}
			if got := producerAliasPageMatches(page, test.Creator); got != test.Want {
				t.Fatalf("producerAliasPageMatches() = %t, want %t", got, test.Want)
			}
			canonical, ok := producerCanonicalIdentity(test.PageTitle)
			if !ok || canonical != test.Canonical {
				t.Fatalf("producer canonical identity = %q, %t; want %q, true", canonical, ok, test.Canonical)
			}
		})
	}
	for _, test := range fixture.RoleCredits {
		t.Run("role credits/"+test.Name, func(t *testing.T) {
			identity := MusicIdentity{
				JapaneseTitle: test.Title, Lyricist: test.Lyricist, Composer: test.Composer, Arranger: test.Arranger,
			}
			aliases := map[string]string{}
			for _, expected := range roleBoundCreditExpectations(identity) {
				contributors, ok := splitTopLevelContributors(expected.credit)
				if strings.TrimSpace(expected.credit) == "" {
					continue
				}
				if !ok {
					t.Fatalf("fixture credit did not split: role=%s credit=%q", expected.role, expected.credit)
				}
				for _, contributor := range contributors {
					if canonical := test.Aliases[contributor]; canonical != "" {
						aliases[creditAliasKey(expected.role, contributor)] = canonical
					}
				}
			}
			if got := roleBoundCreditsMatchWithAliases(identity, test.Content, aliases); got != test.Want {
				t.Fatalf("roleBoundCreditsMatchWithAliases() = %t, want %t; credits=%+v aliases=%+v", got, test.Want, wikiRoleCredits(test.Content), aliases)
			}
		})
	}
	for _, test := range fixture.TitleMatches {
		t.Run("title/"+test.Name, func(t *testing.T) {
			if got := candidateTitleMatches(test.PageTitle, test.Wanted); got != test.Want {
				t.Fatalf("candidateTitleMatches(%q, %q) = %t, want %t", test.PageTitle, test.Wanted, got, test.Want)
			}
		})
	}
	for _, test := range fixture.SongSignals {
		t.Run("song signal/"+test.Name, func(t *testing.T) {
			if got := hasSongSignal(test.Wanted, test.Content, test.Categories); got != test.Want {
				t.Fatalf("hasSongSignal() = %t, want %t", got, test.Want)
			}
		})
	}
	for _, test := range fixture.Restrictions {
		t.Run("restriction/"+test.Name, func(t *testing.T) {
			if got := hasLyricsTextRestriction(test.Content, test.Categories); got != test.WantRestricted {
				t.Fatalf("hasLyricsTextRestriction() = %t, want %t", got, test.WantRestricted)
			}
		})
	}
}

func TestWrongEntityGateRejectsExactAlbumCategories(t *testing.T) {
	identity := MusicIdentity{JapaneseTitle: "新曲", ProducerMetadata: "作者"}
	tests := []struct {
		name       string
		categories []string
		content    string
		want       candidateVerificationOutcome
	}{
		{
			name:       "exact category overrides matching lyrics and song signals",
			categories: []string{"Lyrics", "Albums"},
			content:    "作者 original song Lyrics",
			want:       candidateSignalMismatch,
		},
		{
			name:       "prefixed category is stripped case-insensitively",
			categories: []string{" category:ALBUMS "},
			content:    "作者 original song Lyrics",
			want:       candidateSignalMismatch,
		},
		{
			name:       "singular album category is rejected",
			categories: []string{"Album", "Songs"},
			content:    "作者 original song Lyrics",
			want:       candidateSignalMismatch,
		},
		{
			name:       "category substring is not rejected",
			categories: []string{"Songs from albums", "Lyrics"},
			content:    "作者 original song Lyrics",
			want:       candidateVerified,
		},
		{
			name:       "ordinary song text may mention album",
			categories: []string{"Lyrics", "Songs"},
			content:    "作者 original song Lyrics. This song later appeared on an album.",
			want:       candidateVerified,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := candidateVerification(identity, "新曲", test.content, test.categories); got != test.want {
				t.Fatalf("candidate verification = %d, want %d", got, test.want)
			}
		})
	}
}

func TestSearchWithAlbumAndSongReturnsOnlySong(t *testing.T) {
	tests := []struct {
		name          string
		albumCategory string
	}{
		{name: "canonical plural category", albumCategory: "Albums"},
		{name: "canonical singular category", albumCategory: "Album"},
		{name: "case insensitive category", albumCategory: "aLbUmS"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeEntityRulesPages(w, []entityRulesPage{
					{
						pageID: 1, revisionID: 11, title: "新曲",
						categories: []string{"Lyrics", test.albumCategory}, content: "作者 original song Lyrics",
					},
					{
						pageID: 2, revisionID: 22, title: "新曲",
						categories: []string{"Lyrics", "Songs"}, content: "作者 original song Lyrics",
					},
				})
			}))
			defer server.Close()

			candidates, err := newTestClient(server.URL).Search(context.Background(), MusicIdentity{
				JapaneseTitle: "新曲", ProducerMetadata: "作者",
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(candidates) != 1 || candidates[0].PageID != 2 {
				t.Fatalf("candidates = %+v, want only song page 2", candidates)
			}
		})
	}
}

func TestCreatorAliasFallbackCannotReadmitAlbum(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("gsrsearch") {
		case "新曲", `"新曲"`:
			content := "{{Song box 2\n|lyrics=[[CanonicalP]]\n|music=[[CanonicalP]]\n}}\noriginal song Lyrics"
			writeEntityRulesPages(w, []entityRulesPage{
				{pageID: 1, revisionID: 11, title: "新曲", categories: []string{"Albums", "Lyrics"}, content: content},
				{pageID: 2, revisionID: 22, title: "新曲", categories: []string{"Lyrics", "Songs"}, content: content},
			})
		case "別名P":
			writeEntityRulesPages(w, []entityRulesPage{
				{pageID: 3, revisionID: 33, title: "CanonicalP", categories: []string{"Vocaloid producers"}, content: "|japanese=別名P"},
			})
		default:
			http.Error(w, "unexpected search", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	candidates, diagnostics, err := newTestClient(server.URL).SearchWithDiagnostics(context.Background(), MusicIdentity{
		JapaneseTitle: "新曲", Lyricist: "別名P", Composer: "別名P",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].PageID != 2 {
		t.Fatalf("alias candidates = %+v, want only song page 2", candidates)
	}
	if diagnostics.SignalMismatch != 1 || diagnostics.CreditMismatch != 0 || diagnostics.Verified != 1 {
		t.Fatalf("alias diagnostics = %+v", diagnostics)
	}
}

func TestCreatorAliasIntroductionFixtureFlowsThroughSearch(t *testing.T) {
	fixture := loadIdentityEntityRulesFixture(t)
	producer := fixture.ProducerAliases[0]
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("gsrsearch") {
		case "新曲", `"新曲"`:
			writeEntityRulesPages(w, []entityRulesPage{{
				pageID: 1, revisionID: 11, title: "新曲", categories: []string{"Japanese songs", "Original songs"},
				content: "{{Song box 2\n|title='''新曲'''\n|producers=[[MikitoP]] (music, lyrics)\n}}",
			}})
		case producer.Creator:
			writeEntityRulesPages(w, []entityRulesPage{{
				pageID: 2, revisionID: 22, title: producer.PageTitle, categories: producer.Categories, content: producer.Content,
			}})
		default:
			http.Error(w, "unexpected search", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	candidates, _, err := newTestClient(server.URL).SearchWithDiagnostics(context.Background(), MusicIdentity{
		JapaneseTitle: "新曲", Lyricist: producer.Creator, Composer: producer.Creator,
	})
	if err != nil || len(candidates) != 1 || candidates[0].PageID != 1 {
		t.Fatalf("fixture-backed alias search candidates=%+v err=%v", candidates, err)
	}
}

func TestFetchFixedRevisionRejectsAlbumBeforeParser(t *testing.T) {
	tests := []struct {
		name     string
		category string
	}{
		{name: "canonical plural category", category: "Albums"},
		{name: "canonical singular category", category: "Album"},
		{name: "case insensitive category", category: "ALBUMS"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			content := "作者 original song Lyrics but deliberately no lyrics section"
			sha1 := sha1Hex(content)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("revids") != "34" {
					http.Error(w, "not an exact revision query", http.StatusBadRequest)
					return
				}
				writeEntityRulesPages(w, []entityRulesPage{
					{
						pageID: 12, revisionID: 34, sha1: sha1, title: "新曲", categories: []string{"Lyrics", test.category},
						content: content,
					},
				})
			}))
			defer server.Close()

			_, err := newTestClient(server.URL).FetchFixedRevision(context.Background(), MusicIdentity{
				JapaneseTitle: "新曲", ProducerMetadata: "作者",
			}, 12, 34, sha1)
			if !errors.Is(err, ErrAmbiguous) {
				t.Fatalf("fixed revision error = %v, want entity rejection before parser", err)
			}
		})
	}
}

func TestCandidateTitleTrailingParentheticalPermutations(t *testing.T) {
	wanted := "光"
	for _, test := range []struct {
		name  string
		title string
		want  bool
	}{
		{name: "exact title", title: "光/Mizuno Atsu", want: true},
		{name: "basic romanization", title: "光 (Hikari)/Mizuno Atsu", want: true},
		{name: "multiword romanization", title: "光 (Hikari no Uta)/Mizuno Atsu", want: true},
		{name: "Unicode Latin and punctuation", title: "光 (Lumière = Hikári × 2!?, A/B)/Mizuno Atsu", want: true},
		{name: "fullwidth delimiters", title: "光（Lumière＝Hikári×2！？）／Mizuno Atsu", want: true},
		{name: "balanced nested parenthetical", title: "光 (Hikari (Acoustic!))/Mizuno Atsu", want: true},
		{name: "no creator suffix", title: "光 (Hikári?)", want: true},
		{name: "mixed-script alternate title", title: "光 (Hikari 光)/Mizuno Atsu", want: true},
		{name: "Cyrillic alternate title", title: "光 (Хикари)/Mizuno Atsu", want: true},
		{name: "punctuation without Latin", title: "光 (= × !?, /)/Mizuno Atsu", want: false},
		{name: "unbalanced open", title: "光 (Hikari/Mizuno Atsu", want: false},
		{name: "unbalanced close", title: "光 Hikari)/Mizuno Atsu", want: false},
		{name: "nontrailing parenthetical", title: "光 (Hikari) extra/Mizuno Atsu", want: false},
		{name: "different base title", title: "光る (Hikari)/Mizuno Atsu", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := candidateTitleMatches(test.title, wanted); got != test.want {
				t.Fatalf("candidateTitleMatches(%q) = %t, want %t", test.title, got, test.want)
			}
		})
	}
}

func TestCandidateTitleRejectsReservedEntityAndVersionDisambiguators(t *testing.T) {
	wanted := "光"
	for _, suffix := range []string{
		"album", "Album", "single", "SINGLE", "EP", "ep", "cover", "Hikari Cover", "remix", "Hikari Remix",
		"reloaded", "Rerec", "Re:REC", "reunion", "10th Anniversary", "Game Size", "game-size", "short",
		"preview", "partial", "medley", "version", "Version 2", "ver.", "Hikari ver.2", "Hikari Ver2",
	} {
		t.Run(suffix, func(t *testing.T) {
			title := "光 (" + suffix + ")/Mizuno Atsu"
			if candidateTitleMatches(title, wanted) {
				t.Fatalf("reserved disambiguator was accepted: %q", title)
			}
		})
	}
}

func TestRecallTitleNormalizationAndNonLatinAlternateTitles(t *testing.T) {
	for _, test := range []struct {
		name      string
		pageTitle string
		wanted    string
		want      bool
	}{
		{name: "smart punctuation and CJK spacing", pageTitle: "君の 声 ～ 君's 歌", wanted: "君の声〜君’s歌", want: true},
		{name: "ellipsis and middle-dot typography", pageTitle: "夢...声 · 歌", wanted: "夢…声・歌", want: true},
		{name: "variation selector typography", pageTitle: "キラピピ★️キラピカ", wanted: "キラピピ★キラピカ", want: true},
		{name: "Chinese alternate title", pageTitle: "光 (光芒)/Mizuno Atsu", wanted: "光", want: true},
		{name: "mixed-script alternate title", pageTitle: "光 (Hikari 光)/Mizuno Atsu", wanted: "光", want: true},
		{name: "unrelated one-character Japanese title", pageTitle: "灯/Mizuno Atsu", wanted: "光", want: false},
		{name: "one-character parenthetical is not a well-formed alternate", pageTitle: "光 (灯)/Mizuno Atsu", wanted: "光", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := candidateTitleMatches(test.pageTitle, test.wanted); got != test.want {
				t.Fatalf("candidateTitleMatches(%q, %q) = %t, want %t", test.pageTitle, test.wanted, got, test.want)
			}
		})
	}
}

func TestRecallTitleRejectsReservedDerivativeDisambiguatorsAcrossLanguages(t *testing.T) {
	for _, suffix := range []string{
		"アルバム版", "シングル版", "カバー", "リミックス", "再録", "10周年記念", "ゲームサイズ", "ショート版",
		"プレビュー", "一部", "メドレー", "別バージョン",
		"专辑版", "單曲版", "翻唱版", "混音版", "重新錄製", "十周年紀念版", "遊戲版", "短版本",
		"預覽版", "部分", "組曲版", "其他版本",
	} {
		t.Run(suffix, func(t *testing.T) {
			if candidateTitleMatches("光 ("+suffix+")/Mizuno Atsu", "光") {
				t.Fatalf("reserved multilingual derivative disambiguator was accepted: %q", suffix)
			}
		})
	}
}

func TestMissingRoleRecallRequiresAnExactRemainingRoleAndRejectsContradictions(t *testing.T) {
	identity := MusicIdentity{
		JapaneseTitle: "新曲", Lyricist: "作詞者", Composer: "作曲者", Arranger: "編曲者",
	}
	for name, content := range map[string]string{
		"one missing role":      "Lyrics: 作詞者\nArrangement: 編曲者",
		"two missing roles":     "Music: 作曲者",
		"missing arranger only": "Lyrics: 作詞者\nMusic: 作曲者",
	} {
		t.Run(name, func(t *testing.T) {
			if !verifyCandidate(identity, "新曲", content, []string{"Songs"}) {
				t.Fatal("candidate with exact remaining authoritative role evidence was rejected")
			}
		})
	}
	for name, content := range map[string]string{
		"all roles missing":               "ordinary song metadata",
		"contradictory lyricist":          "Lyrics: 別人\nMusic: 作曲者",
		"contradictory composer":          "Lyrics: 作詞者\nMusic: 別人",
		"extra explicit role contributor": "Lyrics: 作詞者 & 別人\nMusic: 作曲者",
	} {
		t.Run(name, func(t *testing.T) {
			if verifyCandidate(identity, "新曲", content, []string{"Songs"}) {
				t.Fatal("candidate without exact non-contradictory role corroboration was accepted")
			}
		})
	}
}

func TestTitleQueryVariantsAreBoundedLazyAndPreserveEvidenceAndFixedIdentity(t *testing.T) {
	const (
		pageID     = 12
		revisionID = 34
	)
	content := "作者 original song Lyrics\n== Lyrics ==\n歌う"
	sha := sha1Hex(content)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		query := r.URL.Query()
		if revision := query.Get("revids"); revision != "" {
			if revision != strconv.Itoa(revisionID) {
				http.Error(w, "unexpected revision", http.StatusBadRequest)
				return
			}
			writeEntityRulesPages(w, []entityRulesPage{{
				pageID: pageID, revisionID: revisionID, sha1: sha, title: "光 ～ 声! (光之聲)", categories: []string{"Songs"}, content: content,
			}})
			return
		}
		if query.Get("gsrlimit") != strconv.Itoa(maxSearchPages) || query.Has("gsroffset") {
			http.Error(w, "unbounded title query", http.StatusBadRequest)
			return
		}
		if query.Get("gsrsearch") == "光~声!" {
			writeEntityRulesPages(w, []entityRulesPage{{
				pageID: pageID, revisionID: revisionID, sha1: sha, title: "光 ～ 声! (光之聲)", categories: []string{"Songs"}, content: content,
			}})
			return
		}
		writeEntityRulesPages(w, nil)
	}))
	defer server.Close()

	identity := MusicIdentity{JapaneseTitle: "光　〜　声！", ProducerMetadata: "作者"}
	client := newTestClient(server.URL)
	candidates, diagnostics, err := client.SearchWithDiagnostics(context.Background(), identity)
	if err != nil || len(candidates) != 1 || diagnostics.Verified != 1 {
		t.Fatalf("variant candidates=%+v diagnostics=%+v err=%v", candidates, diagnostics, err)
	}
	if requests.Load() != 3 {
		t.Fatalf("title variant requests=%d, want raw, quoted raw, then first successful normalized variant", requests.Load())
	}
	candidate := candidates[0]
	if len(candidate.IndexEvidenceRefs) != 1 || len(candidate.IndexEvidence) != 1 || ValidateCandidateIndexEvidence(candidate) != nil {
		t.Fatalf("variant candidate evidence=%+v refs=%+v", candidate.IndexEvidence, candidate.IndexEvidenceRefs)
	}
	mutatedEvidence := candidate
	mutatedEvidence.IndexEvidence = cloneIndexEvidence(candidate.IndexEvidence)
	mutatedEvidence.IndexEvidence[0].Raw[0] ^= 0xff
	if ValidateCandidateIndexEvidence(mutatedEvidence) == nil {
		t.Fatal("title recall accepted mutated raw index evidence")
	}
	if _, err := client.FetchFixedRevision(context.Background(), identity, candidate.PageID, candidate.RevisionID, strings.Repeat("a", 40)); !errors.Is(err, ErrRevisionChanged) {
		t.Fatalf("title recall fixed SHA drift error=%v", err)
	}
}

func TestTitleQueryRecallDoesNotBypassLyricTextRestrictions(t *testing.T) {
	content := "Do not repost these lyrics.\n== Lyrics ==\n歌う"
	sha := sha1Hex(content)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Query().Get("gsrsearch") == "光~声!" {
			writeEntityRulesPages(w, []entityRulesPage{{
				pageID: 12, revisionID: 34, sha1: sha, title: "光 ～ 声! (光之聲)", categories: []string{"Songs"}, content: content,
			}})
			return
		}
		writeEntityRulesPages(w, nil)
	}))
	defer server.Close()

	candidates, diagnostics, err := newTestClient(server.URL).SearchWithDiagnostics(context.Background(), MusicIdentity{
		JapaneseTitle: "光　〜　声！", ProducerMetadata: "作者",
	})
	if err != nil || len(candidates) != 0 || diagnostics.Restricted != 1 || diagnostics.RestrictedTitleMatch != 1 {
		t.Fatalf("restricted variant candidates=%+v diagnostics=%+v err=%v", candidates, diagnostics, err)
	}
	if requests.Load() > maxTitleSearchQueries {
		t.Fatalf("restricted variant requests=%d, bound=%d", requests.Load(), maxTitleSearchQueries)
	}
}

func TestSplitTopLevelContributorsUsesSafeCatalogBoundaries(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		want  []string
		ok    bool
	}{
		{name: "ASCII ampersand", value: "Giga & Mitchie M", want: []string{"Giga", "Mitchie M"}, ok: true},
		{name: "fullwidth ampersand", value: "Giga＆Mitchie M", want: []string{"Giga", "Mitchie M"}, ok: true},
		{name: "fullwidth slash", value: "Giga／Mitchie M", want: []string{"Giga", "Mitchie M"}, ok: true},
		{name: "ideographic comma", value: "Giga、Mitchie M", want: []string{"Giga", "Mitchie M"}, ok: true},
		{name: "nested delimiter stays in contributor", value: "nyanyannya (Team & Alias) & Giga", want: []string{"nyanyannya (Team & Alias)", "Giga"}, ok: true},
		{name: "Japanese brackets stay in contributor", value: "作者【別名＆共同名】＆共同制作者", want: []string{"作者【別名&共同名】", "共同制作者"}, ok: true},
		{name: "trailing delimiter", value: "Giga &", ok: false},
		{name: "duplicate normalized contributor is deduplicated", value: "Giga＆Ｇｉｇａ", want: []string{"Giga"}, ok: true},
		{name: "unbalanced group", value: "Giga (Team & Mitchie M", ok: false},
		{name: "embedded control", value: "Giga\nMitchie M", ok: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := splitTopLevelContributors(test.value)
			if ok != test.ok {
				t.Fatalf("split ok = %t, want %t; contributors=%q", ok, test.ok, got)
			}
			if !test.ok {
				return
			}
			if len(got) != len(test.want) {
				t.Fatalf("contributors = %q, want %q", got, test.want)
			}
			for index := range got {
				if got[index] != test.want[index] {
					t.Fatalf("contributors = %q, want %q", got, test.want)
				}
			}
		})
	}
}

func TestRoleBoundCatalogCreatorSetsRequireEveryContributorInTheCorrectRole(t *testing.T) {
	identity := MusicIdentity{
		JapaneseTitle: "新曲", Lyricist: "Giga & Mitchie M", Composer: "Giga＆Mitchie M",
	}
	if !verifyCandidate(identity, "新曲", "Lyrics: [[Giga]]＆[[Mitchie M]]\nMusic: Mitchie M / Giga", []string{"Songs"}) {
		t.Fatal("top-level contributor set with exact role-bound credits was rejected")
	}
	if verifyCandidate(identity, "新曲", "Lyrics: Giga\nMusic: Giga & Mitchie M", []string{"Songs"}) {
		t.Fatal("candidate missing one lyricist contributor was accepted")
	}
	if verifyCandidate(identity, "新曲", "Lyrics: Giga\nMusic: Mitchie M", []string{"Songs"}) {
		t.Fatal("contributors split across conflicting roles were accepted")
	}
	nested := MusicIdentity{JapaneseTitle: "新曲", Lyricist: "nyanyannya (Team & Alias) & Giga", Composer: "Giga"}
	if !verifyCandidate(nested, "新曲", "Lyrics: nyanyannya (Team & Alias)＆Giga\nMusic: Giga", []string{"Songs"}) {
		t.Fatal("separator inside a contributor annotation was treated as a top-level contributor boundary")
	}
}

func TestCreatorAliasEvidenceRequiresExactOppositeScriptProducerIdentity(t *testing.T) {
	base := wikiPage{title: "Giga", categories: []string{"Vocaloid producers"}, content: "|japanese=ギガ"}
	if !producerAliasPageMatches(base, "ギガ") {
		t.Fatal("exact Japanese-to-Latin producer alias evidence was rejected")
	}
	reverse := wikiPage{title: "ギガ", categories: []string{"Vocaloid producers"}, content: "|romaji=Giga"}
	if !producerAliasPageMatches(reverse, "Giga") {
		t.Fatal("exact Latin-to-Japanese producer alias evidence was rejected")
	}
	for name, page := range map[string]wikiPage{
		"same-script rename":        {title: "OtherP", categories: []string{"Vocaloid producers"}, content: "|alias=Giga"},
		"substring mention":         {title: "ギガ", categories: []string{"Vocaloid producers"}, content: "|romaji=SuperGiga"},
		"unlabelled lead mention":   {title: "Giga", categories: []string{"Vocaloid producers"}, content: "This producer collaborated with ギガ."},
		"later-section mention":     {title: "Giga", categories: []string{"Vocaloid producers"}, content: "lead\n== Collaborations ==\nギガ"},
		"contributor-set page":      {title: "Giga & Mitchie M", categories: []string{"Vocaloid producers"}, content: "|japanese=ギガ"},
		"missing producer category": {title: "Giga", categories: []string{"People"}, content: "|japanese=ギガ"},
	} {
		t.Run(name, func(t *testing.T) {
			creator := "ギガ"
			if name == "same-script rename" || name == "substring mention" {
				creator = "Giga"
			}
			if producerAliasPageMatches(page, creator) {
				t.Fatalf("unsafe alias page matched: %+v", page)
			}
		})
	}
}

func TestCreatorAliasFallbackCorroboratesContributorSetsInBothDirections(t *testing.T) {
	for _, test := range []struct {
		name          string
		catalogCredit string
		wikiCredit    string
		aliases       map[string]entityRulesPage
	}{
		{
			name: "Japanese catalog to Latin Wiki", catalogCredit: "ギガ＆ミッチーM", wikiCredit: "Giga & Mitchie M",
			aliases: map[string]entityRulesPage{
				"ギガ":    {pageID: 41, revisionID: 51, title: "Giga", categories: []string{"Vocaloid producers"}, content: "|japanese=ギガ"},
				"ミッチーM": {pageID: 42, revisionID: 52, title: "Mitchie M", categories: []string{"Vocaloid producers"}, content: "|japanese=ミッチーM"},
			},
		},
		{
			name: "Latin catalog to Japanese Wiki", catalogCredit: "Giga & Mitchie M", wikiCredit: "ギガ＆ミッチーM",
			aliases: map[string]entityRulesPage{
				"Giga":      {pageID: 43, revisionID: 53, title: "ギガ", categories: []string{"Vocaloid producers"}, content: "|romaji=Giga"},
				"Mitchie M": {pageID: 44, revisionID: 54, title: "ミッチーM", categories: []string{"Vocaloid producers"}, content: "|romaji=Mitchie M"},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			contributors, ok := splitTopLevelContributors(test.catalogCredit)
			if !ok {
				t.Fatalf("catalog contributors did not split: %q", test.catalogCredit)
			}
			directAliases := map[string]string{}
			for _, contributor := range contributors {
				page := test.aliases[contributor]
				if !producerAliasPageMatches(wikiPage{title: page.title, categories: page.categories, content: page.content}, contributor) {
					t.Fatalf("alias page did not corroborate contributor %q: %+v", contributor, page)
				}
				directAliases[creditAliasKey(creditRoleLyricist, contributor)] = page.title
				directAliases[creditAliasKey(creditRoleComposer, contributor)] = page.title
			}
			content := "Lyrics: " + test.wikiCredit + "\nMusic: " + test.wikiCredit + "\noriginal song Lyrics"
			identity := MusicIdentity{JapaneseTitle: "新曲", Lyricist: test.catalogCredit, Composer: test.catalogCredit}
			if !roleBoundCreditsMatchWithAliases(identity, content, directAliases) {
				t.Fatalf("direct alias role corroboration failed: contributors=%q aliases=%+v", contributors, directAliases)
			}

			var aliasRequests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				query := r.URL.Query().Get("gsrsearch")
				switch query {
				case "新曲":
					content := "Lyrics: " + test.wikiCredit + "\nMusic: " + test.wikiCredit + "\noriginal song Lyrics"
					writeEntityRulesPages(w, []entityRulesPage{{pageID: 1, revisionID: 11, title: "新曲", categories: []string{"Lyrics", "Songs"}, content: content}})
				case `"新曲"`:
					writeEntityRulesPages(w, nil)
				default:
					page, ok := test.aliases[query]
					if !ok {
						http.Error(w, "unexpected search", http.StatusBadRequest)
						return
					}
					aliasRequests.Add(1)
					writeEntityRulesPages(w, []entityRulesPage{page})
				}
			}))
			defer server.Close()

			candidates, diagnostics, err := newTestClient(server.URL).SearchWithDiagnostics(context.Background(), identity)
			if err != nil || len(candidates) != 1 || candidates[0].PageID != 1 {
				t.Fatalf("candidates=%+v diagnostics=%+v aliasRequests=%d err=%v", candidates, diagnostics, aliasRequests.Load(), err)
			}
			if aliasRequests.Load() != 2 {
				t.Fatalf("alias requests=%d, want one lookup per unique contributor", aliasRequests.Load())
			}
		})
	}
}

func TestCreatorAliasFallbackResolvesEachPageIndependently(t *testing.T) {
	var firstAliasRequests, secondAliasRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("gsrsearch") {
		case "共同制作曲", `"共同制作曲"`:
			writeEntityRulesPages(w, []entityRulesPage{
				{
					pageID: 1, revisionID: 11, title: "共同制作曲", categories: []string{"Songs"},
					content: "Lyrics: Unknown A & 作者乙\nMusic: Unknown A & 作者乙",
				},
				{
					pageID: 2, revisionID: 22, title: "共同制作曲", categories: []string{"Songs"},
					content: "Lyrics: 作者甲 & Creator B\nMusic: 作者甲 & Creator B",
				},
			})
		case "作者甲":
			firstAliasRequests.Add(1)
			writeEntityRulesPages(w, nil)
		case "作者乙":
			secondAliasRequests.Add(1)
			writeEntityRulesPages(w, []entityRulesPage{{
				pageID: 3, revisionID: 33, title: "Creator B", categories: []string{"Vocaloid producers"}, content: "|japanese=作者乙",
			}})
		default:
			http.Error(w, "unexpected search", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	candidates, diagnostics, err := newTestClient(server.URL).SearchWithDiagnostics(context.Background(), MusicIdentity{
		JapaneseTitle: "共同制作曲", Lyricist: "作者甲 & 作者乙", Composer: "作者甲 & 作者乙",
	})
	if err != nil || len(candidates) != 1 || candidates[0].PageID != 2 {
		t.Fatalf("candidates=%+v diagnostics=%+v err=%v", candidates, diagnostics, err)
	}
	if diagnostics.SearchHits != 2 || diagnostics.CreditMismatch != 1 || diagnostics.Verified != 1 ||
		diagnostics.SearchHits != diagnostics.Restricted+diagnostics.TitleMismatch+diagnostics.CreditMismatch+diagnostics.SignalMismatch+diagnostics.Verified {
		t.Fatalf("partitioned diagnostics=%+v", diagnostics)
	}
	if firstAliasRequests.Load() != 1 || secondAliasRequests.Load() != 1 {
		t.Fatalf("alias requests first=%d second=%d, want one bounded lookup each", firstAliasRequests.Load(), secondAliasRequests.Load())
	}
}

func TestCreatorAliasRequiresOneUniqueProducerPage(t *testing.T) {
	var aliasRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("gsrsearch") {
		case "新曲", `"新曲"`:
			writeEntityRulesPages(w, []entityRulesPage{{
				pageID: 1, revisionID: 11, title: "新曲", categories: []string{"Songs"},
				content: "Lyrics: Creator A\nMusic: Creator A",
			}})
		case "作者甲":
			aliasRequests.Add(1)
			writeEntityRulesPages(w, []entityRulesPage{
				{pageID: 2, revisionID: 22, title: "Creator A", categories: []string{"Vocaloid producers"}, content: "|japanese=作者甲"},
				{pageID: 3, revisionID: 33, title: "Creator B", categories: []string{"Vocaloid producers"}, content: "|japanese=作者甲"},
			})
		default:
			http.Error(w, "unexpected search", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	candidates, diagnostics, err := newTestClient(server.URL).SearchWithDiagnostics(context.Background(), MusicIdentity{
		JapaneseTitle: "新曲", Lyricist: "作者甲", Composer: "作者甲",
	})
	if err != nil || len(candidates) != 0 || diagnostics.CreditMismatch != 1 || diagnostics.Verified != 0 {
		t.Fatalf("candidates=%+v diagnostics=%+v err=%v", candidates, diagnostics, err)
	}
	if aliasRequests.Load() != 1 {
		t.Fatalf("alias requests=%d, want exactly one lookup", aliasRequests.Load())
	}
}

func TestCreatorAliasResponseParsingRemainsSeparatelyBounded(t *testing.T) {
	pages := make([]entityRulesPage, maxCreatorAliasPages+1)
	for index := range pages {
		pages[index] = entityRulesPage{
			pageID: index + 1, revisionID: index + 11, title: fmt.Sprintf("Creator %d", index+1),
			categories: []string{"Vocaloid producers"}, content: "|japanese=作者",
		}
	}
	recorder := httptest.NewRecorder()
	writeEntityRulesPages(recorder, pages)
	if _, err := parseSearchResponseWithLimit(recorder.Body.Bytes(), maxCreatorAliasPages); !errors.Is(err, ErrMalformedResponse) {
		t.Fatalf("creator alias response over %d pages error=%v", maxCreatorAliasPages, err)
	}
	if parsed, err := parseSearchResponse(recorder.Body.Bytes()); err != nil || len(parsed) != len(pages) {
		t.Fatalf("ordinary search coverage parsed=%d err=%v", len(parsed), err)
	}
}

func TestCreatorAliasFallbackIsGloballyBounded(t *testing.T) {
	catalogContributors := make([]string, maxCreatorAliasLookups+1)
	pages := make([]entityRulesPage, len(catalogContributors))
	for index := range catalogContributors {
		catalogContributors[index] = fmt.Sprintf("作者%02d", index+1)
	}
	for index := range pages {
		actual := append([]string{}, catalogContributors...)
		actual[index] = fmt.Sprintf("Unknown %02d", index+1)
		pages[index] = entityRulesPage{
			pageID: index + 1, revisionID: (index + 1) * 11, title: "共同制作曲", categories: []string{"Songs"},
			content: "Lyrics: " + strings.Join(actual, " & "),
		}
	}

	var aliasRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("gsrsearch")
		switch {
		case query == "共同制作曲" || query == `"共同制作曲"`:
			writeEntityRulesPages(w, pages)
		case strings.HasPrefix(query, "作者"):
			aliasRequests.Add(1)
			writeEntityRulesPages(w, nil)
		default:
			http.Error(w, "unexpected search", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	candidates, diagnostics, err := newTestClient(server.URL).SearchWithDiagnostics(context.Background(), MusicIdentity{
		JapaneseTitle: "共同制作曲", Lyricist: strings.Join(catalogContributors, " & "),
	})
	if err != nil || len(candidates) != 0 || diagnostics.SearchHits != len(pages) || diagnostics.CreditMismatch != len(pages) {
		t.Fatalf("candidates=%+v diagnostics=%+v err=%v", candidates, diagnostics, err)
	}
	if aliasRequests.Load() != maxCreatorAliasLookups {
		t.Fatalf("alias requests=%d, want global bound %d", aliasRequests.Load(), maxCreatorAliasLookups)
	}
}

func TestSearchDiagnosticsMergePageRevisionsAndPartitionFinalOutcomes(t *testing.T) {
	var searchRequests, aliasRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("gsrsearch") {
		case "新曲":
			searchRequests.Add(1)
			writeEntityRulesPages(w, []entityRulesPage{
				{
					pageID: 1, revisionID: 11, title: "新曲", categories: []string{"Songs"},
					content: "Lyrics: 作者甲\nMusic: 作者甲\nDo not repost these lyrics.",
				},
				{
					pageID: 2, revisionID: 22, title: "新曲", categories: []string{"Songs"},
					content: "Lyrics: Creator A\nMusic: Creator A",
				},
				{
					pageID: 3, revisionID: 33, title: "別の曲", categories: []string{"Songs"},
					content: "Lyrics: 作者甲\nMusic: 作者甲",
				},
			})
		case `"新曲"`:
			searchRequests.Add(1)
			writeEntityRulesPages(w, []entityRulesPage{
				{
					pageID: 1, revisionID: 11, title: "新曲", categories: []string{"Songs"},
					content: "Lyrics: 作者甲\nMusic: 作者甲",
				},
				{
					pageID: 2, revisionID: 22, title: "新曲", categories: []string{"Songs"},
					content: "Lyrics: Creator A\nMusic: Creator A",
				},
				{
					pageID: 3, revisionID: 33, title: "新曲", categories: []string{"Songs"},
					content: "Lyrics: 作者甲\nMusic: 作者甲",
				},
			})
		case "作者甲":
			aliasRequests.Add(1)
			writeEntityRulesPages(w, []entityRulesPage{{
				pageID: 4, revisionID: 44, title: "Creator A", categories: []string{"Vocaloid producers"}, content: "|japanese=作者甲",
			}})
		default:
			http.Error(w, "unexpected search", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	candidates, diagnostics, err := newTestClient(server.URL).SearchWithDiagnostics(context.Background(), MusicIdentity{
		JapaneseTitle: "新曲", Lyricist: "作者甲", Composer: "作者甲",
	})
	if err != nil || len(candidates) != 1 || candidates[0].PageID != 2 {
		t.Fatalf("candidates=%+v diagnostics=%+v err=%v", candidates, diagnostics, err)
	}
	want := SearchDiagnostics{SearchHits: 3, Restricted: 1, RestrictedTitleMatch: 1, TitleMismatch: 1, Verified: 1}
	if diagnostics != want {
		t.Fatalf("diagnostics=%+v, want %+v", diagnostics, want)
	}
	if diagnostics.SearchHits != diagnostics.Restricted+diagnostics.TitleMismatch+diagnostics.CreditMismatch+diagnostics.SignalMismatch+diagnostics.Verified {
		t.Fatalf("diagnostics do not partition final page outcomes: %+v", diagnostics)
	}
	if searchRequests.Load() != 2 || aliasRequests.Load() != 1 {
		t.Fatalf("search requests=%d alias requests=%d, want two search phases and one alias phase", searchRequests.Load(), aliasRequests.Load())
	}
}

func TestCreatorAliasesCannotFillContributorsFromTheWrongRole(t *testing.T) {
	identity := MusicIdentity{Lyricist: "ギガ & ミッチーM", Composer: "ギガ"}
	aliases := map[string]string{
		creditAliasKey(creditRoleLyricist, "ギガ"):    "Giga",
		creditAliasKey(creditRoleLyricist, "ミッチーM"): "Mitchie M",
		creditAliasKey(creditRoleComposer, "ギガ"):    "Giga",
	}
	content := "Lyrics: Giga\nMusic: Giga & Mitchie M\noriginal song Lyrics"
	if roleBoundCreditsMatchWithAliases(identity, content, aliases) {
		t.Fatal("alias evidence from the composer role filled a missing lyricist contributor")
	}
}

func TestCreatorAliasFallbackCannotBypassLyricsRestriction(t *testing.T) {
	var aliasRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("gsrsearch") {
		case "新曲", `"新曲"`:
			content := "Lyrics: Giga\nMusic: Giga\noriginal song Lyrics\nDo not repost these lyrics."
			writeEntityRulesPages(w, []entityRulesPage{{pageID: 1, revisionID: 11, title: "新曲", categories: []string{"Lyrics"}, content: content}})
		default:
			aliasRequests.Add(1)
			writeEntityRulesPages(w, []entityRulesPage{{pageID: 2, revisionID: 22, title: "Giga", categories: []string{"Vocaloid producers"}, content: "|japanese=ギガ"}})
		}
	}))
	defer server.Close()

	candidates, diagnostics, err := newTestClient(server.URL).SearchWithDiagnostics(context.Background(), MusicIdentity{
		JapaneseTitle: "新曲", Lyricist: "ギガ", Composer: "ギガ",
	})
	if err != nil || len(candidates) != 0 || diagnostics.Restricted != 1 {
		t.Fatalf("candidates=%+v diagnostics=%+v err=%v", candidates, diagnostics, err)
	}
	if aliasRequests.Load() != 0 {
		t.Fatalf("restricted page triggered %d alias requests", aliasRequests.Load())
	}
}

func writeEntityRulesPages(w http.ResponseWriter, pages []entityRulesPage) {
	responsePages := make(map[string]any, len(pages))
	for _, page := range pages {
		categories := make([]map[string]string, 0, len(page.categories))
		for _, category := range page.categories {
			categories = append(categories, map[string]string{"title": "Category:" + category})
		}
		responseSHA1 := page.sha1
		if responseSHA1 == "" {
			responseSHA1 = entityRulesSHA1
		}
		responsePages[strconv.Itoa(page.pageID)] = map[string]any{
			"pageid":     page.pageID,
			"title":      page.title,
			"categories": categories,
			"revisions": []any{map[string]any{
				"revid": page.revisionID,
				"sha1":  responseSHA1,
				"slots": map[string]any{"main": map[string]any{"content": page.content}},
			}},
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"query": map[string]any{"pages": responsePages}})
}
