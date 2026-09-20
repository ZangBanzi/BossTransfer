package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"bosstransfer/internal/downloaderconfig"
)

func TestQBittorrentPasswordSettingsVerifyReuseAndDelete(t *testing.T) {
	const password = "correct horse battery staple"
	var loginCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			loginCalls.Add(1)
			if err := r.ParseForm(); err != nil {
				t.Errorf("ParseForm: %v", err)
			}
			if r.Form.Get("username") != "admin" || r.Form.Get("password") != password {
				_, _ = io.WriteString(w, "Fails.")
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "verified", Path: "/"})
			_, _ = io.WriteString(w, "Ok.")
		case "/api/v2/app/version":
			requireQBittorrentCookie(t, r)
			_, _ = io.WriteString(w, "5.2.3")
		case "/api/v2/app/defaultSavePath":
			requireQBittorrentCookie(t, r)
			_, _ = io.WriteString(w, "/downloads/default")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	path := filepath.Join(t.TempDir(), "qbittorrent.json")
	store, err := downloaderconfig.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	api := newQBittorrentSettingsAPI(store)
	verifiedAt := time.Date(2026, 9, 19, 15, 0, 0, 0, time.UTC)
	api.now = func() time.Time { return verifiedAt }

	response := putQBittorrentSettings(t, api, map[string]any{
		"base_url": server.URL + "/", "auth_mode": "password",
		"username": "admin", "password": password,
	})
	if response.Code != http.StatusOK {
		t.Fatalf("password PUT status = %d, body = %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), password) {
		t.Fatal("password PUT response exposed the password")
	}
	public := store.Public()
	if !public.Available || public.BaseURL != server.URL || public.Username != "admin" || !public.PasswordPresent || public.APIKeyPresent || public.SavePath != "/downloads/default" || public.Version != "5.2.3" || public.LastVerifiedAt == nil || !public.LastVerifiedAt.Equal(verifiedAt) {
		t.Fatalf("password settings public state = %#v", public)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), password) {
		t.Fatal("password settings persisted plaintext password")
	}

	// The same normalized address can reuse the saved password when the
	// browser leaves the secret field blank.
	reused := putQBittorrentSettings(t, api, map[string]any{
		"base_url": server.URL, "auth_mode": "password", "username": "admin",
		"password": "", "save_path": "/downloads/movies",
	})
	if reused.Code != http.StatusOK || store.Public().SavePath != "/downloads/movies" {
		t.Fatalf("same-address secret reuse = %d, body = %s, public = %#v", reused.Code, reused.Body.String(), store.Public())
	}
	if loginCalls.Load() != 2 {
		t.Fatalf("login calls after secret reuse = %d, want 2", loginCalls.Load())
	}

	// A failed Test must not replace the last verified configuration.
	failed := putQBittorrentSettings(t, api, map[string]any{
		"base_url": server.URL, "auth_mode": "password", "username": "admin", "password": "wrong-secret",
	})
	if failed.Code != http.StatusBadGateway || strings.Contains(failed.Body.String(), "wrong-secret") {
		t.Fatalf("failed verification response = %d, body = %s", failed.Code, failed.Body.String())
	}
	if got := store.Config(); got.Password != password || got.SavePath != "/downloads/movies" {
		t.Fatalf("failed verification replaced saved config: %#v", got)
	}
	if !store.Public().Available {
		t.Fatal("failed replacement cleared the existing verified configuration")
	}

	get := httptest.NewRecorder()
	api.settings(get, httptest.NewRequest(http.MethodGet, "/api/v1/client/qbittorrent", nil))
	if get.Code != http.StatusOK || strings.Contains(get.Body.String(), password) || !strings.Contains(get.Body.String(), `"password_present":true`) {
		t.Fatalf("GET response = %d, body = %s", get.Code, get.Body.String())
	}

	deleted := httptest.NewRecorder()
	api.settings(deleted, httptest.NewRequest(http.MethodDelete, "/api/v1/client/qbittorrent", nil))
	if deleted.Code != http.StatusOK || store.Public().Available || store.Public().PasswordPresent {
		t.Fatalf("DELETE response = %d, body = %s, public = %#v", deleted.Code, deleted.Body.String(), store.Public())
	}
}

func TestQBittorrentAPIKeySettingsVerifyAndReuse(t *testing.T) {
	const apiKey = "qbit-api-key-secret"
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path == "/api/v2/auth/login" {
			t.Error("API key mode called the password login endpoint")
			http.Error(w, "unexpected login", http.StatusInternalServerError)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+apiKey {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/api/v2/app/version":
			_, _ = io.WriteString(w, "5.2.4")
		case "/api/v2/app/defaultSavePath":
			_, _ = io.WriteString(w, "/srv/qbit")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	store, err := downloaderconfig.Open(filepath.Join(t.TempDir(), "qbittorrent.json"))
	if err != nil {
		t.Fatal(err)
	}
	api := newQBittorrentSettingsAPI(store)
	first := putQBittorrentSettings(t, api, map[string]any{
		"base_url": server.URL, "auth_mode": "api_key", "api_key": apiKey,
	})
	if first.Code != http.StatusOK {
		t.Fatalf("API key PUT status = %d, body = %s", first.Code, first.Body.String())
	}
	if public := store.Public(); !public.Available || !public.APIKeyPresent || public.PasswordPresent || public.Username != "" || public.SavePath != "/srv/qbit" {
		t.Fatalf("API key public state = %#v", public)
	}
	second := putQBittorrentSettings(t, api, map[string]any{
		"base_url": server.URL, "auth_mode": "api_key", "api_key": "",
	})
	if second.Code != http.StatusOK {
		t.Fatalf("API key reuse status = %d, body = %s", second.Code, second.Body.String())
	}
	if requests.Load() != 4 {
		t.Fatalf("API requests after reuse = %d, want 4", requests.Load())
	}
}

func TestQBittorrentSettingsRejectSSRFAndChangedAddressSecretReuse(t *testing.T) {
	store, err := downloaderconfig.Open(filepath.Join(t.TempDir(), "qbittorrent.json"))
	if err != nil {
		t.Fatal(err)
	}
	api := newQBittorrentSettingsAPI(store)
	rejected := []string{
		"http://example.com:8080",
		"http://8.8.8.8:8080",
		"http://0.0.0.0:8080",
		"http://169.254.169.254/latest/meta-data",
		"http://224.0.0.1:8080",
		"http://user:pass@127.0.0.1:8080",
		"http://127.0.0.1:8080?redirect=http://example.com",
	}
	for _, address := range rejected {
		t.Run(strings.NewReplacer(":", "_", "/", "_").Replace(address), func(t *testing.T) {
			response := putQBittorrentSettings(t, api, map[string]any{
				"base_url": address, "auth_mode": "api_key", "api_key": "secret",
			})
			if response.Code != http.StatusUnprocessableEntity {
				t.Fatalf("address %q status = %d, body = %s", address, response.Code, response.Body.String())
			}
		})
	}
	if store.Public().Available {
		t.Fatal("an SSRF-rejected address became available")
	}

	// A secret saved for one address cannot silently authenticate another.
	verified := downloaderconfig.Config{BaseURL: "http://127.0.0.1:18080", AuthMode: downloaderconfig.AuthAPIKey, APIKey: "saved-key"}
	if err := store.SaveVerified(verified, "5.2.0", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	changed := putQBittorrentSettings(t, api, map[string]any{
		"base_url": "http://127.0.0.1:18081", "auth_mode": "api_key", "api_key": "",
	})
	if changed.Code != http.StatusUnprocessableEntity || !strings.Contains(changed.Body.String(), "credentials_required") {
		t.Fatalf("changed-address blank secret response = %d, body = %s", changed.Code, changed.Body.String())
	}
}

func TestQBittorrentSettingsDoNotFollowRedirects(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetCalls.Add(1)
	}))
	t.Cleanup(target.Close)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(redirector.Close)

	store, err := downloaderconfig.Open(filepath.Join(t.TempDir(), "qbittorrent.json"))
	if err != nil {
		t.Fatal(err)
	}
	response := putQBittorrentSettings(t, newQBittorrentSettingsAPI(store), map[string]any{
		"base_url": redirector.URL, "auth_mode": "api_key", "api_key": "secret",
	})
	if response.Code != http.StatusBadGateway {
		t.Fatalf("redirecting Test status = %d, body = %s", response.Code, response.Body.String())
	}
	if targetCalls.Load() != 0 {
		t.Fatalf("qBittorrent verification followed %d redirect requests", targetCalls.Load())
	}
	if store.Public().Available {
		t.Fatal("redirecting endpoint was marked available")
	}
}

func TestNormalizePrivateQBittorrentURL(t *testing.T) {
	allowed := map[string]string{
		"http://localhost:8080/":                  "http://localhost:8080",
		"https://host.docker.internal:8443/qbit/": "https://host.docker.internal:8443/qbit",
		"http://127.0.0.1:8080":                   "http://127.0.0.1:8080",
		"http://10.1.2.3:8080":                    "http://10.1.2.3:8080",
		"http://172.31.255.254:8080":              "http://172.31.255.254:8080",
		"http://192.168.1.50:8080":                "http://192.168.1.50:8080",
		"http://[::1]:8080":                       "http://[::1]:8080",
		"http://[fd00::1234]:8080":                "http://[fd00::1234]:8080",
	}
	for input, want := range allowed {
		if got, err := normalizePrivateQBittorrentURL(input); err != nil || got != want {
			t.Errorf("normalizePrivateQBittorrentURL(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
}

func putQBittorrentSettings(t *testing.T, api *qBittorrentSettingsAPI, payload any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPut, "/api/v1/client/qbittorrent", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	api.settings(response, request)
	return response
}

func requireQBittorrentCookie(t *testing.T, r *http.Request) {
	t.Helper()
	cookie, err := r.Cookie("SID")
	if err != nil || cookie.Value != "verified" {
		t.Errorf("qBittorrent session cookie = %#v, %v", cookie, err)
	}
}
