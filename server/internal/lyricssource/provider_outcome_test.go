package lyricssource

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"moesekai/server/internal/lyricsprovideroutcome"
	"moesekai/server/internal/model"
)

func TestRegistrySearchOutcomesStopsAfterHighestAuthorityCandidate(t *testing.T) {
	sekaipedia := &outcomeStubProvider{id: ProviderSekaipedia, candidates: []Candidate{{
		Provider: ProviderSekaipedia, PageID: 2, RevisionID: 22,
	}}}
	moegirl := &outcomeStubProvider{id: ProviderMoegirl, searchErr: ErrUnsupportedTable}
	fandom := &outcomeStubProvider{id: ProviderVocaloidFandom, searchErr: &HTTPError{StatusCode: http.StatusServiceUnavailable}}
	registry, err := newRegistryWithProviders(fandom, moegirl, sekaipedia)
	if err != nil {
		t.Fatal(err)
	}

	outcomes, err := registry.SearchOutcomes(context.Background(), MusicIdentity{
		MusicID: 1, JapaneseTitle: "曲", ProducerMetadata: "作者",
	})
	if err != nil || len(outcomes) != 1 {
		t.Fatalf("provider outcomes=%+v err=%v", outcomes, err)
	}
	if outcomes[0].Provider != ProviderSekaipedia || outcomes[0].Status != lyricsprovideroutcome.StatusCandidate ||
		len(outcomes[0].Candidates) != 1 || outcomes[0].Candidates[0].Provider != ProviderSekaipedia {
		t.Fatalf("retained Sekaipedia outcome = %+v", outcomes[0])
	}
	if sekaipedia.searchCalls != 1 || moegirl.searchCalls != 0 || fandom.searchCalls != 0 {
		t.Fatalf("provider calls=%d/%d/%d", sekaipedia.searchCalls, moegirl.searchCalls, fandom.searchCalls)
	}
	candidates, err := registry.Search(context.Background(), MusicIdentity{
		MusicID: 1, JapaneseTitle: "曲", ProducerMetadata: "作者",
	})
	if err != nil || len(candidates) != 1 || candidates[0].Provider != ProviderSekaipedia ||
		sekaipedia.searchCalls != 2 || moegirl.searchCalls != 0 || fandom.searchCalls != 0 {
		t.Fatalf("flat authority result=%+v calls=%d/%d/%d err=%v",
			candidates, sekaipedia.searchCalls, moegirl.searchCalls, fandom.searchCalls, err)
	}
}

func TestRegistrySearchOutcomesMapsClosedProviderFailures(t *testing.T) {
	for _, test := range []struct {
		name      string
		err       error
		status    lyricsprovideroutcome.Status
		reason    lyricsprovideroutcome.ReasonCode
		retryable bool
	}{
		{name: "no match", err: ErrMissingLyrics, status: lyricsprovideroutcome.StatusNoMatch, reason: lyricsprovideroutcome.ReasonMissingLyrics},
		{name: "unsupported", err: ErrUnsupportedTable, status: lyricsprovideroutcome.StatusUnsupported, reason: lyricsprovideroutcome.ReasonUnsupportedFormat},
		{name: "stale", err: ErrRevisionChanged, status: lyricsprovideroutcome.StatusStale, reason: lyricsprovideroutcome.ReasonRevisionChanged},
		{name: "ambiguous", err: ErrAmbiguous, status: lyricsprovideroutcome.StatusAmbiguous, reason: lyricsprovideroutcome.ReasonAmbiguousMatch},
		{name: "transport", err: &HTTPError{StatusCode: http.StatusServiceUnavailable}, status: lyricsprovideroutcome.StatusTransportError, reason: lyricsprovideroutcome.ReasonTransport, retryable: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &outcomeStubProvider{id: ProviderSekaipedia, searchErr: test.err}
			registry, err := newRegistryWithProviders(provider)
			if err != nil {
				t.Fatal(err)
			}
			outcomes, err := registry.SearchOutcomes(context.Background(), MusicIdentity{})
			if err != nil || len(outcomes) != 1 {
				t.Fatalf("outcomes=%+v err=%v", outcomes, err)
			}
			if outcomes[0].Status != test.status || outcomes[0].Diagnostic.ReasonCode != test.reason ||
				outcomes[0].Retryable() != test.retryable {
				t.Fatalf("closed failure outcome = %+v", outcomes[0])
			}
		})
	}
}

func TestRegistrySearchOutcomesRejectsCrossProviderCandidateAndStopsFallback(t *testing.T) {
	sekaipedia := &outcomeStubProvider{id: ProviderSekaipedia}
	moegirl := &outcomeStubProvider{id: ProviderMoegirl, candidates: []Candidate{{
		Provider: ProviderVocaloidFandom, PageID: 3,
	}}}
	fandom := &outcomeStubProvider{id: ProviderVocaloidFandom, candidates: []Candidate{{
		Provider: ProviderVocaloidFandom, PageID: 4,
	}}}
	registry, err := newRegistryWithProviders(moegirl, fandom, sekaipedia)
	if err != nil {
		t.Fatal(err)
	}
	outcomes, err := registry.SearchOutcomes(context.Background(), MusicIdentity{})
	if err != nil || len(outcomes) != 2 {
		t.Fatalf("outcomes=%+v err=%v", outcomes, err)
	}
	if outcomes[0].Status != lyricsprovideroutcome.StatusNoMatch ||
		outcomes[1].Status != lyricsprovideroutcome.StatusUnsupported ||
		outcomes[1].Diagnostic.ReasonCode != lyricsprovideroutcome.ReasonMalformedResponse ||
		fandom.searchCalls != 0 {
		t.Fatalf("provider-scoped candidate enforcement = %+v fandomCalls=%d", outcomes, fandom.searchCalls)
	}
}

func TestRegistrySearchProviderOutcomeEvaluatesExactlyNamedProvider(t *testing.T) {
	sekaipedia := &outcomeStubProvider{id: ProviderSekaipedia, candidates: []Candidate{{
		Provider: ProviderSekaipedia, PageID: 2,
	}}}
	moegirl := &outcomeStubProvider{id: ProviderMoegirl, searchErr: ErrMissingLyrics}
	fandom := &outcomeStubProvider{id: ProviderVocaloidFandom, candidates: []Candidate{{
		Provider: ProviderVocaloidFandom, PageID: 4,
	}}}
	registry, err := newRegistryWithProviders(fandom, sekaipedia, moegirl)
	if err != nil {
		t.Fatal(err)
	}

	outcome, err := registry.SearchProviderOutcome(context.Background(), ProviderMoegirl, MusicIdentity{})
	if err != nil || outcome.Provider != ProviderMoegirl || outcome.Status != lyricsprovideroutcome.StatusNoMatch ||
		outcome.Diagnostic.ReasonCode != lyricsprovideroutcome.ReasonMissingLyrics || outcome.Validate() != nil {
		t.Fatalf("exact Moegirl outcome=%+v err=%v", outcome, err)
	}
	assertProviderDiagnosticContentFree(t, outcome.Diagnostic)
	if sekaipedia.searchCalls != 0 || moegirl.searchCalls != 1 || fandom.searchCalls != 0 {
		t.Fatalf("exact Moegirl calls=%d/%d/%d", sekaipedia.searchCalls, moegirl.searchCalls, fandom.searchCalls)
	}

	outcome, err = registry.SearchProviderOutcome(context.Background(), ProviderVocaloidFandom, MusicIdentity{})
	if err != nil || outcome.Provider != ProviderVocaloidFandom ||
		outcome.Status != lyricsprovideroutcome.StatusCandidate || len(outcome.Candidates) != 1 {
		t.Fatalf("exact Fandom outcome=%+v err=%v", outcome, err)
	}
	assertProviderDiagnosticContentFree(t, outcome.Diagnostic)
	if sekaipedia.searchCalls != 0 || moegirl.searchCalls != 1 || fandom.searchCalls != 1 {
		t.Fatalf("exact Fandom calls=%d/%d/%d", sekaipedia.searchCalls, moegirl.searchCalls, fandom.searchCalls)
	}

	if _, err := registry.SearchProviderOutcome(context.Background(), "other", MusicIdentity{}); err == nil {
		t.Fatal("invalid exact provider was accepted")
	}
	unconfigured, err := newRegistryWithProviders(sekaipedia)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unconfigured.SearchProviderOutcome(context.Background(), ProviderMoegirl, MusicIdentity{}); err == nil {
		t.Fatal("unconfigured exact provider was accepted")
	}
	if sekaipedia.searchCalls != 0 {
		t.Fatalf("unconfigured exact lookup invoked Sekaipedia %d times", sekaipedia.searchCalls)
	}
}

func TestSekaipediaFutureAuthorityConfigIsStructural(t *testing.T) {
	config := historicalSekaipediaProviderConfig()
	config.Indexes = []FixedIndex{{
		PageID: 268, RevisionID: 987654321, RevisionTimestamp: "2027-01-02T03:04:05Z",
		SHA1: strings.Repeat("a", 40), ContentSHA256: strings.Repeat("c", 64),
		RawSHA256: strings.Repeat("b", 64), Title: "List of songs",
	}}
	if err := config.validate(); err != nil {
		t.Fatalf("future Sekaipedia authority config: %v", err)
	}
	baseID, wantBaseID := sekaipediaAuthorityEvidenceID(config.Indexes[0]),
		"authority:sekaipedia:list-of-songs:987654321"
	if baseID != wantBaseID {
		t.Fatalf("future authority evidence base ID=%q want=%q", baseID, wantBaseID)
	}
	acquisitionID := MediaWikiRevisionAcquisitionEvidenceID(
		ProviderSekaipedia, baseID, config.Indexes[0].RevisionTimestamp, config.Indexes[0].RawSHA256,
	)
	if !strings.HasPrefix(acquisitionID, wantBaseID+":") {
		t.Fatalf("future authority acquisition ID=%q want prefix=%q", acquisitionID, wantBaseID+":")
	}
	ref := model.LyricsSourceIndexEvidenceRef{
		EvidenceID: acquisitionID, SHA256: config.Indexes[0].RawSHA256,
	}
	outcome, err := lyricsprovideroutcome.New[Candidate](
		ProviderSekaipedia,
		lyricsprovideroutcome.StatusNoMatch,
		nil,
		lyricsprovideroutcome.Diagnostic{
			Provider: ProviderSekaipedia, Phase: lyricsprovideroutcome.PhaseResolveTargets,
			ReasonCode:      lyricsprovideroutcome.ReasonNoMatch,
			Counts:          lyricsprovideroutcome.Counts{Acquisitions: 1, NoMatch: 1},
			AcquisitionRefs: []model.LyricsSourceIndexEvidenceRef{ref},
		},
	)
	if err != nil || len(outcome.Diagnostic.AcquisitionRefs) != 1 || outcome.Diagnostic.AcquisitionRefs[0] != ref {
		t.Fatalf("future authority outcome=%+v err=%v", outcome, err)
	}
	registry, err := NewRegistry(config)
	if err != nil {
		t.Fatal(err)
	}
	provider := registry.providers[ProviderSekaipedia].(*sekaipediaProvider)
	if provider.config.Indexes[0].RevisionID != 987654321 {
		t.Fatalf("future configured authority=%+v", provider.config.Indexes)
	}
}

func TestSekaipediaContributorAliasesAreExplicitBoundedImmutableConfig(t *testing.T) {
	if aliases := historicalSekaipediaProviderConfig().ContributorAliases; len(aliases) != 0 {
		t.Fatalf("default production aliases=%+v", aliases)
	} else if got := sekaipediaProviderCreditAliases(2, "みきとP", aliases); got != "みきとP" {
		t.Fatalf("default production alias changed contributor to %q", got)
	}
	reviewed := []ProviderContributorAlias{{
		MusicID: 2, CatalogContributor: "みきとP", ProviderContributor: "Mikito-P",
	}}
	config := historicalSekaipediaProviderConfig()
	config.ContributorAliases = reviewed
	if err := config.validate(); err != nil {
		t.Fatal(err)
	}
	provider := newSekaipediaProvider(config, nil)
	reviewed[0].MusicID = 999
	reviewed[0].ProviderContributor = "mutated"
	if got := sekaipediaProviderCreditAliases(
		2, "みきとP | みきとP | みきとP", provider.config.ContributorAliases,
	); got != "Mikito-P" {
		t.Fatalf("defensively copied reviewed alias=%q", got)
	}
	if got := sekaipediaProviderCreditAliases(
		999, "みきとP", provider.config.ContributorAliases,
	); got != "みきとP" {
		t.Fatalf("song-scoped alias leaked to another music ID: %q", got)
	}

	valid := ProviderContributorAlias{
		MusicID: 2, CatalogContributor: "みきとP", ProviderContributor: "Mikito-P",
	}
	for name, aliases := range map[string][]ProviderContributorAlias{
		"too many":           make([]ProviderContributorAlias, maxProviderContributorAliases+1),
		"zero music":         {{CatalogContributor: "みきとP", ProviderContributor: "Mikito-P"}},
		"empty catalog":      {{MusicID: 2, ProviderContributor: "Mikito-P"}},
		"composite catalog":  {{MusicID: 2, CatalogContributor: "A & B", ProviderContributor: "C"}},
		"untrimmed provider": {{MusicID: 2, CatalogContributor: "みきとP", ProviderContributor: " Mikito-P"}},
		"normalized no-op":   {{MusicID: 2, CatalogContributor: "MikitoP", ProviderContributor: "Mikito-P"}},
		"duplicate source":   {valid, valid},
	} {
		t.Run(name, func(t *testing.T) {
			if name == "too many" {
				for index := range aliases {
					aliases[index] = ProviderContributorAlias{
						MusicID: index + 1, CatalogContributor: "catalog" + string(rune('A'+index%26)),
						ProviderContributor: "provider" + string(rune('A'+index%26)),
					}
				}
			}
			invalid := historicalSekaipediaProviderConfig()
			invalid.ContributorAliases = aliases
			if err := invalid.validate(); err == nil {
				t.Fatal("invalid contributor alias plan was accepted")
			}
		})
	}

	fandom := DefaultProviderConfigs()[0]
	fandom.ContributorAliases = []ProviderContributorAlias{valid}
	if err := fandom.validate(); err == nil {
		t.Fatal("cross-provider contributor alias plan was accepted")
	}
}

func assertProviderDiagnosticContentFree(
	t *testing.T,
	diagnostic lyricsprovideroutcome.Diagnostic,
	forbiddenValues ...string,
) {
	t.Helper()
	body, err := json.Marshal(diagnostic)
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(body))
	for _, forbiddenField := range []string{
		`"title":`, `"url":`, `"lyrics":`, `"raw":`, `"wikitext":`, `"pageid":`, `"revisionid":`,
		`"romaji":`, `"romanized":`, `"romanization":`, `"parser":`,
	} {
		if strings.Contains(lower, forbiddenField) {
			t.Fatalf("provider diagnostic contains forbidden field %q: %s", forbiddenField, body)
		}
	}
	for _, forbiddenValue := range forbiddenValues {
		if forbiddenValue != "" && strings.Contains(lower, strings.ToLower(forbiddenValue)) {
			t.Fatalf("provider diagnostic contains forbidden content %q: %s", forbiddenValue, body)
		}
	}
}

type outcomeStubProvider struct {
	id          model.LyricsSourceProvider
	candidates  []Candidate
	searchErr   error
	searchCalls int
}

func (provider *outcomeStubProvider) ProviderID() model.LyricsSourceProvider {
	return provider.id
}

func (provider *outcomeStubProvider) Search(context.Context, MusicIdentity) ([]Candidate, error) {
	provider.searchCalls++
	return cloneProviderCandidates(provider.candidates), provider.searchErr
}

func (provider *outcomeStubProvider) FetchFixedCandidateRevision(
	context.Context,
	MusicIdentity,
	Candidate,
) (FixedRevision, error) {
	return FixedRevision{Provider: provider.id}, nil
}
