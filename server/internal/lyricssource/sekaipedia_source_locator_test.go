package lyricssource

import (
	"testing"

	"moesekai/server/internal/model"
)

func TestSekaipediaPlainRowsBindSourceRubyLocators(t *testing.T) {
	content := `== Versions ==
{{Song versions head}}
{{Song versions line|version=VIRTUAL SINGER|singers=Miku|audio=sample}}
{{Song versions tail}}
== Lyrics ==
{{Lyrics head|columns=japanese,romaji,english|japanese=Japanese lyrics|romaji=Romanized lyrics|english=English translation}}
{{Lyrics line|japanese=形のない気持ち忘れないように|romaji=katachi no nai kimochi wasurenai you ni|english=}}
{{Lyrics line|japanese=君に伝えたいことが|romaji=kimi ni tsutaetai koto ga|english=}}
{{Lyrics tail}}
`
	parsed, err := parseSekaipediaSong(content, PerformerSegmentationDisabled)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Full.Lines) != 2 {
		t.Fatalf("lines=%d", len(parsed.Full.Lines))
	}
	for lineIndex, line := range parsed.Full.Lines {
		for segmentIndex, segment := range line.Segments {
			for spanIndex, span := range segment.Ruby {
				if span.Reading == "" {
					continue
				}
				if span.ReadingEvidenceKind != model.LyricsSourceReadingEvidenceSourceTransliteration ||
					span.SourceRowOrdinal != lineIndex+1 || span.SourceSegmentOrdinal != segmentIndex+1 {
					t.Fatalf("line=%d segment=%d span=%d ruby=%+v", lineIndex+1, segmentIndex+1, spanIndex+1, span)
				}
			}
		}
	}
}

func TestSekaipediaFlattenedRubyKeepsPerSourceSegmentLocators(t *testing.T) {
	spans := []RubySpan{
		{Text: "もう"},
		{Text: "如何", Reading: "どう", ReadingEvidenceKind: model.LyricsSourceReadingEvidenceSourceTransliteration},
		{Text: "しようも"},
		{Text: "無", Reading: "な", ReadingEvidenceKind: model.LyricsSourceReadingEvidenceSourceTransliteration},
		{Text: "くなって"},
		{Text: "叫", Reading: "さけ", ReadingEvidenceKind: model.LyricsSourceReadingEvidenceSourceTransliteration},
		{Text: "ぶんだ"},
	}
	source := []sekaipediaColumnSegment{
		{text: "もう", sourceGroup: 2, sourceSegmentOrdinal: 3},
		{text: "如何", sourceGroup: 1, sourceSegmentOrdinal: 1},
		{text: "しようも無くなって叫ぶんだ", sourceGroup: 2, sourceSegmentOrdinal: 4},
	}
	bound, ok := bindSekaipediaRubyToSourceSegments(spans, source)
	if !ok {
		t.Fatal("source ruby did not split across exact source segments")
	}
	want := map[string][2]int{
		"如何": {1, 1},
		"無":  {2, 4},
		"叫":  {2, 4},
	}
	for _, span := range bound {
		if expected, ok := want[span.Text]; ok {
			if [2]int{span.SourceRowOrdinal, span.SourceSegmentOrdinal} != expected {
				t.Fatalf("span=%+v want locator=%v", span, expected)
			}
		}
	}
}
