package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

type commandOptions struct {
	checkpointPath        string
	destinationRoot       string
	dryRun                bool
	expectedCheckpointSHA string
	expectedEvidenceCount int
}

func main() {
	if err := runCLI(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "lyrics evidence migration failed: %v\n", err)
		os.Exit(1)
	}
}

func runCLI(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("lyrics-evidence-migrate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	checkpoint := flags.String("checkpoint", "", "private v2 preflight checkpoint path")
	destination := flags.String("destination", "", "new private acquisition ledger root")
	dryRun := flags.Bool("dry-run", false, "verify the checkpoint without creating a ledger")
	expectedCheckpointSHA := flags.String("expected-checkpoint-sha256", "", "required exact lowercase checkpoint SHA-256")
	expectedEvidenceCount := flags.Int("expected-evidence-count", 0, "required complete checkpoint evidence count")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("positional arguments are not supported")
	}
	result, err := executeMigration(ctx, commandOptions{
		checkpointPath: *checkpoint, destinationRoot: *destination, dryRun: *dryRun,
		expectedCheckpointSHA: *expectedCheckpointSHA, expectedEvidenceCount: *expectedEvidenceCount,
	})
	if err != nil {
		return err
	}
	body, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode migration summary: %w", err)
	}
	if _, err := fmt.Fprintf(stdout, "%s\n", body); err != nil {
		return fmt.Errorf("write migration summary: %w", err)
	}
	return nil
}
