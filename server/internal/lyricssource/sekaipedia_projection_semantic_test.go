package lyricssource

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestSekaipediaSemanticProjectionUsesPerformerAttribution(t *testing.T) {
	row := func(text string, stanza bool, performerIDs ...string) sekaipediaColumnLine {
		return sekaipediaColumnLine{
			stanzaBreakBefore: stanza,
			segments: []sekaipediaColumnSegment{{
				text: text, performerIDs: append([]string(nil), performerIDs...),
			}},
		}
	}
	full := []sekaipediaColumnLine{
		row("开始", false, "miku"),
		row("重复", false, "miku"),
		row("中段", true, "rin"),
		row("重复", false, "rin"),
		row("结束", false, "len"),
	}
	game := []sekaipediaColumnLine{
		row("开始", false, "miku"),
		row("重复", false, "rin"),
		row("结束", false, "len"),
	}
	projection, err := sekaipediaSemanticSubsequence(full, game)
	if err != nil || fmt.Sprint(projection) != "[0 3 4]" {
		t.Fatalf("performer-attributed projection=%v err=%v", projection, err)
	}
}

func TestSekaipediaSemanticProjectionNormalizesWhitespaceAndSegments(t *testing.T) {
	full := []sekaipediaColumnLine{{
		segments: []sekaipediaColumnSegment{
			{text: "だれかの ", performerIDs: []string{"kanade"}},
			{text: "手のひら", performerIDs: []string{"ena"}},
		},
	}}
	game := []sekaipediaColumnLine{{
		segments: []sekaipediaColumnSegment{
			{text: "だれ", performerIDs: []string{"kanade"}},
			{text: "かの手のひら ", performerIDs: []string{"ena"}},
		},
	}}
	projection, err := sekaipediaSemanticSubsequence(full, game)
	if err != nil || fmt.Sprint(projection) != "[0]" {
		t.Fatalf("segmented whitespace-normalized projection=%v err=%v", projection, err)
	}
}

func TestSekaipediaSemanticProjectionRemainsFailClosedWithoutStrongEvidence(t *testing.T) {
	row := func(text string) sekaipediaColumnLine {
		return sekaipediaColumnLine{segments: []sekaipediaColumnSegment{{text: text}}}
	}
	full := []sekaipediaColumnLine{row("重复"), row("重复"), row("结束")}
	game := []sekaipediaColumnLine{row("重复"), row("结束")}
	projection, err := sekaipediaSemanticSubsequence(full, game)
	if projection != nil || !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("weak repeated-row projection=%v err=%v", projection, err)
	}
}

func TestSekaipediaSemanticProjectionRejectsTiedBestMappingsAfterStrongMatch(t *testing.T) {
	row := func(text string, performerIDs ...string) sekaipediaColumnLine {
		return sekaipediaColumnLine{segments: []sekaipediaColumnSegment{{
			text:         text,
			performerIDs: append([]string(nil), performerIDs...),
		}}}
	}
	full := []sekaipediaColumnLine{
		row("开始", "miku"),
		row("重复"),
		row("重复"),
		row("结束"),
	}
	game := []sekaipediaColumnLine{
		row("开始", "miku"),
		row("重复"),
		row("结束"),
	}
	projection, err := sekaipediaSemanticSubsequence(full, game)
	if projection != nil || !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("strong-prefix tied-suffix projection=%v err=%v", projection, err)
	}
}

func TestSekaipediaOriginalSongCatalogSignalKeepsFixedVirtualSingerTextAndRuby(t *testing.T) {
	content := strings.Join([]string{
		"== Versions ==",
		"{{Song versions head}}",
		"{{Song versions line|version=VIRTUAL SINGER|singers=Hatsune Miku|audio=virtual|date=2026-01-01}}",
		"{{Song versions line|version=SEKAI|singers=Yoisaki Kanade, Hatsune Miku|audio=sekai|date=2026-01-02}}",
		"{{Song versions tail}}",
		"== Lyrics ==",
		"<tabber>",
		"Full Version =",
		sekaipediaSyntheticTaggedLyricsBody("Miku", "歌う", "utau"),
		"</tabber>",
	}, "\n")
	parsed, err := parseSekaipediaSong(content, PerformerSegmentationDisabled)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Full.Version != (LyricsVersion{Kind: "vocaloid", Label: "VIRTUAL SINGER Version"}) ||
		parsed.RenditionKey != "full-vocaloid" || parsed.Section != "Lyrics/Full Version" ||
		parsed.Game != nil || len(parsed.GameLineIndexes) != 0 || len(parsed.Full.Lines) != 1 ||
		parsed.Full.Lines[0].Japanese != "歌う" || len(parsed.Full.Lines[0].Segments) != 1 ||
		!equalStrings(parsed.Full.Lines[0].Segments[0].PerformerIDs, []string{"歌唱者-21"}) {
		t.Fatalf("fixed VIRTUAL SINGER Full extraction=%+v", parsed)
	}
	ruby := parsed.Full.Lines[0].Segments[0].Ruby
	if rubySpansText(ruby) != "歌う" || len(ruby) != 2 ||
		ruby[0].Text != "歌" || ruby[0].Reading != "うた" || ruby[1].Text != "う" || ruby[1].Reading != "" {
		t.Fatalf("fixed VIRTUAL SINGER ruby=%+v", ruby)
	}
}

func TestSekaipediaFixedEmptyLyricStubRemainsUnsupported(t *testing.T) {
	content := `== Versions ==
{{Song versions head}}
{{Song versions line|version=VIRTUAL SINGER|singers=Hatsune Miku|audio=virtual|date=2026-01-01}}
{{Song versions tail}}
== Lyrics ==
{{lyric stub|lyrics|translation|color}}
{{Lyrics head
| columns = japanese,romaji,english
| japanese = Japanese lyrics
| romaji = Romanized lyrics
| english = English translation
}}
{{Lyrics line
| japanese =
| romaji =
| english =
}}
{{Lyrics tail|<!-- Please do NOT add any English translation until it's goes through ours or VLW's vetting process -->}}
`
	if parsed, err := parseSekaipediaSong(content, PerformerSegmentationDisabled); !errors.Is(err, ErrUnsupportedTable) ||
		len(parsed.Full.Lines) != 0 || parsed.Game != nil || len(parsed.GameLineIndexes) != 0 {
		t.Fatalf("empty fixed stub extraction=%+v err=%v", parsed, err)
	}
}

func TestSekaipediaGameTabWithSameLyricsNotePromotesExactFullIdentity(t *testing.T) {
	body := sekaipediaSyntheticTaggedLyricsBody("Kanade,Miku", "歌う", "utau")
	content := strings.Join([]string{
		"== Versions ==",
		"{{Song versions head}}",
		"{{Song versions line|version=VIRTUAL SINGER|singers=Hatsune Miku|audio=sample|date=2026-01-01}}",
		"{{Song versions line|version=SEKAI|singers=Yoisaki Kanade, Hatsune Miku|audio=sample|date=2026-01-01}}",
		"{{Song versions tail}}",
		"== Lyrics ==",
		sekaipediaSameLyricsNote,
		"<tabber>",
		"Game Version =",
		body,
		"</tabber>",
	}, "\n")
	parsed, err := parseSekaipediaSong(content, PerformerSegmentationSekaiEligible)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.ReasonCode != "untagged_uncut_identity" || parsed.Section != "Lyrics/Game Version" ||
		parsed.GameSection != "Lyrics/Game Version" || parsed.RenditionKey != "full-sekai" ||
		len(parsed.Full.Lines) != 1 || parsed.Full.Lines[0].Japanese != "歌う" || parsed.Game != nil ||
		fmt.Sprint(parsed.GameLineIndexes) != "[0]" {
		t.Fatalf("same-lyrics identity extraction=%+v", parsed)
	}
}
