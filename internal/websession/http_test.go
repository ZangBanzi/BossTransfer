package websession

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bosstransfer/internal/localauth"
)

func TestLoginCookieAndCredentialChange(t *testing.T) {
	store, err := localauth.Open(filepath.Join(t.TempDir(), "auth.json"), localauth.Bootstrap{Username: "admin", Password: "password", MustChange: true}, localauth.Options{})
	if err != nil {
		t.Fatal(err)
	}
	auth := New(store, "bt_session", "/login")
	login := httptest.NewRequest(http.MethodPost, "http://nas/api/login", strings.NewReader(`{"username":"admin","password":"password"}`))
	login.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	auth.LoginHandler(w, login)
	if w.Code != http.StatusOK || len(w.Result().Cookies()) != 1 {
		t.Fatalf("login = %d %s", w.Code, w.Body.String())
	}
	cookie := w.Result().Cookies()[0]
	change := httptest.NewRequest(http.MethodPut, "http://nas/api/credentials", strings.NewReader(`{"current_password":"password","username":"owner","password":"new-password"}`))
	change.Header.Set("Content-Type", "application/json")
	change.AddCookie(cookie)
	cw := httptest.NewRecorder()
	auth.CredentialsHandler(cw, change)
	if cw.Code != http.StatusOK {
		t.Fatalf("change = %d %s", cw.Code, cw.Body.String())
	}
	if !store.VerifyCredentials("owner", "new-password") {
		t.Fatal("updated credentials rejected")
	}
	if _, err := store.ValidateSession(cookie.Value); err == nil {
		t.Fatal("old session survived credential change")
	}
}

func TestRequirePasswordChangedBlocksBootstrapAccount(t *testing.T) {
	store, err := localauth.Open(filepath.Join(t.TempDir(), "auth.json"), localauth.Bootstrap{Username: "admin", Password: "password", MustChange: true}, localauth.Options{})
	if err != nil {
		t.Fatal(err)
	}
	auth := New(store, "bt_session", "/login")
	session, err := store.Login("admin", "password")
	if err != nil {
		t.Fatal(err)
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

	page := httptest.NewRequest(http.MethodGet, "http://nas/", nil)
	page.AddCookie(&http.Cookie{Name: auth.CookieName, Value: session.Token})
	pageResponse := httptest.NewRecorder()
	auth.RequirePasswordChanged(next).ServeHTTP(pageResponse, page)
	if pageResponse.Code != http.StatusSeeOther || pageResponse.Header().Get("Location") != "/password" {
		t.Fatalf("page gate = %d location=%q", pageResponse.Code, pageResponse.Header().Get("Location"))
	}

	apiRequest := httptest.NewRequest(http.MethodGet, "http://nas/api/v1/resource", nil)
	apiRequest.AddCookie(&http.Cookie{Name: auth.CookieName, Value: session.Token})
	apiResponse := httptest.NewRecorder()
	auth.RequirePasswordChanged(next).ServeHTTP(apiResponse, apiRequest)
	if apiResponse.Code != http.StatusPreconditionRequired || !strings.Contains(apiResponse.Body.String(), "password_change_required") {
		t.Fatalf("API gate = %d body=%s", apiResponse.Code, apiResponse.Body.String())
	}
}

func TestLoginFailureLimiterHasBoundedCapacityAndPrunesExpiredEntries(t *testing.T) {
	current := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	auth := &Auth{failures: make(map[string]failure), now: func() time.Time { return current }}
	insertions := maxLoginFailureEntries + 128
	for index := 0; index < insertions; index++ {
		current = current.Add(time.Millisecond)
		auth.recordFailure(fmt.Sprintf("198.51.%d.%d", (index/256)%256, index%256))
		if len(auth.failures) > maxLoginFailureEntries {
			t.Fatalf("failure map grew to %d entries, limit is %d", len(auth.failures), maxLoginFailureEntries)
		}
	}
	if len(auth.failures) != maxLoginFailureEntries {
		t.Fatalf("failure map contains %d entries, want %d", len(auth.failures), maxLoginFailureEntries)
	}
	if _, exists := auth.failures["198.51.0.0"]; exists {
		t.Fatal("oldest failure entry was not evicted at capacity")
	}
	lastRemote := fmt.Sprintf("198.51.%d.%d", ((insertions-1)/256)%256, (insertions-1)%256)
	if _, exists := auth.failures[lastRemote]; !exists {
		t.Fatal("new failure entry was not retained at capacity")
	}

	current = current.Add(loginFailureWindow)
	if auth.rateLimited("203.0.113.10") {
		t.Fatal("unseen remote was unexpectedly rate limited")
	}
	if len(auth.failures) != 0 {
		t.Fatalf("expired failure entries were not pruned: %d remain", len(auth.failures))
	}
}

func TestLoginFailureLimiterBlocksThresholdAndExpiresWindow(t *testing.T) {
	current := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	auth := &Auth{failures: make(map[string]failure), now: func() time.Time { return current }}
	const remote = "192.0.2.25"
	for attempt := 0; attempt < maxLoginFailures; attempt++ {
		auth.recordFailure(remote)
	}
	if !auth.rateLimited(remote) {
		t.Fatalf("remote was not limited after %d failures", maxLoginFailures)
	}
	current = current.Add(loginFailureWindow)
	if auth.rateLimited(remote) {
		t.Fatal("remote remained limited after the failure window expired")
	}
	if len(auth.failures) != 0 {
		t.Fatalf("expired remote was not removed: %#v", auth.failures)
	}
}

func TestLoginFailureLimiterUsesDirectRemoteAddrOnly(t *testing.T) {
	current := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	auth := &Auth{failures: make(map[string]failure), now: func() time.Time { return current }}
	const directRemote = "203.0.113.7:45678"
	for attempt := 0; attempt < maxLoginFailures; attempt++ {
		request := httptest.NewRequest(http.MethodPost, "http://nas/api/login", nil)
		request.RemoteAddr = directRemote
		request.Header.Set("X-Forwarded-For", fmt.Sprintf("198.51.100.%d", attempt+1))
		request.Header.Set("X-Real-IP", fmt.Sprintf("192.0.2.%d", attempt+1))
		request.Header.Set("Forwarded", fmt.Sprintf("for=198.18.0.%d", attempt+1))
		auth.recordFailure(remoteIP(request))
	}
	if len(auth.failures) != 1 || !auth.rateLimited("203.0.113.7") {
		t.Fatalf("forwarded headers split the direct-IP failure bucket: %#v", auth.failures)
	}

	ipv6 := httptest.NewRequest(http.MethodPost, "http://nas/api/login", nil)
	ipv6.RemoteAddr = "[2001:db8::5]:44321"
	ipv6.Header.Set("X-Forwarded-For", "198.51.100.99")
	if got := remoteIP(ipv6); got != "2001:db8::5" {
		t.Fatalf("IPv6 direct remote = %q", got)
	}
}
