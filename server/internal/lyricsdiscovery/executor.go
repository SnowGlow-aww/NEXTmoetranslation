package lyricsdiscovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"moesekai/server/internal/lyricssource"
)

type SearchSource interface {
	Search(context.Context, lyricssource.MusicIdentity) ([]lyricssource.Candidate, error)
}

// SourceExecutor runs the existing bounded Vocaloid Wiki search client in
// shadow mode. It persists only a compact candidate summary; no lyric text,
// source response body, draft, publication, or projection data crosses this
// boundary.
type SourceExecutor struct {
	source SearchSource
}

func NewSourceExecutor(source SearchSource) (*SourceExecutor, error) {
	if source == nil {
		return nil, errors.New("lyrics discovery source client is required")
	}
	return &SourceExecutor{source: source}, nil
}

func NewDefaultSourceExecutor() (*SourceExecutor, error) {
	return NewSourceExecutor(lyricssource.New())
}

func (e *SourceExecutor) Discover(ctx context.Context, job Job) (Result, error) {
	if ctx == nil {
		return Result{}, NewError(CodeInvalidJob, errors.New("discovery context is required"))
	}
	if job.MusicID <= 0 || strings.TrimSpace(job.JapaneseTitle) == "" || strings.TrimSpace(job.ProducerMetadata) == "" {
		return Result{}, NewError(CodeInvalidJob, errors.New("catalog identity is incomplete"))
	}
	candidates, err := e.source.Search(ctx, lyricssource.MusicIdentity{
		MusicID: job.MusicID, JapaneseTitle: job.JapaneseTitle, ProducerMetadata: job.ProducerMetadata,
	})
	if err != nil {
		return Result{}, classifySourceError(err)
	}
	outcome := OutcomeNoCandidates
	if len(candidates) == 1 {
		outcome = OutcomeCandidatesFound
	} else if len(candidates) > 1 {
		outcome = OutcomeAmbiguous
	}
	artifact, err := json.Marshal(struct {
		Candidates []lyricssource.Candidate `json:"candidates"`
	}{Candidates: candidates})
	if err != nil {
		return Result{}, NewError(CodeInternal, err)
	}
	return Result{Outcome: outcome, CandidateCount: len(candidates), Artifact: artifact}, nil
}

func classifySourceError(err error) error {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, lyricssource.ErrRestrictedReprint):
		return NewError(CodeRestricted, err)
	case errors.Is(err, lyricssource.ErrAmbiguous):
		return NewError(CodeAmbiguous, err)
	case errors.Is(err, lyricssource.ErrRevisionChanged):
		return NewError(CodeSourceDrift, err)
	case errors.Is(err, lyricssource.ErrUnsupportedTable):
		return NewError(CodeUnsupported, err)
	case errors.Is(err, lyricssource.ErrLyricsTooLarge), errors.Is(err, lyricssource.ErrMalformedResponse):
		return NewError(CodeInvalidResult, err)
	}
	var sourceHTTP *lyricssource.HTTPError
	if errors.As(err, &sourceHTTP) {
		switch {
		case sourceHTTP.StatusCode == 429:
			return NewError(CodeRateLimited, err)
		case sourceHTTP.StatusCode >= 500:
			return NewError(CodeSourceUnavailable, err)
		default:
			return NewError(CodeInvalidResult, err)
		}
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		if networkError.Timeout() {
			return NewError(CodeTimeout, err)
		}
		return NewError(CodeSourceUnavailable, err)
	}
	var urlError *url.Error
	if errors.As(err, &urlError) {
		return NewError(CodeSourceUnavailable, err)
	}
	return NewError(CodeSourceUnavailable, fmt.Errorf("source search failed: %w", err))
}
