package lyricssource

import (
	"html"

	"sort"

	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// sekaipediaOrderedSubsequence maps an explicit provider-tagged Game table to
// its Full table using only the authoritative Japanese-column row structure.
// Repeated rows with identical text, stanza topology, segment boundaries, and
// ordered performer tags are indistinguishable in the source, so the canonical
// leftmost embedding gives them deterministic stable Full IDs. Transient
// romaji-derived ruby and alignment-dependent display flattening never affect
// occurrence identity.
func sekaipediaOrderedSubsequence(full, game []sekaipediaColumnLine) ([]int, error) {
	if len(full) == 0 || len(game) == 0 || len(game) > len(full) {
		return nil, ErrUnsupportedTable
	}
	projection := make([]int, len(game))
	fullIndex := 0
	for gameIndex, gameLine := range game {
		for fullIndex < len(full) && !equalSekaipediaProjectionLine(full[fullIndex], gameLine) {
			fullIndex++
		}
		if fullIndex == len(full) {
			return nil, ErrUnsupportedTable
		}
		projection[gameIndex] = fullIndex
		fullIndex++
	}
	return projection, nil
}

// sekaipediaUniqueTextSubsequence is a conservative fallback for pages whose
// Game performer/stanza markup differs from Full even though the selected
// Japanese rows are identical. It succeeds only when the Japanese-text
// embedding is unique; repeated-row ambiguity remains unsupported.
func sekaipediaUniqueTextSubsequence(full, game []sekaipediaColumnLine) ([]int, error) {
	if len(full) == 0 || len(game) == 0 || len(game) > len(full) {
		return nil, ErrUnsupportedTable
	}
	text := func(line sekaipediaColumnLine) string {
		var result strings.Builder
		for _, segment := range line.segments {
			result.WriteString(segment.text)
		}
		return result.String()
	}
	left := make([]int, len(game))
	fullIndex := 0
	for gameIndex, gameLine := range game {
		wanted := text(gameLine)
		for fullIndex < len(full) && text(full[fullIndex]) != wanted {
			fullIndex++
		}
		if fullIndex == len(full) {
			return nil, ErrUnsupportedTable
		}
		left[gameIndex] = fullIndex
		fullIndex++
	}
	right := make([]int, len(game))
	fullIndex = len(full) - 1
	for gameIndex := len(game) - 1; gameIndex >= 0; gameIndex-- {
		wanted := text(game[gameIndex])
		for fullIndex >= 0 && text(full[fullIndex]) != wanted {
			fullIndex--
		}
		if fullIndex < 0 {
			return nil, ErrUnsupportedTable
		}
		right[gameIndex] = fullIndex
		fullIndex--
	}
	for index := range left {
		if left[index] != right[index] {
			return nil, ErrAmbiguous
		}
	}
	return left, nil
}

// sekaipediaSemanticSubsequence resolves repeated Japanese rows with the
// source's own line-level performer attribution as the primary identity signal.
// Full and Game pages may split the same Japanese text into different segments
// or use different whitespace, so raw row equality is intentionally not enough.
// A unique best embedding is accepted only when at least one row carries strong
// source evidence (an exact non-empty performer set); otherwise the parser
// remains fail-closed instead of guessing from position alone. Equal-scoring
// embeddings are not resolved by source order: multiple global optima remain
// ambiguous.
func sekaipediaSemanticSubsequence(full, game []sekaipediaColumnLine) ([]int, error) {
	if len(full) == 0 || len(game) == 0 || len(game) > len(full) {
		return nil, ErrUnsupportedTable
	}

	type state struct {
		valid         bool
		score         int
		strongMatches int
		bestCount     int
		mapping       []int
	}
	const (
		skipPenalty            = 1
		baseMatchScore         = 100
		rawLineBonus           = 250
		performerSetBonus      = 10000
		performerOverlapBonus  = 80
		stanzaMatchBonus       = 50
		segmentCountMatchBonus = 5
	)
	match := func(fullLine, gameLine sekaipediaColumnLine) (int, int, bool) {
		if sekaipediaProjectionNormalizedText(fullLine) != sekaipediaProjectionNormalizedText(gameLine) {
			return 0, 0, false
		}
		score := baseMatchScore
		strongMatches := 0
		if equalSekaipediaProjectionLine(fullLine, gameLine) {
			score += rawLineBonus
		}
		fullPerformers := sekaipediaProjectionPerformerKey(fullLine)
		gamePerformers := sekaipediaProjectionPerformerKey(gameLine)
		switch {
		case fullPerformers == gamePerformers && fullPerformers != "":
			score += performerSetBonus
			strongMatches++
		case fullPerformers != "" && gamePerformers != "" &&
			sekaipediaProjectionPerformerOverlap(fullLine, gameLine):
			score += performerOverlapBonus
		}
		if fullLine.stanzaBreakBefore == gameLine.stanzaBreakBefore {
			score += stanzaMatchBonus
		}
		if len(fullLine.segments) == len(gameLine.segments) {
			score += segmentCountMatchBonus
		}
		return score, strongMatches, true
	}

	next := make([]state, len(full)+1)
	for start := range next {
		next[start] = state{valid: true, bestCount: 1}
	}
	for gameIndex := len(game) - 1; gameIndex >= 0; gameIndex-- {
		current := make([]state, len(full)+1)
		for start := 0; start < len(full); start++ {
			best := state{}
			for fullIndex := start; fullIndex < len(full); fullIndex++ {
				matchScore, strongMatches, ok := match(full[fullIndex], game[gameIndex])
				if !ok {
					continue
				}
				suffix := next[fullIndex+1]
				if !suffix.valid {
					continue
				}
				candidate := state{
					valid:         true,
					score:         matchScore - (fullIndex-start)*skipPenalty + suffix.score,
					strongMatches: strongMatches + suffix.strongMatches,
					bestCount:     suffix.bestCount,
					mapping:       append([]int{fullIndex}, suffix.mapping...),
				}
				if !best.valid || candidate.score > best.score {
					best = candidate
					continue
				}
				// Every mapping with the same global score is a real tie. Do not
				// use performer-count or source order as a hidden tie-breaker;
				// the projection boundary must fail closed on multiple optima.
				if candidate.score == best.score {
					best.bestCount += candidate.bestCount
					if best.bestCount > 1 {
						best.bestCount = 2
					}
				}
			}
			current[start] = best
		}
		next = current
	}
	result := next[0]
	if !result.valid {
		return nil, ErrUnsupportedTable
	}
	if result.bestCount != 1 || result.strongMatches == 0 {
		return nil, ErrAmbiguous
	}
	return result.mapping, nil
}

func sekaipediaProjectionNormalizedText(line sekaipediaColumnLine) string {
	var result strings.Builder
	for _, segment := range line.segments {
		for _, current := range segment.text {
			if unicode.IsSpace(current) {
				continue
			}
			result.WriteRune(current)
		}
	}
	return result.String()
}

func sekaipediaProjectionPerformerKey(line sekaipediaColumnLine) string {
	seen := map[string]struct{}{}
	ids := []string{}
	for _, segment := range line.segments {
		for _, performerID := range segment.performerIDs {
			if _, exists := seen[performerID]; exists {
				continue
			}
			seen[performerID] = struct{}{}
			ids = append(ids, performerID)
		}
	}
	return strings.Join(ids, ",")
}

func sekaipediaResolveProjection(full, game []sekaipediaColumnLine) ([]int, error) {
	projection, err := sekaipediaOrderedSubsequence(full, game)
	if err == nil {
		return projection, nil
	}
	projection, err = sekaipediaUniqueTextSubsequence(full, game)
	if err == nil {
		return projection, nil
	}
	return sekaipediaSemanticSubsequence(full, game)
}

func sekaipediaProjectionPerformerOverlap(left, right sekaipediaColumnLine) bool {
	leftIDs := map[string]struct{}{}
	for _, segment := range left.segments {
		for _, performerID := range segment.performerIDs {
			leftIDs[performerID] = struct{}{}
		}
	}
	for _, segment := range right.segments {
		for _, performerID := range segment.performerIDs {
			if _, exists := leftIDs[performerID]; exists {
				return true
			}
		}
	}
	return false
}

func equalSekaipediaProjectionLine(left, right sekaipediaColumnLine) bool {
	if left.stanzaBreakBefore != right.stanzaBreakBefore || len(left.segments) != len(right.segments) {
		return false
	}
	for index := range left.segments {
		if left.segments[index].text != right.segments[index].text ||
			!stringSlicesEqual(left.segments[index].performerIDs, right.segments[index].performerIDs) {
			return false
		}
	}
	return true
}

func normalizeSekaipediaVersionSingerConjunction(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", true
	}
	value = strings.ReplaceAll(value, ";", ",")
	for _, pattern := range []string{
		"25-ji, Nightcord de.",
		"25-ji, Nightcord de",
		"25-ji,Nightcord de.",
		"25-ji,Nightcord de",
	} {
		value = strings.ReplaceAll(value, pattern, "25ji")
	}
	const conjunction = " and "
	if strings.Count(value, conjunction) > 1 {
		return "", false
	}
	if strings.Contains(value, conjunction) {
		value = strings.Replace(value, conjunction, ",", 1)
	}
	parts := strings.Split(value, ",")
	for index, part := range parts {
		part = strings.TrimSpace(part)
		switch normalizeSekaipediaSingerAlias(part) {
		case "leoneed":
			part = "L/n"
		case "moremorejump":
			part = "MMJ"
		case "vividbadsquad":
			part = "VBS"
		case "wonderlandsshowtime":
			part = "WxS"
		case "25jinightcordde", "nightcordat2500", "niigo", "25ji":
			part = "25ji"
		}
		parts[index] = part
	}
	return strings.Join(parts, ","), true
}

func sekaipediaVersionSets(records []sekaipediaVersionRecord, kind string) ([]sekaipediaSingerSet, error) {
	aliases, singersByID, err := buildSekaipediaSingerAliases(sekaipediaSingers)
	if err != nil {
		return nil, err
	}
	sets := []sekaipediaSingerSet{}
	seen := map[string]struct{}{}
	for _, record := range records {
		if record.kind != kind {
			continue
		}
		ids := []string{}
		if kind != "original" || record.singers != "" {
			singers, normalized := normalizeSekaipediaVersionSingerConjunction(record.singers)
			if !normalized {
				return nil, ErrUnsupportedTable
			}
			if kind != "alternate" {
				for _, part := range strings.Split(singers, ",") {
					switch normalizeSekaipediaSingerAlias(part) {
					case "vs", "all":
						return nil, ErrUnsupportedTable
					}
				}
			}
			versionRoster := sekaipediaSingerSet{kind: "alternate", ids: make([]string, 0, len(sekaipediaSingers))}
			for _, singer := range sekaipediaSingers {
				versionRoster.ids = append(versionRoster.ids, singer.id)
			}
			ids, err = resolveSekaipediaSingerListWithAliases(singers, versionRoster, true, aliases, singersByID)
			if err != nil {
				return nil, err
			}
		}
		key := strings.Join(ids, ",")
		if _, duplicate := seen[key]; duplicate && kind != "alternate" {
			return nil, ErrAmbiguous
		}
		seen[key] = struct{}{}
		sets = append(sets, sekaipediaSingerSet{kind: kind, ids: ids})
	}
	if len(sets) == 0 {
		return nil, ErrMissingLyrics
	}
	return sets, nil
}

func sekaipediaHasSoleOriginalVersion(records []sekaipediaVersionRecord) bool {
	return len(records) == 1 && records[0].kind == "original" && records[0].singers == ""
}

func sekaipediaHasVersionKind(records []sekaipediaVersionRecord, kind string) bool {
	for _, record := range records {
		if record.kind == kind {
			return true
		}
	}
	return false
}

func resolveSekaipediaSingerList(
	value string,
	set sekaipediaSingerSet,
	allowAggregates bool,
) ([]string, error) {
	aliases, singersByID, err := buildSekaipediaSingerAliases(sekaipediaSingers)
	if err != nil {
		return nil, err
	}
	return resolveSekaipediaSingerListWithAliases(value, set, allowAggregates, aliases, singersByID)
}

func resolveSekaipediaSingerListWithAliases(
	value string,
	set sekaipediaSingerSet,
	allowAggregates bool,
	aliases map[string]string,
	singersByID map[string]sekaipediaSinger,
) ([]string, error) {
	parts := strings.Split(value, ",")
	if len(parts) == 0 || len(parts) > 26 {
		return nil, ErrUnsupportedTable
	}
	allowed := map[string]struct{}{}
	for _, id := range set.ids {
		allowed[id] = struct{}{}
	}
	seen := map[string]struct{}{}
	seenAliases := map[string]string{}
	currentAlias := ""
	result := []string{}
	appendID := func(id string, enforceRoster bool) error {
		if _, duplicate := seen[id]; duplicate {
			if seenAliases[id] == currentAlias {
				return nil
			}
			return ErrAmbiguous
		}
		if enforceRoster && len(allowed) > 0 {
			if _, exists := allowed[id]; !exists {
				return ErrUnsupportedTable
			}
		}
		seen[id] = struct{}{}
		seenAliases[id] = currentAlias
		result = append(result, id)
		return nil
	}
	appendRosterID := func(id string) error {
		return appendID(id, true)
	}
	for _, part := range parts {
		alias := normalizeSekaipediaSingerAlias(part)
		currentAlias = alias
		if alias == "" {
			return nil, ErrUnsupportedTable
		}
		switch alias {
		case "vs":
			if !allowAggregates || len(set.ids) == 0 {
				return nil, ErrUnsupportedTable
			}
			matched := false
			for _, id := range set.ids {
				singer, exists := singersByID[id]
				if !exists {
					return nil, ErrUnsupportedTable
				}
				if !singer.virtual {
					continue
				}
				if err := appendRosterID(id); err != nil {
					return nil, err
				}
				matched = true
			}
			if !matched {
				return nil, ErrUnsupportedTable
			}
		case "unitleaders":
			if !allowAggregates || len(set.ids) == 0 {
				return nil, ErrUnsupportedTable
			}
			leaders := []string{"ichika", "minori", "kohane", "tsukasa", "kanade"}
			if err := appendSekaipediaSingerAggregate(leaders, set, allowed, appendRosterID); err != nil {
				return nil, err
			}
		case "ln", "mmj", "vbs", "wxs", "niigo", "25ji", "maleocs":
			if !allowAggregates || len(set.ids) == 0 {
				return nil, ErrUnsupportedTable
			}
			members := map[string][]string{
				"ln":      {"ichika", "saki", "honami", "shiho"},
				"mmj":     {"minori", "haruka", "airi", "shizuku"},
				"vbs":     {"kohane", "an", "akito", "toya"},
				"wxs":     {"tsukasa", "emu", "nene", "rui"},
				"niigo":   {"kanade", "mafuyu", "ena", "mizuki"},
				"25ji":    {"kanade", "mafuyu", "ena", "mizuki"},
				"maleocs": {"akito", "toya", "tsukasa", "rui"},
			}[alias]
			if err := appendSekaipediaSingerAggregate(members, set, allowed, appendRosterID); err != nil {
				return nil, err
			}
		case "enstars", "diverse":
			if !allowAggregates || len(set.ids) == 0 {
				return nil, ErrUnsupportedTable
			}
			members := []string{"sazanami_jun", "sena_izumi", "morisawa_chiaki", "sakasaki_natsume"}
			for _, id := range members {
				if _, exists := allowed[id]; !exists {
					return nil, ErrUnsupportedTable
				}
				if err := appendRosterID(id); err != nil {
					return nil, err
				}
			}
		case "alkaloid":
			if !allowAggregates || len(set.ids) == 0 {
				return nil, ErrUnsupportedTable
			}
			members := []string{"hiiro_amagi", "aira_shiratori", "mayoi_ayase", "tatsumi_kazehaya"}
			for _, id := range members {
				if _, exists := allowed[id]; !exists {
					return nil, ErrUnsupportedTable
				}
				if err := appendRosterID(id); err != nil {
					return nil, err
				}
			}
		case "crazyb":
			if !allowAggregates || len(set.ids) == 0 {
				return nil, ErrUnsupportedTable
			}
			members := []string{"rinne_amagi", "himeru", "kohaku_oukawa", "niki_shiina"}
			for _, id := range members {
				if _, exists := allowed[id]; !exists {
					return nil, ErrUnsupportedTable
				}
				if err := appendRosterID(id); err != nil {
					return nil, err
				}
			}
		case "virtualsinger":
			if !allowAggregates || len(set.ids) == 0 {
				return nil, ErrUnsupportedTable
			}
			members := []string{"miku", "rin", "len", "luka", "meiko", "kaito"}
			for _, id := range members {
				if _, exists := allowed[id]; !exists {
					return nil, ErrUnsupportedTable
				}
				if err := appendRosterID(id); err != nil {
					return nil, err
				}
			}
		case "all":
			if !allowAggregates || len(set.ids) == 0 {
				return nil, ErrUnsupportedTable
			}
			for _, id := range set.ids {
				if err := appendRosterID(id); err != nil {
					return nil, err
				}
			}
		default:
			id := aliases[alias]
			if id == "" {
				return nil, ErrUnsupportedTable
			}
			enforceRoster := set.kind != "sekai"
			if set.kind == "vocaloid" {
				singer, exists := singersByID[id]
				// The named SEKAI voice is distinct from the Project SEKAI VIRTUAL SINGER
				// roster and remains valid only when the fixed version row lists it.
				enforceRoster = !exists || !singer.virtual || id == "sekai_voice"
			}
			if err := appendID(id, enforceRoster); err != nil {
				return nil, err
			}
		}
	}
	sort.Slice(result, func(left, right int) bool {
		return singersByID[result[left]].order < singersByID[result[right]].order
	})
	return result, nil
}

func appendSekaipediaSingerAggregate(
	members []string,
	set sekaipediaSingerSet,
	allowed map[string]struct{},
	appendID func(string) error,
) error {
	if len(members) == 0 || len(set.ids) == 0 || len(allowed) != len(set.ids) {
		return ErrUnsupportedTable
	}
	for _, id := range members {
		if _, exists := allowed[id]; !exists {
			return ErrUnsupportedTable
		}
		if err := appendID(id); err != nil {
			return err
		}
	}
	return nil
}

func buildSekaipediaSingerAliases(
	singers []sekaipediaSinger,
) (map[string]string, map[string]sekaipediaSinger, error) {
	if sekaipediaSingerAliasVersion == "" || len(singers) < 26 || len(singers) > 256 {
		return nil, nil, ErrMalformedResponse
	}
	aliases := map[string]string{}
	byID := map[string]sekaipediaSinger{}
	orders := map[int]struct{}{}
	persistedIDs := map[string]struct{}{}
	for _, singer := range singers {
		if singer.id == "" || singer.persistedID == "" || singer.name == "" || singer.order < 1 || singer.order > len(singers) || len(singer.aliases) == 0 {
			return nil, nil, ErrMalformedResponse
		}
		if _, duplicate := byID[singer.id]; duplicate {
			return nil, nil, ErrAmbiguous
		}
		if _, duplicate := persistedIDs[singer.persistedID]; duplicate {
			return nil, nil, ErrAmbiguous
		}
		persistedIDs[singer.persistedID] = struct{}{}
		if _, duplicate := orders[singer.order]; duplicate {
			return nil, nil, ErrAmbiguous
		}
		orders[singer.order] = struct{}{}
		byID[singer.id] = singer
		for _, value := range append([]string{singer.id, singer.name}, singer.aliases...) {
			alias := normalizeSekaipediaSingerAlias(value)
			if alias == "" || alias == "vs" || alias == "unitleaders" {
				return nil, nil, ErrMalformedResponse
			}
			if existing := aliases[alias]; existing != "" && existing != singer.id {
				return nil, nil, ErrAmbiguous
			}
			aliases[alias] = singer.id
		}
	}
	return aliases, byID, nil
}

func normalizeSekaipediaSingerAlias(value string) string {
	value = strings.ToLower(norm.NFKC.String(strings.TrimSpace(html.UnescapeString(value))))
	var result strings.Builder
	for _, current := range value {
		if unicode.IsLetter(current) || unicode.IsNumber(current) {
			result.WriteRune(current)
		}
	}
	return result.String()
}

func sekaipediaPerformers(ids []string) []Performer {
	_, byID, err := buildSekaipediaSingerAliases(sekaipediaSingers)
	if err != nil {
		return nil
	}
	result := make([]Performer, 0, len(ids))
	for _, id := range ids {
		singer, found := byID[id]
		if !found {
			return nil
		}
		result = append(result, Performer{PerformerID: singer.persistedID, Name: singer.name})
	}
	return result
}

func sekaipediaPersistedPerformerIDs(ids []string, singersByID map[string]sekaipediaSinger) ([]string, bool) {
	result := make([]string, len(ids))
	for index, id := range ids {
		singer, found := singersByID[id]
		if !found || singer.persistedID == "" {
			return nil, false
		}
		result[index] = singer.persistedID
	}
	return result, true
}

func sameSekaipediaSingerIDs(used map[string]struct{}, wanted []string) bool {
	if len(used) != len(wanted) {
		return false
	}
	for _, id := range wanted {
		if _, exists := used[id]; !exists {
			return false
		}
	}
	return true
}

func containsSekaipediaSingerID(ids []string, wanted string) bool {
	for _, id := range ids {
		if id == wanted {
			return true
		}
	}
	return false
}
