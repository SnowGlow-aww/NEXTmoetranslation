package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const launcherName = "lyrics-recovery-acceptance-launcher"

type objectIdentity struct {
	Device         uint64 `json:"device,omitempty"`
	Inode          uint64 `json:"inode,omitempty"`
	UID            uint32 `json:"uid,omitempty"`
	GID            uint32 `json:"gid,omitempty"`
	Mode           uint32 `json:"mode,omitempty"`
	LinkCount      uint64 `json:"linkCount,omitempty"`
	Size           int64  `json:"size,omitempty"`
	ModificationNS int64  `json:"modificationNs,omitempty"`
}

type filePin struct {
	Path     string         `json:"path"`
	SHA256   string         `json:"sha256"`
	Identity objectIdentity `json:"identity,omitempty"`
}

type directoryPin struct {
	Path     string         `json:"path"`
	Identity objectIdentity `json:"identity,omitempty"`
}

type launcherPolicy struct {
	GOOS             string         `json:"goos"`
	GOARCH           string         `json:"goarch"`
	WorkingDirectory string         `json:"workingDirectory"`
	Runbook          filePin        `json:"runbook"`
	RunbookAncestry  []directoryPin `json:"runbookAncestry"`
	Bash             filePin        `json:"bash"`
	BashAncestry     []directoryPin `json:"bashAncestry"`
}

type invocation struct {
	runbookPath string
	runbookSHA  string
	runbookArgs []string
}

func main() {
	if err := launch(productionPolicy(), os.Args[1:], os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "HOLD %s: %v\n", launcherName, err)
		os.Exit(2)
	}
}

func launch(policy launcherPolicy, arguments, inheritedEnvironment []string) error {
	invocation, err := parseInvocation(policy, arguments)
	if err != nil {
		return err
	}
	if err := rejectInheritedEnvironment(inheritedEnvironment); err != nil {
		return err
	}
	if err := verifyPlatform(policy); err != nil {
		return err
	}
	if policy.WorkingDirectory != filepath.Dir(policy.Runbook.Path) {
		return errors.New("reviewed working directory is not the direct runbook parent")
	}
	if err := verifyCanonicalAncestry(policy.Runbook.Path, policy.RunbookAncestry); err != nil {
		return fmt.Errorf("runbook ancestry: %w", err)
	}
	if err := verifyCanonicalAncestry(policy.Bash.Path, policy.BashAncestry); err != nil {
		return fmt.Errorf("bash ancestry: %w", err)
	}
	if err := verifyPinnedFile(policy.Runbook, invocation.runbookSHA); err != nil {
		return fmt.Errorf("runbook identity: %w", err)
	}
	if err := verifyPinnedFile(policy.Bash, policy.Bash.SHA256); err != nil {
		return fmt.Errorf("bash identity: %w", err)
	}
	if err := verifyStillPinned(policy.Runbook); err != nil {
		return fmt.Errorf("runbook final identity: %w", err)
	}
	if err := verifyStillPinned(policy.Bash); err != nil {
		return fmt.Errorf("bash final identity: %w", err)
	}
	if err := verifyCanonicalAncestry(policy.Runbook.Path, policy.RunbookAncestry); err != nil {
		return fmt.Errorf("runbook final ancestry: %w", err)
	}
	if err := verifyCanonicalAncestry(policy.Bash.Path, policy.BashAncestry); err != nil {
		return fmt.Errorf("bash final ancestry: %w", err)
	}

	argv := []string{policy.Bash.Path, "--noprofile", "--norc", invocation.runbookPath}
	argv = append(argv, invocation.runbookArgs...)
	closedEnvironment := []string{
		"PATH=/usr/bin:/bin",
		"LC_ALL=C",
		"LANG=C",
		"TZ=UTC",
	}
	return execReviewed(policy.Bash.Path, argv, closedEnvironment, policy.WorkingDirectory)
}

func parseInvocation(policy launcherPolicy, arguments []string) (invocation, error) {
	const usage = "usage: --runbook PATH --runbook-sha256 SHA256 -- MODE"
	if len(arguments) != 6 || arguments[0] != "--runbook" || arguments[2] != "--runbook-sha256" || arguments[4] != "--" {
		return invocation{}, errors.New(usage)
	}
	candidate := invocation{
		runbookPath: arguments[1],
		runbookSHA:  arguments[3],
		runbookArgs: []string{arguments[5]},
	}
	if candidate.runbookPath != policy.Runbook.Path {
		return invocation{}, errors.New("externally supplied runbook path does not match the reviewed path")
	}
	if !isCanonicalAbsolutePath(candidate.runbookPath) {
		return invocation{}, errors.New("externally supplied runbook path is not a canonical absolute path")
	}
	if !isLowerSHA256(candidate.runbookSHA) || candidate.runbookSHA != policy.Runbook.SHA256 {
		return invocation{}, errors.New("externally supplied runbook SHA-256 does not match the reviewed digest")
	}
	if !allowedRunbookArgument(candidate.runbookArgs[0]) {
		return invocation{}, errors.New("runbook argument is not an offline acceptance action")
	}
	return candidate, nil
}

func allowedRunbookArgument(argument string) bool {
	switch argument {
	case "--check-only", "--offline-acceptance", "--replay", "--fixture-canary":
		return true
	default:
		return false
	}
}

func isCanonicalAbsolutePath(path string) bool {
	return path != "" && strings.TrimSpace(path) == path && filepath.IsAbs(path) && filepath.Clean(path) == path
}

func isLowerSHA256(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func rejectInheritedEnvironment(environment []string) error {
	for _, entry := range environment {
		separator := strings.IndexByte(entry, '=')
		if separator <= 0 {
			return errors.New("inherited environment contains an invalid entry")
		}
		name, value := entry[:separator], entry[separator+1:]
		if !validEnvironmentName(name) {
			return errors.New("inherited environment contains an invalid variable name")
		}
		if strings.HasPrefix(strings.TrimLeft(value, " \t"), "()") {
			return fmt.Errorf("exported shell function is forbidden: %s", name)
		}
		if forbiddenEnvironmentName(name) {
			return fmt.Errorf("forbidden inherited environment control: %s", name)
		}
	}
	return nil
}

func validEnvironmentName(name string) bool {
	for index, character := range name {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || character == '_' || (index > 0 && character >= '0' && character <= '9') {
			continue
		}
		return false
	}
	return name != ""
}

func forbiddenEnvironmentName(name string) bool {
	switch name {
	case "BASH_ENV", "ENV", "BASHOPTS", "SHELLOPTS", "BASH_COMPAT", "BASH_LOADABLES_PATH", "BASH_XTRACEFD",
		"CDPATH", "GLOBIGNORE", "IFS", "POSIXLY_CORRECT", "PROMPT_COMMAND", "PS4", "TMOUT",
		"INPUTRC", "LOCPATH", "NLSPATH", "TERMINFO", "TERMINFO_DIRS", "TZDIR",
		"LIBPATH", "SHLIB_PATH", "TMPDIR", "SDKROOT", "DEVELOPER_DIR", "MACOSX_DEPLOYMENT_TARGET",
		"CC", "CXX", "CPP", "AR", "AS", "LD", "NM", "OBJCOPY", "OBJDUMP", "RANLIB", "STRIP", "FC", "F77",
		"MAKEFLAGS", "MFLAGS", "CFLAGS", "CPPFLAGS", "CXXFLAGS", "LDFLAGS", "LIBRARY_PATH", "CPATH",
		"C_INCLUDE_PATH", "CPLUS_INCLUDE_PATH", "OBJC_INCLUDE_PATH",
		"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy":
		return true
	}
	for _, prefix := range []string{
		"BASH_FUNC_", "DYLD_", "LD_", "_RLD", "GO", "CGO_", "GCC_", "CLANG_", "GIT_",
		"PKG_CONFIG", "CMAKE_", "MESON_", "NINJA_", "CARGO", "RUST", "NIX_", "PYTHON", "PERL", "RUBY",
		"RECOVERY_", "MOESEKAI_", "IMPORT_", "DEPLOY_", "PUBLIC_",
	} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func verifyPlatform(policy launcherPolicy) error {
	if runtime.GOOS != policy.GOOS || runtime.GOARCH != policy.GOARCH {
		return fmt.Errorf("platform is %s/%s, expected %s/%s", runtime.GOOS, runtime.GOARCH, policy.GOOS, policy.GOARCH)
	}
	return nil
}

func verifyCanonicalAncestry(path string, expected []directoryPin) error {
	if !isCanonicalAbsolutePath(path) {
		return errors.New("path is not a canonical absolute path")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("cannot resolve path: %w", err)
	}
	if resolved != path {
		return errors.New("path traverses a symlink or filesystem alias")
	}
	actualPaths := ancestryPaths(filepath.Dir(path))
	if len(actualPaths) != len(expected) {
		return errors.New("ancestry depth changed")
	}
	for index, directory := range actualPaths {
		pin := expected[index]
		if directory != pin.Path {
			return errors.New("ancestry path changed")
		}
		info, err := os.Lstat(directory)
		if err != nil {
			return fmt.Errorf("cannot inspect ancestry directory: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("ancestry contains a non-direct directory")
		}
	}
	return nil
}

func ancestryPaths(directory string) []string {
	var reversed []string
	for {
		reversed = append(reversed, directory)
		parent := filepath.Dir(directory)
		if parent == directory {
			break
		}
		directory = parent
	}
	paths := make([]string, len(reversed))
	for index := range reversed {
		paths[len(reversed)-1-index] = reversed[index]
	}
	return paths
}

func verifyPinnedFile(pin filePin, expectedSHA string) error {
	if !isCanonicalAbsolutePath(pin.Path) || !isLowerSHA256(pin.SHA256) || expectedSHA != pin.SHA256 {
		return errors.New("file pin is invalid")
	}
	beforeInfo, err := os.Lstat(pin.Path)
	if err != nil {
		return fmt.Errorf("cannot inspect file: %w", err)
	}
	if beforeInfo.Mode()&os.ModeSymlink != 0 || !beforeInfo.Mode().IsRegular() {
		return errors.New("file is not a direct regular file")
	}
	before, err := identityFromFileInfo(beforeInfo)
	if err != nil {
		return err
	}
	if before.LinkCount > 1 {
		return errors.New("file has hard links")
	}

	file, err := openNoFollow(pin.Path)
	if err != nil {
		return fmt.Errorf("cannot open direct file: %w", err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return fmt.Errorf("cannot inspect open file: %w", err)
	}
	if !openedInfo.Mode().IsRegular() {
		return errors.New("opened file is not a direct regular file")
	}
	opened, err := identityFromFileInfo(openedInfo)
	if err != nil {
		return err
	}
	if before.Device != opened.Device || before.Inode != opened.Inode {
		return errors.New("opened file identity differs from path identity")
	}

	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return fmt.Errorf("cannot hash file: %w", err)
	}
	if hex.EncodeToString(digest.Sum(nil)) != expectedSHA {
		return errors.New("SHA-256 mismatch")
	}

	afterInfo, err := os.Lstat(pin.Path)
	if err != nil {
		return fmt.Errorf("cannot re-inspect file: %w", err)
	}
	after, err := identityFromFileInfo(afterInfo)
	if err != nil {
		return err
	}
	if before.Device != after.Device || before.Inode != after.Inode {
		return errors.New("file identity changed during validation")
	}
	return nil
}

func verifyStillPinned(pin filePin) error {
	info, err := os.Lstat(pin.Path)
	if err != nil {
		return fmt.Errorf("cannot inspect pinned path: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("pinned path is no longer a direct regular file")
	}
	identity, err := identityFromFileInfo(info)
	if err != nil {
		return err
	}
	if identity.LinkCount > 1 {
		return errors.New("pinned path has hard links")
	}
	return nil
}
