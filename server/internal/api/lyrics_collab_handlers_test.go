package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"moesekai/server/internal/collab"
	"moesekai/server/internal/editorgate"
	"moesekai/server/internal/store"
)

type lyricsCollabAPIHarness struct {
	legacy  *legacyAPIHarness
	server  *httptest.Server
	service *collab.Service
}

func setupLyricsCollabAPI(t *testing.T) *lyricsCollabAPIHarness {
	t.Helper()
	legacy := setupLegacyAPI(t)
	if err := legacy.store.UpsertMusicCatalog([]store.MusicCatalogRecord{{
		MusicID: 42, JapaneseTitle: "collaboration contract",
	}}); err != nil {
		t.Fatal(err)
	}
	service, err := collab.New(legacy.db, legacy.store, legacy.api.auth, legacy.api.editorGate)
	if err != nil {
		t.Fatal(err)
	}
	legacy.api.SetCollab(service)
	mux := http.NewServeMux()
	legacy.api.RegisterRoutes(mux)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := service.Shutdown(ctx); err != nil {
			t.Errorf("shutdown collaboration service: %v", err)
		}
	})
	return &lyricsCollabAPIHarness{legacy: legacy, server: server, service: service}
}

func collabTicketRequest(
	t *testing.T,
	h *lyricsCollabAPIHarness,
	proof *editorgate.Status,
	body any,
) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, h.server.URL+"/api/editor/v1/lyrics/42/collab-ticket", nil)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		request.Body = io.NopCloser(bytes.NewReader(encoded))
		request.ContentLength = int64(len(encoded))
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Authorization", "Bearer "+h.legacy.token)
	if proof != nil {
		request.Header.Set(loadedProducerStateHeader, loadedState(*proof))
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestLyricsCollabTicketHTTPProducerProofContract(t *testing.T) {
	h := setupLyricsCollabAPI(t)

	unauthorized, err := http.Post(h.server.URL+"/api/editor/v1/lyrics/42/collab-ticket", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized ticket status=%d", unauthorized.StatusCode)
	}

	missing := collabTicketRequest(t, h, nil, map[string]any{})
	missing.Body.Close()
	if missing.StatusCode != http.StatusPreconditionRequired {
		t.Fatalf("missing producer proof status=%d", missing.StatusCode)
	}

	stale := h.legacy.api.editorGate.Status()
	releaseProducer, err := h.legacy.api.editorGate.BeginProducer()
	if err != nil {
		t.Fatal(err)
	}
	releaseProducer()
	staleResponse := collabTicketRequest(t, h, &stale, map[string]any{})
	defer staleResponse.Body.Close()
	if staleResponse.StatusCode != http.StatusConflict {
		body, _ := io.ReadAll(staleResponse.Body)
		t.Fatalf("stale producer proof status=%d body=%s", staleResponse.StatusCode, body)
	}
	var current editorgate.Status
	if err := json.NewDecoder(staleResponse.Body).Decode(&current); err != nil {
		t.Fatal(err)
	}
	if current != h.legacy.api.editorGate.Status() {
		t.Fatalf("stale proof response=%+v current=%+v", current, h.legacy.api.editorGate.Status())
	}

	accepted := h.legacy.api.editorGate.Status()
	response := collabTicketRequest(t, h, &accepted, map[string]any{"musicId": 42})
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("ticket status=%d body=%s", response.StatusCode, body)
	}
	assertNoStoreJSONHeaders(t, response)
	var ticket map[string]any
	if err := json.NewDecoder(response.Body).Decode(&ticket); err != nil {
		t.Fatal(err)
	}
	if len(ticket) != 3 || ticket["room"] != "lyrics-42-e1" || ticket["ticket"] == "" {
		t.Fatalf("ticket response=%#v", ticket)
	}
	expiresAt, ok := ticket["expiresAt"].(string)
	if !ok {
		t.Fatalf("ticket expiresAt=%#v", ticket["expiresAt"])
	}
	parsedExpiry, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil || !parsedExpiry.After(time.Now()) {
		t.Fatalf("ticket expiry=%q err=%v", expiresAt, err)
	}
}

func TestLyricsCollabTicketHTTPBodyIsClosed(t *testing.T) {
	h := setupLyricsCollabAPI(t)
	proof := h.legacy.api.editorGate.Status()

	for _, body := range []any{nil, map[string]any{}, map[string]any{"musicId": 42}} {
		response := collabTicketRequest(t, h, &proof, body)
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("valid optional body %#v status=%d", body, response.StatusCode)
		}
	}

	mismatch := collabTicketRequest(t, h, &proof, map[string]any{"musicId": 43})
	mismatch.Body.Close()
	if mismatch.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("mismatched body musicId status=%d", mismatch.StatusCode)
	}

	unknown := collabTicketRequest(t, h, &proof, map[string]any{"unexpected": true})
	unknown.Body.Close()
	if unknown.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown ticket body field status=%d", unknown.StatusCode)
	}
}
