package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"moesekai/server/internal/lyricssource"
)

const (
	providerSekaipedia       = "sekaipedia"
	providerMoegirlExact     = "moegirl_public_exact"
	musicIDSetEncoding       = "decimal-newline-v1"
	sekaipediaCanonicalHost  = "www.sekaipedia.org"
	maximumPinnedReportBytes = 16 << 20
)

var (
	canonicalSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)
	canonicalSHA1   = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

type options struct {
	scopePath, expectedScopeSHA     string
	basePath, expectedBaseSHA       string
	gapPath, expectedGapSHA         string
	moegirlPath, expectedMoegirlSHA string
	catalogPath, expectedCatalogSHA string
	outputPath                      string
}

type scopeInput struct {
	SchemaVersion              int                  `json:"schemaVersion"`
	CatalogCount               int                  `json:"catalogCount"`
	MappingCount               int                  `json:"mappingCount"`
	BaseSekaipediaMappingCount int                  `json:"baseSekaipediaMappingCount"`
	ProviderCounts             map[string]int       `json:"providerCounts"`
	RequiredSekaipediaMappings []requiredSekaipedia `json:"requiredSekaipediaMappings"`
	ExactPublicPage            exactPublicPage      `json:"exactPublicPage"`
	ExcludedMusicIDs           []int                `json:"excludedMusicIds"`
}

type requiredSekaipedia struct {
	MusicID   int    `json:"musicId"`
	PageTitle string `json:"pageTitle"`
}

type exactPublicPage struct {
	Provider string `json:"provider"`
	MusicID  int    `json:"musicId"`
	PageURL  string `json:"pageUrl"`
}

type sekaipediaMapReport struct {
	SchemaVersion          int                 `json:"schemaVersion"`
	Provider               string              `json:"provider"`
	URLReportSHA256        string              `json:"urlReportSha256"`
	CatalogSHA256          string              `json:"catalogSha256"`
	SourceTargetCount      int                 `json:"sourceTargetCount"`
	MetadataMappedCount    int                 `json:"metadataMappedCount"`
	CatalogMappedCount     int                 `json:"catalogMappedCount"`
	SourceOnlyCount        int                 `json:"sourceOnlyCount"`
	CatalogExcludedCount   int                 `json:"catalogExcludedCount"`
	CheckedAt              string              `json:"checkedAt"`
	Complete               bool                `json:"complete"`
	Failure                string              `json:"failure,omitempty"`
	Mappings               []sekaipediaMapping `json:"mappings"`
	SourceOnly             []json.RawMessage   `json:"sourceOnly"`
	UnsupportedSourcePages []json.RawMessage   `json:"unsupportedSourcePages"`
	CatalogExcluded        []catalogSong       `json:"catalogExcluded"`
	MetadataBatches        []json.RawMessage   `json:"metadataBatches"`
}

type sekaipediaMapping struct {
	MusicID                 int    `json:"musicId"`
	CatalogJapaneseTitle    string `json:"catalogJapaneseTitle"`
	SekaipediaJapaneseTitle string `json:"sekaipediaJapaneseTitle,omitempty"`
	PageTitle               string `json:"pageTitle"`
	CanonicalURL            string `json:"canonicalUrl"`
	ResolvedPageTitle       string `json:"resolvedPageTitle"`
	ResolvedCanonicalURL    string `json:"resolvedCanonicalUrl"`
	PageID                  int    `json:"pageId"`
	RevisionID              int    `json:"revisionId"`
	RevisionTimestamp       string `json:"revisionTimestamp"`
	SHA1                    string `json:"sha1"`
}

type moegirlExtractionReport struct {
	SchemaVersion   int             `json:"schemaVersion"`
	Provider        string          `json:"provider"`
	URLReportSHA256 string          `json:"urlReportSha256"`
	RawHTMLSHA256   string          `json:"rawHtmlSha256"`
	CatalogSHA256   string          `json:"catalogSha256"`
	Catalog         catalogIdentity `json:"catalog"`
	PageURL         string          `json:"pageUrl"`
	PageTitle       string          `json:"pageTitle"`
	JapaneseTitle   string          `json:"japaneseTitle"`
	PageID          int             `json:"pageId"`
	RevisionID      int             `json:"revisionId"`
	FetchedAt       string          `json:"fetchedAt"`
	RightsNotice    string          `json:"rightsNotice"`
	LineCount       int             `json:"lineCount"`
	StanzaCount     int             `json:"stanzaCount"`
	Lines           []moegirlLine   `json:"lines"`
}

type catalogIdentity struct {
	MusicID          int    `json:"musicId"`
	JapaneseTitle    string `json:"japaneseTitle"`
	ProducerMetadata string `json:"producerMetadata"`
	Lyricist         string `json:"lyricist"`
	Composer         string `json:"composer"`
	Arranger         string `json:"arranger"`
	LyricsVersion    string `json:"lyricsVersion"`
}

type moegirlLine struct {
	Japanese          string `json:"japanese"`
	Translation       string `json:"translation"`
	StanzaBreakBefore bool   `json:"stanzaBreakBefore,omitempty"`
}

type catalogSong struct {
	MusicID       int    `json:"musicId"`
	JapaneseTitle string `json:"japaneseTitle"`
}

type inputPins struct {
	ScopeSHA256                string `json:"scopeSha256"`
	BaseSekaipediaReportSHA256 string `json:"baseSekaipediaReportSha256"`
	GapSekaipediaReportSHA256  string `json:"gapSekaipediaReportSha256"`
	MoegirlExtractionSHA256    string `json:"moegirlExtractionSha256"`
}

type assembledMapping struct {
	MusicID              int                  `json:"musicId"`
	CatalogJapaneseTitle string               `json:"catalogJapaneseTitle"`
	Provider             string               `json:"provider"`
	Sekaipedia           *sekaipediaMapping   `json:"sekaipedia,omitempty"`
	MoegirlPublicExact   *moegirlExactMapping `json:"moegirlPublicExact,omitempty"`
}

type moegirlExactMapping struct {
	PageURL                string `json:"pageUrl"`
	PageTitle              string `json:"pageTitle"`
	JapaneseTitle          string `json:"japaneseTitle"`
	PageID                 int    `json:"pageId"`
	RevisionID             int    `json:"revisionId"`
	FetchedAt              string `json:"fetchedAt"`
	URLReportSHA256        string `json:"urlReportSha256"`
	RawHTMLSHA256          string `json:"rawHtmlSha256"`
	ExtractionReportSHA256 string `json:"extractionReportSha256"`
	LineCount              int    `json:"lineCount"`
	StanzaCount            int    `json:"stanzaCount"`
}

type assembledReport struct {
	SchemaVersion           int                `json:"schemaVersion"`
	CatalogSHA256           string             `json:"catalogSha256"`
	CatalogCount            int                `json:"catalogCount"`
	Inputs                  inputPins          `json:"inputs"`
	MappingCount            int                `json:"mappingCount"`
	SekaipediaCount         int                `json:"sekaipediaCount"`
	MoegirlPublicExactCount int                `json:"moegirlPublicExactCount"`
	MusicIDSetEncoding      string             `json:"musicIdSetEncoding"`
	MusicIDSetSHA256        string             `json:"musicIdSetSha256"`
	MappingsSHA256          string             `json:"mappingsSha256"`
	ExcludedMusic           []catalogSong      `json:"excludedMusic"`
	Mappings                []assembledMapping `json:"mappings"`
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(arguments []string, output io.Writer) error {
	parsed, err := parseOptions(arguments)
	if err != nil {
		return err
	}
	scopeRaw, err := readPinnedFile(parsed.scopePath, parsed.expectedScopeSHA, 1<<20)
	if err != nil {
		return err
	}
	baseRaw, err := readPinnedFile(parsed.basePath, parsed.expectedBaseSHA, maximumPinnedReportBytes)
	if err != nil {
		return err
	}
	gapRaw, err := readPinnedFile(parsed.gapPath, parsed.expectedGapSHA, maximumPinnedReportBytes)
	if err != nil {
		return err
	}
	moegirlRaw, err := readPinnedFile(parsed.moegirlPath, parsed.expectedMoegirlSHA, maximumPinnedReportBytes)
	if err != nil {
		return err
	}
	catalogRaw, err := readPinnedFile(parsed.catalogPath, parsed.expectedCatalogSHA, 64<<20)
	if err != nil {
		return err
	}
	_ = catalogRaw

	var scope scopeInput
	var base, gap sekaipediaMapReport
	var moegirl moegirlExtractionReport
	if err := decodeStrict(scopeRaw, &scope); err != nil {
		return fmt.Errorf("decode target-map scope: %w", err)
	}
	if err := decodeStrict(baseRaw, &base); err != nil {
		return fmt.Errorf("decode base Sekaipedia report: %w", err)
	}
	if err := decodeStrict(gapRaw, &gap); err != nil {
		return fmt.Errorf("decode gap Sekaipedia report: %w", err)
	}
	if err := decodeStrict(moegirlRaw, &moegirl); err != nil {
		return fmt.Errorf("decode exact Moegirl report: %w", err)
	}
	catalog, err := readCatalog(parsed.catalogPath, scope.CatalogCount)
	if err != nil {
		return err
	}
	defer catalog.Close()
	catalogSongs, err := readCatalogSongs(catalog)
	if err != nil {
		return err
	}

	result, err := assemble(scope, base, gap, moegirl, catalogSongs, inputPins{
		ScopeSHA256:                parsed.expectedScopeSHA,
		BaseSekaipediaReportSHA256: parsed.expectedBaseSHA,
		GapSekaipediaReportSHA256:  parsed.expectedGapSHA,
		MoegirlExtractionSHA256:    parsed.expectedMoegirlSHA,
	}, parsed.expectedCatalogSHA)
	if err != nil {
		return err
	}
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if err := writePrivateExclusive(parsed.outputPath, append(body, '\n')); err != nil {
		return err
	}
	_, err = fmt.Fprintf(output,
		"PASS mode=lyrics-target-map-assemble mappings=%d sekaipedia=%d moegirlPublicExact=%d exclusions=%d output=%s\n",
		result.MappingCount, result.SekaipediaCount, result.MoegirlPublicExactCount, len(result.ExcludedMusic), parsed.outputPath,
	)
	return err
}

func assemble(
	scope scopeInput,
	base, gap sekaipediaMapReport,
	moegirl moegirlExtractionReport,
	catalogSongs map[int]catalogSong,
	pins inputPins,
	catalogSHA string,
) (assembledReport, error) {
	if err := validateScope(scope, catalogSongs); err != nil {
		return assembledReport{}, err
	}
	if err := validateSekaipediaReport(base, catalogSHA, scope.BaseSekaipediaMappingCount, false); err != nil {
		return assembledReport{}, fmt.Errorf("base Sekaipedia report: %w", err)
	}
	if err := validateSekaipediaReport(gap, catalogSHA, len(scope.RequiredSekaipediaMappings), true); err != nil {
		return assembledReport{}, fmt.Errorf("gap Sekaipedia report: %w", err)
	}
	required := make(map[int]string, len(scope.RequiredSekaipediaMappings))
	for _, item := range scope.RequiredSekaipediaMappings {
		required[item.MusicID] = item.PageTitle
	}
	if len(gap.Mappings) != len(required) {
		return assembledReport{}, errors.New("gap Sekaipedia report does not contain the exact required mapping set")
	}
	for _, item := range gap.Mappings {
		if required[item.MusicID] != item.PageTitle {
			return assembledReport{}, fmt.Errorf("gap Sekaipedia mapping music ID %d has unexpected page title", item.MusicID)
		}
	}
	if err := validateMoegirlReport(scope, moegirl, catalogSHA, pins.MoegirlExtractionSHA256, catalogSongs); err != nil {
		return assembledReport{}, err
	}

	mappingByMusicID := make(map[int]assembledMapping, scope.MappingCount)
	sekaPageIDs := make(map[int]int, scope.ProviderCounts[providerSekaipedia])
	sekaCanonicalURLs := make(map[string]int, scope.ProviderCounts[providerSekaipedia])
	sekaResolvedURLs := make(map[string]int, scope.ProviderCounts[providerSekaipedia])
	for _, source := range append(append([]sekaipediaMapping{}, base.Mappings...), gap.Mappings...) {
		catalog, found := catalogSongs[source.MusicID]
		if !found || catalog.JapaneseTitle != source.CatalogJapaneseTitle {
			return assembledReport{}, fmt.Errorf("Sekaipedia mapping music ID %d does not match the catalog", source.MusicID)
		}
		if _, duplicate := mappingByMusicID[source.MusicID]; duplicate {
			return assembledReport{}, fmt.Errorf("duplicate mapped music ID %d", source.MusicID)
		}
		if previous := sekaPageIDs[source.PageID]; previous != 0 {
			return assembledReport{}, fmt.Errorf("Sekaipedia page ID %d is shared by music IDs %d and %d", source.PageID, previous, source.MusicID)
		}
		if previous := sekaCanonicalURLs[source.CanonicalURL]; previous != 0 {
			return assembledReport{}, fmt.Errorf("Sekaipedia canonical URL is shared by music IDs %d and %d", previous, source.MusicID)
		}
		if previous := sekaResolvedURLs[source.ResolvedCanonicalURL]; previous != 0 {
			return assembledReport{}, fmt.Errorf("Sekaipedia resolved URL is shared by music IDs %d and %d", previous, source.MusicID)
		}
		copy := source
		mappingByMusicID[source.MusicID] = assembledMapping{
			MusicID: source.MusicID, CatalogJapaneseTitle: catalog.JapaneseTitle,
			Provider: providerSekaipedia, Sekaipedia: &copy,
		}
		sekaPageIDs[source.PageID] = source.MusicID
		sekaCanonicalURLs[source.CanonicalURL] = source.MusicID
		sekaResolvedURLs[source.ResolvedCanonicalURL] = source.MusicID
	}

	moegirlCatalog := catalogSongs[moegirl.Catalog.MusicID]
	mappingByMusicID[moegirl.Catalog.MusicID] = assembledMapping{
		MusicID: moegirl.Catalog.MusicID, CatalogJapaneseTitle: moegirlCatalog.JapaneseTitle,
		Provider: providerMoegirlExact,
		MoegirlPublicExact: &moegirlExactMapping{
			PageURL: moegirl.PageURL, PageTitle: moegirl.PageTitle, JapaneseTitle: moegirl.JapaneseTitle,
			PageID: moegirl.PageID, RevisionID: moegirl.RevisionID, FetchedAt: moegirl.FetchedAt,
			URLReportSHA256: moegirl.URLReportSHA256, RawHTMLSHA256: moegirl.RawHTMLSHA256,
			ExtractionReportSHA256: pins.MoegirlExtractionSHA256,
			LineCount:              moegirl.LineCount, StanzaCount: moegirl.StanzaCount,
		},
	}

	excludedSet := make(map[int]struct{}, len(scope.ExcludedMusicIDs))
	for _, musicID := range scope.ExcludedMusicIDs {
		excludedSet[musicID] = struct{}{}
	}
	mappings := make([]assembledMapping, 0, len(mappingByMusicID))
	excluded := make([]catalogSong, 0, len(excludedSet))
	for musicID, catalog := range catalogSongs {
		mapping, mapped := mappingByMusicID[musicID]
		_, isExcluded := excludedSet[musicID]
		if mapped == isExcluded {
			return assembledReport{}, fmt.Errorf("catalog music ID %d must be exactly one of mapped or excluded", musicID)
		}
		if mapped {
			mappings = append(mappings, mapping)
		} else {
			excluded = append(excluded, catalog)
		}
	}
	sort.Slice(mappings, func(i, j int) bool { return mappings[i].MusicID < mappings[j].MusicID })
	sort.Slice(excluded, func(i, j int) bool { return excluded[i].MusicID < excluded[j].MusicID })
	if len(mappings) != scope.MappingCount || len(mappingByMusicID) != scope.MappingCount || len(excluded) != len(scope.ExcludedMusicIDs) {
		return assembledReport{}, errors.New("assembled target-map counts do not match the immutable scope")
	}
	sekaCount, moegirlCount := 0, 0
	for _, mapping := range mappings {
		switch mapping.Provider {
		case providerSekaipedia:
			if mapping.Sekaipedia == nil || mapping.MoegirlPublicExact != nil {
				return assembledReport{}, errors.New("invalid Sekaipedia mapping union")
			}
			sekaCount++
		case providerMoegirlExact:
			if mapping.MoegirlPublicExact == nil || mapping.Sekaipedia != nil || mapping.MusicID != scope.ExactPublicPage.MusicID {
				return assembledReport{}, errors.New("invalid exact Moegirl mapping union")
			}
			moegirlCount++
		default:
			return assembledReport{}, errors.New("assembled target map contains an unauthorized provider")
		}
	}
	if sekaCount != scope.ProviderCounts[providerSekaipedia] || moegirlCount != scope.ProviderCounts[providerMoegirlExact] {
		return assembledReport{}, errors.New("assembled provider counts do not match the immutable scope")
	}
	mappingsBody, err := json.Marshal(mappings)
	if err != nil {
		return assembledReport{}, err
	}
	return assembledReport{
		SchemaVersion: 1, CatalogSHA256: catalogSHA, CatalogCount: len(catalogSongs), Inputs: pins,
		MappingCount: len(mappings), SekaipediaCount: sekaCount, MoegirlPublicExactCount: moegirlCount,
		MusicIDSetEncoding: musicIDSetEncoding, MusicIDSetSHA256: musicIDSetDigest(mappings),
		MappingsSHA256: sha256Hex(mappingsBody), ExcludedMusic: excluded, Mappings: mappings,
	}, nil
}

func validateScope(scope scopeInput, catalogSongs map[int]catalogSong) error {
	if scope.SchemaVersion != 1 || scope.CatalogCount != len(catalogSongs) || scope.MappingCount <= 0 ||
		scope.MappingCount != scope.CatalogCount-len(scope.ExcludedMusicIDs) || scope.BaseSekaipediaMappingCount <= 0 {
		return errors.New("target-map scope counts are invalid")
	}
	if len(scope.ProviderCounts) != 2 || scope.ProviderCounts[providerMoegirlExact] != 1 ||
		scope.ProviderCounts[providerSekaipedia]+scope.ProviderCounts[providerMoegirlExact] != scope.MappingCount ||
		scope.BaseSekaipediaMappingCount+len(scope.RequiredSekaipediaMappings) != scope.ProviderCounts[providerSekaipedia] {
		return errors.New("target-map scope provider counts are invalid")
	}
	if scope.ExactPublicPage.Provider != providerMoegirlExact || scope.ExactPublicPage.MusicID <= 0 {
		return errors.New("target-map scope exact public-page binding is invalid")
	}
	target, err := lyricssource.MoegirlPageURLTargetForURL(scope.ExactPublicPage.PageURL)
	if err != nil || target.PageURL != scope.ExactPublicPage.PageURL {
		return errors.New("target-map scope must contain one canonical complete zh.moegirl.org.cn article URL")
	}
	seen := map[int]string{}
	for _, required := range scope.RequiredSekaipediaMappings {
		if required.MusicID <= 0 || strings.TrimSpace(required.PageTitle) != required.PageTitle || required.PageTitle == "" ||
			required.MusicID == scope.ExactPublicPage.MusicID || seen[required.MusicID] != "" {
			return errors.New("target-map scope required Sekaipedia mappings are invalid")
		}
		seen[required.MusicID] = required.PageTitle
	}
	excluded := map[int]struct{}{}
	last := 0
	for _, musicID := range scope.ExcludedMusicIDs {
		if musicID <= last || musicID == scope.ExactPublicPage.MusicID {
			return errors.New("target-map scope excluded music IDs must be sorted, unique, and disjoint")
		}
		if _, found := catalogSongs[musicID]; !found {
			return errors.New("target-map scope excludes a music ID absent from the catalog")
		}
		excluded[musicID] = struct{}{}
		last = musicID
	}
	if _, found := catalogSongs[scope.ExactPublicPage.MusicID]; !found {
		return errors.New("target-map scope exact public-page music ID is absent from the catalog")
	}
	for musicID := range seen {
		if _, found := catalogSongs[musicID]; !found {
			return errors.New("target-map scope requires a music ID absent from the catalog")
		}
		if _, blocked := excluded[musicID]; blocked {
			return errors.New("target-map scope both requires and excludes a music ID")
		}
	}
	return nil
}

func validateSekaipediaReport(report sekaipediaMapReport, catalogSHA string, expectedMappings int, requireNoUnsupported bool) error {
	if report.SchemaVersion != 1 || report.Provider != providerSekaipedia || !report.Complete || report.Failure != "" ||
		report.CatalogSHA256 != catalogSHA || !canonicalSHA256.MatchString(report.URLReportSHA256) ||
		report.CatalogMappedCount != len(report.Mappings) || len(report.Mappings) != expectedMappings ||
		report.SourceOnlyCount != len(report.SourceOnly) || len(report.SourceOnly) != 0 ||
		report.CatalogExcludedCount != len(report.CatalogExcluded) || report.SourceTargetCount <= 0 ||
		report.MetadataMappedCount < report.CatalogMappedCount || len(report.MetadataBatches) == 0 {
		return errors.New("report summary is incomplete or inconsistent")
	}
	if requireNoUnsupported && len(report.UnsupportedSourcePages) != 0 {
		return errors.New("exact gap report contains unsupported source pages")
	}
	if _, err := time.Parse(time.RFC3339Nano, report.CheckedAt); err != nil {
		return errors.New("report checkedAt is invalid")
	}
	last := 0
	for _, mapping := range report.Mappings {
		if mapping.MusicID <= last || strings.TrimSpace(mapping.CatalogJapaneseTitle) == "" ||
			strings.TrimSpace(mapping.PageTitle) != mapping.PageTitle || mapping.PageTitle == "" ||
			strings.TrimSpace(mapping.ResolvedPageTitle) != mapping.ResolvedPageTitle || mapping.ResolvedPageTitle == "" ||
			mapping.PageID <= 0 || mapping.RevisionID <= 0 || !canonicalSHA1.MatchString(mapping.SHA1) ||
			!validSekaipediaURL(mapping.CanonicalURL) || !validSekaipediaURL(mapping.ResolvedCanonicalURL) {
			return errors.New("report contains an invalid Sekaipedia mapping")
		}
		if _, err := time.Parse(time.RFC3339, mapping.RevisionTimestamp); err != nil {
			return errors.New("report contains an invalid Sekaipedia revision timestamp")
		}
		last = mapping.MusicID
	}
	return nil
}

func validateMoegirlReport(
	scope scopeInput,
	report moegirlExtractionReport,
	catalogSHA, reportSHA string,
	catalogSongs map[int]catalogSong,
) error {
	catalog, found := catalogSongs[scope.ExactPublicPage.MusicID]
	target, targetErr := lyricssource.MoegirlPageURLTargetForURL(scope.ExactPublicPage.PageURL)
	if report.SchemaVersion != 1 || report.Provider != providerMoegirlExact || report.CatalogSHA256 != catalogSHA ||
		!canonicalSHA256.MatchString(reportSHA) || !canonicalSHA256.MatchString(report.URLReportSHA256) ||
		!canonicalSHA256.MatchString(report.RawHTMLSHA256) || !found || targetErr != nil ||
		report.Catalog.MusicID != scope.ExactPublicPage.MusicID || report.Catalog.JapaneseTitle != catalog.JapaneseTitle ||
		report.JapaneseTitle != catalog.JapaneseTitle || report.PageURL != scope.ExactPublicPage.PageURL ||
		report.PageTitle != target.PageTitle || report.PageID <= 0 || report.RevisionID <= 0 ||
		report.LineCount != len(report.Lines) || report.LineCount <= 0 || report.StanzaCount <= 0 || report.RightsNotice == "" {
		return errors.New("exact Moegirl public HTML report is incomplete or does not match the immutable scope")
	}
	if _, err := time.Parse(time.RFC3339Nano, report.FetchedAt); err != nil {
		return errors.New("exact Moegirl public HTML report fetchedAt is invalid")
	}
	stanzas := 1
	for _, line := range report.Lines {
		if strings.TrimSpace(line.Japanese) == "" || strings.TrimSpace(line.Translation) == "" {
			return errors.New("exact Moegirl public HTML report contains a blank lyrics pair")
		}
		if line.StanzaBreakBefore {
			stanzas++
		}
	}
	if stanzas != report.StanzaCount {
		return errors.New("exact Moegirl public HTML stanza count is inconsistent")
	}
	return nil
}

func validSekaipediaURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.String() == value && parsed.Scheme == "https" && parsed.Host == sekaipediaCanonicalHost &&
		parsed.User == nil && strings.HasPrefix(parsed.EscapedPath(), "/wiki/") && len(parsed.EscapedPath()) > len("/wiki/") &&
		parsed.RawQuery == "" && !parsed.ForceQuery && parsed.Fragment == ""
}

func parseOptions(arguments []string) (options, error) {
	var parsed options
	flags := flag.NewFlagSet("lyrics-target-map-assemble", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&parsed.scopePath, "scope", "", "immutable target-map scope")
	flags.StringVar(&parsed.expectedScopeSHA, "expected-scope-sha256", "", "exact target-map scope SHA-256")
	flags.StringVar(&parsed.basePath, "base-sekaipedia-report", "", "fixed base Sekaipedia mapping report")
	flags.StringVar(&parsed.expectedBaseSHA, "expected-base-sekaipedia-report-sha256", "", "exact base report SHA-256")
	flags.StringVar(&parsed.gapPath, "gap-sekaipedia-report", "", "fixed exact Sekaipedia gap mapping report")
	flags.StringVar(&parsed.expectedGapSHA, "expected-gap-sekaipedia-report-sha256", "", "exact gap report SHA-256")
	flags.StringVar(&parsed.moegirlPath, "moegirl-extraction-report", "", "fixed exact Moegirl public HTML extraction report")
	flags.StringVar(&parsed.expectedMoegirlSHA, "expected-moegirl-extraction-report-sha256", "", "exact Moegirl extraction SHA-256")
	flags.StringVar(&parsed.catalogPath, "catalog", "", "immutable catalog database")
	flags.StringVar(&parsed.expectedCatalogSHA, "expected-catalog-sha256", "", "exact catalog SHA-256")
	flags.StringVar(&parsed.outputPath, "output", "", "create-exclusive private assembled target map")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 {
		return options{}, errors.New("lyrics target-map assembly accepts only named flags")
	}
	for _, path := range []string{parsed.scopePath, parsed.basePath, parsed.gapPath, parsed.moegirlPath, parsed.catalogPath, parsed.outputPath} {
		if path == "" || strings.TrimSpace(path) != path || !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return options{}, errors.New("lyrics target-map assembly paths must be canonical and absolute")
		}
	}
	for _, digest := range []string{parsed.expectedScopeSHA, parsed.expectedBaseSHA, parsed.expectedGapSHA, parsed.expectedMoegirlSHA, parsed.expectedCatalogSHA} {
		if !canonicalSHA256.MatchString(digest) {
			return options{}, errors.New("lyrics target-map assembly SHA-256 pins are invalid")
		}
	}
	return parsed, nil
}

func decodeStrict(body []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}

func readCatalog(path string, expectedCount int) (*sql.DB, error) {
	database, err := sql.Open("sqlite", "file:"+path+"?mode=ro&immutable=1")
	if err != nil {
		return nil, err
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM catalog_music`).Scan(&count); err != nil {
		_ = database.Close()
		return nil, err
	}
	if count != expectedCount {
		_ = database.Close()
		return nil, fmt.Errorf("catalog count=%d, want %d", count, expectedCount)
	}
	return database, nil
}

func readCatalogSongs(database *sql.DB) (map[int]catalogSong, error) {
	rows, err := database.Query(`SELECT music_id, title_ja FROM catalog_music ORDER BY music_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[int]catalogSong{}
	last := 0
	for rows.Next() {
		var song catalogSong
		if err := rows.Scan(&song.MusicID, &song.JapaneseTitle); err != nil {
			return nil, err
		}
		if song.MusicID <= last || strings.TrimSpace(song.JapaneseTitle) == "" {
			return nil, errors.New("catalog music identity is invalid")
		}
		result[song.MusicID] = song
		last = song.MusicID
	}
	return result, rows.Err()
}

func musicIDSetDigest(mappings []assembledMapping) string {
	var encoded strings.Builder
	for _, mapping := range mappings {
		fmt.Fprintf(&encoded, "%d\n", mapping.MusicID)
	}
	return sha256Hex([]byte(encoded.String()))
}

func sha256Hex(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

func readPinnedFile(path, expectedSHA string, maximum int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Type() != 0 || info.Size() <= 0 || info.Size() > maximum {
		return nil, errors.New("target-map assembly input is not a bounded regular file")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if actual := sha256Hex(body); actual != expectedSHA {
		return nil, fmt.Errorf("target-map assembly input SHA-256=%s, want %s", actual, expectedSHA)
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, after) || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return nil, errors.New("target-map assembly input changed while being read")
	}
	return body, nil
}

func writePrivateExclusive(path string, body []byte) error {
	parentPath := filepath.Dir(path)
	parent, err := os.Stat(parentPath)
	if err != nil || !parent.IsDir() || parent.Mode().Perm() != 0o700 || len(body) == 0 {
		return errors.New("private target-map output parent or body is invalid")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	written, writeErr := file.Write(body)
	if writeErr == nil && written != len(body) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	writeErr = errors.Join(writeErr, file.Close())
	if writeErr != nil {
		return writeErr
	}
	directory, err := os.Open(parentPath)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
