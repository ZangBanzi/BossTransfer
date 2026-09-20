package transfer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSafeJoinRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	if _, _, err := SafeJoin(root, "../secret.txt"); !errors.Is(err, ErrPathOutsideRoot) {
		t.Fatalf("SafeJoin traversal error = %v, want ErrPathOutsideRoot", err)
	}
	if _, _, err := SafeJoin(root, filepath.Join("folder", "..", "..", "secret.txt")); !errors.Is(err, ErrPathOutsideRoot) {
		t.Fatalf("SafeJoin nested traversal error = %v, want ErrPathOutsideRoot", err)
	}
}

func TestConfigStoreUpdateValidatesDownloadTargetBeforeCommit(t *testing.T) {
	source := t.TempDir()
	target := t.TempDir()
	original := Config{SourceDir: source, TargetDir: target, MaxResults: 20}
	store := NewInMemoryConfigStore(original)

	missingTarget := filepath.Join(t.TempDir(), "missing-target")
	if _, err := store.Update(Config{SourceDir: source, TargetDir: missingTarget, MaxResults: 30}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Update missing target error = %v, want os.ErrNotExist", err)
	}
	if _, err := os.Stat(missingTarget); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing target was created or returned unexpected error: %v", err)
	}
	if got := store.Get(); got != original {
		t.Fatalf("config changed after invalid target: got %#v, want %#v", got, original)
	}

}

func TestFileConfigStoreUpdatePersistsAndReloads(t *testing.T) {
	base := t.TempDir()
	configPath := filepath.Join(base, "config", "config.json")
	source := t.TempDir()
	target := t.TempDir()
	defaults := Config{SourceDir: source, TargetDir: target, MaxResults: 10}
	store, err := NewFileConfigStore(configPath, defaults)
	if err != nil {
		t.Fatal(err)
	}

	want := Config{SourceDir: source, TargetDir: target, MaxResults: 42}
	got, err := store.Update(want)
	if err != nil {
		t.Fatalf("Update returned error: %v", err)
	}
	if got != want || store.Get() != want {
		t.Fatalf("updated config = %#v, store = %#v, want %#v", got, store.Get(), want)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var persisted Config
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("persisted config is not valid JSON: %v", err)
	}
	if persisted != want {
		t.Fatalf("persisted config = %#v, want %#v", persisted, want)
	}
	want.MaxResults = 43
	if _, err := store.Update(want); err != nil {
		t.Fatalf("second atomic Update returned error: %v", err)
	}
	temporaryFiles, err := filepath.Glob(filepath.Join(filepath.Dir(configPath), ".config.json.tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(temporaryFiles) != 0 {
		t.Fatalf("atomic update left temporary files: %v", temporaryFiles)
	}

	reloaded, err := NewFileConfigStore(configPath, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Get() != want {
		t.Fatalf("reloaded config = %#v, want %#v", reloaded.Get(), want)
	}
}

func TestFileConfigStoreFailedPersistenceKeepsCurrentConfig(t *testing.T) {
	base := t.TempDir()
	blockedPath := filepath.Join(base, "config.json")
	if err := os.Mkdir(blockedPath, 0o755); err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	target := t.TempDir()
	original := Config{SourceDir: source, TargetDir: target, MaxResults: 10}
	store := &ConfigStore{path: blockedPath, cfg: original}

	candidate := Config{SourceDir: source, TargetDir: target, MaxResults: 25}
	if _, err := store.Update(candidate); err == nil {
		t.Fatal("Update succeeded despite an unusable config file path")
	}
	if got := store.Get(); got != original {
		t.Fatalf("config changed after persistence failed: got %#v, want %#v", got, original)
	}
	temporaryFiles, err := filepath.Glob(filepath.Join(base, ".config.json.tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(temporaryFiles) != 0 {
		t.Fatalf("failed update left temporary files: %v", temporaryFiles)
	}
}

func TestChecksDoesNotCreateMissingTarget(t *testing.T) {
	source := t.TempDir()
	target := filepath.Join(t.TempDir(), "missing-target")
	service := NewService(NewInMemoryConfigStore(Config{SourceDir: source, TargetDir: target}))

	checks := service.Checks()
	if len(checks) != 1 || checks[0].Name != "target_writable" || checks[0].Status != "fail" {
		t.Fatalf("Checks = %#v, want failed target_writable check", checks)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Checks created the missing target or returned unexpected error: %v", err)
	}
}

func TestSearchReturnsErrorWhenSourceRootIsUnavailable(t *testing.T) {
	target := t.TempDir()
	missingSource := filepath.Join(t.TempDir(), "missing-source")
	service := NewService(NewInMemoryConfigStore(Config{SourceDir: missingSource, TargetDir: target}))

	if _, err := service.Search(context.Background(), "movie"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Search error = %v, want os.ErrNotExist", err)
	}
	if _, err := service.Search(context.Background(), ""); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty Search error = %v, want os.ErrNotExist", err)
	}
}

func TestListDirectoriesReturnsImmediateChildren(t *testing.T) {
	source := t.TempDir()
	target := t.TempDir()
	for _, dir := range []string{
		filepath.Join(source, "alpha", "nested", "deep"),
		filepath.Join(source, "Bravo"),
		filepath.Join(target, "downloads"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "movie.txt"), []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewService(NewInMemoryConfigStore(Config{SourceDir: source, TargetDir: target}))

	got, err := service.ListDirectories(DirectoryRootSource, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []Directory{{Name: "alpha", Path: "alpha"}, {Name: "Bravo", Path: "Bravo"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("source directories = %#v, want %#v", got, want)
	}

	got, err = service.ListDirectories(DirectoryRootSource, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	want = []Directory{{Name: "nested", Path: "alpha/nested"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("nested directories = %#v, want %#v", got, want)
	}

	got, err = service.ListDirectories(DirectoryRootTarget, ".")
	if err != nil {
		t.Fatal(err)
	}
	want = []Directory{{Name: "downloads", Path: "downloads"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("target directories = %#v, want %#v", got, want)
	}
}

func TestListDirectoriesRejectsUnsafePathsAndUnknownRoot(t *testing.T) {
	source := t.TempDir()
	target := t.TempDir()
	service := NewService(NewInMemoryConfigStore(Config{SourceDir: source, TargetDir: target}))

	for _, relative := range []string{"../outside", "child/../outside", "/absolute", `\absolute`, `C:\Windows`} {
		t.Run(relative, func(t *testing.T) {
			if _, err := service.ListDirectories(DirectoryRootSource, relative); !errors.Is(err, ErrPathOutsideRoot) {
				t.Fatalf("ListDirectories(%q) error = %v, want ErrPathOutsideRoot", relative, err)
			}
		})
	}
	if _, err := service.ListDirectories(DirectoryRoot("unknown"), ""); !errors.Is(err, ErrInvalidDirectoryRoot) {
		t.Fatalf("unknown root error = %v, want ErrInvalidDirectoryRoot", err)
	}
}

func TestListDirectoriesRejectsSymbolicLinkTraversal(t *testing.T) {
	source := t.TempDir()
	target := t.TempDir()
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(outside, "escaped"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(source, "linked")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symbolic links are unavailable on this system: %v", err)
	}
	service := NewService(NewInMemoryConfigStore(Config{SourceDir: source, TargetDir: target}))

	directories, err := service.ListDirectories(DirectoryRootSource, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(directories) != 0 {
		t.Fatalf("root listing exposed a symbolic link: %#v", directories)
	}
	if _, err := service.ListDirectories(DirectoryRootSource, "linked"); !errors.Is(err, ErrSymbolicLink) {
		t.Fatalf("symbolic-link traversal error = %v, want ErrSymbolicLink", err)
	}
}

func TestSearchAndDownloadCopiesFile(t *testing.T) {
	source := t.TempDir()
	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "电影"), 0o755); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(source, "电影", "测试影片.txt")
	if err := os.WriteFile(input, []byte("hello from 115 mount"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := NewInMemoryConfigStore(Config{SourceDir: source, TargetDir: target, MaxResults: 20})
	service := NewService(store)

	entries, err := service.Search(context.Background(), "测试")
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("Search returned %d entries, want 1", len(entries))
	}
	if entries[0].Path != filepath.ToSlash(filepath.Join("电影", "测试影片.txt")) {
		t.Fatalf("entry path = %q", entries[0].Path)
	}

	task, err := service.StartDownload(entries[0].Path)
	if err != nil {
		t.Fatalf("StartDownload returned error: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		task, _ = service.Task(task.ID)
		if task.Status == "completed" || task.Status == "failed" || task.Status == "cancelled" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if task.Status != "completed" {
		t.Fatalf("task status = %q, error = %q", task.Status, task.Error)
	}
	output := filepath.Join(target, "电影", "测试影片.txt")
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("read copied file: %v", err)
	}
	if string(data) != "hello from 115 mount" {
		t.Fatalf("copied content = %q", data)
	}
}

func TestRemoteDownloadUsesResolvedURLWithoutPersistingSecret(t *testing.T) {
	const body = "remote resource payload"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "BossTransfer-test" {
			t.Errorf("User-Agent = %q", r.Header.Get("User-Agent"))
		}
		if encoding := r.Header.Get("Accept-Encoding"); encoding != "" {
			t.Errorf("Accept-Encoding = %q, want empty because automatic compression must be disabled", encoding)
		}
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	target := t.TempDir()
	service := NewService(NewInMemoryConfigStore(Config{SourceDir: t.TempDir(), TargetDir: target}))
	task, err := service.StartRemoteDownload(RemoteDownload{
		ResourceID: "opaque-resource-id",
		Name:       "测试资源.txt",
		Size:       int64(len(body)),
		URL:        server.URL + "/secret?token=must-not-persist",
		Headers:    map[string]string{"User-Agent": "BossTransfer-test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		task, _ = service.Task(task.ID)
		if task.Status == "completed" || task.Status == "failed" || task.Status == "cancelled" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if task.Status != "completed" || task.Progress != 100 || task.Downloader != "system" {
		t.Fatalf("remote task = %#v", task)
	}
	serialized, err := json.Marshal(service.Tasks())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serialized), "must-not-persist") {
		t.Fatalf("task state leaked resolved URL: %s", serialized)
	}
	data, err := os.ReadFile(filepath.Join(target, "测试资源.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != body {
		t.Fatalf("download body = %q", data)
	}
}

func TestRemoteDownloadRejectsUnsafeFilename(t *testing.T) {
	service := NewService(NewInMemoryConfigStore(Config{SourceDir: t.TempDir(), TargetDir: t.TempDir()}))
	if _, err := service.StartRemoteDownload(RemoteDownload{Name: "../secret", URL: "https://example.invalid/file"}); err == nil {
		t.Fatal("unsafe remote filename was accepted")
	}
}

func TestRemoteDownloadRejectsCrossOriginRedirectWithoutLeakingHeadersOrURL(t *testing.T) {
	leaked := make(chan http.Header, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked <- r.Header.Clone()
		_, _ = w.Write([]byte("x"))
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/private", http.StatusFound)
	}))
	defer redirector.Close()

	service := NewService(NewInMemoryConfigStore(Config{SourceDir: t.TempDir(), TargetDir: t.TempDir()}))
	defer service.Close()
	const bearer = "Bearer must-not-cross-origin"
	task, err := service.StartRemoteDownload(RemoteDownload{
		ResourceID: "redirect-resource",
		Name:       "redirect.bin",
		Size:       1,
		URL:        redirector.URL + "/signed?token=must-not-persist",
		Headers:    map[string]string{"Authorization": bearer, "X-CD2-Secret": "private-header"},
	})
	if err != nil {
		t.Fatal(err)
	}
	task = waitRemoteTask(t, service, task.ID)
	if task.Status != "failed" {
		t.Fatalf("redirect task = %#v, want failed", task)
	}
	select {
	case header := <-leaked:
		t.Fatalf("cross-origin redirect reached target with headers: %#v", header)
	case <-time.After(100 * time.Millisecond):
	}
	serialized, err := json.Marshal(service.Tasks())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"must-not-persist", "must-not-cross-origin", "private-header", redirector.URL, target.URL} {
		if strings.Contains(string(serialized), secret) {
			t.Fatalf("task state leaked %q: %s", secret, serialized)
		}
	}
}

func TestRemoteDownloadEnforcesKnownSizeAndCleansTemporaryFile(t *testing.T) {
	tests := []struct {
		name string
		body string
		size int64
	}{
		{name: "larger than declared", body: "123456", size: 5},
		{name: "smaller than declared", body: "12345", size: 6},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()

			target := t.TempDir()
			service := NewService(NewInMemoryConfigStore(Config{SourceDir: t.TempDir(), TargetDir: target}))
			defer service.Close()
			task, err := service.StartRemoteDownload(RemoteDownload{Name: "size.bin", Size: test.size, URL: server.URL + "/file"})
			if err != nil {
				t.Fatal(err)
			}
			task = waitRemoteTask(t, service, task.ID)
			if task.Status != "failed" {
				t.Fatalf("size mismatch task = %#v, want failed", task)
			}
			service.Close()
			if _, err := os.Stat(filepath.Join(target, "size.bin")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("size mismatch left final file: %v", err)
			}
			parts, err := filepath.Glob(filepath.Join(target, ".size.bin.bosstransfer-*.part"))
			if err != nil {
				t.Fatal(err)
			}
			if len(parts) != 0 {
				t.Fatalf("size mismatch left temporary files: %v", parts)
			}
		})
	}
}

func TestRemoteDownloadCancellationCleansTemporaryFile(t *testing.T) {
	started := make(chan struct{}, 1)
	handlerDone := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		started <- struct{}{}
		<-r.Context().Done()
		handlerDone <- struct{}{}
	}))
	defer server.Close()

	target := t.TempDir()
	service := NewService(NewInMemoryConfigStore(Config{SourceDir: t.TempDir(), TargetDir: target}))
	task, err := service.StartRemoteDownload(RemoteDownload{Name: "cancel.bin", Size: 100, URL: server.URL + "/slow?token=hidden"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("remote handler did not start")
	}
	if _, cancelled := service.CancelRemoteDownload(task.ID); !cancelled {
		t.Fatal("CancelRemoteDownload returned false for active task")
	}
	task = waitRemoteTask(t, service, task.ID)
	if task.Status != "cancelled" || task.Error != "" {
		t.Fatalf("cancelled task = %#v", task)
	}
	if strings.Contains(task.Error, "hidden") || strings.Contains(task.Error, server.URL) {
		t.Fatalf("cancelled task leaked source URL: %#v", task)
	}
	service.Close()
	select {
	case <-handlerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("server request context was not cancelled")
	}
	if _, err := os.Stat(filepath.Join(target, "cancel.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled download left final file: %v", err)
	}
	parts, err := filepath.Glob(filepath.Join(target, ".cancel.bin.bosstransfer-*.part"))
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 0 {
		t.Fatalf("cancelled download left temporary files: %v", parts)
	}
}

func TestRemoteDownloadCancellationWinsBeforePublish(t *testing.T) {
	const body = "download completed at the network layer"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	target := t.TempDir()
	service := NewService(NewInMemoryConfigStore(Config{SourceDir: t.TempDir(), TargetDir: target}))
	beforePublish := make(chan struct{})
	releasePublish := make(chan struct{})
	var releaseOnce sync.Once
	service.beforeRemotePublish = func() {
		close(beforePublish)
		<-releasePublish
	}
	defer func() {
		releaseOnce.Do(func() { close(releasePublish) })
		service.Close()
	}()

	task, err := service.StartRemoteDownload(RemoteDownload{
		Name: "cancel-before-publish.bin", Size: int64(len(body)), URL: server.URL + "/resolved?token=hidden",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-beforePublish:
	case <-time.After(5 * time.Second):
		t.Fatal("download did not reach the publish barrier")
	}
	if _, cancelled := service.CancelRemoteDownload(task.ID); !cancelled {
		t.Fatal("CancelRemoteDownload returned false before publish")
	}
	releaseOnce.Do(func() { close(releasePublish) })
	service.Close()

	task, ok := service.Task(task.ID)
	if !ok || task.Status != "cancelled" || task.Error != "" {
		t.Fatalf("task after cancellation = %#v, found=%v", task, ok)
	}
	if _, err := os.Stat(filepath.Join(target, "cancel-before-publish.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled task published a final file: %v", err)
	}
	parts, err := filepath.Glob(filepath.Join(target, ".cancel-before-publish.bin.bosstransfer-*.part"))
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 0 {
		t.Fatalf("cancelled task left temporary files: %v", parts)
	}
}

func TestRemoteDownloadShutdownHonorsDeadlineDuringFinalization(t *testing.T) {
	const body = "ready to publish"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	target := t.TempDir()
	service := NewService(NewInMemoryConfigStore(Config{SourceDir: t.TempDir(), TargetDir: target}))
	beforePublish := make(chan struct{})
	releasePublish := make(chan struct{})
	var releaseOnce sync.Once
	service.beforeRemotePublish = func() {
		close(beforePublish)
		<-releasePublish
	}
	defer func() {
		releaseOnce.Do(func() { close(releasePublish) })
		service.Close()
	}()

	task, err := service.StartRemoteDownload(RemoteDownload{
		Name: "shutdown-timeout.bin", Size: int64(len(body)), URL: server.URL + "/file",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-beforePublish:
	case <-time.After(5 * time.Second):
		t.Fatal("download did not reach the publish barrier")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	started := time.Now()
	err = service.Shutdown(shutdownCtx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Shutdown ignored deadline and took %v", elapsed)
	}

	releaseOnce.Do(func() { close(releasePublish) })
	service.Close()
	task, ok := service.Task(task.ID)
	if !ok || task.Status != "cancelled" {
		t.Fatalf("task after bounded shutdown = %#v, found=%v", task, ok)
	}
	if _, err := os.Stat(filepath.Join(target, "shutdown-timeout.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("shutdown task published a final file: %v", err)
	}
}

func TestTaskRetentionNeverEvictsActiveTasks(t *testing.T) {
	for _, status := range []string{"queued", "running"} {
		t.Run(status, func(t *testing.T) {
			service := NewService(NewInMemoryConfigStore(Config{SourceDir: t.TempDir(), TargetDir: t.TempDir()}))
			defer service.Close()
			_, cancel := context.WithCancel(context.Background())
			const activeID = "active-system-task"
			service.mu.Lock()
			service.tasks[activeID] = &Task{ID: activeID, Status: status, Downloader: "system"}
			service.remoteCancels[activeID] = cancel
			service.order = []string{activeID}
			service.mu.Unlock()

			for index := 0; index < maxRememberedTasks+1; index++ {
				service.RecordSubmitted(fmt.Sprintf("resource-%d", index), fmt.Sprintf("file-%d", index), "qbittorrent", "/downloads")
			}
			if _, ok := service.Task(activeID); !ok {
				t.Fatal("active task was evicted when task history reached its limit")
			}
			if got := len(service.Tasks()); got != maxRememberedTasks {
				t.Fatalf("retained task count = %d, want %d", got, maxRememberedTasks)
			}
			if _, cancelled := service.CancelRemoteDownload(activeID); !cancelled {
				t.Fatal("retained active task could not be cancelled")
			}
		})
	}
}

func TestTaskRetentionConvergesWhenActiveTasksFinish(t *testing.T) {
	service := NewService(NewInMemoryConfigStore(Config{SourceDir: t.TempDir(), TargetDir: t.TempDir()}))
	defer service.Close()
	ids := make([]string, 0, maxRememberedTasks+1)
	service.mu.Lock()
	for index := 0; index < maxRememberedTasks+1; index++ {
		id := fmt.Sprintf("active-%03d", index)
		ids = append(ids, id)
		service.tasks[id] = &Task{ID: id, Status: "running", Downloader: "system"}
		service.order = append([]string{id}, service.order...)
	}
	service.mu.Unlock()

	for _, id := range ids {
		service.updateTask(id, func(task *Task) { task.Status = "completed" })
	}
	if got := len(service.Tasks()); got != maxRememberedTasks {
		t.Fatalf("retained task count after completion = %d, want %d", got, maxRememberedTasks)
	}
	for _, task := range service.Tasks() {
		if task.Status == "queued" || task.Status == "running" {
			t.Fatalf("active task remained after completion: %#v", task)
		}
	}
}

func TestRemoteDownloadConcurrencyLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	service := NewService(NewInMemoryConfigStore(Config{SourceDir: t.TempDir(), TargetDir: t.TempDir()}))
	for i := 0; i < maxConcurrentRemoteDownloads; i++ {
		if _, err := service.StartRemoteDownload(RemoteDownload{
			Name: fmt.Sprintf("active-%d.bin", i), Size: 1, URL: server.URL + fmt.Sprintf("/%d", i),
		}); err != nil {
			t.Fatalf("start active download %d: %v", i, err)
		}
	}
	if _, err := service.StartRemoteDownload(RemoteDownload{Name: "excess.bin", Size: 1, URL: server.URL + "/excess"}); !errors.Is(err, ErrRemoteDownloadBusy) {
		t.Fatalf("excess download error = %v, want ErrRemoteDownloadBusy", err)
	}
	service.Close()
	if _, err := service.StartRemoteDownload(RemoteDownload{Name: "closed.bin", Size: 1, URL: server.URL + "/closed"}); !errors.Is(err, ErrServiceClosed) {
		t.Fatalf("closed service error = %v, want ErrServiceClosed", err)
	}
}

func TestRemoteDownloadUsesUniqueTemporaryFileAndDestinationReservation(t *testing.T) {
	const body = "same-name"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	target := t.TempDir()
	stale := filepath.Join(target, "same.bin.bosstransfer.part")
	if err := os.WriteFile(stale, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewService(NewInMemoryConfigStore(Config{SourceDir: t.TempDir(), TargetDir: target}))
	defer service.Close()
	first, err := service.StartRemoteDownload(RemoteDownload{Name: "same.bin", Size: int64(len(body)), URL: server.URL + "/one"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.StartRemoteDownload(RemoteDownload{Name: "same.bin", Size: int64(len(body)), URL: server.URL + "/two"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Destination == second.Destination {
		t.Fatalf("simultaneous downloads reserved the same destination %q", first.Destination)
	}
	first = waitRemoteTask(t, service, first.ID)
	second = waitRemoteTask(t, service, second.ID)
	if first.Status != "completed" || second.Status != "completed" {
		t.Fatalf("same-name tasks = %#v / %#v", first, second)
	}
	if data, err := os.ReadFile(stale); err != nil || string(data) != "stale" {
		t.Fatalf("stale legacy part was modified: data=%q err=%v", data, err)
	}
}

func waitRemoteTask(t *testing.T, service *Service, id string) Task {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		task, ok := service.Task(id)
		if !ok {
			t.Fatalf("task %q disappeared", id)
		}
		if task.Status == "completed" || task.Status == "failed" || task.Status == "cancelled" {
			return task
		}
		time.Sleep(10 * time.Millisecond)
	}
	task, _ := service.Task(id)
	t.Fatalf("task %q did not finish: %#v", id, task)
	return Task{}
}
