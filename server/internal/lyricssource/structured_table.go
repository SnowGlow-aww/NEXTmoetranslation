package lyricssource

import (
	"strings"
)

func extractStructuredLyricsTable(body string, performers []Performer) ([]StructuredLine, error) {
	allowedPerformers := make(map[string]struct{}, len(performers))
	for _, performer := range performers {
		allowedPerformers[performer.PerformerID] = struct{}{}
	}
	start := strings.Index(body, "{|")
	end := strings.LastIndex(body, "|}")
	if start < 0 || end <= start || strings.Count(body, "{|") != 1 || strings.Count(body, "|}") != 1 {
		return nil, ErrUnsupportedTable
	}
	table := body[start : end+2]
	if strings.Contains(strings.ToLower(table), "rowspan") {
		return nil, ErrUnsupportedTable
	}
	var headers []string
	var rowCells []string
	type structuredSourceCell struct {
		raw             string
		shared          bool
		requireJapanese bool
	}
	var rawSourceCells []structuredSourceCell
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
		defer func() { rowCells = nil }()
		if sourceColumn < 0 && len(headers) == 0 {
			if len(rowCells) == 1 {
				firstRaw := strings.TrimSpace(rowCells[0])
				sharedRaw, shared, sharedErr := parseStructuredSharedCell(firstRaw)
				if sharedErr != nil {
					return sharedErr
				}
				if shared || structuredBreakPattern.MatchString(firstRaw) {
					if shared {
						firstRaw = sharedRaw
					}
					if _, _, err := sanitizeStructuredCell(firstRaw, allowedPerformers, false); err != nil {
						return err
					}
					rawSourceCells = append(rawSourceCells, structuredSourceCell{raw: firstRaw, shared: shared})
					return nil
				}
			}
			inferred, recognized, inferErr := structuredHeadersFromDataCells(rowCells)
			if inferErr != nil {
				return inferErr
			}
			if recognized {
				headers = inferred
				return flushHeaders()
			}
		}
		if err := flushHeaders(); err != nil {
			return err
		}
		rowCells = normalizeHarmlessTrailingEmptyStructuredCell(headers, rowCells)
		var breakAfter bool
		rowCells, breakAfter = normalizeStructuredOverflowBreak(headers, rowCells)
		if len(rowCells) > len(headers) || sourceColumn >= len(rowCells) {
			return ErrUnsupportedTable
		}
		sourceRaw := strings.TrimSpace(rowCells[sourceColumn])
		sharedRaw, shared, sharedErr := parseStructuredSharedCell(sourceRaw)
		if sharedErr != nil {
			return sharedErr
		}
		if shared {
			sourceRaw = sharedRaw
		}
		spanningRaw, spanning, spanningErr := parseStructuredColspanSourceCell(sourceRaw)
		if spanningErr != nil {
			return spanningErr
		}
		if spanning {
			if shared {
				return ErrUnsupportedTable
			}
			sourceRaw = spanningRaw
			shared = true
		}
		stanzaBreak := structuredBreakPattern.MatchString(sourceRaw)
		for index, raw := range rowCells {
			if index == sourceColumn {
				if looksLikeStructuredTableCellAttributes(strings.TrimSpace(raw)) && !shared {
					return ErrUnsupportedTable
				}
				continue
			}
			if strings.Contains(strings.ToLower(raw), "colspan") {
				return ErrUnsupportedTable
			}
			if strings.TrimSpace(raw) == "" {
				continue
			}
			if _, _, err := sanitizeStructuredCell(raw, allowedPerformers, false); err != nil {
				return err
			}
		}
		requireJapanese, requirementErr := structuredSourceCellRequiresJapanese(
			sourceRaw, rowCells, sourceColumn, shared, stanzaBreak,
			hasExplicitStructuredJapaneseHeader(headers), hasExplicitStructuredRomajiHeader(headers),
		)
		if requirementErr != nil {
			return requirementErr
		}
		rawSourceCells = append(rawSourceCells, structuredSourceCell{
			raw: sourceRaw, shared: shared, requireJapanese: requireJapanese,
		})
		if breakAfter {
			rawSourceCells = append(rawSourceCells, structuredSourceCell{raw: "<br>"})
		}
		return nil
	}
	for _, rawLine := range strings.Split(table, "\n") {
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
			if !isSafeStructuredRowSeparator(line) {
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
			rawHeaders, ok := splitTopLevelStructuredFields(strings.TrimPrefix(line, "!"), "!!")
			if !ok {
				return nil, ErrUnsupportedTable
			}
			for _, rawHeader := range rawHeaders {
				header, headerErr := sanitizeStructuredHeader(rawHeader)
				if headerErr != nil {
					return nil, headerErr
				}
				headers = append(headers, header)
			}
			continue
		}
		if strings.HasPrefix(line, "|+") {
			return nil, ErrUnsupportedTable
		}
		if !strings.HasPrefix(line, "|") {
			if line == "" {
				continue
			}
			if len(rowCells) == 0 || !isSafeStructuredCellContinuation(line) {
				return nil, ErrUnsupportedTable
			}
			rowCells[len(rowCells)-1] += line
			continue
		}
		if len(headers) > 0 || sourceColumn >= 0 {
			if err := flushHeaders(); err != nil {
				return nil, err
			}
			dataStarted = true
		}
		cells, ok := splitStructuredRowCells(strings.TrimPrefix(line, "|"), headers, len(rowCells) > 0)
		if !ok {
			return nil, ErrUnsupportedTable
		}
		for _, cell := range cells {
			rowCells = append(rowCells, strings.TrimSpace(cell))
		}
	}
	if inTable || sourceColumn < 0 {
		return nil, ErrUnsupportedTable
	}
	result := make([]StructuredLine, 0, len(rawSourceCells))
	stanza := false
	totalBytes := 0
	for _, source := range rawSourceCells {
		raw := source.raw
		if structuredBreakPattern.MatchString(raw) || strings.TrimSpace(raw) == "" {
			if len(result) > 0 {
				stanza = true
			}
			continue
		}
		segments, trailing, err := sanitizeStructuredCell(raw, allowedPerformers, source.requireJapanese)
		if err != nil {
			return nil, err
		}
		var text strings.Builder
		for _, segment := range segments {
			text.WriteString(segment.text)
		}
		japanese := text.String()
		if strings.TrimSpace(japanese) == "" {
			return nil, ErrMissingLyrics
		}
		if len(japanese) > maxExtractedLineBytes || len(result) >= maxExtractedLines || totalBytes > maxExtractedTextBytes-len(japanese) {
			return nil, ErrLyricsTooLarge
		}
		totalBytes += len(japanese)
		structuredSegments := make([]LyricsSegment, 0, len(segments))
		segmentTexts := make([]string, 0, len(segments))
		for _, segment := range segments {
			if segment.text != "" {
				segmentTexts = append(segmentTexts, segment.text)
			}
		}
		segmentRuby, err := generateRubySpansForTexts(segmentTexts)
		if err != nil {
			return nil, err
		}
		rubyIndex := 0
		for _, segment := range segments {
			if segment.text == "" {
				continue
			}
			structuredSegments = append(structuredSegments, LyricsSegment{
				Text: segment.text, PerformerIDs: append([]string{}, segment.performerIDs...), Ruby: segmentRuby[rubyIndex],
			})
			rubyIndex++
		}
		result = append(result, StructuredLine{Japanese: japanese, StanzaBreakBefore: stanza,
			Segments: structuredSegments, TrailingPerformerIDs: trailing})
		stanza = false
	}
	if len(result) == 0 {
		return nil, ErrMissingLyrics
	}
	return result, nil
}

// extractPlaintextLyricsTable is a deliberately lossy fallback for one already
// selected source-version table. It keeps only the first/source column, stanza
// breaks, and exact {{Shared}} lines; performer assignments are discarded and
// ruby is regenerated from the resulting source text. It never selects a
// version, translation tab, Romaji column, or additional table.
func extractPlaintextLyricsTable(body string) ([]ExtractedLine, error) {
	start := strings.Index(body, "{|")
	end := strings.LastIndex(body, "|}")
	if start < 0 || end <= start || strings.Count(body, "{|") != 1 || strings.Count(body, "|}") != 1 {
		return nil, ErrUnsupportedTable
	}
	table := body[start : end+2]
	lower := strings.ToLower(table)
	if strings.Contains(lower, "rowspan") || strings.Contains(lower, "colspan") {
		return nil, ErrUnsupportedTable
	}

	var headers []string
	var sourceRows []string
	var rowCells []string
	inTable := false
	dataStarted := false
	headersComplete := false
	flushHeaders := func() error {
		if headersComplete {
			return nil
		}
		if len(headers) == 0 || !isSupportedSourceHeader(headers[0]) {
			return ErrUnsupportedTable
		}
		for _, header := range headers[1:] {
			if isSupportedSourceHeader(header) {
				return ErrUnsupportedTable
			}
		}
		headersComplete = true
		return nil
	}
	flushRow := func() error {
		if len(rowCells) == 0 {
			return nil
		}
		defer func() { rowCells = nil }()
		if !headersComplete && len(headers) == 0 {
			firstRaw := strings.TrimSpace(rowCells[0])
			_, preHeaderShared, sharedErr := parseStructuredSharedCell(firstRaw)
			if sharedErr != nil {
				return sharedErr
			}
			if !preHeaderShared && !structuredBreakPattern.MatchString(firstRaw) {
				inferred, recognized, inferErr := structuredHeadersFromDataCells(rowCells)
				if inferErr != nil {
					return inferErr
				}
				if recognized {
					headers = inferred
					return flushHeaders()
				}
			}
		}
		breakAfter := false
		if headersComplete {
			rowCells = normalizeHarmlessTrailingEmptyStructuredCell(headers, rowCells)
			rowCells, breakAfter = normalizeStructuredOverflowBreak(headers, rowCells)
		}
		sourceRaw := strings.TrimSpace(rowCells[0])
		sharedRaw, shared, sharedErr := parseStructuredSharedCell(sourceRaw)
		if sharedErr != nil {
			return sharedErr
		}
		if shared {
			sourceRaw = sharedRaw
		}
		spanningRaw, spanning, spanningErr := parseStructuredColspanSourceCell(sourceRaw)
		if spanningErr != nil {
			return spanningErr
		}
		if spanning {
			if shared {
				return ErrUnsupportedTable
			}
			sourceRaw = spanningRaw
			shared = true
		}
		stanzaBreak := structuredBreakPattern.MatchString(sourceRaw)
		if !headersComplete {
			if !shared && !stanzaBreak {
				return ErrUnsupportedTable
			}
		} else if len(rowCells) > len(headers) {
			return ErrUnsupportedTable
		}
		for _, raw := range rowCells[1:] {
			if strings.Contains(strings.ToLower(raw), "colspan") {
				return ErrUnsupportedTable
			}
		}
		if looksLikeStructuredTableCellAttributes(sourceRaw) && !shared {
			return ErrUnsupportedTable
		}
		requireJapanese, requirementErr := structuredSourceCellRequiresJapanese(
			sourceRaw, rowCells, 0, shared, stanzaBreak,
			hasExplicitStructuredJapaneseHeader(headers), hasExplicitStructuredRomajiHeader(headers),
		)
		if requirementErr != nil {
			return requirementErr
		}
		line, err := sanitizePlaintextStructuredCell(sourceRaw, requireJapanese)
		if err != nil {
			return err
		}
		if stanzaBreak || line == "" {
			sourceRows = append(sourceRows, "")
		} else {
			sourceRows = append(sourceRows, line)
		}
		if breakAfter {
			sourceRows = append(sourceRows, "")
		}
		return nil
	}

	for _, rawLine := range strings.Split(strings.ReplaceAll(table, "\r", ""), "\n") {
		line := strings.TrimSpace(rawLine)
		switch {
		case strings.HasPrefix(line, "{|"):
			if inTable {
				return nil, ErrUnsupportedTable
			}
			inTable = true
		case !inTable:
			continue
		case line == "|}":
			if err := flushRow(); err != nil {
				return nil, err
			}
			if err := flushHeaders(); err != nil {
				return nil, err
			}
			inTable = false
		case strings.HasPrefix(line, "|-"):
			if !isSafeStructuredRowSeparator(line) {
				return nil, ErrUnsupportedTable
			}
			if err := flushRow(); err != nil {
				return nil, err
			}
		case strings.HasPrefix(line, "!"):
			if dataStarted || len(rowCells) > 0 {
				return nil, ErrUnsupportedTable
			}
			rawHeaders, ok := splitTopLevelStructuredFields(strings.TrimPrefix(line, "!"), "!!")
			if !ok {
				return nil, ErrUnsupportedTable
			}
			for _, rawHeader := range rawHeaders {
				header, headerErr := sanitizeStructuredHeader(rawHeader)
				if headerErr != nil {
					return nil, headerErr
				}
				headers = append(headers, header)
			}
			if err := flushHeaders(); err != nil {
				return nil, err
			}
		case strings.HasPrefix(line, "|+"):
			return nil, ErrUnsupportedTable
		case strings.HasPrefix(line, "|"):
			if headersComplete {
				dataStarted = true
			}
			cells, ok := splitStructuredRowCells(strings.TrimPrefix(line, "|"), headers, len(rowCells) > 0)
			if !ok {
				return nil, ErrUnsupportedTable
			}
			for _, cell := range cells {
				rowCells = append(rowCells, strings.TrimSpace(cell))
			}
		default:
			if line == "" {
				continue
			}
			if len(rowCells) == 0 || !isSafeStructuredCellContinuation(line) {
				return nil, ErrUnsupportedTable
			}
			rowCells[len(rowCells)-1] += line
		}
	}
	if inTable || !headersComplete {
		return nil, ErrUnsupportedTable
	}
	return lyricLines(sourceRows, false)
}

func isSafeStructuredCellContinuation(line string) bool {
	if strings.TrimSpace(line) == "" || strings.ContainsAny(line, "\r\n") {
		return false
	}
	for _, structural := range []string{"{{", "}}", "[[", "]]", "{|", "|}", "<", ">"} {
		if strings.Contains(line, structural) {
			return false
		}
	}
	return true
}

func isSafeStructuredRowSeparator(line string) bool {
	if line == "|-" {
		return true
	}
	attributes := strings.TrimSpace(strings.TrimPrefix(line, "|-"))
	return attributes != "" && parseStrictStructuredAttributes(attributes, true)
}

func parseStrictStructuredAttributes(value string, displayOnly bool) bool {
	if strings.Contains(value, "{{") || strings.Contains(value, "}}") || strings.Contains(value, "[[") ||
		strings.Contains(value, "]]") || strings.Contains(value, "<") || strings.Contains(value, ">") ||
		strings.Contains(value, "`") || strings.Contains(value, "|") {
		return false
	}
	seen := map[string]struct{}{}
	parsed := false
	for offset := 0; offset < len(value); {
		for offset < len(value) && (value[offset] == ' ' || value[offset] == '\t') {
			offset++
		}
		if offset == len(value) {
			return parsed
		}
		name := structuredAttributeName.FindString(value[offset:])
		if name == "" {
			return false
		}
		offset += len(name)
		lowerName := strings.ToLower(name)
		if displayOnly && !structuredDisplayAttributeName.MatchString(lowerName) {
			return false
		}
		if _, exists := seen[lowerName]; exists {
			return false
		}
		seen[lowerName] = struct{}{}
		for offset < len(value) && (value[offset] == ' ' || value[offset] == '\t') {
			offset++
		}
		if offset == len(value) || value[offset] != '=' {
			return false
		}
		offset++
		for offset < len(value) && (value[offset] == ' ' || value[offset] == '\t') {
			offset++
		}
		if offset == len(value) {
			return false
		}
		if value[offset] == '\'' || value[offset] == '"' {
			quote := value[offset]
			offset++
			start := offset
			for offset < len(value) && value[offset] != quote {
				offset++
			}
			if offset == len(value) || offset == start {
				return false
			}
			offset++
			if offset < len(value) && value[offset] != ' ' && value[offset] != '\t' {
				return false
			}
		} else {
			for {
				start := offset
				for offset < len(value) && value[offset] != ' ' && value[offset] != '\t' {
					if value[offset] == '\'' || value[offset] == '"' || value[offset] == '=' {
						return false
					}
					offset++
				}
				if offset == start {
					return false
				}
				spaces := offset
				for offset < len(value) && (value[offset] == ' ' || value[offset] == '\t') {
					offset++
				}
				if offset == len(value) {
					break
				}
				candidate := structuredAttributeName.FindString(value[offset:])
				candidateEnd := offset + len(candidate)
				for candidateEnd < len(value) && (value[candidateEnd] == ' ' || value[candidateEnd] == '\t') {
					candidateEnd++
				}
				if candidate != "" && candidateEnd < len(value) && value[candidateEnd] == '=' {
					break
				}
				if offset == spaces {
					return false
				}
			}
		}
		parsed = true
	}
	return parsed
}

func looksLikeStructuredTableCellAttributes(value string) bool {
	separator := firstTopLevelStructuredPipe(value)
	if separator < 0 {
		return false
	}
	return looksLikeStructuredTableCellAttributePrefix(value[:separator])
}

func looksLikeStructuredTableCellAttributePrefix(value string) bool {
	attributes := strings.TrimSpace(value)
	if attributes == "" {
		return false
	}
	lower := strings.ToLower(attributes)
	return strings.Contains(attributes, "=") || strings.Contains(lower, "style") || strings.Contains(lower, "class") ||
		strings.Contains(lower, "scope")
}

func firstTopLevelStructuredPipe(value string) int {
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
		case templateDepth > 0 && index+1 < len(value) && value[index:index+2] == "}}":
			templateDepth--
			index++
		case templateDepth == 0 && index+1 < len(value) && value[index:index+2] == "[[":
			linkDepth++
			index++
		case templateDepth == 0 && linkDepth > 0 && index+1 < len(value) && value[index:index+2] == "]]":
			linkDepth--
			index++
		case templateDepth == 0 && linkDepth == 0 && value[index] == '<':
			inTag = true
		case templateDepth == 0 && linkDepth == 0 && value[index] == '|':
			return index
		}
	}
	return -1
}
