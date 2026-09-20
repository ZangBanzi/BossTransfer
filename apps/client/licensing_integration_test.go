package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bosstransfer/internal/licenseclient"
)

func TestRequireActiveLicenseRejectsUnactivatedAndRevokedClients(t *testing.T) {
	store, err := licenseclient.Open(filepath.Join(t.TempDir(), "license.json"), "http://manager.example")
	if err != nil {
		t.Fatal(err)
	}
	service := licenseclient.NewService(store, nil)

	assertLicenseMiddlewareDenied(t, service, "before activation")

	now := time.Now().UTC()
	if err := store.SaveActivation(
		"http://manager.example",
		"device-1",
		"device-token",
		licenseclient.License{ID: "license-1", Customer: "已授权客户", Status: "active"},
		now.Add(time.Hour),
		now,
	); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range licensedClientEndpoints() {
		downstreamCalled := false
		protected := requireActiveLicense(service, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			downstreamCalled = true
			w.WriteHeader(http.StatusNoContent)
		}))
		allowedResponse := httptest.NewRecorder()
		protected.ServeHTTP(allowedResponse, httptest.NewRequest(endpoint.method, endpoint.path, nil))
		if allowedResponse.Code != http.StatusNoContent || !downstreamCalled {
			t.Fatalf("active license %s %s = %d, downstream called = %t", endpoint.method, endpoint.path, allowedResponse.Code, downstreamCalled)
		}
	}

	if err := store.MarkRemoteStatus("revoked", "授权已被管理员撤销", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	assertLicenseMiddlewareDenied(t, service, "after revocation")
}

func assertLicenseMiddlewareDenied(t *testing.T, service *licenseclient.Service, stage string) {
	t.Helper()
	for _, endpoint := range licensedClientEndpoints() {
		downstreamCalled := false
		handler := requireActiveLicense(service, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			downstreamCalled = true
		}))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(endpoint.method, endpoint.path, nil))
		if response.Code != http.StatusForbidden || downstreamCalled || !strings.Contains(response.Body.String(), `"error":"license_required"`) {
			t.Fatalf("license middleware %s for %s %s = status %d, downstream called %t, body %s", stage, endpoint.method, endpoint.path, response.Code, downstreamCalled, response.Body.String())
		}
	}
	pageResponse := httptest.NewRecorder()
	requireLicensePage(service, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("unlicensed resource page reached downstream handler")
	})).ServeHTTP(pageResponse, httptest.NewRequest(http.MethodGet, "/", nil))
	if pageResponse.Code != http.StatusSeeOther || pageResponse.Header().Get("Location") != "/activate" {
		t.Fatalf("resource page %s = status %d, location %q", stage, pageResponse.Code, pageResponse.Header().Get("Location"))
	}
}

func licensedClientEndpoints() []struct{ method, path string } {
	return []struct{ method, path string }{
		{http.MethodGet, "/api/v1/client/search?q=movie"},
		{http.MethodGet, "/api/v1/client/downloaders"},
		{http.MethodGet, "/api/v1/client/downloads"},
		{http.MethodPost, "/api/v1/client/downloads"},
	}
}
