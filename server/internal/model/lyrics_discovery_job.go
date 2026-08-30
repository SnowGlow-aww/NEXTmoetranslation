package model

import "time"

type LyricsDiscoveryJobKind string

const (
	LyricsDiscoveryJobDiscover         LyricsDiscoveryJobKind = "discover"
	LyricsDiscoveryJobFetchRevision    LyricsDiscoveryJobKind = "fetch_revision"
	LyricsDiscoveryJobRevalidatePinned LyricsDiscoveryJobKind = "revalidate_pinned"
	LyricsDiscoveryJobRevalidateHead   LyricsDiscoveryJobKind = "revalidate_head"
)

func IsValidLyricsDiscoveryJobKind(kind LyricsDiscoveryJobKind) bool {
	switch kind {
	case LyricsDiscoveryJobDiscover, LyricsDiscoveryJobFetchRevision,
		LyricsDiscoveryJobRevalidatePinned, LyricsDiscoveryJobRevalidateHead:
		return true
	default:
		return false
	}
}

type LyricsDiscoveryJobState string

const (
	LyricsDiscoveryJobQueued     LyricsDiscoveryJobState = "queued"
	LyricsDiscoveryJobLeased     LyricsDiscoveryJobState = "leased"
	LyricsDiscoveryJobRetryWait  LyricsDiscoveryJobState = "retry_wait"
	LyricsDiscoveryJobSucceeded  LyricsDiscoveryJobState = "succeeded"
	LyricsDiscoveryJobDeadLetter LyricsDiscoveryJobState = "dead_letter"
	LyricsDiscoveryJobCancelled  LyricsDiscoveryJobState = "cancelled"
)

func IsTerminalLyricsDiscoveryJobState(state LyricsDiscoveryJobState) bool {
	switch state {
	case LyricsDiscoveryJobSucceeded, LyricsDiscoveryJobDeadLetter, LyricsDiscoveryJobCancelled:
		return true
	default:
		return false
	}
}

type LyricsDiscoveryJobTarget struct {
	MusicID            int
	PageID             int
	RevisionID         int
	ArtifactID         int64
	CatalogFingerprint string
	PolicyVersion      string
	ExpectedSHA1       string
	FixedCandidate     *LyricsSourceCandidateIdentity
}

type LyricsDiscoveryJob struct {
	ID             int64
	IdempotencyKey string
	Kind           LyricsDiscoveryJobKind
	State          LyricsDiscoveryJobState
	Target         LyricsDiscoveryJobTarget
	Attempts       int
	MaxAttempts    int
	NextAttemptAt  time.Time
	LeaseOwner     string
	LeaseExpiresAt time.Time
	LastErrorCode  string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	CompletedAt    time.Time
	Version        int64
}
