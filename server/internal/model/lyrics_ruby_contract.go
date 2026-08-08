package model

import "unicode"

// LyricsSourceRubyBaseRune reports whether a visible rune may receive a kana
// reading under the strict lyrics contract. Go classifies U+3007 IDEOGRAPHIC
// NUMBER ZERO as Han, but numeric characters must remain plain text rather than
// becoming ruby bases.
func LyricsSourceRubyBaseRune(current rune) bool {
	return unicode.In(current, unicode.Han) && !unicode.IsNumber(current)
}
