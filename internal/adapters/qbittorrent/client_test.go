package qbittorrent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPasswordConnectUsesOfficialLoginAndCookieJar(t *testing.T) {
	var mu sync.Mutex
	paths := make([]string, 0, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		if r.Header.Get("Authorization") != "" {
			t.Errorf("password request unexpectedly has Authorization header")
		}
		switch r.URL.Path {
		case "/webui/api/v2/auth/login":
			if r.Method != http.MethodPost {
				t.Errorf("login method = %s", r.Method)
			}
			if got := r.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
				t.Errorf("login content type = %q", got)
			}
			if err := r.ParseForm(); err != nil {
				t.Errorf("ParseForm: %v", err)
			}
			if r.Form.Get("username") != "admin" || r.Form.Get("password") != "correct horse battery staple" {
				t.Errorf("login form = %#v", r.Form)
			}
			http.SetCookie(w, &http.Cookie{Name: "server_chosen_cookie_name", Value: "session-value", Path: "/webui"})
			_, _ = io.WriteString(w, "Ok.")
		case "/webui/api/v2/app/version":
			requireCookie(t, r, "server_chosen_cookie_name", "session-value")
			_, _ = io.WriteString(w, "5.2.1\n")
		case "/webui/api/v2/app/defaultSavePath":
			requireCookie(t, r, "server_chosen_cookie_name", "session-value")
			_, _ = io.WriteString(w, "/downloads\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	providedClient := server.Client()
	if providedClient.Jar != nil {
		t.Fatal("httptest client unexpectedly has a jar")
	}
	client, err := NewClient(Config{
		BaseURL:  server.URL + "/webui/",
		Username: " admin ",
		Password: "correct horse battery staple",
	}, providedClient)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if providedClient.Jar != nil {
		t.Fatal("NewClient mutated the supplied HTTP client")
	}

	info, err := client.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if info.Version != "5.2.1" || info.DefaultSavePath != "/downloads" {
		t.Fatalf("connection info = %+v", info)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(paths, ",") != "/webui/api/v2/auth/login,/webui/api/v2/app/version,/webui/api/v2/app/defaultSavePath" {
		t.Fatalf("request paths = %v", paths)
	}
}

func TestLoginAcceptsNoContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			http.SetCookie(w, &http.Cookie{Name: "another_cookie_name", Value: "ok", Path: "/"})
			w.WriteHeader(http.StatusNoContent)
		case "/api/v2/app/version":
			requireCookie(t, r, "another_cookie_name", "ok")
			_, _ = io.WriteString(w, "5.2.0")
		case "/api/v2/app/defaultSavePath":
			requireCookie(t, r, "another_cookie_name", "ok")
			_, _ = io.WriteString(w, "/data")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClient(Config{BaseURL: server.URL, Username: "user", Password: "pass"}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Test(context.Background()); err != nil {
		t.Fatalf("Test: %v", err)
	}
}

func TestAPIKeyConnectAddAndTorrents(t *testing.T) {
	const apiKey = "qbit-api-key-secret"
	var addForm url.Values
	var infoQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			t.Error("API key mode must not call login")
			http.Error(w, "unexpected", http.StatusInternalServerError)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+apiKey {
			t.Errorf("Authorization = %q", got)
		}
		switch r.URL.Path {
		case "/api/v2/app/version":
			_, _ = io.WriteString(w, "5.2.2")
		case "/api/v2/app/defaultSavePath":
			_, _ = io.WriteString(w, "/srv/downloads")
		case "/api/v2/torrents/add":
			if r.Method != http.MethodPost {
				t.Errorf("add method = %s", r.Method)
			}
			if err := r.ParseForm(); err != nil {
				t.Errorf("ParseForm: %v", err)
			}
			addForm = r.PostForm
			_, _ = io.WriteString(w, "Ok.")
		case "/api/v2/torrents/info":
			infoQuery = r.URL.Query()
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[{
				"hash":"ABC123","name":"Example","state":"downloading",
				"category":"movies","save_path":"/srv/movies","progress":0.25,
				"dlspeed":2048,"size":4096,"amount_left":3072
			}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClient(Config{BaseURL: server.URL, APIKey: apiKey}, server.Client())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	info, err := client.Connect(context.Background())
	if err != nil || info.Version != "5.2.2" || info.DefaultSavePath != "/srv/downloads" {
		t.Fatalf("Connect = %+v, %v", info, err)
	}
	err = client.Add(context.Background(), AddOptions{
		URL:      "magnet:?xt=urn:btih:0123456789abcdef",
		URLs:     []string{"https://files.example/Example.TORRENT?signature=abc"},
		SavePath: " /srv/movies ",
		Category: " movies ",
		Paused:   true,
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got := addForm.Get("urls"); got != "magnet:?xt=urn:btih:0123456789abcdef\nhttps://files.example/Example.TORRENT?signature=abc" {
		t.Errorf("add urls = %q", got)
	}
	if addForm.Get("savepath") != "/srv/movies" || addForm.Get("category") != "movies" ||
		addForm.Get("paused") != "true" || addForm.Get("stopped") != "true" {
		t.Errorf("add form = %#v", addForm)
	}

	torrents, err := client.TorrentsInfo(context.Background(), TorrentQuery{
		Filter: "downloading", Category: "movies", Tag: "favorite", Sort: "added_on",
		Reverse: true, Limit: 25, Offset: -50, Hashes: []string{" ABC123 ", "DEF456"},
	})
	if err != nil {
		t.Fatalf("TorrentsInfo: %v", err)
	}
	if len(torrents) != 1 || torrents[0].Hash != "ABC123" || torrents[0].Progress != 0.25 || torrents[0].DownloadSpeed != 2048 {
		t.Fatalf("torrents = %+v", torrents)
	}
	wantQuery := map[string]string{
		"filter": "downloading", "category": "movies", "tag": "favorite", "sort": "added_on",
		"reverse": "true", "limit": "25", "offset": "-50", "hashes": "ABC123|DEF456",
	}
	for key, want := range wantQuery {
		if got := infoQuery.Get(key); got != want {
			t.Errorf("query %s = %q, want %q", key, got, want)
		}
	}
}

func TestAddRejectsOrdinaryHTTPMediaURLWithoutRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client, err := NewClient(Config{BaseURL: server.URL, APIKey: "key"}, server.Client())
	if err != nil {
		t.Fatal(err)
	}

	err = client.Add(context.Background(), AddOptions{URL: "https://media.example/movie.mkv?token=one"})
	if !errors.Is(err, ErrUnsupportedSource) || !strings.Contains(err.Error(), "媒体 URL") {
		t.Fatalf("Add error = %v", err)
	}
	if requests != 0 {
		t.Fatalf("server received %d requests", requests)
	}
}

func TestAddAcceptsOpaqueSignedURLForVerifiedTorrentName(t *testing.T) {
	var submitted string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/torrents/add" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		submitted = r.Form.Get("urls")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client, err := NewClient(Config{BaseURL: server.URL, APIKey: "key"}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	const signedURL = "https://manager.example/api/download/opaque?signature=secret"
	if err := client.Add(context.Background(), AddOptions{URL: signedURL, VerifiedTorrentName: "release.TORRENT"}); err != nil {
		t.Fatalf("Add verified torrent URL: %v", err)
	}
	if submitted != signedURL {
		t.Fatalf("submitted URL = %q", submitted)
	}
}

func TestAddTorrentUploadsPayloadAndOptions(t *testing.T) {
	payload := []byte("d4:infod4:name5:moviee")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/torrents/add" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer key" {
			t.Errorf("Authorization = %q", got)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		file, header, err := r.FormFile("torrents")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		got, err := io.ReadAll(file)
		if err != nil {
			t.Fatal(err)
		}
		if header.Filename != "movie.torrent" || string(got) != string(payload) {
			t.Errorf("torrent = %q %q", header.Filename, got)
		}
		if r.FormValue("savepath") != "/downloads" || r.FormValue("category") != "movies" || r.FormValue("stopped") != "true" {
			t.Errorf("multipart form = %#v", r.MultipartForm.Value)
		}
		_, _ = io.WriteString(w, "Ok.")
	}))
	defer server.Close()
	client, err := NewClient(Config{BaseURL: server.URL, APIKey: "key"}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := client.AddTorrent(context.Background(), "movie.torrent", payload, AddOptions{SavePath: " /downloads ", Category: "movies", Paused: true}); err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}
}

func TestAddRejectsInvalidMagnetAndFailsResponse(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = io.WriteString(w, "Fails.")
	}))
	defer server.Close()
	client, err := NewClient(Config{BaseURL: server.URL, APIKey: "key"}, server.Client())
	if err != nil {
		t.Fatal(err)
	}

	err = client.Add(context.Background(), AddOptions{URL: "magnet:?dn=no-exact-topic"})
	if !errors.Is(err, ErrUnsupportedSource) {
		t.Fatalf("invalid magnet error = %v", err)
	}
	if requests != 0 {
		t.Fatalf("invalid magnet sent %d requests", requests)
	}
	err = client.Add(context.Background(), AddOptions{URL: "magnet:?xt=urn:btmh:1220abcdef"})
	if err == nil || !strings.Contains(err.Error(), "拒绝") {
		t.Fatalf("Fails response error = %v", err)
	}
	if requests != 1 {
		t.Fatalf("valid magnet sent %d requests", requests)
	}
}

func TestAddHandlesWebAPI214Responses(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr bool
	}{
		{name: "success with partial failure", status: http.StatusOK, body: `{"success_count":1,"failure_count":1,"pending_count":0,"added_torrent_ids":["abc"]}`},
		{name: "pending", status: http.StatusAccepted, body: `{"success_count":0,"failure_count":0,"pending_count":1,"added_torrent_ids":[]}`},
		{name: "all failed", status: http.StatusOK, body: `{"success_count":0,"failure_count":1,"pending_count":0,"added_torrent_ids":[]}`, wantErr: true},
		{name: "malformed JSON", status: http.StatusOK, body: `{"success_count":`, wantErr: true},
		{name: "conflict", status: http.StatusConflict, body: `{"success_count":0,"failure_count":1,"pending_count":0}`, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			client, err := NewClient(Config{BaseURL: server.URL, APIKey: "key"}, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			err = client.Add(context.Background(), AddOptions{URL: "magnet:?xt=urn:btih:abcdef"})
			if (err != nil) != test.wantErr {
				t.Fatalf("Add error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestAuthenticationFailureAndTransportErrorsDoNotLeakSecrets(t *testing.T) {
	const password = "never-print-this-password"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "Fails.")
	}))
	defer server.Close()
	client, err := NewClient(Config{BaseURL: server.URL, Username: "admin", Password: password}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Connect(context.Background())
	if !errors.Is(err, ErrAuthentication) {
		t.Fatalf("Connect error = %v", err)
	}
	if strings.Contains(err.Error(), password) {
		t.Fatalf("authentication error leaked password: %v", err)
	}

	leakyHTTPClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("transport copied " + password)
	})}
	client, err = NewClient(Config{BaseURL: "http://qbit.example:8080", Username: "admin", Password: password}, leakyHTTPClient)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Connect(context.Background())
	if err == nil || strings.Contains(err.Error(), password) {
		t.Fatalf("transport error = %v", err)
	}
}

func TestClientDoesNotFollowRedirectsWithAuthentication(t *testing.T) {
	targetRequests := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetRequests++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/capture", http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	client, err := NewClient(Config{BaseURL: redirector.URL, APIKey: "must-not-be-forwarded"}, redirector.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Version(context.Background())
	if err == nil || !strings.Contains(err.Error(), "HTTP 307") {
		t.Fatalf("Version error = %v", err)
	}
	if targetRequests != 0 {
		t.Fatalf("authenticated redirect reached target %d times", targetRequests)
	}
}

func TestResponseSizeLimitAndMalformedTorrentJSON(t *testing.T) {
	t.Run("oversized", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, strings.Repeat("x", maxResponseSize+1))
		}))
		defer server.Close()
		client, err := NewClient(Config{BaseURL: server.URL, APIKey: "key"}, server.Client())
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.Version(context.Background())
		if err == nil || !strings.Contains(err.Error(), "大小限制") {
			t.Fatalf("Version error = %v", err)
		}
	})

	t.Run("malformed JSON does not echo body", func(t *testing.T) {
		const secretBody = `[{"password":"server-secret"}`
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, secretBody)
		}))
		defer server.Close()
		client, err := NewClient(Config{BaseURL: server.URL, APIKey: "key"}, server.Client())
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.TorrentsInfo(context.Background(), TorrentQuery{})
		if err == nil || strings.Contains(err.Error(), "server-secret") {
			t.Fatalf("TorrentsInfo error = %v", err)
		}
	})
}

func TestConfigurationValidationAndTimeout(t *testing.T) {
	invalid := []Config{
		{},
		{BaseURL: "localhost:8080", APIKey: "key"},
		{BaseURL: "ftp://localhost:8080", APIKey: "key"},
		{BaseURL: "http://user:pass@localhost:8080", APIKey: "key"},
		{BaseURL: "http://localhost:8080?key=value", APIKey: "key"},
		{BaseURL: "http://localhost:8080", Username: "admin"},
		{BaseURL: "http://localhost:8080", Password: "password"},
		{BaseURL: "http://localhost:8080", APIKey: "key", Timeout: -time.Second},
	}
	for _, config := range invalid {
		client, err := NewClient(config)
		if err == nil {
			t.Fatalf("NewClient(%+v) unexpectedly returned %+v", config, client)
		}
	}

	client, err := NewClient(Config{BaseURL: "http://localhost:8080", APIKey: "key"})
	if err != nil {
		t.Fatal(err)
	}
	if client.http.Timeout != DefaultTimeout {
		t.Fatalf("timeout = %s, want %s", client.http.Timeout, DefaultTimeout)
	}
	customHTTPClient := &http.Client{Timeout: 2 * time.Second}
	client, err = NewClient(Config{BaseURL: "http://localhost:8080", APIKey: "key"}, customHTTPClient)
	if err != nil {
		t.Fatal(err)
	}
	if client.http.Timeout != 2*time.Second {
		t.Fatalf("custom timeout = %s", client.http.Timeout)
	}

	encoded, err := json.Marshal(Config{
		BaseURL: "http://localhost:8080", Username: "admin",
		Password: "password-secret", APIKey: "api-key-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "password-secret") || strings.Contains(string(encoded), "api-key-secret") {
		t.Fatalf("marshaled Config leaked secrets: %s", encoded)
	}
	if _, err := client.TorrentsInfo(context.Background(), TorrentQuery{Limit: -1}); err == nil {
		t.Fatal("negative limit unexpectedly accepted")
	}
}

func requireCookie(t *testing.T, r *http.Request, name, value string) {
	t.Helper()
	cookie, err := r.Cookie(name)
	if err != nil || cookie.Value != value {
		t.Errorf("cookie %q = %#v, %v", name, cookie, err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
