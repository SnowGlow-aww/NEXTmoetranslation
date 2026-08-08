package lyricssource

import "reflect"

type sekaipediaFullGameSelection struct {
	full       sekaipediaRenditionExtraction
	game       sekaipediaRenditionExtraction
	projection []int
}

func sekaipediaSingerSetCoversUsedIDs(set sekaipediaSingerSet, usedIDs map[string]struct{}) bool {
	allowed := make(map[string]struct{}, len(set.ids))
	for _, id := range set.ids {
		allowed[id] = struct{}{}
	}
	for id := range usedIDs {
		if _, exists := allowed[id]; !exists {
			return false
		}
	}
	return true
}

func parseSekaipediaRenditionAgainstSets(
	body, kind string,
	sets []sekaipediaSingerSet,
	requireFullSet bool,
) (sekaipediaRenditionExtraction, error) {
	matches := make([]sekaipediaRenditionExtraction, 0, len(sets))
	var firstErr error
	for _, set := range sets {
		parsed, err := parseSekaipediaRenditionWithSet(body, kind, set, requireFullSet)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		matches = append(matches, parsed)
	}
	if len(matches) == 0 {
		if firstErr != nil {
			return sekaipediaRenditionExtraction{}, firstErr
		}
		return sekaipediaRenditionExtraction{}, ErrMissingLyrics
	}
	coveredIndex := -1
	coveredCount := 0
	for index, match := range matches {
		if sekaipediaSingerSetCoversUsedIDs(match.set, match.usedIDs) {
			coveredIndex = index
			coveredCount++
		}
	}
	if coveredCount == 1 {
		return matches[coveredIndex], nil
	}
	selected := matches[0]
	for _, match := range matches[1:] {
		if !equivalentSekaipediaRendition(selected, match) {
			return sekaipediaRenditionExtraction{}, ErrAmbiguous
		}
	}
	return selected, nil
}

func parseSekaipediaFullGameAgainstSets(
	fullBody, gameBody, kind string,
	sets []sekaipediaSingerSet,
) (sekaipediaFullGameSelection, error) {
	matches := make([]sekaipediaFullGameSelection, 0, len(sets))
	var firstErr error
	for _, set := range sets {
		full, err := parseSekaipediaRenditionWithSet(fullBody, kind, set, true)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		game, err := parseSekaipediaRenditionWithSet(gameBody, kind, set, false)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		projection, err := sekaipediaOrderedSubsequence(full.projectionLines, game.projectionLines)
		if err != nil {
			projection, err = sekaipediaUniqueTextSubsequence(full.projectionLines, game.projectionLines)
		}
		if err != nil {
			projection, err = sekaipediaSemanticSubsequence(full.projectionLines, game.projectionLines)
		}
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		matches = append(matches, sekaipediaFullGameSelection{
			full:       full,
			game:       game,
			projection: projection,
		})
	}
	if len(matches) == 0 {
		if firstErr != nil {
			return sekaipediaFullGameSelection{}, firstErr
		}
		return sekaipediaFullGameSelection{}, ErrMissingLyrics
	}
	coveredIndex := -1
	coveredCount := 0
	for index, match := range matches {
		if sekaipediaSingerSetCoversUsedIDs(match.full.set, match.full.usedIDs) &&
			sekaipediaSingerSetCoversUsedIDs(match.game.set, match.game.usedIDs) {
			coveredIndex = index
			coveredCount++
		}
	}
	if coveredCount == 1 {
		return matches[coveredIndex], nil
	}
	selected := matches[0]
	for _, match := range matches[1:] {
		if !equivalentSekaipediaRendition(selected.full, match.full) ||
			!equivalentSekaipediaRendition(selected.game, match.game) ||
			!reflect.DeepEqual(selected.projection, match.projection) {
			return sekaipediaFullGameSelection{}, ErrAmbiguous
		}
	}
	return selected, nil
}

func equivalentSekaipediaRendition(left, right sekaipediaRenditionExtraction) bool {
	left.set = sekaipediaSingerSet{}
	right.set = sekaipediaSingerSet{}
	return reflect.DeepEqual(left, right)
}
