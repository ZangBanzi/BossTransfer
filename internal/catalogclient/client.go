package catalogclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxResponseBytes = 4 << 20

type Credentials interface {
	Identity() (managerURL, installationID, publicKey string)
	DeviceCredential() (deviceID, token string, err error)
}

type Client struct {
	credentials Credentials
	http        *http.Client
}

type Item struct {
	ResourceID string     `json:"resource_id"`
	Name       string     `json:"name"`
	GroupID    string     `json:"group_id"`
	GroupName  string     `json:"group_name"`
	Size       int64      `json:"size"`
	IsDir      bool       `json:"is_dir"`
	ModifiedAt *time.Time `json:"modified_at,omitempty"`
}

type Transfer struct {
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	SHA1  string `json:"sha1"`
	PreID string `json:"preid"`
}

type RemoteError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e RemoteError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("管理端返回 HTTP %d", e.StatusCode)
}

func New(credentials Credentials, client *http.Client) *Client {
	if client == nil {
		client = &http.Client{
			Timeout:       30 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	return &Client{credentials: credentials, http: client}
}

func (c *Client) Search(ctx context.Context, query string) ([]Item, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return []Item{}, nil
	}
	managerURL, deviceID, token, err := c.connection()
	if err != nil {
		return nil, err
	}
	endpoint := managerURL + "/api/v1/catalog/search?q=" + url.QueryEscape(query)
	request, err := http.NewRequestWithContext(contextOrBackground(ctx), http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	c.authorize(request, deviceID, token)
	var response struct {
		Items []Item `json:"items"`
	}
	if err := c.do(request, &response); err != nil {
		return nil, err
	}
	if response.Items == nil {
		response.Items = []Item{}
	}
	return response.Items, nil
}

func (c *Client) AppConfig(ctx context.Context) (string, error) {
	managerURL, deviceID, token, err := c.connection()
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(contextOrBackground(ctx), http.MethodGet, managerURL+"/api/v1/catalog/app", nil)
	if err != nil {
		return "", err
	}
	c.authorize(request, deviceID, token)
	var response struct {
		ClientID string `json:"client_id"`
	}
	if err := c.do(request, &response); err != nil {
		return "", err
	}
	if strings.TrimSpace(response.ClientID) == "" {
		return "", errors.New("管理员尚未配置 115 Open AppID")
	}
	return strings.TrimSpace(response.ClientID), nil
}

func (c *Client) Prepare(ctx context.Context, resourceID string) (Transfer, error) {
	resourceID = strings.TrimSpace(resourceID)
	if resourceID == "" {
		return Transfer{}, errors.New("资源编号不能为空")
	}
	managerURL, deviceID, token, err := c.connection()
	if err != nil {
		return Transfer{}, err
	}
	body, err := json.Marshal(map[string]string{"resource_id": resourceID})
	if err != nil {
		return Transfer{}, err
	}
	request, err := http.NewRequestWithContext(contextOrBackground(ctx), http.MethodPost, managerURL+"/api/v1/catalog/prepare", bytes.NewReader(body))
	if err != nil {
		return Transfer{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	c.authorize(request, deviceID, token)
	var response struct {
		Transfer Transfer `json:"transfer"`
	}
	if err := c.do(request, &response); err != nil {
		return Transfer{}, err
	}
	if response.Transfer.Name == "" || response.Transfer.Size <= 0 || response.Transfer.SHA1 == "" || response.Transfer.PreID == "" {
		return Transfer{}, errors.New("管理端没有返回完整秒传信息")
	}
	return response.Transfer, nil
}

func (c *Client) Sign(ctx context.Context, resourceID, signCheck string) (string, error) {
	resourceID = strings.TrimSpace(resourceID)
	signCheck = strings.TrimSpace(signCheck)
	if resourceID == "" || signCheck == "" {
		return "", errors.New("二次校验信息不完整")
	}
	managerURL, deviceID, token, err := c.connection()
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(map[string]string{"resource_id": resourceID, "sign_check": signCheck})
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(contextOrBackground(ctx), http.MethodPost, managerURL+"/api/v1/catalog/sign", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json")
	c.authorize(request, deviceID, token)
	var response struct {
		SignVal string `json:"sign_val"`
	}
	if err := c.do(request, &response); err != nil {
		return "", err
	}
	if strings.TrimSpace(response.SignVal) == "" {
		return "", errors.New("管理端未返回二次校验结果")
	}
	return strings.TrimSpace(response.SignVal), nil
}

func (c *Client) connection() (managerURL, deviceID, token string, err error) {
	managerURL, _, _ = c.credentials.Identity()
	managerURL = strings.TrimRight(strings.TrimSpace(managerURL), "/")
	parsed, parseErr := url.Parse(managerURL)
	if parseErr != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", "", "", errors.New("授权服务器地址无效，请重新激活客户端")
	}
	deviceID, token, err = c.credentials.DeviceCredential()
	if err != nil {
		return "", "", "", err
	}
	return managerURL, deviceID, token, nil
}

func (c *Client) authorize(request *http.Request, deviceID, token string) {
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-BossTransfer-Device-ID", deviceID)
}

func (c *Client) do(request *http.Request, target any) error {
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("无法连接管理端: %w", err)
	}
	defer response.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes+1))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var body struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		_ = decoder.Decode(&body)
		return RemoteError{StatusCode: response.StatusCode, Code: body.Error, Message: body.Message}
	}
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("管理端响应无效: %w", err)
	}
	return nil
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
