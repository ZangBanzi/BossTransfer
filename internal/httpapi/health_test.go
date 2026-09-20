package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLiveHandler(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health/live", nil)
	rec := httptest.NewRecorder()

	Method(http.MethodGet, LiveHandler())(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("status body = %#v", body["status"])
	}
}

func TestReadyHandlerDegraded(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health/ready", nil)
	rec := httptest.NewRecorder()

	ReadyHandler(func(*http.Request) []Check {
		return []Check{
			{Name: "process", Status: "ok"},
			{Name: "database", Status: "warn"},
		}
	})(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body ReadyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Status != "degraded" {
		t.Fatalf("ready status = %q, want degraded", body.Status)
	}
}

func TestReadyHandlerNotReady(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health/ready", nil)
	rec := httptest.NewRecorder()

	ReadyHandler(func(*http.Request) []Check {
		return []Check{{Name: "database", Status: "fail"}}
	})(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestMethodRejectsWrongVerb(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/health/live", nil)
	rec := httptest.NewRecorder()

	Method(http.MethodGet, LiveHandler())(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	if rec.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("allow = %q, want GET", rec.Header().Get("Allow"))
	}
}
