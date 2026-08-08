package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const contentBackupPackageFixtureRoot = "../lyricssource/testdata"

func mustContentBackupPackageFixture(t *testing.T, name string) []byte {
	t.Helper()
	path, err := contentBackupPackageFixturePath(name)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("content backup fixture is not a direct regular file: %s", path)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func contentBackupPackageFixturePath(name string) (string, error) {
	if name == "" || filepath.Base(name) != name || name == "." || name == ".." {
		return "", errorsForContentBackupFixtureName(name)
	}
	root, err := filepath.Abs(contentBackupPackageFixtureRoot)
	if err != nil {
		return "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	path := filepath.Join(root, name)
	relative, err := filepath.Rel(root, path)
	if err != nil || relative != name || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("content backup fixture path escapes package fixture root: %q", name)
	}
	return path, nil
}

func errorsForContentBackupFixtureName(name string) error {
	return fmt.Errorf("content backup fixture name is invalid: %q", name)
}

func TestContentBackupFixtureResolutionIsPackageLocalAndTrimpathStable(t *testing.T) {
	path, err := contentBackupPackageFixturePath("sekaipedia-list-335193.json")
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		t.Fatalf("content backup fixture path is not canonical absolute: %s", path)
	}
	body := mustContentBackupPackageFixture(t, "sekaipedia-list-335193.json")
	if len(body) == 0 {
		t.Fatal("content backup package fixture is empty")
	}
	for _, invalid := range []string{"", ".", "..", "../sekaipedia-list-335193.json", "nested/fixture.json"} {
		if _, err := contentBackupPackageFixturePath(invalid); err == nil {
			t.Fatalf("unsafe content backup fixture name was accepted: %q", invalid)
		}
	}
}
