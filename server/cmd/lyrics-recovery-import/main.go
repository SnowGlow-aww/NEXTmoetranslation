package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"moesekai/server/internal/db"
	"moesekai/server/internal/lyricsevidencepack"
	"moesekai/server/internal/lyricsrecoveryimport"
	"moesekai/server/internal/lyricsrootmanifest"
	"moesekai/server/internal/store"
)

type options struct {
	rootPath          string
	manifestPath      string
	receiptPath       string
	evidencePath      string
	databasePath      string
	importReceiptPath string
	actor             string
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string, output io.Writer) error {
	if ctx == nil || output == nil {
		return errors.New("recovery import requires context and output")
	}
	var opts options
	flags := flag.NewFlagSet("lyrics-recovery-import", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&opts.rootPath, "root-manifest", "", "canonical compact recovery root")
	flags.StringVar(&opts.manifestPath, "import-manifest", "", "canonical all-root recovery import manifest")
	flags.StringVar(&opts.receiptPath, "evidence-receipt", "", "canonical compact recovery evidence receipt")
	flags.StringVar(&opts.evidencePath, "evidence-pack", "", "private exact evidence-pack directory")
	flags.StringVar(&opts.databasePath, "database", "", "existing private SQLite database")
	flags.StringVar(&opts.importReceiptPath, "import-receipt", "", "canonical durable recovery import receipt")
	flags.StringVar(&opts.actor, "actor", "", "offline import operator")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 {
		return errors.New("recovery import requires only explicit named flags")
	}
	for _, path := range []string{
		opts.rootPath, opts.manifestPath, opts.receiptPath, opts.evidencePath, opts.databasePath, opts.importReceiptPath,
	} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return errors.New("recovery import paths must be canonical absolute paths")
		}
	}
	for _, path := range []string{opts.rootPath, opts.manifestPath, opts.receiptPath, opts.evidencePath, opts.databasePath} {
		if path == opts.importReceiptPath {
			return errors.New("recovery import receipt must not alias an input or the database")
		}
	}
	if relative, err := filepath.Rel(opts.evidencePath, opts.importReceiptPath); err != nil ||
		relative == "." || relative != ".." && !filepath.IsAbs(relative) &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("recovery import receipt must remain outside the evidence pack")
	}
	if opts.actor == "" {
		return errors.New("recovery import actor is required")
	}
	info, err := os.Lstat(opts.databasePath)
	if err != nil {
		return fmt.Errorf("inspect recovery import database: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("recovery import database must be an existing direct regular file")
	}
	resolved, err := filepath.EvalSymlinks(opts.databasePath)
	if err != nil || resolved != opts.databasePath {
		return errors.New("recovery import database must not traverse a filesystem alias")
	}

	rootBody, err := os.ReadFile(opts.rootPath)
	if err != nil {
		return err
	}
	root, err := lyricsrootmanifest.DecodeCanonical(rootBody)
	if err != nil {
		return err
	}
	manifestBody, err := os.ReadFile(opts.manifestPath)
	if err != nil {
		return err
	}
	manifest, err := lyricsrecoveryimport.DecodeCanonical(manifestBody)
	if err != nil {
		return err
	}
	receiptBody, err := os.ReadFile(opts.receiptPath)
	if err != nil {
		return err
	}
	receipt, err := lyricsrecoveryimport.DecodeEvidenceReceipt(receiptBody)
	if err != nil {
		return err
	}
	resolver, err := lyricsevidencepack.OpenResolver(opts.evidencePath)
	if err != nil {
		return err
	}

	database, err := db.Open(opts.databasePath)
	if err != nil {
		return err
	}
	defer database.Close()
	lyricsStore := store.New(database)
	results, err := lyricsStore.ImportRecoveryLyricsManifest(ctx, root, manifest, receipt, resolver, opts.actor)
	if err != nil {
		return err
	}
	if err := database.Checkpoint(ctx); err != nil {
		return fmt.Errorf("checkpoint recovery import database: %w", err)
	}
	if err := database.IntegrityCheck(ctx); err != nil {
		return fmt.Errorf("verify recovery import database: %w", err)
	}
	changed := 0
	states := map[string]int{}
	for _, result := range results {
		if result.Changed {
			changed++
		}
		states[string(result.State)]++
	}
	var batches, items, editableLyrics, sourceDocuments, availabilityDocuments, artifacts, evidence, links, contributions int
	if err := database.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM lyrics_recovery_import_batches),
		(SELECT COUNT(*) FROM lyrics_recovery_import_items WHERE batch_sha256=?),
		(SELECT COUNT(*) FROM song_lyrics AS lyrics JOIN lyrics_recovery_import_items AS item
		 ON item.music_id=lyrics.music_id WHERE item.batch_sha256=? AND item.state='complete'),
		(SELECT COUNT(*) FROM song_lyrics_source_documents WHERE manifest_batch_sha256=?),
		(SELECT COUNT(*) FROM song_lyrics_availability_documents WHERE batch_sha256=?),
		(SELECT COUNT(*) FROM lyrics_recovery_import_artifacts WHERE batch_sha256=?),
		(SELECT COUNT(*) FROM lyrics_recovery_source_evidence),
		(SELECT COUNT(*) FROM lyrics_recovery_import_artifact_evidence WHERE batch_sha256=?),
		(SELECT COUNT(*) FROM lyrics_recovery_import_component_contributions WHERE batch_sha256=?)`,
		manifest.BatchSHA256, manifest.BatchSHA256, manifest.BatchSHA256, manifest.BatchSHA256,
		manifest.BatchSHA256, manifest.BatchSHA256, manifest.BatchSHA256).Scan(&batches, &items, &editableLyrics,
		&sourceDocuments, &availabilityDocuments, &artifacts, &evidence, &links, &contributions); err != nil {
		return err
	}
	counts := lyricsrecoveryimport.ImportStorageCounts{
		BatchItems: items, EditableLyrics: editableLyrics, SourceDocuments: sourceDocuments,
		AvailabilityDocuments: availabilityDocuments, Artifacts: artifacts,
		EvidenceSelection: receipt.EvidenceCount, ArtifactEvidenceLinks: links,
		ComponentContributions: contributions,
	}
	if counts != lyricsrecoveryimport.ExpectedImportStorageCounts(manifest, receipt) {
		return errors.New("committed recovery storage counts do not match the exact import manifest")
	}
	var committedAt int64
	if err := database.QueryRowContext(ctx,
		`SELECT created_at FROM lyrics_recovery_import_batches WHERE batch_sha256=?`, manifest.BatchSHA256).
		Scan(&committedAt); err != nil {
		return err
	}
	receiptItems := make([]lyricsrecoveryimport.ImportReceiptItem, len(results))
	for index, result := range results {
		receiptItems[index] = lyricsrecoveryimport.ImportReceiptItem{
			MusicID: result.MusicID, State: result.State, Revision: result.Revision,
			DocumentSHA256:             result.DocumentSHA256,
			AvailabilityDocumentSHA256: result.AvailabilityDocumentSHA256,
		}
	}
	importReceipt, err := lyricsrecoveryimport.NewImportReceipt(root, manifest, receipt,
		lyricsrecoveryimport.ImportReceiptBinding{
			RootManifestFileSHA256: digestBytes(rootBody), ImportManifestFileSHA256: digestBytes(manifestBody),
			EvidenceReceiptFileSHA256: digestBytes(receiptBody), DatabasePath: opts.databasePath,
			ReceiptPath: opts.importReceiptPath, Actor: opts.actor,
			CommittedAt: time.Unix(committedAt, 0).UTC().Format(time.RFC3339Nano),
			Counts:      counts, Items: receiptItems,
		})
	if err != nil {
		return err
	}
	importReceiptBody, err := lyricsrecoveryimport.MarshalImportReceiptCanonical(importReceipt)
	if err != nil {
		return err
	}
	if err := publishExactRecoveryImportReceipt(opts.importReceiptPath, importReceiptBody); err != nil {
		return err
	}
	_, err = fmt.Fprintf(output,
		"PASS recoveryImportReplay items=%d changed=%d replayed=%d complete=%d gameOnly=%d noLyrics=%d unresolved=%d batches=%d sourceDocuments=%d availabilityDocuments=%d artifacts=%d evidence=%d links=%d contributions=%d batchSha256=%s rootSha256=%s importReceiptSha256=%s importReceipt=%s\n",
		items, changed, len(results)-changed, states["complete"], states["game_only"], states["satisfied_no_lyrics"],
		states["ambiguous"]+states["missing"]+states["incomplete"]+states["failed"], batches,
		sourceDocuments, availabilityDocuments, artifacts, evidence, links, contributions,
		manifest.BatchSHA256, root.RootSHA256, importReceipt.ReceiptSHA256, opts.importReceiptPath)
	return err
}

func digestBytes(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

func publishExactRecoveryImportReceipt(path string, body []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		info, inspectErr := os.Lstat(path)
		if inspectErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("existing recovery import receipt is not a direct regular file")
		}
		existing, readErr := os.ReadFile(path)
		if readErr != nil || !bytes.Equal(existing, body) {
			return errors.New("existing recovery import receipt conflicts with the committed batch")
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("create recovery import receipt: %w", err)
	}
	cleanup := true
	defer func() {
		_ = file.Close()
		if cleanup {
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
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return err
	}
	if err := directory.Close(); err != nil {
		return err
	}
	cleanup = false
	return nil
}
