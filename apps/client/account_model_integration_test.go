package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"bosstransfer/internal/config"
	"bosstransfer/internal/licenseclient"
	"bosstransfer/internal/localauth"
	"bosstransfer/internal/websession"
)

func TestClientAccountBootstrapWebChangeAndEnvironmentRestart(t *testing.T) {
	t.Setenv("BOSSTRANSFER_CLIENT_ADDR", "")
	t.Setenv("BOSSTRANSFER_SETUP_USER", "")
	t.Setenv("BOSSTRANSFER_SETUP_PASSWORD", "")
	initialConfig, err := config.LoadClient()
	if err != nil {
		t.Fatal(err)
	}
	if initialConfig.SetupUser != "admin" || initialConfig.SetupPassword != "password" {
		t.Fatalf("fresh defaults = %q/%q", initialConfig.SetupUser, initialConfig.SetupPassword)
	}

	root := t.TempDir()
	authPath := filepath.Join(root, "client-auth.json")
	licensePath := filepath.Join(root, "client-license.json")
	account, err := localauth.Open(authPath, localauth.Bootstrap{
		Username: initialConfig.SetupUser, Password: initialConfig.SetupPassword, MustChange: true,
	}, localauth.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !account.VerifyCredentials("admin", "password") {
		t.Fatal("fresh client rejected admin/password")
	}
	licenseStore, err := licenseclient.Open(licensePath, "http://manager.example")
	if err != nil {
		t.Fatal(err)
	}
	licenseService := licenseclient.NewService(licenseStore, nil)
	if licenseService.Status().Activated {
		t.Fatal("local Web account unexpectedly activated the software license")
	}

	auth := websession.New(account, "client_session", "/login")
	login := httptest.NewRequest(http.MethodPost, "http://nas/api/v1/client/auth/login", strings.NewReader(`{"username":"admin","password":"password"}`))
	login.Header.Set("Content-Type", "application/json")
	loginResponse := httptest.NewRecorder()
	auth.LoginHandler(loginResponse, login)
	if loginResponse.Code != http.StatusOK || len(loginResponse.Result().Cookies()) != 1 {
		t.Fatalf("default Web login = %d, body = %s", loginResponse.Code, loginResponse.Body.String())
	}
	cookie := loginResponse.Result().Cookies()[0]
	preChangeResource := httptest.NewRequest(http.MethodGet, "http://nas/api/v1/client/search?q=movie", nil)
	preChangeResource.AddCookie(cookie)
	preChangeResponse := httptest.NewRecorder()
	auth.RequirePasswordChanged(requireActiveLicense(licenseService, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))).ServeHTTP(preChangeResponse, preChangeResource)
	if preChangeResponse.Code != http.StatusPreconditionRequired || !strings.Contains(preChangeResponse.Body.String(), `"error":"password_change_required"`) {
		t.Fatalf("default-password resource access = %d, body = %s", preChangeResponse.Code, preChangeResponse.Body.String())
	}

	change := httptest.NewRequest(http.MethodPut, "http://nas/api/v1/client/auth/credentials", strings.NewReader(`{"current_password":"password","username":"customer-admin","password":"customer-password"}`))
	change.Header.Set("Content-Type", "application/json")
	change.AddCookie(cookie)
	changeResponse := httptest.NewRecorder()
	auth.CredentialsHandler(changeResponse, change)
	if changeResponse.Code != http.StatusOK {
		t.Fatalf("Web credential change = %d, body = %s", changeResponse.Code, changeResponse.Body.String())
	}
	if !account.VerifyCredentials("customer-admin", "customer-password") || account.VerifyCredentials("admin", "password") {
		t.Fatal("Web credential change did not replace the default account")
	}
	if licenseService.Status().Activated {
		t.Fatal("changing local Web credentials changed central activation state")
	}

	// Simulate a container restart with different environment bootstrap values.
	// The existing persistent account must win; the new environment values are
	// used only for a genuinely new client data directory.
	t.Setenv("BOSSTRANSFER_SETUP_USER", "environment-user")
	t.Setenv("BOSSTRANSFER_SETUP_PASSWORD", "environment-password")
	restartConfig, err := config.LoadClient()
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := localauth.Open(authPath, localauth.Bootstrap{
		Username: restartConfig.SetupUser, Password: restartConfig.SetupPassword,
	}, localauth.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !restarted.VerifyCredentials("customer-admin", "customer-password") {
		t.Fatal("restart lost the Web credentials changed by the customer")
	}
	if restarted.VerifyCredentials("environment-user", "environment-password") {
		t.Fatal("restart environment overwrote the persistent Web account")
	}

	freshFromEnvironment, err := localauth.Open(filepath.Join(root, "new-client-auth.json"), localauth.Bootstrap{
		Username: restartConfig.SetupUser, Password: restartConfig.SetupPassword,
	}, localauth.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !freshFromEnvironment.VerifyCredentials("environment-user", "environment-password") {
		t.Fatal("environment bootstrap was not applied to a fresh client")
	}

	// A valid local session can open local settings while the same session is
	// denied access to centrally licensed resources before activation.
	restartedAuth := websession.New(restarted, "client_session", "/login")
	relogin := httptest.NewRequest(http.MethodPost, "http://nas/api/v1/client/auth/login", strings.NewReader(`{"username":"customer-admin","password":"customer-password"}`))
	relogin.Header.Set("Content-Type", "application/json")
	reloginResponse := httptest.NewRecorder()
	restartedAuth.LoginHandler(reloginResponse, relogin)
	if reloginResponse.Code != http.StatusOK {
		t.Fatalf("changed-account login = %d, body = %s", reloginResponse.Code, reloginResponse.Body.String())
	}
	reloginCookie := reloginResponse.Result().Cookies()[0]
	localRequest := httptest.NewRequest(http.MethodGet, "http://nas/settings", nil)
	localRequest.AddCookie(reloginCookie)
	localResponse := httptest.NewRecorder()
	restartedAuth.Require(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(localResponse, localRequest)
	if localResponse.Code != http.StatusNoContent {
		t.Fatalf("local settings access = %d", localResponse.Code)
	}

	resourceRequest := httptest.NewRequest(http.MethodGet, "http://nas/api/v1/client/search?q=movie", nil)
	resourceRequest.AddCookie(reloginCookie)
	resourceResponse := httptest.NewRecorder()
	restartedAuth.RequirePasswordChanged(requireActiveLicense(licenseService, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))).ServeHTTP(resourceResponse, resourceRequest)
	if resourceResponse.Code != http.StatusForbidden || !strings.Contains(resourceResponse.Body.String(), `"error":"license_required"`) {
		t.Fatalf("unactivated resource access = %d, body = %s", resourceResponse.Code, resourceResponse.Body.String())
	}
}

func TestClientLoginPageDocumentsDefaultsWithoutAutofillingPassword(t *testing.T) {
	var page bytes.Buffer
	if err := loginTemplate.Execute(&page, nil); err != nil {
		t.Fatal(err)
	}
	html := page.String()
	if !strings.Contains(html, "默认账号：<strong>admin</strong>") || !strings.Contains(html, "默认密码：<strong>password</strong>") {
		t.Fatal("login page no longer documents the fresh-install credentials")
	}
	if strings.Contains(html, `id="pass" type="password" autocomplete="current-password" value=`) || strings.Contains(html, `id="user" autocomplete="username" value=`) {
		t.Fatal("login page autofills bootstrap credentials and can mask environment-provided values")
	}
}
