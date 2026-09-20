package open115

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestDocumented115RequestShapes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open/authDeviceCode":
			form := require115Form(t, r)
			if form.Get("client_id") != "app-id" || form.Get("code_challenge_method") != "sha256" || form.Get("code_challenge") != CodeChallenge("verifier") {
				t.Errorf("device form = %#v", form)
			}
			write115Data(w, map[string]any{"uid": "uid-1", "time": 123, "qrcode": "qr-content", "sign": "sign-1"})
		case "/open/ufile/search":
			require115Bearer(t, r, "access-token")
			if r.URL.Query().Get("search_value") != "电影" || r.URL.Query().Get("cid") != "folder" || r.URL.Query().Get("limit") != "20" {
				t.Errorf("search query = %s", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"state": true, "data": []map[string]string{{"file_id": "f1", "file_name": "电影.mkv", "file_size": "123", "sha1": "ABC", "pick_code": "pick", "parent_id": "folder", "file_category": "1"}}})
		case "/open/ufile/files":
			require115Bearer(t, r, "access-token")
			_ = json.NewEncoder(w).Encode(map[string]any{"state": true, "data": []map[string]string{{"fid": "d1", "pid": "0", "fc": "0", "fn": "下载"}, {"fid": "f1", "pid": "0", "fc": "1", "fn": "file"}}})
		case "/open/upload/init":
			require115Bearer(t, r, "access-token")
			form := require115Form(t, r)
			if form.Get("target") != "U_1_900" || form.Get("fileid") != "ABCDEF" || form.Get("preid") != "1122" || form.Get("sign_val") != "3344" {
				t.Errorf("upload form = %#v", form)
			}
			write115Data(w, map[string]any{"status": 2, "pick_code": "customer-pick"})
		case "/open/ufile/downurl":
			require115Bearer(t, r, "access-token")
			if require115Form(t, r).Get("pick_code") != "customer-pick" {
				t.Error("missing customer pick code")
			}
			write115Data(w, map[string]any{"f1": map[string]any{"file_name": "电影.mkv", "file_size": 123, "pick_code": "customer-pick", "url": map[string]string{"url": "https://download.example/file"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := NewWithEndpoints(server.Client(), Endpoints{APIBase: server.URL, PassportBase: server.URL, QRCodeBase: server.URL})

	device, err := client.StartDeviceAuth(context.Background(), "app-id", "verifier")
	if err != nil || device.UID != "uid-1" {
		t.Fatalf("StartDeviceAuth = %#v, %v", device, err)
	}
	files, err := client.Search(context.Background(), "access-token", "电影", "folder", 20)
	if err != nil || len(files) != 1 || files[0].Size() != 123 || files[0].PickCode != "pick" {
		t.Fatalf("Search = %#v, %v", files, err)
	}
	directories, err := client.ListDirectories(context.Background(), "access-token", "0")
	if err != nil || len(directories) != 1 || directories[0].Name != "下载" {
		t.Fatalf("ListDirectories = %#v, %v", directories, err)
	}
	upload, err := client.UploadInit(context.Background(), "access-token", UploadInitRequest{FileName: "电影.mkv", FileSize: 123, Target: "900", FileID: "abcdef", PreID: "1122", SignKey: "key", SignVal: "3344"})
	if err != nil || upload.Status != 2 || upload.PickCode != "customer-pick" {
		t.Fatalf("UploadInit = %#v, %v", upload, err)
	}
	download, err := client.DownloadURL(context.Background(), "access-token", "customer-pick")
	if err != nil || download.URL.URL != "https://download.example/file" {
		t.Fatalf("DownloadURL = %#v, %v", download, err)
	}
}

func Test115APIErrorAndResponseLimits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"state": false, "code": 99, "message": "登录失效"})
	}))
	defer server.Close()
	client := NewWithEndpoints(server.Client(), Endpoints{APIBase: server.URL, PassportBase: server.URL, QRCodeBase: server.URL})
	_, err := client.UserInfo(context.Background(), "expired")
	if err == nil || !IsAuthorizationError(err) || err.Error() != "登录失效" {
		t.Fatalf("error = %T %v", err, err)
	}
}

func TestQRImageURLEscapesUID(t *testing.T) {
	client := NewWithEndpoints(nil, Endpoints{QRCodeBase: "https://qr.example"})
	if got := client.QRImageURL("uid + value"); !strings.Contains(got, "uid=uid+%2B+value") {
		t.Fatalf("QRImageURL = %q", got)
	}
}

func require115Form(t *testing.T, r *http.Request) url.Values {
	t.Helper()
	if err := r.ParseForm(); err != nil {
		t.Fatal(err)
	}
	return r.Form
}

func require115Bearer(t *testing.T, r *http.Request, token string) {
	t.Helper()
	if r.Header.Get("Authorization") != "Bearer "+token {
		t.Errorf("authorization = %q", r.Header.Get("Authorization"))
	}
	if r.Header.Get("User-Agent") != DefaultUserAgent {
		t.Errorf("user-agent = %q", r.Header.Get("User-Agent"))
	}
}

func write115Data(w http.ResponseWriter, data any) {
	_ = json.NewEncoder(w).Encode(map[string]any{"state": true, "data": data})
}
