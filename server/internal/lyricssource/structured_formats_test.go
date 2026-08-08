package lyricssource

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"os"
	"testing"
)

func TestExtractStructuredLyricsKnownFixedRevisionFormats(t *testing.T) {
	tests := []struct {
		name      string
		file      string
		wantLabel string
		wantLines int
		wantErr   error
	}{
		{name: "Nostalogic radio and single edits stay ambiguous", file: "testdata/nostalogic-1486934.wiki", wantErr: ErrAmbiguous},
		{name: "Hand in Hand singer renditions stay ambiguous without catalog evidence", file: "testdata/hand-in-hand-1492304.wiki", wantErr: ErrAmbiguous},
		{name: "Melt original and remix stay ambiguous without catalog evidence", file: "testdata/melt-1492310.wiki", wantErr: ErrAmbiguous},
		{name: "Servant of Evil Niconico and SCP stay ambiguous", file: "testdata/aku-no-meshitsukai-1490265.wiki", wantErr: ErrAmbiguous},
		{name: "Tailor of Enbizaka Niconico and SCP stay ambiguous", file: "testdata/enbizaka-no-shitateya-1482395.wiki", wantErr: ErrAmbiguous},
		{name: "Judgment of Corruption Niconico and SCP stay ambiguous", file: "testdata/akutoku-no-judgment-1492435.wiki", wantErr: ErrAmbiguous},
		{name: "Evil Food Eater Conchita Niconico and SCP stay ambiguous", file: "testdata/akujiki-musume-conchita-1490777.wiki", wantErr: ErrAmbiguous},
		{name: "Gift from the Princess Niconico and SCP stay ambiguous", file: "testdata/nemurase-hime-kara-no-gift-1489599.wiki", wantErr: ErrAmbiguous},
		{name: "Main Character original and remaster stay ambiguous without catalog evidence", file: "testdata/main-character-1470920.wiki", wantErr: ErrAmbiguous},
		{name: "Hello Sekai singer renditions stay ambiguous", file: "testdata/hello-sekai-1469579.wiki", wantErr: ErrAmbiguous},
		{name: "Ifuudoudou original and Vocalofuture stay ambiguous without target rendition lyrics", file: "testdata/ifuudoudou-1491980.wiki", wantErr: ErrAmbiguous},
		{name: "Issen Kounen original and album rosters stay ambiguous without catalog performer identity", file: "testdata/issen-kounen-1484034.wiki", wantErr: ErrAmbiguous},
		{name: "Poppin Candy virtual singer renditions stay ambiguous without a SEKAI lyrics block", file: "testdata/poppin-candy-fever-1491170.wiki", wantErr: ErrAmbiguous},
		{name: "Roki unresolved Han fails closed", file: "testdata/roki-1486687.wiki", wantErr: ErrUnsupportedTable},
		{name: "Oki ni Mesu mama bounded nowiki literals", file: "testdata/oki-ni-mesu-mama-1479774.wiki", wantLabel: "Original Version", wantLines: 56},
		{name: "Mahou Shoujo keeps ideographic-zero redactions as plain text", file: "testdata/mahou-shoujo-to-chocolate-1487821.wiki"},
		{name: "Raspberry Monster Latin source phrase in strongly Japanese table", file: "testdata/raspberry-monster-1490279.wiki", wantLabel: "Japanese Lyrics", wantLines: 34},
		{name: "Dramaturgy unresolved Han fails closed", file: "testdata/dramaturgy-1485725.wiki", wantErr: ErrUnsupportedTable},
		{name: "Hoshikuzu Utopia language tabs", file: "testdata/hoshikuzu-utopia-1469307.wiki"},
		{name: "Highlight English source lyrics template", file: "testdata/highlight-1475423.wiki"},
		{name: "Yoka ni Mitorete colored SEKAI table", file: "testdata/yoka-ni-mitorete-1486167.wiki"},
		{name: "Nihil-san colored SEKAI table", file: "testdata/nihil-san-1486161.wiki"},
		{name: "Treasure Garden unresolved Han fails closed", file: "testdata/treasure-garden-1491533.wiki", wantErr: ErrUnsupportedTable},
		{name: "Dreamer's Beat unresolved Han fails closed", file: "testdata/dreamers-beat-1487678.wiki", wantErr: ErrUnsupportedTable},
		{name: "Sympathy embedded and trailing performer squares", file: "testdata/sympathy-1483434.wiki"},
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
					t.Fatalf("extraction error = %v, want %v", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if test.wantLabel != "" && extraction.Version.Label != test.wantLabel {
				t.Fatalf("selected label=%q want=%q", extraction.Version.Label, test.wantLabel)
			}
			if test.wantLines > 0 && len(extraction.Lines) != test.wantLines {
				t.Fatalf("lines=%d want=%d", len(extraction.Lines), test.wantLines)
			}
			if len(extraction.Lines) == 0 || extraction.RubyGeneratorVersion != rubyGeneratorVersion {
				t.Fatalf("incomplete extraction metadata: lines=%d ruby=%q", len(extraction.Lines), extraction.RubyGeneratorVersion)
			}
			for lineIndex, line := range extraction.Lines {
				if line.Japanese == "" || len(line.Segments) == 0 {
					t.Fatalf("line %d lacks source text or editable segments", lineIndex)
				}
				joined := ""
				for segmentIndex, segment := range line.Segments {
					joined += segment.Text
					if segment.Text == "" || len(segment.Ruby) == 0 {
						t.Fatalf("line %d segment %d lacks editable ruby", lineIndex, segmentIndex)
					}
					rubyText := ""
					for _, span := range segment.Ruby {
						rubyText += span.Text
					}
					if rubyText != segment.Text {
						t.Fatalf("line %d segment %d ruby does not reproduce text", lineIndex, segmentIndex)
					}
				}
				if joined != line.Japanese {
					t.Fatalf("line %d segments do not reproduce source text", lineIndex)
				}
			}
		})
	}
}

func TestFresh701RecoveredFixturesPreserveBoundedSourceDetails(t *testing.T) {
	tests := []struct {
		file      string
		wantLines []string
		wantErr   error
	}{
		{file: "testdata/oki-ni-mesu-mama-1479774.wiki", wantLines: []string{"フェイズ <１>", "フェイズ <２>"}},
		{file: "testdata/mahou-shoujo-to-chocolate-1487821.wiki"},
		{file: "testdata/raspberry-monster-1490279.wiki", wantLines: []string{"Bye LOVE"}},
	}
	for _, test := range tests {
		t.Run(test.file, func(t *testing.T) {
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
			found := make(map[string]bool, len(test.wantLines))
			for _, wanted := range test.wantLines {
				found[wanted] = false
			}
			for _, line := range extraction.Lines {
				if _, wanted := found[line.Japanese]; wanted {
					found[line.Japanese] = true
				}
			}
			for _, wanted := range test.wantLines {
				if !found[wanted] {
					t.Fatalf("source line %q was not preserved", wanted)
				}
			}
		})
	}

	content, err := os.ReadFile("testdata/roki-1486687.wiki")
	if err != nil {
		t.Fatal(err)
	}
	if extraction, err := extractStructuredLyrics(string(content)); !errors.Is(err, ErrUnsupportedTable) {
		t.Fatalf("Roki unresolved Han was not rejected: extraction=%+v err=%v", extraction, err)
	}
}

func TestFresh701IncompleteFixturesMatchFixedRevisionSHA1(t *testing.T) {
	fixtures := map[string]string{
		"testdata/nostalogic-1486934.wiki":                 "2562b9e9e28b7f90fd1583beeaace3c873f3162a",
		"testdata/hand-in-hand-1492304.wiki":               "a52d806efc56874db9dd491537635351061df48c",
		"testdata/melt-1492310.wiki":                       "2a8b797281a34129cf3d7901d0e337fbd61e6d98",
		"testdata/aku-no-meshitsukai-1490265.wiki":         "3d6fdcfe96dec1e6f0df8ad32a938e308a5d551c",
		"testdata/enbizaka-no-shitateya-1482395.wiki":      "cd8a0f32d9fd842f8c1e713dab8da5dc856e112b",
		"testdata/akutoku-no-judgment-1492435.wiki":        "c38c1b4e1fc759b7e99b257584ed7f76fe3031a4",
		"testdata/akujiki-musume-conchita-1490777.wiki":    "17a097f52304e94430ef094f0065c4265029ad93",
		"testdata/nemurase-hime-kara-no-gift-1489599.wiki": "96d7560d481cde4a8d6198de83efe6e7ca938e4e",
		"testdata/mind-brand-1492103.wiki":                 "3199edd28a2776e2fe2d6dca7b2ef9c1f49d3299",
		"testdata/main-character-1470920.wiki":             "6ed91c7c1674fc00d2f1f36c68d0acf0350542b5",
		"testdata/hello-sekai-1469579.wiki":                "8efc0992fab558f0cb1b73ab6be369ecf3ba5681",
		"testdata/roki-1486687.wiki":                       "3838cdb16e59d70bbc54f1bfa4514be201f7603e",
		"testdata/oki-ni-mesu-mama-1479774.wiki":           "95e11296e347b5718c8249ad28b5be061416be9b",
		"testdata/intergalactic-bound-1487105.wiki":        "ecb8943d0e70897a501045a3edd1074bf145eda7",
		"testdata/mahou-shoujo-to-chocolate-1487821.wiki":  "34d156a297fe84432e93427ae57544b7c1d42f8f",
		"testdata/raspberry-monster-1490279.wiki":          "ee5b3dedf7a4058859aa7d6021fa1df366b3b36e",
		"testdata/ifuudoudou-1491980.wiki":                 "ae3d8da5307989c72a0ec6948303298dd64569db",
		"testdata/issen-kounen-1484034.wiki":               "24c4c4a6aa2c879e8a1b01a7da43b567f97fdf13",
		"testdata/poppin-candy-fever-1491170.wiki":         "e3a9dc0ff084cc2c7e551aa15503d825d7b41aeb",
	}
	for file, want := range fixtures {
		content, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha1.Sum(content)
		if got := hex.EncodeToString(digest[:]); got != want {
			t.Fatalf("%s SHA-1=%s want=%s", file, got, want)
		}
	}
}

func TestStructuredEmbeddedPerformerSquaresAnnotatePrecedingText(t *testing.T) {
	content, err := os.ReadFile("testdata/sympathy-1483434.wiki")
	if err != nil {
		t.Fatal(err)
	}
	extraction, err := extractStructuredLyrics(string(content))
	if err != nil {
		t.Fatal(err)
	}
	var target *StructuredLine
	for index := range extraction.Lines {
		line := &extraction.Lines[index]
		if line.Japanese == "こだまして忘れないようかき鳴らした日々を" {
			target = line
			break
		}
	}
	if target == nil {
		t.Fatalf("embedded-square source line missing from %d extracted lines", len(extraction.Lines))
	}
	if len(target.Segments) != 2 || target.Segments[0].Text != "こだまして" ||
		!equalStrings(target.Segments[0].PerformerIDs, []string{"shiho", "ichika", "saki", "honami"}) ||
		target.Segments[1].Text != "忘れないようかき鳴らした日々を" || len(target.Segments[1].PerformerIDs) != 0 ||
		!equalStrings(target.TrailingPerformerIDs, []string{"shiho", "saki", "honami"}) {
		t.Fatalf("embedded-square line = %+v", *target)
	}
	for lineIndex, line := range extraction.Lines {
		assertUnique := func(label string, performerIDs []string) {
			t.Helper()
			seen := map[string]bool{}
			for _, performerID := range performerIDs {
				if seen[performerID] {
					t.Fatalf("line %d %s contains duplicate performer %q", lineIndex+1, label, performerID)
				}
				seen[performerID] = true
			}
		}
		assertUnique("trailing performers", line.TrailingPerformerIDs)
		for segmentIndex, segment := range line.Segments {
			assertUnique("segment performers", segment.PerformerIDs)
			if len(segment.Ruby) == 0 {
				t.Fatalf("line %d segment %d lacks editable ruby", lineIndex+1, segmentIndex+1)
			}
		}
	}
}

func TestStructuredColoredFragmentsPreserveMeaningfulWhitespace(t *testing.T) {
	content, err := os.ReadFile("testdata/flyway-1490478.wiki")
	if err != nil {
		t.Fatal(err)
	}
	extraction, err := extractStructuredLyrics(string(content))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"旋律は僕が　律動は君に":      false,
		"顔も名も残らない　白銀の未開拓地": false,
	}
	for _, line := range extraction.Lines {
		if _, exists := want[line.Japanese]; exists {
			want[line.Japanese] = true
		}
	}
	for line, found := range want {
		if !found {
			t.Fatalf("meaningful source whitespace was not preserved in %q", line)
		}
	}
}
