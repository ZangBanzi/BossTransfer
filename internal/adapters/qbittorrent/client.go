// Package qbittorrent implements the parts of qBittorrent's Web API used by
// BossTransfer. It supports the regular Web UI login flow and qBittorrent
// 5.2+ API keys.
package qbittorrent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultTimeout bounds each Web API request when the caller does not
	// supply a timeout.
	DefaultTimeout  = 10 * time.Second
	maxResponseSize = 8 << 20
)

var (
	ErrAuthentication    = errors.New("qBittorrent 认证失败")
	ErrUnsupportedSource = errors.New("不支持的 qBittorrent 下载地址")
)

// Config contains the address and one authentication method for a
// qBittorrent Web UI. APIKey takes precedence when it is present; otherwise
// Username and Password are used with /api/v2/auth/login.
type Config struct {
	BaseURL  string        `json:"base_url"`
	Username string        `json:"username,omitempty"`
	Password string        `json:"-"`
	APIKey   string        `json:"-"`
	Timeout  time.Duration `json:"-"`
}

type Client struct {
	baseURL  *url.URL
	username string
	password string
	apiKey   string
	http     *http.Client
}

// CloseIdleConnections releases connections owned by this short-lived client.
func (c *Client) CloseIdleConnections() {
	if c != nil && c.http != nil {
		c.http.CloseIdleConnections()
	}
}

// ConnectionInfo is returned after the client has authenticated and verified
// the application endpoints.
type ConnectionInfo struct {
	Version         string `json:"version"`
	DefaultSavePath string `json:"default_save_path"`
}

// AddOptions describes a URL submission to /api/v2/torrents/add. URL is a
// convenience for one source; URLs may be used to submit several sources in
// one request. If both are set, URL is added before URLs.
type AddOptions struct {
	URL      string   `json:"url,omitempty"`
	URLs     []string `json:"urls,omitempty"`
	SavePath string   `json:"save_path,omitempty"`
	Category string   `json:"category,omitempty"`
	Paused   bool     `json:"paused,omitempty"`
	// VerifiedTorrentName may be set only after a trusted catalog verified the
	// file name. It permits signed torrent URLs whose path is opaque.
	VerifiedTorrentName string `json:"-"`
}

type addResponse struct {
	SuccessCount    int      `json:"success_count"`
	FailureCount    int      `json:"failure_count"`
	PendingCount    int      `json:"pending_count"`
	AddedTorrentIDs []string `json:"added_torrent_ids"`
}

// TorrentQuery maps to the documented /api/v2/torrents/info query options.
type TorrentQuery struct {
	Filter   string
	Category string
	Tag      string
	Sort     string
	Reverse  bool
	Limit    int
	Offset   int
	Hashes   []string
}

// Torrent contains the qBittorrent fields BossTransfer commonly needs. The
// Web API may add fields without breaking decoding.
type Torrent struct {
	Hash              string  `json:"hash"`
	Name              string  `json:"name"`
	State             string  `json:"state"`
	Category          string  `json:"category"`
	Tags              string  `json:"tags"`
	SavePath          string  `json:"save_path"`
	DownloadPath      string  `json:"download_path"`
	ContentPath       string  `json:"content_path"`
	Tracker           string  `json:"tracker"`
	Size              int64   `json:"size"`
	TotalSize         int64   `json:"total_size"`
	Downloaded        int64   `json:"downloaded"`
	Uploaded          int64   `json:"uploaded"`
	AmountLeft        int64   `json:"amount_left"`
	DownloadSpeed     int64   `json:"dlspeed"`
	UploadSpeed       int64   `json:"upspeed"`
	DownloadLimit     int64   `json:"dl_limit"`
	UploadLimit       int64   `json:"up_limit"`
	ETA               int64   `json:"eta"`
	AddedOn           int64   `json:"added_on"`
	CompletionOn      int64   `json:"completion_on"`
	LastActivity      int64   `json:"last_activity"`
	TimeActive        int64   `json:"time_active"`
	SeedingTime       int64   `json:"seeding_time"`
	Seeds             int64   `json:"num_seeds"`
	CompleteSeeds     int64   `json:"num_complete"`
	Leeches           int64   `json:"num_leechs"`
	IncompleteLeeches int64   `json:"num_incomplete"`
	Priority          int64   `json:"priority"`
	Progress          float64 `json:"progress"`
	Ratio             float64 `json:"ratio"`
	ForceStart        bool    `json:"force_start"`
	Sequential        bool    `json:"seq_dl"`
}

// New constructs a client with a private cookie jar.
func New(config Config) (*Client, error) {
	return NewClient(config)
}

// NewClient constructs a client. An optional HTTP client may be supplied for
// tests or custom transports. It is copied so its CookieJar and Timeout can be
// set without mutating the caller's client.
func NewClient(config Config, clients ...*http.Client) (*Client, error) {
	if len(clients) > 1 {
		return nil, errors.New("只能提供一个 HTTP 客户端")
	}

	baseURL, err := parseBaseURL(config.BaseURL)
	if err != nil {
		return nil, err
	}
	username := strings.TrimSpace(config.Username)
	apiKey := strings.TrimSpace(config.APIKey)
	if apiKey == "" && (username == "" || config.Password == "") {
		return nil, errors.New("qBittorrent 账号和密码不能为空，或者请提供 API Key")
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, errors.New("无法创建 qBittorrent CookieJar")
	}
	var httpClient http.Client
	if len(clients) == 1 && clients[0] != nil {
		httpClient = *clients[0]
	}
	httpClient.Jar = jar
	// Authentication material must never be forwarded by redirects. A Web UI
	// address should already name its final origin, so return redirect responses
	// to the regular status handling instead of following them.
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	timeout := config.Timeout
	if timeout < 0 {
		return nil, errors.New("qBittorrent 超时时间不能为负数")
	}
	if timeout == 0 {
		timeout = httpClient.Timeout
	}
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	httpClient.Timeout = timeout

	return &Client{
		baseURL:  baseURL,
		username: username,
		password: config.Password,
		apiKey:   apiKey,
		http:     &httpClient,
	}, nil
}

func parseBaseURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New("qBittorrent 地址必须是完整的 http 或 https URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("qBittorrent 地址只能包含协议、主机、端口和路径")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = ""
	return parsed, nil
}

// Connect authenticates, then reads the version and default save path. In API
// key mode no login request is necessary; every request receives a Bearer
// Authorization header.
func (c *Client) Connect(ctx context.Context) (ConnectionInfo, error) {
	if c.apiKey == "" {
		if err := c.login(ctx); err != nil {
			return ConnectionInfo{}, err
		}
	}
	version, err := c.Version(ctx)
	if err != nil {
		return ConnectionInfo{}, err
	}
	savePath, err := c.DefaultSavePath(ctx)
	if err != nil {
		return ConnectionInfo{}, err
	}
	return ConnectionInfo{Version: version, DefaultSavePath: savePath}, nil
}

// Test performs the same server checks as Connect. The session cookie obtained
// during a successful password login remains available for later calls.
func (c *Client) Test(ctx context.Context) (ConnectionInfo, error) {
	return c.Connect(ctx)
}

func (c *Client) login(ctx context.Context) error {
	form := url.Values{}
	form.Set("username", c.username)
	form.Set("password", c.password)
	req, err := c.newRequest(ctx, http.MethodPost, "/api/v2/auth/login", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.do(req, "登录 qBittorrent")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		discardResponse(resp.Body)
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return ErrAuthentication
		}
		return fmt.Errorf("登录 qBittorrent 失败：HTTP %d", resp.StatusCode)
	}
	body, err := readResponse(resp.Body)
	if err != nil {
		return fmt.Errorf("读取 qBittorrent 登录响应失败: %w", err)
	}
	result := strings.TrimSpace(string(body))
	if result != "Ok." && result != "Ok" {
		return ErrAuthentication
	}
	return nil
}

// Version reads /api/v2/app/version.
func (c *Client) Version(ctx context.Context) (string, error) {
	return c.getText(ctx, "/api/v2/app/version", "读取 qBittorrent 版本")
}

// DefaultSavePath reads /api/v2/app/defaultSavePath.
func (c *Client) DefaultSavePath(ctx context.Context) (string, error) {
	return c.getText(ctx, "/api/v2/app/defaultSavePath", "读取 qBittorrent 默认保存路径")
}

func (c *Client) getText(ctx context.Context, path, operation string) (string, error) {
	req, err := c.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.do(req, operation)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp, operation); err != nil {
		return "", err
	}
	body, err := readResponse(resp.Body)
	if err != nil {
		return "", fmt.Errorf("%s失败: %w", operation, err)
	}
	value := strings.TrimSpace(string(body))
	if value == "" {
		return "", fmt.Errorf("%s失败：qBittorrent 返回空响应", operation)
	}
	return value, nil
}

// Add submits magnet links and HTTP(S) URLs whose path ends in .torrent.
// Ordinary HTTP media URLs are rejected before any request is sent.
func (c *Client) Add(ctx context.Context, options AddOptions) error {
	sources := make([]string, 0, len(options.URLs)+1)
	if strings.TrimSpace(options.URL) != "" {
		sources = append(sources, strings.TrimSpace(options.URL))
	}
	for _, source := range options.URLs {
		if value := strings.TrimSpace(source); value != "" {
			sources = append(sources, value)
		}
	}
	if len(sources) == 0 {
		return errors.New("qBittorrent 下载地址不能为空")
	}
	for _, source := range sources {
		if err := validateSource(source, options.VerifiedTorrentName); err != nil {
			return err
		}
	}

	form := url.Values{}
	form.Set("urls", strings.Join(sources, "\n"))
	if strings.TrimSpace(options.SavePath) != "" {
		form.Set("savepath", strings.TrimSpace(options.SavePath))
	}
	if strings.TrimSpace(options.Category) != "" {
		form.Set("category", strings.TrimSpace(options.Category))
	}
	if options.Paused {
		// paused is the documented parameter on older qBittorrent releases;
		// stopped replaced it in qBittorrent 5.0. Sending both keeps the same
		// behavior across supported Web API versions.
		form.Set("paused", "true")
		form.Set("stopped", "true")
	}
	req, err := c.newRequest(ctx, http.MethodPost, "/api/v2/torrents/add", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.do(req, "添加 qBittorrent 任务")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp, "添加 qBittorrent 任务"); err != nil {
		return err
	}
	body, err := readResponse(resp.Body)
	if err != nil {
		return fmt.Errorf("读取 qBittorrent 添加任务响应失败: %w", err)
	}
	result := strings.TrimSpace(string(body))
	if strings.EqualFold(result, "Fails.") {
		return errors.New("qBittorrent 拒绝了下载任务")
	}
	// Web API 2.14 returns structured counts. Mixed success/failure is still an
	// accepted request; report an error only when the server accepted none.
	if strings.HasPrefix(result, "{") {
		var response addResponse
		if err := json.Unmarshal(body, &response); err != nil {
			return errors.New("解析 qBittorrent 添加任务响应失败")
		}
		if response.SuccessCount == 0 && response.PendingCount == 0 && response.FailureCount > 0 {
			return errors.New("qBittorrent 拒绝了下载任务")
		}
	}
	return nil
}

// AddTorrent uploads a .torrent payload to qBittorrent. This is used when the
// torrent lives behind a short-lived URL that requires download headers which
// qBittorrent cannot attach while fetching a URL itself.
func (c *Client) AddTorrent(ctx context.Context, filename string, payload []byte, options AddOptions) error {
	filename = strings.TrimSpace(filename)
	if !strings.EqualFold(path.Ext(filename), ".torrent") {
		return ErrUnsupportedSource
	}
	if len(payload) == 0 {
		return errors.New("qBittorrent 种子文件不能为空")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("torrents", path.Base(filename))
	if err != nil {
		return fmt.Errorf("创建 qBittorrent 种子上传失败: %w", err)
	}
	if _, err := file.Write(payload); err != nil {
		return fmt.Errorf("写入 qBittorrent 种子上传失败: %w", err)
	}
	for key, value := range map[string]string{
		"savepath": strings.TrimSpace(options.SavePath),
		"category": strings.TrimSpace(options.Category),
	} {
		if value != "" {
			if err := writer.WriteField(key, value); err != nil {
				return fmt.Errorf("创建 qBittorrent 上传参数失败: %w", err)
			}
		}
	}
	if options.Paused {
		if err := writer.WriteField("paused", "true"); err != nil {
			return err
		}
		if err := writer.WriteField("stopped", "true"); err != nil {
			return err
		}
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("完成 qBittorrent 种子上传失败: %w", err)
	}
	req, err := c.newRequest(ctx, http.MethodPost, "/api/v2/torrents/add", &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := c.do(req, "上传 qBittorrent 种子")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp, "上传 qBittorrent 种子"); err != nil {
		return err
	}
	response, err := readResponse(resp.Body)
	if err != nil {
		return fmt.Errorf("读取 qBittorrent 添加任务响应失败: %w", err)
	}
	if strings.EqualFold(strings.TrimSpace(string(response)), "Fails.") {
		return errors.New("qBittorrent 拒绝了种子文件")
	}
	return nil
}

func validateSource(source, verifiedTorrentName string) error {
	parsed, err := url.Parse(source)
	if err != nil {
		return ErrUnsupportedSource
	}
	switch strings.ToLower(parsed.Scheme) {
	case "magnet":
		validExactTopic := false
		for _, topic := range parsed.Query()["xt"] {
			topic = strings.ToLower(strings.TrimSpace(topic))
			if strings.HasPrefix(topic, "urn:btih:") && len(topic) > len("urn:btih:") ||
				strings.HasPrefix(topic, "urn:btmh:") && len(topic) > len("urn:btmh:") {
				validExactTopic = true
				break
			}
		}
		if !validExactTopic {
			return fmt.Errorf("%w：magnet 地址无效", ErrUnsupportedSource)
		}
		return nil
	case "http", "https":
		if parsed.Host == "" {
			return ErrUnsupportedSource
		}
		trustedNameIsTorrent := strings.EqualFold(path.Ext(strings.TrimSpace(verifiedTorrentName)), ".torrent")
		if !strings.HasSuffix(strings.ToLower(parsed.EscapedPath()), ".torrent") && !trustedNameIsTorrent {
			return fmt.Errorf("%w：普通 HTTP 媒体 URL 不能交给 qBittorrent，仅支持 .torrent URL", ErrUnsupportedSource)
		}
		return nil
	default:
		return fmt.Errorf("%w：仅支持 magnet 和 .torrent URL", ErrUnsupportedSource)
	}
}

// TorrentsInfo queries /api/v2/torrents/info.
func (c *Client) TorrentsInfo(ctx context.Context, query TorrentQuery) ([]Torrent, error) {
	if query.Limit < 0 {
		return nil, errors.New("qBittorrent 查询的 limit 不能为负数")
	}
	values := url.Values{}
	setQueryValue(values, "filter", query.Filter)
	setQueryValue(values, "category", query.Category)
	setQueryValue(values, "tag", query.Tag)
	setQueryValue(values, "sort", query.Sort)
	if query.Reverse {
		values.Set("reverse", "true")
	}
	if query.Limit > 0 {
		values.Set("limit", strconv.Itoa(query.Limit))
	}
	// qBittorrent defines a negative offset as an offset from the end.
	if query.Offset != 0 {
		values.Set("offset", strconv.Itoa(query.Offset))
	}
	if len(query.Hashes) > 0 {
		hashes := make([]string, 0, len(query.Hashes))
		for _, hash := range query.Hashes {
			if value := strings.TrimSpace(hash); value != "" {
				hashes = append(hashes, value)
			}
		}
		if len(hashes) > 0 {
			values.Set("hashes", strings.Join(hashes, "|"))
		}
	}

	path := "/api/v2/torrents/info"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	req, err := c.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.do(req, "读取 qBittorrent 任务")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp, "读取 qBittorrent 任务"); err != nil {
		return nil, err
	}
	body, err := readResponse(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取 qBittorrent 任务响应失败: %w", err)
	}
	var torrents []Torrent
	if err := json.Unmarshal(body, &torrents); err != nil {
		return nil, errors.New("解析 qBittorrent 任务响应失败")
	}
	if torrents == nil {
		torrents = []Torrent{}
	}
	return torrents, nil
}

// Torrents is a short alias for TorrentsInfo.
func (c *Client) Torrents(ctx context.Context, query TorrentQuery) ([]Torrent, error) {
	return c.TorrentsInfo(ctx, query)
}

func setQueryValue(values url.Values, key, value string) {
	if value = strings.TrimSpace(value); value != "" {
		values.Set(key, value)
	}
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	endpoint := *c.baseURL
	pathParts := strings.SplitN(path, "?", 2)
	endpoint.Path = strings.TrimRight(c.baseURL.Path, "/") + "/" + strings.TrimLeft(pathParts[0], "/")
	endpoint.RawPath = ""
	if len(pathParts) == 2 {
		endpoint.RawQuery = pathParts[1]
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return nil, errors.New("无法创建 qBittorrent 请求")
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	return req, nil
}

// do deliberately does not wrap transport errors. A custom transport can
// echo request secrets in its error text, while callers only need a safe,
// actionable connection error here.
func (c *Client) do(req *http.Request, operation string) (*http.Response, error) {
	resp, err := c.http.Do(req)
	if err == nil {
		return resp, nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(req.Context().Err(), context.Canceled) {
		return nil, fmt.Errorf("%s已取消", operation)
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(req.Context().Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("%s超时", operation)
	}
	return nil, fmt.Errorf("%s失败：无法连接 qBittorrent", operation)
}

func checkStatus(resp *http.Response, operation string) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	discardResponse(resp.Body)
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return ErrAuthentication
	}
	return fmt.Errorf("%s失败：HTTP %d", operation, resp.StatusCode)
}

func readResponse(body io.Reader) ([]byte, error) {
	limited := io.LimitReader(body, maxResponseSize+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, errors.New("读取响应失败")
	}
	if len(data) > maxResponseSize {
		return nil, errors.New("响应超过大小限制")
	}
	return data, nil
}

func discardResponse(body io.Reader) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 4096))
}
