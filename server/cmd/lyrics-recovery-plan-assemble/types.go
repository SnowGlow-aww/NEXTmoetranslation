package main

type options struct {
	sourceRoot                  string
	createdAt                   string
	planID                      string
	scopeID                     string
	targetMapPath               string
	targetMapSHA256             string
	catalogPath                 string
	catalogSHA256               string
	catalogReceiptPath          string
	catalogReceiptSHA256        string
	listResponsePath            string
	listResponseSHA256          string
	listReplayLedgerPath        string
	listReplayAcquisitionID     string
	canaryResponsePath          string
	canaryResponseSHA256        string
	exactRawHTMLPath            string
	exactRawHTMLSHA256          string
	exactExtractionReportPath   string
	exactExtractionReportSHA256 string
	runRoot                     string
	outputPlanPath              string
}

type catalogSong struct {
	MusicID       int    `json:"musicId"`
	JapaneseTitle string `json:"japaneseTitle"`
}

type targetMapInputs struct {
	ScopeSHA256                string `json:"scopeSha256"`
	BaseSekaipediaReportSHA256 string `json:"baseSekaipediaReportSha256"`
	GapSekaipediaReportSHA256  string `json:"gapSekaipediaReportSha256"`
	MoegirlExtractionSHA256    string `json:"moegirlExtractionSha256"`
}

type targetMapReport struct {
	SchemaVersion           int                `json:"schemaVersion"`
	CatalogSHA256           string             `json:"catalogSha256"`
	CatalogCount            int                `json:"catalogCount"`
	Inputs                  targetMapInputs    `json:"inputs"`
	MappingCount            int                `json:"mappingCount"`
	SekaipediaCount         int                `json:"sekaipediaCount"`
	MoegirlPublicExactCount int                `json:"moegirlPublicExactCount"`
	MusicIDSetEncoding      string             `json:"musicIdSetEncoding"`
	MusicIDSetSHA256        string             `json:"musicIdSetSha256"`
	MappingsSHA256          string             `json:"mappingsSha256"`
	ExcludedMusic           []catalogSong      `json:"excludedMusic"`
	Mappings                []targetMapMapping `json:"mappings"`
}

type targetMapMapping struct {
	MusicID              int                       `json:"musicId"`
	CatalogJapaneseTitle string                    `json:"catalogJapaneseTitle"`
	Provider             string                    `json:"provider"`
	Sekaipedia           *targetSekaipediaMapping  `json:"sekaipedia,omitempty"`
	MoegirlPublicExact   *targetExactPublicMapping `json:"moegirlPublicExact,omitempty"`
}

type targetSekaipediaMapping struct {
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
	ContentSHA256           string `json:"contentSha256,omitempty"`
	RawResponseSHA256       string `json:"rawResponseSha256,omitempty"`
}

type targetExactPublicMapping struct {
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

type catalogFilterReceipt struct {
	SchemaVersion   int                  `json:"schemaVersion"`
	SourceCatalog   sourceCatalogBinding `json:"sourceCatalog"`
	TargetMapSHA256 string               `json:"targetMapSha256"`
	CatalogFile     string               `json:"catalogFile"`
	Catalog         catalogVerification  `json:"catalog"`
	ExcludedMusic   []catalogSong        `json:"excludedMusic"`
}

type sourceCatalogBinding struct {
	ByteCount   int64  `json:"byteCount"`
	SHA256      string `json:"sha256"`
	RecordCount int    `json:"recordCount"`
}

type catalogVerification struct {
	ByteCount             int64  `json:"byteCount"`
	SHA256                string `json:"sha256"`
	SchemaVersion         int    `json:"schemaVersion"`
	RuntimeSchemaVersion  int    `json:"runtimeSchemaVersion"`
	RecordCount           int    `json:"recordCount"`
	IdentityPolicyVersion string `json:"identityPolicyVersion"`
	IdentitySHA256        string `json:"identitySha256"`
	MusicIDsSHA256        string `json:"musicIdsSha256"`
}

type mediaWikiResponse struct {
	BatchComplete bool           `json:"batchcomplete"`
	Query         mediaWikiQuery `json:"query"`
	Limits        mediaWikiLimit `json:"limits"`
}

type mediaWikiQuery struct {
	Pages []mediaWikiPage `json:"pages"`
}

type mediaWikiLimit struct {
	Categories int `json:"categories"`
}

type mediaWikiPage struct {
	PageID               int                 `json:"pageid"`
	Namespace            int                 `json:"ns"`
	Title                string              `json:"title"`
	Revisions            []mediaWikiRevision `json:"revisions"`
	Categories           []mediaWikiCategory `json:"categories"`
	ContentModel         string              `json:"contentmodel"`
	PageLanguage         string              `json:"pagelanguage"`
	PageLanguageHTMLCode string              `json:"pagelanguagehtmlcode"`
	PageLanguageDir      string              `json:"pagelanguagedir"`
	Touched              string              `json:"touched"`
	LastRevisionID       int                 `json:"lastrevid"`
	Length               int                 `json:"length"`
	FullURL              string              `json:"fullurl"`
	EditURL              string              `json:"editurl"`
	CanonicalURL         string              `json:"canonicalurl"`
}

type mediaWikiCategory struct {
	Namespace int    `json:"ns"`
	Title     string `json:"title"`
}

type mediaWikiRevision struct {
	RevisionID int            `json:"revid"`
	ParentID   int            `json:"parentid"`
	Timestamp  string         `json:"timestamp"`
	SHA1       string         `json:"sha1"`
	Slots      mediaWikiSlots `json:"slots"`
}

type mediaWikiSlots struct {
	Main mediaWikiSlot `json:"main"`
}

type mediaWikiSlot struct {
	ContentModel  string `json:"contentmodel"`
	ContentFormat string `json:"contentformat"`
	Content       string `json:"content"`
}

type strictExtractionReport struct {
	SchemaVersion   int                  `json:"schemaVersion"`
	Provider        string               `json:"provider"`
	URLReportSHA256 string               `json:"urlReportSha256"`
	RawHTMLSHA256   string               `json:"rawHtmlSha256"`
	CatalogSHA256   string               `json:"catalogSha256"`
	Catalog         exactCatalogIdentity `json:"catalog"`
	PageURL         string               `json:"pageUrl"`
	PageTitle       string               `json:"pageTitle"`
	JapaneseTitle   string               `json:"japaneseTitle"`
	PageID          int                  `json:"pageId"`
	RevisionID      int                  `json:"revisionId"`
	FetchedAt       string               `json:"fetchedAt"`
	RightsNotice    string               `json:"rightsNotice"`
	LineCount       int                  `json:"lineCount"`
	StanzaCount     int                  `json:"stanzaCount"`
	Lines           []exactReportLine    `json:"lines"`
}

type exactCatalogIdentity struct {
	MusicID          int    `json:"musicId"`
	JapaneseTitle    string `json:"japaneseTitle"`
	ProducerMetadata string `json:"producerMetadata"`
	Lyricist         string `json:"lyricist"`
	Composer         string `json:"composer"`
	Arranger         string `json:"arranger"`
	LyricsVersion    string `json:"lyricsVersion"`
}

type exactReportLine struct {
	Japanese          string `json:"japanese"`
	Translation       string `json:"translation"`
	StanzaBreakBefore bool   `json:"stanzaBreakBefore,omitempty"`
}
