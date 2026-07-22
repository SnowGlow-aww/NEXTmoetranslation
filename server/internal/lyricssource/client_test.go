package lyricssource

import (
	"errors"
	"testing"
)

func TestExtractLyricsPreservesOrderAndStanzas(t *testing.T) {
	lines, err := extractLyrics("intro\n== Lyrics ==\n歌う\n\n踊る\n== Other ==\nignored")
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0].Japanese != "歌う" || lines[0].StanzaBreakBefore ||
		lines[1].Japanese != "踊る" || !lines[1].StanzaBreakBefore {
		t.Fatalf("extracted lines = %+v", lines)
	}
}

func TestExtractLyricsRejectsRestrictedAndAmbiguousMarkup(t *testing.T) {
	if _, err := extractLyrics("== Lyrics ==\n歌う\n無断転載禁止"); !errors.Is(err, ErrRestrictedReprint) {
		t.Fatalf("restricted source error = %v", err)
	}
	if _, err := extractLyrics("== Lyrics ==\n{|\n| 歌う\n|}\n{|\n| 踊る\n|}"); !errors.Is(err, ErrUnsupportedTable) {
		t.Fatalf("ambiguous table error = %v", err)
	}
}

func TestCandidateRequiresTitleProducerAndSongSignal(t *testing.T) {
	identity := MusicIdentity{MusicID: 10, JapaneseTitle: "新曲", ProducerMetadata: "作者"}
	if !verifyCandidate(identity, "新曲", "作者による original song の Lyrics", nil) {
		t.Fatal("verified source candidate was rejected")
	}
	if verifyCandidate(identity, "新曲", "別人による Lyrics", nil) {
		t.Fatal("candidate without producer identity was accepted")
	}
}
