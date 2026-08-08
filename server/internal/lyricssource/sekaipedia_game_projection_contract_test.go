package lyricssource

import "testing"

func TestSekaipediaExactGameProjectionCompatibilityRejectsSemanticTextDrift(t *testing.T) {
	full := sekaipediaRenditionExtraction{
		extraction: Extraction{Lines: []StructuredLine{{Japanese: "雨が降る"}, {Japanese: "明日も"}}},
	}
	game := sekaipediaRenditionExtraction{
		extraction: Extraction{Lines: []StructuredLine{{Japanese: "雨が降る "}}},
	}
	if sekaipediaExactGameProjectionCompatible(full, game, []int{0}) {
		t.Fatal("semantic-only text alignment was incorrectly persisted as an exact Game projection")
	}
	game.extraction.Lines[0].Japanese = "雨が降る"
	if !sekaipediaExactGameProjectionCompatible(full, game, []int{0}) {
		t.Fatal("exact Game projection text identity was rejected")
	}
}
