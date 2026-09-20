package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bosstransfer/internal/adapters/clouddrive2"
	"bosstransfer/internal/setup"
	"bosstransfer/internal/transfer"
)

type fakeCloudDriveClient struct {
	contexts    *[]context.Context
	tokens      *[]string
	auths       *[]string
	permissions *clouddrive2.TokenPermissions
	mounts      []clouddrive2.MountPoint
	files       []clouddrive2.File
	filesByPath map[string][]clouddrive2.File
	listPaths   *[]string
}

func (f fakeCloudDriveClient) Authenticate(ctx context.Context, username, password, totpCode string) (clouddrive2.Authentication, error) {
	f.recordContext(ctx)
	if f.auths != nil {
		*f.auths = append(*f.auths, username+":"+password+":"+totpCode)
	}
	return clouddrive2.Authentication{Token: "password-jwt"}, nil
}

func (f fakeCloudDriveClient) recordContext(ctx context.Context) {
	if f.contexts != nil {
		*f.contexts = append(*f.contexts, ctx)
	}
}

func (f fakeCloudDriveClient) GetSystemInfo(ctx context.Context) (clouddrive2.SystemInfo, error) {
	f.recordContext(ctx)
	return clouddrive2.SystemInfo{IsLogin: true, SystemReady: true}, nil
}

func (f fakeCloudDriveClient) GetApiTokenInfo(ctx context.Context, token string) (clouddrive2.TokenInfo, error) {
	f.recordContext(ctx)
	if f.tokens != nil {
		*f.tokens = append(*f.tokens, token)
	}
	permissions := clouddrive2.TokenPermissions{AllowGetMounts: true, AllowOfflineDownload: true}
	if f.permissions != nil {
		permissions = *f.permissions
	}
	return clouddrive2.TokenInfo{FriendlyName: "BossTransfer", Permissions: permissions}, nil
}

func (f fakeCloudDriveClient) GetMountPoints(ctx context.Context, token string) ([]clouddrive2.MountPoint, error) {
	f.recordContext(ctx)
	if f.tokens != nil {
		*f.tokens = append(*f.tokens, token)
	}
	if f.mounts != nil {
		return append([]clouddrive2.MountPoint(nil), f.mounts...), nil
	}
	return []clouddrive2.MountPoint{{MountPoint: "/CloudNAS/CloudDrive", SourceDir: "/", Name: "115 主盘", IsMounted: true}}, nil
}

func (fakeCloudDriveClient) Close() error { return nil }

func (f fakeCloudDriveClient) ListFiles(_ context.Context, _ string, directory string, _ int) ([]clouddrive2.File, error) {
	if f.listPaths != nil {
		*f.listPaths = append(*f.listPaths, directory)
	}
	if f.filesByPath != nil {
		return append([]clouddrive2.File(nil), f.filesByPath[directory]...), nil
	}
	return append([]clouddrive2.File(nil), f.files...), nil
}

func (fakeCloudDriveClient) SearchFiles(context.Context, string, string, string, int) ([]clouddrive2.File, error) {
	return nil, nil
}

func (fakeCloudDriveClient) GetDownloadLink(context.Context, string, string, bool) (clouddrive2.DownloadLink, error) {
	return clouddrive2.DownloadLink{}, nil
}

func (fakeCloudDriveClient) CopyFiles(context.Context, string, []string, string) (clouddrive2.FileOperation, error) {
	return clouddrive2.FileOperation{}, nil
}

func (fakeCloudDriveClient) AddOfflineFiles(context.Context, string, []string, string) (clouddrive2.FileOperation, error) {
	return clouddrive2.FileOperation{}, nil
}

func (fakeCloudDriveClient) AddSharedLink(context.Context, string, string, string, string) error {
	return nil
}

func newTestSetupApp(t *testing.T) (*clientSetupApp, string, string, string) {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(filepath.Join(source, "115open"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(target, "BossTransfer"), 0o755); err != nil {
		t.Fatal(err)
	}
	transferStore := transfer.NewInMemoryConfigStore(transfer.Config{SourceDir: source, TargetDir: target, MaxResults: 20})
	setupStore, err := setup.NewStore(filepath.Join(root, "data", "client-setup.json"), setup.Config{CloudDrive2Address: "127.0.0.1:19798"})
	if err != nil {
		t.Fatal(err)
	}
	app := newClientSetupApp(transfer.NewService(transferStore), setupStore, source, target, "/host/source", "/host/target")
	app.newCloudDrive = func(string) (clouddrive2.FileClient, error) {
		return fakeCloudDriveClient{files: []clouddrive2.File{{Name: "115 主盘", FullPath: "/115", IsDir: true, IsCloudRoot: true, CanOfflineDownload: true}}}, nil
	}
	return app, source, target, root
}

func newJSONRequest(method, target, body string) *http.Request {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	return request
}

func TestConnectAndApplySetupWithoutExposingToken(t *testing.T) {
	app, source, target, root := newTestSetupApp(t)
	token := "super-secret-token"
	connectRequest := newJSONRequest(http.MethodPost, "/api/v1/client/clouddrive/connect", `{"address":"127.0.0.1:19798","token":"`+token+`"}`)
	connectResponse := httptest.NewRecorder()
	app.connectHandler(connectResponse, connectRequest)
	if connectResponse.Code != http.StatusOK {
		t.Fatalf("connect status = %d, body = %s", connectResponse.Code, connectResponse.Body.String())
	}
	if strings.Contains(connectResponse.Body.String(), token) {
		t.Fatal("connect response exposed API token")
	}
	raw, err := os.ReadFile(filepath.Join(root, "data", "client-setup.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), token) {
		t.Fatal("client setup file contains plaintext API token")
	}

	applyRequest := newJSONRequest(http.MethodPut, "/api/v1/client/setup", `{"mount_point":"/CloudNAS/CloudDrive","cloud_target_dir":"/115","source_relative":"115open","target_relative":"BossTransfer"}`)
	applyResponse := httptest.NewRecorder()
	app.setupHandler(applyResponse, applyRequest)
	if applyResponse.Code != http.StatusOK {
		t.Fatalf("apply status = %d, body = %s", applyResponse.Code, applyResponse.Body.String())
	}
	cfg := app.transfer.Config()
	if cfg.SourceDir != filepath.Join(source, "115open") || cfg.TargetDir != filepath.Join(target, "BossTransfer") {
		t.Fatalf("transfer paths = %q / %q", cfg.SourceDir, cfg.TargetDir)
	}
	if got := app.store.Get().CloudTargetDir; got != "/115" {
		t.Fatalf("CloudDrive2 target dir = %q, want /115", got)
	}
}

func TestApplySystemDownloadLocationBeforeCloudDriveIsConfigured(t *testing.T) {
	app, source, target, _ := newTestSetupApp(t)
	request := newJSONRequest(http.MethodPut, "/api/v1/client/setup", `{"mount_point":"","source_relative":"115open","target_relative":"BossTransfer"}`)
	response := httptest.NewRecorder()
	app.setupHandler(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("apply system-only setup status = %d, body = %s", response.Code, response.Body.String())
	}
	if got := app.transfer.Config(); got.SourceDir != filepath.Join(source, "115open") || got.TargetDir != filepath.Join(target, "BossTransfer") {
		t.Fatalf("system-only transfer paths = %#v", got)
	}
	stored := app.store.Get()
	if stored.Token != "" || stored.Password != "" || stored.MountPoint != "" || stored.CloudTargetDir != "" {
		t.Fatalf("system-only setup created a partial CloudDrive2 integration: %#v", stored)
	}
}

func TestCloudDirectoryBrowserUsesCloudDriveOfflineCapability(t *testing.T) {
	app, _, _, _ := newTestSetupApp(t)
	if err := app.store.Update(setup.Config{
		CloudDrive2Address: "127.0.0.1:19798",
		AuthMode:           "token",
		Token:              "secret-token",
	}); err != nil {
		t.Fatal(err)
	}
	var listed []string
	app.newCloudDrive = func(string) (clouddrive2.FileClient, error) {
		return fakeCloudDriveClient{
			listPaths: &listed,
			filesByPath: map[string][]clouddrive2.File{
				"/": {
					{Name: "115 主盘", FullPath: "/115", IsDir: true, IsCloudRoot: true, CanOfflineDownload: true},
					{Name: "只读盘", FullPath: "/readonly", IsDir: true, IsCloudRoot: true},
				},
				"/115": {
					{Name: "下载", FullPath: "/115/Downloads", IsDir: true, CanOfflineDownload: true},
				},
			},
		}, nil
	}

	rootRequest := httptest.NewRequest(http.MethodGet, "/api/v1/client/clouddrive/directories?mount_point=%2FCloudNAS%2FCloudDrive&path=%2F", nil)
	rootResponse := httptest.NewRecorder()
	app.cloudDirectoriesHandler(rootResponse, rootRequest)
	if rootResponse.Code != http.StatusOK ||
		!strings.Contains(rootResponse.Body.String(), `"path":"/115","can_offline_download":true`) ||
		!strings.Contains(rootResponse.Body.String(), `"path":"/readonly","can_offline_download":false`) ||
		!strings.Contains(rootResponse.Body.String(), `"current_selectable":false`) {
		t.Fatalf("root directory response = %d, body = %s", rootResponse.Code, rootResponse.Body.String())
	}

	cloudRequest := httptest.NewRequest(http.MethodGet, "/api/v1/client/clouddrive/directories?mount_point=%2FCloudNAS%2FCloudDrive&path=%2F115", nil)
	cloudResponse := httptest.NewRecorder()
	app.cloudDirectoriesHandler(cloudResponse, cloudRequest)
	if cloudResponse.Code != http.StatusOK ||
		!strings.Contains(cloudResponse.Body.String(), `"current_selectable":true`) ||
		!strings.Contains(cloudResponse.Body.String(), `"parent":"/"`) ||
		!strings.Contains(cloudResponse.Body.String(), `"path":"/115/Downloads"`) {
		t.Fatalf("cloud directory response = %d, body = %s", cloudResponse.Code, cloudResponse.Body.String())
	}
	if strings.Join(listed, ",") != "/,/,/115" {
		t.Fatalf("CloudDrive2 list paths = %#v", listed)
	}
}

func TestCloudDirectoryBrowserAllowsDirectCloudRootMount(t *testing.T) {
	app, _, _, _ := newTestSetupApp(t)
	if err := app.store.Update(setup.Config{CloudDrive2Address: "127.0.0.1:19798", AuthMode: "token", Token: "secret-token"}); err != nil {
		t.Fatal(err)
	}
	app.newCloudDrive = func(string) (clouddrive2.FileClient, error) {
		return fakeCloudDriveClient{
			mounts: []clouddrive2.MountPoint{{MountPoint: "/CloudNAS/115", SourceDir: "/115", Name: "115 主盘", IsMounted: true}},
			filesByPath: map[string][]clouddrive2.File{
				"/":    {{Name: "115 主盘", FullPath: "/115", IsDir: true, IsCloudRoot: true, CanOfflineDownload: true}},
				"/115": {},
			},
		}, nil
	}
	listing, err := app.listCloudDirectories(context.Background(), app.store.Get(), "/CloudNAS/115", "")
	if err != nil {
		t.Fatal(err)
	}
	if listing.Root != "/115" || listing.Current != "/115" || !listing.CurrentSelectable {
		t.Fatalf("direct cloud root listing = %#v", listing)
	}
	validated, err := app.validateCloudDownloadTarget(context.Background(), app.store.Get(), "/CloudNAS/115", "/115")
	if err != nil || validated != "/115" {
		t.Fatalf("validate direct cloud root = %q, %v", validated, err)
	}
}

func TestApplySetupRejectsUnsupportedCloudDirectory(t *testing.T) {
	app, _, _, _ := newTestSetupApp(t)
	if err := app.store.Update(setup.Config{CloudDrive2Address: "127.0.0.1:19798", AuthMode: "token", Token: "secret-token"}); err != nil {
		t.Fatal(err)
	}
	app.newCloudDrive = func(string) (clouddrive2.FileClient, error) {
		return fakeCloudDriveClient{filesByPath: map[string][]clouddrive2.File{
			"/": {{Name: "只读盘", FullPath: "/readonly", IsDir: true}},
		}}, nil
	}
	request := newJSONRequest(http.MethodPut, "/api/v1/client/setup", `{"mount_point":"/CloudNAS/CloudDrive","cloud_target_dir":"/readonly","source_relative":"115open","target_relative":"BossTransfer"}`)
	response := httptest.NewRecorder()
	app.setupHandler(response, request)
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "不支持 CloudDrive2 离线下载") {
		t.Fatalf("unsupported directory response = %d, body = %s", response.Code, response.Body.String())
	}
	if app.store.Get().CloudTargetDir != "" {
		t.Fatal("unsupported CloudDrive2 directory was persisted")
	}
}

func TestPasswordConnectionReusesSavedSessionForMountRefresh(t *testing.T) {
	app, _, _, _ := newTestSetupApp(t)
	var authentications []string
	var observedTokens []string
	app.newCloudDrive = func(string) (clouddrive2.FileClient, error) {
		return fakeCloudDriveClient{auths: &authentications, tokens: &observedTokens}, nil
	}
	connect := newJSONRequest(http.MethodPost, "/api/v1/client/clouddrive/connect", `{"address":"127.0.0.1:19798","auth_mode":"password","username":"alice","password":"secret","totp_code":"123456"}`)
	connectResponse := httptest.NewRecorder()
	app.connectHandler(connectResponse, connect)
	if connectResponse.Code != http.StatusOK {
		t.Fatalf("password connect status = %d, body = %s", connectResponse.Code, connectResponse.Body.String())
	}
	if len(authentications) != 1 || authentications[0] != "alice:secret:123456" {
		t.Fatalf("initial authentications = %#v", authentications)
	}
	if got := app.store.Get().Token; got != "password-jwt" {
		t.Fatalf("saved session token = %q", got)
	}

	refresh := httptest.NewRequest(http.MethodGet, "/api/v1/client/clouddrive/mounts", nil)
	refreshResponse := httptest.NewRecorder()
	app.mountsHandler(refreshResponse, refresh)
	if refreshResponse.Code != http.StatusOK {
		t.Fatalf("mount refresh status = %d, body = %s", refreshResponse.Code, refreshResponse.Body.String())
	}
	if len(authentications) != 1 {
		t.Fatalf("mount refresh unexpectedly reauthenticated without TOTP: %#v", authentications)
	}
	if len(observedTokens) == 0 || observedTokens[len(observedTokens)-1] != "password-jwt" {
		t.Fatalf("mount refresh tokens = %#v", observedTokens)
	}
}

func TestTokenConnectionRequiresOfflineDownloadPermission(t *testing.T) {
	app, _, _, _ := newTestSetupApp(t)
	permissions := clouddrive2.TokenPermissions{AllowGetMounts: true}
	app.newCloudDrive = func(string) (clouddrive2.FileClient, error) {
		return fakeCloudDriveClient{permissions: &permissions}, nil
	}
	request := newJSONRequest(http.MethodPost, "/api/v1/client/clouddrive/connect", `{"address":"127.0.0.1:19798","auth_mode":"token","token":"limited-token"}`)
	response := httptest.NewRecorder()
	app.connectHandler(response, request)
	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "添加离线下载") {
		t.Fatalf("limited token response = %d, body = %s", response.Code, response.Body.String())
	}
	if app.store.Get().Token != "" {
		t.Fatal("token without offline-download permission was persisted")
	}
}

func TestConnectRequiresNewTokenWhenAddressChanges(t *testing.T) {
	app, _, _, _ := newTestSetupApp(t)
	const savedToken = "saved-secret-token"
	if err := app.store.Update(setup.Config{CloudDrive2Address: "127.0.0.1:19798", Token: savedToken}); err != nil {
		t.Fatal(err)
	}

	factoryCalls := 0
	app.newCloudDrive = func(string) (clouddrive2.FileClient, error) {
		factoryCalls++
		return fakeCloudDriveClient{}, nil
	}
	changedRequest := newJSONRequest(http.MethodPost, "/api/v1/client/clouddrive/connect", `{"address":"192.168.1.9:19798","token":""}`)
	changedResponse := httptest.NewRecorder()
	app.connectHandler(changedResponse, changedRequest)
	if changedResponse.Code != http.StatusUnprocessableEntity {
		t.Fatalf("changed-address status = %d, body = %s", changedResponse.Code, changedResponse.Body.String())
	}
	if factoryCalls != 0 {
		t.Fatalf("CloudDrive client was created %d times before a new token was supplied", factoryCalls)
	}
	if !strings.Contains(changedResponse.Body.String(), "token_required_for_address_change") || strings.Contains(changedResponse.Body.String(), savedToken) {
		t.Fatalf("unexpected changed-address response: %s", changedResponse.Body.String())
	}

	var observedTokens []string
	app.newCloudDrive = func(string) (clouddrive2.FileClient, error) {
		factoryCalls++
		return fakeCloudDriveClient{tokens: &observedTokens}, nil
	}
	equivalentRequest := newJSONRequest(http.MethodPost, "/api/v1/client/clouddrive/connect", `{"address":"http://127.0.0.1:19798/","token":""}`)
	equivalentResponse := httptest.NewRecorder()
	app.connectHandler(equivalentResponse, equivalentRequest)
	if equivalentResponse.Code != http.StatusOK {
		t.Fatalf("equivalent-address status = %d, body = %s", equivalentResponse.Code, equivalentResponse.Body.String())
	}
	if len(observedTokens) != 2 || observedTokens[0] != savedToken || observedTokens[1] != savedToken {
		t.Fatalf("saved token was not reused for the equivalent endpoint: %#v", observedTokens)
	}
}

func TestConnectRejectsNonJSONContentType(t *testing.T) {
	app, _, _, _ := newTestSetupApp(t)
	factoryCalls := 0
	app.newCloudDrive = func(string) (clouddrive2.FileClient, error) {
		factoryCalls++
		return fakeCloudDriveClient{}, nil
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/client/clouddrive/connect", strings.NewReader(`{"address":"127.0.0.1:19798","token":"secret"}`))
	request.Header.Set("Content-Type", "text/plain")
	response := httptest.NewRecorder()
	app.connectHandler(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "application/json") {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if factoryCalls != 0 {
		t.Fatalf("CloudDrive client was created %d times for text/plain", factoryCalls)
	}
}

func TestValidatePrivateEndpointRanges(t *testing.T) {
	allowed := []string{
		"127.0.0.1:19798",
		"[::1]:19798",
		"10.1.2.3:19798",
		"172.16.0.1:19798",
		"172.31.255.254:19798",
		"http://192.168.1.5:19798/",
		"localhost:19798",
		"host.docker.internal:19798",
	}
	for _, address := range allowed {
		t.Run("allow_"+strings.NewReplacer(":", "_", "/", "_").Replace(address), func(t *testing.T) {
			if err := validatePrivateEndpoint(context.Background(), address); err != nil {
				t.Fatalf("validatePrivateEndpoint(%q): %v", address, err)
			}
		})
	}

	rejected := []string{
		"0.0.0.0:19798",
		"[::]:19798",
		"169.254.169.254:19798",
		"[fe80::1]:19798",
		"224.0.0.1:19798",
		"[ff02::1]:19798",
		"172.32.0.1:19798",
		"8.8.8.8:19798",
		"[fc00::1]:19798",
		"nas.lan:19798",
	}
	for _, address := range rejected {
		t.Run("reject_"+strings.NewReplacer(":", "_", "/", "_").Replace(address), func(t *testing.T) {
			if err := validatePrivateEndpoint(context.Background(), address); err == nil {
				t.Fatalf("validatePrivateEndpoint(%q) unexpectedly succeeded", address)
			}
		})
	}
}

func TestInspectCloudDriveUsesOneOverallDeadline(t *testing.T) {
	app, _, _, _ := newTestSetupApp(t)
	var contexts []context.Context
	app.newCloudDrive = func(string) (clouddrive2.FileClient, error) {
		return fakeCloudDriveClient{contexts: &contexts}, nil
	}
	if _, err := app.inspectCloudDrive(context.Background(), setup.Config{CloudDrive2Address: "127.0.0.1:19798", AuthMode: "token", Token: "token"}, ""); err != nil {
		t.Fatal(err)
	}
	if len(contexts) != 3 {
		t.Fatalf("RPC context count = %d", len(contexts))
	}
	firstDeadline, ok := contexts[0].Deadline()
	if !ok {
		t.Fatal("RPC context has no overall deadline")
	}
	if remaining := time.Until(firstDeadline); remaining <= 0 || remaining > cloudDriveInspectTimeout {
		t.Fatalf("unexpected remaining deadline: %s", remaining)
	}
	for index, ctx := range contexts[1:] {
		deadline, ok := ctx.Deadline()
		if !ok || !deadline.Equal(firstDeadline) {
			t.Fatalf("RPC context %d does not share the overall deadline", index+1)
		}
	}
}

func TestApplySetupRejectsSymbolicLinkDirectories(t *testing.T) {
	for _, kind := range []string{"source", "target"} {
		t.Run(kind, func(t *testing.T) {
			app, source, target, _ := newTestSetupApp(t)
			if err := app.store.Update(setup.Config{CloudDrive2Address: "127.0.0.1:19798", Token: "token"}); err != nil {
				t.Fatal(err)
			}
			linkName := "linked"
			linkRoot := source
			linkTarget := filepath.Join(source, "115open")
			sourceRelative := linkName
			targetRelative := "BossTransfer"
			wantError := "invalid_source"
			if kind == "target" {
				linkRoot = target
				linkTarget = filepath.Join(target, "BossTransfer")
				sourceRelative = "115open"
				targetRelative = linkName
				wantError = "invalid_target"
			}
			if err := os.Symlink(linkTarget, filepath.Join(linkRoot, linkName)); err != nil {
				t.Skipf("symbolic links unavailable: %v", err)
			}
			request := newJSONRequest(http.MethodPut, "/api/v1/client/setup", `{"mount_point":"/CloudNAS/CloudDrive","cloud_target_dir":"/115","source_relative":"`+sourceRelative+`","target_relative":"`+targetRelative+`"}`)
			response := httptest.NewRecorder()
			app.setupHandler(response, request)
			if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), wantError) {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestEnsurePathInsideRejectsSymbolicLinkRoot(t *testing.T) {
	parent := t.TempDir()
	realRoot := filepath.Join(parent, "real")
	linkRoot := filepath.Join(parent, "link")
	if err := os.Mkdir(realRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	if err := ensurePathInside(linkRoot, linkRoot); err == nil {
		t.Fatal("symbolic-link root was accepted")
	}
}

func TestDirectoryBrowserRejectsTraversal(t *testing.T) {
	app, _, _, _ := newTestSetupApp(t)
	successRequest := httptest.NewRequest(http.MethodGet, "/api/v1/client/directories?kind=source", nil)
	successResponse := httptest.NewRecorder()
	app.directoriesHandler(successResponse, successRequest)
	if successResponse.Code != http.StatusOK || !strings.Contains(successResponse.Body.String(), "115open") {
		t.Fatalf("directory listing status = %d, body = %s", successResponse.Code, successResponse.Body.String())
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/client/directories?kind=source&path=../secret", nil)
	response := httptest.NewRecorder()
	app.directoriesHandler(response, request)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestDirectoryBrowserVerifiesTargetWriteAccess(t *testing.T) {
	app, _, target, _ := newTestSetupApp(t)
	listing, err := app.listDirectories("target", "BossTransfer")
	if err != nil {
		t.Fatal(err)
	}
	if !listing.Writable {
		t.Fatal("writable target was reported as not writable")
	}
	leftovers, err := filepath.Glob(filepath.Join(target, "BossTransfer", ".bosstransfer-write-test-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("write probe left temporary files: %v", leftovers)
	}
	if directoryWritable(filepath.Join(target, "missing")) {
		t.Fatal("missing target directory was reported as writable")
	}
}
