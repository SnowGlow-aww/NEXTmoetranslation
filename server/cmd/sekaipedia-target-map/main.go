package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"moesekai/server/internal/httpx"
	"moesekai/server/internal/lyricsprovidercoord"
	"moesekai/server/internal/lyricsproviderpolicy"
	"moesekai/server/internal/lyricssource"
)

const authorization = "MOESEKAI_SEKAIPEDIA_TARGET_MAP_AUTHORIZED"

var canonicalSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)

type options struct {
	urlReportPath        string
	expectedURLReportSHA string
	catalogPath          string
	expectedCatalogSHA   string
	expectedCatalogCount int
	outputRoot           string
	authorization        string
	crawlDelay           time.Duration
	requestTimeout       time.Duration
	maxAttempts          int
	retryDelay           time.Duration
}

type urlReport struct {
	AllURLsExist bool                                   `json:"allUrlsExist"`
	TargetCount  int                                    `json:"targetCount"`
	Statuses     []lyricssource.SekaipediaPageURLStatus `json:"statuses"`
}

type catalogSong struct {
	MusicID       int    `json:"musicId"`
	JapaneseTitle string `json:"japaneseTitle"`
}

type mappingRecord struct {
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

type mapReport struct {
	SchemaVersion          int                                       `json:"schemaVersion"`
	Provider               string                                    `json:"provider"`
	URLReportSHA256        string                                    `json:"urlReportSha256"`
	CatalogSHA256          string                                    `json:"catalogSha256"`
	SourceTargetCount      int                                       `json:"sourceTargetCount"`
	MetadataMappedCount    int                                       `json:"metadataMappedCount"`
	CatalogMappedCount     int                                       `json:"catalogMappedCount"`
	SourceOnlyCount        int                                       `json:"sourceOnlyCount"`
	CatalogExcludedCount   int                                       `json:"catalogExcludedCount"`
	CheckedAt              string                                    `json:"checkedAt"`
	Complete               bool                                      `json:"complete"`
	Failure                string                                    `json:"failure,omitempty"`
	Mappings               []mappingRecord                           `json:"mappings"`
	SourceOnly             []lyricssource.SekaipediaSongPageMetadata `json:"sourceOnly"`
	UnsupportedSourcePages []lyricssource.SekaipediaSongPageMetadata `json:"unsupportedSourcePages"`
	CatalogExcluded        []catalogSong                             `json:"catalogExcluded"`
	MetadataBatches        []lyricssource.SekaipediaMetadataBatch    `json:"metadataBatches"`
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string, output io.Writer) (resultErr error) {
	parsed, err := parseOptions(arguments)
	if err != nil {
		return err
	}
	if parsed.authorization != authorization {
		_, writeErr := fmt.Fprintln(output, "HOLD mode=sekaipedia-target-map authorization=missing network=HOLD")
		return writeErr
	}
	urlRaw, err := readPinnedFile(parsed.urlReportPath, parsed.expectedURLReportSHA, 4<<20)
	if err != nil {
		return err
	}
	var urls urlReport
	if err := json.Unmarshal(urlRaw, &urls); err != nil || !urls.AllURLsExist || urls.TargetCount != len(urls.Statuses) || len(urls.Statuses) == 0 {
		return errors.New("Sekaipedia URL report is incomplete")
	}
	catalogRaw, err := readPinnedFile(parsed.catalogPath, parsed.expectedCatalogSHA, 64<<20)
	if err != nil {
		return err
	}
	_ = catalogRaw
	catalog, err := openCatalog(parsed.catalogPath, parsed.expectedCatalogCount)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, catalog.Close()) }()
	if err := createPrivateOutputRoot(parsed.outputRoot); err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(parsed.outputRoot)
		}
	}()

	owner, err := lyricsprovidercoord.AcquireDefault()
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, owner.Close()) }()
	client := httpx.NewUpstreamClientWithOptions(httpx.UpstreamClientOptions{
		Timeout: parsed.requestTimeout, DialTimeout: 10 * time.Second,
		TLSHandshakeTimeout: 12 * time.Second, ResponseHeaderTimeout: 20 * time.Second,
		Policy: httpx.UpstreamPolicyFromEnvironment(), AllowQuery: true,
	})
	wrapped, err := owner.Wrap(lyricsproviderpolicy.ProviderSekaipedia, client.Transport)
	if err != nil {
		return err
	}
	client.Transport = wrapped
	metadata, batches, metadataErr := lyricssource.FetchSekaipediaSongPageMetadata(
		ctx, urls.Statuses, client, parsed.crawlDelay, 10*time.Second, parsed.maxAttempts, parsed.retryDelay,
	)
	resolveErr := owner.ResolveProvider(lyricsproviderpolicy.ProviderSekaipedia)

	responsesRoot := filepath.Join(parsed.outputRoot, "responses")
	if err := os.Mkdir(responsesRoot, 0o700); err != nil {
		return err
	}
	for index := range batches {
		path := filepath.Join(responsesRoot, fmt.Sprintf("batch-%04d.json", index+1))
		if err := writePrivateExclusive(path, batches[index].Raw); err != nil {
			return err
		}
		batches[index].Raw = nil
	}

	catalogSongs, err := readCatalogSongs(catalog)
	if err != nil {
		return err
	}
	result, mappingErr := buildMapReport(
		metadata, catalogSongs, batches, parsed.expectedURLReportSHA, parsed.expectedCatalogSHA,
	)
	combinedErr := errors.Join(metadataErr, resolveErr, mappingErr)
	result.Complete = combinedErr == nil
	if combinedErr != nil {
		result.Failure = combinedErr.Error()
	}
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if err := writePrivateExclusive(filepath.Join(parsed.outputRoot, "report.json"), append(body, '\n')); err != nil {
		return err
	}
	if err := syncDirectory(parsed.outputRoot); err != nil {
		return err
	}
	cleanup = false
	if combinedErr != nil {
		return combinedErr
	}
	_, err = fmt.Fprintf(output,
		"PASS mode=sekaipedia-target-map source=%d metadataMapped=%d catalogMapped=%d sourceOnly=%d catalogExcluded=%d output=%s\n",
		result.SourceTargetCount, result.MetadataMappedCount, result.CatalogMappedCount,
		result.SourceOnlyCount, result.CatalogExcludedCount, parsed.outputRoot,
	)
	return err
}

func buildMapReport(
	metadata []lyricssource.SekaipediaSongPageMetadata,
	catalogSongs map[int]catalogSong,
	batches []lyricssource.SekaipediaMetadataBatch,
	urlSHA, catalogSHA string,
) (mapReport, error) {
	result := mapReport{
		SchemaVersion: 1, Provider: "sekaipedia", URLReportSHA256: urlSHA, CatalogSHA256: catalogSHA,
		SourceTargetCount: len(metadata), CheckedAt: time.Now().UTC().Format(time.RFC3339Nano), MetadataBatches: batches,
		Mappings: []mappingRecord{}, SourceOnly: []lyricssource.SekaipediaSongPageMetadata{},
		UnsupportedSourcePages: []lyricssource.SekaipediaSongPageMetadata{}, CatalogExcluded: []catalogSong{},
	}
	mappedCatalog := make(map[int]struct{}, len(metadata))
	seenSourceMusicIDs := make(map[int]string, len(metadata))
	var failures []string
	for _, source := range metadata {
		if source.Status != lyricssource.SekaipediaMetadataMapped {
			result.UnsupportedSourcePages = append(result.UnsupportedSourcePages, source)
			continue
		}
		result.MetadataMappedCount++
		if previous := seenSourceMusicIDs[source.MusicID]; previous != "" {
			failures = append(failures, fmt.Sprintf("music ID %d is shared by %q and %q", source.MusicID, previous, source.PageTitle))
			continue
		}
		seenSourceMusicIDs[source.MusicID] = source.PageTitle
		catalog, found := catalogSongs[source.MusicID]
		if !found {
			result.SourceOnly = append(result.SourceOnly, source)
			continue
		}
		mappedCatalog[source.MusicID] = struct{}{}
		result.Mappings = append(result.Mappings, mappingRecord{
			MusicID: source.MusicID, CatalogJapaneseTitle: catalog.JapaneseTitle,
			SekaipediaJapaneseTitle: source.JapaneseTitle, PageTitle: source.PageTitle, CanonicalURL: source.CanonicalURL,
			ResolvedPageTitle: source.ResolvedPageTitle, ResolvedCanonicalURL: source.ResolvedCanonicalURL,
			PageID: source.PageID, RevisionID: source.RevisionID, RevisionTimestamp: source.RevisionTimestamp, SHA1: source.SHA1,
		})
	}
	for musicID, song := range catalogSongs {
		if _, found := mappedCatalog[musicID]; !found {
			result.CatalogExcluded = append(result.CatalogExcluded, song)
		}
	}
	sort.Slice(result.Mappings, func(i, j int) bool { return result.Mappings[i].MusicID < result.Mappings[j].MusicID })
	sort.Slice(result.SourceOnly, func(i, j int) bool { return result.SourceOnly[i].PageID < result.SourceOnly[j].PageID })
	sort.Slice(result.UnsupportedSourcePages, func(i, j int) bool {
		return result.UnsupportedSourcePages[i].PageID < result.UnsupportedSourcePages[j].PageID
	})
	sort.Slice(result.CatalogExcluded, func(i, j int) bool { return result.CatalogExcluded[i].MusicID < result.CatalogExcluded[j].MusicID })
	result.CatalogMappedCount = len(result.Mappings)
	result.SourceOnlyCount = len(result.SourceOnly)
	result.CatalogExcludedCount = len(result.CatalogExcluded)
	if len(failures) > 0 {
		return result, errors.New(strings.Join(failures, "; "))
	}
	return result, nil
}

func openCatalog(path string, expectedCount int) (*sql.DB, error) {
	database, err := sql.Open("sqlite", "file:"+path+"?mode=ro&immutable=1")
	if err != nil {
		return nil, err
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM catalog_music`).Scan(&count); err != nil || count != expectedCount {
		_ = database.Close()
		return nil, fmt.Errorf("catalog count=%d, want %d: %w", count, expectedCount, err)
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

func parseOptions(arguments []string) (options, error) {
	var parsed options
	flags := flag.NewFlagSet("sekaipedia-target-map", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&parsed.urlReportPath, "url-report", "", "successful URL preflight report")
	flags.StringVar(&parsed.expectedURLReportSHA, "expected-url-report-sha256", "", "exact URL report SHA-256")
	flags.StringVar(&parsed.catalogPath, "catalog", "", "immutable catalog database")
	flags.StringVar(&parsed.expectedCatalogSHA, "expected-catalog-sha256", "", "exact catalog SHA-256")
	flags.IntVar(&parsed.expectedCatalogCount, "expected-catalog-count", 0, "exact catalog row count")
	flags.StringVar(&parsed.outputRoot, "output", "", "create-exclusive private output root")
	flags.StringVar(&parsed.authorization, "authorization", "", "explicit live metadata authorization")
	flags.DurationVar(&parsed.crawlDelay, "crawl-delay", 10*time.Second, "minimum request start interval")
	flags.DurationVar(&parsed.requestTimeout, "request-timeout", 2*time.Minute, "per-request timeout")
	flags.IntVar(&parsed.maxAttempts, "max-attempts", 5, "bounded retry attempts")
	flags.DurationVar(&parsed.retryDelay, "retry-delay", 30*time.Second, "additional retry delay")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 {
		return options{}, errors.New("Sekaipedia target map accepts only explicit named flags")
	}
	for _, path := range []string{parsed.urlReportPath, parsed.catalogPath, parsed.outputRoot} {
		if path == "" || strings.TrimSpace(path) != path || !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return options{}, errors.New("Sekaipedia target-map paths must be canonical and absolute")
		}
	}
	if !canonicalSHA256.MatchString(parsed.expectedURLReportSHA) || !canonicalSHA256.MatchString(parsed.expectedCatalogSHA) ||
		parsed.expectedCatalogCount < 1 || parsed.crawlDelay < 10*time.Second || parsed.requestTimeout <= 0 ||
		parsed.maxAttempts < 1 || parsed.maxAttempts > 5 || parsed.retryDelay < 0 {
		return options{}, errors.New("Sekaipedia target-map bounds are invalid")
	}
	return parsed, nil
}

func readPinnedFile(path, expectedSHA string, maximum int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Type() != 0 || info.Size() <= 0 || info.Size() > maximum {
		return nil, errors.New("pinned input is not a bounded regular file")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(body)
	if actual := hex.EncodeToString(digest[:]); actual != expectedSHA {
		return nil, fmt.Errorf("pinned input SHA-256=%s, want %s", actual, expectedSHA)
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, after) || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return nil, errors.New("pinned input changed while being read")
	}
	return body, nil
}

func createPrivateOutputRoot(path string) error {
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil || !parent.IsDir() || parent.Mode().Perm() != 0o700 {
		return errors.New("target-map output parent must exist with mode 0700")
	}
	return os.Mkdir(path, 0o700)
}

func writePrivateExclusive(path string, body []byte) error {
	if len(body) == 0 {
		return errors.New("refusing to publish an empty target-map artifact")
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
	return errors.Join(writeErr, file.Close())
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
