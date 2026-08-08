package lifecycle

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const shutdownHelperEnv = "MOESEKAI_SHUTDOWN_HELPER"

func TestShutdownSubprocessCompletesWithinOneBudget(t *testing.T) {
	command, stderr, stdout := startShutdownHelper(t, "clean")
	started := time.Now()
	if !stdout.Scan() || stdout.Text() != "done" {
		t.Fatalf("clean shutdown did not finish: output=%q err=%v stderr=%s", stdout.Text(), stdout.Err(), stderr.String())
	}
	if elapsed := time.Since(started); elapsed > 750*time.Millisecond {
		t.Fatalf("clean shutdown took %s", elapsed)
	}
	err := command.Wait()
	if err != nil {
		t.Fatalf("clean shutdown subprocess: %v\n%s", err, stderr.String())
	}
}

func TestShutdownSubprocessWatchdogFailsClosed(t *testing.T) {
	command, stderr, stdout := startShutdownHelper(t, "blocked")
	started := time.Now()
	if !stdout.Scan() || stdout.Text() != "watchdog" {
		t.Fatalf("shutdown watchdog did not fire: output=%q err=%v stderr=%s", stdout.Text(), stdout.Err(), stderr.String())
	}
	if elapsed := time.Since(started); elapsed > 750*time.Millisecond {
		t.Fatalf("watchdog took %s", elapsed)
	}
	err := command.Wait()
	if err == nil {
		t.Fatalf("blocked shutdown exited successfully: %s", stderr.String())
	}
	exitError, ok := err.(*exec.ExitError)
	if !ok || exitError.ExitCode() != 1 {
		t.Fatalf("blocked shutdown exit = %v, output=%s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "CRITICAL: total shutdown budget") ||
		!strings.Contains(stderr.String(), "without closing SQLite") {
		t.Fatalf("watchdog output = %q", stderr.String())
	}
}

func startShutdownHelper(t *testing.T, mode string) (*exec.Cmd, *bytes.Buffer, *bufio.Scanner) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestShutdownSubprocessHelper$")
	command.Env = append(os.Environ(), shutdownHelperEnv+"="+mode)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "ready" {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatalf("shutdown helper did not become ready: output=%q err=%v stderr=%s", scanner.Text(), scanner.Err(), stderr.String())
	}
	return command, &stderr, scanner
}

func TestShutdownSubprocessHelper(t *testing.T) {
	mode := os.Getenv(shutdownHelperEnv)
	if mode == "" {
		return
	}
	fmt.Fprintln(os.Stdout, "ready")
	wait := func() error { return nil }
	if mode == "blocked" {
		wait = func() error { select {} }
	}
	err := RunShutdown(ShutdownConfig{Budget: 60 * time.Millisecond, Drain: 10 * time.Millisecond},
		func(format string, values ...any) { fmt.Fprintf(os.Stderr, format+"\n", values...) }, func(code int) {
			fmt.Fprintln(os.Stdout, "watchdog")
			os.Exit(code)
		},
		func(context.Context) error { return nil }, func() {}, wait)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	fmt.Fprintln(os.Stdout, "done")
	// Skip the test runner's race-detector process teardown; the parent measures
	// only the shutdown path after the explicit ready handshake.
	os.Exit(0)
}
