package lyricssource

import (
	"strings"
)

type structuredForensicRow struct {
	separator     string
	separatorLine int
	lines         []string
	lineIndexes   []int
	stanzaBreak   bool
}

type structuredForensicTable struct {
	headers []string
	rows    []structuredForensicRow
}

type structuredForensicStanza struct {
	rows            []structuredForensicRow
	startsAtTable   bool
	endsAtTable     bool
	precededByBreak bool
	followedByBreak bool
}

// repairBoundedStructuredSourceTable performs read-only audits of one already
// selected source table. A successful audit changes only the proven two-byte
// template suffix or exact separator typo; the ordinary strict parser then
// parses the repaired body from scratch.
func repairBoundedStructuredSourceTable(body string, performers []Performer) string {
	repaired := body
	if candidate, ok := repairStructuredUnclosedColorWithWitness(repaired, performers); ok {
		repaired = candidate
	}
	if candidate, ok := repairStructuredExactMalformedSeparator(repaired, performers); ok {
		repaired = candidate
	}
	if candidate, ok := repairStructuredLeadingDoublePipeWithWitness(repaired, performers); ok {
		repaired = candidate
	}
	return repaired
}

// repairStructuredUnclosedColorWithWitness accepts one source-cell template
// missing exactly its final "}}" only when the same strict row serialization
// forms the sole complete in-table stanza witness.
func repairStructuredUnclosedColorWithWitness(body string, performers []Performer) (string, bool) {
	start, end, table, ok := selectedStructuredSingleTable(body)
	if !ok || len(performers) == 0 {
		return body, false
	}
	allowedPerformers := make(map[string]struct{}, len(performers))
	for _, performer := range performers {
		allowedPerformers[performer.PerformerID] = struct{}{}
	}
	lines := strings.Split(table, "\n")
	candidateLine := -1
	for index, line := range lines {
		match := structuredUnclosedColorCellPattern.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		performerID := normalizePerformerID(match[1])
		if performerID == "" {
			return body, false
		}
		if _, exists := allowedPerformers[performerID]; !exists || candidateLine >= 0 {
			return body, false
		}
		candidateLine = index
	}
	if candidateLine < 0 {
		return body, false
	}
	lines[candidateLine] += "}}"
	repairedTable := strings.Join(lines, "\n")
	repairedBody := body[:start] + repairedTable + body[end:]
	if _, err := extractStructuredLyricsTable(repairedBody, performers); err != nil {
		return body, false
	}
	forensic, ok := inspectStructuredForensicJapaneseRomajiTable(repairedTable)
	if !ok {
		return body, false
	}
	candidateRow := -1
	for index, row := range forensic.rows {
		for _, lineIndex := range row.lineIndexes {
			if lineIndex == candidateLine {
				if candidateRow >= 0 {
					return body, false
				}
				candidateRow = index
			}
		}
	}
	if candidateRow < 0 || len(forensic.rows[candidateRow].lineIndexes) != 2 ||
		forensic.rows[candidateRow].lineIndexes[0] != candidateLine ||
		!isCompleteStructuredForensicTwoColumnRow(forensic.rows[candidateRow], forensic.headers) {
		return body, false
	}
	stanzas := structuredForensicStanzas(forensic)
	candidateStanza := -1
	candidateSeparator := forensic.rows[candidateRow].separatorLine
	for index, stanza := range stanzas {
		for _, row := range stanza.rows {
			if row.separatorLine == candidateSeparator {
				candidateStanza = index
			}
		}
	}
	if candidateStanza < 0 || !isCompleteStructuredForensicStanza(stanzas[candidateStanza], forensic.headers) {
		return body, false
	}
	witnesses := 0
	for index, stanza := range stanzas {
		if index == candidateStanza || !isCompleteStructuredForensicStanza(stanza, forensic.headers) {
			continue
		}
		if sameStructuredForensicStanza(stanzas[candidateStanza], stanza) {
			witnesses++
		}
	}
	if witnesses != 1 {
		return body, false
	}
	return repairedBody, true
}

// repairStructuredExactMalformedSeparator recognizes only a literal "|-f"
// between two complete rows of one strict Japanese/Romaji table.
func repairStructuredExactMalformedSeparator(body string, performers []Performer) (string, bool) {
	start, end, table, ok := selectedStructuredSingleTable(body)
	if !ok {
		return body, false
	}
	lines := strings.Split(table, "\n")
	candidateLine := -1
	for index, line := range lines {
		if line != "|-f" {
			continue
		}
		if candidateLine >= 0 {
			return body, false
		}
		candidateLine = index
	}
	if candidateLine < 0 {
		return body, false
	}
	lines[candidateLine] = "|-"
	repairedTable := strings.Join(lines, "\n")
	repairedBody := body[:start] + repairedTable + body[end:]
	if _, err := extractStructuredLyricsTable(repairedBody, performers); err != nil {
		return body, false
	}
	forensic, ok := inspectStructuredForensicJapaneseRomajiTable(repairedTable)
	if !ok {
		return body, false
	}
	followingRow := -1
	for index, row := range forensic.rows {
		if row.separatorLine == candidateLine {
			followingRow = index
			break
		}
	}
	if followingRow <= 0 || followingRow >= len(forensic.rows) ||
		!isCompleteStructuredForensicTwoColumnRow(forensic.rows[followingRow-1], forensic.headers) ||
		!isCompleteStructuredForensicTwoColumnRow(forensic.rows[followingRow], forensic.headers) {
		return body, false
	}
	return repairedBody, true
}

// repairStructuredLeadingDoublePipeWithWitness removes one accidental extra
// leading pipe from a source cell only when the repaired Japanese/Romaji table
// parses strictly and the affected row is bracketed by complete rows.
func repairStructuredLeadingDoublePipeWithWitness(body string, performers []Performer) (string, bool) {
	start, end, table, ok := selectedStructuredSingleTable(body)
	if !ok {
		return body, false
	}
	lines := strings.Split(table, "\n")
	candidateLine := -1
	for index, line := range lines {
		if !strings.HasPrefix(line, "||") || strings.HasPrefix(line, "|||") || strings.TrimSpace(strings.TrimPrefix(line, "||")) == "" {
			continue
		}
		if candidateLine >= 0 {
			return body, false
		}
		candidateLine = index
	}
	if candidateLine < 0 {
		return body, false
	}
	lines[candidateLine] = strings.TrimPrefix(lines[candidateLine], "|")
	repairedTable := strings.Join(lines, "\n")
	repairedBody := body[:start] + repairedTable + body[end:]
	if _, err := extractStructuredLyricsTable(repairedBody, performers); err != nil {
		return body, false
	}
	forensic, ok := inspectStructuredForensicJapaneseRomajiTable(repairedTable)
	if !ok {
		return body, false
	}
	candidateRow := -1
	for index, row := range forensic.rows {
		for _, lineIndex := range row.lineIndexes {
			if lineIndex == candidateLine {
				if candidateRow >= 0 {
					return body, false
				}
				candidateRow = index
			}
		}
	}
	if candidateRow <= 0 || candidateRow >= len(forensic.rows)-1 ||
		!isCompleteStructuredForensicTwoColumnRow(forensic.rows[candidateRow-1], forensic.headers) ||
		!isCompleteStructuredForensicTwoColumnRow(forensic.rows[candidateRow], forensic.headers) ||
		!isCompleteStructuredForensicTwoColumnRow(forensic.rows[candidateRow+1], forensic.headers) {
		return body, false
	}
	cells, ok := structuredForensicRowCells(forensic.rows[candidateRow], forensic.headers)
	if !ok || len(cells) != 2 {
		return body, false
	}
	source, err := sanitizePlaintextStructuredCell(cells[0], false)
	if err != nil || !containsStructuredJapanese(source) {
		return body, false
	}
	return repairedBody, true
}

func selectedStructuredSingleTable(body string) (int, int, string, bool) {
	start := strings.Index(body, "{|")
	end := strings.LastIndex(body, "|}")
	if start < 0 || end <= start || strings.Count(body, "{|") != 1 || strings.Count(body, "|}") != 1 {
		return 0, 0, "", false
	}
	end += 2
	return start, end, body[start:end], true
}

func inspectStructuredForensicJapaneseRomajiTable(table string) (structuredForensicTable, bool) {
	lines := strings.Split(strings.ReplaceAll(table, "\r", ""), "\n")
	if len(lines) < 4 || !strings.HasPrefix(strings.TrimSpace(lines[0]), "{|") {
		return structuredForensicTable{}, false
	}
	headers := make([]string, 0, 2)
	firstSeparator := -1
headerLoop:
	for index := 1; index < len(lines); index++ {
		line := strings.TrimSpace(lines[index])
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "!"):
			rawHeaders, ok := splitTopLevelStructuredFields(strings.TrimPrefix(line, "!"), "!!")
			if !ok {
				return structuredForensicTable{}, false
			}
			for _, rawHeader := range rawHeaders {
				header, err := sanitizeStructuredHeader(rawHeader)
				if err != nil {
					return structuredForensicTable{}, false
				}
				headers = append(headers, header)
			}
		case strings.HasPrefix(line, "|-"):
			if !isSafeStructuredRowSeparator(line) {
				return structuredForensicTable{}, false
			}
			firstSeparator = index
			break headerLoop
		default:
			return structuredForensicTable{}, false
		}
	}
	if firstSeparator < 0 || !isStrictStructuredJapaneseRomajiHeaders(headers) {
		return structuredForensicTable{}, false
	}
	rows := make([]structuredForensicRow, 0)
	separator := firstSeparator
	for {
		nextBoundary := -1
		closed := false
		for index := separator + 1; index < len(lines); index++ {
			line := strings.TrimSpace(lines[index])
			if strings.HasPrefix(line, "|-") {
				if !isSafeStructuredRowSeparator(line) {
					return structuredForensicTable{}, false
				}
				nextBoundary = index
				break
			}
			if line == "|}" {
				nextBoundary = index
				closed = true
				break
			}
		}
		if nextBoundary < 0 {
			return structuredForensicTable{}, false
		}
		row := structuredForensicRow{separator: strings.TrimSpace(lines[separator]), separatorLine: separator}
		for index := separator + 1; index < nextBoundary; index++ {
			line := strings.TrimSpace(lines[index])
			if line == "" {
				continue
			}
			row.lines = append(row.lines, line)
			row.lineIndexes = append(row.lineIndexes, index)
		}
		row.stanzaBreak = isStructuredForensicBreakRow(row, headers)
		rows = append(rows, row)
		if closed {
			for index := nextBoundary + 1; index < len(lines); index++ {
				if strings.TrimSpace(lines[index]) != "" {
					return structuredForensicTable{}, false
				}
			}
			break
		}
		separator = nextBoundary
	}
	return structuredForensicTable{headers: headers, rows: rows}, true
}

func isStrictStructuredJapaneseRomajiHeaders(headers []string) bool {
	if len(headers) != 2 {
		return false
	}
	source := parseStructuredLanguageLabel(strings.ReplaceAll(headers[0], "'", ""))
	romanized := parseStructuredLanguageLabel(strings.ReplaceAll(headers[1], "'", ""))
	return source.language == structuredTabLanguageJapanese && !source.explicitTranslation && !source.conflictingLanguages &&
		romanized.language == structuredTabLanguageRomanized && !romanized.explicitTranslation && !romanized.conflictingLanguages
}

func structuredForensicRowCells(row structuredForensicRow, headers []string) ([]string, bool) {
	cells := make([]string, 0, 2)
	for _, line := range row.lines {
		if !strings.HasPrefix(line, "|") {
			if len(cells) == 0 || !isSafeStructuredCellContinuation(line) {
				return nil, false
			}
			cells[len(cells)-1] += line
			continue
		}
		if strings.HasPrefix(line, "|-") || line == "|}" || strings.HasPrefix(line, "|+") {
			return nil, false
		}
		parsed, ok := splitStructuredRowCells(strings.TrimPrefix(line, "|"), headers, len(cells) > 0)
		if !ok {
			return nil, false
		}
		for _, cell := range parsed {
			cells = append(cells, strings.TrimSpace(cell))
		}
	}
	return cells, true
}

func isCompleteStructuredForensicTwoColumnRow(row structuredForensicRow, headers []string) bool {
	if row.stanzaBreak {
		return false
	}
	cells, ok := structuredForensicRowCells(row, headers)
	return ok && len(cells) == 2 && strings.TrimSpace(cells[0]) != "" && strings.TrimSpace(cells[1]) != ""
}

func isStructuredForensicBreakRow(row structuredForensicRow, headers []string) bool {
	cells, ok := structuredForensicRowCells(row, headers)
	return ok && len(cells) == 1 && structuredBreakPattern.MatchString(strings.TrimSpace(cells[0]))
}

func structuredForensicStanzas(table structuredForensicTable) []structuredForensicStanza {
	stanzas := make([]structuredForensicStanza, 0)
	for start := 0; start < len(table.rows); {
		for start < len(table.rows) && table.rows[start].stanzaBreak {
			start++
		}
		if start == len(table.rows) {
			break
		}
		end := start
		for end < len(table.rows) && !table.rows[end].stanzaBreak {
			end++
		}
		stanzas = append(stanzas, structuredForensicStanza{
			rows:            append([]structuredForensicRow(nil), table.rows[start:end]...),
			startsAtTable:   start == 0,
			endsAtTable:     end == len(table.rows),
			precededByBreak: start > 0 && table.rows[start-1].stanzaBreak,
			followedByBreak: end < len(table.rows) && table.rows[end].stanzaBreak,
		})
		start = end
	}
	return stanzas
}

func isCompleteStructuredForensicStanza(stanza structuredForensicStanza, headers []string) bool {
	if len(stanza.rows) == 0 {
		return false
	}
	for _, row := range stanza.rows {
		if !isCompleteStructuredForensicTwoColumnRow(row, headers) {
			return false
		}
	}
	return true
}

func sameStructuredForensicStanza(left, right structuredForensicStanza) bool {
	if left.startsAtTable != right.startsAtTable || left.endsAtTable != right.endsAtTable ||
		left.precededByBreak != right.precededByBreak || left.followedByBreak != right.followedByBreak ||
		len(left.rows) != len(right.rows) {
		return false
	}
	for index := range left.rows {
		if left.rows[index].separator != right.rows[index].separator ||
			!stringSlicesEqual(left.rows[index].lines, right.rows[index].lines) {
			return false
		}
	}
	return true
}
