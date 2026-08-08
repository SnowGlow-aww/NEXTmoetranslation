package main

import (
	"path/filepath"
	"strings"
	"testing"

	"moesekai/server/internal/lyricsextractionplan"
)

func TestReleasePlanAssemblerPinsReviewedNetworkTimeout(t *testing.T) {
	floors := lyricsextractionplan.CompiledSafetyFloors()
	ceilings := lyricsextractionplan.CompiledHardCeilings()
	if requiredRequestTimeoutMillis != 30_000 ||
		requiredRequestTimeoutMillis < floors.RequestTimeoutMillis ||
		requiredRequestTimeoutMillis > ceilings.RequestTimeoutMillis {
		t.Fatalf("reviewed request timeout=%d floors=%+v ceilings=%+v",
			requiredRequestTimeoutMillis, floors, ceilings)
	}
}

func TestExactMediaWikiRevisionAcceptsFixedRevisionResponseWithoutUnrequestedPageInfo(t *testing.T) {
	response := mediaWikiResponse{
		BatchComplete: true,
		Limits:        mediaWikiLimit{Categories: 500},
		Query: mediaWikiQuery{Pages: []mediaWikiPage{{
			PageID: 268, Namespace: 0, Title: "List of songs",
			Categories: []mediaWikiCategory{{Namespace: 14, Title: "Category:Lists"}},
			Revisions: []mediaWikiRevision{{
				RevisionID: 338123, ParentID: 337351, Timestamp: "2026-08-04T08:01:35Z",
				SHA1: "d025c2122cbcb86f96368d7ca109af8a4ffd3d69",
				Slots: mediaWikiSlots{Main: mediaWikiSlot{
					ContentModel: "wikitext", ContentFormat: "text/x-wiki", Content: "fixed content",
				}},
			}},
		}}},
	}
	revision, err := exactMediaWikiRevision(response, "Sekaipedia List")
	if err != nil || revision.PageID != 268 || revision.RevisionID != 338123 ||
		revision.Title != "List of songs" || revision.Timestamp != "2026-08-04T08:01:35Z" ||
		revision.SHA1 != "d025c2122cbcb86f96368d7ca109af8a4ffd3d69" ||
		revision.ContentSHA256 != sha256Hex([]byte("fixed content")) {
		t.Fatalf("fixed revision=%+v err=%v", revision, err)
	}
}

func TestReleasePlanAssemblerExactPublicURLIsCompleteAndNotICU(t *testing.T) {
	if !strings.HasPrefix(requiredExactPublicURL, "https://zh.moegirl.org.cn/") ||
		strings.Contains(requiredExactPublicURL, "moegirl.icu") || strings.Contains(requiredExactPublicURL, "api.php") {
		t.Fatalf("exact public URL is not the user-authorized complete page URL: %s", requiredExactPublicURL)
	}
}

func TestReleasePlanAssemblerAcceptsOnlyExplicitPrivateOutputBoundaries(t *testing.T) {
	sessionsRoot := t.TempDir()
	t.Setenv("MOESEKAI_SESSION_ROOT", filepath.Join(sessionsRoot, "active-run"))

	for _, allowed := range []string{
		filepath.Join(sessionsRoot, "recovery-v6", "run"),
		filepath.Join(sessionsRoot, "recovery-v6", "plan", "recovery-plan-698.json"),
		"/private/tmp/moesekai-external-runtime/recovery-v5/root.json",
	} {
		if !lyricsextractionplan.RecoveryPrivateOutputPathAllowed(allowed) {
			t.Fatalf("private recovery output path was rejected: %s", allowed)
		}
	}
	for _, rejected := range []string{
		sessionsRoot,
		filepath.Join(filepath.Dir(sessionsRoot), "outside", "root.json"),
		filepath.Join(sessionsRoot, "production", "root.json"),
		filepath.Join(sessionsRoot, "recovery-v6", "moesekai.db"),
	} {
		if lyricsextractionplan.RecoveryPrivateOutputPathAllowed(rejected) {
			t.Fatalf("path outside the closed private recovery boundary was accepted: %s", rejected)
		}
	}
}
