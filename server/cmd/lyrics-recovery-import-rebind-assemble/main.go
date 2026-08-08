package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
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

	"moesekai/server/internal/lyricsevidencepack"
	"moesekai/server/internal/lyricsextractionplan"
	"moesekai/server/internal/lyricsoutcomeartifact"
	"moesekai/server/internal/lyricsrecovery"
	"moesekai/server/internal/lyricsrecoveryimport"
	"moesekai/server/internal/lyricsreview"
	"moesekai/server/internal/lyricsrootmanifest"
	"moesekai/server/internal/lyricssource"
	"moesekai/server/internal/model"

	_ "modernc.org/sqlite"
)

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type options struct {
	planPath            string
	recoveryCatalogPath string
	runtimeDatabasePath string
	rootPath            string
	songResultsPath     string
	outcomesPath        string
	evidencePath        string
	manifestOutput      string
	receiptOutput       string
	rebindReceiptOutput string
	reviewManifestPath  string
}

type runtimeCatalog struct {
	database       *sql.DB
	path           string
	sha256         string
	schemaVersion  int
	recordCount    int
	identitySHA256 string
	musicIDsSHA256 string
	items          map[int]runtimeCatalogItem
}

type runtimeCatalogItem struct {
	MusicID            int
	JapaneseTitle      string
	CatalogFingerprint string
	PerformerPolicy    lyricssource.PerformerSegmentationPolicy
}

func (catalog *runtimeCatalog) Close() error {
	if catalog == nil || catalog.database == nil {
		return nil
	}
	err := catalog.database.Close()
	catalog.database = nil
	return err
}

type runtimeCatalogPresence struct {
	Lyricist      bool `json:"lyricist"`
	Composer      bool `json:"composer"`
	Arranger      bool `json:"arranger"`
	Assetbundle   bool `json:"assetbundle"`
	VersionHint   bool `json:"versionHint"`
	LyricsVersion bool `json:"lyricsVersion"`
	Vocals        bool `json:"vocals"`
}

type rebindCatalogBinding struct {
	Path           string `json:"path"`
	SHA256         string `json:"sha256"`
	SchemaVersion  int    `json:"schemaVersion"`
	RecordCount    int    `json:"recordCount"`
	IdentitySHA256 string `json:"identitySha256"`
	MusicIDsSHA256 string `json:"musicIdsSha256"`
	IdentityPolicy string `json:"identityPolicyVersion"`
}

type rebindItem struct {
	MusicID             int    `json:"musicId"`
	JapaneseTitle       string `json:"japaneseTitle"`
	RecoveryFingerprint string `json:"recoveryCatalogFingerprint"`
	RuntimeFingerprint  string `json:"runtimeCatalogFingerprint"`
}

type rebindReceipt struct {
	SchemaVersion       int                  `json:"schemaVersion"`
	CanonicalEncoding   string               `json:"canonicalEncoding"`
	DigestAlgorithm     string               `json:"digestAlgorithm"`
	RecoveryPlanSHA256  string               `json:"recoveryPlanSha256"`
	RecoveryRootSHA256  string               `json:"recoveryRootSha256"`
	RecoveryCatalog     rebindCatalogBinding `json:"recoveryCatalog"`
	RuntimeCatalog      rebindCatalogBinding `json:"runtimeCatalog"`
	ManifestBatchSHA256 string               `json:"manifestBatchSha256"`
	Items               []rebindItem         `json:"items"`
	ReceiptSHA256       string               `json:"receiptSha256"`
}

func run(ctx context.Context, arguments []string, output io.Writer) error {
	if ctx == nil || output == nil {
		return errors.New("catalog rebind assembly requires context and output")
	}
	opts, err := parseOptions(arguments)
	if err != nil {
		return err
	}
	planBody, err := os.ReadFile(opts.planPath)
	if err != nil {
		return err
	}
	plan, err := lyricsextractionplan.DecodeRecoveryCanonical(planBody)
	if err != nil {
		return err
	}
	planSHA256 := sha256Hex(planBody)
	if plan.Catalog.Path != opts.recoveryCatalogPath || plan.Outputs.RootManifest != opts.rootPath ||
		plan.Outputs.SongResults != opts.songResultsPath || plan.Outputs.ProviderOutcomes != opts.outcomesPath ||
		plan.Outputs.EvidencePack != opts.evidencePath {
		return errors.New("rebind inputs do not exactly match the immutable recovery plan outputs")
	}
	rootBody, err := os.ReadFile(opts.rootPath)
	if err != nil {
		return err
	}
	root, err := lyricsrootmanifest.DecodeCanonical(rootBody)
	if err != nil {
		return err
	}
	if root.Plan.PlanID != plan.PlanID || root.Plan.SHA256 != planSHA256 || root.Catalog.SourceSHA256 != plan.Catalog.SourceSHA256 ||
		root.Catalog.RecordCount != plan.Catalog.RecordCount || root.Catalog.MusicIDsSHA256 != plan.Catalog.MusicIDsSHA256 {
		return errors.New("compact root does not match the immutable recovery plan")
	}

	recoveryCatalog, _, err := lyricsrecovery.OpenCatalogAgainstPlan(ctx, opts.recoveryCatalogPath, plan.Catalog)
	if err != nil {
		return fmt.Errorf("validate recovery catalog: %w", err)
	}
	defer recoveryCatalog.Close()
	runtime, err := openRuntimeCatalog(ctx, opts.runtimeDatabasePath, root)
	if err != nil {
		return fmt.Errorf("open runtime catalog: %w", err)
	}
	defer runtime.Close()

	resolver, err := lyricsevidencepack.OpenResolver(opts.evidencePath)
	if err != nil {
		return err
	}
	var reviewResolver *lyricsreview.Resolver
	if opts.reviewManifestPath != "" {
		loaded, reviewErr := lyricsreview.OpenResolver(opts.reviewManifestPath, plan.PlanID, planSHA256, plan.SourceSnapshot.SHA256)
		if reviewErr != nil {
			return reviewErr
		}
		reviewResolver = &loaded
	}

	results := make(map[int]lyricsrecovery.SongResult, len(root.Songs))
	items := make([]lyricsrecoveryimport.Item, len(root.Songs))
	rebindItems := make([]rebindItem, len(root.Songs))
	for index, rootSong := range root.Songs {
		if err := ctx.Err(); err != nil {
			return err
		}
		resultPath := filepath.Join(opts.songResultsPath, fmt.Sprintf("music-%d-%s.json", rootSong.MusicID, rootSong.ResultSHA256))
		result, err := lyricsrecovery.OpenSongResult(resultPath)
		if err != nil {
			return err
		}
		outcomes := make([]lyricsoutcomeartifact.Artifact, len(rootSong.ProviderOutcomes))
		for outcomeIndex, reference := range rootSong.ProviderOutcomes {
			path := filepath.Join(opts.outcomesPath, fmt.Sprintf("music-%d-%s-%s.json", rootSong.MusicID, reference.Provider, reference.SHA256))
			outcome, err := lyricsoutcomeartifact.Open(path)
			if err != nil {
				return err
			}
			outcomes[outcomeIndex] = outcome
		}
		identity, err := recoveryCatalog.ImportIdentity(ctx, rootSong.MusicID)
		if err != nil {
			return err
		}
		runtimeItem, ok := runtime.items[rootSong.MusicID]
		if !ok || runtimeItem.JapaneseTitle != identity.JapaneseTitle {
			return fmt.Errorf("music %d runtime catalog title or identity is missing", rootSong.MusicID)
		}
		item, err := lyricsrecoveryimport.BuildItemWithReview(lyricsrecoveryimport.CatalogItem{
			MusicID: identity.MusicID, JapaneseTitle: identity.JapaneseTitle,
			CatalogFingerprint: runtimeItem.CatalogFingerprint, TargetMusicID: identity.MusicID,
			AssociationMusicIDs: []int{}, PerformerSegmentationPolicy: runtimeItem.PerformerPolicy,
		}, result, outcomes, resolver, reviewResolver)
		if err != nil {
			return fmt.Errorf("music %d: %w", rootSong.MusicID, err)
		}
		results[rootSong.MusicID] = result
		items[index] = item
		rebindItems[index] = rebindItem{
			MusicID: rootSong.MusicID, JapaneseTitle: identity.JapaneseTitle,
			RecoveryFingerprint: identity.CatalogFingerprint, RuntimeFingerprint: runtimeItem.CatalogFingerprint,
		}
	}
	manifest, err := lyricsrecoveryimport.NewManifest(root, items)
	if err != nil {
		return err
	}
	if err := lyricsrecoveryimport.ValidateAgainstRoot(manifest, root, results); err != nil {
		return err
	}
	pack := resolver.Manifest()
	receipt, err := lyricsrecoveryimport.NewEvidenceReceipt(root, manifest, pack)
	if err != nil {
		return err
	}
	manifestBody, err := lyricsrecoveryimport.MarshalCanonical(manifest)
	if err != nil {
		return err
	}
	receiptBody, err := lyricsrecoveryimport.MarshalEvidenceReceipt(receipt)
	if err != nil {
		return err
	}
	if err := publishExclusive(opts.manifestOutput, manifestBody); err != nil {
		return err
	}
	if err := publishExclusive(opts.receiptOutput, receiptBody); err != nil {
		return err
	}
	binding := rebindReceipt{
		SchemaVersion: 1, CanonicalEncoding: "moesekai-lyrics-recovery-catalog-rebind-ordered-json-v1",
		DigestAlgorithm:    "sha256-moesekai-lyrics-recovery-catalog-rebind-v1",
		RecoveryPlanSHA256: planSHA256, RecoveryRootSHA256: root.RootSHA256,
		RecoveryCatalog: rebindCatalogBinding{Path: opts.recoveryCatalogPath, SHA256: plan.Catalog.SourceSHA256,
			SchemaVersion: plan.Catalog.SchemaVersion, RecordCount: plan.Catalog.RecordCount,
			IdentitySHA256: plan.Catalog.IdentitySHA256, MusicIDsSHA256: plan.Catalog.MusicIDsSHA256,
			IdentityPolicy: plan.Catalog.IdentityPolicyVersion},
		RuntimeCatalog: rebindCatalogBinding{Path: runtime.path, SHA256: runtime.sha256, SchemaVersion: runtime.schemaVersion,
			RecordCount: runtime.recordCount, IdentitySHA256: runtime.identitySHA256, MusicIDsSHA256: runtime.musicIDsSHA256,
			IdentityPolicy: model.LyricsCatalogIdentityPolicyVersion},
		ManifestBatchSHA256: manifest.BatchSHA256, Items: rebindItems,
	}
	binding.ReceiptSHA256 = sha256Hex(mustMarshal(binding))
	if err := publishExclusive(opts.rebindReceiptOutput, mustMarshal(binding)); err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "PASS mode=catalog-rebind-assemble items=%d batchSha256=%s recoveryCatalogSha256=%s runtimeCatalogSha256=%s rebindReceiptSha256=%s\n",
		len(items), manifest.BatchSHA256, plan.Catalog.SourceSHA256, runtime.sha256, binding.ReceiptSHA256)
	return err
}

func parseOptions(arguments []string) (options, error) {
	var opts options
	flags := flag.NewFlagSet("lyrics-recovery-import-rebind-assemble", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&opts.planPath, "plan", "", "immutable recovery plan")
	flags.StringVar(&opts.recoveryCatalogPath, "recovery-catalog", "", "immutable recovery catalog bound by the plan")
	flags.StringVar(&opts.runtimeDatabasePath, "runtime-database", "", "complete runtime SQLite catalog")
	flags.StringVar(&opts.rootPath, "root-manifest", "", "compact recovery root")
	flags.StringVar(&opts.songResultsPath, "song-results", "", "private song-result directory")
	flags.StringVar(&opts.outcomesPath, "provider-outcomes", "", "private provider-outcome directory")
	flags.StringVar(&opts.evidencePath, "evidence-pack", "", "private evidence-pack directory")
	flags.StringVar(&opts.manifestOutput, "output-manifest", "", "create-exclusive recovery import manifest")
	flags.StringVar(&opts.receiptOutput, "output-evidence-receipt", "", "create-exclusive projected private evidence receipt")
	flags.StringVar(&opts.rebindReceiptOutput, "output-rebind-receipt", "", "create-exclusive catalog rebind receipt")
	flags.StringVar(&opts.reviewManifestPath, "review-decision-manifest", "", "content-free manual review decision manifest")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 {
		return opts, errors.New("catalog rebind assembly requires only explicit named flags")
	}
	paths := []string{opts.planPath, opts.recoveryCatalogPath, opts.runtimeDatabasePath, opts.rootPath, opts.songResultsPath,
		opts.outcomesPath, opts.evidencePath, opts.manifestOutput, opts.receiptOutput, opts.rebindReceiptOutput}
	for _, path := range paths {
		if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return opts, errors.New("catalog rebind paths must be canonical absolute paths")
		}
	}
	if opts.reviewManifestPath != "" && (!filepath.IsAbs(opts.reviewManifestPath) || filepath.Clean(opts.reviewManifestPath) != opts.reviewManifestPath) {
		return opts, errors.New("review decision manifest path must be canonical absolute")
	}
	seen := map[string]bool{}
	for _, path := range []string{opts.manifestOutput, opts.receiptOutput, opts.rebindReceiptOutput} {
		if seen[path] {
			return opts, errors.New("catalog rebind outputs must be distinct")
		}
		seen[path] = true
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			if err == nil {
				return opts, fmt.Errorf("create-exclusive output already exists: %s", path)
			}
			return opts, err
		}
	}
	return opts, nil
}

func openRuntimeCatalog(ctx context.Context, path string, root lyricsrootmanifest.Manifest) (*runtimeCatalog, error) {
	if ctx == nil || path == "" {
		return nil, errors.New("runtime catalog input is invalid")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, errors.New("runtime catalog is empty")
	}
	database, err := sql.Open("sqlite", "file:"+path+"?mode=ro&immutable=1")
	if err != nil {
		return nil, err
	}
	database.SetMaxOpenConns(1)
	fail := func(err error) (*runtimeCatalog, error) {
		_ = database.Close()
		return nil, err
	}
	var schemaVersion int
	if err := database.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&schemaVersion); err != nil {
		return fail(err)
	}
	if schemaVersion < 27 {
		return fail(fmt.Errorf("runtime catalog schema=%d want >=27", schemaVersion))
	}
	rows, err := database.QueryContext(ctx, `SELECT music_id,title_ja,lyricist,composer,arranger,assetbundle_name,version_hint,
		lyrics_version,lyrics_evidence_presence_json,vocal_signals_json,lyrics_catalog_fingerprint,lyrics_catalog_policy_version
		FROM catalog_music ORDER BY music_id`)
	if err != nil {
		return fail(err)
	}
	defer rows.Close()
	items := make(map[int]runtimeCatalogItem, root.Catalog.RecordCount)
	ids := make([]int, 0, root.Catalog.RecordCount)
	identityDigest := sha256.New()
	_, _ = identityDigest.Write([]byte("moesekai-lyrics-recovery-catalog-fingerprints-v1\x00"))
	var encoded [8]byte
	lastID := 0
	for rows.Next() {
		var musicID int
		var title, lyricist, composer, arranger, assetbundle, versionHint, lyricsVersion, presenceJSON, vocalsJSON, fingerprint, policy string
		if err := rows.Scan(&musicID, &title, &lyricist, &composer, &arranger, &assetbundle, &versionHint, &lyricsVersion,
			&presenceJSON, &vocalsJSON, &fingerprint, &policy); err != nil {
			return fail(err)
		}
		if musicID <= lastID || strings.TrimSpace(title) == "" || !sha256Pattern.MatchString(fingerprint) || policy != model.LyricsCatalogIdentityPolicyVersion {
			return fail(fmt.Errorf("runtime catalog row %d is invalid", musicID))
		}
		var presence model.CatalogEvidencePresence
		var vocals []model.CatalogVocalSignal
		if err := decodeClosedJSON([]byte(presenceJSON), &presence); err != nil {
			return fail(fmt.Errorf("runtime catalog music %d presence: %w", musicID, err))
		}
		if err := decodeClosedJSON([]byte(vocalsJSON), &vocals); err != nil || vocals == nil {
			return fail(fmt.Errorf("runtime catalog music %d vocals: %w", musicID, err))
		}
		evidence := model.CatalogLyricsEvidence{Title: title, Lyricist: lyricist, Composer: composer, Arranger: arranger,
			Assetbundle: assetbundle, VersionHint: versionHint, LyricsVersion: lyricsVersion, Presence: presence, Vocals: vocals}
		computed, err := model.CatalogLyricsEvidenceFingerprint(evidence)
		if err != nil || computed != fingerprint {
			return fail(fmt.Errorf("runtime catalog music %d fingerprint does not match full evidence", musicID))
		}
		ids = append(ids, musicID)
		binary.BigEndian.PutUint64(encoded[:], uint64(musicID))
		_, _ = identityDigest.Write(encoded[:])
		fingerprintBytes, _ := hex.DecodeString(fingerprint)
		_, _ = identityDigest.Write(fingerprintBytes)
		items[musicID] = runtimeCatalogItem{MusicID: musicID, JapaneseTitle: title, CatalogFingerprint: fingerprint,
			PerformerPolicy: lyricssource.PerformerSegmentationPolicyFromCatalogVocals(vocals)}
		lastID = musicID
	}
	if err := rows.Err(); err != nil {
		return fail(err)
	}
	if len(ids) != root.Catalog.RecordCount {
		return fail(fmt.Errorf("runtime catalog count=%d want=%d", len(ids), root.Catalog.RecordCount))
	}
	for index, song := range root.Songs {
		if ids[index] != song.MusicID {
			return fail(fmt.Errorf("runtime catalog music ID at index %d=%d want=%d", index, ids[index], song.MusicID))
		}
	}
	musicIDsSHA, err := lyricsrootmanifest.OrderedMusicIDsSHA256(ids)
	if err != nil {
		return fail(err)
	}
	return &runtimeCatalog{database: database, path: path, sha256: sha256Hex(body), schemaVersion: schemaVersion,
		recordCount: len(ids), identitySHA256: hex.EncodeToString(identityDigest.Sum(nil)), musicIDsSHA256: musicIDsSHA, items: items}, nil
}

func decodeClosedJSON(body []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON")
		}
		return err
	}
	return nil
}

func publishExclusive(path string, body []byte) error {
	if len(body) == 0 {
		return errors.New("output body is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(body); err != nil {
		return err
	}
	return file.Sync()
}

func mustMarshal(value any) []byte {
	body, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return append(body, '\n')
}

func sha256Hex(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}
