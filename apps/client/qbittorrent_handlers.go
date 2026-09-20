package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"bosstransfer/internal/adapters/qbittorrent"
	"bosstransfer/internal/downloaderconfig"
	"bosstransfer/internal/httpapi"
)

type qBittorrentSettingsAPI struct {
	store      *downloaderconfig.Store
	now        func() time.Time
	httpClient func() *http.Client
}

func newQBittorrentSettingsAPI(store *downloaderconfig.Store) *qBittorrentSettingsAPI {
	return &qBittorrentSettingsAPI{
		store:      store,
		now:        time.Now,
		httpClient: newPrivateQBittorrentHTTPClient,
	}
}

// newQBittorrentSettingsHandler is the route wiring entry point. The caller
// should wrap it with the client's existing local-session and safe-mutation
// middleware.
func newQBittorrentSettingsHandler(store *downloaderconfig.Store) http.Handler {
	return http.HandlerFunc(newQBittorrentSettingsAPI(store).settings)
}

func (a *qBittorrentSettingsAPI) settings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		httpapi.WriteJSON(w, http.StatusOK, map[string]any{"qbittorrent": a.store.Public()})
	case http.MethodPut:
		a.update(w, r)
	case http.MethodDelete:
		if err := a.store.Delete(); err != nil {
			writeQBittorrentError(w, http.StatusInternalServerError, "delete_failed", "删除 qBittorrent 配置失败")
			return
		}
		httpapi.WriteJSON(w, http.StatusOK, map[string]any{"qbittorrent": a.store.Public()})
	default:
		w.Header().Set("Allow", "GET, PUT, DELETE")
		writeQBittorrentError(w, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不支持")
	}
}

func (a *qBittorrentSettingsAPI) update(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		BaseURL  string `json:"base_url"`
		AuthMode string `json:"auth_mode"`
		Username string `json:"username"`
		Password string `json:"password"`
		APIKey   string `json:"api_key"`
		SavePath string `json:"save_path"`
	}
	if err := decodeQBittorrentJSON(w, r, &payload); err != nil {
		writeQBittorrentError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	baseURL, err := normalizePrivateQBittorrentURL(payload.BaseURL)
	if err != nil {
		writeQBittorrentError(w, http.StatusUnprocessableEntity, "invalid_qbittorrent_address", err.Error())
		return
	}
	candidate, err := a.store.Resolve(downloaderconfig.Update{
		BaseURL:  baseURL,
		AuthMode: payload.AuthMode,
		Username: payload.Username,
		Password: payload.Password,
		APIKey:   payload.APIKey,
		SavePath: payload.SavePath,
	})
	if err != nil {
		writeQBittorrentError(w, http.StatusUnprocessableEntity, "credentials_required", err.Error())
		return
	}

	httpClient := a.httpClient()
	defer httpClient.CloseIdleConnections()
	client, err := qbittorrent.NewClient(qbittorrent.Config{
		BaseURL:  candidate.BaseURL,
		Username: candidate.Username,
		Password: candidate.Password,
		APIKey:   candidate.APIKey,
	}, httpClient)
	if err != nil {
		writeQBittorrentError(w, http.StatusUnprocessableEntity, "invalid_qbittorrent", err.Error())
		return
	}
	info, err := client.Test(r.Context())
	if err != nil {
		writeQBittorrentError(w, http.StatusBadGateway, "qbittorrent_connection_failed", err.Error())
		return
	}
	if candidate.SavePath == "" {
		candidate.SavePath = strings.TrimSpace(info.DefaultSavePath)
	}
	if err := a.store.SaveVerified(candidate, info.Version, a.now().UTC()); err != nil {
		writeQBittorrentError(w, http.StatusInternalServerError, "save_failed", "qBittorrent 已验证，但配置保存失败")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"qbittorrent": a.store.Public()})
}

func writeQBittorrentError(w http.ResponseWriter, status int, code, message string) {
	httpapi.WriteJSON(w, status, map[string]string{"error": code, "message": message})
}

func decodeQBittorrentJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return errors.New("Content-Type 必须为 application/json")
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("请求正文只能包含一个 JSON 对象")
		}
		return err
	}
	return nil
}

// normalizePrivateQBittorrentURL accepts only explicit IP literals in private
// or loopback ranges, plus the two container-local hostnames used by the app.
// Other DNS names are rejected before any lookup to avoid DNS rebinding.
func normalizePrivateQBittorrentURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("qBittorrent 地址必须是完整的 http 或 https URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("qBittorrent 地址只能包含协议、主机、端口和路径")
	}
	host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(parsed.Hostname())), ".")
	if host == "" {
		return "", errors.New("qBittorrent 地址缺少主机")
	}
	if ip := net.ParseIP(host); ip != nil {
		if !isPrivateQBittorrentIP(ip) {
			return "", errors.New("qBittorrent 地址只允许本机或私网 IP")
		}
		host = ip.String()
	} else if host != "localhost" && host != "host.docker.internal" {
		return "", errors.New("qBittorrent 地址只允许 localhost、host.docker.internal 或私网 IP")
	}
	port := parsed.Port()
	if port != "" {
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return "", errors.New("qBittorrent 端口必须是 1 到 65535 之间的数字")
		}
		parsed.Host = net.JoinHostPort(host, strconv.Itoa(portNumber))
	} else if strings.Contains(host, ":") {
		parsed.Host = "[" + host + "]"
	} else {
		parsed.Host = host
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = ""
	return parsed.String(), nil
}

func isPrivateQBittorrentIP(ip net.IP) bool {
	return ip != nil && !ip.IsUnspecified() && !ip.IsMulticast() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() && (ip.IsLoopback() || ip.IsPrivate())
}

func newPrivateQBittorrentHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = dialPrivateQBittorrent
	return &http.Client{
		Transport: transport,
		Timeout:   qbittorrent.DefaultTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			// qBittorrent API endpoints do not require redirects. Returning the
			// response prevents credentials or a second request escaping to a
			// redirect target.
			return http.ErrUseLastResponse
		},
	}
}

func dialPrivateQBittorrent(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, errors.New("qBittorrent 连接地址无效")
	}
	if ip := net.ParseIP(host); ip != nil {
		if !isPrivateQBittorrentIP(ip) {
			return nil, errors.New("已阻止连接非私网 qBittorrent 地址")
		}
		return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host != "localhost" && host != "host.docker.internal" {
		return nil, errors.New("已阻止连接未授权的 qBittorrent 主机名")
	}
	resolved, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, errors.New("无法解析 qBittorrent 主机名")
	}
	var lastError error
	for _, candidate := range resolved {
		if !isPrivateQBittorrentIP(candidate.IP) {
			continue
		}
		connection, dialErr := (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(candidate.IP.String(), port))
		if dialErr == nil {
			return connection, nil
		}
		lastError = dialErr
	}
	if lastError != nil {
		return nil, errors.New("无法连接私网 qBittorrent")
	}
	return nil, errors.New("qBittorrent 主机名没有解析到允许的私网地址")
}
