package lyricssource

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestStrictEnglishPoemExactFixtures(t *testing.T) {
	tests := []struct {
		file         string
		sha1Prefix   string
		lineCount    int
		breakIndexes []int
	}{
		{file: "goodbye-circusp-1440103.wiki", sha1Prefix: "703c", lineCount: 49, breakIndexes: []int{5, 9, 18, 21, 25, 34, 35, 38, 41}},
		{file: "copycat-1484160.wiki", sha1Prefix: "0379", lineCount: 73, breakIndexes: []int{8, 12, 16, 21, 25, 33, 37, 41, 46, 54, 58, 62}},
		{file: "underwater-1431078.wiki", sha1Prefix: "8b0e", lineCount: 6, breakIndexes: []int{3}},
		{file: "imaginary-love-story-1490553.wiki", sha1Prefix: "8d8b", lineCount: 46, breakIndexes: []int{5, 7, 11, 15, 17, 19, 21, 25, 29, 33, 37, 39, 43}},
		{file: "internet-junk-junkie-1492074.wiki", sha1Prefix: "380e", lineCount: 39, breakIndexes: []int{5, 9, 13, 21, 24, 28, 32}},
		{file: "intergalactic-bound-1487105.wiki", sha1Prefix: "ecb8", lineCount: 63, breakIndexes: []int{5, 11, 15, 18, 22, 26, 30, 35, 39, 42, 46, 50, 53, 57, 60}},
	}

	for _, test := range tests {
		t.Run(test.file, func(t *testing.T) {
			content, err := os.ReadFile("testdata/" + test.file)
			if err != nil {
				t.Fatal(err)
			}
			digest := fmt.Sprintf("%x", sha1.Sum(content))
			if !strings.HasPrefix(digest, test.sha1Prefix) {
				t.Fatalf("fixture SHA-1 = %s, want prefix %s", digest, test.sha1Prefix)
			}

			extraction, err := extractCategoryAwareLyrics(string(content), []string{"Original songs", "English songs"})
			if err != nil {
				t.Fatal(err)
			}
			if extraction.Version != (LyricsVersion{Kind: "original", Label: "Original Version"}) ||
				extraction.RubyGeneratorVersion != rubyGeneratorVersion || extraction.Performers == nil || len(extraction.Performers) != 0 {
				t.Fatalf("extraction metadata = %+v", extraction)
			}
			if len(extraction.Lines) != test.lineCount {
				t.Fatalf("line count = %d, want %d", len(extraction.Lines), test.lineCount)
			}
			if got := englishPoemBreakIndexes(extraction.Lines); !reflect.DeepEqual(got, test.breakIndexes) {
				t.Fatalf("break indexes = %v, want %v", got, test.breakIndexes)
			}
			assertEnglishPoemSegments(t, extraction.Lines)
		})
	}
}

func TestStrictMixedEnglishPoemAndJapaneseTableExactFixture(t *testing.T) {
	content, err := os.ReadFile("testdata/about-me-1486152.wiki")
	if err != nil {
		t.Fatal(err)
	}
	if digest := fmt.Sprintf("%x", sha1.Sum(content)); digest != "c137ad9b031a74a8b53decb7bae576b4020477af" {
		t.Fatalf("fixture SHA-1 = %s", digest)
	}

	section, ok := englishPoemFallbackSection(string(content))
	if !ok {
		t.Fatal("Lyrics section not found")
	}
	tableStart, tableEnd := strings.Index(section, "{|"), strings.Index(section, "|}")
	if tableStart < 0 || tableEnd <= tableStart {
		t.Fatal("mixed table not found")
	}
	table := section[tableStart : tableEnd+2]
	if !strictMixedEnglishJapaneseRomajiTable(table) {
		t.Fatal("mixed table headers were not recognized")
	}
	if _, err := extractStructuredLyrics("== Lyrics ==\n" + table); err != nil {
		t.Fatalf("standalone mixed table error = %v", err)
	}

	extraction, err := extractCategoryAwareLyrics(string(content), []string{
		"English songs", "Original songs", "Partially bilingual songs",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(extraction.Lines) != 60 {
		t.Fatalf("line count = %d, want 60", len(extraction.Lines))
	}
	for index, want := range map[int]string{
		0:  "I have a story to tell",
		50: "気付いた時には終わりを告げ",
		53: "君を思い出すよ",
		54: "Maybe I'm afraid I'm not as tender guy as you think",
		59: "Good-bye...",
	} {
		if got := extraction.Lines[index].Japanese; got != want {
			t.Fatalf("line %d = %q, want %q", index+1, got, want)
		}
	}
	if !extraction.Lines[50].StanzaBreakBefore || !extraction.Lines[54].StanzaBreakBefore {
		t.Fatalf("mixed block boundaries were not preserved: line51=%v line55=%v",
			extraction.Lines[50].StanzaBreakBefore, extraction.Lines[54].StanzaBreakBefore)
	}
	if got := extraction.Lines[50].Segments[0].Ruby; len(got) == 0 || got[0].Reading == "" {
		t.Fatalf("Japanese bridge ruby was not generated: %+v", got)
	}
}

func TestStrictMixedEnglishPoemRequiresPageAndHeaderEvidence(t *testing.T) {
	valid := `== Lyrics ==
<poem>English opening</poem>
<br>
{|
! Japanese
! Romaji
|-
|歌う
|utau
|}
<br />
<poem>English ending</poem>`
	categories := []string{"English songs", "Partially bilingual songs"}
	if _, err := extractCategoryAwareLyrics(valid, categories); err != nil {
		t.Fatalf("valid mixed source error = %v", err)
	}

	tests := []struct {
		name       string
		content    string
		categories []string
	}{
		{name: "missing bilingual evidence", content: valid, categories: []string{"English songs"}},
		{name: "translation header", content: strings.Replace(valid, "! Japanese", "! Japanese Translation", 1), categories: categories},
		{name: "english table", content: strings.Replace(valid, "! Japanese", "! English", 1), categories: categories},
		{name: "missing romaji", content: strings.Replace(valid, "! Romaji", "! Translation", 1), categories: categories},
		{name: "adjacent poems", content: strings.Replace(valid, "{|\n! Japanese\n! Romaji\n|-\n|歌う\n|utau\n|}", "<poem>extra</poem>", 1), categories: categories},
		{name: "unsafe separator", content: strings.Replace(valid, "<br>", "active", 1), categories: categories},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := extractCategoryAwareLyrics(test.content, test.categories); !errors.Is(err, ErrUnsupportedTable) {
				t.Fatalf("error = %v, want %v", err, ErrUnsupportedTable)
			}
		})
	}
}

func TestStrictEnglishPoemPreservesTextEntitiesAndStanzas(t *testing.T) {
	content := "== Lyrics ==\n<poem>\nDon't stop &amp; I won't.\n1984 -- 100%!\n\nRock 'n' roll &quot;again&quot;.\n</poem>"
	extraction, err := extractCategoryAwareLyrics(content, []string{"Category: English_songs"})
	if err != nil {
		t.Fatal(err)
	}
	want := []StructuredLine{
		englishPoemTestLine("Don't stop & I won't.", false),
		englishPoemTestLine("1984 -- 100%!", false),
		englishPoemTestLine("Rock 'n' roll \"again\".", true),
	}
	if !reflect.DeepEqual(extraction.Lines, want) {
		t.Fatalf("lines = %#v, want %#v", extraction.Lines, want)
	}
}

func TestStrictEnglishPoemPreservesSimpleInternalLinkText(t *testing.T) {
	content := "== Lyrics ==\n<poem>\nA [[Thousand Little Voices|thousand little voices]] sing\nHelp us [[Can't Make A Song!!|make this song]]\n</poem>"
	extraction, err := extractCategoryAwareLyrics(content, []string{"English songs", "Original songs"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"A thousand little voices sing", "Help us make this song"}
	if len(extraction.Lines) != len(want) {
		t.Fatalf("lines=%d want=%d", len(extraction.Lines), len(want))
	}
	for index := range want {
		if extraction.Lines[index].Japanese != want[index] {
			t.Fatalf("line %d=%q want=%q", index, extraction.Lines[index].Japanese, want[index])
		}
	}
}

func TestStrictEnglishPoemCategoryAndEnvelopeRejections(t *testing.T) {
	base := "== Lyrics ==\n<poem>\nEnglish line\n</poem>"
	tests := []struct {
		name       string
		content    string
		categories []string
		wantErr    error
	}{
		{name: "missing category", content: base, categories: []string{"Original songs"}, wantErr: ErrMissingLyrics},
		{name: "not exact category", content: base, categories: []string{"English song"}, wantErr: ErrMissingLyrics},
		{name: "translation category", content: base, categories: []string{"English songs", "Songs with English translations"}, wantErr: ErrMissingLyrics},
		{name: "translated category", content: base, categories: []string{"English songs", "Translated songs"}, wantErr: ErrMissingLyrics},
		{name: "romanization category", content: base, categories: []string{"English songs", "Songs needing romanization"}, wantErr: ErrMissingLyrics},
		{name: "romaji category", content: base, categories: []string{"English songs", "Romaji lyrics"}, wantErr: ErrMissingLyrics},
		{name: "two poems", content: "== Lyrics ==\n<poem>One</poem>\n<poem>Two</poem>", categories: []string{"English songs"}, wantErr: ErrUnsupportedTable},
		{name: "text before", content: "== Lyrics ==\nactive\n<poem>English line</poem>", categories: []string{"English songs"}, wantErr: ErrUnsupportedTable},
		{name: "text after", content: "== Lyrics ==\n<poem>English line</poem>\nactive", categories: []string{"English songs"}, wantErr: ErrUnsupportedTable},
		{name: "nested heading", content: "== Lyrics ==\n<poem>English line\n=== Translation ===\nOther</poem>", categories: []string{"English songs"}, wantErr: ErrUnsupportedTable},
		{name: "table", content: "== Lyrics ==\n<poem>English line</poem>\n{|", categories: []string{"English songs"}, wantErr: ErrUnsupportedTable},
		{name: "tabber", content: "== Lyrics ==\n<tabber>Original =\n<poem>English line</poem>\n</tabber>", categories: []string{"English songs"}, wantErr: ErrUnsupportedTable},
		{name: "mixed Japanese", content: "== Lyrics ==\n<poem>English 歌 line</poem>", categories: []string{"English songs"}, wantErr: ErrUnsupportedTable},
		{name: "mixed Cyrillic", content: "== Lyrics ==\n<poem>English Ж line</poem>", categories: []string{"English songs"}, wantErr: ErrUnsupportedTable},
		{name: "unknown template", content: "== Lyrics ==\n<poem>{{unknown|English line}}</poem>", categories: []string{"English songs"}, wantErr: ErrUnsupportedTable},
		{name: "namespaced wiki link", content: "== Lyrics ==\n<poem>[[Category:Song|English line]]</poem>", categories: []string{"English songs"}, wantErr: ErrUnsupportedTable},
		{name: "anchored wiki link", content: "== Lyrics ==\n<poem>[[Song#Verse|English line]]</poem>", categories: []string{"English songs"}, wantErr: ErrUnsupportedTable},
		{name: "multi-pipe wiki link", content: "== Lyrics ==\n<poem>[[Song|English|line]]</poem>", categories: []string{"English songs"}, wantErr: ErrUnsupportedTable},
		{name: "external link", content: "== Lyrics ==\n<poem>[https://example.test English line]</poem>", categories: []string{"English songs"}, wantErr: ErrUnsupportedTable},
		{name: "unknown attribute", content: "== Lyrics ==\n<poem class=\"lyrics\">English line</poem>", categories: []string{"English songs"}, wantErr: ErrUnsupportedTable},
		{name: "style variant", content: "== Lyrics ==\n<poem style=\"margin-left: 1em;\">English line</poem>", categories: []string{"English songs"}, wantErr: ErrUnsupportedTable},
		{name: "explicit restriction", content: "Do not repost these lyrics.\n" + base, categories: []string{"English songs"}, wantErr: ErrRestrictedReprint},
		{name: "category restriction", content: base, categories: []string{"English songs", "Lyrics may not be reprinted"}, wantErr: ErrRestrictedReprint},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := extractCategoryAwareLyrics(test.content, test.categories); !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestStrictEnglishPoemLimits(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "line bytes", body: strings.Repeat("a", maxExtractedLineBytes+1)},
		{name: "line count", body: strings.Repeat("a\n", maxExtractedLines+1)},
		{name: "total bytes", body: englishPoemOverTotalLimitBody()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			content := "== Lyrics ==\n<poem>\n" + test.body + "\n</poem>"
			if _, err := extractCategoryAwareLyrics(content, []string{"English songs"}); !errors.Is(err, ErrLyricsTooLarge) {
				t.Fatalf("error = %v, want %v", err, ErrLyricsTooLarge)
			}
		})
	}
}

func TestCategoryBlindSyntheticExtractionDoesNotEnableEnglishPoemFallback(t *testing.T) {
	content := "== Lyrics ==\n<poem>\nEnglish only\n</poem>"
	if _, err := extractStructuredLyrics(content); !errors.Is(err, ErrMissingLyrics) {
		t.Fatalf("structured parser error = %v", err)
	}
	if _, err := extractLyrics(content); !errors.Is(err, ErrMissingLyrics) {
		t.Fatalf("legacy parser error = %v", err)
	}
}

func TestEnglishPoemFallbackDoesNotLeakTranslationTabsAndKeepsLyricsTemplateRoute(t *testing.T) {
	tabbed, err := os.ReadFile("testdata/journey-1476888.wiki")
	if err != nil {
		t.Fatal(err)
	}
	extraction, err := extractCategoryAwareLyrics(string(tabbed), []string{"English songs"})
	if err != nil {
		t.Fatal(err)
	}
	if len(extraction.Lines) != 43 || extraction.Lines[0].Japanese != "Journey" {
		t.Fatalf("tabbed extraction version=%+v lines=%d", extraction.Version, len(extraction.Lines))
	}
	for _, line := range extraction.Lines {
		if strings.Contains(line.Japanese, "My growing pile") || strings.Contains(line.Japanese, "Saving Grace") {
			t.Fatalf("English translation leaked into source lines: %q", line.Japanese)
		}
	}

	explicit := "== Lyrics ==\n{{Lyrics|We'll keep this explicit}}"
	extraction, err = extractCategoryAwareLyrics(explicit, []string{"English songs"})
	if err != nil {
		t.Fatal(err)
	}
	if len(extraction.Lines) != 1 || extraction.Lines[0].Japanese != "We'll keep this explicit" {
		t.Fatalf("explicit Lyrics route = %+v", extraction)
	}
}

func TestPreviewAndFetchFixedRevisionUseCategoryAwareEnglishPoem(t *testing.T) {
	const (
		pageID     = 12
		revisionID = 34
	)
	content := "Producer original song\n== Lyrics ==\n<poem>\nDon't go &amp; leave.\n</poem>"
	sha := sha1Hex(content)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeEnglishPoemPageResponse(t, w, pageID, revisionID, sha, "English Song", content, []string{"Original songs", "English songs"})
	}))
	defer server.Close()

	client := newTestClient(server.URL)
	identity := MusicIdentity{MusicID: 1, JapaneseTitle: "English Song", ProducerMetadata: "Producer"}
	preview, err := client.Preview(context.Background(), identity, pageID, revisionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.StructuredLines) != 1 || preview.StructuredLines[0].Japanese != "Don't go & leave." {
		t.Fatalf("preview = %+v", preview)
	}
	fixed, err := client.FetchFixedRevision(context.Background(), identity, pageID, revisionID, sha)
	if err != nil {
		t.Fatal(err)
	}
	if len(fixed.Extraction.Lines) != 1 || fixed.Extraction.Lines[0].Japanese != "Don't go & leave." {
		t.Fatalf("fixed = %+v", fixed)
	}
}

func englishPoemBreakIndexes(lines []StructuredLine) []int {
	result := []int{}
	for index, line := range lines {
		if line.StanzaBreakBefore {
			// Fixture expectations use human-readable, one-based line numbers.
			result = append(result, index+1)
		}
	}
	return result
}

func assertEnglishPoemSegments(t *testing.T, lines []StructuredLine) {
	t.Helper()
	for index, line := range lines {
		if len(line.Segments) != 1 || line.Segments[0].Text != line.Japanese || line.Segments[0].PerformerIDs == nil || len(line.Segments[0].PerformerIDs) != 0 ||
			line.TrailingPerformerIDs == nil || len(line.TrailingPerformerIDs) != 0 {
			t.Fatalf("line %d segment = %+v", index, line)
		}
		if len(line.Segments[0].Ruby) != 1 || line.Segments[0].Ruby[0] != (RubySpan{Text: line.Japanese, Reading: ""}) {
			t.Fatalf("line %d ruby = %+v", index, line.Segments[0].Ruby)
		}
	}
}

func englishPoemTestLine(text string, stanzaBreak bool) StructuredLine {
	return StructuredLine{
		Japanese:          text,
		StanzaBreakBefore: stanzaBreak,
		Segments: []LyricsSegment{{
			Text:         text,
			PerformerIDs: []string{},
			Ruby:         []RubySpan{{Text: text, Reading: ""}},
		}},
		TrailingPerformerIDs: []string{},
	}
}

func englishPoemOverTotalLimitBody() string {
	line := strings.Repeat("a", maxExtractedLineBytes)
	count := maxExtractedTextBytes/maxExtractedLineBytes + 1
	return strings.Repeat(line+"\n", count)
}

func writeEnglishPoemPageResponse(t *testing.T, w http.ResponseWriter, pageID, revisionID int, sha, title, content string, categories []string) {
	t.Helper()
	categoryValues := make([]map[string]string, len(categories))
	for index, category := range categories {
		categoryValues[index] = map[string]string{"title": "Category:" + category}
	}
	response := map[string]any{
		"query": map[string]any{
			"pages": map[string]any{
				fmt.Sprintf("%d", pageID): map[string]any{
					"pageid":     pageID,
					"title":      title,
					"categories": categoryValues,
					"revisions": []any{map[string]any{
						"revid": revisionID,
						"sha1":  sha,
						"slots": map[string]any{"main": map[string]any{"content": content}},
					}},
				},
			},
		},
	}
	if err := json.NewEncoder(w).Encode(response); err != nil {
		t.Errorf("encode page response: %v", err)
	}
}
