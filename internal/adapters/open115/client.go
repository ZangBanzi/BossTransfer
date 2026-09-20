// Package open115 implements the documented 115 Open Platform API used by
// BossTransfer. It deliberately does not use cookie or private web APIs.
package open115

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultAPIBase      = "https://proapi.115.com"
	DefaultPassportBase = "https://passportapi.115.com"
	DefaultQRCodeBase   = "https://qrcodeapi.115.com"
	DefaultUserAgent    = "BossTransfer/2.1"
	maxResponseBytes    = 8 << 20
)

type Endpoints struct {
	APIBase      string
	PassportBase string
	QRCodeBase   string
}

type Client struct {
	http      *http.Client
	endpoints Endpoints
	userAgent string
}

type APIError struct {
	Code    int64
	Message string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("115 Open API 返回错误 %d", e.Code)
}

func IsAuthorizationError(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && (apiErr.Code == 99 || apiErr.Code/100000 == 401)
}

type DeviceCode struct {
	UID    string `json:"uid"`
	Time   int64  `json:"time"`
	QRCode string `json:"qrcode"`
	Sign   string `json:"sign"`
}

type QRStatus struct {
	Message string `json:"msg"`
	Status  int    `json:"status"`
	Version string `json:"version"`
}

type Tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

type UserInfo struct {
	UserID   string `json:"user_id"`
	UserName string `json:"user_name"`
	Avatar   string `json:"avatar"`
}

type File struct {
	FileID       string `json:"file_id"`
	SHA1         string `json:"sha1"`
	FileName     string `json:"file_name"`
	FileSize     string `json:"file_size"`
	UserPTime    string `json:"user_ptime"`
	UserUTime    string `json:"user_utime"`
	PickCode     string `json:"pick_code"`
	ParentID     string `json:"parent_id"`
	FileCategory string `json:"file_category"`
	Icon         string `json:"ico"`
}

func (f File) Size() int64 {
	value, _ := strconv.ParseInt(strings.TrimSpace(f.FileSize), 10, 64)
	return value
}

func (f File) IsDir() bool { return strings.TrimSpace(f.FileCategory) == "0" }

type Directory struct {
	ID       string `json:"id"`
	ParentID string `json:"parent_id"`
	Name     string `json:"name"`
}

type FolderInfo struct {
	FileName     string `json:"file_name"`
	FileID       string `json:"file_id"`
	FileCategory string `json:"file_category"`
	Paths        []struct {
		FileID   string `json:"file_id"`
		FileName string `json:"file_name"`
	} `json:"paths"`
}

type Download struct {
	FileName string `json:"file_name"`
	FileSize int64  `json:"file_size"`
	PickCode string `json:"pick_code"`
	SHA1     string `json:"sha1"`
	URL      struct {
		URL string `json:"url"`
	} `json:"url"`
}

type UploadInitRequest struct {
	FileName string
	FileSize int64
	Target   string
	FileID   string
	PreID    string
	PickCode string
	SignKey  string
	SignVal  string
}

type UploadInitResponse struct {
	PickCode  string `json:"pick_code"`
	Status    int    `json:"status"`
	SignKey   string `json:"sign_key"`
	SignCheck string `json:"sign_check"`
	FileID    string `json:"file_id"`
	Target    string `json:"target"`
}

type envelope struct {
	State   json.RawMessage `json:"state"`
	Code    int64           `json:"code"`
	Message string          `json:"message"`
	Error   string          `json:"error"`
	Errno   int64           `json:"errno"`
	Data    json.RawMessage `json:"data"`
}

func New(client *http.Client) *Client {
	return NewWithEndpoints(client, Endpoints{})
}

func NewWithEndpoints(client *http.Client, endpoints Endpoints) *Client {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	if strings.TrimSpace(endpoints.APIBase) == "" {
		endpoints.APIBase = DefaultAPIBase
	}
	if strings.TrimSpace(endpoints.PassportBase) == "" {
		endpoints.PassportBase = DefaultPassportBase
	}
	if strings.TrimSpace(endpoints.QRCodeBase) == "" {
		endpoints.QRCodeBase = DefaultQRCodeBase
	}
	endpoints.APIBase = strings.TrimRight(endpoints.APIBase, "/")
	endpoints.PassportBase = strings.TrimRight(endpoints.PassportBase, "/")
	endpoints.QRCodeBase = strings.TrimRight(endpoints.QRCodeBase, "/")
	return &Client{http: client, endpoints: endpoints, userAgent: DefaultUserAgent}
}

func CodeChallenge(verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	return base64.StdEncoding.EncodeToString(digest[:])
}

func (c *Client) QRImageURL(uid string) string {
	return c.endpoints.QRCodeBase + "/api/1.0/mac/1.0/qrcode?uid=" + url.QueryEscape(strings.TrimSpace(uid))
}

func (c *Client) StartDeviceAuth(ctx context.Context, clientID, verifier string) (DeviceCode, error) {
	var result DeviceCode
	err := c.form(ctx, http.MethodPost, c.endpoints.PassportBase+"/open/authDeviceCode", "", url.Values{
		"client_id": {strings.TrimSpace(clientID)}, "code_challenge": {CodeChallenge(verifier)}, "code_challenge_method": {"sha256"},
	}, &result)
	return result, err
}

func (c *Client) QRStatus(ctx context.Context, uid string, timestamp int64, sign string) (QRStatus, error) {
	query := url.Values{"uid": {uid}, "time": {strconv.FormatInt(timestamp, 10)}, "sign": {sign}}
	var result QRStatus
	err := c.form(ctx, http.MethodGet, c.endpoints.QRCodeBase+"/get/status/?"+query.Encode(), "", nil, &result)
	return result, err
}

func (c *Client) ExchangeDeviceCode(ctx context.Context, uid, verifier string) (Tokens, error) {
	var result Tokens
	err := c.form(ctx, http.MethodPost, c.endpoints.PassportBase+"/open/deviceCodeToToken", "", url.Values{
		"uid": {uid}, "code_verifier": {verifier},
	}, &result)
	return result, err
}

func (c *Client) Refresh(ctx context.Context, refreshToken string) (Tokens, error) {
	var result Tokens
	err := c.form(ctx, http.MethodPost, c.endpoints.PassportBase+"/open/refreshToken", "", url.Values{
		"refresh_token": {refreshToken},
	}, &result)
	return result, err
}

func (c *Client) UserInfo(ctx context.Context, accessToken string) (UserInfo, error) {
	var result UserInfo
	err := c.form(ctx, http.MethodGet, c.endpoints.APIBase+"/open/user/info", accessToken, nil, &result)
	return result, err
}

func (c *Client) Search(ctx context.Context, accessToken, query, cid string, limit int) ([]File, error) {
	if limit < 1 || limit > 1000 {
		limit = 100
	}
	values := url.Values{
		"search_value": {strings.TrimSpace(query)}, "limit": {strconv.Itoa(limit)}, "offset": {"0"}, "cid": {strings.TrimSpace(cid)},
	}
	var raw struct {
		State   json.RawMessage `json:"state"`
		Code    int64           `json:"code"`
		Message string          `json:"message"`
		Error   string          `json:"error"`
		Errno   int64           `json:"errno"`
		Data    []File          `json:"data"`
	}
	if err := c.raw(ctx, http.MethodGet, c.endpoints.APIBase+"/open/ufile/search?"+values.Encode(), accessToken, nil, &raw); err != nil {
		return nil, err
	}
	if err := validateResponse(envelope{State: raw.State, Code: raw.Code, Message: raw.Message, Error: raw.Error, Errno: raw.Errno}); err != nil {
		return nil, err
	}
	if raw.Data == nil {
		raw.Data = []File{}
	}
	return raw.Data, nil
}

func (c *Client) ListDirectories(ctx context.Context, accessToken, cid string) ([]Directory, error) {
	values := url.Values{
		"cid": {defaultFolderID(cid)}, "limit": {"1000"}, "offset": {"0"}, "asc": {"1"}, "o": {"file_name"},
		"custom_order": {"1"}, "stdir": {"1"}, "cur": {"1"}, "show_dir": {"1"},
	}
	var raw struct {
		State   json.RawMessage `json:"state"`
		Code    int64           `json:"code"`
		Message string          `json:"message"`
		Error   string          `json:"error"`
		Errno   int64           `json:"errno"`
		Data    []struct {
			FID string `json:"fid"`
			PID string `json:"pid"`
			FC  string `json:"fc"`
			FN  string `json:"fn"`
		} `json:"data"`
	}
	if err := c.raw(ctx, http.MethodGet, c.endpoints.APIBase+"/open/ufile/files?"+values.Encode(), accessToken, nil, &raw); err != nil {
		return nil, err
	}
	if err := validateResponse(envelope{State: raw.State, Code: raw.Code, Message: raw.Message, Error: raw.Error, Errno: raw.Errno}); err != nil {
		return nil, err
	}
	directories := make([]Directory, 0, len(raw.Data))
	for _, item := range raw.Data {
		if item.FC == "0" && strings.TrimSpace(item.FID) != "" {
			directories = append(directories, Directory{ID: item.FID, ParentID: item.PID, Name: item.FN})
		}
	}
	return directories, nil
}

func (c *Client) GetFolderInfo(ctx context.Context, accessToken, fileID string) (FolderInfo, error) {
	var raw json.RawMessage
	if err := c.form(ctx, http.MethodGet, c.endpoints.APIBase+"/open/folder/get_info?file_id="+url.QueryEscape(defaultFolderID(fileID)), accessToken, nil, &raw); err != nil {
		return FolderInfo{}, err
	}
	var result FolderInfo
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var values []FolderInfo
		if err := json.Unmarshal(trimmed, &values); err != nil || len(values) == 0 {
			return FolderInfo{}, errors.New("115 目录不存在")
		}
		result = values[0]
	} else if err := json.Unmarshal(trimmed, &result); err != nil {
		return FolderInfo{}, err
	}
	return result, nil
}

func (c *Client) DownloadURL(ctx context.Context, accessToken, pickCode string) (Download, error) {
	var values map[string]Download
	if err := c.form(ctx, http.MethodPost, c.endpoints.APIBase+"/open/ufile/downurl", accessToken, url.Values{
		"pick_code": {strings.TrimSpace(pickCode)},
	}, &values); err != nil {
		return Download{}, err
	}
	for _, item := range values {
		if strings.TrimSpace(item.URL.URL) != "" {
			return item, nil
		}
	}
	return Download{}, errors.New("115 未返回可用下载地址")
}

func (c *Client) UploadInit(ctx context.Context, accessToken string, request UploadInitRequest) (UploadInitResponse, error) {
	var result UploadInitResponse
	err := c.form(ctx, http.MethodPost, c.endpoints.APIBase+"/open/upload/init", accessToken, url.Values{
		"file_name": {request.FileName}, "file_size": {strconv.FormatInt(request.FileSize, 10)},
		"target": {"U_1_" + defaultFolderID(request.Target)}, "fileid": {strings.ToUpper(request.FileID)},
		"preid": {strings.ToUpper(request.PreID)}, "pick_code": {request.PickCode},
		"sign_key": {request.SignKey}, "sign_val": {strings.ToUpper(request.SignVal)},
	}, &result)
	return result, err
}

func (c *Client) form(ctx context.Context, method, endpoint, accessToken string, form url.Values, target any) error {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	request, err := http.NewRequestWithContext(contextOrBackground(ctx), method, endpoint, body)
	if err != nil {
		return err
	}
	if form != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	var response envelope
	if err := c.do(request, accessToken, &response); err != nil {
		return err
	}
	if err := validateResponse(response); err != nil {
		return err
	}
	if target == nil || len(response.Data) == 0 || bytes.Equal(response.Data, []byte("null")) {
		return nil
	}
	if raw, ok := target.(*json.RawMessage); ok {
		*raw = append((*raw)[:0], response.Data...)
		return nil
	}
	if err := json.Unmarshal(response.Data, target); err != nil {
		return fmt.Errorf("解析 115 响应失败: %w", err)
	}
	return nil
}

func (c *Client) raw(ctx context.Context, method, endpoint, accessToken string, body io.Reader, target any) error {
	request, err := http.NewRequestWithContext(contextOrBackground(ctx), method, endpoint, body)
	if err != nil {
		return err
	}
	return c.do(request, accessToken, target)
}

func (c *Client) do(request *http.Request, accessToken string, target any) error {
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", c.userAgent)
	if strings.TrimSpace(accessToken) != "" {
		request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	reader := io.LimitReader(response.Body, maxResponseBytes+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	if len(data) > maxResponseBytes {
		return errors.New("115 响应过大")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("115 Open API 返回 HTTP %d", response.StatusCode)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("115 返回内容格式无效: %w", err)
	}
	return nil
}

func validateResponse(response envelope) error {
	if response.Code != 0 || response.Errno != 0 || !stateOK(response.State) {
		message := strings.TrimSpace(response.Message)
		if message == "" {
			message = strings.TrimSpace(response.Error)
		}
		code := response.Code
		if code == 0 {
			code = response.Errno
		}
		return &APIError{Code: code, Message: message}
	}
	return nil
}

func stateOK(raw json.RawMessage) bool {
	if len(bytes.TrimSpace(raw)) == 0 {
		return true
	}
	value := strings.TrimSpace(string(raw))
	return value == "true" || value == "1" || value == `"1"`
}

func defaultFolderID(value string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return "0"
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
