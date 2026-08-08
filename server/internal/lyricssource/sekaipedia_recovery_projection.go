package lyricssource

import (
	"errors"
	"fmt"
	"strings"

	"moesekai/server/internal/lyricsreview"
	"moesekai/server/internal/model"
)

// SekaipediaRecoveryProjection is the independently reparsed, Japanese-only
// projection of one exact Sekaipedia revision evidence envelope. It contains
// no translation or romanization columns.
type SekaipediaRecoveryProjection struct {
	Section                 string
	RenditionKey            string
	ReasonCode              model.LyricsSourceVersionReasonCode
	Full                    model.LyricsSourceFull
	Game                    *model.LyricsSourceFull
	GameProjection          *model.LyricsSourceGameProjection
	AlternateVocals         []model.LyricsSourceAlternateVocal
	FixedJapaneseWikitext   []byte
	AuthoritativeStructured bool
}

// RecoverSekaipediaProjection reparses one exact MediaWiki revision envelope
// against its immutable semantic tuple. This is the recovery-to-import trust
// boundary: callers can compare the returned model directly with a SongResult
// without trusting a reconstructed digest in that result.
func RecoverSekaipediaProjection(
	raw []byte,
	expected FixedIndex,
	policy PerformerSegmentationPolicy,
) (SekaipediaRecoveryProjection, error) {
	return recoverSekaipediaProjection(raw, expected, policy, 0, nil)
}

// RecoverSekaipediaProjectionWithReview applies the same content-free manual
// review resolver used by exact replay before the reparsed projection crosses
// into the recovery-import boundary.
func RecoverSekaipediaProjectionWithReview(
	raw []byte,
	expected FixedIndex,
	policy PerformerSegmentationPolicy,
	musicID int,
	resolver *lyricsreview.Resolver,
) (SekaipediaRecoveryProjection, error) {
	return recoverSekaipediaProjection(raw, expected, policy, musicID, resolver)
}

func recoverSekaipediaProjection(
	raw []byte,
	expected FixedIndex,
	policy PerformerSegmentationPolicy,
	musicID int,
	resolver *lyricsreview.Resolver,
) (SekaipediaRecoveryProjection, error) {
	if err := VerifySekaipediaRevisionContent(raw, expected); err != nil {
		return SekaipediaRecoveryProjection{}, err
	}
	page, err := parsePageResponse(raw)
	if err != nil || page.title != expected.Title {
		return SekaipediaRecoveryProjection{}, ErrRevisionChanged
	}
	parsed, err := parseSekaipediaSong(page.content, policy)
	if err != nil {
		return SekaipediaRecoveryProjection{}, err
	}
	parsedFull := extractionToModelFull(parsed.Full)
	fixed := sekaipediaFixedJapaneseWikitext(parsed.Full.Lines)
	if len(fixed) == 0 {
		return SekaipediaRecoveryProjection{}, ErrMalformedResponse
	}
	var full model.LyricsSourceFull
	if !strings.HasPrefix(parsed.RenditionKey, "game-") {
		full = parsedFull
	}
	var game *model.LyricsSourceFull
	if parsed.Game != nil {
		gameValue := extractionToModelFull(*parsed.Game)
		for index := range gameValue.Lines {
			gameValue.Lines[index].ID = fmt.Sprintf("game-%06d", index+1)
		}
		game = &gameValue
	}
	var gameProjection *model.LyricsSourceGameProjection
	if len(parsed.GameLineIndexes) != 0 {
		lineIDs := make([]string, len(parsed.GameLineIndexes))
		last := -1
		for index, position := range parsed.GameLineIndexes {
			if position < 0 || position >= len(full.Lines) || position <= last {
				return SekaipediaRecoveryProjection{}, errors.New("Sekaipedia Game projection is invalid")
			}
			lineIDs[index] = full.Lines[position].ID
			last = position
		}
		gameProjection = &model.LyricsSourceGameProjection{LineIDs: lineIDs}
	}
	alternates := recoveryAlternateVocals(parsed.AlternateVocals)
	projection := SekaipediaRecoveryProjection{
		Section: parsed.Section, RenditionKey: parsed.RenditionKey, ReasonCode: parsed.ReasonCode,
		Full: full, Game: game, GameProjection: gameProjection, AlternateVocals: alternates,
		FixedJapaneseWikitext: fixed, AuthoritativeStructured: parsed.AuthoritativeStructured,
	}
	if resolver != nil {
		observation := lyricsreview.ProjectionObservation{
			RevisionObservation: lyricsreview.RevisionObservation{
				Provider:       ProviderSekaipedia,
				PageID:         expected.PageID,
				RevisionID:     expected.RevisionID,
				Title:          expected.Title,
				SHA1:           expected.SHA1,
				ContentSHA256:  expected.ContentSHA256,
				ResponseSHA256: expected.RawSHA256,
			},
			HasFull:           len(full.Lines) != 0,
			HasGame:           game != nil,
			HasGameProjection: gameProjection != nil,
		}
		for _, alternate := range alternates {
			kind := ""
			if alternate.Full != nil {
				kind = alternate.Full.Version.Kind
			} else if alternate.Game != nil {
				kind = alternate.Game.Version.Kind
			}
			if kind == "another" || strings.Contains(strings.ToLower(alternate.TabLabel), "another") {
				observation.AnotherCount++
			} else {
				observation.AlternateCount++
			}
		}
		if err := resolver.ValidateProjection(musicID, observation); err != nil {
			return SekaipediaRecoveryProjection{}, err
		}
	}
	return projection, nil
}

func recoveryAlternateVocals(input []sekaipediaAlternateVocalExtraction) []model.LyricsSourceAlternateVocal {
	if input == nil {
		return nil
	}
	result := make([]model.LyricsSourceAlternateVocal, 0, len(input))
	for _, alternate := range input {
		converted := model.LyricsSourceAlternateVocal{
			TabLabel: alternate.TabLabel, SingerLabel: alternate.SingerLabel,
			SingerIDs: append([]string{}, alternate.SingerIDs...),
		}
		if alternate.Full != nil {
			converted.Full = cloneRecoveryAlternateFull(extractionToModelFull(*alternate.Full))
		}
		if alternate.Game != nil {
			converted.Game = cloneRecoveryAlternateFull(extractionToModelFull(*alternate.Game))
		}
		if alternate.Full != nil && alternate.Game != nil {
			mapping, err := sekaipediaResolveProjection(alternate.fullProjectionLines, alternate.gameProjectionLines)
			if err == nil && len(mapping) == len(converted.Game.Lines) {
				lineIDs := make([]string, len(mapping))
				valid := true
				for index, position := range mapping {
					if position < 0 || position >= len(converted.Full.Lines) {
						valid = false
						break
					}
					lineIDs[index] = converted.Full.Lines[position].ID
				}
				if valid {
					converted.GameProjection = &model.LyricsSourceGameProjection{LineIDs: lineIDs}
				}
			}
		}
		result = append(result, converted)
	}
	return result
}

func cloneRecoveryAlternateFull(input model.LyricsSourceFull) *model.LyricsSourceFull {
	return model.CloneLyricsSourceFull(&input)
}
