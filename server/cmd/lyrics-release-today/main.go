package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

const commandName = "lyrics-release-today"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", commandName, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string, stdout io.Writer) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if len(arguments) == 0 {
		return usageError()
	}
	switch arguments[0] {
	case "help", "-h", "--help":
		_, err := io.WriteString(stdout, usageText())
		return err
	case "validate-fresh", "check-backup", "check-post-import", "check-deploy", "check-public":
		return errors.New("retired historical 698-song Public v2 release gate; use lyrics-recovery-acceptance-launcher and lyrics-recovery-public-candidate for the current Public v3 recovery chain")
	default:
		return usageError()
	}
}

func usageError() error {
	return errors.New("usage: lyrics-release-today validate-fresh|check-backup|check-post-import|check-deploy|check-public [explicit flags]")
}

func usageText() string {
	return `lyrics-release-today is retired.

It remains in the source tree only to preserve and test the historical 2026-08-03
698-song Public v2 validation contract. Its operational subcommands are disabled
so they cannot be mistaken for the current recovery release gate.

Use lyrics-recovery-acceptance-launcher for the current offline acceptance run and
lyrics-recovery-public-candidate for current Public v3 candidate generation.
No command in this retired CLI performs provider acquisition, database mutation,
deployment, or publication.
`
}
