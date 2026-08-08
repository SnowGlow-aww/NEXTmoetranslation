package lyricssource

import (
	"errors"
	"os"
	"testing"
)

func TestExtractStructuredLyricsFixedRevisionFallbackFixturesKeepSourceTextAndRuby(t *testing.T) {
	tests := []struct {
		name      string
		file      string
		wantFirst string
		wantLine  string
		wantErr   error
	}{
		{name: "Ready Steady", file: "testdata/ready-steady-1473124.wiki", wantFirst: "ローリスクじゃ物足りなくなっちゃったし", wantLine: "Ready Steady"},
		{name: "Attakaito unresolved Han", file: "testdata/attakaito-1492247.wiki", wantErr: ErrUnsupportedTable},
		{name: "Flyway", file: "testdata/flyway-1490478.wiki", wantFirst: "叶わなくて良いから願わせて", wantLine: "［深呼吸］"},
		{name: "I'm Mine unresolved Han", file: "testdata/im-mine-1486196.wiki", wantErr: ErrUnsupportedTable},
		{name: "Beyond the way", file: "testdata/beyond-the-way-1492500.wiki", wantFirst: "(Giga)", wantLine: "Ready Steady? Get out the way."},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			content, err := os.ReadFile(test.file)
			if err != nil {
				t.Fatal(err)
			}
			extraction, err := extractStructuredLyrics(string(content))
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("extraction error=%v, want %v", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(extraction.Lines) == 0 || extraction.Lines[0].Japanese != test.wantFirst {
				t.Fatalf("first line=%+v", extraction.Lines)
			}
			found := false
			for _, line := range extraction.Lines {
				if line.Japanese == test.wantLine {
					found = true
				}
				if len(line.Segments) == 0 {
					t.Fatalf("line has no editable segments: %+v", line)
				}
				for _, segment := range line.Segments {
					if segment.Text == "" || segment.Ruby == nil {
						t.Fatalf("segment lacks editable ruby contract: %+v", segment)
					}
				}
			}
			if !found {
				t.Fatalf("wanted source line %q not found", test.wantLine)
			}
		})
	}
}

func TestPlaintextFallbackDoesNotRelaxAmbiguousVersionSelection(t *testing.T) {
	content := `== Lyrics ==
<tabber>SEKAI Version =
{|
! Japanese
|-
|歌う
|}
|-|Project SEKAI Version =
{|
! Japanese
|-
|踊る
|}
</tabber>`
	if _, err := extractStructuredLyrics(content); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("ambiguous source error=%v", err)
	}
}
