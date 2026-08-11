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
	"strings"

	_ "modernc.org/sqlite"

	"moesekai/server/internal/lyricssource"
)

var canonicalSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)

type urlReport struct {
	Provider  string                                `json:"provider"`
	MusicID   int                                   `json:"musicId"`
	URLExists bool                                  `json:"urlExists"`
	Status    lyricssource.MoegirlPageURLStatus     `json:"status"`
	Batch     lyricssource.MoegirlURLPreflightBatch `json:"batch"`
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

type extractionReport struct {
	SchemaVersion   int                                  `json:"schemaVersion"`
	Provider        string                               `json:"provider"`
	URLReportSHA256 string                               `json:"urlReportSha256"`
	RawHTMLSHA256   string                               `json:"rawHtmlSha256"`
	CatalogSHA256   string                               `json:"catalogSha256"`
	Catalog         catalogIdentity                      `json:"catalog"`
	PageURL         string                               `json:"pageUrl"`
	PageTitle       string                               `json:"pageTitle"`
	JapaneseTitle   string                               `json:"japaneseTitle"`
	PageID          int                                  `json:"pageId"`
	RevisionID      int                                  `json:"revisionId"`
	FetchedAt       string                               `json:"fetchedAt"`
	RightsNotice    string                               `json:"rightsNotice"`
	LineCount       int                                  `json:"lineCount"`
	StanzaCount     int                                  `json:"stanzaCount"`
	Lines           []lyricssource.MoegirlPublicHTMLLine `json:"lines"`
}

type options struct {
	urlReportPath, expectedURLReportSHA   string
	htmlPath, expectedHTMLSHA             string
	catalogPath, expectedCatalogSHA       string
	expectedCatalogCount, expectedMusicID int
	outputPath                            string
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string, output io.Writer) error {
	parsed, err := parseOptions(arguments)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	urlBody, err := readPinnedFile(parsed.urlReportPath, parsed.expectedURLReportSHA, 1<<20)
	if err != nil {
		return err
	}
	var source urlReport
	if err := json.Unmarshal(urlBody, &source); err != nil || source.Provider != "moegirl" ||
		!source.URLExists || source.MusicID != parsed.expectedMusicID || source.Status.StatusCode != 200 ||
		source.Status.PageURL == "" || source.Status.FinalURL != source.Status.PageURL || source.Status.Redirected ||
		source.Status.ContentType != "text/html" || source.Batch.RequestURL != source.Status.PageURL ||
		source.Batch.RawSHA256 != parsed.expectedHTMLSHA {
		return errors.New("exact Moegirl public URL report is invalid or incomplete")
	}
	htmlBody, err := readPinnedFile(parsed.htmlPath, parsed.expectedHTMLSHA, 2<<20)
	if err != nil {
		return err
	}
	if source.Status.ContentBytes != len(htmlBody) {
		return errors.New("exact Moegirl public HTML size does not match its URL report")
	}
	extraction, err := lyricssource.ParseMoegirlPublicPageHTML(htmlBody, source.Status.PageURL)
	if err != nil {
		return err
	}
	catalogBody, err := readPinnedFile(parsed.catalogPath, parsed.expectedCatalogSHA, 64<<20)
	if err != nil {
		return err
	}
	_ = catalogBody
	catalog, err := openCatalog(parsed.catalogPath, parsed.expectedCatalogCount)
	if err != nil {
		return err
	}
	defer catalog.Close()
	identity, err := readCatalogIdentity(catalog, parsed.expectedMusicID)
	if err != nil {
		return err
	}
	if extraction.JapaneseTitle != identity.JapaneseTitle ||
		!catalogContributorContains(identity.ProducerMetadata, "いのうつはSA") ||
		identity.Lyricist != "いのうつはSA" || identity.Composer != "いのうつはSA" || identity.Arranger != "いのうつはSA" {
		return errors.New("exact Moegirl public HTML identity does not match catalog music 795")
	}
	stanzas := 1
	for _, line := range extraction.Lines {
		if line.StanzaBreakBefore {
			stanzas++
		}
	}
	result := extractionReport{
		SchemaVersion: 1, Provider: "moegirl_public_exact",
		URLReportSHA256: parsed.expectedURLReportSHA, RawHTMLSHA256: parsed.expectedHTMLSHA,
		CatalogSHA256: parsed.expectedCatalogSHA, Catalog: identity,
		PageURL: extraction.PageURL, PageTitle: extraction.PageTitle,
		JapaneseTitle: extraction.JapaneseTitle, PageID: extraction.PageID,
		RevisionID: extraction.RevisionID, FetchedAt: source.Batch.FetchedAt,
		RightsNotice: extraction.RightsNotice, LineCount: len(extraction.Lines),
		StanzaCount: stanzas, Lines: extraction.Lines,
	}
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if err := writePrivateExclusive(parsed.outputPath, append(body, '\n')); err != nil {
		return err
	}
	_, err = fmt.Fprintf(output,
		"PASS mode=moegirl-public-html-extract musicID=%d pageID=%d revisionID=%d lines=%d stanzas=%d output=%s\n",
		identity.MusicID, extraction.PageID, extraction.RevisionID, len(extraction.Lines), stanzas, parsed.outputPath,
	)
	return err
}

func parseOptions(arguments []string) (options, error) {
	var parsed options
	flags := flag.NewFlagSet("moegirl-public-html-extract", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&parsed.urlReportPath, "url-report", "", "successful exact URL report")
	flags.StringVar(&parsed.expectedURLReportSHA, "expected-url-report-sha256", "", "exact URL report SHA-256")
	flags.StringVar(&parsed.htmlPath, "html", "", "retained exact public HTML")
	flags.StringVar(&parsed.expectedHTMLSHA, "expected-html-sha256", "", "exact HTML SHA-256")
	flags.StringVar(&parsed.catalogPath, "catalog", "", "immutable catalog database")
	flags.StringVar(&parsed.expectedCatalogSHA, "expected-catalog-sha256", "", "exact catalog SHA-256")
	flags.IntVar(&parsed.expectedCatalogCount, "expected-catalog-count", 0, "exact catalog row count")
	flags.IntVar(&parsed.expectedMusicID, "expected-music-id", 0, "exact music ID")
	flags.StringVar(&parsed.outputPath, "output", "", "create-exclusive private extraction report")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 {
		return options{}, errors.New("Moegirl public HTML extraction accepts only named flags")
	}
	for _, path := range []string{parsed.urlReportPath, parsed.htmlPath, parsed.catalogPath, parsed.outputPath} {
		if path == "" || strings.TrimSpace(path) != path || !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return options{}, errors.New("Moegirl public HTML extraction paths must be canonical and absolute")
		}
	}
	if !canonicalSHA256.MatchString(parsed.expectedURLReportSHA) ||
		!canonicalSHA256.MatchString(parsed.expectedHTMLSHA) ||
		!canonicalSHA256.MatchString(parsed.expectedCatalogSHA) ||
		parsed.expectedCatalogCount < 1 || parsed.expectedMusicID <= 0 {
		return options{}, errors.New("Moegirl public HTML extraction pins are invalid")
	}
	return parsed, nil
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

func readCatalogIdentity(database *sql.DB, musicID int) (catalogIdentity, error) {
	var result catalogIdentity
	err := database.QueryRow(`
		SELECT music_id, title_ja, producer_metadata, lyricist, composer, arranger, lyrics_version
		FROM catalog_music WHERE music_id = ?`, musicID).Scan(
		&result.MusicID, &result.JapaneseTitle, &result.ProducerMetadata,
		&result.Lyricist, &result.Composer, &result.Arranger, &result.LyricsVersion,
	)
	if err != nil || result.MusicID != musicID || strings.TrimSpace(result.JapaneseTitle) == "" {
		return catalogIdentity{}, errors.New("catalog music identity is missing or invalid")
	}
	return result, nil
}

func catalogContributorContains(metadata, wanted string) bool {
	for _, value := range strings.Split(metadata, "|") {
		if strings.TrimSpace(value) == wanted {
			return true
		}
	}
	return false
}

func readPinnedFile(path, expectedSHA string, maximum int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Type() != 0 || info.Size() <= 0 || info.Size() > maximum {
		return nil, errors.New("pinned extraction input is not a bounded regular file")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(body)
	if actual := hex.EncodeToString(digest[:]); actual != expectedSHA {
		return nil, fmt.Errorf("pinned extraction input SHA-256=%s, want %s", actual, expectedSHA)
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, after) || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return nil, errors.New("pinned extraction input changed while being read")
	}
	return body, nil
}

func writePrivateExclusive(path string, body []byte) error {
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil || !parent.IsDir() || parent.Mode().Perm() != 0o700 || len(body) == 0 {
		return errors.New("private extraction output parent or body is invalid")
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
