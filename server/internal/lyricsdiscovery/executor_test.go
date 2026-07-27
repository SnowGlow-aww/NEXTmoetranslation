package lyricsdiscovery

import (
	"context"
	"errors"
	"strings"
	"testing"

	"moesekai/server/internal/lyricssource"
)

type searchSourceFunc func(context.Context, lyricssource.MusicIdentity) ([]lyricssource.Candidate, error)

func (fn searchSourceFunc) Search(ctx context.Context, identity lyricssource.MusicIdentity) ([]lyricssource.Candidate, error) {
	return fn(ctx, identity)
}

func TestSourceExecutorProducesShadowCandidateSummary(t *testing.T) {
	for name, candidates := range map[string][]lyricssource.Candidate{
		"none": {},
		"one":  {{PageID: 12, Title: "合成試験曲", CanonicalURL: "https://vocaloid.fandom.com/wiki/Song?oldid=34", RevisionID: 34, SHA1: strings.Repeat("a", 40)}},
		"ambiguous": {
			{PageID: 12, Title: "合成試験曲", CanonicalURL: "https://vocaloid.fandom.com/wiki/Song_A?oldid=34", RevisionID: 34, SHA1: strings.Repeat("a", 40)},
			{PageID: 13, Title: "合成試験曲/制作者", CanonicalURL: "https://vocaloid.fandom.com/wiki/Song_B?oldid=35", RevisionID: 35, SHA1: strings.Repeat("b", 40)},
		},
	} {
		t.Run(name, func(t *testing.T) {
			executor, err := NewSourceExecutor(searchSourceFunc(func(_ context.Context, identity lyricssource.MusicIdentity) ([]lyricssource.Candidate, error) {
				if identity.MusicID != 10 || identity.JapaneseTitle != "合成試験曲" || identity.ProducerMetadata != "制作者" {
					t.Fatalf("identity=%+v", identity)
				}
				return candidates, nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			result, err := executor.Discover(context.Background(), Job{MusicID: 10, JapaneseTitle: "合成試験曲", ProducerMetadata: "制作者"})
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
