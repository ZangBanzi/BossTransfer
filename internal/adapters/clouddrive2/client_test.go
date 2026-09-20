package clouddrive2

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"bosstransfer/internal/adapters/clouddrive2/internal/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	validToken        = "secret-valid-token"
	invalidToken      = "secret-invalid-token"
	missingMountToken = "secret-no-mount-token"
	passwordToken     = "secret-password-jwt"
)

type mockCloudDriveServer struct {
	pb.UnimplementedCloudDriveFileSrvServer
}

func (mockCloudDriveServer) GetSystemInfo(ctx context.Context, _ *emptypb.Empty) (*pb.CloudDriveSystemInfo, error) {
	if values := incomingAuthorization(ctx); len(values) != 0 {
		return nil, status.Error(codes.InvalidArgument, "GetSystemInfo must not receive authorization")
	}
	message := "ready"
	return &pb.CloudDriveSystemInfo{
		IsLogin:       true,
		UserName:      "tester",
		SystemReady:   true,
		SystemMessage: &message,
	}, nil
}

func (mockCloudDriveServer) GetToken(ctx context.Context, request *pb.GetTokenRequest) (*pb.JWTToken, error) {
	if values := incomingAuthorization(ctx); len(values) != 0 {
		return nil, status.Error(codes.InvalidArgument, "GetToken must not receive authorization")
	}
	if request.GetUserName() != "admin" || request.GetPassword() != "password" {
		return &pb.JWTToken{Success: false, ErrorMessage: "invalid credentials"}, nil
	}
	return &pb.JWTToken{Success: true, Token: passwordToken, Expiration: timestamppb.New(time.Now().Add(time.Hour))}, nil
}

func (mockCloudDriveServer) GetApiTokenInfo(ctx context.Context, request *pb.StringValue) (*pb.TokenInfo, error) {
	if values := incomingAuthorization(ctx); len(values) != 0 {
		return nil, status.Error(codes.InvalidArgument, "GetApiTokenInfo must not receive authorization")
	}
	switch request.GetValue() {
	case validToken:
		expires := uint64(3600)
		return &pb.TokenInfo{
			RootDir:      "/115",
			FriendlyName: "BossTransfer",
			ExpiresIn:    &expires,
			Permissions: &pb.TokenPermissions{
				AllowList:      true,
				AllowSearch:    true,
				AllowRead:      true,
				AllowGetMounts: true,
			},
		}, nil
	case missingMountToken:
		return &pb.TokenInfo{
			RootDir: "/115",
			Permissions: &pb.TokenPermissions{
				AllowList:   true,
				AllowSearch: true,
				AllowRead:   true,
			},
		}, nil
	default:
		return nil, status.Errorf(codes.NotFound, "token %s not found", request.GetValue())
	}
}

func (mockCloudDriveServer) GetMountPoints(ctx context.Context, _ *emptypb.Empty) (*pb.GetMountPointsResult, error) {
	values := incomingAuthorization(ctx)
	if len(values) != 1 {
		return nil, status.Error(codes.Unauthenticated, "missing bearer token")
	}
	switch values[0] {
	case "Bearer " + validToken, "Bearer " + passwordToken:
		return &pb.GetMountPointsResult{MountPoints: []*pb.MountPoint{
			{
				MountPoint: "/CloudNAS/115",
				SourceDir:  "/115/user",
				Name:       "我的115",
				IsMounted:  true,
				ReadOnly:   true,
				AutoMount:  true,
			},
			{
				MountPoint: "/CloudNAS/broken",
				SourceDir:  "/115/broken",
				Name:       "异常挂载",
				IsMounted:  false,
				FailReason: "mount failed",
			},
		}}, nil
	case "Bearer " + missingMountToken:
		return nil, status.Errorf(codes.PermissionDenied, "token %s lacks allow_get_mounts", missingMountToken)
	default:
		return nil, status.Errorf(codes.Unauthenticated, "invalid token %s", invalidToken)
	}
}

func incomingAuthorization(ctx context.Context) []string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil
	}
	return md.Get("authorization")
}

func startMockServer(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := grpc.NewServer()
	pb.RegisterCloudDriveFileSrvServer(server, mockCloudDriveServer{})
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})
	return listener.Addr().String()
}

func TestClientSuccess(t *testing.T) {
	address := startMockServer(t)
	client, err := NewClient("http://" + address + "/")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()

	ctx := context.Background()
	systemInfo, err := client.GetSystemInfo(ctx)
	if err != nil {
		t.Fatalf("GetSystemInfo: %v", err)
	}
	if !systemInfo.IsLogin || !systemInfo.SystemReady || systemInfo.UserName != "tester" || systemInfo.SystemMessage != "ready" {
		t.Fatalf("unexpected system info: %+v", systemInfo)
	}
	authentication, err := client.Authenticate(ctx, "admin", "password", "")
	if err != nil || authentication.Token != passwordToken || authentication.ExpiresAt == nil {
		t.Fatalf("Authenticate: %+v, %v", authentication, err)
	}

	tokenInfo, err := client.GetApiTokenInfo(ctx, validToken)
	if err != nil {
		t.Fatalf("GetApiTokenInfo: %v", err)
	}
	if tokenInfo.RootDir != "/115" || tokenInfo.FriendlyName != "BossTransfer" || tokenInfo.ExpiresInSeconds != 3600 {
		t.Fatalf("unexpected token info: %+v", tokenInfo)
	}
	if !tokenInfo.Permissions.AllowList || !tokenInfo.Permissions.AllowSearch || !tokenInfo.Permissions.AllowRead || !tokenInfo.Permissions.CanDiscoverMounts() {
		t.Fatalf("unexpected permissions: %+v", tokenInfo.Permissions)
	}

	mounts, err := client.GetMountPoints(ctx, validToken)
	if err != nil {
		t.Fatalf("GetMountPoints: %v", err)
	}
	if len(mounts) != 2 {
		t.Fatalf("mount count = %d, want 2", len(mounts))
	}
	if mounts[0].MountPoint != "/CloudNAS/115" || mounts[0].SourceDir != "/115/user" || mounts[0].Name != "我的115" || !mounts[0].IsMounted || !mounts[0].ReadOnly || !mounts[0].AutoMount {
		t.Fatalf("unexpected first mount: %+v", mounts[0])
	}
	if mounts[1].IsMounted || mounts[1].FailReason != "mount failed" {
		t.Fatalf("unexpected second mount: %+v", mounts[1])
	}
}

func TestClientReportsMissingMountPermissionWithoutLeakingToken(t *testing.T) {
	client, err := NewClient(startMockServer(t))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()

	info, err := client.GetApiTokenInfo(context.Background(), missingMountToken)
	if err != nil {
		t.Fatalf("GetApiTokenInfo: %v", err)
	}
	if info.Permissions.CanDiscoverMounts() {
		t.Fatal("token unexpectedly has mount permission")
	}

	_, err = client.GetMountPoints(context.Background(), missingMountToken)
	if err == nil || !strings.Contains(err.Error(), "获取挂载点") {
		t.Fatalf("error = %v, want mount permission error", err)
	}
	if strings.Contains(err.Error(), missingMountToken) {
		t.Fatalf("error leaked token: %v", err)
	}
}

func TestClientReportsInvalidTokenWithoutLeakingToken(t *testing.T) {
	client, err := NewClient(startMockServer(t))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()

	_, err = client.GetApiTokenInfo(context.Background(), invalidToken)
	if err == nil || !strings.Contains(err.Error(), "无效或已过期") {
		t.Fatalf("GetApiTokenInfo error = %v", err)
	}
	if strings.Contains(err.Error(), invalidToken) {
		t.Fatalf("GetApiTokenInfo leaked token: %v", err)
	}

	_, err = client.GetMountPoints(context.Background(), invalidToken)
	if err == nil || !strings.Contains(err.Error(), "无效或已过期") {
		t.Fatalf("GetMountPoints error = %v", err)
	}
	if strings.Contains(err.Error(), invalidToken) {
		t.Fatalf("GetMountPoints leaked token: %v", err)
	}
}

func TestDefaultTimeoutIsBounded(t *testing.T) {
	if DefaultTimeout < 3*time.Second || DefaultTimeout > 5*time.Second {
		t.Fatalf("DefaultTimeout = %s, want 3-5 seconds", DefaultTimeout)
	}
}

func TestAddressValidation(t *testing.T) {
	for _, address := range []string{"", "localhost", "ftp://localhost:19798", "http://localhost:19798/path"} {
		client, err := NewClient(address)
		if err == nil {
			_ = client.Close()
			t.Fatalf("NewClient(%q) unexpectedly succeeded", address)
		}
	}
}

type searchRPCResponse struct {
	files []*pb.CloudDriveFile
	code  codes.Code
	delay time.Duration
}

type searchCompatibilityServer struct {
	pb.UnimplementedCloudDriveFileSrvServer

	search map[string]searchRPCResponse
	list   map[string][]*pb.CloudDriveFile

	mu              sync.Mutex
	searchCalls     []string
	mountedOptions  []*bool
	listCalls       []string
	activeSearches  int
	maxActiveSearch int
}

func (s *searchCompatibilityServer) GetSearchResults(request *pb.SearchRequest, stream grpc.ServerStreamingServer[pb.SubFilesReply]) error {
	if values := incomingAuthorization(stream.Context()); len(values) != 1 || values[0] != "Bearer "+validToken {
		return status.Error(codes.Unauthenticated, "invalid bearer token")
	}
	s.mu.Lock()
	s.searchCalls = append(s.searchCalls, request.GetPath())
	var mountedOption *bool
	if request.AddResultToMountedSearchFolder != nil {
		value := request.GetAddResultToMountedSearchFolder()
		mountedOption = &value
	}
	s.mountedOptions = append(s.mountedOptions, mountedOption)
	s.activeSearches++
	if s.activeSearches > s.maxActiveSearch {
		s.maxActiveSearch = s.activeSearches
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.activeSearches--
		s.mu.Unlock()
	}()

	response, ok := s.search[request.GetPath()]
	if !ok {
		response.code = codes.Unimplemented
	}
	if response.delay > 0 {
		timer := time.NewTimer(response.delay)
		defer timer.Stop()
		select {
		case <-stream.Context().Done():
			return status.FromContextError(stream.Context().Err()).Err()
		case <-timer.C:
		}
	}
	if response.code != codes.OK {
		return status.Errorf(response.code, "search failed for token %s", validToken)
	}
	if len(response.files) == 0 {
		return nil
	}
	return stream.Send(&pb.SubFilesReply{SubFiles: response.files})
}

func (s *searchCompatibilityServer) GetSubFiles(request *pb.ListSubFileRequest, stream grpc.ServerStreamingServer[pb.SubFilesReply]) error {
	if values := incomingAuthorization(stream.Context()); len(values) != 1 || values[0] != "Bearer "+validToken {
		return status.Error(codes.Unauthenticated, "invalid bearer token")
	}
	requestPath := strings.TrimSuffix(request.GetPath(), "/")
	if requestPath == "" {
		requestPath = "/"
	}
	s.mu.Lock()
	s.listCalls = append(s.listCalls, requestPath)
	s.mu.Unlock()
	files := s.list[requestPath]
	if len(files) == 0 {
		return nil
	}
	return stream.Send(&pb.SubFilesReply{SubFiles: files})
}

func (s *searchCompatibilityServer) snapshot() (searchCalls, listCalls []string, maxActive int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.searchCalls...), append([]string(nil), s.listCalls...), s.maxActiveSearch
}

func (s *searchCompatibilityServer) mountedSearchOptions() []*bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*bool(nil), s.mountedOptions...)
}

func startSearchCompatibilityServer(t *testing.T, implementation *searchCompatibilityServer) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := grpc.NewServer()
	pb.RegisterCloudDriveFileSrvServer(server, implementation)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})
	return listener.Addr().String()
}

func newSearchTestClient(t *testing.T, server *searchCompatibilityServer) FileClient {
	t.Helper()
	client, err := NewFileClient(startSearchCompatibilityServer(t, server))
	if err != nil {
		t.Fatalf("NewFileClient: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func cloudDirectory(name, fullPath string, canSearch bool) *pb.CloudDriveFile {
	return &pb.CloudDriveFile{
		Name: name, FullPathName: fullPath, FileType: pb.CloudDriveFile_Directory,
		IsDirectory: true, IsCloudRoot: true, IsCloudDirectory: true, CanSearch: canSearch,
	}
}

func ordinaryDirectory(name, fullPath string) *pb.CloudDriveFile {
	return &pb.CloudDriveFile{Name: name, FullPathName: fullPath, FileType: pb.CloudDriveFile_Directory, IsDirectory: true}
}

func ordinaryFile(name, fullPath string) *pb.CloudDriveFile {
	return &pb.CloudDriveFile{Name: name, FullPathName: fullPath, FileType: pb.CloudDriveFile_File, Size: 1024, IsCloudFile: true}
}

func TestSearchFilesUsesNativeSearchAndMapsCapabilities(t *testing.T) {
	server := &searchCompatibilityServer{
		search: map[string]searchRPCResponse{
			"/aggregate": {files: []*pb.CloudDriveFile{cloudDirectory("Searchable", "/aggregate/searchable", true)}},
		},
		list: map[string][]*pb.CloudDriveFile{},
	}
	client := newSearchTestClient(t, server)
	files, err := client.SearchFiles(context.Background(), validToken, "/aggregate", "Search", 10)
	if err != nil {
		t.Fatalf("SearchFiles: %v", err)
	}
	if len(files) != 1 || !files[0].CanSearch || !files[0].IsCloudRoot || !files[0].IsCloud {
		t.Fatalf("unexpected mapped file: %+v", files)
	}
	searchCalls, listCalls, _ := server.snapshot()
	if strings.Join(searchCalls, ",") != "/aggregate" || len(listCalls) != 0 {
		t.Fatalf("search calls = %v, list calls = %v", searchCalls, listCalls)
	}
	mountedOptions := server.mountedSearchOptions()
	if len(mountedOptions) != 1 || mountedOptions[0] == nil || *mountedOptions[0] {
		t.Fatalf("mounted search option = %v, want an explicit false value", mountedOptions)
	}
}

func TestSearchFilesExpandsCloudRootsConcurrentlyAndDeduplicates(t *testing.T) {
	server := &searchCompatibilityServer{
		search: map[string]searchRPCResponse{
			"/aggregate":   {code: codes.Unimplemented},
			"/aggregate/a": {delay: 80 * time.Millisecond, files: []*pb.CloudDriveFile{ordinaryFile("Shared.mkv", "/aggregate/shared.mkv"), ordinaryFile("A.mkv", "/aggregate/a/A.mkv")}},
			"/aggregate/b": {delay: 80 * time.Millisecond, files: []*pb.CloudDriveFile{ordinaryFile("Shared.mkv", "/aggregate/shared.mkv"), ordinaryFile("B.mkv", "/aggregate/b/B.mkv")}},
		},
		list: map[string][]*pb.CloudDriveFile{
			"/aggregate": {
				cloudDirectory("A", "/aggregate/a", true),
				cloudDirectory("B", "/aggregate/b", true),
				cloudDirectory("NoSearch", "/aggregate/no-search", false),
			},
		},
	}
	client := newSearchTestClient(t, server)
	files, err := client.SearchFiles(context.Background(), validToken, "/aggregate", "movie", 10)
	if err != nil {
		t.Fatalf("SearchFiles: %v", err)
	}
	if len(files) != 3 || files[0].Name != "Shared.mkv" || files[1].Name != "A.mkv" || files[2].Name != "B.mkv" {
		t.Fatalf("unexpected merged files: %+v", files)
	}
	searchCalls, listCalls, maxActive := server.snapshot()
	if len(searchCalls) != 3 || len(listCalls) != 1 || listCalls[0] != "/aggregate" || maxActive < 2 {
		t.Fatalf("search calls = %v, list calls = %v, max active = %d", searchCalls, listCalls, maxActive)
	}
	for _, call := range searchCalls {
		if call == "/aggregate/no-search" {
			t.Fatalf("non-searchable root was searched: %v", searchCalls)
		}
	}
}

func TestSearchFilesReturnsSupportedCloudRootWhenPeerIsUnimplemented(t *testing.T) {
	server := &searchCompatibilityServer{
		search: map[string]searchRPCResponse{
			"/aggregate":   {code: codes.Unimplemented},
			"/aggregate/a": {files: []*pb.CloudDriveFile{ordinaryFile("Available.mkv", "/aggregate/a/Available.mkv")}},
			"/aggregate/b": {code: codes.Unimplemented},
		},
		list: map[string][]*pb.CloudDriveFile{
			"/aggregate": {cloudDirectory("A", "/aggregate/a", true), cloudDirectory("B", "/aggregate/b", true)},
		},
	}
	client := newSearchTestClient(t, server)
	files, err := client.SearchFiles(context.Background(), validToken, "/aggregate", "available", 10)
	if err != nil {
		t.Fatalf("SearchFiles: %v", err)
	}
	if len(files) != 1 || files[0].FullPath != "/aggregate/a/Available.mkv" {
		t.Fatalf("unexpected files: %+v", files)
	}
	_, listCalls, _ := server.snapshot()
	if len(listCalls) != 1 || listCalls[0] != "/aggregate" {
		t.Fatalf("supported peer unexpectedly triggered BFS: %v", listCalls)
	}
}

func TestSearchFilesFallsBackToBFSWhenAllCloudRootsAreUnimplemented(t *testing.T) {
	server := &searchCompatibilityServer{
		search: map[string]searchRPCResponse{
			"/aggregate":   {code: codes.Unimplemented},
			"/aggregate/a": {code: codes.Unimplemented},
		},
		list: map[string][]*pb.CloudDriveFile{
			"/aggregate": {
				cloudDirectory("A", "/aggregate/a", true),
				ordinaryDirectory("Library", "/aggregate/library"),
			},
			"/aggregate/a":       {},
			"/aggregate/library": {ordinaryFile("Wanted Movie.mkv", "")},
		},
	}
	client := newSearchTestClient(t, server)
	files, err := client.SearchFiles(context.Background(), validToken, "/aggregate", "wanted", 10)
	if err != nil {
		t.Fatalf("SearchFiles: %v", err)
	}
	if len(files) != 1 || files[0].FullPath != "/aggregate/library/Wanted Movie.mkv" {
		t.Fatalf("unexpected BFS files: %+v", files)
	}
	_, listCalls, _ := server.snapshot()
	rootLists := 0
	for _, call := range listCalls {
		if call == "/aggregate" {
			rootLists++
		}
	}
	if rootLists != 1 {
		t.Fatalf("aggregate root listed %d times; calls = %v", rootLists, listCalls)
	}
}

func TestSearchFilesBFSRejectsOutsidePathsAndCycles(t *testing.T) {
	server := &searchCompatibilityServer{
		search: map[string]searchRPCResponse{"/aggregate": {code: codes.Unimplemented}},
		list: map[string][]*pb.CloudDriveFile{
			"/aggregate": {
				ordinaryDirectory("Outside", "/outside"),
				ordinaryDirectory("Loop", "/aggregate/loop"),
			},
			"/aggregate/loop": {
				ordinaryDirectory("Loop", "/aggregate/loop"),
				ordinaryFile("Safe Match.mkv", "/aggregate/loop/Safe Match.mkv"),
			},
		},
	}
	client := newSearchTestClient(t, server)
	files, err := client.SearchFiles(context.Background(), validToken, "/aggregate", "safe", 10)
	if err != nil {
		t.Fatalf("SearchFiles: %v", err)
	}
	if len(files) != 1 || files[0].FullPath != "/aggregate/loop/Safe Match.mkv" {
		t.Fatalf("unexpected files: %+v", files)
	}
	_, listCalls, _ := server.snapshot()
	if len(listCalls) != 2 || listCalls[0] != "/aggregate" || listCalls[1] != "/aggregate/loop" {
		t.Fatalf("unexpected list calls: %v", listCalls)
	}
}

func TestSearchFilesDoesNotFallbackOnNonUnimplementedError(t *testing.T) {
	server := &searchCompatibilityServer{
		search: map[string]searchRPCResponse{"/aggregate": {code: codes.PermissionDenied}},
		list:   map[string][]*pb.CloudDriveFile{},
	}
	client := newSearchTestClient(t, server)
	_, err := client.SearchFiles(context.Background(), validToken, "/aggregate", "movie", 10)
	if err == nil || !strings.Contains(err.Error(), "权限不足") {
		t.Fatalf("error = %v, want permission error", err)
	}
	if strings.Contains(err.Error(), validToken) {
		t.Fatalf("error leaked token: %v", err)
	}
	_, listCalls, _ := server.snapshot()
	if len(listCalls) != 0 {
		t.Fatalf("permission error unexpectedly triggered listing: %v", listCalls)
	}
}

func TestSearchFilesDoesNotFallbackWhenCloudRootHasFatalError(t *testing.T) {
	server := &searchCompatibilityServer{
		search: map[string]searchRPCResponse{
			"/aggregate":   {code: codes.Unimplemented},
			"/aggregate/a": {code: codes.Unimplemented},
			"/aggregate/b": {code: codes.PermissionDenied},
		},
		list: map[string][]*pb.CloudDriveFile{
			"/aggregate": {cloudDirectory("A", "/aggregate/a", true), cloudDirectory("B", "/aggregate/b", true)},
		},
	}
	client := newSearchTestClient(t, server)
	_, err := client.SearchFiles(context.Background(), validToken, "/aggregate", "movie", 10)
	if err == nil || !strings.Contains(err.Error(), "权限不足") {
		t.Fatalf("error = %v, want permission error", err)
	}
	_, listCalls, _ := server.snapshot()
	if len(listCalls) != 1 {
		t.Fatalf("fatal branch error unexpectedly triggered BFS: %v", listCalls)
	}
}

func TestSearchFilesCloudRootsShareCallerDeadline(t *testing.T) {
	server := &searchCompatibilityServer{
		search: map[string]searchRPCResponse{
			"/aggregate":   {code: codes.Unimplemented},
			"/aggregate/a": {delay: time.Second},
			"/aggregate/b": {delay: time.Second},
		},
		list: map[string][]*pb.CloudDriveFile{
			"/aggregate": {cloudDirectory("A", "/aggregate/a", true), cloudDirectory("B", "/aggregate/b", true)},
		},
	}
	client := newSearchTestClient(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := client.SearchFiles(ctx, validToken, "/aggregate", "movie", 10)
	if err == nil || err.Error() != "CloudDrive2 搜索超时，请输入更具体的关键词或缩小资源目录" {
		t.Fatalf("error = %v, want actionable search timeout", err)
	}
	if elapsed := time.Since(started); elapsed > 400*time.Millisecond {
		t.Fatalf("shared deadline was not honored: %s", elapsed)
	}
	_, _, maxActive := server.snapshot()
	if maxActive < 2 {
		t.Fatalf("cloud root searches were not concurrent; max active = %d", maxActive)
	}
}

func TestSearchTimeoutMessageDoesNotChangeOtherOperations(t *testing.T) {
	err := mapRPCError(operationListFiles, status.Error(codes.DeadlineExceeded, "slow"))
	if err == nil || err.Error() != "连接 CloudDrive2 超时" {
		t.Fatalf("list timeout error = %v", err)
	}
}

func TestSearchFilesTraversalLimitReturnsNoPartialResults(t *testing.T) {
	rootFiles := make([]File, searchMaxTraversalEntries+1)
	rootFiles[0] = File{Name: "match.mkv", FullPath: "/aggregate/match.mkv"}
	client := &grpcClient{}
	files, err := client.searchFilesByWalking(context.Background(), "/aggregate", "match", 100, rootFiles, len(rootFiles))
	if !errors.Is(err, errSearchTraversalLimit) {
		t.Fatalf("error = %v, want traversal limit", err)
	}
	if files != nil {
		t.Fatalf("overflow returned partial files: %+v", files)
	}

	directories := make([]File, searchMaxTraversalDirectories)
	for index := range directories {
		directories[index] = File{Name: "directory", FullPath: "/aggregate/directory-" + time.Unix(int64(index), 0).UTC().Format("150405.000000000"), IsDir: true}
	}
	files, err = client.searchFilesByWalking(context.Background(), "/aggregate", "not-found", 100, directories, len(directories))
	if !errors.Is(err, errSearchTraversalLimit) || files != nil {
		t.Fatalf("directory overflow files = %+v, error = %v", files, err)
	}
}

func TestNormalizeListedSearchFileRejectsUnsafeNames(t *testing.T) {
	for _, name := range []string{".", "..", "folder/file", `folder\file`, "bad\x00name"} {
		if file, ok := normalizeListedSearchFile("/aggregate", "/aggregate", File{Name: name, FullPath: "/aggregate/safe.mkv"}); ok {
			t.Fatalf("unsafe name %q accepted as %+v", name, file)
		}
	}
	file, ok := normalizeListedSearchFile("/aggregate", "/aggregate", File{Name: "safe.mkv", FullPath: "/aggregate/safe.mkv"})
	if !ok || file.FullPath != "/aggregate/safe.mkv" {
		t.Fatalf("safe file rejected: %+v, %t", file, ok)
	}
}
