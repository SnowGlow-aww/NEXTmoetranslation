// Package lyricsprovideroutcome defines the bounded, content-free provider
// result envelope used by lyrics source discovery. It intentionally knows
// nothing about lyric text or provider page metadata.
package lyricsprovideroutcome

import (
	"errors"
	"regexp"
	"sort"
	"strings"

	"moesekai/server/internal/model"
)

const (
	MaxDiagnosticCount = 1_000_000
	MaxAcquisitionRefs = 64
)

var (
	canonicalEvidenceSHA256         = regexp.MustCompile(`^[0-9a-f]{64}$`)
	fandomAcquisitionID             = regexp.MustCompile(`^search:vocaloid-fandom:[0-9a-f]{64}$`)
	moegirlAcquisitionID            = regexp.MustCompile(`^search:moegirl:[1-9][0-9]{0,18}:[0-9a-f]{64}$`)
	moegirlPublicExactAcquisitionID = regexp.MustCompile(`^public:moegirl-public-exact:[0-9a-f]{64}$`)
	sekaipediaAcquisitionID         = regexp.MustCompile(
		`^(authority:sekaipedia:[a-z0-9](?:[a-z0-9-]{0,127}):[1-9][0-9]{0,18}|revision:sekaipedia:[1-9][0-9]{0,18}:[1-9][0-9]{0,18}):[0-9a-f]{64}$`,
	)
)

// Status is closed so provider failures cannot be collapsed into an
// unstructured error string or silently overwrite another provider's result.
type Status string

const (
	StatusCandidate      Status = "candidate"
	StatusNoMatch        Status = "no_match"
	StatusUnsupported    Status = "unsupported"
	StatusStale          Status = "stale"
	StatusTransportError Status = "transport_error"
	StatusAmbiguous      Status = "ambiguous"
)

// Phase identifies the bounded provider operation that determined an outcome.
type Phase string

const (
	PhaseAcquireAuthority Phase = "acquire_authority"
	PhaseResolveTargets   Phase = "resolve_targets"
	PhaseAcquireTarget    Phase = "acquire_target"
	PhaseMatchIdentity    Phase = "match_identity"
	PhaseParseLyrics      Phase = "parse_lyrics"
	PhaseFinalize         Phase = "finalize"
)

// ReasonCode is a closed, content-free explanation for an outcome.
type ReasonCode string

const (
	ReasonCandidate                ReasonCode = "candidate"
	ReasonNoMatch                  ReasonCode = "no_match"
	ReasonNoSearchHits             ReasonCode = "no_search_hits"
	ReasonIdentityMismatch         ReasonCode = "identity_mismatch"
	ReasonMissingSongSignal        ReasonCode = "missing_song_signal"
	ReasonMissingLyrics            ReasonCode = "missing_lyrics"
	ReasonUnsupportedFormat        ReasonCode = "unsupported_format"
	ReasonMalformedResponse        ReasonCode = "malformed_response"
	ReasonLyricsTooLarge           ReasonCode = "lyrics_too_large"
	ReasonCatalogRenditionConflict ReasonCode = "catalog_rendition_conflict"
	ReasonRestrictedReprint        ReasonCode = "restricted_reprint"
	ReasonRevisionChanged          ReasonCode = "revision_changed"
	ReasonTransport                ReasonCode = "transport"
	ReasonCanceled                 ReasonCode = "canceled"
	ReasonDeadlineExceeded         ReasonCode = "deadline_exceeded"
	ReasonAmbiguousMatch           ReasonCode = "ambiguous_match"
	ReasonCandidateConflict        ReasonCode = "candidate_conflict"
	ReasonMultipleCandidates       ReasonCode = "multiple_candidates"
)

// Counts contains only bounded aggregate values. It cannot carry titles,
// parser strings, URLs, source text, or any other provider content.
type Counts struct {
	Acquisitions    int `json:"acquisitions"`
	Targets         int `json:"targets"`
	Evaluated       int `json:"evaluated"`
	Candidates      int `json:"candidates"`
	NoMatch         int `json:"noMatch"`
	Unsupported     int `json:"unsupported"`
	Stale           int `json:"stale"`
	TransportErrors int `json:"transportErrors"`
	Ambiguous       int `json:"ambiguous"`
}

// Diagnostic is the complete diagnostic surface. AcquisitionRefs point only
// to separately bounded immutable evidence and never embed those bytes.
type Diagnostic struct {
	Provider        model.LyricsSourceProvider           `json:"provider"`
	Phase           Phase                                `json:"phase"`
	ReasonCode      ReasonCode                           `json:"reasonCode"`
	Counts          Counts                               `json:"counts"`
	AcquisitionRefs []model.LyricsSourceIndexEvidenceRef `json:"acquisitionRefs"`
}

// Outcome retains exactly one provider's accepted candidate or its closed
// provider-local failure. A non-candidate outcome never exposes provisional
// candidates that failed closed.
type Outcome[T any] struct {
	Provider   model.LyricsSourceProvider `json:"provider"`
	Status     Status                     `json:"status"`
	Candidates []T                        `json:"candidates"`
	Diagnostic Diagnostic                 `json:"diagnostic"`
}

// New validates and defensively copies one provider-local outcome.
func New[T any](
	provider model.LyricsSourceProvider,
	status Status,
	candidates []T,
	diagnostic Diagnostic,
) (Outcome[T], error) {
	outcome := Outcome[T]{
		Provider: provider, Status: status,
		Candidates: append([]T(nil), candidates...), Diagnostic: diagnostic,
	}
	refs, err := canonicalRefs(provider, diagnostic.AcquisitionRefs)
	if err != nil {
		return Outcome[T]{}, err
	}
	outcome.Diagnostic.AcquisitionRefs = refs
	if err := outcome.Validate(); err != nil {
		return Outcome[T]{}, err
	}
	return outcome, nil
}

// Retryable is derived from the closed status rather than stored as another
// mutable diagnostic field.
func (outcome Outcome[T]) Retryable() bool {
	return outcome.Status == StatusTransportError
}

func (outcome Outcome[T]) Validate() error {
	if !model.IsValidLyricsSourceProvider(outcome.Provider) || outcome.Diagnostic.Provider != outcome.Provider ||
		!validStatus(outcome.Status) || !validPhase(outcome.Diagnostic.Phase) ||
		!reasonAllowed(outcome.Status, outcome.Diagnostic.ReasonCode) || !validCounts(outcome.Diagnostic.Counts) {
		return errors.New("provider outcome is invalid")
	}
	if outcome.Status == StatusCandidate {
		if len(outcome.Candidates) != 1 || outcome.Diagnostic.Counts.Candidates != 1 {
			return errors.New("candidate provider outcome is invalid")
		}
	} else if len(outcome.Candidates) != 0 {
		return errors.New("non-candidate provider outcome is invalid")
	}
	if !countsSupportStatus(outcome.Status, outcome.Diagnostic.Counts) {
		return errors.New("provider outcome counts are invalid")
	}
	refs, err := canonicalRefs(outcome.Provider, outcome.Diagnostic.AcquisitionRefs)
	if err != nil || !equalRefs(refs, outcome.Diagnostic.AcquisitionRefs) {
		return errors.New("provider outcome acquisition references are invalid")
	}
	return nil
}

func validStatus(status Status) bool {
	switch status {
	case StatusCandidate, StatusNoMatch, StatusUnsupported, StatusStale, StatusTransportError, StatusAmbiguous:
		return true
	default:
		return false
	}
}

func validPhase(phase Phase) bool {
	switch phase {
	case PhaseAcquireAuthority, PhaseResolveTargets, PhaseAcquireTarget, PhaseMatchIdentity, PhaseParseLyrics, PhaseFinalize:
		return true
	default:
		return false
	}
}

func reasonAllowed(status Status, reason ReasonCode) bool {
	switch status {
	case StatusCandidate:
		return reason == ReasonCandidate
	case StatusNoMatch:
		return reason == ReasonNoMatch || reason == ReasonNoSearchHits || reason == ReasonIdentityMismatch ||
			reason == ReasonMissingSongSignal || reason == ReasonMissingLyrics
	case StatusUnsupported:
		return reason == ReasonUnsupportedFormat || reason == ReasonMalformedResponse || reason == ReasonLyricsTooLarge ||
			reason == ReasonCatalogRenditionConflict || reason == ReasonRestrictedReprint
	case StatusStale:
		return reason == ReasonRevisionChanged
	case StatusTransportError:
		return reason == ReasonTransport || reason == ReasonCanceled || reason == ReasonDeadlineExceeded
	case StatusAmbiguous:
		return reason == ReasonAmbiguousMatch || reason == ReasonCandidateConflict || reason == ReasonMultipleCandidates
	default:
		return false
	}
}

func validCounts(counts Counts) bool {
	values := []int{
		counts.Acquisitions, counts.Targets, counts.Evaluated, counts.Candidates, counts.NoMatch,
		counts.Unsupported, counts.Stale, counts.TransportErrors, counts.Ambiguous,
	}
	for _, value := range values {
		if value < 0 || value > MaxDiagnosticCount {
			return false
		}
	}
	return true
}

func countsSupportStatus(status Status, counts Counts) bool {
	switch status {
	case StatusCandidate:
		return counts.Candidates == 1
	case StatusNoMatch:
		return counts.Candidates == 0 && counts.NoMatch > 0
	case StatusUnsupported:
		return counts.Unsupported > 0
	case StatusStale:
		return counts.Stale > 0
	case StatusTransportError:
		return counts.TransportErrors > 0
	case StatusAmbiguous:
		return counts.Ambiguous > 0
	default:
		return false
	}
}

func canonicalRefs(
	provider model.LyricsSourceProvider,
	input []model.LyricsSourceIndexEvidenceRef,
) ([]model.LyricsSourceIndexEvidenceRef, error) {
	if len(input) > MaxAcquisitionRefs {
		return nil, errors.New("provider outcome acquisition references are invalid")
	}
	refs := append([]model.LyricsSourceIndexEvidenceRef(nil), input...)
	sort.Slice(refs, func(left, right int) bool {
		if refs[left].EvidenceID != refs[right].EvidenceID {
			return refs[left].EvidenceID < refs[right].EvidenceID
		}
		return refs[left].SHA256 < refs[right].SHA256
	})
	result := refs[:0]
	for _, reference := range refs {
		if !canonicalAcquisitionID(provider, reference.EvidenceID) ||
			!canonicalEvidenceSHA256.MatchString(reference.SHA256) {
			return nil, errors.New("provider outcome acquisition references are invalid")
		}
		if len(result) > 0 && result[len(result)-1].EvidenceID == reference.EvidenceID {
			if result[len(result)-1].SHA256 != reference.SHA256 {
				return nil, errors.New("provider outcome acquisition references are invalid")
			}
			continue
		}
		result = append(result, reference)
	}
	return append([]model.LyricsSourceIndexEvidenceRef(nil), result...), nil
}

func canonicalAcquisitionID(provider model.LyricsSourceProvider, evidenceID string) bool {
	switch provider {
	case model.LyricsSourceProviderVocaloidFandom:
		return fandomAcquisitionID.MatchString(evidenceID)
	case model.LyricsSourceProviderMoegirl:
		return moegirlAcquisitionID.MatchString(evidenceID)
	case model.LyricsSourceProviderMoegirlPublicExact:
		return moegirlPublicExactAcquisitionID.MatchString(evidenceID)
	case model.LyricsSourceProviderSekaipedia:
		if !sekaipediaAcquisitionID.MatchString(evidenceID) {
			return false
		}
		if strings.HasPrefix(evidenceID, "authority:sekaipedia:") {
			key := strings.TrimPrefix(evidenceID, "authority:sekaipedia:")
			key = key[:strings.IndexByte(key, ':')]
			return strings.Contains(key, "song") || strings.Contains(key, "index")
		}
		return true
	default:
		return false
	}
}

func equalRefs(left, right []model.LyricsSourceIndexEvidenceRef) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
