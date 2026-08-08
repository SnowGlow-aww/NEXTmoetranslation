package lyricssource

import (
	"html"

	"strings"

	"unicode"

	"golang.org/x/text/unicode/norm"
)

func structuredSourceCellRequiresJapanese(sourceRaw string, rowCells []string, sourceColumn int, shared, stanzaBreak,
	hasJapaneseHeader, hasRomanizedColumn bool) (bool, error) {
	if shared || stanzaBreak {
		return false, nil
	}
	sourceText, err := sanitizePlaintextStructuredCell(sourceRaw, false)
	if err != nil {
		return true, err
	}
	if containsStructuredJapanese(sourceText) || isLanguageNeutralLyricText(sourceText) {
		return true, nil
	}
	if !isStructuredLatinLyricText(sourceText) || !hasRomanizedColumn {
		return true, nil
	}
	boundedJapaneseEvidence := hasJapaneseHeader &&
		(structuredRubyHasJapaneseReading(sourceRaw) || structuredAnnotatedModelCode(sourceRaw, sourceText))
	for index, raw := range rowCells {
		if index == sourceColumn || strings.TrimSpace(raw) == "" {
			continue
		}
		comparison, comparisonErr := sanitizePlaintextStructuredCell(raw, false)
		if comparisonErr != nil {
			return true, comparisonErr
		}
		if mirroredStructuredLatinEvidenceMatches(sourceText, comparison) {
			return false, nil
		}
		if boundedJapaneseEvidence && !containsStructuredJapanese(comparison) && isStructuredLatinLyricText(comparison) {
			return false, nil
		}
	}
	return true, nil
}

func structuredRubyHasJapaneseReading(raw string) bool {
	for _, match := range structuredRubyPattern.FindAllStringSubmatch(raw, -1) {
		if len(match) == 3 && containsStructuredJapanese(match[2]) {
			return true
		}
	}
	return false
}

func structuredAnnotatedModelCode(raw, sourceText string) bool {
	hasColoredText := false
	for _, match := range structuredColorPattern.FindAllStringSubmatch(raw, -1) {
		if len(match) == 3 && strings.TrimSpace(match[2]) != "■" {
			hasColoredText = true
			break
		}
	}
	if !hasColoredText {
		return false
	}
	canonical := strings.Join(strings.Fields(norm.NFKC.String(sourceText)), " ")
	if !structuredModelCodePattern.MatchString(canonical) {
		return false
	}
	hasUpper := false
	hasDigit := false
	for _, current := range canonical {
		hasUpper = hasUpper || unicode.IsUpper(current)
		hasDigit = hasDigit || unicode.IsDigit(current)
	}
	return hasUpper && hasDigit
}

func hasExplicitStructuredJapaneseHeader(headers []string) bool {
	if len(headers) == 0 {
		return false
	}
	parsed := parseStructuredLanguageLabel(strings.ReplaceAll(headers[0], "'", ""))
	return parsed.language == structuredTabLanguageJapanese && !parsed.explicitTranslation && !parsed.conflictingLanguages
}

func hasExplicitStructuredRomajiHeader(headers []string) bool {
	if len(headers) < 2 {
		return false
	}
	count := 0
	for _, header := range headers[1:] {
		parsed := parseStructuredLanguageLabel(strings.ReplaceAll(header, "'", ""))
		if parsed.language == structuredTabLanguageRomanized && !parsed.explicitTranslation && !parsed.conflictingLanguages {
			count++
		}
	}
	return count == 1
}

func mirroredStructuredLatinEvidenceMatches(source, comparison string) bool {
	left := canonicalStructuredLatinEvidence(source)
	right := canonicalStructuredLatinEvidence(comparison)
	if left == right {
		return true
	}
	if strings.ContainsAny(left, " \t") || strings.ContainsAny(right, " \t") {
		return false
	}
	return strings.Count(left, ".") >= 2 && strings.TrimSuffix(left, ".") == right ||
		strings.Count(right, ".") >= 2 && strings.TrimSuffix(right, ".") == left
}

func canonicalStructuredLatinEvidence(value string) string {
	value = norm.NFKC.String(strings.TrimSpace(value))
	value = strings.Join(strings.Fields(value), " ")
	return strings.TrimRight(value, "\"'’”」』")
}

func isStructuredLatinLyricText(value string) bool {
	hasLetter := false
	for _, current := range strings.TrimSpace(value) {
		if unicode.IsLetter(current) {
			if !unicode.In(current, unicode.Latin) {
				return false
			}
			hasLetter = true
		}
	}
	return hasLetter
}

func sanitizeStructuredHeader(raw string) (string, error) {
	header := strings.TrimSpace(raw)
	if separator := firstTopLevelStructuredPipe(header); separator >= 0 {
		attributes := strings.TrimSpace(header[:separator])
		if attributes == "" || !parseStrictStructuredAttributes(attributes, true) {
			return "", ErrUnsupportedTable
		}
		header = strings.TrimSpace(header[separator+1:])
	}
	sanitized, err := sanitizeLyricText(header)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(html.UnescapeString(sanitized)), nil
}

func structuredHeadersFromDataCells(cells []string) ([]string, bool, error) {
	if len(cells) == 0 {
		return nil, false, nil
	}
	headers := make([]string, len(cells))
	for index, cell := range cells {
		header, err := sanitizeStructuredHeader(cell)
		if err != nil {
			return nil, false, err
		}
		headers[index] = header
	}
	if !isSupportedSourceHeader(headers[0]) {
		return nil, false, nil
	}
	return headers, true, nil
}

func parseStructuredSharedCell(raw string) (string, bool, error) {
	match := structuredSharedCellPattern.FindStringSubmatch(raw)
	if match == nil {
		return raw, false, nil
	}
	remainder := strings.TrimSpace(match[2])
	if remainder == "" {
		return "", true, nil
	}
	if strings.HasPrefix(remainder, "|") {
		return strings.TrimSpace(strings.TrimPrefix(remainder, "|")), true, nil
	}
	separator := firstTopLevelStructuredPipe(remainder)
	if separator < 0 {
		return "", false, ErrUnsupportedTable
	}
	attributes := strings.TrimSpace(remainder[:separator])
	if attributes == "" || !parseStrictStructuredAttributes(attributes, true) {
		return "", false, ErrUnsupportedTable
	}
	return strings.TrimSpace(remainder[separator+1:]), true, nil
}

func parseStructuredColspanSourceCell(raw string) (string, bool, error) {
	separator := firstTopLevelStructuredPipe(raw)
	if separator < 0 {
		if strings.Contains(strings.ToLower(raw), "colspan") {
			return "", false, ErrUnsupportedTable
		}
		return raw, false, nil
	}
	attributes := strings.TrimSpace(raw[:separator])
	if !strings.Contains(strings.ToLower(attributes), "colspan") {
		return raw, false, nil
	}
	if structuredColspanPattern.FindStringSubmatch(attributes) == nil {
		return "", false, ErrUnsupportedTable
	}
	payload := strings.TrimSpace(raw[separator+1:])
	if payload == "" {
		return "", false, ErrUnsupportedTable
	}
	return payload, true, nil
}

func splitTopLevelStructuredFields(value, separator string) ([]string, bool) {
	if separator == "" {
		return nil, false
	}
	result := make([]string, 0, 2)
	start := 0
	templateDepth := 0
	linkDepth := 0
	inTag := false
	var tagQuote byte
	for index := 0; index < len(value); index++ {
		switch {
		case inTag:
			if tagQuote != 0 {
				if value[index] == tagQuote {
					tagQuote = 0
				}
				continue
			}
			if value[index] == '\'' || value[index] == '"' {
				tagQuote = value[index]
				continue
			}
			if value[index] == '>' {
				inTag = false
			}
		case index+1 < len(value) && value[index:index+2] == "{{":
			templateDepth++
			index++
		case index+1 < len(value) && value[index:index+2] == "}}":
			if templateDepth == 0 {
				return nil, false
			}
			templateDepth--
			index++
		case templateDepth == 0 && index+1 < len(value) && value[index:index+2] == "[[":
			linkDepth++
			index++
		case templateDepth == 0 && index+1 < len(value) && value[index:index+2] == "]]":
			if linkDepth == 0 {
				return nil, false
			}
			linkDepth--
			index++
		case templateDepth == 0 && linkDepth == 0 && value[index] == '<':
			inTag = true
		case templateDepth == 0 && linkDepth == 0 && strings.HasPrefix(value[index:], separator):
			result = append(result, value[start:index])
			index += len(separator) - 1
			start = index + 1
		}
	}
	if templateDepth != 0 || linkDepth != 0 || inTag || tagQuote != 0 {
		return nil, false
	}
	return append(result, value[start:]), true
}

func splitStructuredRowCells(value string, headers []string, rowStarted bool) ([]string, bool) {
	cells, ok := splitTopLevelStructuredFields(value, "||")
	if !ok || len(cells) != 1 || len(headers) != 2 || rowStarted {
		return cells, ok
	}
	if _, shared, sharedErr := parseStructuredSharedCell(strings.TrimSpace(value)); sharedErr != nil {
		return nil, false
	} else if shared {
		return cells, true
	}
	separator := firstTopLevelStructuredPipe(value)
	if separator < 0 {
		return cells, true
	}
	pair, pairOK := splitTopLevelStructuredFields(value, "|")
	if !pairOK || len(pair) != 2 || strings.TrimSpace(pair[0]) == "" || strings.TrimSpace(pair[1]) == "" {
		return nil, false
	}
	attributePrefix := strings.TrimSpace(pair[0])
	if looksLikeStructuredTableCellAttributePrefix(attributePrefix) {
		if structuredColspanPattern.FindStringSubmatch(attributePrefix) != nil {
			return cells, true
		}
		return nil, false
	}
	if !isSupportedSourceHeader(headers[0]) || !hasExplicitStructuredRomajiHeader(headers) {
		return nil, false
	}
	source, sourceErr := sanitizePlaintextStructuredCell(pair[0], false)
	comparison, comparisonErr := sanitizePlaintextStructuredCell(pair[1], false)
	if sourceErr != nil || comparisonErr != nil || !containsStructuredJapanese(source) ||
		containsStructuredJapanese(comparison) || !isStructuredLatinLyricText(comparison) {
		return nil, false
	}
	return pair, true
}

func normalizeHarmlessTrailingEmptyStructuredCell(headers, cells []string) []string {
	if len(headers) != 2 || len(cells) != len(headers)+1 || strings.TrimSpace(cells[len(cells)-1]) != "" {
		return cells
	}
	return cells[:len(cells)-1]
}

func normalizeStructuredOverflowBreak(headers, cells []string) ([]string, bool) {
	if len(headers) == 0 || len(cells) <= len(headers) || len(cells) > len(headers)+2 {
		return cells, false
	}
	extra := cells[len(headers):]
	if !structuredBreakPattern.MatchString(strings.TrimSpace(extra[len(extra)-1])) {
		return cells, false
	}
	for _, cell := range extra[:len(extra)-1] {
		if strings.TrimSpace(cell) != "" {
			return cells, false
		}
	}
	return cells[:len(headers)], true
}

func expandSafeStructuredNowiki(raw string) (string, error) {
	value := raw
	matches := structuredNowikiPattern.FindAllStringSubmatchIndex(value, -1)
	for index := len(matches) - 1; index >= 0; index-- {
		match := matches[index]
		literal := value[match[2]:match[3]]
		if literal == "" || strings.ContainsAny(literal, "\r\n") ||
			strings.Contains(literal, "{{") || strings.Contains(literal, "}}") ||
			strings.Contains(literal, "[[") || strings.Contains(literal, "]]") ||
			strings.Contains(literal, "{|") || strings.Contains(literal, "|}") ||
			strings.Contains(strings.ToLower(literal), "<nowiki") {
			return "", ErrUnsupportedTable
		}
		value = value[:match[0]] + html.EscapeString(literal) + value[match[1]:]
	}
	lower := strings.ToLower(value)
	if strings.Contains(lower, "<nowiki") || strings.Contains(lower, "</nowiki") {
		return "", ErrUnsupportedTable
	}
	return value, nil
}
