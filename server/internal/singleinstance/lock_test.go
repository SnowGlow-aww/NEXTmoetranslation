package singleinstance

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const (
	helperProcessEnv = "MOESEKAI_INSTANCE_LOCK_HELPER"
	helperDBPathEnv  = "MOESEKAI_INSTANCE_LOCK_DB"
)

func TestProcessLifetimeDatabaseOwnership(t *testing.T) {
	if os.Getenv(helperProcessEnv) == "1" {
		runLockHelper(t)
		return
	}

	databasePath := filepath.Join(t.TempDir(), "instance.db")
	command := exec.Command(os.Args[0], "-test.run=^TestProcessLifetimeDatabaseOwnership$")
	command.Env = append(os.Environ(), helperProcessEnv+"=1", helperDBPathEnv+"="+databasePath)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	finished := false
	t.Cleanup(func() {
		_ = stdin.Close()
		if !finished {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})

	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "locked" {
		t.Fatalf("helper did not acquire lock: output=%q err=%v", scanner.Text(), scanner.Err())
	}
	aliasPath := filepath.Join(filepath.Dir(databasePath), ".", filepath.Base(databasePath))
	second, err := Acquire(aliasPath)
	if second != nil {
		_ = second.Close()
	}
	if !errors.Is(err, ErrAlreadyOwned) {
		t.Fatalf("second process acquire error = %v", err)
	}

	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
	finished = true

	reacquired, err := Acquire(databasePath)
	if err != nil {
		t.Fatalf("acquire after owner exit: %v", err)
	}
	if err := reacquired.Close(); err != nil {
		t.Fatalf("release reacquired lock: %v", err)
	}
}

func TestCloseReleasesDatabaseOwnership(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "instance.db")
	first, err := Acquire(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	second, err := Acquire(databasePath)
	if err != nil {
		t.Fatalf("acquire after close: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireCreatesMissingDatabaseParentsAndKeepsCanonicalAliasOwnership(t *testing.T) {
	root := t.TempDir()
	realRoot := filepath.Join(root, "real")
	if err := os.Mkdir(realRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	aliasRoot := filepath.Join(root, "alias")
	if err := os.Symlink(realRoot, aliasRoot); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	databasePath := filepath.Join(realRoot, "not-created", "nested", "instance.db")
	first, err := Acquire(databasePath)
	if err != nil {
		t.Fatalf("acquire with missing parents: %v", err)
	}
	defer first.Close()
	if info, err := os.Stat(filepath.Dir(databasePath)); err != nil || !info.IsDir() {
		t.Fatalf("database parent was not created: info=%v err=%v", info, err)
	}

	aliasPath := filepath.Join(aliasRoot, "not-created", "nested", "instance.db")
	second, err := Acquire(aliasPath)
	if second != nil {
		_ = second.Close()
	}
	if !errors.Is(err, ErrAlreadyOwned) {
		t.Fatalf("canonical alias acquired a second lock: %v", err)
	}
}

func runLockHelper(t *testing.T) {
	lock, err := Acquire(os.Getenv(helperDBPathEnv))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	fmt.Fprintln(os.Stdout, "locked")
	_, err = io.Copy(io.Discard, os.Stdin)
	if err != nil {
		t.Fatal(err)
	}
}
