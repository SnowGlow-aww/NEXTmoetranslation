package lyricssource

import (
	"html"

	"regexp"

	"strings"
)

var headingPattern = regexp.MustCompile(`(?im)^==+\s*Lyrics\s*==+\s*$`)
var nextHeadingPattern = regexp.MustCompile(`(?m)^==+[^=].*==+\s*$`)
var topLevelHeadingPattern = regexp.MustCompile(`(?m)^==[^=].*?==\s*$`)
var topLevelLyricsHeadingPattern = regexp.MustCompile(`(?im)^==[ \t]*Lyrics[ \t]*==[ \t]*$`)
var inactiveRestrictionMarkupPattern = regexp.MustCompile(`(?is)<!--.*?-->|<nowiki\b[^>]*>.*?</nowiki\s*>`)
var wikiBreakPattern = regexp.MustCompile(`(?i)<br\s*/?>`)
var wikiRoleAssignmentPattern = regexp.MustCompile(`(?i)^\s*[|!]??\s*(lyrics?|lyricist|words|written(?:\s+by)?|music|composer|composition|composed(?:\s+by)?|arrangement|arranger|arranged(?:\s+by)?|作詞|作曲|編曲)\s*(?:=|:|：)\s*(.+?)\s*$`)
var wikiProducerAssignmentPattern = regexp.MustCompile(`(?i)^\s*\|\s*producers?\s*=\s*(.*?)\s*$`)
var wikiSongBoxStartPattern = regexp.MustCompile(`(?i)\{\{\s*song\s*box\s*2\b`)
var wikiSongBoxTitlePattern = regexp.MustCompile(`(?im)^\s*\|\s*title\s*=\s*(.*?)\s*$`)
var wikiRoleByPattern = regexp.MustCompile(`(?i)^\s*[|!]??\s*(lyrics?|lyricist|words|music|composer|composition|arrangement|arranger)\s+by\s+(.+?)\s*$`)
var wikiRoleLabelPattern = regexp.MustCompile(`(?i)^\s*!\s*(lyrics?|lyricist|words|written(?:\s+by)?|music|composer|composition|composed(?:\s+by)?|arrangement|arranger|arranged(?:\s+by)?|作詞|作曲|編曲)\s*$`)
var markupPattern = regexp.MustCompile(`(?s)<!--.*?-->|<ref[^>]*>.*?</ref>|<[^>]+>`)
var linkPattern = regexp.MustCompile(`\[\[(?:[^]|]+\|)?([^]]+)\]\]`)
var simpleIdentityTemplatePattern = regexp.MustCompile(`\{\{[^{}]*\}\}`)
var spacedContributorXPattern = regexp.MustCompile(`\s+[xX]\s+`)
var sharedPlainLinePattern = regexp.MustCompile(`(?i)^\{\{\s*shared\s*\}\}\s*(?:\|\s*)?(.*)$`)
var sharedTableCellPattern = regexp.MustCompile(`(?i)^\{\{\s*shared\s*\}\}\s*(?:\|\s*)?`)
var lyricsTextRestrictionPattern = regexp.MustCompile(`(?i)(?:` +
	`(?:do\s+not|must\s+not|may\s+not|cannot|can't|not\s+allowed\s+to)\s+(?:repost|reprint|copy|reproduce|redistribute|use|transcribe)\s+(?:these\s+|the\s+)?(?:lyrics?|lyric\s+text|transcription|text)\b|` +
	`(?:lyrics?|lyric\s+text|transcription)\s+(?:(?:may|must|can)\s+not|cannot|can't)\s+be\s+(?:reposted|reprinted|copied|reproduced|redistributed|used|transcribed)\b|` +
	`(?:reposting|reprinting|copying|reproduction|redistribution|use|transcription|transcribing)\s+of\s+(?:these\s+|the\s+)?(?:lyrics?|lyric\s+text|transcription)\s+(?:is|are)\s+(?:prohibited|forbidden|not\s+allowed)\b|` +
	`歌詞(?:テキスト|本文)?(?:` +
	`(?:の|を|は)?(?:無断)?(?:転載|複製|再配布|使用|書き起こし|文字起こし)(?:を|は)?(?:禁止|禁じ|不可|しないで|しないこと|ご遠慮)|` +
	`(?:を)?(?:書き起こさ|文字起こしし)(?:ないで|ないこと)` +
	`)` +
	`)`)
var mediaWikiSHA1Pattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// HasCanonicalSHA1 reports whether value is the lowercase 40-hex revision
// identity required for every provenance-bearing preview, save, and restore.
func HasCanonicalSHA1(value string) bool {
	return mediaWikiSHA1Pattern.MatchString(value)
}

func extractLyrics(content string) ([]ExtractedLine, error) {
	if hasLyricsTextRestriction(content, nil) {
		return nil, ErrRestrictedReprint
	}
	location := headingPattern.FindStringIndex(content)
	if location == nil {
		return nil, ErrMissingLyrics
	}
	section := content[location[1]:]
	if next := nextHeadingPattern.FindStringIndex(section); next != nil {
		section = section[:next[0]]
	}
	tableCount := strings.Count(section, "{|")
	if tableCount > 1 {
		return nil, ErrUnsupportedTable
	}
	if tableCount == 1 {
		return extractLyricsTable(section)
	}
	return extractPlainLyrics(section)
}

// hasLyricsTextRestriction distinguishes an explicit restriction on lyric text
// from the Wiki's ordinary NoReprint/media-reupload metadata. A song, audio, or
// video reprint restriction alone does not prohibit reading and transcribing the
// page's lyric text, so only statements that name lyrics/text/transcription are
// enforced here.
func hasLyricsTextRestriction(content string, categories []string) bool {
	content = inactiveRestrictionMarkupPattern.ReplaceAllString(content, "")
	for _, category := range categories {
		if lyricsTextRestrictionStatement(strings.TrimPrefix(category, "Category:")) {
			return true
		}
	}
	content = wikiBreakPattern.ReplaceAllString(strings.ReplaceAll(content, "\r", ""), "\n")
	rawLines := strings.Split(content, "\n")
	lines := make([]string, len(rawLines))
	for index, line := range rawLines {
		lines[index] = stripRestrictionMarkup(line)
	}
	const maxRestrictionLines = 3
	for start := range lines {
		parts := make([]string, 0, maxRestrictionLines)
		for end := start; end < len(lines) && end < start+maxRestrictionLines; end++ {
			parts = append(parts, lines[end])
			if lyricsTextRestrictionStatement(strings.Join(parts, " ")) ||
				lyricsTextRestrictionStatement(strings.Join(parts, "")) {
				return true
			}
		}
	}
	return false
}

func stripRestrictionMarkup(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimLeft(value, "*#:;!| ")
	value = strings.TrimSpace(value)
	value = strings.Trim(value, "'\"")
	value = linkPattern.ReplaceAllString(value, "$1")
	value = markupPattern.ReplaceAllString(value, " ")
	value = strings.Trim(value, "'\"")
	return strings.Join(strings.Fields(value), " ")
}

func lyricsTextRestrictionStatement(value string) bool {
	return lyricsTextRestrictionPattern.MatchString(strings.TrimSpace(value))
}

func extractPlainLyrics(section string) ([]ExtractedLine, error) {
	raw := strings.Split(strings.ReplaceAll(section, "\r", ""), "\n")
	lines := make([]string, 0, len(raw))
	explicitShared := make([]bool, 0, len(raw))
	for _, line := range raw {
		shared := sharedPlainLinePattern.FindStringSubmatch(strings.TrimSpace(line))
		if shared != nil {
			line = shared[1]
		}
		sanitized, err := sanitizeLyricText(line)
		if err != nil {
			return nil, err
		}
		lines = append(lines, sanitized)
		explicitShared = append(explicitShared, shared != nil)
	}
	return plainLyricLines(lines, explicitShared)
}

func extractLyricsTable(section string) ([]ExtractedLine, error) {
	lower := strings.ToLower(section)
	if strings.Contains(lower, "rowspan") || strings.Contains(lower, "colspan") || strings.Count(section, "{|") != 1 {
		return nil, ErrUnsupportedTable
	}

	var cells []string
	var headers []string
	var rowCells []string
	inTable := false
	dataStarted := false
	sourceColumn := -1

	flushHeaders := func() error {
		if sourceColumn >= 0 {
			return nil
		}
		if len(headers) == 0 || !isSupportedSourceHeader(headers[0]) {
			return ErrUnsupportedTable
		}
		sourceColumn = 0
		for _, header := range headers[1:] {
			if isSupportedSourceHeader(header) {
				return ErrUnsupportedTable
			}
		}
		return nil
	}
	flushRow := func() error {
		if len(rowCells) == 0 {
			return nil
		}
		if err := flushHeaders(); err != nil {
			return err
		}
		if len(rowCells) > len(headers) || sourceColumn >= len(rowCells) {
			return ErrUnsupportedTable
		}
		sanitized := make([]string, len(rowCells))
		for index, raw := range rowCells {
			cell := strings.TrimSpace(raw)
			if index == sourceColumn {
				if looksLikeTableCellAttributes(cell) {
					return ErrUnsupportedTable
				}
				cell = sharedTableCellPattern.ReplaceAllString(cell, "")
			}
			var err error
			cell, err = sanitizeLyricText(cell)
			if err != nil {
				return err
			}
			sanitized[index] = strings.TrimSpace(html.UnescapeString(cell))
		}
		source := sanitized[sourceColumn]
		if source == "" {
			for index, cell := range sanitized {
				if index != sourceColumn && cell != "" {
					return ErrUnsupportedTable
				}
			}
		}
		cells = append(cells, source)
		rowCells = nil
		return nil
	}

	for _, rawLine := range strings.Split(strings.ReplaceAll(section, "\r", ""), "\n") {
		line := strings.TrimSpace(rawLine)
		if strings.HasPrefix(line, "{|") {
			if inTable {
				return nil, ErrUnsupportedTable
			}
			inTable = true
			continue
		}
		if !inTable {
			continue
		}
		if line == "|}" {
			if err := flushRow(); err != nil {
				return nil, err
			}
			if err := flushHeaders(); err != nil {
				return nil, err
			}
			inTable = false
			continue
		}
		if strings.HasPrefix(line, "|-") {
			if line != "|-" {
				return nil, ErrUnsupportedTable
			}
			if err := flushRow(); err != nil {
				return nil, err
			}
			continue
		}
		if strings.HasPrefix(line, "!") {
			if dataStarted || len(rowCells) > 0 {
				return nil, ErrUnsupportedTable
			}
			for _, rawHeader := range strings.Split(strings.TrimPrefix(line, "!"), "!!") {
				header := strings.TrimSpace(rawHeader)
				if separator := strings.LastIndex(header, "|"); separator >= 0 {
					header = strings.TrimSpace(header[separator+1:])
				}
				var err error
				header, err = sanitizeLyricText(header)
				if err != nil {
					return nil, err
				}
				headers = append(headers, strings.TrimSpace(html.UnescapeString(header)))
			}
			continue
		}
		if strings.HasPrefix(line, "|+") {
			return nil, ErrUnsupportedTable
		}
		if !strings.HasPrefix(line, "|") {
			if len(rowCells) > 0 && line != "" {
				return nil, ErrUnsupportedTable
			}
			continue
		}
		if err := flushHeaders(); err != nil {
			return nil, err
		}
		dataStarted = true
		for _, cell := range strings.Split(strings.TrimPrefix(line, "|"), "||") {
			rowCells = append(rowCells, strings.TrimSpace(cell))
		}
	}
	if inTable || sourceColumn < 0 {
		return nil, ErrUnsupportedTable
	}
	return lyricLines(cells, false)
}

func isSupportedSourceHeader(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "'", "")
	value = strings.Join(strings.Fields(value), " ")
	if value == "japanese" || value == "japanese lyrics" || value == "lyrics" || value == "original" || value == "original lyrics" ||
		value == "source" || value == "source lyrics" || value == "日本語" || value == "日本語歌詞" {
		return true
	}
	return strings.HasPrefix(value, "japanese (") && strings.HasSuffix(value, ")") && strings.Contains(value, "日本語歌詞")
}

func sanitizeLyricText(value string) (string, error) {
	value = markupPattern.ReplaceAllString(value, "")
	value = linkPattern.ReplaceAllString(value, "$1")
	if strings.Contains(value, "{{") || strings.Contains(value, "}}") || strings.Contains(value, "{|") || strings.Contains(value, "|}") ||
		strings.Contains(value, "[[") || strings.Contains(value, "]]") {
		return "", ErrUnsupportedTable
	}
	return value, nil
}

func looksLikeTableCellAttributes(value string) bool {
	separator := strings.Index(value, "|")
	if separator < 0 {
		return false
	}
	attributes := strings.ToLower(strings.TrimSpace(value[:separator]))
	return strings.Contains(attributes, "=") || strings.Contains(attributes, "style") ||
		strings.Contains(attributes, "class") || strings.Contains(attributes, "scope")
}

func plainLyricLines(raw []string, explicitShared []bool) ([]ExtractedLine, error) {
	if len(raw) != len(explicitShared) {
		return nil, ErrMalformedResponse
	}
	result := []ExtractedLine{}
	stanza := false
	totalBytes := 0
	for index, line := range raw {
		line = strings.TrimSpace(html.UnescapeString(line))
		line = strings.Trim(line, "'")
		if line == "" {
			if len(result) > 0 {
				stanza = true
			}
			continue
		}
		if strings.HasPrefix(line, "Category:") || strings.HasPrefix(line, "[[Category:") {
			continue
		}
		if !explicitShared[index] && !containsJapanese(line) {
			continue
		}
		if err := appendExtractedLine(&result, line, stanza, &totalBytes); err != nil {
			return nil, err
		}
		stanza = false
	}
	if len(result) == 0 {
		return nil, ErrMissingLyrics
	}
	return result, nil
}

func lyricLines(raw []string, requireJapanese bool) ([]ExtractedLine, error) {
	result := []ExtractedLine{}
	stanza := false
	totalBytes := 0
	for _, line := range raw {
		line = strings.TrimSpace(html.UnescapeString(line))
		line = strings.Trim(line, "'")
		if line == "" {
			if len(result) > 0 {
				stanza = true
			}
			continue
		}
		if strings.HasPrefix(line, "Category:") || strings.HasPrefix(line, "[[Category:") {
			continue
		}
		if requireJapanese && !containsJapanese(line) {
			continue
		}
		if err := appendExtractedLine(&result, line, stanza, &totalBytes); err != nil {
			return nil, err
		}
		stanza = false
	}
	if len(result) == 0 {
		return nil, ErrMissingLyrics
	}
	return result, nil
}

func appendExtractedLine(result *[]ExtractedLine, line string, stanza bool, totalBytes *int) error {
	if len(line) > maxExtractedLineBytes || len(*result) >= maxExtractedLines || *totalBytes > maxExtractedTextBytes-len(line) {
		return ErrLyricsTooLarge
	}
	*totalBytes += len(line)
	*result = append(*result, ExtractedLine{Japanese: line, StanzaBreakBefore: stanza})
	return nil
}

func containsJapanese(value string) bool {
	for _, r := range value {
		if (r >= 0x3040 && r <= 0x30ff) || (r >= 0x4e00 && r <= 0x9fff) {
			return true
		}
	}
	return false
}
