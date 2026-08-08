package api

import (
	"net/http"
	"time"

	"moesekai/server/internal/auth"
)

// RegisterRoutes mounts all console API routes on mux. Every route except login
// requires a valid JWT; admin-only routes (registered elsewhere) additionally
// require the admin role. Public, cacheable file serving is mounted separately.
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	// Auth
	mux.HandleFunc("/api/auth/setup-status", s.handleSetupStatus)
	mux.HandleFunc("/api/auth/setup", s.handleSetup)
	mux.HandleFunc("/api/auth/login", s.handleLogin)
	mux.HandleFunc("/api/auth/me", s.auth.RequireAuth(s.handleMe))
	mux.HandleFunc("/api/auth/refresh", s.auth.RequireAuth(s.handleRefresh))

	// Translations
	mux.HandleFunc("/api/editor-gate/status", s.auth.RequireAuth(s.handleEditorGateStatus))
	mux.HandleFunc("/api/categories", s.auth.RequireAuth(s.handleCategories))
	mux.HandleFunc("/api/entries", s.auth.RequireAuth(s.handleEntries))
	mux.HandleFunc("/api/entry", s.auth.RequireAuth(s.contentMutation(s.handleUpdateEntry)))
	mux.HandleFunc("/api/category/snapshot", s.auth.RequireAuth(s.handleCategorySnapshot))
	mux.HandleFunc("/api/category/batch", s.auth.RequireAuth(s.contentMutation(s.handleCategoryBatch)))
	mux.HandleFunc("/api/projection/status", s.auth.RequireAuth(s.handleProjectionStatus))
	mux.HandleFunc("/api/search/status", s.auth.RequireAuth(s.handleSearchStatus))

	// Stable masterdata catalog and manual lyrics workflow.
	mux.HandleFunc("/api/catalog/music", s.auth.RequireAuth(s.handleCatalogMusic))
	mux.HandleFunc("/api/catalog/characters", s.auth.RequireAuth(s.handleCatalogCharacters))
	mux.HandleFunc("/api/lyrics", s.auth.RequireAuth(s.handleLyricsList))
	mux.HandleFunc("/api/lyrics/detail", s.auth.RequireAuth(s.handleLyricsDetail))
	mux.HandleFunc("/api/lyrics/save", s.auth.RequireAuth(s.contentMutation(s.handleLyricsSave)))
	mux.HandleFunc("/api/lyrics/publish", s.auth.RequireAdmin(s.contentMutation(s.handleLyricsPublish)))
	mux.HandleFunc("/api/lyrics/unpublish", s.auth.RequireAdmin(s.contentMutation(s.handleLyricsUnpublish)))
	mux.HandleFunc("/api/lyrics/source/search", s.auth.RequireAdmin(s.handleLyricsSourceSearch))
	mux.HandleFunc("/api/lyrics/source/preview", s.auth.RequireAdmin(s.handleLyricsSourcePreview))

	// Event stories
	mux.HandleFunc("/api/event-stories", s.auth.RequireAuth(s.handleEventStories))
	mux.HandleFunc("/api/event-story", s.auth.RequireAuth(s.handleEventStory))
	mux.HandleFunc("/api/event-story/episode-snapshot", s.auth.RequireAuth(s.handleEventEpisodeSnapshot))
	mux.HandleFunc("/api/event-story/update", s.auth.RequireAuth(s.contentMutation(s.handleUpdateEventStory)))
	mux.HandleFunc("/api/event-story/promote-human", s.auth.RequireAdmin(s.contentMutation(s.handlePromoteEventStoryHuman)))
	mux.HandleFunc("/api/event-story/retry", s.auth.RequireAdmin(s.handleRetryEventStory))
	mux.HandleFunc("/api/event-story/reorder", s.auth.RequireAdmin(s.handleReorderEventStory))

	// Translation engine
	mux.HandleFunc("/api/translate/status", s.auth.RequireAuth(s.handleTranslateStatus))
	mux.HandleFunc("/api/translate/cn-sync", s.auth.RequireAdmin(s.handleCNSync))
	mux.HandleFunc("/api/translate/ai", s.auth.RequireAdmin(s.handleTranslateAI))
	mux.HandleFunc("/api/translate/ai-all", s.auth.RequireAdmin(s.handleTranslateAIAll))
	mux.HandleFunc("/api/translate/ai-story", s.auth.RequireAdmin(s.handleTranslateAIStory))

	// Read-only upstream status for any authenticated user (user settings page).
	mux.HandleFunc("/api/upstream/status", s.auth.RequireAuth(s.handleUpstreamStatus))

	// Admin (admin role required)
	mux.HandleFunc("/api/admin/users", s.auth.RequireAdmin(s.handleUsersRouter))
	mux.HandleFunc("/api/admin/settings", s.auth.RequireAdmin(s.handleSettingsRouter))
	mux.HandleFunc("/api/admin/upstream", s.auth.RequireAdmin(s.handleUpstreamStatus))
	mux.HandleFunc("/api/admin/upstream/check", s.auth.RequireAdmin(s.handleUpstreamCheck))
	mux.HandleFunc("/api/admin/lyrics-source-reviews", s.auth.RequireAdmin(s.handleLyricsSourceReviews))
	mux.HandleFunc("/api/admin/lyrics-source-reviews/detail", s.auth.RequireAdmin(s.handleLyricsSourceReviewDetail))
	mux.HandleFunc("/api/admin/lyrics-source-reviews/decision", s.auth.RequireAdmin(s.handleLyricsSourceReviewDecision))
	mux.HandleFunc("/api/admin/lyrics-source-reviews/candidate-selection", s.auth.RequireAdmin(s.handleLyricsSourceCandidateSelection))
	mux.HandleFunc("/api/admin/lyrics-source-reviews/import", s.auth.RequireAdmin(s.contentMutation(s.handleLyricsSourceReviewImport)))

	// Backup status is readable by editors; push and restore are admin operations.
	mux.HandleFunc("/api/backup/status", s.auth.RequireAuth(s.handleBackupStatus))
	mux.HandleFunc("/api/backup/push", s.auth.RequireAdmin(s.editorMutation(s.handleBackupPush)))
	mux.HandleFunc("/api/backup/restore", s.auth.RequireAdmin(s.handleBackupRestore))

	// Strict single-instance aliases used by the producer-aware editor. The
	// canonical routes above remain compatible for existing clients.
	mux.HandleFunc("/api/editor/v1/entry", s.auth.RequireAuth(s.strictContentMutation(s.handleUpdateEntry)))
	mux.HandleFunc("/api/editor/v1/category/batch", s.auth.RequireAuth(s.strictContentMutation(s.handleCategoryBatch)))
	mux.HandleFunc("/api/editor/v1/event-story/update", s.auth.RequireAuth(s.strictContentMutation(s.handleUpdateEventStory)))
	mux.HandleFunc("/api/editor/v1/event-story/promote-human", s.auth.RequireAdmin(s.strictContentMutation(s.handlePromoteEventStoryHuman)))
	mux.HandleFunc("/api/editor/v1/lyrics/save", s.auth.RequireAuth(s.strictContentMutation(s.handleLyricsSave)))
	mux.HandleFunc("/api/editor/v1/lyrics/publish", s.auth.RequireAdmin(s.strictContentMutation(s.handleLyricsPublish)))
	mux.HandleFunc("/api/editor/v1/lyrics/unpublish", s.auth.RequireAdmin(s.strictContentMutation(s.handleLyricsUnpublish)))
	mux.HandleFunc("/api/editor/v1/backup/push", s.auth.RequireAdmin(s.strictEditorMutation(s.handleBackupPush)))

	// Realtime: SSE stream authenticated with the normal session bearer JWT.
	if s.hub != nil {
		mux.HandleFunc("/sse", s.auth.RequireAuth(s.hub.Handler(currentUser, func(r *http.Request) bool {
			_, err := s.auth.VerifyToken(auth.BearerTokenFromRequest(r))
			return err == nil
		}, func(r *http.Request) time.Time {
			claims, _ := auth.FromContext(r.Context())
			if claims == nil || claims.ExpiresAt == nil {
				return time.Time{}
			}
			return claims.ExpiresAt.Time
		})))
	}

	// Keep unknown API paths inside the JSON API contract instead of falling
	// through to the static SPA catch-all.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeErr(w, http.StatusNotFound, "not found")
	})
}
