package account115

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bosstransfer/internal/adapters/open115"
)

type fakeGateway struct {
	device       open115.DeviceCode
	status       open115.QRStatus
	tokens       open115.Tokens
	user         open115.UserInfo
	searchTokens []string
	failSearch   bool
	refreshes    int
	upload       open115.UploadInitRequest
}

func (*fakeGateway) QRImageURL(uid string) string { return "https://qr.example/" + uid }
func (f *fakeGateway) StartDeviceAuth(context.Context, string, string) (open115.DeviceCode, error) {
	return f.device, nil
}
func (f *fakeGateway) QRStatus(context.Context, string, int64, string) (open115.QRStatus, error) {
	return f.status, nil
}
func (f *fakeGateway) ExchangeDeviceCode(context.Context, string, string) (open115.Tokens, error) {
	return f.tokens, nil
}
func (f *fakeGateway) Refresh(context.Context, string) (open115.Tokens, error) {
	f.refreshes++
	return f.tokens, nil
}
func (f *fakeGateway) UserInfo(context.Context, string) (open115.UserInfo, error) {
	return f.user, nil
}
func (f *fakeGateway) Search(_ context.Context, token, _, _ string, _ int) ([]open115.File, error) {
	f.searchTokens = append(f.searchTokens, token)
	if f.failSearch && len(f.searchTokens) == 1 {
		return nil, &open115.APIError{Code: 99, Message: "expired"}
	}
	return []open115.File{{FileID: "f1", FileName: "movie.mkv"}}, nil
}
func (*fakeGateway) ListDirectories(context.Context, string, string) ([]open115.Directory, error) {
	return nil, nil
}
func (*fakeGateway) GetFolderInfo(context.Context, string, string) (open115.FolderInfo, error) {
	return open115.FolderInfo{}, nil
}
func (*fakeGateway) DownloadURL(context.Context, string, string) (open115.Download, error) {
	return open115.Download{}, nil
}
func (f *fakeGateway) UploadInit(_ context.Context, _ string, request open115.UploadInitRequest) (open115.UploadInitResponse, error) {
	f.upload = request
	return open115.UploadInitResponse{Status: 2, PickCode: "pick"}, nil
}

func TestDeviceBindingEncryptsTokensAndPersistsAccount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "account.json")
	store, err := Open(path, "app-id")
	if err != nil {
		t.Fatal(err)
	}
	gateway := &fakeGateway{
		device: open115.DeviceCode{UID: "uid", Time: 123, QRCode: "content", Sign: "sign"},
		status: open115.QRStatus{Status: 2},
		tokens: open115.Tokens{AccessToken: "access-plaintext", RefreshToken: "refresh-plaintext", ExpiresIn: 3600},
		user:   open115.UserInfo{UserID: "u1", UserName: "客户账号"},
	}
	service := NewService(store, gateway)
	auth, err := service.StartAuth(context.Background(), "")
	if err != nil || auth.Status != "waiting" || auth.QRImageURL == "" {
		t.Fatalf("StartAuth = %#v, %v", auth, err)
	}
	auth, err = service.PollAuth(context.Background())
	if err != nil || auth.Status != "bound" || !service.Public().Bound {
		t.Fatalf("PollAuth = %#v, %v", auth, err)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(onDisk), "access-plaintext") || strings.Contains(string(onDisk), "refresh-plaintext") || !strings.Contains(string(onDisk), encryptedPrefix) {
		t.Fatalf("tokens were not encrypted: %s", onDisk)
	}
	reloaded, err := Open(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Get().AccessToken != "access-plaintext" || reloaded.Public().UserName != "客户账号" {
		t.Fatalf("reloaded = %#v", reloaded.Get())
	}
}

func TestAppIDChangeClearsOldAccount(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "account.json"), "app-one")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAccount("access", "refresh", time.Now().Add(time.Hour), "u1", "owner", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.SetClientID("app-two"); err != nil {
		t.Fatal(err)
	}
	if store.Public().Bound || store.Get().AccessToken != "" || store.Public().ClientID != "app-two" {
		t.Fatalf("old account survived app change: %#v", store.Get())
	}
}

func TestServiceRefreshesAndRetriesAuthorizationFailure(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "account.json"), "app-id")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAccount("old-access", "old-refresh", time.Now().Add(time.Hour), "u1", "owner", ""); err != nil {
		t.Fatal(err)
	}
	gateway := &fakeGateway{failSearch: true, tokens: open115.Tokens{AccessToken: "new-access", RefreshToken: "new-refresh", ExpiresIn: 3600}}
	service := NewService(store, gateway)
	files, err := service.Search(context.Background(), "movie", "0", 10)
	if err != nil || len(files) != 1 {
		t.Fatalf("Search = %#v, %v", files, err)
	}
	if gateway.refreshes != 1 || len(gateway.searchTokens) != 2 || gateway.searchTokens[0] != "old-access" || gateway.searchTokens[1] != "new-access" {
		t.Fatalf("refresh flow = refreshes %d, tokens %#v", gateway.refreshes, gateway.searchTokens)
	}
}

func TestUploadUsesVisuallySelectedFolder(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "account.json"), "app-id")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAccount("access", "refresh", time.Now().Add(time.Hour), "u1", "owner", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.SelectFolder("folder-900", "BossTransfer"); err != nil {
		t.Fatal(err)
	}
	gateway := &fakeGateway{}
	service := NewService(store, gateway)
	if _, err := service.UploadInit(context.Background(), open115.UploadInitRequest{FileName: "movie.mkv", FileSize: 1, FileID: "A", PreID: "B"}); err != nil {
		t.Fatal(err)
	}
	if gateway.upload.Target != "folder-900" {
		t.Fatalf("upload target = %q", gateway.upload.Target)
	}
}
