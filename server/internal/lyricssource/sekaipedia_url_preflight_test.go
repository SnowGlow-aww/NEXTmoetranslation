package lyricssource

import (
	"encoding/json"
	"strings"
	"testing"

	"moesekai/server/internal/lyricsprovideroutcome"
)

func TestSekaipediaPageURLTargetForTitle(t *testing.T) {
	target, err := SekaipediaPageURLTargetForTitle("Memoria (song)")
	if err != nil {
		t.Fatal(err)
	}
	if target.PageTitle != "Memoria (song)" ||
		target.CanonicalURL != "https://www.sekaipedia.org/wiki/Memoria_%28song%29" {
		t.Fatalf("explicit URL target=%+v", target)
	}
	for _, invalid := range []string{"", " Memoria (song)", "Memoria\n(song)", "Memoria [song]"} {
		if _, err := SekaipediaPageURLTargetForTitle(invalid); err == nil {
			t.Fatalf("invalid explicit page title %q was accepted", invalid)
		}
	}
}

func TestSekaipediaListPageURLTargetsUseOfficialRomanizedTitles(t *testing.T) {
	raw := readSekaipediaFixture(t, "testdata/sekaipedia-list-335193.json")
	targets, err := SekaipediaListPageURLTargets(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 698 {
		t.Fatalf("URL targets=%d, want 698", len(targets))
	}
	if targets[0].PageTitle != "Tell Your World" ||
		targets[0].CanonicalURL != "https://www.sekaipedia.org/wiki/Tell_Your_World" {
		t.Fatalf("first URL target=%+v", targets[0])
	}
	var inochi *SekaipediaPageURLTarget
	for index := range targets {
		if targets[index].PageTitle == "Inochi ni Kirawarete iru." {
			inochi = &targets[index]
			break
		}
	}
	if inochi == nil || inochi.CanonicalURL != "https://www.sekaipedia.org/wiki/Inochi_ni_Kirawarete_iru." {
		t.Fatalf("trailing-period URL target=%+v", inochi)
	}
}

func TestSekaipediaURLExistenceResponsePreservesListOrderAndRecordsRedirects(t *testing.T) {
	targets := []SekaipediaPageURLTarget{
		{PageTitle: "Tell Your World", CanonicalURL: "https://www.sekaipedia.org/wiki/Tell_Your_World"},
		{PageTitle: "Inochi ni Kirawarete iru.", CanonicalURL: "https://www.sekaipedia.org/wiki/Inochi_ni_Kirawarete_iru."},
	}
	body, err := json.Marshal(map[string]any{
		"batchcomplete": true,
		"query": map[string]any{"pages": []map[string]any{
			{"pageid": 22, "ns": 0, "title": "Inochi ni Kirawarete iru."},
			{"pageid": 1, "ns": 0, "title": "Tell Your World"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	statuses, err := parseSekaipediaURLExistenceResponse(body, targets)
	if err != nil || len(statuses) != 2 || statuses[0].PageID != 1 || statuses[1].PageID != 22 ||
		statuses[0].ResolvedPageTitle != targets[0].PageTitle || statuses[0].Redirected {
		t.Fatalf("existence statuses=%+v err=%v", statuses, err)
	}

	redirectedBody, err := json.Marshal(map[string]any{
		"batchcomplete": true,
		"query": map[string]any{
			"redirects": []map[string]any{{"from": "Tell Your World", "to": "Tell Your World (song)"}},
			"pages": []map[string]any{
				{"pageid": 22, "ns": 0, "title": "Inochi ni Kirawarete iru."},
				{"pageid": 1, "ns": 0, "title": "Tell Your World (song)"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	redirected, err := parseSekaipediaURLExistenceResponse(redirectedBody, targets)
	if err != nil || !redirected[0].Redirected || redirected[0].ResolvedPageTitle != "Tell Your World (song)" ||
		redirected[0].ResolvedCanonicalURL != "https://www.sekaipedia.org/wiki/Tell_Your_World_%28song%29" {
		t.Fatalf("redirected statuses=%+v err=%v", redirected, err)
	}

	missing := strings.Replace(string(body), `"pageid":1`, `"missing":true,"pageid":-1`, 1)
	if _, err := parseSekaipediaURLExistenceResponse([]byte(missing), targets); err == nil {
		t.Fatal("missing List page URL was accepted")
	}
}

func TestRecoverySekaipediaReviewedExactTargetMayPostdateFixedList(t *testing.T) {
	fixture := newSekaipediaFixtureServer(t)
	defer fixture.Close()
	fixture.pages["Reviewed Roki"] = fixture.pages["Roki"]

	config := historicalSekaipediaProviderConfig()
	config.RecoveryExactCapture = true
	config.SekaipediaTargets = []SekaipediaPageTarget{{
		MusicID: 2, PageTitle: "Reviewed Roki", ResolvedPageTitle: "Roki",
	}}
	config.APIEndpoint = fixture.URL + "/w/api.php"
	config.CrawlDelay = 0
	provider := newSekaipediaProvider(
		config,
		newMediaWikiClient(config.APIEndpoint, 0, defaultProviderCacheTTL, fixture.Client()),
	)

	outcome, err := provider.SearchOutcome(t.Context(), rokiSekaipediaIdentity())
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != lyricsprovideroutcome.StatusCandidate || len(outcome.Candidates) != 1 ||
		outcome.Candidates[0].Title != "Roki" || outcome.Diagnostic.Counts.Acquisitions != 2 ||
		outcome.Diagnostic.Counts.Targets != 1 {
		t.Fatalf("reviewed post-List recovery outcome=%+v", outcome)
	}
	if fixture.ListRequests() != 1 || fixture.TitleRequests() != 1 || fixture.Requests() != 2 {
		t.Fatalf("reviewed post-List recovery requests total/list/title=%d/%d/%d",
			fixture.Requests(), fixture.ListRequests(), fixture.TitleRequests())
	}
}

func TestOrdinarySekaipediaReviewedTargetStillRequiresFixedListMembership(t *testing.T) {
	fixture := newSekaipediaFixtureServer(t)
	defer fixture.Close()
	fixture.pages["Reviewed Roki"] = fixture.pages["Roki"]

	config := historicalSekaipediaProviderConfig()
	config.SekaipediaTargets = []SekaipediaPageTarget{{
		MusicID: 2, PageTitle: "Reviewed Roki", ResolvedPageTitle: "Roki",
	}}
	config.APIEndpoint = fixture.URL + "/w/api.php"
	config.CrawlDelay = 0
	provider := newSekaipediaProvider(
		config,
		newMediaWikiClient(config.APIEndpoint, 0, defaultProviderCacheTTL, fixture.Client()),
	)

	outcome, err := provider.SearchOutcome(t.Context(), rokiSekaipediaIdentity())
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != lyricsprovideroutcome.StatusNoMatch ||
		outcome.Diagnostic.ReasonCode != lyricsprovideroutcome.ReasonNoSearchHits ||
		outcome.Diagnostic.Counts.Acquisitions != 1 || outcome.Diagnostic.Counts.Targets != 0 {
		t.Fatalf("ordinary post-List target outcome=%+v", outcome)
	}
	if fixture.ListRequests() != 1 || fixture.TitleRequests() != 0 || fixture.Requests() != 1 {
		t.Fatalf("ordinary post-List target bypassed List: total/list/title=%d/%d/%d",
			fixture.Requests(), fixture.ListRequests(), fixture.TitleRequests())
	}
}

func TestRecoverySekaipediaWithoutExactTargetMapDoesNotGuessJapaneseTitleURL(t *testing.T) {
	fixture := newSekaipediaFixtureServer(t)
	defer fixture.Close()
	config := historicalSekaipediaProviderConfig()
	config.RecoveryExactCapture = true
	config.SekaipediaTargets = nil
	provider := newSekaipediaProvider(config, newMediaWikiClient(fixture.URL+"/w/api.php", 0, defaultProviderCacheTTL, fixture.Client()))

	outcome, err := provider.SearchOutcome(t.Context(), rokiSekaipediaIdentity())
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != lyricsprovideroutcome.StatusNoMatch ||
		outcome.Diagnostic.ReasonCode != lyricsprovideroutcome.ReasonNoSearchHits ||
		outcome.Diagnostic.Counts.Acquisitions != 1 || outcome.Diagnostic.Counts.Targets != 0 {
		t.Fatalf("unmapped recovery outcome=%+v", outcome)
	}
	if fixture.ListRequests() != 1 || fixture.TitleRequests() != 0 || fixture.Requests() != 1 {
		t.Fatalf("unmapped recovery guessed a page request: total/list/title=%d/%d/%d",
			fixture.Requests(), fixture.ListRequests(), fixture.TitleRequests())
	}
}
