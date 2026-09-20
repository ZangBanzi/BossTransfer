package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bosstransfer/internal/config"
	"bosstransfer/internal/licenseclient"
	"bosstransfer/internal/licensing"
	"bosstransfer/internal/localauth"
	"bosstransfer/internal/websession"
)

func TestManagerDefaultLoginAndEndToEndLicensing(t *testing.T) {
	if config.DefaultManagerUser != "admin" || config.DefaultManagerPassword != "password" {
		t.Fatalf("manager defaults = %q / <redacted>, want admin / password", config.DefaultManagerUser)
	}
	dataDir := t.TempDir()
	authStore, err := localauth.Open(
		filepath.Join(dataDir, "manager-auth.json"),
		localauth.Bootstrap{
			Username:   config.DefaultManagerUser,
			Password:   config.DefaultManagerPassword,
			MustChange: true,
		},
		localauth.Options{},
	)
	if err != nil {
		t.Fatal(err)
	}
	licensePath := filepath.Join(dataDir, "licenses.json")
	licenseStore, err := licensing.NewStore(licensePath, []byte("manager-integration-test-pepper"))
	if err != nil {
		t.Fatal(err)
	}

	webAuth := websession.New(authStore, "bosstransfer_manager_session", "/login")
	licenses := licenseAPI{store: licenseStore}
	protectAdminMutation := func(handler http.Handler) http.Handler {
		return webAuth.RequirePasswordChanged(requireSafeMutation(handler))
	}
	protectAuthMutation := func(handler http.Handler) http.Handler {
		return webAuth.Require(requireSafeMutation(handler))
	}
	mux := http.NewServeMux()
	mux.Handle("/api/v1/manager/auth/login", requireSafeMutation(http.HandlerFunc(webAuth.LoginHandler)))
	mux.Handle("/api/v1/manager/auth/credentials", protectAuthMutation(http.HandlerFunc(webAuth.CredentialsHandler)))
	mux.Handle("/api/v1/licensing/activate", requireSafeMutation(http.HandlerFunc(licenses.publicActivate)))
	mux.Handle("/api/v1/licensing/heartbeat", requireSafeMutation(http.HandlerFunc(licenses.publicHeartbeat)))
	mux.Handle("/api/v1/manager/licenses", protectAdminMutation(http.HandlerFunc(licenses.licenses)))
	mux.Handle("/api/v1/manager/licenses/", protectAdminMutation(http.HandlerFunc(licenses.licenseAction)))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	unauthenticated, err := http.Get(server.URL + "/api/v1/manager/licenses")
	if err != nil {
		t.Fatal(err)
	}
	unauthenticated.Body.Close()
	if unauthenticated.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list status = %d, want %d", unauthenticated.StatusCode, http.StatusUnauthorized)
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := server.Client()
	client.Jar = jar
	loginResponse := postManagerJSON(t, client, server.URL+"/api/v1/manager/auth/login", map[string]string{
		"username": "admin",
		"password": "password",
	})
	loginBody := readManagerResponse(t, loginResponse)
	if loginResponse.StatusCode != http.StatusOK {
		t.Fatalf("default login status = %d, body = %s", loginResponse.StatusCode, loginBody)
	}
	var login struct {
		Authenticated bool   `json:"authenticated"`
		Username      string `json:"username"`
		MustChange    bool   `json:"must_change"`
	}
	if err := json.Unmarshal(loginBody, &login); err != nil {
		t.Fatal(err)
	}
	if !login.Authenticated || login.Username != config.DefaultManagerUser || !login.MustChange {
		t.Fatalf("unexpected default login response: %#v", login)
	}
	blockedCreate := postManagerJSON(t, client, server.URL+"/api/v1/manager/licenses", licensing.CreateLicenseRequest{
		Customer: "必须先改密", DeviceLimit: 1,
	})
	blockedBody := readManagerResponse(t, blockedCreate)
	if blockedCreate.StatusCode != http.StatusPreconditionRequired || !bytes.Contains(blockedBody, []byte(`"error":"password_change_required"`)) {
		t.Fatalf("pre-change create status = %d, body = %s", blockedCreate.StatusCode, blockedBody)
	}

	changeResponse := putManagerJSON(t, client, server.URL+"/api/v1/manager/auth/credentials", map[string]string{
		"current_password": "password",
		"username":         "manager-owner",
		"password":         "manager-password",
	})
	changeBody := readManagerResponse(t, changeResponse)
	if changeResponse.StatusCode != http.StatusOK {
		t.Fatalf("credential change status = %d, body = %s", changeResponse.StatusCode, changeBody)
	}
	reloginResponse := postManagerJSON(t, client, server.URL+"/api/v1/manager/auth/login", map[string]string{
		"username": "manager-owner",
		"password": "manager-password",
	})
	reloginBody := readManagerResponse(t, reloginResponse)
	if reloginResponse.StatusCode != http.StatusOK || !bytes.Contains(reloginBody, []byte(`"must_change":false`)) {
		t.Fatalf("changed-account login status = %d, body = %s", reloginResponse.StatusCode, reloginBody)
	}

	createResponse := postManagerJSON(t, client, server.URL+"/api/v1/manager/licenses", licensing.CreateLicenseRequest{
		Customer:    "集成测试客户",
		DeviceLimit: 1,
	})
	createBody := readManagerResponse(t, createResponse)
	if createResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create license status = %d, body = %s", createResponse.StatusCode, createBody)
	}
	var created licensing.CreateLicenseResponse
	if err := json.Unmarshal(createBody, &created); err != nil {
		t.Fatal(err)
	}
	code := created.Code.AuthorizationCode
	if created.License.ID == "" || code == "" {
		t.Fatalf("create response omitted license or authorization code: %s", createBody)
	}
	if count := bytes.Count(createBody, []byte(code)); count != 1 {
		t.Fatalf("authorization code appeared %d times in its one-time response", count)
	}

	listRequest, err := http.NewRequest(http.MethodGet, server.URL+"/api/v1/manager/licenses", nil)
	if err != nil {
		t.Fatal(err)
	}
	listResponse, err := client.Do(listRequest)
	if err != nil {
		t.Fatal(err)
	}
	listBody := readManagerResponse(t, listResponse)
	if listResponse.StatusCode != http.StatusOK {
		t.Fatalf("list licenses status = %d, body = %s", listResponse.StatusCode, listBody)
	}
	if bytes.Contains(listBody, []byte(code)) {
		t.Fatalf("license list exposed the one-time authorization code: %s", listBody)
	}
	var listed struct {
		Licenses []licensing.PublicLicense `json:"licenses"`
	}
	if err := json.Unmarshal(listBody, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Licenses) != 1 || len(listed.Licenses[0].Codes) != 1 {
		t.Fatalf("license list omitted safe code metadata: %#v", listed.Licenses)
	}
	codeSummary := listed.Licenses[0].Codes[0]
	if codeSummary.Prefix == "" || codeSummary.Suffix == "" {
		t.Fatalf("license list omitted code prefix or suffix: %#v", codeSummary)
	}

	persisted, err := os.ReadFile(licensePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(persisted), code) {
		t.Fatal("licensing store persisted the one-time authorization code in plaintext")
	}

	clientStore, err := licenseclient.Open(filepath.Join(dataDir, "client", "license.json"), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	licenseService := licenseclient.NewService(clientStore, server.Client())
	activated, err := licenseService.Activate(context.Background(), server.URL, code, "Customer NAS")
	if err != nil {
		t.Fatalf("client activation through manager handler failed: %v", err)
	}
	if !activated.Activated || activated.DeviceID == "" || activated.License.ID != created.License.ID {
		t.Fatalf("unexpected activated client status: %#v", activated)
	}
	heartbeat, err := licenseService.Heartbeat(context.Background())
	if err != nil {
		t.Fatalf("client heartbeat through manager handler failed: %v", err)
	}
	if !heartbeat.Activated || heartbeat.LastCheckedAt == nil {
		t.Fatalf("heartbeat did not refresh the active lease: %#v", heartbeat)
	}
	managerLicense, err := licenseStore.GetLicense(created.License.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(managerLicense.Devices) != 1 || managerLicense.Devices[0].LastHeartbeatAt == nil {
		t.Fatalf("manager did not record the client heartbeat: %#v", managerLicense.Devices)
	}

	revokeResponse := postManagerJSON(
		t,
		client,
		server.URL+"/api/v1/manager/licenses/"+created.License.ID+"/revoke",
		map[string]any{},
	)
	revokeBody := readManagerResponse(t, revokeResponse)
	if revokeResponse.StatusCode != http.StatusOK {
		t.Fatalf("revoke license status = %d, body = %s", revokeResponse.StatusCode, revokeBody)
	}
	revoked, err := licenseService.Heartbeat(context.Background())
	if err == nil {
		t.Fatal("client heartbeat unexpectedly succeeded after manager revocation")
	}
	var remoteError licenseclient.RemoteError
	if !errors.As(err, &remoteError) || remoteError.StatusCode != http.StatusForbidden || remoteError.Code != "revoked" {
		t.Fatalf("revoked heartbeat error = %#v, want HTTP 403 revoked", err)
	}
	if revoked.Activated || revoked.License.Status != "revoked" || revoked.LastError == "" {
		t.Fatalf("client did not retain the rejected heartbeat state: %#v", revoked)
	}
}

func postManagerJSON(t *testing.T, client *http.Client, target string, payload any) *http.Response {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Post(target, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func putManagerJSON(t *testing.T, client *http.Client, target string, payload any) *http.Response {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPut, target, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func readManagerResponse(t *testing.T, response *http.Response) []byte {
	t.Helper()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
