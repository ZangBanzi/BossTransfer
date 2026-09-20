package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"bosstransfer/internal/localauth"
	"bosstransfer/internal/websession"
)

func TestManagerFirstLoginPasswordChangeFlow(t *testing.T) {
	store, err := localauth.Open(filepath.Join(t.TempDir(), "auth.json"), localauth.Bootstrap{
		Username: "admin", Password: "password", MustChange: true,
	}, localauth.Options{})
	if err != nil {
		t.Fatal(err)
	}
	auth := websession.New(store, "manager_session", "/login")

	login := httptest.NewRequest(http.MethodPost, "http://nas/api/v1/manager/auth/login", strings.NewReader(`{"username":"admin","password":"password"}`))
	login.Header.Set("Content-Type", "application/json")
	loginResponse := httptest.NewRecorder()
	requireSafeMutation(http.HandlerFunc(auth.LoginHandler)).ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("login = %d %s", loginResponse.Code, loginResponse.Body.String())
	}
	var loginBody struct {
		MustChange bool `json:"must_change"`
	}
	if err := json.Unmarshal(loginResponse.Body.Bytes(), &loginBody); err != nil || !loginBody.MustChange {
		t.Fatalf("login must_change = %#v, err = %v", loginBody, err)
	}
	cookie := loginResponse.Result().Cookies()[0]

	loginPage := httptest.NewRequest(http.MethodGet, "http://nas/login", nil)
	loginPage.AddCookie(cookie)
	loginPageResponse := httptest.NewRecorder()
	managerLoginPageHandler(auth).ServeHTTP(loginPageResponse, loginPage)
	if loginPageResponse.Code != http.StatusSeeOther || loginPageResponse.Header().Get("Location") != "/password" {
		t.Fatalf("login page = %d location=%q", loginPageResponse.Code, loginPageResponse.Header().Get("Location"))
	}

	passwordPage := httptest.NewRequest(http.MethodGet, "http://nas/password", nil)
	passwordPage.AddCookie(cookie)
	passwordPageResponse := httptest.NewRecorder()
	auth.Require(http.HandlerFunc(managerPasswordPageHandler(auth))).ServeHTTP(passwordPageResponse, passwordPage)
	pageHTML := passwordPageResponse.Body.String()
	if passwordPageResponse.Code != http.StatusOK ||
		!strings.Contains(pageHTML, `id="current"`) ||
		!strings.Contains(pageHTML, `id="password"`) ||
		!strings.Contains(pageHTML, "/api/v1/manager/auth/credentials") ||
		!strings.Contains(pageHTML, "location.href='/login'") {
		t.Fatalf("password page = %d body=%s", passwordPageResponse.Code, pageHTML)
	}

	assertManagerPasswordGate(t, auth, cookie, "http://nas/", http.StatusSeeOther)
	assertManagerPasswordGate(t, auth, cookie, "http://nas/api/v1/manager/licenses", http.StatusPreconditionRequired)

	change := httptest.NewRequest(http.MethodPut, "http://nas/api/v1/manager/auth/credentials", strings.NewReader(`{"current_password":"password","username":"owner","password":"manager-password"}`))
	change.Header.Set("Content-Type", "application/json")
	change.AddCookie(cookie)
	changeResponse := httptest.NewRecorder()
	auth.Require(requireSafeMutation(http.HandlerFunc(auth.CredentialsHandler))).ServeHTTP(changeResponse, change)
	if changeResponse.Code != http.StatusOK {
		t.Fatalf("credential change = %d %s", changeResponse.Code, changeResponse.Body.String())
	}

	relogin := httptest.NewRequest(http.MethodPost, "http://nas/api/v1/manager/auth/login", strings.NewReader(`{"username":"owner","password":"manager-password"}`))
	relogin.Header.Set("Content-Type", "application/json")
	reloginResponse := httptest.NewRecorder()
	requireSafeMutation(http.HandlerFunc(auth.LoginHandler)).ServeHTTP(reloginResponse, relogin)
	if reloginResponse.Code != http.StatusOK {
		t.Fatalf("relogin = %d %s", reloginResponse.Code, reloginResponse.Body.String())
	}
	var reloginBody struct {
		MustChange bool `json:"must_change"`
	}
	if err := json.Unmarshal(reloginResponse.Body.Bytes(), &reloginBody); err != nil || reloginBody.MustChange {
		t.Fatalf("relogin must_change = %#v, err = %v", reloginBody, err)
	}
	changedCookie := reloginResponse.Result().Cookies()[0]
	assertManagerPasswordGate(t, auth, changedCookie, "http://nas/", http.StatusNoContent)
	assertManagerPasswordGate(t, auth, changedCookie, "http://nas/api/v1/manager/licenses", http.StatusNoContent)
}

func assertManagerPasswordGate(t *testing.T, auth *websession.Auth, cookie *http.Cookie, target string, want int) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	auth.RequirePasswordChanged(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(response, request)
	if response.Code != want {
		t.Fatalf("password gate %s = %d body=%s, want %d", target, response.Code, response.Body.String(), want)
	}
	if want == http.StatusSeeOther && response.Header().Get("Location") != "/password" {
		t.Fatalf("password gate %s location=%q", target, response.Header().Get("Location"))
	}
	if want == http.StatusPreconditionRequired && !strings.Contains(response.Body.String(), `"error":"password_change_required"`) {
		t.Fatalf("password gate %s body=%s", target, response.Body.String())
	}
}
