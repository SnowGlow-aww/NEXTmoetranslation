package lyricssource

import (
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"moesekai/server/internal/model"
)

var (
	moegirlLyricsHeadingPattern = regexp.MustCompile(`(?m)^===[ \t]*(?:歌词|歌詞)[ \t]*===[ \t]*$`)
	moegirlSubheadingPattern    = regexp.MustCompile(`(?m)^===[^=].*?===[ \t]*$`)
	moegirlTagPattern           = regexp.MustCompile(`(?i)^<--Tag-(Start|End)(?::[ \t]*(Full|Game)[ \t]+Ver\.)?-->$`)
	moegirlColorPattern         = regexp.MustCompile(`(?i)^#[0-9a-f]{3}(?:[0-9a-f]{3})?$`)
	moegirlGradientPattern      = regexp.MustCompile(`(?i)^lg\([ \t]*(#[0-9a-f]{3}(?:[0-9a-f]{3})?)(?:[ \t]*,[ \t]*(#[0-9a-f]{3}(?:[0-9a-f]{3})?)){1,15}[ \t]*\)$`)
	moegirlRomaParameterPattern = regexp.MustCompile(`(?i)^(?:roma|romaji|romanization|romanized)$`)
)

type moegirlPerformerRef struct {
	performerIDs []string
}

type moegirlInlinePiece struct {
	text       string
	sourceRuby []RubySpan
}

type moegirlTaggedMode uint8

const (
	maxMoegirlPerformers = 256

	moegirlModeShared moegirlTaggedMode = iota
	moegirlModeFull
	moegirlModeGame
)

// MoegirlSectionExtraction is the source-only parse of one LyricsKai/ext song
// section. Full owns authoritative text whenever this source proves it; Game
// retains tagged Game-only evidence without promoting it. GameLineIndexes is
// the unique ordered projection used only when this source proves both versions.
type MoegirlSectionExtraction struct {
	Full            Extraction
	Game            Extraction
	GameLineIndexes []int
	ReasonCode      model.LyricsSourceVersionReasonCode
	TaggedFull      bool
	TaggedGame      bool
}

// ParseMoegirlSection parses isolated source evidence without catalog
// authority. Performer segmentation remains disabled, but the source rendition
// kind is not rewritten. Production callers must pass an explicit catalog
// policy through ParseMoegirlSectionWithPolicy.
func ParseMoegirlSection(section string) (MoegirlSectionExtraction, error) {
	return ParseMoegirlSectionWithPolicy(section, "")
}

// ParseMoegirlSectionWithPolicy parses one song section. Translated and
// Roma/romanization parameters are structural landmarks only and can never
// contribute output.
func ParseMoegirlSectionWithPolicy(section string, policy PerformerSegmentationPolicy) (MoegirlSectionExtraction, error) {
	return parseMoegirlSongSection(section, policy)
}

func parseMoegirlSongSection(section string, policy PerformerSegmentationPolicy) (MoegirlSectionExtraction, error) {
	if !utf8.ValidString(section) || len(section) > maxResponseBytes {
		return MoegirlSectionExtraction{}, ErrLyricsTooLarge
	}
	if policy != "" && policy != PerformerSegmentationDisabled && policy != PerformerSegmentationSekaiEligible {
		return MoegirlSectionExtraction{}, ErrMalformedResponse
	}
	lyricsSection := section
	if location := moegirlLyricsHeadingPattern.FindStringIndex(section); location != nil {
		lyricsSection = section[location[1]:]
		if next := moegirlSubheadingPattern.FindStringIndex(lyricsSection); next != nil {
			lyricsSection = lyricsSection[:next[0]]
		}
	}
	start, end, inner, ok := findBalancedNamedTemplate(lyricsSection, "LyricsKai/ext")
	if !ok {
		return MoegirlSectionExtraction{}, ErrMissingLyrics
	}
	if _, _, _, duplicate := findBalancedNamedTemplate(lyricsSection[end:], "LyricsKai/ext"); duplicate {
		return MoegirlSectionExtraction{}, ErrUnsupportedTable
	}
	if strings.Contains(strings.ToLower(lyricsSection[:start]), "{{lyricskai/ext") ||
		strings.Contains(strings.ToLower(lyricsSection[end:]), "{{lyricskai/ext") {
		return MoegirlSectionExtraction{}, ErrUnsupportedTable
	}
	params, err := parseMoegirlLyricsParameters(inner)
	if err != nil {
		return MoegirlSectionExtraction{}, err
	}
	classification, err := classifyMoegirlVersionMarkers(params["original"], policy)
	if err != nil {
		return MoegirlSectionExtraction{}, err
	}
	allowSegmentation := performerSegmentationAllowed(policy, "sekai")
	performers := []Performer{}
	refs := []moegirlPerformerRef{}
	if allowSegmentation {
		performers, refs, err = parseMoegirlPerformers(params["charas"], params["colors"])
		if err != nil {
			return classification, err
		}
	}
	full, game, taggedFull, taggedGame, err := parseMoegirlOriginal(params["original"], refs, allowSegmentation)
	if err != nil {
		return classification, err
	}
	if len(full) == 0 && !(taggedGame && !taggedFull && len(game) > 0) {
		return MoegirlSectionExtraction{}, ErrMissingLyrics
	}
	if policy == PerformerSegmentationDisabled && (taggedFull || taggedGame) {
		return MoegirlSectionExtraction{
			ReasonCode: model.LyricsSourceVersionReasonVersionConflict,
			TaggedFull: taggedFull, TaggedGame: taggedGame,
		}, ErrCatalogRenditionConflict
	}

	result := MoegirlSectionExtraction{TaggedFull: taggedFull, TaggedGame: taggedGame}
	switch {
	case taggedFull && taggedGame:
		// Full and Game are independent exact artifacts. A strict projection is
		// only an optional relation; ambiguity or a non-subset Game must not erase
		// either rendition.
		result.ReasonCode = model.LyricsSourceVersionReasonTaggedFullAndGame
		if projection, projectionErr := moegirlGameProjection(full, game); projectionErr == nil {
			result.GameLineIndexes = projection
		}
	case taggedGame && !taggedFull:
		// This provider has only tagged Game text. A later provider-aware
		// reconciliation may pair it with Vocaloid Full; this parser never
		// promotes the Game text to authoritative Full on its own.
		result.ReasonCode = model.LyricsSourceVersionReasonTaggedGameOnlyFullFromVocaloid
		result.GameLineIndexes = nil
		full = nil
	case taggedFull && !taggedGame:
		return MoegirlSectionExtraction{ReasonCode: model.LyricsSourceVersionReasonVersionConflict}, ErrUnsupportedTable
	default:
		result.ReasonCode = model.LyricsSourceVersionReasonUntaggedFullOnly
	}
	version := LyricsVersion{Kind: "sekai", Label: "Project SEKAI Version"}
	if policy == PerformerSegmentationDisabled {
		version = LyricsVersion{Kind: "vocaloid", Label: "Vocaloid Version"}
	}
	baseExtraction := Extraction{
		Version: version, Performers: performers, RubyGeneratorVersion: rubyGeneratorVersion,
	}
	result.Full = baseExtraction
	result.Full.Lines = full
	result.Game = baseExtraction
	result.Game.Lines = game
	return result, nil
}

func parseMoegirlLyricsParameters(inner string) (map[string]string, error) {
	fields, ok := splitTopLevelStructuredFields(inner, "|")
	if !ok || len(fields) < 2 || !strings.EqualFold(strings.TrimSpace(fields[0]), "LyricsKai/ext") {
		return nil, ErrUnsupportedTable
	}
	params := make(map[string]string, len(fields)-1)
	for _, field := range fields[1:] {
		separator := strings.IndexByte(field, '=')
		if separator < 0 {
			if strings.TrimSpace(field) == "" {
				continue
			}
			return nil, ErrUnsupportedTable
		}
		name := strings.ToLower(strings.TrimSpace(field[:separator]))
		value := strings.TrimSpace(field[separator+1:])
		if name == "" {
			return nil, ErrUnsupportedTable
		}
		if _, duplicate := params[name]; duplicate {
			return nil, ErrUnsupportedTable
		}
		switch {
		case name == "type", name == "default", name == "colors", name == "charas", name == "tracolors",
			name == "charablock", name == "original", name == "translated", moegirlRomaParameterPattern.MatchString(name):
			params[name] = value
		default:
			return nil, ErrUnsupportedTable
		}
	}
	for _, required := range []string{"type", "colors", "charas", "original"} {
		if strings.TrimSpace(params[required]) == "" {
			return nil, ErrMissingLyrics
		}
	}
	types := strings.Split(strings.ToLower(params["type"]), ",")
	seenTypes := map[string]bool{}
	for _, value := range types {
		value = strings.TrimSpace(value)
		if value == "" || (value != "multiver" && value != "colors") || seenTypes[value] {
			return nil, ErrUnsupportedTable
		}
		seenTypes[value] = true
	}
	if !seenTypes["multiver"] || !seenTypes["colors"] || len(seenTypes) != 2 {
		return nil, ErrUnsupportedTable
	}
	if value := strings.TrimSpace(params["default"]); value != "" &&
		!strings.EqualFold(value, "Game Ver.") && !strings.EqualFold(value, "Full Ver.") {
		return nil, ErrUnsupportedTable
	}
	for _, option := range []string{"tracolors", "charablock"} {
		if value, exists := params[option]; exists && !strings.EqualFold(strings.TrimSpace(value), "on") {
			return nil, ErrUnsupportedTable
		}
	}
	return params, nil
}

func parseMoegirlPerformers(charasValue, colorsValue string) ([]Performer, []moegirlPerformerRef, error) {
	charas := splitMoegirlList(charasValue)
	colors := splitMoegirlList(colorsValue)
	if len(charas) == 0 || len(charas) != len(colors) || len(charas) > maxMoegirlPerformers {
		return nil, nil, ErrUnsupportedTable
	}

	type charaEntry struct {
		names  []string
		chorus bool
	}
	entries := make([]charaEntry, len(charas))
	performers := []Performer{}
	performerByName := map[string]string{}
	for index, raw := range charas {
		value := strings.TrimSpace(raw)
		if strings.HasSuffix(strings.ToLower(value), "(@nolink)") {
			value = strings.TrimSpace(value[:len(value)-len("(@nolink)")])
		}
		if value == "" {
			return nil, nil, ErrUnsupportedTable
		}
		names := splitMoegirlCharaNames(value)
		if len(names) == 0 {
			return nil, nil, ErrUnsupportedTable
		}
		entries[index] = charaEntry{names: names, chorus: len(names) == 1 && isMoegirlChorusName(names[0])}
		if len(names) != 1 || entries[index].chorus {
			continue
		}
		name := canonicalMoegirlPerformerName(names[0])
		id := normalizePerformerID(name)
		if id == "" {
			return nil, nil, ErrUnsupportedTable
		}
		key := normalizeTitle(name)
		if _, duplicate := performerByName[key]; duplicate {
			return nil, nil, ErrUnsupportedTable
		}
		color, ok := normalizeMoegirlColor(colors[index])
		if !ok {
			return nil, nil, ErrUnsupportedTable
		}
		performerByName[key] = id
		performers = append(performers, Performer{PerformerID: id, Name: name, Color: color})
	}
	if len(performers) == 0 {
		return nil, nil, ErrUnsupportedTable
	}

	refs := make([]moegirlPerformerRef, len(entries))
	allIDs := make([]string, len(performers))
	for index, performer := range performers {
		allIDs[index] = performer.PerformerID
	}
	for index, entry := range entries {
		if entry.chorus {
			refs[index] = moegirlPerformerRef{performerIDs: append([]string{}, allIDs...)}
			if !validMoegirlGroupColor(colors[index]) {
				return nil, nil, ErrUnsupportedTable
			}
			continue
		}
		ids := make([]string, 0, len(entry.names))
		seen := map[string]struct{}{}
		for _, rawName := range entry.names {
			name := canonicalMoegirlPerformerName(rawName)
			id := performerByName[normalizeTitle(name)]
			if id == "" {
				return nil, nil, ErrUnsupportedTable
			}
			if _, duplicate := seen[id]; duplicate {
				return nil, nil, ErrUnsupportedTable
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
		if len(entry.names) == 1 {
			if _, ok := normalizeMoegirlColor(colors[index]); !ok {
				return nil, nil, ErrUnsupportedTable
			}
		} else if !validMoegirlGroupColor(colors[index]) {
			return nil, nil, ErrUnsupportedTable
		}
		refs[index] = moegirlPerformerRef{performerIDs: ids}
	}
	return performers, refs, nil
}

func splitMoegirlList(value string) []string {
	fields := strings.FieldsFunc(value, func(current rune) bool { return current == ';' || current == '；' })
	result := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field != "" {
			result = append(result, field)
		}
	}
	return result
}

func splitMoegirlCharaNames(value string) []string {
	fields := strings.FieldsFunc(value, func(current rune) bool { return current == '、' || current == ',' || current == '，' })
	result := make([]string, 0, len(fields))
	for _, field := range fields {
		field = canonicalMoegirlPerformerName(field)
		if field == "" {
			return nil
		}
		result = append(result, field)
	}
	return result
}

func canonicalMoegirlPerformerName(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(norm.NFKC.String(html.UnescapeString(value)))), " ")
	if open := strings.LastIndexByte(value, '('); open > 0 && strings.HasSuffix(value, ")") {
		suffix := normalizeTitle(value[open+1 : len(value)-1])
		if suffix == normalizeTitle("世界计划") || suffix == normalizeTitle("世界計劃") || suffix == normalizeTitle("世界プロジェクト") {
			value = strings.TrimSpace(value[:open])
		}
	}
	return value
}

func isMoegirlChorusName(value string) bool {
	normalized := normalizeTitle(value)
	return normalized == normalizeTitle("合唱") || normalized == normalizeTitle("全员") || normalized == normalizeTitle("全員")
}

func normalizeMoegirlColor(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if !moegirlColorPattern.MatchString(value) {
		return "", false
	}
	value = strings.ToUpper(value)
	if len(value) == 4 {
		value = fmt.Sprintf("#%c%c%c%c%c%c", value[1], value[1], value[2], value[2], value[3], value[3])
	}
	return value, true
}

func validMoegirlGroupColor(value string) bool {
	if _, ok := normalizeMoegirlColor(value); ok {
		return true
	}
	return moegirlGradientPattern.MatchString(strings.TrimSpace(value))
}

// classifyMoegirlVersionMarkers is deliberately independent of lyric and ruby
// parsing. Once exact source markers prove a catalog conflict, a later
// unsupported inline/ruby shape must not erase that diagnostic or enable a
// provider fallback.
func classifyMoegirlVersionMarkers(original string, policy PerformerSegmentationPolicy) (MoegirlSectionExtraction, error) {
	mode := moegirlModeShared
	taggedFull := false
	taggedGame := false
	for _, rawLine := range strings.Split(strings.ReplaceAll(original, "\r", ""), "\n") {
		trimmed := strings.TrimSpace(rawLine)
		if marker := moegirlTagPattern.FindStringSubmatch(trimmed); marker != nil {
			action := strings.ToLower(marker[1])
			label := strings.ToLower(marker[2])
			if action == "start" {
				if mode != moegirlModeShared || label == "" {
					return MoegirlSectionExtraction{}, ErrUnsupportedTable
				}
				switch label {
				case "full":
					mode = moegirlModeFull
					taggedFull = true
				case "game":
					mode = moegirlModeGame
					taggedGame = true
				default:
					return MoegirlSectionExtraction{}, ErrUnsupportedTable
				}
			} else {
				if mode == moegirlModeShared || label != "" {
					return MoegirlSectionExtraction{}, ErrUnsupportedTable
				}
				mode = moegirlModeShared
			}
			continue
		}
		if strings.HasPrefix(strings.ToLower(trimmed), "<--tag-") {
			return MoegirlSectionExtraction{}, ErrUnsupportedTable
		}
	}
	if mode != moegirlModeShared {
		return MoegirlSectionExtraction{}, ErrUnsupportedTable
	}
	result := MoegirlSectionExtraction{TaggedFull: taggedFull, TaggedGame: taggedGame}
	if policy == PerformerSegmentationDisabled && (taggedFull || taggedGame) || taggedFull && !taggedGame {
		result.ReasonCode = model.LyricsSourceVersionReasonVersionConflict
	}
	return result, nil
}

func parseMoegirlOriginal(original string, refs []moegirlPerformerRef, allowSegmentation bool) ([]StructuredLine, []StructuredLine, bool, bool, error) {
	original = strings.ReplaceAll(original, "\r", "")
	mode := moegirlModeShared
	taggedFull := false
	taggedGame := false
	full := []StructuredLine{}
	game := []StructuredLine{}
	fullStanza := false
	gameStanza := false
	fullBytes := 0
	gameBytes := 0
	for _, rawLine := range strings.Split(original, "\n") {
		trimmed := strings.TrimSpace(rawLine)
		if marker := moegirlTagPattern.FindStringSubmatch(trimmed); marker != nil {
			action := strings.ToLower(marker[1])
			label := strings.ToLower(marker[2])
			if action == "start" {
				if mode != moegirlModeShared || label == "" {
					return nil, nil, false, false, ErrUnsupportedTable
				}
				switch label {
				case "full":
					mode = moegirlModeFull
					taggedFull = true
				case "game":
					mode = moegirlModeGame
					taggedGame = true
				default:
					return nil, nil, false, false, ErrUnsupportedTable
				}
			} else {
				if mode == moegirlModeShared || label != "" {
					return nil, nil, false, false, ErrUnsupportedTable
				}
				mode = moegirlModeShared
			}
			continue
		}
		if strings.HasPrefix(strings.ToLower(trimmed), "<--tag-") {
			return nil, nil, false, false, ErrUnsupportedTable
		}
		if trimmed == "" {
			switch mode {
			case moegirlModeShared:
				if len(full) > 0 {
					fullStanza = true
				}
				if len(game) > 0 {
					gameStanza = true
				}
			case moegirlModeFull:
				if len(full) > 0 {
					fullStanza = true
				}
			case moegirlModeGame:
				if len(game) > 0 {
					gameStanza = true
				}
			}
			continue
		}
		line, err := parseMoegirlOriginalLine(trimmed, refs, allowSegmentation)
		if err != nil {
			return nil, nil, false, false, err
		}
		switch mode {
		case moegirlModeShared:
			fullLine := cloneStructuredLine(line)
			fullLine.StanzaBreakBefore = fullStanza
			gameLine := cloneStructuredLine(line)
			gameLine.StanzaBreakBefore = gameStanza
			if err := appendMoegirlLine(&full, fullLine, &fullBytes); err != nil {
				return nil, nil, false, false, err
			}
			if err := appendMoegirlLine(&game, gameLine, &gameBytes); err != nil {
				return nil, nil, false, false, err
			}
			fullStanza, gameStanza = false, false
		case moegirlModeFull:
			line.StanzaBreakBefore = fullStanza
			if err := appendMoegirlLine(&full, line, &fullBytes); err != nil {
				return nil, nil, false, false, err
			}
			fullStanza = false
		case moegirlModeGame:
			line.StanzaBreakBefore = gameStanza
			if err := appendMoegirlLine(&game, line, &gameBytes); err != nil {
				return nil, nil, false, false, err
			}
			gameStanza = false
		}
	}
	if mode != moegirlModeShared {
		return nil, nil, false, false, ErrUnsupportedTable
	}
	return full, game, taggedFull, taggedGame, nil
}

func appendMoegirlLine(lines *[]StructuredLine, line StructuredLine, totalBytes *int) error {
	if line.Japanese == "" || len(line.Japanese) > maxExtractedLineBytes || len(*lines) >= maxExtractedLines ||
		*totalBytes > maxExtractedTextBytes-len(line.Japanese) {
		return ErrLyricsTooLarge
	}
	*totalBytes += len(line.Japanese)
	*lines = append(*lines, line)
	return nil
}

func parseMoegirlOriginalLine(line string, refs []moegirlPerformerRef, allowSegmentation bool) (StructuredLine, error) {
	type rawSegment struct {
		performerIDs []string
		pieces       []moegirlInlinePiece
	}
	segments := []rawSegment{{performerIDs: []string{}}}
	cursor := 0
	for cursor < len(line) {
		switch {
		case strings.HasPrefix(line[cursor:], "{{"):
			_, end, inner, ok := balancedTemplateAt(line, cursor)
			if !ok {
				return StructuredLine{}, ErrUnsupportedTable
			}
			pieces, err := parseMoegirlInlineTemplate(inner)
			if err != nil {
				return StructuredLine{}, err
			}
			segments[len(segments)-1].pieces = append(segments[len(segments)-1].pieces, pieces...)
			cursor = end
		case strings.HasPrefix(strings.ToLower(line[cursor:]), "<nowiki>"):
			end := strings.Index(strings.ToLower(line[cursor+len("<nowiki>"):]), "</nowiki>")
			if end < 0 {
				return StructuredLine{}, ErrUnsupportedTable
			}
			startContent := cursor + len("<nowiki>")
			endContent := startContent + end
			content := line[startContent:endContent]
			if content == "" || strings.ContainsAny(content, "\r\n<>") || strings.Contains(content, "{{") || strings.Contains(content, "}}") {
				return StructuredLine{}, ErrUnsupportedTable
			}
			segments[len(segments)-1].pieces = append(segments[len(segments)-1].pieces, moegirlInlinePiece{text: content})
			cursor = endContent + len("</nowiki>")
		case line[cursor] == '@':
			ids, consumed, ok := parseMoegirlPerformerMarker(line[cursor:], refs, allowSegmentation)
			if !ok {
				return StructuredLine{}, ErrUnsupportedTable
			}
			if allowSegmentation {
				if len(segments[len(segments)-1].pieces) == 0 {
					segments[len(segments)-1].performerIDs = ids
				} else {
					segments = append(segments, rawSegment{performerIDs: ids})
				}
			}
			cursor += consumed
		default:
			next := len(line)
			for _, token := range []string{"{{", "<nowiki>", "<NOWIKI>", "@"} {
				if offset := strings.Index(line[cursor:], token); offset >= 0 && cursor+offset < next {
					next = cursor + offset
				}
			}
			plain, err := sanitizeMoegirlPlainInline(line[cursor:next])
			if err != nil {
				return StructuredLine{}, err
			}
			if plain != "" {
				segments[len(segments)-1].pieces = append(segments[len(segments)-1].pieces, moegirlInlinePiece{text: plain})
			}
			cursor = next
		}
	}

	structuredSegments := make([]LyricsSegment, 0, len(segments))
	var japanese strings.Builder
	for _, segment := range segments {
		text, ruby, err := moegirlPiecesRuby(segment.pieces)
		if err != nil {
			return StructuredLine{}, err
		}
		if text == "" {
			continue
		}
		if len(structuredSegments) > 0 && stringSlicesEqual(structuredSegments[len(structuredSegments)-1].PerformerIDs, segment.performerIDs) {
			last := &structuredSegments[len(structuredSegments)-1]
			last.Text += text
			last.Ruby = appendRubySpans(last.Ruby, ruby...)
		} else {
			structuredSegments = append(structuredSegments, LyricsSegment{
				Text: text, PerformerIDs: append([]string{}, segment.performerIDs...), Ruby: ruby,
			})
		}
		japanese.WriteString(text)
	}
	text := strings.TrimSpace(japanese.String())
	if text == "" || len(structuredSegments) == 0 {
		return StructuredLine{}, ErrMissingLyrics
	}
	// Trimming is allowed only at the outer source boundary; rebuild ruby for
	// the rare surrounding ASCII whitespace rather than risking span drift.
	if text != japanese.String() {
		trimmed, err := trimMoegirlStructuredSegments(structuredSegments)
		if err != nil {
			return StructuredLine{}, err
		}
		structuredSegments = trimmed
	}
	return StructuredLine{Japanese: text, Segments: structuredSegments, TrailingPerformerIDs: []string{}}, nil
}

func parseMoegirlPerformerMarker(value string, refs []moegirlPerformerRef, allowSegmentation bool) ([]string, int, bool) {
	if value == "" || value[0] != '@' {
		return nil, 0, false
	}
	index := 1
	for index < len(value) && value[index] >= '0' && value[index] <= '9' {
		index++
	}
	if index == 1 {
		// LyricsKai uses a bare @ to reset the current color before bold or
		// template markup. Other literal at-signs must be escaped with nowiki.
		if len(value) == 1 || strings.HasPrefix(value[1:], "''") || strings.HasPrefix(value[1:], "{{") {
			return []string{}, 1, true
		}
		return nil, 0, false
	}
	raw := value[1:index]
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed <= 0 || strconv.Itoa(parsed) != raw {
		return nil, 0, false
	}
	if !allowSegmentation {
		return []string{}, index, true
	}
	if parsed > len(refs) || len(refs[parsed-1].performerIDs) == 0 {
		return nil, 0, false
	}
	return append([]string{}, refs[parsed-1].performerIDs...), index, true
}

func parseMoegirlInlineTemplate(inner string) ([]moegirlInlinePiece, error) {
	parts, ok := splitTopLevelStructuredFields(inner, "|")
	if !ok || len(parts) == 0 {
		return nil, ErrUnsupportedTable
	}
	name := strings.ToLower(strings.TrimSpace(parts[0]))
	switch name {
	case "lj":
		if len(parts) != 2 {
			return nil, ErrUnsupportedTable
		}
		return parseMoegirlInlineValue(parts[1])
	case "注解":
		if len(parts) != 3 || strings.TrimSpace(parts[2]) == "" {
			return nil, ErrUnsupportedTable
		}
		return parseMoegirlInlineValue(parts[1])
	case "coloredlink":
		if len(parts) != 4 {
			return nil, ErrUnsupportedTable
		}
		if _, ok := normalizeMoegirlColor(parts[1]); !ok || strings.TrimSpace(parts[2]) == "" {
			return nil, ErrUnsupportedTable
		}
		return parseMoegirlInlineValue(parts[3])
	case "ruby":
		if len(parts) != 3 {
			return nil, ErrUnsupportedTable
		}
		basePieces, err := parseMoegirlInlineValue(parts[1])
		if err != nil {
			return nil, err
		}
		base := moegirlPiecesText(basePieces)
		reading, err := sanitizeMoegirlRubyReading(parts[2])
		if err != nil {
			return nil, err
		}
		if spans, ok := alignedMoegirlSourceRuby(base, reading); ok {
			return []moegirlInlinePiece{{text: base, sourceRuby: spans}}, nil
		}
		// Non-kana, whole-word, and non-grapheme-aligned source annotations
		// contribute only their base. Kagome generates the ordinary ruby later.
		return []moegirlInlinePiece{{text: base}}, nil
	default:
		return nil, ErrUnsupportedTable
	}
}

func parseMoegirlInlineValue(value string) ([]moegirlInlinePiece, error) {
	pieces := []moegirlInlinePiece{}
	cursor := 0
	for cursor < len(value) {
		if strings.HasPrefix(value[cursor:], "{{") {
			_, end, inner, ok := balancedTemplateAt(value, cursor)
			if !ok {
				return nil, ErrUnsupportedTable
			}
			nested, err := parseMoegirlInlineTemplate(inner)
			if err != nil {
				return nil, err
			}
			pieces = append(pieces, nested...)
			cursor = end
			continue
		}
		next := len(value)
		if offset := strings.Index(value[cursor:], "{{"); offset >= 0 {
			next = cursor + offset
		}
		plain, err := sanitizeMoegirlPlainInline(value[cursor:next])
		if err != nil {
			return nil, err
		}
		if plain != "" {
			pieces = append(pieces, moegirlInlinePiece{text: plain})
		}
		cursor = next
	}
	if moegirlPiecesText(pieces) == "" {
		return nil, ErrUnsupportedTable
	}
	return pieces, nil
}

func sanitizeMoegirlPlainInline(value string) (string, error) {
	if strings.Contains(value, "[[") || strings.Contains(value, "]]") || strings.ContainsAny(value, "<>[]{}") || strings.Contains(value, "}}") {
		return "", ErrUnsupportedTable
	}
	value = strings.ReplaceAll(value, "'''", "")
	value = strings.ReplaceAll(value, "''", "")
	value = html.UnescapeString(value)
	if !utf8.ValidString(value) {
		return "", ErrUnsupportedTable
	}
	for _, current := range value {
		if unicode.IsControl(current) || unicode.In(current, unicode.Cf) {
			return "", ErrUnsupportedTable
		}
	}
	return value, nil
}

func sanitizeMoegirlRubyReading(value string) (string, error) {
	value = strings.TrimSpace(html.UnescapeString(value))
	value = strings.ReplaceAll(value, "'''", "")
	value = strings.ReplaceAll(value, "''", "")
	if value == "" || !utf8.ValidString(value) || strings.ContainsAny(value, "\r\n<>[]{}|") {
		return "", ErrUnsupportedTable
	}
	return value, nil
}

func alignedMoegirlSourceRuby(base, reading string) ([]RubySpan, bool) {
	baseGraphemes := splitMoegirlGraphemes(base)
	readingGraphemes := splitMoegirlGraphemes(reading)
	if len(baseGraphemes) == 0 || len(baseGraphemes) != len(readingGraphemes) {
		return nil, false
	}
	spans := make([]RubySpan, len(baseGraphemes))
	for index := range baseGraphemes {
		if !isMoegirlKanaGrapheme(readingGraphemes[index]) {
			return nil, false
		}
		spans[index] = RubySpan{Text: baseGraphemes[index], Reading: readingGraphemes[index]}
	}
	return spans, true
}

func splitMoegirlGraphemes(value string) []string {
	if value == "" {
		return nil
	}
	result := []string{}
	start := 0
	previousZWJ := false
	regionalCount := 0
	for index, current := range value {
		if index == 0 {
			previousZWJ = current == '\u200d'
			if isRegionalIndicator(current) {
				regionalCount = 1
			}
			continue
		}
		continueCluster := previousZWJ || current == '\u200d' || unicode.Is(unicode.Mn, current) || unicode.Is(unicode.Mc, current) ||
			(current >= '\ufe00' && current <= '\ufe0f') || (current >= '\U000e0100' && current <= '\U000e01ef') ||
			(current >= '\U0001f3fb' && current <= '\U0001f3ff')
		if isRegionalIndicator(current) {
			continueCluster = regionalCount%2 == 1
			regionalCount++
		} else if !continueCluster {
			regionalCount = 0
		}
		if !continueCluster {
			result = append(result, value[start:index])
			start = index
		}
		previousZWJ = current == '\u200d'
	}
	return append(result, value[start:])
}

func isRegionalIndicator(current rune) bool {
	return current >= '\U0001f1e6' && current <= '\U0001f1ff'
}

func isMoegirlKanaGrapheme(value string) bool {
	hasKana := false
	for _, current := range value {
		switch {
		case unicode.In(current, unicode.Hiragana, unicode.Katakana):
			hasKana = true
		case current == 'ー' || current == '・':
			// Valid kana reading punctuation. A cluster containing only this
			// marker is accepted because alignment itself is grapheme-based.
		case unicode.Is(unicode.Mn, current), unicode.Is(unicode.Mc, current):
		default:
			return false
		}
	}
	return hasKana || value == "ー" || value == "・"
}

func moegirlPiecesText(pieces []moegirlInlinePiece) string {
	var result strings.Builder
	for _, piece := range pieces {
		result.WriteString(piece.text)
	}
	return result.String()
}

func moegirlPiecesRuby(pieces []moegirlInlinePiece) (string, []RubySpan, error) {
	var text strings.Builder
	ruby := []RubySpan{}
	var generated strings.Builder
	flushGenerated := func() error {
		if generated.Len() == 0 {
			return nil
		}
		spans, err := generateRubySpans(generated.String())
		if err != nil {
			return err
		}
		ruby = appendRubySpans(ruby, spans...)
		generated.Reset()
		return nil
	}
	for _, piece := range pieces {
		text.WriteString(piece.text)
		if len(piece.sourceRuby) == 0 {
			generated.WriteString(piece.text)
			continue
		}
		if err := flushGenerated(); err != nil {
			return "", nil, err
		}
		ruby = appendRubySpans(ruby, piece.sourceRuby...)
	}
	if err := flushGenerated(); err != nil {
		return "", nil, err
	}
	if text.Len() > 0 && len(ruby) == 0 {
		ruby = []RubySpan{{Text: text.String()}}
	}
	return text.String(), ruby, nil
}

func appendRubySpans(existing []RubySpan, additional ...RubySpan) []RubySpan {
	for _, span := range additional {
		if span.Text == "" {
			continue
		}
		if len(existing) > 0 && existing[len(existing)-1].Reading == "" && span.Reading == "" {
			existing[len(existing)-1].Text += span.Text
		} else {
			existing = append(existing, span)
		}
	}
	return existing
}

func trimMoegirlStructuredSegments(segments []LyricsSegment) ([]LyricsSegment, error) {
	for len(segments) > 0 && strings.TrimSpace(segments[0].Text) == "" {
		segments = segments[1:]
	}
	for len(segments) > 0 && strings.TrimSpace(segments[len(segments)-1].Text) == "" {
		segments = segments[:len(segments)-1]
	}
	if len(segments) == 0 {
		return nil, ErrMissingLyrics
	}
	segments[0].Text = strings.TrimLeftFunc(segments[0].Text, unicode.IsSpace)
	segments[len(segments)-1].Text = strings.TrimRightFunc(segments[len(segments)-1].Text, unicode.IsSpace)
	texts := make([]string, len(segments))
	for index := range segments {
		texts[index] = segments[index].Text
	}
	ruby, err := generateRubySpansForTexts(texts)
	if err != nil {
		return nil, err
	}
	for index := range segments {
		segments[index].Ruby = ruby[index]
	}
	return segments, nil
}

func moegirlGameProjection(full, game []StructuredLine) ([]int, error) {
	if len(full) == 0 || len(game) == 0 || len(game) > len(full) {
		return nil, ErrUnsupportedTable
	}
	earliest := make([]int, len(game))
	fullIndex := 0
	for gameIndex, gameLine := range game {
		for fullIndex < len(full) && full[fullIndex].Japanese != gameLine.Japanese {
			fullIndex++
		}
		if fullIndex == len(full) {
			return nil, ErrUnsupportedTable
		}
		earliest[gameIndex] = fullIndex
		fullIndex++
	}
	latest := make([]int, len(game))
	fullIndex = len(full) - 1
	for gameIndex := len(game) - 1; gameIndex >= 0; gameIndex-- {
		for fullIndex >= 0 && full[fullIndex].Japanese != game[gameIndex].Japanese {
			fullIndex--
		}
		if fullIndex < 0 {
			return nil, ErrUnsupportedTable
		}
		latest[gameIndex] = fullIndex
		fullIndex--
	}
	for index := range earliest {
		if earliest[index] != latest[index] {
			return nil, ErrUnsupportedTable
		}
	}
	return earliest, nil
}

func cloneStructuredLine(line StructuredLine) StructuredLine {
	clone := line
	clone.TrailingPerformerIDs = append([]string{}, line.TrailingPerformerIDs...)
	clone.Segments = make([]LyricsSegment, len(line.Segments))
	for index, segment := range line.Segments {
		clone.Segments[index] = segment
		clone.Segments[index].PerformerIDs = append([]string{}, segment.PerformerIDs...)
		clone.Segments[index].Ruby = append([]RubySpan{}, segment.Ruby...)
	}
	return clone
}

func findBalancedNamedTemplate(value, name string) (int, int, string, bool) {
	lower := strings.ToLower(value)
	needle := "{{" + strings.ToLower(name)
	cursor := 0
	for {
		offset := strings.Index(lower[cursor:], needle)
		if offset < 0 {
			return 0, 0, "", false
		}
		start := cursor + offset
		_, end, inner, ok := balancedTemplateAt(value, start)
		if ok {
			parts, fieldsOK := splitTopLevelStructuredFields(inner, "|")
			if fieldsOK && len(parts) > 0 && strings.EqualFold(strings.TrimSpace(parts[0]), name) {
				return start, end, inner, true
			}
		}
		cursor = start + 2
	}
}

func balancedTemplateAt(value string, start int) (int, int, string, bool) {
	if start < 0 || start+2 > len(value) || value[start:start+2] != "{{" {
		return 0, 0, "", false
	}
	depth := 0
	for index := start; index+1 < len(value); index++ {
		switch value[index : index+2] {
		case "{{":
			depth++
			index++
		case "}}":
			depth--
			index++
			if depth == 0 {
				end := index + 1
				return start, end, value[start+2 : end-2], true
			}
			if depth < 0 {
				return 0, 0, "", false
			}
		}
	}
	return 0, 0, "", false
}
