package store

import (
	"moesekai/server/internal/model"
)

const (
	publicLyricsFullVersion = "full"
	publicLyricsGameVersion = "game"
)

type PublicLyricsAvailabilityState string

const (
	PublicLyricsStateComplete          PublicLyricsAvailabilityState = "complete"
	PublicLyricsStateGameOnly          PublicLyricsAvailabilityState = "game_only"
	PublicLyricsStateSatisfiedNoLyrics PublicLyricsAvailabilityState = "satisfied_no_lyrics"
	PublicLyricsStateAmbiguous         PublicLyricsAvailabilityState = "ambiguous"
	PublicLyricsStateMissing           PublicLyricsAvailabilityState = "missing"
	PublicLyricsStateIncomplete        PublicLyricsAvailabilityState = "incomplete"
	PublicLyricsStateFailed            PublicLyricsAvailabilityState = "failed"
)

type PublicLyricsAttribution struct {
	Provider    model.LyricsSourceProvider `json:"provider"`
	Title       string                     `json:"title"`
	RevisionID  int                        `json:"revisionId"`
	RevisionURL string                     `json:"revisionUrl"`
	LicenseName string                     `json:"licenseName"`
	LicenseURL  string                     `json:"licenseUrl"`
}

type PublicLyricsGameProjection struct {
	ReasonCode model.LyricsSourceVersionReasonCode `json:"reasonCode"`
	LineIDs    []string                            `json:"lineIds"`
}

// PublicLyricsLine is additive over the legacy editable line shape. A
// source-backed v2 detail may expose trailingPerformerIds only for exact
// whole-line performer attribution; v1 conversion leaves it absent so the
// historical v1 JSON bytes remain unchanged.
type PublicLyricsLine struct {
	ID                   string               `json:"id"`
	Order                int                  `json:"order"`
	Japanese             string               `json:"japanese"`
	Chinese              string               `json:"zh-CN"`
	English              string               `json:"en-US"`
	StanzaBreakBefore    bool                 `json:"stanzaBreakBefore,omitempty"`
	Segments             []model.LyricSegment `json:"segments"`
	TrailingPerformerIDs []int                `json:"trailingPerformerIds,omitempty"`
}

// PublicLyricsDetailDocument is the closed public read model for both frozen v1
// publications and source-backed v2 publications. Omitempty fields are arranged
// so a v1 document retains its historical JSON field order and bytes, while a
// v2 document emits only the v2 contract surface.
//
// Backup restore canonicalization remains owned by content_backup.go and reuses
// this v2 validator so publication rows cannot bypass the source-backed contract.
type PublicLyricsTranslationCredits struct {
	Translation  string `json:"translation,omitempty"`
	Proofreading string `json:"proofreading,omitempty"`
}

type PublicLyricsDetailDocument struct {
	Version            int                             `json:"version"`
	MusicID            int                             `json:"musicId"`
	Revision           int                             `json:"revision"`
	UpdatedAt          string                          `json:"updatedAt"`
	State              PublicLyricsAvailabilityState   `json:"state,omitempty"`
	Attribution        string                          `json:"attribution,omitempty"`
	Attributions       []PublicLyricsAttribution       `json:"attributions,omitempty"`
	TranslationCredits *PublicLyricsTranslationCredits `json:"translationCredits,omitempty"`
	AvailableVersions  []string                        `json:"availableVersions,omitempty"`
	NoLyricsReason     string                          `json:"noLyricsReason,omitempty"`
	Lines              []PublicLyricsLine              `json:"lines"`
	GameProjection     *PublicLyricsGameProjection     `json:"gameProjection,omitempty"`
}

type PublicLyricsIndexSong struct {
	MusicID           int                           `json:"musicId"`
	Revision          int                           `json:"revision"`
	UpdatedAt         string                        `json:"updatedAt"`
	State             PublicLyricsAvailabilityState `json:"state,omitempty"`
	Title             model.LocalizedTitle          `json:"title"`
	AvailableVersions []string                      `json:"availableVersions,omitempty"`
	NoLyricsReason    string                        `json:"noLyricsReason,omitempty"`
}

type PublicLyricsIndexDocument struct {
	Version int                     `json:"version"`
	Songs   []PublicLyricsIndexSong `json:"songs"`
}

type publicLyricsSourceBundle struct {
	documentID    int64
	documentJSON  string
	documentSHA   string
	document      model.LyricsSourceDocument
	contributions map[string]string
}

type publicLyricsPublishedRecord struct {
	musicID      int
	revision     int
	updatedAt    int64
	payload      string
	title        model.LocalizedTitle
	hasSourceDoc bool
}

func (s *Store) publicLyricsPublication(q queryRower, lyrics model.SongLyrics, performers map[int]bool) (any, error) {
	if !lyricsHasTranslationCredit(lyrics) {
		return nil, &LyricsContractError{Code: "incomplete_publication", Details: []string{
			"translation credit is required for publication",
		}}
	}
	bundle, err := s.loadPublicLyricsSourceBundle(q, lyrics.MusicID)
	if err != nil {
		return nil, &LyricsContractError{Code: "incomplete_publication", Details: []string{err.Error()}}
	}
	code, details, _ := validateLyrics(lyrics, performers, bundle == nil)
	if code != "" {
		if code != "invalid_performer" {
			code = "incomplete_publication"
		}
		return nil, &LyricsContractError{Code: code, Details: details}
	}
	if bundle == nil {
		public := publicLyricsV1(lyrics)
		if err := validatePublicLyricsArtifactSize(public); err != nil {
			return nil, &LyricsContractError{Code: "incomplete_publication", Details: []string{err.Error()}}
		}
		return public, nil
	}
	catalogPerformers, err := loadCatalogPerformerAliases(q)
	if err != nil {
		return nil, &LyricsContractError{Code: "incomplete_publication", Details: []string{err.Error()}}
	}
	public, err := buildPublicLyricsV2(lyrics, bundle, catalogPerformers)
	if err != nil {
		return nil, &LyricsContractError{Code: "incomplete_publication", Details: []string{err.Error()}}
	}
	if err := validatePublicLyricsArtifactSize(public); err != nil {
		return nil, &LyricsContractError{Code: "incomplete_publication", Details: []string{err.Error()}}
	}
	return public, nil
}
