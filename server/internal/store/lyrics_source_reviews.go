package store

import (
	"time"

	"moesekai/server/internal/model"
)

const (
	LyricsSourceReviewKindCandidate = "candidate_selection"
	LyricsSourceReviewKindArtifact  = "artifact_review"

	LyricsSourceReviewStatePending    = "pending"
	LyricsSourceReviewStateApproved   = "approved"
	LyricsSourceReviewStateRejected   = "rejected"
	LyricsSourceReviewStateSuperseded = "superseded"

	LyricsSourceReviewGateIdentity  = "identity"
	LyricsSourceReviewGateSourceUse = "source_use"
	LyricsSourceReviewGateParse     = "parse"
	LyricsSourceReviewGateOverall   = "overall"

	LyricsSourceReviewDecisionApproved = "approved"
	LyricsSourceReviewDecisionRejected = "rejected"
)

type createLyricsSourceReviewParams struct {
	Provider           model.LyricsSourceProvider
	Kind               string
	AnalysisID         int64
	MusicID            int
	CatalogFingerprint string
	ReasonCode         string
	EvidenceJSON       []byte
	Priority           int
	CreatedAt          time.Time
}

type LyricsSourceReviewFilter struct {
	Kind     string
	State    string
	Gate     string
	Limit    int
	LimitSet bool
	Cursor   string
}

type LyricsSourceReviewSummary struct {
	ReviewID           int64     `json:"reviewId"`
	Kind               string    `json:"kind"`
	State              string    `json:"state"`
	MusicID            int       `json:"musicId"`
	Title              string    `json:"title"`
	CatalogFingerprint string    `json:"catalogFingerprint"`
	ReasonCode         string    `json:"reasonCode"`
	IdentityGate       string    `json:"identityGate"`
	SourceUseGate      string    `json:"sourceUseGate"`
	ParseGate          string    `json:"parseGate"`
	Version            int64     `json:"version"`
	Priority           int       `json:"priority"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
	CompletedAt        time.Time `json:"-"`
}

type LyricsSourceReviewPage struct {
	Items      []LyricsSourceReviewSummary `json:"items"`
	NextCursor string                      `json:"nextCursor,omitempty"`
}

type LyricsSourceArtifactFact struct {
	SourceType           string    `json:"sourceType"`
	SourceOrigin         string    `json:"sourceOrigin"`
	PageID               int       `json:"pageId"`
	RevisionID           int       `json:"revisionId"`
	PageTitle            string    `json:"pageTitle"`
	CanonicalRevisionURL string    `json:"canonicalRevisionUrl"`
	MediaWikiSHA1        string    `json:"mediaWikiSha1"`
	Categories           []string  `json:"categories"`
	FirstFetchedAt       time.Time `json:"firstFetchedAt"`
}

type LyricsSourceAnalysisFact struct {
	MatchingPolicyVersion    string                            `json:"matchingPolicyVersion"`
	RestrictionPolicyVersion string                            `json:"restrictionPolicyVersion"`
	ExtractorVersion         string                            `json:"extractorVersion"`
	MatchOutcome             string                            `json:"matchOutcome"`
	RestrictionOutcome       string                            `json:"restrictionOutcome"`
	ExtractionOutcome        string                            `json:"extractionOutcome"`
	MatchingEvidence         []model.LyricsSourceEvidence      `json:"matchingEvidence"`
	RestrictionRuleIDs       []string                          `json:"restrictionRuleIds"`
	SelectedVersion          model.LyricsSourceVersion         `json:"selectedVersion"`
	Performers               []model.LyricsSourcePerformer     `json:"performers"`
	RubyGeneratorVersion     string                            `json:"rubyGeneratorVersion"`
	ExtractedLines           []model.LyricsSourceExtractedLine `json:"extractedLines"`
}

type LyricsSourceAssociationFact struct {
	MusicID            int    `json:"musicId"`
	CatalogFingerprint string `json:"catalogFingerprint"`
	Kind               string `json:"kind"`
}

type LyricsSourceReviewDecisionFact struct {
	DecisionID        int64                                `json:"decisionId"`
	Gate              string                               `json:"gate"`
	Decision          string                               `json:"decision"`
	SelectedCandidate *model.LyricsSourceCandidateIdentity `json:"selectedCandidate,omitempty"`
	Actor             string                               `json:"actor"`
	Note              string                               `json:"note"`
	ExpectedVersion   int64                                `json:"expectedVersion"`
	ResultVersion     int64                                `json:"resultVersion"`
	DecidedAt         time.Time                            `json:"decidedAt"`
}

type LyricsSourceReviewDetail struct {
	Review       LyricsSourceReviewSummary             `json:"review"`
	Candidates   []model.LyricsSourceCandidateIdentity `json:"candidates"`
	Artifact     *LyricsSourceArtifactFact             `json:"artifact,omitempty"`
	Analysis     *LyricsSourceAnalysisFact             `json:"analysis,omitempty"`
	Associations []LyricsSourceAssociationFact         `json:"associations"`
	Decisions    []LyricsSourceReviewDecisionFact      `json:"decisions"`
}

type LyricsSourceReviewDecisionParams struct {
	ReviewID        int64
	Gate            string
	Decision        string
	ExpectedVersion int64
	Actor           string
	IdempotencyKey  string
	Note            string
	DecidedAt       time.Time
}

type LyricsSourceReviewBatchItem struct {
	ReviewID        int64 `json:"reviewId"`
	ExpectedVersion int64 `json:"expectedVersion"`
}

type LyricsSourceReviewBatchDecisionParams struct {
	Gate           string
	Decision       string
	Items          []LyricsSourceReviewBatchItem
	Actor          string
	IdempotencyKey string
	Note           string
	DecidedAt      time.Time
}

type LyricsSourceReviewBatchConflict struct {
	ReviewID int64  `json:"reviewId"`
	Reason   string `json:"reason"`
}

type LyricsSourceReviewBatchResult struct {
	Items     []model.LyricsSourceReviewItem
	Replayed  bool
	Conflicts []LyricsSourceReviewBatchConflict
}

type LyricsSourceCandidateSelectionParams struct {
	ReviewID          int64
	CandidateIdentity *model.LyricsSourceCandidateIdentity
	Exclude           bool
	ExpectedVersion   int64
	Actor             string
	IdempotencyKey    string
	Note              string
	DecidedAt         time.Time
}

type lyricsReviewCursor struct {
	Priority int   `json:"priority"`
	ReviewID int64 `json:"reviewId"`
}
