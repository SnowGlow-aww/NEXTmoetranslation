package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEventAssociationsHandlerRequiresGET(t *testing.T) {
	server := &Server{}
	request := httptest.NewRequest(http.MethodPost, "/api/event-associations", nil)
	recorder := httptest.NewRecorder()

	server.handleEventAssociations(recorder, request)

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, body=%q", recorder.Code, recorder.Body.String())
	}
}

func TestEventAssociationsHandlerReturnsClosedEmptyShapeWithoutTranslator(t *testing.T) {
	server := &Server{}
	request := httptest.NewRequest(http.MethodGet, "/api/event-associations", nil)
	recorder := httptest.NewRecorder()

	server.handleEventAssociations(recorder, request)

	if recorder.Code != http.StatusOK || recorder.Body.String() != "{\"categories\":{}}\n" {
		t.Fatalf("GET status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}
