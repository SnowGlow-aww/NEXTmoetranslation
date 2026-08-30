package lyricssource

import (
	"errors"
	"testing"
)

func TestStructuredTableAllowsOnlyHarmlessTrailingEmptyArtifact(t *testing.T) {
	valid := `== Lyrics ==
{|
! Japanese
! Romaji
|-
|歌う
|utau
|
|}`
	extraction, err := extractStructuredLyrics(valid)
	if err != nil {
		t.Fatal(err)
	}
	if len(extraction.Lines) != 1 || extraction.Lines[0].Japanese != "歌う" {
		t.Fatalf("extraction = %+v", extraction)
	}

	unsafe := `== Lyrics ==
{|
! Japanese
! Romaji
|-
|歌う
|utau
|translation
|}`
	if _, err := extractStructuredLyrics(unsafe); !errors.Is(err, ErrUnsupportedTable) {
		t.Fatalf("nonempty third-cell error = %v", err)
	}
}

func TestStructuredJapaneseColumnKeepsLanguageNeutralLyricText(t *testing.T) {
	content := `== Lyrics ==
{|
! Japanese
! Romaji
|-
|1, 2, 3, 4
|wan, tsu, san, shi
|-
|歌う
|utau
|}`
	extraction, err := extractStructuredLyrics(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(extraction.Lines) != 2 || extraction.Lines[0].Japanese != "1, 2, 3, 4" || extraction.Lines[1].Japanese != "歌う" {
		t.Fatalf("lines = %+v", extraction.Lines)
	}
}

func TestStructuredSharedInterwikiKeepsExplicitDisplayText(t *testing.T) {
	content := `== Lyrics ==
{|
! Japanese
! Romaji
|-
|{{Shared}}|({{IW|Sonic|Escape from the City|Follow me!}})
|Follow me!
|-
|歌う
|utau
|}`
	extraction, err := extractStructuredLyrics(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(extraction.Lines) != 2 || extraction.Lines[0].Japanese != "(Follow me!)" {
		t.Fatalf("lines = %+v", extraction.Lines)
	}
}

func TestWrappedLyricsTemplateTreatsBodyAsSourceLanguage(t *testing.T) {
	content := `== Lyrics ==
{{Lyrics|Turn it up

We shine}}`
	extraction, err := extractStructuredLyrics(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(extraction.Lines) != 2 || extraction.Lines[0].Japanese != "Turn it up" ||
		extraction.Lines[1].Japanese != "We shine" || !extraction.Lines[1].StanzaBreakBefore {
		t.Fatalf("lines = %+v", extraction.Lines)
	}
}
