package lyricssource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"moesekai/server/internal/lyricsprovideroutcome"
)

func TestRecoverySekaipediaCanaryUsesOneExactPlanPinnedSongRevisionRequest(t *testing.T) {
	fixture := newSekaipediaFixtureServer(t)
	defer fixture.Close()
	provider, pinned := exactRecoverySekaipediaFixtureProvider(t, fixture, 330574)
	outcome, err := provider.SearchOutcome(t.Context(), rokiSekaipediaIdentity())
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != lyricsprovideroutcome.StatusCandidate || len(outcome.Candidates) != 1 ||
		outcome.Candidates[0].PageID != pinned.PageID || outcome.Candidates[0].RevisionID != pinned.RevisionID ||
		fixture.Requests() != 2 || fixture.ListRequests() != 1 || fixture.RevisionRequests() != 1 ||
		fixture.TitleRequests() != 0 {
		t.Fatalf("exact recovery request outcome=%+v requests=%d list=%d revision=%d title=%d", outcome,
			fixture.Requests(), fixture.ListRequests(), fixture.RevisionRequests(), fixture.TitleRequests())
	}
}

func TestRecoverySekaipediaCanaryChangedPinnedRevisionFailsClosed(t *testing.T) {
	fixture := newSekaipediaFixtureServer(t)
	defer fixture.Close()
	fixture.driftTarget.Store(true)
	provider, _ := exactRecoverySekaipediaFixtureProvider(t, fixture, 330574)
	outcome, err := provider.SearchOutcome(t.Context(), rokiSekaipediaIdentity())
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != lyricsprovideroutcome.StatusStale ||
		outcome.Diagnostic.ReasonCode != lyricsprovideroutcome.ReasonRevisionChanged || len(outcome.Candidates) != 0 ||
		fixture.Requests() != 2 || fixture.ListRequests() != 1 || fixture.RevisionRequests() != 1 ||
		fixture.TitleRequests() != 0 {
		t.Fatalf("changed exact revision outcome=%+v requests=%d list=%d revision=%d title=%d", outcome,
			fixture.Requests(), fixture.ListRequests(), fixture.RevisionRequests(), fixture.TitleRequests())
	}
}

func exactRecoverySekaipediaFixtureProvider(
	t *testing.T,
	fixture *sekaipediaFixtureServer,
	revisionID int,
) (*sekaipediaProvider, FixedIndex) {
	t.Helper()
	raw := fixture.revisions[revisionID]
	page, err := parsePageResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	contentSHA256 := sha256.Sum256([]byte(page.content))
	rawSHA256 := sha256.Sum256(raw)
	pinned := FixedIndex{
		PageID: page.pageID, RevisionID: page.revisionID, Title: page.title,
		RevisionTimestamp: page.revisionTimestamp.UTC().Format(time.RFC3339Nano),
		SHA1:              page.sha1,
		ContentSHA256:     hex.EncodeToString(contentSHA256[:]),
		RawSHA256:         hex.EncodeToString(rawSHA256[:]),
	}
	config := historicalSekaipediaProviderConfig()
	config.RecoveryExactCapture = true
	config, err = BindRecoverySekaipediaRevision(config, pinned)
	if err != nil {
		t.Fatal(err)
	}
	if config.RecoveryRevision == nil || config.RecoveryRevision.RawSHA256 != "" {
		t.Fatal("recovery revision incorrectly treated the raw envelope digest as semantic authority")
	}
	config.APIEndpoint = fixture.URL + "/w/api.php"
	config.CrawlDelay = 0
	config.CacheTTL = time.Hour
	return newSekaipediaProvider(
		config,
		newMediaWikiClient(config.APIEndpoint, config.CrawlDelay, config.CacheTTL, fixture.Client()),
	), pinned
}

func TestSekaipediaCanaryMalformedAndCanceledStagesFailClosed(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*sekaipediaFixtureServer) context.Context
		phase     lyricsprovideroutcome.Phase
		reason    lyricsprovideroutcome.ReasonCode
		status    lyricsprovideroutcome.Status
	}{
		{
			name: "malformed fixed List acquisition",
			configure: func(fixture *sekaipediaFixtureServer) context.Context {
				fixture.list = []byte(`{"query":{}}`)
				return context.Background()
			},
			phase: lyricsprovideroutcome.PhaseAcquireAuthority, reason: lyricsprovideroutcome.ReasonMalformedResponse,
			status: lyricsprovideroutcome.StatusUnsupported,
		},
		{
			name: "malformed target acquisition",
			configure: func(fixture *sekaipediaFixtureServer) context.Context {
				fixture.pages["Roki"] = []byte(`{"query":{}}`)
				return context.Background()
			},
			phase: lyricsprovideroutcome.PhaseAcquireTarget, reason: lyricsprovideroutcome.ReasonMalformedResponse,
			status: lyricsprovideroutcome.StatusUnsupported,
		},
		{
			name: "canceled fixed List acquisition",
			configure: func(*sekaipediaFixtureServer) context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			phase: lyricsprovideroutcome.PhaseAcquireAuthority, reason: lyricsprovideroutcome.ReasonCanceled,
			status: lyricsprovideroutcome.StatusTransportError,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSekaipediaFixtureServer(t)
			defer fixture.Close()
			outcome, err := fixture.Provider(t).SearchOutcome(test.configure(fixture), rokiSekaipediaIdentity())
			if err != nil {
				t.Fatal(err)
			}
			if outcome.Status != test.status || outcome.Diagnostic.ReasonCode != test.reason ||
				outcome.Diagnostic.Phase != test.phase || len(outcome.Candidates) != 0 {
				t.Fatalf("stage-specific fail-closed outcome=%+v", outcome)
			}
		})
	}
}
