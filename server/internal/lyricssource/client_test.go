package lyricssource

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

func TestExtractLyricsPreservesOrderAndStanzas(t *testing.T) {
	lines, err := extractLyrics("intro\n== Lyrics ==\n歌う\n\n踊る\n== Other ==\nignored")
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0].Japanese != "歌う" || lines[0].StanzaBreakBefore ||
		lines[1].Japanese != "踊る" || !lines[1].StanzaBreakBefore {
		t.Fatalf("extracted lines = %+v", lines)
	}
}

func TestExtractPlainLyricsPreservesExplicitSharedEnglishAndRejectsUnknownTemplates(t *testing.T) {
	lines, err := extractLyrics("== Lyrics ==\n歌う\n{{Shared}}|Oh yeah\n踊る")
	if err != nil {
		t.Fatal(err)
	}
	want := []ExtractedLine{{Japanese: "歌う"}, {Japanese: "Oh yeah"}, {Japanese: "踊る"}}
	if !equalExtractedLines(lines, want) {
		t.Fatalf("plain shared lines = %+v, want %+v", lines, want)
	}
	for _, content := range []string{
		"== Lyrics ==\n{{ruby|歌|うた}}を歌う",
		"== Lyrics ==\n歌う{{unknown|payload}}",
	} {
		if _, err := extractLyrics(content); !errors.Is(err, ErrUnsupportedTable) {
			t.Fatalf("unknown plain template error = %v content=%q", err, content)
		}
	}
}

func TestExtractLyricsRejectsRestrictedAndAmbiguousMarkup(t *testing.T) {
	if _, err := extractLyrics("== Lyrics ==\n歌う\n無断転載禁止"); !errors.Is(err, ErrRestrictedReprint) {
		t.Fatalf("restricted source error = %v", err)
	}
	if _, err := extractLyrics("== Lyrics ==\n歌う\n{{No reprint}}"); !errors.Is(err, ErrRestrictedReprint) {
		t.Fatalf("No reprint template error = %v", err)
	}
	if !hasReprintRestriction("== Lyrics ==\n歌う", []string{"Songs with reprints prohibited"}) {
		t.Fatal("restriction category was not detected")
	}
	if _, err := extractLyrics("== Lyrics ==\n{|\n| 歌う\n|}\n{|\n| 踊る\n|}"); !errors.Is(err, ErrUnsupportedTable) {
		t.Fatalf("ambiguous table error = %v", err)
	}
}

func TestExtractLyricsTableSanitizesSafeMarkupAndRejectsAmbiguousCells(t *testing.T) {
	lines, err := extractLyrics("== Lyrics ==\n{|\n! Japanese\n! English\n|-\n| [[歌う|歌詞]]<ref>citation</ref>\n| English\n|-\n| 踊る\n|}")
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0].Japanese != "歌詞" || lines[1].Japanese != "踊る" {
		t.Fatalf("sanitized table lines = %+v", lines)
	}
	if _, err := extractLyrics("== Lyrics ==\n{|\n! Romaji\n! Original\n|-\n| Uta o utau || 歌を歌う\n|}"); !errors.Is(err, ErrUnsupportedTable) {
		t.Fatalf("second source column error = %v", err)
	}
	englishSource, err := extractLyrics("== Lyrics ==\n{|\n! Lyrics\n! Translation\n|-\n| {{Shared}}|Changing the world\n| 世界を変える\n|}")
	if err != nil || !equalExtractedLines(englishSource, []ExtractedLine{{Japanese: "Changing the world"}}) {
		t.Fatalf("explicit English source line = %+v err=%v", englishSource, err)
	}
	for _, content := range []string{
		"== Lyrics ==\n{|\n! Japanese\n|-\n| style=\"color:red\" | 歌う\n|}",
		"== Lyrics ==\n{|\n! Japanese\n|-\n| {{ruby|歌|うた}}\n|}",
		"== Lyrics ==\n{|\n! Japanese\n|-\n| {{Shared|unsafe}}|歌う\n|}",
		"== Lyrics ==\n{|\n! Japanese\n! English\n|-\n| 歌う\ncontinued source text\n| English\n|}",
		"== Lyrics ==\n{|\n! Japanese\n|+ 歌詞表\n|-\n| 歌う\n|}",
		"== Lyrics ==\n{|\n|-\n| 歌う\n|}",
		"== Lyrics ==\n{|\n! Romaji\n! Translation\n|-\n| utau\n| song\n|}",
		"== Lyrics ==\n{|\n! English lyrics\n! Translation\n|-\n| song\n| 歌\n|}",
		"== Lyrics ==\n{|\n! Japanese\n! Original\n|-\n| 歌う\n| song\n|}",
		"== Lyrics ==\n{|\n! Japanese\n! Romaji\n|-\n|\n| utau\n|}",
		"== Lyrics ==\n{|\n! Romaji\n! Japanese\n|-\n| utau\n|}",
	} {
		if _, err := extractLyrics(content); !errors.Is(err, ErrUnsupportedTable) {
			t.Fatalf("ambiguous table cell error = %v content=%q", err, content)
		}
	}
}

func TestExtractLyricsTableUsesFirstColumnAndOnlyBlankSourceRowsCreateStanzas(t *testing.T) {
	for name, content := range map[string]string{
		"NEO dual column and br": `== Lyrics ==
{|
! Japanese
! Romaji
|-
| 世界を変える
| Sekai o kaeru
|-
| <br />
|-
| 夢を見て || Yume o mite
|}`,
		"Journey tabber shared source": `== Lyrics ==
<tabber>
Japanese=
{|
! Japanese
! Translation
|-
| {{shared}} | Journey
| Journey
|-
| {{Shared}}|Oh yeah
| Oh yeah
|}
</tabber>`,
		"Worlders English shared line": `== Lyrics ==
{|
! Japanese
! Romaji
|-
|{{Shared}}|Changing the world
| Changing the world
|-
|{{Shared}}|Oh yeah
| Oh yeah
|-
| <br />
|-
| また歌う
| Mata utau
|}`,
	} {
		t.Run(name, func(t *testing.T) {
			lines, err := extractLyrics(content)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "NEO dual column and br":
				want := []ExtractedLine{{Japanese: "世界を変える"}, {Japanese: "夢を見て", StanzaBreakBefore: true}}
				if !equalExtractedLines(lines, want) {
					t.Fatalf("NEO lines = %+v, want %+v", lines, want)
				}
			case "Journey tabber shared source":
				want := []ExtractedLine{{Japanese: "Journey"}, {Japanese: "Oh yeah"}}
				if !equalExtractedLines(lines, want) {
					t.Fatalf("Journey lines = %+v, want %+v", lines, want)
				}
			case "Worlders English shared line":
				want := []ExtractedLine{{Japanese: "Changing the world"}, {Japanese: "Oh yeah"}, {Japanese: "また歌う", StanzaBreakBefore: true}}
				if !equalExtractedLines(lines, want) {
					t.Fatalf("Worlders lines = %+v, want %+v", lines, want)
				}
			}
		})
	}
}

func TestExtractLyricsRealWikiFixtures(t *testing.T) {
	for _, test := range []struct {
		name          string
		file          string
		pageID        int
		revisionID    int
		fixtureSHA1   string
		sectionSHA256 string
		lineCount     int
		stanzaCount   int
		assert        func(*testing.T, []ExtractedLine)
	}{
		{
			name:          "NEO dual-column table with br stanzas",
			file:          "testdata/neo.wiki",
			pageID:        254205,
			revisionID:    1489471,
			fixtureSHA1:   "3a710840662568c75e29c979132e078de12ef0c9",
			sectionSHA256: "aef60b81cd503249d61eae5a914d3f322cb25039798e1c7ea93ae890d28d19d4",
			lineCount:     31,
			stanzaCount:   7,
			assert: func(t *testing.T, lines []ExtractedLine) {
				assertExtractedLine(t, lines, 0, "不完全な僕を生き写したような音色", false)
				assertExtractedLine(t, lines, 4, "時代がワープして君は置いてかれるから", true)
				assertExtractedLine(t, lines, 30, "「初めまして」は届いたかい NEO", false)
				assertNoExtractedLine(t, lines, "fukanzenna boku o ikiutsushita you na neiro")
			},
		},
		{
			name:          "Journey tabber with lowercase shared source",
			file:          "testdata/journey.wiki",
			pageID:        250312,
			revisionID:    1476888,
			fixtureSHA1:   "59ac8fed26f20ad93e57a1f70dccf4052170860a",
			sectionSHA256: "471dcdc11df95da66634da3f34b7e87551e8846d186d4da00d15c6ef80e0e3b4",
			lineCount:     43,
			stanzaCount:   12,
			assert: func(t *testing.T, lines []ExtractedLine) {
				assertExtractedLine(t, lines, 0, "Journey", false)
				assertExtractedLine(t, lines, 1, "溜めてきた　“なんとかなる”は", true)
				assertExtractedLine(t, lines, 42, "笑って生きていくためのJourney　奏でるMelody", false)
				assertNoExtractedLine(t, lines, "tamete kita “nantoka naru” wa")
				assertNoExtractedLine(t, lines, "My growing pile of, “It’ll figure itself out”")
			},
		},
		{
			name:          "Worlders pure-English shared source rows",
			file:          "testdata/worlders.wiki",
			pageID:        259687,
			revisionID:    1470244,
			fixtureSHA1:   "96af29e962edb95697d82cee8e4ce9b350c79ed0",
			sectionSHA256: "2e4e3b15128f5b7ca8c32618d8d2cda0f0503876949f568c848bc6d6e00440ed",
			lineCount:     90,
			stanzaCount:   34,
			assert: func(t *testing.T, lines []ExtractedLine) {
				assertExtractedLine(t, lines, 50, "Oh yeah", true)
				assertExtractedLine(t, lines, 51, "Changing the world", false)
				assertExtractedLine(t, lines, 57, "Let's keep singing", false)
				assertExtractedLine(t, lines, 66, "(Do, Re, Mi, Fa, So, La, Si, Do)", true)
				assertExtractedLine(t, lines, 89, "Let's keep singing", false)
				assertNoExtractedLine(t, lines, "kitto kimi mo")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.pageID <= 0 || test.revisionID <= 0 || !HasCanonicalSHA1(test.fixtureSHA1) || len(test.sectionSHA256) != 64 {
				t.Fatalf("invalid fixed fixture identity page=%d revision=%d sha1=%q sectionSha256=%q", test.pageID, test.revisionID, test.fixtureSHA1, test.sectionSHA256)
			}
			content, err := os.ReadFile(test.file)
			if err != nil {
				t.Fatal(err)
			}
			sectionHash := fmt.Sprintf("%x", sha256.Sum256(content))
			if sectionHash != test.sectionSHA256 {
				t.Fatalf("fixture section SHA-256 = %s, want %s", sectionHash, test.sectionSHA256)
			}
			lines, err := extractLyrics(string(content))
			if err != nil {
				t.Fatal(err)
			}
			if len(lines) != test.lineCount {
				t.Fatalf("line count = %d, want %d", len(lines), test.lineCount)
			}
			stanzaCount := 0
			for _, line := range lines {
				if line.StanzaBreakBefore {
					stanzaCount++
				}
			}
			if stanzaCount != test.stanzaCount {
				t.Fatalf("stanza count = %d, want %d", stanzaCount, test.stanzaCount)
			}
			test.assert(t, lines)
		})
	}
}

func assertExtractedLine(t *testing.T, lines []ExtractedLine, index int, japanese string, stanzaBreakBefore bool) {
	t.Helper()
	if index < 0 || index >= len(lines) {
		t.Fatalf("line index %d outside extracted line count %d", index, len(lines))
	}
	want := ExtractedLine{Japanese: japanese, StanzaBreakBefore: stanzaBreakBefore}
	if lines[index] != want {
		t.Fatalf("line %d = %+v, want %+v", index, lines[index], want)
	}
}

func assertNoExtractedLine(t *testing.T, lines []ExtractedLine, unwanted string) {
	t.Helper()
	for index, line := range lines {
		if line.Japanese == unwanted {
			t.Fatalf("unexpected non-source cell at line %d: %q", index, unwanted)
		}
	}
}

func equalExtractedLines(got, want []ExtractedLine) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range want {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func TestExtractLyricsAcceptsExactPreviewLimitBoundaries(t *testing.T) {
	t.Run("maximum line count", func(t *testing.T) {
		lines, err := extractLyrics("== Lyrics ==\n" + strings.Repeat("歌\n", maxExtractedLines))
		if err != nil {
			t.Fatal(err)
		}
		if len(lines) != maxExtractedLines {
			t.Fatalf("line count = %d, want %d", len(lines), maxExtractedLines)
		}
	})

	t.Run("maximum single-line bytes", func(t *testing.T) {
		line := strings.Repeat("歌", maxExtractedLineBytes/len("歌")) + strings.Repeat("a", maxExtractedLineBytes%len("歌"))
		lines, err := extractLyrics("== Lyrics ==\n" + line)
		if err != nil {
			t.Fatal(err)
		}
		if len(lines) != 1 || lines[0].Japanese != line || len(lines[0].Japanese) != maxExtractedLineBytes {
			t.Fatalf("boundary line = %+v, bytes=%d", lines, len(lines[0].Japanese))
		}
	})

	t.Run("maximum total extracted bytes", func(t *testing.T) {
		lines, err := extractLyrics(lyricsTableWithTotalBytes(maxExtractedTextBytes))
		if err != nil {
			t.Fatal(err)
		}
		totalBytes := 0
		for _, line := range lines {
			totalBytes += len(line.Japanese)
		}
		if totalBytes != maxExtractedTextBytes {
			t.Fatalf("total extracted bytes = %d, want %d", totalBytes, maxExtractedTextBytes)
		}
	})
}

func TestExtractLyricsRejectsValuesBeyondPreviewLimits(t *testing.T) {
	exactLine := strings.Repeat("歌", maxExtractedLineBytes/len("歌")) + strings.Repeat("a", maxExtractedLineBytes%len("歌"))
	for name, content := range map[string]string{
		"too many plain lines":        "== Lyrics ==\n" + strings.Repeat("歌\n", maxExtractedLines+1),
		"oversized plain line":        "== Lyrics ==\n" + exactLine + "a",
		"oversized total text":        lyricsTableWithTotalBytes(maxExtractedTextBytes + 1),
		"too many table rows":         "== Lyrics ==\n{|\n! Japanese\n" + strings.Repeat("|-\n|歌\n", maxExtractedLines+1) + "|}",
		"oversized table source cell": "== Lyrics ==\n{|\n! Japanese\n|-\n|" + strings.Repeat("a", maxExtractedLineBytes+1) + "\n|}",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := extractLyrics(content); !errors.Is(err, ErrLyricsTooLarge) {
				t.Fatalf("extract error = %v", err)
			}
		})
	}
}

func lyricsTableWithTotalBytes(totalBytes int) string {
	var table strings.Builder
	table.WriteString("== Lyrics ==\n{|\n")
	table.WriteString("! Japanese\n")
	for totalBytes > 0 {
		cellBytes := totalBytes
		if cellBytes > maxExtractedLineBytes {
			cellBytes = maxExtractedLineBytes
		}
		table.WriteString("|-\n|")
		table.WriteString(strings.Repeat("a", cellBytes))
		table.WriteByte('\n')
		totalBytes -= cellBytes
	}
	table.WriteString("|}")
	return table.String()
}

func TestCanonicalURLPreservesMediaWikiSubpageSeparators(t *testing.T) {
	if got, want := canonicalURL("Journey/DECO*27"), "https://vocaloid.fandom.com/wiki/Journey/DECO%2A27"; got != want {
		t.Fatalf("canonical URL = %q, want %q", got, want)
	}
	if got, want := canonicalURL("Song ?#% Title"), "https://vocaloid.fandom.com/wiki/Song_%3F%23%25_Title"; got != want {
		t.Fatalf("escaped canonical URL = %q, want %q", got, want)
	}
}

func TestPreviewReturnsCompleteCanonicalMediaWikiIdentity(t *testing.T) {
	const sha1 = "0123456789abcdef0123456789abcdef01234567"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if query.Get("action") != "query" || query.Get("prop") != "revisions|categories" || query.Get("revids") != "34" ||
			query.Get("rvprop") != "ids|sha1|content" || query.Get("rvslots") != "main" || query.Has("pageids") || query.Has("rvlimit") {
			http.Error(w, "not an exact fixed-revision query", http.StatusBadRequest)
			return
		}
		fmt.Fprintf(w, `{"query":{"pages":{"12":{"pageid":12,"title":"新曲/作者","categories":[{"title":"Category:Lyrics"}],"revisions":[{"revid":34,"sha1":%q,"slots":{"main":{"content":"作者 original song\n== Lyrics ==\n歌う\n\n踊る"}}}]}}}}`, sha1)
	}))
	defer server.Close()
	client := New()
	client.endpoint = server.URL
	preview, err := client.Preview(context.Background(), MusicIdentity{MusicID: 1, JapaneseTitle: "新曲", ProducerMetadata: "作者"}, 12, 34)
	if err != nil {
		t.Fatal(err)
	}
	if preview.PageID != 12 || preview.RevisionID != 34 || preview.SHA1 != sha1 || !mediaWikiSHA1Pattern.MatchString(preview.SHA1) ||
		preview.CanonicalURL != "https://vocaloid.fandom.com/wiki/%E6%96%B0%E6%9B%B2/%E4%BD%9C%E8%80%85" ||
		len(preview.Categories) != 1 || preview.Categories[0] != "Lyrics" ||
		!equalExtractedLines(preview.Lines, []ExtractedLine{{Japanese: "歌う"}, {Japanese: "踊る", StanzaBreakBefore: true}}) {
		t.Fatalf("preview identity = %+v", preview)
	}
}

func TestPreviewRejectsMalformedMediaWikiRevisionIdentity(t *testing.T) {
	for name, revision := range map[string]string{
		"missing revision id": `{"revid":0,"sha1":"0123456789abcdef0123456789abcdef01234567"}`,
		"missing sha1":        `{"revid":34,"sha1":""}`,
		"short sha1":          `{"revid":34,"sha1":"0123456789abcdef"}`,
		"uppercase sha1":      `{"revid":34,"sha1":"0123456789ABCDEF0123456789ABCDEF01234567"}`,
		"non-hex sha1":        `{"revid":34,"sha1":"gggggggggggggggggggggggggggggggggggggggg"}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprintf(w, `{"query":{"pages":{"12":{"pageid":12,"title":"新曲","categories":[{"title":"Category:Lyrics"}],"revisions":[%s]}}}}`, revision)
			}))
			defer server.Close()
			client := New()
			client.endpoint = server.URL
			_, err := client.Preview(context.Background(), MusicIdentity{MusicID: 1, JapaneseTitle: "新曲", ProducerMetadata: "作者"}, 12, 34)
			if !errors.Is(err, ErrMalformedResponse) {
				t.Fatalf("malformed identity error = %v", err)
			}
		})
	}
}

func TestPreviewRejectsRevisionFromAnotherPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"query":{"pages":{"999":{"pageid":999,"title":"新曲","categories":[{"title":"Category:Lyrics"}],"revisions":[{"revid":34,"sha1":"0123456789abcdef0123456789abcdef01234567","slots":{"main":{"content":"作者 original song\n== Lyrics ==\n歌う"}}}]}}}}`)
	}))
	defer server.Close()
	client := New()
	client.endpoint = server.URL
	_, err := client.Preview(context.Background(), MusicIdentity{MusicID: 1, JapaneseTitle: "新曲", ProducerMetadata: "作者"}, 12, 34)
	if !errors.Is(err, ErrRevisionChanged) {
		t.Fatalf("cross-page revision error = %v", err)
	}
}

func TestRequestRejectsCrossOriginRedirect(t *testing.T) {
	var targetRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetRequests.Add(1)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer source.Close()
	client := New()
	client.endpoint = source.URL
	_, err := client.Search(context.Background(), MusicIdentity{JapaneseTitle: "新曲", ProducerMetadata: "作者"})
	if err == nil || targetRequests.Load() != 0 {
		t.Fatalf("cross-origin redirect err=%v targetRequests=%d", err, targetRequests.Load())
	}
}

func TestCandidateRequiresTitleProducerAndSongSignal(t *testing.T) {
	identity := MusicIdentity{MusicID: 10, JapaneseTitle: "新曲", ProducerMetadata: "作者"}
	if !verifyCandidate(identity, "新曲", "作者による original song の Lyrics", nil) {
		t.Fatal("verified source candidate was rejected")
	}
	if !verifyCandidate(identity, "新曲/作者", "作者 original song Lyrics", nil) {
		t.Fatal("verified source subpage candidate was rejected")
	}
	if verifyCandidate(identity, "新曲", "別人による Lyrics", nil) {
		t.Fatal("candidate without producer identity was accepted")
	}
	if verifyCandidate(identity, "新曲", "作者についての日本語の記事", nil) {
		t.Fatal("candidate without a song or lyrics signal was accepted")
	}
	if verifyCandidate(identity, "超新曲集", "作者本人ではない original song Lyrics", nil) {
		t.Fatal("title and producer substrings were accepted as exact catalog identity")
	}
	if verifyCandidate(identity, "別の曲", "新曲 作者 original song Lyrics", nil) {
		t.Fatal("body-only title mention was accepted as page identity")
	}
	if verifyCandidate(MusicIdentity{JapaneseTitle: "新曲", ProducerMetadata: "deco"}, "新曲", "decorator original song Lyrics", nil) {
		t.Fatal("ASCII producer substring was accepted inside another identifier")
	}
}

func TestSearchFailsClosedWhenAnyCandidateDetailFetchFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("list") == "search" {
			fmt.Fprint(w, `{"query":{"search":[{"pageid":1,"title":"新曲"},{"pageid":2,"title":"新曲/作者"}]}}`)
			return
		}
		if r.URL.Query().Get("pageids") == "1" {
			fmt.Fprint(w, `{"query":{"pages":{"1":{"pageid":1,"title":"新曲","categories":[{"title":"Category:Lyrics"}],"revisions":[{"revid":34,"sha1":"0123456789abcdef0123456789abcdef01234567","slots":{"main":{"content":"作者 original song Lyrics"}}}]}}}}`)
			return
		}
		http.Error(w, "unavailable", http.StatusBadGateway)
	}))
	defer server.Close()
	client := New()
	client.endpoint = server.URL
	result, err := client.Search(context.Background(), MusicIdentity{JapaneseTitle: "新曲", ProducerMetadata: "作者"})
	if err == nil || result != nil || !strings.Contains(err.Error(), "fetch 1 of 2 source candidates") {
		t.Fatalf("partial candidate failure result=%+v err=%v", result, err)
	}
}

func TestSearchFailsWhenEveryCandidateDetailFetchFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("list") == "search" {
			fmt.Fprint(w, `{"query":{"search":[{"pageid":1,"title":"新曲"}]}}`)
			return
		}
		http.Error(w, "unavailable", http.StatusBadGateway)
	}))
	defer server.Close()
	client := New()
	client.endpoint = server.URL
	_, err := client.Search(context.Background(), MusicIdentity{JapaneseTitle: "新曲", ProducerMetadata: "作者"})
	if err == nil {
		t.Fatal("all failed candidate detail requests were reported as an empty result")
	}
}

func TestSearchRejectsMediaWikiErrorPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"error":{"code":"ratelimited","info":"slow down"}}`)
	}))
	defer server.Close()
	client := New()
	client.endpoint = server.URL
	_, err := client.Search(context.Background(), MusicIdentity{JapaneseTitle: "新曲", ProducerMetadata: "作者"})
	if !errors.Is(err, ErrMalformedResponse) {
		t.Fatalf("MediaWiki error payload = %v", err)
	}
}
