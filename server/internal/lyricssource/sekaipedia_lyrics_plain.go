package lyricssource

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

type sekaipediaNowikiLiteral struct {
	token string
	text  string
}

func maskSekaipediaMultilineNowiki(value string) (string, []sekaipediaNowikiLiteral, error) {
	if strings.ContainsAny(value, "\ue000\ue001") {
		return "", nil, ErrUnsupportedTable
	}
	var result strings.Builder
	literals := []sekaipediaNowikiLiteral{}
	for cursor := 0; cursor < len(value); {
		lowerRemaining := strings.ToLower(value[cursor:])
		openAt := strings.Index(lowerRemaining, "<nowiki>")
		if openAt < 0 {
			result.WriteString(value[cursor:])
			break
		}
		openAt += cursor
		result.WriteString(value[cursor:openAt])
		innerStart := openAt + len("<nowiki>")
		lowerInner := strings.ToLower(value[innerStart:])
		closeAt := strings.Index(lowerInner, "</nowiki>")
		if closeAt < 0 {
			return "", nil, ErrUnsupportedTable
		}
		innerEnd := innerStart + closeAt
		inner := value[innerStart:innerEnd]
		if inner == "" || !utf8.ValidString(inner) || strings.ContainsAny(inner, "\r\x00") ||
			strings.Contains(strings.ToLower(inner), "<nowiki>") {
			return "", nil, ErrUnsupportedTable
		}
		fragments := strings.Split(inner, "\n")
		for index, fragment := range fragments {
			if fragment != "" {
				token := fmt.Sprintf("\ue000%d\ue001", len(literals))
				literals = append(literals, sekaipediaNowikiLiteral{token: token, text: fragment})
				result.WriteString(token)
			}
			if index+1 < len(fragments) {
				result.WriteByte('\n')
			}
		}
		cursor = innerEnd + len("</nowiki>")
	}
	return result.String(), literals, nil
}

func restoreSekaipediaNowikiLiterals(value string, literals []sekaipediaNowikiLiteral) string {
	for _, literal := range literals {
		value = strings.ReplaceAll(value, literal.token, literal.text)
	}
	return value
}

func stripSekaipediaMultilineCombinedFormatting(value string) (string, error) {
	formatStack := []string{}
	var result strings.Builder
	for cursor := 0; cursor < len(value); {
		if strings.HasPrefix(value[cursor:], "'''''") {
			closesCombinedFormatting := len(formatStack) >= 2 &&
				(formatStack[len(formatStack)-2] == "italic" && formatStack[len(formatStack)-1] == "bold" ||
					formatStack[len(formatStack)-2] == "bold" && formatStack[len(formatStack)-1] == "italic")
			if closesCombinedFormatting {
				formatStack = formatStack[:len(formatStack)-2]
			} else {
				if len(formatStack) >= 6 {
					return "", ErrUnsupportedTable
				}
				formatStack = append(formatStack, "italic", "bold")
			}
			cursor += 5
			continue
		}
		result.WriteByte(value[cursor])
		cursor++
	}
	if len(formatStack) != 0 {
		return "", ErrUnsupportedTable
	}
	return result.String(), nil
}

func unwrapSekaipediaWholeNowikiColumn(value string) (string, bool, error) {
	lowerValue := strings.ToLower(value)
	if !strings.HasPrefix(lowerValue, "<nowiki>") {
		return value, false, nil
	}
	if !strings.HasSuffix(lowerValue, "</nowiki>") || strings.Count(lowerValue, "<nowiki>") != 1 ||
		strings.Count(lowerValue, "</nowiki>") != 1 {
		return "", false, ErrUnsupportedTable
	}
	inner := strings.TrimSpace(value[len("<nowiki>") : len(value)-len("</nowiki>")])
	if inner == "" || !utf8.ValidString(inner) || strings.ContainsAny(inner, "\r\x00") {
		return "", false, ErrUnsupportedTable
	}
	return inner, true, nil
}

func parseSekaipediaPlainLyricColumn(value string) ([]sekaipediaColumnLine, error) {
	value = strings.Trim(value, " \t\r\n")
	if value == "" || len(value) > maxExtractedTextBytes || !utf8.ValidString(value) ||
		strings.ContainsAny(value, "\r\x00") {
		return nil, ErrUnsupportedTable
	}
	value, literalNowiki, err := unwrapSekaipediaWholeNowikiColumn(value)
	literals := []sekaipediaNowikiLiteral{}
	if err == nil && !literalNowiki {
		value, literals, err = maskSekaipediaMultilineNowiki(value)
	}
	if err == nil && !literalNowiki {
		value, err = expandSekaipediaMultilineColorTemplates(value, 0)
	}
	if err == nil && !literalNowiki {
		value, err = unwrapSekaipediaPlainColumnFormatting(value)
	}
	if err == nil && !literalNowiki {
		value, err = stripSekaipediaMultilineCombinedFormatting(value)
	}
	if err != nil || value == "" || len(value) > maxExtractedTextBytes || strings.ContainsAny(value, "\r\x00") {
		return nil, ErrUnsupportedTable
	}
	result := make([]sekaipediaColumnLine, 0, strings.Count(value, "\n")+1)
	stanzaBreak := false
	for _, sourceLine := range strings.Split(value, "\n") {
		if strings.TrimSpace(sourceLine) == "" {
			if len(result) == 0 || stanzaBreak {
				return nil, ErrUnsupportedTable
			}
			stanzaBreak = true
			continue
		}
		text := strings.TrimSpace(sourceLine)
		if !literalNowiki {
			text, err = renderSekaipediaPlainLyricText(text, 0)
			if err == nil {
				text = restoreSekaipediaNowikiLiterals(text, literals)
			}
		}
		if err != nil || text == "" || strings.TrimSpace(text) != text || len(text) > maxExtractedLineBytes ||
			strings.ContainsAny(text, "\r\n\x00") {
			return nil, ErrUnsupportedTable
		}
		result = append(result, sekaipediaColumnLine{
			segments:          []sekaipediaColumnSegment{{text: text, performerIDs: []string{}}},
			stanzaBreakBefore: stanzaBreak,
		})
		stanzaBreak = false
	}
	if stanzaBreak || len(result) == 0 || len(result) > maxExtractedLines {
		return nil, ErrUnsupportedTable
	}
	return result, nil
}

func renderSekaipediaPlainLyricText(value string, depth int) (string, error) {
	if depth > 8 || value == "" || len(value) > maxExtractedLineBytes || !utf8.ValidString(value) ||
		strings.ContainsAny(value, "\r\n\x00") {
		return "", ErrUnsupportedTable
	}
	var result strings.Builder
	formatStack := []string{}
	toggleWikiFormatting := func(marker string) error {
		if len(formatStack) > 0 && formatStack[len(formatStack)-1] == marker {
			formatStack = formatStack[:len(formatStack)-1]
			return nil
		}
		for _, active := range formatStack {
			if active == marker {
				return ErrUnsupportedTable
			}
		}
		if len(formatStack) >= 8 {
			return ErrUnsupportedTable
		}
		formatStack = append(formatStack, marker)
		return nil
	}
	for cursor := 0; cursor < len(value); {
		switch {
		case strings.HasPrefix(value[cursor:], "{{"):
			_, end, inner, ok := balancedSekaipediaTemplateAt(value, cursor)
			if !ok {
				return "", ErrUnsupportedTable
			}
			fields, ok := splitTopLevelSekaipediaFields(inner, "|")
			if !ok || len(fields) == 0 {
				return "", ErrUnsupportedTable
			}
			rendered, err := renderSekaipediaPlainLyricTemplate(fields, depth+1)
			if err != nil {
				return "", err
			}
			result.WriteString(rendered)
			cursor = end
		case strings.HasPrefix(value[cursor:], "'''''"):
			closesCombinedFormatting := len(formatStack) >= 2 &&
				(formatStack[len(formatStack)-2] == "italic" && formatStack[len(formatStack)-1] == "bold" ||
					formatStack[len(formatStack)-2] == "bold" && formatStack[len(formatStack)-1] == "italic")
			if closesCombinedFormatting {
				formatStack = formatStack[:len(formatStack)-2]
			} else {
				if len(formatStack) >= 7 {
					return "", ErrUnsupportedTable
				}
				formatStack = append(formatStack, "italic", "bold")
			}
			cursor += 5
		case strings.HasPrefix(value[cursor:], "'''"):
			if err := toggleWikiFormatting("bold"); err != nil {
				return "", err
			}
			cursor += 3
		case strings.HasPrefix(value[cursor:], "''"):
			if err := toggleWikiFormatting("italic"); err != nil {
				return "", err
			}
			cursor += 2
		case value[cursor] == '<':
			remaining := value[cursor:]
			lowerRemaining := strings.ToLower(remaining)
			if strings.HasPrefix(lowerRemaining, "<nowiki>") {
				closeAt := strings.Index(lowerRemaining[len("<nowiki>"):], "</nowiki>")
				if closeAt < 0 {
					return "", ErrUnsupportedTable
				}
				innerStart := len("<nowiki>")
				innerEnd := innerStart + closeAt
				literal := remaining[innerStart:innerEnd]
				if literal == "" || strings.ContainsAny(literal, "\r\n\x00") || !utf8.ValidString(literal) {
					return "", ErrUnsupportedTable
				}
				result.WriteString(literal)
				cursor += innerEnd + len("</nowiki>")
				continue
			}
			if consumed, ok := consumeSekaipediaIgnoredCitationPrefix(remaining); ok {
				cursor += consumed
				continue
			}
			lowerRemaining = strings.ToLower(remaining)
			switch {
			case strings.HasPrefix(lowerRemaining, "<br>"):
				cursor += len("<br>")
				continue
			case strings.HasPrefix(lowerRemaining, "<br/>"):
				cursor += len("<br/>")
				continue
			case strings.HasPrefix(lowerRemaining, "<br />"):
				cursor += len("<br />")
				continue
			}
			if tag, closing, consumed, matched := parseSekaipediaLyricFormattingTag(remaining); matched {
				if closing {
					if len(formatStack) == 0 || formatStack[len(formatStack)-1] != tag {
						return "", ErrUnsupportedTable
					}
					formatStack = formatStack[:len(formatStack)-1]
				} else {
					if len(formatStack) >= 8 {
						return "", ErrUnsupportedTable
					}
					formatStack = append(formatStack, tag)
				}
				cursor += consumed
				continue
			}
			if sekaipediaHTMLTagStartsAt(value, cursor) {
				return "", ErrUnsupportedTable
			}
			result.WriteByte('<')
			cursor++
		case strings.HasPrefix(value[cursor:], "}}") || strings.HasPrefix(value[cursor:], "[[") ||
			strings.HasPrefix(value[cursor:], "]]"):
			return "", ErrUnsupportedTable
		default:
			result.WriteByte(value[cursor])
			cursor++
		}
		if result.Len() > maxExtractedLineBytes {
			return "", ErrLyricsTooLarge
		}
	}
	if len(formatStack) != 0 {
		return "", ErrUnsupportedTable
	}
	return strings.TrimSpace(result.String()), nil
}

func renderSekaipediaPlainLyricTemplate(fields []string, depth int) (string, error) {
	name := strings.TrimSpace(fields[0])
	switch {
	case strings.EqualFold(name, "ruby"):
		if len(fields) != 3 || strings.TrimSpace(fields[0]) == "" || strings.TrimSpace(fields[1]) == "" ||
			strings.TrimSpace(fields[2]) == "" || len(fields[2]) > maxExtractedLineBytes {
			return "", ErrUnsupportedTable
		}
		return renderSekaipediaPlainLyricText(strings.TrimSpace(fields[1]), depth)
	case name == "Color":
		if len(fields) != 3 || !validSekaipediaIgnoredFormattingSelector(fields[1]) {
			return "", ErrUnsupportedTable
		}
		return renderSekaipediaPlainLyricText(strings.TrimSpace(fields[2]), depth)
	default:
		aliases, _, err := buildSekaipediaSingerAliases(sekaipediaSingers)
		if err != nil || aliases[normalizeSekaipediaSingerAlias(name)] == "" || len(fields) != 2 {
			return "", ErrUnsupportedTable
		}
		return renderSekaipediaPlainLyricText(strings.TrimSpace(fields[1]), depth)
	}
}
