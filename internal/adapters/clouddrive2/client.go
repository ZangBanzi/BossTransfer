package clouddrive2

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"bosstransfer/internal/adapters/clouddrive2/internal/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// DefaultTimeout bounds every CloudDrive2 RPC, including connection setup.
const DefaultTimeout = 4 * time.Second

const fileOperationTimeout = 30 * time.Second

const (
	searchOperationTimeout        = 25 * time.Second
	searchBranchConcurrency       = 4
	searchMaxCloudRoots           = 64
	searchMaxTraversalEntries     = 50_000
	searchMaxTraversalDirectories = 2_000
	searchMaxTraversalDepth       = 32
	searchMaxRemotePathLength     = 4_096
	searchMaxRemoteNameLength     = 1_024
)

var ErrTokenInvalid = errors.New("CloudDrive2 API Token 无效或已过期")

var errSearchTraversalLimit = errors.New("CloudDrive2 资源目录过大，搜索已达到安全上限，请缩小管理端开放目录")

// Client exposes the CloudDrive2 calls needed to validate a local integration.
// Implementations must not retain an API token after a call returns.
type Client interface {
	GetSystemInfo(ctx context.Context) (SystemInfo, error)
	Authenticate(ctx context.Context, username, password, totpCode string) (Authentication, error)
	GetApiTokenInfo(ctx context.Context, token string) (TokenInfo, error)
	GetMountPoints(ctx context.Context, token string) ([]MountPoint, error)
	Close() error
}

// FileClient adds the documented file and transfer operations. Setup code can
// depend on Client while catalog and downloader code asks for this interface.
type FileClient interface {
	Client
	ListFiles(ctx context.Context, token, path string, limit int) ([]File, error)
	SearchFiles(ctx context.Context, token, path, query string, limit int) ([]File, error)
	GetDownloadLink(ctx context.Context, token, path string, direct bool) (DownloadLink, error)
	CopyFiles(ctx context.Context, token string, paths []string, destination string) (FileOperation, error)
	AddOfflineFiles(ctx context.Context, token string, urls []string, destination string) (FileOperation, error)
	AddSharedLink(ctx context.Context, token, sharedURL, password, destination string) error
}

type Authentication struct {
	Token     string     `json:"-"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

type SystemInfo struct {
	IsLogin       bool   `json:"is_login"`
	UserName      string `json:"user_name,omitempty"`
	SystemReady   bool   `json:"system_ready"`
	SystemMessage string `json:"system_message,omitempty"`
	HasError      bool   `json:"has_error"`
}

// TokenInfo deliberately excludes the token value returned by CloudDrive2.
type TokenInfo struct {
	RootDir          string           `json:"root_dir,omitempty"`
	FriendlyName     string           `json:"friendly_name,omitempty"`
	ExpiresInSeconds uint64           `json:"expires_in_seconds,omitempty"`
	NeverExpires     bool             `json:"never_expires"`
	Permissions      TokenPermissions `json:"permissions"`
}

type TokenPermissions struct {
	AllowList             bool `json:"allow_list"`
	AllowSearch           bool `json:"allow_search"`
	AllowListLocal        bool `json:"allow_list_local"`
	AllowRead             bool `json:"allow_read"`
	AllowCopy             bool `json:"allow_copy"`
	AllowOfflineDownload  bool `json:"allow_offline_download"`
	AllowSharedLinks      bool `json:"allow_shared_links"`
	AllowGetMounts        bool `json:"allow_get_mounts"`
	AllowGetTransferTasks bool `json:"allow_get_transfer_tasks"`
	AllowGetCloudAPIs     bool `json:"allow_get_cloud_apis"`
	AllowPushMessage      bool `json:"allow_push_message"`
}

func (p TokenPermissions) CanDiscoverMounts() bool {
	return p.AllowGetMounts
}

type MountPoint struct {
	MountPoint string `json:"mount_point"`
	SourceDir  string `json:"source_dir"`
	Name       string `json:"name,omitempty"`
	IsMounted  bool   `json:"is_mounted"`
	FailReason string `json:"fail_reason,omitempty"`
	ReadOnly   bool   `json:"read_only"`
	AutoMount  bool   `json:"auto_mount"`
}

type File struct {
	ID                 string     `json:"id,omitempty"`
	Name               string     `json:"name"`
	FullPath           string     `json:"full_path"`
	Size               int64      `json:"size"`
	IsDir              bool       `json:"is_dir"`
	IsCloud            bool       `json:"is_cloud"`
	IsCloudRoot        bool       `json:"is_cloud_root,omitempty"`
	CanSearch          bool       `json:"can_search,omitempty"`
	CanOfflineDownload bool       `json:"can_offline_download,omitempty"`
	IsForbidden        bool       `json:"is_forbidden,omitempty"`
	ModifiedAt         *time.Time `json:"modified_at,omitempty"`
}

// DownloadLink contains a short-lived secret URL and optional request headers.
// It is intentionally an internal adapter model and must never be logged.
type DownloadLink struct {
	Path              string
	DirectURL         string
	UserAgent         string
	AdditionalHeaders map[string]string
	ExpiresInSeconds  uint64
}

type FileOperation struct {
	Success     bool
	ResultPaths []string
}

type grpcClient struct {
	conn    *grpc.ClientConn
	rpc     pb.CloudDriveFileSrvClient
	timeout time.Duration
}

// NewClient creates a CloudDrive2 client. address may be host:port,
// http://host:port, or https://host:port. Plain host:port and http use h2c;
// https uses the operating system trust store.
func NewClient(address string) (Client, error) {
	return newClient(address, DefaultTimeout)
}

func NewFileClient(address string) (FileClient, error) {
	return newClient(address, DefaultTimeout)
}

func newClient(address string, timeout time.Duration) (*grpcClient, error) {
	if timeout <= 0 {
		return nil, errors.New("CloudDrive2 调用超时时间必须大于 0")
	}

	target, transportCredentials, err := parseAddress(address)
	if err != nil {
		return nil, err
	}

	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(transportCredentials))
	if err != nil {
		return nil, errors.New("无法创建 CloudDrive2 gRPC 客户端")
	}

	return &grpcClient{
		conn:    conn,
		rpc:     pb.NewCloudDriveFileSrvClient(conn),
		timeout: timeout,
	}, nil
}

func (c *grpcClient) ListFiles(ctx context.Context, token, remotePath string, limit int) ([]File, error) {
	remotePath = strings.TrimSpace(remotePath)
	if remotePath == "" {
		remotePath = "/"
	}
	return c.receiveFiles(ctx, token, limit, func(callCtx context.Context) (grpc.ServerStreamingClient[pb.SubFilesReply], error) {
		return c.rpc.GetSubFiles(callCtx, &pb.ListSubFileRequest{Path: remotePath})
	}, operationListFiles)
}

func (c *grpcClient) SearchFiles(ctx context.Context, token, remotePath, query string, limit int) ([]File, error) {
	var ok bool
	remotePath, ok = normalizeSearchRemotePath(remotePath)
	if !ok {
		return nil, errors.New("CloudDrive2 搜索目录无效")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("CloudDrive2 搜索关键词不能为空")
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, errors.New("CloudDrive2 凭据不能为空")
	}
	limit = normalizeFileLimit(limit)

	callCtx, cancel := context.WithTimeout(contextOrBackground(ctx), searchOperationTimeout)
	defer cancel()
	callCtx = metadata.AppendToOutgoingContext(callCtx, "authorization", "Bearer "+token)

	files, err := c.searchFilesNative(callCtx, remotePath, query, limit)
	if err == nil {
		return files, nil
	}
	if status.Code(err) != codes.Unimplemented {
		return nil, mapRPCError(operationSearchFiles, err)
	}

	rootFiles, rootEntriesScanned, err := c.listDirectoryRaw(callCtx, remotePath, searchMaxTraversalEntries)
	if err != nil {
		return nil, mapSearchRawError(operationSearchFiles, err)
	}
	cloudRoots := searchableCloudRoots(remotePath, rootFiles)
	if len(cloudRoots) > searchMaxCloudRoots {
		return nil, errSearchTraversalLimit
	}
	if len(cloudRoots) > 0 {
		outcomes := c.searchCloudRoots(callCtx, cloudRoots, query, limit)
		supported := 0
		for _, outcome := range outcomes {
			if outcome.err == nil {
				supported++
				continue
			}
			if status.Code(outcome.err) != codes.Unimplemented {
				return nil, mapRPCError(operationSearchFiles, outcome.err)
			}
		}
		if supported > 0 {
			return mergeCloudRootResults(remotePath, outcomes, limit), nil
		}
	}

	return c.searchFilesByWalking(callCtx, remotePath, query, limit, rootFiles, rootEntriesScanned)
}

func (c *grpcClient) receiveFiles(ctx context.Context, token string, limit int, start func(context.Context) (grpc.ServerStreamingClient[pb.SubFilesReply], error), op operation) ([]File, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, errors.New("CloudDrive2 凭据不能为空")
	}
	limit = normalizeFileLimit(limit)
	callCtx, cancel := context.WithTimeout(contextOrBackground(ctx), fileOperationTimeout)
	defer cancel()
	callCtx = metadata.AppendToOutgoingContext(callCtx, "authorization", "Bearer "+token)
	result, err := receiveFilesRaw(callCtx, limit, start)
	if err != nil {
		return nil, mapRPCError(op, err)
	}
	return result, nil
}

func receiveFilesRaw(ctx context.Context, limit int, start func(context.Context) (grpc.ServerStreamingClient[pb.SubFilesReply], error)) ([]File, error) {
	stream, err := start(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]File, 0, min(limit, 64))
	for len(result) < limit {
		reply, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			return nil, recvErr
		}
		for _, item := range reply.GetSubFiles() {
			if item == nil {
				continue
			}
			result = append(result, mapCloudDriveFile(item))
			if len(result) == limit {
				break
			}
		}
	}
	return result, nil
}

func normalizeFileLimit(limit int) int {
	if limit <= 0 || limit > 500 {
		return 100
	}
	return limit
}

func mapCloudDriveFile(item *pb.CloudDriveFile) File {
	mapped := File{
		ID: item.GetId(), Name: item.GetName(), FullPath: item.GetFullPathName(), Size: item.GetSize(),
		IsDir:       item.GetIsDirectory() || item.GetFileType() == pb.CloudDriveFile_Directory,
		IsCloud:     item.GetIsCloudFile() || item.GetIsCloudDirectory() || item.GetIsCloudRoot(),
		IsCloudRoot: item.GetIsCloudRoot(), CanSearch: item.GetCanSearch(), CanOfflineDownload: item.GetCanOfflineDownload(), IsForbidden: item.GetIsForbidden(),
	}
	if item.GetWriteTime() != nil {
		value := item.GetWriteTime().AsTime()
		mapped.ModifiedAt = &value
	}
	return mapped
}

func (c *grpcClient) searchFilesNative(ctx context.Context, remotePath, query string, limit int) ([]File, error) {
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	addResultToMountedSearchFolder := false
	return receiveFilesRaw(streamCtx, limit, func(callCtx context.Context) (grpc.ServerStreamingClient[pb.SubFilesReply], error) {
		return c.rpc.GetSearchResults(callCtx, &pb.SearchRequest{
			Path:                           remotePath,
			SearchFor:                      query,
			FuzzyMatch:                     true,
			AddResultToMountedSearchFolder: &addResultToMountedSearchFolder,
		})
	})
}

func (c *grpcClient) listDirectoryRaw(ctx context.Context, remotePath string, maxEntries int) ([]File, int, error) {
	if maxEntries <= 0 {
		return nil, 0, errSearchTraversalLimit
	}
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := c.rpc.GetSubFiles(streamCtx, &pb.ListSubFileRequest{Path: directoryRequestPath(remotePath)})
	if err != nil {
		return nil, 0, err
	}
	result := make([]File, 0, min(maxEntries, 128))
	scanned := 0
	for {
		reply, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			return result, scanned, nil
		}
		if recvErr != nil {
			return nil, scanned, recvErr
		}
		for _, item := range reply.GetSubFiles() {
			if scanned == maxEntries {
				return nil, scanned, errSearchTraversalLimit
			}
			scanned++
			if item == nil {
				continue
			}
			result = append(result, mapCloudDriveFile(item))
		}
	}
}

type cloudRootSearchOutcome struct {
	root  string
	files []File
	err   error
}

func (c *grpcClient) searchCloudRoots(ctx context.Context, roots []string, query string, limit int) []cloudRootSearchOutcome {
	outcomes := make([]cloudRootSearchOutcome, len(roots))
	jobs := make(chan int)
	workers := min(searchBranchConcurrency, len(roots))
	var wait sync.WaitGroup
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			for index := range jobs {
				files, err := c.searchFilesNative(ctx, roots[index], query, limit)
				outcomes[index] = cloudRootSearchOutcome{root: roots[index], files: files, err: err}
			}
		}()
	}
	for index := range roots {
		jobs <- index
	}
	close(jobs)
	wait.Wait()
	return outcomes
}

func searchableCloudRoots(root string, files []File) []string {
	seen := make(map[string]struct{})
	result := make([]string, 0, min(len(files), searchMaxCloudRoots))
	for _, file := range files {
		if file.IsForbidden || !file.IsDir || !file.IsCloudRoot || !file.CanSearch {
			continue
		}
		normalized, ok := normalizeListedSearchFile(root, root, file)
		if !ok {
			continue
		}
		if _, exists := seen[normalized.FullPath]; exists {
			continue
		}
		seen[normalized.FullPath] = struct{}{}
		result = append(result, normalized.FullPath)
	}
	return result
}

func mergeCloudRootResults(root string, outcomes []cloudRootSearchOutcome, limit int) []File {
	seen := make(map[string]struct{})
	result := make([]File, 0, min(limit, 64))
	for _, outcome := range outcomes {
		if outcome.err != nil {
			continue
		}
		for _, file := range outcome.files {
			normalized, ok := normalizeListedSearchFile(root, outcome.root, file)
			if !ok || normalized.IsForbidden {
				continue
			}
			key := searchFileKey(normalized)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, normalized)
			if len(result) == limit {
				return result
			}
		}
	}
	return result
}

func (c *grpcClient) searchFilesByWalking(ctx context.Context, root, query string, limit int, rootFiles []File, rootEntriesScanned int) ([]File, error) {
	if rootEntriesScanned < len(rootFiles) {
		rootEntriesScanned = len(rootFiles)
	}
	if rootEntriesScanned > searchMaxTraversalEntries {
		return nil, errSearchTraversalLimit
	}
	type queuedDirectory struct {
		path       string
		depth      int
		prefetched []File
	}
	queue := []queuedDirectory{{path: root, prefetched: rootFiles}}
	visited := map[string]struct{}{root: {}}
	seenResults := make(map[string]struct{})
	results := make([]File, 0, min(limit, 64))
	scannedEntries := rootEntriesScanned
	queryFolded := strings.ToLower(query)

	for head := 0; head < len(queue); head++ {
		current := queue[head]
		entries := current.prefetched
		if entries == nil {
			remaining := searchMaxTraversalEntries - scannedEntries
			if remaining <= 0 {
				return nil, errSearchTraversalLimit
			}
			var err error
			var directoryEntriesScanned int
			entries, directoryEntriesScanned, err = c.listDirectoryRaw(ctx, current.path, remaining)
			if err != nil {
				return nil, mapSearchRawError(operationSearchFiles, err)
			}
			scannedEntries += directoryEntriesScanned
		}

		for _, file := range entries {
			normalized, ok := normalizeListedSearchFile(root, current.path, file)
			if !ok || normalized.IsForbidden {
				continue
			}
			if strings.Contains(strings.ToLower(normalized.Name), queryFolded) {
				key := searchFileKey(normalized)
				if _, exists := seenResults[key]; !exists {
					seenResults[key] = struct{}{}
					results = append(results, normalized)
					if len(results) == limit {
						return results, nil
					}
				}
			}
			if !normalized.IsDir {
				continue
			}
			if _, exists := visited[normalized.FullPath]; exists {
				continue
			}
			if current.depth+1 > searchMaxTraversalDepth || len(visited) >= searchMaxTraversalDirectories {
				return nil, errSearchTraversalLimit
			}
			visited[normalized.FullPath] = struct{}{}
			queue = append(queue, queuedDirectory{path: normalized.FullPath, depth: current.depth + 1})
		}
	}
	return results, nil
}

func mapSearchRawError(op operation, err error) error {
	if errors.Is(err, errSearchTraversalLimit) {
		return err
	}
	return mapRPCError(op, err)
}

func normalizeSearchRemotePath(value string) (string, bool) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" {
		value = "/"
	}
	if len(value) > searchMaxRemotePathLength || strings.ContainsRune(value, '\x00') || !strings.HasPrefix(value, "/") {
		return "", false
	}
	cleaned := path.Clean(value)
	if cleaned == "." || !strings.HasPrefix(cleaned, "/") {
		return "", false
	}
	return cleaned, true
}

func normalizeListedSearchFile(root, current string, file File) (File, bool) {
	fullPath := strings.TrimSpace(strings.ReplaceAll(file.FullPath, "\\", "/"))
	if !strings.HasPrefix(fullPath, "/") {
		segment := fullPath
		if segment == "" {
			segment = strings.TrimSpace(file.Name)
		}
		if segment == "" || segment == "." || segment == ".." || strings.ContainsAny(segment, "/\\") || strings.ContainsRune(segment, '\x00') {
			return File{}, false
		}
		fullPath = path.Join(current, segment)
	}
	var ok bool
	fullPath, ok = normalizeSearchRemotePath(fullPath)
	if !ok || fullPath == current || !searchRemotePathWithin(root, fullPath) {
		return File{}, false
	}
	name := strings.TrimSpace(file.Name)
	if name == "" {
		name = path.Base(fullPath)
	}
	if name == "." || name == ".." || len(name) > searchMaxRemoteNameLength || strings.ContainsAny(name, "/\\") || strings.ContainsRune(name, '\x00') {
		return File{}, false
	}
	file.FullPath = fullPath
	file.Name = name
	return file, true
}

func searchRemotePathWithin(root, candidate string) bool {
	if root == "/" {
		return strings.HasPrefix(candidate, "/")
	}
	return candidate == root || strings.HasPrefix(candidate, root+"/")
}

func searchFileKey(file File) string {
	if file.FullPath != "" {
		return "path:" + file.FullPath
	}
	if file.ID != "" {
		return "id:" + file.ID
	}
	return fmt.Sprintf("file:%s:%d:%t", file.Name, file.Size, file.IsDir)
}

func directoryRequestPath(value string) string {
	if value == "/" || strings.HasSuffix(value, "/") {
		return value
	}
	return value + "/"
}

func (c *grpcClient) GetDownloadLink(ctx context.Context, token, remotePath string, direct bool) (DownloadLink, error) {
	if strings.TrimSpace(token) == "" || strings.TrimSpace(remotePath) == "" {
		return DownloadLink{}, errors.New("CloudDrive2 凭据和文件路径不能为空")
	}
	callCtx, cancel := context.WithTimeout(contextOrBackground(ctx), fileOperationTimeout)
	defer cancel()
	callCtx = metadata.AppendToOutgoingContext(callCtx, "authorization", "Bearer "+strings.TrimSpace(token))
	result, err := c.rpc.GetDownloadUrlPath(callCtx, &pb.GetDownloadUrlPathRequest{Path: strings.TrimSpace(remotePath), GetDirectUrl: direct})
	if err != nil {
		return DownloadLink{}, mapRPCError(operationDownloadLink, err)
	}
	link := DownloadLink{Path: result.GetDownloadUrlPath(), DirectURL: result.GetDirectUrl(), UserAgent: result.GetUserAgent(), AdditionalHeaders: make(map[string]string, len(result.GetAdditionalHeaders()))}
	if result.ExpiresIn != nil {
		link.ExpiresInSeconds = result.GetExpiresIn()
	}
	for key, value := range result.GetAdditionalHeaders() {
		link.AdditionalHeaders[key] = value
	}
	if strings.TrimSpace(link.Path) == "" && strings.TrimSpace(link.DirectURL) == "" {
		return DownloadLink{}, errors.New("CloudDrive2 未返回可用下载地址")
	}
	return link, nil
}

func (c *grpcClient) CopyFiles(ctx context.Context, token string, paths []string, destination string) (FileOperation, error) {
	cleaned := nonEmptyStrings(paths)
	if strings.TrimSpace(token) == "" || len(cleaned) == 0 || strings.TrimSpace(destination) == "" {
		return FileOperation{}, errors.New("CloudDrive2 复制来源和目标不能为空")
	}
	policy := pb.CopyFileRequest_Rename
	result, err := c.fileOperation(ctx, token, operationCopyFiles, func(callCtx context.Context) (*pb.FileOperationResult, error) {
		return c.rpc.CopyFile(callCtx, &pb.CopyFileRequest{TheFilePaths: cleaned, DestPath: strings.TrimSpace(destination), ConflictPolicy: &policy})
	})
	return result, err
}

func (c *grpcClient) AddOfflineFiles(ctx context.Context, token string, urls []string, destination string) (FileOperation, error) {
	cleaned := nonEmptyStrings(urls)
	if strings.TrimSpace(token) == "" || len(cleaned) == 0 || strings.TrimSpace(destination) == "" {
		return FileOperation{}, errors.New("CloudDrive2 离线下载地址和目标不能为空")
	}
	return c.fileOperation(ctx, token, operationOfflineFiles, func(callCtx context.Context) (*pb.FileOperationResult, error) {
		return c.rpc.AddOfflineFiles(callCtx, &pb.AddOfflineFileRequest{Urls: strings.Join(cleaned, "\n"), ToFolder: strings.TrimSpace(destination)})
	})
}

func (c *grpcClient) AddSharedLink(ctx context.Context, token, sharedURL, password, destination string) error {
	if strings.TrimSpace(token) == "" || strings.TrimSpace(sharedURL) == "" || strings.TrimSpace(destination) == "" {
		return errors.New("CloudDrive2 分享链接和目标不能为空")
	}
	callCtx, cancel := context.WithTimeout(contextOrBackground(ctx), fileOperationTimeout)
	defer cancel()
	callCtx = metadata.AppendToOutgoingContext(callCtx, "authorization", "Bearer "+strings.TrimSpace(token))
	request := &pb.AddSharedLinkRequest{SharedLinkUrl: strings.TrimSpace(sharedURL), ToFolder: strings.TrimSpace(destination)}
	if strings.TrimSpace(password) != "" {
		value := strings.TrimSpace(password)
		request.SharedPassword = &value
	}
	if _, err := c.rpc.AddSharedLink(callCtx, request); err != nil {
		return mapRPCError(operationSharedLink, err)
	}
	return nil
}

func (c *grpcClient) fileOperation(ctx context.Context, token string, op operation, call func(context.Context) (*pb.FileOperationResult, error)) (FileOperation, error) {
	callCtx, cancel := context.WithTimeout(contextOrBackground(ctx), fileOperationTimeout)
	defer cancel()
	callCtx = metadata.AppendToOutgoingContext(callCtx, "authorization", "Bearer "+strings.TrimSpace(token))
	result, err := call(callCtx)
	if err != nil {
		return FileOperation{}, mapRPCError(op, err)
	}
	if !result.GetSuccess() {
		return FileOperation{}, errors.New("CloudDrive2 未接受操作，请检查目标目录和令牌权限")
	}
	return FileOperation{Success: true, ResultPaths: append([]string(nil), result.GetResultFilePaths()...)}, nil
}

func nonEmptyStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func parseAddress(address string) (string, credentials.TransportCredentials, error) {
	raw := strings.TrimSpace(address)
	if raw == "" {
		return "", nil, errors.New("CloudDrive2 地址不能为空")
	}

	scheme := "http"
	target := raw
	if strings.Contains(raw, "://") {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Host == "" {
			return "", nil, errors.New("CloudDrive2 地址格式无效")
		}
		if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
			return "", nil, errors.New("CloudDrive2 地址只能包含协议、主机和端口")
		}
		scheme = strings.ToLower(parsed.Scheme)
		if scheme != "http" && scheme != "https" {
			return "", nil, errors.New("CloudDrive2 地址只支持 http 或 https")
		}
		target = parsed.Host
	}

	host, _, err := net.SplitHostPort(target)
	if err != nil || strings.TrimSpace(host) == "" {
		return "", nil, errors.New("CloudDrive2 地址必须包含有效端口")
	}

	if scheme == "https" {
		serverName := strings.Trim(host, "[]")
		return target, credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName}), nil
	}
	return target, insecure.NewCredentials(), nil
}

func (c *grpcClient) GetSystemInfo(ctx context.Context) (SystemInfo, error) {
	callCtx, cancel := c.withTimeout(ctx)
	defer cancel()

	result, err := c.rpc.GetSystemInfo(callCtx, &emptypb.Empty{})
	if err != nil {
		return SystemInfo{}, mapRPCError(operationSystemInfo, err)
	}
	return SystemInfo{
		IsLogin:       result.GetIsLogin(),
		UserName:      result.GetUserName(),
		SystemReady:   result.GetSystemReady(),
		SystemMessage: result.GetSystemMessage(),
		HasError:      result.GetHasError(),
	}, nil
}

func (c *grpcClient) Authenticate(ctx context.Context, username, password, totpCode string) (Authentication, error) {
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return Authentication{}, errors.New("CloudDrive2 账号和密码不能为空")
	}
	request := &pb.GetTokenRequest{UserName: username, Password: password}
	if strings.TrimSpace(totpCode) != "" {
		value := strings.TrimSpace(totpCode)
		request.TotpCode = &value
	}
	callCtx, cancel := c.withTimeout(ctx)
	defer cancel()
	result, err := c.rpc.GetToken(callCtx, request)
	if err != nil {
		return Authentication{}, mapRPCError(operationAuthenticate, err)
	}
	if !result.GetSuccess() || strings.TrimSpace(result.GetToken()) == "" {
		return Authentication{}, errors.New("CloudDrive2 账号、密码或二步验证码错误")
	}
	var expiresAt *time.Time
	if result.GetExpiration() != nil {
		value := result.GetExpiration().AsTime()
		expiresAt = &value
	}
	return Authentication{Token: result.GetToken(), ExpiresAt: expiresAt}, nil
}

func (c *grpcClient) GetApiTokenInfo(ctx context.Context, token string) (TokenInfo, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return TokenInfo{}, errors.New("CloudDrive2 API Token 不能为空")
	}

	callCtx, cancel := c.withTimeout(ctx)
	defer cancel()

	result, err := c.rpc.GetApiTokenInfo(callCtx, &pb.StringValue{Value: token})
	if err != nil {
		return TokenInfo{}, mapRPCError(operationTokenInfo, err)
	}

	permissions := result.GetPermissions()
	info := TokenInfo{
		RootDir:          result.GetRootDir(),
		FriendlyName:     result.GetFriendlyName(),
		ExpiresInSeconds: result.GetExpiresIn(),
		NeverExpires:     result.ExpiresIn == nil || result.GetExpiresIn() == 0,
	}
	if permissions != nil {
		info.Permissions = TokenPermissions{
			AllowList:             permissions.GetAllowList(),
			AllowSearch:           permissions.GetAllowSearch(),
			AllowListLocal:        permissions.GetAllowListLocal(),
			AllowRead:             permissions.GetAllowRead(),
			AllowCopy:             permissions.GetAllowCopy(),
			AllowOfflineDownload:  permissions.GetAllowAddOfflineDownload(),
			AllowSharedLinks:      permissions.GetAllowSharedLinks(),
			AllowGetMounts:        permissions.GetAllowGetMounts(),
			AllowGetTransferTasks: permissions.GetAllowGetTransferTasks(),
			AllowGetCloudAPIs:     permissions.GetAllowGetCloudApis(),
			AllowPushMessage:      permissions.GetAllowPushMessage(),
		}
	}
	return info, nil
}

func (c *grpcClient) GetMountPoints(ctx context.Context, token string) ([]MountPoint, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, errors.New("CloudDrive2 API Token 不能为空")
	}

	callCtx, cancel := c.withTimeout(ctx)
	defer cancel()
	callCtx = metadata.AppendToOutgoingContext(callCtx, "authorization", "Bearer "+token)

	result, err := c.rpc.GetMountPoints(callCtx, &emptypb.Empty{})
	if err != nil {
		return nil, mapRPCError(operationMountPoints, err)
	}

	mounts := make([]MountPoint, 0, len(result.GetMountPoints()))
	for _, mount := range result.GetMountPoints() {
		if mount == nil {
			continue
		}
		mounts = append(mounts, MountPoint{
			MountPoint: mount.GetMountPoint(),
			SourceDir:  mount.GetSourceDir(),
			Name:       mount.GetName(),
			IsMounted:  mount.GetIsMounted(),
			FailReason: mount.GetFailReason(),
			ReadOnly:   mount.GetReadOnly(),
			AutoMount:  mount.GetAutoMount(),
		})
	}
	return mounts, nil
}

func (c *grpcClient) Close() error {
	return c.conn.Close()
}

func (c *grpcClient) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, c.timeout)
}

type operation uint8

const (
	operationSystemInfo operation = iota
	operationAuthenticate
	operationTokenInfo
	operationMountPoints
	operationListFiles
	operationSearchFiles
	operationDownloadLink
	operationCopyFiles
	operationOfflineFiles
	operationSharedLink
)

// mapRPCError intentionally ignores the server-supplied status message. A
// remote server may include the submitted token in that message.
func mapRPCError(op operation, err error) error {
	switch status.Code(err) {
	case codes.Canceled:
		return errors.New("CloudDrive2 操作已取消")
	case codes.DeadlineExceeded:
		if op == operationSearchFiles {
			return errors.New("CloudDrive2 搜索超时，请输入更具体的关键词或缩小资源目录")
		}
		return errors.New("连接 CloudDrive2 超时")
	case codes.Unavailable:
		return errors.New("无法连接 CloudDrive2 服务，请检查地址和端口")
	case codes.Unauthenticated:
		if op == operationAuthenticate {
			return errors.New("CloudDrive2 账号、密码或二步验证码错误")
		}
		return ErrTokenInvalid
	case codes.PermissionDenied:
		if op == operationMountPoints {
			return errors.New("CloudDrive2 API Token 没有“获取挂载点”权限")
		}
		return errors.New("CloudDrive2 API Token 权限不足")
	case codes.Unimplemented:
		if op == operationOfflineFiles {
			return errors.New("所选 CloudDrive2 目录或网盘不支持离线下载，请重新选择带支持标记的目录")
		}
		return errors.New("当前 CloudDrive2 版本不支持此操作")
	case codes.NotFound, codes.InvalidArgument:
		if op == operationTokenInfo {
			return ErrTokenInvalid
		}
		return errors.New("CloudDrive2 请求参数无效")
	default:
		action := "调用 CloudDrive2"
		switch op {
		case operationSystemInfo:
			action = "读取 CloudDrive2 系统信息"
		case operationTokenInfo:
			action = "检查 CloudDrive2 API Token"
		case operationAuthenticate:
			action = "登录 CloudDrive2"
		case operationMountPoints:
			action = "读取 CloudDrive2 挂载点"
		case operationListFiles:
			action = "读取 CloudDrive2 文件"
		case operationSearchFiles:
			action = "搜索 CloudDrive2 文件"
		case operationDownloadLink:
			action = "获取 CloudDrive2 下载地址"
		case operationCopyFiles:
			action = "提交 CloudDrive2 复制任务"
		case operationOfflineFiles:
			action = "提交 CloudDrive2 离线任务"
		case operationSharedLink:
			action = "接收 CloudDrive2 分享链接"
		}
		return fmt.Errorf("%s失败（gRPC 状态：%s）", action, status.Code(err))
	}
}
