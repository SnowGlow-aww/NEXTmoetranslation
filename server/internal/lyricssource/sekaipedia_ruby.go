package lyricssource

import (
	"crypto/sha256"
	"encoding/hex"
	"html"

	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"moesekai/server/internal/model"
)

type sekaipediaRubyRegion struct {
	text     string
	kana     []rune
	variable bool
	annotate bool
}

type sekaipediaRubyAlignment struct {
	count int
	ends  []int
}

type sekaipediaHybridRubyRegion struct {
	text           string
	fixedKana      []rune
	dictionary     []RubySpan
	dictionaryKana []rune
	variable       bool
	annotate       bool
}

type sekaipediaHybridRubyAlignment struct {
	count      int
	cost       int
	ends       []int
	signature  string
	equivalent bool
}

// The tuple digest is SHA-256(text NUL source-reading NUL dictionary-reading).
// It is bound by the fixed F50 orthography case and the immutable F-ORTH audit;
// keeping only the digest prevents a page, music-ID, or source-text exception.
var sekaipediaFixedReviewedOrthographyDigests = map[string]struct{}{
	"ad2610f3f834f33d28f0f6070c09a6a38f7f3dc97a276d928e29d22d548a61ca": {},
}

func applySekaipediaFixedReviewedOrthography(japanese string, source []RubySpan) []RubySpan {
	dictionary, err := generateRubySpans(japanese)
	if err != nil || len(dictionary) != len(source) || !rubySpansValidForText(japanese, dictionary) {
		return source
	}
	for index := range source {
		if source[index].Text != dictionary[index].Text {
			return source
		}
	}
	result := append([]RubySpan(nil), source...)
	for index := range result {
		if result[index].Reading == "" || result[index].Reading == dictionary[index].Reading ||
			dictionary[index].Reading == "" {
			continue
		}
		digest := sha256.Sum256([]byte(result[index].Text + "\x00" + result[index].Reading + "\x00" + dictionary[index].Reading))
		if _, reviewed := sekaipediaFixedReviewedOrthographyDigests[hex.EncodeToString(digest[:])]; !reviewed {
			continue
		}
		result[index].Reading = dictionary[index].Reading
		result[index].ReadingEvidenceKind = model.LyricsSourceReadingEvidenceFixedReviewedToken
		result[index].GeneratorVersion = rubyGeneratorVersion
		result[index].SourceRowOrdinal = 0
		result[index].SourceSegmentOrdinal = 0
	}
	return result
}

func deriveSekaipediaRuby(japanese, romanized string) ([]RubySpan, bool) {
	if japanese == "" || !utf8.ValidString(japanese) {
		return nil, false
	}
	if source, ok := deriveSekaipediaSourceRuby(japanese, romanized); ok {
		return applySekaipediaFixedReviewedOrthography(japanese, source), true
	}
	if hybrid, ok := deriveSekaipediaHybridRuby(japanese, romanized); ok {
		return hybrid, true
	}
	if dictionary, err := generateRubySpans(japanese); err == nil && rubySpansValidForText(japanese, dictionary) {
		return dictionary, true
	}
	return nil, false
}

func deriveSekaipediaSourceRuby(japanese, romanized string) ([]RubySpan, bool) {
	if japanese == "" || romanized == "" || !utf8.ValidString(japanese) || !utf8.ValidString(romanized) {
		return nil, false
	}
	if !sekaipediaHasJapaneseScript(japanese) {
		return []RubySpan{{Text: japanese}}, true
	}
	words, ok := sekaipediaTransientRomanizedWords(japanese, romanized)
	if !ok || len(words) == 0 {
		return nil, false
	}
	var target strings.Builder
	wordReadings := make([][]rune, 0, len(words))
	for _, word := range words {
		kana, ok := romanizeSekaipediaWordToKana(word)
		if !ok {
			if sekaipediaIgnorableRomanizedWord(word) {
				continue
			}
			return nil, false
		}
		reading := []rune(kana)
		wordReadings = append(wordReadings, reading)
		target.WriteString(kana)
	}
	targetRunes := []rune(target.String())
	if len(targetRunes) == 0 {
		return nil, false
	}
	if spans, aligned := rubySpansFromKanaReading(japanese, targetRunes); aligned && sekaipediaSourceRubyPlausible(spans) {
		return markRubyReadingEvidence(
			spans, model.LyricsSourceReadingEvidenceSourceTransliteration, "",
		), true
	}
	if spans, aligned := rubySpansFromSourceKanaReading(japanese, targetRunes); aligned && sekaipediaSourceRubyPlausible(spans) {
		return markRubyReadingEvidence(
			spans, model.LyricsSourceReadingEvidenceSourceTransliteration, "",
		), true
	}
	spans, aligned := deriveSekaipediaSourceWordRuby(japanese, wordReadings)
	if !aligned {
		return nil, false
	}
	return markRubyReadingEvidence(
		spans, model.LyricsSourceReadingEvidenceSourceTransliteration, "",
	), true
}

// deriveSekaipediaSourceWordRuby recovers a failed whole-segment alignment only
// when the immutable source word boundaries have one exact partition of the
// same visible performer segment. Local dictionary readings bound the possible
// token boundaries, but the retained ruby comes from the fixed-source words.
// Nothing moves across a segment, source row, side, or rendition, and tied or
// otherwise ambiguous mappings fail closed.
func deriveSekaipediaSourceWordRuby(japanese string, wordReadings [][]rune) ([]RubySpan, bool) {
	japaneseRunes := []rune(japanese)
	if len(wordReadings) < 2 || len(wordReadings) > len(japaneseRunes) || initializeFuriganaTokenizer() != nil {
		return nil, false
	}
	tokenBoundaries := map[int]bool{}
	var tokenText strings.Builder
	runeCount := 0
	for _, token := range furiganaTokenizer.Tokenize(japanese) {
		if token.Surface == "" {
			continue
		}
		tokenText.WriteString(token.Surface)
		runeCount += len([]rune(token.Surface))
		tokenBoundaries[runeCount] = true
	}
	if tokenText.String() != japanese || !tokenBoundaries[len(japaneseRunes)] {
		return nil, false
	}
	type state struct {
		word  int
		start int
	}
	type result struct {
		count int
		cost  int
		spans []RubySpan
	}
	memo := map[state]result{}
	var solve func(int, int) result
	solve = func(wordIndex, start int) result {
		key := state{word: wordIndex, start: start}
		if cached, found := memo[key]; found {
			return cached
		}
		if wordIndex == len(wordReadings) {
			if start == len(japaneseRunes) {
				return result{count: 1}
			}
			return result{}
		}
		remainingWords := len(wordReadings) - wordIndex - 1
		lastEnd := len(japaneseRunes) - remainingWords
		best := result{}
		for end := start + 1; end <= lastEnd; end++ {
			if !tokenBoundaries[end] {
				continue
			}
			text := string(japaneseRunes[start:end])
			if !containsKanji(text) {
				continue
			}
			dictionary, ok := kagomeKanaReading(text)
			if !ok {
				continue
			}
			cost := sekaipediaKanaEditDistance(wordReadings[wordIndex], dictionary)
			maxCost := len(wordReadings[wordIndex]) / 3
			if maxCost < 1 {
				maxCost = 1
			}
			if cost > maxCost {
				continue
			}
			spans, ok := rubySpansFromKanaReading(text, wordReadings[wordIndex])
			if !ok {
				spans, ok = rubySpansFromSourceKanaReading(text, wordReadings[wordIndex])
			}
			if !ok || !sekaipediaSourceRubyPlausible(spans) {
				continue
			}
			child := solve(wordIndex+1, end)
			if child.count == 0 {
				continue
			}
			cost += child.cost
			switch {
			case best.count == 0 || cost < best.cost:
				best = result{
					count: child.count, cost: cost,
					spans: append(append([]RubySpan(nil), spans...), child.spans...),
				}
			case cost == best.cost:
				best.count += child.count
				if best.count > 1 {
					best.count = 2
				}
			}
		}
		memo[key] = best
		return best
	}
	aligned := solve(0, 0)
	return aligned.spans, aligned.count == 1 && rubySpansValidForText(japanese, aligned.spans)
}

func sekaipediaSourceRubyPlausible(spans []RubySpan) bool {
	for _, span := range spans {
		if span.Reading == "" {
			continue
		}
		hanCount := 0
		for _, current := range span.Text {
			if model.LyricsSourceRubyBaseRune(current) {
				hanCount++
			}
		}
		if hanCount == 0 || len([]rune(span.Reading)) > 4*hanCount+2 {
			return false
		}
	}
	return true
}

// rubySpansFromKanaReading aligns one complete kana pronunciation against the
// visible Japanese text while emitting readings only for Han-like bases. Kana
// still acts as a fixed alignment anchor, but it is never redundantly rendered
// as ruby above itself. Digits may consume pronunciation during alignment yet
// remain plain visible text.
func deriveSekaipediaHybridRuby(japanese, romanized string) ([]RubySpan, bool) {
	if japanese == "" || romanized == "" || !utf8.ValidString(japanese) || !utf8.ValidString(romanized) ||
		initializeFuriganaTokenizer() != nil {
		return nil, false
	}
	words, ok := sekaipediaTransientRomanizedWords(japanese, romanized)
	if !ok || len(words) == 0 {
		return nil, false
	}
	regions := sekaipediaHybridRubyRegions(japanese)
	if len(regions) == 0 {
		return nil, false
	}
	var target strings.Builder
	wordBoundaries := map[int]bool{}
	repairedLeadingDictionaryWord := false
	for wordIndex, word := range words {
		kana, converted := romanizeSekaipediaWordToKana(word)
		if !converted {
			switch {
			case sekaipediaIgnorableRomanizedWord(word):
				continue
			case wordIndex == 0 && !repairedLeadingDictionaryWord &&
				sekaipediaHybridLeadingDictionaryWordMatches(word, regions):
				repairedLeadingDictionaryWord = true
				continue
			default:
				return nil, false
			}
		}
		target.WriteString(kana)
		wordBoundaries[len([]rune(target.String()))] = true
	}
	targetRunes := []rune(target.String())
	if len(targetRunes) == 0 {
		return nil, false
	}
	ends, aligned := alignSekaipediaHybridRubyRegions(regions, targetRunes, wordBoundaries)
	if !aligned {
		return nil, false
	}
	result := make([]RubySpan, 0, len(regions))
	start := 0
	for index, region := range regions {
		end := ends[index]
		switch {
		case region.annotate && len(region.dictionary) > 0:
			result = appendRubySpans(result, region.dictionary...)
		case region.annotate:
			spans, ok := rubySpansFromKanaReading(region.text, targetRunes[start:end])
			if !ok {
				spans, ok = rubySpansFromSourceKanaReading(region.text, targetRunes[start:end])
			}
			if !ok || !sekaipediaSourceRubyPlausible(spans) {
				return nil, false
			}
			result = appendRubySpans(result, markRubyReadingEvidence(
				spans, model.LyricsSourceReadingEvidenceSourceTransliteration, "",
			)...)
		default:
			result = appendRubySpans(result, RubySpan{Text: region.text})
		}
		start = end
	}
	if start != len(targetRunes) || !rubySpansValidForText(japanese, result) {
		return nil, false
	}
	return result, true
}

func sekaipediaHybridRubyRegions(japanese string) []sekaipediaHybridRubyRegion {
	regions := make([]sekaipediaHybridRubyRegion, 0, len(japanese))
	for _, token := range furiganaTokenizer.Tokenize(japanese) {
		surface := token.Surface
		if surface == "" {
			continue
		}
		if containsKanji(surface) {
			region := sekaipediaHybridRubyRegion{text: surface, variable: true, annotate: true}
			features := token.Features()
			if len(features) >= 8 && features[7] != "*" {
				reading := []rune(katakanaToHiragana(features[7]))
				if validGeneratedRubyReading(string(reading)) {
					if spans, aligned := rubySpansFromKanaReading(surface, reading); aligned {
						region.dictionary = markRubyReadingEvidence(
							spans, model.LyricsSourceReadingEvidenceDeterministicDictionary, rubyGeneratorVersion,
						)
						region.dictionaryKana = reading
					}
				}
			}
			if len(region.dictionary) == 0 {
				if spans, exact := deterministicStandaloneHanRubySpans(surface); exact {
					region.dictionary = spans
					region.dictionaryKana = []rune(spans[0].Reading)
				}
			}
			regions = append(regions, region)
			continue
		}
		for _, sourceRegion := range sekaipediaRubyRegions(surface) {
			regions = append(regions, sekaipediaHybridRubyRegion{
				text: sourceRegion.text, fixedKana: sourceRegion.kana,
				variable: sourceRegion.variable, annotate: sourceRegion.annotate,
			})
		}
	}
	return regions
}

func sekaipediaHybridHasDictionaryPrefixAndFallback(regions []sekaipediaHybridRubyRegion) bool {
	prefixCovered := false
	for _, region := range regions {
		if region.text == "" {
			continue
		}
		if !prefixCovered {
			if !region.annotate || len(region.dictionary) == 0 {
				return false
			}
			prefixCovered = true
			continue
		}
		if region.annotate && len(region.dictionary) == 0 {
			return true
		}
	}
	return false
}

// sekaipediaHybridLeadingDictionaryWordMatches admits one narrowly bounded
// source typo only when Kagome already covers the leading visible Han prefix and
// later Han token still requires exact source alignment. The repair removes at
// most two repeated leading consonants, uses the strict syllable converter (not
// arbitrary letter-name expansion), and accepts only a one-kana near-match to
// the dictionary pronunciation. The source word supplies no persisted reading.
func sekaipediaHybridLeadingDictionaryWordMatches(
	word string, regions []sekaipediaHybridRubyRegion,
) bool {
	if !sekaipediaHybridHasDictionaryPrefixAndFallback(regions) {
		return false
	}
	var dictionary []rune
	for _, region := range regions {
		if region.text == "" {
			continue
		}
		if !region.annotate || len(region.dictionaryKana) == 0 {
			break
		}
		dictionary = append(dictionary, region.dictionaryKana...)
	}
	word = strings.ToLower(strings.TrimSpace(word))
	word = strings.ReplaceAll(word, "l", "r")
	if len(dictionary) == 0 || len(word) < 4 || word[0] != word[1] ||
		strings.ContainsRune("aiueon", rune(word[0])) {
		return false
	}
	for removed := 1; removed <= 2 && removed < len(word); removed++ {
		if word[removed-1] != word[0] {
			break
		}
		candidate, ok := romanizeSekaipediaWordToKanaStrict(word[removed:])
		if !ok {
			continue
		}
		candidateRunes := canonicalizeSekaipediaSourceKana(candidate)
		if len(candidateRunes) == len(dictionary) && sekaipediaKanaEditDistance(candidateRunes, dictionary) <= 1 {
			return true
		}
	}
	return false
}

func alignSekaipediaHybridRubyRegions(
	regions []sekaipediaHybridRubyRegion, target []rune, wordBoundaries map[int]bool,
) ([]int, bool) {
	wordBoundaryCounts := make([]int, len(target)+1)
	for index := 1; index <= len(target); index++ {
		wordBoundaryCounts[index] = wordBoundaryCounts[index-1]
		if wordBoundaries[index] {
			wordBoundaryCounts[index]++
		}
	}
	type state struct {
		region int
		pos    int
	}
	memo := map[state]sekaipediaHybridRubyAlignment{}
	var solve func(int, int) sekaipediaHybridRubyAlignment
	solve = func(regionIndex, pos int) sekaipediaHybridRubyAlignment {
		key := state{region: regionIndex, pos: pos}
		if cached, exists := memo[key]; exists {
			return cached
		}
		if regionIndex == len(regions) {
			if pos == len(target) {
				return sekaipediaHybridRubyAlignment{count: 1, equivalent: true}
			}
			return sekaipediaHybridRubyAlignment{}
		}
		region := regions[regionIndex]
		candidateEnds := []int{}
		switch {
		case len(region.fixedKana) > 0:
			if end, matches := sekaipediaKanaPrefix(target, pos, region.fixedKana); matches {
				candidateEnds = append(candidateEnds, end)
			}
		case !region.variable:
			candidateEnds = append(candidateEnds, pos)
		default:
			firstEnd := pos + 1
			if len(region.dictionary) > 0 || !region.annotate {
				firstEnd = pos
			}
			for end := firstEnd; end <= len(target); end++ {
				candidateEnds = append(candidateEnds, end)
			}
		}

		best := sekaipediaHybridRubyAlignment{}
		for _, end := range candidateEnds {
			if region.annotate && len(region.dictionary) == 0 {
				crossedBoundaries := wordBoundaryCounts[end] - wordBoundaryCounts[pos]
				if crossedBoundaries > 1 || crossedBoundaries == 1 && !wordBoundaries[end] {
					continue
				}
			}
			child := solve(regionIndex+1, end)
			if child.count == 0 {
				continue
			}
			edgeCost := 0
			if region.variable {
				if !wordBoundaries[end] && end != len(target) {
					edgeCost += 10
				}
				wordsConsumed := wordBoundaryCounts[end] - wordBoundaryCounts[pos]
				if wordsConsumed > 1 {
					edgeCost += wordsConsumed - 1
				} else if wordsConsumed == 0 {
					edgeCost++
				}
			}
			switch {
			case len(region.dictionaryKana) > 0:
				edgeCost += 100 * sekaipediaKanaEditDistance(target[pos:end], region.dictionaryKana)
			case len(region.fixedKana) > 0:
				edgeCost += 100 * sekaipediaKanaEditDistance(target[pos:end], region.fixedKana)
			}
			cost := edgeCost + child.cost
			signature := child.signature
			if region.annotate && len(region.dictionary) == 0 {
				signature = string(target[pos:end]) + "\x00" + signature
			}
			switch {
			case best.count == 0 || cost < best.cost:
				best = sekaipediaHybridRubyAlignment{
					count: child.count, cost: cost, ends: append([]int{end}, child.ends...),
					signature: signature, equivalent: child.equivalent,
				}
			case cost == best.cost:
				if !best.equivalent || !child.equivalent || best.signature != signature {
					best.equivalent = false
				}
				best.count += child.count
				if best.count > 1 {
					best.count = 2
				}
			}
		}
		memo[key] = best
		return best
	}
	result := solve(0, 0)
	return result.ends, result.count > 0 && result.equivalent && len(result.ends) == len(regions)
}

func kagomeNormalizedRubySpans(surface string) ([]RubySpan, bool) {
	normalized := norm.NFKC.String(surface)
	if normalized == surface || normalized == "" || !containsKanji(normalized) {
		return nil, false
	}
	reading, ok := kagomeKanaReading(normalized)
	if !ok {
		return nil, false
	}
	return rubySpansFromKanaReading(surface, reading)
}

func preferSekaipediaDictionaryRuby(source []RubySpan) []RubySpan {
	result := append([]RubySpan(nil), source...)
	for index := range result {
		if result[index].Reading == "" {
			continue
		}
		if dictionary, ok := kagomeKanaReading(result[index].Text); ok {
			result[index].Reading = string(dictionary)
		}
	}
	return result
}

func rubySpansFromKanaReading(japanese string, targetRunes []rune) ([]RubySpan, bool) {
	regions := sekaipediaRubyRegions(japanese)
	ends, ok := alignSekaipediaRubyRegions(regions, targetRunes)
	if !ok {
		return nil, false
	}
	result := make([]RubySpan, 0, len(regions))
	start := 0
	for index, region := range regions {
		end := ends[index]
		reading := ""
		if region.variable && end <= start {
			return nil, false
		}
		if region.annotate {
			reading = string(targetRunes[start:end])
			if !validGeneratedRubyReading(reading) {
				return nil, false
			}
		}
		span := RubySpan{Text: region.text, Reading: reading}
		if len(result) > 0 && result[len(result)-1].Reading == "" && span.Reading == "" {
			result[len(result)-1].Text += span.Text
		} else {
			result = append(result, span)
		}
		start = end
	}
	if start != len(targetRunes) || !rubySpansValidForText(japanese, result) {
		return nil, false
	}
	return result, true
}

func rubySpansFromSourceKanaReading(japanese string, targetRunes []rune) ([]RubySpan, bool) {
	regions := sekaipediaRubyRegions(japanese)
	ends, ok := alignSekaipediaSourceRubyRegions(regions, targetRunes)
	if !ok {
		return nil, false
	}
	result := make([]RubySpan, 0, len(regions))
	start := 0
	for index, region := range regions {
		end := ends[index]
		reading := ""
		if region.annotate {
			if end <= start {
				return nil, false
			}
			reading = string(targetRunes[start:end])
			if !validGeneratedRubyReading(reading) {
				return nil, false
			}
		}
		span := RubySpan{Text: region.text, Reading: reading}
		if len(result) > 0 && result[len(result)-1].Reading == "" && span.Reading == "" {
			result[len(result)-1].Text += span.Text
		} else {
			result = append(result, span)
		}
		start = end
	}
	if start != len(targetRunes) || !rubySpansValidForText(japanese, result) {
		return nil, false
	}
	return result, true
}

func alignSekaipediaSourceRubyRegions(regions []sekaipediaRubyRegion, target []rune) ([]int, bool) {
	type state struct {
		region int
		pos    int
	}
	type result struct {
		valid bool
		cost  int
		ends  []int
	}
	memo := map[state]result{}
	var solve func(int, int) result
	solve = func(regionIndex, pos int) result {
		key := state{region: regionIndex, pos: pos}
		if cached, exists := memo[key]; exists {
			return cached
		}
		if regionIndex == len(regions) {
			return result{valid: pos == len(target)}
		}
		region := regions[regionIndex]
		candidateEnds := []int{}
		switch {
		case len(region.kana) > 0:
			for end := pos; end <= len(target); end++ {
				candidateEnds = append(candidateEnds, end)
			}
		case !region.variable:
			candidateEnds = append(candidateEnds, pos)
		default:
			firstEnd := pos
			if region.annotate {
				firstEnd++
			}
			for end := firstEnd; end <= len(target); end++ {
				candidateEnds = append(candidateEnds, end)
			}
		}
		best := result{}
		for _, end := range candidateEnds {
			child := solve(regionIndex+1, end)
			if !child.valid {
				continue
			}
			cost := child.cost
			if len(region.kana) > 0 {
				cost += sekaipediaKanaEditDistance(target[pos:end], region.kana)
			}
			if !best.valid || cost < best.cost {
				best = result{valid: true, cost: cost, ends: append([]int{end}, child.ends...)}
			}
		}
		memo[key] = best
		return best
	}
	aligned := solve(0, 0)
	return aligned.ends, aligned.valid && len(aligned.ends) == len(regions)
}

func sekaipediaHasJapaneseScript(value string) bool {
	for _, current := range value {
		if unicode.In(current, unicode.Han, unicode.Hiragana, unicode.Katakana) {
			return true
		}
	}
	return false
}

func sekaipediaTransientRomanizedWords(japanese, romanized string) ([]string, bool) {
	sourceWords := sekaipediaASCIIWords(norm.NFKC.String(html.UnescapeString(japanese)))
	remaining := make(map[string]int, len(sourceWords))
	for _, word := range sourceWords {
		remaining[word]++
	}
	words := sekaipediaASCIIWords(norm.NFKC.String(html.UnescapeString(romanized)))
	result := make([]string, 0, len(words))
	for _, word := range words {
		if remaining[word] > 0 {
			remaining[word]--
			continue
		}
		result = append(result, word)
	}
	return result, true
}

func sekaipediaASCIIWords(value string) []string {
	words := []string{}
	var current strings.Builder
	flush := func() {
		if current.Len() > 0 {
			words = append(words, current.String())
			current.Reset()
		}
	}
	for _, character := range strings.ToLower(value) {
		switch character {
		case 'ā', 'â':
			current.WriteString("aa")
			continue
		case 'ī', 'î':
			current.WriteString("ii")
			continue
		case 'ū', 'û':
			current.WriteString("uu")
			continue
		case 'ē', 'ê':
			current.WriteString("ee")
			continue
		case 'ō', 'ô':
			current.WriteString("ou")
			continue
		}
		if character <= unicode.MaxASCII && (unicode.IsLetter(character) || unicode.IsDigit(character)) {
			current.WriteRune(character)
			continue
		}
		if unicode.Is(unicode.Mn, character) || unicode.Is(unicode.Mc, character) {
			continue
		}
		flush()
	}
	flush()
	return words
}

func sekaipediaASCIIWordHasLetter(value string) bool {
	for _, current := range value {
		if current >= 'a' && current <= 'z' {
			return true
		}
	}
	return false
}

func sekaipediaRubyRegions(japanese string) []sekaipediaRubyRegion {
	regions := []sekaipediaRubyRegion{}
	var text strings.Builder
	variable := false
	annotate := false
	flush := func() {
		if text.Len() == 0 {
			return
		}
		regions = append(regions, sekaipediaRubyRegion{
			text: text.String(), variable: variable, annotate: annotate,
		})
		text.Reset()
		variable = false
		annotate = false
	}
	for _, current := range japanese {
		if sekaipediaIsKana(current) {
			flush()
			kana := []rune(katakanaToHiragana(string(current)))
			if len(regions) > 0 && len(regions[len(regions)-1].kana) > 0 {
				regions[len(regions)-1].text += string(current)
				regions[len(regions)-1].kana = append(regions[len(regions)-1].kana, kana...)
			} else {
				regions = append(regions, sekaipediaRubyRegion{text: string(current), kana: kana})
			}
			continue
		}
		nextAnnotate := model.LyricsSourceRubyBaseRune(current)
		nextVariable := nextAnnotate || unicode.IsNumber(current)
		if text.Len() > 0 && (nextVariable != variable || nextAnnotate != annotate) {
			flush()
		}
		text.WriteRune(current)
		variable = nextVariable
		annotate = nextAnnotate
	}
	flush()
	for index := range regions {
		if len(regions[index].kana) > 0 {
			regions[index].kana = canonicalizeSekaipediaKana(regions[index].kana)
		}
	}
	return regions
}

func sekaipediaIsKana(value rune) bool {
	return unicode.In(value, unicode.Hiragana, unicode.Katakana) || value == 'ー'
}

func canonicalizeSekaipediaSourceKana(value string) []rune {
	return canonicalizeSekaipediaKana([]rune(norm.NFKC.String(strings.TrimSpace(value))))
}

func canonicalizeSekaipediaKana(input []rune) []rune {
	result := make([]rune, 0, len(input))
	for _, current := range input {
		if current >= 'ァ' && current <= 'ヶ' {
			current -= 'ァ' - 'ぁ'
		}
		if current == 'ー' {
			if vowel := sekaipediaKanaVowel(result); vowel != 0 {
				result = append(result, vowel)
			}
			continue
		}
		if unicode.In(current, unicode.Hiragana) {
			result = append(result, current)
		}
	}
	return result
}

func sekaipediaKanaVowel(value []rune) rune {
	if len(value) == 0 {
		return 0
	}
	current := value[len(value)-1]
	switch {
	case strings.ContainsRune("ぁあかがさざただなはばぱまゃやらゎわ", current):
		return 'あ'
	case strings.ContainsRune("ぃいきぎしじちぢにひびぴみりゐ", current):
		return 'い'
	case strings.ContainsRune("ぅうくぐすずつづぬふぶぷむゅゆるゔ", current):
		return 'う'
	case strings.ContainsRune("ぇえけげせぜてでねへべぺめれゑ", current):
		return 'え'
	case strings.ContainsRune("ぉおこごそぞとのほぼぽもょよろを", current):
		return 'お'
	default:
		return 0
	}
}

func alignSekaipediaRubyRegions(regions []sekaipediaRubyRegion, target []rune) ([]int, bool) {
	type state struct {
		region int
		pos    int
	}
	memo := map[state]sekaipediaRubyAlignment{}
	var solve func(int, int) sekaipediaRubyAlignment
	solve = func(regionIndex, pos int) sekaipediaRubyAlignment {
		key := state{region: regionIndex, pos: pos}
		if cached, exists := memo[key]; exists {
			return cached
		}
		if regionIndex == len(regions) {
			if pos == len(target) {
				return sekaipediaRubyAlignment{count: 1, ends: []int{}}
			}
			return sekaipediaRubyAlignment{}
		}
		region := regions[regionIndex]
		candidateEnds := []int{}
		switch {
		case len(region.kana) > 0:
			if end, matches := sekaipediaKanaPrefix(target, pos, region.kana); matches {
				candidateEnds = append(candidateEnds, end)
			}
		case !region.variable:
			candidateEnds = append(candidateEnds, pos)
		default:
			nextFixed := []rune(nil)
			for next := regionIndex + 1; next < len(regions); next++ {
				switch {
				case len(regions[next].kana) > 0:
					nextFixed = regions[next].kana
				case regions[next].variable:
					next = len(regions)
				default:
					continue
				}
				break
			}
			for end := pos + 1; end <= len(target); end++ {
				if len(nextFixed) == 0 {
					candidateEnds = append(candidateEnds, end)
					continue
				}
				if _, matches := sekaipediaKanaPrefix(target, end, nextFixed); matches {
					candidateEnds = append(candidateEnds, end)
				}
			}
			if region.annotate {
				candidateEnds = constrainSekaipediaRubyCandidateEnds(region.text, target, pos, candidateEnds)
			}
		}
		result := sekaipediaRubyAlignment{}
		for _, end := range candidateEnds {
			child := solve(regionIndex+1, end)
			if child.count == 0 {
				continue
			}
			if result.count == 0 {
				result.ends = append([]int{end}, child.ends...)
			}
			result.count += child.count
			if result.count > 1 {
				result.count = 2
				break
			}
		}
		memo[key] = result
		return result
	}
	result := solve(0, 0)
	return result.ends, result.count == 1 && len(result.ends) == len(regions)
}

func constrainSekaipediaRubyCandidateEnds(text string, target []rune, start int, candidateEnds []int) []int {
	dictionary, ok := kagomeKanaReading(text)
	if !ok {
		return candidateEnds
	}
	matched := make([]int, 0, len(candidateEnds))
	for _, end := range candidateEnds {
		if end > start && end <= len(target) && sekaipediaKanaSlicesEqual(target[start:end], dictionary) {
			matched = append(matched, end)
		}
	}
	if len(matched) == 0 {
		// Keep the fixed-source alignment available when its pronunciation differs.
		// Dictionary-first replacement is applied later only to exact covered bases.
		return candidateEnds
	}
	return matched
}

func sekaipediaKanaPrefix(target []rune, start int, expected []rune) (int, bool) {
	if start < 0 || len(target)-start < len(expected) {
		return start, false
	}
	for index, current := range expected {
		if !sekaipediaKanaEquivalent(current, target[start+index]) {
			return start, false
		}
	}
	return start + len(expected), true
}

func sekaipediaKanaEquivalent(left, right rune) bool {
	if left == right {
		return true
	}
	for _, pair := range [][2]rune{
		{'ぁ', 'あ'}, {'ぃ', 'い'}, {'ぅ', 'う'}, {'ぇ', 'え'}, {'ぉ', 'お'},
		{'じ', 'ぢ'}, {'ず', 'づ'},
		{'は', 'わ'}, {'へ', 'え'}, {'を', 'お'},
	} {
		if left == pair[0] && right == pair[1] || left == pair[1] && right == pair[0] {
			return true
		}
	}
	return false
}

func sekaipediaKanaSlicesEqual(left, right []rune) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !sekaipediaKanaEquivalent(left[index], right[index]) {
			return false
		}
	}
	return true
}

func sekaipediaKanaEditDistance(left, right []rune) int {
	previous := make([]int, len(right)+1)
	for index := range previous {
		previous[index] = index
	}
	for leftIndex, leftRune := range left {
		current := make([]int, len(right)+1)
		current[0] = leftIndex + 1
		for rightIndex, rightRune := range right {
			substitution := previous[rightIndex]
			if !sekaipediaKanaEquivalent(leftRune, rightRune) {
				substitution++
			}
			deletion := previous[rightIndex+1] + 1
			insertion := current[rightIndex] + 1
			current[rightIndex+1] = min(substitution, deletion, insertion)
		}
		previous = current
	}
	return previous[len(right)]
}

func sekaipediaDictionaryKana(japanese string) ([]rune, bool) {
	spans, err := generateRubySpans(japanese)
	if err != nil {
		return nil, false
	}
	var reading strings.Builder
	for _, span := range spans {
		if span.Reading != "" {
			reading.WriteString(span.Reading)
			continue
		}
		for _, current := range span.Text {
			switch {
			case sekaipediaIsKana(current):
				reading.WriteRune(current)
			case unicode.In(current, unicode.Han) || unicode.IsDigit(current):
				return nil, false
			}
		}
	}
	result := canonicalizeSekaipediaKana([]rune(reading.String()))
	return result, len(result) > 0
}

var sekaipediaRomajiKana = map[string]string{
	"kya": "きゃ", "kyu": "きゅ", "kyo": "きょ", "gya": "ぎゃ", "gyu": "ぎゅ", "gyo": "ぎょ",
	"sha": "しゃ", "shu": "しゅ", "sho": "しょ", "sya": "しゃ", "syu": "しゅ", "syo": "しょ",
	"ja": "じゃ", "ju": "じゅ", "jo": "じょ", "jya": "じゃ", "jyu": "じゅ", "jyo": "じょ",
	"cha": "ちゃ", "chu": "ちゅ", "cho": "ちょ", "cya": "ちゃ", "cyu": "ちゅ", "cyo": "ちょ",
	"nya": "にゃ", "nyu": "にゅ", "nyo": "にょ", "hya": "ひゃ", "hyu": "ひゅ", "hyo": "ひょ",
	"bya": "びゃ", "byu": "びゅ", "byo": "びょ", "pya": "ぴゃ", "pyu": "ぴゅ", "pyo": "ぴょ",
	"mya": "みゃ", "myu": "みゅ", "myo": "みょ", "rya": "りゃ", "ryu": "りゅ", "ryo": "りょ",
	"dya": "ぢゃ", "dyu": "ぢゅ", "dyo": "ぢょ", "she": "しぇ", "je": "じぇ", "che": "ちぇ",
	"tsa": "つぁ", "tsi": "つぃ", "tse": "つぇ", "tso": "つぉ", "thi": "てぃ", "the": "てぇ", "tho": "てょ",
	"dhi": "でぃ", "dhe": "でぇ", "dho": "でょ", "kwa": "くぁ", "kwi": "くぃ", "kwe": "くぇ", "kwo": "くぉ",
	"gwa": "ぐぁ", "gwi": "ぐぃ", "gwe": "ぐぇ", "gwo": "ぐぉ",
	"shi": "し", "chi": "ち", "tsu": "つ", "tzu": "つ", "dzi": "ぢ", "dzu": "づ",
	"fa": "ふぁ", "fi": "ふぃ", "fe": "ふぇ", "fo": "ふぉ", "fyu": "ふゅ",
	"va": "ゔぁ", "vi": "ゔぃ", "vu": "ゔ", "ve": "ゔぇ", "vo": "ゔぉ",
	"ji": "じ", "ti": "てぃ", "tu": "とぅ", "di": "でぃ", "du": "どぅ", "wi": "うぃ", "wu": "う", "we": "うぇ", "ye": "いぇ",
	"a": "あ", "i": "い", "u": "う", "e": "え", "o": "お",
	"ka": "か", "ki": "き", "ku": "く", "ke": "け", "ko": "こ",
	"ga": "が", "gi": "ぎ", "gu": "ぐ", "ge": "げ", "go": "ご",
	"sa": "さ", "si": "し", "su": "す", "se": "せ", "so": "そ",
	"za": "ざ", "zi": "じ", "zu": "ず", "ze": "ぜ", "zo": "ぞ",
	"ta": "た", "te": "て", "to": "と", "da": "だ", "de": "で", "do": "ど",
	"na": "な", "ni": "に", "nu": "ぬ", "ne": "ね", "no": "の",
	"ha": "は", "hi": "ひ", "fu": "ふ", "hu": "ふ", "he": "へ", "ho": "ほ",
	"ba": "ば", "bi": "び", "bu": "ぶ", "be": "べ", "bo": "ぼ",
	"pa": "ぱ", "pi": "ぴ", "pu": "ぷ", "pe": "ぺ", "po": "ぽ",
	"ma": "ま", "mi": "み", "mu": "む", "me": "め", "mo": "も",
	"ya": "や", "yu": "ゆ", "yo": "よ", "ra": "ら", "ri": "り", "ru": "る", "re": "れ", "ro": "ろ",
	"wa": "わ", "wo": "を",
}

func sekaipediaIgnorableRomanizedWord(value string) bool {
	return len(value) == 1 && value[0] >= 'a' && value[0] <= 'z' && !strings.ContainsRune("aiueon", rune(value[0]))
}

func sekaipediaRomanizedLetterNames(value string) (string, bool) {
	letterNames := map[byte]string{
		'b': "びー", 'c': "しー", 'd': "でぃー", 'f': "えふ", 'g': "じー", 'h': "えいち",
		'j': "じぇー", 'k': "けー", 'm': "えむ", 'n': "えぬ", 'p': "ぴー", 'q': "きゅー", 'r': "あーる",
		's': "えす", 't': "てぃー", 'v': "ぶい", 'w': "だぶりゅー", 'x': "えっくす", 'y': "わい", 'z': "ぜっと",
	}
	if value == "" || len(value) > 8 {
		return "", false
	}
	var result strings.Builder
	for index := 0; index < len(value); index++ {
		name, ok := letterNames[value[index]]
		if !ok {
			return "", false
		}
		result.WriteString(name)
	}
	return result.String(), result.Len() > 0
}

func romanizeSekaipediaWordToKana(value string) (string, bool) {
	return romanizeSekaipediaWordToKanaMode(value, true)
}

func romanizeSekaipediaWordToKanaStrict(value string) (string, bool) {
	return romanizeSekaipediaWordToKanaMode(value, false)
}

func romanizeSekaipediaWordToKanaMode(value string, allowLetterNames bool) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "l", "r")
	if value == "" {
		return "", false
	}
	if value == "m" {
		return "ん", true
	}
	if allowLetterNames {
		if letterName, ok := map[string]string{
			"h": "えいち",
			"r": "あーる",
			"w": "だぶりゅー",
			"y": "わい",
		}[value]; ok {
			return letterName, true
		}
	}
	for _, current := range value {
		if current < 'a' || current > 'z' {
			return "", false
		}
	}
	var result strings.Builder
	for index := 0; index < len(value); {
		if index+1 < len(value) && value[index] == value[index+1] && value[index] != 'a' && value[index] != 'i' &&
			value[index] != 'u' && value[index] != 'e' && value[index] != 'o' && value[index] != 'n' {
			result.WriteRune('っ')
			index++
			continue
		}
		if strings.HasPrefix(value[index:], "tch") {
			result.WriteRune('っ')
			index++
			continue
		}
		if value[index] == 'n' {
			if index+1 == len(value) {
				result.WriteRune('ん')
				index++
				continue
			}
			next := value[index+1]
			if next == 'n' || !strings.ContainsRune("aiueoy", rune(next)) {
				result.WriteRune('ん')
				index++
				continue
			}
		}
		matched := false
		for length := 3; length >= 1; length-- {
			if index+length > len(value) {
				continue
			}
			if kana, exists := sekaipediaRomajiKana[value[index:index+length]]; exists {
				result.WriteString(kana)
				index += length
				matched = true
				break
			}
		}
		if !matched {
			if !allowLetterNames {
				return "", false
			}
			letterNames, ok := sekaipediaRomanizedLetterNames(value[index:])
			if !ok {
				return "", false
			}
			result.WriteString(letterNames)
			return result.String(), true
		}
	}
	return result.String(), result.Len() > 0
}
