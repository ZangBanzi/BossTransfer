package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"bosstransfer/internal/account115"
	"bosstransfer/internal/adapters/clouddrive2"
	"bosstransfer/internal/adapters/open115"
	"bosstransfer/internal/adapters/qbittorrent"
	"bosstransfer/internal/catalogclient"
	"bosstransfer/internal/downloaderconfig"
	"bosstransfer/internal/httpapi"
	"bosstransfer/internal/licenseclient"
	"bosstransfer/internal/setup"
	"bosstransfer/internal/transfer"
)

type catalogGateway interface {
	Search(context.Context, string) ([]catalogclient.Item, error)
}

type catalogTransferGateway interface {
	Prepare(context.Context, string) (catalogclient.Transfer, error)
	Sign(context.Context, string, string) (string, error)
}

type customer115Gateway interface {
	Public() account115.PublicConfig
	UploadInit(context.Context, open115.UploadInitRequest) (open115.UploadInitResponse, error)
	DownloadURL(context.Context, string) (open115.Download, error)
}

type clientLicenseStatus interface {
	Status() licenseclient.Status
}

type cloudDownloadClientFactory func(string) (clouddrive2.FileClient, error)

type torrentDownloadClient interface {
	Test(context.Context) (qbittorrent.ConnectionInfo, error)
	Add(context.Context, qbittorrent.AddOptions) error
	AddTorrent(context.Context, string, []byte, qbittorrent.AddOptions) error
	CloseIdleConnections()
}

type torrentDownloadClientFactory func(downloaderconfig.Config) (torrentDownloadClient, error)

type clientDownloadAPI struct {
	transfer       *transfer.Service
	catalog        catalogGateway
	license        clientLicenseStatus
	setup          *setup.Store
	qbittorrent    *downloaderconfig.Store
	account115     customer115Gateway
	newCloudDrive  cloudDownloadClientFactory
	newQBittorrent torrentDownloadClientFactory
}

type downloaderOption struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Available   bool   `json:"available"`
	Reason      string `json:"reason,omitempty"`
	TorrentOnly bool   `json:"torrent_only,omitempty"`
}

type clientSearchGroup struct {
	ID            string           `json:"id"`
	Title         string           `json:"title"`
	Quality       string           `json:"quality,omitempty"`
	VideoCount    int              `json:"video_count"`
	TotalSize     int64            `json:"total_size"`
	TotalSizeText string           `json:"total_size_text"`
	Primary       *transfer.Entry  `json:"primary,omitempty"`
	Files         []transfer.Entry `json:"files"`
}

type mediaFolderCandidate struct {
	id    string
	title string
	key   string
}

var (
	mediaMetadataSuffix = regexp.MustCompile(`(?i)\s*-?\s*\{(?:tmdb(?:id)?[=-][^}]+|imdb(?:id)?[=-][^}]+)\}\s*$`)
	mediaCopySuffix     = regexp.MustCompile(`\s*\([0-9]{1,3}\)\s*$`)
	mediaYearPrefix     = regexp.MustCompile(`(?i)^(.+?)[ ._-]+((?:19|20)[0-9]{2})(?:[ ._-]|$)`)
	mediaReleaseSuffix  = regexp.MustCompile(`(?i)[ ._-]+(?:2160p|1080[pi]|720p|4k|uhd|s[0-9]{1,2}e[0-9]{1,3})(?:[ ._-]|$)`)
)

func newClientDownloadAPI(transferService *transfer.Service, catalog catalogGateway, license clientLicenseStatus, setupStore *setup.Store, qbStore *downloaderconfig.Store, accounts ...customer115Gateway) *clientDownloadAPI {
	api := &clientDownloadAPI{
		transfer:      transferService,
		catalog:       catalog,
		license:       license,
		setup:         setupStore,
		qbittorrent:   qbStore,
		newCloudDrive: clouddrive2.NewFileClient,
		newQBittorrent: func(config downloaderconfig.Config) (torrentDownloadClient, error) {
			httpClient := newPrivateQBittorrentHTTPClient()
			client, err := qbittorrent.NewClient(qbittorrent.Config{
				BaseURL:  config.BaseURL,
				Username: config.Username,
				Password: config.Password,
				APIKey:   config.APIKey,
			}, httpClient)
			if err != nil {
				httpClient.CloseIdleConnections()
				return nil, err
			}
			return client, nil
		},
	}
	if len(accounts) > 0 {
		api.account115 = accounts[0]
	}
	return api
}

func (a *clientDownloadAPI) searchHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		httpapi.WriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	items, err := a.catalog.Search(r.Context(), query)
	if err != nil {
		httpapi.WriteJSON(w, http.StatusBadGateway, map[string]string{"error": "catalog_search_failed", "message": err.Error()})
		return
	}
	results := make([]transfer.Entry, 0, len(items))
	for _, item := range items {
		modified := ""
		if item.ModifiedAt != nil {
			modified = item.ModifiedAt.Local().Format("2006-01-02 15:04")
		}
		sizeText := transfer.FormatBytes(item.Size)
		if item.IsDir {
			sizeText = "文件夹"
		}
		results = append(results, transfer.Entry{
			Name: item.Name, Path: item.ResourceID, Parent: "管理员资源库",
			GroupID: item.GroupID, GroupName: item.GroupName,
			Size: item.Size, SizeText: sizeText, IsDir: item.IsDir, ModifiedAt: modified,
		})
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"query": query, "results": results, "groups": buildClientSearchGroups(results), "downloaders": a.downloaders(),
	})
}

func buildClientSearchGroups(results []transfer.Entry) []clientSearchGroup {
	candidates := clientMediaFolderCandidates(results)
	candidateByKey := make(map[string]mediaFolderCandidate, len(candidates))
	for _, candidate := range candidates {
		candidateByKey[candidate.key] = candidate
	}
	groups := make([]clientSearchGroup, 0)
	indexes := make(map[string]int)
	for _, item := range results {
		groupID, title := resolveClientMediaFolder(item, candidates, candidateByKey)
		index, ok := indexes[groupID]
		if !ok {
			index = len(groups)
			indexes[groupID] = index
			groups = append(groups, clientSearchGroup{ID: groupID, Title: title, TotalSizeText: "-", Files: []transfer.Entry{}})
		}
		group := &groups[index]
		if item.IsDir || !isCustomerMediaFile(item.Name) {
			continue
		}
		group.Files = append(group.Files, item)
		group.VideoCount++
		group.TotalSize += item.Size
		if group.Primary == nil || item.Size > group.Primary.Size {
			copy := item
			group.Primary = &copy
		}
		if group.Quality == "" {
			group.Quality = mediaQuality(item.Name + " " + group.Title)
		}
	}
	for index := range groups {
		group := &groups[index]
		sort.SliceStable(group.Files, func(i, j int) bool { return group.Files[i].Name < group.Files[j].Name })
		if group.TotalSize > 0 {
			group.TotalSizeText = transfer.FormatBytes(group.TotalSize)
		}
	}
	visible := groups[:0]
	for _, group := range groups {
		if group.VideoCount > 0 {
			visible = append(visible, group)
		}
	}
	return visible
}

func clientMediaFolderCandidates(results []transfer.Entry) []mediaFolderCandidate {
	candidates := make([]mediaFolderCandidate, 0)
	seen := make(map[string]struct{})
	for _, item := range results {
		if !item.IsDir {
			continue
		}
		title := cleanMediaFolderTitle(firstNonEmpty(item.GroupName, item.Name))
		key := mediaMatchKey(title)
		if key == "" || isVirtualSearchFolder(title) {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		id := strings.TrimSpace(item.GroupID)
		if id == "" {
			id = "folder:" + key
		}
		candidates = append(candidates, mediaFolderCandidate{id: id, title: title, key: key})
	}
	sort.SliceStable(candidates, func(i, j int) bool { return len([]rune(candidates[i].key)) > len([]rune(candidates[j].key)) })
	return candidates
}

func resolveClientMediaFolder(item transfer.Entry, candidates []mediaFolderCandidate, candidateByKey map[string]mediaFolderCandidate) (string, string) {
	title := cleanMediaFolderTitle(firstNonEmpty(item.GroupName, item.Name))
	key := mediaMatchKey(title)
	if item.IsDir {
		if candidate, ok := candidateByKey[key]; ok {
			return candidate.id, candidate.title
		}
	}
	if isVirtualSearchFolder(title) {
		fileKey := mediaMatchKey(strings.TrimSuffix(item.Name, filepath.Ext(item.Name)))
		for _, candidate := range candidates {
			if strings.HasPrefix(fileKey, candidate.key) {
				return candidate.id, candidate.title
			}
		}
		title = inferMediaTitle(item.Name)
		key = mediaMatchKey(title)
	}
	if candidate, ok := candidateByKey[key]; ok {
		return candidate.id, candidate.title
	}
	if title == "" {
		title = inferMediaTitle(item.Name)
		key = mediaMatchKey(title)
	}
	id := strings.TrimSpace(item.GroupID)
	if id == "" || isVirtualSearchFolder(item.GroupName) {
		id = "inferred:" + key
	}
	if key == "" {
		id, title = firstNonEmpty(item.Path, item.Name), "未命名影片"
	}
	return id, title
}

func cleanMediaFolderTitle(value string) string {
	value = strings.TrimSpace(value)
	for {
		cleaned := strings.TrimSpace(mediaMetadataSuffix.ReplaceAllString(value, ""))
		if cleaned == value {
			break
		}
		value = cleaned
	}
	value = strings.TrimSpace(mediaCopySuffix.ReplaceAllString(value, ""))
	return strings.TrimSpace(strings.TrimSuffix(value, "-"))
}

func isVirtualSearchFolder(value string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(value)), "[search]")
}

func mediaMatchKey(value string) string {
	var builder strings.Builder
	for _, char := range strings.ToLower(strings.TrimSpace(value)) {
		if unicode.IsLetter(char) || unicode.IsDigit(char) {
			builder.WriteRune(char)
		}
	}
	return builder.String()
}

func inferMediaTitle(name string) string {
	value := strings.TrimSuffix(strings.TrimSpace(name), filepath.Ext(strings.TrimSpace(name)))
	if match := mediaYearPrefix.FindStringSubmatch(value); len(match) == 3 {
		return normalizeMediaTitleWords(match[1]) + " (" + match[2] + ")"
	}
	if location := mediaReleaseSuffix.FindStringIndex(value); location != nil {
		value = value[:location[0]]
	}
	value = normalizeMediaTitleWords(value)
	if value == "" {
		return "未命名影片"
	}
	return value
}

func normalizeMediaTitleWords(value string) string {
	value = strings.NewReplacer(".", " ", "_", " ").Replace(strings.TrimSpace(value))
	return strings.Join(strings.Fields(value), " ")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func isCustomerMediaFile(name string) bool {
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(name))) {
	case ".mkv", ".mp4", ".avi", ".mov", ".m2ts", ".ts", ".wmv", ".flv", ".webm", ".iso", ".torrent":
		return true
	default:
		return strings.HasPrefix(strings.ToLower(strings.TrimSpace(name)), "magnet:")
	}
}

func mediaQuality(value string) string {
	value = strings.ToLower(value)
	for _, candidate := range []struct{ token, label string }{
		{"2160p", "4K"}, {"uhd", "4K"}, {"4k", "4K"}, {"1080p", "1080P"}, {"1080i", "1080P"}, {"720p", "720P"},
	} {
		if strings.Contains(value, candidate.token) {
			return candidate.label
		}
	}
	return ""
}

func (a *clientDownloadAPI) downloadersHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		httpapi.WriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"downloaders": a.downloaders()})
}

func (a *clientDownloadAPI) downloadsHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/v1/client/downloads" {
		switch r.Method {
		case http.MethodGet:
			httpapi.WriteJSON(w, http.StatusOK, map[string]any{"tasks": a.transfer.Tasks()})
		case http.MethodPost:
			a.startDownload(w, r)
		default:
			w.Header().Set("Allow", "GET, POST")
			httpapi.WriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		}
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/client/downloads/")
	if id == "" || strings.Contains(id, "/") {
		httpapi.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		if task, ok := a.transfer.Task(id); ok {
			httpapi.WriteJSON(w, http.StatusOK, map[string]any{"task": task})
			return
		}
		httpapi.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
	case http.MethodDelete:
		if task, cancelled := a.transfer.CancelRemoteDownload(id); cancelled {
			httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"task": task})
			return
		}
		if _, ok := a.transfer.Task(id); !ok {
			httpapi.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		httpapi.WriteJSON(w, http.StatusConflict, map[string]string{"error": "task_not_cancellable", "message": "该任务已经结束或由外部下载器管理"})
	default:
		w.Header().Set("Allow", "GET, DELETE")
		httpapi.WriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
	}
}

func (a *clientDownloadAPI) startDownload(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		ResourceID string `json:"resource_id"`
		Path       string `json:"path"`
		Downloader string `json:"downloader"`
	}
	if err := decodeJSON(w, r, &payload); err != nil {
		httpapi.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json", "message": err.Error()})
		return
	}
	resourceID := strings.TrimSpace(payload.ResourceID)
	if resourceID == "" {
		resourceID = strings.TrimSpace(payload.Path)
	}
	downloader := strings.ToLower(strings.TrimSpace(payload.Downloader))
	if resourceID == "" || downloader == "" {
		httpapi.WriteJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "download_request_incomplete", "message": "请选择资源和下载方式"})
		return
	}
	option, authorized := a.downloader(downloader)
	if !authorized {
		httpapi.WriteJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "downloader_unknown", "message": "下载方式无效"})
		return
	}
	if a.account115 == nil || !a.account115.Public().Bound {
		httpapi.WriteJSON(w, http.StatusConflict, map[string]string{"error": "account_115_required", "message": "下载前必须先绑定你自己的 115 账号"})
		return
	}
	if !option.Available {
		httpapi.WriteJSON(w, http.StatusConflict, map[string]string{"error": "downloader_not_configured", "message": option.Reason})
		return
	}
	catalog, ok := a.catalog.(catalogTransferGateway)
	if !ok {
		httpapi.WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "catalog_prepare_unavailable", "message": "当前管理端不支持 115 秒传"})
		return
	}
	prepared, err := catalog.Prepare(r.Context(), resourceID)
	if err != nil {
		httpapi.WriteJSON(w, http.StatusBadGateway, map[string]string{"error": "catalog_prepare_failed", "message": err.Error()})
		return
	}
	customerPickCode, err := a.transferToCustomer115(r.Context(), catalog, resourceID, prepared)
	if err != nil {
		httpapi.WriteJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "rapid_transfer_failed", "message": err.Error()})
		return
	}

	var task transfer.Task
	switch downloader {
	case "system":
		download, linkErr := a.account115.DownloadURL(r.Context(), customerPickCode)
		if linkErr != nil {
			err = linkErr
			break
		}
		task, err = a.transfer.StartRemoteDownload(transfer.RemoteDownload{
			ResourceID: resourceID, Name: prepared.Name, Size: prepared.Size,
			URL: download.URL.URL, Headers: map[string]string{"User-Agent": open115.DefaultUserAgent},
		})
	case "clouddrive2":
		task, err = a.submitCloudDrive(r.Context(), resourceID, prepared)
	case "qbittorrent":
		download, linkErr := a.account115.DownloadURL(r.Context(), customerPickCode)
		if linkErr != nil {
			err = linkErr
			break
		}
		task, err = a.submitQBittorrent(r.Context(), resourceID, prepared.Name, download.URL.URL)
	default:
		err = errors.New("未知下载方式")
	}
	if err != nil {
		httpapi.WriteJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "download_failed", "message": err.Error()})
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"task": task})
}

func (a *clientDownloadAPI) transferToCustomer115(ctx context.Context, catalog catalogTransferGateway, resourceID string, prepared catalogclient.Transfer) (string, error) {
	request := open115.UploadInitRequest{
		FileName: prepared.Name, FileSize: prepared.Size, FileID: prepared.SHA1,
		PreID: prepared.PreID,
	}
	result, err := a.account115.UploadInit(ctx, request)
	if err != nil {
		return "", err
	}
	if result.Status == 6 || result.Status == 7 || result.Status == 8 {
		if strings.TrimSpace(result.SignKey) == "" || strings.TrimSpace(result.SignCheck) == "" {
			return "", errors.New("115 要求二次校验，但没有返回完整校验参数")
		}
		signValue, signErr := catalog.Sign(ctx, resourceID, result.SignCheck)
		if signErr != nil {
			return "", signErr
		}
		request.SignKey, request.SignVal = result.SignKey, signValue
		result, err = a.account115.UploadInit(ctx, request)
		if err != nil {
			return "", err
		}
	}
	if result.Status != 2 {
		return "", fmt.Errorf("115 秒传未命中（状态 %d），已停止以避免从管理员网盘直接下载", result.Status)
	}
	if strings.TrimSpace(result.PickCode) == "" {
		return "", errors.New("115 秒传成功但未返回客户文件提取码")
	}
	return strings.TrimSpace(result.PickCode), nil
}

func (a *clientDownloadAPI) submitCloudDrive(ctx context.Context, resourceID string, prepared catalogclient.Transfer) (transfer.Task, error) {
	config := a.setup.Get()
	client, err := a.newCloudDrive(config.CloudDrive2Address)
	if err != nil {
		return transfer.Task{}, err
	}
	defer client.Close()
	credential := strings.TrimSpace(config.Token)
	if credential == "" && config.AuthMode == "password" {
		authentication, authErr := client.Authenticate(ctx, config.Username, config.Password, "")
		if authErr != nil {
			return transfer.Task{}, errors.New("CloudDrive2 登录已过期，请到后端接入页重新连接")
		}
		credential = authentication.Token
	}
	if credential == "" {
		return transfer.Task{}, errors.New("CloudDrive2 凭据缺失，请重新连接")
	}
	remotePath := path.Join(config.CloudTargetDir, prepared.Name)
	link, err := client.GetDownloadLink(ctx, credential, remotePath, true)
	if errors.Is(err, clouddrive2.ErrTokenInvalid) && config.AuthMode == "password" {
		authentication, authErr := client.Authenticate(ctx, config.Username, config.Password, "")
		if authErr != nil || strings.TrimSpace(authentication.Token) == "" {
			return transfer.Task{}, errors.New("CloudDrive2 登录已过期，请到后端接入页重新验证；启用二步验证时需要填写新的验证码")
		}
		credential = strings.TrimSpace(authentication.Token)
		config.Token = credential
		if updateErr := a.setup.Update(config); updateErr != nil {
			return transfer.Task{}, errors.New("CloudDrive2 登录已刷新，但保存新会话失败")
		}
		link, err = client.GetDownloadLink(ctx, credential, remotePath, true)
	}
	if err != nil {
		return transfer.Task{}, fmt.Errorf("CloudDrive2 中没有找到刚秒传的文件；请确认 CloudDrive2 目录与 115 秒传目录一致: %w", err)
	}
	downloadURL, err := cloudDriveDownloadURL(config.CloudDrive2Address, firstNonEmpty(link.DirectURL, link.Path))
	if err != nil {
		return transfer.Task{}, err
	}
	headers := cloneHeaders(link.AdditionalHeaders)
	if strings.TrimSpace(link.UserAgent) != "" {
		headers["User-Agent"] = strings.TrimSpace(link.UserAgent)
	}
	return a.transfer.StartRemoteDownload(transfer.RemoteDownload{
		ResourceID: resourceID, Name: prepared.Name, Size: prepared.Size, URL: downloadURL, Headers: headers, Downloader: "clouddrive2",
	})
}

func (a *clientDownloadAPI) submitQBittorrent(ctx context.Context, resourceID, name, downloadURL string) (transfer.Task, error) {
	if !isTorrentSource(name, downloadURL) {
		return transfer.Task{}, errors.New("qBittorrent 只接收磁力链接或 .torrent 种子文件，请改用系统下载或 CloudDrive2")
	}
	config := a.qbittorrent.Config()
	client, err := a.newQBittorrent(config)
	if err != nil {
		return transfer.Task{}, err
	}
	defer client.CloseIdleConnections()
	if _, err := client.Test(ctx); err != nil {
		return transfer.Task{}, err
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(downloadURL)), "magnet:?") {
		if err := client.Add(ctx, qbittorrent.AddOptions{URL: downloadURL, SavePath: config.SavePath, VerifiedTorrentName: name}); err != nil {
			return transfer.Task{}, err
		}
		return a.transfer.RecordSubmitted(resourceID, name, "qbittorrent", config.SavePath), nil
	}
	payload, err := download115Torrent(ctx, downloadURL)
	if err != nil {
		return transfer.Task{}, err
	}
	if err := client.AddTorrent(ctx, name, payload, qbittorrent.AddOptions{SavePath: config.SavePath}); err != nil {
		return transfer.Task{}, err
	}
	return a.transfer.RecordSubmitted(resourceID, name, "qbittorrent", config.SavePath), nil
}

const maxTorrentFileSize = 16 << 20

func download115Torrent(ctx context.Context, source string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, errors.New("客户 115 返回的种子下载地址无效")
	}
	if request.URL.Scheme != "https" && request.URL.Scheme != "http" {
		return nil, errors.New("客户 115 返回的种子下载地址协议无效")
	}
	request.Header.Set("User-Agent", open115.DefaultUserAgent)
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(next *http.Request, previous []*http.Request) error {
			if len(previous) >= 5 {
				return errors.New("种子下载重定向次数过多")
			}
			next.Header.Set("User-Agent", open115.DefaultUserAgent)
			next.Header.Del("Referer")
			return nil
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("从客户 115 读取种子失败: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("客户 115 返回种子失败：HTTP %d", response.StatusCode)
	}
	if response.ContentLength > maxTorrentFileSize {
		return nil, errors.New("种子文件超过 16 MiB 安全上限")
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxTorrentFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("读取客户 115 种子失败: %w", err)
	}
	if len(payload) == 0 {
		return nil, errors.New("客户 115 返回了空种子文件")
	}
	if len(payload) > maxTorrentFileSize {
		return nil, errors.New("种子文件超过 16 MiB 安全上限")
	}
	return payload, nil
}

func (a *clientDownloadAPI) downloaders() []downloaderOption {
	status := a.license.Status()
	if !status.Activated {
		return []downloaderOption{}
	}
	options := make([]downloaderOption, 0, 3)
	accountReady := a.account115 != nil && a.account115.Public().Bound
	available := false
	reason := "请先在后端接入页选择可写的本地保存目录"
	for _, check := range a.transfer.Checks() {
		if check.Name == "target_writable" && check.Status == "ok" {
			available, reason = true, ""
		}
	}
	if !accountReady {
		available, reason = false, "请先绑定你自己的 115 账号"
	}
	options = append(options, downloaderOption{ID: "system", Name: "系统下载", Available: available, Reason: reason})

	config := a.setup.Get()
	credentialsReady := (config.AuthMode == "password" && config.Username != "" && config.Password != "") || (config.AuthMode != "password" && config.Token != "")
	cloudTarget, targetErr := normalizeCloudDirectoryPath(config.CloudTargetDir)
	cloudAvailable := accountReady && strings.TrimSpace(config.CloudDrive2Address) != "" && strings.TrimSpace(config.MountPoint) != "" && targetErr == nil && cloudTarget != "/" && credentialsReady
	cloudReason := "请先连接 CloudDrive2，并选择与 115 秒传目录对应的云端目录"
	if !accountReady {
		cloudReason = "请先绑定你自己的 115 账号"
	} else if cloudAvailable {
		cloudReason = ""
	}
	options = append(options, downloaderOption{ID: "clouddrive2", Name: "CloudDrive2 下载", Available: cloudAvailable, Reason: cloudReason})

	public := a.qbittorrent.Public()
	qbAvailable := accountReady && public.Available
	qbReason := "请先验证 qBittorrent 连接"
	if !accountReady {
		qbReason = "请先绑定你自己的 115 账号"
	} else if qbAvailable {
		qbReason = ""
	}
	options = append(options, downloaderOption{ID: "qbittorrent", Name: "qBittorrent", Available: qbAvailable, Reason: qbReason, TorrentOnly: true})
	return options
}

func (a *clientDownloadAPI) downloader(id string) (downloaderOption, bool) {
	for _, option := range a.downloaders() {
		if option.ID == id {
			return option, true
		}
	}
	return downloaderOption{}, false
}

func isTorrentSource(name, sourceURL string) bool {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(sourceURL)), "magnet:?") {
		return true
	}
	// The manager verifies the resource name. Signed URLs often use an opaque
	// path, so a trusted .torrent name is sufficient for qBittorrent.
	return strings.EqualFold(filepath.Ext(strings.TrimSpace(name)), ".torrent") || strings.EqualFold(filepath.Ext(strings.Split(strings.TrimSpace(sourceURL), "?")[0]), ".torrent")
}

func cloneHeaders(values map[string]string) map[string]string {
	result := make(map[string]string, len(values)+1)
	for key, value := range values {
		result[key] = value
	}
	return result
}

func cloudDriveDownloadURL(endpoint, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("CloudDrive2 未返回下载地址")
	}
	parsed, err := url.Parse(value)
	if err == nil && parsed.IsAbs() {
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return "", errors.New("CloudDrive2 下载地址协议无效")
		}
		return parsed.String(), nil
	}
	base, err := url.Parse("http://" + strings.TrimSpace(endpoint))
	if err != nil || base.Host == "" {
		return "", errors.New("CloudDrive2 地址无效")
	}
	base.Path = "/"
	base.RawQuery, base.Fragment = "", ""
	ref, err := url.Parse(value)
	if err != nil {
		return "", errors.New("CloudDrive2 下载地址无效")
	}
	return base.ResolveReference(ref).String(), nil
}
