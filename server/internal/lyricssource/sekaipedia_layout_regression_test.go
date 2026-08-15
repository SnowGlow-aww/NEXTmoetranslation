package lyricssource

import (
	"strings"
	"testing"
)

func TestSekaipediaPunctuatedExplicitRubyAlignment(t *testing.T) {
	spans, ok := sekaipediaExplicitRubySpans("一、二、三、四", "ワン、トゥー、スリー、フォー")
	if !ok || len(spans) != 7 {
		t.Fatalf("punctuated ruby spans=%+v ok=%v", spans, ok)
	}
	if spans[0].Text != "一" || spans[0].Reading != "わん" ||
		spans[2].Text != "二" || spans[2].Reading != "とぅう" ||
		spans[4].Text != "三" || spans[4].Reading != "すりい" ||
		spans[6].Text != "四" || spans[6].Reading != "ふぉお" {
		t.Fatalf("unexpected punctuated ruby spans=%+v", spans)
	}
	for _, index := range []int{1, 3, 5} {
		if spans[index].Reading != "" {
			t.Fatalf("punctuation separator received a reading: %+v", spans[index])
		}
	}
}

func TestSekaipediaPrimaryNestedLabelVariants(t *testing.T) {
	for label, want := range map[string]string{
		"SEKAI":           "sekai",
		"VIRTUAL SINGER":  "virtual singer",
		"VIRTUAL SINGERS": "virtual singer",
		"VRITUAL SINGER":  "virtual singer",
		"unknown":         "unknown",
	} {
		if got := sekaipediaPrimaryNestedLabelKey(label); got != want {
			t.Fatalf("label %q key=%q want %q", label, got, want)
		}
	}
	primary := map[string]struct{}{"sekai": {}, "virtual singer": {}}
	if !sekaipediaNestedLabelIsPrimary("VIRTUAL SINGERS", primary) ||
		!sekaipediaNestedLabelIsPrimary("VRITUAL SINGER", primary) ||
		sekaipediaNestedLabelIsPrimary("Another Vocal", primary) {
		t.Fatal("primary nested label classification is wrong")
	}
}

func TestSekaipediaLyricsSectionFallsBackToSongTitleSection(t *testing.T) {
	content := "== Lyrics ==\n{{lyric stub|translation}}\n== Tenbin ==\n<tabber>\nGame Version = \n{{Lyrics head|columns=japanese|japanese=Japanese lyrics}}\n{{Lyrics line|japanese=歌う}}\n{{Lyrics tail}}\n</tabber>\n== Versions ==\n{{Song versions head}}\n{{Song versions tail}}\n"
	section, err := sekaipediaLyricsSection(content)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(section, "<tabber>") || strings.Contains(section, "lyric stub") {
		t.Fatalf("fallback section=%q", section)
	}
}

func TestTrimSekaipediaLeadingLyricsProse(t *testing.T) {
	value := "The Lalala is either Rin or Luka\nWhatever the mess at the end is\n{{Lyrics head|columns=japanese|japanese=Japanese lyrics}}\n{{Lyrics line|japanese=歌う}}\n"
	trimmed := trimSekaipediaLeadingLyricsProse(value)
	if !strings.HasPrefix(trimmed, "{{Lyrics head") {
		t.Fatalf("prose trim=%q", trimmed)
	}
}

func TestSekaipediaExternalSingersResolve(t *testing.T) {
	roster := sekaipediaAllSingerSet()
	ids, err := resolveSekaipediaSingerList("Natsuki Karin, Otomachi Una, GUMI", roster, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 || ids[0] != "gumi" || ids[1] != "natsuki_karin" || ids[2] != "otomachi_una" {
		t.Fatalf("external ids=%v", ids)
	}
	shortIDs, err := resolveSekaipediaSingerList("Una", roster, true)
	if err != nil || len(shortIDs) != 1 || shortIDs[0] != "otomachi_una" {
		t.Fatalf("short Una ids=%v err=%v", shortIDs, err)
	}
}
