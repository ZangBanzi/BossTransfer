package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequireSetupAuth(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler := requireSetupAuth("admin", "a-long-test-password", next)

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/v1/client/setup", nil))
	if unauthorized.Code != http.StatusUnauthorized || unauthorized.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("unauthorized response = %d, challenge=%q", unauthorized.Code, unauthorized.Header().Get("WWW-Authenticate"))
	}

	authorizedRequest := httptest.NewRequest(http.MethodGet, "/api/v1/client/setup", nil)
	authorizedRequest.SetBasicAuth("admin", "a-long-test-password")
	authorized := httptest.NewRecorder()
	handler.ServeHTTP(authorized, authorizedRequest)
	if authorized.Code != http.StatusNoContent {
		t.Fatalf("authorized response = %d", authorized.Code)
	}
}

func TestRequireSafeMutation(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler := requireSafeMutation(next)

	plainRequest := httptest.NewRequest(http.MethodPost, "http://nas.local/api", strings.NewReader(`{}`))
	plainRequest.Header.Set("Content-Type", "text/plain")
	plain := httptest.NewRecorder()
	handler.ServeHTTP(plain, plainRequest)
	if plain.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("plain response = %d", plain.Code)
	}

	crossSiteRequest := httptest.NewRequest(http.MethodPost, "http://nas.local/api", strings.NewReader(`{}`))
	crossSiteRequest.Header.Set("Content-Type", "application/json")
	crossSiteRequest.Header.Set("Origin", "https://attacker.example")
	crossSite := httptest.NewRecorder()
	handler.ServeHTTP(crossSite, crossSiteRequest)
	if crossSite.Code != http.StatusForbidden {
		t.Fatalf("cross-site response = %d", crossSite.Code)
	}

	sameOriginRequest := httptest.NewRequest(http.MethodPost, "http://nas.local/api", strings.NewReader(`{}`))
	sameOriginRequest.Header.Set("Content-Type", "application/json; charset=utf-8")
	sameOriginRequest.Header.Set("Origin", "http://nas.local")
	sameOrigin := httptest.NewRecorder()
	handler.ServeHTTP(sameOrigin, sameOriginRequest)
	if sameOrigin.Code != http.StatusNoContent {
		t.Fatalf("same-origin response = %d", sameOrigin.Code)
	}
}
