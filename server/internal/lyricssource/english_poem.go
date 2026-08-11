package lyricssource

import (
	"errors"
	"html"
	"regexp"
	"strings"
	"unicode"
)

var (
	englishPoemEnvelopePattern    = regexp.MustCompile(`(?s)^[ \t\n]*(<poem(?: style="margin-left:1em;")?>)(.*)</poem>[ \t\n]*$`)
	englishPoemNestedHeading      = regexp.MustCompile(`(?m)^[ \t]*={2,}[^=\n].*?={2,}[ \t]*$`)
	englishPoemExternalLink       = regexp.MustCompile(`(?i)\[(?:https?:|ftp:|mailto:|//)[^]\n]*\]`)
	englishPoemListMarkup         = regexp.MustCompile(`(?m)^[ \t]*[*#:;]`)
	englishPoemHorizontalRule     = regexp.MustCompile(`(?m)^[ \t]*----+[ \t]*$`)
	englishPoemBehaviorSwitch     = regexp.MustCompile(`__[A-Za-z][A-Za-z0-9_]*__`)
	englishMixedBreakPrefix       = regexp.MustCompile(`(?i)^<br\s*/?>`)
	englishPoemTranslationSignals = []string{"translation", "translated", "romanization", "romanized", "romaji"}
)

// extractCategoryAwareLyrics preserves the existing structured parser as the
// primary path. The English <poem> format is eligible only for a production
// page whose categories and Lyrics section satisfy the strict fallback gates.
func extractCategoryAwareLyrics(content string, categories []string) (Extraction, error) {
	if hasLyricsTextRestriction(content, categories) {
		return Extraction{}, ErrRestrictedReprint
	}
	if hasExplicitUnpublishedLyrics(content) {
		return Extraction{}, ErrLyricsUnpublished
	}

	extraction, err := extractStructuredLyrics(content)
	section, eligible := englishPoemFallbackSection(content)
	if eligible && englishPoemCategoriesEligible(categories) && containsEnglishPoemEnvelope(section) &&
		strings.Contains(section, "{|") && !strings.Contains(strings.ToLower(section), "<tabber") {
		// Never accept only the table from a mixed English poem/table envelope:
		// doing so would silently drop source text. The mixed form is eligible
		// only when the fixed page itself is categorized as partially bilingual.
		if !englishPoemMixedCategoriesEligible(categories) {
			return Extraction{}, ErrUnsupportedTable
		}
		mixed, mixedErr := extractStrictMixedEnglishLyrics(section)
		if mixedErr != nil {
			return Extraction{}, mixedErr
		}
		return mixed, nil
	}
	if err == nil {
		// A sole poem envelope is not an implicit structured source. In
		// particular, a stray competing-script letter must not make the legacy
		// plain parser accept an otherwise category-gated English poem. A poem in
		// an unselected translation tab is safe because structured selection has
		// already produced the source extraction.
		if eligible && englishPoemEnvelopePattern.MatchString(section) {
			return Extraction{}, ErrUnsupportedTable
		}
		return extraction, nil
	}
	if !errors.Is(err, ErrMissingLyrics) || !englishPoemCategoriesEligible(categories) || !eligible {
		return Extraction{}, err
	}
	lowerSection := strings.ToLower(section)
	if strings.Contains(section, "{|") || strings.Contains(section, "|}") ||
		strings.Contains(lowerSection, "<tabber") || strings.Contains(lowerSection, "</tabber") {
		return Extraction{}, ErrUnsupportedTable
	}
	return extractStrictEnglishPoem(section)
}

func englishPoemCategoriesEligible(categories []string) bool {
	hasEnglishSongs := false
	for _, category := range categories {
		normalized := normalizeEnglishPoemCategory(category)
		if normalized == "english songs" {
			hasEnglishSongs = true
		}
		for _, signal := range englishPoemTranslationSignals {
			if strings.Contains(normalized, signal) {
				return false
			}
		}
	}
	return hasEnglishSongs
}

func englishPoemMixedCategoriesEligible(categories []string) bool {
	if !englishPoemCategoriesEligible(categories) {
		return false
	}
	for _, category := range categories {
		if normalizeEnglishPoemCategory(category) == "partially bilingual songs" {
			return true
		}
	}
	return false
}

func normalizeEnglishPoemCategory(category string) string {
	category = strings.TrimSpace(category)
	if len(category) >= len("Category:") && strings.EqualFold(category[:len("Category:")], "Category:") {
		category = strings.TrimSpace(category[len("Category:"):])
	}
	category = strings.ReplaceAll(category, "_", " ")
	return strings.ToLower(strings.Join(strings.Fields(category), " "))
}

func englishPoemFallbackSection(content string) (string, bool) {
	location := headingPattern.FindStringIndex(content)
	if location == nil {
		return "", false
	}
	section := strings.ReplaceAll(content[location[1]:], "\r", "")
	if next := topLevelHeadingPattern.FindStringIndex(section); next != nil {
		section = section[:next[0]]
	}
	return section, true
}

func containsEnglishPoemEnvelope(section string) bool {
	lower := strings.ToLower(section)
	return strings.Contains(lower, "<poem") || strings.Contains(lower, "</poem")
}

func extractStrictEnglishPoem(section string) (Extraction, error) {
	lower := strings.ToLower(section)
	openCount := strings.Count(lower, "<poem")
	closeCount := strings.Count(lower, "</poem")
	if openCount == 0 && closeCount == 0 {
		return Extraction{}, ErrMissingLyrics
	}
	if openCount != 1 || closeCount != 1 || englishPoemNestedHeading.MatchString(section) {
		return Extraction{}, ErrUnsupportedTable
	}

	match := englishPoemEnvelopePattern.FindStringSubmatch(section)
	if match == nil {
		return Extraction{}, ErrUnsupportedTable
	}
	body, err := expandStrictEnglishPoemLinks(match[2])
	if err != nil || hasEnglishPoemResidualMarkup(body) {
		return Extraction{}, ErrUnsupportedTable
	}

	lines, err := englishPoemLines(body)
	if err != nil {
		return Extraction{}, err
	}
	return Extraction{
		Version:              LyricsVersion{Kind: "original", Label: "Original Version"},
		Performers:           []Performer{},
		RubyGeneratorVersion: rubyGeneratorVersion,
		Lines:                lines,
	}, nil
}

type strictMixedEnglishBlockKind uint8

const (
	strictMixedEnglishPoemBlock strictMixedEnglishBlockKind = iota + 1
	strictMixedEnglishTableBlock
)

func extractStrictMixedEnglishLyrics(section string) (Extraction, error) {
	section = strings.ReplaceAll(section, "\r", "")
	lower := strings.ToLower(section)
	if englishPoemNestedHeading.MatchString(section) || strings.Contains(lower, "<tabber") || strings.Contains(lower, "</tabber") {
		return Extraction{}, ErrUnsupportedTable
	}

	rest := strings.TrimSpace(section)
	kinds := make([]strictMixedEnglishBlockKind, 0, 3)
	lines := make([]StructuredLine, 0)
	totalBytes := 0
	for rest != "" {
		var (
			kind       strictMixedEnglishBlockKind
			block      string
			extraction Extraction
			err        error
		)
		if end, ok := strictEnglishPoemBlockEnd(rest); ok {
			kind = strictMixedEnglishPoemBlock
			block = rest[:end]
			extraction, err = extractStrictEnglishPoem(block)
		} else if strings.HasPrefix(rest, "{|") {
			end := strings.Index(rest, "|}")
			if end < 0 {
				return Extraction{}, ErrUnsupportedTable
			}
			block = rest[:end+2]
			if strings.Contains(block[2:], "{|") || !strictMixedEnglishJapaneseRomajiTable(block) {
				return Extraction{}, ErrUnsupportedTable
			}
			kind = strictMixedEnglishTableBlock
			extraction, err = extractStructuredLyrics("== Lyrics ==\n" + block)
		} else {
			return Extraction{}, ErrUnsupportedTable
		}
		if err != nil || len(extraction.Lines) == 0 {
			if err != nil {
				return Extraction{}, err
			}
			return Extraction{}, ErrMissingLyrics
		}
		if len(kinds) > 0 && kinds[len(kinds)-1] == kind {
			return Extraction{}, ErrUnsupportedTable
		}
		if len(lines) > 0 {
			extraction.Lines[0].StanzaBreakBefore = true
		}
		for _, line := range extraction.Lines {
			if len(lines) >= maxExtractedLines || totalBytes > maxExtractedTextBytes-len(line.Japanese) {
				return Extraction{}, ErrLyricsTooLarge
			}
			totalBytes += len(line.Japanese)
			lines = append(lines, line)
		}
		kinds = append(kinds, kind)
		rest = strings.TrimSpace(rest[len(block):])
		for {
			match := englishMixedBreakPrefix.FindStringIndex(rest)
			if match == nil {
				break
			}
			rest = strings.TrimSpace(rest[match[1]:])
		}
	}
	if len(kinds) < 3 || kinds[0] != strictMixedEnglishPoemBlock || kinds[len(kinds)-1] != strictMixedEnglishPoemBlock {
		return Extraction{}, ErrUnsupportedTable
	}
	hasTable := false
	for _, kind := range kinds {
		if kind == strictMixedEnglishTableBlock {
			hasTable = true
			break
		}
	}
	if !hasTable {
		return Extraction{}, ErrUnsupportedTable
	}
	return Extraction{
		Version:              LyricsVersion{Kind: "original", Label: "Original Version"},
		Performers:           []Performer{},
		RubyGeneratorVersion: rubyGeneratorVersion,
		Lines:                lines,
	}, nil
}

func strictEnglishPoemBlockEnd(value string) (int, bool) {
	open := ""
	for _, candidate := range []string{"<poem>", `<poem style="margin-left:1em;">`} {
		if strings.HasPrefix(value, candidate) {
			open = candidate
			break
		}
	}
	if open == "" {
		return 0, false
	}
	body := value[len(open):]
	end := strings.Index(body, "</poem>")
	if end < 0 {
		return 0, false
	}
	return len(open) + end + len("</poem>"), true
}

func strictMixedEnglishJapaneseRomajiTable(table string) bool {
	headers := make([]string, 0, 2)
	inTable := false
	for _, rawLine := range strings.Split(table, "\n") {
		line := strings.TrimSpace(rawLine)
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "{|"):
			if inTable {
				return false
			}
			inTable = true
		case line == "|}" || strings.HasPrefix(line, "|-"):
			goto validate
		case strings.HasPrefix(line, "!"):
			if !inTable {
				return false
			}
			fields, ok := splitTopLevelStructuredFields(strings.TrimPrefix(line, "!"), "!!")
			if !ok {
				return false
			}
			for _, field := range fields {
				header, err := sanitizeStructuredHeader(field)
				if err != nil || header == "" {
					return false
				}
				headers = append(headers, header)
			}
		default:
			return false
		}
	}

validate:
	if len(headers) != 2 {
		return false
	}
	source := parseStructuredLanguageLabel(strings.ReplaceAll(headers[0], "'", ""))
	romanized := parseStructuredLanguageLabel(strings.ReplaceAll(headers[1], "'", ""))
	return source.language == structuredTabLanguageJapanese && !source.explicitTranslation && !source.conflictingLanguages &&
		romanized.language == structuredTabLanguageRomanized && !romanized.explicitTranslation && !romanized.conflictingLanguages
}

// expandStrictEnglishPoemLinks preserves only the visible text of simple
// internal Wiki links. Namespaced, anchored, nested, multiline, or multi-pipe
// links remain unsupported so a source poem cannot pull in non-lyric markup.
func expandStrictEnglishPoemLinks(body string) (string, error) {
	var result strings.Builder
	for cursor := 0; cursor < len(body); {
		start := strings.Index(body[cursor:], "[[")
		if start < 0 {
			result.WriteString(body[cursor:])
			break
		}
		start += cursor
		result.WriteString(body[cursor:start])
		end := strings.Index(body[start+2:], "]]")
		if end < 0 {
			return "", ErrUnsupportedTable
		}
		end += start + 2
		inner := body[start+2 : end]
		if inner == "" || strings.ContainsAny(inner, "\r\n[]{}<>") || strings.Count(inner, "|") > 1 {
			return "", ErrUnsupportedTable
		}
		parts := strings.Split(inner, "|")
		target := strings.TrimSpace(parts[0])
		display := target
		if len(parts) == 2 {
			display = strings.TrimSpace(parts[1])
		}
		if target == "" || display == "" || strings.ContainsAny(target, ":#") {
			return "", ErrUnsupportedTable
		}
		hasLatin := false
		if !validEnglishPoemText(display, &hasLatin) {
			return "", ErrUnsupportedTable
		}
		result.WriteString(display)
		cursor = end + 2
	}
	value := result.String()
	if strings.Contains(value, "[[") || strings.Contains(value, "]]") {
		return "", ErrUnsupportedTable
	}
	return value, nil
}

func hasEnglishPoemResidualMarkup(body string) bool {
	decoded := html.UnescapeString(body)
	for _, value := range []string{body, decoded} {
		for _, marker := range []string{"{{", "}}", "[[", "]]", "{|", "|}", "<", ">", "''", "~~~"} {
			if strings.Contains(value, marker) {
				return true
			}
		}
		if englishPoemExternalLink.MatchString(value) || englishPoemListMarkup.MatchString(value) ||
			englishPoemHorizontalRule.MatchString(value) || englishPoemBehaviorSwitch.MatchString(value) {
			return true
		}
	}
	return false
}

func englishPoemLines(body string) ([]StructuredLine, error) {
	rawLines := strings.Split(body, "\n")
	lines := make([]StructuredLine, 0, len(rawLines))
	stanzaBreak := false
	totalBytes := 0
	hasLatinLetter := false

	for _, rawLine := range rawLines {
		line := html.UnescapeString(rawLine)
		if strings.TrimSpace(line) == "" {
			if len(lines) > 0 {
				stanzaBreak = true
			}
			continue
		}
		if strings.ContainsAny(line, "\r\n") || !validEnglishPoemText(line, &hasLatinLetter) {
			return nil, ErrUnsupportedTable
		}
		if len(line) > maxExtractedLineBytes || len(lines) >= maxExtractedLines || totalBytes > maxExtractedTextBytes-len(line) {
			return nil, ErrLyricsTooLarge
		}
		totalBytes += len(line)
		lines = append(lines, StructuredLine{
			Japanese:          line,
			StanzaBreakBefore: stanzaBreak,
			Segments: []LyricsSegment{{
				Text:         line,
				PerformerIDs: []string{},
				Ruby:         []RubySpan{{Text: line, Reading: ""}},
			}},
			TrailingPerformerIDs: []string{},
		})
		stanzaBreak = false
	}
	if len(lines) == 0 || !hasLatinLetter {
		return nil, ErrMissingLyrics
	}
	return lines, nil
}

func validEnglishPoemText(value string, hasLatinLetter *bool) bool {
	for _, current := range value {
		if unicode.IsLetter(current) {
			if !unicode.In(current, unicode.Latin) {
				return false
			}
			*hasLatinLetter = true
			continue
		}
		if unicode.IsNumber(current) || unicode.IsSpace(current) || unicode.IsPunct(current) ||
			unicode.IsSymbol(current) || unicode.IsMark(current) {
			continue
		}
		return false
	}
	return true
}
