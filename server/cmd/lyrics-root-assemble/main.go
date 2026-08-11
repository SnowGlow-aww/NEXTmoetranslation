package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"

	"moesekai/server/internal/lyricsevidencepack"
	"moesekai/server/internal/lyricsrootmanifest"
)

type options struct {
	RequestPath    string
	ParentRootPath string
	PackDirectory  string
	OutputPath     string
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	var opts options
	flags := flag.NewFlagSet("lyrics-root-assemble", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&opts.RequestPath, "request", "", "private canonical root assembly request JSON")
	flags.StringVar(&opts.ParentRootPath, "parent-root", "", "direct private canonical parent root for partial or retry assembly")
	flags.StringVar(&opts.PackDirectory, "evidence-pack-dir", "", "validated private evidence pack directory")
	flags.StringVar(&opts.OutputPath, "output", "", "create-exclusive private root manifest path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || invalidPathOption(opts.RequestPath) || invalidPathOption(opts.PackDirectory) ||
		invalidPathOption(opts.OutputPath) || opts.ParentRootPath != "" && invalidPathOption(opts.ParentRootPath) {
		return errors.New("-request, -evidence-pack-dir, and -output are required without positional arguments; -parent-root must be a valid path when present")
	}
	requestBody, err := readPrivateInput(opts.RequestPath, lyricsrootmanifest.MaxAssemblyRequestBytes)
	if err != nil {
		return fmt.Errorf("read root assembly request: %w", err)
	}
	request, err := lyricsrootmanifest.DecodeAssemblyRequest(requestBody)
	if err != nil {
		return err
	}
	var parent lyricsrootmanifest.Manifest
	switch request.Scope.Kind {
	case lyricsrootmanifest.ScopeFinal:
		if opts.ParentRootPath != "" {
			return errors.New("final lyrics root request must reject -parent-root")
		}
	case lyricsrootmanifest.ScopePartial, lyricsrootmanifest.ScopeRetry:
		if invalidPathOption(opts.ParentRootPath) {
			return errors.New("partial or retry lyrics root request requires -parent-root")
		}
		parentBody, err := readPrivateInput(opts.ParentRootPath, lyricsrootmanifest.MaxManifestBytes)
		if err != nil {
			return fmt.Errorf("read parent lyrics root: %w", err)
		}
		parent, err = lyricsrootmanifest.DecodeCanonical(parentBody)
		if err != nil {
			return fmt.Errorf("decode parent lyrics root: %w", err)
		}
	}
	resolver, err := lyricsevidencepack.OpenResolver(opts.PackDirectory)
	if err != nil {
		return err
	}
	var manifest lyricsrootmanifest.Manifest
	if request.Scope.Kind == lyricsrootmanifest.ScopeFinal {
		manifest, err = lyricsrootmanifest.Assemble(request, resolver)
	} else {
		manifest, err = lyricsrootmanifest.AssembleAgainstParent(request, resolver, parent)
	}
	if err != nil {
		return err
	}
	body, err := lyricsrootmanifest.MarshalCanonical(manifest)
	if err != nil {
		return err
	}
	if err := lyricsrootmanifest.PublishCreateExclusive(opts.OutputPath, body); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "published lyrics root %s for %d songs with %d unique evidence items\n",
		manifest.RootSHA256, manifest.Coverage.Total, manifest.Coverage.UniqueEvidenceCount)
	return err
}

func invalidPathOption(value string) bool {
	return value == "" || strings.TrimSpace(value) != value
}

func readPrivateInput(path string, maximum int) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !validPrivateInputInfo(info) || info.Size() <= 0 || info.Size() > int64(maximum) {
		return nil, errors.New("private input must be a direct effective-UID-owned single-link mode-0600 regular file within its byte bound")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	pathInfo, pathErr := os.Lstat(path)
	if err != nil || pathErr != nil || !os.SameFile(info, opened) || !os.SameFile(info, pathInfo) {
		return nil, errors.New("private input changed while being opened")
	}
	body, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	if err != nil {
		return nil, err
	}
	after, statErr := file.Stat()
	afterPath, lstatErr := os.Lstat(path)
	if statErr != nil || lstatErr != nil || !os.SameFile(info, after) || !os.SameFile(info, afterPath) ||
		!validPrivateInputInfo(after) || len(body) > maximum || int64(len(body)) != info.Size() {
		return nil, errors.New("private input changed or exceeded its byte bound while being read")
	}
	return body, nil
}

func validPrivateInputInfo(info os.FileInfo) bool {
	if info == nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Geteuid() && stat.Nlink == 1
}
