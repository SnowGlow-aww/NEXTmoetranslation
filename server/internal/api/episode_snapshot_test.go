package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"moesekai/server/internal/auth"
	"moesekai/server/internal/model"
	"moesekai/server/internal/store"
)

func seedEpisodeScenario(t *testing.T, h *legacyAPIHarness) {
	t.Helper()
	canonical, digest, err := store.CanonicalizeEventScenario(map[string]any{
		"ScenarioId": "scenario-1",
		"Snippets": []any{
			map[string]any{"Action": float64(1), "ReferenceIndex": float64(0)},
			map[string]any{"Action": float64(6), "ReferenceIndex": float64(0)},
		},
		"TalkData": []any{map[string]any{
			"WindowDisplayName": "角色_制服", "Body": "二", "WhenFinishCloseWindow": float64(0), "Voices": []any{},
		}},
		"SpecialEffectData": []any{map[string]any{"EffectType": float64(23), "StringVal": "选项文本"}},
		"AppearCharacters":  []any{},
	}, "scenario-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.events.BackfillScenarios(42, []store.OrderedEpisode{{
		EpisodeNo: "1", ScenarioID: "scenario-1", ScenarioCanonicalJSON: canonical, ScenarioSHA256: digest,
	}}); err != nil {
		t.Fatal(err)
	}
}

func TestEpisodeSnapshotRequiresAuthAndExplicitLocaleAndAllowsEditors(t *testing.T) {
	h := setupLegacyAPI(t)
	seedEpisodeScenario(t, h)
	path := "/api/event-story/episode-snapshot?eventId=42&episodeNo=1&locale=en-US"
	unauthorized, err := http.Get(h.server.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.StatusCode)
	}
	missingLocale := authorizedRequest(t, h, http.MethodGet, "/api/event-story/episode-snapshot?eventId=42&episodeNo=1", nil)
	missingLocale.Body.Close()
	if missingLocale.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing locale status = %d", missingLocale.StatusCode)
	}
	if _, err := h.api.auth.CreateUser("episode-editor", "strong-editor-password", auth.RoleEditor); err != nil {
		t.Fatal(err)
	}
	login := doJSON(t, http.MethodPost, h.server.URL+"/api/auth/login", "", map[string]string{
		"username": "episode-editor", "password": "strong-editor-password",
	})
	defer login.Body.Close()
	var session struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(login.Body).Decode(&session); err != nil || session.Token == "" {
		t.Fatalf("editor login=%+v err=%v", session, err)
	}
	request, _ := http.NewRequest(http.MethodGet, h.server.URL+path, nil)
	request.Header.Set("Authorization", "Bearer "+session.Token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("editor snapshot status=%d body=%s", response.StatusCode, body)
	}
	var snapshot store.EventEpisodeSnapshot
	if err := json.NewDecoder(response.Body).Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.EventID != 42 || snapshot.EpisodeNo != "1" || snapshot.Locale != model.LocaleEnglish || snapshot.Revision == "" ||
		snapshot.Scenario.ScenarioID != "scenario-1" || snapshot.Scenario.ParserVersion != store.EventScenarioParserVersion ||
		len(snapshot.Scenario.SourceTalks) != 2 || snapshot.Scenario.SourceTalks[0].TalkDataIndex == nil ||
		*snapshot.Scenario.SourceTalks[0].TalkDataIndex != 0 || len(snapshot.Segments) != 3 {
		t.Fatalf("episode snapshot = %+v", snapshot)
	}
}

func TestEpisodeSnapshotFailsClosedWithoutRawJSONOnMismatchOrCorruption(t *testing.T) {
	h := setupLegacyAPI(t)
	seedEpisodeScenario(t, h)
	path := "/api/event-story/episode-snapshot?eventId=42&episodeNo=1&locale=zh-CN"
	if _, err := h.db.Exec(`UPDATE event_story_episodes SET scenario_id='mismatch' WHERE event_id=42 AND episode_no='1'`); err != nil {
		t.Fatal(err)
	}
	conflict := authorizedRequest(t, h, http.MethodGet, path, nil)
	conflictBody, _ := io.ReadAll(conflict.Body)
	conflict.Body.Close()
	if conflict.StatusCode != http.StatusConflict || !strings.Contains(string(conflictBody), `"error":"scenario_conflict"`) || strings.Contains(string(conflictBody), "rawJson") {
		t.Fatalf("scenario conflict status=%d body=%s", conflict.StatusCode, conflictBody)
	}
	if _, err := h.db.Exec(`UPDATE event_story_episodes SET scenario_id='scenario-1' WHERE event_id=42 AND episode_no='1'`); err != nil {
		t.Fatal(err)
	}
	badJSON := `{}`
	badSHA := sha256.Sum256([]byte(badJSON))
	if _, err := h.db.Exec(`UPDATE event_story_scenarios SET canonical_json=?, sha256=? WHERE event_id=42 AND episode_no='1'`,
		badJSON, hex.EncodeToString(badSHA[:])); err != nil {
		t.Fatal(err)
	}
	invalid := authorizedRequest(t, h, http.MethodGet, path, nil)
	invalidBody, _ := io.ReadAll(invalid.Body)
	invalid.Body.Close()
	if invalid.StatusCode != http.StatusInternalServerError || !strings.Contains(string(invalidBody), `"error":"scenario_invalid"`) || strings.Contains(string(invalidBody), "rawJson") {
		t.Fatalf("scenario invalid status=%d body=%s", invalid.StatusCode, invalidBody)
	}
}
