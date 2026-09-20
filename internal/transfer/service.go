package transfer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	DefaultSourceDir  = "/data/source"
	DefaultTargetDir  = "/data/target"
	DefaultDataDir    = "/var/lib/bosstransfer-client"
	DefaultMaxResults = 100

	maxConcurrentRemoteDownloads = 3
	maxRememberedTasks           = 100
	remoteDialTimeout            = 15 * time.Second
	remoteTLSHandshakeTimeout    = 15 * time.Second
	remoteResponseHeaderTimeout  = 30 * time.Second
	remoteIdleTimeout            = 2 * time.Minute
)

var (
	ErrPathOutsideRoot      = errors.New("path is outside configured root")
	ErrInvalidDirectoryRoot = errors.New("invalid directory root")
	ErrSymbolicLink         = errors.New("symbolic links are not supported")
	ErrRemoteDownloadBusy   = errors.New("too many remote downloads are already running")
	ErrServiceClosed        = errors.New("transfer service is closed")

	errRemoteRedirectOrigin = errors.New("remote download redirect changed origin")
	errRemoteRedirectLimit  = errors.New("remote download redirected too many times")
)

type DirectoryRoot string

const (
	DirectoryRootSource DirectoryRoot = "source"
	DirectoryRootTarget DirectoryRoot = "target"
)

type Config struct {
	SourceDir  string `json:"source_dir"`
	TargetDir  string `json:"target_dir"`
	MaxResults int    `json:"max_results"`
}

type ConfigStore struct {
	mu   sync.RWMutex
	path string
	cfg  Config
}

func NormalizeConfig(cfg Config) Config {
	cfg.SourceDir = strings.TrimSpace(cfg.SourceDir)
	cfg.TargetDir = strings.TrimSpace(cfg.TargetDir)
	if cfg.SourceDir == "" {
		cfg.SourceDir = DefaultSourceDir
	}
	if cfg.TargetDir == "" {
		cfg.TargetDir = DefaultTargetDir
	}
	if cfg.MaxResults <= 0 {
		cfg.MaxResults = DefaultMaxResults
	}
	if cfg.MaxResults > 500 {
		cfg.MaxResults = 500
	}
	return cfg
}

func NewInMemoryConfigStore(defaults Config) *ConfigStore {
	return &ConfigStore{cfg: NormalizeConfig(defaults)}
}

func NewFileConfigStore(path string, defaults Config) (*ConfigStore, error) {
	store := &ConfigStore{path: path, cfg: NormalizeConfig(defaults)}
	if path == "" {
		return store, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return store, nil
		}
		return nil, err
	}
	var saved Config
	if err := json.Unmarshal(data, &saved); err != nil {
		return nil, fmt.Errorf("read transfer config: %w", err)
	}
	store.cfg = NormalizeConfig(saved)
	return store, nil
}

func (s *ConfigStore) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

func (s *ConfigStore) Update(cfg Config) (Config, error) {
	cfg = NormalizeConfig(cfg)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validateConfig(cfg); err != nil {
		return Config{}, err
	}
	if s.path == "" {
		s.cfg = cfg
		return cfg, nil
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return Config{}, err
	}
	data = append(data, '\n')
	if err := writeFileAtomic(s.path, data, 0o600); err != nil {
		return Config{}, fmt.Errorf("persist transfer config: %w", err)
	}
	s.cfg = cfg
	return cfg, nil
}

func validateConfig(cfg Config) error {
	if !filepath.IsAbs(cfg.SourceDir) || !filepath.IsAbs(cfg.TargetDir) {
		return errors.New("source_dir and target_dir must be absolute paths")
	}
	if err := validateTargetDir(cfg.TargetDir); err != nil {
		return fmt.Errorf("target_dir: %w", err)
	}
	return nil
}

func writeFileAtomic(filePath string, data []byte, perm os.FileMode) (returnErr error) {
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, "."+filepath.Base(filePath)+".tmp-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() {
		_ = temp.Close()
		if returnErr != nil {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(perm); err != nil {
		return err
	}
	if _, err := temp.Write(data); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, filePath); err != nil {
		return err
	}
	return nil
}

type Check struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

type Entry struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Parent      string `json:"parent"`
	GroupID     string `json:"group_id,omitempty"`
	GroupName   string `json:"group_name,omitempty"`
	Size        int64  `json:"size"`
	SizeText    string `json:"size_text"`
	IsDir       bool   `json:"is_dir"`
	ModifiedAt  string `json:"modified_at"`
	Description string `json:"description,omitempty"`
}

type Directory struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type Task struct {
	ID          string `json:"id"`
	Path        string `json:"path"`
	Name        string `json:"name"`
	Destination string `json:"destination"`
	Status      string `json:"status"`
	Downloader  string `json:"downloader,omitempty"`
	Error       string `json:"error,omitempty"`
	BytesTotal  int64  `json:"bytes_total"`
	BytesCopied int64  `json:"bytes_copied"`
	Progress    int    `json:"progress"`
	StartedAt   string `json:"started_at"`
	FinishedAt  string `json:"finished_at,omitempty"`
}

type Service struct {
	store *ConfigStore
	mu    sync.Mutex
	next  int64
	tasks map[string]*Task
	order []string

	remoteContext      context.Context
	remoteCancel       context.CancelFunc
	remoteSlots        chan struct{}
	remoteCancels      map[string]context.CancelFunc
	remoteReservations map[string]struct{}
	remoteWG           sync.WaitGroup
	remoteDone         chan struct{}
	closeOnce          sync.Once
	closed             bool

	beforeRemotePublish func()
}

func NewService(store *ConfigStore) *Service {
	remoteContext, remoteCancel := context.WithCancel(context.Background())
	return &Service{
		store:              store,
		tasks:              make(map[string]*Task),
		remoteContext:      remoteContext,
		remoteCancel:       remoteCancel,
		remoteSlots:        make(chan struct{}, maxConcurrentRemoteDownloads),
		remoteCancels:      make(map[string]context.CancelFunc),
		remoteReservations: make(map[string]struct{}),
		remoteDone:         make(chan struct{}),
	}
}

func (s *Service) Config() Config {
	return s.store.Get()
}

func (s *Service) UpdateConfig(cfg Config) (Config, error) {
	return s.store.Update(cfg)
}

func (s *Service) Checks() []Check {
	cfg := s.Config()
	checks := make([]Check, 0, 1)
	if err := validateTargetDir(cfg.TargetDir); err != nil {
		checks = append(checks, Check{Name: "target_writable", Status: "fail", Message: "本地保存目录不可用：" + err.Error()})
	} else {
		checks = append(checks, Check{Name: "target_writable", Status: "ok", Message: "本地保存目录可写"})
	}
	return checks
}

func validateSourceDir(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("directory is not accessible: %w", err)
	}
	if !info.IsDir() {
		return errors.New("path is not a directory")
	}
	if err := canReadDir(dir); err != nil {
		return fmt.Errorf("directory is not readable: %w", err)
	}
	return nil
}

func validateTargetDir(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("directory is not accessible: %w", err)
	}
	if !info.IsDir() {
		return errors.New("path is not a directory")
	}
	if err := canWriteDir(dir); err != nil {
		return fmt.Errorf("directory is not writable: %w", err)
	}
	return nil
}

func canReadDir(path string) error {
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	_ = entries
	return nil
}

func canWriteDir(path string) error {
	probe, err := os.CreateTemp(path, ".bosstransfer-write-check-*")
	if err != nil {
		return err
	}
	probePath := probe.Name()
	if err := probe.Close(); err != nil {
		_ = os.Remove(probePath)
		return err
	}
	return os.Remove(probePath)
}

// ListDirectories returns the immediate child directories beneath a configured
// source or target root. relativePath is always interpreted relative to that
// root; absolute paths, traversal, and symbolic-link traversal are rejected.
func (s *Service) ListDirectories(rootKind DirectoryRoot, relativePath string) ([]Directory, error) {
	root, err := s.directoryRoot(rootKind)
	if err != nil {
		return nil, err
	}
	dir, cleanRelative, err := safeDirectoryPath(root, relativePath)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read directory: %w", err)
	}
	directories := make([]Directory, 0, len(entries))
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("inspect directory entry %q: %w", entry.Name(), err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if !info.IsDir() {
			continue
		}
		relative := entry.Name()
		if cleanRelative != "" {
			relative = path.Join(cleanRelative, entry.Name())
		}
		directories = append(directories, Directory{Name: entry.Name(), Path: relative})
	}
	sort.Slice(directories, func(i, j int) bool {
		left := strings.ToLower(directories[i].Name)
		right := strings.ToLower(directories[j].Name)
		if left == right {
			return directories[i].Name < directories[j].Name
		}
		return left < right
	})
	return directories, nil
}

func (s *Service) directoryRoot(rootKind DirectoryRoot) (string, error) {
	cfg := s.Config()
	switch rootKind {
	case DirectoryRootSource:
		return cfg.SourceDir, nil
	case DirectoryRootTarget:
		return cfg.TargetDir, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidDirectoryRoot, rootKind)
	}
}

func safeDirectoryPath(root, relativePath string) (string, string, error) {
	if !filepath.IsAbs(root) {
		return "", "", errors.New("configured directory root must be an absolute path")
	}
	cleanRelative, err := cleanRelativeDirectoryPath(relativePath)
	if err != nil {
		return "", "", err
	}
	dir, _, err := SafeJoin(root, filepath.FromSlash(cleanRelative))
	if err != nil {
		return "", "", err
	}
	if err := rejectSymbolicLinks(root, dir); err != nil {
		return "", "", err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", "", fmt.Errorf("directory is not accessible: %w", err)
	}
	if !info.IsDir() {
		return "", "", errors.New("path is not a directory")
	}
	return dir, cleanRelative, nil
}

func cleanRelativeDirectoryPath(relativePath string) (string, error) {
	if strings.TrimSpace(relativePath) == "" || relativePath == "." {
		return "", nil
	}
	normalized := strings.ReplaceAll(relativePath, `\`, "/")
	if strings.ContainsRune(normalized, '\x00') || strings.HasPrefix(normalized, "/") || filepath.IsAbs(relativePath) || filepath.VolumeName(filepath.FromSlash(normalized)) != "" || looksLikeWindowsVolume(normalized) {
		return "", ErrPathOutsideRoot
	}
	for _, component := range strings.Split(normalized, "/") {
		if component == ".." {
			return "", ErrPathOutsideRoot
		}
	}
	cleaned := path.Clean(normalized)
	if cleaned == "." {
		return "", nil
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", ErrPathOutsideRoot
	}
	return cleaned, nil
}

func looksLikeWindowsVolume(value string) bool {
	return len(value) >= 2 && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) && value[1] == ':'
}

func rejectSymbolicLinks(root, candidate string) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	absCandidate, err := filepath.Abs(candidate)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(absRoot, absCandidate)
	if err != nil {
		return err
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) || filepath.IsAbs(relative) {
		return ErrPathOutsideRoot
	}
	current := absRoot
	checkCurrent := func() error {
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("inspect directory path: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s", ErrSymbolicLink, current)
		}
		return nil
	}
	if err := checkCurrent(); err != nil {
		return err
	}
	if relative == "." {
		return nil
	}
	for _, part := range strings.Split(relative, string(os.PathSeparator)) {
		current = filepath.Join(current, part)
		if err := checkCurrent(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) Search(ctx context.Context, query string) ([]Entry, error) {
	cfg := s.Config()
	if !filepath.IsAbs(cfg.SourceDir) {
		return nil, errors.New("source_dir must be an absolute path")
	}
	if err := validateSourceDir(cfg.SourceDir); err != nil {
		return nil, fmt.Errorf("source directory is unavailable: %w", err)
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return []Entry{}, nil
	}
	root, err := filepath.Abs(cfg.SourceDir)
	if err != nil {
		return nil, err
	}
	needle := strings.ToLower(query)
	limit := cfg.MaxResults
	results := make([]Entry, 0, min(limit, 32))
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == root {
			return nil
		}
		name := d.Name()
		if d.IsDir() && strings.HasPrefix(name, ".") {
			return filepath.SkipDir
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relSlash := filepath.ToSlash(rel)
		if !strings.Contains(strings.ToLower(name), needle) && !strings.Contains(strings.ToLower(relSlash), needle) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		parent := filepath.ToSlash(filepath.Dir(rel))
		if parent == "." {
			parent = "/"
		} else {
			parent = "/" + parent
		}
		entry := Entry{
			Name:       name,
			Path:       relSlash,
			Parent:     parent,
			Size:       info.Size(),
			SizeText:   FormatBytes(info.Size()),
			IsDir:      info.IsDir(),
			ModifiedAt: info.ModTime().Format("2006-01-02 15:04"),
		}
		if info.IsDir() {
			entry.SizeText = "文件夹"
		}
		results = append(results, entry)
		if len(results) >= limit {
			return errSearchLimit
		}
		return nil
	})
	if errors.Is(err, errSearchLimit) {
		err = nil
	}
	return results, err
}

var errSearchLimit = errors.New("search result limit reached")

func (s *Service) StartDownload(rel string) (Task, error) {
	cfg := s.Config()
	sourcePath, safeRel, err := SafeJoin(cfg.SourceDir, rel)
	if err != nil {
		return Task{}, err
	}
	info, err := os.Stat(sourcePath)
	if err != nil {
		return Task{}, err
	}
	destination, _, err := SafeJoin(cfg.TargetDir, safeRel)
	if err != nil {
		return Task{}, err
	}
	destination, err = uniqueDestination(destination)
	if err != nil {
		return Task{}, err
	}

	now := time.Now().UTC()
	s.mu.Lock()
	s.next++
	id := fmt.Sprintf("T%06d", s.next)
	task := &Task{
		ID:          id,
		Path:        safeRel,
		Name:        info.Name(),
		Destination: filepath.ToSlash(destination),
		Status:      "queued",
		Downloader:  "system",
		StartedAt:   now.Format(time.RFC3339),
	}
	s.tasks[id] = task
	s.order = append([]string{id}, s.order...)
	s.trimTerminalTasksLocked(id)
	s.mu.Unlock()

	go s.runDownload(id, sourcePath, destination, info.IsDir())
	return s.taskSnapshot(id), nil
}

// RemoteDownload describes a manager-authorized, short-lived HTTP download.
// URL and Headers are never retained in task state or returned to the browser.
type RemoteDownload struct {
	ResourceID string
	Name       string
	Size       int64
	URL        string
	Headers    map[string]string
	Downloader string
}

// StartRemoteDownload saves a resolved remote resource into the configured
// target directory. The caller must obtain URL from the trusted manager and
// must not pass a browser-supplied URL into this method.
func (s *Service) StartRemoteDownload(download RemoteDownload) (Task, error) {
	cfg := s.Config()
	if err := validateTargetDir(cfg.TargetDir); err != nil {
		return Task{}, fmt.Errorf("target directory is unavailable: %w", err)
	}
	name, err := safeDownloadName(download.Name)
	if err != nil {
		return Task{}, err
	}
	if strings.TrimSpace(download.URL) == "" {
		return Task{}, errors.New("remote download URL is empty")
	}
	parsedURL, err := validateRemoteDownloadURL(download.URL)
	if err != nil {
		return Task{}, err
	}
	destination, _, err := SafeJoin(cfg.TargetDir, name)
	if err != nil {
		return Task{}, err
	}

	select {
	case s.remoteSlots <- struct{}{}:
	default:
		return Task{}, ErrRemoteDownloadBusy
	}

	now := time.Now().UTC()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		<-s.remoteSlots
		return Task{}, ErrServiceClosed
	}
	destination, err = uniqueDestinationReserved(destination, s.remoteReservations)
	if err != nil {
		s.mu.Unlock()
		<-s.remoteSlots
		return Task{}, err
	}
	s.next++
	id := fmt.Sprintf("T%06d", s.next)
	taskContext, cancel := context.WithCancel(s.remoteContext)
	downloader := strings.ToLower(strings.TrimSpace(download.Downloader))
	if downloader == "" {
		downloader = "system"
	}
	task := &Task{
		ID:          id,
		Path:        strings.TrimSpace(download.ResourceID),
		Name:        name,
		Destination: filepath.ToSlash(destination),
		Status:      "queued",
		Downloader:  downloader,
		BytesTotal:  max(download.Size, 0),
		StartedAt:   now.Format(time.RFC3339),
	}
	s.tasks[id] = task
	s.remoteCancels[id] = cancel
	s.remoteReservations[destinationReservationKey(destination)] = struct{}{}
	s.order = append([]string{id}, s.order...)
	s.trimTerminalTasksLocked(id)
	s.remoteWG.Add(1)
	s.mu.Unlock()

	go func() {
		defer func() {
			cancel()
			s.mu.Lock()
			delete(s.remoteCancels, id)
			delete(s.remoteReservations, destinationReservationKey(destination))
			s.mu.Unlock()
			<-s.remoteSlots
			s.remoteWG.Done()
		}()
		s.runRemoteDownload(taskContext, id, parsedURL.String(), download.Headers, destination, download.Size)
	}()
	return s.taskSnapshot(id), nil
}

// CancelRemoteDownload cancels an active system download. A completed,
// failed, externally submitted, or unknown task is not changed.
func (s *Service) CancelRemoteDownload(id string) (Task, bool) {
	s.mu.Lock()
	task, ok := s.tasks[id]
	cancel := s.remoteCancels[id]
	active := ok && cancel != nil && (task.Status == "queued" || task.Status == "running")
	var snapshot Task
	if active {
		markTaskCancelled(task)
		s.trimTerminalTasksLocked(id)
		snapshot = cloneTask(task)
	}
	s.mu.Unlock()
	if !active {
		return Task{}, false
	}
	cancel()
	return snapshot, true
}

// Shutdown prevents new remote downloads, cancels active work, and waits until
// temporary files have been removed or ctx expires. It is safe to call more
// than once; a later call may continue waiting after an earlier timeout.
func (s *Service) Shutdown(ctx context.Context) error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		cancel := s.remoteCancel
		s.mu.Unlock()
		cancel()
		go func() {
			s.remoteWG.Wait()
			close(s.remoteDone)
		}()
	})
	select {
	case <-s.remoteDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close performs an unbounded shutdown. Callers with a process deadline should
// use Shutdown so a stalled filesystem operation cannot hang process exit.
func (s *Service) Close() {
	_ = s.Shutdown(context.Background())
}

// RecordSubmitted records a task accepted by an external downloader. The
// external service owns progress after submission, so no fake percentage is
// reported.
func (s *Service) RecordSubmitted(resourceID, name, downloader, destination string) Task {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	id := fmt.Sprintf("T%06d", s.next)
	task := &Task{
		ID:          id,
		Path:        strings.TrimSpace(resourceID),
		Name:        strings.TrimSpace(name),
		Destination: strings.TrimSpace(destination),
		Status:      "submitted",
		Downloader:  strings.TrimSpace(downloader),
		StartedAt:   now.Format(time.RFC3339),
	}
	s.tasks[id] = task
	s.order = append([]string{id}, s.order...)
	s.trimTerminalTasksLocked(id)
	return cloneTask(task)
}

func safeDownloadName(value string) (string, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\x00", ""))
	if value == "" || value == "." || value == ".." || filepath.Base(value) != value || strings.ContainsAny(value, `/\\`) {
		return "", errors.New("remote download filename is invalid")
	}
	if len([]byte(value)) > 255 {
		return "", errors.New("remote download filename is too long")
	}
	return value, nil
}

func (s *Service) runRemoteDownload(ctx context.Context, id, sourceURL string, headers map[string]string, destination string, expectedSize int64) {
	if !s.beginRemoteTask(id) {
		return
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		s.failRemoteTask(id, errors.New("远程下载地址无效"))
		return
	}
	for key, value := range headers {
		if !isSafeDownloadHeader(key) {
			continue
		}
		request.Header.Set(key, value)
	}
	origin := remoteDownloadOrigin(request.URL)
	dialer := &net.Dialer{Timeout: remoteDialTimeout, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			connection, dialErr := dialer.DialContext(ctx, network, address)
			if dialErr != nil {
				return nil, dialErr
			}
			return &idleTimeoutConn{Conn: connection, timeout: remoteIdleTimeout}, nil
		},
		ForceAttemptHTTP2:     true,
		DisableCompression:    true,
		TLSHandshakeTimeout:   remoteTLSHandshakeTimeout,
		ResponseHeaderTimeout: remoteResponseHeaderTimeout,
		ExpectContinueTimeout: time.Second,
		IdleConnTimeout:       30 * time.Second,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(next *http.Request, previous []*http.Request) error {
			if len(previous) >= 5 {
				return errRemoteRedirectLimit
			}
			if _, redirectErr := validateRemoteDownloadURL(next.URL.String()); redirectErr != nil {
				return redirectErr
			}
			if remoteDownloadOrigin(next.URL) != origin {
				return errRemoteRedirectOrigin
			}
			return nil
		},
	}
	response, err := client.Do(request)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			s.cancelRemoteTask(id)
			return
		}
		s.failRemoteTask(id, safeRemoteNetworkError(ctx, err))
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		s.failRemoteTask(id, fmt.Errorf("远程服务器返回 HTTP %d", response.StatusCode))
		return
	}

	knownSize := expectedSize > 0
	if response.ContentLength >= 0 {
		if knownSize && response.ContentLength != expectedSize {
			s.failRemoteTask(id, errors.New("远程文件大小与资源信息不一致"))
			return
		}
		if !knownSize {
			expectedSize = response.ContentLength
			knownSize = true
		}
	}
	if knownSize {
		s.updateTask(id, func(task *Task) { task.BytesTotal = expectedSize })
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		s.failRemoteTask(id, err)
		return
	}
	file, err := os.CreateTemp(filepath.Dir(destination), "."+filepath.Base(destination)+".bosstransfer-*.part")
	if err != nil {
		s.failRemoteTask(id, err)
		return
	}
	temporary := file.Name()
	removeTemporary := true
	defer func() {
		_ = file.Close()
		if removeTemporary {
			_ = os.Remove(temporary)
		}
	}()
	buffer := make([]byte, 256*1024)
	var copied int64
	for {
		count, readErr := response.Body.Read(buffer)
		if count > 0 {
			if knownSize && (copied > expectedSize || int64(count) > expectedSize-copied) {
				s.failRemoteTask(id, errors.New("远程文件超过资源声明大小"))
				return
			}
			written, writeErr := file.Write(buffer[:count])
			if written > 0 {
				copied += int64(written)
				s.addBytes(id, int64(written))
			}
			if writeErr != nil || written != count {
				if writeErr == nil {
					writeErr = io.ErrShortWrite
				}
				s.failRemoteTask(id, writeErr)
				return
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			if errors.Is(ctx.Err(), context.Canceled) {
				s.cancelRemoteTask(id)
				return
			}
			s.failRemoteTask(id, safeRemoteNetworkError(ctx, readErr))
			return
		}
	}
	if s.shouldStopRemoteTask(ctx, id) {
		return
	}
	if knownSize && copied != expectedSize {
		s.failRemoteTask(id, errors.New("远程文件大小与资源信息不一致"))
		return
	}
	if err := file.Sync(); err != nil {
		s.failRemoteTask(id, err)
		return
	}
	if s.shouldStopRemoteTask(ctx, id) {
		return
	}
	if err := file.Chmod(0o644); err != nil {
		s.failRemoteTask(id, err)
		return
	}
	if s.shouldStopRemoteTask(ctx, id) {
		return
	}
	if err := file.Close(); err != nil {
		s.failRemoteTask(id, err)
		return
	}
	if s.shouldStopRemoteTask(ctx, id) {
		return
	}
	if s.beforeRemotePublish != nil {
		s.beforeRemotePublish()
	}
	published, err := s.publishRemoteTask(ctx, id, temporary, destination)
	if err != nil {
		s.failRemoteTask(id, err)
		return
	}
	removeTemporary = !published
}

type idleTimeoutConn struct {
	net.Conn
	timeout time.Duration
}

func (c *idleTimeoutConn) Read(buffer []byte) (int, error) {
	if err := c.Conn.SetReadDeadline(time.Now().Add(c.timeout)); err != nil {
		return 0, err
	}
	return c.Conn.Read(buffer)
}

func (c *idleTimeoutConn) Write(buffer []byte) (int, error) {
	if err := c.Conn.SetWriteDeadline(time.Now().Add(c.timeout)); err != nil {
		return 0, err
	}
	return c.Conn.Write(buffer)
}

func validateRemoteDownloadURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" || parsed.Opaque != "" {
		return nil, errors.New("远程下载地址必须是完整的 http 或 https URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("远程下载地址必须是完整的 http 或 https URL")
	}
	return parsed, nil
}

func remoteDownloadOrigin(value *url.URL) string {
	scheme := strings.ToLower(value.Scheme)
	host := strings.ToLower(value.Hostname())
	port := value.Port()
	if port == "" {
		if scheme == "http" {
			port = "80"
		} else if scheme == "https" {
			port = "443"
		}
	}
	return scheme + "://" + net.JoinHostPort(host, port)
}

func safeRemoteNetworkError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return errors.New("远程下载已取消")
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return errors.New("远程下载连接超时")
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return errors.New("远程下载连接超时")
	}
	return errors.New("远程下载请求失败")
}

func (s *Service) failRemoteTask(id string, err error) {
	// Remote transport errors can embed a signed URL in url.Error. Transport
	// callers sanitize those errors before they reach this helper; filesystem
	// errors can disclose only the already-visible local destination.
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id]
	if !ok || (task.Status != "queued" && task.Status != "running") {
		return
	}
	task.Status = "failed"
	task.Error = err.Error()
	task.Progress = progress(task.BytesCopied, task.BytesTotal)
	task.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	s.trimTerminalTasksLocked(id)
}

func (s *Service) cancelRemoteTask(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if task, ok := s.tasks[id]; ok && (task.Status == "queued" || task.Status == "running") {
		markTaskCancelled(task)
		s.trimTerminalTasksLocked(id)
	}
}

func (s *Service) beginRemoteTask(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id]
	if !ok || task.Status != "queued" {
		return false
	}
	task.Status = "running"
	return true
}

func (s *Service) shouldStopRemoteTask(ctx context.Context, id string) bool {
	if ctx.Err() != nil {
		s.cancelRemoteTask(id)
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id]
	return !ok || task.Status == "cancelled"
}

// publishRemoteTask is the commit point shared with CancelRemoteDownload.
// Holding s.mu across rename means exactly one operation wins: a successful
// cancellation prevents publication, while a completed publication makes a
// later cancellation return false.
func (s *Service) publishRemoteTask(ctx context.Context, id, temporary, destination string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id]
	if !ok {
		return false, nil
	}
	if task.Status == "cancelled" || ctx.Err() != nil {
		if task.Status == "queued" || task.Status == "running" {
			markTaskCancelled(task)
		}
		return false, nil
	}
	if task.Status != "running" {
		return false, nil
	}
	if err := os.Rename(temporary, destination); err != nil {
		return false, err
	}
	task.Status = "completed"
	if task.BytesTotal <= 0 {
		task.BytesTotal = task.BytesCopied
	}
	task.Progress = 100
	task.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	s.trimTerminalTasksLocked(id)
	return true, nil
}

func markTaskCancelled(task *Task) {
	task.Status = "cancelled"
	task.Error = ""
	task.FinishedAt = time.Now().UTC().Format(time.RFC3339)
}

func (s *Service) trimTerminalTasksLocked(preserveID string) {
	for len(s.order) > maxRememberedTasks {
		removed := false
		for index := len(s.order) - 1; index >= 0; index-- {
			id := s.order[index]
			if id == preserveID {
				continue
			}
			task, ok := s.tasks[id]
			if ok && (task.Status == "queued" || task.Status == "running") {
				continue
			}
			delete(s.tasks, id)
			s.order = append(s.order[:index], s.order[index+1:]...)
			removed = true
			break
		}
		if !removed {
			return
		}
	}
}

func isSafeDownloadHeader(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "user-agent", "referer", "origin", "cookie", "authorization", "x-requested-with":
		return true
	default:
		return strings.HasPrefix(strings.ToLower(strings.TrimSpace(key)), "x-")
	}
}

func (s *Service) Tasks() []Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Task, 0, len(s.order))
	for _, id := range s.order {
		if task, ok := s.tasks[id]; ok {
			out = append(out, cloneTask(task))
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt > out[j].StartedAt })
	return out
}

func (s *Service) Task(id string) (Task, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id]
	if !ok {
		return Task{}, false
	}
	return cloneTask(task), true
}

func (s *Service) runDownload(id, sourcePath, destination string, isDir bool) {
	s.updateTask(id, func(task *Task) {
		task.Status = "running"
	})
	total, err := totalBytes(sourcePath)
	if err != nil {
		s.failTask(id, err)
		return
	}
	s.updateTask(id, func(task *Task) {
		task.BytesTotal = total
		task.Progress = progress(task.BytesCopied, task.BytesTotal)
	})
	if isDir {
		err = copyDir(sourcePath, destination, func(n int64) { s.addBytes(id, n) })
	} else {
		err = copyFile(sourcePath, destination, func(n int64) { s.addBytes(id, n) })
	}
	if err != nil {
		s.failTask(id, err)
		return
	}
	s.updateTask(id, func(task *Task) {
		task.Status = "completed"
		task.BytesCopied = task.BytesTotal
		task.Progress = 100
		task.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	})
}

func (s *Service) failTask(id string, err error) {
	s.updateTask(id, func(task *Task) {
		task.Status = "failed"
		task.Error = err.Error()
		task.Progress = progress(task.BytesCopied, task.BytesTotal)
		task.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	})
}

func (s *Service) addBytes(id string, n int64) {
	s.updateTask(id, func(task *Task) {
		task.BytesCopied += n
		task.Progress = progress(task.BytesCopied, task.BytesTotal)
	})
}

func (s *Service) updateTask(id string, update func(*Task)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if task, ok := s.tasks[id]; ok {
		update(task)
		if task.Status != "queued" && task.Status != "running" {
			s.trimTerminalTasksLocked(id)
		}
	}
}

func (s *Service) taskSnapshot(id string) Task {
	task, _ := s.Task(id)
	return task
}

func cloneTask(task *Task) Task {
	out := *task
	return out
}

func progress(done, total int64) int {
	if total <= 0 {
		if done > 0 {
			return 100
		}
		return 0
	}
	pct := int((done * 100) / total)
	if pct > 100 {
		return 100
	}
	if pct < 0 {
		return 0
	}
	return pct
}

func SafeJoin(root, rel string) (string, string, error) {
	if strings.TrimSpace(root) == "" {
		return "", "", errors.New("root is empty")
	}
	if filepath.IsAbs(rel) {
		return "", "", ErrPathOutsideRoot
	}
	trimmed := strings.TrimLeft(strings.TrimSpace(rel), `/\`)
	cleanRel := filepath.Clean(trimmed)
	if cleanRel == "." {
		cleanRel = ""
	}
	if cleanRel == ".." || strings.HasPrefix(cleanRel, ".."+string(os.PathSeparator)) || strings.HasPrefix(cleanRel, "../") || strings.Contains(cleanRel, string(os.PathSeparator)+".."+string(os.PathSeparator)) {
		return "", "", ErrPathOutsideRoot
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", "", err
	}
	candidate, err := filepath.Abs(filepath.Join(absRoot, cleanRel))
	if err != nil {
		return "", "", err
	}
	relBack, err := filepath.Rel(absRoot, candidate)
	if err != nil {
		return "", "", err
	}
	if relBack == ".." || strings.HasPrefix(relBack, ".."+string(os.PathSeparator)) || filepath.IsAbs(relBack) {
		return "", "", ErrPathOutsideRoot
	}
	if relBack == "." {
		relBack = ""
	}
	return candidate, filepath.ToSlash(relBack), nil
}

func totalBytes(path string) (int64, error) {
	var total int64
	err := filepath.WalkDir(path, func(current string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symbolic links are not supported: %s", current)
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}

func copyDir(source, destination string, progress func(int64)) error {
	return filepath.WalkDir(source, func(current string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symbolic links are not supported: %s", current)
		}
		rel, err := filepath.Rel(source, current)
		if err != nil {
			return err
		}
		target := destination
		if rel != "." {
			target = filepath.Join(destination, rel)
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(current, target, progress)
	})
}

func copyFile(source, destination string, progress func(int64)) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	src, err := os.Open(source)
	if err != nil {
		return err
	}
	defer src.Close()
	info, err := src.Stat()
	if err != nil {
		return err
	}
	dst, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer dst.Close()
	buf := make([]byte, 1024*256)
	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			written, writeErr := dst.Write(buf[:n])
			if written > 0 && progress != nil {
				progress(int64(written))
			}
			if writeErr != nil {
				return writeErr
			}
			if written != n {
				return io.ErrShortWrite
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	return dst.Sync()
}

func uniqueDestination(path string) (string, error) {
	return uniqueDestinationReserved(path, nil)
}

func uniqueDestinationReserved(filePath string, reservations map[string]struct{}) (string, error) {
	available := func(candidate string) (bool, error) {
		if _, reserved := reservations[destinationReservationKey(candidate)]; reserved {
			return false, nil
		}
		if _, err := os.Stat(candidate); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return true, nil
			}
			return false, err
		}
		return false, nil
	}
	if ok, err := available(filePath); err != nil {
		return "", err
	} else if ok {
		return filePath, nil
	}
	dir := filepath.Dir(filePath)
	base := filepath.Base(filePath)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	for i := 1; i <= 999; i++ {
		candidate := filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
		if ok, err := available(candidate); err != nil {
			return "", err
		} else if ok {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("too many existing files near %s", filePath)
}

func destinationReservationKey(filePath string) string {
	key := filepath.Clean(filePath)
	if filepath.Separator == '\\' {
		key = strings.ToLower(key)
	}
	return key
}

func FormatBytes(size int64) string {
	if size < 0 {
		return "-"
	}
	units := []string{"B", "KB", "MB", "GB", "TB"}
	value := float64(size)
	idx := 0
	for value >= 1024 && idx < len(units)-1 {
		value = value / 1024
		idx++
	}
	if idx == 0 {
		return fmt.Sprintf("%d %s", size, units[idx])
	}
	return fmt.Sprintf("%.1f %s", value, units[idx])
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
