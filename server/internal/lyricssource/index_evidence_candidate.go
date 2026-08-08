package lyricssource

import (
	"errors"

	"strings"
	"time"

	"moesekai/server/internal/model"
)

// ValidateCandidateIndexEvidence proves that every compact reference resolves
// exactly once to bounded immutable bytes and that no unreferenced evidence is
// smuggled through the private candidate transport. Sekaipedia candidates bind
// to the single immutable authority carried by their exact evidence rather than
// to mutable authority data compiled into the binary.
func ValidateCandidateIndexEvidence(candidate Candidate) error {
	return validateCandidateIndexEvidence(candidate, FixedIndex{})
}

// ValidateCandidateIndexEvidenceAgainstSekaipediaAuthority validates a
// Sekaipedia candidate against one exact reviewed authority. It is the recovery
// path used by plan-derived configurations; arbitrary authorities cannot be
// accepted without this immutable binding.
func ValidateCandidateIndexEvidenceAgainstSekaipediaAuthority(candidate Candidate, authority FixedIndex) error {
	if candidate.Provider != ProviderSekaipedia || !validSekaipediaAuthorityBinding(authority) {
		return errors.New("Sekaipedia candidate authority is invalid")
	}
	return validateCandidateIndexEvidence(candidate, authority)
}

func validateCandidateIndexEvidence(candidate Candidate, authority FixedIndex) error {
	if !model.IsValidLyricsSourceProvider(candidate.Provider) || len(candidate.IndexEvidenceRefs) == 0 ||
		len(candidate.IndexEvidenceRefs) > 64 || len(candidate.IndexEvidence) != len(candidate.IndexEvidenceRefs) {
		return errors.New("candidate index evidence is incomplete")
	}
	wantOrigin := OriginVocaloidFandom
	switch candidate.Provider {
	case ProviderMoegirl:
		wantOrigin = OriginMoegirl
	case ProviderSekaipedia:
		wantOrigin = OriginSekaipedia
	}
	if candidate.Origin != wantOrigin {
		return errors.New("candidate index evidence provider origin is invalid")
	}

	byID := make(map[string]IndexEvidence, len(candidate.IndexEvidence))
	derivedAuthority := FixedIndex{}
	for _, evidence := range candidate.IndexEvidence {
		binding, err := validateIndexEvidenceWithBinding(evidence)
		if err != nil {
			return err
		}
		if evidence.Provider != candidate.Provider || evidence.Origin != candidate.Origin {
			return errors.New("candidate index evidence provider does not match candidate")
		}
		if _, duplicate := byID[evidence.EvidenceID]; duplicate {
			return errors.New("candidate index evidence ID resolves more than once")
		}
		if binding.sekaipediaAuthority != nil {
			if derivedAuthority.PageID != 0 && derivedAuthority != *binding.sekaipediaAuthority {
				return errors.New("candidate index evidence contains conflicting Sekaipedia authorities")
			}
			derivedAuthority = *binding.sekaipediaAuthority
		}
		byID[evidence.EvidenceID] = evidence
	}
	if candidate.Provider == ProviderSekaipedia {
		if !validSekaipediaAuthorityBinding(derivedAuthority) {
			return errors.New("Sekaipedia candidate has no valid immutable authority evidence")
		}
		if authority != (FixedIndex{}) && !sameSekaipediaRevisionAuthority(authority, derivedAuthority) {
			return errors.New("Sekaipedia candidate evidence does not match the caller authority")
		}
		// The caller binds the immutable revision-content authority. The exact
		// response envelope remains acquisition evidence and may differ only in
		// page-info fields that cannot decide revision freshness.
		authority = derivedAuthority
	}

	seenRefs := make(map[string]struct{}, len(candidate.IndexEvidenceRefs))
	for _, reference := range candidate.IndexEvidenceRefs {
		if !canonicalIndexEvidenceID.MatchString(reference.EvidenceID) ||
			!canonicalIndexEvidenceSHA256.MatchString(reference.SHA256) {
			return errors.New("candidate index evidence reference is invalid")
		}
		if _, duplicate := seenRefs[reference.EvidenceID]; duplicate {
			return errors.New("candidate index evidence reference is duplicated")
		}
		seenRefs[reference.EvidenceID] = struct{}{}
		evidence, found := byID[reference.EvidenceID]
		if !found || evidence.SHA256 != reference.SHA256 || evidence.RawSHA256 != reference.SHA256 {
			return errors.New("candidate index evidence reference digest does not resolve")
		}
		if evidence.Kind == IndexEvidenceKindMediaWikiSearchResponse {
			pages, err := parseSearchResponse(evidence.Raw)
			if err != nil || !searchEvidenceContainsCandidate(pages, candidate) {
				return errors.New("candidate is not present in its search response evidence")
			}
		}
	}
	if candidate.Provider == ProviderSekaipedia {
		return validateSekaipediaCandidateRevisionEvidence(candidate, byID, authority)
	}
	return nil
}

func validateSekaipediaCandidateRevisionEvidence(
	candidate Candidate,
	byID map[string]IndexEvidence,
	authority FixedIndex,
) error {
	if len(candidate.IndexEvidenceRefs) != 2 || len(byID) != 2 {
		return errors.New("Sekaipedia candidate requires fixed List and song revision evidence")
	}
	listCount := 0
	songCount := 0
	for _, reference := range candidate.IndexEvidenceRefs {
		evidence := byID[reference.EvidenceID]
		switch {
		case isFixedSekaipediaAuthorityEvidence(evidence, authority):
			listCount++
		case sekaipediaRevisionEvidenceMatchesCandidate(evidence, candidate):
			songCount++
		default:
			return errors.New("Sekaipedia revision evidence is neither the fixed List nor the candidate song")
		}
	}
	if listCount != 1 || songCount != 1 {
		return errors.New("Sekaipedia candidate evidence is not exactly one fixed List plus one song revision")
	}
	return nil
}

func validSekaipediaAuthorityBinding(authority FixedIndex) bool {
	revisionTimestamp, err := time.Parse(time.RFC3339Nano, authority.RevisionTimestamp)
	return sekaipediaAuthorityEvidenceID(authority) != "" && HasCanonicalSHA1(authority.SHA1) &&
		canonicalIndexEvidenceSHA256.MatchString(authority.ContentSHA256) &&
		canonicalIndexEvidenceSHA256.MatchString(authority.RawSHA256) && err == nil &&
		revisionTimestamp.UTC().Format(time.RFC3339Nano) == authority.RevisionTimestamp &&
		strings.HasSuffix(authority.RevisionTimestamp, "Z")
}

func sameSekaipediaRevisionAuthority(left, right FixedIndex) bool {
	return validSekaipediaAuthorityBinding(left) && validSekaipediaAuthorityBinding(right) &&
		left.PageID == right.PageID && left.RevisionID == right.RevisionID &&
		left.RevisionTimestamp == right.RevisionTimestamp && left.SHA1 == right.SHA1 &&
		left.ContentSHA256 == right.ContentSHA256 && left.Title == right.Title
}

func sekaipediaAuthorityEvidenceIdentityMatches(evidence IndexEvidence, authority FixedIndex) bool {
	baseID := sekaipediaAuthorityEvidenceID(authority)
	return validSekaipediaAuthorityBinding(authority) &&
		evidence.Kind == IndexEvidenceKindMediaWikiRevision &&
		evidence.Provider == ProviderSekaipedia && evidence.Origin == OriginSekaipedia &&
		evidence.EvidenceID == sekaipediaRevisionAcquisitionEvidenceID(
			baseID, evidence.FetchedAt, evidence.RawSHA256,
		) && evidence.PageID == authority.PageID && evidence.RevisionID == authority.RevisionID &&
		evidence.RevisionTimestamp == authority.RevisionTimestamp && evidence.MediaWikiSHA1 == authority.SHA1 &&
		evidence.Title == authority.Title &&
		evidence.CanonicalURL == canonicalRevisionURL(ProviderSekaipedia, authority.Title, authority.RevisionID) &&
		evidence.RawSHA256 == authority.RawSHA256
}

func isFixedSekaipediaAuthorityEvidence(evidence IndexEvidence, authority FixedIndex) bool {
	return sekaipediaAuthorityEvidenceIdentityMatches(evidence, authority) &&
		VerifySekaipediaRevisionContent(evidence.Raw, authority) == nil
}

// SekaipediaAuthorityFromIndexEvidence derives one complete immutable authority
// from an exact validated authority acquisition. Song-revision evidence and
// malformed or non-List authority envelopes are rejected.
func SekaipediaAuthorityFromIndexEvidence(evidence IndexEvidence) (FixedIndex, error) {
	binding, err := validateIndexEvidenceWithBinding(evidence)
	if err != nil {
		return FixedIndex{}, err
	}
	if binding.sekaipediaAuthority == nil {
		return FixedIndex{}, errors.New("index evidence is not a Sekaipedia authority acquisition")
	}
	return *binding.sekaipediaAuthority, nil
}

func sekaipediaRevisionEvidenceIdentityMatchesCandidate(evidence IndexEvidence, candidate Candidate) bool {
	return evidence.Kind == IndexEvidenceKindMediaWikiRevision && evidence.Provider == ProviderSekaipedia &&
		evidence.Origin == OriginSekaipedia && evidence.EvidenceID == sekaipediaRevisionAcquisitionEvidenceID(
		sekaipediaSongEvidenceID(candidate.PageID, candidate.RevisionID), evidence.FetchedAt, evidence.RawSHA256,
	) &&
		evidence.PageID == candidate.PageID && evidence.RevisionID == candidate.RevisionID &&
		evidence.RevisionTimestamp == candidate.RevisionTimestamp && evidence.MediaWikiSHA1 == candidate.SHA1 &&
		evidence.Title == candidate.Title && evidence.CanonicalURL == candidate.CanonicalURL &&
		equalCandidateCategories(evidence.Categories, candidate.Categories) &&
		(candidate.FetchedAt == "" || evidence.FetchedAt == candidate.FetchedAt)
}

func sekaipediaRevisionEvidenceMatchesCandidate(evidence IndexEvidence, candidate Candidate) bool {
	if !sekaipediaRevisionEvidenceIdentityMatchesCandidate(evidence, candidate) {
		return false
	}
	page, err := parsePageResponse(evidence.Raw)
	return err == nil && (candidate.RawSHA256 == "" || page.rawSHA256 == candidate.RawSHA256)
}

func validateSekaipediaCandidateValidatedEvidence(
	candidate Candidate,
	byID map[string]IndexEvidence,
	rawSHA256s map[string]string,
	authority FixedIndex,
	authorityEvidenceIDs map[string]struct{},
) error {
	if len(candidate.IndexEvidenceRefs) != 2 || len(byID) != 2 || !validSekaipediaAuthorityBinding(authority) {
		return errors.New("Sekaipedia candidate requires fixed List and song revision evidence")
	}
	listCount := 0
	songCount := 0
	for _, reference := range candidate.IndexEvidenceRefs {
		evidence := byID[reference.EvidenceID]
		_, authorityEvidence := authorityEvidenceIDs[evidence.EvidenceID]
		switch {
		case authorityEvidence && sekaipediaAuthorityEvidenceIdentityMatches(evidence, authority):
			listCount++
		case sekaipediaRevisionEvidenceIdentityMatchesCandidate(evidence, candidate):
			rawSHA256, found := rawSHA256s[evidence.EvidenceID]
			if !found || candidate.RawSHA256 != "" && rawSHA256 != candidate.RawSHA256 {
				return errors.New("Sekaipedia revision evidence is neither the fixed List nor the candidate song")
			}
			songCount++
		default:
			return errors.New("Sekaipedia revision evidence is neither the fixed List nor the candidate song")
		}
	}
	if listCount != 1 || songCount != 1 {
		return errors.New("Sekaipedia candidate evidence is not exactly one fixed List plus one song revision")
	}
	return nil
}

func sekaipediaCandidateRevisionPage(candidate Candidate) (wikiPage, error) {
	matches := 0
	var result wikiPage
	for _, evidence := range candidate.IndexEvidence {
		if !sekaipediaRevisionEvidenceMatchesCandidate(evidence, candidate) {
			continue
		}
		page, err := parsePageResponse(evidence.Raw)
		fetchedAt, fetchedErr := time.Parse(time.RFC3339Nano, evidence.FetchedAt)
		if err != nil || fetchedErr != nil {
			return wikiPage{}, ErrMalformedResponse
		}
		page.fetchedAt = fetchedAt.UTC()
		page.rawResponse = append([]byte(nil), evidence.Raw...)
		page.rawResponseSHA256 = evidence.RawSHA256
		result = page
		matches++
	}
	if matches != 1 {
		return wikiPage{}, ErrMalformedResponse
	}
	return result, nil
}
