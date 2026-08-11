package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"moesekai/server/internal/lyricsdiscovery"
	"moesekai/server/internal/lyricssource"
	"moesekai/server/internal/model"
)

var _ lyricsdiscovery.FetchStore = (*LyricsSourceFetchAdapter)(nil)

type LyricsSourceFetchAdapter struct {
	store *Store
}

func NewLyricsSourceFetchAdapter(store *Store) (*LyricsSourceFetchAdapter, error) {
	if store == nil {
		return nil, errors.New("lyrics source fetch adapter requires store")
	}
	return &LyricsSourceFetchAdapter{store: store}, nil
}

func (a *LyricsSourceFetchAdapter) ClaimFetch(ctx context.Context, request lyricsdiscovery.ClaimRequest) (lyricsdiscovery.FetchJob, bool, error) {
	if ctx == nil {
		return lyricsdiscovery.FetchJob{}, false, errors.New("lyrics source fetch claim requires context")
	}
	var job model.LyricsDiscoveryJob
	var err error
	// Check the lower-volume providers first so a continuously populated legacy
	// Fandom queue cannot starve provider-aware Sekaipedia or Moegirl fetches.
	for _, provider := range []model.LyricsSourceProvider{
		model.LyricsSourceProviderSekaipedia,
		model.LyricsSourceProviderMoegirl,
		model.LyricsSourceProviderVocaloidFandom,
	} {
		job, err = a.store.ClaimLyricsDiscoveryJob(ctx, LyricsDiscoveryJobLease{
			Owner: request.WorkerID, Duration: request.LeaseDuration, Provider: provider,
			Kind: model.LyricsDiscoveryJobFetchRevision, Now: request.Now,
		})
		if !errors.Is(err, ErrLyricsDiscoveryJobNotFound) {
			break
		}
	}
	if errors.Is(err, ErrLyricsDiscoveryJobNotFound) {
		return lyricsdiscovery.FetchJob{}, false, nil
	}
	if err != nil {
		return lyricsdiscovery.FetchJob{}, false, err
	}
	provider, _, provenanceStatus, provenanceErr := a.store.GetLyricsDiscoveryJobProvenance(ctx, job.ID)
	fixedCandidate, candidateErr := loadLyricsDiscoveryFixedCandidateWithEvidenceContext(ctx, a.store.db, job.ID)
	if provenanceErr != nil || candidateErr != nil || fixedCandidate == nil ||
		(provenanceStatus != "candidate_complete" && provenanceStatus != "complete") ||
		job.Kind != model.LyricsDiscoveryJobFetchRevision || job.Target.PageID <= 0 || job.Target.RevisionID <= 0 ||
		!lyricsDiscoverySHA1Pattern.MatchString(job.Target.ExpectedSHA1) || !lyricsDiscoveryFingerprintPattern.MatchString(job.Target.CatalogFingerprint) ||
		job.Target.FixedCandidate == nil || validateProviderAwareLyricsDiscoveryCandidate(provider, stripLyricsCandidateIndexEvidence(*fixedCandidate)) != nil ||
		!sameLyricsSourceCandidateIdentity(*job.Target.FixedCandidate, *legacyLyricsDiscoveryCandidateIdentity(fixedCandidate)) {
		if failErr := a.failClaimedFetch(ctx, job, string(lyricsdiscovery.CodeInvalidJob)); failErr != nil {
			return lyricsdiscovery.FetchJob{}, false, fmt.Errorf("dead-letter invalid fetch job: %w", failErr)
		}
		return lyricsdiscovery.FetchJob{}, false, lyricsdiscovery.NewError(lyricsdiscovery.CodeInvalidJob, errors.Join(provenanceErr, candidateErr))
	}
	identity, err := a.store.CatalogMusicIdentityContext(ctx, job.Target.MusicID)
	if errors.Is(err, sql.ErrNoRows) {
		if failErr := a.failClaimedFetch(ctx, job, string(lyricsdiscovery.CodeInvalidJob)); failErr != nil {
			return lyricsdiscovery.FetchJob{}, false, failErr
		}
		return lyricsdiscovery.FetchJob{}, false, lyricsdiscovery.NewError(lyricsdiscovery.CodeInvalidJob, err)
	}
	if err != nil {
		return lyricsdiscovery.FetchJob{}, false, err
	}
	if identity.CatalogFingerprint != job.Target.CatalogFingerprint {
		if failErr := a.failClaimedFetch(ctx, job, string(lyricsdiscovery.CodeSourceDrift)); failErr != nil {
			return lyricsdiscovery.FetchJob{}, false, failErr
		}
		return lyricsdiscovery.FetchJob{}, false, lyricsdiscovery.NewError(lyricsdiscovery.CodeSourceDrift, nil)
	}
	return lyricsdiscovery.FetchJob{ID: strconv.FormatInt(job.ID, 10), LeaseToken: encodeLyricsDiscoveryLeaseToken(job.Version),
		Attempt: job.Attempts, MusicID: identity.MusicID, JapaneseTitle: identity.JapaneseTitle,
		ProducerMetadata: identity.ProducerMetadata, Lyricist: identity.Lyricist, Composer: identity.Composer,
		Arranger:                    identity.Arranger,
		PerformerSegmentationPolicy: lyricssource.PerformerSegmentationPolicyFromCatalogVocals(identity.Vocals),
		FixedCandidate:              cloneLyricsDiscoveryCandidate(*fixedCandidate),
	}, true, nil
}

func (a *LyricsSourceFetchAdapter) CompleteFetch(ctx context.Context, completion lyricsdiscovery.FetchCompletion) error {
	jobID, version, err := parseLyricsDiscoveryLease(completion.JobID, completion.LeaseToken, completion.WorkerID)
	if err != nil {
		return err
	}
	_, err = a.store.CompleteLyricsFetch(ctx, CompleteLyricsFetchParams{JobID: jobID, LeaseOwner: completion.WorkerID,
		ExpectedVersion: version, CompletedAt: completion.CompletedAt, Fixed: completion.Result.Fixed,
		Evidence: completion.Result.Evidence, Associations: completion.Result.Associations})
	return err
}

func (a *LyricsSourceFetchAdapter) RetryFetch(ctx context.Context, retry lyricsdiscovery.Retry) error {
	jobID, version, err := parseLyricsDiscoveryLease(retry.JobID, retry.LeaseToken, retry.WorkerID)
	if err != nil {
		return err
	}
	_, err = a.store.RetryLyricsDiscoveryJob(ctx, jobID, retry.WorkerID, version, retry.Attempt, retry.FailedAt,
		retry.NextAttemptAt, string(retry.Failure.Code))
	return err
}

func (a *LyricsSourceFetchAdapter) FailFetch(ctx context.Context, failure lyricsdiscovery.TerminalFailure) error {
	jobID, version, err := parseLyricsDiscoveryLease(failure.JobID, failure.LeaseToken, failure.WorkerID)
	if err != nil {
		return err
	}
	_, err = a.store.TerminalFailLyricsDiscoveryJob(ctx, jobID, failure.WorkerID, version, failure.Attempt,
		failure.FailedAt, string(failure.Failure.Code))
	return err
}

func (a *LyricsSourceFetchAdapter) failClaimedFetch(ctx context.Context, job model.LyricsDiscoveryJob, code string) error {
	_, err := a.store.TerminalFailLyricsDiscoveryJob(ctx, job.ID, job.LeaseOwner, job.Version, job.Attempts, time.Now().UTC(), code)
	return err
}
