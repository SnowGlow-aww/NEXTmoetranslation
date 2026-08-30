package lyricsdiscovery

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"moesekai/server/internal/lyricssource"
	"moesekai/server/internal/model"
)

func TestCandidateArtifactDeduplicatesSharedFandomEvidence(t *testing.T) {
	candidates, raw := sharedFandomArtifactCandidates(t, 64, false)
	if len(candidates) != 2 || candidates[0].IndexEvidenceRefs[0] != candidates[1].IndexEvidenceRefs[0] {
		t.Fatalf("candidates do not share one response-scoped ref: %+v", candidates)
	}
	artifact, err := MarshalCandidateArtifact(candidates)
	if err != nil {
		t.Fatal(err)
	}
	var wire candidateArtifactEnvelope
	if err := json.Unmarshal(artifact, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.SchemaVersion != CandidateArtifactSchemaVersion || len(wire.Candidates) != 2 || len(wire.IndexEvidence) != 1 ||
		wire.Candidates[0].IndexEvidence != nil || wire.Candidates[1].IndexEvidence != nil {
		t.Fatalf("deduplicated wire artifact=%+v", wire)
	}
	resolved, err := DecodeCandidateArtifact(artifact)
	if err != nil || len(resolved) != 2 {
		t.Fatalf("resolved candidates=%+v err=%v", resolved, err)
	}
	for _, candidate := range resolved {
		if len(candidate.IndexEvidence) != 1 || !bytes.Equal(candidate.IndexEvidence[0].Raw, raw) ||
			candidate.IndexEvidenceRefs[0].EvidenceID != wire.IndexEvidence[0].EvidenceID ||
			lyricssource.ValidateCandidateIndexEvidence(candidate) != nil {
			t.Fatalf("candidate did not resolve shared exact evidence once: %+v", candidate)
		}
	}
}

func TestCandidateArtifactRejectsDuplicateMembershipAndOrphanEvidence(t *testing.T) {
	t.Run("candidate occurs twice in shared response", func(t *testing.T) {
		candidates, _ := sharedFandomArtifactCandidates(t, 64, true)
		if len(candidates) != 1 {
			t.Fatalf("candidates=%+v", candidates)
		}
		if _, err := MarshalCandidateArtifact(candidates); err == nil {
			t.Fatal("candidate with duplicate response membership was accepted")
		}
	})

	t.Run("missing evidence", func(t *testing.T) {
		wire := validCandidateArtifactEnvelope(t)
		wire.IndexEvidence = []lyricssource.IndexEvidence{}
		tampered, err := json.Marshal(wire)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeCandidateArtifact(tampered); err == nil {
			t.Fatal("reference without exact evidence was accepted")
		}
	})

	t.Run("duplicate evidence ID", func(t *testing.T) {
		wire := validCandidateArtifactEnvelope(t)
		wire.IndexEvidence = append(wire.IndexEvidence, wire.IndexEvidence[0])
		tampered, err := json.Marshal(wire)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeCandidateArtifact(tampered); err == nil {
			t.Fatal("duplicate exact evidence resolution was accepted")
		}
	})

	t.Run("orphan evidence", func(t *testing.T) {
		wire := validCandidateArtifactEnvelope(t)
		orphan := wire.IndexEvidence[0]
		orphan.EvidenceID += "-orphan"
		wire.IndexEvidence = append(wire.IndexEvidence, orphan)
		tampered, err := json.Marshal(wire)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeCandidateArtifact(tampered); err == nil {
			t.Fatal("orphan exact evidence was accepted")
		}
	})
}

func TestCandidateArtifactRoundTripsTrueMaximumRawEvidence(t *testing.T) {
	candidate, raw := exactRawFandomArtifactCandidate(
		t, "max-raw", 12, 34, MaxCandidateArtifactRawEvidenceBytes, false,
	)
	if len(raw) != lyricssource.MaxIndexEvidenceRawBytes {
		t.Fatalf("raw evidence bytes=%d want=%d", len(raw), lyricssource.MaxIndexEvidenceRawBytes)
	}
	artifact, err := MarshalCandidateArtifact([]lyricssource.Candidate{candidate})
	if err != nil {
		t.Fatal(err)
	}
	if len(artifact) > MaxCandidateArtifactBytes {
		t.Fatalf("encoded artifact bytes=%d limit=%d", len(artifact), MaxCandidateArtifactBytes)
	}
	resolved, err := DecodeCandidateArtifact(artifact)
	if err != nil || len(resolved) != 1 || len(resolved[0].IndexEvidence) != 1 ||
		!bytes.Equal(resolved[0].IndexEvidence[0].Raw, raw) {
		t.Fatalf("maximum raw evidence did not round-trip exactly: candidates=%d err=%v", len(resolved), err)
	}
}

func TestCandidateArtifactRejectsAggregateRawEvidencePlusOne(t *testing.T) {
	first, _ := exactRawFandomArtifactCandidate(t, "aggregate-a", 12, 34, MaxCandidateArtifactRawEvidenceBytes/2, false)
	second, _ := exactRawFandomArtifactCandidate(t, "aggregate-b", 13, 35, MaxCandidateArtifactRawEvidenceBytes/2+1, false)
	candidates := []lyricssource.Candidate{first, second}
	if _, err := MarshalCandidateArtifact(candidates); !errors.Is(err, ErrCandidateArtifactRawEvidenceTooLarge) {
		t.Fatalf("aggregate raw +1 marshal error=%v", err)
	}

	wire := artifactEnvelopeFromHydratedCandidates(candidates)
	encoded, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > MaxCandidateArtifactBytes {
		t.Fatalf("aggregate raw +1 fixture unexpectedly exceeded encoded limit: %d", len(encoded))
	}
	if _, err := DecodeCandidateArtifact(encoded); !errors.Is(err, ErrCandidateArtifactRawEvidenceTooLarge) {
		t.Fatalf("aggregate raw +1 decode error=%v", err)
	}
}

func TestCandidateArtifactRejectsEncodedBytesPlusOne(t *testing.T) {
	largeIdentity, _ := exactRawFandomArtifactCandidate(t, "encoded-marshal", 12, 34, 3*(1<<20)/2, true)
	wire := artifactEnvelopeFromHydratedCandidates([]lyricssource.Candidate{largeIdentity})
	encoded, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) <= MaxCandidateArtifactBytes {
		t.Fatalf("encoded overflow fixture bytes=%d limit=%d", len(encoded), MaxCandidateArtifactBytes)
	}
	if _, err := MarshalCandidateArtifact([]lyricssource.Candidate{largeIdentity}); !errors.Is(err, ErrCandidateArtifactTooLarge) {
		t.Fatalf("encoded overflow marshal error=%v", err)
	}

	small, err := MarshalCandidateArtifact([]lyricssource.Candidate{testDiscoveryCandidate(
		t, 21, 43, "encoded decode", "制作者 original song Lyrics\n== Lyrics ==\n歌う",
	)})
	if err != nil {
		t.Fatal(err)
	}
	exactPlusOne := append([]byte(nil), small...)
	exactPlusOne = append(exactPlusOne, bytes.Repeat([]byte(" "), MaxCandidateArtifactBytes+1-len(exactPlusOne))...)
	if len(exactPlusOne) != MaxCandidateArtifactBytes+1 {
		t.Fatalf("encoded +1 fixture bytes=%d", len(exactPlusOne))
	}
	if _, err := DecodeCandidateArtifact(exactPlusOne); !errors.Is(err, ErrCandidateArtifactTooLarge) {
		t.Fatalf("encoded +1 decode error=%v", err)
	}
}

func exactRawFandomArtifactCandidate(
	t *testing.T,
	query string,
	pageID, revisionID, targetRawBytes int,
	largeTitle bool,
) (lyricssource.Candidate, []byte) {
	t.Helper()
	build := func(fillerBytes int) ([]byte, string, string) {
		title := "精確試験曲"
		content := "制作者 original song Lyrics\n== Lyrics ==\n歌う"
		if largeTitle {
			title += strings.Repeat("t", fillerBytes)
		} else {
			content += strings.Repeat("a", fillerBytes)
		}
		contentSHA1 := fmt.Sprintf("%x", sha1.Sum([]byte(content)))
		raw, err := json.Marshal(map[string]any{"query": map[string]any{"pages": map[string]any{
			fmt.Sprint(pageID): map[string]any{
				"pageid": pageID, "title": title,
				"categories": []map[string]string{{"title": "Category:Songs"}},
				"revisions": []map[string]any{{
					"revid": revisionID, "sha1": contentSHA1,
					"slots": map[string]any{"main": map[string]string{"content": content}},
				}},
			},
		}}})
		if err != nil {
			t.Fatal(err)
		}
		return raw, title, content
	}
	base, _, _ := build(0)
	if targetRawBytes < len(base) {
		t.Fatalf("target raw bytes=%d below fixed response bytes=%d", targetRawBytes, len(base))
	}
	raw, title, content := build(targetRawBytes - len(base))
	if len(raw) != targetRawBytes {
		t.Fatalf("exact raw fixture bytes=%d want=%d", len(raw), targetRawBytes)
	}

	requestURL, err := lyricssource.CanonicalFandomSearchRequestURL(query)
	if err != nil {
		t.Fatal(err)
	}
	rawDigest := sha256.Sum256(raw)
	rawSHA256 := fmt.Sprintf("%x", rawDigest)
	const fetchedAt = "2026-07-31T12:00:00Z"
	evidenceID := lyricssource.MediaWikiSearchResponseAcquisitionEvidenceID(requestURL, fetchedAt, rawSHA256)
	evidence := lyricssource.IndexEvidence{
		EvidenceID: evidenceID, SHA256: rawSHA256,
		Kind:     lyricssource.IndexEvidenceKindMediaWikiSearchResponse,
		Provider: lyricssource.ProviderVocaloidFandom, Origin: lyricssource.OriginVocaloidFandom,
		Categories: []string{}, CanonicalRequestURL: requestURL, FetchedAt: fetchedAt,
		Raw: append([]byte(nil), raw...), RawSHA256: rawSHA256,
	}
	canonical := url.URL{Scheme: "https", Host: "vocaloid.fandom.com", Path: "/wiki/" + strings.ReplaceAll(title, " ", "_")}
	canonicalQuery := canonical.Query()
	canonicalQuery.Set("oldid", fmt.Sprint(revisionID))
	canonical.RawQuery = canonicalQuery.Encode()
	candidate := lyricssource.Candidate{
		Provider: lyricssource.ProviderVocaloidFandom, Origin: lyricssource.OriginVocaloidFandom,
		PageID: pageID, RevisionID: revisionID, Title: title, CanonicalURL: canonical.String(),
		SHA1: fmt.Sprintf("%x", sha1.Sum([]byte(content))), Categories: []string{"Songs"},
		IndexEvidenceRefs: []model.LyricsSourceIndexEvidenceRef{{EvidenceID: evidenceID, SHA256: rawSHA256}},
		IndexEvidence:     []lyricssource.IndexEvidence{evidence},
	}
	if err := lyricssource.ValidateCandidateIndexEvidence(candidate); err != nil {
		t.Fatalf("exact raw candidate is invalid: %v", err)
	}
	return candidate, raw
}

func artifactEnvelopeFromHydratedCandidates(candidates []lyricssource.Candidate) candidateArtifactEnvelope {
	wire := candidateArtifactEnvelope{
		SchemaVersion: CandidateArtifactSchemaVersion,
		Candidates:    make([]lyricssource.Candidate, len(candidates)),
		IndexEvidence: []lyricssource.IndexEvidence{},
	}
	seen := map[string]struct{}{}
	for index, candidate := range candidates {
		wire.Candidates[index] = candidate
		wire.Candidates[index].Categories = append([]string(nil), candidate.Categories...)
		wire.Candidates[index].IndexEvidenceRefs = append([]model.LyricsSourceIndexEvidenceRef(nil), candidate.IndexEvidenceRefs...)
		wire.Candidates[index].IndexEvidence = nil
		for _, evidence := range candidate.IndexEvidence {
			if _, found := seen[evidence.EvidenceID]; found {
				continue
			}
			seen[evidence.EvidenceID] = struct{}{}
			wire.IndexEvidence = append(wire.IndexEvidence, evidence)
		}
	}
	return wire
}

func validCandidateArtifactEnvelope(t *testing.T) candidateArtifactEnvelope {
	t.Helper()
	candidates, _ := sharedFandomArtifactCandidates(t, 64, false)
	artifact, err := MarshalCandidateArtifact(candidates)
	if err != nil {
		t.Fatal(err)
	}
	var wire candidateArtifactEnvelope
	if err := json.Unmarshal(artifact, &wire); err != nil {
		t.Fatal(err)
	}
	return wire
}

func sharedFandomArtifactCandidates(t *testing.T, fillerBytes int, duplicateFirst bool) ([]lyricssource.Candidate, []byte) {
	t.Helper()
	const query = "共同試験曲"
	contents := []string{
		"制作者 original song Lyrics\n== Lyrics ==\n" + strings.Repeat("a", fillerBytes),
		"制作者 original song Lyrics\n== Lyrics ==\n" + strings.Repeat("b", fillerBytes),
	}
	titles := []string{"共同試験曲/制作者", "共同試験曲/制作者"}
	pageIDs := []int{12, 13}
	revisionIDs := []int{34, 35}
	if duplicateFirst {
		contents[1], titles[1], pageIDs[1], revisionIDs[1] = contents[0], titles[0], pageIDs[0], revisionIDs[0]
	}

	pages := make(map[string]any, 2)
	for index := range pageIDs {
		contentSHA1 := fmt.Sprintf("%x", sha1.Sum([]byte(contents[index])))
		pages[fmt.Sprintf("wire-%d", index)] = map[string]any{
			"pageid": pageIDs[index], "title": titles[index],
			"categories": []map[string]string{{"title": "Category:Songs"}},
			"revisions": []map[string]any{{
				"revid": revisionIDs[index], "sha1": contentSHA1,
				"slots": map[string]any{"main": map[string]string{"content": contents[index]}},
			}},
		}
	}
	raw, err := json.Marshal(map[string]any{"query": map[string]any{"pages": pages}})
	if err != nil {
		t.Fatal(err)
	}
	requestURL, err := lyricssource.CanonicalFandomSearchRequestURL(query)
	if err != nil {
		t.Fatal(err)
	}
	rawDigest := sha256.Sum256(raw)
	rawSHA256 := fmt.Sprintf("%x", rawDigest)
	const fetchedAt = "2026-07-31T12:00:00Z"
	evidenceID := lyricssource.MediaWikiSearchResponseAcquisitionEvidenceID(requestURL, fetchedAt, rawSHA256)
	evidence := lyricssource.IndexEvidence{
		EvidenceID: evidenceID, SHA256: rawSHA256,
		Kind:     lyricssource.IndexEvidenceKindMediaWikiSearchResponse,
		Provider: lyricssource.ProviderVocaloidFandom, Origin: lyricssource.OriginVocaloidFandom,
		Categories: []string{}, CanonicalRequestURL: requestURL, FetchedAt: fetchedAt,
		Raw: append([]byte(nil), raw...), RawSHA256: rawSHA256,
	}
	count := 2
	if duplicateFirst {
		count = 1
	}
	candidates := make([]lyricssource.Candidate, 0, count)
	for index := 0; index < count; index++ {
		canonical := url.URL{Scheme: "https", Host: "vocaloid.fandom.com", Path: "/wiki/" + strings.ReplaceAll(titles[index], " ", "_")}
		canonicalQuery := canonical.Query()
		canonicalQuery.Set("oldid", fmt.Sprint(revisionIDs[index]))
		canonical.RawQuery = canonicalQuery.Encode()
		candidates = append(candidates, lyricssource.Candidate{
			Provider: lyricssource.ProviderVocaloidFandom, Origin: lyricssource.OriginVocaloidFandom,
			PageID: pageIDs[index], RevisionID: revisionIDs[index], Title: titles[index], CanonicalURL: canonical.String(),
			SHA1: fmt.Sprintf("%x", sha1.Sum([]byte(contents[index]))), Categories: []string{"Songs"},
			IndexEvidenceRefs: []model.LyricsSourceIndexEvidenceRef{{EvidenceID: evidenceID, SHA256: rawSHA256}},
			IndexEvidence:     []lyricssource.IndexEvidence{evidence},
		})
	}
	return candidates, raw
}
