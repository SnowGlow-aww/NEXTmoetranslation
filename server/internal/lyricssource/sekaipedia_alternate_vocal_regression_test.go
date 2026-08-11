package lyricssource

import (
	"fmt"
	"strings"
	"testing"
)

func TestSekaipediaAlternateVocalDerivesSingerSetForNonSingerTabLabel(t *testing.T) {
	body := strings.Join([]string{
		"{{Lyrics head|columns=japanese,english|japanese=Japanese lyrics|english=English translation}}",
		"{{Lyrics line|japanese={{Lyric|Shiho,Shizuku,An,Kanade|歌う}}|english=ignored translation}}",
		"{{Lyrics tail}}",
	}, "\n")
	tabs := map[string]string{
		"Alternate Vocal": "{{#tag:tabber|\nApril Fools' 2022 =\n" + body + "\n}}",
	}
	versions, err := parseSekaipediaVersions(`{{Song versions head}}
{{Song versions line|version=SEKAI|singers=Hinomori Shiho,Hinomori Shizuku,Shiraishi An,Yoisaki Kanade|audio=sample}}
{{Song versions tail}}`)
	if err != nil {
		t.Fatal(err)
	}
	alternates, err := parseSekaipediaAlternateVocals(tabs, versions)
	if err != nil {
		t.Fatal(err)
	}
	if len(alternates) != 1 {
		t.Fatalf("alternate count=%d values=%+v", len(alternates), alternates)
	}
	alternate := alternates[0]
	if alternate.Key != "alternate-vocal-shiho-shizuku-an-kanade" ||
		alternate.SingerLabel != "April Fools' 2022" || len(alternate.SingerIDs) != 4 || alternate.Game == nil {
		t.Fatalf("alternate=%+v", alternate)
	}
}

func TestSekaipediaAlternateGroupCoversPairGameAndFullByUnitRoster(t *testing.T) {
	body := func(text string) string {
		return strings.Join([]string{
			"{{Lyrics head|columns=japanese,english|japanese=Japanese lyrics|english=English translation}}",
			"{{Lyrics line|japanese={{Lyric|All|" + text + "}}|english=ignored translation}}",
			"{{Lyrics tail}}",
		}, "\n")
	}
	nested := func(leoNeed, moreMoreJump string) string {
		return "{{#tag:tabber|\nLeo/need =\n" + leoNeed +
			"\n{{!}}-{{!}}\nMORE MORE JUMP! =\n" + moreMoreJump + "\n}}"
	}
	tabs := map[string]string{
		"Alt. Group Covers":        nested(body("歌う"), body("踊る")),
		"Alt. Group Covers (Full)": nested(body("歌う"), body("踊る")),
	}
	versions, err := parseSekaipediaVersions(`{{Song versions head}}
{{Song versions line|version=SEKAI|singers=Hoshino Ichika,Tenma Saki,Mochizuki Honami,Hinomori Shiho|audio=sample}}
{{Song versions line|version=SEKAI|singers=Hanasato Minori,Kiritani Haruka,Momoi Airi,Hinomori Shizuku|audio=sample}}
{{Song versions tail}}`)
	if err != nil {
		t.Fatal(err)
	}
	alternates, err := parseSekaipediaAlternateVocals(tabs, versions)
	if err != nil {
		t.Fatal(err)
	}
	if len(alternates) != 2 {
		t.Fatalf("alternate count=%d values=%+v", len(alternates), alternates)
	}
	byLabel := make(map[string]sekaipediaAlternateVocalExtraction, len(alternates))
	for _, alternate := range alternates {
		if alternate.TabLabel != "Alt. Group Covers" || alternate.Game == nil || alternate.Full == nil || len(alternate.SingerIDs) != 4 {
			t.Fatalf("unpaired group cover=%+v", alternate)
		}
		byLabel[alternate.SingerLabel] = alternate
	}
	if alternate := byLabel["Leo/need"]; alternate.Key != "alt-group-covers-ichika-saki-honami-shiho" {
		t.Fatalf("Leo/need alternate=%+v", alternate)
	}
	if alternate := byLabel["MORE MORE JUMP!"]; alternate.Key != "alt-group-covers-minori-haruka-airi-shizuku" {
		t.Fatalf("MORE MORE JUMP alternate=%+v", alternate)
	}
}

func TestSekaipediaAlternateRenditionKeyIsBoundedAndCollisionResistant(t *testing.T) {
	ids := make([]string, 80)
	for index := range ids {
		ids[index] = fmt.Sprintf("singer-%03d", index)
	}
	key := sekaipediaAlternateRenditionKey("Alternate Vocal", ids)
	if key == "" || len("alternate-game-"+key) > 128 {
		t.Fatalf("bounded key=%q bytes=%d", key, len("alternate-game-"+key))
	}
	changed := append([]string(nil), ids...)
	changed[len(changed)-1] = "different-singer"
	changedKey := sekaipediaAlternateRenditionKey("Alternate Vocal", changed)
	if changedKey == key {
		t.Fatalf("long rendition keys collided: %q", key)
	}
	for _, current := range key {
		if current < 'a' || current > 'z' {
			if current < '0' || current > '9' {
				if current != '-' {
					t.Fatalf("key contains noncanonical rune %q: %q", current, key)
				}
			}
		}
	}
}
