package lyricssource

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

func TestReprintRestrictionIgnoresDescriptiveRemovalProse(t *testing.T) {
	for _, description := range []string{
		"An unauthorized reprint was removed by the uploader because reprints are prohibited on the original channel.",
		"The unauthorized reprint was deleted after a rights complaint.",
		"An unauthorized reprint was taken down last year.",
	} {
		content := description + "\n== Lyrics ==\n歌う"
		if hasReprintRestriction(content, nil) {
			t.Fatalf("descriptive removal prose was treated as a current no-reprint restriction: %q", description)
		}
		if _, err := extractLyrics(content); err != nil {
			t.Fatalf("descriptive removal prose extraction error = %v for %q", err, description)
		}
	}
}

func TestReprintRestrictionRejectsExplicitRestrictions(t *testing.T) {
	tests := map[string]struct {
		content    string
		categories []string
	}{
		"spaced template":                     {content: "{{ No_Reprint | reason=producer request }}\n== Lyrics ==\n歌う"},
		"hyphen template":                     {content: "{{no-reprint}}\n== Lyrics ==\n歌う"},
		"compact template":                    {content: "{{noreprint}}\n== Lyrics ==\n歌う"},
		"Japanese prohibition":                {content: "== Lyrics ==\n歌う\n無断転載禁止"},
		"direct English prohibition":          {content: "== Lyrics ==\n歌う\nDo not repost these lyrics."},
		"historical removal then prohibition": {content: "An unauthorized reprint was removed; do not repost these lyrics.\n== Lyrics ==\n歌う"},
		"do not reprint":                      {content: "== Lyrics ==\n歌う\nDo not reprint these lyrics."},
		"reprint prohibited":                  {content: "== Lyrics ==\n歌う\nReprint prohibited."},
		"no unauthorized reprints":            {content: "== Lyrics ==\n歌う\nNo unauthorized reprints."},
		"linked prohibition":                  {content: "== Lyrics ==\n歌う\n'''[[Reprint|Reprints prohibited]]'''"},
		"prohibition category":                {content: "== Lyrics ==\n歌う", categories: []string{"Songs with reprints prohibited"}},
		"unauthorized category":               {content: "== Lyrics ==\n歌う", categories: []string{"Songs with unauthorized reprints"}},
		"Japanese category":                   {content: "== Lyrics ==\n歌う", categories: []string{"Category:無断転載禁止"}},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if !hasReprintRestriction(test.content, test.categories) {
				t.Fatal("explicit restriction was not detected")
			}
		})
	}
}

func TestReprintRestrictionIgnoresInactiveMarkup(t *testing.T) {
	for name, content := range map[string]string{
		"comment":             "<!-- Do not repost these lyrics. -->\n== Lyrics ==\n歌う",
		"nowiki":              "<nowiki>無断転載禁止</nowiki>\n== Lyrics ==\n歌う",
		"template in comment": "<!-- {{No reprint}} -->\n== Lyrics ==\n歌う",
	} {
		t.Run(name, func(t *testing.T) {
			if hasReprintRestriction(content, nil) {
				t.Fatal("inactive restriction markup was treated as active")
			}
			if _, err := extractLyrics(content); err != nil {
				t.Fatalf("inactive restriction extraction error = %v", err)
			}
		})
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

func TestExtractLyricsSyntheticStructuralFixtures(t *testing.T) {
	for _, test := range []struct {
		name          string
		file          string
		sectionSHA256 string
		want          []ExtractedLine
		unwanted      []string
	}{
		{
			name:          "dual-column table with br stanzas",
			file:          "testdata/neo.wiki",
			sectionSHA256: "7414a9b0fab8cbd2ea1f8eef834a90eb3029d3363912edfc7d19d973b79838ee",
			want: []ExtractedLine{
				{Japanese: "合成音色一"},
				{Japanese: "合成音色二合成音色三"},
				{Japanese: "合成表示四", StanzaBreakBefore: true},
				{Japanese: "合成音色五"},
			},
			unwanted: []string{"synthetic neiro one", "synthetic display four"},
		},
		{
			name:          "tabber with lowercase shared source",
			file:          "testdata/journey.wiki",
			sectionSHA256: "89532334b7be1feaa7d11ed2abf09eb29879e18f3f0f42ccff9b21276b69fe7b",
			want: []ExtractedLine{
				{Japanese: "Synthetic Journey"},
				{Japanese: "合成旅路一", StanzaBreakBefore: true},
				{Japanese: "合成旅路二合成旅路三"},
				{Japanese: "Synthetic refrain"},
			},
			unwanted: []string{"gousei tabiji ichi", "Synthetic journey one"},
		},
		{
			name:          "shared English source rows",
			file:          "testdata/worlders.wiki",
			sectionSHA256: "e33829946f85bcb516daa39acfa0297ecb85e54615fb9b0583b1c0852ccda76f",
			want: []ExtractedLine{
				{Japanese: "合成世界一"},
				{Japanese: "Synthetic call", StanzaBreakBefore: true},
				{Japanese: "Change the synthetic world"},
				{Japanese: "合成世界二合成世界三"},
				{Japanese: "Keep testing", StanzaBreakBefore: true},
			},
			unwanted: []string{"gousei sekai one"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
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
			if !equalExtractedLines(lines, test.want) {
				t.Fatalf("extracted lines = %+v, want %+v", lines, test.want)
			}
			for _, unwanted := range test.unwanted {
				assertNoExtractedLine(t, lines, unwanted)
			}
		})
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

func TestCanonicalURLPreservesMediaWikiSubpageSeparatorsAndRevision(t *testing.T) {
	const revisionID = 34
	if got, want := canonicalURL("Journey/DECO*27", revisionID), "https://vocaloid.fandom.com/wiki/Journey/DECO%2A27?oldid="+strconv.Itoa(revisionID); got != want {
		t.Fatalf("canonical URL = %q, want %q", got, want)
	}
	if got, want := canonicalURL("Song ?#% Title", revisionID), "https://vocaloid.fandom.com/wiki/Song_%3F%23%25_Title?oldid="+strconv.Itoa(revisionID); got != want {
		t.Fatalf("escaped canonical URL = %q, want %q", got, want)
	}
	if got, want := canonicalURL("Journey/DECO*27", 0), "https://vocaloid.fandom.com/wiki/Journey/DECO%2A27"; got != want {
		t.Fatalf("unrevisioned canonical URL = %q, want %q", got, want)
	}
}

func TestPreviewReturnsCompleteCanonicalMediaWikiIdentity(t *testing.T) {
	const (
		pageID     = 12
		revisionID = 34
		sha1       = "0123456789abcdef0123456789abcdef01234567"
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if query.Get("action") != "query" || query.Get("prop") != "revisions|categories" || query.Get("revids") != strconv.Itoa(revisionID) ||
			query.Get("rvprop") != "ids|sha1|content" || query.Get("rvslots") != "main" || query.Has("pageids") || query.Has("rvlimit") {
			http.Error(w, "not an exact fixed-revision query", http.StatusBadRequest)
			return
		}
		fmt.Fprintf(w, `{"query":{"pages":{"%d":{"pageid":%d,"title":"新曲/作者","categories":[{"title":"Category:Lyrics"}],"revisions":[{"revid":%d,"sha1":%q,"slots":{"main":{"content":"作者 original song\n== Lyrics ==\n歌う\n\n踊る"}}}]}}}}`, pageID, pageID, revisionID, sha1)
	}))
	defer server.Close()
	client := newTestClient(server.URL)
	preview, err := client.Preview(context.Background(), MusicIdentity{MusicID: 1, JapaneseTitle: "新曲", ProducerMetadata: "作者"}, pageID, revisionID)
	if err != nil {
		t.Fatal(err)
	}
	wantURL := canonicalURL("新曲/作者", revisionID)
	if preview.PageID != pageID || preview.RevisionID != revisionID || preview.SHA1 != sha1 || !mediaWikiSHA1Pattern.MatchString(preview.SHA1) ||
		preview.CanonicalURL != wantURL ||
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
			client := newTestClient(server.URL)
			_, err := client.Preview(context.Background(), MusicIdentity{MusicID: 1, JapaneseTitle: "新曲", ProducerMetadata: "作者"}, 12, 34)
			if !errors.Is(err, ErrMalformedResponse) {
				t.Fatalf("malformed identity error = %v", err)
			}
		})
	}
}

func TestPreviewLatestPageQueryUsesCache(t *testing.T) {
	const (
		pageID     = 12
		revisionID = 34
		sha1       = "0123456789abcdef0123456789abcdef01234567"
	)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("pageids") != strconv.Itoa(pageID) || r.URL.Query().Get("rvlimit") != "1" || r.URL.Query().Has("revids") {
			http.Error(w, "not a latest-page query", http.StatusBadRequest)
			return
		}
		requests.Add(1)
		writePageResponse(w, pageID, revisionID, sha1, "新曲", "作者 original song Lyrics\n== Lyrics ==\n歌う")
	}))
	defer server.Close()
	client := newTestClient(server.URL)
	identity := MusicIdentity{JapaneseTitle: "新曲", ProducerMetadata: "作者"}
	for range 2 {
		preview, err := client.Preview(context.Background(), identity, pageID, 0)
		if err != nil {
			t.Fatal(err)
		}
		if preview.RevisionID != revisionID || preview.CanonicalURL != canonicalURL("新曲", revisionID) {
			t.Fatalf("latest preview = %+v", preview)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("latest-page requests = %d, want cache hit after one fetch", got)
	}
	client.mu.Lock()
	cacheLength := len(client.cache)
	client.mu.Unlock()
	if cacheLength != 1 {
		t.Fatalf("latest-page cache length = %d, want 1", cacheLength)
	}
}

func TestMalformedSearchResponseIsNotPersistentlyCached(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			fmt.Fprint(w, `{"query":{}}`)
			return
		}
		fmt.Fprint(w, `{"query":{"search":[]}}`)
	}))
	defer server.Close()
	client := newTestClient(server.URL)
	identity := MusicIdentity{JapaneseTitle: "新曲", ProducerMetadata: "作者"}
	if _, err := client.Search(context.Background(), identity); !errors.Is(err, ErrMalformedResponse) {
		t.Fatalf("malformed first search error = %v", err)
	}
	result, err := client.Search(context.Background(), identity)
	if err != nil || len(result) != 0 {
		t.Fatalf("recovered search result=%+v err=%v", result, err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("malformed search requests=%d, want refetch", got)
	}
}

func TestMalformedLatestPageResponseIsNotPersistentlyCached(t *testing.T) {
	const (
		pageID     = 12
		revisionID = 34
		sha1       = "0123456789abcdef0123456789abcdef01234567"
	)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			fmt.Fprint(w, `{"query":{"pages":{}}}`)
			return
		}
		writePageResponse(w, pageID, revisionID, sha1, "新曲", "作者 original song Lyrics\n== Lyrics ==\n歌う")
	}))
	defer server.Close()
	client := newTestClient(server.URL)
	identity := MusicIdentity{JapaneseTitle: "新曲", ProducerMetadata: "作者"}
	if _, err := client.Preview(context.Background(), identity, pageID, 0); !errors.Is(err, ErrMalformedResponse) {
		t.Fatalf("malformed first preview error = %v", err)
	}
	preview, err := client.Preview(context.Background(), identity, pageID, 0)
	if err != nil || preview.RevisionID != revisionID {
		t.Fatalf("recovered preview=%+v err=%v", preview, err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("malformed latest-page requests=%d, want refetch", got)
	}
}

func TestPreviewFixedRevisionQueryIsNeverServedFromCache(t *testing.T) {
	const (
		pageID     = 12
		revisionID = 34
		sha1       = "0123456789abcdef0123456789abcdef01234567"
	)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writePageResponse(w, pageID, revisionID, sha1, "新曲", "作者 original song Lyrics\n== Lyrics ==\n歌う")
	}))
	defer server.Close()
	client := newTestClient(server.URL)
	identity := MusicIdentity{JapaneseTitle: "新曲", ProducerMetadata: "作者"}
	for range 2 {
		if _, err := client.Preview(context.Background(), identity, pageID, revisionID); err != nil {
			t.Fatal(err)
		}
	}
	if got := requests.Load(); got != int32(2) {
		t.Fatalf("fixed revision requests = %d, want one exact fetch per preview", got)
	}
	client.mu.Lock()
	cacheLength := len(client.cache)
	client.mu.Unlock()
	if cacheLength != 0 {
		t.Fatalf("fixed revision cache length = %d, want 0", cacheLength)
	}
}

func TestPreviewRejectsReturnedRevisionChange(t *testing.T) {
	const (
		pageID              = 12
		requestedRevisionID = 34
		sha1                = "0123456789abcdef0123456789abcdef01234567"
	)
	returnedRevisionID := requestedRevisionID + 1
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writePageResponse(w, pageID, returnedRevisionID, sha1, "新曲", "作者 original song Lyrics\n== Lyrics ==\n歌う")
	}))
	defer server.Close()
	client := newTestClient(server.URL)
	_, err := client.Preview(context.Background(), MusicIdentity{JapaneseTitle: "新曲", ProducerMetadata: "作者"}, pageID, requestedRevisionID)
	if !errors.Is(err, ErrRevisionChanged) {
		t.Fatalf("changed revision error = %v", err)
	}
}

func TestPreviewRejectsRevisionFromAnotherPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"query":{"pages":{"999":{"pageid":999,"title":"新曲","categories":[{"title":"Category:Lyrics"}],"revisions":[{"revid":34,"sha1":"0123456789abcdef0123456789abcdef01234567","slots":{"main":{"content":"作者 original song\n== Lyrics ==\n歌う"}}}]}}}}`)
	}))
	defer server.Close()
	client := newTestClient(server.URL)
	_, err := client.Preview(context.Background(), MusicIdentity{MusicID: 1, JapaneseTitle: "新曲", ProducerMetadata: "作者"}, 12, 34)
	if !errors.Is(err, ErrRevisionChanged) {
		t.Fatalf("cross-page revision error = %v", err)
	}
}

func TestRequestPreservesEndpointQueryParameters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("base") != "1" || r.URL.Query().Get("action") != "query" {
			http.Error(w, "missing query parameters", http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, "ok")
	}))
	defer server.Close()
	client := newTestClient(server.URL + "?base=1")
	body, err := client.request(context.Background(), "query", url.Values{"action": {"query"}}, false)
	if err != nil || string(body) != "ok" {
		t.Fatalf("query-preserving request body=%q err=%v", body, err)
	}
}

func TestMalformedEndpointDoesNotConsumeRateLimitSlot(t *testing.T) {
	client := New()
	client.endpoint = "://invalid"
	client.rateMu.Lock()
	before := client.lastRequest
	client.rateMu.Unlock()
	if _, err := client.request(context.Background(), "malformed", url.Values{"check": {"endpoint"}}, false); err == nil {
		t.Fatal("malformed endpoint was accepted")
	}
	client.rateMu.Lock()
	after := client.lastRequest
	client.rateMu.Unlock()
	if !after.Equal(before) || len(client.rateToken) != 1 {
		t.Fatalf("malformed endpoint changed rate state lastRequest=%v tokenCount=%d", after, len(client.rateToken))
	}
}

func TestRequestRejectsSchemeChangingRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := *r.URL
		target.Scheme = "https"
		target.Host = r.Host
		http.Redirect(w, r, target.String(), http.StatusFound)
	}))
	defer server.Close()
	client := newTestClient(server.URL)
	_, err := client.request(context.Background(), "redirect", url.Values{"check": {"scheme"}}, false)
	if err == nil || !strings.Contains(err.Error(), "redirect changed origin") {
		t.Fatalf("scheme-changing redirect error = %v", err)
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
	client := newTestClient(source.URL)
	_, err := client.Search(context.Background(), MusicIdentity{JapaneseTitle: "新曲", ProducerMetadata: "作者"})
	if err == nil || !strings.Contains(err.Error(), "redirect changed origin") || targetRequests.Load() != 0 {
		t.Fatalf("cross-origin redirect err=%v targetRequests=%d", err, targetRequests.Load())
	}
}

func TestCanceledContextDoesNotUseOrMutateCacheEntry(t *testing.T) {
	const wantCachedBody = "cached"
	client := New()
	params := url.Values{"cache": {"canceled"}}
	client.cache[params.Encode()] = cacheEntry{body: []byte(wantCachedBody), createdAt: time.Now()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancel()
	body, err := client.request(ctx, "cache", params, true)
	if !errors.Is(err, context.Canceled) || body != nil {
		t.Fatalf("canceled cached request body=%q err=%v", body, err)
	}
	client.mu.Lock()
	inflightCount := len(client.inflight)
	cachedBody := string(client.cache[params.Encode()].body)
	client.mu.Unlock()
	if inflightCount != 0 || cachedBody != wantCachedBody {
		t.Fatalf("canceled request inflight=%d cachedBody=%q", inflightCount, cachedBody)
	}
}

func TestRequestCacheEntryAtTTLIsExpired(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		fmt.Fprint(w, "fresh")
	}))
	defer server.Close()
	client := newTestClient(server.URL)
	client.cacheTTL = time.Hour
	params := url.Values{"cache": {"exact-ttl"}}
	client.cache[params.Encode()] = cacheEntry{body: []byte("stale"), createdAt: time.Now().Add(-client.cacheTTL)}
	body, err := client.request(context.Background(), "cache", params, true)
	if err != nil || string(body) != "fresh" {
		t.Fatalf("exact-TTL body=%q err=%v", body, err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("exact-TTL upstream requests = %d, want 1", got)
	}
}

func TestRequestCacheHitReturnsCopiesAndExpiryRefetches(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "response-%d", requests.Add(1))
	}))
	defer server.Close()
	client := newTestClient(server.URL)
	client.cacheTTL = time.Hour
	params := url.Values{"cache": {"copy-expiry"}}

	first, err := client.request(context.Background(), "cache", params, true)
	if err != nil {
		t.Fatal(err)
	}
	first[0] = 'X'
	client.mu.Lock()
	storedAfterFirstMutation := string(client.cache[params.Encode()].body)
	client.mu.Unlock()
	if storedAfterFirstMutation != "response-1" {
		t.Fatalf("stored cache body after owner mutation = %q", storedAfterFirstMutation)
	}
	second, err := client.request(context.Background(), "cache", params, true)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(second), "response-1"; got != want {
		t.Fatalf("cached body = %q, want %q", got, want)
	}
	second[0] = 'Y'
	client.mu.Lock()
	storedAfterHitMutation := string(client.cache[params.Encode()].body)
	client.mu.Unlock()
	if storedAfterHitMutation != "response-1" {
		t.Fatalf("stored cache body after hit mutation = %q", storedAfterHitMutation)
	}
	third, err := client.request(context.Background(), "cache", params, true)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(third), "response-1"; got != want {
		t.Fatalf("copied cached body = %q, want %q", got, want)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("cache-hit upstream requests = %d, want 1", got)
	}

	client.mu.Lock()
	entry := client.cache[params.Encode()]
	entry.createdAt = time.Now().Add(-client.cacheTTL - time.Second)
	client.cache[params.Encode()] = entry
	client.mu.Unlock()
	refetched, err := client.request(context.Background(), "cache", params, true)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(refetched), "response-2"; got != want {
		t.Fatalf("expired cache body = %q, want %q", got, want)
	}
	client.mu.Lock()
	refetchedEntry := client.cache[params.Encode()]
	client.mu.Unlock()
	if time.Since(refetchedEntry.createdAt) >= client.cacheTTL {
		t.Fatalf("refetched cache entry remained expired: %v", refetchedEntry.createdAt)
	}
}

func TestRequestCachePrunesExpiredEntriesBeforeApplyingBound(t *testing.T) {
	const expiredKey = "expired"
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		fmt.Fprint(w, r.URL.Query().Get("key"))
	}))
	defer server.Close()
	client := newTestClient(server.URL)
	client.cacheTTL = time.Hour
	expiredParams := url.Values{"seed": {expiredKey}}
	client.cache[expiredParams.Encode()] = cacheEntry{body: []byte(expiredKey), createdAt: time.Now().Add(-client.cacheTTL - time.Second)}
	for index := 1; index < maxCacheEntries; index++ {
		params := url.Values{"seed": {strconv.Itoa(index)}}
		client.cache[params.Encode()] = cacheEntry{body: []byte(strconv.Itoa(index)), createdAt: time.Now()}
	}
	params := url.Values{"key": {"fresh"}}
	if _, err := client.request(context.Background(), "cache", params, true); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	cacheLength := len(client.cache)
	_, expiredPresent := client.cache[expiredParams.Encode()]
	_, freshPresent := client.cache[params.Encode()]
	client.mu.Unlock()
	if cacheLength != maxCacheEntries || expiredPresent || !freshPresent {
		t.Fatalf("cache length=%d expiredPresent=%t freshPresent=%t", cacheLength, expiredPresent, freshPresent)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("upstream requests = %d, want 1", got)
	}
}

func TestRequestCacheBoundEvictsOldestEntry(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		fmt.Fprint(w, r.URL.Query().Get("key"))
	}))
	defer server.Close()
	client := newTestClient(server.URL)
	client.cacheTTL = 24 * time.Hour
	baseTime := time.Now().Add(-time.Hour)

	for index := 0; index < maxCacheEntries; index++ {
		params := url.Values{"key": {strconv.Itoa(index)}}
		if _, err := client.request(context.Background(), "cache", params, true); err != nil {
			t.Fatal(err)
		}
		client.mu.Lock()
		entry := client.cache[params.Encode()]
		entry.createdAt = baseTime.Add(time.Duration(index) * time.Millisecond)
		client.cache[params.Encode()] = entry
		client.mu.Unlock()
	}
	oldestKey := url.Values{"key": {"0"}}.Encode()
	newestKey := url.Values{"key": {strconv.Itoa(maxCacheEntries)}}
	if _, err := client.request(context.Background(), "cache", newestKey, true); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	cacheLength := len(client.cache)
	_, oldestPresent := client.cache[oldestKey]
	_, newestPresent := client.cache[newestKey.Encode()]
	client.mu.Unlock()
	if cacheLength != maxCacheEntries || oldestPresent || !newestPresent {
		t.Fatalf("cache length=%d oldestPresent=%t newestPresent=%t", cacheLength, oldestPresent, newestPresent)
	}
	if got, want := requests.Load(), int32(maxCacheEntries+1); got != want {
		t.Fatalf("upstream requests = %d, want %d", got, want)
	}
	if _, err := client.request(context.Background(), "cache", url.Values{"key": {"0"}}, true); err != nil {
		t.Fatal(err)
	}
	if got, want := requests.Load(), int32(maxCacheEntries+2); got != want {
		t.Fatalf("evicted-key requests = %d, want %d", got, want)
	}
	client.mu.Lock()
	cacheLength = len(client.cache)
	_, oldestRestored := client.cache[oldestKey]
	client.mu.Unlock()
	if cacheLength != maxCacheEntries || !oldestRestored {
		t.Fatalf("cache after refetch length=%d oldestRestored=%t", cacheLength, oldestRestored)
	}
}

func TestRequestBoundsDistinctInflightRequestsAndRecoversQueue(t *testing.T) {
	var requests atomic.Int32
	allStarted := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == maxInflightRequests {
			close(allStarted)
		}
		<-release
		fmt.Fprint(w, r.URL.Query().Get("key"))
	}))
	defer func() {
		closeSignal(release)
		server.Close()
	}()
	client := newTestClient(server.URL)

	results := make(chan error, maxInflightRequests)
	for index := 0; index < maxInflightRequests; index++ {
		params := url.Values{"key": {strconv.Itoa(index)}}
		go func() {
			_, err := client.request(context.Background(), "bounded", params, true)
			results <- err
		}()
	}
	awaitSignal(t, allStarted, "maximum distinct in-flight requests")

	queuedCtx, cancelQueued := context.WithCancel(context.Background())
	defer cancelQueued()
	queuedStarted := make(chan struct{})
	queuedErr := make(chan error, 1)
	go func() {
		close(queuedStarted)
		_, err := client.request(queuedCtx, "bounded", url.Values{"key": {"queued"}}, true)
		queuedErr <- err
	}()
	awaitSignal(t, queuedStarted, "bounded queue entry")
	time.Sleep(25 * time.Millisecond)
	if got, want := requests.Load(), int32(maxInflightRequests); got != want {
		t.Fatalf("upstream requests while full = %d, want %d", got, want)
	}
	cancelQueued()
	if err := awaitError(t, queuedErr, "canceled bounded queue entry"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled bounded queue error = %v", err)
	}

	closeSignal(release)
	for range maxInflightRequests {
		if err := awaitError(t, results, "bounded request completion"); err != nil {
			t.Fatal(err)
		}
	}
	client.mu.Lock()
	inflightCount := len(client.inflight)
	client.mu.Unlock()
	if inflightCount != 0 {
		t.Fatalf("in-flight requests after release = %d, want 0", inflightCount)
	}
	if _, err := client.request(context.Background(), "bounded", url.Values{"key": {"recovered"}}, true); err != nil {
		t.Fatalf("request after bounded queue recovery = %v", err)
	}
	if got, want := requests.Load(), int32(maxInflightRequests+1); got != want {
		t.Fatalf("upstream requests after recovery = %d, want %d", got, want)
	}
}

func TestRequestBoundsFixedPreviewHTTPRequests(t *testing.T) {
	var requests atomic.Int32
	allStarted := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == maxInflightRequests {
			close(allStarted)
		}
		<-release
		pageID, _ := strconv.Atoi(r.URL.Query().Get("revids"))
		if pageID <= 0 {
			pageID = 1
		}
		writePageResponse(w, pageID, pageID, "0123456789abcdef0123456789abcdef01234567", "新曲", "作者 original song Lyrics\n== Lyrics ==\n歌う")
	}))
	defer func() {
		closeSignal(release)
		server.Close()
	}()
	client := newTestClient(server.URL)

	results := make(chan error, maxInflightRequests)
	for index := 1; index <= maxInflightRequests; index++ {
		pageID := index
		go func() {
			_, err := client.Preview(context.Background(), MusicIdentity{JapaneseTitle: "新曲", ProducerMetadata: "作者"}, pageID, pageID)
			results <- err
		}()
	}
	awaitSignal(t, allStarted, "maximum fixed preview HTTP requests")
	queuedCtx, cancelQueued := context.WithCancel(context.Background())
	queuedErr := make(chan error, 1)
	go func() {
		_, err := client.Preview(queuedCtx, MusicIdentity{JapaneseTitle: "新曲", ProducerMetadata: "作者"}, maxInflightRequests+1, maxInflightRequests+1)
		queuedErr <- err
	}()
	time.Sleep(25 * time.Millisecond)
	if got := requests.Load(); got != maxInflightRequests {
		t.Fatalf("fixed preview requests while full=%d, want %d", got, maxInflightRequests)
	}
	cancelQueued()
	if err := awaitError(t, queuedErr, "queued fixed preview cancellation"); !errors.Is(err, context.Canceled) {
		t.Fatalf("queued fixed preview error=%v", err)
	}
	closeSignal(release)
	for range maxInflightRequests {
		if err := awaitError(t, results, "fixed preview completion"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRequestCoalescesUpstreamErrorsWithoutCachingThem(t *testing.T) {
	var requests atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			close(started)
		}
		<-release
		http.Error(w, "unavailable", http.StatusBadGateway)
	}))
	defer func() {
		closeSignal(release)
		server.Close()
	}()
	client := newTestClient(server.URL)
	params := url.Values{"cache": {"coalesced-error"}}
	ownerErr := make(chan error, 1)
	go func() {
		_, err := client.request(context.Background(), "cache", params, true)
		ownerErr <- err
	}()
	awaitSignal(t, started, "coalesced error owner")
	waiterErr := make(chan error, 1)
	go func() {
		_, err := client.request(context.Background(), "cache", params, true)
		waiterErr <- err
	}()
	awaitCondition(t, time.Second, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		request := client.inflight[params.Encode()]
		return request != nil && request.waiters == 1
	}, "error waiter to coalesce")
	closeSignal(release)
	for _, result := range []<-chan error{ownerErr, waiterErr} {
		if err := awaitError(t, result, "coalesced error completion"); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("http %d", http.StatusBadGateway)) {
			t.Fatalf("coalesced upstream error = %v", err)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("coalesced error upstream requests = %d, want 1", got)
	}
	client.mu.Lock()
	cacheLength := len(client.cache)
	inflightCount := len(client.inflight)
	client.mu.Unlock()
	if cacheLength != 0 || inflightCount != 0 {
		t.Fatalf("state after coalesced error cache=%d inflight=%d", cacheLength, inflightCount)
	}
}

func TestRequestCoalescedResponsesReturnIndependentCopies(t *testing.T) {
	const payload = "coalesced"
	var requests atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			close(started)
		}
		<-release
		fmt.Fprint(w, payload)
	}))
	defer func() {
		closeSignal(release)
		server.Close()
	}()
	client := newTestClient(server.URL)
	params := url.Values{"cache": {"coalesced-copy"}}
	ownerResult := make(chan requestResult, 1)
	go func() {
		body, err := client.request(context.Background(), "cache", params, true)
		ownerResult <- requestResult{body: body, err: err}
	}()
	awaitSignal(t, started, "coalesced copy owner")
	waiterResult := make(chan requestResult, 1)
	go func() {
		body, err := client.request(context.Background(), "cache", params, true)
		waiterResult <- requestResult{body: body, err: err}
	}()
	awaitCondition(t, time.Second, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		request := client.inflight[params.Encode()]
		return request != nil && request.waiters == 1
	}, "copy waiter to coalesce")
	closeSignal(release)
	owner := awaitRequestResult(t, ownerResult, "coalesced copy owner completion")
	waiter := awaitRequestResult(t, waiterResult, "coalesced copy waiter completion")
	if owner.err != nil {
		t.Fatal(owner.err)
	}
	if waiter.err != nil {
		t.Fatal(waiter.err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("coalesced copy upstream requests = %d, want 1", got)
	}
	owner.body[0] = 'O'
	if got := string(waiter.body); got != payload {
		t.Fatalf("waiter body after owner mutation = %q, want %q", got, payload)
	}
	waiter.body[0] = 'W'
	client.mu.Lock()
	stored := string(client.cache[params.Encode()].body)
	client.mu.Unlock()
	if stored != payload {
		t.Fatalf("stored cache after coalesced mutations = %q, want %q", stored, payload)
	}
}

func TestCanceledOwnerDoesNotAbortHealthyCoalescedWaiter(t *testing.T) {
	const payload = "shared-result"
	var requests atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			close(started)
		}
		<-release
		fmt.Fprint(w, payload)
	}))
	defer func() {
		closeSignal(release)
		server.Close()
	}()
	client := newTestClient(server.URL)
	params := url.Values{"cache": {"canceled-owner"}}
	ownerCtx, cancelOwner := context.WithCancel(context.Background())
	defer cancelOwner()
	ownerErr := make(chan error, 1)
	go func() {
		_, err := client.request(ownerCtx, "cache", params, true)
		ownerErr <- err
	}()
	awaitSignal(t, started, "canceled owner request")
	waiterResult := make(chan requestResult, 1)
	go func() {
		body, err := client.request(context.Background(), "cache", params, true)
		waiterResult <- requestResult{body: body, err: err}
	}()
	awaitCondition(t, time.Second, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		inflight := client.inflight[params.Encode()]
		return inflight != nil && inflight.waiters == 1 && inflight.participants == 2
	}, "healthy waiter to join canceled owner")
	cancelOwner()
	if err := awaitError(t, ownerErr, "canceled owner completion"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled owner error = %v", err)
	}
	closeSignal(release)
	waiter := awaitRequestResult(t, waiterResult, "healthy waiter completion")
	if waiter.err != nil || string(waiter.body) != payload {
		t.Fatalf("healthy waiter body=%q err=%v", waiter.body, waiter.err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("canceled owner upstream requests = %d, want shared request", got)
	}
	client.mu.Lock()
	cacheLength := len(client.cache)
	inflightCount := len(client.inflight)
	client.mu.Unlock()
	if cacheLength != 1 || inflightCount != 0 {
		t.Fatalf("state after canceled owner cache=%d inflight=%d", cacheLength, inflightCount)
	}
}

func TestRequestDoesNotCacheUpstreamErrors(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			http.Error(w, "unavailable", http.StatusBadGateway)
			return
		}
		fmt.Fprint(w, "recovered")
	}))
	defer server.Close()
	client := newTestClient(server.URL)
	params := url.Values{"cache": {"error"}}
	if _, err := client.request(context.Background(), "cache", params, true); err == nil {
		t.Fatal("upstream error was accepted")
	}
	body, err := client.request(context.Background(), "cache", params, true)
	if err != nil || string(body) != "recovered" {
		t.Fatalf("recovery body=%q err=%v", body, err)
	}
	if got := requests.Load(); got != int32(2) {
		t.Fatalf("upstream requests = %d, want retry after error", got)
	}
	client.mu.Lock()
	cacheLength := len(client.cache)
	client.mu.Unlock()
	if cacheLength != 1 {
		t.Fatalf("cache length after recovery = %d, want 1", cacheLength)
	}
}

func TestRequestCoalescesConcurrentIdenticalCacheMisses(t *testing.T) {
	const callers = maxInflightRequests + maxInflightRequests/2
	var requests atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			close(started)
		}
		<-release
		fmt.Fprint(w, `{"query":{"search":[]}}`)
	}))
	defer func() {
		closeSignal(release)
		server.Close()
	}()
	client := newTestClient(server.URL)
	params := searchRequestParams("新曲")

	var ready sync.WaitGroup
	ready.Add(callers)
	start := make(chan struct{})
	errs := make(chan error, callers)
	for range callers {
		go func() {
			ready.Done()
			<-start
			_, err := client.request(context.Background(), "search", params, true)
			errs <- err
		}()
	}
	ready.Wait()
	close(start)
	awaitSignal(t, started, "coalesced upstream request")
	awaitCondition(t, time.Second, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		request := client.inflight[params.Encode()]
		return request != nil && request.waiters == callers-1
	}, "all identical callers to coalesce")
	if got := requests.Load(); got != 1 {
		t.Fatalf("upstream requests while identical calls wait = %d, want 1", got)
	}
	closeSignal(release)
	for range callers {
		if err := awaitError(t, errs, "coalesced request completion"); err != nil {
			t.Fatal(err)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("upstream requests = %d, want 1", got)
	}
	client.mu.Lock()
	inflightCount := len(client.inflight)
	client.mu.Unlock()
	if inflightCount != 0 {
		t.Fatalf("in-flight requests after coalesced completion = %d, want 0", inflightCount)
	}
}

func TestCanceledCoalescedWaiterDoesNotBlockFutureRequest(t *testing.T) {
	var requests atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			close(started)
			<-release
		}
		fmt.Fprint(w, `{"query":{"search":[]}}`)
	}))
	defer func() {
		closeSignal(release)
		server.Close()
	}()
	client := newTestClient(server.URL)
	params := searchRequestParams("新曲")

	ownerErr := make(chan error, 1)
	go func() {
		_, err := client.request(context.Background(), "search", params, true)
		ownerErr <- err
	}()
	awaitSignal(t, started, "owner request")

	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	defer cancelWaiter()
	waiterStarted := make(chan struct{})
	waiterErr := make(chan error, 1)
	go func() {
		close(waiterStarted)
		_, err := client.request(waiterCtx, "search", params, true)
		waiterErr <- err
	}()
	awaitSignal(t, waiterStarted, "coalesced waiter")
	awaitCondition(t, time.Second, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		request := client.inflight[params.Encode()]
		return request != nil && request.waiters == 1
	}, "waiter to join the coalesced request")
	if got := requests.Load(); got != 1 {
		t.Fatalf("upstream requests before canceling waiter = %d, want 1", got)
	}
	cancelWaiter()
	if err := awaitError(t, waiterErr, "canceled waiter"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter error = %v", err)
	}
	awaitCondition(t, time.Second, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		request := client.inflight[params.Encode()]
		return request != nil && request.waiters == 0
	}, "canceled waiter to leave the coalesced request")

	closeSignal(release)
	if err := awaitError(t, ownerErr, "owner completion"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.request(context.Background(), "search", params, true); err != nil {
		t.Fatalf("cache request after canceled waiter = %v", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("upstream requests = %d, want cached recovery after one request", got)
	}
	client.mu.Lock()
	inflightCount := len(client.inflight)
	client.mu.Unlock()
	if inflightCount != 0 {
		t.Fatalf("in-flight requests after canceled waiter recovery = %d, want 0", inflightCount)
	}
}

func TestCanceledWaiterQueuedBehindRateLimitOwnerDoesNotReserveSlot(t *testing.T) {
	client := New()
	<-client.rateToken
	rateTokenReturned := false
	defer func() {
		if !rateTokenReturned {
			client.rateToken <- struct{}{}
		}
	}()
	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	defer cancelWaiter()
	waiterStarted := make(chan struct{})
	waiterErr := make(chan error, 1)
	go func() {
		close(waiterStarted)
		waiterErr <- client.waitRateLimit(waiterCtx)
	}()
	awaitSignal(t, waiterStarted, "queued rate-limit waiter")
	time.Sleep(25 * time.Millisecond)
	cancelWaiter()
	if err := awaitError(t, waiterErr, "canceled queued rate-limit waiter"); !errors.Is(err, context.Canceled) {
		t.Fatalf("queued waiter error = %v", err)
	}
	client.rateToken <- struct{}{}
	rateTokenReturned = true
	if got := len(client.rateToken); got != 1 {
		t.Fatalf("rate token count after queued waiter cancellation = %d, want 1", got)
	}
}

func TestCanceledRateLimitQueueEntryDoesNotDelayFollowingRequest(t *testing.T) {
	client := New()
	client.minInterval = 5 * time.Second
	initialLastRequest := time.Now()
	client.lastRequest = initialLastRequest

	queueCtx, cancelQueue := context.WithCancel(context.Background())
	defer cancelQueue()
	queueErr := make(chan error, 1)
	go func() { queueErr <- client.waitRateLimit(queueCtx) }()
	awaitCondition(t, time.Second, func() bool { return len(client.rateToken) == 0 }, "rate-limit queue entry")
	cancelQueue()
	if err := awaitError(t, queueErr, "canceled rate-limit queue entry"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled queue error = %v", err)
	}
	if got := len(client.rateToken); got != 1 {
		t.Fatalf("rate token count after cancellation = %d, want 1", got)
	}
	client.rateMu.Lock()
	lastRequestAfterCancel := client.lastRequest
	client.rateMu.Unlock()
	if !lastRequestAfterCancel.Equal(initialLastRequest) {
		t.Fatalf("canceled queue entry changed last request from %v to %v", initialLastRequest, lastRequestAfterCancel)
	}

	client.rateMu.Lock()
	client.lastRequest = time.Time{}
	client.rateMu.Unlock()
	start := time.Now()
	if err := client.waitRateLimit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("following request waited %v after canceled queue entry", elapsed)
	}
}

func TestSearchStopsPromptlyAfterContextCancellation(t *testing.T) {
	const candidateCount = maxInflightRequests / 2
	var detailRequests atomic.Int32
	firstDetailStarted := make(chan struct{})
	firstDetailCanceled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("list") == "search" {
			fmt.Fprint(w, searchResponse(candidateCount))
			return
		}
		if detailRequests.Add(1) == 1 {
			close(firstDetailStarted)
		}
		<-r.Context().Done()
		close(firstDetailCanceled)
	}))
	server.Client().Timeout = 3 * time.Second
	client := newTestClient(server.URL)
	client.httpClient = server.Client()
	client.minInterval = 0
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()
		server.Close()
	}()
	searchErr := make(chan error, 1)
	go func() {
		_, err := client.Search(ctx, MusicIdentity{JapaneseTitle: "新曲", ProducerMetadata: "作者"})
		searchErr <- err
	}()
	awaitSignal(t, firstDetailStarted, "first candidate detail request")
	cancel()
	awaitSignal(t, firstDetailCanceled, "first candidate detail cancellation")
	if err := awaitError(t, searchErr, "canceled search"); !errors.Is(err, context.Canceled) {
		t.Fatalf("search error = %v", err)
	}
	if got := detailRequests.Load(); got != 1 {
		t.Fatalf("candidate detail requests after cancellation = %d, want 1", got)
	}
}

func TestOversizedResponseIsNotCached(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			_, _ = w.Write(bytes.Repeat([]byte("x"), maxResponseBytes+1))
			return
		}
		fmt.Fprint(w, "recovered")
	}))
	defer server.Close()
	client := newTestClient(server.URL)
	params := url.Values{"size": {"cache-over-limit"}}
	if _, err := client.request(context.Background(), "size", params, true); err == nil || !strings.Contains(err.Error(), "response too large") {
		t.Fatalf("oversized response error = %v", err)
	}
	body, err := client.request(context.Background(), "size", params, true)
	if err != nil || string(body) != "recovered" {
		t.Fatalf("oversized response recovery body=%q err=%v", body, err)
	}
	if got := requests.Load(); got != int32(2) {
		t.Fatalf("oversized response requests = %d, want retry", got)
	}
}

func TestRequestResponseSizeBoundaryAndOverLimit(t *testing.T) {
	tests := map[string]struct {
		size    int
		wantErr bool
	}{
		"exact boundary": {size: maxResponseBytes},
		"over limit":     {size: maxResponseBytes + 1, wantErr: true},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			payload := bytes.Repeat([]byte("x"), test.size)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write(payload)
			}))
			defer server.Close()
			client := newTestClient(server.URL)
			body, err := client.request(context.Background(), "boundary", url.Values{"size": {strconv.Itoa(test.size)}}, false)
			if test.wantErr {
				if err == nil || !strings.Contains(err.Error(), "response too large") {
					t.Fatalf("request error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(body, payload) {
				t.Fatalf("body length = %d, want exact payload length %d", len(body), len(payload))
			}
		})
	}
}

func TestRequestAllowsRedirectLimitAndRejectsNextSameOriginRedirect(t *testing.T) {
	var requests atomic.Int32
	var redirects atomic.Int32
	redirects.Store(int32(maxSourceRedirects))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		step, err := strconv.Atoi(r.URL.Query().Get("step"))
		if err != nil {
			step = 0
		}
		if int32(step) < redirects.Load() {
			next := *r.URL
			query := next.Query()
			query.Set("step", strconv.Itoa(step+1))
			next.RawQuery = query.Encode()
			http.Redirect(w, r, next.String(), http.StatusFound)
			return
		}
		fmt.Fprint(w, `{"query":{"search":[]}}`)
	}))
	defer server.Close()

	client := newTestClient(server.URL)
	params := searchRequestParams("新曲")
	params.Set("step", "0")
	body, err := client.request(context.Background(), "search", params, false)
	if err != nil {
		t.Fatalf("request with %d redirects failed: %v", maxSourceRedirects, err)
	}
	if got, want := string(body), `{"query":{"search":[]}}`; got != want {
		t.Fatalf("redirected body = %q, want %q", got, want)
	}
	if got, want := requests.Load(), int32(maxSourceRedirects+1); got != want {
		t.Fatalf("requests = %d, want %d", got, want)
	}

	requests.Store(0)
	redirects.Store(int32(maxSourceRedirects + 1))
	if _, err := client.request(context.Background(), "search", params, false); err == nil || !strings.Contains(err.Error(), "redirect limit exceeded") {
		t.Fatalf("request beyond redirect limit error = %v", err)
	}
	if got, want := requests.Load(), int32(maxSourceRedirects+1); got != want {
		t.Fatalf("over-limit requests = %d, want %d", got, want)
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

func TestSearchPreservesReturnedPageRevisionAndSHAIdentity(t *testing.T) {
	const (
		pageID     = 12
		revisionID = 34
		sha1       = "0123456789abcdef0123456789abcdef01234567"
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("list") == "search" {
			fmt.Fprintf(w, `{"query":{"search":[{"pageid":%d,"title":"新曲/作者"}]}}`, pageID)
			return
		}
		writePageResponse(w, pageID, revisionID, sha1, "新曲/作者", "作者 original song Lyrics")
	}))
	defer server.Close()
	client := newTestClient(server.URL)
	candidates, err := client.Search(context.Background(), MusicIdentity{JapaneseTitle: "新曲", ProducerMetadata: "作者"})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].PageID != pageID || candidates[0].RevisionID != revisionID || candidates[0].SHA1 != sha1 ||
		candidates[0].CanonicalURL != canonicalURL("新曲/作者", revisionID) {
		t.Fatalf("candidates = %+v", candidates)
	}
}

func TestSearchFailsClosedOnReturnedPageMismatch(t *testing.T) {
	const (
		searchPageID = 12
		otherPageID  = searchPageID + 1
		revisionID   = 34
		sha1         = "0123456789abcdef0123456789abcdef01234567"
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("list") == "search" {
			fmt.Fprintf(w, `{"query":{"search":[{"pageid":%d,"title":"新曲"}]}}`, searchPageID)
			return
		}
		writePageResponse(w, otherPageID, revisionID, sha1, "新曲", "作者 original song Lyrics")
	}))
	defer server.Close()
	client := newTestClient(server.URL)
	candidates, err := client.Search(context.Background(), MusicIdentity{JapaneseTitle: "新曲", ProducerMetadata: "作者"})
	if err == nil || candidates != nil || !errors.Is(err, ErrRevisionChanged) {
		t.Fatalf("mismatched page result=%+v err=%v", candidates, err)
	}
}

func TestSearchSkipsAmbiguousAndRestrictedCandidatesWithoutUpstreamFailure(t *testing.T) {
	const (
		invalidPageID    = 12
		restrictedPageID = invalidPageID + 1
		revisionID       = 34
		sha1             = "0123456789abcdef0123456789abcdef01234567"
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("list") == "search" {
			fmt.Fprintf(w, `{"query":{"search":[{"pageid":%d,"title":"新曲"},{"pageid":%d,"title":"新曲/作者"}]}}`, invalidPageID, restrictedPageID)
			return
		}
		pageID, err := strconv.Atoi(r.URL.Query().Get("pageids"))
		if err != nil {
			http.Error(w, "invalid page id", http.StatusBadRequest)
			return
		}
		if pageID == invalidPageID {
			fmt.Fprintf(w, `{"query":{"pages":{"%d":{"pageid":%d,"title":"新曲","categories":[{"title":"Category:Articles"}],"revisions":[{"revid":%d,"sha1":%q,"slots":{"main":{"content":"作者 article without song signal"}}}]}}}}`,
				pageID, pageID, revisionID, sha1)
			return
		}
		writePageResponse(w, pageID, revisionID+1, sha1, "新曲/作者", "作者 original song Lyrics\nDo not repost these lyrics.")
	}))
	defer server.Close()
	client := newTestClient(server.URL)
	candidates, err := client.Search(context.Background(), MusicIdentity{JapaneseTitle: "新曲", ProducerMetadata: "作者"})
	if err != nil || len(candidates) != 0 {
		t.Fatalf("filtered candidate result=%+v err=%v", candidates, err)
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
	client := newTestClient(server.URL)
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
	client := newTestClient(server.URL)
	_, err := client.Search(context.Background(), MusicIdentity{JapaneseTitle: "新曲", ProducerMetadata: "作者"})
	if err == nil {
		t.Fatal("all failed candidate detail requests were reported as an empty result")
	}
}

func TestSearchFailsClosedOnMalformedCandidateDetail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("list") == "search" {
			fmt.Fprint(w, `{"query":{"search":[{"pageid":1,"title":"新曲"}]}}`)
			return
		}
		fmt.Fprint(w, `{"query":{"pages":{}}}`)
	}))
	defer server.Close()
	client := newTestClient(server.URL)
	candidates, err := client.Search(context.Background(), MusicIdentity{JapaneseTitle: "新曲", ProducerMetadata: "作者"})
	if candidates != nil || !errors.Is(err, ErrMalformedResponse) {
		t.Fatalf("malformed candidate result=%+v err=%v", candidates, err)
	}
}

func TestSearchRejectsMediaWikiErrorPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"error":{"code":"ratelimited","info":"slow down"}}`)
	}))
	defer server.Close()
	client := newTestClient(server.URL)
	_, err := client.Search(context.Background(), MusicIdentity{JapaneseTitle: "新曲", ProducerMetadata: "作者"})
	if !errors.Is(err, ErrMalformedResponse) {
		t.Fatalf("MediaWiki error payload = %v", err)
	}
}

type requestResult struct {
	body []byte
	err  error
}

func newTestClient(endpoint string) *Client {
	client := New()
	client.endpoint = endpoint
	client.minInterval = 0
	return client
}

func searchRequestParams(title string) url.Values {
	return url.Values{
		"action":      {"query"},
		"format":      {"json"},
		"list":        {"search"},
		"srnamespace": {"0"},
		"srlimit":     {"8"},
		"srsearch":    {title},
	}
}

func searchResponse(candidateCount int) string {
	var response strings.Builder
	response.WriteString(`{"query":{"search":[`)
	for index := 1; index <= candidateCount; index++ {
		if index > 1 {
			response.WriteByte(',')
		}
		fmt.Fprintf(&response, `{"pageid":%d,"title":"新曲/%d"}`, index, index)
	}
	response.WriteString(`]}}`)
	return response.String()
}

func writePageResponse(w http.ResponseWriter, pageID, revisionID int, sha1, title, content string) {
	fmt.Fprintf(w, `{"query":{"pages":{"%d":{"pageid":%d,"title":%q,"categories":[{"title":"Category:Lyrics"}],"revisions":[{"revid":%d,"sha1":%q,"slots":{"main":{"content":%q}}}]}}}}`,
		pageID, pageID, title, revisionID, sha1, content)
}

func closeSignal(signal chan struct{}) {
	defer func() {
		_ = recover()
	}()
	close(signal)
}

func awaitSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func awaitError(t *testing.T, result <-chan error, description string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
		return nil
	}
}

func awaitRequestResult(t *testing.T, result <-chan requestResult, description string) requestResult {
	t.Helper()
	select {
	case response := <-result:
		return response
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
		return requestResult{}
	}
}

func awaitCondition(t *testing.T, timeout time.Duration, condition func() bool, description string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}
