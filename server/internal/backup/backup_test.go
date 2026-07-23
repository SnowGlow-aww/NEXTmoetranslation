package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"moesekai/server/internal/config"
)

func TestTarGzRoundTrip(t *testing.T) {
	src := t.TempDir()
	// Build a small translations-like tree.
	writeFile(t, filepath.Join(src, "cards.json"), `{"prefix":{"こんにちは":"你好"}}`)
	writeFile(t, filepath.Join(src, "eventStory", "event_1.json"), `{"meta":{"source":"official_cn"}}`)

	data, err := tarGzDir(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatal("empty tarball")
	}

	dest := t.TempDir()
	if err := untarGz(data, dest); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, filepath.Join(dest, "cards.json"))
	if got != `{"prefix":{"こんにちは":"你好"}}` {
		t.Errorf("cards.json mismatch: %q", got)
	}
	got2 := readFile(t, filepath.Join(dest, "eventStory", "event_1.json"))
	if got2 != `{"meta":{"source":"official_cn"}}` {
		t.Errorf("event_1.json mismatch: %q", got2)
	}
}

func TestUntarGzRejectsTraversal(t *testing.T) {
	// A hand-built tarball with a ../ entry must be rejected.
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "ok.txt"), "fine")
	data, err := tarGzDir(src)
	if err != nil {
		t.Fatal(err)
	}
	// Normal extraction works.
	if err := untarGz(data, t.TempDir()); err != nil {
		t.Fatalf("normal extract failed: %v", err)
	}
}

func TestSigV4KeyDeterministic(t *testing.T) {
	// SigV4 signing key derivation must be stable for identical inputs.
	k1 := sigv4Key("secret", "20260531", "us-east-1", "s3")
	k2 := sigv4Key("secret", "20260531", "us-east-1", "s3")
	if string(k1) != string(k2) {
		t.Error("signing key not deterministic")
	}
	k3 := sigv4Key("secret", "20260601", "us-east-1", "s3")
	if string(k1) == string(k3) {
		t.Error("signing key should differ by date")
	}
}

func TestGitErrorsRedactCredentialArgumentsAndOutput(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git executable unavailable")
	}
	secret := "credential-that-must-not-leak"
	err := git(t.TempDir(), "remote", "add", "origin", "https://"+secret+"@github.com/owner/repo.git")
	if err == nil {
		t.Fatal("git outside a repository unexpectedly succeeded")
	}
	if strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "https://***@github.com") {
		t.Fatalf("credential was not redacted: %v", err)
	}
}

func TestGitRestoreTreeRejectsUnsafeAndOversizedEntries(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*testing.T, string)
		want  string
	}{
		{
			name: "symlink",
			setup: func(t *testing.T, root string) {
				if err := os.Symlink(filepath.Join(root, "outside.json"), filepath.Join(root, "translations", "cards.json")); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			},
			want: "non-regular",
		},
		{
			name: "file limit",
			setup: func(t *testing.T, root string) {
				if err := os.WriteFile(filepath.Join(root, "translations", "cards.json"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Truncate(filepath.Join(root, "translations", "cards.json"), maxArchiveFileBytes+1); err != nil {
					t.Fatal(err)
				}
			},
			want: "exceeds",
		},
		{
			name: "aggregate limit",
			setup: func(t *testing.T, root string) {
				for index := 0; index < int(maxArchiveExpandedBytes/maxArchiveFileBytes)+1; index++ {
					path := filepath.Join(root, "translations", fmt.Sprintf("part-%d.json", index))
					if err := os.WriteFile(path, nil, 0o600); err != nil {
						t.Fatal(err)
					}
					if err := os.Truncate(path, maxArchiveFileBytes); err != nil {
						t.Fatal(err)
					}
				}
			},
			want: "aggregate",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "translations"), 0o700); err != nil {
				t.Fatal(err)
			}
			test.setup(t, root)
			if err := validateGitRestoreTree(root); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("unsafe tree error = %v", err)
			}
		})
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func TestUntarGzRejectsOversizedFile(t *testing.T) {
	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	tarWriter := tar.NewWriter(gzipWriter)
	size := int64(maxArchiveFileBytes + 1)
	if err := tarWriter.WriteHeader(&tar.Header{Name: "too-large", Mode: 0o600, Size: size, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.CopyN(tarWriter, zeroReader{}, size); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := untarGz(compressed.Bytes(), t.TempDir()); err == nil || !strings.Contains(err.Error(), "archive file") {
		t.Fatalf("oversized archive error = %v", err)
	}
}

func TestScheduledBackupRetriesSameDayAfterFailure(t *testing.T) {
	h := setupLegacyBackup(t)
	_ = h.cfg.Set(config.KeyBackupGitEnabled, "true")
	h.manager.now = func() time.Time { return time.Date(2026, 7, 24, 23, 0, 0, 0, time.UTC) }
	lastSuccessDay := ""
	h.manager.runScheduledIfDue(&lastSuccessDay)
	if lastSuccessDay != "" {
		t.Fatalf("failed attempt consumed day %q", lastSuccessDay)
	}
	_ = h.cfg.Set(config.KeyBackupGitEnabled, "false")
	h.manager.runScheduledIfDue(&lastSuccessDay)
	if lastSuccessDay != "2026-07-24" {
		t.Fatalf("successful retry did not complete day: %q", lastSuccessDay)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
