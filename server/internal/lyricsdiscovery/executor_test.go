package lyricsdiscovery

import (
	"bytes"
	"context"
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

type searchSourceFunc func(context.Context, lyricssource.MusicIdentity) ([]lyricssource.Candidate, error)

func (fn searchSourceFunc) Search(ctx context.Context, identity lyricssource.MusicIdentity) ([]lyricssource.Candidate, error) {
	return fn(ctx, identity)
}

func TestSourceExecutorProducesShadowCandidateSummary(t *testing.T) {
	first := testDiscoveryCandidate(t, 12, 34, "合成試験曲", "制作者 original song Lyrics\n== Lyrics ==\n歌う")
	second := testDiscoveryCandidate(t, 13, 35, "合成試験曲/制作者", "制作者 original song Lyrics\n== Lyrics ==\n踊る")
	for name, candidates := range map[string][]lyricssource.Candidate{
		"none":      {},
		"one":       {first},
		"ambiguous": {first, second},
	} {
		t.Run(name, func(t *testing.T) {
			executor, err := NewSourceExecutor(searchSourceFunc(func(_ context.Context, identity lyricssource.MusicIdentity) ([]lyricssource.Candidate, error) {
				if identity.MusicID != 10 || identity.JapaneseTitle != "合成試験曲" || identity.ProducerMetadata != "制作者" ||
					identity.PerformerSegmentationPolicy != lyricssource.PerformerSegmentationSekaiEligible {
					t.Fatalf("identity=%+v", identity)
				}
				return candidates, nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			result, err := executor.Discover(context.Background(), Job{MusicID: 10, JapaneseTitle: "合成試験曲", ProducerMetadata: "制作者",
				PerformerSegmentationPolicy: lyricssource.PerformerSegmentationSekaiEligible})
			if err != nil {
				t.Fatal(err)
			}
			wantOutcome := OutcomeNoCandidates
			if len(candidates) == 1 {
				wantOutcome = OutcomeCandidatesFound
			} else if len(candidates) > 1 {
				wantOutcome = OutcomeAmbiguous
			}
			if result.Outcome != wantOutcome || result.CandidateCount != len(candidates) || !validResult(result) ||
				!strings.Contains(string(result.Artifact), `"candidates"`) {
				t.Fatalf("result=%+v artifact=%s", result, result.Artifact)
			}
		})
	}
}

func TestSourceExecutorPreservesConcreteIndexEvidenceInArtifact(t *testing.T) {
	candidate := testDiscoveryCandidate(
		t, 12, 34, "合成試験曲", "制作者 original song Lyrics\n== Lyrics ==\n歌う",
	)
	executor, err := NewSourceExecutor(searchSourceFunc(func(context.Context, lyricssource.MusicIdentity) ([]lyricssource.Candidate, error) {
		return []lyricssource.Candidate{candidate}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Discover(context.Background(), Job{
		MusicID: 10, JapaneseTitle: "合成試験曲", ProducerMetadata: "制作者",
	})
	if err != nil {
		t.Fatal(err)
	}
	var wire candidateArtifactEnvelope
	if err := json.Unmarshal(result.Artifact, &wire); err != nil || len(wire.Candidates) != 1 ||
		len(wire.IndexEvidence) != 1 || wire.Candidates[0].IndexEvidence != nil {
		t.Fatalf("artifact=%s wire=%+v err=%v", result.Artifact, wire, err)
	}
	candidates, err := DecodeCandidateArtifact(result.Artifact)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("artifact=%s candidates=%+v err=%v", result.Artifact, candidates, err)
	}
	transported := candidates[0]
	if len(transported.IndexEvidenceRefs) != 1 || len(transported.IndexEvidence) != 1 ||
		!bytes.Equal(transported.IndexEvidence[0].Raw, candidate.IndexEvidence[0].Raw) ||
		transported.IndexEvidence[0].RawSHA256 != candidate.IndexEvidence[0].RawSHA256 ||
		transported.IndexEvidenceRefs[0].SHA256 != candidate.IndexEvidenceRefs[0].SHA256 {
		t.Fatalf("concrete evidence did not survive artifact transport: %+v", transported)
	}
}

func TestSourceExecutorRejectsInvalidEvidenceTransport(t *testing.T) {
	valid := testDiscoveryCandidate(t, 12, 34, "合成試験曲", "制作者 original song Lyrics\n== Lyrics ==\n歌う")
	missing := valid
	missing.IndexEvidence = nil

	conflicting := testDiscoveryCandidate(t, 13, 35, "合成試験曲/制作者", "制作者 original song Lyrics\n== Lyrics ==\n踊る")
	conflicting.IndexEvidence[0].EvidenceID = valid.IndexEvidence[0].EvidenceID
	conflicting.IndexEvidenceRefs[0].EvidenceID = valid.IndexEvidenceRefs[0].EvidenceID
	aggregateFirst, _ := exactRawFandomArtifactCandidate(t, "executor-aggregate-a", 14, 36, MaxCandidateArtifactRawEvidenceBytes/2, false)
	aggregateSecond, _ := exactRawFandomArtifactCandidate(t, "executor-aggregate-b", 15, 37, MaxCandidateArtifactRawEvidenceBytes/2+1, false)
	overLimit := []lyricssource.Candidate{aggregateFirst, aggregateSecond}

	for name, candidates := range map[string][]lyricssource.Candidate{
		"missing exact evidence":               {missing},
		"conflicting duplicate ID":             {valid, conflicting},
		"artifact exceeds aggregate raw limit": overLimit,
	} {
		t.Run(name, func(t *testing.T) {
			executor, err := NewSourceExecutor(searchSourceFunc(func(context.Context, lyricssource.MusicIdentity) ([]lyricssource.Candidate, error) {
				return candidates, nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			_, err = executor.Discover(context.Background(), Job{
				MusicID: 10, JapaneseTitle: "合成試験曲", ProducerMetadata: "制作者",
			})
			failure := Classify(err)
			if failure.Code != CodeInvalidResult || failure.Retryable {
				t.Fatalf("failure=%+v err=%v", failure, err)
			}
		})
	}
}

func TestSourceExecutorRejectsIncompleteCatalogIdentity(t *testing.T) {
	executor, err := NewSourceExecutor(searchSourceFunc(func(context.Context, lyricssource.MusicIdentity) ([]lyricssource.Candidate, error) {
		t.Fatal("invalid job reached source client")
		return nil, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range []Job{{}, {MusicID: 10, JapaneseTitle: "合成試験曲"}} {
		_, err := executor.Discover(context.Background(), job)
		var coded *Error
		if !errors.As(err, &coded) || coded.Code != CodeInvalidJob {
			t.Fatalf("invalid job %+v error=%v", job, err)
		}
	}
}

func TestSourceExecutorClassifiesStableSourceFailures(t *testing.T) {
	for name, sourceErr := range map[string]struct {
		err       error
		code      ErrorCode
		retryable bool
	}{
		"rate limited":       {err: &lyricssource.HTTPError{StatusCode: 429}, code: CodeRateLimited, retryable: true},
		"upstream failure":   {err: &lyricssource.HTTPError{StatusCode: 503}, code: CodeSourceUnavailable, retryable: true},
		"restricted":         {err: lyricssource.ErrRestrictedReprint, code: CodeRestricted},
		"source drift":       {err: lyricssource.ErrRevisionChanged, code: CodeSourceDrift},
		"unsupported markup": {err: lyricssource.ErrUnsupportedTable, code: CodeUnsupported},
		"malformed":          {err: lyricssource.ErrMalformedResponse, code: CodeInvalidResult},
	} {
		t.Run(name, func(t *testing.T) {
			executor, err := NewSourceExecutor(searchSourceFunc(func(context.Context, lyricssource.MusicIdentity) ([]lyricssource.Candidate, error) {
				return nil, sourceErr.err
			}))
			if err != nil {
				t.Fatal(err)
			}
			_, err = executor.Discover(context.Background(), Job{MusicID: 10, JapaneseTitle: "合成試験曲", ProducerMetadata: "制作者"})
			failure := Classify(err)
			if failure.Code != sourceErr.code || failure.Retryable != sourceErr.retryable || strings.Contains(failure.Message, "503") {
				t.Fatalf("failure=%+v err=%v", failure, err)
			}
		})
	}
}

func testDiscoveryCandidate(t *testing.T, pageID, revisionID int, title, content string) lyricssource.Candidate {
	t.Helper()
	contentDigest := sha1.Sum([]byte(content))
	contentSHA1 := fmt.Sprintf("%x", contentDigest)
	raw, err := json.Marshal(map[string]any{
		"query": map[string]any{
			"pages": map[string]any{
				fmt.Sprint(pageID): map[string]any{
					"pageid": pageID,
					"title":  title,
					"categories": []map[string]any{
						{"title": "Category:Songs"},
					},
					"revisions": []map[string]any{
						{
							"revid": revisionID,
							"sha1":  contentSHA1,
							"slots": map[string]any{
								"main": map[string]any{"content": content},
							},
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	requestURL, err := lyricssource.CanonicalFandomSearchRequestURL(title)
	if err != nil {
		t.Fatal(err)
	}
	rawDigest := sha256.Sum256(raw)
	rawSHA256 := fmt.Sprintf("%x", rawDigest)
	const fetchedAt = "2026-07-31T12:00:00Z"
	evidenceID := lyricssource.MediaWikiSearchResponseAcquisitionEvidenceID(requestURL, fetchedAt, rawSHA256)

	canonical := url.URL{Scheme: "https", Host: "vocaloid.fandom.com", Path: "/wiki/" + strings.ReplaceAll(title, " ", "_")}
	canonicalQuery := canonical.Query()
	canonicalQuery.Set("oldid", fmt.Sprint(revisionID))
	canonical.RawQuery = canonicalQuery.Encode()

	return lyricssource.Candidate{
		Provider: lyricssource.ProviderVocaloidFandom, Origin: lyricssource.OriginVocaloidFandom,
		PageID: pageID, Title: title, CanonicalURL: canonical.String(), RevisionID: revisionID,
		SHA1: contentSHA1, Categories: []string{"Songs"},
		IndexEvidenceRefs: []model.LyricsSourceIndexEvidenceRef{{EvidenceID: evidenceID, SHA256: rawSHA256}},
		IndexEvidence: []lyricssource.IndexEvidence{{
			EvidenceID: evidenceID, SHA256: rawSHA256,
			Kind:     lyricssource.IndexEvidenceKindMediaWikiSearchResponse,
			Provider: lyricssource.ProviderVocaloidFandom, Origin: lyricssource.OriginVocaloidFandom,
			Categories: []string{}, CanonicalRequestURL: requestURL, FetchedAt: fetchedAt,
			Raw: append([]byte(nil), raw...), RawSHA256: rawSHA256,
		}},
	}
}
