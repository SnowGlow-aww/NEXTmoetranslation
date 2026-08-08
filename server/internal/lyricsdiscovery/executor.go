package lyricsdiscovery

import (
	"context"
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

// SourceExecutor runs the bounded provider registry in shadow mode. Its private
// artifact carries compact candidate refs plus one deduplicated collection of
// bounded immutable index or search evidence bytes; no extracted lyric draft,
// publication, or projection data crosses this boundary, and public source
// documents retain references only.
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
	registry, err := lyricssource.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	return NewSourceExecutor(registry)
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
		Lyricist: job.Lyricist, Composer: job.Composer, Arranger: job.Arranger,
		PerformerSegmentationPolicy: job.PerformerSegmentationPolicy,
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
	artifact, err := MarshalCandidateArtifact(candidates)
	if err != nil {
		return Result{}, NewError(CodeInvalidResult, err)
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
	case errors.Is(err, lyricssource.ErrMissingLyrics):
		return NewError(CodeNoMatch, err)
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
