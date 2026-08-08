package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const helperPolicyEnvironment = "LAUNCHER_TEST_POLICY"

func TestHostileBASHENVDoesNotWriteMarker(t *testing.T) {
	runbook, policy := newTestRunbook(t, "#!/bin/bash\n/usr/bin/printf 'RUNBOOK_RAN\\n'\n")
	marker := filepath.Join(filepath.Dir(runbook), "bash-env-marker")
	hostile := filepath.Join(filepath.Dir(runbook), "hostile-bash-env.sh")
	writeMode(t, hostile, []byte("#!/bin/bash\n: > "+shellSingleQuote(marker)+"\n"), 0o700)

	stdout, stderr, err := runHelper(t, policy, []string{"BASH_ENV=" + hostile})
	if err == nil {
		t.Fatal("hostile BASH_ENV invocation unexpectedly succeeded")
	}
	if !errors.Is(statError(marker), os.ErrNotExist) {
		t.Fatalf("hostile BASH_ENV wrote marker %s", marker)
	}
	if strings.Contains(stdout, "RUNBOOK_RAN") {
		t.Fatal("reviewed runbook started despite hostile BASH_ENV")
	}
	if !strings.Contains(stderr, "BASH_ENV") {
		t.Fatalf("rejection did not identify BASH_ENV: %s", stderr)
	}
	if strings.Contains(stderr, hostile) {
		t.Fatalf("BASH_ENV value leaked in diagnostics: %s", stderr)
	}
}

func TestCandidateSubstitutionFailsBeforeExecution(t *testing.T) {
	runbook, policy := newTestRunbook(t, "#!/bin/bash\n/usr/bin/printf 'SUBSTITUTED_CANDIDATE_RAN\\n'\n")
	body, err := os.ReadFile(runbook)
	if err != nil {
		t.Fatalf("read reviewed candidate: %v", err)
	}
	replacement := filepath.Join(filepath.Dir(runbook), "replacement.sh")
	writeMode(t, replacement, body, 0o700)
	if err := os.Rename(replacement, runbook); err != nil {
		t.Fatalf("replace candidate: %v", err)
	}

	stdout, stderr, err := runHelper(t, policy, nil)
	if err == nil {
		t.Fatal("same-byte substituted candidate unexpectedly executed")
	}
	if stdout != "" {
		t.Fatalf("same-byte substituted candidate produced stdout: %q", stdout)
	}
	if !strings.Contains(stderr, "identity") {
		t.Fatalf("substitution rejection lacked identity failure: %s", stderr)
	}
}

func TestHardLinkedRunbookIsRejected(t *testing.T) {
	runbook, policy := newTestRunbook(t, "#!/bin/bash\n/usr/bin/printf 'HARD_LINKED_RUNBOOK_RAN\\n'\n")
	alias := filepath.Join(filepath.Dir(runbook), "runbook-hard-link.sh")
	if err := os.Link(runbook, alias); err != nil {
		t.Fatalf("create hard link: %v", err)
	}

	stdout, stderr, err := runHelper(t, policy, nil)
	if err == nil {
		t.Fatal("hard-linked runbook unexpectedly executed")
	}
	if stdout != "" {
		t.Fatalf("hard-linked runbook produced stdout: %q", stdout)
	}
	if !strings.Contains(stderr, "identity") {
		t.Fatalf("hard-link rejection lacked identity failure: %s", stderr)
	}
}

func TestInheritedValuesAreNotLeakedToReviewedRunbook(t *testing.T) {
	secret := "acceptance-secret-value-must-not-cross-exec"
	runbookBody := `#!/bin/bash
if /usr/bin/printenv LAUNCHER_SECRET >/dev/null 2>&1; then
  /usr/bin/printf 'LEAKED=%s\n' "$LAUNCHER_SECRET"
  exit 91
fi
if /usr/bin/printenv HOME >/dev/null 2>&1; then
  /usr/bin/printf 'HOME_LEAKED=%s\n' "$HOME"
  exit 92
fi
/usr/bin/printf 'CLEAN\n'
`
	_, policy := newTestRunbook(t, runbookBody)

	stdout, stderr, err := runHelper(t, policy, []string{"LAUNCHER_SECRET=" + secret})
	if err != nil {
		t.Fatalf("closed-environment invocation failed: %v; stderr=%s", err, stderr)
	}
	if stdout != "CLEAN\n" {
		t.Fatalf("unexpected reviewed runbook output: %q", stdout)
	}
	if strings.Contains(stdout, secret) || strings.Contains(stderr, secret) {
		t.Fatal("inherited value leaked through launcher output")
	}
}

func TestRejectInheritedShellLoaderAndBuildControls(t *testing.T) {
	tests := []struct {
		name  string
		entry string
	}{
		{name: "BASH_ENV", entry: "BASH_ENV=/private/tmp/hostile"},
		{name: "ENV", entry: "ENV=/private/tmp/hostile"},
		{name: "exported function name", entry: "BASH_FUNC_attack%%=() { :; }"},
		{name: "legacy exported function value", entry: "attack=() { :; }"},
		{name: "Darwin loader", entry: "DYLD_INSERT_LIBRARIES=/private/tmp/hostile.dylib"},
		{name: "ELF loader", entry: "LD_PRELOAD=/private/tmp/hostile.so"},
		{name: "Go build", entry: "GOFLAGS=-toolexec=/private/tmp/hostile"},
		{name: "compiler", entry: "CC=/private/tmp/hostile"},
		{name: "live authorization", entry: "MOESEKAI_RECOVERY_V2_LIVE_CANARY_AUTHORIZATION=authorized"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := rejectInheritedEnvironment([]string{test.entry}); err == nil {
				t.Fatalf("dangerous environment entry was accepted: %s", test.entry)
			}
		})
	}
}

func TestOnlyOfflineAcceptanceArgumentsAreAccepted(t *testing.T) {
	policy := productionPolicy()
	base := []string{"--runbook", policy.Runbook.Path, "--runbook-sha256", policy.Runbook.SHA256, "--"}
	for _, allowed := range []string{"--check-only", "--offline-acceptance", "--replay", "--fixture-canary"} {
		if _, err := parseInvocation(policy, append(append([]string{}, base...), allowed)); err != nil {
			t.Fatalf("offline argument %s rejected: %v", allowed, err)
		}
	}
	for _, forbidden := range []string{"--live-canary", "--acquisition", "--migration-hold", "--import", "--deploy", "--help"} {
		if _, err := parseInvocation(policy, append(append([]string{}, base...), forbidden)); err == nil {
			t.Fatalf("privileged or non-acceptance argument %s was accepted", forbidden)
		}
	}
}

func TestLauncherHelperProcess(t *testing.T) {
	encoded := os.Getenv(helperPolicyEnvironment)
	if encoded == "" {
		return
	}
	body, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		fmt.Fprintf(os.Stderr, "HOLD %s test helper: decode policy: %v\n", launcherName, err)
		os.Exit(2)
	}
	var policy launcherPolicy
	if err := json.Unmarshal(body, &policy); err != nil {
		fmt.Fprintf(os.Stderr, "HOLD %s test helper: parse policy: %v\n", launcherName, err)
		os.Exit(2)
	}
	arguments, err := argumentsAfterDoubleDash(os.Args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "HOLD %s test helper: %v\n", launcherName, err)
		os.Exit(2)
	}
	inherited := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		name := entry
		if separator := strings.IndexByte(entry, '='); separator >= 0 {
			name = entry[:separator]
		}
		if strings.HasPrefix(name, "LAUNCHER_TEST_") {
			continue
		}
		inherited = append(inherited, entry)
	}
	if err := launch(policy, arguments, inherited); err != nil {
		fmt.Fprintf(os.Stderr, "HOLD %s: %v\n", launcherName, err)
		os.Exit(2)
	}
	os.Exit(0)
}

func newTestRunbook(t *testing.T, body string) (string, launcherPolicy) {
	t.Helper()
	directory, err := os.MkdirTemp("/private/tmp", "lyrics-acceptance-launcher-test-")
	if err != nil {
		t.Fatalf("create canonical test directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(directory); err != nil {
			t.Errorf("remove test directory: %v", err)
		}
	})
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatalf("secure test directory: %v", err)
	}
	canonicalDirectory, err := filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatalf("canonicalize test directory: %v", err)
	}
	runbook := filepath.Join(canonicalDirectory, "reviewed-runbook.sh")
	writeMode(t, runbook, []byte(body), 0o700)
	pin := captureFilePin(t, runbook)
	policy := productionPolicy()
	policy.GOOS = runtime.GOOS
	policy.GOARCH = runtime.GOARCH
	policy.ExpectedEUID = os.Geteuid()
	policy.WorkingDirectory = canonicalDirectory
	policy.Runbook = pin
	policy.RunbookAncestry = captureAncestry(t, runbook)
	policy.Bash = captureFilePin(t, "/bin/bash")
	policy.BashAncestry = captureAncestry(t, "/bin/bash")
	return runbook, policy
}

func captureFilePin(t *testing.T, path string) filePin {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read test runbook: %v", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("inspect test runbook: %v", err)
	}
	identity, err := identityFromFileInfo(info)
	if err != nil {
		t.Fatalf("capture test runbook identity: %v", err)
	}
	digest := sha256.Sum256(body)
	return filePin{Path: path, SHA256: hex.EncodeToString(digest[:]), Identity: identity}
}

func captureAncestry(t *testing.T, path string) []directoryPin {
	t.Helper()
	paths := ancestryPaths(filepath.Dir(path))
	pins := make([]directoryPin, 0, len(paths))
	for _, directory := range paths {
		info, err := os.Lstat(directory)
		if err != nil {
			t.Fatalf("inspect test ancestry %s: %v", directory, err)
		}
		identity, err := identityFromFileInfo(info)
		if err != nil {
			t.Fatalf("capture test ancestry %s: %v", directory, err)
		}
		pins = append(pins, directoryPin{Path: directory, Identity: identity})
	}
	return pins
}

func runHelper(t *testing.T, policy launcherPolicy, extraEnvironment []string) (string, string, error) {
	t.Helper()
	body, err := json.Marshal(policy)
	if err != nil {
		t.Fatalf("encode helper policy: %v", err)
	}
	arguments := []string{
		"-test.run=^TestLauncherHelperProcess$",
		"--",
		"--runbook", policy.Runbook.Path,
		"--runbook-sha256", policy.Runbook.SHA256,
		"--", "--check-only",
	}
	command := exec.Command(os.Args[0], arguments...)
	command.Env = []string{
		"PATH=/usr/bin:/bin",
		"HOME=" + policy.WorkingDirectory,
		helperPolicyEnvironment + "=" + base64.StdEncoding.EncodeToString(body),
	}
	command.Env = append(command.Env, extraEnvironment...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err = command.Run()
	return stdout.String(), stderr.String(), err
}

func argumentsAfterDoubleDash(arguments []string) ([]string, error) {
	for index, argument := range arguments {
		if argument == "--" {
			if index+1 >= len(arguments) {
				return nil, errors.New("launcher arguments are absent")
			}
			return arguments[index+1:], nil
		}
	}
	return nil, errors.New("launcher argument separator is absent")
}

func writeMode(t *testing.T, path string, body []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, body, mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
}

func statError(path string) error {
	_, err := os.Stat(path)
	return err
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
