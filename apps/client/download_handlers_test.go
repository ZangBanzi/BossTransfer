package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"bosstransfer/internal/account115"
	"bosstransfer/internal/adapters/clouddrive2"
	"bosstransfer/internal/adapters/open115"
	"bosstransfer/internal/adapters/qbittorrent"
	"bosstransfer/internal/catalogclient"
	"bosstransfer/internal/downloaderconfig"
	"bosstransfer/internal/licenseclient"
	"bosstransfer/internal/setup"
	"bosstransfer/internal/transfer"
)

type staticClientLicense struct{ status licenseclient.Status }

func (s staticClientLicense) Status() licenseclient.Status { return s.status }

type fakeCatalogGateway struct {
	mu          sync.Mutex
	items       []catalogclient.Item
	prepared    catalogclient.Transfer
	prepareErr  error
	signValue   string
	prepareIDs  []string
	signChecks  []string
	searchQuery []string
}

func (f *fakeCatalogGateway) Search(_ context.Context, query string) ([]catalogclient.Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.searchQuery = append(f.searchQuery, query)
	return append([]catalogclient.Item(nil), f.items...), nil
}
func (f *fakeCatalogGateway) Prepare(_ context.Context, resourceID string) (catalogclient.Transfer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prepareIDs = append(f.prepareIDs, resourceID)
	return f.prepared, f.prepareErr
}
func (f *fakeCatalogGateway) Sign(_ context.Context, resourceID, signCheck string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.signChecks = append(f.signChecks, resourceID+":"+signCheck)
	return f.signValue, nil
}

type fakeCustomer115 struct {
	public    account115.PublicConfig
	responses []open115.UploadInitResponse
	uploads   []open115.UploadInitRequest
	download  open115.Download
}

func (f *fakeCustomer115) Public() account115.PublicConfig { return f.public }
func (f *fakeCustomer115) UploadInit(_ context.Context, request open115.UploadInitRequest) (open115.UploadInitResponse, error) {
	f.uploads = append(f.uploads, request)
	if len(f.responses) == 0 {
		return open115.UploadInitResponse{}, nil
	}
	response := f.responses[0]
	f.responses = f.responses[1:]
	return response, nil
}
func (f *fakeCustomer115) DownloadURL(context.Context, string) (open115.Download, error) {
	return f.download, nil
}

type fakeTorrentDownloadClient struct {
	addOptions qbittorrent.AddOptions
	filename   string
	payload    []byte
	testCalls  int
	addCalls   int
	uploads    int
}

func (f *fakeTorrentDownloadClient) Test(context.Context) (qbittorrent.ConnectionInfo, error) {
	f.testCalls++
	return qbittorrent.ConnectionInfo{Version: "5.2.4", DefaultSavePath: "/downloads"}, nil
}
func (f *fakeTorrentDownloadClient) Add(_ context.Context, options qbittorrent.AddOptions) error {
	f.addCalls++
	f.addOptions = options
	return nil
}
func (f *fakeTorrentDownloadClient) AddTorrent(_ context.Context, filename string, payload []byte, options qbittorrent.AddOptions) error {
	f.uploads++
	f.filename = filename
	f.payload = append([]byte(nil), payload...)
	f.addOptions = options
	return nil
}
func (*fakeTorrentDownloadClient) CloseIdleConnections() {}

type downloadLinkCloudClient struct {
	path  string
	link  clouddrive2.DownloadLink
	token string
}

func (f *downloadLinkCloudClient) GetSystemInfo(context.Context) (clouddrive2.SystemInfo, error) {
	return clouddrive2.SystemInfo{}, nil
}
func (f *downloadLinkCloudClient) Authenticate(context.Context, string, string, string) (clouddrive2.Authentication, error) {
	return clouddrive2.Authentication{Token: f.token}, nil
}
func (f *downloadLinkCloudClient) GetApiTokenInfo(context.Context, string) (clouddrive2.TokenInfo, error) {
	return clouddrive2.TokenInfo{}, nil
}
func (f *downloadLinkCloudClient) GetMountPoints(context.Context, string) ([]clouddrive2.MountPoint, error) {
	return nil, nil
}
func (f *downloadLinkCloudClient) ListFiles(context.Context, string, string, int) ([]clouddrive2.File, error) {
	return nil, nil
}
func (f *downloadLinkCloudClient) SearchFiles(context.Context, string, string, string, int) ([]clouddrive2.File, error) {
	return nil, nil
}
func (f *downloadLinkCloudClient) GetDownloadLink(_ context.Context, token, path string, _ bool) (clouddrive2.DownloadLink, error) {
	f.token, f.path = token, path
	return f.link, nil
}
func (f *downloadLinkCloudClient) CopyFiles(context.Context, string, []string, string) (clouddrive2.FileOperation, error) {
	return clouddrive2.FileOperation{}, nil
}
func (f *downloadLinkCloudClient) AddOfflineFiles(context.Context, string, []string, string) (clouddrive2.FileOperation, error) {
	return clouddrive2.FileOperation{}, nil
}
func (f *downloadLinkCloudClient) AddSharedLink(context.Context, string, string, string, string) error {
	return nil
}
func (*downloadLinkCloudClient) Close() error { return nil }

func newTestDownloadAPI(t *testing.T, catalog *fakeCatalogGateway, account *fakeCustomer115) (*clientDownloadAPI, *setup.Store, *downloaderconfig.Store) {
	t.Helper()
	root := t.TempDir()
	source, target := filepath.Join(root, "source"), filepath.Join(root, "target")
	for _, dir := range []string{source, target} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	service := transfer.NewService(transfer.NewInMemoryConfigStore(transfer.Config{SourceDir: source, TargetDir: target, MaxResults: 20}))
	t.Cleanup(service.Close)
	setupStore, err := setup.NewStore(filepath.Join(root, "setup.json"), setup.Config{CloudDrive2Address: "127.0.0.1:19798", AuthMode: "password"})
	if err != nil {
		t.Fatal(err)
	}
	qbStore, err := downloaderconfig.Open(filepath.Join(root, "qbittorrent.json"))
	if err != nil {
		t.Fatal(err)
	}
	license := staticClientLicense{status: licenseclient.Status{Activated: true, License: licenseclient.License{Status: "active"}}}
	return newClientDownloadAPI(service, catalog, license, setupStore, qbStore, account), setupStore, qbStore
}

func TestDownloadersDependOnCustomerSetup(t *testing.T) {
	catalog := &fakeCatalogGateway{}
	unbound := &fakeCustomer115{}
	api, _, _ := newTestDownloadAPI(t, catalog, unbound)
	for id, option := range downloaderMap(api.downloaders()) {
		if option.Available {
			t.Fatalf("%s available before customer binds 115", id)
		}
	}

	bound := &fakeCustomer115{public: account115.PublicConfig{Bound: true}}
	api, setupStore, qbStore := newTestDownloadAPI(t, catalog, bound)
	options := downloaderMap(api.downloaders())
	if !options["system"].Available || options["clouddrive2"].Available || options["qbittorrent"].Available {
		t.Fatalf("unexpected initial downloader state: %#v", options)
	}
	if err := setupStore.Update(setup.Config{CloudDrive2Address: "127.0.0.1:19798", AuthMode: "token", Token: "secret", MountPoint: "/mnt/cloud", CloudTargetDir: "/115/downloads"}); err != nil {
		t.Fatal(err)
	}
	if err := qbStore.SaveVerified(downloaderconfig.Config{BaseURL: "http://127.0.0.1:8080", AuthMode: downloaderconfig.AuthAPIKey, APIKey: "secret", SavePath: "/downloads"}, "5.2.4", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	options = downloaderMap(api.downloaders())
	for _, id := range []string{"system", "clouddrive2", "qbittorrent"} {
		if !options[id].Available {
			t.Errorf("%s should be available after local setup: %#v", id, options[id])
		}
	}
}

func TestTransferToCustomer115HandlesSecondaryHash(t *testing.T) {
	catalog := &fakeCatalogGateway{signValue: "SIGNED"}
	account := &fakeCustomer115{public: account115.PublicConfig{Bound: true}, responses: []open115.UploadInitResponse{
		{Status: 7, SignKey: "key", SignCheck: "10-20"},
		{Status: 2, PickCode: "customer-pick"},
	}}
	api, _, _ := newTestDownloadAPI(t, catalog, account)
	prepared := catalogclient.Transfer{Name: "movie.mkv", Size: 999, SHA1: "FILESHA1", PreID: "PRESHA1"}
	pickCode, err := api.transferToCustomer115(context.Background(), catalog, "opaque", prepared)
	if err != nil || pickCode != "customer-pick" {
		t.Fatalf("transfer = %q, %v", pickCode, err)
	}
	if len(account.uploads) != 2 || account.uploads[1].SignKey != "key" || account.uploads[1].SignVal != "SIGNED" {
		t.Fatalf("upload retries = %#v", account.uploads)
	}
	if len(catalog.signChecks) != 1 || catalog.signChecks[0] != "opaque:10-20" {
		t.Fatalf("sign calls = %#v", catalog.signChecks)
	}
}

func TestTransferStopsWhenRapidUploadMisses(t *testing.T) {
	catalog := &fakeCatalogGateway{}
	account := &fakeCustomer115{public: account115.PublicConfig{Bound: true}, responses: []open115.UploadInitResponse{{Status: 1}}}
	api, _, _ := newTestDownloadAPI(t, catalog, account)
	_, err := api.transferToCustomer115(context.Background(), catalog, "opaque", catalogclient.Transfer{Name: "movie.mkv", Size: 99, SHA1: "A", PreID: "B"})
	if err == nil || !strings.Contains(err.Error(), "避免从管理员网盘直接下载") {
		t.Fatalf("error = %v", err)
	}
}

func TestDownloadRequiresCustomer115BeforePrepare(t *testing.T) {
	catalog := &fakeCatalogGateway{prepared: catalogclient.Transfer{Name: "movie.mkv", Size: 99, SHA1: "A", PreID: "B"}}
	api, _, _ := newTestDownloadAPI(t, catalog, &fakeCustomer115{})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/client/downloads", bytes.NewBufferString(`{"resource_id":"opaque","downloader":"system"}`))
	request.Header.Set("Content-Type", "application/json")
	api.downloadsHandler(recorder, request)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "account_115_required") {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if len(catalog.prepareIDs) != 0 {
		t.Fatalf("manager prepare called before customer binding: %#v", catalog.prepareIDs)
	}
}

func TestSystemDownloadUsesCustomer115URL(t *testing.T) {
	catalog := &fakeCatalogGateway{prepared: catalogclient.Transfer{Name: "movie.mkv", Size: 99, SHA1: "A", PreID: "B"}}
	account := &fakeCustomer115{public: account115.PublicConfig{Bound: true}, responses: []open115.UploadInitResponse{{Status: 2, PickCode: "customer-pick"}}}
	account.download.URL.URL = "https://download.example/movie.mkv"
	api, _, _ := newTestDownloadAPI(t, catalog, account)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/client/downloads", bytes.NewBufferString(`{"resource_id":"opaque","downloader":"system"}`))
	request.Header.Set("Content-Type", "application/json")
	api.downloadsHandler(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Task transfer.Task `json:"task"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil || body.Task.Downloader != "system" {
		t.Fatalf("response = %#v, %v", body, err)
	}
}

func TestCloudDriveUsesExistingCustomerFileWithoutOfflineFolder(t *testing.T) {
	catalog := &fakeCatalogGateway{prepared: catalogclient.Transfer{Name: "movie.mkv", Size: 99, SHA1: "A", PreID: "B"}}
	account := &fakeCustomer115{public: account115.PublicConfig{Bound: true}, responses: []open115.UploadInitResponse{{Status: 2, PickCode: "customer-pick"}}}
	api, setupStore, _ := newTestDownloadAPI(t, catalog, account)
	if err := setupStore.Update(setup.Config{CloudDrive2Address: "127.0.0.1:19798", AuthMode: "token", Token: "local-token", MountPoint: "/mnt/cloud", CloudTargetDir: "/115/downloads"}); err != nil {
		t.Fatal(err)
	}
	cloud := &downloadLinkCloudClient{link: clouddrive2.DownloadLink{DirectURL: "https://download.example/movie.mkv"}}
	api.newCloudDrive = func(string) (clouddrive2.FileClient, error) { return cloud, nil }
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/client/downloads", bytes.NewBufferString(`{"resource_id":"opaque","downloader":"clouddrive2"}`))
	request.Header.Set("Content-Type", "application/json")
	api.downloadsHandler(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if cloud.path != "/115/downloads/movie.mkv" || cloud.token != "local-token" {
		t.Fatalf("CloudDrive2 lookup path/token = %q/%q", cloud.path, cloud.token)
	}
}

func TestQBittorrentOnlyReceivesCustomerTorrentURL(t *testing.T) {
	catalog := &fakeCatalogGateway{prepared: catalogclient.Transfer{Name: "movie.torrent", Size: 99, SHA1: "A", PreID: "B"}}
	account := &fakeCustomer115{public: account115.PublicConfig{Bound: true}, responses: []open115.UploadInitResponse{{Status: 2, PickCode: "customer-pick"}}}
	torrent := []byte("d4:infod4:name5:moviee")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != open115.DefaultUserAgent {
			t.Errorf("User-Agent = %q", r.Header.Get("User-Agent"))
		}
		_, _ = w.Write(torrent)
	}))
	defer server.Close()
	account.download.URL.URL = server.URL + "/signed"
	api, _, qbStore := newTestDownloadAPI(t, catalog, account)
	if err := qbStore.SaveVerified(downloaderconfig.Config{BaseURL: "http://127.0.0.1:8080", AuthMode: downloaderconfig.AuthAPIKey, APIKey: "secret", SavePath: "/downloads"}, "5.2.4", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	qb := &fakeTorrentDownloadClient{}
	api.newQBittorrent = func(downloaderconfig.Config) (torrentDownloadClient, error) { return qb, nil }
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/client/downloads", bytes.NewBufferString(`{"resource_id":"opaque","downloader":"qbittorrent"}`))
	request.Header.Set("Content-Type", "application/json")
	api.downloadsHandler(recorder, request)
	if recorder.Code != http.StatusAccepted || qb.addCalls != 0 || qb.uploads != 1 || qb.filename != "movie.torrent" || !bytes.Equal(qb.payload, torrent) {
		t.Fatalf("status=%d add=%d uploads=%d filename=%q payload=%q body=%s", recorder.Code, qb.addCalls, qb.uploads, qb.filename, qb.payload, recorder.Body.String())
	}
}

func TestSearchGroupsFilmsAndHidesMetadataFiles(t *testing.T) {
	catalog := &fakeCatalogGateway{items: []catalogclient.Item{
		{ResourceID: "folder", Name: "星际穿越 (2014)", GroupID: "folder", GroupName: "星际穿越 (2014)", IsDir: true},
		{ResourceID: "video", Name: "Interstellar.2014.2160p.mkv", GroupID: "folder", GroupName: "星际穿越 (2014)", Size: 10},
		{ResourceID: "nfo", Name: "Interstellar.2014.nfo", GroupID: "folder", GroupName: "星际穿越 (2014)", Size: 1},
	}}
	api, _, _ := newTestDownloadAPI(t, catalog, &fakeCustomer115{public: account115.PublicConfig{Bound: true}})
	recorder := httptest.NewRecorder()
	api.searchHandler(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/client/search?q=星际", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Groups []clientSearchGroup `json:"groups"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Groups) != 1 || response.Groups[0].VideoCount != 1 || len(response.Groups[0].Files) != 1 || response.Groups[0].Quality != "4K" {
		t.Fatalf("groups = %#v", response.Groups)
	}
}

func downloaderMap(options []downloaderOption) map[string]downloaderOption {
	result := make(map[string]downloaderOption, len(options))
	for _, option := range options {
		result[option.ID] = option
	}
	return result
}
