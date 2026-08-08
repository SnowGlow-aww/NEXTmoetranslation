package lyricssource

import (
	"testing"

	"moesekai/server/internal/model"
)

func assertExactHanOnlyRuby(t *testing.T, text string, spans []RubySpan) {
	t.Helper()
	if !rubySpansValidForText(text, spans) || rubySpansText(spans) != text {
		t.Fatalf("ruby contract failed: text=%q spans=%+v", text, spans)
	}
	for _, span := range spans {
		if span.Reading == "" {
			continue
		}
		if !validGeneratedRubyReading(span.Reading) {
			t.Fatalf("non-kana reading: %+v", span)
		}
		for _, current := range span.Text {
			if !model.LyricsSourceRubyBaseRune(current) {
				t.Fatalf("non-Han or numeric text received ruby: %+v", span)
			}
		}
	}
}

func TestDeterministicRubyExactHanCoverage(t *testing.T) {
	tests := []struct {
		name        string
		text        string
		wantText    string
		wantReading string
	}{
		{name: "standalone ideographic motion stroke", text: "o彡゜MEIKO!", wantText: "彡", wantReading: "さん"},
		{name: "ordinary Han beside kana", text: "歌う", wantText: "歌", wantReading: "うた"},
		{name: "non-Han remains plain", text: "MEIKO! ゜"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spans, err := generateRubySpans(test.text)
			if err != nil {
				t.Fatal(err)
			}
			assertExactHanOnlyRuby(t, test.text, spans)
			for _, span := range spans {
				if span.Text == test.wantText && span.Reading == test.wantReading {
					return
				}
			}
			if test.wantText != "" || test.wantReading != "" {
				t.Fatalf("missing exact reading %q/%q in %+v", test.wantText, test.wantReading, spans)
			}
		})
	}
}

func TestRubyTreatsIdeographicZeroAsPlainNumericText(t *testing.T) {
	tests := []struct {
		name       string
		japanese   string
		romanized  string
		wantSource bool
	}{
		{
			name:       "redacted phrase",
			japanese:   "「本当は〇〇なんでしょ？」",
			romanized:  `"hontou wa 〇〇 nan desho?"`,
			wantSource: true,
		},
		{
			name:       "single redaction marker",
			japanese:   "T氏の言う通り〇しましょう",
			romanized:  "T-shi no iutoori 〇 shimashou",
			wantSource: true,
		},
		{
			name:     "only redaction markers",
			japanese: "〇〇〇〇〇！！",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var spans []RubySpan
			var ok bool
			if test.wantSource {
				spans, ok = deriveSekaipediaRuby(test.japanese, test.romanized)
			} else {
				var err error
				spans, err = generateRubySpans(test.japanese)
				ok = err == nil
			}
			if !ok || !rubySpansValidForText(test.japanese, spans) || rubySpansText(spans) != test.japanese {
				t.Fatalf("numeric redaction ruby failed: ok=%t spans=%+v", ok, spans)
			}
			foundReading := false
			for _, span := range spans {
				if span.Reading != "" {
					foundReading = true
				}
				if span.Reading != "" && containsRune(span.Text, '〇') {
					t.Fatalf("ideographic zero received ruby: %+v", span)
				}
			}
			if test.wantSource && !foundReading {
				t.Fatalf("source alignment lost all non-numeric Han readings: %+v", spans)
			}
		})
	}
}

func containsRune(value string, target rune) bool {
	for _, current := range value {
		if current == target {
			return true
		}
	}
	return false
}

func TestDeterministicRubyUniqueStandaloneDictionaryProbeFailsClosedOnAmbiguity(t *testing.T) {
	if broad, err := generateRubySpans("煌"); err == nil {
		t.Fatalf("ordinary deterministic generation accepted a probe-only Han token: %+v", broad)
	}
	spans, err := generateSekaipediaMismatchedColumnRubySpans("煌")
	if err != nil || !rubySpansValidForText("煌", spans) || len(spans) != 1 ||
		spans[0].ReadingEvidenceKind != model.LyricsSourceReadingEvidenceDeterministicDictionary ||
		spans[0].GeneratorVersion != rubyGeneratorVersion {
		t.Fatalf("bounded unique standalone dictionary probe failed: err=%v spans=%+v", err, spans)
	}
	if ambiguous, accepted := kagomeUniqueStandaloneHanRubySpans("生"); accepted {
		t.Fatalf("ambiguous standalone dictionary probe was accepted: %+v", ambiguous)
	}
	for _, value := range []string{"煌び", "A", ""} {
		if broad, accepted := kagomeUniqueStandaloneHanRubySpans(value); accepted {
			t.Fatalf("non-standalone Han probe %q was accepted: %+v", value, broad)
		}
	}
}

func TestSekaipediaFixedReviewedOrthographyUsesExactLocalTuple(t *testing.T) {
	spans, ok := deriveSekaipediaRuby("呼び続け", "yobi tsuzuke")
	if !ok {
		t.Fatal("fixed reviewed orthography source alignment failed")
	}
	for _, span := range spans {
		if span.Text != "続" {
			continue
		}
		if span.Reading != "つづ" ||
			span.ReadingEvidenceKind != model.LyricsSourceReadingEvidenceFixedReviewedToken ||
			span.GeneratorVersion != rubyGeneratorVersion || span.SourceRowOrdinal != 0 ||
			span.SourceSegmentOrdinal != 0 {
			t.Fatalf("fixed reviewed orthography span=%+v", span)
		}
		return
	}
	t.Fatalf("fixed reviewed orthography span missing: %+v", spans)
}

func TestSekaipediaHybridRubyBoundsLeadingDictionaryRepair(t *testing.T) {
	if converted, ok := romanizeSekaipediaWordToKana("ttzangi"); ok {
		t.Fatalf("malformed source word gained a broad conversion: %q", converted)
	}
	tests := []struct {
		name        string
		japanese    string
		romanized   string
		wantOK      bool
		wantText    string
		wantReading string
	}{
		{
			name:        "repeated leading consonants are bounded by a Kagome-covered prefix",
			japanese:    "残機は疾うに",
			romanized:   "ttzangi wa tou ni",
			wantOK:      true,
			wantText:    "疾",
			wantReading: "と",
		},
		{
			name:     "single leading consonant remains rejected",
			japanese: "残機は疾うに", romanized: "tzangi wa tou ni", wantOK: false,
		},
		{
			name:     "arbitrary leading source word remains rejected",
			japanese: "残機は疾うに", romanized: "qxzangi wa tou ni", wantOK: false,
		},
		{
			name:     "opaque source word cannot stand in for unresolved Han",
			japanese: "疾うに", romanized: "ttzangi tou ni", wantOK: false,
		},
		{
			name:     "opaque source word after the dictionary prefix is rejected",
			japanese: "残機は疾うに", romanized: "zanki wa ttzangi tou ni", wantOK: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spans, ok := deriveSekaipediaRuby(test.japanese, test.romanized)
			if ok != test.wantOK {
				t.Fatalf("derive ok=%t want=%t spans=%+v", ok, test.wantOK, spans)
			}
			if !ok {
				return
			}
			assertExactHanOnlyRuby(t, test.japanese, spans)
			dictionaryCount := 0
			foundSource := false
			for _, span := range spans {
				switch {
				case span.Text == "残" || span.Text == "機":
					if span.ReadingEvidenceKind != model.LyricsSourceReadingEvidenceDeterministicDictionary ||
						span.GeneratorVersion != rubyGeneratorVersion {
						t.Fatalf("Kagome prefix provenance=%+v", span)
					}
					dictionaryCount++
				case span.Text == test.wantText && span.Reading == test.wantReading:
					if span.ReadingEvidenceKind != model.LyricsSourceReadingEvidenceSourceTransliteration ||
						span.GeneratorVersion != "" {
						t.Fatalf("source fallback provenance=%+v", span)
					}
					foundSource = true
				}
			}
			if dictionaryCount != 2 || !foundSource {
				t.Fatalf("mixed dictionary/source provenance missing in %+v", spans)
			}
		})
	}
}

func TestSekaipediaSourceRubyUsesOneExactWordBoundaryPartition(t *testing.T) {
	spans, ok := deriveSekaipediaSourceRuby("一ヶ月二ヶ月", "ikkagetsu nikkagetsu")
	if !ok {
		t.Fatal("exact source-word partition was rejected")
	}
	assertExactHanOnlyRuby(t, "一ヶ月二ヶ月", spans)
	readings := map[string]string{}
	for _, span := range spans {
		if span.Reading != "" {
			readings[span.Text] = span.Reading
		}
	}
	if readings["一"] != "い" || readings["月"] != "っかげつ" || readings["二"] != "に" {
		t.Fatalf("source-word partition readings=%+v spans=%+v", readings, spans)
	}

	if ambiguous, accepted := deriveSekaipediaSourceWordRuby(
		"一二三", [][]rune{[]rune("いち"), []rune("に")},
	); accepted {
		t.Fatalf("ambiguous source-word partition accepted: %+v", ambiguous)
	}
}

func TestGeneratedRubyPreservesCallerSegmentation(t *testing.T) {
	tests := []struct {
		name  string
		texts []string
	}{
		{name: "two kaomoji performer segments", texts: []string{"o彡゜MEIKO! ", "（o彡゜MEIKO!）"}},
		{name: "Han and plain segments", texts: []string{"歌う", " MEIKO!"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			generated, err := generateRubySpansForTexts(test.texts)
			if err != nil {
				t.Fatal(err)
			}
			if len(generated) != len(test.texts) {
				t.Fatalf("generated segments=%d want=%d", len(generated), len(test.texts))
			}
			for index, text := range test.texts {
				assertExactHanOnlyRuby(t, text, generated[index])
			}
		})
	}
}
