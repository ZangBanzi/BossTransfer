package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"bosstransfer/internal/account115"
	"bosstransfer/internal/adapters/open115"
	"bosstransfer/internal/licensing"
)

type fakeOpen115Gateway struct {
	files       []open115.File
	folderInfo  map[string]open115.FolderInfo
	directURL   string
	downloadIDs []string
}

func (*fakeOpen115Gateway) QRImageURL(uid string) string { return "https://qr.example/" + uid }
func (*fakeOpen115Gateway) StartDeviceAuth(context.Context, string, string) (open115.DeviceCode, error) {
	return open115.DeviceCode{}, nil
}
func (*fakeOpen115Gateway) QRStatus(context.Context, string, int64, string) (open115.QRStatus, error) {
	return open115.QRStatus{}, nil
}
func (*fakeOpen115Gateway) ExchangeDeviceCode(context.Context, string, string) (open115.Tokens, error) {
	return open115.Tokens{}, nil
}
func (*fakeOpen115Gateway) Refresh(context.Context, string) (open115.Tokens, error) {
	return open115.Tokens{}, nil
}
func (*fakeOpen115Gateway) UserInfo(context.Context, string) (open115.UserInfo, error) {
	return open115.UserInfo{}, nil
}
func (f *fakeOpen115Gateway) Search(context.Context, string, string, string, int) ([]open115.File, error) {
	return append([]open115.File(nil), f.files...), nil
}
func (*fakeOpen115Gateway) ListDirectories(context.Context, string, string) ([]open115.Directory, error) {
	return nil, nil
}
func (f *fakeOpen115Gateway) GetFolderInfo(_ context.Context, _, id string) (open115.FolderInfo, error) {
	return f.folderInfo[id], nil
}
func (f *fakeOpen115Gateway) DownloadURL(_ context.Context, _, pickCode string) (open115.Download, error) {
	f.downloadIDs = append(f.downloadIDs, pickCode)
	var result open115.Download
	result.PickCode = pickCode
	result.URL.URL = f.directURL
	return result, nil
}
func (*fakeOpen115Gateway) UploadInit(context.Context, string, open115.UploadInitRequest) (open115.UploadInitResponse, error) {
	return open115.UploadInitResponse{}, nil
}

type catalogTestFixture struct {
	api         catalogAPI
	gateway     *fakeOpen115Gateway
	deviceID    string
	deviceToken string
}

func newCatalogTestFixture(t *testing.T, directURL string) catalogTestFixture {
	t.Helper()
	root := t.TempDir()
	licenseStore, err := licensing.NewStore(filepath.Join(root, "licenses.json"), []byte("catalog-test-pepper-value"))
	if err != nil {
		t.Fatal(err)
	}
	created, err := licenseStore.CreateLicense(licensing.CreateLicenseRequest{Customer: "测试客户", DeviceLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	activated, err := licenseStore.Activate(licensing.ActivateRequest{Code: created.Code.AuthorizationCode, InstallationID: "install-a", PublicKey: "public-key-a"})
	if err != nil {
		t.Fatal(err)
	}
	accountStore, err := account115.Open(filepath.Join(root, "account-115.json"), "app-id")
	if err != nil {
		t.Fatal(err)
	}
	if err := accountStore.SaveAccount("access-secret", "refresh-secret", time.Now().Add(time.Hour), "owner", "资源账号", ""); err != nil {
		t.Fatal(err)
	}
	gateway := &fakeOpen115Gateway{directURL: directURL, folderInfo: make(map[string]open115.FolderInfo)}
	api, err := newCatalogAPI(account115.NewService(accountStore, gateway), licenseStore, []byte("ticket-codec-pepper-value"))
	if err != nil {
		t.Fatal(err)
	}
	return catalogTestFixture{api: api, gateway: gateway, deviceID: activated.DeviceID, deviceToken: activated.DeviceToken}
}

func (f catalogTestFixture) request(method, target string, body []byte) *http.Request {
	request := httptest.NewRequest(method, target, bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+f.deviceToken)
	request.Header.Set("X-BossTransfer-Device-ID", f.deviceID)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return request
}

func TestCatalogSearchReturnsFolderFirstOpaqueResources(t *testing.T) {
	fixture := newCatalogTestFixture(t, "https://owner-download.example/movie.mkv")
	fixture.gateway.files = []open115.File{
		{FileID: "folder-1", FileName: "星际穿越 (2014)", FileCategory: "0", ParentID: "0"},
		{FileID: "file-1", FileName: "Interstellar.2014.2160p.mkv", FileCategory: "1", ParentID: "folder-1", FileSize: "12345", SHA1: "FILESHA1", PickCode: "owner-pick", UserUTime: "1700000000"},
		{FileID: "nfo-1", FileName: "Interstellar.2014.nfo", FileCategory: "1", ParentID: "folder-1", FileSize: "50", SHA1: "NFOSHA1", PickCode: "nfo-pick"},
	}
	recorder := httptest.NewRecorder()
	fixture.api.search(recorder, fixture.request(http.MethodGet, "/api/v1/catalog/search?q=星际", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "owner-pick") || strings.Contains(recorder.Body.String(), "owner-download") {
		t.Fatalf("owner secret leaked: %s", recorder.Body.String())
	}
	var response struct {
		Items []catalogSearchItem `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 3 || !response.Items[0].IsDir || response.Items[1].GroupName != "星际穿越 (2014)" {
		t.Fatalf("items = %#v", response.Items)
	}
	ticket, err := fixture.api.codec.open(response.Items[1].ResourceID)
	if err != nil || ticket.DeviceID != fixture.deviceID || ticket.PickCode != "owner-pick" {
		t.Fatalf("ticket = %#v, %v", ticket, err)
	}
	if strings.Contains(response.Items[1].ResourceID, "owner-pick") || strings.Contains(response.Items[1].ResourceID, "FILESHA1") {
		t.Fatal("opaque resource id contains plaintext owner metadata")
	}
}

func TestPrepareReturnsOnlyRapidUploadMetadata(t *testing.T) {
	content := bytes.Repeat([]byte("BossTransfer"), 20000)
	server := newRangeServer(t, content)
	defer server.Close()
	fixture := newCatalogTestFixture(t, server.URL+"/owner-file")
	ticket, err := fixture.api.codec.seal(resourceTicket{Version: 2, DeviceID: fixture.deviceID, FileID: "file-1", Name: "movie.mkv", Size: int64(len(content)), SHA1: "FULLSHA1", PickCode: "owner-pick", ExpiresAt: time.Now().Add(time.Minute).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]string{"resource_id": ticket})
	recorder := httptest.NewRecorder()
	fixture.api.prepare(recorder, fixture.request(http.MethodPost, "/api/v1/catalog/prepare", payload))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	expected := sha1.Sum(content[:preIDBytes])
	if !strings.Contains(recorder.Body.String(), strings.ToUpper(hex.EncodeToString(expected[:]))) || !strings.Contains(recorder.Body.String(), "FULLSHA1") {
		t.Fatalf("prepare response = %s", recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "owner-pick") || strings.Contains(recorder.Body.String(), server.URL) {
		t.Fatalf("prepare leaked owner credentials: %s", recorder.Body.String())
	}
	if len(fixture.gateway.downloadIDs) != 1 || fixture.gateway.downloadIDs[0] != "owner-pick" {
		t.Fatalf("download lookups = %#v", fixture.gateway.downloadIDs)
	}
}

func TestSignHashesExactOwnerRange(t *testing.T) {
	content := []byte("0123456789abcdefghijklmnopqrstuvwxyz")
	server := newRangeServer(t, content)
	defer server.Close()
	fixture := newCatalogTestFixture(t, server.URL+"/owner-file")
	ticket, err := fixture.api.codec.seal(resourceTicket{Version: 2, DeviceID: fixture.deviceID, FileID: "file-1", Name: "movie.mkv", Size: int64(len(content)), SHA1: "FULLSHA1", PickCode: "owner-pick", ExpiresAt: time.Now().Add(time.Minute).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]string{"resource_id": ticket, "sign_check": "10-20"})
	recorder := httptest.NewRecorder()
	fixture.api.sign(recorder, fixture.request(http.MethodPost, "/api/v1/catalog/sign", payload))
	expected := sha1.Sum(content[10:21])
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), strings.ToUpper(hex.EncodeToString(expected[:]))) {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	badPayload, _ := json.Marshal(map[string]string{"resource_id": ticket, "sign_check": "10-999"})
	bad := httptest.NewRecorder()
	fixture.api.sign(bad, fixture.request(http.MethodPost, "/api/v1/catalog/sign", badPayload))
	if bad.Code != http.StatusUnprocessableEntity {
		t.Fatalf("out-of-range status = %d, body = %s", bad.Code, bad.Body.String())
	}
}

func TestResourceTicketRejectsTamperExpiryAndAnotherDevice(t *testing.T) {
	fixture := newCatalogTestFixture(t, "https://download.example/file")
	valid, err := fixture.api.codec.seal(resourceTicket{Version: 2, DeviceID: fixture.deviceID, FileID: "file", Name: "movie.mkv", Size: 1, SHA1: "A", PickCode: "P", ExpiresAt: time.Now().Add(time.Minute).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.api.codec.open(valid[:len(valid)-1] + "x"); err == nil {
		t.Fatal("tampered ticket accepted")
	}

	expired, _ := fixture.api.codec.seal(resourceTicket{Version: 2, DeviceID: fixture.deviceID, FileID: "file", Name: "movie.mkv", Size: 1, SHA1: "A", PickCode: "P", ExpiresAt: time.Now().Add(-time.Minute).Unix()})
	requestBody, _ := json.Marshal(map[string]string{"resource_id": expired})
	recorder := httptest.NewRecorder()
	fixture.api.prepare(recorder, fixture.request(http.MethodPost, "/api/v1/catalog/prepare", requestBody))
	if recorder.Code != http.StatusGone {
		t.Fatalf("expired status = %d", recorder.Code)
	}

	otherDevice, _ := fixture.api.codec.seal(resourceTicket{Version: 2, DeviceID: "another-device", FileID: "file", Name: "movie.mkv", Size: 1, SHA1: "A", PickCode: "P", ExpiresAt: time.Now().Add(time.Minute).Unix()})
	requestBody, _ = json.Marshal(map[string]string{"resource_id": otherDevice})
	recorder = httptest.NewRecorder()
	fixture.api.prepare(recorder, fixture.request(http.MethodPost, "/api/v1/catalog/prepare", requestBody))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("device mismatch status = %d", recorder.Code)
	}
}

func TestClientAppReturnsManagerConfiguredAppID(t *testing.T) {
	fixture := newCatalogTestFixture(t, "https://download.example/file")
	recorder := httptest.NewRecorder()
	fixture.api.clientApp(recorder, fixture.request(http.MethodGet, "/api/v1/catalog/app", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"client_id":"app-id"`) {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func newRangeServer(t *testing.T, content []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != open115.DefaultUserAgent {
			t.Errorf("user-agent = %q", r.Header.Get("User-Agent"))
		}
		value := strings.TrimPrefix(r.Header.Get("Range"), "bytes=")
		parts := strings.Split(value, "-")
		if len(parts) != 2 {
			http.Error(w, "range required", http.StatusBadRequest)
			return
		}
		start, err1 := strconv.Atoi(parts[0])
		end, err2 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil || start < 0 || end < start || end >= len(content) {
			http.Error(w, "bad range", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(content)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(content[start : end+1])
	}))
}
