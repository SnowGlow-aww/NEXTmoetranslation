package backup

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"moesekai/server/internal/config"
)

func TestCollaborationRestoreHookRunsOnlyAfterCommittedEpochFence(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git executable unavailable")
	}
	source := setupLegacyBackup(t)
	remote := filepath.Join(t.TempDir(), "collaboration-restore.git")
	command := exec.Command("git", "init", "--bare", "--initial-branch=collaboration-restore", remote)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v: %s", err, output)
	}
	for key, value := range map[string]string{
		config.KeyBackupGitRepoURL: "file://" + remote,
		config.KeyBackupGitBranch:  "collaboration-restore",
	} {
		if err := source.cfg.Set(key, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := source.manager.backupGit(); err != nil {
		t.Fatal(err)
	}

	destination := setupLegacyBackup(t)
	for key, value := range map[string]string{
		config.KeyBackupGitRepoURL: "file://" + remote,
		config.KeyBackupGitBranch:  "missing-branch",
	} {
		if err := destination.cfg.Set(key, value); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := destination.database.Exec(`INSERT INTO lyrics_collab_documents
		(music_id,schema_version,epoch,update_v1,base_revision,authority_sha256,updated_at)
		VALUES (10,1,5,?,0,?,?)`, []byte{1}, strings.Repeat("a", 64), time.Now().UTC().Unix()); err != nil {
		t.Fatal(err)
	}

	callbackCount := 0
	var observedEpoch int64
	var callbackErr error
	destination.manager.SetAfterRestore(func() {
		callbackCount++
		callbackErr = destination.database.QueryRow(`SELECT epoch FROM lyrics_collab_documents WHERE music_id=10`).Scan(&observedEpoch)
	})
	if _, err := destination.manager.RestoreFromAs("git", "operator"); err == nil {
		t.Fatal("missing backup branch unexpectedly restored")
	}
	if callbackCount != 0 || observedEpoch != 0 || collaborationLedgerEpoch(t, destination) != 5 {
		t.Fatalf("failed restore callback=%d observed=%d stored=%d", callbackCount, observedEpoch, collaborationLedgerEpoch(t, destination))
	}

	if err := destination.cfg.Set(config.KeyBackupGitBranch, "collaboration-restore"); err != nil {
		t.Fatal(err)
	}
	if _, err := destination.manager.RestoreFromAs("git", "operator"); err != nil {
		t.Fatal(err)
	}
	if callbackErr != nil || callbackCount != 1 || observedEpoch != 6 {
		t.Fatalf("committed restore callback=%d epoch=%d err=%v", callbackCount, observedEpoch, callbackErr)
	}
}

func collaborationLedgerEpoch(t *testing.T, h *legacyBackupHarness) int64 {
	t.Helper()
	var epoch int64
	if err := h.database.QueryRow(`SELECT epoch FROM lyrics_collab_documents WHERE music_id=10`).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	return epoch
}
