package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"

	"moesekai/server/internal/lyricsacquisition"
	"moesekai/server/internal/lyricsextractionplan"
	"moesekai/server/internal/lyricsrecovery"
	"moesekai/server/internal/lyricssource"
)

const (
	requiredTargetCount                = 700
	requiredSekaipediaCount            = 699
	requiredExactPublicMusic           = 795
	requiredExactPublicURL             = "https://zh.moegirl.org.cn/%E4%BA%BF%E5%B9%B4%E7%88%B1%E6%81%8B"
	requiredCanaryMusicID              = 2
	requiredRequestTimeoutMillis       = 30_000
	maximumInputBytes            int64 = 32 << 20
)

var (
	lowerSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)
	lowerSHA1   = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string, output io.Writer) (returnErr error) {
	opts, err := parseOptions(arguments)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	targetBody, err := readPinnedFile(opts.targetMapPath, opts.targetMapSHA256, maximumInputBytes)
	if err != nil {
		return fmt.Errorf("target map: %w", err)
	}
	receiptBody, err := readPinnedFile(opts.catalogReceiptPath, opts.catalogReceiptSHA256, 1<<20)
	if err != nil {
		return fmt.Errorf("catalog receipt: %w", err)
	}
	catalogBody, err := readPinnedFile(opts.catalogPath, opts.catalogSHA256, lyricsextractionplan.MaxCatalogDatabaseBytes)
	if err != nil {
		return fmt.Errorf("catalog: %w", err)
	}
	listBody, err := readPinnedFile(opts.listResponsePath, opts.listResponseSHA256, maximumInputBytes)
	if err != nil {
		return fmt.Errorf("Sekaipedia List response: %w", err)
	}
	canaryBody, err := readPinnedFile(opts.canaryResponsePath, opts.canaryResponseSHA256, maximumInputBytes)
	if err != nil {
		return fmt.Errorf("Sekaipedia canary response: %w", err)
	}
	exactRaw, err := readPinnedFile(opts.exactRawHTMLPath, opts.exactRawHTMLSHA256, int64(2<<20))
	if err != nil {
		return fmt.Errorf("exact public HTML: %w", err)
	}
	exactReportBody, err := readPinnedFile(opts.exactExtractionReportPath, opts.exactExtractionReportSHA256, 1<<20)
	if err != nil {
		return fmt.Errorf("exact public extraction report: %w", err)
	}

	var target targetMapReport
	if err := decodeStrict(targetBody, &target); err != nil {
		return fmt.Errorf("decode target map: %w", err)
	}
	var receipt catalogFilterReceipt
	if err := decodeStrict(receiptBody, &receipt); err != nil {
		return fmt.Errorf("decode catalog receipt: %w", err)
	}
	var listResponse, canaryResponse mediaWikiResponse
	if err := decodeStrict(listBody, &listResponse); err != nil {
		return fmt.Errorf("decode Sekaipedia List response: %w", err)
	}
	if err := decodeStrict(canaryBody, &canaryResponse); err != nil {
		return fmt.Errorf("decode Sekaipedia canary response: %w", err)
	}
	var exactReport strictExtractionReport
	if err := decodeStrict(exactReportBody, &exactReport); err != nil {
		return fmt.Errorf("decode exact public extraction report: %w", err)
	}

	if err := validateTargetMap(target); err != nil {
		return err
	}
	if err := validateCatalogReceipt(opts, target, receipt, int64(len(catalogBody))); err != nil {
		return err
	}
	list, err := exactMediaWikiRevision(listResponse, "Sekaipedia List")
	if err != nil {
		return err
	}
	canary, err := exactMediaWikiRevision(canaryResponse, "Sekaipedia canary")
	if err != nil {
		return err
	}
	exact, err := exactPublicMapping(target)
	if err != nil {
		return err
	}
	if err := validateExactPublicArtifacts(opts, target.CatalogSHA256, exact, exactRaw, exactReport); err != nil {
		return err
	}

	authority, err := buildListAuthority(list, opts.listResponseSHA256)
	if err != nil {
		return err
	}
	if err := validateHistoricalListAcquisition(ctx, opts, authority); err != nil {
		return err
	}
	canaryPlan, err := buildCanaryPlan(target, canary, opts.canaryResponseSHA256, authority, opts.listReplayAcquisitionID)
	if err != nil {
		return err
	}

	sourceSnapshot, err := lyricsextractionplan.PrepareRecoverySourceSnapshot(opts.sourceRoot, opts.createdAt)
	if err != nil {
		return fmt.Errorf("prepare recovery source snapshot: %w", err)
	}
	plan, err := assemblePlan(opts, target, receipt, authority, canaryPlan, sourceSnapshot, exact)
	if err != nil {
		return err
	}
	body, err := lyricsextractionplan.MarshalRecoveryCanonical(plan)
	if err != nil {
		return fmt.Errorf("marshal canonical recovery plan: %w", err)
	}
	if bytes.Contains(body, []byte("moegirl.icu")) || bytes.Contains(bytes.ToLower(body), []byte("romanization")) ||
		bytes.Contains(bytes.ToLower(body), []byte(`"romaji"`)) {
		return errors.New("canonical recovery plan contains a forbidden ICU or romanization marker")
	}
	planSHA, err := lyricsextractionplan.RecoveryCanonicalSHA256(plan)
	if err != nil {
		return err
	}
	if err := lyricsextractionplan.VerifyRecoverySourceSnapshot(opts.sourceRoot, plan); err != nil {
		return err
	}
	catalog, verification, err := lyricsrecovery.OpenCatalogAgainstPlan(ctx, opts.catalogPath, plan.Catalog)
	if err != nil {
		return fmt.Errorf("verify catalog against assembled plan: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, catalog.Close()) }()
	if verification.RecordCount != requiredTargetCount || !reflect.DeepEqual(catalog.MusicIDs(), plan.Scope.MusicIDs) {
		return errors.New("assembled plan scope does not exactly equal the verified 700 catalog")
	}
	if err := requireRunOutputsAbsent(plan.Outputs); err != nil {
		return err
	}
	if err := writePrivateExclusive(opts.outputPlanPath, body); err != nil {
		return err
	}
	_, err = fmt.Fprintf(output,
		"PASS mode=lyrics-recovery-plan-assemble planId=%s songs=%d sekaipedia=%d exactPublic=%d planSha256=%s sourceSha256=%s output=%s\n",
		plan.PlanID, len(plan.Scope.MusicIDs), len(plan.Providers.Configurations[0].MusicIDs),
		len(plan.Providers.Configurations[1].MusicIDs), planSHA, plan.SourceSnapshot.SHA256, opts.outputPlanPath,
	)
	return err
}

func parseOptions(arguments []string) (options, error) {
	var opts options
	flags := flag.NewFlagSet("lyrics-recovery-plan-assemble", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&opts.sourceRoot, "source-root", "", "canonical repository root")
	flags.StringVar(&opts.createdAt, "created-at", "", "canonical UTC plan timestamp")
	flags.StringVar(&opts.planID, "plan-id", "", "immutable plan identity")
	flags.StringVar(&opts.scopeID, "scope-id", "", "immutable final scope identity")
	flags.StringVar(&opts.targetMapPath, "target-map", "", "canonical 700 target map")
	flags.StringVar(&opts.targetMapSHA256, "target-map-sha256", "", "target-map SHA-256")
	flags.StringVar(&opts.catalogPath, "catalog", "", "reviewed filtered catalog")
	flags.StringVar(&opts.catalogSHA256, "catalog-sha256", "", "catalog SHA-256")
	flags.StringVar(&opts.catalogReceiptPath, "catalog-receipt", "", "catalog filter receipt")
	flags.StringVar(&opts.catalogReceiptSHA256, "catalog-receipt-sha256", "", "catalog receipt SHA-256")
	flags.StringVar(&opts.listResponsePath, "sekaipedia-list-response", "", "fixed List response")
	flags.StringVar(&opts.listResponseSHA256, "sekaipedia-list-response-sha256", "", "fixed List raw SHA-256")
	flags.StringVar(&opts.listReplayLedgerPath, "sekaipedia-list-replay-ledger", "", "exact historical List ledger")
	flags.StringVar(&opts.listReplayAcquisitionID, "sekaipedia-list-acquisition-id", "", "exact historical List AcquisitionID")
	flags.StringVar(&opts.canaryResponsePath, "sekaipedia-canary-response", "", "fixed canary response")
	flags.StringVar(&opts.canaryResponseSHA256, "sekaipedia-canary-response-sha256", "", "fixed canary raw SHA-256")
	flags.StringVar(&opts.exactRawHTMLPath, "exact-public-html", "", "fixed complete public-page HTML")
	flags.StringVar(&opts.exactRawHTMLSHA256, "exact-public-html-sha256", "", "fixed public HTML SHA-256")
	flags.StringVar(&opts.exactExtractionReportPath, "exact-public-report", "", "fixed extraction report")
	flags.StringVar(&opts.exactExtractionReportSHA256, "exact-public-report-sha256", "", "fixed extraction report SHA-256")
	flags.StringVar(&opts.runRoot, "run-root", "", "private output parent with absent child outputs")
	flags.StringVar(&opts.outputPlanPath, "output-plan", "", "create-exclusive canonical plan path")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 {
		return opts, errors.New("lyrics-recovery-plan-assemble requires only explicit named flags")
	}
	for _, value := range []string{
		opts.sourceRoot, opts.targetMapPath, opts.catalogPath, opts.catalogReceiptPath, opts.listResponsePath,
		opts.listReplayLedgerPath, opts.canaryResponsePath, opts.exactRawHTMLPath,
		opts.exactExtractionReportPath, opts.runRoot, opts.outputPlanPath,
	} {
		if !canonicalAbsolutePath(value) {
			return opts, errors.New("all paths must be explicit canonical absolute paths")
		}
	}
	for _, value := range []string{
		opts.targetMapSHA256, opts.catalogSHA256, opts.catalogReceiptSHA256, opts.listResponseSHA256,
		opts.listReplayAcquisitionID, opts.canaryResponseSHA256, opts.exactRawHTMLSHA256,
		opts.exactExtractionReportSHA256,
	} {
		if !lowerSHA256.MatchString(value) {
			return opts, errors.New("every input digest and AcquisitionID must be canonical lowercase SHA-256")
		}
	}
	createdAt, err := time.Parse(time.RFC3339Nano, opts.createdAt)
	if err != nil || createdAt.Location() != time.UTC || createdAt.Format(time.RFC3339Nano) != opts.createdAt {
		return opts, errors.New("-created-at must be canonical UTC RFC3339Nano")
	}
	if opts.planID == "" || opts.scopeID == "" || strings.ContainsAny(opts.planID+opts.scopeID, "\r\n\x00") {
		return opts, errors.New("plan and scope IDs are required")
	}
	if opts.catalogPath != filepath.Join(filepath.Dir(opts.catalogReceiptPath), "catalog.db") {
		return opts, errors.New("catalog path must be the catalog.db sibling bound by its receipt")
	}
	if !lyricsextractionplan.RecoveryPrivateOutputPathAllowed(opts.runRoot) ||
		!lyricsextractionplan.RecoveryPrivateOutputPathAllowed(opts.outputPlanPath) {
		return opts, errors.New("run and plan outputs must remain inside the explicit private recovery boundary")
	}
	if err := requirePrivateDirectory(opts.runRoot); err != nil {
		return opts, fmt.Errorf("run root: %w", err)
	}
	if err := requirePrivateDirectory(filepath.Dir(opts.outputPlanPath)); err != nil {
		return opts, fmt.Errorf("plan output parent: %w", err)
	}
	return opts, nil
}

func assemblePlan(
	opts options,
	target targetMapReport,
	receipt catalogFilterReceipt,
	authority lyricsextractionplan.FixedAuthority,
	canary *lyricsextractionplan.RecoverySekaipediaCanaryPlan,
	snapshot lyricsextractionplan.SourceSnapshot,
	exact targetExactPublicMapping,
) (lyricsextractionplan.RecoveryPlan, error) {
	allIDs := make([]int, 0, len(target.Mappings))
	sekaIDs := make([]int, 0, requiredSekaipediaCount)
	sekaTargets := make([]lyricsextractionplan.RecoverySekaipediaPageTarget, 0, requiredSekaipediaCount)
	for _, mapping := range target.Mappings {
		allIDs = append(allIDs, mapping.MusicID)
		if mapping.Provider != string(lyricsextractionplan.ProviderSekaipedia) {
			continue
		}
		sekaIDs = append(sekaIDs, mapping.MusicID)
		target := lyricsextractionplan.RecoverySekaipediaPageTarget{
			MusicID: mapping.MusicID, PageTitle: mapping.Sekaipedia.PageTitle,
			ResolvedPageTitle: mapping.Sekaipedia.ResolvedPageTitle,
		}
		if mapping.Sekaipedia.ContentSHA256 != "" || mapping.Sekaipedia.RawResponseSHA256 != "" {
			target.FixedRevision = &lyricsextractionplan.RecoverySekaipediaRevisionBinding{
				PageID: mapping.Sekaipedia.PageID, RevisionID: mapping.Sekaipedia.RevisionID,
				RevisionTimestamp: mapping.Sekaipedia.RevisionTimestamp, SHA1: mapping.Sekaipedia.SHA1,
				ContentSHA256:     mapping.Sekaipedia.ContentSHA256,
				RawResponseSHA256: mapping.Sekaipedia.RawResponseSHA256,
			}
		}
		sekaTargets = append(sekaTargets, target)
	}
	versions, err := lyricsextractionplan.CompiledScopedRecoveryVersions([]lyricsextractionplan.Provider{
		lyricsextractionplan.ProviderSekaipedia, lyricsextractionplan.ProviderMoegirlPublicExact,
	})
	if err != nil {
		return lyricsextractionplan.RecoveryPlan{}, err
	}
	floors := lyricsextractionplan.CompiledSafetyFloors()
	outputs := lyricsextractionplan.RequiredRecoveryOutputs([6]string{
		filepath.Join(opts.runRoot, "ledger"), filepath.Join(opts.runRoot, "acquisition-set.json"),
		filepath.Join(opts.runRoot, "provider-outcomes"), filepath.Join(opts.runRoot, "song-results"),
		filepath.Join(opts.runRoot, "evidence-pack"), filepath.Join(opts.runRoot, "root.json"),
	})
	plan := lyricsextractionplan.RecoveryPlan{
		SchemaVersion: lyricsextractionplan.RecoverySchemaVersionV2, CanonicalEncoding: lyricsextractionplan.RecoveryCanonicalEncodingV2,
		DigestAlgorithm: lyricsextractionplan.RecoveryDigestAlgorithmV2, PlanID: opts.planID, CreatedAt: opts.createdAt,
		Catalog: lyricsextractionplan.RecoveryCatalogBinding{
			Path: opts.catalogPath, SizeBytes: receipt.Catalog.ByteCount, SourceSHA256: receipt.Catalog.SHA256,
			SchemaVersion: receipt.Catalog.SchemaVersion, RuntimeSchemaVersion: receipt.Catalog.RuntimeSchemaVersion,
			RecordCount: receipt.Catalog.RecordCount, IdentityPolicyVersion: receipt.Catalog.IdentityPolicyVersion,
			IdentitySHA256: receipt.Catalog.IdentitySHA256, MusicIDsSHA256: receipt.Catalog.MusicIDsSHA256,
		},
		SourceSnapshot: snapshot,
		Scope: lyricsextractionplan.RecoveryScopeBinding{
			Kind: lyricsextractionplan.RecoveryScopeFinal, ScopeID: opts.scopeID, MusicIDs: allIDs,
			SupersedesRootID: "", SupersedesRootSHA256: "",
		},
		Providers: lyricsextractionplan.RecoveryProviderConfiguration{
			Order: []lyricsextractionplan.Provider{
				lyricsextractionplan.ProviderSekaipedia, lyricsextractionplan.ProviderMoegirlPublicExact,
			},
			Configurations: []lyricsextractionplan.RecoveryProviderPlan{
				{
					Provider: lyricsextractionplan.ProviderSekaipedia, Mode: lyricsextractionplan.ProviderModeActive,
					CrawlDelayMillis: floors.ProviderCrawlDelayMillis, CacheTTLMillis: floors.ProviderCacheTTLMillis,
					MusicIDs: sekaIDs, Authorities: []lyricsextractionplan.FixedAuthority{authority},
					ContributorAliases: []lyricsextractionplan.RecoveryContributorAlias{{
						MusicID: 2, CatalogContributor: "みきとP", ProviderContributor: "Mikito-P",
					}},
					SekaipediaTargets: sekaTargets,
				},
				{
					Provider: lyricsextractionplan.ProviderMoegirlPublicExact, Mode: lyricsextractionplan.ProviderModeActive,
					CrawlDelayMillis: floors.ProviderCrawlDelayMillis, CacheTTLMillis: floors.ProviderCacheTTLMillis,
					MusicIDs: []int{requiredExactPublicMusic}, Authorities: []lyricsextractionplan.FixedAuthority{},
					ContributorAliases: []lyricsextractionplan.RecoveryContributorAlias{},
					ExactPublicTargets: []lyricsextractionplan.RecoveryExactPublicPageTarget{{
						MusicID: requiredExactPublicMusic, PageURL: exact.PageURL, PageTitle: exact.PageTitle,
						JapaneseTitle: exact.JapaneseTitle, PageID: exact.PageID, RevisionID: exact.RevisionID,
						FetchedAt: exact.FetchedAt,
						RawHTML: lyricsextractionplan.RecoveryFileBinding{
							Path: opts.exactRawHTMLPath, SizeBytes: mustFileSize(opts.exactRawHTMLPath), SHA256: opts.exactRawHTMLSHA256,
						},
						ExtractionReport: lyricsextractionplan.RecoveryFileBinding{
							Path: opts.exactExtractionReportPath, SizeBytes: mustFileSize(opts.exactExtractionReportPath),
							SHA256: opts.exactExtractionReportSHA256,
						},
					}},
				},
			},
		},
		Versions: versions,
		Execution: lyricsextractionplan.RecoveryExecutionSettings{
			MaxAttempts: 1, RequestTimeoutMillis: requiredRequestTimeoutMillis, RetryDelayMillis: floors.RetryDelayMillis,
			ProviderResponseBytes:    lyricsextractionplan.CompiledHardCeilings().ProviderResponseBytes,
			MaxActualNetworkInFlight: lyricsextractionplan.RecoveryMaxActualInFlight,
			MediaWikiMaxlag:          lyricsextractionplan.RecoveryRequiredMaxlag, LiveCanaryMusicIDs: []int{requiredCanaryMusicID},
		},
		SekaipediaCanary: canary, Outputs: outputs, Deployment: lyricsextractionplan.RequiredDeploymentPolicy(),
	}
	if err := lyricsextractionplan.ValidateRecovery(plan); err != nil {
		return lyricsextractionplan.RecoveryPlan{}, fmt.Errorf("validate assembled recovery plan: %w", err)
	}
	return plan, nil
}

func buildListAuthority(revision exactRevision, rawSHA256 string) (lyricsextractionplan.FixedAuthority, error) {
	if revision.PageID != 268 || revision.RevisionID != 338123 || revision.Title != "List of songs" ||
		revision.Timestamp != "2026-08-04T08:01:35Z" ||
		revision.SHA1 != "d025c2122cbcb86f96368d7ca109af8a4ffd3d69" ||
		revision.ContentSHA256 != "36d45904d6511a2e810110c0859fdcc4b2c57f798fbdde290cf2059601f5b6f9" ||
		rawSHA256 != "d06e034f6b7676a2f1569c0b28915a7a35c605a7dd1da0e73fabfdbc5d4072ef" {
		return lyricsextractionplan.FixedAuthority{}, errors.New("Sekaipedia List response is not the reviewed fixed authority")
	}
	authority := lyricsextractionplan.FixedAuthority{
		Disposition: lyricsextractionplan.AuthorityActive, Role: lyricsextractionplan.AuthorityRoleSongIndex,
		CaptureProfile: lyricsextractionplan.CaptureProfileMediaWikiAPIRevisionResponseV1,
		PageID:         revision.PageID, RevisionID: revision.RevisionID, RevisionTimestamp: revision.Timestamp,
		SHA1: revision.SHA1, ContentSHA256: revision.ContentSHA256, RawSHA256: rawSHA256, Title: revision.Title,
		CanonicalURL: lyricsextractionplan.FixedAuthorityCanonicalURL(
			lyricsextractionplan.ProviderSekaipedia, revision.Title, revision.RevisionID,
		),
	}
	var err error
	authority.EvidenceID, err = lyricsextractionplan.FixedAuthorityEvidenceID(
		lyricsextractionplan.ProviderSekaipedia, authority.Role, authority.PageID, authority.RevisionID, authority.Title,
	)
	return authority, err
}

func buildCanaryPlan(
	target targetMapReport,
	revision exactRevision,
	rawSHA256 string,
	authority lyricsextractionplan.FixedAuthority,
	acquisitionID string,
) (*lyricsextractionplan.RecoverySekaipediaCanaryPlan, error) {
	var mapping *targetMapMapping
	for index := range target.Mappings {
		if target.Mappings[index].MusicID == requiredCanaryMusicID {
			mapping = &target.Mappings[index]
			break
		}
	}
	if mapping == nil || mapping.Sekaipedia == nil || revision.PageID != mapping.Sekaipedia.PageID ||
		revision.RevisionID != mapping.Sekaipedia.RevisionID || revision.Timestamp != mapping.Sekaipedia.RevisionTimestamp ||
		revision.SHA1 != mapping.Sekaipedia.SHA1 || revision.Title != mapping.Sekaipedia.ResolvedPageTitle {
		return nil, errors.New("Sekaipedia canary response does not exactly match target-map music ID 2")
	}
	return &lyricsextractionplan.RecoverySekaipediaCanaryPlan{
		List: lyricsextractionplan.RecoverySekaipediaCanaryRevision{
			AcquisitionID: acquisitionID, PageID: authority.PageID, RevisionID: authority.RevisionID,
			RevisionTimestamp: authority.RevisionTimestamp, SHA1: authority.SHA1,
			ContentSHA256: authority.ContentSHA256, RawResponseSHA256: authority.RawSHA256,
		},
		Songs: []lyricsextractionplan.RecoverySekaipediaCanarySong{{
			MusicID: requiredCanaryMusicID, CatalogTitle: mapping.CatalogJapaneseTitle, ProviderTitle: revision.Title,
			PageID: revision.PageID, RevisionID: revision.RevisionID, RevisionTimestamp: revision.Timestamp,
			SHA1: revision.SHA1, ContentSHA256: revision.ContentSHA256, RawResponseSHA256: rawSHA256,
		}},
	}, nil
}

func validateHistoricalListAcquisition(
	ctx context.Context,
	opts options,
	authority lyricsextractionplan.FixedAuthority,
) (returnErr error) {
	ledger, err := lyricsacquisition.OpenLedger(ctx, opts.listReplayLedgerPath)
	if err != nil {
		return fmt.Errorf("open exact List replay ledger: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, ledger.Close()) }()
	acquired, err := ledger.ReplayByAcquisitionID(ctx, lyricsacquisition.AcquisitionID(opts.listReplayAcquisitionID))
	if err != nil {
		return fmt.Errorf("replay exact List AcquisitionID: %w", err)
	}
	if acquired.AcquisitionID != lyricsacquisition.AcquisitionID(opts.listReplayAcquisitionID) {
		return errors.New("List ledger returned a different AcquisitionID")
	}
	_, err = lyricsrecovery.NewPlanBoundSekaipediaListReplayTransport(acquired, lyricssource.FixedIndex{
		PageID: authority.PageID, RevisionID: authority.RevisionID, RevisionTimestamp: authority.RevisionTimestamp,
		SHA1: authority.SHA1, ContentSHA256: authority.ContentSHA256, RawSHA256: authority.RawSHA256, Title: authority.Title,
	}, rejectNetworkTransport{})
	if err != nil {
		return fmt.Errorf("validate exact historical List acquisition against authority: %w", err)
	}
	return nil
}

type rejectNetworkTransport struct{}

func (rejectNetworkTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("network is prohibited during recovery-plan assembly")
}

func validateTargetMap(target targetMapReport) error {
	if target.SchemaVersion != 1 || target.CatalogCount != 704 || target.MappingCount != requiredTargetCount ||
		target.SekaipediaCount != requiredSekaipediaCount || target.MoegirlPublicExactCount != 1 ||
		len(target.Mappings) != requiredTargetCount || target.MusicIDSetEncoding != "decimal-newline-v1" ||
		!lowerSHA256.MatchString(target.MusicIDSetSHA256) || !lowerSHA256.MatchString(target.MappingsSHA256) ||
		target.Inputs.MoegirlExtractionSHA256 == "" {
		return errors.New("canonical target map summary is not the reviewed 700 / 699+1 shape")
	}
	mappingBody, err := json.Marshal(target.Mappings)
	if err != nil || sha256Hex(mappingBody) != target.MappingsSHA256 {
		return errors.New("canonical target-map mapping digest is invalid")
	}
	var musicIDs strings.Builder
	seenPages := make(map[int]int, requiredSekaipediaCount)
	seenURLs := make(map[string]int, requiredSekaipediaCount)
	last := 0
	sekaCount, exactCount := 0, 0
	for _, mapping := range target.Mappings {
		if mapping.MusicID <= last || strings.TrimSpace(mapping.CatalogJapaneseTitle) == "" {
			return errors.New("target map music IDs or catalog titles are invalid")
		}
		fmt.Fprintf(&musicIDs, "%d\n", mapping.MusicID)
		switch mapping.Provider {
		case string(lyricsextractionplan.ProviderSekaipedia):
			item := mapping.Sekaipedia
			if item == nil || mapping.MoegirlPublicExact != nil || item.MusicID != mapping.MusicID ||
				item.CatalogJapaneseTitle != mapping.CatalogJapaneseTitle || item.PageID <= 0 || item.RevisionID <= 0 ||
				!lowerSHA1.MatchString(item.SHA1) || !canonicalSekaipediaArticleURL(item.CanonicalURL, item.PageTitle) ||
				!canonicalSekaipediaArticleURL(item.ResolvedCanonicalURL, item.ResolvedPageTitle) {
				return fmt.Errorf("target map Sekaipedia mapping %d is invalid", mapping.MusicID)
			}
			if _, err := time.Parse(time.RFC3339, item.RevisionTimestamp); err != nil {
				return fmt.Errorf("target map Sekaipedia mapping %d timestamp is invalid", mapping.MusicID)
			}
			fixed := item.ContentSHA256 != "" || item.RawResponseSHA256 != ""
			if fixed != (item.ContentSHA256 != "" && item.RawResponseSHA256 != "") ||
				(fixed && (!lowerSHA256.MatchString(item.ContentSHA256) || !lowerSHA256.MatchString(item.RawResponseSHA256))) {
				return fmt.Errorf("target map Sekaipedia mapping %d fixed revision digests are invalid", mapping.MusicID)
			}
			if err := validateReviewedNewSongMapping(*item); err != nil {
				return err
			}
			if previous := seenPages[item.PageID]; previous != 0 || seenURLs[item.ResolvedCanonicalURL] != 0 {
				return errors.New("target map contains duplicate Sekaipedia page identities")
			}
			seenPages[item.PageID], seenURLs[item.ResolvedCanonicalURL] = mapping.MusicID, mapping.MusicID
			sekaCount++
		case string(lyricsextractionplan.ProviderMoegirlPublicExact):
			item := mapping.MoegirlPublicExact
			if item == nil || mapping.Sekaipedia != nil || mapping.MusicID != requiredExactPublicMusic ||
				item.PageURL != requiredExactPublicURL || item.JapaneseTitle != mapping.CatalogJapaneseTitle ||
				item.PageID != 649688 || item.RevisionID != 8500224 || item.LineCount != 65 || item.StanzaCount != 16 ||
				!lowerSHA256.MatchString(item.RawHTMLSHA256) || !lowerSHA256.MatchString(item.ExtractionReportSHA256) {
				return errors.New("target map exact-public music ID 795 binding is invalid")
			}
			exactCount++
		default:
			return errors.New("target map contains an unauthorized provider")
		}
		last = mapping.MusicID
	}
	if sekaCount != requiredSekaipediaCount || exactCount != 1 || sha256Hex([]byte(musicIDs.String())) != target.MusicIDSetSHA256 {
		return errors.New("target map provider counts or ordered music-ID digest is invalid")
	}
	wantExcluded := []catalogSong{
		{388, "初音ミクの激唱"}, {674, "MASTER高難易度楽曲メドレー"},
		{675, "プロセカULTIMATE楽曲メドレー"}, {676, "周年記念高難易度書き下ろし楽曲メドレー"},
	}
	if !reflect.DeepEqual(target.ExcludedMusic, wantExcluded) {
		return errors.New("target map does not contain the exact four reviewed exclusions")
	}
	return nil
}

func validateReviewedNewSongMapping(item targetSekaipediaMapping) error {
	type reviewedSong struct {
		catalogTitle, providerTitle string
		pageID, revisionID          int
		timestamp, sha1             string
		contentSHA256, rawSHA256    string
	}
	reviewed := map[int]reviewedSong{
		765: {
			catalogTitle: "レム", providerTitle: "REM", pageID: 110337, revisionID: 337495,
			timestamp: "2026-08-02T15:14:03Z", sha1: "5f39ec1b38919d4044b4da5cfdeff2566a637d18",
			contentSHA256: "3538fc21da500c46a2b1dc8c844f7307940958bf6f5684ff8a4a92a3afabe728",
			rawSHA256:     "a17181de74a57adabdfe3a0208bce72f30184021bbd88b07850516f2ec69b369",
		},
		789: {
			catalogTitle: "天秤、指先で触れて", providerTitle: "Tenbin, Yubisaki de Furete",
			pageID: 110395, revisionID: 337447, timestamp: "2026-08-02T02:16:32Z",
			sha1:          "e4e96d5f96d711c2d60da6cc74b5fb54f62b5927",
			contentSHA256: "10e4630869c1d132cbc4e7e70580db5037bf797ca8aad50495fe9b9a16935655",
			rawSHA256:     "3d606933528528533a68c59ee57969f3a6d04913247123915c68c83c0d97105c",
		},
	}
	expected, isNew := reviewed[item.MusicID]
	if !isNew {
		if item.ContentSHA256 != "" || item.RawResponseSHA256 != "" {
			return fmt.Errorf("target map Sekaipedia mapping %d has an unreviewed fixed revision", item.MusicID)
		}
		return nil
	}
	if item.CatalogJapaneseTitle != expected.catalogTitle || item.SekaipediaJapaneseTitle != expected.catalogTitle ||
		item.PageTitle != expected.providerTitle || item.ResolvedPageTitle != expected.providerTitle ||
		item.PageID != expected.pageID || item.RevisionID != expected.revisionID ||
		item.RevisionTimestamp != expected.timestamp || item.SHA1 != expected.sha1 ||
		item.ContentSHA256 != expected.contentSHA256 || item.RawResponseSHA256 != expected.rawSHA256 {
		return fmt.Errorf("target map Sekaipedia mapping %d is not the exact reviewed fixed song revision", item.MusicID)
	}
	return nil
}

func validateCatalogReceipt(opts options, target targetMapReport, receipt catalogFilterReceipt, catalogBytes int64) error {
	policy := lyricsextractionplan.CompiledEffectiveVersions().Policies.CatalogIdentity
	if receipt.SchemaVersion != 1 || receipt.TargetMapSHA256 != opts.targetMapSHA256 || receipt.CatalogFile != "catalog.db" ||
		receipt.SourceCatalog.SHA256 != target.CatalogSHA256 || receipt.SourceCatalog.RecordCount != target.CatalogCount ||
		receipt.Catalog.ByteCount != catalogBytes || receipt.Catalog.SHA256 != opts.catalogSHA256 ||
		receipt.Catalog.SchemaVersion != lyricsextractionplan.CatalogSchemaVersion ||
		receipt.Catalog.RuntimeSchemaVersion < lyricsextractionplan.CatalogSchemaVersion ||
		receipt.Catalog.RuntimeSchemaVersion > lyricsextractionplan.MaximumCatalogRuntimeSchema ||
		receipt.Catalog.RecordCount != requiredTargetCount || receipt.Catalog.IdentityPolicyVersion != policy ||
		!lowerSHA256.MatchString(receipt.Catalog.IdentitySHA256) || !lowerSHA256.MatchString(receipt.Catalog.MusicIDsSHA256) ||
		!reflect.DeepEqual(receipt.ExcludedMusic, target.ExcludedMusic) {
		return errors.New("catalog receipt does not exactly bind the reviewed filtered 700 catalog")
	}
	return nil
}

func validateExactPublicArtifacts(
	opts options,
	sourceCatalogSHA256 string,
	exact targetExactPublicMapping,
	raw []byte,
	report strictExtractionReport,
) error {
	parsed, err := lyricssource.ParseMoegirlPublicPageHTML(raw, requiredExactPublicURL)
	if err != nil {
		return fmt.Errorf("parse fixed exact public HTML: %w", err)
	}
	if exact.PageURL != requiredExactPublicURL || exact.RawHTMLSHA256 != opts.exactRawHTMLSHA256 ||
		exact.ExtractionReportSHA256 != opts.exactExtractionReportSHA256 || parsed.PageURL != exact.PageURL ||
		parsed.PageTitle != exact.PageTitle || parsed.JapaneseTitle != exact.JapaneseTitle ||
		parsed.PageID != exact.PageID || parsed.RevisionID != exact.RevisionID || len(parsed.Lines) != exact.LineCount {
		return errors.New("fixed exact public HTML does not match target-map music ID 795")
	}
	if report.SchemaVersion != 1 || report.Provider != string(lyricsextractionplan.ProviderMoegirlPublicExact) ||
		report.CatalogSHA256 != sourceCatalogSHA256 || !lowerSHA256.MatchString(report.URLReportSHA256) ||
		report.PageURL != requiredExactPublicURL || report.PageTitle != exact.PageTitle ||
		report.JapaneseTitle != exact.JapaneseTitle || report.PageID != exact.PageID || report.RevisionID != exact.RevisionID ||
		report.FetchedAt != exact.FetchedAt || report.RawHTMLSHA256 != opts.exactRawHTMLSHA256 ||
		report.Catalog.MusicID != requiredExactPublicMusic || report.Catalog.JapaneseTitle != exact.JapaneseTitle ||
		report.LineCount != exact.LineCount || report.StanzaCount != exact.StanzaCount || len(report.Lines) != exact.LineCount ||
		report.RightsNotice == "" {
		return errors.New("fixed exact public extraction report does not match target-map music ID 795")
	}
	stanzaCount := 1
	for index := range parsed.Lines {
		line := report.Lines[index]
		if line.Japanese != parsed.Lines[index].Japanese || line.Translation != parsed.Lines[index].Translation ||
			line.StanzaBreakBefore != parsed.Lines[index].StanzaBreakBefore {
			return errors.New("fixed exact public extraction report line differs from the strict HTML parser")
		}
		if line.StanzaBreakBefore {
			stanzaCount++
		}
	}
	if stanzaCount != exact.StanzaCount {
		return errors.New("fixed exact public extraction report stanza count is invalid")
	}
	if _, err := time.Parse(time.RFC3339Nano, exact.FetchedAt); err != nil {
		return errors.New("exact public fetchedAt is invalid")
	}
	return nil
}

func exactPublicMapping(target targetMapReport) (targetExactPublicMapping, error) {
	for _, mapping := range target.Mappings {
		if mapping.MusicID == requiredExactPublicMusic && mapping.MoegirlPublicExact != nil {
			return *mapping.MoegirlPublicExact, nil
		}
	}
	return targetExactPublicMapping{}, errors.New("target map contains no exact-public music ID 795")
}

type exactRevision struct {
	PageID        int
	Title         string
	RevisionID    int
	Timestamp     string
	SHA1          string
	ContentSHA256 string
}

func exactMediaWikiRevision(response mediaWikiResponse, label string) (exactRevision, error) {
	if !response.BatchComplete || response.Limits.Categories != 500 || len(response.Query.Pages) != 1 {
		return exactRevision{}, fmt.Errorf("%s response is incomplete", label)
	}
	page := response.Query.Pages[0]
	if page.PageID <= 0 || page.Namespace != 0 || page.Title == "" || len(page.Revisions) != 1 || len(page.Categories) == 0 {
		return exactRevision{}, fmt.Errorf("%s page identity is invalid", label)
	}
	revision := page.Revisions[0]
	if revision.RevisionID <= 0 || revision.Timestamp == "" || !lowerSHA1.MatchString(revision.SHA1) ||
		revision.Slots.Main.ContentModel != "wikitext" || revision.Slots.Main.ContentFormat != "text/x-wiki" ||
		revision.Slots.Main.Content == "" {
		return exactRevision{}, fmt.Errorf("%s revision identity is invalid", label)
	}
	if _, err := time.Parse(time.RFC3339, revision.Timestamp); err != nil {
		return exactRevision{}, fmt.Errorf("%s revision timestamp is invalid", label)
	}
	return exactRevision{
		PageID: page.PageID, Title: page.Title, RevisionID: revision.RevisionID, Timestamp: revision.Timestamp,
		SHA1: revision.SHA1, ContentSHA256: sha256Hex([]byte(revision.Slots.Main.Content)),
	}, nil
}

func canonicalSekaipediaArticleURL(value, title string) bool {
	if value == "" || title == "" || strings.TrimSpace(title) != title {
		return false
	}
	canonical := &url.URL{Scheme: "https", Host: "www.sekaipedia.org", Path: "/wiki/" + strings.ReplaceAll(title, " ", "_")}
	return value == canonical.String()
}

func decodeStrict(body []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}
		return errors.New("trailing JSON value")
	}
	return nil
}

func readPinnedFile(path, expectedSHA256 string, maximum int64) ([]byte, error) {
	if !canonicalAbsolutePath(path) || !lowerSHA256.MatchString(expectedSHA256) || maximum <= 0 {
		return nil, errors.New("pinned input identity is invalid")
	}
	before, err := os.Lstat(path)
	if err != nil || before.Mode().Type() != 0 || before.Size() <= 0 || before.Size() > maximum {
		return nil, errors.New("pinned input is not a bounded direct regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, errors.New("pinned input changed while being opened")
	}
	body, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(body)) != before.Size() {
		return nil, errors.Join(errors.New("pinned input size changed while reading"), err)
	}
	after, err := file.Stat()
	pathAfter, pathErr := os.Lstat(path)
	if err != nil || pathErr != nil || !os.SameFile(before, after) || !os.SameFile(before, pathAfter) ||
		after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return nil, errors.New("pinned input changed while hashing")
	}
	if actual := sha256Hex(body); actual != expectedSHA256 {
		return nil, fmt.Errorf("pinned input SHA-256=%s, want %s", actual, expectedSHA256)
	}
	return body, nil
}

func requireRunOutputsAbsent(outputs lyricsextractionplan.RecoveryOutputs) error {
	for _, path := range []string{
		outputs.Ledger, outputs.AcquisitionSet, outputs.ProviderOutcomes,
		outputs.SongResults, outputs.EvidencePack, outputs.RootManifest,
	} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			if err == nil {
				return fmt.Errorf("planned create-exclusive output already exists: %s", path)
			}
			return err
		}
	}
	return nil
}

func requirePrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return errors.New("directory must be a direct mode-0700 directory")
	}
	return nil
}

func writePrivateExclusive(path string, body []byte) (returnErr error) {
	if len(body) == 0 || len(body) > lyricsextractionplan.MaxPlanBytes {
		return errors.New("canonical plan body is invalid")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	if _, err := file.Write(body); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil || info.Mode().Perm() != 0o600 || info.Size() != int64(len(body)) {
		return errors.New("published canonical plan mode or size is invalid")
	}
	return nil
}

func canonicalAbsolutePath(value string) bool {
	return value != "" && filepath.IsAbs(value) && filepath.Clean(value) == value && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\r\n\x00")
}

func mustFileSize(path string) int64 {
	info, err := os.Lstat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func sha256Hex(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}
