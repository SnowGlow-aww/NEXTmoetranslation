package lyricssource

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestStructuredFixedRevisionStrictColspanSourceRow(t *testing.T) {
	content, err := os.ReadFile("testdata/hurray-1461629.wiki")
	if err != nil {
		t.Fatal(err)
	}
	extraction, err := extractStructuredLyrics(string(content))
	if err != nil {
		t.Fatal(err)
	}

	found := false
	for index, line := range extraction.Lines {
		if line.Japanese != "AH" {
			continue
		}
		found = true
		if index == 0 || index+1 >= len(extraction.Lines) ||
			extraction.Lines[index-1].Japanese != "濁しながら逃げて逃げて" ||
			extraction.Lines[index+1].Japanese != "恐れるな" ||
			!extraction.Lines[index+1].StanzaBreakBefore {
			t.Fatalf("colspan source context = %+v", extraction.Lines[max(0, index-1):min(len(extraction.Lines), index+2)])
		}
	}
	if !found {
		t.Fatalf("strict colspan source row missing from %d lines", len(extraction.Lines))
	}
}

func TestStructuredStrictColspanSourceRowFailsClosed(t *testing.T) {
	for _, source := range []string{
		`colspan="3" style="text-align:center" | AH`,
		`style="text-align:center" colspan="3" | AH`,
		`onclick="alert(1)" | AH`,
		`colspan="03" | AH`,
		`colspan="3 | AH`,
		`colspan="3" |`,
		`colspan="3" || AH`,
		`colspan="3" | <center>AH`,
		`colspan="3" | <center><span>AH</center></span>`,
		`colspan="3" | <span onclick="alert(1)">AH</span>`,
	} {
		t.Run(source, func(t *testing.T) {
			assertStructuredUnsupported(t, structuredTwoColumnTable(source))
		})
	}
}

func TestStructuredMalformedMarkupAndArbitraryAttributesFailClosed(t *testing.T) {
	for _, source := range []string{
		`<center>歌う`,
		`<center><span>歌う</center></span>`,
		`<span onclick="alert(1)">歌う</span>`,
		`{{ruby|歌|うた`,
	} {
		content := `== Lyrics ==
{|
! Japanese
|-
|` + source + `
|}`
		assertStructuredUnsupported(t, content)
	}
}

func TestStructuredAllowsOmittedTrailingNonSourceColumns(t *testing.T) {
	content := `== Lyrics ==
{|
! Japanese
! Romaji
! English
|-
|歌う
|-
|踊る
|odoru
|dance
|}`
	extraction, err := extractStructuredLyrics(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(extraction.Lines) != 2 || extraction.Lines[0].Japanese != "歌う" || extraction.Lines[1].Japanese != "踊る" {
		t.Fatalf("lines = %+v", extraction.Lines)
	}
}

func TestStructuredOmittedSourceDoesNotBecomeTrailingColumnLeakage(t *testing.T) {
	content := `== Lyrics ==
{|
! Japanese
! Romaji
! English
|-
|translated prose
|}`
	assertStructuredUnsupported(t, content)
}

func TestStructuredNormalizesOnlyExactOverflowBreakAfterCompleteRow(t *testing.T) {
	for _, overflow := range []string{"|<br>", "|\n|<br />"} {
		t.Run(strings.ReplaceAll(overflow, "\n", "_"), func(t *testing.T) {
			content := `== Lyrics ==
{|
! Japanese
! Romaji
|-
|歌う
|utau
` + overflow + `
|-
|踊る
|odoru
|}`
			extraction, err := extractStructuredLyrics(content)
			if err != nil {
				t.Fatal(err)
			}
			if len(extraction.Lines) != 2 || extraction.Lines[0].Japanese != "歌う" ||
				extraction.Lines[1].Japanese != "踊る" || !extraction.Lines[1].StanzaBreakBefore {
				t.Fatalf("overflow break lines = %+v", extraction.Lines)
			}
		})
	}
}

func TestStructuredOverflowDoesNotAcceptTranslationOrMalformedBreak(t *testing.T) {
	for _, overflow := range []string{
		"|translated prose",
		"|\n|translated prose",
		"|\n|\n|<br>",
		`|<br class="verse">`,
	} {
		t.Run(strings.ReplaceAll(overflow, "\n", "_"), func(t *testing.T) {
			content := `== Lyrics ==
{|
! Japanese
! Romaji
|-
|歌う
|utau
` + overflow + `
|}`
			assertStructuredUnsupported(t, content)
		})
	}
}

func TestStructuredSinglePipePhysicalSourceRomajiPair(t *testing.T) {
	content := `== Lyrics ==
{|
! Japanese
! Romaji
|-
|スターダムへのセトリ旅したら（モ・モ・ジャン 命）|sutaadamu e no setori tabi shitara (mo mo jan inochi)
|}`
	extraction, err := extractStructuredLyrics(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(extraction.Lines) != 1 || extraction.Lines[0].Japanese != "スターダムへのセトリ旅したら（モ・モ・ジャン 命）" {
		t.Fatalf("single-pipe physical pair lines = %+v", extraction.Lines)
	}
}

func TestStructuredSinglePipePhysicalPairFailsClosed(t *testing.T) {
	tests := []string{
		`== Lyrics ==
{|
! Japanese
! English
|-
|歌う|sing
|}`,
		`== Lyrics ==
{|
! Japanese
! Romaji
|-
|style="text-align:center" |歌う
|}`,
		`== Lyrics ==
{|
! Japanese
! Romaji
|-
|歌う|utau|sing
|}`,
		`== Lyrics ==
{|
! Japanese
! Romaji
|-
|Source prose|source prose
|}`,
	}
	for index, content := range tests {
		t.Run(string(rune('a'+index)), func(t *testing.T) {
			assertStructuredUnsupported(t, content)
		})
	}
}

func TestStructuredTranslationNotesHeadingInsideTabberDoesNotTruncateSource(t *testing.T) {
	content := `== Lyrics ==
<tabber>Japanese =
{|
! Japanese
! Romaji
|-
|歌う
|utau
|}
|-|Official English lyrics =
<poem>
Sing
</poem>
==Translation Notes==
<references group="TL note"/>
<!-- Translation-only material. -->
</tabber>

==Gallery==
ignored`
	extraction, err := extractStructuredLyrics(content)
	if err != nil {
		t.Fatal(err)
	}
	if extraction.Version.Label != "Japanese" || len(extraction.Lines) != 1 || extraction.Lines[0].Japanese != "歌う" {
		t.Fatalf("extraction = %+v", extraction)
	}
}

func TestStructuredTranslationOnlyTabWithNotesIsNotSource(t *testing.T) {
	content := `== Lyrics ==
<tabber>Official English lyrics =
{|
! Lyrics
|-
|Translated prose
|}
==Translation Notes==
<references group="TL note"/>
</tabber>`
	if _, err := extractStructuredLyrics(content); !errors.Is(err, ErrMissingLyrics) {
		t.Fatalf("translation-only tab error = %v", err)
	}
}

func TestStructuredPreservesStrictPreTabberLegend(t *testing.T) {
	content := `== Lyrics ==
{{lrc legend|background
|Miku:#39C5BB
}}
<tabber>Japanese =
{|
! Japanese
! Romaji
|-
|{{lrc color|miku|歌う}}
|utau
|}
|-|Official English lyrics =
{|
! Lyrics
|-
|Sing
|}
</tabber>`
	extraction, err := extractStructuredLyrics(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(extraction.Performers) != 1 || extraction.Performers[0].PerformerID != "miku" ||
		len(extraction.Lines) != 1 || len(extraction.Lines[0].Segments) != 1 ||
		!stringSlicesEqual(extraction.Lines[0].Segments[0].PerformerIDs, []string{"miku"}) {
		t.Fatalf("pre-tabber legend extraction = %+v", extraction)
	}
}

func TestStructuredPreTabberPrefixRejectsAnythingBesidesOneLegend(t *testing.T) {
	for _, prefix := range []string{
		"arbitrary text\n{{lrc legend|background|Miku:#39C5BB}}",
		"{{lrc legend|background|Miku:#39C5BB}}\n{{lrc legend|background|Rin:#FFA500}}",
		"{{unknown}}",
	} {
		content := "== Lyrics ==\n" + prefix + `
<tabber>Japanese =
{|
! Japanese
|-
|歌う
|}
</tabber>`
		assertStructuredUnsupported(t, content)
	}
}

func TestStructuredMirroredLatinCanonicalEvidence(t *testing.T) {
	tests := []struct {
		name       string
		source     string
		comparison string
	}{
		{name: "spaces", source: "Give   love", comparison: "Give love"},
		{name: "terminal quote", source: "Hello world”", comparison: "Hello world"},
		{name: "acronym terminal punctuation", source: "F.U.C.K.Y.O.U.", comparison: "F.U.C.K.Y.O.U"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			content := `== Lyrics ==
{|
! Japanese
! Romaji
|-
|` + test.source + `
|` + test.comparison + `
|}`
			extraction, err := extractStructuredLyrics(content)
			if err != nil {
				t.Fatal(err)
			}
			if len(extraction.Lines) != 1 || extraction.Lines[0].Japanese != test.source {
				t.Fatalf("mirrored Latin lines = %+v", extraction.Lines)
			}
		})
	}
}

func TestStructuredMirroredLatinEvidenceFailsClosed(t *testing.T) {
	for _, pair := range [][2]string{
		{"Give love!", "Give love"},
		{"F.U.C.K.Y.O.U", "FUCKYOU"},
		{"Translated prose", "different text"},
		{"Hello world\" trailing", "Hello world"},
	} {
		content := `== Lyrics ==
{|
! Japanese
! Romaji
|-
|` + pair[0] + `
|` + pair[1] + `
|}`
		assertStructuredUnsupported(t, content)
	}
}

func assertStructuredUnsupported(t *testing.T, content string) {
	t.Helper()
	if _, err := extractStructuredLyrics(content); !errors.Is(err, ErrUnsupportedTable) {
		t.Fatalf("error = %v, want %v", err, ErrUnsupportedTable)
	}
}

func structuredTwoColumnTable(source string) string {
	return `== Lyrics ==
{|
! Japanese
! Romaji
|-
|` + source + `
|}`
}
