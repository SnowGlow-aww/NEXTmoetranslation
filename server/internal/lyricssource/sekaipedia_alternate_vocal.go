package lyricssource

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"moesekai/server/internal/model"
)

func parseSekaipediaNestedRenditionTabs(body string) (map[string]string, error) {
	templates, err := parseSekaipediaTemplateSequence(body)
	if err != nil || len(templates) != 1 || templates[0].name != "#tag:tabber" {
		return map[string]string{"": body}, nil
	}
	if len(templates[0].fields) != 2 {
		return nil, ErrUnsupportedTable
	}
	inner := templates[0].fields[1]
	const escapedSeparator = "\n{{!}}-{{!}}\n"
	const literalSeparator = "\n|-|\n"
	if strings.Contains(inner, escapedSeparator) {
		if strings.Contains(inner, literalSeparator) {
			return nil, ErrUnsupportedTable
		}
		return parseSekaipediaTabberParts(strings.Split(inner, escapedSeparator))
	}
	return parseSekaipediaTabberEntries(inner)
}

type sekaipediaAlternateSourceTab struct {
	tabLabel     string
	singerLabel  string
	body         string
	isFull       bool
	path         []string
	explicitSet  *sekaipediaSingerSet
	optionalOnly bool
}

func parseSekaipediaAlternateVocals(
	tabs map[string]string,
	records []sekaipediaVersionRecord,
) ([]sekaipediaAlternateVocalExtraction, error) {
	_, singersByID, err := buildSekaipediaSingerAliases(sekaipediaSingers)
	if err != nil {
		return nil, err
	}

	versionSets := func(kind string) ([]sekaipediaSingerSet, error) {
		sets, setErr := sekaipediaVersionSets(records, kind)
		if errors.Is(setErr, ErrMissingLyrics) {
			return nil, nil
		}
		return sets, setErr
	}
	anotherSets, err := versionSets("another")
	if err != nil {
		return nil, err
	}
	sekaiSets, err := versionSets("sekai")
	if err != nil {
		return nil, err
	}
	auxiliarySets, err := versionSets("alternate")
	if err != nil {
		return nil, err
	}
	declaredSets := sekaipediaDistinctSingerSets(append(
		append(append([]sekaipediaSingerSet{}, anotherSets...), sekaiSets...), auxiliarySets...,
	))
	type auxiliaryVersionSet struct {
		set         sekaipediaSingerSet
		singerLabel string
	}
	auxiliaryByLabel := map[string]auxiliaryVersionSet{}
	auxiliaryIndex := 0
	for _, record := range records {
		if record.kind != "alternate" {
			continue
		}
		if auxiliaryIndex >= len(auxiliarySets) || strings.TrimSpace(record.label) == "" {
			return nil, ErrUnsupportedTable
		}
		label := strings.TrimSpace(record.label)
		if _, duplicate := auxiliaryByLabel[label]; duplicate {
			return nil, ErrAmbiguous
		}
		auxiliaryByLabel[label] = auxiliaryVersionSet{
			set: auxiliarySets[auxiliaryIndex], singerLabel: strings.Join(auxiliarySets[auxiliaryIndex].ids, ", "),
		}
		auxiliaryIndex++
	}
	if auxiliaryIndex != len(auxiliarySets) {
		return nil, ErrUnsupportedTable
	}
	sources := make([]sekaipediaAlternateSourceTab, 0)

	// Standard Alternate/Another Vocal top-level tabs retain their declared
	// singer-labelled nested tabs and pair Game/Full by singer identity.
	for _, top := range []struct {
		label  string
		isFull bool
	}{
		{label: "Alternate Vocal"},
		{label: "Another Vocal"},
		{label: "Alternate Vocal (Full)", isFull: true},
		{label: "Another Vocal (Full)", isFull: true},
	} {
		body := strings.TrimSpace(tabs[top.label])
		if body == "" {
			continue
		}
		nested, nestedErr := parseSekaipediaNestedRenditionTabs(body)
		if nestedErr != nil {
			return nil, fmt.Errorf("alternate tab %q nested parse: %w", top.label, nestedErr)
		}
		for label, nestedBody := range nested {
			path := []string{top.label}
			if strings.TrimSpace(label) != "" {
				path = append(path, strings.TrimSpace(label))
			}
			sources = append(sources, sekaipediaAlternateSourceTab{
				tabLabel:    strings.TrimSuffix(top.label, " (Full)"),
				singerLabel: strings.TrimSpace(label),
				body:        nestedBody,
				isFull:      top.isFull,
				path:        path,
			})
		}
	}

	// Pages also use named Archive/Auxiliary tabs for alternate renditions,
	// for example VBS Archive and April Fools. Preserve those tabs instead of
	// treating them as unsupported noise. Their singer IDs come from the tab
	// label when present, otherwise from structured Lyric attribution.
	primaryNestedLabels := sekaipediaPrimaryNestedRenditionLabels(records)
	for label, body := range tabs {
		if !sekaipediaIsOptionalAlternateTopLevelTab(label, tabs) {
			continue
		}
		tabLabel, isFull := sekaipediaOptionalAlternateTabIdentity(label)
		nested, nestedErr := parseSekaipediaNestedRenditionTabs(body)
		if nestedErr != nil {
			return nil, fmt.Errorf("optional alternate tab %q nested parse: %w", label, nestedErr)
		}
		for nestedLabel, nestedBody := range nested {
			if sekaipediaNestedLabelIsPrimary(nestedLabel, primaryNestedLabels) {
				continue
			}
			singerLabel := strings.TrimSpace(nestedLabel)
			if singerLabel == "" {
				singerLabel = sekaipediaAlternateSingerLabelFromTab(label)
			}
			var explicitSet *sekaipediaSingerSet
			if declared, ok := auxiliaryByLabel[tabLabel]; ok {
				setCopy := declared.set
				explicitSet = &setCopy
				if singerLabel == "" {
					singerLabel = declared.singerLabel
				}
			}
			path := []string{label}
			if strings.TrimSpace(nestedLabel) != "" {
				path = append(path, strings.TrimSpace(nestedLabel))
			}
			sources = append(sources, sekaipediaAlternateSourceTab{
				tabLabel:     tabLabel,
				singerLabel:  singerLabel,
				body:         nestedBody,
				isFull:       isFull,
				path:         path,
				explicitSet:  explicitSet,
				optionalOnly: true,
			})
		}
	}

	// Some pages put auxiliary renditions inside the primary Game/Full tabber,
	// such as COLORFUL LIVE. Keep every nested label other than the selected
	// primary rendition as its own alternate rendition.
	for _, top := range []struct {
		label  string
		isFull bool
	}{
		{label: "Game Version"},
		{label: "Full Version", isFull: true},
		{label: "APPEND/Full Version", isFull: true},
	} {
		body := strings.TrimSpace(tabs[top.label])
		if body == "" {
			continue
		}
		nested, nestedErr := parseSekaipediaNestedRenditionTabs(body)
		if nestedErr != nil {
			return nil, fmt.Errorf("primary auxiliary tab %q nested parse: %w", top.label, nestedErr)
		}
		if len(nested) <= 1 {
			continue
		}
		for nestedLabel, nestedBody := range nested {
			if sekaipediaNestedLabelIsPrimary(nestedLabel, primaryNestedLabels) || strings.TrimSpace(nestedLabel) == "" {
				continue
			}
			sources = append(sources, sekaipediaAlternateSourceTab{
				tabLabel:     strings.TrimSpace(nestedLabel),
				singerLabel:  sekaipediaAlternateSingerLabelFromTab(nestedLabel),
				body:         nestedBody,
				isFull:       top.isFull,
				path:         []string{top.label, strings.TrimSpace(nestedLabel)},
				optionalOnly: true,
			})
		}
	}

	type tabEntry struct {
		key         string
		tabLabel    string
		set         sekaipediaSingerSet
		setResolved bool
		singerLabel string
		gameBody    string
		fullBody    string
		gamePath    []string
		fullPath    []string
	}
	entries := map[string]tabEntry{}
	for _, source := range sources {
		set := sekaipediaSingerSet{kind: "alternate"}
		setResolved := false
		singerLabel := strings.TrimSpace(source.singerLabel)
		if source.explicitSet != nil {
			set = *source.explicitSet
			set.kind = "alternate"
			set.ids = append([]string(nil), source.explicitSet.ids...)
			setResolved = len(set.ids) > 0
		} else if singerLabel != "" {
			if resolved, resolvedLabel, ok := sekaipediaAlternateSingerSet(singerLabel, anotherSets); ok {
				set = resolved
				if expanded, unique := sekaipediaUniqueSingerSetSuperset(set, sekaiSets); unique {
					set = expanded
				}
				setResolved = true
				singerLabel = resolvedLabel
			} else if resolved, resolvedLabel, ok := sekaipediaAlternateAggregateSingerSet(
				singerLabel, declaredSets,
			); ok {
				set = resolved
				setResolved = true
				singerLabel = resolvedLabel
			}
		}
		key := strings.Join(set.ids, ",")
		if singerLabel != "" {
			if !setResolved {
				if resolvedIDs, resolveErr := resolveSekaipediaSingerList(singerLabel, sekaipediaSingerSet{}, false); resolveErr == nil {
					set = sekaipediaSingerSet{kind: "alternate", ids: resolvedIDs}
					setResolved = true
					key = strings.Join(resolvedIDs, ",")
				}
			}
			// Preserve the exact singer-labelled tab identity even when parsing
			// legitimately requires the unique source-declared unit superset.
			key = singerLabel + "\x00" + key
		}
		if source.optionalOnly && singerLabel == "" {
			key = source.tabLabel + "\x00" + key
		}
		entry := entries[source.tabLabel+"\x00"+key]
		entry.key = source.tabLabel + "\x00" + key
		entry.tabLabel = source.tabLabel
		entry.set = set
		entry.setResolved = entry.setResolved || setResolved
		if entry.singerLabel == "" {
			entry.singerLabel = singerLabel
		}
		if source.isFull {
			if entry.fullBody != "" {
				return nil, ErrAmbiguous
			}
			entry.fullBody = source.body
			entry.fullPath = append([]string(nil), source.path...)
		} else {
			if entry.gameBody != "" {
				return nil, ErrAmbiguous
			}
			entry.gameBody = source.body
			entry.gamePath = append([]string(nil), source.path...)
		}
		entries[entry.key] = entry
	}

	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]sekaipediaAlternateVocalExtraction, 0, len(keys))
	for _, key := range keys {
		entry := entries[key]
		parseBody := func(body string, requireFull bool) (*sekaipediaRenditionExtraction, error) {
			if entry.setResolved {
				return parseSekaipediaAlternateBody(body, entry.set, requireFull)
			}
			return parseSekaipediaAlternateBodyAgainstSets(body, declaredSets, requireFull)
		}
		parsedGame, gameErr := parseBody(entry.gameBody, false)
		parsedFull, fullErr := parseBody(entry.fullBody, true)
		if strings.TrimSpace(entry.gameBody) != "" && gameErr != nil {
			return nil, fmt.Errorf("alternate %q/%q game parse: %w", entry.tabLabel, entry.singerLabel, gameErr)
		}
		if strings.TrimSpace(entry.fullBody) != "" && fullErr != nil {
			return nil, fmt.Errorf("alternate %q/%q full parse: %w", entry.tabLabel, entry.singerLabel, fullErr)
		}
		if gameErr != nil && fullErr != nil {
			return nil, fmt.Errorf("alternate %q/%q has no parseable rendition: %w", entry.tabLabel, entry.singerLabel, ErrMissingLyrics)
		}
		usedIDs := make(map[string]struct{})
		if parsedGame != nil {
			for id := range parsedGame.usedIDs {
				usedIDs[id] = struct{}{}
			}
		}
		if parsedFull != nil {
			for id := range parsedFull.usedIDs {
				usedIDs[id] = struct{}{}
			}
		}
		ids := mapKeysOrdered(usedIDs)
		if len(ids) > 0 {
			entry.set = sekaipediaSingerSet{kind: "alternate", ids: ids}
			entry.setResolved = true
			if entry.singerLabel == "" {
				entry.singerLabel = strings.Join(ids, ", ")
			}
		}
		if entry.singerLabel == "" || len(entry.set.ids) == 0 {
			return nil, fmt.Errorf("alternate %q has no resolvable singer set: %w", entry.key, ErrUnsupportedTable)
		}
		alternate := sekaipediaAlternateVocalExtraction{
			Key:          sekaipediaAlternateRenditionKey(entry.tabLabel, entry.set.ids),
			TabLabel:     entry.tabLabel,
			SingerLabel:  entry.singerLabel,
			DeclaredFull: strings.TrimSpace(entry.fullBody) != "",
			DeclaredGame: strings.TrimSpace(entry.gameBody) != "",
		}
		alternate.SingerIDs, _ = sekaipediaPersistedPerformerIDs(entry.set.ids, singersByID)
		if parsedGame != nil {
			parsedGame.extraction.Version.Label = entry.tabLabel + " — " + entry.singerLabel
			alternate.Game = &parsedGame.extraction
			alternate.GameTabPath = append(model.LyricsSourceTabPath{}, entry.gamePath...)
			alternate.GameStructuredEvidence = sekaipediaStructuredEvidenceState(*parsedGame)
			alternate.gameProjectionLines = append([]sekaipediaColumnLine(nil), parsedGame.projectionLines...)
		}
		if parsedFull != nil {
			parsedFull.extraction.Version.Label = entry.tabLabel + " (Full) — " + entry.singerLabel
			alternate.Full = &parsedFull.extraction
			alternate.FullTabPath = append(model.LyricsSourceTabPath{}, entry.fullPath...)
			alternate.FullStructuredEvidence = sekaipediaStructuredEvidenceState(*parsedFull)
			alternate.fullProjectionLines = append([]sekaipediaColumnLine(nil), parsedFull.projectionLines...)
		}
		if alternate.Full != nil || alternate.Game != nil {
			result = append(result, alternate)
		}
	}
	return result, nil
}

func sekaipediaAlternateRenditionKey(tabLabel string, singerIDs []string) string {
	value := strings.ToLower(tabLabel + "-" + strings.Join(singerIDs, "-"))
	var builder strings.Builder
	lastDash := false
	for _, current := range value {
		if current >= 'a' && current <= 'z' || current >= '0' && current <= '9' {
			builder.WriteRune(current)
			lastDash = false
			continue
		}
		if builder.Len() > 0 && !lastDash {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	key := strings.Trim(builder.String(), "-")
	const maxKeyBytes = 128 - len("alternate-game-")
	if len(key) <= maxKeyBytes {
		return key
	}
	digest := sha256.Sum256([]byte(key))
	suffix := "-" + hex.EncodeToString(digest[:8])
	prefix := strings.TrimRight(key[:maxKeyBytes-len(suffix)], "-")
	return prefix + suffix
}

func parseSekaipediaAlternateBody(body string, set sekaipediaSingerSet, requireFull bool) (*sekaipediaRenditionExtraction, error) {
	if strings.TrimSpace(body) == "" {
		return nil, ErrMissingLyrics
	}
	parsed, err := parseSekaipediaRenditionWithSet(body, "alternate", set, requireFull)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func parseSekaipediaAlternateBodyAgainstSets(
	body string,
	sets []sekaipediaSingerSet,
	requireFull bool,
) (*sekaipediaRenditionExtraction, error) {
	if strings.TrimSpace(body) == "" {
		return nil, ErrMissingLyrics
	}
	if len(sets) == 0 {
		return nil, ErrUnsupportedTable
	}
	parsed, err := parseSekaipediaRenditionAgainstSets(body, "alternate", sets, requireFull)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func sekaipediaUniqueSingerSetSuperset(
	subset sekaipediaSingerSet,
	candidates []sekaipediaSingerSet,
) (sekaipediaSingerSet, bool) {
	wanted := make(map[string]struct{}, len(subset.ids))
	for _, id := range subset.ids {
		wanted[id] = struct{}{}
	}
	var selected sekaipediaSingerSet
	matches := 0
	for _, candidate := range candidates {
		if len(candidate.ids) <= len(subset.ids) {
			continue
		}
		available := make(map[string]struct{}, len(candidate.ids))
		for _, id := range candidate.ids {
			available[id] = struct{}{}
		}
		covered := true
		for id := range wanted {
			if _, ok := available[id]; !ok {
				covered = false
				break
			}
		}
		if covered {
			selected = candidate
			matches++
		}
	}
	if matches != 1 {
		return sekaipediaSingerSet{}, false
	}
	selected.kind = "alternate"
	selected.ids = append([]string(nil), selected.ids...)
	return selected, true
}

func sekaipediaDistinctSingerSets(input []sekaipediaSingerSet) []sekaipediaSingerSet {
	result := make([]sekaipediaSingerSet, 0, len(input))
	seen := map[string]struct{}{}
	for _, set := range input {
		key := strings.Join(set.ids, ",")
		if key == "" {
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		set.kind = "alternate"
		set.ids = append([]string(nil), set.ids...)
		result = append(result, set)
	}
	return result
}

func sekaipediaAllSingerSet() sekaipediaSingerSet {
	ids := make([]string, 0, len(sekaipediaSingers))
	for _, singer := range sekaipediaSingers {
		ids = append(ids, singer.id)
	}
	return sekaipediaSingerSet{kind: "alternate", ids: ids}
}

func mapKeysOrdered(values map[string]struct{}) []string {
	ids := make([]string, 0, len(values))
	for _, singer := range sekaipediaSingers {
		if _, ok := values[singer.id]; ok {
			ids = append(ids, singer.id)
		}
	}
	return ids
}

func sekaipediaPrimaryNestedRenditionLabels(records []sekaipediaVersionRecord) map[string]struct{} {
	result := map[string]struct{}{}
	for _, record := range records {
		switch record.kind {
		case "sekai":
			result["sekai"] = struct{}{}
		case "vocaloid":
			result["virtual singer"] = struct{}{}
		}
	}
	return result
}

func sekaipediaNestedLabelIsPrimary(label string, primary map[string]struct{}) bool {
	_, ok := primary[strings.ToLower(strings.Join(strings.Fields(label), " "))]
	return ok
}

func sekaipediaIsOptionalAlternateTopLevelTab(label string, tabs map[string]string) bool {
	label = strings.TrimSpace(label)
	if label == "" || label == "Full Version" || label == "APPEND/Full Version" || label == "Game Version" ||
		label == "SEKAI" || label == "VIRTUAL SINGER" || label == "Alternate Vocal" || label == "Another Vocal" ||
		label == "Alternate Vocal (Full)" || label == "Another Vocal (Full)" {
		return false
	}
	body := strings.TrimSpace(tabs[label])
	return body != "" && (strings.Contains(body, "Lyrics head") || strings.Contains(body, "#tag:tabber"))
}

func sekaipediaOptionalAlternateTabIdentity(label string) (string, bool) {
	label = strings.TrimSpace(label)
	const fullSuffix = " (Full)"
	if len(label) > len(fullSuffix) && strings.EqualFold(label[len(label)-len(fullSuffix):], fullSuffix) {
		return strings.TrimSpace(label[:len(label)-len(fullSuffix)]), true
	}
	return label, false
}

func sekaipediaAlternateSingerLabelFromTab(label string) string {
	label = strings.TrimSpace(label)
	start := strings.LastIndex(label, "(")
	end := strings.LastIndex(label, ")")
	if start < 0 || end <= start {
		return ""
	}
	return strings.TrimSpace(label[start+1 : end])
}

func sekaipediaAlternateAggregateSingerSet(label string, sets []sekaipediaSingerSet) (sekaipediaSingerSet, string, bool) {
	label = strings.TrimSpace(label)
	aggregate := ""
	switch normalizeSekaipediaSingerAlias(label) {
	case "leoneed", "ln":
		aggregate = "L/n"
	case "moremorejump", "mmj":
		aggregate = "MMJ"
	case "vividbadsquad", "vbs":
		aggregate = "VBS"
	case "wonderlandsshowtime", "wxs":
		aggregate = "WxS"
	case "25jinightcordde", "nightcordat2500", "niigo", "25ji":
		aggregate = "25ji"
	case "maleocs":
		aggregate = "Male OCs"
	case "unitleaders":
		aggregate = "Unit leaders"
	default:
		return sekaipediaSingerSet{}, "", false
	}

	matches := map[string][]string{}
	for _, candidate := range sets {
		ids, err := resolveSekaipediaSingerList(aggregate, candidate, true)
		if err != nil || len(ids) == 0 {
			continue
		}
		key := strings.Join(ids, ",")
		matches[key] = append([]string(nil), ids...)
	}
	if len(matches) != 1 {
		return sekaipediaSingerSet{}, "", false
	}
	for _, ids := range matches {
		return sekaipediaSingerSet{kind: "alternate", ids: ids}, label, true
	}
	return sekaipediaSingerSet{}, "", false
}

func sekaipediaAlternateSingerSet(label string, sets []sekaipediaSingerSet) (sekaipediaSingerSet, string, bool) {
	label = strings.TrimSpace(label)
	if label == "" {
		if len(sets) != 1 {
			return sekaipediaSingerSet{}, "", false
		}
		return sets[0], strings.Join(sets[0].ids, ", "), true
	}
	ids, err := resolveSekaipediaSingerList(label, sekaipediaSingerSet{}, false)
	if err != nil {
		return sekaipediaSingerSet{}, "", false
	}
	used := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		used[id] = struct{}{}
	}
	for _, set := range sets {
		if sameSekaipediaSingerIDs(used, set.ids) {
			return set, label, true
		}
	}
	// Archive and auxiliary tabs are not always declared in the Versions
	// section. The singer label itself is still authoritative when it resolves
	// against the fixed Sekaipedia singer roster.
	return sekaipediaSingerSet{kind: "alternate", ids: ids}, label, true
}
