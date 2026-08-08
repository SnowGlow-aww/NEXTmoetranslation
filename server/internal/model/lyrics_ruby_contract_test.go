package model

import "testing"

func TestLyricsSourceRubyBaseRuneExcludesNumericHan(t *testing.T) {
	for _, current := range []rune{'漢', '々', '\u2ee9', '\u2ec4', '\u2ed8'} {
		if !LyricsSourceRubyBaseRune(current) {
			t.Fatalf("U+%04X should be an annotatable Han ruby base", current)
		}
	}
	for _, current := range []rune{'〇', '0', 'あ', 'A', '。'} {
		if LyricsSourceRubyBaseRune(current) {
			t.Fatalf("U+%04X should not be an annotatable Han ruby base", current)
		}
	}
}
