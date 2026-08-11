package lyricssource

import (
	"errors"

	"moesekai/server/internal/model"
)

// ValidateCandidatesIndexEvidence validates each candidate independently and
// rejects an evidence ID that resolves to conflicting immutable envelopes
// anywhere in the same private transport artifact. Identical evidence may be
// shared by multiple candidates, such as candidates discovered from one fixed
// Moegirl index revision.
func ValidateCandidatesIndexEvidence(candidates []Candidate) error {
	byID := make(map[string]IndexEvidence)
	for _, candidate := range candidates {
		if err := ValidateCandidateIndexEvidence(candidate); err != nil {
			return err
		}
		for _, evidence := range candidate.IndexEvidence {
			if existing, found := byID[evidence.EvidenceID]; found && !indexEvidenceEqual(existing, evidence) {
				return errors.New("candidate index evidence ID has conflicting resolutions")
			}
			byID[evidence.EvidenceID] = evidence
		}
	}
	return nil
}

// CandidateIndexEvidenceResolver validates exact evidence envelopes once, then
// resolves any number of compact candidates without rehashing or reparsing the
// same immutable raw bytes per candidate. It is safe for concurrent readers.
type candidateIndexEvidenceIdentity struct {
	pageID     int
	revisionID int
	sha1       string
	title      string
	categories []string
}

type CandidateIndexEvidenceResolver struct {
	byID                           map[string]IndexEvidence
	searchIdentities               map[string][]candidateIndexEvidenceIdentity
	sekaipediaRawSHA256s           map[string]string
	sekaipediaAuthority            FixedIndex
	sekaipediaAuthorityEvidenceIDs map[string]struct{}
}

// NewCandidateIndexEvidenceResolver constructs an immutable exact-evidence
// index. Every raw envelope is validated once during construction.
func NewCandidateIndexEvidenceResolver(evidence []IndexEvidence) (*CandidateIndexEvidenceResolver, error) {
	return newCandidateIndexEvidenceResolver(evidence, true)
}

// ValidateCandidatesAgainstIndexEvidence validates one borrowed evidence set
// and any number of compact candidates without retaining or cloning raw bytes.
// The borrowed resolver never escapes this call.
func ValidateCandidatesAgainstIndexEvidence(evidence []IndexEvidence, candidates []Candidate) error {
	resolver, err := newCandidateIndexEvidenceResolver(evidence, false)
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		if err := resolver.ValidateCandidate(candidate); err != nil {
			return err
		}
	}
	return nil
}

func newCandidateIndexEvidenceResolver(evidence []IndexEvidence, cloneEvidence bool) (*CandidateIndexEvidenceResolver, error) {
	if evidence == nil {
		return nil, errors.New("candidate index evidence resolver is incomplete")
	}

	// Validate every envelope and reject duplicate identities before optionally
	// cloning raw bytes. Callers that already enforced their receipt capacities
	// therefore cannot retain a large partial raw index when a later envelope is
	// malformed.
	bindings := make([]validatedIndexEvidenceBinding, len(evidence))
	seen := make(map[string]struct{}, len(evidence))
	for index, item := range evidence {
		binding, err := validateIndexEvidenceWithBinding(item)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[item.EvidenceID]; duplicate {
			return nil, errors.New("candidate index evidence resolves more than once")
		}
		seen[item.EvidenceID] = struct{}{}
		bindings[index] = binding
	}

	resolver := &CandidateIndexEvidenceResolver{
		byID:                           make(map[string]IndexEvidence, len(evidence)),
		searchIdentities:               make(map[string][]candidateIndexEvidenceIdentity),
		sekaipediaRawSHA256s:           make(map[string]string),
		sekaipediaAuthorityEvidenceIDs: make(map[string]struct{}),
	}
	for index, item := range evidence {
		binding := bindings[index]
		if binding.sekaipediaAuthority != nil {
			if resolver.sekaipediaAuthority.PageID != 0 && resolver.sekaipediaAuthority != *binding.sekaipediaAuthority {
				return nil, errors.New("candidate index evidence contains conflicting Sekaipedia authorities")
			}
			resolver.sekaipediaAuthority = *binding.sekaipediaAuthority
			resolver.sekaipediaAuthorityEvidenceIDs[item.EvidenceID] = struct{}{}
		}
		if cloneEvidence {
			item = cloneCandidateIndexEvidence(item)
		}
		resolver.byID[item.EvidenceID] = item
		if binding.searchIdentities != nil {
			resolver.searchIdentities[item.EvidenceID] = binding.searchIdentities
		}
		if binding.sekaipediaRawSHA256 != "" {
			resolver.sekaipediaRawSHA256s[item.EvidenceID] = binding.sekaipediaRawSHA256
		}
	}
	return resolver, nil
}

// ResolveCandidate binds one compact candidate to defensively cloned evidence
// without repeating receipt-wide or raw-envelope validation.
func (resolver *CandidateIndexEvidenceResolver) ResolveCandidate(candidate Candidate) (Candidate, error) {
	resolved, err := resolver.ResolveCandidates([]Candidate{candidate})
	if err != nil {
		return Candidate{}, err
	}
	return resolved[0], nil
}

// ResolveCandidates validates a candidate batch before cloning raw bytes, then
// clones each canonical evidence envelope once for the whole batch. Candidates
// that reference the same immutable acquisition share that batch-local clone;
// no returned evidence aliases the resolver's retained index.
func (resolver *CandidateIndexEvidenceResolver) ResolveCandidates(candidates []Candidate) ([]Candidate, error) {
	resolved := make([]Candidate, len(candidates))
	for index, candidate := range candidates {
		validated, err := resolver.resolveCandidate(candidate)
		if err != nil {
			return nil, err
		}
		resolved[index] = validated
	}

	clonedByID := make(map[string]IndexEvidence)
	for candidateIndex, candidate := range candidates {
		resolved[candidateIndex].IndexEvidence = make([]IndexEvidence, len(candidate.IndexEvidenceRefs))
		for evidenceIndex, reference := range candidate.IndexEvidenceRefs {
			cloned, found := clonedByID[reference.EvidenceID]
			if !found {
				cloned = cloneCandidateIndexEvidence(resolver.byID[reference.EvidenceID])
				clonedByID[reference.EvidenceID] = cloned
			}
			resolved[candidateIndex].IndexEvidence[evidenceIndex] = cloned
		}
	}
	return resolved, nil
}

// ValidateCandidate binds one compact candidate to the immutable resolver index
// without cloning or retaining its raw evidence. Receipt-wide callers can use
// this path to validate large candidate sets with memory proportional only to
// their compact references.
func (resolver *CandidateIndexEvidenceResolver) ValidateCandidate(candidate Candidate) error {
	_, err := resolver.resolveCandidate(candidate)
	return err
}

// ValidateResolvedCandidate proves that concrete candidate evidence is exactly
// the already-validated resolver content without rehashing, reparsing, or
// cloning any raw envelope. It is intended for fixed fetch results that must
// retain the preflight evidence unchanged.
func (resolver *CandidateIndexEvidenceResolver) ValidateResolvedCandidate(candidate Candidate) error {
	if resolver == nil || resolver.byID == nil || len(candidate.IndexEvidence) != len(candidate.IndexEvidenceRefs) {
		return errors.New("candidate index evidence is incomplete")
	}
	compact := candidate
	compact.IndexEvidence = nil
	if _, err := resolver.resolveCandidate(compact); err != nil {
		return err
	}
	resolvedByID := make(map[string]IndexEvidence, len(candidate.IndexEvidence))
	for _, evidence := range candidate.IndexEvidence {
		if _, duplicate := resolvedByID[evidence.EvidenceID]; duplicate {
			return errors.New("candidate index evidence ID resolves more than once")
		}
		canonical, found := resolver.byID[evidence.EvidenceID]
		if !found || !indexEvidenceEqual(canonical, evidence) {
			return errors.New("candidate index evidence drifted from its validated resolution")
		}
		resolvedByID[evidence.EvidenceID] = evidence
	}
	for _, reference := range candidate.IndexEvidenceRefs {
		if _, found := resolvedByID[reference.EvidenceID]; !found {
			return errors.New("candidate index evidence reference digest does not resolve")
		}
	}
	return nil
}

func (resolver *CandidateIndexEvidenceResolver) resolveCandidate(candidate Candidate) (Candidate, error) {
	if resolver == nil || resolver.byID == nil || candidate.IndexEvidence != nil ||
		!model.IsValidLyricsSourceProvider(candidate.Provider) || len(candidate.IndexEvidenceRefs) == 0 ||
		len(candidate.IndexEvidenceRefs) > 64 {
		return Candidate{}, errors.New("candidate index evidence is incomplete")
	}
	wantOrigin := OriginVocaloidFandom
	switch candidate.Provider {
	case ProviderMoegirl:
		wantOrigin = OriginMoegirl
	case ProviderSekaipedia:
		wantOrigin = OriginSekaipedia
	}
	if candidate.Origin != wantOrigin {
		return Candidate{}, errors.New("candidate index evidence provider origin is invalid")
	}

	resolved := candidate
	seenRefs := make(map[string]struct{}, len(candidate.IndexEvidenceRefs))
	byID := make(map[string]IndexEvidence, len(candidate.IndexEvidenceRefs))
	for _, reference := range candidate.IndexEvidenceRefs {
		if !canonicalIndexEvidenceID.MatchString(reference.EvidenceID) ||
			!canonicalIndexEvidenceSHA256.MatchString(reference.SHA256) {
			return Candidate{}, errors.New("candidate index evidence reference is invalid")
		}
		if _, duplicate := seenRefs[reference.EvidenceID]; duplicate {
			return Candidate{}, errors.New("candidate index evidence reference is duplicated")
		}
		seenRefs[reference.EvidenceID] = struct{}{}
		item, found := resolver.byID[reference.EvidenceID]
		if !found || item.SHA256 != reference.SHA256 || item.RawSHA256 != reference.SHA256 {
			return Candidate{}, errors.New("candidate index evidence reference digest does not resolve")
		}
		if item.Provider != candidate.Provider || item.Origin != candidate.Origin {
			return Candidate{}, errors.New("candidate index evidence provider does not match candidate")
		}
		if item.Kind == IndexEvidenceKindMediaWikiSearchResponse &&
			!searchIdentityContainsCandidate(resolver.searchIdentities[item.EvidenceID], candidate) {
			return Candidate{}, errors.New("candidate is not present in its search response evidence")
		}
		byID[item.EvidenceID] = item
	}
	if candidate.Provider == ProviderSekaipedia {
		if err := validateSekaipediaCandidateValidatedEvidence(
			candidate,
			byID,
			resolver.sekaipediaRawSHA256s,
			resolver.sekaipediaAuthority,
			resolver.sekaipediaAuthorityEvidenceIDs,
		); err != nil {
			return Candidate{}, err
		}
	}
	return resolved, nil
}

func cloneCandidateIndexEvidence(evidence IndexEvidence) IndexEvidence {
	evidence.Categories = append([]string(nil), evidence.Categories...)
	evidence.Raw = append([]byte(nil), evidence.Raw...)
	return evidence
}
