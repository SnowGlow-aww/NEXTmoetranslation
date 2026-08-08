package lyricssource

import (
	"errors"
	"strings"
	"testing"
)

func TestStructuredNamedLegendColorsAreFiniteAndCanonical(t *testing.T) {
	content := `== Lyrics ==
{{lrc legend|background
|Singer:orange
}}
{|
! Japanese
! Romaji
|-
|{{lrc color|singer|歌う}}
|{{lrc color|singer|utau}}
|}`
	extraction, err := extractStructuredLyrics(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(extraction.Performers) != 1 || extraction.Performers[0].Color != "#FFA500" {
		t.Fatalf("performers=%+v", extraction.Performers)
	}

	unsafe := strings.Replace(content, "Singer:orange", "Singer:expression", 1)
	section, err := structuredLyricsSection(unsafe)
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := structuredLyricsVersionBlocks(section)
	if err != nil || len(blocks) != 1 {
		t.Fatalf("blocks=%+v err=%v", blocks, err)
	}
	if _, err := extractStructuredLegend(blocks[0].body); !errors.Is(err, ErrUnsupportedTable) {
		t.Fatalf("unsafe named color legend error=%v", err)
	}
	fallback, err := extractStructuredLyrics(unsafe)
	if err != nil || len(fallback.Performers) != 0 {
		t.Fatalf("unsafe color should be discarded by plaintext fallback: extraction=%+v err=%v", fallback, err)
	}
}

func TestStructuredNowikiLiteralsAreBounded(t *testing.T) {
	content := `== Lyrics ==
{|
! Japanese
! Romaji
|-
|フェイズ <nowiki><１></nowiki>
|feizu
|}`
	extraction, err := extractStructuredLyrics(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(extraction.Lines) != 1 || extraction.Lines[0].Japanese != "フェイズ <１>" {
		t.Fatalf("lines=%+v", extraction.Lines)
	}

	for _, unsafe := range []string{
		strings.Replace(content, "<１>", "{{unknown}}", 1),
		strings.Replace(content, "<１>", "[[unknown]]", 1),
		strings.Replace(content, "<nowiki><１></nowiki>", "<nowiki class=lyrics><１></nowiki>", 1),
	} {
		if _, err := extractStructuredLyrics(unsafe); !errors.Is(err, ErrUnsupportedTable) {
			t.Fatalf("unsafe nowiki error=%v", err)
		}
	}
}

func TestStructuredSharedPreambleBeforeExplicitHeaders(t *testing.T) {
	content := `== Lyrics ==
{|
|{{Shared}}|(Uh)
|-
|<br>
|-
! Japanese
! Romaji
|-
|歌う
|utau
|}`
	extraction, err := extractStructuredLyrics(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(extraction.Lines) != 2 || extraction.Lines[0].Japanese != "(Uh)" ||
		extraction.Lines[1].Japanese != "歌う" || !extraction.Lines[1].StanzaBreakBefore {
		t.Fatalf("lines=%+v", extraction.Lines)
	}

	unsafe := strings.Replace(content, "{{Shared}}|(Uh)", "active preamble", 1)
	if _, err := extractStructuredLyrics(unsafe); !errors.Is(err, ErrUnsupportedTable) {
		t.Fatalf("unshared pre-header row error=%v", err)
	}
}

func TestStructuredLatinSourceRowsRequireBoundedJapaneseEvidence(t *testing.T) {
	rows := []string{
		"|-\n|歌う\n|utau",
		"|-\n|踊る\n|odoru",
		"|-\n|進む\n|susumu",
		"|-\n|Bye {{ruby|LOVE|アイ}}\n|bai ai",
	}
	for _, ordered := range [][]string{rows, []string{rows[3], rows[1], rows[0], rows[2]}} {
		content := "== Lyrics ==\n{|\n! Japanese\n! Romaji\n" + strings.Join(ordered, "\n") + "\n|}"
		extraction, err := extractStructuredLyrics(content)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, line := range extraction.Lines {
			found = found || line.Japanese == "Bye LOVE"
		}
		if !found {
			t.Fatalf("Latin source line missing from %+v", extraction.Lines)
		}
	}

	weak := "== Lyrics ==\n{|\n! Japanese\n! Romaji\n|-\n|歌う\n|utau\n|-\n|Translated prose\n|different text\n|}"
	if _, err := extractStructuredLyrics(weak); !errors.Is(err, ErrUnsupportedTable) {
		t.Fatalf("weak table evidence error=%v", err)
	}

	modelCode := "== Lyrics ==\n{|\n! Japanese\n! Romaji\n|-\n|{{lrc color|rin|VOX AC30}}w\n|bokusu eeshii sanjuu watto\n|}"
	extraction, err := extractStructuredLyrics(modelCode)
	if err != nil || len(extraction.Lines) != 1 || extraction.Lines[0].Japanese != "VOX AC30w" {
		t.Fatalf("bounded model code extraction=%+v err=%v", extraction, err)
	}
	for _, unsafe := range []string{
		strings.Replace(modelCode, "{{lrc color|rin|VOX AC30}}w", "VOX AC30w", 1),
		strings.Replace(modelCode, "VOX AC30", "Translation 30", 1),
		strings.Replace(modelCode, "! Japanese", "! Lyrics", 1),
		strings.Replace(modelCode, "! Romaji", "! English", 1),
	} {
		if _, err := extractStructuredLyrics(unsafe); !errors.Is(err, ErrUnsupportedTable) {
			t.Fatalf("unsafe model code evidence error=%v", err)
		}
	}
}

func TestStructuredVersionBoundaryCloseRepairIsExact(t *testing.T) {
	content := `== Lyrics ==
<tabber>
|-|Japanese lyrics=
{|
! Japanese
! Romaji
|-
|歌う
|utau
|-
|-|Official English translation =
<poem>Translated line</poem>
</tabber>`
	extraction, err := extractStructuredLyrics(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(extraction.Lines) != 1 || extraction.Lines[0].Japanese != "歌う" {
		t.Fatalf("lines=%+v", extraction.Lines)
	}

	unsafe := strings.Replace(content, "|utau\n|-\n|-|", "|utau\nactive\n|-|", 1)
	if _, err := extractStructuredLyrics(unsafe); !errors.Is(err, ErrUnsupportedTable) {
		t.Fatalf("non-boundary close repair error=%v", err)
	}
}

func TestStructuredLeadingDoublePipeRepairNeedsCompleteNeighbors(t *testing.T) {
	content := `== Lyrics ==
{|
! Japanese
! Romaji
|-
|前
|mae
|-
||心配してる
|shinpai shiteru
|-
|後
|ato
|}`
	extraction, err := extractStructuredLyrics(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(extraction.Lines) != 3 || extraction.Lines[1].Japanese != "心配してる" {
		t.Fatalf("lines=%+v", extraction.Lines)
	}

	for _, unsafe := range []string{
		strings.Replace(content, "|前\n|mae\n|-\n", "", 1),
		strings.Replace(content, "|後\n|ato", "||後\n|ato", 1),
	} {
		if _, err := extractStructuredLyrics(unsafe); !errors.Is(err, ErrUnsupportedTable) {
			t.Fatalf("unsafe leading double pipe repair error=%v", err)
		}
	}
}
