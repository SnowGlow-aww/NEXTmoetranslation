package lyricssource

import (
	"errors"

	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

func parseSekaipediaTemplateSequence(value string) ([]sekaipediaTemplate, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\r", ""))
	if value == "" {
		return nil, ErrUnsupportedTable
	}
	result := []sekaipediaTemplate{}
	ignoredStrayBraceAfterLast := false
	for cursor := 0; cursor < len(value); {
		for cursor < len(value) {
			current, size := utf8.DecodeRuneInString(value[cursor:])
			if current == utf8.RuneError && size == 1 || !unicode.IsSpace(current) {
				break
			}
			cursor += size
		}
		if cursor >= len(value) {
			break
		}
		if !strings.HasPrefix(value[cursor:], "{{") {
			if value[cursor] == '}' && !ignoredStrayBraceAfterLast && len(result) > 0 &&
				(strings.EqualFold(result[len(result)-1].name, "Lyrics line") ||
					strings.EqualFold(result[len(result)-1].name, "Lyrics tail")) {
				ignoredStrayBraceAfterLast = true
				cursor++
				continue
			}
			consumed, ok := consumeSekaipediaIgnoredCitationPrefix(value[cursor:])
			if !ok {
				return nil, ErrUnsupportedTable
			}
			cursor += consumed
			continue
		}
		_, end, inner, ok := balancedSekaipediaTemplateAt(value, cursor)
		if !ok {
			return nil, ErrUnsupportedTable
		}
		fields, ok := splitTopLevelSekaipediaFields(inner, "|")
		if !ok || len(fields) == 0 {
			return nil, ErrUnsupportedTable
		}
		name := strings.TrimSpace(fields[0])
		if name == "" {
			return nil, ErrUnsupportedTable
		}
		result = append(result, sekaipediaTemplate{name: name, fields: fields})
		ignoredStrayBraceAfterLast = false
		cursor = end
	}
	if len(result) == 0 || len(result) > maxExtractedLines+2 {
		return nil, ErrUnsupportedTable
	}
	return result, nil
}

func sekaipediaNamedParameters(fields []string, allowed map[string]bool) (map[string]string, error) {
	if len(fields) < 2 {
		return nil, ErrUnsupportedTable
	}
	params := map[string]string{}
	for _, field := range fields[1:] {
		separator := strings.IndexByte(field, '=')
		if separator < 0 {
			return nil, ErrUnsupportedTable
		}
		name := strings.ToLower(strings.TrimSpace(field[:separator]))
		value := strings.Trim(field[separator+1:], " \t\r\n")
		if name == "" || !allowed[name] {
			return nil, ErrUnsupportedTable
		}
		if _, duplicate := params[name]; duplicate {
			return nil, ErrAmbiguous
		}
		params[name] = value
	}
	return params, nil
}

func sekaipediaTopLevelSection(content, wanted string) (string, error) {
	content = strings.ReplaceAll(content, "\r", "")
	matches, err := sekaipediaActiveTopLevelHeadings(content)
	if err != nil {
		return "", err
	}
	matched := -1
	for index, location := range matches {
		if strings.TrimSpace(content[location[2]:location[3]]) != wanted {
			continue
		}
		if matched >= 0 {
			return "", ErrAmbiguous
		}
		matched = index
	}
	if matched < 0 {
		return "", ErrMissingLyrics
	}
	start := matches[matched][1]
	end := len(content)
	if matched+1 < len(matches) {
		end = matches[matched+1][0]
	}
	return content[start:end], nil
}

func sekaipediaActiveTopLevelHeadings(content string) ([][]int, error) {
	matches := moegirlTopHeadingPattern.FindAllStringSubmatchIndex(content, -1)
	if len(matches) == 0 {
		return matches, nil
	}
	type commentRange struct{ start, end int }
	comments := []commentRange{}
	for cursor := 0; cursor < len(content); {
		relativeStart := strings.Index(content[cursor:], "<!--")
		if relativeStart < 0 {
			break
		}
		start := cursor + relativeStart
		relativeEnd := strings.Index(content[start+len("<!--"):], "-->")
		if relativeEnd < 0 {
			return nil, ErrMalformedResponse
		}
		end := start + len("<!--") + relativeEnd + len("-->")
		comments = append(comments, commentRange{start: start, end: end})
		cursor = end
	}
	active := make([][]int, 0, len(matches))
	commentIndex := 0
	for _, match := range matches {
		for commentIndex < len(comments) && comments[commentIndex].end <= match[0] {
			commentIndex++
		}
		if commentIndex < len(comments) && comments[commentIndex].start <= match[0] && match[0] < comments[commentIndex].end {
			continue
		}
		active = append(active, match)
	}
	return active, nil
}

func parseSekaipediaInfoboxSongParams(content string) (map[string]string, error) {
	start, end, inner, ok := findBalancedSekaipediaNamedTemplate(content, "Infobox song")
	if !ok || start > 4096 {
		return nil, ErrMalformedResponse
	}
	if _, _, _, duplicate := findBalancedSekaipediaNamedTemplate(content[end:], "Infobox song"); duplicate {
		return nil, ErrAmbiguous
	}
	fields, ok := splitTopLevelSekaipediaFields(inner, "|")
	if !ok || len(fields) < 2 || strings.TrimSpace(fields[0]) != "Infobox song" {
		return nil, ErrMalformedResponse
	}
	params := map[string]string{}
	for _, field := range fields[1:] {
		separator := strings.IndexByte(field, '=')
		if separator < 0 {
			return nil, ErrMalformedResponse
		}
		name := strings.ToLower(strings.Join(strings.Fields(field[:separator]), " "))
		if name == "" {
			return nil, ErrMalformedResponse
		}
		if _, duplicate := params[name]; duplicate {
			return nil, ErrAmbiguous
		}
		params[name] = strings.TrimSpace(field[separator+1:])
	}
	return params, nil
}

func sekaipediaCatalogIdentityMatches(content, pageTitle string, identity MusicIdentity) bool {
	params, parseErr := parseSekaipediaInfoboxSongParams(content)
	if parseErr != nil {
		return false
	}
	musicID, err := strconv.Atoi(params["song id"])
	if err != nil || musicID != identity.MusicID {
		return false
	}
	titleMatched := titleFormMatches(pageTitle, identity.JapaneseTitle)
	for _, name := range []string{"song name", "japanese", "romaji", "english", "global english"} {
		if value := identityDisplayText(params[name]); value != "" && titleFormMatches(value, identity.JapaneseTitle) {
			titleMatched = true
		}
	}
	if !titleMatched {
		return false
	}

	expectedRoles := []struct {
		wanted string
		field  string
	}{
		{identity.Lyricist, "lyricists"},
		{identity.Composer, "composers"},
		{identity.Arranger, "arrangers"},
	}
	matchedRoles := 0
	for _, role := range expectedRoles {
		wanted := strings.TrimSpace(role.wanted)
		// Catalog rows use placeholder credits ("-", "N/A", "—") when a role
		// is genuinely absent; those must not fail the role comparison.
		if wanted == "" || normalizeTitle(wanted) == "" {
			continue
		}
		actual := identityDisplayText(params[role.field])
		wantedSet, wantedOK := contributorSet(role.wanted)
		actualSet, actualOK := contributorSet(actual)
		if !wantedOK || !actualOK || !contributorSetsEqual(wantedSet, actualSet) {
			return false
		}
		matchedRoles++
	}
	if matchedRoles > 0 {
		return true
	}
	wantedProducers, wantedOK := contributorSet(identity.ProducerMetadata)
	actualProducers, actualOK := contributorSet(identityDisplayText(params["producers"]))
	if !wantedOK || !actualOK {
		return false
	}
	for producer := range wantedProducers {
		if _, exists := actualProducers[producer]; exists {
			return true
		}
	}
	return false
}

var _ = errors.Is
