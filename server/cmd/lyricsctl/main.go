package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

const (
	Version     = "1.0.0"
	ProgramName = "lyricsctl"
)

// SubcommandHandler executes a subcommand with the provided arguments and streams.
type SubcommandHandler func(ctx context.Context, args []string, stdout, stderr io.Writer) error

// Command represents a subcommand supported by lyricsctl.
type Command struct {
	Name        string
	BinaryName  string
	Description string
	Usage       string
	DefineFlags func(f *flag.FlagSet)
	Handler     SubcommandHandler
}

// CLI holds the configuration and registered subcommands for lyricsctl.
type CLI struct {
	commands map[string]*Command
}

// NewDefaultCLI creates a CLI instance with all standard subcommands registered.
func NewDefaultCLI() *CLI {
	cli := &CLI{
		commands: make(map[string]*Command),
	}

	cli.Register(&Command{
		Name:        "preflight",
		BinaryName:  "lyrics-preflight",
		Description: "Perform source discovery, catalog verification, preflight checks, and candidate report generation",
		Usage:       "lyricsctl preflight -db <path> -output <path> [options]",
		DefineFlags: func(f *flag.FlagSet) {
			f.String("db", "", "path to an existing schema-v18 SQLite database")
			f.String("output", "", "new private JSON report path for atomic final publication")
			f.String("checkpoint", "", "create a new exclusive mode-0600 private SQLite checkpoint")
			f.String("resume-checkpoint", "", "validate and continue the same private SQLite checkpoint")
			f.String("resume-report", "", "prior complete JSON report whose selected safe items should be retried")
			f.String("resume-error-codes", "rate_limited", "comma-separated safe error codes to retry from -resume-report")
			f.String("resume-missing-reasons", "", "comma-separated missing reasons to search again from -resume-report")
			f.String("resume-incomplete-codes", "", "comma-separated fixed-revision incomplete codes to retry without Search")
			f.Bool("resume-unique-complete", false, "revalidate every recorded unique_complete fixed revision without Search")
			f.Int("concurrency", 4, "bounded source request concurrency")
			f.Int("max-attempts", 3, "maximum attempts per source operation")
			f.Duration("request-timeout", 8*time.Minute, "timeout for each source operation")
			f.Duration("retry-delay", 250*time.Millisecond, "initial retry delay")
		},
	})

	cli.Register(&Command{
		Name:        "stage",
		BinaryName:  "lyrics-stage",
		Description: "Fetch fixed candidate revisions from preflight report and assemble staging manifest",
		Usage:       "lyricsctl stage -report <path> -db <path> -output <path> [options]",
		DefineFlags: func(f *flag.FlagSet) {
			f.String("report", "", "complete lyrics-preflight JSON report")
			f.String("db", "", "existing local SQLite database snapshot")
			f.String("output", "", "new private local staging manifest path")
			f.String("evidence-receipt-output", "", "optional new private canonical EvidenceReceipt-v1 path")
			f.Int("concurrency", 4, "bounded fixed-revision fetch concurrency")
			f.Int("max-attempts", 3, "maximum attempts per fixed-revision fetch")
			f.Duration("request-timeout", 8*time.Minute, "timeout for each fixed-revision operation")
			f.Duration("retry-delay", 250*time.Millisecond, "initial retry delay")
		},
	})

	cli.Register(&Command{
		Name:        "import-stage",
		BinaryName:  "lyrics-import-stage",
		Description: "Commit staged lyrics into the local database with backup, receipt, and audit logging",
		Usage:       "lyricsctl import-stage -manifest <path> -db <path> -backup <path> -receipt <path> [options]",
		DefineFlags: func(f *flag.FlagSet) {
			f.String("validation-receipt", "", "path to validation receipt")
			f.String("root-manifest", "", "path to root manifest")
			f.String("manifest", "", "path to staging manifest")
			f.String("evidence-receipt", "", "path to evidence receipt")
			f.String("db", "", "path to SQLite database")
			f.String("backup", "", "path to database backup output")
			f.String("backup-sha256", "", "expected database backup SHA-256")
			f.String("receipt", "", "path to import receipt output")
			f.String("operator", "", "operator identifier")
			f.Bool("confirm-local-offline", false, "confirm local offline import execution")
		},
	})

	cli.Register(&Command{
		Name:        "validate",
		BinaryName:  "lyrics-validate",
		Description: "Validate staging manifest self-consistency, schema integrity, and batch hash",
		Usage:       "lyricsctl validate -input <manifest-path>",
		DefineFlags: func(f *flag.FlagSet) {
			f.String("input", "", "local lyrics staging manifest to check for self-consistency and integrity")
		},
	})

	cli.Register(&Command{
		Name:        "catalog-filter",
		BinaryName:  "lyrics-catalog-filter",
		Description: "Filter catalog database against target mapping report and produce filtered catalog snapshot and receipt",
		Usage:       "lyricsctl catalog-filter -source-catalog <path> -source-catalog-sha256 <sha> -target-map <path> -target-map-sha256 <sha> -output-root <dir>",
		DefineFlags: func(f *flag.FlagSet) {
			f.String("source-catalog", "", "source catalog database path")
			f.String("source-catalog-sha256", "", "expected source catalog SHA-256")
			f.String("target-map", "", "target map report path")
			f.String("target-map-sha256", "", "expected target map SHA-256")
			f.String("output-root", "", "output root directory")
		},
	})

	cli.Register(&Command{
		Name:        "candidate",
		BinaryName:  "lyrics-recovery-public-candidate",
		Description: "Generate strict Public v3 candidate bundle and optional v2 compatibility artifacts from recovery database",
		Usage:       "lyricsctl candidate -database <path> -batch-sha256 <sha> -output-directory <dir> [options]",
		DefineFlags: func(f *flag.FlagSet) {
			f.String("database", "", "existing standalone recovery SQLite database")
			f.String("batch-sha256", "", "exact lowercase recovery batch SHA-256")
			f.String("output-directory", "", "new immutable local strict Public v3 candidate directory")
			f.String("v2-compat-output-directory", "", "optional separate immutable lossless Public v2 compatibility directory")
		},
	})

	cli.Register(&Command{
		Name:        "recovery",
		BinaryName:  "lyrics-recovery",
		Description: "Coordinate lyrics recovery phases, state provisioning, replay, and migration",
		Usage:       "lyricsctl recovery -mode <mode> -plan <path> -catalog <path> [options]",
		DefineFlags: func(f *flag.FlagSet) {
			f.String("mode", "check", "recovery mode (check, run, provision-live-state, replay, etc.)")
			f.String("plan", "", "extraction plan path")
			f.String("plan-sha256", "", "expected extraction plan SHA-256")
			f.String("source-root", "", "source root directory")
			f.String("catalog", "", "catalog database path")
			f.String("catalog-sha256", "", "expected catalog SHA-256")
			f.Int("catalog-count", 0, "expected catalog record count")
			f.String("catalog-music-ids-sha256", "", "expected catalog music IDs SHA-256")
			f.String("ledger", "", "ledger output path")
			f.String("acquisition-set", "", "acquisition set output path")
			f.String("provider-outcomes", "", "provider outcomes path")
			f.String("song-results", "", "song results path")
			f.String("evidence-pack", "", "evidence pack path")
			f.String("root-manifest", "", "root manifest path")
			f.String("review-decision-manifest", "", "review decision manifest path")
			f.String("parent-root", "", "parent root directory")
			f.String("rebind-source-ledger", "", "rebind source ledger path")
			f.String("rebind-source-acquisition-set", "", "rebind source acquisition set path")
			f.String("rebind-supplement-ledger", "", "rebind supplement ledger path")
			f.String("rebind-supplement-acquisition-set", "", "rebind supplement acquisition set path")
			f.String("sekaipedia-list-replay-ledger", "", "sekaipedia list replay ledger path")
			f.String("sekaipedia-list-replay-acquisition-id", "", "sekaipedia list replay acquisition ID")
			f.String("acquisition-music-ids", "", "comma-separated music IDs to acquire")
			f.String("live-canary-token", "", "authorization token for live canary")
			f.String("acquisition-token", "", "authorization token for acquisition")
			f.String("migration-token", "", "authorization token for migration")
			f.String("live-state-provision-token", "", "authorization token for live state provision")
		},
	})

	return cli
}

// Register adds a command to the CLI registry.
func (cli *CLI) Register(cmd *Command) {
	if cmd.Handler == nil && cmd.BinaryName != "" {
		binaryName := cmd.BinaryName
		cmd.Handler = func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
			return executeBinaryDelegate(ctx, binaryName, args, stdout, stderr)
		}
	}
	cli.commands[cmd.Name] = cmd
}

// SetHandler allows overriding the execution handler for a registered command.
func (cli *CLI) SetHandler(name string, handler SubcommandHandler) error {
	cmd, ok := cli.commands[name]
	if !ok {
		return fmt.Errorf("unknown subcommand %q", name)
	}
	cmd.Handler = handler
	return nil
}

// FindCommand retrieves a command by name.
func (cli *CLI) FindCommand(name string) *Command {
	return cli.commands[name]
}

// Commands returns all registered commands sorted by name.
func (cli *CLI) Commands() []*Command {
	list := make([]*Command, 0, len(cli.commands))
	for _, cmd := range cli.commands {
		list = append(list, cmd)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].Name < list[j].Name
	})
	return list
}

// Dispatch routes the command-line invocation to the proper subcommand handler.
func (cli *CLI) Dispatch(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		cli.PrintUsage(stderr)
		return errors.New("no subcommand specified; run 'lyricsctl help' for usage")
	}

	cmdName := args[0]

	// Handle version
	if cmdName == "version" || cmdName == "--version" || cmdName == "-v" {
		fmt.Fprintf(stdout, "%s version %s\n", ProgramName, Version)
		return nil
	}

	// Handle help
	if cmdName == "help" || cmdName == "--help" || cmdName == "-h" {
		if len(args) > 1 {
			targetName := args[1]
			targetCmd := cli.FindCommand(targetName)
			if targetCmd != nil {
				cli.PrintCommandHelp(stdout, targetCmd)
				return nil
			}
			fmt.Fprintf(stderr, "unknown subcommand %q for help\n\n", targetName)
			cli.PrintUsage(stderr)
			return fmt.Errorf("unknown subcommand %q", targetName)
		}
		cli.PrintUsage(stdout)
		return nil
	}

	cmd := cli.FindCommand(cmdName)
	if cmd == nil {
		fmt.Fprintf(stderr, "unknown subcommand %q\n\n", cmdName)
		cli.PrintUsage(stderr)
		return fmt.Errorf("unknown subcommand %q; run 'lyricsctl help' for available commands", cmdName)
	}

	subArgs := args[1:]

	// Check if help flag was passed to subcommand
	for _, arg := range subArgs {
		if arg == "-h" || arg == "--help" || arg == "-help" {
			cli.PrintCommandHelp(stdout, cmd)
			return nil
		}
	}

	// Validate flags if defined
	if cmd.DefineFlags != nil {
		flagSet := flag.NewFlagSet(cmd.Name, flag.ContinueOnError)
		flagSet.SetOutput(io.Discard)
		cmd.DefineFlags(flagSet)
		if err := flagSet.Parse(subArgs); err != nil {
			fmt.Fprintf(stderr, "%s: flag error: %v\n\n", cmd.Name, err)
			cli.PrintCommandHelp(stderr, cmd)
			return fmt.Errorf("invalid flags for %s: %w", cmd.Name, err)
		}
	}

	if cmd.Handler == nil {
		return fmt.Errorf("subcommand %q has no registered handler", cmd.Name)
	}

	return cmd.Handler(ctx, subArgs, stdout, stderr)
}

// PrintUsage outputs the root usage guide listing all subcommands.
func (cli *CLI) PrintUsage(w io.Writer) {
	fmt.Fprintf(w, "Usage: %s <subcommand> [flags]\n\n", ProgramName)
	fmt.Fprintf(w, "Unified management CLI for lyrics preflight, staging, import, verification, and recovery.\n\n")
	fmt.Fprintf(w, "Available Subcommands:\n")

	for _, cmd := range cli.Commands() {
		fmt.Fprintf(w, "  %-16s %s\n", cmd.Name, cmd.Description)
	}
	fmt.Fprintf(w, "  %-16s %s\n", "version", "Print version information")
	fmt.Fprintf(w, "  %-16s %s\n", "help", "Show help for lyricsctl or a specific subcommand")

	fmt.Fprintf(w, "\nRun '%s help <subcommand>' or '%s <subcommand> --help' for details on a specific subcommand.\n", ProgramName, ProgramName)
}

// PrintCommandHelp outputs detailed help for a specific command including its flags.
func (cli *CLI) PrintCommandHelp(w io.Writer, cmd *Command) {
	fmt.Fprintf(w, "Usage: %s\n\n", cmd.Usage)
	fmt.Fprintf(w, "%s\n", cmd.Description)

	if cmd.DefineFlags != nil {
		flagSet := flag.NewFlagSet(cmd.Name, flag.ContinueOnError)
		flagSet.SetOutput(w)
		cmd.DefineFlags(flagSet)
		fmt.Fprintf(w, "\nFlags:\n")
		flagSet.PrintDefaults()
	}
}

func executeBinaryDelegate(ctx context.Context, binaryName string, args []string, stdout, stderr io.Writer) error {
	path, err := resolveBinaryPath(binaryName)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func resolveBinaryPath(binaryName string) (string, error) {
	// Look in directory of current running executable first
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidate := filepath.Join(dir, binaryName)
		if isExecutableFile(candidate) {
			return candidate, nil
		}
	}
	// Look in PATH
	if path, err := exec.LookPath(binaryName); err == nil {
		return path, nil
	}
	return "", fmt.Errorf("subcommand delegate binary %q not found in executable directory or PATH", binaryName)
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode()&0111 != 0
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cli := NewDefaultCLI()
	if err := cli.Dispatch(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		os.Exit(1)
	}
}
