package lyricssource

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"

	"moesekai/server/internal/lyricsprovideroutcome"
	"moesekai/server/internal/model"
)

type outcomeSourceProvider interface {
	SearchOutcome(context.Context, MusicIdentity) (lyricsprovideroutcome.Outcome[Candidate], error)
}

type providerOutcomeEvaluator interface {
	evaluateProviderSearch(context.Context, MusicIdentity) providerSearchResult
}

type providerSearchResult struct {
	outcome         lyricsprovideroutcome.Outcome[Candidate]
	legacyErr       error
	outcomeErr      error
	contractFailure bool
}

func (provider *fandomProvider) SearchOutcome(
	ctx context.Context,
	identity MusicIdentity,
) (lyricsprovideroutcome.Outcome[Candidate], error) {
	result := provider.evaluateProviderSearch(ctx, identity)
	return result.outcome, result.outcomeErr
}

func (provider *fandomProvider) evaluateProviderSearch(
	ctx context.Context,
	identity MusicIdentity,
) providerSearchResult {
	candidates, diagnostics, searchErr := provider.client.SearchWithDiagnostics(ctx, identity)
	counts := lyricsprovideroutcome.Counts{
		Targets: diagnostics.SearchHits, Evaluated: diagnostics.SearchHits, Candidates: len(candidates),
	}
	var (
		outcome    lyricsprovideroutcome.Outcome[Candidate]
		outcomeErr error
		legacyErr  = searchErr
	)
	if searchErr != nil {
		outcome, outcomeErr = providerSearchOutcome(ProviderVocaloidFandom, nil, searchErr, counts, nil)
		return providerSearchResult{outcome: outcome, legacyErr: legacyErr, outcomeErr: outcomeErr}
	}
	if len(candidates) != 0 {
		outcome, outcomeErr = providerSearchOutcome(
			ProviderVocaloidFandom, candidates, nil, counts, candidateAcquisitionRefs(candidates),
		)
		if len(candidates) > 1 {
			legacyErr = ErrAmbiguous
		}
		return providerSearchResult{outcome: outcome, legacyErr: legacyErr, outcomeErr: outcomeErr}
	}
	reason := lyricsprovideroutcome.ReasonNoMatch
	status := lyricsprovideroutcome.StatusNoMatch
	if zeroReason, ok := diagnostics.ZeroCandidateReason(); ok {
		switch zeroReason {
		case ZeroCandidateNoSearchHits:
			reason = lyricsprovideroutcome.ReasonNoSearchHits
		case ZeroCandidateTitleMismatch, ZeroCandidateCreditMismatch:
			reason = lyricsprovideroutcome.ReasonIdentityMismatch
		case ZeroCandidateRestricted:
			status = lyricsprovideroutcome.StatusUnsupported
			reason = lyricsprovideroutcome.ReasonRestrictedReprint
		case ZeroCandidateMissingSongSignal:
			reason = lyricsprovideroutcome.ReasonMissingSongSignal
		}
	}
	if status == lyricsprovideroutcome.StatusUnsupported {
		counts.Unsupported = 1
	} else {
		counts.NoMatch = 1
	}
	outcome, outcomeErr = newProviderSearchOutcome(
		ProviderVocaloidFandom, status, reason, lyricsprovideroutcome.PhaseMatchIdentity,
		nil, counts, nil,
	)
	return providerSearchResult{outcome: outcome, outcomeErr: outcomeErr}
}

func providerSearchOutcome(
	providerID model.LyricsSourceProvider,
	candidates []Candidate,
	searchErr error,
	counts lyricsprovideroutcome.Counts,
	refs []model.LyricsSourceIndexEvidenceRef,
) (lyricsprovideroutcome.Outcome[Candidate], error) {
	if searchErr != nil {
		status, phase, reason := classifyProviderSearchFailure(searchErr)
		incrementOutcomeFailureCount(&counts, status)
		return newProviderSearchOutcome(providerID, status, reason, phase, nil, counts, refs)
	}
	normalized, err := normalizeProviderCandidates(providerID, candidates)
	if err != nil {
		counts.Candidates = 0
		counts.Unsupported++
		return newProviderSearchOutcome(
			providerID, lyricsprovideroutcome.StatusUnsupported, lyricsprovideroutcome.ReasonMalformedResponse,
			lyricsprovideroutcome.PhaseFinalize, nil, counts, refs,
		)
	}
	counts.Candidates = len(normalized)
	switch len(normalized) {
	case 0:
		counts.NoMatch++
		return newProviderSearchOutcome(
			providerID, lyricsprovideroutcome.StatusNoMatch, lyricsprovideroutcome.ReasonNoMatch,
			lyricsprovideroutcome.PhaseMatchIdentity, nil, counts, refs,
		)
	case 1:
		return newProviderSearchOutcome(
			providerID, lyricsprovideroutcome.StatusCandidate, lyricsprovideroutcome.ReasonCandidate,
			lyricsprovideroutcome.PhaseFinalize, normalized, counts, refs,
		)
	default:
		counts.Ambiguous++
		return newProviderSearchOutcome(
			providerID, lyricsprovideroutcome.StatusAmbiguous, lyricsprovideroutcome.ReasonMultipleCandidates,
			lyricsprovideroutcome.PhaseFinalize, nil, counts, refs,
		)
	}
}

func newProviderSearchOutcome(
	providerID model.LyricsSourceProvider,
	status lyricsprovideroutcome.Status,
	reason lyricsprovideroutcome.ReasonCode,
	phase lyricsprovideroutcome.Phase,
	candidates []Candidate,
	counts lyricsprovideroutcome.Counts,
	refs []model.LyricsSourceIndexEvidenceRef,
) (lyricsprovideroutcome.Outcome[Candidate], error) {
	return lyricsprovideroutcome.New(
		providerID,
		status,
		cloneProviderCandidates(candidates),
		lyricsprovideroutcome.Diagnostic{
			Provider: providerID, Phase: phase, ReasonCode: reason, Counts: counts,
			AcquisitionRefs: append([]model.LyricsSourceIndexEvidenceRef(nil), refs...),
		},
	)
}

func classifyProviderSearchFailure(
	err error,
) (lyricsprovideroutcome.Status, lyricsprovideroutcome.Phase, lyricsprovideroutcome.ReasonCode) {
	switch {
	case errors.Is(err, context.Canceled):
		return lyricsprovideroutcome.StatusTransportError, lyricsprovideroutcome.PhaseAcquireTarget,
			lyricsprovideroutcome.ReasonCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return lyricsprovideroutcome.StatusTransportError, lyricsprovideroutcome.PhaseAcquireTarget,
			lyricsprovideroutcome.ReasonDeadlineExceeded
	case errors.Is(err, ErrRevisionChanged):
		return lyricsprovideroutcome.StatusStale, lyricsprovideroutcome.PhaseAcquireTarget,
			lyricsprovideroutcome.ReasonRevisionChanged
	case errors.Is(err, ErrAmbiguous):
		return lyricsprovideroutcome.StatusAmbiguous, lyricsprovideroutcome.PhaseResolveTargets,
			lyricsprovideroutcome.ReasonAmbiguousMatch
	case errors.Is(err, ErrMissingLyrics):
		return lyricsprovideroutcome.StatusNoMatch, lyricsprovideroutcome.PhaseParseLyrics,
			lyricsprovideroutcome.ReasonMissingLyrics
	case errors.Is(err, ErrCatalogRenditionConflict):
		return lyricsprovideroutcome.StatusUnsupported, lyricsprovideroutcome.PhaseParseLyrics,
			lyricsprovideroutcome.ReasonCatalogRenditionConflict
	case errors.Is(err, ErrUnsupportedTable):
		return lyricsprovideroutcome.StatusUnsupported, lyricsprovideroutcome.PhaseParseLyrics,
			lyricsprovideroutcome.ReasonUnsupportedFormat
	case errors.Is(err, ErrLyricsTooLarge):
		return lyricsprovideroutcome.StatusUnsupported, lyricsprovideroutcome.PhaseParseLyrics,
			lyricsprovideroutcome.ReasonLyricsTooLarge
	case errors.Is(err, ErrRestrictedReprint):
		return lyricsprovideroutcome.StatusUnsupported, lyricsprovideroutcome.PhaseMatchIdentity,
			lyricsprovideroutcome.ReasonRestrictedReprint
	case errors.Is(err, ErrMalformedResponse):
		return lyricsprovideroutcome.StatusUnsupported, lyricsprovideroutcome.PhaseFinalize,
			lyricsprovideroutcome.ReasonMalformedResponse
	}
	var sourceHTTP *HTTPError
	if errors.As(err, &sourceHTTP) {
		if sourceHTTP.StatusCode == http.StatusRequestTimeout || sourceHTTP.StatusCode == http.StatusTooEarly ||
			sourceHTTP.StatusCode == http.StatusTooManyRequests || sourceHTTP.StatusCode >= http.StatusInternalServerError {
			return lyricsprovideroutcome.StatusTransportError, lyricsprovideroutcome.PhaseAcquireTarget,
				lyricsprovideroutcome.ReasonTransport
		}
		return lyricsprovideroutcome.StatusUnsupported, lyricsprovideroutcome.PhaseAcquireTarget,
			lyricsprovideroutcome.ReasonMalformedResponse
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		if networkError.Timeout() {
			return lyricsprovideroutcome.StatusTransportError, lyricsprovideroutcome.PhaseAcquireTarget,
				lyricsprovideroutcome.ReasonDeadlineExceeded
		}
		return lyricsprovideroutcome.StatusTransportError, lyricsprovideroutcome.PhaseAcquireTarget,
			lyricsprovideroutcome.ReasonTransport
	}
	return lyricsprovideroutcome.StatusTransportError, lyricsprovideroutcome.PhaseAcquireTarget,
		lyricsprovideroutcome.ReasonTransport
}

func incrementOutcomeFailureCount(counts *lyricsprovideroutcome.Counts, status lyricsprovideroutcome.Status) {
	switch status {
	case lyricsprovideroutcome.StatusNoMatch:
		counts.NoMatch++
	case lyricsprovideroutcome.StatusUnsupported:
		counts.Unsupported++
	case lyricsprovideroutcome.StatusStale:
		counts.Stale++
	case lyricsprovideroutcome.StatusTransportError:
		counts.TransportErrors++
	case lyricsprovideroutcome.StatusAmbiguous:
		counts.Ambiguous++
	}
}

func normalizeProviderCandidates(
	providerID model.LyricsSourceProvider,
	candidates []Candidate,
) ([]Candidate, error) {
	result := cloneProviderCandidates(candidates)
	for index := range result {
		if result[index].Provider == "" {
			result[index].Provider = providerID
		}
		if result[index].Provider != providerID {
			return nil, ErrMalformedResponse
		}
	}
	return result, nil
}

func cloneProviderCandidates(candidates []Candidate) []Candidate {
	if candidates == nil {
		return nil
	}
	result := make([]Candidate, len(candidates))
	for index, candidate := range candidates {
		result[index] = candidate
		result[index].Categories = cloneIdentityCategories(candidate.Categories)
		result[index].IndexEvidenceRefs = cloneIndexEvidenceRefs(candidate.IndexEvidenceRefs)
		result[index].IndexEvidence = cloneStrictIndexEvidence(candidate.IndexEvidence)
	}
	return result
}

func candidateAcquisitionRefs(candidates []Candidate) []model.LyricsSourceIndexEvidenceRef {
	refs := []model.LyricsSourceIndexEvidenceRef{}
	for _, candidate := range candidates {
		refs = append(refs, candidate.IndexEvidenceRefs...)
	}
	return refs
}

// providerOutcomeAllowsFallback implements the authority chain without
// flattening provider-local failures into one aggregate error. Sekaipedia is
// always authoritative when it has a candidate. Moegirl may yield to Fandom
// only after a closed, candidate-free structural miss.
func providerOutcomeAllowsFallback(
	providerID model.LyricsSourceProvider,
	outcome lyricsprovideroutcome.Outcome[Candidate],
) bool {
	if outcome.Status == lyricsprovideroutcome.StatusCandidate || len(outcome.Candidates) != 0 {
		return false
	}
	switch providerID {
	case ProviderSekaipedia:
		switch outcome.Status {
		case lyricsprovideroutcome.StatusNoMatch,
			lyricsprovideroutcome.StatusStale,
			lyricsprovideroutcome.StatusAmbiguous:
			return true
		case lyricsprovideroutcome.StatusUnsupported:
			return outcome.Diagnostic.Phase == lyricsprovideroutcome.PhaseParseLyrics ||
				outcome.Diagnostic.Phase == lyricsprovideroutcome.PhaseFinalize
		}
	case ProviderMoegirl:
		if outcome.Diagnostic.Counts.Candidates != 0 {
			return false
		}
		if outcome.Status == lyricsprovideroutcome.StatusNoMatch {
			return true
		}
		return outcome.Status == lyricsprovideroutcome.StatusUnsupported &&
			outcome.Diagnostic.Phase == lyricsprovideroutcome.PhaseParseLyrics
	}
	return false
}

func (registry *Registry) Search(ctx context.Context, identity MusicIdentity) ([]Candidate, error) {
	results, err := registry.searchProviderResults(ctx, identity)
	if err != nil {
		return nil, err
	}
	for _, result := range results {
		if result.outcome.Status == lyricsprovideroutcome.StatusCandidate {
			return cloneProviderCandidates(result.outcome.Candidates), nil
		}
	}
	if len(results) == 0 {
		return []Candidate{}, nil
	}
	terminal := results[len(results)-1]
	if terminal.legacyErr != nil {
		return nil, terminal.legacyErr
	}
	if terminal.outcome.Provider == ProviderVocaloidFandom {
		return []Candidate{}, nil
	}
	if err := closedOutcomeLegacyError(terminal.outcome); err != nil {
		return nil, err
	}
	return []Candidate{}, nil
}

// SearchOutcomes follows the authoritative fallback chain and retains each
// attempted provider's closed outcome. A provider success stops fallback, so a
// later flat error cannot erase it. Retryable acquisition failures stop, while
// deterministic structural failures follow the provider-specific fallback
// policy without being mislabeled as absence.
func (registry *Registry) SearchOutcomes(
	ctx context.Context,
	identity MusicIdentity,
) ([]lyricsprovideroutcome.Outcome[Candidate], error) {
	results, err := registry.searchProviderResults(ctx, identity)
	if err != nil {
		return nil, err
	}
	outcomes := make([]lyricsprovideroutcome.Outcome[Candidate], len(results))
	for index := range results {
		outcomes[index] = results[index].outcome
	}
	return outcomes, nil
}

// SearchProviderOutcome evaluates exactly one explicitly named configured
// provider. It never traverses the authority fallback chain or selects a
// candidate from another provider; provider-local failures remain closed in the
// returned outcome.
func (registry *Registry) SearchProviderOutcome(
	ctx context.Context,
	providerID model.LyricsSourceProvider,
	identity MusicIdentity,
) (lyricsprovideroutcome.Outcome[Candidate], error) {
	if registry == nil || len(registry.providers) == 0 {
		return lyricsprovideroutcome.Outcome[Candidate]{}, errors.New("lyrics source registry is not configured")
	}
	if ctx == nil {
		return lyricsprovideroutcome.Outcome[Candidate]{}, errors.New("lyrics source outcome search requires context")
	}
	if !model.IsValidLyricsSourceProvider(providerID) {
		return lyricsprovideroutcome.Outcome[Candidate]{}, fmt.Errorf("lyrics source provider %q is invalid", providerID)
	}
	if registry.providers[providerID] == nil {
		return lyricsprovideroutcome.Outcome[Candidate]{}, fmt.Errorf("lyrics source provider %q is not configured", providerID)
	}
	result, err := validateProviderSearchResult(providerID, registry.evaluateProviderSearch(ctx, providerID, identity))
	if err != nil {
		return lyricsprovideroutcome.Outcome[Candidate]{}, err
	}
	return result.outcome, nil
}

func (registry *Registry) searchProviderResults(
	ctx context.Context,
	identity MusicIdentity,
) ([]providerSearchResult, error) {
	if registry == nil || len(registry.order) == 0 {
		return nil, errors.New("lyrics source registry is not configured")
	}
	if ctx == nil {
		return nil, errors.New("lyrics source outcome search requires context")
	}
	results := make([]providerSearchResult, 0, len(registry.order))
	for _, providerID := range registry.order {
		if registry.providers[providerID] == nil {
			return results, fmt.Errorf("lyrics source provider %q is not configured", providerID)
		}
		result, err := validateProviderSearchResult(
			providerID,
			registry.evaluateProviderSearch(ctx, providerID, identity),
		)
		if err != nil {
			return results, err
		}
		results = append(results, result)
		if result.contractFailure || !providerOutcomeAllowsFallback(providerID, result.outcome) {
			break
		}
	}
	return results, nil
}

func validateProviderSearchResult(
	providerID model.LyricsSourceProvider,
	result providerSearchResult,
) (providerSearchResult, error) {
	if result.outcomeErr == nil && result.outcome.Provider == providerID && result.outcome.Validate() == nil {
		return result, nil
	}
	outcome, outcomeErr := newProviderSearchOutcome(
		providerID, lyricsprovideroutcome.StatusUnsupported, lyricsprovideroutcome.ReasonMalformedResponse,
		lyricsprovideroutcome.PhaseFinalize, nil,
		lyricsprovideroutcome.Counts{Unsupported: 1}, nil,
	)
	if outcomeErr != nil {
		return providerSearchResult{}, outcomeErr
	}
	return providerSearchResult{
		outcome: outcome, legacyErr: ErrMalformedResponse, contractFailure: true,
	}, nil
}

func (registry *Registry) evaluateProviderSearch(
	ctx context.Context,
	providerID model.LyricsSourceProvider,
	identity MusicIdentity,
) providerSearchResult {
	if contextErr := ctx.Err(); contextErr != nil {
		return contextProviderSearchResult(providerID, contextErr)
	}
	provider := registry.providers[providerID]
	var result providerSearchResult
	if evaluator, ok := provider.(providerOutcomeEvaluator); ok {
		result = evaluator.evaluateProviderSearch(ctx, identity)
	} else if detailed, ok := provider.(outcomeSourceProvider); ok {
		result.outcome, result.outcomeErr = detailed.SearchOutcome(ctx, identity)
		result.legacyErr = closedOutcomeLegacyError(result.outcome)
	} else {
		candidates, searchErr := provider.Search(ctx, identity)
		result.outcome, result.outcomeErr = providerSearchOutcome(
			providerID, candidates, searchErr, lyricsprovideroutcome.Counts{}, candidateAcquisitionRefs(candidates),
		)
		result.legacyErr = searchErr
		if result.legacyErr == nil {
			result.legacyErr = closedOutcomeLegacyError(result.outcome)
		}
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return contextProviderSearchResult(providerID, contextErr)
	}
	return result
}

func contextProviderSearchResult(
	providerID model.LyricsSourceProvider,
	contextErr error,
) providerSearchResult {
	outcome, outcomeErr := providerSearchOutcome(
		providerID, nil, contextErr, lyricsprovideroutcome.Counts{}, nil,
	)
	return providerSearchResult{outcome: outcome, legacyErr: contextErr, outcomeErr: outcomeErr}
}

func closedOutcomeLegacyError(outcome lyricsprovideroutcome.Outcome[Candidate]) error {
	switch outcome.Status {
	case lyricsprovideroutcome.StatusCandidate, lyricsprovideroutcome.StatusNoMatch:
		if outcome.Diagnostic.ReasonCode == lyricsprovideroutcome.ReasonMissingLyrics {
			return ErrMissingLyrics
		}
		return nil
	case lyricsprovideroutcome.StatusStale:
		return ErrRevisionChanged
	case lyricsprovideroutcome.StatusAmbiguous:
		return ErrAmbiguous
	case lyricsprovideroutcome.StatusTransportError:
		switch outcome.Diagnostic.ReasonCode {
		case lyricsprovideroutcome.ReasonCanceled:
			return context.Canceled
		case lyricsprovideroutcome.ReasonDeadlineExceeded:
			return context.DeadlineExceeded
		default:
			return errors.New("lyrics source provider transport failed")
		}
	case lyricsprovideroutcome.StatusUnsupported:
		switch outcome.Diagnostic.ReasonCode {
		case lyricsprovideroutcome.ReasonUnsupportedFormat:
			return ErrUnsupportedTable
		case lyricsprovideroutcome.ReasonLyricsTooLarge:
			return ErrLyricsTooLarge
		case lyricsprovideroutcome.ReasonCatalogRenditionConflict:
			return ErrCatalogRenditionConflict
		case lyricsprovideroutcome.ReasonRestrictedReprint:
			return ErrRestrictedReprint
		default:
			return ErrMalformedResponse
		}
	default:
		return ErrMalformedResponse
	}
}
