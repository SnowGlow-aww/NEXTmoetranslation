package main

import (
	"encoding/json"
	"strings"
	"testing"
)

const testMoegirlURL = "https://zh.moegirl.org.cn/%E4%BA%BF%E5%B9%B4%E7%88%B1%E6%81%8B"

func TestAssembleExactProviderScope(t *testing.T) {
	scope, base, gap, moegirl, catalog, pins, catalogSHA := validAssemblyFixture()
	result, err := assemble(scope, base, gap, moegirl, catalog, pins, catalogSHA)
	if err != nil {
		t.Fatal(err)
	}
	if result.MappingCount != 3 || result.SekaipediaCount != 2 || result.MoegirlPublicExactCount != 1 {
		t.Fatalf("unexpected provider counts: %+v", result)
	}
	if len(result.ExcludedMusic) != 1 || result.ExcludedMusic[0].MusicID != 4 {
		t.Fatalf("unexpected exclusions: %+v", result.ExcludedMusic)
	}
	if result.Mappings[2].MusicID != 3 || result.Mappings[2].Provider != providerMoegirlExact ||
		result.Mappings[2].MoegirlPublicExact == nil || result.Mappings[2].MoegirlPublicExact.PageURL != testMoegirlURL {
		t.Fatalf("unexpected exact public-page mapping: %+v", result.Mappings[2])
	}
	body, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "Japanese line") || strings.Contains(string(body), "Chinese line") ||
		strings.Contains(strings.ToLower(string(body)), "romaji") || strings.Contains(string(body), "moegirl.icu") {
		t.Fatalf("assembled map leaked lyrics or an unauthorized source: %s", body)
	}
}

func TestAssembleRejectsDuplicateSekaipediaPageIdentity(t *testing.T) {
	scope, base, gap, moegirl, catalog, pins, catalogSHA := validAssemblyFixture()
	gap.Mappings[0].PageID = base.Mappings[0].PageID
	if _, err := assemble(scope, base, gap, moegirl, catalog, pins, catalogSHA); err == nil || !strings.Contains(err.Error(), "page ID") {
		t.Fatalf("expected duplicate page identity rejection, got %v", err)
	}
}

func TestValidateScopeRejectsICUURL(t *testing.T) {
	scope, _, _, _, catalog, _, _ := validAssemblyFixture()
	scope.ExactPublicPage.PageURL = "https://moegirl.icu/api.php"
	if err := validateScope(scope, catalog); err == nil || !strings.Contains(err.Error(), "complete zh.moegirl.org.cn") {
		t.Fatalf("expected ICU URL rejection, got %v", err)
	}
}

func validAssemblyFixture() (
	scopeInput,
	sekaipediaMapReport,
	sekaipediaMapReport,
	moegirlExtractionReport,
	map[int]catalogSong,
	inputPins,
	string,
) {
	catalogSHA := strings.Repeat("c", 64)
	catalog := map[int]catalogSong{
		1: {MusicID: 1, JapaneseTitle: "One"},
		2: {MusicID: 2, JapaneseTitle: "Two"},
		3: {MusicID: 3, JapaneseTitle: "一億年恋してる"},
		4: {MusicID: 4, JapaneseTitle: "Excluded"},
	}
	scope := scopeInput{
		SchemaVersion: 1, CatalogCount: 4, MappingCount: 3, BaseSekaipediaMappingCount: 1,
		ProviderCounts:             map[string]int{providerSekaipedia: 2, providerMoegirlExact: 1},
		RequiredSekaipediaMappings: []requiredSekaipedia{{MusicID: 2, PageTitle: "Gap_Page"}},
		ExactPublicPage:            exactPublicPage{Provider: providerMoegirlExact, MusicID: 3, PageURL: testMoegirlURL},
		ExcludedMusicIDs:           []int{4},
	}
	baseMapping := testSekaipediaMapping(1, "One", "Base_Page", 101, 1001)
	gapMapping := testSekaipediaMapping(2, "Two", "Gap_Page", 102, 1002)
	base := testSekaipediaReport(catalogSHA, []sekaipediaMapping{baseMapping})
	gap := testSekaipediaReport(catalogSHA, []sekaipediaMapping{gapMapping})
	moegirl := moegirlExtractionReport{
		SchemaVersion: 1, Provider: providerMoegirlExact,
		URLReportSHA256: strings.Repeat("a", 64), RawHTMLSHA256: strings.Repeat("b", 64), CatalogSHA256: catalogSHA,
		Catalog: catalogIdentity{MusicID: 3, JapaneseTitle: "一億年恋してる", LyricsVersion: "game_size"},
		PageURL: testMoegirlURL, PageTitle: "亿年爱恋", JapaneseTitle: "一億年恋してる",
		PageID: 103, RevisionID: 1003, FetchedAt: "2026-08-03T14:58:50.501307Z", RightsNotice: "rights",
		LineCount: 1, StanzaCount: 1,
		Lines: []moegirlLine{{Japanese: "Japanese line", Translation: "Chinese line"}},
	}
	pins := inputPins{
		ScopeSHA256: strings.Repeat("d", 64), BaseSekaipediaReportSHA256: strings.Repeat("e", 64),
		GapSekaipediaReportSHA256: strings.Repeat("f", 64), MoegirlExtractionSHA256: strings.Repeat("1", 64),
	}
	return scope, base, gap, moegirl, catalog, pins, catalogSHA
}

func testSekaipediaReport(catalogSHA string, mappings []sekaipediaMapping) sekaipediaMapReport {
	return sekaipediaMapReport{
		SchemaVersion: 1, Provider: providerSekaipedia, URLReportSHA256: strings.Repeat("2", 64), CatalogSHA256: catalogSHA,
		SourceTargetCount: len(mappings), MetadataMappedCount: len(mappings), CatalogMappedCount: len(mappings),
		CheckedAt: "2026-08-03T14:28:00.847189Z", Complete: true,
		Mappings: mappings, SourceOnly: []json.RawMessage{}, UnsupportedSourcePages: []json.RawMessage{},
		CatalogExcluded: []catalogSong{}, MetadataBatches: []json.RawMessage{json.RawMessage(`{}`)},
	}
}

func testSekaipediaMapping(musicID int, title, pageTitle string, pageID, revisionID int) sekaipediaMapping {
	pageURL := "https://www.sekaipedia.org/wiki/" + pageTitle
	return sekaipediaMapping{
		MusicID: musicID, CatalogJapaneseTitle: title, PageTitle: pageTitle,
		CanonicalURL: pageURL, ResolvedPageTitle: pageTitle, ResolvedCanonicalURL: pageURL,
		PageID: pageID, RevisionID: revisionID, RevisionTimestamp: "2026-08-03T14:00:00Z",
		SHA1: strings.Repeat("3", 40),
	}
}
