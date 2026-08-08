package lyricssource

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestStructuredForensicRepairFixedRevision212(t *testing.T) {
	content, err := os.ReadFile("testdata/hoshizora-no-melody-1479186.wiki")
	if err != nil {
		t.Fatal(err)
	}
	extraction, err := extractStructuredLyrics(string(content))
	if err != nil {
		t.Fatal(err)
	}
	if len(extraction.Lines) != 53 {
		t.Fatalf("lines=%d want=53", len(extraction.Lines))
	}
	darknessLines := 0
	for _, line := range extraction.Lines {
		if line.Japanese == "kurayami o terashita" || strings.Contains(line.Japanese, "lighting up the darkness") {
			t.Fatalf("Romaji/English translation leaked into source: %q", line.Japanese)
		}
		if line.Japanese != "暗闇を照らした" {
			continue
		}
		darknessLines++
		if len(line.Segments) != 1 || !stringSlicesEqual(line.Segments[0].PerformerIDs, []string{"miku"}) {
			t.Fatalf("repeated darkness line=%+v", line)
		}
	}
	if darknessLines != 2 {
		t.Fatalf("darkness lines=%d want=2", darknessLines)
	}
}

func TestStructuredForensicRepairFixedRevision369(t *testing.T) {
	content, err := os.ReadFile("testdata/melancholic-1485729.wiki")
	if err != nil {
		t.Fatal(err)
	}
	extraction, err := extractStructuredLyrics(string(content))
	if err != nil {
		t.Fatal(err)
	}
	if len(extraction.Lines) != 32 {
		t.Fatalf("lines=%d want=32", len(extraction.Lines))
	}
	stanzaBreaks := 0
	for _, line := range extraction.Lines {
		if line.StanzaBreakBefore {
			stanzaBreaks++
		}
		if line.Japanese == "sore wa nichiyoubi no higure dattari" {
			t.Fatalf("Romaji leaked into source: %q", line.Japanese)
		}
	}
	if stanzaBreaks != 8 {
		t.Fatalf("stanza breaks=%d want=8", stanzaBreaks)
	}
}

func TestStructuredUnclosedColorRepairUsesGenericUniqueWitness(t *testing.T) {
	content := forensicColorDocument(
		forensicColorStanza(`{{lrc color|miku|暗闇を照らした`, "kurayami o terashita"),
		forensicColorStanza(`{{lrc color|miku|暗闇を照らした}}`, "kurayami o terashita"),
	)
	extraction, err := extractStructuredLyrics(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(extraction.Lines) != 6 {
		t.Fatalf("lines=%d want=6", len(extraction.Lines))
	}
	matched := 0
	for _, line := range extraction.Lines {
		if line.Japanese == "暗闇を照らした" {
			matched++
			if len(line.Segments) != 1 || !stringSlicesEqual(line.Segments[0].PerformerIDs, []string{"miku"}) {
				t.Fatalf("generic repaired line=%+v", line)
			}
		}
	}
	if matched != 2 {
		t.Fatalf("matching repaired lines=%d want=2", matched)
	}
}

func TestStructuredExactMalformedSeparatorRepairUsesGenericCompleteNeighbors(t *testing.T) {
	content := forensicSeparatorDocument("|-f", "Japanese", "Romaji", "前", "mae", "後", "ato")
	extraction, err := extractStructuredLyrics(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(extraction.Lines) != 2 || extraction.Lines[0].Japanese != "前" || extraction.Lines[1].Japanese != "後" {
		t.Fatalf("generic separator extraction=%+v", extraction.Lines)
	}
}

func TestStructuredUnclosedColorRepairRequiresUniqueCompleteStanzaWitness(t *testing.T) {
	complete := forensicColorStanza(`{{lrc color|miku|暗闇を照らした}}`, "kurayami o terashita")
	tests := map[string]string{
		"no witness": forensicColorDocument(
			forensicColorStanza(`{{lrc color|miku|暗闇を照らした`, "kurayami o terashita"),
			forensicColorStanza(`{{lrc color|miku|別の暗闇}}`, "betsu no kurayami"),
		),
		"multiple malformed candidates": forensicColorDocument(
			forensicColorStanza(`{{lrc color|miku|暗闇を照らした`, "kurayami o terashita"),
			forensicColorStanza(`{{lrc color|miku|暗闇を照らした`, "kurayami o terashita"),
		),
		"multiple complete witnesses": forensicColorDocument(
			forensicColorStanza(`{{lrc color|miku|暗闇を照らした`, "kurayami o terashita"),
			complete,
			complete,
		),
		"companion mismatch": forensicColorDocument(
			forensicColorStanza(`{{lrc color|miku|暗闇を照らした`, "kurayami o terashita"),
			forensicColorStanza(`{{lrc color|miku|暗闇を照らした}}`, "different romaji"),
		),
		"cross-line closure": forensicColorDocument(
			forensicColorStanza("{{lrc color|miku|暗闇を照らした\n}}", "kurayami o terashita"),
			complete,
		),
		"extra parameter": forensicColorDocument(
			forensicColorStanza(`{{lrc color|miku|暗闇を照らした|extra`, "kurayami o terashita"),
			complete,
		),
		"nested payload": forensicColorDocument(
			forensicColorStanza(`{{lrc color|miku|{{ruby|暗闇|くらやみ`, "kurayami o terashita"),
			complete,
		),
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			assertStructuredForensicUnsupported(t, content)
		})
	}
}

func TestStructuredUnclosedColorRepairRequiresLegendSourceCellAndMatchingBoundaries(t *testing.T) {
	t.Run("performer absent from legend", func(t *testing.T) {
		content := strings.ReplaceAll(forensicColorDocument(
			forensicColorStanza(`{{lrc color|rin|暗闇を照らした`, "kurayami o terashita"),
			forensicColorStanza(`{{lrc color|rin|暗闇を照らした}}`, "kurayami o terashita"),
		), "|Miku:#39C5BB", "|Len:#FFCC11")
		assertStructuredForensicUnsupported(t, content)
	})

	t.Run("candidate is companion cell", func(t *testing.T) {
		candidate := forensicColorStanza("暗闇を照らした", `{{lrc color|miku|kurayami o terashita`)
		witness := forensicColorStanza("暗闇を照らした", `{{lrc color|miku|kurayami o terashita}}`)
		assertStructuredForensicUnsupported(t, forensicColorDocument(candidate, witness))
	})

	t.Run("witness has table-edge boundary", func(t *testing.T) {
		candidate := forensicColorStanza(`{{lrc color|miku|暗闇を照らした`, "kurayami o terashita")
		witness := forensicColorStanza(`{{lrc color|miku|暗闇を照らした}}`, "kurayami o terashita")
		content := forensicColorDocumentWithoutFinalBreak(candidate, witness)
		assertStructuredForensicUnsupported(t, content)
	})

	t.Run("witness exists only in translation tab", func(t *testing.T) {
		candidate := forensicColorStanza(`{{lrc color|miku|暗闇を照らした`, "kurayami o terashita")
		content := `== Lyrics ==
{{lrc legend|background
|Miku:#39C5BB
}}
<tabber>Japanese lyrics =
` + forensicColorTable(candidate) + `
|-|Official English translation =
` + forensicColorTable(forensicColorStanza(`{{lrc color|miku|暗闇を照らした}}`, "kurayami o terashita")) + `
</tabber>`
		assertStructuredForensicUnsupported(t, content)
	})

	t.Run("translation-only tab is not repairable source", func(t *testing.T) {
		content := `== Lyrics ==
<tabber>Official English translation =
` + forensicColorTable(
			forensicColorStanza(`{{lrc color|miku|暗闇を照らした`, "kurayami o terashita"),
			forensicColorStanza(`{{lrc color|miku|暗闇を照らした}}`, "kurayami o terashita"),
		) + `
</tabber>`
		if _, err := extractStructuredLyrics(content); !errors.Is(err, ErrMissingLyrics) {
			t.Fatalf("translation-only repair error=%v", err)
		}
	})
}

func TestStructuredExactMalformedSeparatorRepairFailsClosed(t *testing.T) {
	for name, content := range map[string]string{
		"first position": `== Lyrics ==
{|
! Japanese
! Romaji
|-f
|後
|ato
|}`,
		"last position": `== Lyrics ==
{|
! Japanese
! Romaji
|-
|前
|mae
|-f
|}`,
		"multiple anomalies": `== Lyrics ==
{|
! Japanese
! Romaji
|-
|前
|mae
|-f
|中
|naka
|-f
|後
|ato
|}`,
		"uppercase suffix":               forensicSeparatorDocument("|-F", "Japanese", "Romaji", "前", "mae", "後", "ato"),
		"spaced suffix":                  forensicSeparatorDocument("|- f", "Japanese", "Romaji", "前", "mae", "後", "ato"),
		"attribute-like suffix":          forensicSeparatorDocument(`|-f="x"`, "Japanese", "Romaji", "前", "mae", "後", "ato"),
		"unsafe ordinary row attributes": forensicSeparatorDocument(`|-onclick="alert(1)"`, "Japanese", "Romaji", "前", "mae", "後", "ato"),
		"missing previous companion":     forensicSeparatorDocument("|-f", "Japanese", "Romaji", "前", "", "後", "ato"),
		"missing following companion":    forensicSeparatorDocument("|-f", "Japanese", "Romaji", "前", "mae", "後", ""),
		"translation companion header":   forensicSeparatorDocument("|-f", "Japanese", "English", "前", "mae", "後", "ato"),
		"conflicting companion header":   forensicSeparatorDocument("|-f", "Japanese", "Romaji English", "前", "mae", "後", "ato"),
		"adjacent stanza break": `== Lyrics ==
{|
! Japanese
! Romaji
|-
|<br>
|-f
|後
|ato
|}`,
	} {
		t.Run(name, func(t *testing.T) {
			assertStructuredForensicUnsupported(t, content)
		})
	}
}

func forensicColorDocument(stanzas ...string) string {
	return `== Lyrics ==
{{lrc legend|background
|Miku:#39C5BB
}}
` + forensicColorTable(stanzas...)
}

func forensicColorTable(stanzas ...string) string {
	var body strings.Builder
	body.WriteString(`{|
! Japanese
! Romaji
|-
|<br>
`)
	for _, stanza := range stanzas {
		body.WriteString(stanza)
		body.WriteString("|-\n|<br>\n")
	}
	body.WriteString("|}")
	return body.String()
}

func forensicColorDocumentWithoutFinalBreak(candidate, witness string) string {
	return `== Lyrics ==
{{lrc legend|background
|Miku:#39C5BB
}}
{|
! Japanese
! Romaji
|-
|<br>
` + candidate + `|-
|<br>
` + witness + `|}`
}

func forensicColorStanza(source, companion string) string {
	return `|-
|{{lrc color|miku|星に願いをかけて}}
|hoshi ni negai o kakete
|-
|` + source + `
|` + companion + `
|-
|夢を満たしてゆこう
|yume o mitashite yukou
`
}

func forensicSeparatorDocument(separator, sourceHeader, companionHeader, beforeSource, beforeCompanion, afterSource, afterCompanion string) string {
	var body strings.Builder
	body.WriteString("== Lyrics ==\n{|\n! ")
	body.WriteString(sourceHeader)
	body.WriteString("\n! ")
	body.WriteString(companionHeader)
	body.WriteString("\n|-\n|")
	body.WriteString(beforeSource)
	if beforeCompanion != "" {
		body.WriteString("\n|")
		body.WriteString(beforeCompanion)
	}
	body.WriteString("\n")
	body.WriteString(separator)
	body.WriteString("\n|")
	body.WriteString(afterSource)
	if afterCompanion != "" {
		body.WriteString("\n|")
		body.WriteString(afterCompanion)
	}
	body.WriteString("\n|}")
	return body.String()
}

func assertStructuredForensicUnsupported(t *testing.T, content string) {
	t.Helper()
	if _, err := extractStructuredLyrics(content); !errors.Is(err, ErrUnsupportedTable) {
		t.Fatalf("error=%v want=%v", err, ErrUnsupportedTable)
	}
}
