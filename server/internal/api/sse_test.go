package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestSSEBroadcastOnEdit connects an SSE client, performs an entry edit through
// the API, and asserts the client receives an entry.updated event.
func TestSSEBroadcastOnEdit(t *testing.T) {
	ts, token := setup(t)

	// Open the SSE stream with the token as a query param (EventSource style).
	req, _ := http.NewRequest("GET", ts.URL+"/sse?token="+token, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("sse connect status %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("sse Content-Type = %q", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("sse Cache-Control = %q", got)
	}
	if got := resp.Header.Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("sse X-Accel-Buffering = %q", got)
	}

	// Read events in a goroutine until we see entry.updated or time out.
	got := make(chan map[string]any, 1)
	go func() {
		reader := bufio.NewReader(resp.Body)
		var event string
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			switch {
			case strings.HasPrefix(line, "event: "):
				event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				if event == "entry.updated" {
					var data map[string]any
					_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &data)
					got <- data
					return
				}
			}
		}
	}()

	// Give the client a moment to register before broadcasting.
	time.Sleep(100 * time.Millisecond)

	body, _ := json.Marshal(map[string]string{
		"category": "cards", "field": "prefix", "key": "こんにちは",
		"text": "你好（已校对）", "source": "human",
	})
	editReq, _ := http.NewRequest("PUT", ts.URL+"/api/entry", bytes.NewReader(body))
	editReq.Header.Set("Authorization", "Bearer "+token)
	editResp, err := http.DefaultClient.Do(editReq)
	if err != nil {
		t.Fatal(err)
	}
	editResp.Body.Close()

	select {
	case data := <-got:
		if data["key"] != "こんにちは" || data["text"] != "你好（已校对）" {
			t.Errorf("unexpected event payload: %+v", data)
		}
		if data["user"] != "alice" {
			t.Errorf("expected user alice, got %v", data["user"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for entry.updated SSE event")
	}
}

// TestSSERequiresAuth verifies the stream rejects unauthenticated clients.
func TestSSERequiresAuth(t *testing.T) {
	ts, _ := setup(t)
	resp, err := http.Get(ts.URL + "/sse")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestLegacySSENoopDoesNotBroadcast(t *testing.T) {
	ts, token := setup(t)
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/sse?token="+token, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	events := make(chan string, 1)
	go func() {
		reader := bufio.NewReader(resp.Body)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(line, "event: ") {
				events <- strings.TrimSpace(strings.TrimPrefix(line, "event: "))
				return
			}
		}
	}()

	noopBody, _ := json.Marshal(map[string]string{
		"category": "cards", "field": "prefix", "key": "こんにちは",
		"text": "你好", "source": "cn",
	})
	editReq, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/entry", bytes.NewReader(noopBody))
	editReq.Header.Set("Authorization", "Bearer "+token)
	editResp, err := http.DefaultClient.Do(editReq)
	if err != nil {
		t.Fatal(err)
	}
	defer editResp.Body.Close()
	var result map[string]string
	if err := json.NewDecoder(editResp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result["status"] != "noop" {
		t.Fatalf("status = %q", result["status"])
	}

	select {
	case event := <-events:
		t.Fatalf("noop broadcast unexpected SSE event %q", event)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestLegacySSEEventStoryUpdatePayload(t *testing.T) {
	ts, token := setup(t)
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/sse?token="+token, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	got := make(chan map[string]any, 1)
	go func() {
		reader := bufio.NewReader(resp.Body)
		var event string
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "event: ") {
				event = strings.TrimPrefix(line, "event: ")
			}
			if event == "eventstory.updated" && strings.HasPrefix(line, "data: ") {
				var data map[string]any
				_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &data)
				got <- data
				return
			}
		}
	}()

	body, _ := json.Marshal(map[string]any{
		"eventId": 1, "episodeNo": "1", "jpKey": "おはよう",
		"cnText": "早安", "source": "human", "entryType": "talk",
	})
	editReq, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/event-story/update", bytes.NewReader(body))
	editReq.Header.Set("Authorization", "Bearer "+token)
	editResp, err := http.DefaultClient.Do(editReq)
	if err != nil {
		t.Fatal(err)
	}
	editResp.Body.Close()

	select {
	case data := <-got:
		if data["eventId"] != float64(1) || data["episodeNo"] != "1" || data["jpKey"] != "おはよう" ||
			data["cnText"] != "早安" || data["source"] != "human" || data["entryType"] != "talk" || data["user"] != "alice" {
			t.Fatalf("eventstory.updated payload = %#v", data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for eventstory.updated SSE event")
	}
}
