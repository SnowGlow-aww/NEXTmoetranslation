package api

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"moesekai/server/internal/backup"
	"moesekai/server/internal/files"
)

func installBackupManager(t *testing.T, h *legacyAPIHarness) *backup.Manager {
	t.Helper()
	manager := backup.NewManager(h.api.cfg, files.NewGenerator(h.store, h.events, ""), h.store, h.events,
		filepath.Join(t.TempDir(), "backup-work"))
	manager.SetEditorGate(h.api.editorGate)
	h.api.backup = manager
	return manager
}

func TestRestoreTargetValidationDoesNotAcquireProducerGate(t *testing.T) {
	h := setupLegacyAPI(t)
	installBackupManager(t, h)
	before := h.api.editorGate.Status()
	response := authorizedRequest(t, h, http.MethodPost, "/api/backup/restore", map[string]string{"target": "invalid"})
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid restore target status = %d", response.StatusCode)
	}
	if after := h.api.editorGate.Status(); after != before {
		t.Fatalf("invalid restore target changed gate: before=%+v after=%+v", before, after)
	}
}

func TestRestoreRequiresExactServerSideConfirmation(t *testing.T) {
	h := setupLegacyAPI(t)
	installBackupManager(t, h)
	for _, confirmation := range []string{"", "RESTORE", "RESTORE:git"} {
		response := authorizedRequest(t, h, http.MethodPost, "/api/backup/restore", map[string]string{
			"target": "s3", "confirmation": confirmation,
		})
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("confirmation %q status = %d", confirmation, response.StatusCode)
		}
	}
	if h.api.editorGate.Status().Generation != 0 {
		t.Fatalf("invalid confirmation acquired producer gate: %+v", h.api.editorGate.Status())
	}
}

func TestRestoreBusyConflictsReturnHTTP409(t *testing.T) {
	t.Run("producer gate", func(t *testing.T) {
		h := setupLegacyAPI(t)
		manager := installBackupManager(t, h)
		releaseProducer, err := h.api.editorGate.BeginProducer()
		if err != nil {
			t.Fatal(err)
		}
		response := authorizedRequest(t, h, http.MethodPost, "/api/backup/restore", map[string]string{"target": "s3", "confirmation": "RESTORE:s3"})
		response.Body.Close()
		releaseProducer()
		if response.StatusCode != http.StatusConflict {
			t.Fatalf("producer conflict status = %d", response.StatusCode)
		}
		if manager.Status().Running {
			t.Fatal("producer conflict left backup manager running")
		}
	})

	t.Run("backup manager", func(t *testing.T) {
		h := setupLegacyAPI(t)
		manager := installBackupManager(t, h)
		releaseEditor := h.api.editorGate.BeginEditor()
		firstDone := make(chan error, 1)
		go func() {
			_, err := manager.RestoreFromAs("s3", "operator")
			firstDone <- err
		}()
		deadline := time.Now().Add(time.Second)
		for !manager.Status().Running && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if !manager.Status().Running {
			releaseEditor()
			t.Fatal("first restore did not enter manager")
		}
		response := authorizedRequest(t, h, http.MethodPost, "/api/backup/restore", map[string]string{"target": "git", "confirmation": "RESTORE:git"})
		response.Body.Close()
		if response.StatusCode != http.StatusConflict {
			releaseEditor()
			t.Fatalf("manager conflict status = %d", response.StatusCode)
		}
		releaseEditor()
		select {
		case <-firstDone:
		case <-time.After(time.Second):
			t.Fatal("first restore did not finish")
		}
	})
}
