package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"bosstransfer/internal/adapters/clouddrive2"
	"bosstransfer/internal/buildinfo"
	"bosstransfer/internal/httpapi"
	"bosstransfer/internal/setup"
	"bosstransfer/internal/transfer"
)

const cloudDriveInspectTimeout = 8 * time.Second

type cloudDriveClientFactory func(string) (clouddrive2.FileClient, error)

type clientSetupApp struct {
	transfer       *transfer.Service
	store          *setup.Store
	newCloudDrive  cloudDriveClientFactory
	sourceRoot     string
	targetRoot     string
	sourceHostRoot string
	targetHostRoot string
}

type directoryEntry struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	ModifiedAt string `json:"modified_at"`
}

type directoryListing struct {
	Kind         string           `json:"kind"`
	Current      string           `json:"current"`
	Parent       string           `json:"parent,omitempty"`
	HostPath     string           `json:"host_path"`
	InternalPath string           `json:"internal_path"`
	Writable     bool             `json:"writable"`
	Directories  []directoryEntry `json:"directories"`
}

type cloudDirectoryEntry struct {
	Name               string `json:"name"`
	Path               string `json:"path"`
	CanOfflineDownload bool   `json:"can_offline_download"`
}

type cloudDirectoryListing struct {
	MountPoint        string                `json:"mount_point"`
	Root              string                `json:"root"`
	Current           string                `json:"current"`
	Parent            string                `json:"parent,omitempty"`
	CurrentSelectable bool                  `json:"current_selectable"`
	Directories       []cloudDirectoryEntry `json:"directories"`
}

func newClientSetupApp(transferService *transfer.Service, store *setup.Store, sourceRoot, targetRoot, sourceHostRoot, targetHostRoot string) *clientSetupApp {
	return &clientSetupApp{
		transfer:       transferService,
		store:          store,
		newCloudDrive:  clouddrive2.NewFileClient,
		sourceRoot:     sourceRoot,
		targetRoot:     targetRoot,
		sourceHostRoot: sourceHostRoot,
		targetHostRoot: targetHostRoot,
	}
}

func (a *clientSetupApp) setupHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.writeSetup(w, http.StatusOK)
	case http.MethodPut:
		a.applySetup(w, r)
	default:
		w.Header().Set("Allow", "GET, PUT")
		httpapi.WriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
	}
}

func (a *clientSetupApp) connectHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		httpapi.WriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	var payload struct {
		Address  string `json:"address"`
		AuthMode string `json:"auth_mode"`
		Username string `json:"username"`
		Password string `json:"password"`
		TOTPCode string `json:"totp_code"`
		Token    string `json:"token"`
	}
	if err := decodeJSON(w, r, &payload); err != nil {
		httpapi.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json", "message": err.Error()})
		return
	}
	current := a.store.Get()
	address := strings.TrimSpace(payload.Address)
	if address == "" {
		address = current.CloudDrive2Address
	}
	normalizedAddress, _, err := normalizeCloudDriveEndpoint(address)
	if err != nil {
		httpapi.WriteJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "invalid_address", "message": err.Error()})
		return
	}
	authMode := strings.ToLower(strings.TrimSpace(payload.AuthMode))
	if authMode != "password" {
		authMode = "token"
	}
	candidate := current
	candidate.CloudDrive2Address = address
	candidate.AuthMode = authMode
	currentAddress, _, currentErr := normalizeCloudDriveEndpoint(current.CloudDrive2Address)
	sameEndpoint := currentErr == nil && normalizedAddress == currentAddress && current.AuthMode == authMode
	if !sameEndpoint {
		// Mount paths are scoped to the endpoint where they were discovered.
		candidate.MountPoint = ""
		candidate.CloudTargetDir = ""
	}
	if authMode == "password" {
		candidate.Username = strings.TrimSpace(payload.Username)
		candidate.Password = payload.Password
		candidate.Token = ""
		if sameEndpoint && candidate.Username == "" {
			candidate.Username = current.Username
		}
		if sameEndpoint && candidate.Password == "" {
			candidate.Password = current.Password
		}
		if candidate.Username == "" || candidate.Password == "" {
			if !sameEndpoint {
				httpapi.WriteJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "credentials_required_for_address_change", "message": "更换地址或认证方式时必须重新输入 CloudDrive2 账号和密码"})
				return
			}
			httpapi.WriteJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "credentials_required", "message": "请输入 CloudDrive2 账号和密码"})
			return
		}
	} else {
		candidate.Token = strings.TrimSpace(payload.Token)
		candidate.Username, candidate.Password = "", ""
		if sameEndpoint && candidate.Token == "" {
			candidate.Token = current.Token
		}
		if candidate.Token == "" {
			if !sameEndpoint {
				httpapi.WriteJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "token_required_for_address_change", "message": "更换地址或认证方式时必须重新输入 CloudDrive2 API Token"})
				return
			}
			httpapi.WriteJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "token_required", "message": "请输入 CloudDrive2 API Token"})
			return
		}
	}
	if err := validatePrivateEndpoint(r.Context(), address); err != nil {
		httpapi.WriteJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "invalid_address", "message": err.Error()})
		return
	}
	connection, credential, err := a.inspectCloudDriveWithCredential(r.Context(), candidate, payload.TOTPCode)
	if err != nil {
		httpapi.WriteJSON(w, http.StatusBadGateway, map[string]string{"error": "clouddrive_connection_failed", "message": err.Error()})
		return
	}
	// Password login can require a current TOTP code. Keep the resulting bearer
	// token encrypted so later CloudDrive2 downloads do not prompt on every job.
	if candidate.AuthMode == "password" {
		candidate.Token = credential
	}
	if err := a.store.Update(candidate); err != nil {
		httpapi.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "save_failed", "message": "连接成功，但本机配置保存失败"})
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"connection": connection,
		"setup":      a.store.Public(),
	})
}

func (a *clientSetupApp) mountsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		httpapi.WriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	cfg := a.store.Get()
	if (cfg.AuthMode == "password" && (cfg.Username == "" || cfg.Password == "")) || (cfg.AuthMode != "password" && cfg.Token == "") {
		httpapi.WriteJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "credentials_required", "message": "尚未保存 CloudDrive2 登录信息"})
		return
	}
	connection, err := a.inspectCloudDrive(r.Context(), cfg, "")
	if err != nil {
		httpapi.WriteJSON(w, http.StatusBadGateway, map[string]string{"error": "clouddrive_connection_failed", "message": err.Error()})
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"connection": connection})
}

func (a *clientSetupApp) cloudDirectoriesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		httpapi.WriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	cfg := a.store.Get()
	mountPoint := strings.TrimSpace(r.URL.Query().Get("mount_point"))
	if mountPoint == "" {
		mountPoint = cfg.MountPoint
	}
	listing, err := a.listCloudDirectories(r.Context(), cfg, mountPoint, r.URL.Query().Get("path"))
	if err != nil {
		httpapi.WriteJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "cloud_directory_unavailable", "message": err.Error()})
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, listing)
}

func (a *clientSetupApp) directoriesHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		kind := strings.TrimSpace(r.URL.Query().Get("kind"))
		relative := strings.TrimSpace(r.URL.Query().Get("path"))
		listing, err := a.listDirectories(kind, relative)
		if err != nil {
			httpapi.WriteJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "directory_unavailable", "message": err.Error()})
			return
		}
		httpapi.WriteJSON(w, http.StatusOK, listing)
	case http.MethodPost:
		a.createDirectory(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		httpapi.WriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
	}
}

func (a *clientSetupApp) applySetup(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		MountPoint     string `json:"mount_point"`
		CloudTargetDir string `json:"cloud_target_dir"`
		SourceRelative string `json:"source_relative"`
		TargetRelative string `json:"target_relative"`
	}
	if err := decodeJSON(w, r, &payload); err != nil {
		httpapi.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json", "message": err.Error()})
		return
	}
	payload.MountPoint = strings.TrimSpace(payload.MountPoint)
	cloudTargetDir := ""
	if payload.MountPoint != "" {
		setupCfg := a.store.Get()
		var err error
		cloudTargetDir, err = a.validateCloudDownloadTarget(r.Context(), setupCfg, payload.MountPoint, payload.CloudTargetDir)
		if err != nil {
			httpapi.WriteJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "cloud_target_unavailable", "message": err.Error()})
			return
		}
	}
	sourcePath, sourceRel, err := transfer.SafeJoin(a.sourceRoot, payload.SourceRelative)
	if err != nil {
		httpapi.WriteJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "invalid_source", "message": "115 内容目录超出允许范围"})
		return
	}
	if err := ensurePathInside(a.sourceRoot, sourcePath); err != nil {
		httpapi.WriteJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "invalid_source", "message": err.Error()})
		return
	}
	targetPath, targetRel, err := transfer.SafeJoin(a.targetRoot, payload.TargetRelative)
	if err != nil {
		httpapi.WriteJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "invalid_target", "message": "本地保存目录超出允许范围"})
		return
	}
	if err := ensurePathInside(a.targetRoot, targetPath); err != nil {
		httpapi.WriteJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "invalid_target", "message": err.Error()})
		return
	}
	oldTransfer := a.transfer.Config()
	updated := oldTransfer
	updated.SourceDir = sourcePath
	updated.TargetDir = targetPath
	if _, err := a.transfer.UpdateConfig(updated); err != nil {
		httpapi.WriteJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "invalid_directory", "message": err.Error()})
		return
	}
	oldSetup := a.store.Get()
	newSetup := oldSetup
	newSetup.MountPoint = payload.MountPoint
	newSetup.CloudTargetDir = cloudTargetDir
	newSetup.SourceRelative = sourceRel
	newSetup.TargetRelative = targetRel
	if err := a.store.Update(newSetup); err != nil {
		_, _ = a.transfer.UpdateConfig(oldTransfer)
		httpapi.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "save_failed", "message": "目录有效，但本机配置保存失败"})
		return
	}
	a.writeSetup(w, http.StatusOK)
}

func (a *clientSetupApp) listCloudDirectories(ctx context.Context, cfg setup.Config, mountPoint, current string) (cloudDirectoryListing, error) {
	if strings.TrimSpace(mountPoint) == "" {
		return cloudDirectoryListing{}, errors.New("请先选择一个 CloudDrive2 挂载点")
	}
	client, err := a.newCloudDrive(cfg.CloudDrive2Address)
	if err != nil {
		return cloudDirectoryListing{}, fmt.Errorf("CloudDrive2 地址无效：%w", err)
	}
	defer client.Close()
	credential, err := cloudDriveCredential(ctx, client, cfg)
	if err != nil {
		return cloudDirectoryListing{}, err
	}
	mounts, err := client.GetMountPoints(ctx, credential)
	if err != nil {
		return cloudDirectoryListing{}, fmt.Errorf("无法读取 CloudDrive2 挂载点：%w", err)
	}
	root := ""
	for _, mount := range mounts {
		if mount.MountPoint == mountPoint && mount.IsMounted {
			root, err = normalizeCloudDirectoryPath(mount.SourceDir)
			if err != nil {
				return cloudDirectoryListing{}, errors.New("CloudDrive2 挂载根目录无效")
			}
			break
		}
	}
	if root == "" {
		return cloudDirectoryListing{}, errors.New("所选 CloudDrive2 挂载点不存在或当前未挂载，请重新检测")
	}
	current = strings.TrimSpace(current)
	if current == "" {
		current = root
	}
	current, err = normalizeCloudDirectoryPath(current)
	if err != nil || !cloudPathWithin(root, current) {
		return cloudDirectoryListing{}, errors.New("CloudDrive2 目录超出所选挂载范围")
	}
	currentSelectable := false
	// CloudDrive2 exposes offline-download capability on the directory entry
	// returned by its parent. Checking the parent also supports installations
	// that mount a cloud root such as /115 directly instead of mounting the
	// aggregate root. The aggregate root itself is never a valid destination.
	if current != "/" {
		parentFiles, parentErr := client.ListFiles(ctx, credential, path.Dir(current), 500)
		if parentErr != nil {
			return cloudDirectoryListing{}, fmt.Errorf("无法验证 CloudDrive2 目录能力：%w", parentErr)
		}
		for _, file := range parentFiles {
			fullPath, pathErr := normalizeCloudDirectoryPath(file.FullPath)
			if pathErr == nil && fullPath == current && file.IsDir && !file.IsForbidden {
				currentSelectable = file.CanOfflineDownload
				break
			}
		}
	}
	files, err := client.ListFiles(ctx, credential, current, 500)
	if err != nil {
		return cloudDirectoryListing{}, fmt.Errorf("无法读取 CloudDrive2 目录：%w", err)
	}
	directories := make([]cloudDirectoryEntry, 0)
	for _, file := range files {
		if !file.IsDir || file.IsForbidden {
			continue
		}
		fullPath, pathErr := normalizeCloudDirectoryPath(file.FullPath)
		if pathErr != nil || fullPath == current || !cloudPathWithin(root, fullPath) {
			continue
		}
		name := strings.TrimSpace(file.Name)
		if name == "" {
			name = path.Base(fullPath)
		}
		directories = append(directories, cloudDirectoryEntry{Name: name, Path: fullPath, CanOfflineDownload: file.CanOfflineDownload})
	}
	sort.Slice(directories, func(i, j int) bool {
		return strings.ToLower(directories[i].Name) < strings.ToLower(directories[j].Name)
	})
	parent := ""
	if current != root {
		parent = path.Dir(current)
		if !cloudPathWithin(root, parent) {
			parent = root
		}
	}
	return cloudDirectoryListing{
		MountPoint: mountPoint, Root: root, Current: current, Parent: parent,
		CurrentSelectable: currentSelectable,
		Directories:       directories,
	}, nil
}

func (a *clientSetupApp) validateCloudDownloadTarget(ctx context.Context, cfg setup.Config, mountPoint, target string) (string, error) {
	target, err := normalizeCloudDirectoryPath(target)
	if err != nil || target == "/" {
		return "", errors.New("请选择标记为“支持离线下载”的具体云盘目录，不能使用聚合根目录")
	}
	listing, err := a.listCloudDirectories(ctx, cfg, mountPoint, target)
	if err != nil {
		return "", err
	}
	if listing.Current == target && listing.CurrentSelectable {
		return target, nil
	}
	return "", errors.New("所选目录不支持 CloudDrive2 离线下载，请选择带“支持离线下载”标记的云盘目录")
}

func cloudDriveCredential(ctx context.Context, client clouddrive2.Client, cfg setup.Config) (string, error) {
	credential := strings.TrimSpace(cfg.Token)
	if cfg.AuthMode == "password" && credential == "" {
		authentication, err := client.Authenticate(ctx, cfg.Username, cfg.Password, "")
		if err != nil {
			return "", fmt.Errorf("CloudDrive2 登录失败：%w", err)
		}
		credential = strings.TrimSpace(authentication.Token)
	}
	if credential == "" {
		return "", errors.New("CloudDrive2 凭据缺失，请重新连接")
	}
	return credential, nil
}

func normalizeCloudDirectoryPath(value string) (string, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, `\`, "/"))
	if value == "" || value == "." {
		return "/", nil
	}
	if strings.ContainsRune(value, '\x00') || !strings.HasPrefix(value, "/") {
		return "", errors.New("CloudDrive2 目录必须是绝对路径")
	}
	cleaned := path.Clean(value)
	if cleaned == "." || cleaned == "" {
		cleaned = "/"
	}
	return cleaned, nil
}

func cloudPathWithin(root, candidate string) bool {
	if root == "/" {
		return strings.HasPrefix(candidate, "/")
	}
	return candidate == root || strings.HasPrefix(candidate, root+"/")
}

func (a *clientSetupApp) createDirectory(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Kind   string `json:"kind"`
		Parent string `json:"parent"`
		Name   string `json:"name"`
	}
	if err := decodeJSON(w, r, &payload); err != nil {
		httpapi.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json", "message": err.Error()})
		return
	}
	if payload.Kind != "target" {
		httpapi.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "read_only", "message": "115 来源目录为只读，不能在这里新建文件夹"})
		return
	}
	name := strings.TrimSpace(payload.Name)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\\`) {
		httpapi.WriteJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "invalid_name", "message": "文件夹名称不能为空，也不能包含路径分隔符"})
		return
	}
	parent, _, err := transfer.SafeJoin(a.targetRoot, payload.Parent)
	if err != nil {
		httpapi.WriteJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "invalid_parent", "message": "父目录超出允许范围"})
		return
	}
	destination := filepath.Join(parent, name)
	if err := ensurePathInside(a.targetRoot, destination); err != nil {
		httpapi.WriteJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "invalid_path", "message": err.Error()})
		return
	}
	if err := os.Mkdir(destination, 0o755); err != nil {
		if errors.Is(err, os.ErrExist) {
			httpapi.WriteJSON(w, http.StatusConflict, map[string]string{"error": "already_exists", "message": "同名文件夹已经存在"})
			return
		}
		httpapi.WriteJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "create_failed", "message": "无法创建文件夹：" + err.Error()})
		return
	}
	listing, err := a.listDirectories("target", payload.Parent)
	if err != nil {
		httpapi.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "list_failed", "message": err.Error()})
		return
	}
	httpapi.WriteJSON(w, http.StatusCreated, listing)
}

func (a *clientSetupApp) inspectCloudDrive(ctx context.Context, cfg setup.Config, totpCode string) (map[string]any, error) {
	connection, _, err := a.inspectCloudDriveWithCredential(ctx, cfg, totpCode)
	return connection, err
}

func (a *clientSetupApp) inspectCloudDriveWithCredential(ctx context.Context, cfg setup.Config, totpCode string) (map[string]any, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	inspectCtx, cancel := context.WithTimeout(ctx, cloudDriveInspectTimeout)
	defer cancel()

	client, err := a.newCloudDrive(cfg.CloudDrive2Address)
	if err != nil {
		return nil, "", fmt.Errorf("CloudDrive2 地址无效：%w", err)
	}
	defer client.Close()
	system, err := client.GetSystemInfo(inspectCtx)
	if err != nil {
		return nil, "", fmt.Errorf("无法连接 CloudDrive2：%w", err)
	}
	if !system.SystemReady || system.HasError {
		message := strings.TrimSpace(system.SystemMessage)
		if message == "" {
			message = "CloudDrive2 服务尚未就绪"
		}
		return nil, "", errors.New(message)
	}
	credential := cfg.Token
	tokenInfo := clouddrive2.TokenInfo{}
	if cfg.AuthMode == "password" {
		// The initial connection clears cfg.Token and authenticates with the
		// supplied TOTP code. Later mount refreshes and setup saves reuse the
		// encrypted session token, otherwise TOTP-enabled accounts would be
		// forced to enter a new code for every local action.
		if strings.TrimSpace(credential) == "" {
			authentication, authErr := client.Authenticate(inspectCtx, cfg.Username, cfg.Password, totpCode)
			if authErr != nil {
				return nil, "", fmt.Errorf("CloudDrive2 登录失败：%w", authErr)
			}
			credential = authentication.Token
		}
	} else {
		var tokenErr error
		tokenInfo, tokenErr = client.GetApiTokenInfo(inspectCtx, cfg.Token)
		if tokenErr != nil {
			return nil, "", fmt.Errorf("API Token 无效或已过期：%w", tokenErr)
		}
		if !tokenInfo.Permissions.CanDiscoverMounts() {
			return nil, "", errors.New("API Token 缺少“获取挂载点”权限，请在 CloudDrive2 中编辑后重试")
		}
		if !tokenInfo.Permissions.AllowOfflineDownload {
			return nil, "", errors.New("API Token 缺少“添加离线下载”权限，请在 CloudDrive2 中编辑后重试")
		}
	}
	mounts, err := client.GetMountPoints(inspectCtx, credential)
	if err != nil {
		return nil, "", fmt.Errorf("无法读取 CloudDrive2 挂载点：%w", err)
	}
	return map[string]any{
		"connected":           true,
		"auth_mode":           cfg.AuthMode,
		"username":            cfg.Username,
		"system_ready":        system.SystemReady,
		"logged_in":           system.IsLogin,
		"token_name":          tokenInfo.FriendlyName,
		"token_root":          tokenInfo.RootDir,
		"token_expires_in":    tokenInfo.ExpiresInSeconds,
		"token_never_expires": tokenInfo.NeverExpires,
		"mounts":              mounts,
	}, credential, nil
}

func (a *clientSetupApp) writeSetup(w http.ResponseWriter, status int) {
	cfg := a.store.Get()
	transferCfg := a.transfer.Config()
	httpapi.WriteJSON(w, status, map[string]any{
		"setup":   a.store.Public(),
		"version": buildinfo.Version,
		"paths": map[string]string{
			"source_host_root":     a.sourceHostRoot,
			"target_host_root":     a.targetHostRoot,
			"source_internal_root": a.sourceRoot,
			"target_internal_root": a.targetRoot,
			"source_host_selected": joinDisplayPath(a.sourceHostRoot, cfg.SourceRelative),
			"target_host_selected": joinDisplayPath(a.targetHostRoot, cfg.TargetRelative),
			"source_internal":      transferCfg.SourceDir,
			"target_internal":      transferCfg.TargetDir,
		},
		"checks": a.transfer.Checks(),
	})
}

func (a *clientSetupApp) listDirectories(kind, relative string) (directoryListing, error) {
	root := a.sourceRoot
	hostRoot := a.sourceHostRoot
	writable := false
	if kind == "target" {
		root = a.targetRoot
		hostRoot = a.targetHostRoot
		writable = true
	} else if kind != "source" {
		return directoryListing{}, errors.New("目录类型必须是 source 或 target")
	}
	current, cleanRel, err := transfer.SafeJoin(root, relative)
	if err != nil {
		return directoryListing{}, errors.New("目录超出允许范围")
	}
	if err := ensurePathInside(root, current); err != nil {
		return directoryListing{}, err
	}
	info, err := os.Stat(current)
	if err != nil {
		return directoryListing{}, fmt.Errorf("目录不可访问：%w", err)
	}
	if !info.IsDir() {
		return directoryListing{}, errors.New("选择的位置不是文件夹")
	}
	if writable {
		writable = directoryWritable(current)
	}
	entries, err := os.ReadDir(current)
	if err != nil {
		return directoryListing{}, fmt.Errorf("目录不可读取：%w", err)
	}
	directories := make([]directoryEntry, 0)
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		entryInfo, err := entry.Info()
		if err != nil {
			continue
		}
		rel := filepath.ToSlash(filepath.Join(cleanRel, entry.Name()))
		directories = append(directories, directoryEntry{Name: entry.Name(), Path: rel, ModifiedAt: entryInfo.ModTime().Format("2006-01-02 15:04")})
	}
	sort.Slice(directories, func(i, j int) bool {
		return strings.ToLower(directories[i].Name) < strings.ToLower(directories[j].Name)
	})
	parent := ""
	if cleanRel != "" {
		parent = filepath.ToSlash(filepath.Dir(cleanRel))
		if parent == "." {
			parent = ""
		}
	}
	return directoryListing{
		Kind:         kind,
		Current:      cleanRel,
		Parent:       parent,
		HostPath:     joinDisplayPath(hostRoot, cleanRel),
		InternalPath: current,
		Writable:     writable,
		Directories:  directories,
	}, nil
}

func directoryWritable(path string) bool {
	file, err := os.CreateTemp(path, ".bosstransfer-write-test-*")
	if err != nil {
		return false
	}
	name := file.Name()
	writeErr := error(nil)
	if _, err := file.Write([]byte{0}); err != nil {
		writeErr = err
	} else if err := file.Sync(); err != nil {
		writeErr = err
	}
	if err := file.Close(); writeErr == nil && err != nil {
		writeErr = err
	}
	removeErr := os.Remove(name)
	return writeErr == nil && removeErr == nil
}

func ensurePathInside(root, path string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	rootInfo, err := os.Lstat(rootAbs)
	if err != nil {
		return fmt.Errorf("根目录不可访问：%w", err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("根目录不能是符号链接")
	}
	if !rootInfo.IsDir() {
		return errors.New("根路径不是文件夹")
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return errors.New("目录超出允许范围")
	}
	if rel == "." {
		return nil
	}
	current := rootAbs
	parts := strings.Split(rel, string(os.PathSeparator))
	for index, part := range parts {
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) && index == len(parts)-1 {
				return nil
			}
			return fmt.Errorf("目录不可访问：%w", statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("不能选择符号链接目录")
		}
	}
	return nil
}

func joinDisplayPath(root, relative string) string {
	if strings.TrimSpace(relative) == "" {
		return filepath.ToSlash(root)
	}
	return filepath.ToSlash(filepath.Join(root, filepath.FromSlash(relative)))
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) error {
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
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("请求正文只能包含一个 JSON 对象")
		}
		return err
	}
	return nil
}

func validatePrivateEndpoint(_ context.Context, address string) error {
	_, host, err := normalizeCloudDriveEndpoint(address)
	if err != nil {
		return err
	}
	if strings.EqualFold(host, "host.docker.internal") || strings.EqualFold(host, "localhost") {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil {
		if isAllowedCloudDriveIP(ip) {
			return nil
		}
		return errors.New("出于安全考虑，只允许连接本机或局域网内的 CloudDrive2")
	}
	return errors.New("CloudDrive2 地址只允许 localhost、host.docker.internal 或局域网 IP，不能使用其他主机名")
}

func normalizeCloudDriveEndpoint(address string) (normalized string, host string, err error) {
	raw := strings.TrimSpace(address)
	if raw == "" {
		return "", "", errors.New("CloudDrive2 地址不能为空")
	}

	scheme := "http"
	hostPort := raw
	if strings.Contains(raw, "://") {
		parsed, parseErr := url.Parse(raw)
		if parseErr != nil || parsed.Host == "" {
			return "", "", errors.New("CloudDrive2 地址格式不正确")
		}
		scheme = strings.ToLower(parsed.Scheme)
		if scheme != "http" && scheme != "https" {
			return "", "", errors.New("CloudDrive2 地址只支持 http 或 https")
		}
		if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
			return "", "", errors.New("CloudDrive2 地址只能包含协议、主机和端口")
		}
		hostPort = parsed.Host
	}

	host, port, splitErr := net.SplitHostPort(hostPort)
	if splitErr != nil || strings.TrimSpace(host) == "" {
		return "", "", errors.New("CloudDrive2 地址必须包含端口，例如 host.docker.internal:19798")
	}
	if host != strings.TrimSpace(host) {
		return "", "", errors.New("CloudDrive2 主机名格式不正确")
	}
	portNumber, portErr := strconv.Atoi(port)
	if portErr != nil || portNumber < 1 || portNumber > 65535 {
		return "", "", errors.New("CloudDrive2 端口必须是 1 到 65535 之间的数字")
	}

	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	}
	normalized = scheme + "://" + net.JoinHostPort(host, strconv.Itoa(portNumber))
	return normalized, host, nil
}

func isAllowedCloudDriveIP(ip net.IP) bool {
	if ip == nil || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	ipv4 := ip.To4()
	if ipv4 == nil {
		return false
	}
	return ipv4[0] == 10 ||
		(ipv4[0] == 172 && ipv4[1] >= 16 && ipv4[1] <= 31) ||
		(ipv4[0] == 192 && ipv4[1] == 168)
}
