package lyricssource

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSekaipediaJapaneseOnlyRecoveryFixturesRemainStructuredOrFailClosed(t *testing.T) {
	for _, test := range []struct {
		name    string
		file    string
		policy  PerformerSegmentationPolicy
		wantErr error
	}{
		{name: "Roki unresolved Han", file: "sekaipedia-roki-330574.json", policy: PerformerSegmentationSekaiEligible, wantErr: ErrUnsupportedTable},
		{name: "Journey deterministic Kagome", file: "sekaipedia-journey-326737.json", policy: PerformerSegmentationSekaiEligible},
	} {
		t.Run(test.name, func(t *testing.T) {
			content := japaneseOnlySekaipediaFixtureContent(t, test.file)
			parsed, err := parseSekaipediaSong(content, test.policy)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("Japanese-only Sekaipedia fixture error=%v, want %v (%s)", err, test.wantErr, japaneseOnlySekaipediaDiagnostics(content))
				}
				return
			}
			if err != nil {
				t.Fatalf("Japanese-only Sekaipedia fixture: %v (%s)", err, japaneseOnlySekaipediaDiagnostics(content))
			}
			if len(parsed.Full.Lines) == 0 || len(parsed.Full.Performers) == 0 || !parsed.AuthoritativeStructured ||
				parsed.Full.RubyGeneratorVersion != rubyGeneratorVersion {
				t.Fatalf("Japanese-only structured boundary lines=%d performers=%d authoritative=%t rubyVersion=%q",
					len(parsed.Full.Lines), len(parsed.Full.Performers), parsed.AuthoritativeStructured,
					parsed.Full.RubyGeneratorVersion)
			}
			for lineIndex, line := range parsed.Full.Lines {
				for segmentIndex, segment := range line.Segments {
					if !rubySpansValidForText(segment.Text, segment.Ruby) {
						t.Fatalf("line %d segment %d violates strict ruby contract: %+v", lineIndex+1, segmentIndex+1, segment)
					}
				}
			}
		})
	}
}

func japaneseOnlySekaipediaFixtureContent(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Query struct {
			Pages []struct {
				Revisions []struct {
					Slots struct {
						Main struct {
							Content string `json:"content"`
						} `json:"main"`
					} `json:"slots"`
				} `json:"revisions"`
			} `json:"pages"`
		} `json:"query"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || len(envelope.Query.Pages) != 1 ||
		len(envelope.Query.Pages[0].Revisions) != 1 {
		t.Fatal("Sekaipedia fixture envelope is invalid")
	}
	lines := strings.Split(envelope.Query.Pages[0].Revisions[0].Slots.Main.Content, "\n")
	filtered := lines[:0]
	skippingRomaji := false
	for _, line := range lines {
		lower := strings.ToLower(strings.TrimSpace(line))
		if strings.HasPrefix(lower, "| romaji") || strings.HasPrefix(lower, "|romaji") {
			skippingRomaji = true
			continue
		}
		if skippingRomaji {
			if strings.HasPrefix(lower, "| english") || strings.HasPrefix(lower, "|english") ||
				strings.HasPrefix(lower, "| japanese") || strings.HasPrefix(lower, "|japanese") || lower == "}}" {
				skippingRomaji = false
			} else {
				continue
			}
		}
		filtered = append(filtered, strings.ReplaceAll(line, ",romaji", ""))
	}
	content := strings.Join(filtered, "\n")
	if strings.Contains(strings.ToLower(content), "romaji") {
		t.Fatal("Japanese-only Sekaipedia fixture retains a romaji field")
	}
	return content
}

func japaneseOnlySekaipediaDiagnostics(content string) string {
	lyrics, lyricsErr := sekaipediaTopLevelSection(content, "Lyrics")
	versions, versionsErr := sekaipediaTopLevelSection(content, "Versions")
	if lyricsErr != nil || versionsErr != nil {
		return fmt.Sprintf("sections lyrics=%v versions=%v", lyricsErr, versionsErr)
	}
	records, recordsErr := parseSekaipediaVersions(versions)
	tabs, _, tabsErr := parseSekaipediaLyricsTabs(lyrics)
	if recordsErr != nil || tabsErr != nil {
		return fmt.Sprintf("records=%v tabs=%v", recordsErr, tabsErr)
	}
	sets, setsErr := sekaipediaVersionSets(records, "sekai")
	if setsErr != nil {
		return fmt.Sprintf("sets=%v", setsErr)
	}
	parts := make([]string, 0, len(sets))
	for index, set := range sets {
		_, err := parseSekaipediaRenditionWithSet(tabs["Full Version"], "sekai", set, true)
		parts = append(parts, fmt.Sprintf("set%d=%v", index, err))
	}
	return strings.Join(parts, ",")
}
