package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"moesekai/server/internal/auth"
	"moesekai/server/internal/config"
	"moesekai/server/internal/db"
	"moesekai/server/internal/model"
	"moesekai/server/internal/sse"
	"moesekai/server/internal/store"
	"moesekai/server/internal/translator"
	"moesekai/server/internal/workspaceverify"
)

func setup(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	database, err := db.Open(t.TempDir() + "/api.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	s := store.New(database)
	es := store.NewEventStore(database)
	a := auth.New(database, "test-secret-at-least-32-bytes-long", time.Hour)
	cfg, _ := config.New(database, "master-key")

	// Seed a user and some data.
	if _, err := a.CreateUser("alice", "strong-password-123", auth.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportCategory("cards", model.Category{
		"prefix": {"こんにちは": {Text: "你好", Source: model.SourceCN}},
	}); err != nil {
		t.Fatal(err)
	}
	es.ImportOrdered(1, model.EventStoryMeta{Source: "official_cn", Version: "1.0"},
		[]store.OrderedEpisode{{
			EpisodeNo: "1", Title: "标题", TalkKeys: []string{"おはよう"},
			TalkData: map[string]string{"おはよう": "早上好"},
		}})

	srv := NewServer(s, es, a, cfg, sse.NewHub(), translator.New(s, es, cfg), nil, nil)
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	// Log in to get a token.
	body, _ := json.Marshal(map[string]string{"username": "alice", "password": "strong-password-123"})
	resp, err := http.Post(ts.URL+"/api/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("login status %d", resp.StatusCode)
	}
	var lr struct {
		Token string `json:"token"`
		Role  string `json:"role"`
	}
	json.NewDecoder(resp.Body).Decode(&lr)
	if lr.Token == "" || lr.Role != auth.RoleAdmin {
		t.Fatalf("bad login response: %+v", lr)
	}
	return ts, lr.Token
}

func authGET(t *testing.T, ts *httptest.Server, token, path string) *http.Response {
	req, _ := http.NewRequest("GET", ts.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestUnauthorizedRejected(t *testing.T) {
	ts, _ := setup(t)
	resp, err := http.Get(ts.URL + "/api/categories")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestUnknownAPIPathReturnsJSON404(t *testing.T) {
	ts, _ := setup(t)
	response, err := http.Get(ts.URL + "/api/does-not-exist")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound || response.Header.Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("unknown API status=%d content-type=%q", response.StatusCode, response.Header.Get("Content-Type"))
	}
	var body map[string]string
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil || body["error"] != "not found" {
		t.Fatalf("unknown API body=%v err=%v", body, err)
	}
}

func TestJSONMutationBodiesRejectUnknownAndTrailingValues(t *testing.T) {
	h := setupLegacyAPI(t)
	for _, raw := range []string{
		`{"musicId":10,"revision":1,"clientId":"tab","unexpected":true}`,
		`{"musicId":10,"revision":1,"clientId":"tab"}{"second":true}`,
		`{"musicId":10,"musicId":11,"revision":1,"clientId":"tab"}`,
	} {
		request, err := http.NewRequest(http.MethodPost, h.server.URL+"/api/lyrics/publish", strings.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+h.token)
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("body %q status=%d, want 400", raw, response.StatusCode)
		}
	}
}

func TestJSONMutationBodiesRejectNestedDuplicateKeys(t *testing.T) {
	h := setupLegacyAPI(t)
	raw := `{"musicId":10,"revision":0,"status":"draft","attribution":"team","lines":[{"id":"line-1","order":0,"japanese":"歌","chinese":"","english":"","stanzaBreakBefore":false,"segments":[{"text":"歌","text":"改ざん","performerIds":[]}]}]}`
	request, err := http.NewRequest(http.MethodPut, h.server.URL+"/api/lyrics/save", strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+h.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("nested duplicate-key body status=%d, want 400", response.StatusCode)
	}
}

func TestSettingsUpdateRejectsInvalidDailyHourAndUnknownKeysAtomically(t *testing.T) {
	h := setupLegacyAPI(t)
	for _, patch := range []map[string]string{
		{config.KeyLLMType: "openai", config.KeyBackupDailyHour: "24"},
		{config.KeyBackupDailyHour: "01"},
		{config.KeyBackupDailyHour: "7", "backup.unrecognized": "value"},
	} {
		response := doJSON(t, http.MethodPut, h.server.URL+"/api/admin/settings", h.token, patch)
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("invalid settings patch %+v status = %d", patch, response.StatusCode)
		}
		if h.api.cfg.Get(config.KeyLLMType) != "" || h.api.cfg.Get(config.KeyBackupDailyHour) != "" {
			t.Fatalf("invalid patch persisted values: %+v", h.api.cfg.All(true))
		}
	}
	response := doJSON(t, http.MethodPut, h.server.URL+"/api/admin/settings", h.token,
		map[string]string{config.KeyBackupDailyHour: "23"})
	response.Body.Close()
	if response.StatusCode != http.StatusOK || h.api.cfg.Get(config.KeyBackupDailyHour) != "23" {
		t.Fatalf("valid daily hour status=%d value=%q", response.StatusCode, h.api.cfg.Get(config.KeyBackupDailyHour))
	}
}

func TestLoginRejectsInjectedUnknownRole(t *testing.T) {
	h := setupLegacyAPI(t)
	if _, err := h.api.auth.CreateUser("viewer", "strong-password-123", auth.RoleEditor); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`UPDATE users SET role='viewer' WHERE username='viewer'`); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]string{"username": "viewer", "password": "strong-password-123"})
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Post(h.server.URL+"/api/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("viewer login status = %d", response.StatusCode)
	}
}

func TestWorkspaceCapabilityContractIsMountedByTheServer(t *testing.T) {
	h := setupLegacyAPI(t)
	editor, err := h.api.auth.CreateUser("route-editor", "strong-password-123", auth.RoleEditor)
	if err != nil {
		t.Fatal(err)
	}
	currentEditorToken := func() string {
		t.Helper()
		currentEditor, err := h.api.auth.GetUser(editor.Username)
		if err != nil {
			t.Fatal(err)
		}
		token, _, err := h.api.auth.IssueToken(currentEditor)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	for _, route := range workspaceverify.RequiredRoutes() {
		request, err := http.NewRequest(route.Method, h.server.URL+route.Path, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("%s %s: %v", route.Method, route.Path, err)
		}
		response.Body.Close()
		if route.Authentication == "none" {
			if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusNotFound {
				t.Fatalf("public workspace capability policy mismatch: %s %s status=%d", route.Method, route.Path, response.StatusCode)
			}
		} else if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("workspace capability accepted missing %s auth: %s %s status=%d", route.Authentication, route.Method, route.Path, response.StatusCode)
		}

		if route.ProducerProof {
			token := currentEditorToken()
			if len(route.AllowedRoles) == 1 && route.AllowedRoles[0] == auth.RoleAdmin {
				token = h.token
			}
			withoutProof := doJSON(t, route.Method, h.server.URL+route.Path, token, nil)
			withoutProof.Body.Close()
			if withoutProof.StatusCode != http.StatusPreconditionRequired {
				t.Fatalf("workspace producer proof policy mismatch: %s %s status=%d", route.Method, route.Path, withoutProof.StatusCode)
			}
			continue
		}
		if len(route.AllowedRoles) == 1 && route.AllowedRoles[0] == auth.RoleAdmin {
			forbidden := doJSON(t, route.Method, h.server.URL+route.Path, currentEditorToken(), nil)
			forbidden.Body.Close()
			if forbidden.StatusCode != http.StatusForbidden {
				t.Fatalf("workspace admin policy mismatch: %s %s status=%d", route.Method, route.Path, forbidden.StatusCode)
			}
			continue
		}
		if route.Authentication == "bearer" && len(route.AllowedRoles) == 2 {
			allowed := doJSON(t, route.Method, h.server.URL+route.Path, currentEditorToken(), nil)
			allowed.Body.Close()
			if allowed.StatusCode == http.StatusUnauthorized || allowed.StatusCode == http.StatusForbidden || allowed.StatusCode == http.StatusNotFound {
				t.Fatalf("workspace editor policy mismatch: %s %s status=%d", route.Method, route.Path, allowed.StatusCode)
			}
		}
	}
}

func TestCategoriesAndEntries(t *testing.T) {
	ts, token := setup(t)

	resp := authGET(t, ts, token, "/api/categories")
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("categories status %d", resp.StatusCode)
	}
	var cats []model.CategoryInfo
	json.NewDecoder(resp.Body).Decode(&cats)
	if len(cats) != 1 || cats[0].Name != "cards" {
		t.Fatalf("unexpected categories: %+v", cats)
	}

	resp2 := authGET(t, ts, token, "/api/entries?category=cards&field=prefix")
	defer resp2.Body.Close()
	var entries []model.EntryWithKey
	json.NewDecoder(resp2.Body).Decode(&entries)
	if len(entries) != 1 || entries[0].Text != "你好" {
		t.Fatalf("unexpected entries: %+v", entries)
	}
}

func TestUpdateEntryRoundTrip(t *testing.T) {
	ts, token := setup(t)
	body, _ := json.Marshal(map[string]string{
		"category": "cards", "field": "prefix", "key": "こんにちは",
		"text": "你好呀", "source": "human",
	})
	req, _ := http.NewRequest("PUT", ts.URL+"/api/entry", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("update status %d", resp.StatusCode)
	}
	// Verify the change persisted.
	resp2 := authGET(t, ts, token, "/api/entries?category=cards&field=prefix&source=human")
	defer resp2.Body.Close()
	var entries []model.EntryWithKey
	json.NewDecoder(resp2.Body).Decode(&entries)
	if len(entries) != 1 || entries[0].Text != "你好呀" || entries[0].Source != "human" {
		t.Fatalf("update not persisted: %+v", entries)
	}
}

func TestEventStoryUpdate(t *testing.T) {
	ts, token := setup(t)
	body, _ := json.Marshal(map[string]any{
		"eventId": 1, "episodeNo": "1", "jpKey": "おはよう",
		"cnText": "早安", "source": "human", "entryType": "talk",
	})
	req, _ := http.NewRequest("PUT", ts.URL+"/api/event-story/update", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("event update status %d", resp.StatusCode)
	}
	resp2 := authGET(t, ts, token, "/api/event-story?eventId=1")
	defer resp2.Body.Close()
	var detail model.EventStoryDetail
	json.NewDecoder(resp2.Body).Decode(&detail)
	if detail.Episodes["1"].TalkData["おはよう"] != "早安" {
		t.Fatalf("event line not updated: %+v", detail.Episodes["1"])
	}
}

// setupEmpty builds a server with NO users seeded, for first-run setup tests.
func setupEmpty(t *testing.T) *httptest.Server {
	t.Helper()
	database, err := db.Open(t.TempDir() + "/empty.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	s := store.New(database)
	es := store.NewEventStore(database)
	a := auth.New(database, "test-secret-at-least-32-bytes-long", time.Hour)
	cfg, _ := config.New(database, "master-key")

	srv := NewServer(s, es, a, cfg, sse.NewHub(), translator.New(s, es, cfg), nil, nil)
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func TestSetupStatusAndFirstAdmin(t *testing.T) {
	ts := setupEmpty(t)

	// Fresh install: setup-status reports needsSetup=true.
	resp := mustGet(t, ts.URL+"/api/auth/setup-status")
	defer resp.Body.Close()
	var status struct {
		NeedsSetup bool `json:"needsSetup"`
	}
	json.NewDecoder(resp.Body).Decode(&status)
	if !status.NeedsSetup {
		t.Fatal("expected needsSetup=true on fresh install")
	}

	// Registering the first account creates an admin and returns a token.
	body, _ := json.Marshal(map[string]string{"username": "root", "password": "strong-password-123"})
	resp2, err := http.Post(ts.URL+"/api/auth/setup", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Fatalf("setup status %d", resp2.StatusCode)
	}
	var lr struct {
		Token string `json:"token"`
		Role  string `json:"role"`
	}
	json.NewDecoder(resp2.Body).Decode(&lr)
	if lr.Token == "" || lr.Role != auth.RoleAdmin {
		t.Fatalf("first account must be admin with a token: %+v", lr)
	}

	// Now setup-status flips to false and a second setup attempt is rejected.
	resp3 := mustGet(t, ts.URL+"/api/auth/setup-status")
	defer resp3.Body.Close()
	json.NewDecoder(resp3.Body).Decode(&status)
	if status.NeedsSetup {
		t.Fatal("expected needsSetup=false after first admin created")
	}

	body2, _ := json.Marshal(map[string]string{"username": "intruder", "password": "strong-password-456"})
	resp4, err := http.Post(ts.URL+"/api/auth/setup", "application/json", bytes.NewReader(body2))
	if err != nil {
		t.Fatal(err)
	}
	defer resp4.Body.Close()
	if resp4.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 on second setup, got %d", resp4.StatusCode)
	}
}

func mustGet(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s: status %d", url, resp.StatusCode)
	}
	return resp
}
