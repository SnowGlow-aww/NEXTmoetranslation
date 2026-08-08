package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"moesekai/server/internal/lyricsevidencepack"
	"moesekai/server/internal/lyricsextractionplan"
	"moesekai/server/internal/lyricsoutcomeartifact"
	"moesekai/server/internal/lyricsrecovery"
	"moesekai/server/internal/lyricsrecoveryimport"
	"moesekai/server/internal/lyricsreview"
	"moesekai/server/internal/lyricsrootmanifest"
)

type options struct {
	planPath           string
	catalogPath        string
	rootPath           string
	songResultsPath    string
	outcomesPath       string
	evidencePath       string
	manifestOutput     string
	receiptOutput      string
	reviewManifestPath string
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string, output io.Writer) error {
	var opts options
	flags := flag.NewFlagSet("lyrics-recovery-import-assemble", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&opts.planPath, "plan", "", "immutable recovery plan")
	flags.StringVar(&opts.catalogPath, "catalog", "", "immutable recovery catalog")
	flags.StringVar(&opts.rootPath, "root-manifest", "", "compact recovery root")
	flags.StringVar(&opts.songResultsPath, "song-results", "", "private song-result directory")
	flags.StringVar(&opts.outcomesPath, "provider-outcomes", "", "private provider-outcome directory")
	flags.StringVar(&opts.evidencePath, "evidence-pack", "", "private evidence-pack directory")
	flags.StringVar(&opts.manifestOutput, "output-manifest", "", "create-exclusive recovery import manifest")
	flags.StringVar(&opts.receiptOutput, "output-evidence-receipt", "", "create-exclusive projected private evidence receipt")
	flags.StringVar(&opts.reviewManifestPath, "review-decision-manifest", "", "content-free manual review decision manifest")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 {
		return errors.New("recovery import assembly requires only explicit named flags")
	}
	for _, path := range []string{
		opts.planPath, opts.catalogPath, opts.rootPath, opts.songResultsPath, opts.outcomesPath,
		opts.evidencePath, opts.manifestOutput, opts.receiptOutput,
	} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return errors.New("recovery import assembly paths must be canonical absolute paths")
		}
	}
	if opts.reviewManifestPath != "" && (!filepath.IsAbs(opts.reviewManifestPath) || filepath.Clean(opts.reviewManifestPath) != opts.reviewManifestPath) {
		return errors.New("review decision manifest path must be canonical absolute")
	}
	if opts.manifestOutput == opts.receiptOutput {
		return errors.New("recovery import outputs must be distinct")
	}
	for _, outputPath := range []string{opts.manifestOutput, opts.receiptOutput} {
		if _, err := os.Lstat(outputPath); !errors.Is(err, os.ErrNotExist) {
			if err == nil {
				return fmt.Errorf("create-exclusive output already exists: %s", outputPath)
			}
			return err
		}
	}

	planBody, err := os.ReadFile(opts.planPath)
	if err != nil {
		return err
	}
	plan, err := lyricsextractionplan.DecodeRecoveryCanonical(planBody)
	if err != nil {
		return err
	}
	planDigest := sha256.Sum256(planBody)
	planSHA256 := hex.EncodeToString(planDigest[:])
	var reviewResolver *lyricsreview.Resolver
	if opts.reviewManifestPath != "" {
		loaded, reviewErr := lyricsreview.OpenResolver(
			opts.reviewManifestPath, plan.PlanID, planSHA256, plan.SourceSnapshot.SHA256,
		)
		if reviewErr != nil {
			return reviewErr
		}
		reviewResolver = &loaded
	}
	if plan.Catalog.Path != opts.catalogPath || plan.Outputs.RootManifest != opts.rootPath ||
		plan.Outputs.SongResults != opts.songResultsPath || plan.Outputs.ProviderOutcomes != opts.outcomesPath ||
		plan.Outputs.EvidencePack != opts.evidencePath {
		return errors.New("recovery import inputs do not exactly match the immutable plan outputs")
	}
	rootBody, err := os.ReadFile(opts.rootPath)
	if err != nil {
		return err
	}
	root, err := lyricsrootmanifest.DecodeCanonical(rootBody)
	if err != nil {
		return err
	}
	if root.Plan.PlanID != plan.PlanID || root.Catalog.SourceSHA256 != plan.Catalog.SourceSHA256 ||
		root.Catalog.RecordCount != plan.Catalog.RecordCount || root.Catalog.MusicIDsSHA256 != plan.Catalog.MusicIDsSHA256 {
		return errors.New("compact root does not match the immutable plan and catalog")
	}
	catalog, _, err := lyricsrecovery.OpenCatalogAgainstPlan(ctx, opts.catalogPath, plan.Catalog)
	if err != nil {
		return err
	}
	defer catalog.Close()
	resolver, err := lyricsevidencepack.OpenResolver(opts.evidencePath)
	if err != nil {
		return err
	}

	results := make(map[int]lyricsrecovery.SongResult, len(root.Songs))
	items := make([]lyricsrecoveryimport.Item, len(root.Songs))
	for index, rootSong := range root.Songs {
		if err := ctx.Err(); err != nil {
			return err
		}
		resultPath := filepath.Join(opts.songResultsPath,
			fmt.Sprintf("music-%d-%s.json", rootSong.MusicID, rootSong.ResultSHA256))
		result, err := lyricsrecovery.OpenSongResult(resultPath)
		if err != nil {
			return err
		}
		outcomes := make([]lyricsoutcomeartifact.Artifact, len(rootSong.ProviderOutcomes))
		for outcomeIndex, reference := range rootSong.ProviderOutcomes {
			path := filepath.Join(opts.outcomesPath,
				fmt.Sprintf("music-%d-%s-%s.json", rootSong.MusicID, reference.Provider, reference.SHA256))
			outcome, err := lyricsoutcomeartifact.Open(path)
			if err != nil {
				return err
			}
			outcomes[outcomeIndex] = outcome
		}
		identity, err := catalog.ImportIdentity(ctx, rootSong.MusicID)
		if err != nil {
			return err
		}
		item, err := lyricsrecoveryimport.BuildItemWithReview(lyricsrecoveryimport.CatalogItem{
			MusicID: identity.MusicID, JapaneseTitle: identity.JapaneseTitle,
			CatalogFingerprint: identity.CatalogFingerprint, TargetMusicID: identity.MusicID,
			AssociationMusicIDs: []int{}, PerformerSegmentationPolicy: identity.PerformerSegmentationPolicy,
		}, result, outcomes, resolver, reviewResolver)
		if err != nil {
			return fmt.Errorf("music %d: %w", rootSong.MusicID, err)
		}
		results[rootSong.MusicID] = result
		items[index] = item
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
	_, err = fmt.Fprintf(output,
		"PASS recoveryImport items=%d complete=%d gameOnly=%d noLyrics=%d unresolved=%d batchSha256=%s evidence=%d receiptSha256=%s\n",
		len(manifest.Items), root.Coverage.Complete, root.Coverage.GameOnly, root.Coverage.SatisfiedNoLyrics,
		root.Coverage.Ambiguous+root.Coverage.Missing+root.Coverage.Incomplete+root.Coverage.Failed,
		manifest.BatchSHA256, len(receipt.Evidence), receipt.ReceiptSHA256)
	return err
}

func publishExclusive(path string, body []byte) error {
	if len(body) == 0 {
		return errors.New("recovery import output body is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	failed := true
	defer func() {
		_ = file.Close()
		if failed {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(body); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	failed = false
	return nil
}
