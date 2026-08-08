package lyricssource

import (
	"testing"

	"moesekai/server/internal/model"
)

func TestSekaipediaV3EvidenceDowngradesConcreteRosterMismatchOnlyAtBuild(t *testing.T) {
	complete := Extraction{
		Performers: []Performer{{PerformerID: "歌唱者-01"}, {PerformerID: "歌唱者-02"}},
		Lines: []StructuredLine{
			{
				Japanese: "歌う",
				Segments: []LyricsSegment{
					{Text: "歌", PerformerIDs: []string{"歌唱者-01"}},
					{Text: "う", PerformerIDs: []string{"歌唱者-02"}},
				},
			},
		},
	}
	sourceRoster := []string{"歌唱者-01", "歌唱者-02"}
	parsed := sekaipediaRenditionExtraction{
		extraction:   complete,
		set:          sekaipediaSingerSet{kind: "sekai", ids: sourceRoster},
		sourceTagged: true,
		usedIDs: map[string]struct{}{
			"歌唱者-01": {},
			"歌唱者-02": {},
		},
	}
	if got := sekaipediaStructuredEvidenceState(parsed); got != sekaipediaPerformerEvidenceComplete {
		t.Fatalf("exact source roster parser evidence=%q", got)
	}
	if got := sekaipediaModelPerformerEvidenceStateForExtraction(
		sekaipediaPerformerEvidenceComplete, &complete, sourceRoster,
	); got != model.LyricsSourcePerformerEvidenceSourceComplete {
		t.Fatalf("exact source roster v3 evidence=%q", got)
	}

	gameSubset := complete
	gameSubset.Performers = gameSubset.Performers[:1]
	gameSubset.Lines = []StructuredLine{
		{
			Japanese: "歌",
			Segments: []LyricsSegment{{Text: "歌", PerformerIDs: []string{"歌唱者-01"}}},
		},
	}
	parsed.extraction = gameSubset
	parsed.usedIDs = map[string]struct{}{"歌唱者-01": {}, "歌唱者-02": {}}
	if got := sekaipediaStructuredEvidenceState(parsed); got != sekaipediaPerformerEvidenceComplete {
		t.Fatalf("parser compatibility state changed=%q", got)
	}
	if got := sekaipediaModelPerformerEvidenceStateForExtraction(
		sekaipediaPerformerEvidenceComplete, &gameSubset, sourceRoster,
	); got != model.LyricsSourcePerformerEvidenceSourcePartial {
		t.Fatalf("source-roster subset v3 evidence=%q want=%q", got, model.LyricsSourcePerformerEvidenceSourcePartial)
	}
}
