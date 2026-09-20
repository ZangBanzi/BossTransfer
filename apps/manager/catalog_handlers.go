package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"bosstransfer/internal/account115"
	"bosstransfer/internal/adapters/open115"
	"bosstransfer/internal/httpapi"
	"bosstransfer/internal/licensing"
)

const (
	catalogSearchLimit = 100
	resourceTicketTTL  = 10 * time.Minute
	resourceTicketAAD  = "BossTransfer/catalog-resource/v2"
	preIDBytes         = int64(128 * 1024)
	maxSignBytes       = int64(8 * 1024 * 1024)
)

type catalogAPI struct {
	account  *account115.Service
	licenses *licensing.Store
	codec    resourceTicketCodec
	http     *http.Client
	now      func() time.Time
}

func newCatalogAPI(account *account115.Service, licenses *licensing.Store, pepper []byte) (catalogAPI, error) {
	if account == nil || licenses == nil {
		return catalogAPI{}, errors.New("catalog dependencies are required")
	}
	codec, err := newResourceTicketCodec(pepper)
	if err != nil {
		return catalogAPI{}, err
	}
	return catalogAPI{
		account: account, licenses: licenses, codec: codec, now: time.Now,
		http: &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}, nil
}

func (a catalogAPI) managerSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		httpapi.WriteJSON(w, http.StatusOK, map[string]any{"catalog": a.account.Public()})
	case http.MethodPut:
		var request struct {
			ClientID   string `json:"client_id"`
			FolderID   string `json:"folder_id"`
			FolderName string `json:"folder_name"`
		}
		if err := decodeManagerJSON(w, r, &request); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_json", "请求内容格式无效")
			return
		}
		if strings.TrimSpace(request.ClientID) != "" {
			if err := a.account.SetClientID(request.ClientID); err != nil {
				writeAPIError(w, http.StatusUnprocessableEntity, "invalid_client_id", err.Error())
				return
			}
		}
		if request.FolderID != "" {
			if err := a.account.SelectFolder(r.Context(), request.FolderID, request.FolderName); err != nil {
				writeAPIError(w, http.StatusUnprocessableEntity, "invalid_folder", err.Error())
				return
			}
		}
		httpapi.WriteJSON(w, http.StatusOK, map[string]any{"catalog": a.account.Public()})
	default:
		allow(w, "GET, PUT")
	}
}

func (a catalogAPI) managerAuthStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		allow(w, http.MethodPost)
		return
	}
	var request struct {
		ClientID string `json:"client_id"`
	}
	if err := decodeManagerJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "请求内容格式无效")
		return
	}
	state, err := a.account.StartAuth(r.Context(), request.ClientID)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "auth_start_failed", err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"auth": state, "catalog": a.account.Public()})
}

func (a catalogAPI) managerAuthStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		allow(w, http.MethodGet)
		return
	}
	state, err := a.account.PollAuth(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "auth_status_failed", err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"auth": state, "catalog": a.account.Public()})
}

func (a catalogAPI) managerAccount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		allow(w, http.MethodDelete)
		return
	}
	if err := a.account.ClearAccount(); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "unbind_failed", err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"catalog": a.account.Public()})
}

func (a catalogAPI) managerDirectories(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		allow(w, http.MethodGet)
		return
	}
	cid := strings.TrimSpace(r.URL.Query().Get("cid"))
	if cid == "" {
		cid = "0"
	}
	directories, err := a.account.ListDirectories(r.Context(), cid)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "directories_failed", err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"cid": cid, "directories": directories})
}

func (a catalogAPI) clientApp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		allow(w, http.MethodGet)
		return
	}
	if _, ok := a.authorizeDevice(w, r); !ok {
		return
	}
	public := a.account.Public()
	if !public.AppConfigured {
		writeAPIError(w, http.StatusServiceUnavailable, "app_not_configured", "管理员尚未配置 115 Open AppID")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"client_id": public.ClientID})
}

func (a catalogAPI) search(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		allow(w, http.MethodGet)
		return
	}
	authorization, ok := a.authorizeDevice(w, r)
	if !ok {
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" || len([]rune(query)) > 200 {
		writeAPIError(w, http.StatusUnprocessableEntity, "invalid_query", "搜索关键词不能为空且不能超过 200 个字符")
		return
	}
	cfg := a.account.Public()
	if !cfg.Bound {
		writeAPIError(w, http.StatusServiceUnavailable, "catalog_not_ready", "管理员尚未绑定资源 115 账号")
		return
	}
	files, err := a.account.Search(r.Context(), query, cfg.FolderID, catalogSearchLimit)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "catalog_search_failed", err.Error())
		return
	}
	parentNames := make(map[string]string)
	for _, file := range files {
		if file.IsDir() {
			parentNames[file.FileID] = file.FileName
		}
	}
	lookups := 0
	for _, file := range files {
		if file.IsDir() || file.ParentID == "" || parentNames[file.ParentID] != "" || lookups >= 20 {
			continue
		}
		lookups++
		if info, infoErr := a.account.FolderInfo(r.Context(), file.ParentID); infoErr == nil {
			parentNames[file.ParentID] = strings.TrimSpace(info.FileName)
		}
	}

	now := a.now().UTC()
	items := make([]catalogSearchItem, 0, len(files))
	for _, file := range files {
		name := strings.TrimSpace(file.FileName)
		if name == "" || len(name) > 1024 {
			continue
		}
		ticket := resourceTicket{
			Version: 2, DeviceID: authorization.DeviceID, FileID: file.FileID, ParentID: file.ParentID,
			Name: name, Size: file.Size(), SHA1: strings.ToUpper(strings.TrimSpace(file.SHA1)),
			PickCode: strings.TrimSpace(file.PickCode), IsDir: file.IsDir(), ExpiresAt: now.Add(resourceTicketTTL).Unix(),
		}
		if !ticket.IsDir && (ticket.Size <= 0 || ticket.SHA1 == "" || ticket.PickCode == "") {
			continue
		}
		resourceID, sealErr := a.codec.seal(ticket)
		if sealErr != nil {
			writeAPIError(w, http.StatusInternalServerError, "ticket_failed", "无法生成资源凭据")
			return
		}
		groupID := strings.TrimSpace(file.ParentID)
		groupName := strings.TrimSpace(parentNames[groupID])
		if file.IsDir() {
			groupID, groupName = file.FileID, name
		}
		if groupID == "" {
			groupID = "file:" + file.FileID
		}
		items = append(items, catalogSearchItem{
			ResourceID: resourceID, Name: name, GroupID: groupID, GroupName: groupName,
			Size: ticket.Size, IsDir: ticket.IsDir, ModifiedAt: parse115Time(file.UserUTime),
		})
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a catalogAPI) prepare(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		allow(w, http.MethodPost)
		return
	}
	authorization, ok := a.authorizeDevice(w, r)
	if !ok {
		return
	}
	var request struct {
		ResourceID string `json:"resource_id"`
	}
	if err := decodeManagerJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_resource_id", "资源凭据格式无效")
		return
	}
	ticket, ok := a.openTicket(w, authorization.DeviceID, request.ResourceID)
	if !ok {
		return
	}
	end := preIDBytes - 1
	if ticket.Size <= preIDBytes {
		end = ticket.Size - 1
	}
	preID, err := a.ownerRangeSHA1(r.Context(), ticket, 0, end)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "prepare_failed", err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"transfer": map[string]any{
		"name": ticket.Name, "size": ticket.Size, "sha1": ticket.SHA1, "preid": preID,
	}})
}

func (a catalogAPI) sign(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		allow(w, http.MethodPost)
		return
	}
	authorization, ok := a.authorizeDevice(w, r)
	if !ok {
		return
	}
	var request struct {
		ResourceID string `json:"resource_id"`
		SignCheck  string `json:"sign_check"`
	}
	if err := decodeManagerJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "二次校验请求无效")
		return
	}
	ticket, ok := a.openTicket(w, authorization.DeviceID, request.ResourceID)
	if !ok {
		return
	}
	start, end, err := parseSignRange(request.SignCheck, ticket.Size)
	if err != nil {
		writeAPIError(w, http.StatusUnprocessableEntity, "invalid_sign_range", err.Error())
		return
	}
	value, err := a.ownerRangeSHA1(r.Context(), ticket, start, end)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "sign_failed", err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"sign_val": value})
}

func (a catalogAPI) openTicket(w http.ResponseWriter, deviceID, resourceID string) (resourceTicket, bool) {
	ticket, err := a.codec.open(strings.TrimSpace(resourceID))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_resource_id", "资源凭据无效或已损坏")
		return resourceTicket{}, false
	}
	if ticket.ExpiresAt <= a.now().UTC().Unix() {
		writeAPIError(w, http.StatusGone, "resource_id_expired", "资源凭据已过期，请重新搜索")
		return resourceTicket{}, false
	}
	if ticket.DeviceID != deviceID {
		writeAPIError(w, http.StatusForbidden, "resource_id_device_mismatch", "资源凭据不属于当前设备")
		return resourceTicket{}, false
	}
	if ticket.IsDir {
		writeAPIError(w, http.StatusUnprocessableEntity, "directory_not_downloadable", "请选择具体影视文件")
		return resourceTicket{}, false
	}
	return ticket, true
}

func (a catalogAPI) ownerRangeSHA1(ctx context.Context, ticket resourceTicket, start, end int64) (string, error) {
	if start < 0 || end < start || end >= ticket.Size {
		return "", errors.New("资源校验范围无效")
	}
	download, err := a.account.DownloadURL(ctx, ticket.PickCode)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(strings.TrimSpace(download.URL.URL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", errors.New("115 返回的资源地址无效")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("User-Agent", open115.DefaultUserAgent)
	request.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	response, err := a.http.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusPartialContent && !(response.StatusCode == http.StatusOK && start == 0) {
		return "", fmt.Errorf("读取 115 校验片段失败：HTTP %d", response.StatusCode)
	}
	length := end - start + 1
	hash := sha1.New()
	written, err := io.CopyN(hash, response.Body, length)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if written != length {
		return "", errors.New("115 返回的校验片段不完整")
	}
	return strings.ToUpper(hex.EncodeToString(hash.Sum(nil))), nil
}

func parseSignRange(value string, size int64) (int64, int64, error) {
	parts := strings.Split(strings.TrimSpace(value), "-")
	if len(parts) != 2 {
		return 0, 0, errors.New("115 二次校验范围格式无效")
	}
	start, err1 := strconv.ParseInt(parts[0], 10, 64)
	end, err2 := strconv.ParseInt(parts[1], 10, 64)
	if err1 != nil || err2 != nil || start < 0 || end < start || end >= size || end-start+1 > maxSignBytes {
		return 0, 0, errors.New("115 二次校验范围超出资源边界")
	}
	return start, end, nil
}

func (a catalogAPI) authorizeDevice(w http.ResponseWriter, r *http.Request) (licensing.HeartbeatResponse, bool) {
	deviceID := strings.TrimSpace(r.Header.Get("X-BossTransfer-Device-ID"))
	if deviceID == "" {
		writeAPIError(w, http.StatusUnauthorized, "device_required", "客户端设备身份缺失")
		return licensing.HeartbeatResponse{}, false
	}
	authorization, err := a.licenses.AuthorizeDevice(deviceID, r.Header.Get("Authorization"))
	if err != nil {
		writeLicensingError(w, err)
		return licensing.HeartbeatResponse{}, false
	}
	return authorization, true
}

type catalogSearchItem struct {
	ResourceID string     `json:"resource_id"`
	Name       string     `json:"name"`
	GroupID    string     `json:"group_id"`
	GroupName  string     `json:"group_name"`
	Size       int64      `json:"size"`
	IsDir      bool       `json:"is_dir"`
	ModifiedAt *time.Time `json:"modified_at,omitempty"`
}

type resourceTicket struct {
	Version   int    `json:"v"`
	DeviceID  string `json:"d"`
	FileID    string `json:"f"`
	ParentID  string `json:"p,omitempty"`
	Name      string `json:"n"`
	Size      int64  `json:"s"`
	SHA1      string `json:"h"`
	PickCode  string `json:"c"`
	IsDir     bool   `json:"i"`
	ExpiresAt int64  `json:"e"`
}

type resourceTicketCodec struct{ aead cipher.AEAD }

func newResourceTicketCodec(pepper []byte) (resourceTicketCodec, error) {
	if len(pepper) < 16 {
		return resourceTicketCodec{}, errors.New("catalog ticket pepper must be at least 16 bytes")
	}
	mac := hmac.New(sha256.New, pepper)
	_, _ = mac.Write([]byte(resourceTicketAAD))
	block, err := aes.NewCipher(mac.Sum(nil))
	if err != nil {
		return resourceTicketCodec{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return resourceTicketCodec{}, err
	}
	return resourceTicketCodec{aead: aead}, nil
}

func (c resourceTicketCodec) seal(ticket resourceTicket) (string, error) {
	plain, err := json.Marshal(ticket)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := c.aead.Seal(nonce, nonce, plain, []byte(resourceTicketAAD))
	return "bt2." + base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (c resourceTicketCodec) open(value string) (resourceTicket, error) {
	if !strings.HasPrefix(value, "bt2.") || len(value) > 8192 {
		return resourceTicket{}, errors.New("invalid resource ticket")
	}
	encoded := strings.TrimPrefix(value, "bt2.")
	sealed, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(sealed) < c.aead.NonceSize()+c.aead.Overhead() || base64.RawURLEncoding.EncodeToString(sealed) != encoded {
		return resourceTicket{}, errors.New("invalid resource ticket")
	}
	plain, err := c.aead.Open(nil, sealed[:c.aead.NonceSize()], sealed[c.aead.NonceSize():], []byte(resourceTicketAAD))
	if err != nil {
		return resourceTicket{}, errors.New("invalid resource ticket")
	}
	var ticket resourceTicket
	if err := json.Unmarshal(plain, &ticket); err != nil || ticket.Version != 2 || ticket.DeviceID == "" || ticket.FileID == "" || ticket.Name == "" || ticket.ExpiresAt <= 0 || ticket.Size < 0 {
		return resourceTicket{}, errors.New("invalid resource ticket")
	}
	if !ticket.IsDir && (ticket.Size <= 0 || ticket.SHA1 == "" || ticket.PickCode == "") {
		return resourceTicket{}, errors.New("invalid resource ticket")
	}
	return ticket, nil
}

func parse115Time(value string) *time.Time {
	seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || seconds <= 0 {
		return nil
	}
	parsed := time.Unix(seconds, 0).UTC()
	return &parsed
}
