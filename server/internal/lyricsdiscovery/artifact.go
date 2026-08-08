package lyricsdiscovery

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sort"

	"moesekai/server/internal/legacy"
	"moesekai/server/internal/lyricssource"
	"moesekai/server/internal/model"
)

const (
	CandidateArtifactSchemaVersion       = 1
	MaxCandidateArtifactCandidates       = 8
	MaxCandidateArtifactEvidence         = 64
	MaxCandidateArtifactRawEvidenceBytes = lyricssource.MaxIndexEvidenceRawBytes
	// MaxCandidateArtifactBytes is the encoded worker-completion envelope limit.
	// Durable shadow, review, and job JSON remains compact refs-only and may keep
	// its separate 1 MiB bound. Evidence bytes are never truncated or discarded.
	MaxCandidateArtifactBytes = 4 << 20
)

var (
	ErrCandidateArtifactTooLarge            = errors.New("lyrics discovery candidate artifact exceeds encoded safe limit")
	ErrCandidateArtifactRawEvidenceTooLarge = errors.New("lyrics discovery candidate artifact exceeds aggregate raw evidence limit")
)

type candidateArtifactEnvelope struct {
	SchemaVersion int                          `json:"schemaVersion"`
	Candidates    []lyricssource.Candidate     `json:"candidates"`
	IndexEvidence []lyricssource.IndexEvidence `json:"indexEvidence"`
}

// MarshalCandidateArtifact creates the private T2-to-T3 artifact. Candidate
// identities retain only compact refs on the wire; exact evidence envelopes are
// deduplicated by ID at artifact scope and emitted once in deterministic order.
func MarshalCandidateArtifact(candidates []lyricssource.Candidate) ([]byte, error) {
	if len(candidates) > MaxCandidateArtifactCandidates {
		return nil, errors.New("lyrics discovery candidate artifact has too many candidates")
	}
	if err := lyricssource.ValidateCandidatesIndexEvidence(candidates); err != nil {
		return nil, err
	}

	detached := make([]lyricssource.Candidate, len(candidates))
	byID := make(map[string]lyricssource.IndexEvidence)
	totalRawEvidenceBytes := 0
	for index, candidate := range candidates {
		detached[index] = cloneArtifactCandidate(candidate)
		detached[index].IndexEvidence = nil
		for _, evidence := range candidate.IndexEvidence {
			if existing, found := byID[evidence.EvidenceID]; found {
				if !artifactIndexEvidenceEqual(existing, evidence) {
					return nil, errors.New("candidate artifact evidence ID has conflicting resolutions")
				}
				continue
			}
			if len(evidence.Raw) > MaxCandidateArtifactRawEvidenceBytes-totalRawEvidenceBytes {
				return nil, ErrCandidateArtifactRawEvidenceTooLarge
			}
			totalRawEvidenceBytes += len(evidence.Raw)
			byID[evidence.EvidenceID] = cloneArtifactIndexEvidence(evidence)
		}
	}

	if len(byID) > MaxCandidateArtifactEvidence {
		return nil, errors.New("lyrics discovery candidate artifact has too much evidence")
	}
	evidenceIDs := make([]string, 0, len(byID))
	for evidenceID := range byID {
		evidenceIDs = append(evidenceIDs, evidenceID)
	}
	sort.Strings(evidenceIDs)
	evidence := make([]lyricssource.IndexEvidence, 0, len(evidenceIDs))
	for _, evidenceID := range evidenceIDs {
		evidence = append(evidence, byID[evidenceID])
	}
	if detached == nil {
		detached = []lyricssource.Candidate{}
	}
	if evidence == nil {
		evidence = []lyricssource.IndexEvidence{}
	}

	artifact, err := json.Marshal(candidateArtifactEnvelope{
		SchemaVersion: CandidateArtifactSchemaVersion,
		Candidates:    detached,
		IndexEvidence: evidence,
	})
	if err != nil {
		return nil, err
	}
	if len(artifact) > MaxCandidateArtifactBytes {
		return nil, ErrCandidateArtifactTooLarge
	}
	return artifact, nil
}

// DecodeCandidateArtifact validates the private artifact and reattaches an
// independent copy of each exact evidence envelope to every candidate ref. It
// rejects missing, duplicated, conflicting, or orphan evidence.
func DecodeCandidateArtifact(artifact []byte) ([]lyricssource.Candidate, error) {
	if len(artifact) < 2 {
		return nil, errors.New("lyrics discovery candidate artifact is empty")
	}
	if len(artifact) > MaxCandidateArtifactBytes {
		return nil, ErrCandidateArtifactTooLarge
	}
	if err := legacy.ValidateUniqueJSON(artifact); err != nil {
		return nil, errors.New("lyrics discovery candidate artifact is invalid")
	}

	var envelope candidateArtifactEnvelope
	decoder := json.NewDecoder(bytes.NewReader(artifact))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return nil, errors.New("lyrics discovery candidate artifact is invalid")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("lyrics discovery candidate artifact has trailing data")
	}
	if envelope.SchemaVersion != CandidateArtifactSchemaVersion || envelope.Candidates == nil || envelope.IndexEvidence == nil ||
		len(envelope.Candidates) > MaxCandidateArtifactCandidates || len(envelope.IndexEvidence) > MaxCandidateArtifactEvidence {
		return nil, errors.New("lyrics discovery candidate artifact shape is invalid")
	}

	byID := make(map[string]lyricssource.IndexEvidence, len(envelope.IndexEvidence))
	totalRawEvidenceBytes := 0
	for _, evidence := range envelope.IndexEvidence {
		if _, duplicate := byID[evidence.EvidenceID]; duplicate {
			return nil, errors.New("candidate artifact evidence ID resolves more than once")
		}
		if len(evidence.Raw) > MaxCandidateArtifactRawEvidenceBytes-totalRawEvidenceBytes {
			return nil, ErrCandidateArtifactRawEvidenceTooLarge
		}
		totalRawEvidenceBytes += len(evidence.Raw)
		byID[evidence.EvidenceID] = cloneArtifactIndexEvidence(evidence)
	}

	resolved := make([]lyricssource.Candidate, len(envelope.Candidates))
	usedEvidence := make(map[string]struct{}, len(byID))
	for index, candidate := range envelope.Candidates {
		if candidate.IndexEvidence != nil {
			return nil, errors.New("candidate artifact embeds non-deduplicated evidence")
		}
		resolved[index] = cloneArtifactCandidate(candidate)
		resolved[index].IndexEvidence = make([]lyricssource.IndexEvidence, 0, len(candidate.IndexEvidenceRefs))
		for _, reference := range candidate.IndexEvidenceRefs {
			evidence, found := byID[reference.EvidenceID]
			if !found {
				return nil, errors.New("candidate artifact reference has no exact evidence")
			}
			resolved[index].IndexEvidence = append(resolved[index].IndexEvidence, cloneArtifactIndexEvidence(evidence))
			usedEvidence[reference.EvidenceID] = struct{}{}
		}
	}
	if len(usedEvidence) != len(byID) {
		return nil, errors.New("candidate artifact contains orphan evidence")
	}
	if err := lyricssource.ValidateCandidatesIndexEvidence(resolved); err != nil {
		return nil, err
	}
	return resolved, nil
}

func cloneArtifactCandidate(candidate lyricssource.Candidate) lyricssource.Candidate {
	candidate.Categories = append([]string(nil), candidate.Categories...)
	candidate.IndexEvidenceRefs = append([]model.LyricsSourceIndexEvidenceRef(nil), candidate.IndexEvidenceRefs...)
	if candidate.IndexEvidence != nil {
		candidate.IndexEvidence = make([]lyricssource.IndexEvidence, len(candidate.IndexEvidence))
		for index, evidence := range candidate.IndexEvidence {
			candidate.IndexEvidence[index] = cloneArtifactIndexEvidence(evidence)
		}
	}
	return candidate
}

func cloneArtifactIndexEvidence(evidence lyricssource.IndexEvidence) lyricssource.IndexEvidence {
	evidence.Categories = append([]string(nil), evidence.Categories...)
	evidence.Raw = append([]byte(nil), evidence.Raw...)
	return evidence
}

func artifactIndexEvidenceEqual(left, right lyricssource.IndexEvidence) bool {
	if left.EvidenceID != right.EvidenceID || left.SHA256 != right.SHA256 || left.Kind != right.Kind ||
		left.Provider != right.Provider || left.Origin != right.Origin || left.PageID != right.PageID ||
		left.RevisionID != right.RevisionID || left.MediaWikiSHA1 != right.MediaWikiSHA1 || left.Title != right.Title ||
		left.CanonicalURL != right.CanonicalURL || left.CanonicalRequestURL != right.CanonicalRequestURL ||
		left.FetchedAt != right.FetchedAt || left.RawSHA256 != right.RawSHA256 ||
		!bytes.Equal(left.Raw, right.Raw) || len(left.Categories) != len(right.Categories) {
		return false
	}
	for index := range left.Categories {
		if left.Categories[index] != right.Categories[index] {
			return false
		}
	}
	return true
}
