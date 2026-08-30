package lyricssource

import (
	"errors"
	"testing"
)

func TestStructuredLowRiskTabberRules(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		wantLabel string
		wantLine  string
	}{
		{
			name: "standalone case insensitive clr suffix",
			content: `== Lyrics ==
<tabber>Japanese =
{|
! Japanese
|-
|歌う
|}
</tabber>
  {{ClR}}  `,
			wantLabel: "Japanese",
			wantLine:  "歌う",
		},
		{
			name: "empty tab parts are ignored",
			content: `== Lyrics ==
<tabber>
|-|
Japanese =
{|
! Japanese
|-
|踊る
|}
|-|
|-|
English =
{|
! Lyrics
|-
|dance
|}
|-|
</tabber>`,
			wantLabel: "Japanese",
			wantLine:  "踊る",
		},
		{
			name: "same line table opener belongs to selected body",
			content: `== Lyrics ==
<tabber>Japanese = {| class="wikitable"
! Japanese
|-
|光る
|}
|-|English = {| class="wikitable"
! Lyrics
|-
|shine
|}
</tabber>`,
			wantLabel: "Japanese",
			wantLine:  "光る",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			extraction, err := extractStructuredLyrics(test.content)
			if err != nil {
				t.Fatal(err)
			}
			if extraction.Version.Label != test.wantLabel || len(extraction.Lines) != 1 || extraction.Lines[0].Japanese != test.wantLine {
				t.Fatalf("extraction = %+v", extraction)
			}
		})
	}
}

func TestStructuredLowRiskTabberSuffixRejectsUnknownContent(t *testing.T) {
	for _, suffix := range []string{
		"{{unknown}}",
		"{{clr|unsafe}}",
		"{{clr}} trailing",
		"<!-- comment -->",
	} {
		content := `== Lyrics ==
<tabber>Japanese =
{|
! Japanese
|-
|歌う
|}
</tabber>
` + suffix
		if _, err := extractStructuredLyrics(content); !errors.Is(err, ErrUnsupportedTable) {
			t.Fatalf("suffix %q error = %v", suffix, err)
		}
	}
}

func TestStructuredLowRiskDisplayOnlyRowAttributes(t *testing.T) {
	content := `== Lyrics ==
{|
! Japanese
|-
|歌う
|- class="verse" style='background: transparent' align=center
|踊る
|}`
	extraction, err := extractStructuredLyrics(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(extraction.Lines) != 2 || extraction.Lines[0].Japanese != "歌う" || extraction.Lines[1].Japanese != "踊る" {
		t.Fatalf("lines = %+v", extraction.Lines)
	}
}

func TestStructuredLowRiskRowAttributesFailClosed(t *testing.T) {
	for _, suffix := range []string{
		"f",
		"{{row|class=verse}}",
		`onclick="alert(1)"`,
		`class="verse`,
		`class="verse"junk`,
		`data-song="safe-looking"`,
		`class="verse" class="chorus"`,
		`class=""`,
		`class=verse=chorus`,
	} {
		content := `== Lyrics ==
{|
! Japanese
|-` + suffix + `
|歌う
|}`
		if _, err := extractStructuredLyrics(content); !errors.Is(err, ErrUnsupportedTable) {
			t.Fatalf("row suffix %q error = %v", suffix, err)
		}
	}
}

func TestStructuredLowRiskDisplayTemplatesPreserveExplicitText(t *testing.T) {
	for _, test := range []struct {
		name   string
		source string
		want   string
	}{
		{name: "VLW explicit display", source: "{{VLW|歌|歌う}}", want: "歌う"},
		{name: "Wp explicit display", source: "{{Wp|Dance|踊る}}", want: "踊る"},
		{name: "color explicit display", source: "{{color|#abcdef|光る}}", want: "光る"},
		{name: "VLW target display", source: "{{VLW|歌}}", want: "歌"},
		{name: "Wp target display", source: "{{Wp|踊る}}", want: "踊る"},
		{name: "IW explicit display with empty target", source: "{{IW|wiki||奏でる}}", want: "奏でる"},
	} {
		t.Run(test.name, func(t *testing.T) {
			expanded, err := expandSafeStructuredTemplates(test.source)
			if err != nil || expanded != test.want {
				t.Fatalf("expanded = %q, err = %v, want %q", expanded, err, test.want)
			}
			segments, trailing, err := sanitizeStructuredCell(test.source, map[string]struct{}{}, true)
			if err != nil || len(segments) != 1 || segments[0].text != test.want || len(trailing) != 0 {
				t.Fatalf("segments = %+v, trailing = %+v, err = %v", segments, trailing, err)
			}
			content := `== Lyrics ==
{|
! Japanese
|-
|` + test.source + `
|}`
			extraction, err := extractStructuredLyrics(content)
			if err != nil {
				t.Fatal(err)
			}
			if len(extraction.Lines) != 1 || extraction.Lines[0].Japanese != test.want {
				t.Fatalf("lines = %+v, want %q", extraction.Lines, test.want)
			}
		})
	}
}

func TestStructuredLowRiskDisplayTemplatesFailClosed(t *testing.T) {
	for _, source := range []string{
		"{{VLW|歌|}}",
		"{{Wp|Dance|}}",
		"{{color|red}}",
		"{{color|red|}}",
		"{{color|red|光る|extra}}",
		"{{VLW|歌|{{unknown|歌う}}}}",
		"{{Wp|Dance|{{unknown|踊る}}}}",
		"{{color|red|{{unknown|光る}}}}",
		"{{VLW|歌|歌う|extra}}",
		"{{Wp|Dance|踊る|translation=歌詞}}",
		"{{IW|wiki|target|歌う|extra}}",
		"{{IW||歌う}}",
		"{{ruby|歌|うた|extra}}",
	} {
		content := `== Lyrics ==
{|
! Japanese
|-
|` + source + `
|}`
		if _, err := extractStructuredLyrics(content); !errors.Is(err, ErrUnsupportedTable) {
			t.Fatalf("source %q error = %v", source, err)
		}
	}
}

func TestStructuredLowRiskCellAttributeDetectionIgnoresLaterTemplatePipes(t *testing.T) {
	content := `== Lyrics ==
{|
! Japanese
|-
|<span style="white-space:nowrap">その指先</span>{{ruby|体温|ねつ}}が触れた
|}`
	extraction, err := extractStructuredLyrics(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(extraction.Lines) != 1 || extraction.Lines[0].Japanese != "その指先体温が触れた" {
		t.Fatalf("lines = %+v", extraction.Lines)
	}

	unsafe := `== Lyrics ==
{|
! Japanese
|-
|style="white-space:nowrap" | 歌う{{ruby|空|そら}}
|}`
	if _, err := extractStructuredLyrics(unsafe); !errors.Is(err, ErrUnsupportedTable) {
		t.Fatalf("leading cell attributes error = %v", err)
	}
}

func TestStructuredLowRiskRejectsHTMLClassEntitiesSplitAcrossSegments(t *testing.T) {
	allowed := map[string]struct{}{"miku": {}}
	if _, _, err := sanitizeStructuredCell("{{lrc color|miku|歌&}}amp;", allowed, true); !errors.Is(err, ErrUnsupportedTable) {
		t.Fatalf("split HTML entity error = %v", err)
	}
}

func TestStructuredLowRiskPerformerSquareRunsFailClosedWhenAmbiguous(t *testing.T) {
	allowed := map[string]struct{}{"miku": {}}
	for _, source := range []string{
		"歌う {{lrc color|miku|■}}{{lrc color|miku|■}}",
		"{{lrc color|miku|■}}歌う",
		"{{lrc color|miku|歌う}}{{lrc color|miku|■}}踊る",
	} {
		if _, _, err := sanitizeStructuredCell(source, allowed, true); !errors.Is(err, ErrUnsupportedTable) {
			t.Fatalf("ambiguous performer-square source %q error = %v", source, err)
		}
	}
}

func TestStructuredLowRiskMirroredLatinRefrainIsSourceEvidence(t *testing.T) {
	content := `== Lyrics ==
{|
! Japanese
! Romaji
|-
|Give love
|Give love
|}`
	extraction, err := extractStructuredLyrics(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(extraction.Lines) != 1 || extraction.Lines[0].Japanese != "Give love" {
		t.Fatalf("mirrored Latin lines = %+v", extraction.Lines)
	}
}

func TestStructuredLowRiskUnmirroredLatinSourceFailsClosed(t *testing.T) {
	content := `== Lyrics ==
{|
! Japanese
! Romaji
|-
|Translated prose
|different text
|}`
	if _, err := extractStructuredLyrics(content); !errors.Is(err, ErrUnsupportedTable) {
		t.Fatalf("unmirrored Latin source error = %v", err)
	}
}

func TestStructuredLowRiskMultilineCellsConcatenateWithinCurrentColumn(t *testing.T) {
	content := `== Lyrics ==
{|
! Japanese
! Romaji
|-
|
染み込んだこの温度が
|shimikonda kono ondo ga
|-
|•♬•♫•.
僕を連れてって
|•♬•♫•.
boku o tsuretette
|}`
	extraction, err := extractStructuredLyrics(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(extraction.Lines) != 2 || extraction.Lines[0].Japanese != "染み込んだこの温度が" ||
		extraction.Lines[1].Japanese != "•♬•♫•.僕を連れてって" {
		t.Fatalf("multiline-cell lines = %+v", extraction.Lines)
	}
}

func TestStructuredLowRiskMultilineCellsRejectStructuralContinuation(t *testing.T) {
	content := `== Lyrics ==
{|
! Japanese
|-
|歌う
<tabber>
|}`
	if _, err := extractStructuredLyrics(content); !errors.Is(err, ErrUnsupportedTable) {
		t.Fatalf("structural continuation error = %v", err)
	}
}

func TestStructuredLowRiskDataCellHeadersAreRecognizedBeforeLyrics(t *testing.T) {
	content := `== Lyrics ==
{|
|'''''Japanese'' (日本語歌詞)'''''
|'''''Romaji'' (ローマ字)'''''
|-
|歌う
|utau
|}`
	extraction, err := extractStructuredLyrics(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(extraction.Lines) != 1 || extraction.Lines[0].Japanese != "歌う" {
		t.Fatalf("data-cell-header lines = %+v", extraction.Lines)
	}
}

func TestStructuredLowRiskDataCellHeaderAttributesFailClosed(t *testing.T) {
	content := `== Lyrics ==
{|
|onclick="alert(1)" | Japanese
|Romaji
|-
|歌う
|utau
|}`
	if _, err := extractStructuredLyrics(content); !errors.Is(err, ErrUnsupportedTable) {
		t.Fatalf("unsafe data-cell header error = %v", err)
	}
}

func TestStructuredLowRiskSharedSpanPreservesExplicitSourceText(t *testing.T) {
	content := `== Lyrics ==
{|
! Japanese
! Romaji
|-
|{{Shared|3}} style="font-style:italic; font-weight:bold; text-align:center;" | F.U.C.K.Y.O.U
|-
|{{Shared|2}}|Oh yeah!
|}`
	extraction, err := extractStructuredLyrics(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(extraction.Lines) != 2 || extraction.Lines[0].Japanese != "F.U.C.K.Y.O.U" || extraction.Lines[1].Japanese != "Oh yeah!" {
		t.Fatalf("shared-span lines = %+v", extraction.Lines)
	}
}

func TestStructuredLowRiskSharedSpanAttributesFailClosed(t *testing.T) {
	for _, source := range []string{
		`{{Shared|2}} onclick="alert(1)" | 歌う`,
		`{{Shared|2}} style="font-style:italic" trailing | 歌う`,
		`{{Shared|2}} 歌う`,
	} {
		if _, shared, err := parseStructuredSharedCell(source); shared || !errors.Is(err, ErrUnsupportedTable) {
			t.Fatalf("unsafe shared source %q shared=%t error=%v", source, shared, err)
		}
	}
}

func TestStructuredLowRiskHalfwidthKatakanaIsJapaneseWithoutNormalization(t *testing.T) {
	content := `== Lyrics ==
{|
! Japanese
! Romaji
|-
|ﾊﾛｰ
|haroo
|}`
	extraction, err := extractStructuredLyrics(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(extraction.Lines) != 1 || extraction.Lines[0].Japanese != "ﾊﾛｰ" || len(extraction.Lines[0].Segments) != 1 || extraction.Lines[0].Segments[0].Text != "ﾊﾛｰ" {
		t.Fatalf("lines = %+v", extraction.Lines)
	}
}
