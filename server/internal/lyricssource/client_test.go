package lyricssource

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"moesekai/server/internal/httpx"
)

type offlineRequestMarkerTransport struct {
	mu       sync.Mutex
	requests []string
}

func (transport *offlineRequestMarkerTransport) RecoveryRequestOffline(request *http.Request) bool {
	return request != nil && request.URL != nil && request.URL.Query().Get("replay") == "list"
}

func (transport *offlineRequestMarkerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.mu.Lock()
	transport.requests = append(transport.requests, request.URL.Query().Get("replay"))
	transport.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{}`)),
		Request:    request,
	}, nil
}

func TestRecoveryOfflineRequestBypassesActualHTTPRateDelay(t *testing.T) {
	transport := &offlineRequestMarkerTransport{}
	client := newMediaWikiClient(
		"https://www.sekaipedia.org/w/api.php", 5*time.Second, time.Minute,
		&http.Client{Transport: transport},
	)
	if _, _, err := client.requestWithFetchedAt(t.Context(), "list", url.Values{
		"action": {"query"}, "replay": {"list"},
	}, false); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, _, err := client.requestWithFetchedAt(ctx, "song", url.Values{
		"action": {"query"}, "replay": {"song"},
	}, false); err != nil {
		t.Fatalf("live request waited behind an offline replay: %v", err)
	}
	if elapsed := time.Since(started); elapsed >= 500*time.Millisecond {
		t.Fatalf("live request inherited offline replay delay: %s", elapsed)
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if !reflect.DeepEqual(transport.requests, []string{"list", "song"}) {
		t.Fatalf("request order=%v", transport.requests)
	}
}

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

func TestExtractStructuredLyricsPrefersSekaiVersionAndPreservesPerformerSquares(t *testing.T) {
	content := `== Lyrics ==
<tabber>25-ji, Nightcord de. Version =
{{lrc legend|background
|MEIKO:#DE4444
|Ena:#CCAA87
|Mafuyu:#8889CC
}}
{| style="width:100%"
! Japanese (日本語歌詞)
! Romaji
|-
|{{lrc color|ena|今}}この瞬間を {{lrc color|meiko|■}}{{lrc color|ena|■}}
|ima kono shunkan o
|-
|<br>
|-
|不器用だから {{lrc color|mafuyu|■}}{{lrc color|ena|■}}
|bukiyou dakara
|}
|-|VOCALOID Version =
{| style="width:100%"
! Japanese (日本語歌詞)
! Romaji
|-
|別の歌詞
|betsu no kashi
|}
</tabber>`
	extraction, err := extractStructuredLyrics(content)
	if err != nil {
		t.Fatal(err)
	}
	if extraction.Version.Kind != "sekai" || extraction.Version.Label != "25-ji, Nightcord de. Version" ||
		extraction.RubyGeneratorVersion != rubyGeneratorVersion || len(extraction.Performers) != 3 || len(extraction.Lines) != 2 {
		t.Fatalf("structured extraction = %+v", extraction)
	}
	line := extraction.Lines[0]
	if line.Japanese != "今この瞬間を" || len(line.Segments) != 2 ||
		!equalStrings(line.Segments[0].PerformerIDs, []string{"ena"}) || line.Segments[0].Text != "今" ||
		line.Segments[1].PerformerIDs == nil || len(line.Segments[1].PerformerIDs) != 0 ||
		!equalStrings(line.TrailingPerformerIDs, []string{"meiko", "ena"}) {
		t.Fatalf("structured first line = %+v", line)
	}
	if len(line.Segments[0].Ruby) != 1 || line.Segments[0].Ruby[0].Reading != "いま" {
		t.Fatalf("structured first line ruby = %+v", line.Segments[0].Ruby)
	}
	if !extraction.Lines[1].StanzaBreakBefore || !equalStrings(extraction.Lines[1].TrailingPerformerIDs, []string{"mafuyu", "ena"}) {
		t.Fatalf("structured second line = %+v", extraction.Lines[1])
	}
	if got := extraction.Performers[0]; got.PerformerID != "meiko" || got.Color != "#DE4444" {
		t.Fatalf("first performer = %+v", got)
	}
}

func TestExtractStructuredLyricsSelectsJapaneseTabAndPreservesExactSharedLine(t *testing.T) {
	content, err := os.ReadFile("testdata/journey-1476888.wiki")
	if err != nil {
		t.Fatal(err)
	}
	extraction, err := extractStructuredLyrics(string(content))
	if err != nil {
		t.Fatal(err)
	}
	if extraction.Version.Kind != "original" || extraction.Version.Label != "Japanese lyrics" || len(extraction.Lines) != 43 {
		t.Fatalf("Journey extraction version=%+v lines=%d", extraction.Version, len(extraction.Lines))
	}
	if extraction.Lines[0].Japanese != "Journey" || extraction.Lines[0].StanzaBreakBefore ||
		extraction.Lines[1].Japanese != "溜めてきた　“なんとかなる”は" || !extraction.Lines[1].StanzaBreakBefore ||
		extraction.Lines[len(extraction.Lines)-1].Japanese != "笑って生きていくためのJourney　奏でるMelody" {
		t.Fatalf("Journey selected lines first=%+v second=%+v last=%+v", extraction.Lines[0], extraction.Lines[1], extraction.Lines[len(extraction.Lines)-1])
	}
	for _, line := range extraction.Lines {
		if strings.Contains(line.Japanese, "My growing pile") || strings.Contains(line.Japanese, "Saving Grace") {
			t.Fatalf("official English translation leaked into source lines: %q", line.Japanese)
		}
	}
}

func TestExtractStructuredLyricsUsesSoleVocaloidVersionAndRejectsAmbiguousVersions(t *testing.T) {
	sole := `== Lyrics ==
<tabber>VOCALOID Version =
{|
! Japanese
! Romaji
|-
|光を見る
|hikari o miru
|}
</tabber>`
	extraction, err := extractStructuredLyrics(sole)
	if err != nil || extraction.Version.Kind != "vocaloid" || len(extraction.Lines) != 1 {
		t.Fatalf("sole Vocaloid extraction=%+v err=%v", extraction, err)
	}
	ambiguous := `== Lyrics ==
<tabber>SEKAI Version =
{|\n! Japanese\n|-
|歌う
|}
|-|Project SEKAI Version =
{|\n! Japanese\n|-
|踊る
|}
</tabber>`
	if _, err := extractStructuredLyrics(ambiguous); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("ambiguous SEKAI versions error = %v", err)
	}
}

func TestExtractStructuredLyricsRealHikariFixture(t *testing.T) {
	content, err := os.ReadFile("testdata/hikari.wiki")
	if err != nil {
		t.Fatal(err)
	}
	extraction, err := extractStructuredLyrics(string(content))
	if err != nil {
		t.Fatal(err)
	}
	if extraction.Version.Kind != "sekai" || extraction.Version.Label != "25-ji, Nightcord de. Version" ||
		len(extraction.Performers) != 6 || len(extraction.Lines) != 32 {
		t.Fatalf("Hikari extraction version=%+v performers=%d lines=%d", extraction.Version, len(extraction.Performers), len(extraction.Lines))
	}
	if extraction.Lines[0].Japanese != "この涙が渇くまで" || extraction.Lines[6].Japanese != "今この瞬間を" ||
		!equalStrings(extraction.Lines[6].TrailingPerformerIDs, []string{"meiko", "ena"}) {
		t.Fatalf("Hikari selected lines = %+v / %+v", extraction.Lines[0], extraction.Lines[6])
	}
	for _, line := range extraction.Lines {
		if line.Japanese == "少なくなるのかな今" {
			t.Fatal("VOCALOID segmentation leaked into selected SEKAI version")
		}
	}
}

func equalStrings(got, want []string) bool {
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

func TestExtractLyricsRejectsAmbiguousTables(t *testing.T) {
	if _, err := extractLyrics("== Lyrics ==\n{|\n| 歌う\n|}\n{|\n| 踊る\n|}"); !errors.Is(err, ErrUnsupportedTable) {
		t.Fatalf("ambiguous table error = %v", err)
	}
}

func TestMediaReprintRestrictionsDoNotBlockLyricText(t *testing.T) {
	tests := map[string]struct {
		content    string
		categories []string
	}{
		"spaced template":           {content: "{{ No_Reprint | reason=producer request }}\n== Lyrics ==\n歌う"},
		"hyphen template":           {content: "{{no-reprint}}\n== Lyrics ==\n歌う"},
		"compact template":          {content: "{{noreprint}}\n== Lyrics ==\n歌う"},
		"Japanese media notice":     {content: "無断転載禁止\n== Lyrics ==\n歌う"},
		"do not reprint":            {content: "Do not reprint this song.\n== Lyrics ==\n歌う"},
		"reprint prohibited":        {content: "Reprint prohibited.\n== Lyrics ==\n歌う"},
		"historical removal":        {content: "An unauthorized reprint was removed because reprints are prohibited.\n== Lyrics ==\n歌う"},
		"linked prohibition":        {content: "'''[[Reprint|Reprints prohibited]]'''\n== Lyrics ==\n歌う"},
		"audio transcription":       {content: "Transcription of this audio is prohibited.\n== Lyrics ==\n歌う"},
		"video transcription":       {content: "Do not transcribe this video.\n== Lyrics ==\n歌う"},
		"song transcription":        {content: "Do not transcribe this song.\n== Lyrics ==\n歌う"},
		"Japanese audio control":    {content: "歌唱音声の文字起こしは禁止\n== Lyrics ==\n歌う"},
		"official video provenance": {content: "Lyrics copied from the official video description\n== Lyrics ==\n歌う"},
		"prohibition category":      {content: "== Lyrics ==\n歌う", categories: []string{"Songs with reprints prohibited"}},
		"unauthorized category":     {content: "== Lyrics ==\n歌う", categories: []string{"Songs with unauthorized reprints"}},
		"Japanese category":         {content: "== Lyrics ==\n歌う", categories: []string{"Category:無断転載禁止"}},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if hasLyricsTextRestriction(test.content, test.categories) {
				t.Fatal("media-only reprint restriction was treated as a lyric-text restriction")
			}
			lines, err := extractLyrics(test.content)
			if err != nil || !equalExtractedLines(lines, []ExtractedLine{{Japanese: "歌う"}}) {
				t.Fatalf("media-only page lines=%+v err=%v", lines, err)
			}
		})
	}
}

func TestLyricsTextRestrictionRejectsOnlyExplicitLyricTextNotices(t *testing.T) {
	tests := map[string]struct {
		content    string
		categories []string
	}{
		"English repost":                   {content: "Do not repost these lyrics.\n== Lyrics ==\n歌う"},
		"English copy":                     {content: "The lyrics may not be copied.\n== Lyrics ==\n歌う"},
		"English cannot be copied":         {content: "These lyrics cannot be copied.\n== Lyrics ==\n歌う"},
		"English can't be reprinted":       {content: "The lyric text can't be reprinted.\n== Lyrics ==\n歌う"},
		"English reproduction":             {content: "Reproduction of the lyric text is prohibited.\n== Lyrics ==\n歌う"},
		"English transcription noun":       {content: "Transcription of these lyrics is prohibited.\n== Lyrics ==\n歌う"},
		"English transcription imperative": {content: "Do not transcribe these lyrics.\n== Lyrics ==\n歌う"},
		"Japanese reprint":                 {content: "歌詞の無断転載禁止\n== Lyrics ==\n歌う"},
		"Japanese copy":                    {content: "歌詞本文の複製を禁止\n== Lyrics ==\n歌う"},
		"Japanese transcription noun":      {content: "歌詞の書き起こしは禁止です\n== Lyrics ==\n歌う"},
		"Japanese transcription command":   {content: "歌詞を書き起こさないでください\n== Lyrics ==\n歌う"},
		"Japanese text transcription":      {content: "歌詞本文の文字起こしを禁止します\n== Lyrics ==\n歌う"},
		"lyrics category":                  {content: "== Lyrics ==\n歌う", categories: []string{"Lyrics may not be reprinted"}},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if !hasLyricsTextRestriction(test.content, test.categories) {
				t.Fatal("explicit lyric-text restriction was not detected")
			}
			if len(test.categories) == 0 {
				if _, err := extractLyrics(test.content); !errors.Is(err, ErrRestrictedReprint) {
					t.Fatalf("explicit lyric-text restriction error=%v", err)
				}
			}
		})
	}
}

func TestLyricsTextRestrictionStatementRegressions(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "cannot be copied", value: "These lyrics cannot be copied.", want: true},
		{name: "can't be reprinted", value: "The lyric text can't be reprinted.", want: true},
		{name: "cannot be transcribed", value: "Lyrics cannot be transcribed.", want: true},
		{name: "official video provenance", value: "Lyrics copied from the official video description", want: false},
		{name: "credited provenance", value: "Lyrics copied from the producer's website by an editor", want: false},
		{name: "ordinary song restriction", value: "This song cannot be reprinted.", want: false},
		{name: "ordinary audio restriction", value: "The audio can't be transcribed.", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := lyricsTextRestrictionStatement(test.value); got != test.want {
				t.Fatalf("lyricsTextRestrictionStatement(%q) = %t, want %t", test.value, got, test.want)
			}
		})
	}
}

func TestLyricsTextRestrictionIgnoresInactiveMarkup(t *testing.T) {
	for name, content := range map[string]string{
		"comment": "<!-- Do not repost these lyrics. -->\n== Lyrics ==\n歌う",
		"nowiki":  "<nowiki>歌詞の無断転載禁止</nowiki>\n== Lyrics ==\n歌う",
	} {
		t.Run(name, func(t *testing.T) {
			if hasLyricsTextRestriction(content, nil) {
				t.Fatal("inactive lyric-text restriction was treated as active")
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
		if query.Get("action") != "query" || query.Get("maxlag") != mediaWikiMaxLag ||
			query.Get("prop") != "revisions|categories" || query.Get("revids") != strconv.Itoa(revisionID) ||
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

func TestFetchFixedRevisionRevalidatesAuthoritativeCreatorAliases(t *testing.T) {
	content := "{{Song box 2\n|lyrics=[[CanonicalP]] & [[SecondP]]\n|music=[[CanonicalP]]＆[[SecondP]]\n}}\noriginal song Lyrics\n== Lyrics ==\n歌う"
	sha1 := sha1Hex(content)
	var songRequests, aliasRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		switch {
		case query.Get("revids") == "34":
			songRequests.Add(1)
			writePageResponse(w, 12, 34, sha1, "新曲", content)
		case query.Get("gsrsearch") == "別名P":
			aliasRequests.Add(1)
			fmt.Fprint(w, `{"query":{"pages":{"44":{"pageid":44,"title":"CanonicalP","categories":[{"title":"Category:Vocaloid producers"}],"revisions":[{"revid":55,"sha1":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","slots":{"main":{"content":"|japanese=別名P"}}}]}}}}`)
		case query.Get("gsrsearch") == "第二P":
			aliasRequests.Add(1)
			fmt.Fprint(w, `{"query":{"pages":{"45":{"pageid":45,"title":"SecondP","categories":[{"title":"Category:Vocaloid producers"}],"revisions":[{"revid":56,"sha1":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","slots":{"main":{"content":"|japanese=第二P"}}}]}}}}`)
		default:
			http.Error(w, "unexpected request", http.StatusBadRequest)
		}
	}))
	defer server.Close()
	client := newTestClient(server.URL)
	fixed, err := client.FetchFixedRevision(context.Background(), MusicIdentity{
		MusicID: 1, JapaneseTitle: "新曲", Lyricist: "別名P ＆ 第二P", Composer: "別名P & 第二P",
	}, 12, 34, sha1)
	if err != nil || len(fixed.Lines) != 1 || fixed.Lines[0].Japanese != "歌う" {
		t.Fatalf("fixed=%+v err=%v", fixed, err)
	}
	if songRequests.Load() != 1 || aliasRequests.Load() != 2 {
		t.Fatalf("song requests=%d alias requests=%d", songRequests.Load(), aliasRequests.Load())
	}
}

func TestPreviewRevalidatesAuthoritativeCreatorAliases(t *testing.T) {
	content := "{{Song box 2\n|lyrics=[[CanonicalP]] & [[SecondP]]\n|music=[[CanonicalP]]＆[[SecondP]]\n}}\noriginal song Lyrics\n== Lyrics ==\n歌う"
	sha1 := sha1Hex(content)
	var songRequests, aliasRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		switch {
		case query.Get("revids") == "34":
			songRequests.Add(1)
			writePageResponse(w, 12, 34, sha1, "新曲", content)
		case query.Get("gsrsearch") == "別名P":
			aliasRequests.Add(1)
			fmt.Fprint(w, `{"query":{"pages":{"44":{"pageid":44,"title":"CanonicalP","categories":[{"title":"Category:Vocaloid producers"}],"revisions":[{"revid":55,"sha1":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","slots":{"main":{"content":"|japanese=別名P"}}}]}}}}`)
		case query.Get("gsrsearch") == "第二P":
			aliasRequests.Add(1)
			fmt.Fprint(w, `{"query":{"pages":{"45":{"pageid":45,"title":"SecondP","categories":[{"title":"Category:Vocaloid producers"}],"revisions":[{"revid":56,"sha1":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","slots":{"main":{"content":"|japanese=第二P"}}}]}}}}`)
		default:
			http.Error(w, "unexpected request", http.StatusBadRequest)
		}
	}))
	defer server.Close()
	client := newTestClient(server.URL)
	preview, err := client.Preview(context.Background(), MusicIdentity{
		MusicID: 1, JapaneseTitle: "新曲", Lyricist: "別名P ＆ 第二P", Composer: "別名P & 第二P",
	}, 12, 34)
	if err != nil || len(preview.Lines) != 1 || preview.Lines[0].Japanese != "歌う" {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	if songRequests.Load() != 1 || aliasRequests.Load() != 2 {
		t.Fatalf("song requests=%d alias requests=%d", songRequests.Load(), aliasRequests.Load())
	}
}

func TestPreviewAndFetchFreshRevalidateCompletedCreatorAliasCache(t *testing.T) {
	content := "{{Song box 2\n|lyrics=[[CanonicalP]]\n|music=[[CanonicalP]]\n}}\noriginal song Lyrics\n== Lyrics ==\n歌う"
	sha1 := sha1Hex(content)
	identity := MusicIdentity{MusicID: 1, JapaneseTitle: "新曲", Lyricist: "別名P", Composer: "別名P"}

	for _, test := range []struct {
		name string
		run  func(*Client, Candidate) error
	}{
		{name: "preview", run: func(client *Client, candidate Candidate) error {
			_, err := client.Preview(context.Background(), identity, candidate.PageID, candidate.RevisionID)
			return err
		}},
		{name: "fixed fetch", run: func(client *Client, candidate Candidate) error {
			_, err := client.FetchFixedRevision(context.Background(), identity, candidate.PageID, candidate.RevisionID, candidate.SHA1)
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var aliasRequests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				query := r.URL.Query()
				switch {
				case query.Get("gsrsearch") == "新曲" || query.Get("gsrsearch") == `"新曲"`:
					writePageResponse(w, 12, 34, sha1, "新曲", content)
				case query.Get("gsrsearch") == "別名P":
					if aliasRequests.Add(1) == 1 {
						fmt.Fprint(w, `{"query":{"pages":{"44":{"pageid":44,"title":"CanonicalP","categories":[{"title":"Category:Vocaloid producers"}],"revisions":[{"revid":55,"sha1":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","slots":{"main":{"content":"|japanese=別名P"}}}]}}}}`)
						return
					}
					fmt.Fprint(w, `{"query":{"pages":{}}}`)
				case query.Get("revids") == "34":
					writePageResponse(w, 12, 34, sha1, "新曲", content)
				default:
					http.Error(w, "unexpected request", http.StatusBadRequest)
				}
			}))
			defer server.Close()

			client := newTestClient(server.URL)
			candidates, err := client.Search(context.Background(), identity)
			if err != nil || len(candidates) != 1 {
				t.Fatalf("priming search candidates=%+v err=%v", candidates, err)
			}
			if err := test.run(client, candidates[0]); !errors.Is(err, ErrAmbiguous) {
				t.Fatalf("fresh alias revalidation error=%v, want %v", err, ErrAmbiguous)
			}
			if got := aliasRequests.Load(); got != 2 {
				t.Fatalf("creator alias requests=%d, want cached Search success plus fresh Preview/Fetch revalidation", got)
			}
		})
	}
}

func TestFreshCreatorAliasMissesBypassCompletedCacheAndCoalesce(t *testing.T) {
	const callers = 8
	var requests atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			close(started)
		}
		<-release
		fmt.Fprint(w, `{"query":{"pages":{}}}`)
	}))
	defer func() {
		closeSignal(release)
		server.Close()
	}()
	client := newTestClient(server.URL)
	params := creatorSearchRequestParams("別名P")
	client.cache[params.Encode()] = cacheEntry{
		body:      []byte(`{"query":{"pages":{"44":{"pageid":44,"title":"CanonicalP","categories":[{"title":"Category:Vocaloid producers"}],"revisions":[{"revid":55,"sha1":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","slots":{"main":{"content":"|japanese=別名P"}}}]}}}}`),
		createdAt: time.Now(),
	}

	var ready sync.WaitGroup
	ready.Add(callers)
	start := make(chan struct{})
	errs := make(chan error, callers)
	for range callers {
		go func() {
			ready.Done()
			<-start
			_, err := client.requestFresh(context.Background(), "creator-alias", params)
			errs <- err
		}()
	}
	ready.Wait()
	close(start)
	awaitSignal(t, started, "fresh creator-alias request")
	awaitCondition(t, time.Second, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		inflight := client.inflight[params.Encode()]
		return inflight != nil && inflight.waiters == callers-1
	}, "fresh creator-alias callers to coalesce")
	closeSignal(release)
	for range callers {
		if err := awaitError(t, errs, "fresh creator-alias completion"); err != nil {
			t.Fatal(err)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("fresh creator-alias requests=%d, want one coalesced revalidation", got)
	}
}

func TestPreviewRejectsExcludedPrimaryVersion(t *testing.T) {
	content := "{{Song box 2\n|producers=作者 (music, lyrics)\n|type=Preview Version\n}}\n== Lyrics ==\n歌う"
	sha1 := sha1Hex(content)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writePageResponse(w, 12, 34, sha1, "新曲", content)
	}))
	defer server.Close()
	client := newTestClient(server.URL)
	_, err := client.Preview(context.Background(), MusicIdentity{
		MusicID: 1, JapaneseTitle: "新曲", ProducerMetadata: "作者",
	}, 12, 34)
	if !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("excluded primary version error=%v", err)
	}
}

func TestMalformedCreatorAliasResponseIsNotCached(t *testing.T) {
	content := "{{Song box 2\n|lyrics=[[CanonicalP]]\n|music=[[CanonicalP]]\n}}\noriginal song Lyrics\n== Lyrics ==\n歌う"
	sha1 := sha1Hex(content)
	var songRequests, aliasRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		switch {
		case query.Get("revids") == "34":
			songRequests.Add(1)
			writePageResponse(w, 12, 34, sha1, "新曲", content)
		case query.Get("gsrsearch") == "別名P":
			if aliasRequests.Add(1) == 1 {
				fmt.Fprint(w, `{"query":{}}`)
				return
			}
			fmt.Fprint(w, `{"query":{"pages":{"44":{"pageid":44,"title":"CanonicalP","categories":[{"title":"Category:Vocaloid producers"}],"revisions":[{"revid":55,"sha1":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","slots":{"main":{"content":"|japanese=別名P"}}}]}}}}`)
		default:
			http.Error(w, "unexpected request", http.StatusBadRequest)
		}
	}))
	defer server.Close()
	client := newTestClient(server.URL)
	identity := MusicIdentity{MusicID: 1, JapaneseTitle: "新曲", Lyricist: "別名P", Composer: "別名P"}
	if _, err := client.FetchFixedRevision(context.Background(), identity, 12, 34, sha1); !errors.Is(err, ErrMalformedResponse) {
		t.Fatalf("first malformed alias error=%v", err)
	}
	fixed, err := client.FetchFixedRevision(context.Background(), identity, 12, 34, sha1)
	if err != nil || len(fixed.Lines) != 1 || fixed.Lines[0].Japanese != "歌う" {
		t.Fatalf("recovered fixed=%+v err=%v", fixed, err)
	}
	if songRequests.Load() != 2 || aliasRequests.Load() != 2 {
		t.Fatalf("song requests=%d alias requests=%d", songRequests.Load(), aliasRequests.Load())
	}
}

func TestFetchFixedRevisionIsUncachedAndRejectsSHAOrContentDrift(t *testing.T) {
	const pageID, revisionID = 12, 34
	content := "作者 original song Lyrics\n== Lyrics ==\n歌う"
	sha1 := sha1Hex(content)
	var requests atomic.Int32
	var responseSHA1 atomic.Value
	var responseContent atomic.Value
	responseSHA1.Store(sha1)
	responseContent.Store(content)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Query().Get("revids") != strconv.Itoa(revisionID) || r.URL.Query().Has("pageids") || r.URL.Query().Has("rvlimit") {
			http.Error(w, "not exact revision", http.StatusBadRequest)
			return
		}
		writePageResponse(w, pageID, revisionID, responseSHA1.Load().(string), "新曲", responseContent.Load().(string))
	}))
	defer server.Close()
	client := newTestClient(server.URL)
	identity := MusicIdentity{MusicID: 1, JapaneseTitle: "新曲", ProducerMetadata: "作者"}
	for range 2 {
		fixed, err := client.FetchFixedRevision(context.Background(), identity, pageID, revisionID, sha1)
		if err != nil || string(fixed.Wikitext) != content || len(fixed.Lines) != 1 {
			t.Fatalf("fixed=%+v err=%v", fixed, err)
		}
	}
	if requests.Load() != 2 {
		t.Fatalf("fixed revision requests=%d want=2", requests.Load())
	}

	if _, err := client.FetchFixedRevision(context.Background(), identity, pageID, revisionID, strings.Repeat("b", 40)); !errors.Is(err, ErrRevisionChanged) {
		t.Fatalf("expected SHA drift error=%v", err)
	}
	responseSHA1.Store(strings.Repeat("b", 40))
	if _, err := client.FetchFixedRevision(context.Background(), identity, pageID, revisionID, sha1); !errors.Is(err, ErrRevisionChanged) {
		t.Fatalf("response SHA drift error=%v", err)
	}
	responseSHA1.Store(sha1)
	responseContent.Store(content + "\n改変")
	if _, err := client.FetchFixedRevision(context.Background(), identity, pageID, revisionID, sha1); !errors.Is(err, ErrRevisionChanged) {
		t.Fatalf("content drift error=%v", err)
	}
}

func TestFetchFixedCandidateRevisionRejectsReviewedMetadataDriftBeforeRestrictions(t *testing.T) {
	const pageID, revisionID = 12, 34
	content := "作者 original song Lyrics\n== Lyrics ==\n歌う"
	sha1 := sha1Hex(content)
	var title atomic.Value
	var categories atomic.Value
	title.Store("新曲")
	categories.Store([]string{"Lyrics"})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writePageResponseWithCategories(w, pageID, revisionID, sha1, title.Load().(string), content, categories.Load().([]string))
	}))
	defer server.Close()
	client := newTestClient(server.URL)
	identity := MusicIdentity{MusicID: 1, JapaneseTitle: "新曲", ProducerMetadata: "作者"}
	candidate := Candidate{PageID: pageID, RevisionID: revisionID, SHA1: sha1, Title: "新曲",
		CanonicalURL: canonicalURL("新曲", revisionID), Categories: []string{"Lyrics"}}
	if _, err := client.FetchFixedCandidateRevision(context.Background(), identity, candidate); err != nil {
		t.Fatalf("unchanged fixed candidate rejected: %v", err)
	}

	title.Store("新曲 (renamed)")
	if _, err := client.FetchFixedCandidateRevision(context.Background(), identity, candidate); !errors.Is(err, ErrRevisionChanged) {
		t.Fatalf("title/canonical URL drift error=%v", err)
	}
	title.Store("新曲")

	for name, driftedCategories := range map[string][]string{
		"ordinary category":    {"Lyrics", "Songs"},
		"restriction category": {"Lyrics", "Lyrics may not be reprinted"},
	} {
		t.Run(name, func(t *testing.T) {
			categories.Store(driftedCategories)
			_, err := client.FetchFixedCandidateRevision(context.Background(), identity, candidate)
			if !errors.Is(err, ErrRevisionChanged) || errors.Is(err, ErrRestrictedReprint) {
				t.Fatalf("category drift error=%v", err)
			}
		})
	}
}

func TestFetchFixedRevisionRejectsDeadlineObservedAfterExtraction(t *testing.T) {
	const pageID, revisionID = 12, 34
	content := "作者 original song Lyrics\n== Lyrics ==\n歌う"
	sha1 := sha1Hex(content)
	response := fmt.Sprintf(`{"query":{"pages":{"%d":{"pageid":%d,"title":"新曲","categories":[{"title":"Category:Lyrics"}],"revisions":[{"revid":%d,"sha1":%q,"slots":{"main":{"content":%q}}}]}}}}`,
		pageID, pageID, revisionID, sha1, content)
	newClient := func() *Client {
		client := newTestClient("http://lyrics.test/api.php")
		client.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(response)),
			}, nil
		})}
		return client
	}
	identity := MusicIdentity{MusicID: 1, JapaneseTitle: "新曲", ProducerMetadata: "作者"}
	measureCtx := &deadlineOnErrCallContext{Context: context.Background(), failAt: 1 << 30}
	if _, err := newClient().FetchFixedRevision(measureCtx, identity, pageID, revisionID, sha1); err != nil {
		t.Fatal(err)
	}
	ctx := &deadlineOnErrCallContext{Context: context.Background(), failAt: measureCtx.calls.Load()}
	fixed, err := newClient().FetchFixedRevision(ctx, identity, pageID, revisionID, sha1)
	if !errors.Is(err, context.DeadlineExceeded) || fixed.PageID != 0 || fixed.Wikitext != nil || fixed.Lines != nil {
		t.Fatalf("fixed=%+v deadline error=%v", fixed, err)
	}
	if got := ctx.calls.Load(); got != ctx.failAt {
		t.Fatalf("context Err calls=%d, want final post-extraction check %d", got, ctx.failAt)
	}
}

func TestSearchAndPreviewRejectDeadlineObservedAfterCPUWork(t *testing.T) {
	const pageID, revisionID = 12, 34
	content := "作者 original song Lyrics\n== Lyrics ==\n歌う"
	sha1 := sha1Hex(content)
	response := fmt.Sprintf(`{"query":{"pages":{"%d":{"pageid":%d,"title":"新曲","categories":[{"title":"Category:Lyrics"}],"revisions":[{"revid":%d,"sha1":%q,"slots":{"main":{"content":%q}}}]}}}}`,
		pageID, pageID, revisionID, sha1, content)
	newClient := func() *Client {
		client := newTestClient("http://lyrics.test/api.php")
		client.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(response)),
			}, nil
		})}
		return client
	}
	identity := MusicIdentity{MusicID: 1, JapaneseTitle: "新曲", ProducerMetadata: "作者"}

	for _, test := range []struct {
		name string
		run  func(context.Context, *Client) (bool, error)
	}{
		{name: "search", run: func(ctx context.Context, client *Client) (bool, error) {
			candidates, err := client.Search(ctx, identity)
			return len(candidates) > 0, err
		}},
		{name: "search with diagnostics", run: func(ctx context.Context, client *Client) (bool, error) {
			candidates, _, err := client.SearchWithDiagnostics(ctx, identity)
			return len(candidates) > 0, err
		}},
		{name: "preview", run: func(ctx context.Context, client *Client) (bool, error) {
			preview, err := client.Preview(ctx, identity, pageID, revisionID)
			return preview.PageID != 0, err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			measureCtx := &deadlineOnErrCallContext{Context: context.Background(), failAt: 1 << 30}
			if populated, err := test.run(measureCtx, newClient()); err != nil || !populated {
				t.Fatalf("measurement populated=%t err=%v", populated, err)
			}
			ctx := &deadlineOnErrCallContext{Context: context.Background(), failAt: measureCtx.calls.Load()}
			populated, err := test.run(ctx, newClient())
			if !errors.Is(err, context.DeadlineExceeded) || populated {
				t.Fatalf("post-CPU result populated=%t err=%v", populated, err)
			}
			if got := ctx.calls.Load(); got != ctx.failAt {
				t.Fatalf("context Err calls=%d, want final CPU-side check %d", got, ctx.failAt)
			}
		})
	}
}

func TestExcludedVersionSignalUsesOnlyPrimaryPageIdentity(t *testing.T) {
	for name, test := range map[string]struct {
		title      string
		content    string
		categories []string
	}{
		"title":                  {title: "新曲 (Game Size)", content: "作者 original song\n== Lyrics ==\n歌う"},
		"category":               {title: "新曲", content: "作者 original song\n== Lyrics ==\n歌う", categories: []string{"Cover versions"}},
		"lead metadata":          {title: "新曲", content: "{{Song box 2\n|type=Preview Version\n}}\n== Lyrics ==\n歌う"},
		"Japanese lead metadata": {title: "新曲", content: "{{Song box 2\n|type=再録\n}}\n== Lyrics ==\n歌う"},
		"Chinese lead metadata":  {title: "新曲", content: "{{Song box 2\n|type=遊戲版\n}}\n== Lyrics ==\n歌う"},
	} {
		t.Run(name, func(t *testing.T) {
			if !hasExcludedVersionSignal(test.title, test.content, test.categories) {
				t.Fatal("excluded primary version identity was not detected")
			}
		})
	}

	content := "{{Song box 2\n|producers=作者 (music, lyrics)\n}}\noriginal song\n== Lyrics ==\n歌う\n== Succeeding versions ==\nA cover version, game-size edit, preview version, and medley were later released."
	if hasExcludedVersionSignal("新曲", content, []string{"Original songs"}) {
		t.Fatal("later derivative-version documentation rejected the primary full page")
	}
}

func TestFetchFixedRevisionAcceptsFullPageWithLaterDerivativeVersions(t *testing.T) {
	content := "{{Song box 2\n|producers=作者 (music, lyrics)\n}}\noriginal song\n== Lyrics ==\n歌う\n== Succeeding versions ==\nA cover version, game-size edit, preview version, and medley were later released."
	sha1 := sha1Hex(content)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writePageResponse(w, 12, 34, sha1, "新曲", content)
	}))
	defer server.Close()
	client := newTestClient(server.URL)
	fixed, err := client.FetchFixedRevision(context.Background(), MusicIdentity{MusicID: 1, JapaneseTitle: "新曲", ProducerMetadata: "作者",
		Lyricist: "作者", Composer: "作者"}, 12, 34, sha1)
	if err != nil {
		t.Fatal(err)
	}
	if !equalExtractedLines(fixed.Lines, []ExtractedLine{{Japanese: "歌う"}}) {
		t.Fatalf("full-page lyrics = %+v", fixed.Lines)
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

func TestSearchAcceptsCanonicalMediaWikiNoResults(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Query().Get("gsrsearch") {
		case "新曲", `"新曲"`:
			fmt.Fprint(w, `{"batchcomplete":"","limits":{"categories":500}}`)
		default:
			http.Error(w, "unexpected search", http.StatusBadRequest)
		}
	}))
	defer server.Close()
	client := newTestClient(server.URL)
	result, diagnostics, err := client.SearchWithDiagnostics(context.Background(), MusicIdentity{JapaneseTitle: "新曲", ProducerMetadata: "作者"})
	if err != nil || len(result) != 0 || diagnostics.SearchHits != 0 {
		t.Fatalf("zero-result search=%+v diagnostics=%+v err=%v", result, diagnostics, err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("zero-result requests=%d, want one unquoted and one quoted search", got)
	}
}

func TestSearchWithDiagnosticsUsesQuotedFallbackAfterUnquotedNoResults(t *testing.T) {
	const sha1 = "0123456789abcdef0123456789abcdef01234567"
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		query := r.URL.Query()
		if query.Get("gsrlimit") != strconv.Itoa(maxSearchPages) || query.Get("generator") != "search" ||
			query.Get("maxlag") != mediaWikiMaxLag || query.Has("gsroffset") {
			http.Error(w, "unbounded search", http.StatusBadRequest)
			return
		}
		switch query.Get("gsrsearch") {
		case "新曲":
			fmt.Fprint(w, `{"batchcomplete":"","limits":{"categories":500}}`)
		case `"新曲"`:
			fmt.Fprintf(w, `{"query":{"pages":{"12":{"pageid":12,"title":"新曲/作者","categories":[{"title":"Category:Songs"}],"revisions":[{"revid":34,"sha1":%q,"slots":{"main":{"content":"作者 original song Lyrics"}}}]}}}}`, sha1)
		default:
			http.Error(w, "unexpected search", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	candidates, diagnostics, err := newTestClient(server.URL).SearchWithDiagnostics(context.Background(), MusicIdentity{
		JapaneseTitle: "新曲", ProducerMetadata: "作者",
	})
	if err != nil || len(candidates) != 1 || candidates[0].PageID != 12 {
		t.Fatalf("quoted fallback candidates=%+v diagnostics=%+v err=%v", candidates, diagnostics, err)
	}
	if diagnostics.SearchHits != 1 || diagnostics.Verified != 1 {
		t.Fatalf("quoted fallback diagnostics=%+v", diagnostics)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("quoted fallback requests=%d, want 2", got)
	}
}

func TestSearchUsesBoundedQuotedAndCreatorAliasRecovery(t *testing.T) {
	const sha1 = "0123456789abcdef0123456789abcdef01234567"
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		query := r.URL.Query()
		switch query.Get("gsrsearch") {
		case "新曲":
			if query.Get("gsrlimit") != strconv.Itoa(maxSearchPages) {
				http.Error(w, "unbounded title search", http.StatusBadRequest)
				return
			}
			fmt.Fprint(w, `{"batchcomplete":"","limits":{"categories":500}}`)
		case `"新曲"`:
			if query.Get("gsrlimit") != strconv.Itoa(maxSearchPages) {
				http.Error(w, "unbounded exact-title search", http.StatusBadRequest)
				return
			}
			fmt.Fprintf(w, `{"query":{"pages":{"12":{"pageid":12,"title":"新曲","categories":[{"title":"Category:Songs"}],"revisions":[{"revid":34,"sha1":%q,"slots":{"main":{"content":"Lyrics: CanonicalP\nMusic: CanonicalP"}}}]}}}}`, sha1)
		case "別名P":
			if query.Get("gsrlimit") != "3" {
				http.Error(w, "unbounded creator alias search", http.StatusBadRequest)
				return
			}
			fmt.Fprint(w, `{"query":{"pages":{"44":{"pageid":44,"title":"CanonicalP","categories":[{"title":"Category:Vocaloid producers"}],"revisions":[{"revid":55,"sha1":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","slots":{"main":{"content":"|japanese=別名P"}}}]}}}}`)
		default:
			http.Error(w, "unexpected search", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	candidates, err := newTestClient(server.URL).Search(context.Background(), MusicIdentity{
		JapaneseTitle: "新曲", Lyricist: "別名P", Composer: "別名P",
	})
	if err != nil || len(candidates) != 1 || candidates[0].PageID != 12 {
		t.Fatalf("production search candidates=%+v err=%v", candidates, err)
	}
	if got := requests.Load(); got != 3 {
		t.Fatalf("production search requests=%d, want bounded unquoted, quoted, and alias phases", got)
	}
}

func TestSearchRejectsArbitraryMissingQueryShapes(t *testing.T) {
	for _, body := range []string{`{}`, `{"limits":{"categories":500}}`, `{"query":null}`, `{"query":{}}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, body) }))
			defer server.Close()
			client := newTestClient(server.URL)
			if _, err := client.Search(context.Background(), MusicIdentity{JapaneseTitle: "新曲", ProducerMetadata: "作者"}); !errors.Is(err, ErrMalformedResponse) {
				t.Fatalf("body=%s error=%v", body, err)
			}
		})
	}
}

func TestMalformedSearchResponseIsNotPersistentlyCached(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			fmt.Fprint(w, `{"query":{}}`)
			return
		}
		fmt.Fprint(w, `{"query":{"pages":{}}}`)
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
	if got := requests.Load(); got != 3 {
		t.Fatalf("malformed search requests=%d, want malformed initial fetch plus bounded unquoted and quoted recovery", got)
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

func TestNewClientUsesOneSecondPerClientRequestPolicy(t *testing.T) {
	first := New()
	second := New()
	if defaultRequestInterval != time.Second {
		t.Fatalf("default request interval=%v, want %v", defaultRequestInterval, time.Second)
	}
	for index, client := range []*Client{first, second} {
		if client.minInterval != defaultRequestInterval {
			t.Fatalf("client %d minimum interval=%v, want %v", index, client.minInterval, defaultRequestInterval)
		}
		if client.httpClient == nil || client.httpClient.Timeout != 12*time.Second || client.httpClient.Transport == nil ||
			client.httpClient.Transport == http.DefaultTransport || client.cacheTTL != 2*time.Minute {
			t.Fatalf("client %d constructor policy http=%+v cacheTTL=%v", index, client.httpClient, client.cacheTTL)
		}
		actualHTTP := client.actualHTTPRequestSemaphore()
		if cap(client.requestSlots) != maxInflightRequests || len(client.requestSlots) != 0 || cap(client.rateToken) != 1 || len(client.rateToken) != 1 ||
			cap(actualHTTP) != 1 || len(actualHTTP) != 1 {
			t.Fatalf("client %d request slots=%d/%d rate token=%d/%d actual HTTP token=%d/%d", index,
				len(client.requestSlots), cap(client.requestSlots), len(client.rateToken), cap(client.rateToken), len(actualHTTP), cap(actualHTTP))
		}
		if !client.lastRequest.IsZero() || !client.cooldownUntil.IsZero() {
			t.Fatalf("client %d initial rate state last=%v cooldown=%v", index, client.lastRequest, client.cooldownUntil)
		}
	}
	first.extendCooldown("60", time.Now())
	if second.cooldownUntil != (time.Time{}) {
		t.Fatalf("per-client cooldown leaked to second client: %v", second.cooldownUntil)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{name: "missing", value: "", want: retryAfterFallback},
		{name: "whitespace missing", value: "  ", want: retryAfterFallback},
		{name: "zero delta", value: "0", want: 0},
		{name: "leading zero delta", value: "00060", want: time.Minute},
		{name: "five minute delta", value: "300", want: 5 * time.Minute},
		{name: "delta above former cap", value: "301", want: 301 * time.Second},
		{name: "provider multi-hour delta", value: "7200", want: 2 * time.Hour},
		{name: "huge unsigned delta saturates only at representation limit", value: strings.Repeat("9", 1000), want: maximumRetryAfterDelay},
		{name: "future HTTP date", value: now.Add(90 * time.Second).Format(http.TimeFormat), want: 90 * time.Second},
		{name: "future HTTP date after former cap", value: now.Add(24 * time.Hour).Format(http.TimeFormat), want: 24 * time.Hour},
		{name: "equal HTTP date", value: now.Format(http.TimeFormat), want: 0},
		{name: "past HTTP date", value: now.Add(-time.Hour).Format(http.TimeFormat), want: 0},
		{name: "obsolete HTTP date", value: now.Add(30 * time.Second).Format(time.RFC850), want: 30 * time.Second},
		{name: "signed delta", value: "+1", want: retryAfterFallback},
		{name: "negative delta", value: "-1", want: retryAfterFallback},
		{name: "fractional delta", value: "1.5", want: retryAfterFallback},
		{name: "capped numeric prefix with suffix", value: "301x", want: retryAfterFallback},
		{name: "huge numeric prefix with suffix", value: strings.Repeat("9", 1000) + "x", want: retryAfterFallback},
		{name: "malformed date", value: "tomorrow", want: retryAfterFallback},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := parseRetryAfter(test.value, now); got != test.want {
				t.Fatalf("parseRetryAfter(%q)=%v, want %v", test.value, got, test.want)
			}
		})
	}
}

func TestRequest429SetsSharedAbsoluteCooldownAndPreservesHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "7200")
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))
	defer server.Close()
	client := newTestClient(server.URL)
	before := time.Now()
	_, err := client.request(context.Background(), "rate-limited", url.Values{"key": {"429"}}, false)
	after := time.Now()
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("429 error=%v status=%+v", err, httpErr)
	}
	client.rateMu.Lock()
	cooldown := client.cooldownUntil
	client.rateMu.Unlock()
	if cooldown.Before(before.Add(2*time.Hour)) || cooldown.After(after.Add(2*time.Hour)) {
		t.Fatalf("429 cooldown=%v, want response time + 2h in [%v,%v]", cooldown, before.Add(2*time.Hour), after.Add(2*time.Hour))
	}
	client.extendCooldown("1", after)
	client.rateMu.Lock()
	shortened := client.cooldownUntil
	client.rateMu.Unlock()
	if !shortened.Equal(cooldown) {
		t.Fatalf("later cooldown shortened from %v to %v", cooldown, shortened)
	}
}

func TestMediaWikiMaxLagResponseUsesRateLimitedContractAndCooldown(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if query := r.URL.Query(); query.Get("action") != "query" || query.Get("maxlag") != mediaWikiMaxLag {
			http.Error(w, "missing maxlag", http.StatusBadRequest)
			return
		}
		w.Header().Set("Retry-After", "7200")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"error":{"code":"maxlag","info":"server load","lag":8}}`)
	}))
	defer server.Close()

	client := newTestClient(server.URL)
	before := time.Now()
	candidates, err := client.Search(context.Background(), MusicIdentity{JapaneseTitle: "新曲", ProducerMetadata: "作者"})
	after := time.Now()
	var httpErr *HTTPError
	if candidates != nil || !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusTooManyRequests ||
		errors.Is(err, ErrMalformedResponse) {
		t.Fatalf("maxlag candidates=%+v error=%v status=%+v", candidates, err, httpErr)
	}
	client.rateMu.Lock()
	cooldown := client.cooldownUntil
	client.rateMu.Unlock()
	if cooldown.Before(before.Add(2*time.Hour)) || cooldown.After(after.Add(2*time.Hour)) {
		t.Fatalf("maxlag cooldown=%v, want response time + 2h in [%v,%v]", cooldown, before.Add(2*time.Hour), after.Add(2*time.Hour))
	}
	client.mu.Lock()
	cacheLength := len(client.cache)
	client.mu.Unlock()
	if requests.Load() != 1 || cacheLength != 0 {
		t.Fatalf("maxlag requests=%d cache entries=%d", requests.Load(), cacheLength)
	}
}

func TestRequest503HonorsRetryAfterAndCancellationPreservesCooldown(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Retry-After", "7200")
		http.Error(w, "loaded", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	client := newTestClient(server.URL)
	before := time.Now()
	_, err := client.request(context.Background(), "loaded", url.Values{"action": {"query"}, "key": {"first"}}, false)
	after := time.Now()
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("503 error=%v status=%+v", err, httpErr)
	}
	client.rateMu.Lock()
	cooldown := client.cooldownUntil
	client.rateMu.Unlock()
	if cooldown.Before(before.Add(2*time.Hour)) || cooldown.After(after.Add(2*time.Hour)) {
		t.Fatalf("503 cooldown=%v, want response time + 2h in [%v,%v]", cooldown, before.Add(2*time.Hour), after.Add(2*time.Hour))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	canceled := make(chan error, 1)
	go func() {
		_, requestErr := client.request(ctx, "loaded", url.Values{"action": {"query"}, "key": {"second"}}, false)
		canceled <- requestErr
	}()
	awaitCondition(t, time.Second, func() bool { return len(client.rateToken) == 0 }, "503 cooldown waiter")
	cancel()
	if requestErr := awaitError(t, canceled, "503 cooldown cancellation"); !errors.Is(requestErr, context.Canceled) {
		t.Fatalf("503 cooldown cancellation error=%v", requestErr)
	}
	client.rateMu.Lock()
	afterCancellation := client.cooldownUntil
	client.rateMu.Unlock()
	if !afterCancellation.Equal(cooldown) || requests.Load() != 1 {
		t.Fatalf("503 cooldown changed from %v to %v or extra requests=%d", cooldown, afterCancellation, requests.Load())
	}
}

func TestRequest429And503UseConservativeRetryAfterFallback(t *testing.T) {
	for _, test := range []struct {
		name       string
		statusCode int
		retryAfter string
	}{
		{name: "429 missing", statusCode: http.StatusTooManyRequests},
		{name: "503 malformed", statusCode: http.StatusServiceUnavailable, retryAfter: "tomorrow"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if test.retryAfter != "" {
					w.Header().Set("Retry-After", test.retryAfter)
				}
				http.Error(w, "retry later", test.statusCode)
			}))
			defer server.Close()

			client := newTestClient(server.URL)
			before := time.Now()
			_, err := client.request(context.Background(), "fallback", url.Values{"key": {test.name}}, false)
			after := time.Now()
			var httpErr *HTTPError
			if !errors.As(err, &httpErr) || httpErr.StatusCode != test.statusCode {
				t.Fatalf("fallback error=%v status=%+v", err, httpErr)
			}
			client.rateMu.Lock()
			cooldown := client.cooldownUntil
			client.rateMu.Unlock()
			if cooldown.Before(before.Add(retryAfterFallback)) || cooldown.After(after.Add(retryAfterFallback)) {
				t.Fatalf("fallback cooldown=%v, want response time + %v in [%v,%v]", cooldown, retryAfterFallback,
					before.Add(retryAfterFallback), after.Add(retryAfterFallback))
			}
		})
	}
}

func TestConcurrentCooldownExtensionsNeverShorten(t *testing.T) {
	now := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
	client := New()
	values := []string{"0", "1", "60", "299", "300", "120", "bad", strings.Repeat("9", 100)}
	var ready sync.WaitGroup
	ready.Add(len(values))
	start := make(chan struct{})
	var complete sync.WaitGroup
	complete.Add(len(values))
	for _, value := range values {
		value := value
		go func() {
			defer complete.Done()
			ready.Done()
			<-start
			client.extendCooldown(value, now)
		}()
	}
	ready.Wait()
	close(start)
	complete.Wait()
	client.rateMu.Lock()
	cooldown := client.cooldownUntil
	client.rateMu.Unlock()
	if want := now.Add(maximumRetryAfterDelay); !cooldown.Equal(want) {
		t.Fatalf("concurrent cooldown=%v, want maximum representable %v", cooldown, want)
	}
	client.extendCooldown("0", now.Add(time.Hour))
	client.rateMu.Lock()
	afterZero := client.cooldownUntil
	client.rateMu.Unlock()
	if !afterZero.Equal(cooldown) {
		t.Fatalf("valid zero Retry-After changed cooldown from %v to %v", cooldown, afterZero)
	}
}

func TestCacheHitRemainsImmediateDuringCooldown(t *testing.T) {
	client := New()
	client.endpoint = "://invalid"
	params := url.Values{"cache": {"cooldown-hit"}}
	client.cache[params.Encode()] = cacheEntry{body: []byte("cached"), createdAt: time.Now()}
	client.extendCooldown("300", time.Now())
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	body, err := client.request(ctx, "cache", params, true)
	if err != nil || string(body) != "cached" {
		t.Fatalf("cooldown cache hit body=%q err=%v", body, err)
	}
	if len(client.requestSlots) != 0 || len(client.rateToken) != 1 {
		t.Fatalf("cache hit consumed request state slots=%d token=%d", len(client.requestSlots), len(client.rateToken))
	}
}

func TestQueuedRequestRecomputesCooldownExtensionAndCancellationReleasesState(t *testing.T) {
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondStarted := make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Query().Get("key") {
		case "first":
			close(firstStarted)
			<-releaseFirst
			w.Header().Set("Retry-After", "1")
			http.Error(w, "slow down", http.StatusTooManyRequests)
		case "second":
			close(secondStarted)
			fmt.Fprint(w, "unexpected")
		default:
			http.Error(w, "unexpected request", http.StatusBadRequest)
		}
	}))
	defer func() {
		closeSignal(releaseFirst)
		server.Close()
	}()
	client := newTestClient(server.URL)
	client.minInterval = 75 * time.Millisecond

	firstErr := make(chan error, 1)
	go func() {
		_, err := client.request(context.Background(), "cooldown", url.Values{"key": {"first"}}, true)
		firstErr <- err
	}()
	awaitSignal(t, firstStarted, "first cooldown request")

	secondCtx, cancelSecond := context.WithCancel(context.Background())
	defer cancelSecond()
	secondErr := make(chan error, 1)
	go func() {
		_, err := client.request(secondCtx, "cooldown", url.Values{"key": {"second"}}, true)
		secondErr <- err
	}()
	awaitCondition(t, time.Second, func() bool {
		return len(client.requestSlots) == 2 && len(client.actualHTTPRequestSemaphore()) == 0
	}, "second request to queue behind the active HTTP request")
	closeSignal(releaseFirst)
	var firstHTTPError *HTTPError
	if err := awaitError(t, firstErr, "first 429 completion"); !errors.As(err, &firstHTTPError) || firstHTTPError.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("first cooldown error=%v", err)
	}

	select {
	case <-secondStarted:
		t.Fatal("queued request ignored the extended shared cooldown")
	case <-time.After(3 * client.minInterval):
	}
	cancelSecond()
	if err := awaitError(t, secondErr, "queued cooldown cancellation"); !errors.Is(err, context.Canceled) {
		t.Fatalf("queued cooldown cancellation error=%v", err)
	}
	awaitCondition(t, time.Second, func() bool {
		client.mu.Lock()
		inflight := len(client.inflight)
		client.mu.Unlock()
		return inflight == 0 && len(client.requestSlots) == 0 && len(client.rateToken) == 1 &&
			len(client.actualHTTPRequestSemaphore()) == 1
	}, "canceled cooldown request state release")
	if got := requests.Load(); got != 1 {
		t.Fatalf("upstream requests=%d, want only the 429 request", got)
	}
}

func TestNewClientTransportAllowsMediaWikiQueryAndExactOriginRedirect(t *testing.T) {
	t.Setenv("MOESEKAI_PRODUCTION", "false")
	t.Setenv(httpx.UpstreamAllowInsecureLocalEnv, "true")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("maxlag") != mediaWikiMaxLag {
			http.Error(w, "missing MediaWiki maxlag", http.StatusBadRequest)
			return
		}
		if r.URL.Query().Get("redirected") == "" {
			http.Redirect(w, r, "/api.php?action=query&redirected=1", http.StatusFound)
			return
		}
		if r.URL.Query().Get("action") != "query" {
			http.Error(w, "missing MediaWiki query", http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, "ok")
	}))
	defer server.Close()
	client := New()
	client.endpoint = server.URL + "/api.php"
	client.minInterval = 0
	body, err := client.request(context.Background(), "query", url.Values{"action": {"query"}}, false)
	if err != nil || string(body) != "ok" {
		t.Fatalf("secure constructor query/redirect body=%q err=%v", body, err)
	}
}

func TestRequestPreservesEndpointQueryParameters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("base") != "1" || r.URL.Query().Get("action") != "query" ||
			r.URL.Query().Get("maxlag") != mediaWikiMaxLag {
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

func TestRequestBoundsDistinctInflightWorkAndSerializesActualHTTP(t *testing.T) {
	var requests atomic.Int32
	var active atomic.Int32
	var maximumActive atomic.Int32
	firstStarted := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			close(firstStarted)
		}
		current := active.Add(1)
		for {
			observed := maximumActive.Load()
			if current <= observed || maximumActive.CompareAndSwap(observed, current) {
				break
			}
		}
		defer active.Add(-1)
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
	awaitSignal(t, firstStarted, "first distinct HTTP request")
	awaitCondition(t, time.Second, func() bool { return len(client.requestSlots) == maxInflightRequests }, "bounded in-flight work slots")
	if got := requests.Load(); got != 1 || maximumActive.Load() != 1 {
		t.Fatalf("serialized upstream requests=%d maximum active=%d, want 1/1", got, maximumActive.Load())
	}

	queuedCtx, cancelQueued := context.WithCancel(context.Background())
	defer cancelQueued()
	queuedErr := make(chan error, 1)
	go func() {
		_, err := client.request(queuedCtx, "bounded", url.Values{"key": {"queued"}}, true)
		queuedErr <- err
	}()
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
	if inflightCount != 0 || maximumActive.Load() != 1 {
		t.Fatalf("after release in-flight work=%d maximum active HTTP=%d", inflightCount, maximumActive.Load())
	}
	if _, err := client.request(context.Background(), "bounded", url.Values{"key": {"recovered"}}, true); err != nil {
		t.Fatalf("request after bounded queue recovery = %v", err)
	}
	if got, want := requests.Load(), int32(maxInflightRequests+1); got != want {
		t.Fatalf("upstream requests after recovery = %d, want %d", got, want)
	}
}

func TestClientSerializesFixedRevisionHTTPRequests(t *testing.T) {
	var requests atomic.Int32
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestNumber := requests.Add(1)
		if requestNumber == 1 {
			close(firstStarted)
			<-releaseFirst
		}
		pageID, _ := strconv.Atoi(r.URL.Query().Get("revids"))
		writePageResponse(w, pageID, pageID, "0123456789abcdef0123456789abcdef01234567", "新曲", "作者 original song Lyrics\n== Lyrics ==\n歌う")
	}))
	defer func() {
		closeSignal(releaseFirst)
		server.Close()
	}()
	client := newTestClient(server.URL)

	firstErr := make(chan error, 1)
	go func() {
		_, err := client.fetchPage(context.Background(), 1, 1, false)
		firstErr <- err
	}()
	awaitSignal(t, firstStarted, "first fixed revision request")

	secondCtx, cancelSecond := context.WithCancel(context.Background())
	defer cancelSecond()
	secondErr := make(chan error, 1)
	go func() {
		_, err := client.fetchPage(secondCtx, 2, 2, false)
		secondErr <- err
	}()
	awaitCondition(t, time.Second, func() bool {
		return len(client.requestSlots) == 2 && len(client.actualHTTPRequestSemaphore()) == 0
	}, "second fixed revision to queue behind active HTTP")
	if got := requests.Load(); got != 1 {
		t.Fatalf("fixed revision HTTP requests in flight=%d, want one", got)
	}
	cancelSecond()
	if err := awaitError(t, secondErr, "queued fixed revision cancellation"); !errors.Is(err, context.Canceled) {
		t.Fatalf("queued fixed revision error=%v", err)
	}
	closeSignal(releaseFirst)
	if err := awaitError(t, firstErr, "first fixed revision completion"); err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("fixed revision upstream requests=%d, want only the completed first request", got)
	}
}

func TestDistinctClientsCanRunHTTPRequestsInParallel(t *testing.T) {
	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	release := make(chan struct{})
	firstServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(firstStarted)
		<-release
		fmt.Fprint(w, "first")
	}))
	defer firstServer.Close()
	secondServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(secondStarted)
		<-release
		fmt.Fprint(w, "second")
	}))
	defer secondServer.Close()
	defer closeSignal(release)

	firstErr := make(chan error, 1)
	secondErr := make(chan error, 1)
	go func() {
		_, err := newTestClient(firstServer.URL).request(context.Background(), "parallel", url.Values{"key": {"first"}}, false)
		firstErr <- err
	}()
	go func() {
		_, err := newTestClient(secondServer.URL).request(context.Background(), "parallel", url.Values{"key": {"second"}}, false)
		secondErr <- err
	}()
	awaitSignal(t, firstStarted, "first provider client HTTP request")
	awaitSignal(t, secondStarted, "second provider client HTTP request")
	closeSignal(release)
	if err := awaitError(t, firstErr, "first provider client completion"); err != nil {
		t.Fatal(err)
	}
	if err := awaitError(t, secondErr, "second provider client completion"); err != nil {
		t.Fatal(err)
	}
}

func TestRequestDeduplicatesConcurrent429MissesAndSharesCooldown(t *testing.T) {
	const callers = 8
	var requests atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			close(started)
		}
		<-release
		w.Header().Set("Retry-After", "300")
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))
	defer func() {
		closeSignal(release)
		server.Close()
	}()
	client := newTestClient(server.URL)
	params := url.Values{"cache": {"coalesced-429"}}
	results := make(chan error, callers)
	var ready sync.WaitGroup
	ready.Add(callers)
	start := make(chan struct{})
	for range callers {
		go func() {
			ready.Done()
			<-start
			_, err := client.request(context.Background(), "cache", params, true)
			results <- err
		}()
	}
	ready.Wait()
	close(start)
	awaitSignal(t, started, "deduplicated 429 owner")
	awaitCondition(t, time.Second, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		inflight := client.inflight[params.Encode()]
		return inflight != nil && inflight.waiters == callers-1
	}, "all 429 callers to deduplicate")
	closeSignal(release)
	for range callers {
		var httpErr *HTTPError
		if err := awaitError(t, results, "deduplicated 429 completion"); !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("deduplicated 429 error=%v", err)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("deduplicated 429 upstream requests=%d, want 1", got)
	}
	client.mu.Lock()
	cacheLength := len(client.cache)
	inflightCount := len(client.inflight)
	client.mu.Unlock()
	client.rateMu.Lock()
	cooldownRemaining := time.Until(client.cooldownUntil)
	client.rateMu.Unlock()
	if cacheLength != 0 || inflightCount != 0 || cooldownRemaining <= 4*time.Minute || cooldownRemaining > 5*time.Minute {
		t.Fatalf("deduplicated 429 state cache=%d inflight=%d cooldownRemaining=%v", cacheLength, inflightCount, cooldownRemaining)
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

func TestCoalescedFetchedAtRemainsBoundToCompletedAcquisitionAfterCacheReplacement(t *testing.T) {
	client := New()
	key := url.Values{"cache": {"fetched-at-identity"}}.Encode()
	workCtx, cancel := context.WithCancel(context.Background())
	inflight := &inflightRequest{
		done: make(chan struct{}), participants: 1, ctx: workCtx, cancel: cancel,
	}
	client.mu.Lock()
	client.inflight[key] = inflight
	client.mu.Unlock()

	body := []byte("same immutable response")
	originalFetchedAt := time.Date(2026, time.July, 31, 12, 30, 0, 0, time.UTC)
	client.finishCachedRequest(key, inflight, body, originalFetchedAt, nil)
	if !inflight.fetchedAt.Equal(originalFetchedAt) {
		t.Fatal("completed acquisition did not retain its fetched-at identity")
	}

	replacementFetchedAt := originalFetchedAt.Add(time.Hour)
	client.mu.Lock()
	client.cache[key] = cacheEntry{body: append([]byte(nil), body...), createdAt: replacementFetchedAt}
	client.mu.Unlock()

	gotBody, gotFetchedAt, err := client.cachedParticipantResult(inflight, false)
	if err != nil || !bytes.Equal(gotBody, body) || !gotFetchedAt.Equal(originalFetchedAt) || gotFetchedAt.Equal(replacementFetchedAt) {
		t.Fatalf("participant body=%q fetchedAt=%v replacement=%v err=%v",
			gotBody, gotFetchedAt, replacementFetchedAt, err)
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
		fmt.Fprint(w, `{"query":{"pages":{}}}`)
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
		fmt.Fprint(w, `{"query":{"pages":{}}}`)
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
	var requests atomic.Int32
	started := make(chan struct{})
	canceled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		close(started)
		<-r.Context().Done()
		close(canceled)
	}))
	server.Client().Timeout = 3 * time.Second
	client := newTestClient(server.URL)
	client.httpClient = server.Client()
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
	awaitSignal(t, started, "bulk discovery request")
	cancel()
	awaitSignal(t, canceled, "bulk discovery cancellation")
	if err := awaitError(t, searchErr, "canceled search"); !errors.Is(err, context.Canceled) {
		t.Fatalf("search error = %v", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("bulk discovery requests after cancellation = %d, want 1", got)
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
		fmt.Fprint(w, `{"query":{"pages":{}}}`)
	}))
	defer server.Close()

	client := newTestClient(server.URL)
	params := searchRequestParams("新曲")
	params.Set("step", "0")
	body, err := client.request(context.Background(), "search", params, false)
	if err != nil {
		t.Fatalf("request with %d redirects failed: %v", maxSourceRedirects, err)
	}
	if got, want := string(body), `{"query":{"pages":{}}}`; got != want {
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
	if !verifyCandidate(identity, "新曲", "作者による original song の Lyrics", []string{"Songs"}) {
		t.Fatal("verified source candidate was rejected")
	}
	if !verifyCandidate(identity, "新曲/作者", "作者 original song Lyrics", []string{"Songs"}) {
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
	roleBound := MusicIdentity{JapaneseTitle: "新曲", ProducerMetadata: "作詞者 | 作曲者 | 編曲者",
		Lyricist: "作詞者", Composer: "作曲者", Arranger: "編曲者"}
	for name, content := range map[string]string{
		"MediaWiki parameters":  "{{Song\n|lyrics=[[作詞者]]\n|music=作曲者\n|arrangement=編曲者\n}}\noriginal song Lyrics",
		"English prose labels":  "Lyrics by 作詞者\nMusic by 作曲者\nArrangement by 編曲者\noriginal song Lyrics",
		"Japanese labels":       "作詞：作詞者<br>作曲：作曲者<br />編曲：編曲者\noriginal song Lyrics",
		"credit table":          "{|\n! Lyrics\n| 作詞者\n! Composer\n| 作曲者\n! Arranger\n| 編曲者\n|}\noriginal song Lyrics",
		"annotated producers":   "{{Song box 2\n|producers=[[作詞者]] (lyrics), 作曲者 (music), 編曲者 (arrangement)\n}}\noriginal song Lyrics",
		"producer bullet block": "{{Song box 2\n|producers='''制作チーム:'''\n* [[作詞者]] (lyrics)\n* 作曲者 (music)\n* 編曲者 (arrangement)\n* 絵師 (illustration)\n}}\noriginal song Lyrics",
	} {
		t.Run(name, func(t *testing.T) {
			if !verifyCandidate(roleBound, "新曲", content, []string{"Songs"}) {
				t.Fatal("candidate with correctly role-bound credits was rejected")
			}
		})
	}
	partialRoleBound := MusicIdentity{JapaneseTitle: "新曲", Lyricist: "作詞者", Composer: "作曲者"}
	if !verifyCandidate(partialRoleBound, "新曲", "Lyrics: 作詞者\nMusic: 作曲者", []string{"Songs"}) {
		t.Fatal("candidate with every available role-bound catalog credit was rejected")
	}
	if !verifyCandidate(roleBound, "新曲", "Lyrics: 作詞者\nMusic: 作曲者", []string{"Songs"}) {
		t.Fatal("candidate was rejected only because optional arrangement evidence was absent")
	}
	for name, content := range map[string]string{
		"unlabelled tokens":             "作詞者 作曲者 編曲者 original song Lyrics",
		"swapped lyricist and composer": "Lyrics: 作曲者\nMusic: 作詞者\nArrangement: 編曲者\noriginal song Lyrics",
		"swapped annotated producers":   "|producers=作詞者 (music), 作曲者 (lyrics), 編曲者 (arrangement)\noriginal song Lyrics",
		"unannotated producers":         "|producers=作詞者, 作曲者, 編曲者\noriginal song Lyrics",
		"non-credit annotations":        "|producers=作詞者 (illustration), 作曲者 (video), 編曲者 (mixing)\noriginal song Lyrics",
		"credits only in later section": "original song Lyrics\n== Cover version ==\n|producers=作詞者 (lyrics), 作曲者 (music), 編曲者 (arrangement)",
		"all credits under lyricist":    "Lyrics: 作詞者 / 作曲者 / 編曲者\noriginal song Lyrics",
		"wrong role plus correct prose": "Lyrics: 別人\nMusic: 作曲者\nArrangement: 編曲者\n作詞者 discusses this original song and its Lyrics",
	} {
		t.Run(name, func(t *testing.T) {
			if verifyCandidate(roleBound, "新曲", content, []string{"Songs"}) {
				t.Fatal("candidate without deterministic correct-role credit evidence was accepted")
			}
		})
	}
}

func TestRestrictedNonTitleMatchesRemainExcludedAndClassifyAsTitleMismatch(t *testing.T) {
	const sha1 = "0123456789abcdef0123456789abcdef01234567"
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		fmt.Fprintf(w, `{"query":{"pages":{"1":{"pageid":1,"title":"別の曲","categories":[{"title":"Category:Lyrics"}],"revisions":[{"revid":11,"sha1":%q,"slots":{"main":{"content":"作者 original song Lyrics. Do not repost these lyrics."}}}]}}}}`, sha1)
	}))
	defer server.Close()

	candidates, diagnostics, err := newTestClient(server.URL).SearchWithDiagnostics(context.Background(), MusicIdentity{
		JapaneseTitle: "新曲", ProducerMetadata: "作者",
	})
	if err != nil || len(candidates) != 0 {
		t.Fatalf("restricted non-title candidates=%+v diagnostics=%+v err=%v", candidates, diagnostics, err)
	}
	if diagnostics.SearchHits != 1 || diagnostics.Restricted != 1 || diagnostics.RestrictedTitleMatch != 0 || diagnostics.TitleMismatch != 0 {
		t.Fatalf("restricted non-title diagnostics=%+v", diagnostics)
	}
	if reason, ok := diagnostics.ZeroCandidateReason(); !ok || reason != ZeroCandidateTitleMismatch {
		t.Fatalf("restricted non-title reason=%q ok=%t diagnostics=%+v", reason, ok, diagnostics)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("restricted non-title requests=%d, want bounded unquoted and quoted searches", got)
	}

	matching := SearchDiagnostics{SearchHits: 2, Restricted: 1, RestrictedTitleMatch: 1, TitleMismatch: 1}
	if reason, ok := matching.ZeroCandidateReason(); !ok || reason != ZeroCandidateRestricted {
		t.Fatalf("matching restricted reason=%q ok=%t diagnostics=%+v", reason, ok, matching)
	}
}

func TestSearchDiagnosticsCountsOnlyBoundedRejectionClasses(t *testing.T) {
	const sha1 = "0123456789abcdef0123456789abcdef01234567"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"query":{"pages":{`+
			`"1":{"pageid":1,"title":"別の曲","categories":[{"title":"Category:Lyrics"}],"revisions":[{"revid":11,"sha1":%q,"slots":{"main":{"content":"作者 original song Lyrics"}}}]},`+
			`"2":{"pageid":2,"title":"新曲","categories":[{"title":"Category:Lyrics"}],"revisions":[{"revid":22,"sha1":%q,"slots":{"main":{"content":"別人 original song Lyrics"}}}]},`+
			`"3":{"pageid":3,"title":"新曲","categories":[{"title":"Category:Articles"}],"revisions":[{"revid":33,"sha1":%q,"slots":{"main":{"content":"作者による記事"}}}]},`+
			`"4":{"pageid":4,"title":"新曲","categories":[{"title":"Category:Lyrics"}],"revisions":[{"revid":44,"sha1":%q,"slots":{"main":{"content":"作者 original song Lyrics Do not repost these lyrics."}}}]},`+
			`"5":{"pageid":5,"title":"新曲","categories":[{"title":"Category:Songs"}],"revisions":[{"revid":55,"sha1":%q,"slots":{"main":{"content":"作者 original song Lyrics"}}}]}`+
			`}}}`, sha1, sha1, sha1, sha1, sha1)
	}))
	defer server.Close()
	client := newTestClient(server.URL)
	candidates, diagnostics, err := client.SearchWithDiagnostics(context.Background(), MusicIdentity{JapaneseTitle: "新曲", ProducerMetadata: "作者"})
	if err != nil || len(candidates) != 1 || candidates[0].PageID != 5 {
		t.Fatalf("candidates=%+v diagnostics=%+v err=%v", candidates, diagnostics, err)
	}
	if diagnostics != (SearchDiagnostics{SearchHits: 5, Restricted: 1, RestrictedTitleMatch: 1, TitleMismatch: 1, CreditMismatch: 1, SignalMismatch: 1, Verified: 1}) {
		t.Fatalf("diagnostics=%+v", diagnostics)
	}
}

func TestSearchUsesOneBulkRequestAndParsesFiltersAndSortsPages(t *testing.T) {
	const sha1 = "0123456789abcdef0123456789abcdef01234567"
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		query := r.URL.Query()
		if query.Get("action") != "query" || query.Get("format") != "json" || query.Get("generator") != "search" ||
			query.Get("maxlag") != mediaWikiMaxLag || query.Get("gsrnamespace") != "0" ||
			query.Get("gsrlimit") != strconv.Itoa(maxSearchPages) || query.Get("gsrsearch") != "新曲" ||
			query.Get("prop") != "revisions|categories" || query.Get("rvprop") != "ids|sha1|content" || query.Get("rvslots") != "main" ||
			query.Has("rvlimit") || query.Get("cllimit") != "max" || query.Has("list") || query.Has("pageids") || query.Has("revids") {
			http.Error(w, "not a bounded bulk discovery query", http.StatusBadRequest)
			return
		}
		fmt.Fprintf(w, `{"query":{"pages":{`+
			`"30":{"pageid":30,"title":"新曲/作者","categories":[{"title":"Category:Songs"},{"title":"Category:Lyrics"}],"revisions":[{"revid":300,"sha1":%q,"slots":{"main":{"content":"作者 original song Lyrics"}}}]},`+
			`"20":{"pageid":20,"title":"新曲/作者","categories":[{"title":"Category:Lyrics"}],"revisions":[{"revid":200,"sha1":%q,"slots":{"main":{"content":"作者 original song Lyrics\nDo not repost these lyrics."}}}]},`+
			`"10":{"pageid":10,"title":"新曲/作者","categories":[{"title":"Category:Lyrics"}],"revisions":[{"revid":100,"sha1":%q,"slots":{"main":{"content":"無関係 original song Lyrics"}}}]},`+
			`"5":{"pageid":5,"title":"新曲/作者","categories":[{"title":"Category:Songs"}],"revisions":[{"revid":50,"sha1":%q,"slots":{"main":{"content":"作者 original song Lyrics"}}}]}`+
			`}}}`, sha1, sha1, sha1, sha1)
	}))
	defer server.Close()
	client := newTestClient(server.URL)
	candidates, err := client.Search(context.Background(), MusicIdentity{JapaneseTitle: "新曲", ProducerMetadata: "作者"})
	if err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("bulk discovery requests = %d, want 1", got)
	}
	if len(candidates) != 2 || candidates[0].PageID != 5 || candidates[1].PageID != 30 || candidates[1].RevisionID != 300 || candidates[1].SHA1 != sha1 ||
		candidates[1].CanonicalURL != canonicalURL("新曲/作者", 300) || len(candidates[1].Categories) != 2 ||
		candidates[1].Categories[0] != "Lyrics" || candidates[1].Categories[1] != "Songs" {
		t.Fatalf("bulk candidates = %+v", candidates)
	}
}

func TestSearchFailsClosedOnAnyMalformedBulkPage(t *testing.T) {
	const sha1 = "0123456789abcdef0123456789abcdef01234567"
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		fmt.Fprintf(w, `{"query":{"pages":{`+
			`"1":{"pageid":1,"title":"新曲/作者","categories":[{"title":"Category:Lyrics"}],"revisions":[{"revid":34,"sha1":%q,"slots":{"main":{"content":"作者 original song Lyrics"}}}]},`+
			`"2":{"pageid":2,"title":"新曲/作者","categories":[{"title":"Category:Lyrics"}],"revisions":[]}`+
			`}}}`, sha1)
	}))
	defer server.Close()
	client := newTestClient(server.URL)
	candidates, err := client.Search(context.Background(), MusicIdentity{JapaneseTitle: "新曲", ProducerMetadata: "作者"})
	if candidates != nil || !errors.Is(err, ErrMalformedResponse) {
		t.Fatalf("malformed bulk result=%+v err=%v", candidates, err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("malformed bulk requests = %d, want 1", got)
	}
	client.mu.Lock()
	cacheLength := len(client.cache)
	client.mu.Unlock()
	if cacheLength != 0 {
		t.Fatalf("malformed bulk cache length = %d, want 0", cacheLength)
	}
}

func TestCandidateTitleAllowsWellFormedTrailingAlternateTitles(t *testing.T) {
	identity := MusicIdentity{JapaneseTitle: "光", ProducerMetadata: "Mizuno Atsu"}
	for _, title := range []string{
		"光 (Hikari)/Mizuno Atsu",
		"光 (ひかり)/Mizuno Atsu",
		"光 (Hikari 光)/Mizuno Atsu",
	} {
		if !verifyCandidate(identity, title, "Mizuno Atsu original song Lyrics", []string{"Songs"}) {
			t.Fatalf("safe trailing alternate title was rejected: %q", title)
		}
	}
	for _, title := range []string{
		"光 (曖昧さ回避)/Mizuno Atsu",
		"光 (ゲームサイズ)/Mizuno Atsu",
		"灯 (ひかり)/Mizuno Atsu",
	} {
		if verifyCandidate(identity, title, "Mizuno Atsu original song Lyrics", nil) {
			t.Fatalf("unsafe or unrelated title was accepted: %q", title)
		}
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type deadlineOnErrCallContext struct {
	context.Context
	failAt int32
	calls  atomic.Int32
}

func (c *deadlineOnErrCallContext) Err() error {
	if c.calls.Add(1) >= c.failAt {
		return context.DeadlineExceeded
	}
	return nil
}

func sha1Hex(content string) string {
	return fmt.Sprintf("%x", sha1.Sum([]byte(content)))
}

func newTestClient(endpoint string) *Client {
	client := New()
	client.endpoint = endpoint
	client.httpClient = &http.Client{Timeout: 12 * time.Second}
	client.minInterval = 0
	return client
}

func writePageResponse(w http.ResponseWriter, pageID, revisionID int, sha1, title, content string) {
	writePageResponseWithCategories(w, pageID, revisionID, sha1, title, content, []string{"Lyrics"})
}

func writePageResponseWithCategories(w http.ResponseWriter, pageID, revisionID int, sha1, title, content string, categories []string) {
	encodedCategories := make([]map[string]string, len(categories))
	for index, category := range categories {
		encodedCategories[index] = map[string]string{"title": "Category:" + category}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"query": map[string]any{"pages": map[string]any{
		strconv.Itoa(pageID): map[string]any{
			"pageid": pageID, "title": title, "categories": encodedCategories,
			"revisions": []any{map[string]any{"revid": revisionID, "sha1": sha1,
				"slots": map[string]any{"main": map[string]any{"content": content}}}},
		},
	}}})
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
