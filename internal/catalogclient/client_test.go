package catalogclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeCredentials struct{ manager, device, token string }

func (f fakeCredentials) Identity() (string, string, string) {
	return f.manager, "installation", "public"
}
func (f fakeCredentials) DeviceCredential() (string, string, error) { return f.device, f.token, nil }

func TestCatalogFlowUsesDeviceCredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer device-secret" || r.Header.Get("X-BossTransfer-Device-ID") != "device-1" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/catalog/app":
			_ = json.NewEncoder(w).Encode(map[string]string{"client_id": "official-app"})
		case "/api/v1/catalog/search":
			if r.URL.Query().Get("q") != "测试 文件" {
				t.Errorf("query = %q", r.URL.Query().Get("q"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{{"resource_id": "opaque", "name": "资源.mkv", "size": 12}}})
		case "/api/v1/catalog/prepare":
			assertCatalogPayload(t, r, map[string]string{"resource_id": "opaque"})
			_ = json.NewEncoder(w).Encode(map[string]any{"transfer": map[string]any{"name": "资源.mkv", "size": 12, "sha1": "ABC", "preid": "DEF"}})
		case "/api/v1/catalog/sign":
			assertCatalogPayload(t, r, map[string]string{"resource_id": "opaque", "sign_check": "10-20"})
			_ = json.NewEncoder(w).Encode(map[string]string{"sign_val": "AABB"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := New(fakeCredentials{manager: server.URL, device: "device-1", token: "device-secret"}, server.Client())
	appID, err := client.AppConfig(context.Background())
	if err != nil || appID != "official-app" {
		t.Fatalf("AppConfig = %q, %v", appID, err)
	}
	items, err := client.Search(context.Background(), " 测试 文件 ")
	if err != nil || len(items) != 1 || items[0].ResourceID != "opaque" {
		t.Fatalf("Search = %#v, %v", items, err)
	}
	transfer, err := client.Prepare(context.Background(), items[0].ResourceID)
	if err != nil || transfer.Name != "资源.mkv" || transfer.SHA1 != "ABC" || transfer.PreID != "DEF" {
		t.Fatalf("Prepare = %#v, %v", transfer, err)
	}
	sign, err := client.Sign(context.Background(), items[0].ResourceID, "10-20")
	if err != nil || sign != "AABB" {
		t.Fatalf("Sign = %q, %v", sign, err)
	}
}

func assertCatalogPayload(t *testing.T, r *http.Request, expected map[string]string) {
	t.Helper()
	var payload map[string]string
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		t.Errorf("decode request: %v", err)
		return
	}
	for key, value := range expected {
		if payload[key] != value {
			t.Errorf("payload[%q] = %q, want %q", key, payload[key], value)
		}
	}
}

func TestPrepareRejectsIncompleteTransfer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"transfer": map[string]any{"name": "file.mkv", "size": 10, "sha1": "ABC"}})
	}))
	defer server.Close()
	client := New(fakeCredentials{manager: server.URL, device: "d", token: "t"}, server.Client())
	if _, err := client.Prepare(context.Background(), "opaque"); err == nil {
		t.Fatal("incomplete transfer unexpectedly accepted")
	}
}

func TestRemoteErrorMessageIsReturned(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "revoked", "message": "授权已撤销"})
	}))
	defer server.Close()
	client := New(fakeCredentials{manager: server.URL, device: "d", token: "t"}, server.Client())
	_, err := client.Search(context.Background(), "query")
	if err == nil || err.Error() != "授权已撤销" {
		t.Fatalf("error = %v", err)
	}
}

func TestDefaultClientDoesNotForwardDeviceCredentialAcrossRedirect(t *testing.T) {
	received := false
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.Header.Get("Authorization") != "" || r.Header.Get("X-BossTransfer-Device-ID") != ""
		w.WriteHeader(http.StatusOK)
	}))
	defer destination.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()
	client := New(fakeCredentials{manager: redirector.URL, device: "device-secret", token: "token-secret"}, nil)
	if _, err := client.Search(context.Background(), "movie"); err == nil {
		t.Fatal("redirect unexpectedly succeeded")
	}
	if received {
		t.Fatal("device credentials were forwarded to redirect destination")
	}
}

func TestConnectionRejectsAmbiguousManagerURL(t *testing.T) {
	for _, value := range []string{
		"https://user:pass@example.com", "https://example.com?next=other", "https://example.com/#fragment",
	} {
		client := New(fakeCredentials{manager: value, device: "d", token: "t"}, nil)
		if _, err := client.Search(context.Background(), "movie"); err == nil {
			t.Fatalf("accepted unsafe manager URL %q", value)
		}
	}
}
