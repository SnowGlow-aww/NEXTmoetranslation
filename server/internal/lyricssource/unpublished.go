package lyricssource

import (
	"errors"
	"regexp"
	"strings"
)

// ErrLyricsUnpublished means the fixed source revision explicitly withholds or
// omits lyric text. It is a deterministic, non-fabrication exception rather
// than a parser-format failure.
var ErrLyricsUnpublished = errors.New("lyrics text is explicitly unpublished")

var omittedLyricsSectionPattern = regexp.MustCompile(`(?i)^\s*\{\{\s*omitted[ _-]*lyrics\s*\}\}\s*$`)

func hasExplicitUnpublishedLyrics(content string) bool {
	location := headingPattern.FindStringIndex(content)
	if location == nil {
		return false
	}
	section := strings.ReplaceAll(content[location[1]:], "\r", "")
	if next := topLevelHeadingPattern.FindStringIndex(section); next != nil {
		section = section[:next[0]]
	}
	return omittedLyricsSectionPattern.MatchString(section)
}
