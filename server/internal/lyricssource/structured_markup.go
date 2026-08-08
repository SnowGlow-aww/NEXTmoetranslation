package lyricssource

import (
	"html"

	"strings"

	"unicode"
)

func expandSafeStructuredTemplates(raw string) (string, error) {
	value, err := expandSafeStructuredNowiki(raw)
	if err != nil {
		return "", err
	}
	for {
		changed := false
		value = structuredInterwikiPattern.ReplaceAllStringFunc(value, func(template string) string {
			match := structuredInterwikiPattern.FindStringSubmatch(template)
			if match == nil {
				return template
			}
			parts := strings.Split(match[1], "|")
			if len(parts) != 2 && len(parts) != 3 || strings.TrimSpace(parts[0]) == "" ||
				len(parts) == 2 && strings.TrimSpace(parts[1]) == "" {
				return template
			}
			display := strings.TrimSpace(parts[len(parts)-1])
			if display == "" {
				return template
			}
			changed = true
			return display
		})
		value = structuredDisplayTemplatePattern.ReplaceAllStringFunc(value, func(template string) string {
			match := structuredDisplayTemplatePattern.FindStringSubmatch(template)
			if match == nil {
				return template
			}
			parts := strings.Split(match[2], "|")
			var display string
			switch strings.ToLower(match[1]) {
			case "vlw", "wp":
				if len(parts) != 1 && len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
					return template
				}
				display = strings.TrimSpace(parts[len(parts)-1])
			case "color":
				if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
					return template
				}
				display = strings.TrimSpace(parts[1])
			}
			if display == "" {
				return template
			}
			changed = true
			return display
		})
		for {
			match := structuredRubyPattern.FindStringSubmatchIndex(value)
			if match == nil {
				break
			}
			replacement := value[match[2]:match[3]]
			value = value[:match[0]] + replacement + value[match[1]:]
			changed = true
		}
		if !changed {
			break
		}
	}
	if strings.Contains(strings.ToLower(value), "{{ruby") {
		return "", ErrUnsupportedTable
	}
	return value, nil
}

func validateStructuredMarkup(value string) bool {
	stack := make([]string, 0, 4)
	for offset := 0; offset < len(value); {
		relativeStart := strings.IndexByte(value[offset:], '<')
		if relativeStart < 0 {
			break
		}
		start := offset + relativeStart
		if strings.HasPrefix(value[start:], "<!--") {
			relativeEnd := strings.Index(value[start+4:], "-->")
			if relativeEnd < 0 {
				return false
			}
			offset = start + 4 + relativeEnd + 3
			continue
		}
		end := -1
		var quote byte
		for index := start + 1; index < len(value); index++ {
			if quote != 0 {
				if value[index] == quote {
					quote = 0
				}
				continue
			}
			if value[index] == '\'' || value[index] == '"' {
				quote = value[index]
				continue
			}
			if value[index] == '<' {
				return false
			}
			if value[index] == '>' {
				end = index
				break
			}
		}
		if end < 0 || quote != 0 {
			return false
		}
		token := strings.TrimSpace(value[start+1 : end])
		closing := strings.HasPrefix(token, "/")
		if closing {
			token = strings.TrimSpace(strings.TrimPrefix(token, "/"))
		}
		selfClosing := strings.HasSuffix(token, "/")
		if selfClosing {
			token = strings.TrimSpace(strings.TrimSuffix(token, "/"))
		}
		name := structuredAttributeName.FindString(token)
		if name == "" {
			return false
		}
		name = strings.ToLower(name)
		attributes := strings.TrimSpace(token[len(name):])
		displayOnly := true
		switch name {
		case "br":
			if closing || attributes != "" {
				return false
			}
			offset = end + 1
			continue
		case "ref":
			displayOnly = false
		case "nowiki":
			if attributes != "" {
				return false
			}
		case "b", "big", "center", "del", "em", "i", "ins", "mark", "ruby", "rp", "rt", "s", "small", "span", "strike", "strong", "sub", "sup", "u":
		default:
			return false
		}
		if closing {
			if selfClosing || attributes != "" || len(stack) == 0 || stack[len(stack)-1] != name {
				return false
			}
			stack = stack[:len(stack)-1]
		} else {
			if attributes != "" && !parseStrictStructuredAttributes(attributes, displayOnly) {
				return false
			}
			if !selfClosing {
				stack = append(stack, name)
			}
		}
		offset = end + 1
	}
	return len(stack) == 0
}

func stripStructuredWholeCellEmphasis(value string) string {
	trimmed := strings.TrimSpace(value)
	for _, marker := range []string{"'''''", "'''", "''"} {
		if len(trimmed) <= len(marker)*2 || !strings.HasPrefix(trimmed, marker) || !strings.HasSuffix(trimmed, marker) {
			continue
		}
		inner := strings.TrimSpace(trimmed[len(marker) : len(trimmed)-len(marker)])
		if inner != "" {
			return inner
		}
	}
	return value
}

func sanitizePlaintextStructuredCell(raw string, requireJapanese bool) (string, error) {
	value, err := expandSafeStructuredTemplates(raw)
	if err != nil {
		return "", err
	}
	if !validateStructuredMarkup(value) {
		return "", ErrUnsupportedTable
	}
	value = markupPattern.ReplaceAllString(value, "")
	value = linkPattern.ReplaceAllString(value, "$1")
	value = stripStructuredWholeCellEmphasis(value)
	var text strings.Builder
	cursor := 0
	for _, match := range structuredColorPattern.FindAllStringSubmatchIndex(value, -1) {
		text.WriteString(value[cursor:match[0]])
		colored := value[match[4]:match[5]]
		if strings.TrimSpace(colored) != "■" {
			text.WriteString(colored)
		}
		cursor = match[1]
	}
	text.WriteString(value[cursor:])
	plain := strings.TrimSpace(html.UnescapeString(text.String()))
	if strings.Contains(plain, "{{") || strings.Contains(plain, "}}") || strings.Contains(plain, "[[") || strings.Contains(plain, "]]") ||
		strings.Contains(plain, "{|") || strings.Contains(plain, "|}") {
		return "", ErrUnsupportedTable
	}
	if requireJapanese && !containsStructuredJapanese(plain) && !isLanguageNeutralLyricText(plain) {
		return "", ErrUnsupportedTable
	}
	return plain, nil
}

func sanitizeStructuredCell(raw string, allowedPerformers map[string]struct{}, requireJapanese bool) ([]structuredRawSegment, []string, error) {
	value, err := expandSafeStructuredTemplates(raw)
	if err != nil {
		return nil, nil, err
	}
	if !validateStructuredMarkup(value) {
		return nil, nil, ErrUnsupportedTable
	}
	value = markupPattern.ReplaceAllString(value, "")
	value = linkPattern.ReplaceAllString(value, "$1")
	value = stripStructuredWholeCellEmphasis(value)
	segments := make([]structuredRawSegment, 0, 3)
	pendingSquares := make([]string, 0, 3)
	cursor := 0
	matches := structuredColorPattern.FindAllStringSubmatchIndex(value, -1)
	for _, match := range matches {
		performerID := normalizePerformerID(value[match[2]:match[3]])
		if performerID == "" {
			return nil, nil, ErrUnsupportedTable
		}
		if len(allowedPerformers) > 0 {
			if _, ok := allowedPerformers[performerID]; !ok {
				return nil, nil, ErrUnsupportedTable
			}
		}
		coloredText := value[match[4]:match[5]]
		isSquare := strings.TrimSpace(coloredText) == "■"
		between := value[cursor:match[0]]
		if len(pendingSquares) > 0 && (strings.TrimSpace(between) != "" || !isSquare) {
			if err := assignEmbeddedStructuredPerformerRun(segments, pendingSquares); err != nil {
				return nil, nil, err
			}
			pendingSquares = pendingSquares[:0]
			between = strings.TrimLeftFunc(between, unicode.IsSpace)
		}
		if isSquare {
			between = strings.TrimRightFunc(between, unicode.IsSpace)
		}
		if between != "" && !(len(pendingSquares) > 0 && strings.TrimSpace(between) == "") {
			segments = appendPlainStructuredSegment(segments, between)
		}
		if isSquare {
			var appendErr error
			pendingSquares, appendErr = appendUniqueStructuredPerformer(pendingSquares, performerID)
			if appendErr != nil {
				return nil, nil, appendErr
			}
		} else {
			segments = append(segments, structuredRawSegment{text: coloredText, performerIDs: []string{performerID}})
		}
		cursor = match[1]
	}
	if remainder := value[cursor:]; remainder != "" {
		if len(pendingSquares) > 0 && strings.TrimSpace(remainder) != "" {
			if err := assignEmbeddedStructuredPerformerRun(segments, pendingSquares); err != nil {
				return nil, nil, err
			}
			pendingSquares = pendingSquares[:0]
			remainder = strings.TrimLeftFunc(remainder, unicode.IsSpace)
		}
		if len(pendingSquares) == 0 {
			segments = appendPlainStructuredSegment(segments, remainder)
		}
	}
	trailing := append([]string{}, pendingSquares...)
	var rawText strings.Builder
	var decodedText strings.Builder
	for index := range segments {
		rawText.WriteString(segments[index].text)
		segments[index].text = html.UnescapeString(segments[index].text)
		decodedText.WriteString(segments[index].text)
	}
	if html.UnescapeString(rawText.String()) != decodedText.String() {
		return nil, nil, ErrUnsupportedTable
	}
	segments = trimStructuredSegmentBoundaries(segments)
	segments = compactStructuredSegments(segments)
	var text strings.Builder
	for _, segment := range segments {
		text.WriteString(segment.text)
	}
	plain := text.String()
	if strings.Contains(plain, "{{") || strings.Contains(plain, "}}") || strings.Contains(plain, "[[") || strings.Contains(plain, "]]") ||
		strings.Contains(plain, "{|") || strings.Contains(plain, "|}") {
		return nil, nil, ErrUnsupportedTable
	}
	if requireJapanese && !containsStructuredJapanese(plain) && !isLanguageNeutralLyricText(plain) {
		return nil, nil, ErrUnsupportedTable
	}
	return segments, trailing, nil
}

// Embedded square runs annotate the immediately preceding uncolored text.
// A square run at the end of the cell remains a line-level fallback so the
// existing import contract can apply it to otherwise unassigned segments.
func assignEmbeddedStructuredPerformerRun(segments []structuredRawSegment, performerIDs []string) error {
	if len(segments) == 0 || len(performerIDs) == 0 {
		return ErrUnsupportedTable
	}
	last := &segments[len(segments)-1]
	if strings.TrimSpace(last.text) == "" || len(last.performerIDs) > 0 {
		return ErrUnsupportedTable
	}
	last.performerIDs = append([]string{}, performerIDs...)
	return nil
}

func appendUniqueStructuredPerformer(performerIDs []string, performerID string) ([]string, error) {
	for _, existing := range performerIDs {
		if existing == performerID {
			return nil, ErrUnsupportedTable
		}
	}
	return append(performerIDs, performerID), nil
}

func isLanguageNeutralLyricText(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	hasLetter := false
	for _, current := range value {
		if unicode.IsLetter(current) {
			hasLetter = true
			continue
		}
		if unicode.IsNumber(current) || unicode.IsSpace(current) || unicode.IsPunct(current) || unicode.IsSymbol(current) {
			continue
		}
		return false
	}
	return !hasLetter
}

func appendPlainStructuredSegment(segments []structuredRawSegment, text string) []structuredRawSegment {
	if text == "" {
		return segments
	}
	return append(segments, structuredRawSegment{text: text, performerIDs: []string{}})
}

func trimStructuredSegmentBoundaries(segments []structuredRawSegment) []structuredRawSegment {
	for len(segments) > 0 {
		segments[0].text = strings.TrimLeftFunc(segments[0].text, unicode.IsSpace)
		if segments[0].text != "" {
			break
		}
		segments = segments[1:]
	}
	for len(segments) > 0 {
		last := len(segments) - 1
		segments[last].text = strings.TrimRightFunc(segments[last].text, unicode.IsSpace)
		if segments[last].text != "" {
			break
		}
		segments = segments[:last]
	}
	return segments
}

func compactStructuredSegments(segments []structuredRawSegment) []structuredRawSegment {
	result := make([]structuredRawSegment, 0, len(segments))
	for _, segment := range segments {
		if segment.text == "" {
			continue
		}
		if len(result) > 0 && strings.TrimSpace(segment.text) == "" {
			result[len(result)-1].text += segment.text
			continue
		}
		if len(result) > 0 && stringSlicesEqual(result[len(result)-1].performerIDs, segment.performerIDs) {
			result[len(result)-1].text += segment.text
			continue
		}
		result = append(result, segment)
	}
	return result
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
