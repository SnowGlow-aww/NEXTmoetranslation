// Package embeddedlyricsseed validates the private, image-embedded editor seed.
// It contains only editor-required immutable source structure and availability
// state. Raw provider payloads, recovery evidence, credentials, accounts, and
// application settings are outside this package and archive contract.
package embeddedlyricsseed

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"moesekai/server/internal/legacy"
	"moesekai/server/internal/model"
)

const (
	SchemaVersion         = 1
	ManifestName          = "manifest.json"
	DocumentsName         = "source-documents.json"
	ArtifactsName         = "source-artifacts.json"
	ContributionsName     = "source-contributions.json"
	LegacyDocumentsName   = "legacy-documents.json"
	LegacyLinesName       = "legacy-lines.json"
	LegacySegmentsName    = "legacy-segments.json"
	AvailabilityName      = "availability-documents.json"
	ExpectedCatalogCount  = 700
	ExpectedSourceV3      = 652
	ExpectedLegacy        = 1
	ExpectedAvailability  = 47
	ExpectedArtifacts     = 785
	ExpectedContributions = 4893
	maxArchiveBytes       = 64 << 20
	maxTarBytes           = 192 << 20
	maxEntryBytes         = 64 << 20
)

var canonicalSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Manifest struct {
	SchemaVersion             int           `json:"schemaVersion"`
	ReleaseID                 string        `json:"releaseId"`
	SourceBatchSHA256         string        `json:"sourceBatchSha256"`
	RootSHA256                string        `json:"rootSha256"`
	CatalogPolicyVersion      string        `json:"catalogPolicyVersion"`
	CatalogCount              int           `json:"catalogCount"`
	MusicIDsSHA256            string        `json:"musicIdsSha256"`
	CatalogFingerprintsSHA256 string        `json:"catalogFingerprintsSha256"`
	SeedSHA256                string        `json:"seedSha256"`
	CreatedAt                 int64         `json:"createdAt"`
	Items                     []CatalogItem `json:"items"`
	Files                     []FileRecord  `json:"files"`
}

type CatalogItem struct {
	MusicID            int    `json:"musicId"`
	JapaneseTitle      string `json:"japaneseTitle"`
	CatalogFingerprint string `json:"catalogFingerprint"`
	State              string `json:"state"`
	SeedKind           string `json:"seedKind"`
	ResultSHA256       string `json:"resultSha256"`
	DocumentSHA256     string `json:"documentSha256,omitempty"`
	AvailabilitySHA256 string `json:"availabilitySha256,omitempty"`
	CreatedAt          int64  `json:"createdAt"`
}

type FileRecord struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
	Count  int    `json:"count"`
}

type SourceDocumentRecord struct {
	MusicID             int    `json:"musicId"`
	SchemaVersion       int    `json:"schemaVersion"`
	ReasonCode          string `json:"reasonCode"`
	DocumentJSON        string `json:"documentJson"`
	DocumentSHA256      string `json:"documentSha256"`
	ManifestBatchSHA256 string `json:"manifestBatchSha256"`
	CreatedAt           int64  `json:"createdAt"`
}

type SourceArtifactRecord struct {
	MusicID                 int    `json:"musicId"`
	Provider                string `json:"provider"`
	RenditionKey            string `json:"renditionKey"`
	Origin                  string `json:"origin"`
	PageID                  int    `json:"pageId"`
	RevisionID              int    `json:"revisionId"`
	RevisionTimestamp       string `json:"revisionTimestamp"`
	MediaWikiSHA1           string `json:"mediawikiSha1"`
	PageTitle               string `json:"pageTitle"`
	CanonicalRevisionURL    string `json:"canonicalRevisionUrl"`
	FetchedAt               string `json:"fetchedAt"`
	CategoriesJSON          string `json:"categoriesJson"`
	Section                 string `json:"section"`
	CompositionRenditionKey string `json:"compositionRenditionKey"`
	VersionReason           string `json:"versionReason"`
	IndexEvidenceRefsJSON   string `json:"indexEvidenceRefsJson"`
	FixedIdentityJSON       string `json:"fixedIdentityJson"`
	FixedIdentitySHA256     string `json:"fixedIdentitySha256"`
	RawByteCount            int    `json:"rawByteCount"`
	RawWikitextSHA256       string `json:"rawWikitextSha256"`
	ArtifactSHA256          string `json:"artifactSha256"`
}

type SourceContributionRecord struct {
	MusicID            int    `json:"musicId"`
	Component          string `json:"component"`
	RenditionKey       string `json:"renditionKey"`
	ContributionSHA256 string `json:"contributionSha256"`
}

type LegacyDocumentRecord struct {
	MusicID                int    `json:"musicId"`
	Revision               int    `json:"revision"`
	UpdatedAt              int64  `json:"updatedAt"`
	UpdatedBy              string `json:"updatedBy"`
	Attribution            string `json:"attribution"`
	TranslationCredit      string `json:"translationCredit"`
	ProofreadingCredit     string `json:"proofreadingCredit"`
	SourceNote             string `json:"sourceNote"`
	SourceURL              string `json:"sourceUrl"`
	LicenseNote            string `json:"licenseNote"`
	SourceHash             string `json:"sourceHash"`
	SourcePageID           int    `json:"sourcePageId"`
	SourceRevisionID       int    `json:"sourceRevisionId"`
	SourceSHA1             string `json:"sourceSha1"`
	SourceFetchedAt        int64  `json:"sourceFetchedAt"`
	SourceFetchedAtRFC3339 string `json:"sourceFetchedAtRfc3339"`
}

type LegacyLineRecord struct {
	MusicID           int    `json:"musicId"`
	LineID            string `json:"lineId"`
	Position          int    `json:"position"`
	Japanese          string `json:"japanese"`
	Chinese           string `json:"zh-CN"`
	English           string `json:"en-US"`
	StanzaBreakBefore int    `json:"stanzaBreakBefore"`
}

type LegacySegmentRecord struct {
	MusicID          int    `json:"musicId"`
	LineID           string `json:"lineId"`
	Position         int    `json:"position"`
	Text             string `json:"text"`
	PerformerIDsJSON string `json:"performerIdsJson"`
	RubyJSON         string `json:"rubyJson"`
}

type AvailabilityRecord struct {
	MusicID        int    `json:"musicId"`
	SchemaVersion  int    `json:"schemaVersion"`
	State          string `json:"state"`
	ReasonCode     string `json:"reasonCode"`
	NoLyricsReason string `json:"noLyricsReason"`
	DocumentJSON   string `json:"documentJson"`
	DocumentSHA256 string `json:"documentSha256"`
	ResultSHA256   string `json:"resultSha256"`
	CreatedAt      int64  `json:"createdAt"`
}

type Bundle struct {
	ArchiveSHA256   string
	Manifest        Manifest
	Documents       []SourceDocumentRecord
	Artifacts       []SourceArtifactRecord
	Contributions   []SourceContributionRecord
	LegacyDocuments []LegacyDocumentRecord
	LegacyLines     []LegacyLineRecord
	LegacySegments  []LegacySegmentRecord
	Availability    []AvailabilityRecord
}

func DecodeArchive(archive []byte) (Bundle, error) {
	if len(archive) == 0 || len(archive) > maxArchiveBytes {
		return Bundle{}, errors.New("embedded lyrics editor seed archive size is invalid")
	}
	archiveDigest := sha256.Sum256(archive)
	bundle := Bundle{ArchiveSHA256: hex.EncodeToString(archiveDigest[:])}
	files, err := decodeTarGzip(archive)
	if err != nil {
		return Bundle{}, err
	}
	manifestBody, ok := files[ManifestName]
	if !ok {
		return Bundle{}, errors.New("embedded lyrics editor seed manifest is missing")
	}
	if err := decodeClosedJSON(manifestBody, &bundle.Manifest); err != nil {
		return Bundle{}, fmt.Errorf("decode embedded lyrics editor seed manifest: %w", err)
	}
	delete(files, ManifestName)
	if err := validateManifest(bundle.Manifest, files); err != nil {
		return Bundle{}, err
	}
	for _, target := range []struct {
		name  string
		value any
	}{
		{DocumentsName, &bundle.Documents},
		{ArtifactsName, &bundle.Artifacts},
		{ContributionsName, &bundle.Contributions},
		{LegacyDocumentsName, &bundle.LegacyDocuments},
		{LegacyLinesName, &bundle.LegacyLines},
		{LegacySegmentsName, &bundle.LegacySegments},
		{AvailabilityName, &bundle.Availability},
	} {
		if err := decodeClosedJSON(files[target.name], target.value); err != nil {
			return Bundle{}, fmt.Errorf("decode embedded lyrics editor seed %s: %w", target.name, err)
		}
	}
	if err := validateBundle(bundle); err != nil {
		return Bundle{}, err
	}
	return bundle, nil
}

func decodeTarGzip(archive []byte) (map[string][]byte, error) {
	compressed, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("open embedded lyrics editor seed archive: %w", err)
	}
	defer compressed.Close()
	compressed.Multistream(false)
	if compressed.Name != "" || compressed.Comment != "" || len(compressed.Extra) != 0 || !compressed.ModTime.IsZero() {
		return nil, errors.New("embedded lyrics editor seed gzip metadata is not canonical")
	}
	limited := &io.LimitedReader{R: compressed, N: maxTarBytes + 1}
	reader := tar.NewReader(limited)
	files := map[string][]byte{}
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read embedded lyrics editor seed archive: %w", err)
		}
		if header.Typeflag != tar.TypeReg || header.Mode != 0o400 || header.Uid != 0 || header.Gid != 0 ||
			header.Uname != "" || header.Gname != "" || header.Linkname != "" || header.Devmajor != 0 || header.Devminor != 0 ||
			header.ModTime.Unix() != 0 || !header.AccessTime.IsZero() || !header.ChangeTime.IsZero() ||
			len(header.PAXRecords) != 0 || len(header.Xattrs) != 0 || path.Clean(header.Name) != header.Name || strings.Contains(header.Name, "/") {
			return nil, fmt.Errorf("invalid embedded lyrics editor seed entry %q", header.Name)
		}
		if header.Size < 2 || header.Size > maxEntryBytes {
			return nil, fmt.Errorf("invalid embedded lyrics editor seed entry size for %q", header.Name)
		}
		if _, duplicate := files[header.Name]; duplicate {
			return nil, fmt.Errorf("duplicate embedded lyrics editor seed entry %q", header.Name)
		}
		body, err := io.ReadAll(io.LimitReader(reader, maxEntryBytes+1))
		if err != nil || int64(len(body)) != header.Size {
			return nil, fmt.Errorf("read embedded lyrics editor seed entry %q", header.Name)
		}
		files[header.Name] = body
	}
	if limited.N <= 0 {
		return nil, errors.New("embedded lyrics editor seed tar exceeds its size limit")
	}
	return files, nil
}

func decodeClosedJSON(body []byte, target any) error {
	if len(body) == 0 || !utf8.Valid(body) {
		return errors.New("JSON body is empty or invalid UTF-8")
	}
	if err := legacy.ValidateUniqueJSON(body); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func validateManifest(manifest Manifest, files map[string][]byte) error {
	if manifest.SchemaVersion != SchemaVersion || strings.TrimSpace(manifest.ReleaseID) != manifest.ReleaseID || manifest.ReleaseID == "" ||
		!canonicalSHA256.MatchString(manifest.SourceBatchSHA256) || !canonicalSHA256.MatchString(manifest.RootSHA256) ||
		manifest.CatalogPolicyVersion != model.LyricsCatalogIdentityPolicyVersion || manifest.CatalogCount != ExpectedCatalogCount ||
		!canonicalSHA256.MatchString(manifest.MusicIDsSHA256) || !canonicalSHA256.MatchString(manifest.CatalogFingerprintsSHA256) ||
		!canonicalSHA256.MatchString(manifest.SeedSHA256) || manifest.CreatedAt <= 0 || len(manifest.Items) != ExpectedCatalogCount {
		return errors.New("embedded lyrics editor seed manifest identity is invalid")
	}
	if len(manifest.Files) != len(files) {
		return errors.New("embedded lyrics editor seed file inventory count differs")
	}
	seenFiles := map[string]bool{}
	for _, record := range manifest.Files {
		body, exists := files[record.Name]
		digest := sha256.Sum256(body)
		if !exists || seenFiles[record.Name] || record.Bytes != len(body) || record.Count < 0 ||
			!canonicalSHA256.MatchString(record.SHA256) || record.SHA256 != hex.EncodeToString(digest[:]) {
			return fmt.Errorf("embedded lyrics editor seed file inventory is invalid for %q", record.Name)
		}
		seenFiles[record.Name] = true
	}
	for _, name := range []string{DocumentsName, ArtifactsName, ContributionsName, LegacyDocumentsName, LegacyLinesName, LegacySegmentsName, AvailabilityName} {
		if !seenFiles[name] {
			return fmt.Errorf("embedded lyrics editor seed file %q is missing", name)
		}
	}
	return nil
}

func validateBundle(bundle Bundle) error {
	manifest := bundle.Manifest
	fileCount := func(name string) int {
		for _, record := range manifest.Files {
			if record.Name == name {
				return record.Count
			}
		}
		return -1
	}
	if len(bundle.Documents) != ExpectedSourceV3 || len(bundle.Artifacts) != ExpectedArtifacts ||
		len(bundle.Contributions) != ExpectedContributions || len(bundle.LegacyDocuments) != ExpectedLegacy ||
		len(bundle.Availability) != ExpectedAvailability || fileCount(DocumentsName) != len(bundle.Documents) ||
		fileCount(ArtifactsName) != len(bundle.Artifacts) || fileCount(ContributionsName) != len(bundle.Contributions) ||
		fileCount(LegacyDocumentsName) != len(bundle.LegacyDocuments) || fileCount(LegacyLinesName) != len(bundle.LegacyLines) ||
		fileCount(LegacySegmentsName) != len(bundle.LegacySegments) || fileCount(AvailabilityName) != len(bundle.Availability) {
		return errors.New("embedded lyrics editor seed record counts differ")
	}
	items := make(map[int]CatalogItem, len(manifest.Items))
	musicIDs := make([]int, 0, len(manifest.Items))
	catalogDigest := sha256.New()
	lastMusicID := 0
	kindCounts := map[string]int{}
	for _, item := range manifest.Items {
		if item.MusicID <= lastMusicID || strings.TrimSpace(item.JapaneseTitle) != item.JapaneseTitle || item.JapaneseTitle == "" ||
			!canonicalSHA256.MatchString(item.CatalogFingerprint) || !canonicalSHA256.MatchString(item.ResultSHA256) || item.CreatedAt <= 0 {
			return fmt.Errorf("embedded lyrics editor seed item %d is invalid or unordered", item.MusicID)
		}
		switch item.SeedKind {
		case "source_v3":
			if (item.State != "complete" && item.State != "game_only") || !canonicalSHA256.MatchString(item.DocumentSHA256) || item.AvailabilitySHA256 != "" {
				return fmt.Errorf("embedded lyrics editor seed source-v3 item %d is invalid", item.MusicID)
			}
		case "legacy":
			if item.State != "complete" || !canonicalSHA256.MatchString(item.DocumentSHA256) || item.AvailabilitySHA256 != "" {
				return fmt.Errorf("embedded lyrics editor seed legacy item %d is invalid", item.MusicID)
			}
		case "availability":
			if item.State != "satisfied_no_lyrics" && item.State != "incomplete" && item.State != "ambiguous" && item.State != "missing" && item.State != "failed" {
				return fmt.Errorf("embedded lyrics editor seed availability item %d has invalid state", item.MusicID)
			}
			if item.DocumentSHA256 != "" || !canonicalSHA256.MatchString(item.AvailabilitySHA256) {
				return fmt.Errorf("embedded lyrics editor seed availability item %d is invalid", item.MusicID)
			}
		default:
			return fmt.Errorf("embedded lyrics editor seed item %d has unknown kind %q", item.MusicID, item.SeedKind)
		}
		items[item.MusicID] = item
		musicIDs = append(musicIDs, item.MusicID)
		kindCounts[item.SeedKind]++
		catalogDigest.Write([]byte(strconv.Itoa(item.MusicID)))
		catalogDigest.Write([]byte{0})
		catalogDigest.Write([]byte(item.CatalogFingerprint))
		catalogDigest.Write([]byte{'\n'})
		lastMusicID = item.MusicID
	}
	if kindCounts["source_v3"] != ExpectedSourceV3 || kindCounts["legacy"] != ExpectedLegacy || kindCounts["availability"] != ExpectedAvailability ||
		MusicIDsSHA256(musicIDs) != manifest.MusicIDsSHA256 || hex.EncodeToString(catalogDigest.Sum(nil)) != manifest.CatalogFingerprintsSHA256 {
		return errors.New("embedded lyrics editor seed catalog digest or partition differs")
	}

	documents := make(map[int]SourceDocumentRecord, len(bundle.Documents))
	artifactKeys := make(map[int]map[string]bool, len(bundle.Documents))
	contributionKeys := make(map[int]map[string]bool, len(bundle.Documents))
	for _, record := range bundle.Documents {
		item, exists := items[record.MusicID]
		digest := sha256.Sum256([]byte(record.DocumentJSON))
		document, err := model.DecodeLyricsSourceDocument([]byte(record.DocumentJSON))
		canonicalBody, marshalErr := json.Marshal(document)
		if !exists || item.SeedKind != "source_v3" || record.SchemaVersion != model.LyricsSourceDocumentSchemaVersionV3 ||
			record.ReasonCode != "" || record.ManifestBatchSHA256 != manifest.SeedSHA256 || record.CreatedAt != item.CreatedAt ||
			record.DocumentSHA256 != item.DocumentSHA256 || record.DocumentSHA256 != hex.EncodeToString(digest[:]) || err != nil ||
			marshalErr != nil || string(canonicalBody) != record.DocumentJSON || document.SchemaVersion != record.SchemaVersion ||
			string(document.ReasonCode) != record.ReasonCode {
			return fmt.Errorf("embedded lyrics editor seed source document %d is invalid", record.MusicID)
		}
		if _, duplicate := documents[record.MusicID]; duplicate {
			return fmt.Errorf("embedded lyrics editor seed source document %d is duplicated", record.MusicID)
		}
		documents[record.MusicID] = record
	}
	for _, record := range bundle.Artifacts {
		documentRecord, exists := documents[record.MusicID]
		document, err := model.DecodeLyricsSourceDocument([]byte(documentRecord.DocumentJSON))
		identity, identityErr := model.DecodeLyricsSourceFixedIdentity([]byte(record.FixedIdentityJSON))
		canonicalIdentity, marshalErr := json.Marshal(identity)
		identityDigest := sha256.Sum256([]byte(record.FixedIdentityJSON))
		var expected *model.LyricsSourceFixedIdentity
		for index := range document.FixedIdentities {
			if document.FixedIdentities[index].RenditionKey == record.RenditionKey {
				expected = &document.FixedIdentities[index]
				break
			}
		}
		categories, categoriesErr := json.Marshal(identity.Categories)
		references, referencesErr := json.Marshal(identity.IndexEvidenceRefs)
		if !exists || err != nil || identityErr != nil || marshalErr != nil || expected == nil || !sameIdentity(*expected, identity) ||
			string(canonicalIdentity) != record.FixedIdentityJSON || record.FixedIdentitySHA256 != hex.EncodeToString(identityDigest[:]) ||
			record.Provider != string(identity.Provider) || record.Origin != identity.Origin || record.PageID != identity.PageID ||
			record.RevisionID != identity.RevisionID || record.RevisionTimestamp != identity.RevisionTimestamp ||
			record.MediaWikiSHA1 != identity.SHA1 || record.PageTitle != identity.Title || record.CanonicalRevisionURL != identity.CanonicalURL ||
			record.FetchedAt != identity.FetchedAt || categoriesErr != nil || string(categories) != record.CategoriesJSON ||
			record.Section != identity.Section || record.CompositionRenditionKey != identity.CompositionRenditionKey ||
			record.VersionReason != string(identity.VersionReason) || referencesErr != nil || string(references) != record.IndexEvidenceRefsJSON ||
			record.RawByteCount <= 0 || record.RawByteCount > 2<<20 || !canonicalSHA256.MatchString(record.RawWikitextSHA256) ||
			!canonicalSHA256.MatchString(record.ArtifactSHA256) {
			return fmt.Errorf("embedded lyrics editor seed source artifact %d/%s is invalid", record.MusicID, record.RenditionKey)
		}
		if artifactKeys[record.MusicID] == nil {
			artifactKeys[record.MusicID] = map[string]bool{}
		}
		if artifactKeys[record.MusicID][record.RenditionKey] {
			return fmt.Errorf("embedded lyrics editor seed source artifact %d/%s is duplicated", record.MusicID, record.RenditionKey)
		}
		artifactKeys[record.MusicID][record.RenditionKey] = true
	}
	for musicID, record := range documents {
		document, _ := model.DecodeLyricsSourceDocument([]byte(record.DocumentJSON))
		if len(artifactKeys[musicID]) != len(document.FixedIdentities) {
			return fmt.Errorf("embedded lyrics editor seed source artifacts for music %d are incomplete", musicID)
		}
	}
	for _, record := range bundle.Contributions {
		documentRecord, exists := documents[record.MusicID]
		document, err := model.DecodeLyricsSourceDocument([]byte(documentRecord.DocumentJSON))
		bindings, bindingErr := model.EnumerateLyricsSourceRenditionComponents(document.Renditions)
		expectedKey := ""
		for _, binding := range bindings {
			if binding.ComponentKey == record.Component {
				expectedKey = binding.FixedIdentityKey
				break
			}
		}
		digest := sha256.Sum256([]byte(documentRecord.DocumentSHA256 + "\x00" + record.Component + "\x00" + record.RenditionKey))
		if !exists || err != nil || bindingErr != nil || expectedKey == "" || expectedKey != record.RenditionKey ||
			!artifactKeys[record.MusicID][record.RenditionKey] || record.ContributionSHA256 != hex.EncodeToString(digest[:]) {
			return fmt.Errorf("embedded lyrics editor seed contribution %d/%s is invalid", record.MusicID, record.Component)
		}
		if contributionKeys[record.MusicID] == nil {
			contributionKeys[record.MusicID] = map[string]bool{}
		}
		if contributionKeys[record.MusicID][record.Component] {
			return fmt.Errorf("embedded lyrics editor seed contribution %d/%s is duplicated", record.MusicID, record.Component)
		}
		contributionKeys[record.MusicID][record.Component] = true
	}
	for musicID, record := range documents {
		document, _ := model.DecodeLyricsSourceDocument([]byte(record.DocumentJSON))
		bindings, _ := model.EnumerateLyricsSourceRenditionComponents(document.Renditions)
		if len(contributionKeys[musicID]) != len(bindings) {
			return fmt.Errorf("embedded lyrics editor seed contributions for music %d are incomplete", musicID)
		}
	}

	legacyDocs := map[int]LegacyDocumentRecord{}
	for _, record := range bundle.LegacyDocuments {
		item, exists := items[record.MusicID]
		if !exists || item.SeedKind != "legacy" || record.Revision <= 0 || record.UpdatedAt <= 0 || record.SourceHash == "" ||
			!canonicalSHA256.MatchString(record.SourceHash) || record.SourcePageID <= 0 || record.SourceRevisionID <= 0 ||
			len(record.SourceSHA1) != 40 || record.SourceFetchedAt <= 0 || record.SourceFetchedAtRFC3339 == "" {
			return fmt.Errorf("embedded lyrics editor seed legacy document %d is invalid", record.MusicID)
		}
		legacyDocs[record.MusicID] = record
	}
	lineKeys := map[int]map[string]bool{}
	linePositions := map[int]map[int]bool{}
	for _, record := range bundle.LegacyLines {
		if _, exists := legacyDocs[record.MusicID]; !exists || record.LineID == "" || record.Position < 0 ||
			(record.StanzaBreakBefore != 0 && record.StanzaBreakBefore != 1) || record.Japanese == "" {
			return fmt.Errorf("embedded lyrics editor seed legacy line %d/%s is invalid", record.MusicID, record.LineID)
		}
		if lineKeys[record.MusicID] == nil {
			lineKeys[record.MusicID] = map[string]bool{}
			linePositions[record.MusicID] = map[int]bool{}
		}
		if lineKeys[record.MusicID][record.LineID] || linePositions[record.MusicID][record.Position] {
			return fmt.Errorf("embedded lyrics editor seed legacy line %d/%s is duplicated", record.MusicID, record.LineID)
		}
		lineKeys[record.MusicID][record.LineID] = true
		linePositions[record.MusicID][record.Position] = true
	}
	segmentsByLine := map[string]int{}
	for _, record := range bundle.LegacySegments {
		key := strconv.Itoa(record.MusicID) + "\x00" + record.LineID
		if !lineKeys[record.MusicID][record.LineID] || record.Position < 0 || record.Text == "" ||
			!json.Valid([]byte(record.PerformerIDsJSON)) || !json.Valid([]byte(record.RubyJSON)) {
			return fmt.Errorf("embedded lyrics editor seed legacy segment %d/%s/%d is invalid", record.MusicID, record.LineID, record.Position)
		}
		segmentsByLine[key]++
	}
	for musicID, keys := range lineKeys {
		for lineID := range keys {
			if segmentsByLine[strconv.Itoa(musicID)+"\x00"+lineID] == 0 {
				return fmt.Errorf("embedded lyrics editor seed legacy line %d/%s has no segments", musicID, lineID)
			}
		}
	}

	availability := map[int]bool{}
	for _, record := range bundle.Availability {
		item, exists := items[record.MusicID]
		digest := sha256.Sum256([]byte(record.DocumentJSON))
		document, err := model.DecodeLyricsAvailabilityDocument([]byte(record.DocumentJSON))
		canonicalBody, marshalErr := json.Marshal(document)
		if !exists || item.SeedKind != "availability" || record.SchemaVersion != model.LyricsAvailabilityDocumentSchemaVersion ||
			record.State != item.State || record.ResultSHA256 != item.ResultSHA256 || record.DocumentSHA256 != item.AvailabilitySHA256 ||
			record.DocumentSHA256 != hex.EncodeToString(digest[:]) || record.CreatedAt != item.CreatedAt || err != nil || marshalErr != nil ||
			string(canonicalBody) != record.DocumentJSON || document.SchemaVersion != record.SchemaVersion ||
			string(document.State) != record.State || string(document.ReasonCode) != record.ReasonCode || document.NoLyricsReason != record.NoLyricsReason {
			return fmt.Errorf("embedded lyrics editor seed availability document %d is invalid", record.MusicID)
		}
		if availability[record.MusicID] {
			return fmt.Errorf("embedded lyrics editor seed availability document %d is duplicated", record.MusicID)
		}
		availability[record.MusicID] = true
	}
	if len(availability) != ExpectedAvailability {
		return errors.New("embedded lyrics editor seed availability coverage differs")
	}
	if SeedSHA256(manifest.Items, manifest.ReleaseID, manifest.SourceBatchSHA256, manifest.RootSHA256,
		manifest.CatalogPolicyVersion, manifest.SchemaVersion) != manifest.SeedSHA256 {
		return errors.New("embedded lyrics editor seed identity digest differs")
	}
	return nil
}

func SeedSHA256(items []CatalogItem, releaseID, sourceBatchSHA256, rootSHA256, catalogPolicyVersion string, schemaVersion int) string {
	digest := sha256.New()
	digest.Write([]byte(strconv.Itoa(schemaVersion)))
	digest.Write([]byte{0})
	digest.Write([]byte(releaseID))
	digest.Write([]byte{0})
	digest.Write([]byte(sourceBatchSHA256))
	digest.Write([]byte{0})
	digest.Write([]byte(rootSHA256))
	digest.Write([]byte{0})
	digest.Write([]byte(catalogPolicyVersion))
	digest.Write([]byte{'\n'})
	for _, item := range items {
		digest.Write([]byte(strconv.Itoa(item.MusicID)))
		digest.Write([]byte{0})
		digest.Write([]byte(item.JapaneseTitle))
		digest.Write([]byte{0})
		digest.Write([]byte(item.CatalogFingerprint))
		digest.Write([]byte{0})
		digest.Write([]byte(item.State))
		digest.Write([]byte{0})
		digest.Write([]byte(item.SeedKind))
		digest.Write([]byte{0})
		digest.Write([]byte(item.ResultSHA256))
		digest.Write([]byte{0})
		digest.Write([]byte(item.DocumentSHA256))
		digest.Write([]byte{0})
		digest.Write([]byte(item.AvailabilitySHA256))
		digest.Write([]byte{0})
		digest.Write([]byte(strconv.FormatInt(item.CreatedAt, 10)))
		digest.Write([]byte{'\n'})
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func sameIdentity(left, right model.LyricsSourceFixedIdentity) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func MusicIDsSHA256(musicIDs []int) string {
	copyIDs := append([]int(nil), musicIDs...)
	sort.Ints(copyIDs)
	digest := sha256.New()
	for _, musicID := range copyIDs {
		digest.Write([]byte(strconv.Itoa(musicID)))
		digest.Write([]byte{'\n'})
	}
	return hex.EncodeToString(digest.Sum(nil))
}
